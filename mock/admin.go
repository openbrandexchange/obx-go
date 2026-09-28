package mock

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// user is an admin or retailer account.
type user struct {
	id, email, name, role, retailerID, status, language string
	passwordHash                                        []byte
	lastLogin                                           *time.Time
	inviteExpires                                       *time.Time
	createdAt                                           time.Time
}

type session struct {
	userID  string
	csrf    string
	expires time.Time
}

type invite struct {
	userID  string
	expires time.Time
	used    bool
}

// bcryptCost is the password hashing cost (security.md: ≥ 10).
const bcryptCost = 10

func (u *user) view() nodeadmin.User {
	v := nodeadmin.User{ID: u.id, Email: u.email, Name: u.name, Role: u.role, Status: u.status, Language: u.language, LastLoginAt: u.lastLogin, InviteExpiresAt: u.inviteExpires, CreatedAt: u.createdAt}
	if u.retailerID != "" {
		v.RetailerID = ptr(u.retailerID)
	}
	return v
}

// adminCtx is the authenticated admin/portal caller.
type adminCtx struct {
	user    *user
	session *session
	sid     string
}

func (a *adminCtx) isAdmin() bool { return a != nil && a.user.role == nodeadmin.RoleAdmin }

const (
	cookieSession = "obx_session"
	cookieCSRF    = "obx_csrf"
)

func secureCookie(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: secureCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

// currentSession returns the logged-in user, if any. Holds n.mu.
func (n *Node) currentSession(r *http.Request) *adminCtx {
	c, err := r.Cookie(cookieSession)
	if err != nil {
		return nil
	}
	sid := hashSecret(c.Value)
	sess := n.st.sessions[sid]
	if sess == nil || n.now().After(sess.expires) {
		return nil
	}
	u := n.st.users[sess.userID]
	if u == nil || u.status != "active" {
		return nil
	}
	if u.retailerID != "" {
		if rt := n.st.retailers[u.retailerID]; rt == nil || rt.status != "active" {
			return nil
		}
	}
	sess.expires = n.now().Add(30 * 24 * time.Hour)
	return &adminCtx{user: u, session: sess, sid: sid}
}

// checkCSRF implements the double-submit check for state-changing requests.
func (n *Node) checkCSRF(w http.ResponseWriter, r *http.Request, ac *adminCtx) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	h := r.Header.Get(obx.HeaderCSRF)
	c, err := r.Cookie(cookieCSRF)
	ok := h != "" && err == nil && constantEqual(h, c.Value)
	if ok && ac != nil && ac.session.csrf != "" {
		ok = constantEqual(h, ac.session.csrf)
	}
	if !ok {
		writeProblem(w, problem(obx.ErrCSRFFailed, "missing or wrong X-CSRF-Token; read /admin/api/session first"))
		return false
	}
	return true
}

// admin wraps an admin API handler: setup check, session, role and CSRF.
// role is "admin", "any" (admin or retailer) or "" (public).
func (n *Node) admin(role string, h func(w http.ResponseWriter, r *http.Request, ac *adminCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		if !n.st.setupDone {
			writeProblem(w, problem(obx.ErrSetupRequired, "finish the first-run setup first"))
			return
		}
		ac := n.currentSession(r)
		if role != "" && ac == nil {
			writeProblem(w, problem(obx.ErrUnauthorized, "please log in"))
			return
		}
		if role == nodeadmin.RoleAdmin && !ac.isAdmin() {
			writeProblem(w, problem(obx.ErrForbidden, "only the brand's admins can do this"))
			return
		}
		if !n.checkCSRF(w, r, ac) {
			return
		}
		n.st.tick()
		h(w, r, ac)
	}
}

func (n *Node) sessionView(ac *adminCtx, csrf string) nodeadmin.Session {
	s := nodeadmin.Session{CSRFToken: csrf, SetupRequired: !n.st.setupDone}
	if ac != nil {
		u := ac.user.view()
		s.Authenticated, s.User = true, &u
		if rt := n.st.retailers[ac.user.retailerID]; rt != nil {
			s.Retailer = &nodeadmin.IDName{ID: rt.id, Name: rt.name}
		}
	}
	return s
}

// ensureCSRF returns the CSRF cookie value, creating one when needed.
func ensureCSRF(w http.ResponseWriter, r *http.Request, ac *adminCtx) string {
	if ac != nil && ac.session.csrf != "" {
		setCookie(w, r, cookieCSRF, ac.session.csrf, 30*24*3600)
		return ac.session.csrf
	}
	if c, err := r.Cookie(cookieCSRF); err == nil && len(c.Value) >= 20 {
		return c.Value
	}
	t := randomToken("", 24)
	setCookie(w, r, cookieCSRF, t, 30*24*3600)
	return t
}

// login starts a session (rotating the session id and CSRF token).
func (n *Node) login(w http.ResponseWriter, r *http.Request, u *user) nodeadmin.Session {
	if c, err := r.Cookie(cookieSession); err == nil {
		delete(n.st.sessions, hashSecret(c.Value))
	}
	sid := randomToken("", 32)
	csrf := randomToken("", 24)
	sess := &session{userID: u.id, csrf: csrf, expires: n.now().Add(30 * 24 * time.Hour)}
	n.st.sessions[hashSecret(sid)] = sess
	setCookie(w, r, cookieSession, sid, 30*24*3600)
	setCookie(w, r, cookieCSRF, csrf, 30*24*3600)
	now := n.now()
	u.lastLogin = &now
	return n.sessionView(&adminCtx{user: u, session: sess}, csrf)
}

func (n *Node) endSessionsOf(userID string) {
	for k, s := range n.st.sessions {
		if s.userID == userID {
			delete(n.st.sessions, k)
		}
	}
}

// ---- public, session, setup

func (n *Node) publicBranding(r *http.Request) nodeadmin.PublicBranding {
	s := n.st
	b := nodeadmin.PublicBranding{Name: s.settings.displayName, AccentColor: s.settings.accent, WelcomeText: nilIfEmpty(s.settings.welcome),
		PortalName: nilIfEmpty(s.settings.portalName),
		Languages:  s.settings.languages, DefaultLanguage: s.settings.defaultLang, AccessRequestsEnabled: s.settings.accessRequests,
		SetupRequired: !s.setupDone, Brands: []nodeadmin.IDName{}}
	if s.settings.logo != nil {
		b.LogoURL = ptr(n.baseURL(r) + obx.PathAdminAPI + "/public/logo")
	}
	now := n.now()
	for _, br := range sortedByID(s.brands) {
		if s.isVisible(br, now) {
			b.Brands = append(b.Brands, nodeadmin.IDName{ID: br.id, Name: br.brand.Name})
		}
	}
	return b
}

func (n *Node) handlePublicBranding(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	writeJSON(w, http.StatusOK, n.publicBranding(r))
}

func (n *Node) handlePublicLogo(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.st.settings.logo == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	w.Header().Set("Content-Type", n.st.settings.logoType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(n.st.settings.logo)
}

func (n *Node) handleGetSetup(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"required": !n.st.setupDone})
}

func (n *Node) handleSession(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	var ac *adminCtx
	if n.st.setupDone {
		ac = n.currentSession(r)
	}
	writeJSON(w, http.StatusOK, n.sessionView(ac, ensureCSRF(w, r, ac)))
}

func (n *Node) handleSetup(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	if s.setupDone {
		writeProblem(w, problem(obx.ErrSetupCompleted, "this node is already set up"))
		return
	}
	if !n.checkCSRF(w, r, nil) {
		return
	}
	var in nodeadmin.SetupInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if !constantEqual(in.SetupCode, n.opts.SetupCode) {
		writeProblem(w, problem(obx.ErrForbidden, "the setup code is wrong; find it in the node's log or in OBX_SETUP_CODE"))
		return
	}
	if p := validatePassword(in.Admin.Password); p != nil {
		writeProblem(w, p.WithField("/admin/password", obx.ErrValidationFailed, "10-72 characters"))
		return
	}
	if strings.TrimSpace(in.Company.Name) == "" || !strings.Contains(in.Admin.Email, "@") || strings.TrimSpace(in.Admin.Name) == "" {
		writeProblem(w, problem(obx.ErrValidationFailed, "company name, admin name and e-mail are required"))
		return
	}
	u, p := n.completeSetup(in)
	if p != nil {
		writeProblem(w, p)
		return
	}
	writeJSON(w, http.StatusCreated, n.login(w, r, u))
}

// completeSetup applies the wizard (also used by the fixture loader).
func (n *Node) completeSetup(in nodeadmin.SetupInput) (*user, *obx.Problem) {
	s := n.st
	langs := in.Company.Languages
	def := in.Company.DefaultLanguage
	if def == "" {
		def = "en"
	}
	if len(langs) == 0 {
		langs = []string{def}
	}
	if !obx.Contains(langs, def) {
		langs = append([]string{def}, langs...)
	}
	s.settings.companyName = in.Company.Name
	s.settings.displayName = in.Company.Name
	if in.Company.Website != "" && webURL(in.Company.Website) {
		s.settings.website = ptr(in.Company.Website)
	}
	if in.Company.ContactEmail != "" {
		s.settings.contactEmail = ptr(in.Company.ContactEmail)
	}
	if in.Company.AccentColor != "" {
		s.settings.accent = ptr(in.Company.AccentColor)
	}
	s.settings.languages, s.settings.defaultLang = langs, def
	s.setupDone = true
	brandName := in.Company.FirstBrandName
	if brandName == "" {
		brandName = in.Company.Name
	}
	if _, p := s.apply(writer{}, obx.IngestOpUpsert, obx.KindBrand, obx.Slug(brandName), canonicalJSON(obx.BrandInput{Name: brandName}), ""); p != nil {
		s.setupDone = false
		return nil, p
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(in.Admin.Password), bcryptCost)
	u := &user{id: s.newID("usr"), email: strings.ToLower(strings.TrimSpace(in.Admin.Email)), name: in.Admin.Name, role: nodeadmin.RoleAdmin, status: "active",
		language: def, passwordHash: hash, createdAt: n.now()}
	s.users[u.id] = u
	return u, nil
}

func validatePassword(p string) *obx.Problem {
	if len(p) < 10 || len(p) > 72 {
		return problem(obx.ErrValidationFailed, "the password must be 10-72 characters")
	}
	for _, common := range []string{"password12", "1234567890", "qwertyuiop", "password123", "welcome123"} {
		if strings.EqualFold(p, common) {
			return problem(obx.ErrValidationFailed, "this password is too common")
		}
	}
	return nil
}

func (n *Node) userByEmail(email string) *user {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, u := range n.st.users {
		if u.email == email {
			return u
		}
	}
	return nil
}

func (n *Node) handleLogin(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	u := n.userByEmail(in.Email)
	if u == nil || u.passwordHash == nil || bcrypt.CompareHashAndPassword(u.passwordHash, []byte(in.Password)) != nil {
		writeProblem(w, problem(obx.ErrInvalidCredentials, "wrong e-mail or password"))
		return
	}
	if u.status == "blocked" {
		writeProblem(w, problem(obx.ErrAccountBlocked, "this account is blocked"))
		return
	}
	if rt := n.st.retailers[u.retailerID]; u.retailerID != "" && (rt == nil || rt.status != "active") {
		writeProblem(w, problem(obx.ErrAccountBlocked, "access for your company is blocked"))
		return
	}
	writeJSON(w, http.StatusOK, n.login(w, r, u))
}

func (n *Node) handleLogout(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	if ac != nil {
		delete(n.st.sessions, ac.sid)
	}
	setCookie(w, r, cookieSession, "", -1)
	setCookie(w, r, cookieCSRF, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (n *Node) newInvite(r *http.Request, u *user, kind string) nodeadmin.InviteResult {
	tok := randomToken("", 24)
	exp := n.now().Add(7 * 24 * time.Hour)
	if kind == "password_reset" {
		exp = n.now().Add(time.Hour)
		n.st.resets[hashSecret(tok)] = &invite{userID: u.id, expires: exp}
	} else {
		n.st.invites[hashSecret(tok)] = &invite{userID: u.id, expires: exp}
		u.inviteExpires = &exp
	}
	path := "/invite/"
	if kind == "password_reset" {
		path = "/reset-password/"
	}
	link := n.baseURL(r) + path + tok
	n.email(u.email, kind, u.language, link, nil)
	return nodeadmin.InviteResult{User: u.view(), InviteURL: link, ExpiresAt: exp, EmailSent: true}
}

func (n *Node) findInvite(tok string, table map[string]*invite) (*invite, *user) {
	inv := table[hashSecret(tok)]
	if inv == nil || inv.used || n.now().After(inv.expires) {
		return nil, nil
	}
	u := n.st.users[inv.userID]
	if u == nil {
		return nil, nil
	}
	return inv, u
}

func (n *Node) handleGetInvite(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	inv, u := n.findInvite(r.PathValue("token"), n.st.invites)
	if inv == nil {
		writeProblem(w, problem(obx.ErrInviteInvalid, "this invitation link is no longer valid; ask for a new one"))
		return
	}
	info := nodeadmin.InviteInfo{Email: u.email, Role: u.role, ExpiresAt: inv.expires}
	if u.name != "" {
		info.Name = ptr(u.name)
	}
	if rt := n.st.retailers[u.retailerID]; rt != nil {
		info.RetailerName = ptr(rt.name)
	}
	writeJSON(w, http.StatusOK, info)
}

func (n *Node) handleAcceptInvite(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Language string `json:"language"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	inv, u := n.findInvite(r.PathValue("token"), n.st.invites)
	if inv == nil {
		writeProblem(w, problem(obx.ErrInviteInvalid, "this invitation link is no longer valid; ask for a new one"))
		return
	}
	if p := validatePassword(in.Password); p != nil {
		writeProblem(w, p)
		return
	}
	inv.used = true
	u.passwordHash, _ = bcrypt.GenerateFromPassword([]byte(in.Password), bcryptCost)
	u.status, u.inviteExpires = "active", nil
	if in.Name != "" {
		u.name = in.Name
	}
	if in.Language != "" {
		u.language = in.Language
	}
	writeJSON(w, http.StatusOK, n.login(w, r, u))
}

func (n *Node) handleForgot(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	var in struct {
		Email string `json:"email"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if u := n.userByEmail(in.Email); u != nil && u.status == "active" {
		n.newInvite(r, u, "password_reset")
	}
	w.WriteHeader(http.StatusAccepted)
}

func (n *Node) handleReset(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	inv, u := n.findInvite(in.Token, n.st.resets)
	if inv == nil {
		writeProblem(w, problem(obx.ErrInviteInvalid, "this reset link is no longer valid"))
		return
	}
	if p := validatePassword(in.Password); p != nil {
		writeProblem(w, p)
		return
	}
	inv.used = true
	u.passwordHash, _ = bcrypt.GenerateFromPassword([]byte(in.Password), bcryptCost)
	n.endSessionsOf(u.id)
	writeJSON(w, http.StatusOK, n.login(w, r, u))
}

func (n *Node) handleMe(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	var in struct {
		Name            *string `json:"name"`
		Language        *string `json:"language"`
		CurrentPassword string  `json:"current_password"`
		NewPassword     string  `json:"new_password"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	u := ac.user
	if in.NewPassword != "" {
		if bcrypt.CompareHashAndPassword(u.passwordHash, []byte(in.CurrentPassword)) != nil {
			writeProblem(w, problem(obx.ErrInvalidCredentials, "the current password is wrong"))
			return
		}
		if p := validatePassword(in.NewPassword); p != nil {
			writeProblem(w, p)
			return
		}
		u.passwordHash, _ = bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcryptCost)
		n.endSessionsOf(u.id)
		if in.Name != nil {
			u.name = *in.Name
		}
		if in.Language != nil {
			u.language = *in.Language
		}
		writeJSON(w, http.StatusOK, n.login(w, r, u))
		return
	}
	if in.Name != nil {
		u.name = *in.Name
	}
	if in.Language != nil {
		u.language = *in.Language
	}
	writeJSON(w, http.StatusOK, n.sessionView(ac, ac.session.csrf))
}

// ---- settings

func (n *Node) settingsView(r *http.Request) nodeadmin.Settings {
	st := n.st.settings
	base := n.baseURL(r)
	out := nodeadmin.Settings{
		Company:  &nodeadmin.SettingsCompany{Name: st.companyName, Website: st.website, ContactEmail: st.contactEmail, Country: st.country},
		Branding: &nodeadmin.SettingsBranding{DisplayName: st.displayName, AccentColor: st.accent, WelcomeText: st.welcome, PortalName: st.portalName},
		Portal: &nodeadmin.SettingsPortal{AccessRequestsEnabled: ptr(st.accessRequests), AllowOriginalDownload: ptr(st.allowOriginal),
			AllowArticleDataExport: ptr(st.allowDataExport), RetailerAPISelfService: ptr(st.apiSelfService), Languages: st.languages, DefaultLanguage: st.defaultLang},
		Defaults: &nodeadmin.SettingsDefaults{Rights: ptr(st.defaultRights), IngestStatus: st.defaultIngestStatus},
		Node: &nodeadmin.SettingsNode{PublicURL: base, DiscoveryURL: base + obx.PathDiscovery, Software: "obx-go mock", Version: obx.SpecVersion,
			EmailConfigured: false, RenditionsServerGenerated: !n.opts.ClientRenditions, MaxUploadBytes: n.opts.MaxUploadBytes,
			CustomDomainHelp: "The mock node answers on any host name."},
	}
	if st.logo != nil {
		out.Branding.LogoURL = ptr(base + obx.PathAdminAPI + "/public/logo")
	}
	return out
}

func (n *Node) handleSettings(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, n.settingsView(r))
		return
	}
	var in nodeadmin.Settings
	body, p := readJSONBody(r, &in)
	if p != nil {
		writeProblem(w, p)
		return
	}
	// The update is a JSON merge patch (RFC 7396): the localised texts are
	// merged per language from the raw body, where null removes a language
	// (or, for the whole field, the whole text).
	var raw struct {
		Branding map[string]json.RawMessage `json:"branding"`
	}
	json.Unmarshal(body, &raw)
	mergeText := func(cur obx.LocalizedText, key string) obx.LocalizedText {
		v, ok := raw.Branding[key]
		if !ok {
			return cur
		}
		if strings.TrimSpace(string(v)) == "null" {
			return nil
		}
		var patch map[string]*string
		if json.Unmarshal(v, &patch) != nil {
			return cur
		}
		out := obx.LocalizedText{}
		for k, t := range cur {
			out[k] = t
		}
		for k, t := range patch {
			if t == nil || strings.TrimSpace(*t) == "" {
				delete(out, k)
			} else {
				out[k] = *t
			}
		}
		return nilIfEmpty(out)
	}
	if c := in.Company; c != nil && c.Website != nil && *c.Website != "" && !webURL(*c.Website) {
		writeProblem(w, problem(obx.ErrValidationFailed, "company.website must be an http(s) address"))
		return
	}
	s := n.st
	st := &s.settings
	origBefore := map[string]bool{}
	for id, rt := range s.retailers {
		origBefore[id] = s.originalAllowed(rt)
	}
	if c := in.Company; c != nil {
		if c.Name != "" {
			st.companyName = c.Name
		}
		if c.Website != nil {
			st.website = c.Website
		}
		if c.ContactEmail != nil {
			st.contactEmail = c.ContactEmail
		}
		if c.Country != nil {
			st.country = c.Country
		}
	}
	if b := in.Branding; b != nil {
		if b.DisplayName != "" {
			st.displayName = b.DisplayName
		}
		if b.AccentColor != nil {
			st.accent = b.AccentColor
		}
	}
	st.welcome = mergeText(st.welcome, "welcome_text")
	st.portalName = mergeText(st.portalName, "portal_name")
	if p := in.Portal; p != nil {
		if p.AccessRequestsEnabled != nil {
			st.accessRequests = *p.AccessRequestsEnabled
		}
		if p.AllowOriginalDownload != nil {
			st.allowOriginal = *p.AllowOriginalDownload
		}
		if p.AllowArticleDataExport != nil {
			st.allowDataExport = *p.AllowArticleDataExport
		}
		if p.RetailerAPISelfService != nil {
			st.apiSelfService = *p.RetailerAPISelfService
		}
		if len(p.Languages) > 0 {
			st.languages = p.Languages
		}
		if p.DefaultLanguage != "" {
			st.defaultLang = p.DefaultLanguage
		}
	}
	if d := in.Defaults; d != nil {
		if d.Rights != nil {
			st.defaultRights = *d.Rights
		}
		if d.IngestStatus != "" {
			st.defaultIngestStatus = d.IngestStatus
		}
	}
	for id, rt := range s.retailers {
		if origBefore[id] != s.originalAllowed(rt) {
			s.grantChanged(rt, true)
		}
	}
	s.dirtyAll = true // default rights and publication status apply to every asset
	s.commit()
	writeJSON(w, http.StatusOK, n.settingsView(r))
}

func (n *Node) handleLogo(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	st := &n.st.settings
	if r.Method == http.MethodDelete {
		st.logo, st.logoType = nil, ""
		writeJSON(w, http.StatusOK, n.settingsView(r))
		return
	}
	data, p := readBody(r, 2<<20)
	if p != nil {
		writeProblem(w, p)
		return
	}
	ct := http.DetectContentType(data)
	if ct != "image/png" && ct != "image/jpeg" && ct != "image/webp" {
		writeProblem(w, problem(obx.ErrUnsupportedMediaType, "the logo must be PNG, JPEG or WebP"))
		return
	}
	st.logo, st.logoType = data, ct
	writeJSON(w, http.StatusOK, n.settingsView(r))
}

// ---- retailers

func (n *Node) retailerView(rt *retailer) nodeadmin.Retailer {
	v := nodeadmin.Retailer{ID: rt.id, Name: rt.name, Reference: rt.reference, Website: rt.website, VATNumber: rt.vat, Country: rt.country, Notes: rt.notes,
		Status: rt.status, BrandIDs: rt.brandIDs, AllowOriginal: rt.allowOriginal, LastActivityAt: rt.lastActivity, CreatedAt: rt.createdAt}
	for _, u := range n.st.users {
		if u.retailerID == rt.id {
			v.UserCount++
		}
	}
	for _, c := range n.st.clients {
		if c.retailerID == rt.id && c.status == "active" {
			v.APIClientCount++
		}
	}
	return v
}

func (n *Node) newRetailer(name string) *retailer {
	s := n.st
	rt := &retailer{id: s.newID("rtl"), name: name, status: "active", createdAt: n.now()}
	rt.grantID = "grt_" + strings.TrimPrefix(rt.id, "rtl_")
	rt.grantUpdatedAt = rt.createdAt
	s.retailers[rt.id] = rt
	return rt
}

// patchRetailer applies a merge patch to a retailer.
func (n *Node) patchRetailer(rt *retailer, raw json.RawMessage) *obx.Problem {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return problem(obx.ErrInvalidRequest, "the body must be a JSON object")
	}
	s := n.st
	grantBefore := canonicalJSON(s.grantOf(rt))
	origBefore := s.originalAllowed(rt)
	brandsBefore := canonicalJSON(rt.brandIDs)
	str := func(key string, dst **string) {
		if v, ok := m[key]; ok {
			var x *string
			json.Unmarshal(v, &x)
			*dst = x
		}
	}
	if v, ok := m["name"]; ok {
		var name string
		json.Unmarshal(v, &name)
		if strings.TrimSpace(name) == "" {
			return problem(obx.ErrValidationFailed, "name is required")
		}
		rt.name = name
	}
	str("reference", &rt.reference)
	str("website", &rt.website)
	if rt.website != nil && *rt.website != "" && !webURL(*rt.website) {
		return problem(obx.ErrValidationFailed, "website must be an http(s) address")
	}
	str("vat_number", &rt.vat)
	str("country", &rt.country)
	str("notes", &rt.notes)
	if v, ok := m["brand_ids"]; ok {
		var ids []string
		json.Unmarshal(v, &ids)
		for _, id := range ids {
			if b := s.byID[id]; b == nil || b.kind != obx.KindBrand {
				return problemf(obx.ErrValidationFailed, "unknown brand %s", id)
			}
		}
		rt.brandIDs = ids
	}
	if v, ok := m["allow_original"]; ok {
		var b *bool
		json.Unmarshal(v, &b)
		rt.allowOriginal = b
	}
	if v, ok := m["status"]; ok {
		var st string
		json.Unmarshal(v, &st)
		if st != "active" && st != "blocked" {
			return problem(obx.ErrValidationFailed, "status must be active or blocked")
		}
		rt.status = st
		if st == "blocked" {
			for _, u := range s.users {
				if u.retailerID == rt.id {
					n.endSessionsOf(u.id)
				}
			}
		}
	}
	if string(grantBefore) != string(canonicalJSON(s.grantOf(rt))) {
		resync := origBefore != s.originalAllowed(rt) || string(brandsBefore) != string(canonicalJSON(rt.brandIDs))
		s.grantChanged(rt, resync)
	}
	return nil
}

func (n *Node) handleRetailers(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	if r.Method == http.MethodGet {
		q := strings.ToLower(r.URL.Query().Get("q"))
		st := r.URL.Query().Get("status")
		var items []any
		for _, id := range sortedKeys(s.retailers) {
			rt := s.retailers[id]
			if (q != "" && !strings.Contains(strings.ToLower(rt.name), q)) || (st != "" && rt.status != st) {
				continue
			}
			items = append(items, n.retailerView(rt))
		}
		writePaged(w, r, items, 50, 200)
		return
	}
	raw, p := readRaw(r)
	if p != nil {
		writeProblem(w, p)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	json.Unmarshal(raw, &in)
	if strings.TrimSpace(in.Name) == "" {
		writeProblem(w, problem(obx.ErrValidationFailed, "name is required").WithField("/name", obx.ErrValidationFailed, "required"))
		return
	}
	rt := n.newRetailer(in.Name)
	if p := n.patchRetailer(rt, raw); p != nil {
		delete(s.retailers, rt.id)
		writeProblem(w, p)
		return
	}
	writeJSON(w, http.StatusCreated, n.retailerView(rt))
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (n *Node) handleRetailer(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	rt := s.retailers[r.PathValue("id")]
	if rt == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, n.retailerView(rt))
	case http.MethodPatch:
		raw, p := readRaw(r)
		if p != nil {
			writeProblem(w, p)
			return
		}
		if p := n.patchRetailer(rt, raw); p != nil {
			writeProblem(w, p)
			return
		}
		writeJSON(w, http.StatusOK, n.retailerView(rt))
	case http.MethodDelete:
		rt.status = "blocked"
		for _, c := range s.clients {
			if c.retailerID == rt.id {
				c.status = "revoked"
			}
		}
		for id, u := range s.users {
			if u.retailerID == rt.id {
				n.endSessionsOf(u.id)
				delete(s.users, id)
			}
		}
		delete(s.retailers, rt.id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- access requests (admin)

func (n *Node) adminAccessRequest(ar *accessRequest) nodeadmin.AccessRequest {
	v := nodeadmin.AccessRequest{ID: ar.id, Status: ar.status, CreatedAt: ar.createdAt, DecidedAt: ar.decidedAt, Reason: ar.reason,
		Retailer: ar.in.Retailer, Contact: ar.in.Contact, Access: ar.access(), RequestedBrandIDs: ar.in.RequestedBrandIDs, Message: ar.in.Message}
	if ar.retailerID != "" {
		v.RetailerID = ptr(ar.retailerID)
	}
	return v
}

func (n *Node) handleAccessRequests(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	st := r.URL.Query().Get("status")
	var items []any
	for _, id := range sortedKeys(n.st.accessRequests) {
		ar := n.st.accessRequests[id]
		if st == "" || ar.status == st {
			items = append(items, n.adminAccessRequest(ar))
		}
	}
	writePaged(w, r, items, 50, 200)
}

func (n *Node) handleAccessRequest(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	ar := n.st.accessRequests[r.PathValue("id")]
	if ar == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	writeJSON(w, http.StatusOK, n.adminAccessRequest(ar))
}

func (n *Node) handleApprove(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	ar := s.accessRequests[r.PathValue("id")]
	if ar == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	if ar.status != "pending" {
		writeProblem(w, problemf(obx.ErrConflict, "this request is already %s", ar.status))
		return
	}
	var in nodeadmin.ApproveInput
	raw, p := readRaw(r)
	if p != nil {
		raw = []byte("{}")
	}
	json.Unmarshal(raw, &in)
	var rt *retailer
	if in.RetailerID != "" {
		if rt = s.retailers[in.RetailerID]; rt == nil {
			writeProblem(w, problem(obx.ErrValidationFailed, "unknown retailer"))
			return
		}
	} else {
		rt = n.newRetailer(ar.in.Retailer.Name)
		rt.reference, rt.website, rt.vat, rt.country = ar.in.Retailer.Reference, ar.in.Retailer.Website, ar.in.Retailer.VATNumber, ar.in.Retailer.Country
		var brands []string
		if len(ar.in.RequestedBrandIDs) > 0 {
			brands = ar.in.RequestedBrandIDs
		}
		var m map[string]json.RawMessage
		json.Unmarshal(raw, &m)
		if v, ok := m["brand_ids"]; ok {
			brands = nil
			json.Unmarshal(v, &brands)
		}
		rt.brandIDs = brands
	}
	now := n.now()
	ar.status, ar.decidedAt, ar.retailerID = "approved", &now, rt.id
	out := nodeadmin.ApproveResult{Retailer: n.retailerView(rt)}
	wantPortal := obx.Contains(ar.access(), "portal")
	if in.InvitePortalUser != nil {
		wantPortal = *in.InvitePortalUser
	}
	wantAPI := obx.Contains(ar.access(), "api")
	if in.CreateAPIClient != nil {
		wantAPI = *in.CreateAPIClient
	}
	if wantPortal {
		u := n.userByEmail(ar.in.Contact.Email)
		if u == nil {
			u = &user{id: s.newID("usr"), email: strings.ToLower(ar.in.Contact.Email), name: ar.in.Contact.Name, role: nodeadmin.RoleRetailer, retailerID: rt.id, status: "invited", language: ar.language, createdAt: now}
			s.users[u.id] = u
		}
		inv := n.newInvite(r, u, "invite")
		out.Invite = &inv
		ar.invited = true
	}
	if wantAPI {
		c, secret := n.newClient(rt, "API key for "+rt.name, nil)
		ar.clientID, ar.secret = c.id, secret
		v := n.clientView(c)
		out.APIClient = &v
	}
	n.email(ar.in.Contact.Email, "access_approved", ar.language, n.baseURL(r)+"/login", nil)
	out.AccessRequest = n.adminAccessRequest(ar)
	out.Retailer = n.retailerView(rt)
	writeJSON(w, http.StatusOK, out)
}

func (n *Node) handleReject(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	ar := n.st.accessRequests[r.PathValue("id")]
	if ar == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	if ar.status != "pending" {
		writeProblem(w, problemf(obx.ErrConflict, "this request is already %s", ar.status))
		return
	}
	var in struct {
		Reason *string `json:"reason"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	now := n.now()
	ar.status, ar.decidedAt, ar.reason = "rejected", &now, in.Reason
	n.email(ar.in.Contact.Email, "access_rejected", ar.language, "", nil)
	writeJSON(w, http.StatusOK, n.adminAccessRequest(ar))
}

// ---- users

func (n *Node) handleUsers(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	q := r.URL.Query()
	var items []any
	for _, id := range sortedKeys(n.st.users) {
		u := n.st.users[id]
		if (q.Get("role") != "" && u.role != q.Get("role")) || (q.Get("retailer_id") != "" && u.retailerID != q.Get("retailer_id")) {
			continue
		}
		items = append(items, u.view())
	}
	writePaged(w, r, items, 50, 200)
}

func (n *Node) handleInviteUser(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	var in nodeadmin.InviteInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if !strings.Contains(in.Email, "@") || (in.Role != nodeadmin.RoleAdmin && in.Role != nodeadmin.RoleRetailer) {
		writeProblem(w, problem(obx.ErrValidationFailed, "a valid e-mail and a role (admin or retailer) are required"))
		return
	}
	if in.Role == nodeadmin.RoleRetailer && n.st.retailers[in.RetailerID] == nil {
		writeProblem(w, problem(obx.ErrValidationFailed, "retailer_id is required for retailer users").WithField("/retailer_id", obx.ErrValidationFailed, "unknown retailer"))
		return
	}
	if n.userByEmail(in.Email) != nil {
		writeProblem(w, problem(obx.ErrConflict, "there is already an account with this e-mail"))
		return
	}
	lang := in.Language
	if lang == "" {
		lang = n.st.settings.defaultLang
	}
	u := &user{id: n.st.newID("usr"), email: strings.ToLower(strings.TrimSpace(in.Email)), name: in.Name, role: in.Role, status: "invited", language: lang, createdAt: n.now()}
	if in.Role == nodeadmin.RoleRetailer {
		u.retailerID = in.RetailerID
	}
	n.st.users[u.id] = u
	writeJSON(w, http.StatusCreated, n.newInvite(r, u, "invite"))
}

func (n *Node) handleUser(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	u := n.st.users[r.PathValue("id")]
	if u == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	if r.Method == http.MethodDelete {
		if u.id == ac.user.id {
			writeProblem(w, problem(obx.ErrConflict, "you cannot delete your own account"))
			return
		}
		n.endSessionsOf(u.id)
		delete(n.st.users, u.id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var in struct {
		Name   *string `json:"name"`
		Status *string `json:"status"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if in.Name != nil {
		u.name = *in.Name
	}
	if in.Status != nil {
		if u.id == ac.user.id {
			writeProblem(w, problem(obx.ErrConflict, "you cannot block yourself"))
			return
		}
		if *in.Status != "active" && *in.Status != "blocked" {
			writeProblem(w, problem(obx.ErrValidationFailed, "status must be active or blocked"))
			return
		}
		u.status = *in.Status
		if u.status == "blocked" {
			n.endSessionsOf(u.id)
		}
	}
	writeJSON(w, http.StatusOK, u.view())
}

func (n *Node) handleResendInvite(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	u := n.st.users[r.PathValue("id")]
	if u == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	if u.status != "invited" {
		writeProblem(w, problem(obx.ErrConflict, "this user already accepted the invitation"))
		return
	}
	writeJSON(w, http.StatusOK, n.newInvite(r, u, "invite"))
}

// ---- API clients

func (n *Node) clientView(c *apiClient) nodeadmin.APIClient {
	return nodeadmin.APIClient{ID: c.id, ClientID: c.clientID, Name: c.name, RetailerID: c.retailerID, Scopes: c.scopes, Status: c.status, CreatedAt: c.createdAt, LastUsedAt: c.lastUsedAt}
}

func (n *Node) newClient(rt *retailer, name string, scopes []string) (*apiClient, string) {
	if scopes == nil {
		scopes = []string{obx.ScopeCatalogRead, obx.ScopeMediaRead}
		if n.st.originalAllowed(rt) {
			scopes = append(scopes, obx.ScopeMediaOriginal)
		}
	}
	secret := randomToken(obx.PrefixClientSecret, 32)
	c := &apiClient{id: n.st.newID("cli"), clientID: randomToken(obx.PrefixClientID, 12), secretHash: hashSecret(secret), name: name, retailerID: rt.id, scopes: scopes, status: "active", createdAt: n.now()}
	n.st.clients[c.id] = c
	return c, secret
}

func (n *Node) withSecret(r *http.Request, c *apiClient, secret string) nodeadmin.APIClientWithSecret {
	base := n.baseURL(r)
	return nodeadmin.APIClientWithSecret{Client: n.clientView(c), ClientSecret: secret, TokenEndpoint: base + obx.PathAPIBase + "/oauth/token", DiscoveryURL: base + obx.PathDiscovery}
}

func validScopes(scopes []string) bool {
	for _, s := range scopes {
		if !obx.Contains(obx.AllScopes, s) {
			return false
		}
	}
	return true
}

func (n *Node) handleAPIClients(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	if r.Method == http.MethodGet {
		out := obx.Items[nodeadmin.APIClient]{Items: []nodeadmin.APIClient{}}
		for _, id := range sortedKeys(s.clients) {
			c := s.clients[id]
			if rid := r.URL.Query().Get("retailer_id"); rid != "" && c.retailerID != rid {
				continue
			}
			out.Items = append(out.Items, n.clientView(c))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var in nodeadmin.APIClientInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	rt := s.retailers[in.RetailerID]
	if rt == nil || strings.TrimSpace(in.Name) == "" || !validScopes(in.Scopes) {
		writeProblem(w, problem(obx.ErrValidationFailed, "a retailer, a name and valid scopes are required"))
		return
	}
	if obx.Contains(in.Scopes, obx.ScopeMediaOriginal) && !s.originalAllowed(rt) {
		writeProblem(w, problem(obx.ErrValidationFailed, "originals are not allowed for this retailer").WithField("/scopes", obx.ErrValidationFailed, "media:original not allowed"))
		return
	}
	var scopes []string
	if len(in.Scopes) > 0 {
		scopes = in.Scopes
	}
	c, secret := n.newClient(rt, in.Name, scopes)
	writeJSON(w, http.StatusCreated, n.withSecret(r, c, secret))
}

func (n *Node) handleAPIClient(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	c := n.st.clients[r.PathValue("id")]
	if c == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	n.clientAction(w, r, c)
}

func (n *Node) clientAction(w http.ResponseWriter, r *http.Request, c *apiClient) {
	if r.Method == http.MethodDelete {
		c.status = "revoked"
		w.WriteHeader(http.StatusNoContent)
		return
	}
	secret := randomToken(obx.PrefixClientSecret, 32)
	c.secretHash = hashSecret(secret)
	for k, t := range n.st.accessTokens {
		if t.clientID == c.id {
			delete(n.st.accessTokens, k)
		}
	}
	writeJSON(w, http.StatusOK, n.withSecret(r, c, secret))
}

// ---- ingest tokens

func (n *Node) tokenView(t *ingestToken) nodeadmin.IngestToken {
	st := t.status
	if st == "active" && t.expiresAt != nil && n.now().After(*t.expiresAt) {
		st = "expired"
	}
	return nodeadmin.IngestToken{ID: t.id, Name: t.name, Prefix: t.prefix, Scopes: t.scopes, BrandExternalIDs: t.brands, Status: st, ExpiresAt: t.expiresAt, CreatedAt: t.createdAt, LastUsedAt: t.lastUsedAt}
}

func (n *Node) newIngestToken(name string, scopes, brands []string, expires *time.Time) (*ingestToken, string) {
	secret := randomToken(obx.PrefixIngestToken, 32)
	t := &ingestToken{id: n.st.newID("tok"), name: name, prefix: secret[:12], secretHash: hashSecret(secret), scopes: scopes, brands: brands, status: "active", expiresAt: expires, createdAt: n.now()}
	n.st.ingestTokens[t.id] = t
	return t, secret
}

func (n *Node) handleIngestTokens(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	if r.Method == http.MethodGet {
		out := obx.Items[nodeadmin.IngestToken]{Items: []nodeadmin.IngestToken{}}
		for _, id := range sortedKeys(s.ingestTokens) {
			out.Items = append(out.Items, n.tokenView(s.ingestTokens[id]))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var in nodeadmin.IngestTokenInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Scopes) == 0 {
		writeProblem(w, problem(obx.ErrValidationFailed, "a name and at least one scope are required"))
		return
	}
	for _, sc := range in.Scopes {
		if sc != obx.IngestScopeRead && sc != obx.IngestScopeWrite && sc != obx.IngestScopeDelete {
			writeProblem(w, problemf(obx.ErrValidationFailed, "unknown scope %s", sc))
			return
		}
	}
	for _, b := range in.BrandExternalIDs {
		if s.brands[b] == nil {
			writeProblem(w, problemf(obx.ErrValidationFailed, "unknown brand %s", b))
			return
		}
	}
	t, secret := n.newIngestToken(in.Name, in.Scopes, in.BrandExternalIDs, in.ExpiresAt)
	writeJSON(w, http.StatusCreated, nodeadmin.IngestTokenWithSecret{Token: n.tokenView(t), Secret: secret, IngestURL: n.baseURL(r) + obx.PathIngest})
}

func (n *Node) handleIngestToken(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	t := n.st.ingestTokens[r.PathValue("id")]
	if t == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	if r.Method == http.MethodDelete {
		t.status = "revoked"
		w.WriteHeader(http.StatusNoContent)
		return
	}
	secret := randomToken(obx.PrefixIngestToken, 32)
	t.secretHash, t.prefix = hashSecret(secret), secret[:12]
	writeJSON(w, http.StatusOK, nodeadmin.IngestTokenWithSecret{Token: n.tokenView(t), Secret: secret, IngestURL: n.baseURL(r) + obx.PathIngest})
}

// ---- admin webhooks

var adminEvents = []string{obx.EventAccessRequestCreated, obx.EventImportRunCompleted, obx.EventIngestJobCompleted}

func (n *Node) handleAdminWebhooks(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	if r.Method == http.MethodGet {
		out := obx.Items[obx.WebhookSubscription]{Items: []obx.WebhookSubscription{}}
		for _, s := range n.sortedSubs("admin") {
			out.Items = append(out.Items, s.public(false))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var in obx.WebhookInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	sub, p := n.createSub("admin", in, adminEvents, 20)
	if p != nil {
		writeProblem(w, p)
		return
	}
	writeJSON(w, http.StatusCreated, sub.public(true))
}

func (n *Node) handleAdminWebhook(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	sub := n.st.webhooks[r.PathValue("id")]
	if sub == nil || sub.owner != "admin" {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	switch r.Method {
	case http.MethodDelete:
		delete(n.st.webhooks, sub.id)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		var in struct {
			Events []string `json:"events"`
			Status string   `json:"status"`
		}
		if p := readJSON(r, &in); p != nil {
			writeProblem(w, p)
			return
		}
		if in.Events != nil {
			sub.events = in.Events
		}
		if in.Status == "active" || in.Status == "disabled" {
			sub.status = in.Status
		}
		writeJSON(w, http.StatusOK, sub.public(false))
	}
}

func (n *Node) handleAdminWebhookTest(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	sub := n.st.webhooks[r.PathValue("id")]
	ac := n.currentSession(r)
	if !ac.isAdmin() || !n.checkCSRF(w, r, ac) {
		n.mu.Unlock()
		if !ac.isAdmin() {
			writeProblem(w, problem(obx.ErrUnauthorized, "please log in as an admin"))
		}
		return
	}
	if sub == nil || sub.owner != "admin" {
		n.mu.Unlock()
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	cp := *sub
	n.mu.Unlock()
	d := n.send(cp, obx.EventWebhookTest, map[string]any{})
	n.mu.Lock()
	n.recordDelivery(cp.id, d)
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, d)
}

func (n *Node) handleAdminWebhookDeliveries(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	sub := n.st.webhooks[r.PathValue("id")]
	if sub == nil || sub.owner != "admin" {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	var items []any
	for _, d := range n.st.deliveries[sub.id] {
		items = append(items, d)
	}
	writePaged(w, r, items, 50, 200)
}

// ---- stats

type downloadEvent struct {
	id         string
	at         time.Time
	kind       string
	retailer   *retailer
	actorType  string
	actorName  string
	variant    string
	productID  string
	assetID    string
	usageEvent string
	files      int
	bytes      int64
}

// logDownload records a download for the brand's statistics. Downloads
// without a retailer (admin previews, signed URLs) are not counted.
func (s *state) logDownload(e downloadEvent) {
	if e.retailer == nil && e.kind != "usage" {
		return
	}
	e.id = s.newID("dl")
	e.at = s.n.now()
	if e.actorType == "" {
		e.actorType = "api_client"
	}
	s.events = append(s.events, e)
}

func (n *Node) handleStats(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	days := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 {
		days = min(v, 366)
	}
	since := n.now().AddDate(0, 0, -days)
	st := nodeadmin.Stats{Days: days, ByDay: []nodeadmin.StatsDay{}, TopRetailers: []nodeadmin.StatsRetailer{}, TopProducts: []nodeadmin.StatsProduct{}}
	byDay := map[string]*nodeadmin.StatsDay{}
	byRetailer := map[string]*nodeadmin.StatsRetailer{}
	byProduct := map[string]*nodeadmin.StatsProduct{}
	active := map[string]bool{}
	for _, e := range s.events {
		if e.at.Before(since) {
			continue
		}
		if e.kind == "usage" {
			st.Totals.UsageEvents++
			continue
		}
		st.Totals.Downloads++
		images := e.files
		switch e.kind {
		case "csv":
			st.Totals.CSVExports++
			images = 0
		case "product_zip", "selection_zip", "bulk_zip":
			st.Totals.Zips++
		case "api_rendition", "api_export":
			st.Totals.APIDownloads++
		}
		st.Totals.Images += images
		day := e.at.Format("2006-01-02")
		if byDay[day] == nil {
			byDay[day] = &nodeadmin.StatsDay{Date: day}
		}
		byDay[day].Downloads++
		byDay[day].Images += images
		if e.retailer != nil {
			active[e.retailer.id] = true
			sr := byRetailer[e.retailer.id]
			if sr == nil {
				sr = &nodeadmin.StatsRetailer{RetailerID: e.retailer.id, Name: e.retailer.name}
				byRetailer[e.retailer.id] = sr
			}
			sr.Downloads++
			sr.Images += images
			at := e.at
			sr.LastActivityAt = &at
		}
		if e.productID != "" {
			sp := byProduct[e.productID]
			if sp == nil {
				sp = &nodeadmin.StatsProduct{ProductID: e.productID}
				if p := s.byID[e.productID]; p != nil {
					sp.StyleNumber, sp.Name = p.product.StyleNumber, p.product.Name
				}
				byProduct[e.productID] = sp
			}
			sp.Downloads++
		}
	}
	st.Totals.ActiveRetailers = len(active)
	for _, k := range sortedKeys(byDay) {
		st.ByDay = append(st.ByDay, *byDay[k])
	}
	for _, v := range byRetailer {
		st.TopRetailers = append(st.TopRetailers, *v)
	}
	sort.Slice(st.TopRetailers, func(i, j int) bool { return st.TopRetailers[i].Downloads > st.TopRetailers[j].Downloads })
	for _, v := range byProduct {
		st.TopProducts = append(st.TopProducts, *v)
	}
	sort.Slice(st.TopProducts, func(i, j int) bool { return st.TopProducts[i].Downloads > st.TopProducts[j].Downloads })
	st.TopRetailers = st.TopRetailers[:min(20, len(st.TopRetailers))]
	st.TopProducts = st.TopProducts[:min(20, len(st.TopProducts))]
	writeJSON(w, http.StatusOK, st)
}

func (n *Node) handleStatsEvents(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	q := r.URL.Query()
	var items []any
	for i := len(n.st.events) - 1; i >= 0; i-- {
		e := n.st.events[i]
		if q.Get("kind") != "" && e.kind != q.Get("kind") {
			continue
		}
		if q.Get("retailer_id") != "" && (e.retailer == nil || e.retailer.id != q.Get("retailer_id")) {
			continue
		}
		v := nodeadmin.DownloadEvent{ID: e.id, At: e.at, Kind: e.kind, ActorType: e.actorType, Files: e.files}
		if e.retailer != nil {
			v.RetailerID, v.RetailerName = ptr(e.retailer.id), ptr(e.retailer.name)
		}
		if e.actorName != "" {
			v.ActorName = ptr(e.actorName)
		}
		if e.variant != "" {
			v.Variant = ptr(e.variant)
		}
		if e.productID != "" {
			v.ProductID = ptr(e.productID)
		}
		if e.assetID != "" {
			v.AssetID = ptr(e.assetID)
		}
		if e.usageEvent != "" {
			v.UsageEvent = ptr(e.usageEvent)
		}
		if e.bytes > 0 {
			v.Bytes = ptr(e.bytes)
		}
		items = append(items, v)
	}
	writePaged(w, r, items, 50, 200)
}

var _ = url.PathEscape

// webURL reports whether s is an absolute http(s) URL: the admin UI shows
// websites as links, so javascript: and data: URLs are refused.
func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}
