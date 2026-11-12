package mock

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/obx"
)

// retailer is a retailer company with its grant.
type retailer struct {
	id, name       string
	reference      *string
	website        *string
	vat            *string
	country        *string
	notes          *string
	status         string
	brandIDs       []string // nil = all published brands
	allowOriginal  *bool
	createdAt      time.Time
	grantID        string
	grantUpdatedAt time.Time
	lastActivity   *time.Time
}

// apiClient is an API key for a retailer's software.
type apiClient struct {
	id, clientID, secretHash, name, retailerID string
	scopes                                     []string
	status                                     string
	createdAt                                  time.Time
	lastUsedAt                                 *time.Time
}

type accessToken struct {
	clientID string // internal id
	scopes   []string
	expires  time.Time
}

// caller is an authenticated protocol caller.
type caller struct {
	client   *apiClient
	retailer *retailer
	scopes   []string
}

func (c *caller) has(scope string) bool { return obx.Contains(c.scopes, scope) }

type rateWindow struct {
	start time.Time
	count int
}

func (s *state) originalAllowed(r *retailer) bool {
	return s.settings.allowOriginal && (r.allowOriginal == nil || *r.allowOriginal)
}

func (s *state) brandAllowed(r *retailer, brandID string) bool {
	return r.brandIDs == nil || obx.Contains(r.brandIDs, brandID)
}

// grantScopes are the scopes a retailer's clients may hold.
func (s *state) grantScopes(r *retailer) []string {
	sc := []string{obx.ScopeCatalogRead, obx.ScopeMediaRead}
	if s.originalAllowed(r) {
		sc = append(sc, obx.ScopeMediaOriginal)
	}
	return append(sc, obx.ScopeUsageWrite)
}

func (s *state) grantOf(r *retailer) obx.Grant {
	status := "active"
	if r.status != "active" {
		status = "revoked"
	}
	var brands []string
	if r.brandIDs != nil {
		brands = append([]string{}, r.brandIDs...)
	}
	return obx.Grant{ID: r.grantID, Retailer: obx.GrantRetailer{ID: r.id, Name: r.name}, BrandIDs: brands, Scopes: s.grantScopes(r),
		OriginalAllowed: s.originalAllowed(r), Status: status, UpdatedAt: r.grantUpdatedAt}
}

// capability reports whether an optional capability is offered.
func (n *Node) capability(c string) bool {
	if n.opts.Capabilities == nil {
		if c == obx.CapServerRenditions {
			return !n.opts.ClientRenditions
		}
		return true
	}
	return obx.Contains(n.opts.Capabilities, c)
}

func (n *Node) capabilities() []string {
	all := []string{obx.CapChanges, obx.CapProducts, obx.CapByGTIN, obx.CapRenditions}
	for _, c := range []string{obx.CapExports, obx.CapWebhooks, obx.CapUsage, obx.CapAccessRequests, obx.CapIngest, obx.CapServerRenditions, obx.CapPortal} {
		if n.capability(c) {
			all = append(all, c)
		}
	}
	return all
}

// ---- rate limiting and knobs

// limit applies the per-client rate limit and the 429/5xx knobs. It writes
// the RateLimit headers and returns false when the request was answered.
func (n *Node) limit(w http.ResponseWriter, key string, perMinute int) bool {
	n.knobs.mu.Lock()
	if n.knobs.force429 > 0 {
		n.knobs.force429--
		ra := n.knobs.retryAfter
		n.knobs.mu.Unlock()
		p := problem(obx.ErrRateLimited, "slow down")
		p.RetryAfter = &ra
		w.Header().Set("RateLimit-Limit", strconv.Itoa(perMinute))
		w.Header().Set("RateLimit-Remaining", "0")
		w.Header().Set("RateLimit-Reset", strconv.Itoa(ra))
		writeProblem(w, p)
		return false
	}
	if n.knobs.failNext5xx > 0 {
		n.knobs.failNext5xx--
		n.knobs.mu.Unlock()
		writeProblem(w, problem(obx.ErrServiceUnavailable, "temporarily unavailable (test knob)"))
		return false
	}
	n.knobs.mu.Unlock()
	now := n.now()
	rw := n.st.rate[key]
	if rw == nil || now.Sub(rw.start) >= time.Minute {
		rw = &rateWindow{start: now}
		n.st.rate[key] = rw
	}
	rw.count++
	reset := int(time.Minute.Seconds() - now.Sub(rw.start).Seconds())
	if reset < 1 {
		reset = 1
	}
	remaining := perMinute - rw.count
	if remaining < 0 {
		remaining = 0
	}
	w.Header().Set("RateLimit-Limit", strconv.Itoa(perMinute))
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("RateLimit-Reset", strconv.Itoa(reset))
	if rw.count > perMinute {
		p := problem(obx.ErrRateLimited, "too many requests")
		p.RetryAfter = &reset
		writeProblem(w, p)
		return false
	}
	return true
}

// ---- auth

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authProtocol authenticates a protocol call; scope "" = any. It holds n.mu.
func (n *Node) authProtocol(w http.ResponseWriter, r *http.Request, scope string) (*caller, bool) {
	s := n.st
	if !s.setupDone {
		writeProblem(w, problem(obx.ErrSetupRequired, "this node has not been set up yet").WithStatus(http.StatusServiceUnavailable))
		return nil, false
	}
	tok := bearer(r)
	if tok == "" {
		writeProblem(w, problem(obx.ErrUnauthorized, "send Authorization: Bearer <access token>"))
		return nil, false
	}
	at := s.accessTokens[hashSecret(tok)]
	if at == nil || n.now().After(at.expires) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeProblem(w, problem(obx.ErrInvalidToken, "the access token is unknown or expired; request a new one"))
		return nil, false
	}
	c := s.clients[at.clientID]
	rt := s.retailers[c.retailerID]
	if c.status != "active" || rt == nil || rt.status != "active" {
		writeProblem(w, problem(obx.ErrAccessRevoked, "access for this API key was revoked by the brand"))
		return nil, false
	}
	if !n.limit(w, "client:"+c.id, n.opts.RateLimit) {
		return nil, false
	}
	cl := &caller{client: c, retailer: rt, scopes: at.scopes}
	if !s.originalAllowed(rt) {
		cl.scopes = without(cl.scopes, obx.ScopeMediaOriginal)
	}
	if scope != "" && !cl.has(scope) {
		writeProblem(w, problemf(obx.ErrInsufficientScope, "this call needs the %s scope", scope))
		return nil, false
	}
	now := n.now()
	c.lastUsedAt, rt.lastActivity = &now, &now
	s.tick()
	return cl, true
}

func without(list []string, x string) []string {
	var out []string
	for _, v := range list {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func (n *Node) signer(r *http.Request, c *caller) *urlSigner {
	return &urlSigner{base: n.baseURL(r), originalAllowed: c.has(obx.ScopeMediaOriginal), expires: n.now().Add(time.Hour).Truncate(time.Second), n: n}
}

// visibleTo reports whether a record is visible to the caller's retailer.
func (s *state) visibleTo(r *record, rt *retailer, now time.Time) bool {
	if r == nil || !s.isVisible(r, now) {
		return false
	}
	b := s.brandOf(r)
	return b != nil && s.brandAllowed(rt, b.id)
}

// ---- handlers

func (n *Node) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	if !s.setupDone {
		writeProblem(w, problem(obx.ErrSetupRequired, "this node has not been set up yet").WithStatus(http.StatusServiceUnavailable))
		return
	}
	base := n.baseURL(r)
	var logo *string
	if s.settings.logo != nil {
		logo = ptr(base + obx.PathAdminAPI + "/public/logo")
	}
	d := obx.DiscoveryDocument{
		ObxVersion: obx.Version, SpecVersion: obx.SpecVersion,
		Node: obx.DiscoveryNode{Software: "obx-go mock", Version: obx.SpecVersion},
		Publisher: obx.Publisher{ID: n.nodeID, Name: s.settings.displayName, LogoURL: logo, AccentColor: s.settings.accent, Website: s.settings.website,
			ContactEmail: s.settings.contactEmail, WelcomeText: nilIfEmpty(s.settings.welcome)},
		APIBase:   base + obx.PathAPIBase,
		PortalURL: ptr(base + "/"),
		Auth: obx.DiscoveryAuth{TokenEndpoint: base + obx.PathAPIBase + "/oauth/token", ScopesSupported: obx.AllScopes,
			TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "client_secret_post"}},
		Capabilities: n.capabilities(),
		Brands:       []obx.DiscoveryBrand{},
		Languages:    s.settings.languages,
		Limits:       obx.DiscoveryLimits{ChangesPageMax: 500, ProductsPageMax: 100, GTINLookupMax: 2000, TombstoneRetentionDays: 90},
	}
	if n.capability(obx.CapAccessRequests) {
		d.AccessRequestsURL = ptr(base + obx.PathAPIBase + "/access-requests")
	}
	if n.capability(obx.CapIngest) {
		d.IngestURL = ptr(base + obx.PathIngest)
	}
	now := n.now()
	for _, b := range sortedByID(s.brands) {
		if s.isVisible(b, now) {
			d.Brands = append(d.Brands, obx.DiscoveryBrand{ID: b.id, Name: b.brand.Name, LogoURL: b.brand.LogoURL})
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, d)
}

func sortedByID(m map[string]*record) []*record {
	out := make([]*record, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="obx"`)
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(obx.OAuthError{Code: code, Description: desc})
}

func (n *Node) handleToken(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	if !n.limit(w, "token:"+clientIP(r), 120) {
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body must be application/x-www-form-urlencoded")
		return
	}
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "only client_credentials is supported")
		return
	}
	var c *apiClient
	for _, x := range s.clients {
		if x.clientID == id {
			c = x
		}
	}
	if c == nil || !constantEqual(c.secretHash, hashSecret(secret)) {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client or wrong secret")
		return
	}
	rt := s.retailers[c.retailerID]
	if c.status != "active" || rt == nil || rt.status != "active" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "this API key was revoked")
		return
	}
	allowed := c.scopes
	if !s.originalAllowed(rt) {
		allowed = without(allowed, obx.ScopeMediaOriginal)
	}
	scopes := allowed
	if req := strings.Fields(r.PostForm.Get("scope")); len(req) > 0 {
		for _, sc := range req {
			if !obx.Contains(allowed, sc) {
				oauthError(w, http.StatusBadRequest, "invalid_scope", "scope "+sc+" is not available to this client")
				return
			}
		}
		scopes = req
	}
	tok := randomToken("obxat_", 32)
	s.accessTokens[hashSecret(tok)] = &accessToken{clientID: c.id, scopes: scopes, expires: n.now().Add(time.Hour)}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, obx.TokenResponse{AccessToken: tok, TokenType: "Bearer", ExpiresIn: 3600, Scope: strings.Join(scopes, " ")})
}

func (n *Node) handleGrant(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, "")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, n.st.grantOf(c.retailer))
}

// ---- change feed

type cursorData struct {
	N string `json:"n"`
	E int    `json:"e"`
	R string `json:"r"`
	P string `json:"p"`
	H int64  `json:"h,omitempty"`
	T int    `json:"t,omitempty"`
	I string `json:"i,omitempty"`
	S int64  `json:"s,omitempty"`
	A int64  `json:"a"`
}

func (n *Node) encodeCursor(c cursorData) string {
	c.N, c.E = n.nodeID, n.st.cursorEpoch
	if c.A == 0 {
		c.A = n.now().Unix()
	}
	b, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + n.sign("cursor", p)[:24]
}

func (n *Node) decodeCursor(s string, rt *retailer) (*cursorData, *obx.Problem) {
	p, sig, ok := strings.Cut(s, ".")
	if !ok || !constantEqual(sig, n.sign("cursor", p)[:24]) {
		return nil, problem(obx.ErrCursorInvalid, "this cursor was not issued by this node; restart without a cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	var c cursorData
	if err != nil || json.Unmarshal(b, &c) != nil || c.N != n.nodeID || c.R != rt.id {
		return nil, problem(obx.ErrCursorInvalid, "malformed cursor; restart without a cursor")
	}
	if c.E < n.st.cursorEpoch {
		return nil, problem(obx.ErrCursorExpired, "this cursor is too old; restart without a cursor")
	}
	if c.P == obx.PhaseSnapshot && n.now().Sub(time.Unix(c.A, 0)) > n.knobs.snapshotMaxAge {
		return nil, problem(obx.ErrCursorExpired, "snapshot cursors expire after 24 hours; restart without a cursor")
	}
	return &c, nil
}

func (n *Node) changeData(r *http.Request, c *caller, rec *record) json.RawMessage {
	s := n.st
	switch rec.kind {
	case obx.KindBrand:
		return canonicalJSON(s.brandRepr(rec))
	case obx.KindProduct:
		return canonicalJSON(s.productRepr(rec))
	case obx.KindVariant:
		return canonicalJSON(s.variantRepr(rec))
	default:
		return canonicalJSON(s.assetRepr(rec, n.signer(r, c)))
	}
}

func (n *Node) handleChanges(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	s := n.st
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		l, err := strconv.Atoi(v)
		if err != nil || l < 1 {
			writeProblem(w, problem(obx.ErrInvalidRequest, "limit must be 1-500"))
			return
		}
		limit = min(l, 500)
	}
	now := n.now()
	page := obx.ChangesPage{Changes: []obx.Change{}}
	cur := cursorData{R: c.retailer.id, P: obx.PhaseSnapshot, H: s.seq, T: -1}
	if v := r.URL.Query().Get("cursor"); v != "" {
		cd, p := n.decodeCursor(v, c.retailer)
		if p != nil {
			writeProblem(w, p)
			return
		}
		cur = *cd
	}
	if cur.P == obx.PhaseSnapshot {
		page.Phase = obx.PhaseSnapshot
		type item struct {
			rank int
			rec  *record
		}
		var items []item
		for _, kind := range []string{obx.KindBrand, obx.KindProduct, obx.KindVariant, obx.KindAsset} {
			for _, rec := range sortedByID(s.table(kind)) {
				rank := typeRank[kind]
				if rank < cur.T || (rank == cur.T && rec.id <= cur.I) {
					continue
				}
				if s.visibleTo(rec, c.retailer, now) {
					items = append(items, item{rank, rec})
				}
			}
		}
		if len(items) > limit {
			items = items[:limit]
			last := items[len(items)-1]
			next := cur
			next.T, next.I, next.A = last.rank, last.rec.id, cur.A
			page.NextCursor = n.encodeCursor(next)
		} else {
			page.NextCursor = n.encodeCursor(cursorData{R: c.retailer.id, P: obx.PhaseLive, S: cur.H})
		}
		for _, it := range items {
			page.Changes = append(page.Changes, n.changeFor(r, c, it.rec, s.lastSeq[it.rec.id], obx.OpUpsert, it.rec.protoAt))
		}
		page.HasMore = true
		writeJSON(w, http.StatusOK, page)
		return
	}
	page.Phase = obx.PhaseLive
	n.knobs.mu.Lock()
	dup := n.knobs.duplicate
	n.knobs.mu.Unlock()
	start := sort.Search(len(s.log), func(i int) bool { return s.log[i].seq > cur.S })
	lastSeq := cur.S
	count := 0
	i := start
	for ; i < len(s.log) && count < limit; i++ {
		e := s.log[i]
		lastSeq = e.seq
		ch, ok := n.liveEntry(r, c, e, now)
		if !ok {
			continue
		}
		page.Changes = append(page.Changes, ch)
		if dup {
			page.Changes = append(page.Changes, ch)
		}
		count++
	}
	page.HasMore = i < len(s.log)
	page.NextCursor = n.encodeCursor(cursorData{R: c.retailer.id, P: obx.PhaseLive, S: lastSeq})
	writeJSON(w, http.StatusOK, page)
}

func (n *Node) changeFor(r *http.Request, c *caller, rec *record, seq int64, op string, at time.Time) obx.Change {
	s := n.st
	ch := obx.Change{Seq: seq, Type: rec.kind, Op: op, ID: rec.id, ExternalID: ptr(rec.extID), UpdatedAt: at}
	if b := s.brandOf(rec); b != nil {
		ch.BrandID = ptr(b.id)
	}
	if rec.kind == obx.KindVariant || rec.kind == obx.KindAsset {
		if p := s.products[rec.parentExt]; p != nil {
			ch.ProductID = ptr(p.id)
		}
	}
	if op == obx.OpUpsert {
		ch.Data = n.changeData(r, c, rec)
	}
	return ch
}

func (n *Node) liveEntry(r *http.Request, c *caller, e entry, now time.Time) (obx.Change, bool) {
	s := n.st
	if e.typ == obx.ChangeGrant {
		if e.retailerID != c.retailer.id {
			return obx.Change{}, false
		}
		ch := obx.Change{Seq: e.seq, Type: obx.ChangeGrant, Op: obx.OpUpsert, ID: e.id, UpdatedAt: e.at, Resync: ptr(e.resync)}
		ch.Data = canonicalJSON(s.grantOf(c.retailer))
		return ch, true
	}
	if !s.brandAllowed(c.retailer, e.brandID) {
		return obx.Change{}, false
	}
	if e.op == obx.OpDelete {
		ch := obx.Change{Seq: e.seq, Type: e.typ, Op: obx.OpDelete, ID: e.id, ExternalID: ptr(e.extID), UpdatedAt: e.at}
		if e.brandID != "" {
			ch.BrandID = ptr(e.brandID)
		}
		if e.productID != "" {
			ch.ProductID = ptr(e.productID)
		}
		return ch, true
	}
	rec := s.byID[e.id]
	if !s.visibleTo(rec, c.retailer, now) {
		return obx.Change{}, false
	}
	return n.changeFor(r, c, rec, e.seq, obx.OpUpsert, rec.protoAt), true
}

// ---- catalog reads

func (n *Node) handleBrands(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	now := n.now()
	out := obx.Items[obx.Brand]{Items: []obx.Brand{}}
	for _, b := range sortedByID(n.st.brands) {
		if n.st.visibleTo(b, c.retailer, now) {
			out.Items = append(out.Items, n.st.brandRepr(b))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (n *Node) handleBrand(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	b := n.st.byID[r.PathValue("id")]
	if b == nil || b.kind != obx.KindBrand || !n.st.visibleTo(b, c.retailer, n.now()) {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	writeJSON(w, http.StatusOK, n.st.brandRepr(b))
}

func (n *Node) handleProducts(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	s := n.st
	q := r.URL.Query()
	limit := 25
	if v := q.Get("limit"); v != "" {
		l, err := strconv.Atoi(v)
		if err != nil || l < 1 {
			writeProblem(w, problem(obx.ErrInvalidRequest, "limit must be 1-100"))
			return
		}
		limit = min(l, 100)
	}
	var ids map[string]bool
	if v := q.Get("ids"); v != "" {
		ids = map[string]bool{}
		for _, id := range strings.Split(v, ",") {
			ids[strings.TrimSpace(id)] = true
		}
	}
	var since *time.Time
	if v := q.Get("updated_since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(w, problem(obx.ErrInvalidRequest, "updated_since must be an RFC 3339 timestamp"))
			return
		}
		since = &t
	}
	after := ""
	if v := q.Get("cursor"); v != "" {
		b, err := base64.RawURLEncoding.DecodeString(v)
		if err != nil {
			writeProblem(w, problem(obx.ErrCursorInvalid, "malformed cursor"))
			return
		}
		after = string(b)
	}
	now := n.now()
	signer := n.signer(r, c)
	page := obx.Page[obx.ProductDetail]{Items: []obx.ProductDetail{}}
	for _, p := range sortedByID(s.products) {
		if p.id <= after || !s.visibleTo(p, c.retailer, now) {
			continue
		}
		if ids != nil && !ids[p.id] {
			continue
		}
		b := s.brands[p.parentExt]
		if bid := q.Get("brand_id"); bid != "" && b.id != bid {
			continue
		}
		if since != nil && p.protoAt.Before(*since) {
			continue
		}
		if len(page.Items) == limit {
			next := base64.RawURLEncoding.EncodeToString([]byte(page.Items[len(page.Items)-1].ID))
			page.NextCursor = &next
			break
		}
		page.Items = append(page.Items, s.productDetail(p, signer, now))
	}
	writeJSON(w, http.StatusOK, page)
}

func (n *Node) productETag(p *record, c *caller, now time.Time) string {
	h := p.protoHash
	for _, k := range []string{obx.KindVariant, obx.KindAsset} {
		for _, ch := range n.st.childrenOf(p.extID, k) {
			if n.st.isVisible(ch, now) {
				h += ch.protoHash
			}
		}
	}
	if c.has(obx.ScopeMediaOriginal) {
		h += "o"
	}
	return `"` + hashBytes([]byte(h))[:24] + `"`
}

func (n *Node) handleProduct(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	now := n.now()
	p := n.st.byID[r.PathValue("id")]
	if p == nil || p.kind != obx.KindProduct || !n.st.visibleTo(p, c.retailer, now) {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	etag := n.productETag(p, c, now)
	w.Header().Set("ETag", etag)
	if noneMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, n.st.productDetail(p, n.signer(r, c), now))
}

// noneMatch evaluates If-None-Match with the weak comparison of RFC 9110
// 13.1.2: W/"x" matches "x". A CDN that compresses a response (Cloudflare)
// turns a strong ETag into a weak one, and clients send that one back.
func noneMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	weak := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "W/") }
	for _, v := range strings.Split(header, ",") {
		if strings.TrimSpace(v) == "*" || weak(v) == weak(etag) {
			return true
		}
	}
	return false
}

// lookupGTINs answers a GTIN lookup for a retailer (also used by the portal).
func (n *Node) lookupGTINs(inputs []string, rt *retailer, signer *urlSigner) obx.GTINLookupResult {
	s := n.st
	now := n.now()
	res := obx.GTINLookupResult{Results: []obx.GTINResult{}, Products: []obx.ProductDetail{}}
	seen := map[string]bool{}
	seenProduct := map[string]bool{}
	for _, in := range inputs {
		g, ok := obx.NormalizeLookupGTIN(in)
		key := g
		if !ok {
			key = "invalid:" + in
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		echo := strings.TrimSpace(in)
		if !ok {
			res.Results = append(res.Results, obx.GTINResult{Input: echo, Status: obx.GTINInvalid})
			continue
		}
		out := obx.GTINResult{Input: echo, GTIN: ptr(g), Status: obx.GTINUnmatched}
		if ext, found := s.gtins[g]; found {
			v := s.variants[ext]
			if s.visibleTo(v, rt, now) {
				p := s.products[v.parentExt]
				out.Status, out.ProductID, out.VariantID = obx.GTINMatched, ptr(p.id), ptr(v.id)
				if !seenProduct[p.id] {
					seenProduct[p.id] = true
					res.Products = append(res.Products, s.productDetail(p, signer, now))
				}
			}
		}
		res.Results = append(res.Results, out)
	}
	return res
}

func (n *Node) handleByGTIN(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	var inputs []string
	if r.Method == http.MethodGet {
		v := r.URL.Query().Get("gtin")
		if v == "" {
			writeProblem(w, problem(obx.ErrInvalidRequest, "gtin is required"))
			return
		}
		inputs = strings.Split(v, ",")
		if len(inputs) > 100 {
			writeProblem(w, problem(obx.ErrLimitExceeded, "at most 100 GTINs per GET; use POST for up to 2000"))
			return
		}
	} else {
		var body struct {
			GTINs []string `json:"gtins"`
		}
		if p := readJSON(r, &body); p != nil {
			writeProblem(w, p)
			return
		}
		if body.GTINs == nil {
			writeProblem(w, problem(obx.ErrValidationFailed, "gtins is required").WithField("/gtins", obx.ErrValidationFailed, "required"))
			return
		}
		if len(body.GTINs) > 2000 {
			writeProblem(w, problem(obx.ErrLimitExceeded, "at most 2000 GTINs per lookup"))
			return
		}
		inputs = body.GTINs
	}
	writeJSON(w, http.StatusOK, n.lookupGTINs(inputs, c.retailer, n.signer(r, c)))
}

func (n *Node) handleAsset(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c, ok := n.authProtocol(w, r, obx.ScopeMediaRead)
	if !ok {
		return
	}
	a := n.st.byID[r.PathValue("id")]
	if a == nil || a.kind != obx.KindAsset || !n.st.visibleTo(a, c.retailer, n.now()) {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	writeJSON(w, http.StatusOK, n.st.assetRepr(a, n.signer(r, c)))
}

func (n *Node) handleRendition(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	id, name := r.PathValue("id"), r.PathValue("name")
	if !obx.Contains(obx.RenditionNames, name) {
		writeProblem(w, problem(obx.ErrNotFound, "unknown rendition"))
		return
	}
	var rt *retailer
	q := r.URL.Query()
	if sig := q.Get("sig"); sig != "" && bearer(r) == "" {
		exp, err := strconv.ParseInt(q.Get("expires"), 10, 64)
		p := r.URL.Path
		if err != nil || !constantEqual(sig, n.sign("GET", p, q.Get("expires"))) || n.now().Unix() > exp {
			writeProblem(w, problem(obx.ErrUnauthorized, "the signed URL is invalid or expired; read the asset again for a fresh URL"))
			return
		}
		a := s.byID[id]
		if a == nil || a.kind != obx.KindAsset || !s.isVisible(a, n.now()) {
			writeProblem(w, problem(obx.ErrNotFound, ""))
			return
		}
	} else {
		c, ok := n.authProtocol(w, r, obx.ScopeMediaRead)
		if !ok {
			return
		}
		rt = c.retailer
		a := s.byID[id]
		if a == nil || a.kind != obx.KindAsset || !s.visibleTo(a, rt, n.now()) {
			writeProblem(w, problem(obx.ErrNotFound, ""))
			return
		}
		if name == obx.RenditionOriginal && !c.has(obx.ScopeMediaOriginal) {
			writeProblem(w, problem(obx.ErrOriginalNotAllowed, "originals are not available to you"))
			return
		}
	}
	a := s.byID[id]
	f := a.files[name]
	b := s.blobs[f.SHA256]
	s.logDownload(downloadEvent{kind: "api_rendition", retailer: rt, variant: name, productID: s.products[a.parentExt].id, assetID: a.id, files: 1, bytes: int64(len(b.data))})
	n.serveBlob(w, r, b, canonicalcsv.RenditionFilename(s.assetRepr(a, nil).Filename, name), "inline")
}

// serveBlob sends bytes with ETag, OBX-Content-SHA256, Range and 304 support.
func (n *Node) serveBlob(w http.ResponseWriter, r *http.Request, b *blob, filename, disposition string) {
	data := b.data
	n.knobs.mu.Lock()
	corrupt := n.knobs.corrupt
	n.knobs.mu.Unlock()
	if corrupt && len(data) > 0 {
		data = append([]byte(nil), data...)
		data[len(data)-1] ^= 0xFF
	}
	w.Header().Set("ETag", `"`+b.sha+`"`)
	w.Header().Set(obx.HeaderContentSHA256, b.sha)
	w.Header().Set("Content-Type", b.contentType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", contentDisposition(disposition, filename))
	http.ServeContent(w, r, "", b.createdAt, bytes.NewReader(data))
}

func contentDisposition(kind, name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, ascii, url.PathEscape(name))
}

// ---- usage

func (n *Node) handleUsage(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.capability(obx.CapUsage) {
		writeProblem(w, problem(obx.ErrNotImplemented, "usage reporting is not offered"))
		return
	}
	c, ok := n.authProtocol(w, r, obx.ScopeUsageWrite)
	if !ok {
		return
	}
	var u obx.Usage
	if p := readJSON(r, &u); p != nil {
		writeProblem(w, p)
		return
	}
	if len(u.Events) > 1000 {
		writeProblem(w, problem(obx.ErrLimitExceeded, "at most 1000 events per call"))
		return
	}
	accepted := 0
	for i, e := range u.Events {
		if !obx.Contains([]string{obx.UsageSynced, obx.UsagePublished, obx.UsageDownloaded, obx.UsageRemoved}, e.Event) {
			writeProblem(w, problemf(obx.ErrValidationFailed, "event %d: unknown event %q", i, e.Event).WithField(fmt.Sprintf("/events/%d/event", i), obx.ErrValidationFailed, "unknown event"))
			return
		}
	}
	for _, e := range u.Events {
		a := n.st.byID[e.AssetID]
		if a == nil || a.kind != obx.KindAsset {
			continue
		}
		accepted++
		count := e.Count
		if count <= 0 {
			count = 1
		}
		n.st.logDownload(downloadEvent{kind: "usage", retailer: c.retailer, usageEvent: e.Event, productID: n.st.products[a.parentExt].id, assetID: a.id, files: count, variant: obx.Str(e.Rendition)})
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": accepted})
}

// ---- exports

type export struct {
	obx.Export
	clientID string
	manifest []byte
	parts    []*blob
}

func (n *Node) handleCreateExport(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.capability(obx.CapExports) {
		writeProblem(w, problem(obx.ErrNotImplemented, "exports are not offered"))
		return
	}
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	s := n.st
	key := r.Header.Get(obx.HeaderIdempotencyKey)
	if key != "" {
		if e, ok := s.idempotency["export:"+c.client.id+":"+key]; ok {
			writeJSON(w, http.StatusAccepted, s.exports[e.ref].Export)
			return
		}
	}
	var in obx.ExportRequest
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if len(in.Renditions) == 0 {
		in.Renditions = []string{obx.RenditionWeb}
	}
	for _, name := range in.Renditions {
		if !obx.Contains(obx.RenditionNames, name) {
			writeProblem(w, problemf(obx.ErrValidationFailed, "unknown rendition %q", name))
			return
		}
		if name == obx.RenditionOriginal && !c.has(obx.ScopeMediaOriginal) {
			writeProblem(w, problem(obx.ErrOriginalNotAllowed, "originals are not available to you"))
			return
		}
		if name != obx.RenditionOriginal && !c.has(obx.ScopeMediaRead) {
			writeProblem(w, problem(obx.ErrInsufficientScope, "exports with files need media:read"))
			return
		}
	}
	e := n.buildExport(r, c, in)
	s.exports[e.ID] = e
	if key != "" {
		s.idempotency["export:"+c.client.id+":"+key] = idemEntry{ref: e.ID, at: n.now()}
	}
	w.Header().Set("Location", n.baseURL(r)+obx.PathAPIBase+"/exports/"+e.ID)
	writeJSON(w, http.StatusAccepted, e.Export)
	n.notifyExport(c.client.id, e)
}

func (n *Node) buildExport(r *http.Request, c *caller, in obx.ExportRequest) *export {
	s := n.st
	now := n.now()
	id := s.newID("exp")
	base := n.baseURL(r)
	includeData := in.IncludeData == nil || *in.IncludeData
	var products []*record
	for _, p := range sortedByID(s.products) {
		if !s.visibleTo(p, c.retailer, now) {
			continue
		}
		if f := in.Filter; f != nil {
			if f.BrandIDs != nil && !obx.Contains(f.BrandIDs, s.brands[p.parentExt].id) {
				continue
			}
			if f.ProductIDs != nil && !obx.Contains(f.ProductIDs, p.id) {
				continue
			}
			if f.UpdatedSince != nil && p.protoAt.Before(*f.UpdatedSince) {
				continue
			}
			if f.GTINs != nil {
				match := false
				for _, g := range f.GTINs {
					if g14, ok := obx.NormalizeLookupGTIN(g); ok {
						if v := s.variants[s.gtins[g14]]; v != nil && v.parentExt == p.extID {
							match = true
						}
					}
				}
				if !match {
					continue
				}
			}
		}
		products = append(products, p)
	}
	var lines [][]byte
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	counts := obx.ExportCounts{}
	var fileLines [][]byte
	brandsSeen := map[string]bool{}
	var brandLines [][]byte
	var body [][]byte
	for _, p := range products {
		b := s.brands[p.parentExt]
		if !brandsSeen[b.id] {
			brandsSeen[b.id] = true
			brandLines = append(brandLines, canonicalJSON(map[string]any{"type": "brand", "data": s.brandRepr(b)}))
		}
		counts.Products++
		if includeData {
			body = append(body, canonicalJSON(map[string]any{"type": "product", "data": s.productRepr(p)}))
		}
		for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
			if s.isVisible(v, now) {
				counts.Variants++
				if includeData {
					body = append(body, canonicalJSON(map[string]any{"type": "variant", "data": s.variantRepr(v)}))
				}
			}
		}
		for _, a := range s.childrenOf(p.extID, obx.KindAsset) {
			if !s.isVisible(a, now) {
				continue
			}
			counts.Assets++
			repr := s.assetRepr(a, &urlSigner{base: base, originalAllowed: c.has(obx.ScopeMediaOriginal), expires: now.Add(time.Hour), n: n})
			if includeData {
				body = append(body, canonicalJSON(map[string]any{"type": "asset", "data": repr}))
			}
			for _, name := range in.Renditions {
				f := a.files[name]
				bl := s.blobs[f.SHA256]
				fname := canonicalcsv.RenditionFilename(repr.Filename, name)
				zpath := safeZipName(p.product.StyleNumber) + "/" + name + "/" + safeZipName(fname)
				fw, _ := zw.CreateHeader(&zip.FileHeader{Name: zpath, Method: zip.Store, Modified: now})
				fw.Write(bl.data)
				counts.Files++
				fileLines = append(fileLines, canonicalJSON(obx.ExportManifestLine{Type: "file", AssetID: a.id, Rendition: name, Part: 1, Path: zpath, Bytes: int64(len(bl.data)), SHA256: bl.sha}))
			}
		}
	}
	zw.Close()
	cursor := n.encodeCursor(cursorData{R: c.retailer.id, P: obx.PhaseLive, S: s.seq})
	header := obx.ExportManifestLine{Type: "header", ExportID: id, CreatedAt: &now, ObxVersion: obx.Version, ChangesCursor: cursor, Counts: &counts}
	lines = append(lines, canonicalJSON(header))
	if includeData {
		lines = append(lines, brandLines...)
	}
	lines = append(lines, body...)
	lines = append(lines, fileLines...)
	manifest := append(bytes.Join(lines, []byte("\n")), '\n')
	part, _ := s.putBlob(zipBuf.Bytes(), "application/zip", 0, 0)
	exp := now.Add(48 * time.Hour)
	e := &export{clientID: c.client.id, manifest: manifest, parts: []*blob{part}}
	e.Export = obx.Export{ID: id, Status: obx.JobSucceeded, CreatedAt: now, FinishedAt: &now, ExpiresAt: &exp,
		Progress: obx.Progress{Done: counts.Files, Total: ptr(counts.Files)}, ManifestURL: ptr(base + obx.PathAPIBase + "/exports/" + id + "/manifest"),
		Parts: []obx.ExportPart{{Number: 1, URL: base + obx.PathAPIBase + "/exports/" + id + "/parts/1", Bytes: int64(len(part.data)), SHA256: part.sha, Files: counts.Files}}}
	s.logDownload(downloadEvent{kind: "api_export", retailer: c.retailer, files: counts.Files, bytes: int64(len(part.data))})
	return e
}

// safeZipName removes path separators and dot segments from a zip entry part.
func safeZipName(s string) string {
	s = strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(s)
	if s == "." || s == ".." || s == "" {
		return "_"
	}
	return s
}

func (n *Node) ownedExport(w http.ResponseWriter, r *http.Request, scope string) (*export, *caller, bool) {
	c, ok := n.authProtocol(w, r, scope)
	if !ok {
		return nil, nil, false
	}
	e := n.st.exports[r.PathValue("id")]
	if e == nil || e.clientID != c.client.id {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return nil, nil, false
	}
	if e.ExpiresAt != nil && n.now().After(*e.ExpiresAt) {
		writeProblem(w, problem(obx.ErrExportExpired, "this export expired; start a new one"))
		return nil, nil, false
	}
	return e, c, true
}

func (n *Node) handleGetExport(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.capability(obx.CapExports) {
		writeProblem(w, problem(obx.ErrNotImplemented, "exports are not offered"))
		return
	}
	if e, _, ok := n.ownedExport(w, r, obx.ScopeCatalogRead); ok {
		writeJSON(w, http.StatusOK, e.Export)
	}
}

func (n *Node) handleExportManifest(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.capability(obx.CapExports) {
		writeProblem(w, problem(obx.ErrNotImplemented, "exports are not offered"))
		return
	}
	if e, _, ok := n.ownedExport(w, r, obx.ScopeCatalogRead); ok {
		w.Header().Set("Content-Type", obx.MediaTypeNDJSON)
		w.Write(e.manifest)
	}
}

func (n *Node) handleExportPart(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.capability(obx.CapExports) {
		writeProblem(w, problem(obx.ErrNotImplemented, "exports are not offered"))
		return
	}
	e, _, ok := n.ownedExport(w, r, obx.ScopeMediaRead)
	if !ok {
		return
	}
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || num < 1 || num > len(e.parts) {
		writeProblem(w, problem(obx.ErrNotFound, "no such part"))
		return
	}
	n.serveBlob(w, r, e.parts[num-1], fmt.Sprintf("export-%s-part-%d.zip", e.ID, num), "attachment")
}
