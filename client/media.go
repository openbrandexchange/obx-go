package client

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/internal/httpx"
	"github.com/openbrandexchange/obx-go/obx"
)

// ErrChecksum is returned when downloaded bytes do not match the sha256 the
// node announced. Nothing that failed the check should be used.
var ErrChecksum = errors.New("obx: downloaded file does not match its sha256")

// Download fetches a rendition into w and verifies its sha256 and size. It
// uses the signed URL without credentials while it is valid, else the Bearer
// token. The written bytes must be discarded when an error is returned.
func (c *Client) Download(ctx context.Context, r *obx.Rendition, w io.Writer) (int64, error) {
	if r == nil || r.URL == "" {
		return 0, errors.New("obx: rendition has no url")
	}
	var resp *http.Response
	var err error
	if r.URLExpiresAt != nil && time.Now().Before(r.URLExpiresAt.Add(-5*time.Second)) {
		req, _ := httpx.NewRequest(http.MethodGet, r.URL, nil, "")
		req.Header.Del("Accept")
		resp, err = c.doer.Do(ctx, req, true)
	} else {
		resp, err = c.Do(ctx, http.MethodGet, r.URL, nil, "", nil)
	}
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, httpx.Error(resp)
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
	if err != nil {
		return n, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if r.SHA256 != "" && !strings.EqualFold(got, r.SHA256) {
		return n, fmt.Errorf("%w: %s is %s, expected %s", ErrChecksum, r.Name, got, r.SHA256)
	}
	if r.Bytes > 0 && n != r.Bytes {
		return n, fmt.Errorf("%w: %s has %d bytes, expected %d", ErrChecksum, r.Name, n, r.Bytes)
	}
	return n, nil
}

// DownloadAsset fetches the asset (for fresh URLs) and downloads one of its
// renditions. It returns obx.ErrOriginalNotAllowed when the original is not
// offered to the caller.
func (c *Client) DownloadAsset(ctx context.Context, assetID, rendition string, w io.Writer) (*obx.Rendition, int64, error) {
	a, err := c.Asset(ctx, assetID)
	if err != nil {
		return nil, 0, err
	}
	r := a.Renditions.Get(rendition)
	if r == nil {
		if rendition == obx.RenditionOriginal {
			return nil, 0, obx.NewProblem(obx.ErrOriginalNotAllowed, "the original is not available to you")
		}
		return nil, 0, fmt.Errorf("obx: unknown rendition %q", rendition)
	}
	n, err := c.Download(ctx, r, w)
	return r, n, err
}

// RequestAccess sends an access request (no credentials needed). Keep the
// returned RequestToken: it is shown once and needed to poll.
func (c *Client) RequestAccess(ctx context.Context, in obx.AccessRequestInput) (*obx.AccessRequest, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	u := c.base + obx.PathAPIBase + "/access-requests"
	if d, err := c.Discovery(ctx); err == nil && d.AccessRequestsURL != nil && *d.AccessRequestsURL != "" {
		u = *d.AccessRequestsURL
	}
	req, _ := httpx.NewRequest(http.MethodPost, u, raw, "application/json")
	resp, err := c.doer.Do(ctx, req, false)
	if err != nil {
		return nil, err
	}
	var ar obx.AccessRequest
	return &ar, httpx.Decode(resp, &ar)
}

// PollAccessRequest reads the status of an access request with its request
// token. After approval with API access, the first read carries the client
// secret; store it immediately, it is never shown again.
func (c *Client) PollAccessRequest(ctx context.Context, id, requestToken string) (*obx.AccessRequest, error) {
	u := c.apiBase(ctx) + "/access-requests/" + url.PathEscape(id)
	req, _ := httpx.NewRequest(http.MethodGet, u, nil, "")
	req.Header.Set("Authorization", "Bearer "+requestToken)
	resp, err := c.doer.Do(ctx, req, false)
	if err != nil {
		return nil, err
	}
	var ar obx.AccessRequest
	return &ar, httpx.Decode(resp, &ar)
}

// CreateExport starts a bulk export.
func (c *Client) CreateExport(ctx context.Context, in obx.ExportRequest, idempotencyKey string) (*obx.Export, error) {
	var h http.Header
	if idempotencyKey != "" {
		h = http.Header{obx.HeaderIdempotencyKey: {idempotencyKey}}
	}
	var e obx.Export
	return &e, c.sendJSON(ctx, http.MethodPost, "/exports", in, &e, h)
}

// Export reads the status of an export.
func (c *Client) Export(ctx context.Context, id string) (*obx.Export, error) {
	var e obx.Export
	return &e, c.getJSON(ctx, "/exports/"+url.PathEscape(id), &e)
}

// WaitExport polls an export until it succeeded or failed.
func (c *Client) WaitExport(ctx context.Context, id string, every time.Duration) (*obx.Export, error) {
	if every <= 0 {
		every = 10 * time.Second
	}
	for {
		e, err := c.Export(ctx, id)
		if err != nil {
			return nil, err
		}
		switch e.Status {
		case obx.JobSucceeded:
			return e, nil
		case obx.JobFailed, obx.JobExpired:
			if e.Error != nil {
				return e, e.Error
			}
			return e, fmt.Errorf("obx: export %s %s", id, e.Status)
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// ExportManifest streams the NDJSON manifest of a finished export and calls
// fn per line.
func (c *Client) ExportManifest(ctx context.Context, e *obx.Export, fn func(*obx.ExportManifestLine) error) error {
	u := "/exports/" + url.PathEscape(e.ID) + "/manifest"
	if e.ManifestURL != nil && *e.ManifestURL != "" {
		u = *e.ManifestURL
	}
	resp, err := c.Do(ctx, http.MethodGet, u, nil, "", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return httpx.Error(resp)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var l obx.ExportManifestLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			return fmt.Errorf("obx: manifest line: %w", err)
		}
		if err := fn(&l); err != nil {
			return err
		}
	}
	return sc.Err()
}

// DownloadExportPart downloads one zip part and verifies its sha256.
func (c *Client) DownloadExportPart(ctx context.Context, p obx.ExportPart, w io.Writer) (int64, error) {
	return c.Download(ctx, &obx.Rendition{Name: fmt.Sprintf("part %d", p.Number), URL: p.URL, SHA256: p.SHA256, Bytes: p.Bytes}, w)
}
