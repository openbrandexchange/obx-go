package conformance

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/renditions"
	"github.com/openbrandexchange/obx-go/specdata"
	"github.com/openbrandexchange/obx-go/webhook"
)

func init() {
	p := ProfileProtocol
	register(p, "P-DISC-1", "discovery document is public, valid, CORS *, api_base ends in /obx/v1", true, testDiscovery)
	register(p, "P-DISC-2", "required capabilities present; missing optional capabilities answer 501", true, testCapabilities)
	register(p, "P-AUTH-1", "token endpoint: client_secret_basic, invalid_client, invalid_scope", true, testTokenEndpoint)
	register(p, "P-AUTH-2", "missing/invalid token and revoked client are refused", true, testAuthErrors)
	register(p, "P-AUTH-3", "without media:original no original anywhere, 403 on /renditions/original", true, testNoOriginal)
	register(p, "P-ACC-1", "access request, status polling, credentials exactly once", true, testAccessRequest)
	register(p, "P-FEED-1", "snapshot lists exactly the visible fixture objects in type order, then live", true, testSnapshot)
	register(p, "P-FEED-2", "next_cursor always present; limit=1 gives the same entries as limit=500", true, testPaging)
	register(p, "P-FEED-3", "garbage cursor → 400 cursor_invalid", true, testGarbageCursor)
	register(p, "P-FEED-4", "restricted retailer only sees brand kestrel", true, testRestricted)
	registerPhase(phaseOf[ProfileIngest]+1, p, "P-FEED-5", "ingest replay produces exactly expected-changes.json", true, testReplayFeed)
	register(p, "P-FEED-6", "grant change → grant entry with resync:true", true, testGrantResync)
	register(p, "P-CAT-1", "products KO-1001 and KO-1005 match the expected fixtures, including the 0.2 article data", true, testProductFixtures)
	register(p, "P-CAT-2", "GET /products pagination is complete and stable; ids= filter", true, testProductList)
	register(p, "P-CAT-3", "GTIN lookup matches the expected fixture; 2001 codes → limit_exceeded", true, testGTINLookup)
	register(p, "P-CAT-4", "invisible objects answer 404, never 403", true, testInvisible404)
	register(p, "P-CAT-5", "article data round trip: KO-1008, KO-1009, KO-1010 and KT-2001 match the expected fixtures", true, testArticleRoundTrip)
	register(p, "P-CAT-6", "article data shape over the whole snapshot: 0.2 properties present, valid, normalised fibres, no empty objects", true, testArticleShape)
	register(p, "P-MEDIA-1", "assets match the expected fixtures (AI, embargo, large original, no original)", true, testAssetFixtures)
	register(p, "P-MEDIA-2", "renditions: sha256, ETag, OBX-Content-SHA256, Range, If-None-Match, signed URLs", true, testRenditionHTTP)
	register(p, "P-MEDIA-3", "originals byte-identical; web ≤ 2000, thumb ≤ 600, square is square, JPEG, never upscaled", true, testRenditionRules)
	register(p, "P-EXP-1", "export: manifest, zip parts, hashes, changes_cursor", false, testExport)
	register(p, "P-HOOK-1", "webhooks: signed webhook.test delivery; unsafe targets refused", false, testWebhooks)
	register(p, "P-USE-1", "usage events accepted and counted", false, testUsage)
	register(p, "P-RATE-1", "RateLimit headers; 429 carries Retry-After", true, testRateLimit)
	register(p, "P-ERR-1", "every error answer is problem+json with a known obx_error", true, testProblems)
}

// ---- helpers

func (t *T) validate(schema string, raw []byte, what string) {
	v, err := specdata.DefaultValidator()
	t.Check(err, "schemas")
	if err := v.Validate(schema, raw); err != nil {
		t.Errorf("%s is not a valid %s: %v", what, schema, err)
	}
}

func (t *T) expectStatus(res *response, status int, code string, what string) {
	if res.status != status || (code != "" && res.code() != code) {
		t.Errorf("%s: want %d %s, got %d %s %s", what, status, code, res.status, res.code(), truncate(res.body))
	}
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// feed is everything read from /changes.
type feed struct {
	snapshot []obx.Change
	live     []obx.Change
	cursor   string
	pages    []*obx.ChangesPage
}

// readFeed reads /changes from cursor until has_more is false.
func (t *T) readFeed(c *apiKey, cursor string, limit int) *feed {
	f := &feed{}
	for i := 0; i < 100000; i++ {
		v := url.Values{}
		if cursor != "" {
			v.Set("cursor", cursor)
		}
		v.Set("limit", fmt.Sprint(limit))
		res := t.get(c, "/changes?"+v.Encode())
		if res.status != http.StatusOK {
			t.Fatalf("GET /changes: %d %s", res.status, truncate(res.body))
		}
		var page obx.ChangesPage
		t.Check(res.decode(&page), "decode ChangesPage")
		f.pages = append(f.pages, &page)
		if page.NextCursor == "" {
			t.Fatalf("a /changes page has no next_cursor")
		}
		for _, ch := range page.Changes {
			if page.Phase == obx.PhaseSnapshot {
				f.snapshot = append(f.snapshot, ch)
			} else {
				f.live = append(f.live, ch)
			}
		}
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}
	f.cursor = cursor
	return f
}

// liveCursor drains the snapshot and returns a live cursor.
func (t *T) liveCursor(c *apiKey) string { return t.readFeed(c, "", 500).cursor }

// newEntries returns the entries after cursor.
func (t *T) newEntries(c *apiKey, cursor string) ([]obx.Change, string) {
	f := t.readFeed(c, cursor, 500)
	return f.live, f.cursor
}

func (t *T) needAdmin(what string) {
	if t.r.admin == nil {
		t.Skip("%s needs admin credentials (--admin-user/--admin-password or --setup-code)", what)
	}
}

func (t *T) productID(ext string) string {
	id, ok := t.r.productIDs[ext]
	if !ok {
		t.Fatalf("fixture product %s is not visible to the unrestricted API key", ext)
	}
	return id
}

func (t *T) assetID(ext string) string {
	id, ok := t.r.assetIDs[ext]
	if !ok {
		t.Fatalf("fixture image %s is not visible to the unrestricted API key", ext)
	}
	return id
}

// ---- discovery and auth

func testDiscovery(t *T) {
	res, err := t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathDiscovery, nil, http.Header{"Origin": {"https://retailer.example"}})
	t.Check(err, "GET /.well-known/obx")
	if res.status != http.StatusOK {
		t.Fatalf("status %d: %s", res.status, truncate(res.body))
	}
	t.validate("DiscoveryDocument", res.body, "the discovery document")
	if res.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("Access-Control-Allow-Origin is %q, want *", res.header.Get("Access-Control-Allow-Origin"))
	}
	var d obx.DiscoveryDocument
	t.Check(res.decode(&d), "decode")
	if !strings.HasSuffix(strings.TrimRight(d.APIBase, "/"), "/obx/v1") {
		t.Errorf("api_base %q does not end in /obx/v1", d.APIBase)
	}
	if d.ObxVersion != obx.Version {
		// P-DISC-1 does not ask for a version; the article data tests
		// (P-CAT-1, P-CAT-5, P-CAT-6) show what a 0.1 node misses.
		t.Notef("obx_version %q; this suite checks protocol %s", d.ObxVersion, obx.Version)
	}
	if len(d.Languages) == 0 {
		t.Errorf("languages is empty")
	}
}

func testCapabilities(t *T) {
	d, err := t.r.newClient("", "").Discovery(t.ctx)
	t.Check(err, "discovery")
	for _, c := range []string{obx.CapChanges, obx.CapProducts, obx.CapByGTIN, obx.CapRenditions} {
		if !d.Has(c) {
			t.Errorf("required capability %q is missing", c)
		}
	}
	if t.r.online() && t.r.full == nil {
		t.onlineStop("the 501 answers of capabilities the portal does not offer need an API key")
	}
	tok, err := t.r.full.Token(t.ctx)
	t.Check(err, "token")
	base := t.r.cfg.BaseURL + obx.PathAPIBase
	probes := map[string]func() (*response, error){
		obx.CapExports: func() (*response, error) {
			return t.r.do(t.ctx, http.MethodPost, base+"/exports", []byte(`{"renditions":["thumb"]}`), mergeHeader(bearerHeader(tok), "Content-Type", "application/json"))
		},
		obx.CapWebhooks: func() (*response, error) {
			return t.r.do(t.ctx, http.MethodGet, base+"/webhooks", nil, bearerHeader(tok))
		},
		obx.CapUsage: func() (*response, error) {
			return t.r.do(t.ctx, http.MethodPost, base+"/usage", []byte(`{"events":[]}`), mergeHeader(bearerHeader(tok), "Content-Type", "application/json"))
		},
		obx.CapIngest: func() (*response, error) {
			return t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathIngest, nil, bearerHeader("obxi_probe"))
		},
	}
	for c, probe := range probes {
		if d.Has(c) {
			continue
		}
		if t.r.online() && (c == obx.CapExports || c == obx.CapUsage) {
			t.partialf("the 501 answer for %s (the probe is a POST)", c)
			continue
		}
		res, err := probe()
		t.Check(err, c)
		t.expectStatus(res, http.StatusNotImplemented, obx.ErrNotImplemented, "capability "+c+" is not advertised, so its endpoint")
	}
}

func mergeHeader(h http.Header, k, v string) http.Header {
	h.Set(k, v)
	return h
}

func (r *runner) tokenRequest(ctx context.Context, id, secret, scope string) (*response, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if scope != "" {
		form.Set("scope", scope)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.BaseURL+obx.PathAPIBase+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(id, secret)
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &response{status: resp.StatusCode, header: resp.Header, body: b}, nil
}

func testTokenEndpoint(t *T) {
	id, secret := t.r.fullCreds[0], t.r.fullCreds[1]
	res, err := t.r.tokenRequest(t.ctx, id, secret, "")
	t.Check(err, "token request")
	if res.status != http.StatusOK {
		t.Fatalf("client_secret_basic: status %d %s", res.status, truncate(res.body))
	}
	t.validate("TokenResponse", res.body, "the token response")
	var tr obx.TokenResponse
	res.decode(&tr)
	if !strings.EqualFold(tr.TokenType, "bearer") || tr.ExpiresIn <= 0 {
		t.Errorf("token_type %q expires_in %d", tr.TokenType, tr.ExpiresIn)
	}
	if cc := res.header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control is %q, want no-store", cc)
	}
	res, err = t.r.tokenRequest(t.ctx, id, secret+"x", "")
	t.Check(err, "token request")
	t.expectStatus(res, http.StatusUnauthorized, "invalid_client", "wrong secret")
	res, err = t.r.tokenRequest(t.ctx, id, secret, "bogus:scope")
	t.Check(err, "token request")
	t.expectStatus(res, http.StatusBadRequest, "invalid_scope", "unknown scope")
}

func testAuthErrors(t *T) {
	base := t.r.cfg.BaseURL + obx.PathAPIBase
	res, err := t.r.do(t.ctx, http.MethodGet, base+"/changes", nil, nil)
	t.Check(err, "GET /changes")
	t.expectStatus(res, http.StatusUnauthorized, obx.ErrUnauthorized, "no token")
	res, err = t.r.do(t.ctx, http.MethodGet, base+"/changes", nil, bearerHeader("obxat_not-a-real-token"))
	t.Check(err, "GET /changes")
	t.expectStatus(res, http.StatusUnauthorized, obx.ErrInvalidToken, "unknown token")
	t.needOperator("revocation")
	c := t.newThrowawayKey("OBX conformance: revocation", nil)
	tok, err := c.Token(t.ctx)
	t.Check(err, "token of a new API key")
	if res := t.get(c, "/grant"); res.status != http.StatusOK {
		t.Fatalf("GET /grant with a new API key: %d", res.status)
	}
	if t.r.admin != nil {
		clients, err := t.r.adminClients(t.ctx)
		t.Check(err, "list API keys")
		var revoked bool
		for _, cl := range clients {
			if cl.ClientID == c.id {
				t.Check(t.r.admin.RevokeAPIClient(t.ctx, cl.ID), "revoke API key")
				revoked = true
			}
		}
		if !revoked {
			t.Fatalf("the new API key is not in the admin list")
		}
	} else {
		t.opStep(Action{Name: ActRevokeAPIKey, Params: map[string]string{"client_id": c.id}, Hint: "Revoke the API key with this client id."})
	}
	res, err = t.r.do(t.ctx, http.MethodGet, base+"/grant", nil, bearerHeader(tok))
	t.Check(err, "GET /grant")
	t.expectStatus(res, http.StatusUnauthorized, obx.ErrAccessRevoked, "revoked API key")
	res, err = t.r.tokenRequest(t.ctx, c.id, c.secret, "")
	t.Check(err, "token")
	t.expectStatus(res, http.StatusUnauthorized, "invalid_client", "token for a revoked API key")
}

// throwawayClient creates a retailer and an API key through the admin API.
func (r *runner) throwawayClient(name string, brandIDs *[]string) *apiKey {
	ctx := context.Background()
	rt, err := r.admin.CreateRetailer(ctx, nodeadmin.RetailerInput{Name: name, BrandIDs: brandIDs})
	if err != nil {
		panic(fmt.Sprintf("create retailer: %v", err))
	}
	c, err := r.admin.CreateAPIClient(ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: name, Scopes: []string{obx.ScopeCatalogRead, obx.ScopeMediaRead, obx.ScopeUsageWrite}})
	if err != nil {
		panic(fmt.Sprintf("create API key: %v", err))
	}
	cl := r.newClient(c.Client.ClientID, c.ClientSecret)
	cl.retailerID = rt.ID
	return cl
}

func (r *runner) adminClients(ctx context.Context) ([]nodeadmin.APIClient, error) {
	var out obx.Items[nodeadmin.APIClient]
	err := r.admin.Call(ctx, http.MethodGet, obx.PathAdminAPI+"/api-clients", nil, &out)
	return out.Items, err
}

func testNoOriginal(t *T) {
	if t.r.noOrig == nil {
		t.Skip("needs an API key without originals (--no-original-client-id) or admin credentials")
	}
	res, err := t.r.tokenRequest(t.ctx, t.r.cfg.NoOriginalClientID, t.r.cfg.NoOriginalClientSecret, obx.ScopeMediaOriginal)
	t.Check(err, "token")
	if res.status == http.StatusOK {
		var tr obx.TokenResponse
		res.decode(&tr)
		if strings.Contains(tr.Scope, obx.ScopeMediaOriginal) {
			t.Errorf("a client without originals obtained a token with media:original")
		}
	}
	p := t.get(t.r.noOrig, "/products/"+t.productID("KO-1005"))
	if p.status != http.StatusOK {
		t.Fatalf("GET product: %d", p.status)
	}
	if bytes.Contains(p.body, []byte(`"original"`)) {
		t.Errorf("the product answer contains an original rendition")
	}
	a := t.get(t.r.noOrig, "/assets/"+t.assetID("KO-1005_COG_1"))
	if bytes.Contains(a.body, []byte(`"original"`)) {
		t.Errorf("the asset answer contains an original rendition")
	}
	r := t.get(t.r.noOrig, "/assets/"+t.assetID("KO-1005_COG_1")+"/renditions/original")
	t.expectStatus(r, http.StatusForbidden, obx.ErrOriginalNotAllowed, "GET /renditions/original without media:original")
	g := t.get(t.r.noOrig, "/products:by-gtin?gtin=2000100501016")
	if bytes.Contains(g.body, []byte(`"original"`)) {
		t.Errorf("the GTIN lookup contains an original rendition")
	}
	f := t.readFeed(t.r.noOrig, "", 500)
	for _, ch := range f.snapshot {
		if ch.Type == obx.ChangeAsset && bytes.Contains(ch.Data, []byte(`"original"`)) {
			t.Errorf("snapshot entry %s contains an original rendition", ch.ExternalIDValue())
			break
		}
	}
}

func testAccessRequest(t *T) {
	if t.r.discovery != nil && !t.r.discovery.Has(obx.CapAccessRequests) {
		t.Skip("capability access_requests not offered")
	}
	anon := t.r.newClient("", "")
	retailerName := "Conformance Boutique " + randomSuffix()
	ar, err := anon.RequestAccess(t.ctx, obx.AccessRequestInput{
		Retailer: obx.AccessRequestRetailer{Name: retailerName, Country: obx.Ptr("NL")},
		Contact:  obx.AccessRequestContact{Name: "Robin Example", Email: "robin+" + randomSuffix() + "@boutique.example", Language: obx.Ptr("nl")},
		Access:   []string{"api"}, RequestedBrandIDs: []string{}})
	t.Check(err, "POST /access-requests")
	if ar.Status != "pending" || ar.RequestToken == "" || ar.ID == "" {
		t.Fatalf("want status pending with id and request_token, got %+v", ar)
	}
	st, err := anon.PollAccessRequest(t.ctx, ar.ID, ar.RequestToken)
	t.Check(err, "poll")
	if st.Status != "pending" || st.Credentials != nil {
		t.Errorf("before approval: %+v", st)
	}
	if _, err := anon.PollAccessRequest(t.ctx, ar.ID, "obxr_wrong"); err == nil {
		t.Errorf("polling with a wrong request token succeeded")
	}
	t.needOperator("approving the request")
	if t.r.admin != nil {
		_, err = t.r.admin.Approve(t.ctx, ar.ID, nodeadmin.ApproveInput{CreateAPIClient: obx.Ptr(true)})
		t.Check(err, "approve")
	} else {
		t.opStep(Action{Name: ActApproveAccessRequest, Params: map[string]string{"request_id": ar.ID, "retailer_name": retailerName},
			Hint: "Approve the access request from this retailer, with API access (an API key)."})
	}
	st, err = anon.PollAccessRequest(t.ctx, ar.ID, ar.RequestToken)
	t.Check(err, "poll after approval")
	if st.Status != "approved" || st.Credentials == nil || st.Credentials.ClientSecret == nil || *st.Credentials.ClientSecret == "" {
		t.Fatalf("first poll after approval must carry the client secret, got %s", short(st))
	}
	secret := *st.Credentials.ClientSecret
	st2, err := anon.PollAccessRequest(t.ctx, ar.ID, ar.RequestToken)
	t.Check(err, "second poll")
	if st2.Credentials == nil || st2.Credentials.ClientSecret != nil || !st2.Credentials.Claimed {
		t.Errorf("second poll must return client_secret null and claimed true, got %s", short(st2.Credentials))
	}
	c := t.r.newClient(st.Credentials.ClientID, secret)
	if _, err := c.Grant(t.ctx); err != nil {
		t.Errorf("the delivered credentials do not work: %v", err)
	}
}

// ---- change feed

func testSnapshot(t *T) {
	f := t.readFeed(t.r.full, "", 500)
	want, err := t.r.fixtureJSON("expected/counts.json")
	t.Check(err, "expected/counts.json")
	counts := map[string]int{}
	rank := map[string]int{obx.ChangeBrand: 0, obx.ChangeProduct: 1, obx.ChangeVariant: 2, obx.ChangeAsset: 3}
	last := -1
	v, _ := specdata.DefaultValidator()
	schemaFor := map[string]string{obx.ChangeBrand: "Brand", obx.ChangeProduct: "Product", obx.ChangeVariant: "Variant", obx.ChangeAsset: "Asset"}
	for _, ch := range f.snapshot {
		counts[ch.Type]++
		if ch.Op != obx.OpUpsert {
			t.Errorf("snapshot entry %s %s has op %s", ch.Type, ch.ID, ch.Op)
		}
		if rk, ok := rank[ch.Type]; ok {
			if rk < last {
				t.Errorf("snapshot is not in type order (brands, products, variants, assets): %s after a later type", ch.Type)
			}
			last = rk
		}
		raw, _ := json.Marshal(ch)
		if err := v.Validate("Change", raw); err != nil {
			t.Errorf("snapshot entry %s is not a valid Change: %v", ch.ID, err)
		}
		if s := schemaFor[ch.Type]; s != "" {
			if err := v.Validate(s, ch.Data); err != nil {
				t.Errorf("data of %s %s is not a valid %s: %v", ch.Type, ch.ExternalIDValue(), s, err)
			}
		}
	}
	for _, k := range []string{"brands", "products", "variants", "assets"} {
		if t.r.online() && !t.r.fixturesPresent {
			t.partialf("the counts of expected/counts.json (needs the Kestrel test data)")
			break
		}
		exp := int(want.(map[string]any)[k].(float64))
		if got := counts[strings.TrimSuffix(k, "s")]; got != exp {
			t.Errorf("snapshot has %d %s, want %d", got, k, exp)
		}
	}
	sawLive := false
	for _, p := range f.pages {
		if p.Phase == obx.PhaseLive {
			sawLive = true
		}
	}
	if !sawLive {
		t.Errorf("the feed never switched to phase live")
	}
	for i, p := range f.pages {
		if p.Phase == obx.PhaseSnapshot && i == len(f.pages)-1 {
			t.Errorf("the last page is a snapshot page")
		}
		if p.Phase == obx.PhaseSnapshot && !p.HasMore {
			t.Errorf("a snapshot page has has_more false")
		}
	}
}

// brandProductCount counts the products of a brand (by its name in the brand
// column) in kestrel/products.csv.
func (t *T) brandProductCount(brand string) int {
	data, err := t.r.fixture("kestrel/products.csv")
	t.Check(err, "read kestrel/products.csv")
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	t.Check(err, "parse kestrel/products.csv")
	seen := map[string]bool{}
	for _, r := range rows[1:] {
		if len(r) > 1 && r[0] == brand {
			seen[r[1]] = true
		}
	}
	return len(seen)
}

// fixtureCount returns a count of expected/counts.json (brands, products,
// variants, assets).
func (t *T) fixtureCount(what string) int {
	want, err := t.r.fixtureJSON("expected/counts.json")
	t.Check(err, "expected/counts.json")
	n, _ := want.(map[string]any)[what].(float64)
	return int(n)
}

func key(ch obx.Change) string { return ch.Type + "/" + ch.Op + "/" + ch.ID }

func testPaging(t *T) {
	big := t.readFeed(t.r.full, "", 500)
	limit := 1
	if n := len(big.snapshot) + len(big.live); t.r.online() && n > 300 {
		limit = (n + 299) / 300
		t.partialf("paging with limit=1: the feed has %d entries, so limit=%d was compared with limit=500", n, limit)
	}
	small := t.readFeed(t.r.full, "", limit)
	join := func(f *feed) []string {
		var out []string
		seen := map[string]bool{}
		for _, ch := range append(append([]obx.Change{}, f.snapshot...), f.live...) {
			k := key(ch)
			if !seen[k] || ch.Op == obx.OpDelete {
				out = append(out, k)
			}
			seen[k] = true
		}
		return out
	}
	a, b := join(big), join(small)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("limit=1 and limit=500 give different entries (%d vs %d)", len(b), len(a))
	}
	for _, p := range small.pages {
		if limit == 1 && len(p.Changes) > 1 && !allSame(p.Changes) {
			t.Errorf("a page with limit=1 has %d entries", len(p.Changes))
			break
		}
	}
}

func allSame(chs []obx.Change) bool {
	for _, c := range chs {
		if key(c) != key(chs[0]) {
			return false
		}
	}
	return true
}

func testGarbageCursor(t *T) {
	for _, c := range []string{"garbage", "bGl2ZToxMDQ0", strings.Repeat("A", 60)} {
		res := t.get(t.r.full, "/changes?cursor="+url.QueryEscape(c))
		t.expectStatus(res, http.StatusBadRequest, obx.ErrCursorInvalid, "cursor "+c)
	}
}

func testRestricted(t *T) {
	if t.r.second == nil {
		t.Skip("needs an API key restricted to brand kestrel (--second-client-id) or admin credentials")
	}
	kestrel := t.r.brandIDs["kestrel"]
	f := t.readFeed(t.r.second, "", 500)
	var products int
	for _, ch := range f.snapshot {
		if ch.BrandID == nil || *ch.BrandID != kestrel {
			if ch.Type != obx.ChangeGrant {
				t.Errorf("restricted snapshot contains %s %s of brand %v", ch.Type, ch.ExternalIDValue(), ch.BrandID)
			}
		}
		if ch.Type == obx.ChangeProduct {
			products++
		}
	}
	if want := t.brandProductCount("Kestrel"); products != want {
		t.Errorf("restricted snapshot has %d products, want the %d of brand kestrel", products, want)
	}
	res := t.get(t.r.second, "/products/"+t.productID("KT-2001"))
	t.expectStatus(res, http.StatusNotFound, obx.ErrNotFound, "GET a kestrel-trail product with the restricted key")
	lookup, err := t.r.second.LookupGTINs(t.ctx, []string{"2000200101017"})
	t.Check(err, "lookup")
	if len(lookup.Results) != 1 || lookup.Results[0].Status != obx.GTINUnmatched || len(lookup.Products) != 0 {
		t.Errorf("a kestrel-trail EAN must be unmatched for the restricted key, got %s", short(lookup))
	}
	var n int
	t.Check(t.r.second.EachProduct(t.ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error {
		n++
		if p.BrandID != kestrel {
			t.Errorf("GET /products returned %s of another brand", p.ExternalID)
		}
		return nil
	}), "list products")
	if t.r.online() {
		t.partialf("the restricted export (it creates an export) and the live entries (they need a brand-side step)")
		return
	}
	if t.r.discovery != nil && t.r.discovery.Has(obx.CapExports) {
		e, err := t.r.second.CreateExport(t.ctx, obx.ExportRequest{Renditions: []string{obx.RenditionThumb}}, "")
		t.Check(err, "export")
		e, err = t.r.second.WaitExport(t.ctx, e.ID, 500*time.Millisecond)
		t.Check(err, "export")
		t.Check(t.r.second.ExportManifest(t.ctx, e, func(l *obx.ExportManifestLine) error {
			if l.Type == "product" || l.Type == "brand" {
				var x struct {
					ID      string `json:"id"`
					BrandID string `json:"brand_id"`
				}
				json.Unmarshal(l.Data, &x)
				if (l.Type == "brand" && x.ID != kestrel) || (l.Type == "product" && x.BrandID != kestrel) {
					t.Errorf("the restricted export contains a %s of another brand", l.Type)
				}
			}
			return nil
		}), "manifest")
	}
	if t.r.ing == nil && t.r.cfg.Operator == nil {
		t.Notef("live entries not checked (no upload key or operator)")
		return
	}
	cur2 := f.cursor
	cur1 := t.liveCursor(t.r.full)
	if t.r.ing != nil {
		_, err = t.r.ing.Patch(t.ctx, obx.KindProduct, "KT-2001", map[string]any{"season": "SS27"}, nil)
		t.Check(err, "change KT-2001")
		defer t.r.ing.Patch(t.ctx, obx.KindProduct, "KT-2001", map[string]any{"season": "SS26"}, nil)
	} else {
		t.opStep(productAction(ActUpdateProduct, "KT-2001", "Change the season of this product.", "field", "season", "value", "SS27"))
		defer t.r.operate(t.ctx, productAction(ActUpdateProduct, "KT-2001", "Change the season of this product back.", "field", "season", "value", "SS26"))
	}
	live1, _ := t.newEntries(t.r.full, cur1)
	live2, _ := t.newEntries(t.r.second, cur2)
	if len(live1) == 0 {
		t.Errorf("the unrestricted key saw no entry for a change of KT-2001")
	}
	for _, ch := range live2 {
		if ch.Type != obx.ChangeGrant && (ch.BrandID == nil || *ch.BrandID != kestrel) {
			t.Errorf("the restricted key saw live entry %s %s of another brand", ch.Type, ch.ExternalIDValue())
		}
	}
}

func testGrantResync(t *T) {
	t.needOperator("changing a grant")
	c := t.newThrowawayKey("OBX conformance: grant change", nil)
	cursor := t.liveCursor(c)
	kestrel := t.r.brandIDs["kestrel"]
	if t.r.admin != nil {
		_, err := t.r.admin.PatchRetailer(t.ctx, c.retailerID, map[string]any{"brand_ids": []string{kestrel}})
		t.Check(err, "change the grant")
	} else {
		t.opStep(Action{Name: ActSetGrant, Params: map[string]string{"client_id": c.id, "brands": "kestrel"},
			Hint: "Let the retailer of this API key see only brand kestrel."})
	}
	live, _ := t.newEntries(c, cursor)
	found := false
	for _, ch := range live {
		if ch.Type == obx.ChangeGrant {
			found = true
			if !ch.IsResync() {
				t.Errorf("grant entry without resync:true")
			}
			var g obx.Grant
			if err := ch.Decode(&g); err != nil || len(g.BrandIDs) != 1 || g.BrandIDs[0] != kestrel {
				t.Errorf("grant entry data does not show the new brands: %s", truncate(ch.Data))
			}
		}
	}
	if !found {
		t.Errorf("no grant entry after the grant changed (got %d entries)", len(live))
	}
	f := t.readFeed(c, "", 500)
	for _, ch := range f.snapshot {
		if ch.BrandID != nil && *ch.BrandID != kestrel {
			t.Errorf("after the grant change the snapshot still has %s %s", ch.Type, ch.ExternalIDValue())
			break
		}
	}
}

// ---- catalog

func testProductFixtures(t *T) {
	for _, ext := range []string{"KO-1001", "KO-1005"} {
		want, err := t.r.fixtureExpect("product-" + ext + ".json")
		t.Check(err, "fixture")
		res := t.get(t.r.full, "/products/"+t.productID(ext))
		if res.status != http.StatusOK {
			t.Errorf("GET %s: %d", ext, res.status)
			continue
		}
		t.validate("ProductDetail", res.body, ext)
		var got any
		json.Unmarshal(res.body, &got)
		for _, d := range matchExpected(got, want, ext, t.r.productExpectation(t.ctx)) {
			t.Errorf("%s", d)
		}
		etag := res.header.Get("ETag")
		if etag == "" {
			t.Errorf("GET %s has no ETag", ext)
			continue
		}
		tok, _ := t.r.full.Token(t.ctx)
		h := bearerHeader(tok)
		h.Set("If-None-Match", etag)
		again, err := t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathAPIBase+"/products/"+t.productID(ext), nil, h)
		t.Check(err, "conditional GET")
		if again.status != http.StatusNotModified {
			t.Errorf("If-None-Match with the ETag: want 304, got %d", again.status)
		}
	}
}

func testProductList(t *T) {
	var all []string
	t.Check(t.r.full.EachProduct(t.ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error { all = append(all, p.ID); return nil }), "limit 100")
	var paged []string
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 1000; i++ {
		page, err := t.r.full.Products(t.ctx, client.ProductsQuery{Limit: 3, Cursor: cursor})
		t.Check(err, "page")
		for _, p := range page.Items {
			if seen[p.ID] {
				t.Errorf("product %s appears twice while paging", p.ID)
			}
			seen[p.ID] = true
			paged = append(paged, p.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if strings.Join(all, ",") != strings.Join(paged, ",") {
		t.Errorf("paging with limit=3 gives %v, limit=100 gives %v", paged, all)
	}
	if t.r.online() && !t.r.fixturesPresent {
		t.partialf("the number of products (needs the Kestrel test data)")
		if len(all) < 2 {
			t.Notef("ids= filter not checked: fewer than two products")
			return
		}
		page, err := t.r.full.Products(t.ctx, client.ProductsQuery{IDs: []string{all[0], all[1], "prd_does-not-exist"}, Limit: 100})
		t.Check(err, "ids filter")
		if len(page.Items) != 2 {
			t.Errorf("ids= with two known and one unknown id returned %d products", len(page.Items))
		}
		return
	}
	if want := t.fixtureCount("products"); len(all) != want {
		t.Errorf("GET /products lists %d products, want %d", len(all), want)
	}
	ids := []string{t.productID("KO-1001"), t.productID("KT-2002"), "prd_does-not-exist"}
	page, err := t.r.full.Products(t.ctx, client.ProductsQuery{IDs: ids, Limit: 100})
	t.Check(err, "ids filter")
	if len(page.Items) != 2 {
		t.Errorf("ids= with two known and one unknown id returned %d products", len(page.Items))
	}
}

func testGTINLookup(t *T) {
	reqBody, err := t.r.fixture("expected/gtin-lookup.request.json")
	t.Check(err, "fixture")
	want, err := t.r.fixtureExpect("gtin-lookup.json")
	t.Check(err, "fixture")
	tok, _ := t.r.full.Token(t.ctx)
	res, err := t.r.do(t.ctx, http.MethodPost, t.r.cfg.BaseURL+obx.PathAPIBase+"/products:by-gtin", reqBody, mergeHeader(bearerHeader(tok), "Content-Type", "application/json"))
	t.Check(err, "POST by-gtin")
	if res.status != http.StatusOK {
		t.Fatalf("POST by-gtin: %d %s", res.status, truncate(res.body))
	}
	t.validate("GtinLookupResult", res.body, "the lookup result")
	if t.r.online() && !t.r.fixturesPresent {
		t.partialf("the answer against expected/gtin-lookup.json (needs the Kestrel test data); its shape was checked")
	} else {
		var got any
		json.Unmarshal(res.body, &got)
		for _, d := range matchExpected(got, want, "", t.r.productExpectation(t.ctx)) {
			t.Errorf("%s", d)
		}
	}
	many := make([]string, 2001)
	for i := range many {
		many[i] = fmt.Sprintf("2000100%06d", i)
	}
	body, _ := json.Marshal(map[string]any{"gtins": many})
	res, err = t.r.do(t.ctx, http.MethodPost, t.r.cfg.BaseURL+obx.PathAPIBase+"/products:by-gtin", body, mergeHeader(bearerHeader(tok), "Content-Type", "application/json"))
	t.Check(err, "POST by-gtin")
	t.expectStatus(res, http.StatusBadRequest, obx.ErrLimitExceeded, "2001 GTINs")
	g := t.get(t.r.full, "/products:by-gtin?gtin=2000100101025,12345")
	if g.status != http.StatusOK {
		t.Errorf("GET by-gtin: %d", g.status)
	}
}

func testInvisible404(t *T) {
	for _, p := range []string{"/products/prd_does-not-exist", "/assets/ast_does-not-exist", "/brands/brd_does-not-exist", "/assets/ast_does-not-exist/renditions/web"} {
		t.expectStatus(t.get(t.r.full, p), http.StatusNotFound, obx.ErrNotFound, "GET "+p)
	}
	if t.r.second != nil && (!t.r.online() || t.r.fixturesPresent) {
		t.expectStatus(t.get(t.r.second, "/products/"+t.productID("KT-2001")), http.StatusNotFound, obx.ErrNotFound, "product outside the grant")
		t.expectStatus(t.get(t.r.second, "/assets/"+t.assetID("KT-2001_BLK_1")), http.StatusNotFound, obx.ErrNotFound, "image outside the grant")
		t.expectStatus(t.get(t.r.second, "/brands/"+t.r.brandIDs["kestrel-trail"]), http.StatusNotFound, obx.ErrNotFound, "brand outside the grant")
	}
	switch {
	case t.r.ing != nil:
		_, err := t.r.ing.Unpublish(t.ctx, obx.KindProduct, "KO-1006")
		t.Check(err, "unpublish KO-1006")
		defer t.r.ing.Publish(t.ctx, obx.KindProduct, "KO-1006")
	case t.r.cfg.Operator != nil:
		t.opStep(productAction(ActUnpublishProduct, "KO-1006", "Unpublish this product (retailers must no longer see it)."))
		defer t.r.operate(t.ctx, productAction(ActPublishProduct, "KO-1006", "Publish this product again."))
	case t.r.online():
		t.partialf("unpublished products answer 404 (needs a brand-side step)")
		return
	default:
		t.Notef("unpublishing not checked (no upload key or operator)")
		return
	}
	t.expectStatus(t.get(t.r.full, "/products/"+t.productID("KO-1006")), http.StatusNotFound, obx.ErrNotFound, "unpublished product")
	t.expectStatus(t.get(t.r.full, "/assets/"+t.assetID("KO-1006_NVY_1")), http.StatusNotFound, obx.ErrNotFound, "image of an unpublished product")
}

// ---- media

func testAssetFixtures(t *T) {
	cases := []struct {
		file, ext string
		c         *apiKey
	}{
		{"asset-ai-generated.json", "KO-1003_RST_3", t.r.full},
		{"asset-embargoed.json", "KO-1002_BLK_2", t.r.full},
		{"asset-large-original.json", "KO-1002_OLV_1", t.r.full},
		{"asset-without-original-scope.json", "KO-1001_NVY_1", t.r.noOrig},
	}
	for _, c := range cases {
		if c.c == nil {
			t.Notef("%s skipped: no API key without originals", c.file)
			continue
		}
		want, err := t.r.fixtureExpect(c.file)
		t.Check(err, c.file)
		res := t.get(c.c, "/assets/"+t.assetID(c.ext))
		if res.status != http.StatusOK {
			t.Errorf("GET asset %s: %d", c.ext, res.status)
			continue
		}
		t.validate("Asset", res.body, c.ext)
		var got any
		json.Unmarshal(res.body, &got)
		for _, d := range matchExpected(got, want, c.ext, t.r.productExpectation(t.ctx)) {
			t.Errorf("%s", d)
		}
	}
}

// onlineAssets returns up to n images of whatever catalog the key sees, for
// the media checks of an online run without the Kestrel test data.
func (t *T) onlineAssets(n int) []*obx.Asset {
	page, err := t.r.full.Products(t.ctx, client.ProductsQuery{Limit: 10})
	t.Check(err, "list products")
	var out []*obx.Asset
	for _, p := range page.Items {
		for _, ref := range p.Assets {
			if len(out) == n {
				return out
			}
			a, err := t.r.full.Asset(t.ctx, ref.ID)
			t.Check(err, "asset "+ref.ID)
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		t.Skip("not checked online: the API key sees no product with images")
	}
	return out
}

func testRenditionHTTP(t *T) {
	type target struct {
		label string
		a     *obx.Asset
	}
	var targets []target
	names := obx.RenditionNames
	if t.r.online() && !t.r.fixturesPresent {
		t.partialf("originals are not downloaded online without the Kestrel test data; web, square and thumb of %s were checked", "the first images of the catalog")
		names = obx.DerivedRenditionNames
		for _, a := range t.onlineAssets(2) {
			targets = append(targets, target{a.ID, a})
		}
	} else {
		for _, ext := range []string{"KO-1001_NVY_1", "KO-1002_OLV_1", "KO-1003_RST_3"} {
			a, err := t.r.full.Asset(t.ctx, t.assetID(ext))
			t.Check(err, "asset "+ext)
			targets = append(targets, target{ext, a})
		}
	}
	checked := 0
	for _, tg := range targets {
		ext, a := tg.label, tg.a
		for _, name := range names {
			rd := a.Renditions.Get(name)
			if rd == nil {
				t.Errorf("%s has no %s rendition", ext, name)
				continue
			}
			checked++
			var buf bytes.Buffer
			if _, err := t.r.full.Download(t.ctx, rd, &buf); err != nil {
				t.Errorf("%s %s: %v", ext, name, err)
				continue
			}
			tok, _ := t.r.full.Token(t.ctx)
			res, err := t.r.do(t.ctx, http.MethodGet, rd.URL, nil, bearerHeader(tok))
			t.Check(err, "GET rendition")
			if got := res.header.Get("ETag"); got != `"`+rd.SHA256+`"` {
				t.Errorf("%s %s: ETag %q, want the quoted sha256", ext, name, got)
			}
			if got := res.header.Get(obx.HeaderContentSHA256); got != rd.SHA256 {
				t.Errorf("%s %s: OBX-Content-SHA256 %q", ext, name, got)
			}
			if res.header.Get("Accept-Ranges") != "bytes" {
				t.Errorf("%s %s: Accept-Ranges is not bytes", ext, name)
			}
			if cd := res.header.Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
				t.Errorf("%s %s: Content-Disposition %q, want inline", ext, name, cd)
			} else if name != obx.RenditionOriginal && !strings.Contains(cd, "_"+name+".jpg") {
				t.Errorf("%s %s: Content-Disposition %q does not end in _%s.jpg", ext, name, cd, name)
			}
			h := bearerHeader(tok)
			h.Set("Range", "bytes=0-99")
			part, err := t.r.do(t.ctx, http.MethodGet, rd.URL, nil, h)
			t.Check(err, "range")
			if part.status != http.StatusPartialContent || len(part.body) != 100 || !bytes.Equal(part.body, buf.Bytes()[:100]) {
				t.Errorf("%s %s: Range bytes=0-99 gave %d with %d bytes", ext, name, part.status, len(part.body))
			}
			h = bearerHeader(tok)
			h.Set("If-None-Match", `"`+rd.SHA256+`"`)
			nm, err := t.r.do(t.ctx, http.MethodGet, rd.URL, nil, h)
			t.Check(err, "conditional")
			if nm.status != http.StatusNotModified {
				t.Errorf("%s %s: If-None-Match gave %d, want 304", ext, name, nm.status)
			}
			anon, err := t.r.do(t.ctx, http.MethodGet, rd.URL, nil, nil)
			t.Check(err, "anonymous")
			if rd.URLExpiresAt != nil {
				if anon.status != http.StatusOK || sha(anon.body) != rd.SHA256 {
					t.Errorf("%s %s: the signed URL does not work without Authorization (%d)", ext, name, anon.status)
				}
				if rd.URLExpiresAt.After(time.Now().Add(25 * time.Hour)) {
					t.Errorf("%s %s: signed URL lives longer than 24 hours", ext, name)
				}
			} else if anon.status == http.StatusOK {
				t.Errorf("%s %s: URL without expiry works without Authorization (permanent public URL)", ext, name)
			}
		}
	}
	if checked == 0 {
		t.Errorf("no renditions checked")
	}
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func testRenditionRules(t *T) {
	if t.r.online() && !t.r.fixturesPresent {
		t.partialf("byte-identical originals (needs the Kestrel test data); the rules for web, square and thumb were checked on the first images of the catalog")
		for _, a := range t.onlineAssets(2) {
			t.checkDerived(a.ID, a, a.Width, a.Height)
		}
		return
	}
	var info map[string]struct {
		SHA256 string `json:"sha256"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	raw, err := t.r.fixture("kestrel/images.json")
	t.Check(err, "images.json")
	json.Unmarshal(raw, &info)
	byExt := map[string]string{}
	for file := range info {
		n := strings.TrimSuffix(file, ".png")
		parts := strings.Split(n, "_")
		byExt[strings.Join(parts[:3], "_")] = file
	}
	for ext, file := range byExt {
		id, ok := t.r.assetIDs[ext]
		if !ok {
			t.Errorf("image %s is not visible", ext)
			continue
		}
		a, err := t.r.full.Asset(t.ctx, id)
		t.Check(err, "asset")
		orig := a.Renditions.Original
		if orig == nil {
			t.Errorf("%s: no original for an API key with media:original", ext)
			continue
		}
		var ob bytes.Buffer
		if _, err := t.r.full.Download(t.ctx, orig, &ob); err != nil {
			t.Errorf("%s original: %v", ext, err)
			continue
		}
		fixture, _ := t.r.fixture("kestrel/images/" + file)
		if !bytes.Equal(ob.Bytes(), fixture) {
			t.Errorf("%s: the original is not byte-identical to %s", ext, file)
		}
		t.checkDerived(ext, a, info[file].Width, info[file].Height)
	}
}

// ---- optional capabilities

func testExport(t *T) {
	if t.r.discovery == nil || !t.r.discovery.Has(obx.CapExports) {
		t.Skip("capability exports not offered")
	}
	kestrel := t.r.brandIDs["kestrel"]
	e, err := t.r.full.CreateExport(t.ctx, obx.ExportRequest{Filter: &obx.ExportFilter{BrandIDs: []string{kestrel}}, Renditions: []string{obx.RenditionWeb}}, "conformance-"+randomSuffix())
	t.Check(err, "POST /exports")
	e, err = t.r.full.WaitExport(t.ctx, e.ID, 500*time.Millisecond)
	t.Check(err, "export")
	raw, _ := json.Marshal(e)
	t.validate("Export", raw, "the export")
	type fileLine struct {
		path, sha string
		size      int64
		part      int
	}
	var files []fileLine
	lines, products := 0, 0
	var header *obx.ExportManifestLine
	v, _ := specdata.DefaultValidator()
	t.Check(t.r.full.ExportManifest(t.ctx, e, func(l *obx.ExportManifestLine) error {
		lines++
		b, _ := json.Marshal(l)
		if err := v.Validate("ExportManifestLine", b); err != nil {
			t.Errorf("manifest line %d is invalid: %v", lines, err)
		}
		switch l.Type {
		case "header":
			if lines != 1 {
				t.Errorf("header is line %d, want 1", lines)
			}
			cp := *l
			header = &cp
		case "product":
			products++
		case "file":
			files = append(files, fileLine{l.Path, l.SHA256, l.Bytes, l.Part})
		}
		return nil
	}), "manifest")
	if header == nil {
		t.Fatalf("the manifest has no header line")
	}
	if want := t.brandProductCount("Kestrel"); header.Counts == nil || header.Counts.Products != products || products != want {
		t.Errorf("header counts %+v, product lines %d, want %d kestrel products", header.Counts, products, want)
	}
	entries := map[string][]byte{}
	for _, p := range e.Parts {
		var buf bytes.Buffer
		if _, err := t.r.full.DownloadExportPart(t.ctx, p, &buf); err != nil {
			t.Errorf("part %d: %v", p.Number, err)
			continue
		}
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Errorf("part %d is not a zip: %v", p.Number, err)
			continue
		}
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "..") || strings.Contains(f.Name, "\\") {
				t.Errorf("unsafe zip entry %q", f.Name)
			}
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			entries[fmt.Sprintf("%d:%s", p.Number, f.Name)] = b
		}
	}
	for _, f := range files {
		b, ok := entries[fmt.Sprintf("%d:%s", f.part, f.path)]
		if !ok {
			t.Errorf("file line %s is not in part %d", f.path, f.part)
			continue
		}
		if sha(b) != f.sha || int64(len(b)) != f.size {
			t.Errorf("zip entry %s does not match its file line", f.path)
		}
		if parts := strings.Split(f.path, "/"); len(parts) != 3 || parts[1] != obx.RenditionWeb {
			t.Errorf("zip entry %q is not <style_number>/<rendition>/<filename>", f.path)
		}
	}
	if len(files) == 0 {
		t.Errorf("the export has no files")
	}
	res := t.get(t.r.full, "/changes?cursor="+url.QueryEscape(header.ChangesCursor))
	if res.status != http.StatusOK {
		t.Errorf("the manifest's changes_cursor does not work: %d %s", res.status, truncate(res.body))
	}
}

func testWebhooks(t *T) {
	if t.r.discovery == nil || !t.r.discovery.Has(obx.CapWebhooks) {
		t.Skip("capability webhooks not offered")
	}
	type delivery struct {
		header http.Header
		body   []byte
		at     time.Time
	}
	var mu sync.Mutex
	var got []delivery
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	t.Check(err, "listen")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, delivery{r.Header.Clone(), b, time.Now()})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	c := t.r.full
	sub, err := c.CreateWebhook(t.ctx, obx.WebhookInput{URL: srv.URL + "/hook", Events: []string{obx.EventChangesAvailable}})
	strict := obx.IsCode(err, obx.ErrUnsafeURL)
	allowed := !strict
	t.r.privateURLsAllowed = &allowed
	if strict {
		t.Notef("the node refuses local webhook targets (as it should in production); run it with OBX_ALLOW_PRIVATE_URLS=true to test delivery")
	} else {
		t.Check(err, "subscribe")
		if sub.Secret == nil || !strings.HasPrefix(*sub.Secret, obx.PrefixWebhookSecret) {
			t.Errorf("the subscription answer must carry the secret (whsec_…) once")
		}
		defer c.DeleteWebhook(t.ctx, sub.ID)
		res, err := c.TestWebhook(t.ctx, sub.ID)
		t.Check(err, "test delivery")
		if !res.Delivered {
			t.Errorf("webhook.test was not delivered (%s)", short(res))
		}
		mu.Lock()
		deliveries := append([]delivery(nil), got...)
		mu.Unlock()
		var found bool
		for _, d := range deliveries {
			var ev obx.WebhookEvent
			json.Unmarshal(d.body, &ev)
			if ev.Type != obx.EventWebhookTest {
				continue
			}
			found = true
			if err := webhook.Verify(d.header.Get(obx.HeaderWebhookSignature), d.header.Get(obx.HeaderWebhookTimestamp), d.body, d.at, 0, obx.Str(sub.Secret)); err != nil {
				t.Errorf("webhook.test signature: %v", err)
			}
			if d.header.Get(obx.HeaderWebhookID) != ev.ID {
				t.Errorf("OBX-Webhook-Id %q does not match the event id %q", d.header.Get(obx.HeaderWebhookID), ev.ID)
			}
		}
		if !found {
			t.Errorf("the receiver got no webhook.test event")
		}
		list, err := c.Webhooks(t.ctx)
		t.Check(err, "list")
		for _, s := range list {
			if s.ID == sub.ID && s.Secret != nil {
				t.Errorf("listing webhooks returns the secret again")
			}
		}
	}
	for _, bad := range []string{"https://169.254.169.254/latest/meta-data", "https://[fe80::1]/hook"} {
		_, err := c.CreateWebhook(t.ctx, obx.WebhookInput{URL: bad, Events: []string{obx.EventChangesAvailable}})
		if !obx.IsCode(err, obx.ErrUnsafeURL) {
			t.Errorf("%s: want 422 unsafe_url, got %v", bad, err)
		}
	}
	if strict {
		for _, bad := range []string{"https://127.0.0.1/hook", "https://10.0.0.1/hook", "http://93.184.215.14/hook"} {
			_, err := c.CreateWebhook(t.ctx, obx.WebhookInput{URL: bad, Events: []string{obx.EventChangesAvailable}})
			if !obx.IsCode(err, obx.ErrUnsafeURL) {
				t.Errorf("%s: want 422 unsafe_url, got %v", bad, err)
			}
		}
	}
}

func testUsage(t *T) {
	if t.r.discovery == nil || !t.r.discovery.Has(obx.CapUsage) {
		t.Skip("capability usage not offered")
	}
	var before int
	if t.r.admin != nil {
		st, err := t.r.admin.Stats(t.ctx, 30)
		t.Check(err, "stats")
		before = st.Totals.UsageEvents
	}
	now := time.Now().UTC().Truncate(time.Second)
	n, err := t.r.full.ReportUsage(t.ctx, []obx.UsageEvent{
		{AssetID: t.assetID("KO-1001_NVY_1"), Rendition: obx.Ptr("web"), Event: obx.UsagePublished, Channel: obx.Ptr("webshop"), At: now},
		{AssetID: t.assetID("KO-1001_NVY_2"), Rendition: obx.Ptr("web"), Event: obx.UsageSynced, At: now},
		{AssetID: "ast_unknown-to-this-node", Event: obx.UsageSynced, At: now},
	})
	t.Check(err, "POST /usage")
	if n != 2 {
		t.Errorf("accepted %d events, want 2 (unknown asset ids are ignored)", n)
	}
	if t.r.admin != nil {
		st, err := t.r.admin.Stats(t.ctx, 30)
		t.Check(err, "stats")
		if st.Totals.UsageEvents < before+2 {
			t.Errorf("admin stats usage_events went from %d to %d, want +2", before, st.Totals.UsageEvents)
		}
	}
}

func testRateLimit(t *T) {
	res := t.get(t.r.full, "/changes?limit=1")
	for _, h := range []string{"RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset"} {
		if res.header.Get(h) == "" {
			t.Errorf("GET /changes has no %s header", h)
		}
	}
	if !t.r.cfg.RateLimitTest {
		t.Notef("429 not provoked (use --rate-limit-test against a node with a low limit)")
		return
	}
	tok, err := t.r.full.Token(t.ctx)
	t.Check(err, "token")
	ctx := withoutRateRetry(t.ctx)
	for i := 0; i < 5000; i++ {
		res, err := t.r.do(ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathAPIBase+"/grant", nil, bearerHeader(tok))
		if err != nil {
			t.Errorf("GET /grant: %v", err)
			return
		}
		if res.status == http.StatusTooManyRequests {
			if res.header.Get("Retry-After") == "" {
				t.Errorf("429 without Retry-After")
			}
			if res.code() != obx.ErrRateLimited {
				t.Errorf("429 with obx_error %q", res.code())
			}
			return
		}
	}
	t.Errorf("no 429 after 5000 requests")
}

func testProblems(t *T) {
	base := t.r.cfg.BaseURL + obx.PathAPIBase
	var auth http.Header
	if t.r.full != nil {
		tok, _ := t.r.full.Token(t.ctx)
		auth = bearerHeader(tok)
	} else {
		t.partialf("error answers behind a valid token (no API key given); the answers without a token were checked")
	}
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/products/prd_nope"},
		{http.MethodDelete, "/changes"},
		{http.MethodGet, "/changes?cursor=nope"},
		{http.MethodGet, "/nope"},
	} {
		res, err := t.r.do(t.ctx, probe.method, base+probe.path, nil, auth)
		t.Check(err, probe.path)
		if res.status < 400 {
			t.Errorf("%s %s answered %d", probe.method, probe.path, res.status)
		}
	}
	t.r.mu.Lock()
	v := append([]string(nil), t.r.violations...)
	t.r.mu.Unlock()
	for _, s := range v {
		t.Errorf("%s", s)
	}
}

// checkDerived checks the web, square and thumb renditions of a against the
// rules (JPEG, size limits, never upscaled from origW x origH).
func (t *T) checkDerived(ext string, a *obx.Asset, origW, origH int) {
	for _, name := range obx.DerivedRenditionNames {
		rd := a.Renditions.Get(name)
		var buf bytes.Buffer
		if _, err := t.r.full.Download(t.ctx, rd, &buf); err != nil {
			t.Errorf("%s %s: %v", ext, name, err)
			continue
		}
		if renditions.DetectContentType(buf.Bytes()) != "image/jpeg" || rd.ContentType != "image/jpeg" {
			t.Errorf("%s %s is not a JPEG", ext, name)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Errorf("%s %s cannot be decoded: %v", ext, name, err)
			continue
		}
		if cfg.Width != rd.Width || cfg.Height != rd.Height {
			t.Errorf("%s %s: file is %dx%d, the asset says %dx%d", ext, name, cfg.Width, cfg.Height, rd.Width, rd.Height)
		}
		long := max(cfg.Width, cfg.Height)
		switch name {
		case obx.RenditionWeb:
			if long > 2000 || cfg.Width > origW || cfg.Height > origH {
				t.Errorf("%s web is %dx%d (max 2000, never upscaled from %dx%d)", ext, cfg.Width, cfg.Height, origW, origH)
			}
		case obx.RenditionThumb:
			if long > 600 || cfg.Width > origW {
				t.Errorf("%s thumb is %dx%d (max 600)", ext, cfg.Width, cfg.Height)
			}
		case obx.RenditionSquare:
			web := a.Renditions.Web
			if cfg.Width != cfg.Height || cfg.Width != max(web.Width, web.Height) {
				t.Errorf("%s square is %dx%d, want a square of the web image's longest side (%d)", ext, cfg.Width, cfg.Height, max(web.Width, web.Height))
			}
		}
	}
}
