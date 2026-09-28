package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/mock"
)

func noEnv(string) string { return "" }

func TestProtocolWithAPIKeysOnly(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	c := n.Credentials()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"protocol", "--url", srv.URL, "--client-id", c.Full.ClientID, "--client-secret", c.Full.ClientSecret,
		"--second-client-id", c.Restricted.ClientID, "--second-client-secret", c.Restricted.ClientSecret,
		"--no-original-client-id", c.NoOriginals.ClientID, "--no-original-client-secret", c.NoOriginals.ClientSecret, "--token", c.IngestToken, "--json"}, &out, &errb, noEnv)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	var rep conformance.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 0 || rep.Passed < 20 {
		t.Fatalf("passed %d failed %d", rep.Passed, rep.Failed)
	}
	for _, r := range rep.Results {
		if r.Status == conformance.StatusSkip && !strings.Contains(strings.Join(r.Messages, " "), "admin") && r.ID != "P-FEED-5" {
			t.Errorf("%s skipped for another reason than missing admin credentials: %v", r.ID, r.Messages)
		}
	}
}

func TestBrokenNodeFails(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	broken := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/obx" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"obx_version":"0.1"}`))
			return
		}
		n.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(broken)
	defer srv.Close()
	c := n.Credentials()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"protocol", "--url", srv.URL, "--admin-user", c.AdminEmail, "--admin-password", c.AdminPassword, "--only", "P-DISC-1"}, &out, &errb, noEnv)
	if code != 1 || !strings.Contains(out.String(), "FAIL  P-DISC-1") || !strings.Contains(out.String(), "NOT CONFORMANT") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
}

func TestUsageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"protocol"}, &out, &errb, noEnv); code != 2 {
		t.Fatalf("missing --url: %d", code)
	}
	if code := run(context.Background(), []string{"list"}, &out, &errb, noEnv); code != 0 || !strings.Contains(out.String(), "I-REPLAY-1") {
		t.Fatalf("list: %d %s", code, out.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"all", "--url", "http://127.0.0.1:1", "--setup-code", "x"}, &out, &errb, noEnv); code != 2 {
		t.Fatalf("unreachable node: %d %s", code, errb.String())
	}
}
