// Package conformance is the OBX conformance suite (docs/conformance.md): it
// drives a node through the OBX Protocol, the Ingest API and the Node Admin +
// Portal API and reports one result per test id. cmd/obx-conformance is its
// command-line front end; node implementations can also call Run from their
// own tests.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/internal/httpx"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

// Profiles.
const (
	ProfileProtocol = "protocol"
	ProfileIngest   = "ingest"
	ProfilePortal   = "portal"
	ProfileAll      = "all"
)

// Config describes the node under test and the credentials to use.
type Config struct {
	// BaseURL of the node (protocol and ingest).
	BaseURL string
	// AdminURL of the admin API when it differs from BaseURL.
	AdminURL string
	// AdminEmail/AdminPassword log in to the admin API (lets the suite create
	// retailers, API keys and upload keys itself).
	AdminEmail    string
	AdminPassword string
	// SetupCode completes first-run setup on a fresh node.
	SetupCode string
	// API keys: an unrestricted one (with originals), one restricted to brand
	// "kestrel", and one without originals. Created via the admin API when
	// empty and admin credentials are given.
	ClientID, ClientSecret                     string
	SecondClientID, SecondClientSecret         string
	NoOriginalClientID, NoOriginalClientSecret string
	// IngestToken is an upload key with all ingest scopes.
	IngestToken string
	// Fixtures overrides the embedded spec fixtures (a spec "fixtures" dir).
	Fixtures fs.FS
	// HTTPClient for all calls (default 2 minute timeout).
	HTTPClient *http.Client
	// Only runs tests whose id starts with one of these prefixes.
	Only []string
	// RateLimitTest hammers an endpoint until it answers 429 (use with a
	// node configured with a low limit).
	RateLimitTest bool
	// Log receives progress lines (optional).
	Log io.Writer
}

// Status of a test.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// Result is the outcome of one test.
type Result struct {
	ID       string   `json:"id"`
	Profile  string   `json:"profile"`
	Title    string   `json:"title"`
	Required bool     `json:"required"`
	Status   string   `json:"status"`
	Messages []string `json:"messages,omitempty"`
	Millis   int64    `json:"duration_ms"`
}

// Report is the outcome of a run.
type Report struct {
	BaseURL    string    `json:"base_url"`
	Profiles   []string  `json:"profiles"`
	Software   string    `json:"software,omitempty"`
	SpecCommit string    `json:"spec_commit"`
	StartedAt  time.Time `json:"started_at"`
	Millis     int64     `json:"duration_ms"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Results    []Result  `json:"results"`
	Setup      []string  `json:"setup,omitempty"`
	// SetupError is set when the suite could not prepare the run (no
	// connection, wrong credentials); every test then fails or is skipped.
	SetupError string `json:"setup_error,omitempty"`
}

// OK reports whether every required test passed.
func (r *Report) OK() bool {
	for _, res := range r.Results {
		if res.Required && res.Status == StatusFail {
			return false
		}
	}
	return true
}

// testCase is one registered test.
type testCase struct {
	id       string
	phase    int
	profile  string
	title    string
	required bool
	run      func(*T)
}

// T is handed to each test.
type T struct {
	r      *runner
	ctx    context.Context
	res    *Result
	failed bool
}

type abort struct{}

type skipTest struct{ msg string }

// Errorf records a failure and continues.
func (t *T) Errorf(format string, args ...any) {
	t.failed = true
	msg := fmt.Sprintf(format, args...)
	if len(t.res.Messages) < 40 {
		t.res.Messages = append(t.res.Messages, msg)
	} else if len(t.res.Messages) == 40 {
		t.res.Messages = append(t.res.Messages, "… more problems omitted")
	}
}

// Fatalf records a failure and stops the test.
func (t *T) Fatalf(format string, args ...any) {
	t.Errorf(format, args...)
	panic(abort{})
}

// Skip stops the test and marks it skipped.
func (t *T) Skip(format string, args ...any) { panic(skipTest{fmt.Sprintf(format, args...)}) }

// Notef adds an informational message.
func (t *T) Notef(format string, args ...any) {
	t.res.Messages = append(t.res.Messages, "note: "+fmt.Sprintf(format, args...))
}

// Check fails the test with msg when err is not nil.
func (t *T) Check(err error, what string) {
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// runner holds the state shared by the tests of one run.
type runner struct {
	cfg      Config
	fixtures fs.FS
	report   *Report
	hc       *http.Client // records problem+json violations
	raw      *httpx.Doer  // no retries

	admin     *nodeadmin.Client
	full      *apiKey
	second    *apiKey
	noOrig    *apiKey
	ing       *ingest.Client
	fullCreds [2]string

	discovery *obx.DiscoveryDocument
	info      *obx.IngestInfo

	// ids of fixture objects (external id → protocol id), from the full client
	productIDs map[string]string
	assetIDs   map[string]string
	brandIDs   map[string]string
	products   map[string]*obx.ProductDetail

	setup              *setupNotes
	privateURLsAllowed *bool
	replayChanges      []string // P-FEED-5 input from I-REPLAY-1
	replayRan          bool
	portalClient       *nodeadmin.Client
	portalRetailerRec  *nodeadmin.Retailer

	mu         sync.Mutex
	violations []string
}

// recorder checks every error answer for problem+json (P-ERR-1).
type recorder struct {
	next http.RoundTripper
	r    *runner
}

func (rt recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.next.RoundTrip(req)
	if err != nil || resp.StatusCode < 400 || req.Method == http.MethodHead {
		return resp, err
	}
	p := req.URL.Path
	if !(strings.HasPrefix(p, obx.PathAPIBase) || strings.HasPrefix(p, obx.PathAdminAPI) || p == obx.PathDiscovery) || strings.HasSuffix(p, "/oauth/token") {
		return resp, err
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if rerr != nil {
		return resp, err
	}
	var prob obx.Problem
	ct := resp.Header.Get("Content-Type")
	switch {
	case !strings.HasPrefix(ct, obx.MediaTypeProblem):
		rt.r.violation(fmt.Sprintf("%s %s → %d with Content-Type %q (want application/problem+json)", req.Method, p, resp.StatusCode, ct))
	case json.Unmarshal(body, &prob) != nil || prob.ObxError == "":
		rt.r.violation(fmt.Sprintf("%s %s → %d without obx_error", req.Method, p, resp.StatusCode))
	case !obx.KnownErrorCode(prob.ObxError):
		rt.r.violation(fmt.Sprintf("%s %s → %d with unknown obx_error %q", req.Method, p, resp.StatusCode, prob.ObxError))
	case prob.Status != resp.StatusCode:
		rt.r.violation(fmt.Sprintf("%s %s → %d but problem.status %d", req.Method, p, resp.StatusCode, prob.Status))
	}
	return resp, err
}

func (r *runner) violation(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.violations) < 50 {
		r.violations = append(r.violations, s)
	}
}

func (r *runner) logf(format string, args ...any) {
	if r.cfg.Log != nil {
		fmt.Fprintf(r.cfg.Log, format+"\n", args...)
	}
}

// Run executes the given profiles ("protocol", "ingest", "portal" or "all").
func Run(ctx context.Context, profiles []string, cfg Config) (*Report, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("conformance: the node URL is required")
	}
	base, err := client.NodeBase(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	cfg.BaseURL = base
	if cfg.AdminURL == "" {
		cfg.AdminURL = base
	}
	want := map[string]bool{}
	for _, p := range profiles {
		switch p {
		case ProfileAll:
			want[ProfileProtocol], want[ProfileIngest], want[ProfilePortal] = true, true, true
		case ProfileProtocol, ProfileIngest, ProfilePortal:
			want[p] = true
		default:
			return nil, fmt.Errorf("conformance: unknown profile %q (use protocol, ingest, portal or all)", p)
		}
	}
	r := &runner{cfg: cfg, fixtures: cfg.Fixtures, report: &Report{BaseURL: base, SpecCommit: specdata.SpecCommit(), StartedAt: time.Now().UTC()}}
	if r.fixtures == nil {
		r.fixtures = specdata.Fixtures()
	}
	for _, p := range []string{ProfileProtocol, ProfileIngest, ProfilePortal} {
		if want[p] {
			r.report.Profiles = append(r.report.Profiles, p)
		}
	}
	inner := http.DefaultTransport
	timeout := 2 * time.Minute
	if cfg.HTTPClient != nil {
		if cfg.HTTPClient.Transport != nil {
			inner = cfg.HTTPClient.Transport
		}
		if cfg.HTTPClient.Timeout != 0 {
			timeout = cfg.HTTPClient.Timeout
		}
	}
	r.hc = &http.Client{Transport: recorder{next: inner, r: r}, Timeout: timeout}
	r.raw = &httpx.Doer{HTTP: r.hc, MaxRetries: -1}
	start := time.Now()
	setupErr := r.bootstrap(ctx, want)
	if setupErr != nil {
		r.report.SetupError = setupErr.Error()
	}
	for _, tc := range registry() {
		if !want[tc.profile] || !r.selected(tc.id) {
			continue
		}
		res := Result{ID: tc.id, Profile: tc.profile, Title: tc.title, Required: tc.required}
		if setupErr != nil {
			res.Status = StatusFail
			res.Messages = []string{"setup failed: " + setupErr.Error()}
			if !tc.required {
				res.Status = StatusSkip
			}
			r.report.Results = append(r.report.Results, res)
			continue
		}
		r.runTest(ctx, tc, &res)
		r.report.Results = append(r.report.Results, res)
	}
	for _, res := range r.report.Results {
		switch res.Status {
		case StatusPass:
			r.report.Passed++
		case StatusFail:
			r.report.Failed++
		default:
			r.report.Skipped++
		}
	}
	r.report.Millis = time.Since(start).Milliseconds()
	if r.discovery != nil {
		r.report.Software = r.discovery.Node.Software + " " + r.discovery.Node.Version
	}
	return r.report, nil
}

func (r *runner) selected(id string) bool {
	if len(r.cfg.Only) == 0 {
		return true
	}
	for _, p := range r.cfg.Only {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func (r *runner) runTest(ctx context.Context, tc testCase, res *Result) {
	t := &T{r: r, ctx: ctx, res: res}
	start := time.Now()
	r.logf("%s %s", tc.id, tc.title)
	func() {
		defer func() {
			if v := recover(); v != nil {
				switch x := v.(type) {
				case abort:
				case skipTest:
					res.Status = StatusSkip
					res.Messages = append(res.Messages, x.msg)
				default:
					t.failed = true
					res.Messages = append(res.Messages, fmt.Sprintf("panic: %v", v))
				}
			}
		}()
		tc.run(t)
	}()
	res.Millis = time.Since(start).Milliseconds()
	if res.Status == StatusSkip {
		if !tc.required && len(res.Messages) > 0 {
			return
		}
		return
	}
	if t.failed {
		res.Status = StatusFail
	} else {
		res.Status = StatusPass
	}
}

var (
	regMu sync.Mutex
	tests []testCase
)

var phaseOf = map[string]int{ProfileProtocol: 0, ProfileIngest: 10, ProfilePortal: 20}

func register(profile, id, title string, required bool, run func(*T)) {
	registerPhase(phaseOf[profile], profile, id, title, required, run)
}

// registerPhase registers a test that runs in another profile's phase (for
// example P-FEED-5, which reads the result of the ingest replay).
func registerPhase(phase int, profile, id, title string, required bool, run func(*T)) {
	regMu.Lock()
	defer regMu.Unlock()
	tests = append(tests, testCase{id: id, phase: phase, profile: profile, title: title, required: required, run: run})
}

// registry returns the tests in execution order: protocol, ingest, portal,
// each in registration order.
func registry() []testCase {
	regMu.Lock()
	defer regMu.Unlock()
	out := append([]testCase(nil), tests...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].phase < out[j].phase })
	return out
}

// Tests lists the ids and titles of all tests (for documentation).
func Tests() []Result {
	var out []Result
	for _, tc := range registry() {
		out = append(out, Result{ID: tc.id, Profile: tc.profile, Title: tc.title, Required: tc.required})
	}
	return out
}

// ---- HTTP helpers for tests

// response is a fully read HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r *response) problem() *obx.Problem {
	var p obx.Problem
	if json.Unmarshal(r.body, &p) != nil || p.ObxError == "" {
		return nil
	}
	return &p
}

func (r *response) code() string {
	if p := r.problem(); p != nil {
		return p.ObxError
	}
	var oe obx.OAuthError
	if json.Unmarshal(r.body, &oe) == nil && oe.Code != "" {
		return oe.Code
	}
	return ""
}

func (r *response) decode(v any) error { return json.Unmarshal(r.body, v) }

// noRateRetry marks a context whose requests must see a 429 as it is.
type noRateRetry struct{}

// do sends a request and reads the whole answer. A 429 is retried after its
// Retry-After (a node may rate-limit the suite's heavy tests), except under
// a context from withoutRateRetry; nothing else is retried.
func (r *runner) do(ctx context.Context, method, url string, body []byte, header http.Header) (*response, error) {
	for attempt := 0; ; attempt++ {
		res, err := r.doOnce(ctx, method, url, body, header)
		if err != nil || res.status != http.StatusTooManyRequests || attempt >= 5 || ctx.Value(noRateRetry{}) != nil {
			return res, err
		}
		wait, _ := strconv.Atoi(res.header.Get("Retry-After"))
		wait = min(max(wait, 1), 65)
		select {
		case <-time.After(time.Duration(wait) * time.Second):
		case <-ctx.Done():
			return res, nil
		}
	}
}

// withoutRateRetry returns a context whose requests are not retried on 429.
func withoutRateRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRateRetry{}, true)
}

func (r *runner) doOnce(ctx context.Context, method, url string, body []byte, header http.Header) (*response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	return &response{status: resp.StatusCode, header: resp.Header, body: b}, err
}

// bearerHeader returns an Authorization header.
func bearerHeader(tok string) http.Header {
	return http.Header{"Authorization": {"Bearer " + tok}}
}

// fetch performs an authenticated protocol GET with c's token.
func (t *T) get(c *apiKey, path string) *response {
	tok, err := c.Token(t.ctx)
	t.Check(err, "token")
	u := path
	if !strings.HasPrefix(path, "http") {
		u = t.r.cfg.BaseURL + obx.PathAPIBase + path
	}
	res, err := t.r.do(t.ctx, http.MethodGet, u, nil, bearerHeader(tok))
	t.Check(err, "GET "+path)
	return res
}

// newAdmin returns a fresh admin API session client (not logged in).
func (r *runner) newAdmin() *nodeadmin.Client {
	c, _ := nodeadmin.New(r.cfg.AdminURL, r.hc)
	return c
}

// apiKey is a protocol client with its credentials.
type apiKey struct {
	*client.Client
	id, secret, retailerID string
}

// newClient builds a protocol client using the recording transport.
func (r *runner) newClient(id, secret string) *apiKey {
	c, _ := client.New(client.Config{BaseURL: r.cfg.BaseURL, ClientID: id, ClientSecret: secret, HTTPClient: r.hc, UserAgent: "obx-conformance", MaxRetries: 2})
	return &apiKey{Client: c, id: id, secret: secret}
}

func (r *runner) newIngest(token string) *ingest.Client {
	c, _ := ingest.New(r.cfg.BaseURL, token, ingest.WithHTTPClient(r.hc), ingest.WithUserAgent("obx-conformance"))
	return c
}

// fixture reads a fixture file.
func (r *runner) fixture(name string) ([]byte, error) { return fs.ReadFile(r.fixtures, name) }

// fixtureJSON decodes a fixture file into any.
func (r *runner) fixtureJSON(name string) (any, error) {
	b, err := r.fixture(name)
	if err != nil {
		return nil, err
	}
	var v any
	return v, json.Unmarshal(b, &v)
}

// jarClient returns an http.Client with its own cookie jar (for raw session
// tests).
func (r *runner) jarClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: r.hc.Transport, Timeout: r.hc.Timeout, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
