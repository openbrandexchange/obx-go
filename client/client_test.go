package client_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/obx"
)

// fixtureNode returns a fresh mock node with the Kestrel fixtures.
func fixtureNode(t *testing.T) (*mock.Node, *httptest.Server) {
	t.Helper()
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	return n, srv
}

func newClient(t *testing.T, srv *httptest.Server, c mock.ClientCredentials) *client.Client {
	t.Helper()
	cl, err := client.New(client.Config{BaseURL: srv.URL, ClientID: c.ClientID, ClientSecret: c.ClientSecret})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

// mirror is a tiny consumer: it keeps what the feed says is visible.
type mirror struct {
	objects map[string]obx.Change
	seen    map[string]bool
	events  []client.EventKind
	resyncs []string
}

func newMirror() *mirror { return &mirror{objects: map[string]obx.Change{}} }

func (m *mirror) handle(_ context.Context, ev client.Event) error {
	m.events = append(m.events, ev.Kind)
	switch ev.Kind {
	case client.EventSnapshotStarted:
		m.seen = map[string]bool{}
	case client.EventSnapshotComplete:
		for id := range m.objects {
			if !m.seen[id] {
				delete(m.objects, id)
			}
		}
		m.seen = nil
	case client.EventResync:
		m.resyncs = append(m.resyncs, ev.Reason)
	case client.EventChange:
		ch := ev.Change
		if ch.Op == obx.OpDelete {
			delete(m.objects, ch.ID)
			return nil
		}
		m.objects[ch.ID] = *ch
		if m.seen != nil {
			m.seen[ch.ID] = true
		}
	}
	return nil
}

func (m *mirror) count(typ string) int {
	n := 0
	for _, c := range m.objects {
		if c.Type == typ {
			n++
		}
	}
	return n
}

func TestSyncSnapshotThenLive(t *testing.T) {
	n, srv := fixtureNode(t)
	c := newClient(t, srv, n.Credentials().Full)
	store := client.FileCursorStore(filepath.Join(t.TempDir(), "cursor.txt"))
	m := newMirror()
	ctx := context.Background()
	if err := c.Sync(ctx, store, client.SyncOptions{Limit: 10}, m.handle); err != nil {
		t.Fatal(err)
	}
	if m.count("product") != 11 || m.count("variant") != 46 || m.count("asset") != 35 || m.count("brand") != 2 {
		t.Fatalf("mirror after snapshot: %d brands %d products %d variants %d assets", m.count("brand"), m.count("product"), m.count("variant"), m.count("asset"))
	}
	cur, _ := store.Load(ctx)
	if cur == "" {
		t.Fatal("cursor not saved")
	}
	ing, _ := ingest.New(srv.URL, n.Credentials().IngestToken)
	if _, err := ing.Unpublish(ctx, obx.KindProduct, "KO-1006"); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(ctx, store, client.SyncOptions{}, m.handle); err != nil {
		t.Fatal(err)
	}
	if m.count("product") != 10 || m.count("variant") != 44 || m.count("asset") != 33 {
		t.Fatalf("after unpublishing KO-1006: %d products %d variants %d assets", m.count("product"), m.count("variant"), m.count("asset"))
	}
}

func TestSyncToleratesDuplicates(t *testing.T) {
	n, srv := fixtureNode(t)
	c := newClient(t, srv, n.Credentials().Full)
	ctx := context.Background()
	store := &client.MemoryCursorStore{}
	m := newMirror()
	if err := c.Sync(ctx, store, client.SyncOptions{}, m.handle); err != nil {
		t.Fatal(err)
	}
	n.SetDuplicateChanges(true)
	ing, _ := ingest.New(srv.URL, n.Credentials().IngestToken)
	ing.Patch(ctx, obx.KindProduct, "KO-1001", map[string]any{"season": "AW27"}, nil)
	ing.Unpublish(ctx, obx.KindAsset, "KO-1001_NVY_2")
	changes := 0
	err := c.Sync(ctx, store, client.SyncOptions{}, func(ctx context.Context, ev client.Event) error {
		if ev.Kind == client.EventChange {
			changes++
		}
		return m.handle(ctx, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	if changes != 4 {
		t.Fatalf("want 2 entries delivered twice, got %d", changes)
	}
	if m.count("asset") != 34 || m.count("product") != 11 {
		t.Fatalf("duplicates corrupted the mirror: %d assets", m.count("asset"))
	}
}

func TestSyncRestartsOnExpiredCursor(t *testing.T) {
	n, srv := fixtureNode(t)
	c := newClient(t, srv, n.Credentials().Full)
	ctx := context.Background()
	store := &client.MemoryCursorStore{}
	m := newMirror()
	if err := c.Sync(ctx, store, client.SyncOptions{}, m.handle); err != nil {
		t.Fatal(err)
	}
	n.ExpireCursors()
	ing, _ := ingest.New(srv.URL, n.Credentials().IngestToken)
	ing.Delete(ctx, obx.KindProduct, "KT-2002")
	m.events = nil
	if err := c.Sync(ctx, store, client.SyncOptions{}, m.handle); err != nil {
		t.Fatal(err)
	}
	if len(m.resyncs) != 1 || m.resyncs[0] != "cursor_expired" {
		t.Fatalf("resyncs %v", m.resyncs)
	}
	if m.count("product") != 10 {
		t.Fatalf("mark and sweep after the restart should drop KT-2002: %d products", m.count("product"))
	}
	if !contains(m.events, client.EventSnapshotComplete) {
		t.Fatalf("no snapshot_complete after the restart: %v", m.events)
	}
}

func TestSyncResyncsOnGrantChange(t *testing.T) {
	n, srv := fixtureNode(t)
	creds := n.Credentials()
	c := newClient(t, srv, creds.Full)
	ctx := context.Background()
	store := &client.MemoryCursorStore{}
	m := newMirror()
	if err := c.Sync(ctx, store, client.SyncOptions{}, m.handle); err != nil {
		t.Fatal(err)
	}
	if err := n.SetRetailerBrands(creds.Full.RetailerID, []string{creds.KestrelBrandID}); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(ctx, store, client.SyncOptions{}, m.handle); err != nil {
		t.Fatal(err)
	}
	if len(m.resyncs) != 1 || m.resyncs[0] != "grant" {
		t.Fatalf("resyncs %v", m.resyncs)
	}
	if m.count("product") != 9 || m.count("brand") != 1 {
		t.Fatalf("after narrowing the grant: %d products, %d brands", m.count("product"), m.count("brand"))
	}
}

func TestRevokedAccess(t *testing.T) {
	n, srv := fixtureNode(t)
	creds := n.Credentials()
	c := newClient(t, srv, creds.Full)
	ctx := context.Background()
	if _, err := c.Grant(ctx); err != nil {
		t.Fatal(err)
	}
	n.RevokeClient(creds.Full.ClientID)
	_, err := c.Grant(ctx)
	if !obx.IsCode(err, obx.ErrAccessRevoked) {
		t.Fatalf("want access_revoked, got %v", err)
	}
	c2 := newClient(t, srv, creds.Full)
	_, err = c2.Grant(ctx)
	var oe *obx.OAuthError
	if !errors.As(err, &oe) || oe.Code != "invalid_client" {
		t.Fatalf("new token for a revoked client: want invalid_client, got %v", err)
	}
}

func TestDownloadVerifiesSHA256(t *testing.T) {
	n, srv := fixtureNode(t)
	c := newClient(t, srv, n.Credentials().Full)
	ctx := context.Background()
	res, err := c.LookupGTINs(ctx, []string{"2000100501016"})
	if err != nil || len(res.Products) != 1 {
		t.Fatalf("lookup: %v %+v", err, res)
	}
	a := res.Products[0].Assets[0]
	var buf bytes.Buffer
	if _, err := c.Download(ctx, &a.Renditions.Web, &buf); err != nil {
		t.Fatal(err)
	}
	n.SetCorruptDownloads(true)
	buf.Reset()
	_, err = c.Download(ctx, a.Renditions.Original, &buf)
	if !errors.Is(err, client.ErrChecksum) {
		t.Fatalf("changed bytes: want ErrChecksum, got %v", err)
	}
	n.SetCorruptDownloads(false)
	if _, _, err := c.DownloadAsset(ctx, a.ID, obx.RenditionOriginal, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	noOrig := newClient(t, srv, n.Credentials().NoOriginals)
	if _, _, err := noOrig.DownloadAsset(ctx, a.ID, obx.RenditionOriginal, &bytes.Buffer{}); !obx.IsCode(err, obx.ErrOriginalNotAllowed) {
		t.Fatalf("original without permission: %v", err)
	}
}

func TestRetriesAfter429(t *testing.T) {
	n, srv := fixtureNode(t)
	c := newClient(t, srv, n.Credentials().Full)
	ctx := context.Background()
	if _, err := c.Grant(ctx); err != nil {
		t.Fatal(err)
	}
	n.Force429(2, 1)
	start := time.Now()
	g, err := c.Grant(ctx)
	if err != nil {
		t.Fatalf("the client should retry 429: %v", err)
	}
	if time.Since(start) < 2*time.Second {
		t.Fatalf("the client did not wait for Retry-After (%s)", time.Since(start))
	}
	if g.Retailer.Name != "Example Store" {
		t.Fatalf("grant %+v", g)
	}
	n.Fail5xx(1)
	if _, err := c.Brands(ctx); err != nil {
		t.Fatalf("the client should retry a 503 on GET: %v", err)
	}
}

func TestLookupSplitsLongLists(t *testing.T) {
	n, srv := fixtureNode(t)
	c := newClient(t, srv, n.Credentials().Full)
	codes := make([]string, 0, 2500)
	for i := 0; i < 2499; i++ {
		codes = append(codes, "12345")
	}
	codes = append(codes, "2000100101025")
	res, err := c.LookupGTINs(context.Background(), codes)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Products) != 1 || res.Products[0].ExternalID != "KO-1001" {
		t.Fatalf("got %+v", res.Products)
	}
}

func TestAccessRequestFlow(t *testing.T) {
	_, srv := fixtureNode(t)
	c, _ := client.New(client.Config{BaseURL: srv.URL + "/.well-known/obx"})
	ar, err := c.RequestAccess(context.Background(), obx.AccessRequestInput{Retailer: obx.AccessRequestRetailer{Name: "Shop"},
		Contact: obx.AccessRequestContact{Name: "A", Email: "a@shop.example"}, Access: []string{"api"}, RequestedBrandIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.PollAccessRequest(context.Background(), ar.ID, ar.RequestToken)
	if err != nil || st.Status != "pending" {
		t.Fatalf("%v %+v", err, st)
	}
}

func TestNodeBase(t *testing.T) {
	cases := map[string]string{
		"brands.kestrel.example":                         "https://brands.kestrel.example",
		"https://brands.kestrel.example/.well-known/obx": "https://brands.kestrel.example",
		"https://brands.kestrel.example/obx/v1/ingest":   "https://brands.kestrel.example",
		"http://localhost:8080/":                         "http://localhost:8080",
		"https://host.example/sub/obx/v1/changes?x=1":    "https://host.example/sub",
	}
	for in, want := range cases {
		got, err := client.NodeBase(in)
		if err != nil || got != want {
			t.Errorf("NodeBase(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := client.NodeBase("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

func contains(list []client.EventKind, k client.EventKind) bool {
	for _, x := range list {
		if x == k {
			return true
		}
	}
	return false
}
