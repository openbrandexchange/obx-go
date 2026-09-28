package conformance_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/mock"
)

func report(t *testing.T, rep *conformance.Report) {
	t.Helper()
	for _, r := range rep.Results {
		if r.Status != conformance.StatusPass {
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
			if rep.Failed > 0 || rep.Skipped > 0 {
				t.Fatalf("the mock must pass everything: %d failed, %d skipped", rep.Failed, rep.Skipped)
			}
		})
	}
}
