// Package client is a Go client for the OBX Protocol, the read side of a
// brand node that retailers' software uses (docs/protocol.md).
//
//	c, _ := client.New(client.Config{BaseURL: "https://brands.kestrel.example", ClientID: id, ClientSecret: secret})
//	err := c.Sync(ctx, client.FileCursorStore("cursor.txt"), client.SyncOptions{}, func(ctx context.Context, ev client.Event) error {
//		// store ev.Change …
//		return nil
//	})
//
// The client discovers the API base, fetches and caches OAuth2 tokens (and
// fetches a new one when a call answers invalid_token), retries 429 answers
// after Retry-After and verifies the sha256 of every downloaded file.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/internal/httpx"
	"github.com/openbrandexchange/obx-go/obx"
)

// Config configures a Client.
type Config struct {
	// BaseURL is the node's address ("https://brands.kestrel.example") or
	// its discovery URL (".../.well-known/obx").
	BaseURL string
	// ClientID and ClientSecret are the API key for your software (OAuth2
	// client credentials). They may be empty for public calls (discovery,
	// access requests).
	ClientID     string
	ClientSecret string
	// Scopes to request; empty means all scopes of the client.
	Scopes []string
	// HTTPClient defaults to a client with a 60 s timeout.
	HTTPClient *http.Client
	// UserAgent is sent with every request.
	UserAgent string
	// MaxRetries for 429/5xx answers (0 = default 4, negative = none).
	MaxRetries int
}

// Client talks to one node.
type Client struct {
	cfg  Config
	base string // scheme://host[:port] without trailing slash
	doer *httpx.Doer

	mu        sync.Mutex
	discovery *obx.DiscoveryDocument
	token     string
	tokenExp  time.Time
}

// New creates a Client. It does not contact the node.
func New(cfg Config) (*Client, error) {
	base, err := NodeBase(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{cfg: cfg, base: base, doer: &httpx.Doer{HTTP: hc, UserAgent: cfg.UserAgent, MaxRetries: cfg.MaxRetries}}, nil
}

// NodeBase normalises a node address: it accepts a bare host, a base URL or
// any URL on the node (discovery, API base) and returns scheme://host.
// Plain http is only accepted for localhost-like hosts.
func NodeBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("obx: node address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("obx: %q is not a web address", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("obx: %q must start with https://", raw)
	}
	p := u.Path
	for _, suffix := range []string{obx.PathDiscovery, obx.PathIngest, obx.PathAPIBase, obx.PathAdminAPI} {
		if i := strings.Index(p, suffix); i >= 0 {
			p = p[:i]
			break
		}
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(p, "/"), nil
}

// Base returns the node base URL.
func (c *Client) Base() string { return c.base }

// HTTP returns the underlying retrying doer (for tools such as the
// conformance suite).
func (c *Client) HTTP() *httpx.Doer { return c.doer }

// apiBase returns the API base from discovery, falling back to base+/obx/v1.
func (c *Client) apiBase(ctx context.Context) string {
	c.mu.Lock()
	d := c.discovery
	c.mu.Unlock()
	if d == nil {
		if dd, err := c.Discovery(ctx); err == nil {
			d = dd
		}
	}
	if d != nil && d.APIBase != "" {
		return strings.TrimRight(d.APIBase, "/")
	}
	return c.base + obx.PathAPIBase
}

// Discovery fetches (and caches) the discovery document.
func (c *Client) Discovery(ctx context.Context) (*obx.DiscoveryDocument, error) {
	c.mu.Lock()
	if c.discovery != nil {
		d := c.discovery
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()
	req, _ := httpx.NewRequest(http.MethodGet, c.base+obx.PathDiscovery, nil, "")
	resp, err := c.doer.Do(ctx, req, true)
	if err != nil {
		return nil, err
	}
	var d obx.DiscoveryDocument
	if err := httpx.Decode(resp, &d); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.discovery = &d
	c.mu.Unlock()
	return &d, nil
}

// Token returns a cached access token, requesting a new one when needed.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()
	tr, err := c.RequestToken(ctx, c.cfg.Scopes)
	if err != nil {
		return "", err
	}
	exp := time.Duration(tr.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = time.Hour
	}
	margin := 60 * time.Second
	if exp < 2*margin {
		margin = exp / 4
	}
	c.mu.Lock()
	c.token, c.tokenExp = tr.AccessToken, time.Now().Add(exp-margin)
	c.mu.Unlock()
	return tr.AccessToken, nil
}

// ForgetToken drops the cached token so the next call requests a new one.
func (c *Client) ForgetToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// TokenEndpoint returns the token URL from discovery (or the default).
func (c *Client) TokenEndpoint(ctx context.Context) string {
	if d, err := c.Discovery(ctx); err == nil && d.Auth.TokenEndpoint != "" {
		return d.Auth.TokenEndpoint
	}
	return c.base + obx.PathAPIBase + "/oauth/token"
}

// RequestToken performs the client-credentials grant (client_secret_basic)
// without caching. Errors are *obx.OAuthError for RFC 6749 answers.
func (c *Client) RequestToken(ctx context.Context, scopes []string) (*obx.TokenResponse, error) {
	if c.cfg.ClientID == "" || c.cfg.ClientSecret == "" {
		return nil, errors.New("obx: this call needs an API key for your software (client id and secret)")
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	req, _ := httpx.NewRequest(http.MethodPost, c.TokenEndpoint(ctx), []byte(form.Encode()), "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
	resp, err := c.doer.Do(ctx, req, false)
	if err != nil {
		return nil, err
	}
	var tr obx.TokenResponse
	if err := httpx.Decode(resp, &tr); err != nil {
		return nil, err
	}
	if tr.AccessToken == "" {
		return nil, errors.New("obx: token endpoint returned no access_token")
	}
	return &tr, nil
}

// Do sends an authenticated request to path (relative to the API base, for
// example "/changes?limit=10", or an absolute URL on the node). When the node
// answers invalid_token the token is renewed and the request sent once more.
// The caller closes the response body; non-2xx responses are returned as is.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, contentType string, header http.Header) (*http.Response, error) {
	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = c.apiBase(ctx) + path
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.Token(ctx)
		if err != nil {
			return nil, err
		}
		req, err := httpx.NewRequest(method, u, body, contentType)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		idem := method == http.MethodGet || method == http.MethodHead || method == http.MethodPut || method == http.MethodDelete
		resp, err := c.doer.Do(ctx, req, idem)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			err := httpx.Error(resp)
			if obx.IsCode(err, obx.ErrInvalidToken) {
				c.ForgetToken()
				continue
			}
			return nil, err
		}
		return resp, nil
	}
}

// getJSON GETs path and decodes the answer into v.
func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.Do(ctx, http.MethodGet, path, nil, "", nil)
	if err != nil {
		return err
	}
	return httpx.Decode(resp, v)
}

// sendJSON sends body as JSON and decodes the answer into v.
func (c *Client) sendJSON(ctx context.Context, method, path string, body any, v any, header http.Header) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, method, path, raw, "application/json", header)
	if err != nil {
		return err
	}
	return httpx.Decode(resp, v)
}

// Grant returns what the caller may see.
func (c *Client) Grant(ctx context.Context) (*obx.Grant, error) {
	var g obx.Grant
	return &g, c.getJSON(ctx, "/grant", &g)
}

// Brands lists the visible brands.
func (c *Client) Brands(ctx context.Context) ([]obx.Brand, error) {
	var out obx.Items[obx.Brand]
	return out.Items, c.getJSON(ctx, "/brands", &out)
}

// Brand returns one brand.
func (c *Client) Brand(ctx context.Context, id string) (*obx.Brand, error) {
	var b obx.Brand
	return &b, c.getJSON(ctx, "/brands/"+url.PathEscape(id), &b)
}

// ProductsQuery filters GET /products.
type ProductsQuery struct {
	IDs          []string
	BrandID      string
	UpdatedSince *time.Time
	Cursor       string
	Limit        int
}

func (q ProductsQuery) values() url.Values {
	v := url.Values{}
	if len(q.IDs) > 0 {
		v.Set("ids", strings.Join(q.IDs, ","))
	}
	if q.BrandID != "" {
		v.Set("brand_id", q.BrandID)
	}
	if q.UpdatedSince != nil {
		v.Set("updated_since", q.UpdatedSince.UTC().Format(time.RFC3339))
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	if q.Limit > 0 {
		v.Set("limit", fmt.Sprint(q.Limit))
	}
	return v
}

// Products returns one page of products.
func (c *Client) Products(ctx context.Context, q ProductsQuery) (*obx.Page[obx.ProductDetail], error) {
	var p obx.Page[obx.ProductDetail]
	path := "/products"
	if enc := q.values().Encode(); enc != "" {
		path += "?" + enc
	}
	return &p, c.getJSON(ctx, path, &p)
}

// EachProduct walks all pages of a product query.
func (c *Client) EachProduct(ctx context.Context, q ProductsQuery, fn func(*obx.ProductDetail) error) error {
	for {
		page, err := c.Products(ctx, q)
		if err != nil {
			return err
		}
		for i := range page.Items {
			if err := fn(&page.Items[i]); err != nil {
				return err
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return nil
		}
		q.Cursor = *page.NextCursor
	}
}

// Product returns one product with its variants and assets.
func (c *Client) Product(ctx context.Context, id string) (*obx.ProductDetail, error) {
	var p obx.ProductDetail
	return &p, c.getJSON(ctx, "/products/"+url.PathEscape(id), &p)
}

// GTINLookupMax is the most GTINs one POST lookup may contain.
const GTINLookupMax = 2000

// LookupGTINs looks products up by GTIN (EAN). Longer lists are split into
// several requests and merged.
func (c *Client) LookupGTINs(ctx context.Context, gtins []string) (*obx.GTINLookupResult, error) {
	out := &obx.GTINLookupResult{Results: []obx.GTINResult{}, Products: []obx.ProductDetail{}}
	seen := map[string]bool{}
	for start := 0; start < len(gtins) || start == 0; start += GTINLookupMax {
		end := min(start+GTINLookupMax, len(gtins))
		var r obx.GTINLookupResult
		if err := c.sendJSON(ctx, http.MethodPost, "/products:by-gtin", map[string]any{"gtins": gtins[start:end]}, &r, nil); err != nil {
			return nil, err
		}
		out.Results = append(out.Results, r.Results...)
		for _, p := range r.Products {
			if !seen[p.ID] {
				seen[p.ID] = true
				out.Products = append(out.Products, p)
			}
		}
		if end >= len(gtins) {
			break
		}
	}
	return out, nil
}

// Asset returns one asset with fresh rendition URLs.
func (c *Client) Asset(ctx context.Context, id string) (*obx.Asset, error) {
	var a obx.Asset
	return &a, c.getJSON(ctx, "/assets/"+url.PathEscape(id), &a)
}

// ReportUsage sends usage events (at most 1000 per call; longer lists are
// split) and returns how many the node accepted.
func (c *Client) ReportUsage(ctx context.Context, events []obx.UsageEvent) (int, error) {
	total := 0
	for start := 0; start < len(events); start += 1000 {
		end := min(start+1000, len(events))
		var r struct {
			Accepted int `json:"accepted"`
		}
		if err := c.sendJSON(ctx, http.MethodPost, "/usage", obx.Usage{Events: events[start:end]}, &r, nil); err != nil {
			return total, err
		}
		total += r.Accepted
	}
	return total, nil
}

// Webhooks lists the caller's webhook subscriptions.
func (c *Client) Webhooks(ctx context.Context) ([]obx.WebhookSubscription, error) {
	var out obx.Items[obx.WebhookSubscription]
	return out.Items, c.getJSON(ctx, "/webhooks", &out)
}

// CreateWebhook subscribes url to events. The returned Secret is shown once.
func (c *Client) CreateWebhook(ctx context.Context, in obx.WebhookInput) (*obx.WebhookSubscription, error) {
	var s obx.WebhookSubscription
	return &s, c.sendJSON(ctx, http.MethodPost, "/webhooks", in, &s, nil)
}

// DeleteWebhook removes a subscription.
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	resp, err := c.Do(ctx, http.MethodDelete, "/webhooks/"+url.PathEscape(id), nil, "", nil)
	if err != nil {
		return err
	}
	return httpx.Decode(resp, nil)
}

// TestWebhook asks the node to send a webhook.test event now.
func (c *Client) TestWebhook(ctx context.Context, id string) (*obx.WebhookTestResult, error) {
	var r obx.WebhookTestResult
	return &r, c.sendJSON(ctx, http.MethodPost, "/webhooks/"+url.PathEscape(id)+"/test", map[string]any{}, &r, nil)
}
