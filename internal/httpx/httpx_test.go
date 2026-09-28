package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

func TestRetryAfter(t *testing.T) {
	h := http.Header{}
	if RetryAfter(h, time.Second) != time.Second {
		t.Fatal("default")
	}
	h.Set("Retry-After", "3")
	if RetryAfter(h, 0) != 3*time.Second {
		t.Fatal("seconds")
	}
	h.Set("Retry-After", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	if RetryAfter(h, time.Second) != 0 {
		t.Fatal("date in the past")
	}
}

func TestErrorDecoding(t *testing.T) {
	cases := []struct {
		ct, body string
		status   int
		code     string
	}{
		{obx.MediaTypeProblem, `{"type":"x","title":"t","status":410,"obx_error":"cursor_expired","detail":"restart"}`, 410, obx.ErrCursorExpired},
		{"application/json", `{"error":"invalid_client"}`, 401, ""},
		{"text/html", "<h1>bad gateway</h1>", 502, obx.ErrInternal},
		{"text/plain", "not found", 404, obx.ErrNotFound},
	}
	for _, c := range cases {
		resp := &http.Response{StatusCode: c.status, Header: http.Header{"Content-Type": {c.ct}}, Body: io.NopCloser(strings.NewReader(c.body)), Status: http.StatusText(c.status)}
		err := Error(resp)
		if c.code == "" {
			if oe, ok := err.(*obx.OAuthError); !ok || oe.Code != "invalid_client" || oe.HTTPStatus != 401 {
				t.Errorf("oauth error: %#v", err)
			}
			continue
		}
		if obx.ErrorCode(err) != c.code {
			t.Errorf("%s: got %v", c.body, err)
		}
	}
}

func TestDoRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Errorf("body not replayed: %q", body)
		}
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	d := &Doer{}
	req, _ := NewRequest(http.MethodPut, srv.URL, []byte("payload"), "text/plain")
	resp, err := d.Do(context.Background(), req, true)
	if err != nil || resp.StatusCode != 200 || calls.Load() != 3 {
		t.Fatalf("status %v err %v calls %d", resp, err, calls.Load())
	}
	resp.Body.Close()
	calls.Store(1) // next answer: 502
	req, _ = NewRequest(http.MethodPost, srv.URL, []byte("payload"), "text/plain")
	resp, err = d.Do(context.Background(), req, false)
	if err != nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("non-idempotent requests must not be retried on 5xx: %v %v", resp, err)
	}
	resp.Body.Close()
}
