package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/mock"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	for _, a := range f[host] {
		out = append(out, net.IPAddr{IP: net.ParseIP(a)})
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}

func TestCheckTargetRefusesUnsafeAddresses(t *testing.T) {
	res := fakeResolver{"portal.example.com": {"93.184.216.34"}, "internal.example.com": {"10.0.0.5"}, "rebind.example.com": {"93.184.216.34", "127.0.0.1"}}
	for raw, code := range map[string]string{
		"http://portal.example.com":                      "not_https",
		"https://127.0.0.1":                              "ip_address",
		"https://169.254.169.254/latest/meta-data":       "ip_address",
		"https://[::1]":                                  "ip_address",
		"https://localhost":                              "not_public",
		"https://internal.example.com":                   "not_public",
		"https://rebind.example.com":                     "not_public",
		"https://portal.example.com:8443":                "port",
		"https://user:pw@portal.example.com":             "invalid_url",
		"https://nowhere.example.com":                    "dns",
		"ftp://portal.example.com":                       "not_https",
		"https://portal.example.com/?x=1":                "invalid_url",
		"portal.example.com":                             "",
		"https://portal.example.com/":                    "",
		"https://portal.example.com:443/.well-known/obx": "",
	} {
		base, _, err := checkTarget(context.Background(), raw, false, res)
		switch {
		case code == "" && err != nil:
			t.Errorf("%s: refused (%s)", raw, err.Code)
		case code == "" && !strings.HasPrefix(base, "https://portal.example.com"):
			t.Errorf("%s: base %q", raw, base)
		case code != "" && (err == nil || err.Code != code):
			t.Errorf("%s: %v, want %s", raw, err, code)
		}
	}
}

func TestCheckServer(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true, ReadOnly: true, FullClientID: "obxc_demo", FullClientSecret: "obxs_public-demo-key"})
	portal := httptest.NewServer(n)
	defer portal.Close()
	var logs bytes.Buffer
	h := newCheckServer(checkServerOptions{Online: conformance.Online{AllowInsecure: true, MinInterval: time.Millisecond}, Logger: log.New(&logs, "", 0), MaxConcurrent: 1})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := `{"url":"` + portal.URL + `","client":{"id":"obxc_demo","secret":"obxs_public-demo-key"}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/check", strings.NewReader(body))
	req.Header.Set("Accept", "application/x-ndjson")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var results int
	var rep *conformance.Report
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(nil, 4<<20)
	for sc.Scan() {
		var l streamLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		switch l.Type {
		case "result":
			results++
		case "report":
			rep = l.Report
		}
	}
	if rep == nil || results != len(rep.Results) || rep.Failed != 0 || !rep.ReadOnly {
		t.Fatalf("stream: %d results, report %+v", results, rep)
	}
	if strings.Contains(logs.String(), "obxs_public-demo-key") || strings.Contains(logs.String(), "obxc_demo") {
		t.Errorf("the log contains the key: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "check host=127.0.0.1") {
		t.Errorf("the log has no metadata line: %s", logs.String())
	}

	// Plain JSON, anonymous.
	res, err = http.Post(srv.URL+"/check", "application/json", strings.NewReader(`{"url":"`+portal.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var plain conformance.Report
	json.NewDecoder(res.Body).Decode(&plain)
	res.Body.Close()
	if res.StatusCode != 200 || plain.Passed == 0 || plain.Failed != 0 {
		t.Fatalf("plain JSON: %d %+v", res.StatusCode, plain)
	}

	for _, bad := range []string{`{"url":"ftp://x"}`, `{"url":"` + portal.URL + `","client":{"id":"x"}}`, `nope`} {
		res, err := http.Post(srv.URL+"/check", "application/json", strings.NewReader(bad))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, res.StatusCode)
		}
	}
}
