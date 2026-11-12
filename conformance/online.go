package conformance

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/internal/safedial"
)

// Online configures a read-only check of a portal on the public internet,
// as run for a visitor of openbrandexchange.com (obx-conformance check). The
// suite then never writes: it creates no API keys, access requests, exports,
// webhooks or usage events, imports nothing and needs no operator. Only the
// tests (and the parts of tests) in onlineTier run; the others are reported
// as skipped with "not checked online". A read-only run is always Partial: it
// is never a conformance claim.
type Online struct {
	// MaxRequests caps the HTTP requests of the whole run (default 800).
	MaxRequests int
	// MaxBytes caps the response bytes of the whole run (default 64 MiB).
	MaxBytes int64
	// MinInterval is the minimum time between two requests (default 100 ms,
	// at most 10 requests per second).
	MinInterval time.Duration
	// MaxRetryAfter is the longest Retry-After of a 429 answer that the
	// check waits for (default 2 s). A longer one stops the run: the
	// remaining tests are skipped with "rate limited" instead of the check
	// waiting minutes for the portal.
	MaxRetryAfter time.Duration
	// MaxRedirects per request (default 3).
	MaxRedirects int
	// AllowInsecure allows http:// and loopback/private addresses (tests and
	// local portals only; never on a public service).
	AllowInsecure bool
}

// What an online test needs.
const (
	// OnlineAnonymous tests run without credentials.
	OnlineAnonymous = "anonymous"
	// OnlineAPIKey tests need one API key (OAuth2 client credentials). On a
	// portal without the Kestrel test data their fixture parts are skipped
	// and the result is marked partial.
	OnlineAPIKey = "api_key"
	// OnlineFixtures tests need one API key whose retailer sees exactly the
	// Kestrel test data (spec fixtures/kestrel).
	OnlineFixtures = "fixtures"
)

// onlineTier lists the tests a read-only run may execute. Every other test
// writes, needs brand-side steps (operator, admin, ingest) or sends traffic a
// public checker must not send (path traversal probes, rate-limit tests).
var onlineTier = map[string]string{
	"P-DISC-1":  OnlineAnonymous,
	"P-DISC-2":  OnlineAnonymous, // presence anonymous; 501 probes (GET only) with a key
	"P-AUTH-2":  OnlineAnonymous, // without the revocation part
	"P-ERR-1":   OnlineAnonymous, // with a key it also probes authenticated errors
	"P-AUTH-1":  OnlineAPIKey,
	"P-FEED-1":  OnlineAPIKey, // counts only with fixtures
	"P-FEED-2":  OnlineAPIKey,
	"P-FEED-3":  OnlineAPIKey,
	"P-CAT-2":   OnlineAPIKey, // count only with fixtures
	"P-CAT-3":   OnlineAPIKey, // expected answer only with fixtures
	"P-CAT-4":   OnlineAPIKey, // without the unpublish part
	"P-CAT-6":   OnlineAPIKey, // KO-1003/KO-1004 only with fixtures
	"P-RATE-1":  OnlineAPIKey, // headers only, never provokes a 429
	"P-CAT-1":   OnlineFixtures,
	"P-CAT-5":   OnlineFixtures,
	"P-MEDIA-1": OnlineFixtures,
	"P-MEDIA-2": OnlineAPIKey,   // without fixtures: the first images of the catalog, no originals
	"P-MEDIA-3": OnlineAPIKey,   // without fixtures: the derived rules, not byte-identical originals
	"P-AUTH-3":  OnlineFixtures, // only with a second key without originals
	"P-FEED-4":  OnlineFixtures, // only with a key restricted to brand kestrel; no export, no live part
}

// OnlineTier returns what a test needs in a read-only run, or "" when it is
// not run online.
func OnlineTier(id string) string { return onlineTier[id] }

// OnlineTests lists the ids that a read-only run may execute, sorted.
func OnlineTests() []string {
	var out []string
	for id := range onlineTier {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (r *runner) online() bool { return r.cfg.Online != nil }

// ErrBudget is returned by every request after a read-only run used up its
// request or byte budget.
var ErrBudget = errors.New("conformance: request budget of the online check used up")

// ErrRateLimited is returned by every request after the portal answered 429
// with a Retry-After longer than Online.MaxRetryAfter.
var ErrRateLimited = errors.New("conformance: the portal rate-limited the online check")

// budget is the transport of a read-only run: it refuses non-https URLs
// (unless AllowInsecure), paces requests and caps their number and bytes.
type budget struct {
	next http.RoundTripper
	o    Online

	mu       sync.Mutex
	requests int
	bytes    int64
	last     time.Time
	spent    bool
	limited  time.Duration // Retry-After of the 429 that stopped the run
}

func newBudget(next http.RoundTripper, o Online) *budget {
	if o.MaxRequests <= 0 {
		o.MaxRequests = 800
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 64 << 20
	}
	if o.MinInterval <= 0 {
		o.MinInterval = 100 * time.Millisecond
	}
	if o.MaxRetryAfter <= 0 {
		o.MaxRetryAfter = 2 * time.Second
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = 3
	}
	return &budget{next: next, o: o}
}

// stopped returns why the run cannot send more requests, or "".
func (b *budget) stopped() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.limited > 0:
		return fmt.Sprintf("the portal rate-limited the check (429 with Retry-After %s); try again later", b.limited.Round(time.Second))
	case b.spent:
		return "the request budget of the online check was used up"
	}
	return ""
}

func (b *budget) usage() (int, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests, b.bytes
}

func (b *budget) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && !b.o.AllowInsecure {
		return nil, fmt.Errorf("conformance: the online check only connects over https, not to %s", req.URL.Redacted())
	}
	b.mu.Lock()
	if b.limited > 0 {
		b.mu.Unlock()
		return nil, ErrRateLimited
	}
	if b.spent || b.requests >= b.o.MaxRequests {
		b.spent = true
		b.mu.Unlock()
		return nil, ErrBudget
	}
	b.requests++
	wait := time.Until(b.last.Add(b.o.MinInterval))
	if wait < 0 {
		wait = 0
	}
	b.last = time.Now().Add(wait)
	b.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	resp, err := b.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if wait := retryAfter(resp.Header); wait > b.o.MaxRetryAfter {
			// Waiting minutes is no use for a visitor; stop the run here.
			resp.Body.Close()
			b.mu.Lock()
			b.limited = wait
			b.mu.Unlock()
			return nil, ErrRateLimited
		}
	}
	resp.Body = &countingBody{ReadCloser: resp.Body, b: b}
	return resp, nil
}

// retryAfter reads Retry-After (seconds or an HTTP date); a 429 without it
// counts as one minute.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return time.Minute
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(n, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return time.Minute
}

type countingBody struct {
	io.ReadCloser
	b *budget
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.b.mu.Lock()
	c.b.bytes += int64(n)
	over := c.b.bytes > c.b.o.MaxBytes
	if over {
		c.b.spent = true
	}
	c.b.mu.Unlock()
	if over {
		return n, ErrBudget
	}
	return n, err
}

// onlineTransport is the default transport of a read-only run: every
// connection is checked against loopback, private, link-local (metadata),
// CGNAT, multicast and unspecified addresses after DNS resolution.
func onlineTransport(o Online) http.RoundTripper {
	return safedial.Transport(o.AllowInsecure)
}

// onlineRedirects limits redirects and keeps them on https.
func onlineRedirects(o Online, max int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > max {
			return fmt.Errorf("conformance: more than %d redirects", max)
		}
		if req.URL.Scheme != "https" && !o.AllowInsecure {
			return fmt.Errorf("conformance: redirect to %s is not https", req.URL.Redacted())
		}
		return nil
	}
}

// stopOnline ends a test in a read-only run where the rest of it needs a
// write or a brand-side step. What ran so far counts.
type stopOnline struct{ msg string }

// partialf notes a part of the test that a read-only run does not check.
func (t *T) partialf(format string, args ...any) {
	t.res.Online = "partial"
	t.res.Messages = append(t.res.Messages, "not checked online: "+fmt.Sprintf(format, args...))
}

// onlineStop ends the test here in a read-only run.
func (t *T) onlineStop(format string, args ...any) {
	panic(stopOnline{fmt.Sprintf(format, args...)})
}

// onlineGate returns why a test does not run in this read-only run, or "".
func (r *runner) onlineGate(id string) string {
	switch onlineTier[id] {
	case "":
		return "not checked online: this test writes to the portal or needs brand-side steps; run obx-conformance locally"
	case OnlineAPIKey:
		if r.full == nil {
			return "not checked online: needs a test API key (client id and secret)"
		}
	case OnlineFixtures:
		if r.full == nil {
			return "not checked online: needs a test API key (client id and secret) of a test account with the Kestrel test data"
		}
		if !r.fixturesPresent {
			return "not checked online: needs a test account with the Kestrel test data (spec fixtures/kestrel)"
		}
		if id == "P-AUTH-3" && r.cfg.NoOriginalClientID == "" {
			return "not checked online: needs a second test API key without access to originals; run obx-conformance locally"
		}
		if id == "P-FEED-4" && r.cfg.SecondClientID == "" {
			return "not checked online: needs a second test API key restricted to one brand; run obx-conformance locally"
		}
	}
	return ""
}

// tokenPattern matches access tokens and secrets that may show up in a
// message (for example the body of a token answer the portal should not
// have given).
var tokenPattern = regexp.MustCompile(`("(?:access_token|client_secret|refresh_token)"\s*:\s*")[^"]*(")|\bobx(?:at|s|i|r)_[A-Za-z0-9_\-]+`)

// clean removes the run's secrets and any token from s.
func (r *runner) clean(s string) string {
	for _, sec := range []string{r.cfg.ClientSecret, r.cfg.SecondClientSecret, r.cfg.NoOriginalClientSecret} {
		if len(sec) >= 8 {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return tokenPattern.ReplaceAllStringFunc(s, func(m string) string {
		if sub := tokenPattern.FindStringSubmatch(m); sub[1] != "" {
			return sub[1] + "[redacted]" + sub[2]
		}
		return "[redacted]"
	})
}

// cleanAll returns a cleaned copy of msgs.
func (r *runner) cleanAll(msgs []string) []string {
	if msgs == nil {
		return nil
	}
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = r.clean(m)
	}
	return out
}

// redact removes the run's secrets and any token from the report messages,
// so that an online report can be shown, stored and shared.
func (r *runner) redact() {
	for i := range r.report.Results {
		r.report.Results[i].Messages = r.cleanAll(r.report.Results[i].Messages)
	}
	r.report.Setup = r.cleanAll(r.report.Setup)
	r.report.SetupError = r.clean(r.report.SetupError)
}
