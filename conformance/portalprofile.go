package conformance

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
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

func init() {
	p := ProfilePortal
	register(p, "A-SETUP-1", "setup needs the setup code; setup_required before, setup_completed after", false, testSetup)
	register(p, "A-SESS-1", "login/logout, invalid_credentials, csrf_failed, cookie flags", false, testSessions)
	register(p, "A-INV-1", "an invite link works once, then invite_invalid", false, testInvites)
	register(p, "A-RET-1", "approve with API → credentials once; reject with reason → reason shown", false, testApproveReject)
	register(p, "A-RET-2", "blocking a retailer ends sessions and API access", false, testBlockRetailer)
	register(p, "A-KEY-1", "API keys and upload keys: secret once, rotate, revoke", false, testKeys)
	register(p, "A-CAT-1", "admin catalog writes behave like ingest", false, testAdminCatalog)
	register(p, "A-SRC-1", "scheduled import URLs are SSRF-checked; credentials are never returned", false, testImportSources)
	register(p, "A-PORT-1", "retailer portal: list, facets, detail, EAN paste, selection, zips, CSV, download job", false, testPortal)
	register(p, "A-PORT-2", "restricted retailer never sees other brands; originals hidden when not allowed", false, testPortalRestricted)
	register(p, "A-STAT-1", "retailer downloads appear in stats; admin previews do not", false, testStats)
}

// ---- helpers

// portalUser creates a retailer and logs in one of its users through an
// invitation. It returns the logged-in client and the retailer.
func (t *T) portalUser(name string, in nodeadmin.RetailerInput) (*nodeadmin.Client, *nodeadmin.Retailer) {
	in.Name = name
	rt, err := t.r.admin.CreateRetailer(t.ctx, in)
	t.Check(err, "create retailer")
	inv, err := t.r.admin.Invite(t.ctx, nodeadmin.InviteInput{Email: "buyer+" + randomSuffix() + "@portal.example", Name: "Portal Buyer", Role: nodeadmin.RoleRetailer, RetailerID: rt.ID, Language: "nl"})
	t.Check(err, "invite")
	c := t.r.newAdmin()
	_, err = c.AcceptInvite(t.ctx, nodeadmin.InviteToken(inv.InviteURL), "Portal Buyer", "portal-buyer-password", "nl")
	t.Check(err, "accept invite")
	return c, rt
}

func (t *T) call(c *nodeadmin.Client, method, path string, in, out any) {
	t.Check(c.Call(t.ctx, method, obx.PathAdminAPI+path, in, out), method+" "+path)
}

func (t *T) download(c *nodeadmin.Client, path string) (int, http.Header, []byte) {
	status, h, b, err := c.Get(t.ctx, obx.PathAdminAPI+path)
	t.Check(err, "GET "+path)
	return status, h, b
}

func zipNames(t *T, data []byte) []string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

// ---- tests

func testSetup(t *T) {
	t.needAdmin("setup checks")
	n := t.r.setup
	if n != nil && n.ran {
		for _, p := range n.problems {
			t.Errorf("%s", p)
		}
		return
	}
	_, err := t.r.newAdmin().Setup(t.ctx, nodeadmin.SetupInput{SetupCode: "whatever-code", Company: nodeadmin.SetupCompany{Name: "x", DefaultLanguage: "en"}, Admin: nodeadmin.SetupAdmin{Name: "x", Email: "x@x.example", Password: "xxxxxxxxxxxx"}})
	if !obx.IsCode(err, obx.ErrSetupCompleted) {
		t.Errorf("setup on a set-up node: want 409 setup_completed, got %v", err)
	}
	t.Notef("the node was already set up, so the checks before setup were not run (use a fresh node with --setup-code)")
}

// rawJSON sends a JSON request with a jar client.
func (t *T) rawJSON(hc *http.Client, method, path string, body any, csrf string) *response {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(t.ctx, method, t.r.cfg.AdminURL+obx.PathAdminAPI+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set(obx.HeaderCSRF, csrf)
	}
	resp, err := hc.Do(req)
	t.Check(err, method+" "+path)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &response{status: resp.StatusCode, header: resp.Header, body: b}
}

func cookieFlags(h http.Header, name string) (string, bool) {
	for _, sc := range h.Values("Set-Cookie") {
		if strings.HasPrefix(sc, name+"=") {
			return sc, true
		}
	}
	return "", false
}

func testSessions(t *T) {
	t.needAdmin("session checks")
	hc := t.r.jarClient()
	res := t.rawJSON(hc, http.MethodGet, "/session", nil, "")
	var s nodeadmin.Session
	res.decode(&s)
	if s.CSRFToken == "" || s.Authenticated {
		t.Fatalf("anonymous session: %s", truncate(res.body))
	}
	login := map[string]string{"email": t.r.cfg.AdminEmail, "password": t.r.cfg.AdminPassword}
	t.expectStatus(t.rawJSON(hc, http.MethodPost, "/session/login", login, ""), http.StatusForbidden, obx.ErrCSRFFailed, "login without X-CSRF-Token")
	t.expectStatus(t.rawJSON(hc, http.MethodPost, "/session/login", map[string]string{"email": t.r.cfg.AdminEmail, "password": "wrong-password-123"}, s.CSRFToken), http.StatusUnauthorized, obx.ErrInvalidCredentials, "wrong password")
	t.expectStatus(t.rawJSON(hc, http.MethodPost, "/session/login", map[string]string{"email": "nobody@nowhere.example", "password": "wrong-password-123"}, s.CSRFToken), http.StatusUnauthorized, obx.ErrInvalidCredentials, "unknown e-mail")
	res = t.rawJSON(hc, http.MethodPost, "/session/login", login, s.CSRFToken)
	if res.status != http.StatusOK {
		t.Fatalf("login: %d %s", res.status, truncate(res.body))
	}
	var in nodeadmin.Session
	res.decode(&in)
	if !in.Authenticated || in.User == nil || in.User.Role != nodeadmin.RoleAdmin {
		t.Errorf("login answer: %s", truncate(res.body))
	}
	if in.CSRFToken == s.CSRFToken {
		t.Errorf("login did not rotate the CSRF token")
	}
	sc, ok := cookieFlags(res.header, "obx_session")
	if !ok {
		t.Errorf("login set no obx_session cookie")
	} else {
		lc := strings.ToLower(sc)
		if !strings.Contains(lc, "httponly") || !strings.Contains(lc, "samesite=lax") || !strings.Contains(lc, "path=/") {
			t.Errorf("obx_session cookie flags: %s", sc)
		}
		if strings.HasPrefix(t.r.cfg.AdminURL, "https://") && !strings.Contains(lc, "secure") {
			t.Errorf("obx_session over https must be Secure: %s", sc)
		}
	}
	t.expectStatus(t.rawJSON(hc, http.MethodPatch, "/settings", map[string]any{}, s.CSRFToken), http.StatusForbidden, obx.ErrCSRFFailed, "PATCH with the pre-login CSRF token")
	t.expectStatus(t.rawJSON(hc, http.MethodPatch, "/settings", map[string]any{}, ""), http.StatusForbidden, obx.ErrCSRFFailed, "PATCH without X-CSRF-Token")
	if r := t.rawJSON(hc, http.MethodGet, "/settings", nil, ""); r.status != http.StatusOK {
		t.Errorf("GET /settings after login: %d", r.status)
	}
	if r := t.rawJSON(hc, http.MethodPost, "/session/logout", nil, in.CSRFToken); r.status != http.StatusNoContent && r.status != http.StatusOK {
		t.Errorf("logout: %d", r.status)
	}
	res = t.rawJSON(hc, http.MethodGet, "/session", nil, "")
	res.decode(&s)
	if s.Authenticated {
		t.Errorf("still authenticated after logout")
	}
	t.expectStatus(t.rawJSON(hc, http.MethodGet, "/settings", nil, ""), http.StatusUnauthorized, obx.ErrUnauthorized, "GET /settings after logout")
	req, _ := http.NewRequestWithContext(t.ctx, http.MethodGet, t.r.cfg.AdminURL+obx.PathAdminAPI+"/settings", nil)
	req.Header.Set("Authorization", "Bearer "+t.r.cfg.IngestToken)
	resp, err := t.r.hc.Do(req)
	t.Check(err, "bearer on admin")
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("the admin API accepted a Bearer token")
	}
}

func testInvites(t *T) {
	t.needAdmin("invites")
	rt, err := t.r.admin.CreateRetailer(t.ctx, nodeadmin.RetailerInput{Name: "OBX conformance: invites"})
	t.Check(err, "create retailer")
	email := "invitee+" + randomSuffix() + "@portal.example"
	inv, err := t.r.admin.Invite(t.ctx, nodeadmin.InviteInput{Email: email, Role: nodeadmin.RoleRetailer, RetailerID: rt.ID, Language: "de"})
	t.Check(err, "invite")
	if !strings.Contains(inv.InviteURL, "/invite/") || inv.User.Status != "invited" {
		t.Errorf("invite answer: %s", short(inv))
	}
	tok := nodeadmin.InviteToken(inv.InviteURL)
	anon := t.r.newAdmin()
	var info nodeadmin.InviteInfo
	t.call(anon, http.MethodGet, "/invites/"+url.PathEscape(tok), nil, &info)
	if info.Email != email || info.Role != nodeadmin.RoleRetailer {
		t.Errorf("invite info: %s", short(info))
	}
	s, err := anon.AcceptInvite(t.ctx, tok, "Invitee", "invitee-password-1", "de")
	t.Check(err, "accept")
	if !s.Authenticated || s.User == nil || s.User.Role != nodeadmin.RoleRetailer || s.Retailer == nil || s.Retailer.ID != rt.ID {
		t.Errorf("session after accepting: %s", short(s))
	}
	_, err = t.r.newAdmin().AcceptInvite(t.ctx, tok, "Again", "invitee-password-2", "")
	if !obx.IsCode(err, obx.ErrInviteInvalid) {
		t.Errorf("accepting twice: want 400 invite_invalid, got %v", err)
	}
	err = t.r.newAdmin().Call(t.ctx, http.MethodGet, obx.PathAdminAPI+"/invites/"+url.PathEscape(tok), nil, &info)
	if !obx.IsCode(err, obx.ErrInviteInvalid) {
		t.Errorf("used invite: want 400 invite_invalid, got %v", err)
	}
	if _, err := t.r.newAdmin().Login(t.ctx, email, "invitee-password-1"); err != nil {
		t.Errorf("login with the chosen password: %v", err)
	}
}

func testApproveReject(t *T) {
	t.needAdmin("access requests")
	anon := t.r.newClient("", "")
	mk := func(name string) *obx.AccessRequest {
		ar, err := anon.RequestAccess(t.ctx, obx.AccessRequestInput{Retailer: obx.AccessRequestRetailer{Name: name},
			Contact: obx.AccessRequestContact{Name: "Sam Example", Email: "sam+" + randomSuffix() + "@shop.example"}, Access: []string{"portal", "api"}, RequestedBrandIDs: []string{}})
		t.Check(err, "access request")
		return ar
	}
	a := mk("Approved Shop " + randomSuffix())
	reqs, err := t.r.admin.AccessRequests(t.ctx, "pending")
	t.Check(err, "list requests")
	found := false
	for _, r := range reqs {
		if r.ID == a.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the new request is not in the pending list")
	}
	res, err := t.r.admin.Approve(t.ctx, a.ID, nodeadmin.ApproveInput{})
	t.Check(err, "approve")
	if res.AccessRequest.Status != "approved" || res.APIClient == nil || res.Invite == nil {
		t.Errorf("approve answer should create an API key and a portal invite for portal+api: %s", short(res))
	}
	if strings.Contains(string(mustJSON(res)), "obxs_") {
		t.Errorf("the approve answer shows the client secret to the brand")
	}
	st, err := anon.PollAccessRequest(t.ctx, a.ID, a.RequestToken)
	t.Check(err, "poll")
	if st.Credentials == nil || st.Credentials.ClientSecret == nil {
		t.Errorf("first poll: no client secret")
	}
	st, err = anon.PollAccessRequest(t.ctx, a.ID, a.RequestToken)
	t.Check(err, "poll again")
	if st.Credentials == nil || st.Credentials.ClientSecret != nil || !st.Credentials.Claimed {
		t.Errorf("second poll must not repeat the secret")
	}
	if st.Portal == nil || !st.Portal.Invited {
		t.Errorf("portal access must be delivered as an invitation: %s", short(st.Portal))
	}
	_, err = t.r.admin.Approve(t.ctx, a.ID, nodeadmin.ApproveInput{})
	if !obx.IsCode(err, obx.ErrConflict) {
		t.Errorf("approving twice: want 409 conflict, got %v", err)
	}
	b := mk("Rejected Shop " + randomSuffix())
	reason := "We only work with shops in the Benelux."
	_, err = t.r.admin.Reject(t.ctx, b.ID, reason)
	t.Check(err, "reject")
	st, err = anon.PollAccessRequest(t.ctx, b.ID, b.RequestToken)
	t.Check(err, "poll rejected")
	if st.Status != "rejected" || obx.Str(st.Reason) != reason || st.Credentials != nil {
		t.Errorf("rejected request: %s", short(st))
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func testBlockRetailer(t *T) {
	t.needAdmin("blocking")
	user, rt := t.portalUser("OBX conformance: to be blocked", nodeadmin.RetailerInput{})
	c, err := t.r.admin.CreateAPIClient(t.ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: "blocked software", Scopes: []string{obx.ScopeCatalogRead}})
	t.Check(err, "API key")
	key := t.r.newClient(c.Client.ClientID, c.ClientSecret)
	tok, err := key.Token(t.ctx)
	t.Check(err, "token")
	var v nodeadmin.PortalViewer
	t.call(user, http.MethodGet, "/portal/viewer", nil, &v)
	_, err = t.r.admin.PatchRetailer(t.ctx, rt.ID, map[string]any{"status": "blocked"})
	t.Check(err, "block")
	err = user.Call(t.ctx, http.MethodGet, obx.PathAdminAPI+"/portal/viewer", nil, &v)
	if p, ok := err.(*obx.Problem); !ok || p.Status != http.StatusUnauthorized {
		t.Errorf("session of a blocked retailer: want 401, got %v", err)
	}
	res, err := t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathAPIBase+"/grant", nil, bearerHeader(tok))
	t.Check(err, "grant")
	t.expectStatus(res, http.StatusUnauthorized, obx.ErrAccessRevoked, "API token of a blocked retailer")
	res, err = t.r.tokenRequest(t.ctx, c.Client.ClientID, c.ClientSecret, "")
	t.Check(err, "token")
	t.expectStatus(res, http.StatusUnauthorized, "invalid_client", "token for a blocked retailer")
}

func testKeys(t *T) {
	t.needAdmin("keys")
	rt, err := t.r.admin.CreateRetailer(t.ctx, nodeadmin.RetailerInput{Name: "OBX conformance: keys"})
	t.Check(err, "retailer")
	c, err := t.r.admin.CreateAPIClient(t.ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: "keys", Scopes: []string{obx.ScopeCatalogRead}})
	t.Check(err, "API key")
	if !strings.HasPrefix(c.Client.ClientID, obx.PrefixClientID) || !strings.HasPrefix(c.ClientSecret, obx.PrefixClientSecret) {
		t.Errorf("client id/secret prefixes: %s / %.5s…", c.Client.ClientID, c.ClientSecret)
	}
	list, err := t.r.adminClients(t.ctx)
	t.Check(err, "list")
	if strings.Contains(string(mustJSON(list)), c.ClientSecret) {
		t.Errorf("listing API keys shows a secret")
	}
	if r, _ := t.r.tokenRequest(t.ctx, c.Client.ClientID, c.ClientSecret, ""); r == nil || r.status != http.StatusOK {
		t.Errorf("the new secret does not work")
	}
	rot, err := t.r.admin.RotateAPIClient(t.ctx, c.Client.ID)
	t.Check(err, "rotate")
	if r, _ := t.r.tokenRequest(t.ctx, c.Client.ClientID, c.ClientSecret, ""); r == nil || r.status != http.StatusUnauthorized {
		t.Errorf("the old secret still works after rotation")
	}
	r, _ := t.r.tokenRequest(t.ctx, c.Client.ClientID, rot.ClientSecret, "")
	if r == nil || r.status != http.StatusOK {
		t.Fatalf("the rotated secret does not work")
	}
	var tr obx.TokenResponse
	r.decode(&tr)
	t.Check(t.r.admin.RevokeAPIClient(t.ctx, c.Client.ID), "revoke")
	if r, _ := t.r.tokenRequest(t.ctx, c.Client.ClientID, rot.ClientSecret, ""); r == nil || r.status != http.StatusUnauthorized {
		t.Errorf("a revoked API key still gets tokens")
	}
	res, err := t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathAPIBase+"/grant", nil, bearerHeader(tr.AccessToken))
	t.Check(err, "grant")
	t.expectStatus(res, http.StatusUnauthorized, obx.ErrAccessRevoked, "token of a revoked API key")

	it, err := t.r.admin.CreateIngestToken(t.ctx, nodeadmin.IngestTokenInput{Name: "OBX conformance: keys", Scopes: []string{obx.IngestScopeRead}})
	t.Check(err, "upload key")
	if !strings.HasPrefix(it.Secret, obx.PrefixIngestToken) {
		t.Errorf("upload key does not start with obxi_")
	}
	var toks obx.Items[nodeadmin.IngestToken]
	t.call(t.r.admin, http.MethodGet, "/ingest-tokens", nil, &toks)
	if strings.Contains(string(mustJSON(toks)), it.Secret) {
		t.Errorf("listing upload keys shows a secret")
	}
	probe := func(secret string) int {
		res, err := t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathIngest, nil, bearerHeader(secret))
		t.Check(err, "probe")
		return res.status
	}
	if probe(it.Secret) != http.StatusOK {
		t.Errorf("the new upload key does not work")
	}
	rotated, err := t.r.admin.RotateIngestToken(t.ctx, it.Token.ID)
	t.Check(err, "rotate upload key")
	if probe(it.Secret) != http.StatusUnauthorized {
		t.Errorf("the old upload key still works after rotation")
	}
	if probe(rotated.Secret) != http.StatusOK {
		t.Errorf("the rotated upload key does not work")
	}
	t.Check(t.r.admin.RevokeIngestToken(t.ctx, it.Token.ID), "revoke upload key")
	res, err = t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathIngest, nil, bearerHeader(rotated.Secret))
	t.Check(err, "probe")
	t.expectStatus(res, http.StatusUnauthorized, obx.ErrAccessRevoked, "revoked upload key")
}

func testAdminCatalog(t *T) {
	t.needAdmin("catalog")
	var cursor string
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	expect := func(step, want string) {
		if t.r.full == nil {
			return
		}
		var live []obx.Change
		live, cursor = t.newEntries(t.r.full, cursor)
		if got := strings.Join(changeKeys(live), "; "); got != want {
			t.Errorf("%s: /changes gave [%s], want [%s] (as with ingest)", step, got, want)
		}
	}
	p := obx.ProductInput{BrandExternalID: obx.Ptr("kestrel"), StyleNumber: "OBX-CONF-ADMIN", Name: obx.LocalizedText{"en": "Admin Test Cap", "nl": "Testpet"}}
	res, err := t.r.admin.CatalogWrite(t.ctx, http.MethodPut, obx.KindProduct, "OBX-CONF-ADMIN", p)
	t.Check(err, "PUT product")
	if res.Result != obx.ResultCreated {
		t.Errorf("first PUT: %s", res.Result)
	}
	expect("create", "product upsert OBX-CONF-ADMIN")
	res, err = t.r.admin.CatalogWrite(t.ctx, http.MethodPut, obx.KindProduct, "OBX-CONF-ADMIN", p)
	t.Check(err, "PUT again")
	if res.Result != obx.ResultUnchanged {
		t.Errorf("repeated PUT: %s", res.Result)
	}
	expect("unchanged PUT", "")
	_, err = t.r.admin.CatalogWrite(t.ctx, http.MethodPut, obx.KindVariant, "OBX-CONF-ADMIN-V", obx.VariantInput{ProductExternalID: "OBX-CONF-ADMIN", GTIN: obx.Ptr("1234567890123"), Color: obx.Color{Code: "BLK"}})
	if !obx.IsCode(err, obx.ErrInvalidGTIN) {
		t.Errorf("admin variant with a bad EAN: want invalid_gtin, got %v", err)
	}
	res, err = t.r.admin.CatalogWrite(t.ctx, http.MethodPatch, obx.KindProduct, "OBX-CONF-ADMIN", map[string]string{"status": obx.StatusUnpublished})
	t.Check(err, "PATCH")
	if res.Result != obx.ResultUpdated {
		t.Errorf("unpublish: %s", res.Result)
	}
	expect("unpublish", "product delete OBX-CONF-ADMIN")
	res, err = t.r.admin.CatalogWrite(t.ctx, http.MethodDelete, obx.KindProduct, "OBX-CONF-ADMIN", nil)
	t.Check(err, "DELETE")
	if res.Result != obx.ResultDeleted {
		t.Errorf("delete: %s", res.Result)
	}
	expect("delete of an unpublished product", "")
	var list nodeadmin.ProductList
	t.call(t.r.admin, http.MethodGet, "/catalog/products?q=KO-1001", nil, &list)
	if list.Total < 1 || len(list.Items) == 0 || list.Items[0].ExternalID != "KO-1001" {
		t.Errorf("admin product search for KO-1001: %s", short(list))
	}
	var detail nodeadmin.ProductDetail
	t.call(t.r.admin, http.MethodGet, "/catalog/products/KO-1001", nil, &detail)
	if len(detail.Variants) != 8 || len(detail.Assets) != 4 {
		t.Errorf("admin detail of KO-1001: %d variants, %d images", len(detail.Variants), len(detail.Assets))
	}
}

func testImportSources(t *T) {
	t.needAdmin("scheduled imports")
	try := func(u string) error {
		var src obx.ImportSource
		err := t.r.admin.Call(t.ctx, http.MethodPost, obx.PathAdminAPI+"/import-sources", map[string]any{"name": "SSRF probe", "kind": "https_url", "config": map[string]any{"url": u}}, &src)
		if err == nil {
			t.r.admin.Call(t.ctx, http.MethodDelete, obx.PathAdminAPI+"/import-sources/"+src.ID, nil, nil)
		}
		return err
	}
	if err := try("https://169.254.169.254/latest/meta-data/products.csv"); !obx.IsCode(err, obx.ErrUnsafeURL) {
		t.Errorf("cloud metadata address: want 422 unsafe_url, got %v", err)
	}
	err := try("https://127.0.0.1/products.csv")
	switch {
	case obx.IsCode(err, obx.ErrUnsafeURL):
	case t.r.privateURLsAllowed != nil && *t.r.privateURLsAllowed:
		t.Notef("the node allows private addresses (development mode), so https://127.0.0.1 was accepted")
	default:
		t.Errorf("https://127.0.0.1/products.csv: want 422 unsafe_url, got %v", err)
	}
	secret := "Bearer conformance-" + randomSuffix()
	var src obx.ImportSource
	raw := map[string]any{"name": "Nightly export", "kind": "https_url", "enabled": false, "schedule": map[string]any{"frequency": "daily", "time": "02:30", "timezone": "Europe/Amsterdam"},
		"mode": "upsert", "config": map[string]any{"url": "https://93.184.215.14/export/products.csv"}, "credentials": map[string]any{"headers": map[string]string{"Authorization": secret}}}
	b, err := json.Marshal(raw)
	t.Check(err, "marshal")
	resp, err := t.r.admin.Raw(t.ctx, http.MethodPost, obx.PathAdminAPI+"/import-sources", b, "application/json", true)
	t.Check(err, "create source")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create source: %d %s", resp.StatusCode, truncate(body))
	}
	if strings.Contains(string(body), secret) {
		t.Errorf("the create answer contains the credential")
	}
	json.Unmarshal(body, &src)
	t.validate("ImportSource", body, "the import source")
	if !obx.Contains(src.CredentialsSet.Headers, "Authorization") {
		t.Errorf("credentials_set.headers does not list Authorization: %s", short(src.CredentialsSet))
	}
	status, _, got := t.download(t.r.admin, "/import-sources/"+src.ID)
	if status != http.StatusOK || strings.Contains(string(got), secret) {
		t.Errorf("GET source: %d, contains the credential: %v", status, strings.Contains(string(got), secret))
	}
	t.call(t.r.admin, http.MethodDelete, "/import-sources/"+src.ID, nil, nil)
}

// portalRetailer is the retailer user used by A-PORT-1 and A-STAT-1.
func (t *T) portalRetailer() (*nodeadmin.Client, *nodeadmin.Retailer) {
	if t.r.portalClient == nil {
		t.r.portalClient, t.r.portalRetailerRec = t.portalUser("OBX conformance: portal", nodeadmin.RetailerInput{})
	}
	return t.r.portalClient, t.r.portalRetailerRec
}

func testPortal(t *T) {
	t.needAdmin("the portal")
	c, _ := t.portalRetailer()
	var v nodeadmin.PortalViewer
	t.call(c, http.MethodGet, "/portal/viewer", nil, &v)
	if v.Preview || v.Retailer == nil || len(v.Brands) != 2 || !v.Permissions.OriginalDownload {
		t.Errorf("viewer: %s", short(v))
	}
	var list nodeadmin.PortalProductList
	t.call(c, http.MethodGet, "/portal/products?limit=5", nil, &list)
	if list.Total != 8 || len(list.Items) != 5 || list.NextCursor == nil {
		t.Errorf("product list: total %d, %d items (limit 5), next %v", list.Total, len(list.Items), list.NextCursor)
	}
	var page2 nodeadmin.PortalProductList
	t.call(c, http.MethodGet, "/portal/products?limit=5&cursor="+url.QueryEscape(obx.Str(list.NextCursor)), nil, &page2)
	if len(page2.Items) != 3 {
		t.Errorf("second page: %d items, want 3", len(page2.Items))
	}
	var filtered nodeadmin.PortalProductList
	t.call(c, http.MethodGet, "/portal/products?item_type=jacket&q=waxed", nil, &filtered)
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].StyleNumber != "KO-1002" {
		t.Errorf("filter item_type=jacket q=waxed: %s", short(filtered))
	}
	var facets nodeadmin.Facets
	t.call(c, http.MethodGet, "/portal/facets", nil, &facets)
	if len(facets.Brands) != 2 || len(facets.Colors) == 0 || len(facets.ItemTypes) == 0 {
		t.Errorf("facets: %s", short(facets))
	}
	var detail obx.ProductDetail
	t.call(c, http.MethodGet, "/portal/products/"+t.productID("KO-1001"), nil, &detail)
	if detail.ExternalID != "KO-1001" || len(detail.Variants) != 8 || len(detail.Assets) != 4 {
		t.Errorf("detail of KO-1001: %d variants, %d images", len(detail.Variants), len(detail.Assets))
	}
	var lookup obx.GTINLookupResult
	t.call(c, http.MethodPost, "/portal/gtin-lookup", map[string]string{"text": "2000100101025\n12345\n2000999901010"}, &lookup)
	if len(lookup.Results) != 3 || lookup.Results[0].Status != obx.GTINMatched || lookup.Results[1].Status != obx.GTINInvalid || lookup.Results[2].Status != obx.GTINUnmatched {
		t.Errorf("EAN paste: %s", short(lookup.Results))
	}
	var sel nodeadmin.Selection
	t.call(c, http.MethodPut, "/portal/selection", map[string]any{"product_ids": []string{t.productID("KO-1001"), t.productID("KO-1005")}}, &sel)
	if len(sel.ProductIDs) != 2 || sel.ImageCount != 7 {
		t.Errorf("selection: %s (want 2 products, 7 images)", short(sel))
	}
	status, h, data := t.download(c, "/portal/products/"+t.productID("KO-1005")+"/download.zip?variant=web")
	if status != http.StatusOK || !strings.Contains(h.Get("Content-Disposition"), "attachment") {
		t.Errorf("product zip: %d %s", status, h.Get("Content-Disposition"))
	} else {
		names := zipNames(t, data)
		if len(names) != 3 {
			t.Errorf("product zip of KO-1005 has %d entries, want 3: %v", len(names), names)
		}
		for _, n := range names {
			if !strings.HasSuffix(n, "_web.jpg") {
				t.Errorf("product zip entry %q does not end in _web.jpg", n)
			}
		}
	}
	status, _, data = t.download(c, "/portal/selection/download.zip?variant=square")
	if status != http.StatusOK {
		t.Errorf("selection zip: %d", status)
	} else {
		names := zipNames(t, data)
		if len(names) != 7 {
			t.Errorf("selection zip has %d entries, want 7", len(names))
		}
		for _, n := range names {
			if !strings.HasPrefix(n, "KO-1001/") && !strings.HasPrefix(n, "KO-1005/") {
				t.Errorf("selection zip entry %q is not in a folder per style number", n)
			}
		}
	}
	status, _, data = t.download(c, "/portal/selection/data.csv?delimiter=semicolon")
	if status != http.StatusOK {
		t.Errorf("selection CSV: %d", status)
	} else {
		cr := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
		cr.Comma = ';'
		cr.FieldsPerRecord = -1
		rows, err := cr.ReadAll()
		t.Check(err, "parse CSV")
		if len(rows) != 10 {
			t.Errorf("selection CSV has %d rows, want header + 9 variants", len(rows))
		}
		if len(rows) > 0 {
			header := strings.Join(rows[0], ",")
			for _, col := range []string{"style_number", "gtin", "color_code", "size", "name:nl", "name:en", "image_files"} {
				if !strings.Contains(","+header+",", ","+col+",") {
					t.Errorf("CSV header lacks %s: %s", col, header)
				}
			}
		}
		for _, row := range rows[1:] {
			for _, cell := range row {
				if cell != "" && strings.ContainsRune("=+@", rune(cell[0])) {
					t.Errorf("CSV cell %q is not formula-escaped", cell)
				}
			}
		}
	}
	status, _, data = t.download(c, "/portal/products/data.csv?brand_id="+url.QueryEscape(t.r.brandIDs["kestrel-trail"]))
	if status != http.StatusOK || bytes.Count(data, []byte("\n")) != 7 {
		t.Errorf("filtered CSV for kestrel-trail: %d, %d lines (want header + 6)", status, bytes.Count(data, []byte("\n")))
	}
	var job nodeadmin.DownloadJob
	t.call(c, http.MethodPost, "/portal/download-jobs", nodeadmin.DownloadJobInput{Source: "selection", Variant: obx.RenditionWeb}, &job)
	deadline := time.Now().Add(60 * time.Second)
	for job.Status != obx.JobSucceeded && job.Status != obx.JobFailed && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		t.call(c, http.MethodGet, "/portal/download-jobs/"+job.ID, nil, &job)
	}
	if job.Status != obx.JobSucceeded {
		t.Fatalf("download job: %s", short(job))
	}
	status, _, data = t.download(c, "/portal/download-jobs/"+job.ID+"/file")
	if status != http.StatusOK {
		t.Errorf("download job file: %d", status)
	} else if names := zipNames(t, data); len(names) < 7 {
		t.Errorf("download job zip has %d entries, want at least 7", len(names))
	}
	other, _ := t.portalUser("OBX conformance: other retailer", nodeadmin.RetailerInput{})
	if status, _, _ := t.download(other, "/portal/download-jobs/"+job.ID+"/file"); status != http.StatusNotFound {
		t.Errorf("another retailer can fetch this download job (%d)", status)
	}
}

func testPortalRestricted(t *T) {
	t.needAdmin("the portal")
	kestrel := t.r.brandIDs["kestrel"]
	c, _ := t.portalUser("OBX conformance: portal kestrel only", nodeadmin.RetailerInput{BrandIDs: &[]string{kestrel}})
	var list nodeadmin.PortalProductList
	t.call(c, http.MethodGet, "/portal/products?limit=100", nil, &list)
	if list.Total != 6 {
		t.Errorf("restricted retailer sees %d products, want 6", list.Total)
	}
	for _, p := range list.Items {
		if p.Brand.ID != kestrel {
			t.Errorf("restricted retailer sees %s of brand %s", p.StyleNumber, p.Brand.Name)
		}
	}
	var facets nodeadmin.Facets
	t.call(c, http.MethodGet, "/portal/facets", nil, &facets)
	if len(facets.Brands) != 1 || facets.Brands[0].Value != kestrel {
		t.Errorf("restricted facets: %s", short(facets.Brands))
	}
	if status, _, _ := t.download(c, "/portal/products/"+t.productID("KT-2001")); status != http.StatusNotFound {
		t.Errorf("restricted retailer GET kestrel-trail product: %d, want 404", status)
	}
	if status, _, _ := t.download(c, "/portal/assets/"+t.assetID("KT-2001_BLK_1")+"/download?variant=web"); status != http.StatusNotFound {
		t.Errorf("restricted retailer download of a kestrel-trail image: %d, want 404", status)
	}
	var lookup obx.GTINLookupResult
	t.call(c, http.MethodPost, "/portal/gtin-lookup", map[string]any{"gtins": []string{"2000200101017"}}, &lookup)
	if len(lookup.Results) != 1 || lookup.Results[0].Status != obx.GTINUnmatched || len(lookup.Products) != 0 {
		t.Errorf("restricted EAN lookup of a kestrel-trail EAN: %s", short(lookup))
	}
	var v nodeadmin.PortalViewer
	t.call(c, http.MethodGet, "/portal/viewer", nil, &v)
	if len(v.Brands) != 1 {
		t.Errorf("restricted viewer lists %d brands", len(v.Brands))
	}
	no, _ := t.portalUser("OBX conformance: portal no originals", nodeadmin.RetailerInput{AllowOriginal: obx.Ptr(false)})
	t.call(no, http.MethodGet, "/portal/viewer", nil, &v)
	if v.Permissions.OriginalDownload {
		t.Errorf("viewer.permissions.original_download is true for a retailer without originals")
	}
	status, _, body := t.download(no, "/portal/assets/"+t.assetID("KO-1001_NVY_1")+"/download?variant=original")
	if status != http.StatusForbidden || nodeadmin.DecodeProblem(body) == nil || nodeadmin.DecodeProblem(body).ObxError != obx.ErrOriginalNotAllowed {
		t.Errorf("original download without permission: %d %s, want 403 original_not_allowed", status, truncate(body))
	}
	status, _, body = t.download(no, "/portal/products/"+t.productID("KO-1001"))
	if status != http.StatusOK || bytes.Contains(body, []byte(`"original"`)) {
		t.Errorf("product detail for a retailer without originals contains an original (%d)", status)
	}
	if status, _, _ := t.download(no, "/portal/assets/"+t.assetID("KO-1001_NVY_1")+"/download"); status != http.StatusOK {
		t.Errorf("default download for a retailer without originals: %d, want 200 (web)", status)
	}
}

func testStats(t *T) {
	t.needAdmin("stats")
	c, rt := t.portalRetailer()
	before, err := t.r.admin.Stats(t.ctx, 30)
	t.Check(err, "stats")
	if status, _, _ := t.download(c, "/portal/assets/"+t.assetID("KO-1005_COG_1")+"/download?variant=web"); status != http.StatusOK {
		t.Fatalf("retailer download: %d", status)
	}
	after, err := t.r.admin.Stats(t.ctx, 30)
	t.Check(err, "stats")
	if after.Totals.Downloads != before.Totals.Downloads+1 || after.Totals.Images != before.Totals.Images+1 {
		t.Errorf("a retailer download did not count: downloads %d → %d, images %d → %d", before.Totals.Downloads, after.Totals.Downloads, before.Totals.Images, after.Totals.Images)
	}
	found := false
	for _, r := range after.TopRetailers {
		if r.RetailerID == rt.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the retailer is not in top_retailers")
	}
	events, err := t.r.admin.StatsEvents(t.ctx, url.Values{"retailer_id": {rt.ID}})
	t.Check(err, "events")
	if len(events) == 0 || events[0].Kind != nodeadmin.DownloadImage {
		t.Errorf("stats events for the retailer: %s", short(events))
	}
	if status, _, _ := t.download(t.r.admin, "/portal/assets/"+t.assetID("KO-1005_COG_1")+"/download?variant=web"); status != http.StatusOK {
		t.Fatalf("admin preview download: %d", status)
	}
	final, err := t.r.admin.Stats(t.ctx, 30)
	t.Check(err, "stats")
	if final.Totals.Downloads != after.Totals.Downloads {
		t.Errorf("an admin preview download was counted (%d → %d)", after.Totals.Downloads, final.Totals.Downloads)
	}
}

var _ = fmt.Sprint
