package webhook_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
	"github.com/openbrandexchange/obx-go/webhook"
)

type vector struct {
	Secret    string `json:"secret"`
	Timestamp string `json:"timestamp"`
	RawBody   string `json:"raw_body"`
	Header    string `json:"header"`
}

func loadVector(t *testing.T) vector {
	raw, err := fs.ReadFile(specdata.Fixtures(), "examples/webhook-signature-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSpecVector(t *testing.T) {
	v := loadVector(t)
	if got := webhook.Sign(v.Timestamp, []byte(v.RawBody), v.Secret); got != v.Header {
		t.Fatalf("Sign = %s, want %s", got, v.Header)
	}
	at := time.Unix(1790000000, 0)
	if err := webhook.Verify(v.Header, v.Timestamp, []byte(v.RawBody), at.Add(10*time.Second), 0, v.Secret); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	v := loadVector(t)
	at := time.Unix(1790000000, 0)
	body := []byte(v.RawBody)
	cases := []struct {
		name   string
		header string
		ts     string
		body   []byte
		now    time.Time
		secret string
		want   error
	}{
		{"tampered body", v.Header, v.Timestamp, append([]byte{' '}, body...), at, v.Secret, webhook.ErrSignatureInvalid},
		{"wrong secret", v.Header, v.Timestamp, body, at, "whsec_other", webhook.ErrSignatureInvalid},
		{"secret without prefix", v.Header, v.Timestamp, body, at, strings.TrimPrefix(v.Secret, "whsec_"), webhook.ErrSignatureInvalid},
		{"too old", v.Header, v.Timestamp, body, at.Add(301 * time.Second), v.Secret, webhook.ErrTimestampSkew},
		{"from the future", v.Header, v.Timestamp, body, at.Add(-301 * time.Second), v.Secret, webhook.ErrTimestampSkew},
		{"no header", "", v.Timestamp, body, at, v.Secret, webhook.ErrNoSignature},
		{"garbage header", "v2=abc", v.Timestamp, body, at, v.Secret, webhook.ErrNoSignature},
		{"bad timestamp", v.Header, "yesterday", body, at, v.Secret, webhook.ErrBadTimestamp},
	}
	for _, c := range cases {
		if err := webhook.Verify(c.header, c.ts, c.body, c.now, 0, c.secret); err != c.want {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestRotationHeader(t *testing.T) {
	body := []byte(`{"id":"evt_2"}`)
	ts := "1790000000"
	h := webhook.Sign(ts, body, "whsec_old", "whsec_new")
	now := time.Unix(1790000000, 0)
	for _, s := range []string{"whsec_old", "whsec_new"} {
		if err := webhook.Verify(h, ts, body, now, 0, s); err != nil {
			t.Errorf("secret %s: %v", s, err)
		}
	}
}

func TestHandler(t *testing.T) {
	var got []string
	h := webhook.Handler(func(ev *obx.WebhookEvent) error { got = append(got, ev.ID); return nil }, "whsec_test")
	srv := httptest.NewServer(h)
	defer srv.Close()
	body := []byte(`{"id":"evt_9","type":"webhook.test","created_at":"2026-09-28T10:00:00Z","data":{}}`)
	send := func(secret string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(string(body)))
		for k, v := range webhook.Headers("evt_9", time.Now(), body, secret) {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := send("whsec_test"); s != http.StatusNoContent {
		t.Fatalf("valid delivery: status %d", s)
	}
	if s := send("whsec_wrong"); s != http.StatusBadRequest {
		t.Fatalf("invalid delivery: status %d", s)
	}
	if len(got) != 1 || got[0] != "evt_9" {
		t.Fatalf("handler saw %v", got)
	}
}
