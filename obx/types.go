package obx

import (
	"encoding/json"
	"fmt"
	"time"
)

// LocalizedText is free text per BCP 47 language tag, for example
// {"nl": "Trui", "en": "Sweater"}.
type LocalizedText map[string]string

// Get returns the text in lang, falling back to the first language in
// fallbacks that has a value, then to any value (in sorted tag order).
func (t LocalizedText) Get(lang string, fallbacks ...string) string {
	if v, ok := t[lang]; ok && v != "" {
		return v
	}
	for _, f := range fallbacks {
		if v, ok := t[f]; ok && v != "" {
			return v
		}
	}
	best := ""
	bestTag := ""
	for tag, v := range t {
		if v != "" && (bestTag == "" || tag < bestTag) {
			best, bestTag = v, tag
		}
	}
	return best
}

// Brand is a label published by a node (schemas/Brand.json).
type Brand struct {
	ID          string        `json:"id"`
	ExternalID  string        `json:"external_id"`
	Name        string        `json:"name"`
	LogoURL     *string       `json:"logo_url"`
	Website     *string       `json:"website"`
	Description LocalizedText `json:"description"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Fiber is one fibre with its percentage.
type Fiber struct {
	Fiber      string  `json:"fiber"`
	Percentage float64 `json:"percentage"`
}

// MaterialPart is the composition of one garment part (schemas/Materials.json).
type MaterialPart struct {
	Part   string  `json:"part"`
	Fibers []Fiber `json:"fibers"`
}

// Materials is the fibre composition per part.
type Materials []MaterialPart

// CareInstruction is one ISO 3758 aligned care code (schemas/Care.json).
type CareInstruction struct {
	Code string        `json:"code"`
	Text LocalizedText `json:"text"`
}

// Care is an ordered list of care instructions.
type Care []CareInstruction

// Product is a style/model (schemas/Product.json). In /changes it never
// carries Variants or Assets; see ProductDetail.
type Product struct {
	ID              string         `json:"id"`
	ExternalID      string         `json:"external_id"`
	BrandID         string         `json:"brand_id"`
	StyleNumber     string         `json:"style_number"`
	Name            LocalizedText  `json:"name"`
	Description     LocalizedText  `json:"description"`
	ItemType        *string        `json:"item_type"`
	GPCBrick        *string        `json:"gpc_brick"`
	TargetGroup     *string        `json:"target_group"`
	Season          *string        `json:"season"`
	Collection      *string        `json:"collection"`
	Materials       Materials      `json:"materials"`
	Care            Care           `json:"care"`
	CountryOfOrigin *string        `json:"country_of_origin"`
	DPPURL          *string        `json:"dpp_url"`
	Attributes      map[string]any `json:"attributes"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// ProductDetail is a product with its published variants and visible assets
// (schemas/ProductDetail.json), as returned by product endpoints and lookups.
type ProductDetail struct {
	Product
	Variants []Variant `json:"variants"`
	Assets   []Asset   `json:"assets"`
}

// MarshalJSON keeps Variants and Assets as arrays (never null).
func (p ProductDetail) MarshalJSON() ([]byte, error) {
	type plain ProductDetail
	q := plain(p)
	if q.Variants == nil {
		q.Variants = []Variant{}
	}
	if q.Assets == nil {
		q.Assets = []Asset{}
	}
	return json.Marshal(q)
}

// Color of a variant (schemas/Color.json).
type Color struct {
	Code          string        `json:"code"`
	Name          LocalizedText `json:"name"`
	SupplierColor *string       `json:"supplier_color"`
}

// Variant is a sellable colour/size (schemas/Variant.json).
type Variant struct {
	ID           string    `json:"id"`
	ExternalID   string    `json:"external_id"`
	ProductID    string    `json:"product_id"`
	BrandID      string    `json:"brand_id"`
	GTIN         *string   `json:"gtin"`
	GTINOriginal *string   `json:"gtin_original"`
	SKU          *string   `json:"sku"`
	Color        Color     `json:"color"`
	Size         *string   `json:"size"`
	SizeSystem   *string   `json:"size_system"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Rights are the usage rights of an asset (schemas/Rights.json).
type Rights struct {
	License      string     `json:"license"`
	LicenseText  *string    `json:"license_text"`
	Channels     []string   `json:"channels"`
	Territories  []string   `json:"territories"`
	EmbargoUntil *time.Time `json:"embargo_until"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Credit       *string    `json:"credit"`
	ModelRelease *bool      `json:"model_release"`
}

// DefaultRights returns the rights of a node that configured none
// (docs/protocol.md section 5.6).
func DefaultRights() Rights {
	return Rights{License: "retailer-use", Channels: []string{ChannelWebshop, ChannelMarketplace, ChannelSocial}}
}

// Provenance says how an image was made (schemas/Provenance.json).
type Provenance struct {
	AIGenerated     bool    `json:"ai_generated"`
	AIEdited        bool    `json:"ai_edited"`
	Source          string  `json:"source"`
	C2PA            string  `json:"c2pa"`
	C2PAManifestURL *string `json:"c2pa_manifest_url"`
	Generator       *string `json:"generator"`
}

// DefaultProvenance returns the provenance a node assumes when the brand sent none.
func DefaultProvenance() Provenance {
	return Provenance{Source: "photo", C2PA: "none"}
}

// Rendition is one downloadable file of an asset (schemas/Rendition.json).
type Rendition struct {
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	URLExpiresAt *time.Time `json:"url_expires_at"`
	ContentType  string     `json:"content_type"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	Bytes        int64      `json:"bytes"`
	SHA256       string     `json:"sha256"`
}

// Renditions of an asset. Original is only present when the caller may
// download originals.
type Renditions struct {
	Original *Rendition `json:"original,omitempty"`
	Web      Rendition  `json:"web"`
	Square   Rendition  `json:"square"`
	Thumb    Rendition  `json:"thumb"`
}

// Get returns the rendition by name, or nil.
func (r *Renditions) Get(name string) *Rendition {
	switch name {
	case RenditionOriginal:
		return r.Original
	case RenditionWeb:
		return &r.Web
	case RenditionSquare:
		return &r.Square
	case RenditionThumb:
		return &r.Thumb
	}
	return nil
}

// Asset is a final product image (schemas/Asset.json).
type Asset struct {
	ID          string        `json:"id"`
	ExternalID  string        `json:"external_id"`
	ProductID   string        `json:"product_id"`
	BrandID     string        `json:"brand_id"`
	MediaType   string        `json:"media_type"`
	ColorCode   *string       `json:"color_code"`
	VariantIDs  []string      `json:"variant_ids"`
	Role        string        `json:"role"`
	View        *string       `json:"view"`
	Position    int           `json:"position"`
	Filename    string        `json:"filename"`
	AltText     LocalizedText `json:"alt_text"`
	ContentType string        `json:"content_type"`
	Width       int           `json:"width"`
	Height      int           `json:"height"`
	Bytes       int64         `json:"bytes"`
	SHA256      string        `json:"sha256"`
	Rights      Rights        `json:"rights"`
	Provenance  Provenance    `json:"provenance"`
	Renditions  Renditions    `json:"renditions"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// GrantRetailer names the retailer a grant belongs to.
type GrantRetailer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Grant is what a retailer may see (schemas/Grant.json).
type Grant struct {
	ID              string        `json:"id"`
	Retailer        GrantRetailer `json:"retailer"`
	BrandIDs        []string      `json:"brand_ids"`
	Scopes          []string      `json:"scopes"`
	OriginalAllowed bool          `json:"original_allowed"`
	Status          string        `json:"status"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// Change is one entry of the change feed (schemas/Change.json).
type Change struct {
	Seq        int64           `json:"seq"`
	Type       string          `json:"type"`
	Op         string          `json:"op"`
	ID         string          `json:"id"`
	ExternalID *string         `json:"external_id"`
	BrandID    *string         `json:"brand_id"`
	ProductID  *string         `json:"product_id"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Data       json.RawMessage `json:"data,omitempty"`
	Resync     *bool           `json:"resync,omitempty"`
}

// ExternalIDValue returns the external id or "".
func (c *Change) ExternalIDValue() string {
	if c.ExternalID == nil {
		return ""
	}
	return *c.ExternalID
}

// IsResync reports whether the entry tells the consumer to restart without a cursor.
func (c *Change) IsResync() bool { return c.Resync != nil && *c.Resync }

// Decode unmarshals Data into v (for example *Product for a product upsert).
func (c *Change) Decode(v any) error {
	if len(c.Data) == 0 {
		return fmt.Errorf("obx: %s %s entry %d has no data", c.Type, c.Op, c.Seq)
	}
	return json.Unmarshal(c.Data, v)
}

// ChangesPage is one page of the change feed (schemas/ChangesPage.json).
type ChangesPage struct {
	Changes    []Change `json:"changes"`
	NextCursor string   `json:"next_cursor"`
	HasMore    bool     `json:"has_more"`
	Phase      string   `json:"phase"`
}

// DiscoveryNode identifies the node software.
type DiscoveryNode struct {
	Software string `json:"software"`
	Version  string `json:"version"`
}

// Publisher is the brand company behind a node.
type Publisher struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	LogoURL      *string       `json:"logo_url"`
	AccentColor  *string       `json:"accent_color"`
	Website      *string       `json:"website"`
	ContactEmail *string       `json:"contact_email"`
	WelcomeText  LocalizedText `json:"welcome_text"`
}

// DiscoveryAuth describes the token endpoint.
type DiscoveryAuth struct {
	TokenEndpoint                     string   `json:"token_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// DiscoveryBrand is a published brand as listed in discovery.
type DiscoveryBrand struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	LogoURL *string `json:"logo_url"`
}

// DiscoveryLimits are the node's protocol limits.
type DiscoveryLimits struct {
	ChangesPageMax         int `json:"changes_page_max"`
	ProductsPageMax        int `json:"products_page_max"`
	GTINLookupMax          int `json:"gtin_lookup_max"`
	TombstoneRetentionDays int `json:"tombstone_retention_days"`
}

// DiscoveryDocument is served at /.well-known/obx (schemas/DiscoveryDocument.json).
type DiscoveryDocument struct {
	ObxVersion        string           `json:"obx_version"`
	SpecVersion       string           `json:"spec_version"`
	Node              DiscoveryNode    `json:"node"`
	Publisher         Publisher        `json:"publisher"`
	APIBase           string           `json:"api_base"`
	PortalURL         *string          `json:"portal_url"`
	Auth              DiscoveryAuth    `json:"auth"`
	AccessRequestsURL *string          `json:"access_requests_url"`
	IngestURL         *string          `json:"ingest_url"`
	Capabilities      []string         `json:"capabilities"`
	Brands            []DiscoveryBrand `json:"brands"`
	Languages         []string         `json:"languages"`
	Limits            DiscoveryLimits  `json:"limits"`
}

// Has reports whether the node advertises capability c.
func (d *DiscoveryDocument) Has(c string) bool { return Contains(d.Capabilities, c) }

// TokenResponse is the OAuth2 token response (schemas/TokenResponse.json).
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// OAuthError is the RFC 6749 error body of the token endpoint.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	HTTPStatus  int    `json:"-"`
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return "oauth: " + e.Code + ": " + e.Description
	}
	return "oauth: " + e.Code
}

// AccessRequestRetailer is the retailer part of an access request.
type AccessRequestRetailer struct {
	Name      string  `json:"name"`
	Reference *string `json:"reference"`
	Website   *string `json:"website"`
	VATNumber *string `json:"vat_number"`
	Country   *string `json:"country"`
}

// AccessRequestContact is the contact person of an access request.
type AccessRequestContact struct {
	Name     string  `json:"name"`
	Email    string  `json:"email"`
	Phone    *string `json:"phone"`
	Language *string `json:"language"`
}

// AccessRequestInput is the body of POST /obx/v1/access-requests.
type AccessRequestInput struct {
	Retailer          AccessRequestRetailer `json:"retailer"`
	Contact           AccessRequestContact  `json:"contact"`
	Access            []string              `json:"access,omitempty"`
	RequestedBrandIDs []string              `json:"requested_brand_ids"`
	Message           *string               `json:"message"`
}

// AccessRequestPortal tells the requester about portal access.
type AccessRequestPortal struct {
	Invited  bool   `json:"invited"`
	LoginURL string `json:"login_url"`
}

// AccessRequestCredentials are the API credentials of an approved request.
// ClientSecret is only set on the first read after approval.
type AccessRequestCredentials struct {
	ClientID      string   `json:"client_id"`
	ClientSecret  *string  `json:"client_secret"`
	TokenEndpoint string   `json:"token_endpoint"`
	Scopes        []string `json:"scopes"`
	Claimed       bool     `json:"claimed"`
}

// AccessRequest is the requester's view of an access request (schemas/AccessRequest.json).
type AccessRequest struct {
	ID           string                    `json:"id"`
	Status       string                    `json:"status"`
	CreatedAt    time.Time                 `json:"created_at"`
	DecidedAt    *time.Time                `json:"decided_at"`
	Reason       *string                   `json:"reason"`
	RequestToken string                    `json:"request_token,omitempty"`
	StatusURL    string                    `json:"status_url,omitempty"`
	Portal       *AccessRequestPortal      `json:"portal"`
	Credentials  *AccessRequestCredentials `json:"credentials"`
}

// GTINResult is one result of a GTIN lookup.
type GTINResult struct {
	Input     string  `json:"input"`
	GTIN      *string `json:"gtin"`
	Status    string  `json:"status"`
	ProductID *string `json:"product_id"`
	VariantID *string `json:"variant_id"`
}

// GTIN lookup statuses.
const (
	GTINMatched   = "matched"
	GTINUnmatched = "unmatched"
	GTINInvalid   = "invalid"
)

// GTINLookupResult is the answer of a GTIN lookup (schemas/GtinLookupResult.json).
type GTINLookupResult struct {
	Results  []GTINResult    `json:"results"`
	Products []ProductDetail `json:"products"`
}

// ExportFilter selects what an export contains.
type ExportFilter struct {
	BrandIDs     []string   `json:"brand_ids"`
	ProductIDs   []string   `json:"product_ids"`
	GTINs        []string   `json:"gtins"`
	UpdatedSince *time.Time `json:"updated_since"`
}

// ExportRequest is the body of POST /obx/v1/exports (schemas/ExportRequest.json).
type ExportRequest struct {
	Filter      *ExportFilter `json:"filter,omitempty"`
	Renditions  []string      `json:"renditions"`
	IncludeData *bool         `json:"include_data,omitempty"`
}

// Progress of a job.
type Progress struct {
	Done  int  `json:"done"`
	Total *int `json:"total"`
}

// ExportPart is one independent zip of an export.
type ExportPart struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Files  int    `json:"files"`
}

// Export is a bulk export (schemas/Export.json).
type Export struct {
	ID          string       `json:"id"`
	Status      string       `json:"status"`
	CreatedAt   time.Time    `json:"created_at"`
	FinishedAt  *time.Time   `json:"finished_at"`
	ExpiresAt   *time.Time   `json:"expires_at"`
	Progress    Progress     `json:"progress"`
	ManifestURL *string      `json:"manifest_url"`
	Parts       []ExportPart `json:"parts"`
	Error       *Problem     `json:"error"`
}

// ExportCounts are the counts in an export manifest header.
type ExportCounts struct {
	Products int `json:"products"`
	Variants int `json:"variants"`
	Assets   int `json:"assets"`
	Files    int `json:"files"`
}

// ExportManifestLine is one NDJSON line of an export manifest
// (schemas/ExportManifestLine.json). Which fields are set depends on Type:
// header, brand, product, variant, asset or file.
type ExportManifestLine struct {
	Type string `json:"type"`

	// header
	ExportID      string        `json:"export_id,omitempty"`
	CreatedAt     *time.Time    `json:"created_at,omitempty"`
	ObxVersion    string        `json:"obx_version,omitempty"`
	ChangesCursor string        `json:"changes_cursor,omitempty"`
	Counts        *ExportCounts `json:"counts,omitempty"`

	// brand, product, variant, asset
	Data json.RawMessage `json:"data,omitempty"`

	// file
	AssetID   string `json:"asset_id,omitempty"`
	Rendition string `json:"rendition,omitempty"`
	Part      int    `json:"part,omitempty"`
	Path      string `json:"path,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

// UsageEvent is one usage report (schemas/UsageEvent.json).
type UsageEvent struct {
	AssetID   string    `json:"asset_id"`
	Rendition *string   `json:"rendition"`
	Event     string    `json:"event"`
	Channel   *string   `json:"channel"`
	At        time.Time `json:"at"`
	Count     int       `json:"count,omitempty"`
}

// Usage is the body of POST /obx/v1/usage (schemas/Usage.json).
type Usage struct {
	Events []UsageEvent `json:"events"`
}

// WebhookEvent is a delivered webhook (schemas/WebhookEvent.json).
type WebhookEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// WebhookSubscription is a webhook subscription (schemas/WebhookSubscription.json).
// Secret is only returned on creation.
type WebhookSubscription struct {
	ID             string     `json:"id"`
	URL            string     `json:"url"`
	Events         []string   `json:"events"`
	Secret         *string    `json:"secret"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	LastDeliveryAt *time.Time `json:"last_delivery_at"`
	LastStatus     *int       `json:"last_status"`
}

// WebhookInput is the body of POST /obx/v1/webhooks.
type WebhookInput struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// WebhookTestResult is the answer of POST /obx/v1/webhooks/{id}/test.
type WebhookTestResult struct {
	Delivered bool `json:"delivered"`
	Status    *int `json:"status"`
}

// DirectoryEntry is one listed node (schemas/DirectoryEntry.json).
type DirectoryEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	DiscoveryURL string   `json:"discovery_url"`
	Website      *string  `json:"website"`
	Brands       []string `json:"brands"`
	Countries    []string `json:"countries,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	Languages    []string `json:"languages"`
	ListedAt     string   `json:"listed_at"`
}

// Directory is a list of discovery URLs (schemas/Directory.json).
type Directory struct {
	Version   int              `json:"version"`
	UpdatedAt time.Time        `json:"updated_at"`
	Entries   []DirectoryEntry `json:"entries"`
}

// Page is the generic list shape {items, next_cursor}.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// Items is the unpaginated list shape {items}.
type Items[T any] struct {
	Items []T `json:"items"`
}

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }

// Str returns *s or "" when s is nil.
func Str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// StrPtr returns nil for "" and &s otherwise.
func StrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Pick returns the text for a reader who prefers lang, using the fallback
// rule shared by every OBX node and UI: the exact language, then the same
// base language ("nl" for "nl-BE" and the other way round), then English,
// then the first available language in sorted tag order. It returns "" when
// the text is empty.
func (t LocalizedText) Pick(lang string) string {
	if len(t) == 0 {
		return ""
	}
	if v := t[lang]; v != "" {
		return v
	}
	base := baseLang(lang)
	if base != "" {
		if v := t[base]; v != "" {
			return v
		}
		best, bestTag := "", ""
		for tag, v := range t {
			if v != "" && baseLang(tag) == base && (bestTag == "" || tag < bestTag) {
				best, bestTag = v, tag
			}
		}
		if best != "" {
			return best
		}
	}
	if v := t["en"]; v != "" {
		return v
	}
	return t.Get("")
}

func baseLang(tag string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] == '-' || tag[i] == '_' {
			return tag[:i]
		}
	}
	return tag
}
