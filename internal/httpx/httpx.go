// Package httpx holds the HTTP plumbing shared by the OBX clients: retries
// that honour Retry-After, problem+json decoding and small JSON helpers.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

// UserAgent is sent when the caller configured none.
const UserAgent = "obx-go/" + obx.SpecVersion

// Doer sends requests with retries.
type Doer struct {
	HTTP      *http.Client
	UserAgent string
	// MaxRetries is how often a 429 or (for idempotent requests) a 5xx or
	// network error is retried. 0 means the default of 4; negative disables.
	MaxRetries int
	// MaxWait caps a single Retry-After wait (default 60 s).
	MaxWait time.Duration
	// OnRetry, when set, is told about every retry (for progress output).
	OnRetry func(req *http.Request, status int, wait time.Duration)
}

func (d *Doer) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return http.DefaultClient
}

func (d *Doer) retries() int {
	switch {
	case d.MaxRetries < 0:
		return 0
	case d.MaxRetries == 0:
		return 4
	}
	return d.MaxRetries
}

// Do sends req. A 429 is always retried after Retry-After (when the body can
// be replayed); 5xx answers and network errors are retried with exponential
// backoff when idempotent is true. The caller closes the response body.
func (d *Doer) Do(ctx context.Context, req *http.Request, idempotent bool) (*http.Response, error) {
	req = req.WithContext(ctx)
	if req.Header.Get("User-Agent") == "" {
		ua := d.UserAgent
		if ua == "" {
			ua = UserAgent
		}
		req.Header.Set("User-Agent", ua)
	}
	max := d.retries()
	for attempt := 0; ; attempt++ {
		if attempt > 0 && req.Body != nil && req.GetBody != nil {
			b, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = b
		}
		resp, err := d.client().Do(req)
		canReplay := req.Body == nil || req.GetBody != nil
		if attempt >= max || !canReplay {
			return resp, err
		}
		var wait time.Duration
		switch {
		case err != nil:
			if !idempotent || ctx.Err() != nil {
				return nil, err
			}
			wait = backoff(attempt)
		case resp.StatusCode == http.StatusTooManyRequests:
			wait = RetryAfter(resp.Header, backoff(attempt))
		case resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented && idempotent:
			wait = RetryAfter(resp.Header, backoff(attempt))
		default:
			return resp, nil
		}
		maxWait := d.MaxWait
		if maxWait == 0 {
			maxWait = 60 * time.Second
		}
		if wait > maxWait {
			wait = maxWait
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
		if d.OnRetry != nil {
			d.OnRetry(req, status, wait)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func backoff(attempt int) time.Duration {
	base := time.Duration(250<<attempt) * time.Millisecond
	return base + time.Duration(rand.Int64N(int64(base/2)+1))
}

// RetryAfter parses a Retry-After header (seconds or HTTP date).
func RetryAfter(h http.Header, def time.Duration) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
		return 0
	}
	return def
}

// NewRequest builds a request whose body can be replayed.
func NewRequest(method, url string, body []byte, contentType string) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		return nil, err
	}
	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// IsJSON reports whether the response has a JSON (or problem+json) body.
func IsJSON(resp *http.Response) bool {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mt == "application/json" || mt == obx.MediaTypeProblem || strings.HasSuffix(mt, "+json")
}

// maxErrorBody bounds how much of an error body is read.
const maxErrorBody = 1 << 20

// Error turns a non-2xx response into an error: the *obx.Problem of a
// problem+json body, or a synthesised Problem named after the status. It
// closes the body.
func Error(resp *http.Response) error {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if IsJSON(resp) {
		var p obx.Problem
		if json.Unmarshal(raw, &p) == nil && p.ObxError != "" {
			if p.Status == 0 {
				p.Status = resp.StatusCode
			}
			if p.RetryAfter == nil {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if n, err := strconv.Atoi(ra); err == nil {
						p.RetryAfter = &n
					}
				}
			}
			return &p
		}
		var oe obx.OAuthError
		if json.Unmarshal(raw, &oe) == nil && oe.Code != "" {
			oe.HTTPStatus = resp.StatusCode
			return &oe
		}
	}
	code := codeForStatus(resp.StatusCode)
	detail := strings.TrimSpace(string(raw))
	if len(detail) > 300 {
		detail = detail[:300] + "…"
	}
	if detail == "" {
		detail = resp.Status
	}
	return obx.NewProblem(code, detail).WithStatus(resp.StatusCode)
}

func codeForStatus(status int) string {
	switch status {
	case 400:
		return obx.ErrInvalidRequest
	case 401:
		return obx.ErrUnauthorized
	case 403:
		return obx.ErrForbidden
	case 404:
		return obx.ErrNotFound
	case 405:
		return obx.ErrMethodNotAllowed
	case 409:
		return obx.ErrConflict
	case 410:
		return obx.ErrCursorExpired
	case 412:
		return obx.ErrPreconditionFailed
	case 413:
		return obx.ErrPayloadTooLarge
	case 415:
		return obx.ErrUnsupportedMediaType
	case 422:
		return obx.ErrValidationFailed
	case 429:
		return obx.ErrRateLimited
	case 501:
		return obx.ErrNotImplemented
	case 503:
		return obx.ErrServiceUnavailable
	}
	return obx.ErrInternal
}

// Decode reads a JSON body into v (when v is non-nil) and closes it. Non-2xx
// responses become errors via Error.
func Decode(resp *http.Response, v any) error {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Error(resp)
	}
	defer resp.Body.Close()
	if v == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode %s %s: %w", resp.Request.Method, resp.Request.URL.Path, err)
	}
	return nil
}

// ErrStatus is returned by helpers that expect a specific status.
var ErrStatus = errors.New("unexpected status")

// Marshal encodes v as JSON.
func Marshal(v any) ([]byte, error) {
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	if b, ok := v.([]byte); ok {
		return b, nil
	}
	return json.Marshal(v)
}
