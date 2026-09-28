// Package ingest is a Go client for the OBX Ingest API: how a brand's own
// systems publish products, variants and images to its node
// (docs/ingest.md). Every call is idempotent: sending the same data again
// answers "unchanged" and costs nothing downstream.
//
//	c, _ := ingest.New("https://brands.kestrel.example", os.Getenv("OBX_TOKEN"))
//	res, err := c.PutProduct(ctx, "KO-1001", obx.ProductInput{StyleNumber: "KO-1001", Name: obx.LocalizedText{"en": "Merino Crew Sweater"}}, nil)
package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/internal/httpx"
	"github.com/openbrandexchange/obx-go/obx"
)

// Client talks to one node's ingest API with one token.
type Client struct {
	base  string // .../obx/v1/ingest
	token string
	doer  *httpx.Doer

	mu   sync.Mutex
	info *obx.IngestInfo
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (default: 10 minute timeout, since
// uploads can be large).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.doer.HTTP = h } }

// WithUserAgent sets the User-Agent.
func WithUserAgent(ua string) Option { return func(c *Client) { c.doer.UserAgent = ua } }

// WithRetryHook is told about every retry (429 or 5xx).
func WithRetryHook(fn func(req *http.Request, status int, wait time.Duration)) Option {
	return func(c *Client) { c.doer.OnRetry = fn }
}

// WithMaxRetries sets how often 429/5xx answers are retried.
func WithMaxRetries(n int) Option { return func(c *Client) { c.doer.MaxRetries = n } }

// New creates a client. nodeURL is the node address or its ingest URL.
func New(nodeURL, token string, opts ...Option) (*Client, error) {
	base, err := client.NodeBase(nodeURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("obx: an automatic upload key (token starting with obxi_) is required")
	}
	c := &Client{base: base + obx.PathIngest, token: strings.TrimSpace(token), doer: &httpx.Doer{HTTP: &http.Client{Timeout: 10 * time.Minute}}}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// URL returns the ingest base URL.
func (c *Client) URL() string { return c.base }

// Do sends an authenticated request to path (relative to the ingest base).
// The caller closes the body; non-2xx answers are returned as is.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, contentType string, header http.Header) (*http.Response, error) {
	req, err := httpx.NewRequest(method, c.base+path, body, contentType)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	idem := method != http.MethodPost
	return c.doer.Do(ctx, req, idem)
}

func (c *Client) doJSON(ctx context.Context, method, path string, in any, contentType string, out any, header http.Header) (*http.Response, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = httpx.Marshal(in); err != nil {
			return nil, err
		}
		if contentType == "" {
			contentType = "application/json"
		}
	}
	resp, err := c.Do(ctx, method, path, body, contentType, header)
	if err != nil {
		return nil, err
	}
	return resp, httpx.Decode(resp, out)
}

// Info returns the token's rights, the node's limits and whether the node
// renders images itself. The answer is cached.
func (c *Client) Info(ctx context.Context) (*obx.IngestInfo, error) {
	c.mu.Lock()
	if c.info != nil {
		i := c.info
		c.mu.Unlock()
		return i, nil
	}
	c.mu.Unlock()
	var info obx.IngestInfo
	if _, err := c.doJSON(ctx, http.MethodGet, "", nil, "", &info, nil); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.info = &info
	c.mu.Unlock()
	return &info, nil
}

// WriteOptions are optional request headers of a single write.
type WriteOptions struct {
	// IfMatch makes the write conditional on the record's ETag.
	IfMatch string
}

func (o *WriteOptions) header() http.Header {
	if o == nil || o.IfMatch == "" {
		return nil
	}
	return http.Header{"If-Match": {o.IfMatch}}
}

func recordPath(kind, externalID string) (string, error) {
	switch kind {
	case obx.KindBrand, obx.KindProduct, obx.KindVariant, obx.KindAsset:
	default:
		return "", fmt.Errorf("obx: unknown kind %q", kind)
	}
	if !obx.ValidExternalID(externalID) {
		return "", fmt.Errorf("obx: %q is not a valid id (1-200 characters, no \"/\")", externalID)
	}
	return "/" + kind + "s/" + url.PathEscape(externalID), nil
}

// Put creates or replaces a record. body is a BrandInput, ProductInput,
// VariantInput, AssetInput or raw JSON.
func (c *Client) Put(ctx context.Context, kind, externalID string, body any, opts *WriteOptions) (*obx.IngestResult, error) {
	p, err := recordPath(kind, externalID)
	if err != nil {
		return nil, err
	}
	var r obx.IngestResult
	_, err = c.doJSON(ctx, http.MethodPut, p, body, "application/json", &r, opts.header())
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// PutBrand creates or replaces a brand.
func (c *Client) PutBrand(ctx context.Context, externalID string, in obx.BrandInput, opts *WriteOptions) (*obx.IngestResult, error) {
	return c.Put(ctx, obx.KindBrand, externalID, in, opts)
}

// PutProduct creates or replaces a product.
func (c *Client) PutProduct(ctx context.Context, externalID string, in obx.ProductInput, opts *WriteOptions) (*obx.IngestResult, error) {
	return c.Put(ctx, obx.KindProduct, externalID, in, opts)
}

// PutVariant creates or replaces a variant.
func (c *Client) PutVariant(ctx context.Context, externalID string, in obx.VariantInput, opts *WriteOptions) (*obx.IngestResult, error) {
	return c.Put(ctx, obx.KindVariant, externalID, in, opts)
}

// PutAsset creates or replaces asset metadata (and attaches blobs via Files).
func (c *Client) PutAsset(ctx context.Context, externalID string, in obx.AssetInput, opts *WriteOptions) (*obx.IngestResult, error) {
	return c.Put(ctx, obx.KindAsset, externalID, in, opts)
}

// Patch applies a JSON merge patch (RFC 7396) to a record.
func (c *Client) Patch(ctx context.Context, kind, externalID string, patch any, opts *WriteOptions) (*obx.IngestResult, error) {
	p, err := recordPath(kind, externalID)
	if err != nil {
		return nil, err
	}
	var r obx.IngestResult
	_, err = c.doJSON(ctx, http.MethodPatch, p, patch, obx.MediaTypeMergePatch, &r, opts.header())
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Publish sets status published.
func (c *Client) Publish(ctx context.Context, kind, externalID string) (*obx.IngestResult, error) {
	return c.Patch(ctx, kind, externalID, map[string]string{"status": obx.StatusPublished}, nil)
}

// Unpublish sets status unpublished (retailers get a delete in their feed).
func (c *Client) Unpublish(ctx context.Context, kind, externalID string) (*obx.IngestResult, error) {
	return c.Patch(ctx, kind, externalID, map[string]string{"status": obx.StatusUnpublished}, nil)
}

// Delete removes a record (needs the ingest:delete scope). Deleting what does
// not exist answers "unchanged".
func (c *Client) Delete(ctx context.Context, kind, externalID string) (*obx.IngestResult, error) {
	p, err := recordPath(kind, externalID)
	if err != nil {
		return nil, err
	}
	var r obx.IngestResult
	if _, err := c.doJSON(ctx, http.MethodDelete, p, nil, "", &r, nil); err != nil {
		return nil, err
	}
	return &r, nil
}

// Get reads a stored record.
func (c *Client) Get(ctx context.Context, kind, externalID string) (*obx.IngestRecord, error) {
	p, err := recordPath(kind, externalID)
	if err != nil {
		return nil, err
	}
	var r obx.IngestRecord
	if _, err := c.doJSON(ctx, http.MethodGet, p, nil, "", &r, nil); err != nil {
		return nil, err
	}
	return &r, nil
}

// SetAssetOrder assigns positions 1..n to the listed assets of one product
// colour atomically.
func (c *Client) SetAssetOrder(ctx context.Context, productExternalID string, order obx.AssetOrder) ([]obx.IngestResult, error) {
	p, err := recordPath(obx.KindProduct, productExternalID)
	if err != nil {
		return nil, err
	}
	var r []obx.IngestResult
	if _, err := c.doJSON(ctx, http.MethodPut, p+"/asset-order", order, "application/json", &r, nil); err != nil {
		return nil, err
	}
	return r, nil
}

// PutAssetFile uploads the original or a rendition of an existing asset.
// sha256 is optional (verified by the node when given).
func (c *Client) PutAssetFile(ctx context.Context, externalID, rendition, contentType string, data []byte, sha256 string) (*obx.IngestResult, error) {
	p, err := recordPath(obx.KindAsset, externalID)
	if err != nil {
		return nil, err
	}
	var h http.Header
	if sha256 != "" {
		h = http.Header{obx.HeaderContentSHA256: {sha256}}
	}
	resp, err := c.Do(ctx, http.MethodPut, p+"/files/"+url.PathEscape(rendition), data, contentType, h)
	if err != nil {
		return nil, err
	}
	var r obx.IngestResult
	return &r, httpx.Decode(resp, &r)
}

// BatchOptions control Batch.
type BatchOptions struct {
	// IdempotencyKey makes retries safe (same key within 24 h = same answer).
	IdempotencyKey string
	// Async asks the node to answer with a job (Prefer: respond-async).
	Async bool
	// NDJSON sends the operations as NDJSON instead of a JSON array.
	NDJSON bool
	// NoWait returns right after a 202 without waiting for the job.
	NoWait bool
	// PollInterval while waiting for a job (default 1 s).
	PollInterval time.Duration
}

// BatchOutcome is the result of Batch: Results and Summary when the node
// finished (synchronously or after waiting), Job when it answered 202.
type BatchOutcome struct {
	Results []obx.IngestResult
	Summary obx.BatchSummary
	Job     *obx.IngestJob
}

// Batch sends up to limits.max_batch_operations operations, processed in
// order. It handles both a synchronous 200 and an asynchronous 202 + job.
func (c *Client) Batch(ctx context.Context, ops []obx.IngestOperation, opts BatchOptions) (*BatchOutcome, error) {
	var body []byte
	ct := "application/json"
	if opts.NDJSON {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, op := range ops {
			if err := enc.Encode(op); err != nil {
				return nil, err
			}
		}
		body, ct = buf.Bytes(), obx.MediaTypeNDJSON
	} else {
		var err error
		if body, err = json.Marshal(ops); err != nil {
			return nil, err
		}
	}
	h := http.Header{}
	if opts.IdempotencyKey != "" {
		h.Set(obx.HeaderIdempotencyKey, opts.IdempotencyKey)
	}
	if opts.Async {
		h.Set("Prefer", "respond-async")
	}
	resp, err := c.Do(ctx, http.MethodPost, "/batch", body, ct, h)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusAccepted {
		var job obx.IngestJob
		if err := httpx.Decode(resp, &job); err != nil {
			return nil, err
		}
		out := &BatchOutcome{Job: &job}
		if opts.NoWait {
			return out, nil
		}
		done, err := c.WaitJob(ctx, job.ID, opts.PollInterval)
		if err != nil {
			return out, err
		}
		out.Job = done
		if done.Summary != nil {
			out.Summary = *done.Summary
		}
		err = c.EachJobResult(ctx, done.ID, false, func(raw json.RawMessage) error {
			var r obx.IngestResult
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			out.Results = append(out.Results, r)
			return nil
		})
		return out, err
	}
	var br obx.BatchResponse
	if err := httpx.Decode(resp, &br); err != nil {
		return nil, err
	}
	return &BatchOutcome{Results: br.Results, Summary: br.Summary}, nil
}

// Jobs lists recent jobs, newest first.
func (c *Client) Jobs(ctx context.Context, status, cursor string, limit int) (*obx.Page[obx.IngestJob], error) {
	v := url.Values{}
	if status != "" {
		v.Set("status", status)
	}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	if limit > 0 {
		v.Set("limit", fmt.Sprint(limit))
	}
	p := "/jobs"
	if e := v.Encode(); e != "" {
		p += "?" + e
	}
	var out obx.Page[obx.IngestJob]
	_, err := c.doJSON(ctx, http.MethodGet, p, nil, "", &out, nil)
	return &out, err
}

// Job reads one job.
func (c *Client) Job(ctx context.Context, id string) (*obx.IngestJob, error) {
	var j obx.IngestJob
	_, err := c.doJSON(ctx, http.MethodGet, "/jobs/"+url.PathEscape(id), nil, "", &j, nil)
	return &j, err
}

// WaitJob polls a job until it succeeded or failed.
func (c *Client) WaitJob(ctx context.Context, id string, every time.Duration) (*obx.IngestJob, error) {
	if every <= 0 {
		every = time.Second
	}
	for {
		j, err := c.Job(ctx, id)
		if err != nil {
			return nil, err
		}
		if j.Done() {
			return j, nil
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		if every < 5*time.Second {
			every += every / 2
		}
	}
}

// JobResults reads one page of a job's results: IngestResult items for batch
// jobs, ImportRowError items for imports.
func (c *Client) JobResults(ctx context.Context, id string, errorsOnly bool, cursor string, limit int) (*obx.Page[json.RawMessage], error) {
	v := url.Values{}
	if errorsOnly {
		v.Set("errors_only", "true")
	}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	if limit > 0 {
		v.Set("limit", fmt.Sprint(limit))
	}
	p := "/jobs/" + url.PathEscape(id) + "/results"
	if e := v.Encode(); e != "" {
		p += "?" + e
	}
	var out obx.Page[json.RawMessage]
	_, err := c.doJSON(ctx, http.MethodGet, p, nil, "", &out, nil)
	return &out, err
}

// EachJobResult walks every page of a job's results.
func (c *Client) EachJobResult(ctx context.Context, id string, errorsOnly bool, fn func(json.RawMessage) error) error {
	cursor := ""
	for {
		page, err := c.JobResults(ctx, id, errorsOnly, cursor, 1000)
		if err != nil {
			return err
		}
		for _, it := range page.Items {
			if err := fn(it); err != nil {
				return err
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return nil
		}
		cursor = *page.NextCursor
	}
}

// ImportProblems returns every row/file problem of an import job.
func (c *Client) ImportProblems(ctx context.Context, jobID string) ([]obx.ImportRowError, error) {
	var out []obx.ImportRowError
	err := c.EachJobResult(ctx, jobID, false, func(raw json.RawMessage) error {
		var e obx.ImportRowError
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

// readAllLimit reads r up to max bytes.
func readAllLimit(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("obx: file is larger than %d bytes", max)
	}
	return b, nil
}
