package conformance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// writeWatch serves the mock and records every request that could change
// state. Reads that use POST (token, GTIN lookup) are allowed.
type writeWatch struct {
	next   http.Handler
	mu     sync.Mutex
	writes []string
	n      int
}

func (w *writeWatch) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	w.n++
	read := r.Method == http.MethodGet || r.Method == http.MethodHead ||
		strings.HasSuffix(r.URL.Path, "/oauth/token") || strings.HasSuffix(r.URL.Path, "/products:by-gtin") ||
		(r.Method == http.MethodDelete && r.URL.Path == "/obx/v1/changes") // P-ERR-1 probe: must answer 405
	if !read {
		w.writes = append(w.writes, r.Method+" "+r.URL.Path)
	}
	w.mu.Unlock()
	w.next.ServeHTTP(rw, r)
}

func onlineRun(t *testing.T, base string, cfg conformance.Config) *conformance.Report {
	t.Helper()
	cfg.BaseURL = base
	if cfg.Online == nil {
		cfg.Online = &conformance.Online{AllowInsecure: true, MinInterval: time.Millisecond}
	}
	rep, err := conformance.Run(context.Background(), []string{conformance.ProfileProtocol}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SetupError != "" {
		t.Fatalf("setup: %s", rep.SetupError)
	}
	return rep
}

func statuses(rep *conformance.Report) map[string]conformance.Result {
	out := map[string]conformance.Result{}
	for _, r := range rep.Results {
		out[r.ID] = r
	}
	return out
}

func logOnline(t *testing.T, rep *conformance.Report) {
	t.Helper()
	var partial []string
	for _, r := range rep.Results {
		if r.Online == "partial" {
			partial = append(partial, r.ID)
		}
	}
	t.Logf("passed %d, failed %d, skipped %d; %d requests, %d KiB, %d ms; partial: %s",
		rep.Passed, rep.Failed, rep.Skipped, rep.Requests, rep.Bytes>>10, rep.Millis, strings.Join(partial, ","))
}

// The online check against the mock with the Kestrel data: every online
// test passes, nothing is written, and the result is never a claim.
func TestOnlineCheckIsReadOnly(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	c := n.Credentials()
	for _, tc := range []struct {
		name string
		cfg  conformance.Config
		tier map[string]bool
	}{
		{"anonymous", conformance.Config{}, map[string]bool{conformance.OnlineAnonymous: true}},
		{"one key", conformance.Config{ClientID: c.Full.ClientID, ClientSecret: c.Full.ClientSecret},
			map[string]bool{conformance.OnlineAnonymous: true, conformance.OnlineAPIKey: true, conformance.OnlineFixtures: true}},
		{"three keys", conformance.Config{ClientID: c.Full.ClientID, ClientSecret: c.Full.ClientSecret,
			SecondClientID: c.Restricted.ClientID, SecondClientSecret: c.Restricted.ClientSecret,
			NoOriginalClientID: c.NoOriginals.ClientID, NoOriginalClientSecret: c.NoOriginals.ClientSecret},
			map[string]bool{conformance.OnlineAnonymous: true, conformance.OnlineAPIKey: true, conformance.OnlineFixtures: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &writeWatch{next: n}
			srv := httptest.NewServer(w)
			defer srv.Close()
			rep := onlineRun(t, srv.URL, tc.cfg)
			logOnline(t, rep)
			if !rep.Partial || !rep.ReadOnly {
				t.Errorf("an online report must be partial and read-only")
			}
			if len(w.writes) > 0 {
				t.Errorf("the online check wrote: %v", w.writes)
			}
			for _, r := range rep.Results {
				tier := conformance.OnlineTier(r.ID)
				switch {
				case r.Status == conformance.StatusFail:
					t.Errorf("%s failed: %s", r.ID, strings.Join(r.Messages, "; "))
				case tier == "" && r.Status != conformance.StatusSkip:
					t.Errorf("%s is not an online test but ran", r.ID)
				case tier != "" && tc.tier[tier] && r.Status != conformance.StatusPass &&
					!(r.ID == "P-AUTH-3" && c.NoOriginals.ClientID != tc.cfg.NoOriginalClientID) &&
					!(r.ID == "P-FEED-4" && tc.cfg.SecondClientID == ""):
					t.Errorf("%s: %s %s", r.ID, r.Status, strings.Join(r.Messages, "; "))
				}
			}
		})
	}
}

// A key that does not see the Kestrel data (a real catalog): the
// fixture-independent checks run, partly, and the fixture checks are skipped.
func TestOnlineCheckWithoutFixtures(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	c := n.Credentials()
	srv := httptest.NewServer(n)
	defer srv.Close()
	ctx := context.Background()
	admin, _ := nodeadmin.New(srv.URL, http.DefaultClient)
	if _, err := admin.Login(ctx, c.AdminEmail, c.AdminPassword); err != nil {
		t.Fatal(err)
	}
	rt, err := admin.CreateRetailer(ctx, nodeadmin.RetailerInput{Name: "Trail only", BrandIDs: &[]string{c.TrailBrandID}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := admin.CreateAPIClient(ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: "trail", Scopes: []string{obx.ScopeCatalogRead, obx.ScopeMediaRead}})
	if err != nil {
		t.Fatal(err)
	}
	rep := onlineRun(t, srv.URL, conformance.Config{ClientID: key.Client.ClientID, ClientSecret: key.ClientSecret})
	logOnline(t, rep)
	st := statuses(rep)
	for id, want := range map[string]string{"P-FEED-1": "pass", "P-CAT-2": "pass", "P-CAT-6": "pass", "P-CAT-1": "skip", "P-MEDIA-2": "pass", "P-MEDIA-3": "pass", "P-ACC-1": "skip"} {
		if st[id].Status != want {
			t.Errorf("%s: %s, want %s (%v)", id, st[id].Status, want, st[id].Messages)
		}
	}
	for _, id := range []string{"P-FEED-1", "P-MEDIA-2", "P-MEDIA-3"} {
		if st[id].Online != "partial" {
			t.Errorf("%s without fixtures must be partial", id)
		}
	}
}

// The budget stops the run; what did not run is skipped, never failed.
func TestOnlineCheckBudget(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	c := n.Credentials()
	srv := httptest.NewServer(n)
	defer srv.Close()
	rep, err := conformance.Run(context.Background(), []string{conformance.ProfileProtocol}, conformance.Config{BaseURL: srv.URL,
		ClientID: c.Full.ClientID, ClientSecret: c.Full.ClientSecret, Online: &conformance.Online{AllowInsecure: true, MaxRequests: 40, MinInterval: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	logOnline(t, rep)
	if rep.Requests > 40 {
		t.Errorf("%d requests, budget 40", rep.Requests)
	}
	if rep.Failed > 0 {
		for _, r := range rep.Results {
			if r.Status == conformance.StatusFail {
				t.Errorf("%s failed under the budget: %v", r.ID, r.Messages)
			}
		}
	}
}

// Without AllowInsecure, loopback portals and http are refused.
func TestOnlineCheckRefusesPrivate(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewTLSServer(n)
	defer srv.Close()
	rep, err := conformance.Run(context.Background(), []string{conformance.ProfileProtocol}, conformance.Config{BaseURL: srv.URL, Online: &conformance.Online{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.SetupError, "loopback") {
		t.Errorf("a loopback portal must be refused before any test: %q", rep.SetupError)
	}
	if _, err := conformance.Run(context.Background(), []string{conformance.ProfileProtocol}, conformance.Config{BaseURL: "https://x.example", SetupCode: "x", Online: &conformance.Online{}}); err == nil {
		t.Errorf("an online check with a setup code must be refused")
	}
}

// A portal that hands out a token for a wrong secret must not get that token
// (or the secret) into the report, which may be stored and shared.
func TestOnlineReportIsRedacted(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	c := n.Credentials()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth/token") {
			if _, secret, _ := r.BasicAuth(); strings.HasSuffix(secret, "x") {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"access_token":"obxat_leaked-token-value","token_type":"bearer","expires_in":3600,"echo":"` + secret + `"}`))
				return
			}
		}
		n.ServeHTTP(w, r)
	}))
	defer srv.Close()
	rep := onlineRun(t, srv.URL, conformance.Config{ClientID: c.Full.ClientID, ClientSecret: c.Full.ClientSecret})
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), "leaked-token-value") || strings.Contains(string(b), c.Full.ClientSecret) {
		t.Fatalf("the report contains a token or the secret: %s", statuses(rep)["P-AUTH-1"].Messages)
	}
	if statuses(rep)["P-AUTH-1"].Status != conformance.StatusFail {
		t.Errorf("P-AUTH-1 must fail when a wrong secret gets a token")
	}
}

// A portal that answers 429 with a long Retry-After stops the check at once:
// the remaining tests are skipped as rate limited, nothing fails, and the
// check does not wait for the Retry-After.
func TestOnlineCheckStopsOn429(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	c := n.Credentials()
	var count int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		limited := count > 30
		mu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "600")
			w.Header().Set("Content-Type", obx.MediaTypeProblem)
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"type":"about:blank","title":"slow down","status":429,"obx_error":"rate_limited"}`))
			return
		}
		n.ServeHTTP(w, r)
	}))
	defer srv.Close()
	start := time.Now()
	var progress []string
	rep := onlineRun(t, srv.URL, conformance.Config{ClientID: c.Full.ClientID, ClientSecret: c.Full.ClientSecret,
		Progress: func(r conformance.Result) { progress = append(progress, r.ID) }})
	logOnline(t, rep)
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("the check took %s; it must not wait for Retry-After", d)
	}
	limited := 0
	for _, r := range rep.Results {
		if r.Status == conformance.StatusFail {
			t.Errorf("%s failed: %v", r.ID, r.Messages)
		}
		if strings.Contains(strings.Join(r.Messages, " "), "rate-limited") {
			limited++
		}
	}
	if limited == 0 {
		t.Errorf("no test says the check was rate-limited")
	}
	if len(progress) != len(rep.Results) {
		t.Errorf("progress reported %d results, the report has %d", len(progress), len(rep.Results))
	}
}

// The public demo: a read-only mock with a fixed key passes every online
// check, and refuses writes, ingest and admin.
func TestOnlineCheckAgainstReadOnlyDemo(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true, ReadOnly: true, FullClientID: "obxc_demo", FullClientSecret: "obxs_public-demo-key"})
	srv := httptest.NewServer(n)
	defer srv.Close()
	rep := onlineRun(t, srv.URL, conformance.Config{ClientID: "obxc_demo", ClientSecret: "obxs_public-demo-key"})
	logOnline(t, rep)
	for _, r := range rep.Results {
		if r.OnlineTier == "" {
			t.Errorf("%s has no online tier", r.ID)
		}
		want := conformance.StatusPass
		if r.OnlineTier == "local" || r.ID == "P-AUTH-3" || r.ID == "P-FEED-4" {
			want = conformance.StatusSkip
		}
		if r.Status != want {
			t.Errorf("%s: %s, want %s: %v", r.ID, r.Status, want, r.Messages)
		}
	}
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/obx/v1/exports", http.StatusForbidden},
		{http.MethodPost, "/obx/v1/webhooks", http.StatusForbidden},
		{http.MethodPost, "/obx/v1/access-requests", http.StatusForbidden},
		{http.MethodPost, obx.PathAdminAPI + "/session/login", http.StatusNotFound},
		{http.MethodGet, obx.PathAdminAPI + "/setup", http.StatusNotFound},
		{http.MethodGet, obx.PathIngest, http.StatusNotFound},
		{http.MethodPut, obx.PathIngest + "/products/x", http.StatusNotFound},
		{http.MethodGet, "/obx/v1/products", http.StatusUnauthorized},
		{http.MethodDelete, "/obx/v1/changes", http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, res.StatusCode, c.status)
		}
	}
}
