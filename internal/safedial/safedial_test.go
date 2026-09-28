package safedial

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReason(t *testing.T) {
	cases := []struct {
		ip          string
		strict, dev string
	}{
		{"8.8.8.8", "", ""},
		{"2001:4860:4860::8888", "", ""},
		{"127.0.0.1", "loopback", ""},
		{"::1", "loopback", ""},
		{"10.1.2.3", "private", ""},
		{"192.168.0.1", "private", ""},
		{"fd00::1", "private", ""},
		{"100.64.0.1", "shared (CGNAT)", ""},
		{"169.254.169.254", "link-local", "link-local"},
		{"fe80::1", "link-local", "link-local"},
		{"::ffff:169.254.169.254", "link-local", "link-local"},
		{"::ffff:127.0.0.1", "loopback", ""},
		{"0.0.0.0", "unspecified", "unspecified"},
		{"224.0.0.1", "link-local", "link-local"},
		{"239.1.1.1", "multicast", "multicast"},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := Reason(ip, false); got != c.strict {
			t.Errorf("%s strict: %q, want %q", c.ip, got, c.strict)
		}
		if got := Reason(ip, true); got != c.dev {
			t.Errorf("%s with private addresses allowed: %q, want %q", c.ip, got, c.dev)
		}
	}
}

// The check runs on the connected address, so a host name that resolves to
// loopback (as after a DNS rebinding) is refused at connect time.
func TestTransportPinsTheConnectedAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	strict := &http.Client{Transport: Transport(false), Timeout: 5 * time.Second}
	_, err := strict.Get("http://localhost:" + port + "/")
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("localhost in strict mode: want ErrRefused, got %v", err)
	}

	dev := &http.Client{Transport: Transport(true), Timeout: 5 * time.Second}
	resp, err := dev.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback with private addresses allowed: %v", err)
	}
	resp.Body.Close()

	// Cloud metadata is refused before any packet is sent, even in development.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	start := time.Now()
	_, err = dev.Do(req)
	if !errors.Is(err, ErrRefused) || time.Since(start) > 2*time.Second {
		t.Fatalf("metadata address: want an immediate ErrRefused, got %v after %s", err, time.Since(start))
	}
}
