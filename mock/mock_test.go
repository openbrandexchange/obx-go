package mock_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/obx"
)

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func TestRightsExpiryWritesTombstone(t *testing.T) {
	ck := &clock{t: time.Now()}
	n := mock.New(mock.Options{Fixtures: true, Clock: ck.now})
	srv := httptest.NewServer(n)
	defer srv.Close()
	creds := n.Credentials()
	ctx := context.Background()
	ing, _ := ingest.New(srv.URL, creds.IngestToken)
	rec, err := ing.Get(ctx, obx.KindAsset, "KO-1004_IND_3")
	if err != nil {
		t.Fatal(err)
	}
	exp := ck.now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if _, err := ing.Patch(ctx, obx.KindAsset, "KO-1004_IND_3", map[string]any{"rights": map[string]any{"expires_at": exp}}, nil); err != nil {
		t.Fatal(err)
	}
	c, _ := client.New(client.Config{BaseURL: srv.URL, ClientID: creds.Full.ClientID, ClientSecret: creds.Full.ClientSecret})
	var cursor string
	var got []obx.Change
	read := func() {
		for {
			p, err := c.Changes(ctx, cursor, 500)
			if err != nil {
				t.Fatal(err)
			}
			if p.Phase == obx.PhaseLive {
				got = append(got, p.Changes...)
			}
			cursor = p.NextCursor
			if !p.HasMore {
				return
			}
		}
	}
	read()
	got = nil
	ck.add(3 * time.Hour)
	read()
	if len(got) != 1 || got[0].Op != obx.OpDelete || got[0].ExternalIDValue() != "KO-1004_IND_3" {
		t.Fatalf("after expiry: %+v", got)
	}
	if _, err := c.Asset(ctx, rec.ID); !obx.IsCode(err, obx.ErrNotFound) {
		t.Fatalf("expired asset still served: %v", err)
	}
}

func TestSetupRequired(t *testing.T) {
	srv := httptest.NewServer(mock.New(mock.Options{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/.well-known/obx")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Content-Type") != obx.MediaTypeProblem {
		t.Fatalf("discovery before setup: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestUnknownPathsAreProblems(t *testing.T) {
	srv := httptest.NewServer(mock.New(mock.Options{Fixtures: true}))
	defer srv.Close()
	for _, c := range []struct {
		method, path string
		status       int
	}{{http.MethodGet, "/obx/v1/nope", 404}, {http.MethodDelete, "/obx/v1/changes", 405}} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.status || resp.Header.Get("Content-Type") != obx.MediaTypeProblem {
			t.Errorf("%s %s: %d %s", c.method, c.path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}
