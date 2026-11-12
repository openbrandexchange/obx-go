// Package mock is an in-memory OBX node for developing and testing
// consumers, brand automation and the conformance suite itself. It speaks the
// OBX Protocol, the Ingest API and enough of the Node Admin + Portal API to
// pass every conformance profile, and it has knobs for the edge cases real
// consumers must survive: duplicate change entries, expired cursors, revoked
// access, changed bytes and rate limiting.
//
//	n := mock.New(mock.Options{Fixtures: true})
//	srv := httptest.NewServer(n)
//	creds := n.Credentials() // API keys, upload key, admin login
//
// Everything lives in memory; nothing is persisted.
package mock

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

// Options configure a mock node.
type Options struct {
	// Fixtures completes setup and imports the Kestrel Outfitters fixtures
	// (spec fixtures/kestrel) and creates the credentials returned by
	// Credentials. Without it the node starts fresh and needs setup.
	Fixtures bool
	// SetupCode is the one-time setup code (default "obx-setup-code").
	SetupCode string
	// PublicURL overrides the base URL derived from each request.
	PublicURL string
	// ClientRenditions makes the node behave like a node without an image
	// library: server_generated=false, clients upload web/square/thumb.
	ClientRenditions bool
	// DryRunNeedsBlobs makes a dry-run import report blob_missing for images
	// it was not sent, like a portal that only plans uploaded bytes.
	DryRunNeedsBlobs bool
	// AllowPrivateURLs allows loopback and private webhook/import URLs and
	// plain http (like OBX_ALLOW_PRIVATE_URLS=true). Link-local addresses
	// (cloud metadata) stay refused.
	AllowPrivateURLs bool
	// RateLimit is the number of protocol requests per client per minute
	// (default 600).
	RateLimit int
	// Capabilities replaces the advertised optional capabilities; endpoints
	// of capabilities not listed answer 501. Nil = everything.
	Capabilities []string
	// MaxUploadBytes (default 100 MiB) and PartBytes (default 8 MiB).
	MaxUploadBytes int64
	PartBytes      int64
	// Logger receives one line per request when set.
	Logger *log.Logger
	// Clock replaces time.Now (for expiry tests).
	Clock func() time.Time
	// ReadOnly serves the node as a public demo: only the protocol's reads
	// are answered (GET, HEAD and OPTIONS, the token request and the GTIN
	// lookup). Every other protocol request that would change state
	// (access requests, exports, usage events, webhooks) answers 403, and
	// the ingest API, the node admin API and the portal answer 404. The
	// data stays as loaded; a restart resets the node.
	ReadOnly bool
	// FullClientID and FullClientSecret fix the unrestricted fixture API
	// key (Credentials().Full), so that a public demo keeps its published
	// key across restarts. Both or neither.
	FullClientID, FullClientSecret string
}

// Node is an in-memory OBX node. It implements http.Handler.
type Node struct {
	opts   Options
	mux    *http.ServeMux
	key    []byte
	nodeID string

	mu    sync.Mutex
	st    *state
	creds Credentials

	knobs knobs
}

// knobs are edge-case switches (see the Set* methods).
type knobs struct {
	mu              sync.Mutex
	duplicate       bool
	corrupt         bool
	force429        int
	retryAfter      int
	failNext5xx     int
	snapshotMaxAge  time.Duration
	webhookClient   *http.Client
	accessRateLimit int
}

// New creates a node.
func New(opts Options) *Node {
	if opts.SetupCode == "" {
		opts.SetupCode = "obx-setup-code"
	}
	if opts.RateLimit <= 0 {
		opts.RateLimit = 600
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 100 << 20
	}
	if opts.PartBytes <= 0 {
		opts.PartBytes = 8 << 20
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	n := &Node{opts: opts, key: randomBytes(32), nodeID: "node-" + randomToken("", 6)} // the prefix keeps the id matching the Id pattern (a random token may start with - or _)
	n.knobs.snapshotMaxAge = 24 * time.Hour
	n.knobs.accessRateLimit = 5
	n.st = newState(n)
	n.routes()
	if opts.Fixtures {
		if err := n.loadFixtures(); err != nil {
			panic("mock: loading fixtures: " + err.Error())
		}
	}
	return n
}

func (n *Node) now() time.Time { return n.opts.Clock().UTC().Truncate(time.Millisecond) }

// SetupCode returns the one-time setup code.
func (n *Node) SetupCode() string { return n.opts.SetupCode }

// SetDuplicateChanges makes /changes return every live entry twice
// (at-least-once delivery).
func (n *Node) SetDuplicateChanges(on bool) {
	n.knobs.mu.Lock()
	n.knobs.duplicate = on
	n.knobs.mu.Unlock()
}

// SetCorruptDownloads makes rendition downloads return bytes that do not
// match the announced sha256.
func (n *Node) SetCorruptDownloads(on bool) {
	n.knobs.mu.Lock()
	n.knobs.corrupt = on
	n.knobs.mu.Unlock()
}

// Force429 makes the next count API requests answer 429 with Retry-After.
func (n *Node) Force429(count, retryAfterSeconds int) {
	n.knobs.mu.Lock()
	n.knobs.force429, n.knobs.retryAfter = count, retryAfterSeconds
	n.knobs.mu.Unlock()
}

// Fail5xx makes the next count API requests answer 503.
func (n *Node) Fail5xx(count int) {
	n.knobs.mu.Lock()
	n.knobs.failNext5xx = count
	n.knobs.mu.Unlock()
}

// SetWebhookHTTPClient sets the client used for webhook deliveries.
func (n *Node) SetWebhookHTTPClient(c *http.Client) {
	n.knobs.mu.Lock()
	n.knobs.webhookClient = c
	n.knobs.mu.Unlock()
}

// SetAccessRequestLimit changes the per-IP hourly access-request limit
// (default 5).
func (n *Node) SetAccessRequestLimit(perHour int) {
	n.knobs.mu.Lock()
	n.knobs.accessRateLimit = perHour
	n.knobs.mu.Unlock()
}

// ExpireCursors makes every cursor handed out so far answer 410
// cursor_expired.
func (n *Node) ExpireCursors() {
	n.mu.Lock()
	n.st.cursorEpoch++
	n.mu.Unlock()
}

// SimulateSpecUpgrade does to the cursors what an upgrade to a spec version
// with incompatible shapes (0.2 → 0.3) does (protocol.md 6.4): it bumps the
// cursor epoch, so every cursor issued before answers 410 cursor_expired and
// consumers restart without one. The conformance operator action
// simulate_spec_upgrade (P-FEED-7) calls it.
func (n *Node) SimulateSpecUpgrade() { n.ExpireCursors() }

// RevokeClient revokes an API client by its client_id (obxc_…).
func (n *Node) RevokeClient(clientID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.st.clients {
		if c.clientID == clientID {
			c.status = "revoked"
			return true
		}
	}
	return false
}

// SetRetailerBrands changes a retailer's grant (nil = all brands) and writes
// a grant entry with resync:true.
func (n *Node) SetRetailerBrands(retailerID string, brandIDs []string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	r := n.st.retailers[retailerID]
	if r == nil {
		return fmt.Errorf("unknown retailer %s", retailerID)
	}
	r.brandIDs = brandIDs
	n.st.grantChanged(r, true)
	return nil
}

// Email is a message the node would have sent.
type Email struct {
	To       string
	Kind     string // invite, access_request, access_approved, access_rejected, zip_ready, password_reset
	Language string
	Subject  string
	Link     string
	Body     string
	At       time.Time
}

// Outbox returns the e-mails the node "sent".
func (n *Node) Outbox() []Email {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Email(nil), n.st.outbox...)
}

// ServeHTTP implements http.Handler.
func (n *Node) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w.Header().Set(obx.HeaderVersion, obx.Version)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	iw := &interceptWriter{ResponseWriter: w}
	if n.opts.ReadOnly && !n.readOnlyAllowed(iw, r) {
		if n.opts.Logger != nil {
			n.opts.Logger.Printf("%s %s -> %d refused (read-only)", r.Method, r.URL.Path, iw.status)
		}
		return
	}
	n.mux.ServeHTTP(iw, r)
	if n.opts.Logger != nil {
		n.opts.Logger.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, iw.status, time.Since(start).Round(time.Millisecond))
	}
}

// readOnlyAllowed decides a request of a ReadOnly node; when it is refused,
// the answer has been written.
func (n *Node) readOnlyAllowed(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	ingest := p == obx.PathIngest || strings.HasPrefix(p, obx.PathIngest+"/")
	if ingest || p != obx.PathDiscovery && p != obx.PathAPIBase && !strings.HasPrefix(p, obx.PathAPIBase+"/") {
		writeProblem(w, problem(obx.ErrNotFound, "this is a read-only demo node: only the OBX protocol API is served"))
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if r.Method == http.MethodPost && (p == obx.PathAPIBase+"/oauth/token" || p == obx.PathAPIBase+"/products:by-gtin") {
		return true
	}
	if _, pattern := n.mux.Handler(r); pattern == "" {
		return true // no route: the mux answers 404 or 405
	}
	writeProblem(w, problem(obx.ErrForbidden, "this is a read-only demo node: requests that change data are refused"))
	return false
}

// interceptWriter turns the plain-text 404/405 answers of http.ServeMux into
// problem+json.
type interceptWriter struct {
	http.ResponseWriter
	status  int
	swallow bool
}

func (w *interceptWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	ct := w.Header().Get("Content-Type")
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) && strings.HasPrefix(ct, "text/plain") {
		w.swallow = true
		code := obx.ErrNotFound
		if status == http.StatusMethodNotAllowed {
			code = obx.ErrMethodNotAllowed
		}
		p := obx.NewProblem(code, "")
		w.Header().Set("Content-Type", obx.MediaTypeProblem)
		w.Header().Del("X-Content-Type-Options")
		w.ResponseWriter.WriteHeader(status)
		json.NewEncoder(w.ResponseWriter).Encode(p)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *interceptWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.swallow {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *interceptWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// baseURL returns the public base URL for r.
func (n *Node) baseURL(r *http.Request) string {
	if n.opts.PublicURL != "" {
		return strings.TrimRight(n.opts.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

// ---- responses

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeProblem(w http.ResponseWriter, p *obx.Problem) {
	if p.Status == 0 {
		p.Status = obx.StatusFor(p.ObxError)
	}
	w.Header().Set("Content-Type", obx.MediaTypeProblem)
	if p.Status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="obx"`)
	}
	if p.RetryAfter != nil {
		w.Header().Set("Retry-After", strconv.Itoa(*p.RetryAfter))
	}
	w.WriteHeader(p.Status)
	json.NewEncoder(w).Encode(p)
}

func problem(code, detail string) *obx.Problem { return obx.NewProblem(code, detail) }

func problemf(code, format string, args ...any) *obx.Problem {
	return obx.Problemf(code, format, args...)
}

// readJSON decodes a JSON body (max 10 MiB) into v.
func readJSON(r *http.Request, v any) *obx.Problem {
	_, p := readJSONBody(r, v)
	return p
}

// readJSONBody is readJSON that also returns the raw body.
func readJSONBody(r *http.Request, v any) ([]byte, *obx.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20+1))
	if err != nil {
		return nil, problem(obx.ErrInvalidRequest, "could not read the body")
	}
	if len(body) > 10<<20 {
		return nil, problem(obx.ErrPayloadTooLarge, "body larger than 10 MiB")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return nil, problemf(obx.ErrInvalidRequest, "body is not valid JSON: %v", err)
	}
	return body, nil
}

// ---- ids and secrets

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// randomToken returns prefix + n random bytes in base64url.
func randomToken(prefix string, n int) string {
	return prefix + base64.RawURLEncoding.EncodeToString(randomBytes(n))
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (n *Node) sign(parts ...string) string {
	m := hmac.New(sha256.New, n.key)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

func constantEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func ptr[T any](v T) *T { return &v }
