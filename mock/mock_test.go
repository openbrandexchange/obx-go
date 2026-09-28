package mock_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/nodeadmin"
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

// The mock's IngestRecord has the same shape as the nodes': defaults applied,
// every nullable field present, missing_files for assets only.
func TestIngestRecordShape(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	get := func(path string) map[string]any {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+obx.PathIngest+path, nil)
		req.Header.Set("Authorization", "Bearer "+n.Credentials().IngestToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	a := get("/assets/KO-1001_NVY_1")
	data := a["data"].(map[string]any)
	if r, _ := data["rights"].(map[string]any); r == nil || r["license"] == nil {
		t.Errorf("rights with defaults applied: %v", data["rights"])
	}
	if p, _ := data["provenance"].(map[string]any); p == nil || p["source"] != "photo" {
		t.Errorf("provenance with defaults applied: %v", data["provenance"])
	}
	files, _ := data["files"].(map[string]any)
	if len(files) != 4 {
		t.Errorf("files should list all four current blobs: %v", files)
	}
	if mf, ok := a["missing_files"].([]any); !ok || len(mf) != 0 {
		t.Errorf("missing_files of a complete asset: %#v", a["missing_files"])
	}
	p := get("/products/KO-1001")
	if _, ok := p["missing_files"]; ok {
		t.Error("missing_files is for assets only")
	}
	if v, ok := p["data"].(map[string]any)["dpp_url"]; !ok || v != nil {
		t.Errorf("unsent nullable fields are null: %v", p["data"])
	}
}

// A brand's own portal word (branding.portal_name) is stored per language,
// shown on the public branding and cleared with null.
func TestPortalName(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	ctx := context.Background()
	creds := n.Credentials()
	adm, _ := nodeadmin.New(srv.URL, nil)
	if _, err := adm.Login(ctx, creds.AdminEmail, creds.AdminPassword); err != nil {
		t.Fatal(err)
	}
	s, err := adm.PatchSettings(ctx, map[string]any{"branding": map[string]any{"portal_name": map[string]string{"nl": "Merkbeeldbank", "de": ""}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Branding.PortalName; len(got) != 1 || got["nl"] != "Merkbeeldbank" {
		t.Fatalf("settings portal_name: %v", got)
	}
	public := func() nodeadmin.PublicBranding {
		var b nodeadmin.PublicBranding
		if err := adm.Call(ctx, http.MethodGet, obx.PathAdminAPI+"/public/branding", nil, &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := public(); b.PortalName["nl"] != "Merkbeeldbank" {
		t.Fatalf("public branding portal_name: %v", b.PortalName)
	}
	// Changing another setting keeps it; null clears it.
	if _, err := adm.PatchSettings(ctx, map[string]any{"branding": map[string]any{"accent_color": "#123456"}}); err != nil {
		t.Fatal(err)
	}
	if b := public(); b.PortalName["nl"] != "Merkbeeldbank" {
		t.Fatalf("portal_name lost by an unrelated change: %v", b.PortalName)
	}
	s, err = adm.PatchSettings(ctx, json.RawMessage(`{"branding":{"portal_name":null}}`))
	if err != nil || s.Branding.PortalName != nil {
		t.Fatalf("clear portal_name: %v %v", s, err)
	}
	resp, err := http.Get(srv.URL + obx.PathAdminAPI + "/public/branding")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	json.NewDecoder(resp.Body).Decode(&raw)
	resp.Body.Close()
	if string(raw["portal_name"]) != "null" {
		t.Fatalf("public branding without a portal word: portal_name = %s, want null", raw["portal_name"])
	}
}
