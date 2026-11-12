package conformance_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/mock"
)

// mockSkips are tests the mock skips by design: S-RATE-2 needs
// --rate-limit-test, I-IMP-9 and I-IMP-10 the Excel import, a node feature
// the mock does not offer.
var mockSkips = map[string]bool{"S-RATE-2": true, "I-IMP-9": true, "I-IMP-10": true}

func report(t *testing.T, rep *conformance.Report) {
	t.Helper()
	for _, r := range rep.Results {
		if r.Status != conformance.StatusPass && !mockSkips[r.ID] {
			t.Errorf("%s %s: %s\n    %s", r.ID, r.Status, r.Title, strings.Join(r.Messages, "\n    "))
		}
	}
	t.Logf("passed %d, failed %d, skipped %d in %d ms", rep.Passed, rep.Failed, rep.Skipped, rep.Millis)
}

// TestMockPassesAll runs every profile against a fresh mock node, the way
// node repositories run the suite in CI.
func TestMockPassesAll(t *testing.T) {
	for _, clientRenditions := range []bool{false, true} {
		name := "server-renditions"
		if clientRenditions {
			name = "client-renditions"
		}
		t.Run(name, func(t *testing.T) {
			n := mock.New(mock.Options{AllowPrivateURLs: true, ClientRenditions: clientRenditions})
			srv := httptest.NewServer(n)
			defer srv.Close()
			rep, err := conformance.Run(context.Background(), []string{conformance.ProfileAll}, conformance.Config{BaseURL: srv.URL, SetupCode: n.SetupCode()})
			if err != nil {
				t.Fatal(err)
			}
			report(t, rep)
			skipped := rep.Skipped
			for _, r := range rep.Results {
				if mockSkips[r.ID] && r.Status == conformance.StatusSkip {
					skipped--
				}
			}
			if rep.Failed > 0 || skipped > 0 {
				t.Fatalf("the mock must pass everything: %d failed, %d skipped", rep.Failed, rep.Skipped)
			}
		})
	}
}

// A node that sends CORS headers on its APIs fails S-HDR-1: only the
// discovery document may be read by other websites.
func TestCORSOutsideDiscoveryFails(t *testing.T) {
	n := mock.New(mock.Options{AllowPrivateURLs: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		n.ServeHTTP(w, r)
	}))
	defer srv.Close()
	rep, err := conformance.Run(context.Background(), []string{conformance.ProfileSecurity}, conformance.Config{BaseURL: srv.URL, SetupCode: n.SetupCode(), Only: []string{"S-HDR-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != conformance.StatusFail || !strings.Contains(strings.Join(rep.Results[0].Messages, "\n"), "only /.well-known/obx may send CORS headers") {
		t.Fatalf("S-HDR-1 with CORS on every API: %+v", rep.Results)
	}
}
