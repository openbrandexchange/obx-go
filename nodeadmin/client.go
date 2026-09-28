package nodeadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/internal/httpx"
	"github.com/openbrandexchange/obx-go/obx"
)

// Client is a browser-like session against the admin API: it keeps the
// session cookie and sends the CSRF token on every state-changing request.
type Client struct {
	base string
	doer *httpx.Doer

	mu   sync.Mutex
	csrf string
	sess *Session
}

// New creates a client for the node at base (any URL on the node).
func New(base string, hc *http.Client) (*Client, error) {
	b, err := client.NodeBase(base)
	if err != nil {
		return nil, err
	}
	jar, _ := cookiejar.New(nil)
	var c http.Client
	if hc != nil {
		c = *hc
	}
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Minute
	}
	c.Jar = jar
	return &Client{base: b, doer: &httpx.Doer{HTTP: &c, MaxRetries: -1}}, nil
}

// Base returns the node base URL.
func (c *Client) Base() string { return c.base }

// CSRF returns the current CSRF token (fetching a session when needed).
func (c *Client) CSRF(ctx context.Context) (string, error) {
	c.mu.Lock()
	t := c.csrf
	c.mu.Unlock()
	if t != "" {
		return t, nil
	}
	s, err := c.Session(ctx)
	if err != nil {
		return "", err
	}
	return s.CSRFToken, nil
}

// SetCSRF overrides the CSRF token (for tests of the CSRF check).
func (c *Client) SetCSRF(token string) {
	c.mu.Lock()
	c.csrf = token
	c.mu.Unlock()
}

// Raw sends a request to path ("/admin/api/..." or any path on the node) and
// returns the response without interpreting it. csrf controls whether the
// X-CSRF-Token header is sent.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte, contentType string, csrf bool) (*http.Response, error) {
	req, err := httpx.NewRequest(method, c.base+path, body, contentType)
	if err != nil {
		return nil, err
	}
	if csrf {
		t, err := c.CSRF(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set(obx.HeaderCSRF, t)
	}
	return c.doer.Do(ctx, req, false)
}

// Call sends in as JSON (nil for no body) and decodes the answer into out.
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	ct := ""
	if in != nil {
		var err error
		if body, err = httpx.Marshal(in); err != nil {
			return err
		}
		ct = "application/json"
		if method == http.MethodPatch {
			ct = obx.MediaTypeMergePatch
		}
	}
	safe := method == http.MethodGet || method == http.MethodHead
	resp, err := c.Raw(ctx, method, path, body, ct, !safe)
	if err != nil {
		return err
	}
	return httpx.Decode(resp, out)
}

func (c *Client) remember(s *Session) {
	c.mu.Lock()
	c.sess = s
	if s.CSRFToken != "" {
		c.csrf = s.CSRFToken
	}
	c.mu.Unlock()
}

// Session reads the session (works without login and sets the CSRF cookie).
func (c *Client) Session(ctx context.Context) (*Session, error) {
	resp, err := c.Raw(ctx, http.MethodGet, obx.PathAdminAPI+"/session", nil, "", false)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := httpx.Decode(resp, &s); err != nil {
		return nil, err
	}
	c.remember(&s)
	return &s, nil
}

// SetupRequired reports whether the node still needs first-run setup.
func (c *Client) SetupRequired(ctx context.Context) (bool, error) {
	var r struct {
		Required bool `json:"required"`
	}
	resp, err := c.Raw(ctx, http.MethodGet, obx.PathAdminAPI+"/setup", nil, "", false)
	if err != nil {
		return false, err
	}
	err = httpx.Decode(resp, &r)
	return r.Required, err
}

// Setup completes the first-run wizard and logs in.
func (c *Client) Setup(ctx context.Context, in SetupInput) (*Session, error) {
	var s Session
	if err := c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/setup", in, &s); err != nil {
		return nil, err
	}
	c.remember(&s)
	return &s, nil
}

// Login signs in with e-mail and password.
func (c *Client) Login(ctx context.Context, email, password string) (*Session, error) {
	var s Session
	if err := c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/session/login", map[string]string{"email": email, "password": password}, &s); err != nil {
		return nil, err
	}
	c.remember(&s)
	return &s, nil
}

// Logout ends the session.
func (c *Client) Logout(ctx context.Context) error {
	err := c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/session/logout", nil, nil)
	c.mu.Lock()
	c.csrf, c.sess = "", nil
	c.mu.Unlock()
	return err
}

// AcceptInvite sets a password for an invite token and logs in.
func (c *Client) AcceptInvite(ctx context.Context, token, name, password, language string) (*Session, error) {
	body := map[string]string{"password": password}
	if name != "" {
		body["name"] = name
	}
	if language != "" {
		body["language"] = language
	}
	var s Session
	if err := c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/invites/"+url.PathEscape(token)+"/accept", body, &s); err != nil {
		return nil, err
	}
	c.remember(&s)
	return &s, nil
}

// InviteToken extracts the token from an invite URL (<base>/invite/<token>).
func InviteToken(inviteURL string) string {
	i := strings.LastIndex(inviteURL, "/invite/")
	if i < 0 {
		return ""
	}
	t := inviteURL[i+len("/invite/"):]
	if j := strings.IndexAny(t, "?#/"); j >= 0 {
		t = t[:j]
	}
	return t
}

// Settings reads the settings.
func (c *Client) Settings(ctx context.Context) (*Settings, error) {
	var s Settings
	return &s, c.Call(ctx, http.MethodGet, obx.PathAdminAPI+"/settings", nil, &s)
}

// PatchSettings merges a patch into the settings.
func (c *Client) PatchSettings(ctx context.Context, patch any) (*Settings, error) {
	var s Settings
	return &s, c.Call(ctx, http.MethodPatch, obx.PathAdminAPI+"/settings", patch, &s)
}

// Brands lists the brands as ingest records.
func (c *Client) Brands(ctx context.Context) ([]obx.IngestRecord, error) {
	var out obx.Items[obx.IngestRecord]
	return out.Items, c.Call(ctx, http.MethodGet, obx.PathAdminAPI+"/catalog/brands", nil, &out)
}

// BrandID returns the protocol id of the brand with the given external id.
func (c *Client) BrandID(ctx context.Context, externalID string) (string, error) {
	brands, err := c.Brands(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range brands {
		if b.ExternalID == externalID {
			return b.ID, nil
		}
	}
	return "", fmt.Errorf("brand %q not found", externalID)
}

// Products lists admin products.
func (c *Client) Products(ctx context.Context, query url.Values) (*ProductList, error) {
	p := obx.PathAdminAPI + "/catalog/products"
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	var out ProductList
	return &out, c.Call(ctx, http.MethodGet, p, nil, &out)
}

// CatalogWrite sends a catalog write (PUT/PATCH/DELETE) with ingest
// semantics. kind is brand, product, variant or asset.
func (c *Client) CatalogWrite(ctx context.Context, method, kind, externalID string, body any) (*obx.IngestResult, error) {
	var r obx.IngestResult
	err := c.Call(ctx, method, obx.PathAdminAPI+"/catalog/"+kind+"s/"+url.PathEscape(externalID), body, &r)
	return &r, err
}

// CreateRetailer creates a retailer.
func (c *Client) CreateRetailer(ctx context.Context, in RetailerInput) (*Retailer, error) {
	var r Retailer
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/retailers", in, &r)
}

// PatchRetailer changes a retailer (block, grant, originals).
func (c *Client) PatchRetailer(ctx context.Context, id string, patch any) (*Retailer, error) {
	var r Retailer
	return &r, c.Call(ctx, http.MethodPatch, obx.PathAdminAPI+"/retailers/"+url.PathEscape(id), patch, &r)
}

// CreateAPIClient creates an API key for a retailer's software.
func (c *Client) CreateAPIClient(ctx context.Context, in APIClientInput) (*APIClientWithSecret, error) {
	var r APIClientWithSecret
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/api-clients", in, &r)
}

// RotateAPIClient issues a new secret.
func (c *Client) RotateAPIClient(ctx context.Context, id string) (*APIClientWithSecret, error) {
	var r APIClientWithSecret
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/api-clients/"+url.PathEscape(id)+"/rotate", nil, &r)
}

// RevokeAPIClient revokes an API client.
func (c *Client) RevokeAPIClient(ctx context.Context, id string) error {
	return c.Call(ctx, http.MethodDelete, obx.PathAdminAPI+"/api-clients/"+url.PathEscape(id), nil, nil)
}

// CreateIngestToken creates an automatic-upload key.
func (c *Client) CreateIngestToken(ctx context.Context, in IngestTokenInput) (*IngestTokenWithSecret, error) {
	var r IngestTokenWithSecret
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/ingest-tokens", in, &r)
}

// RotateIngestToken issues a new secret for a token.
func (c *Client) RotateIngestToken(ctx context.Context, id string) (*IngestTokenWithSecret, error) {
	var r IngestTokenWithSecret
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/ingest-tokens/"+url.PathEscape(id)+"/rotate", nil, &r)
}

// RevokeIngestToken revokes a token.
func (c *Client) RevokeIngestToken(ctx context.Context, id string) error {
	return c.Call(ctx, http.MethodDelete, obx.PathAdminAPI+"/ingest-tokens/"+url.PathEscape(id), nil, nil)
}

// AccessRequests lists access requests (status optional).
func (c *Client) AccessRequests(ctx context.Context, status string) ([]AccessRequest, error) {
	p := obx.PathAdminAPI + "/access-requests"
	if status != "" {
		p += "?status=" + url.QueryEscape(status)
	}
	var out obx.Page[AccessRequest]
	return out.Items, c.Call(ctx, http.MethodGet, p, nil, &out)
}

// Approve approves an access request.
func (c *Client) Approve(ctx context.Context, id string, in ApproveInput) (*ApproveResult, error) {
	var r ApproveResult
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/access-requests/"+url.PathEscape(id)+"/approve", in, &r)
}

// Reject rejects an access request with an optional reason.
func (c *Client) Reject(ctx context.Context, id, reason string) (*AccessRequest, error) {
	var r AccessRequest
	var body map[string]any
	if reason != "" {
		body = map[string]any{"reason": reason}
	} else {
		body = map[string]any{}
	}
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/access-requests/"+url.PathEscape(id)+"/reject", body, &r)
}

// Invite invites a user.
func (c *Client) Invite(ctx context.Context, in InviteInput) (*InviteResult, error) {
	var r InviteResult
	return &r, c.Call(ctx, http.MethodPost, obx.PathAdminAPI+"/users/invites", in, &r)
}

// Stats reads download statistics.
func (c *Client) Stats(ctx context.Context, days int) (*Stats, error) {
	var s Stats
	p := obx.PathAdminAPI + "/stats"
	if days > 0 {
		p += fmt.Sprintf("?days=%d", days)
	}
	return &s, c.Call(ctx, http.MethodGet, p, nil, &s)
}

// StatsEvents lists recent download events.
func (c *Client) StatsEvents(ctx context.Context, query url.Values) ([]DownloadEvent, error) {
	p := obx.PathAdminAPI + "/stats/events"
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	var out obx.Page[DownloadEvent]
	return out.Items, c.Call(ctx, http.MethodGet, p, nil, &out)
}

// Get fetches path and returns status, headers and body (for downloads).
func (c *Client) Get(ctx context.Context, path string) (int, http.Header, []byte, error) {
	resp, err := c.Raw(ctx, http.MethodGet, path, nil, "", false)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	return resp.StatusCode, resp.Header, b, err
}

// DecodeProblem decodes a problem body (for tests of error answers).
func DecodeProblem(body []byte) *obx.Problem {
	var p obx.Problem
	if json.Unmarshal(body, &p) != nil || p.ObxError == "" {
		return nil
	}
	return &p
}
