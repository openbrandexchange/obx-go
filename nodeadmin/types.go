// Package nodeadmin is a client for the non-normative Node Admin + Portal
// API (openapi/node-admin.yaml in the spec) that the shared admin-ui uses:
// first-run setup, sessions with CSRF, retailers, API keys, automatic-upload
// keys, imports and the retailer portal. The conformance suite uses it to
// drive a node the way the UI does.
package nodeadmin

import (
	"encoding/json"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

// PublicBranding is shown on the login and request-access pages.
type PublicBranding struct {
	Name                  string            `json:"name"`
	LogoURL               *string           `json:"logo_url"`
	AccentColor           *string           `json:"accent_color"`
	WelcomeText           obx.LocalizedText `json:"welcome_text"`
	Languages             []string          `json:"languages"`
	DefaultLanguage       string            `json:"default_language"`
	AccessRequestsEnabled bool              `json:"access_requests_enabled"`
	SetupRequired         bool              `json:"setup_required"`
	Brands                []IDName          `json:"brands,omitempty"`
}

// IDName is an {id, name} pair.
type IDName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SetupCompany is the company part of the setup wizard.
type SetupCompany struct {
	Name            string   `json:"name"`
	FirstBrandName  string   `json:"first_brand_name,omitempty"`
	Website         string   `json:"website,omitempty"`
	ContactEmail    string   `json:"contact_email,omitempty"`
	DefaultLanguage string   `json:"default_language"`
	Languages       []string `json:"languages,omitempty"`
	AccentColor     string   `json:"accent_color,omitempty"`
}

// SetupAdmin is the first admin account.
type SetupAdmin struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SetupInput is the body of POST /admin/api/setup.
type SetupInput struct {
	SetupCode string       `json:"setup_code"`
	Company   SetupCompany `json:"company"`
	Admin     SetupAdmin   `json:"admin"`
}

// User is an admin or retailer account.
type User struct {
	ID              string     `json:"id"`
	Email           string     `json:"email"`
	Name            string     `json:"name"`
	Role            string     `json:"role"`
	RetailerID      *string    `json:"retailer_id"`
	Status          string     `json:"status"`
	Language        string     `json:"language"`
	LastLoginAt     *time.Time `json:"last_login_at"`
	InviteExpiresAt *time.Time `json:"invite_expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Roles.
const (
	RoleAdmin    = "admin"
	RoleRetailer = "retailer"
)

// Session is the answer of the session endpoints.
type Session struct {
	Authenticated bool    `json:"authenticated"`
	CSRFToken     string  `json:"csrf_token"`
	SetupRequired bool    `json:"setup_required"`
	User          *User   `json:"user"`
	Retailer      *IDName `json:"retailer"`
}

// Settings are the node settings (every part optional in a PATCH).
type Settings struct {
	Company  *SettingsCompany  `json:"company,omitempty"`
	Branding *SettingsBranding `json:"branding,omitempty"`
	Portal   *SettingsPortal   `json:"portal,omitempty"`
	Defaults *SettingsDefaults `json:"defaults,omitempty"`
	Node     *SettingsNode     `json:"node,omitempty"`
}

// SettingsCompany is the brand company profile.
type SettingsCompany struct {
	Name         string  `json:"name,omitempty"`
	Website      *string `json:"website,omitempty"`
	ContactEmail *string `json:"contact_email,omitempty"`
	Country      *string `json:"country,omitempty"`
}

// SettingsBranding is the portal branding.
type SettingsBranding struct {
	DisplayName string            `json:"display_name,omitempty"`
	LogoURL     *string           `json:"logo_url,omitempty"`
	AccentColor *string           `json:"accent_color,omitempty"`
	WelcomeText obx.LocalizedText `json:"welcome_text,omitempty"`
}

// SettingsPortal are the portal switches.
type SettingsPortal struct {
	AccessRequestsEnabled  *bool    `json:"access_requests_enabled,omitempty"`
	AllowOriginalDownload  *bool    `json:"allow_original_download,omitempty"`
	AllowArticleDataExport *bool    `json:"allow_article_data_export,omitempty"`
	RetailerAPISelfService *bool    `json:"retailer_api_self_service,omitempty"`
	Languages              []string `json:"languages,omitempty"`
	DefaultLanguage        string   `json:"default_language,omitempty"`
}

// SettingsDefaults are the default rights and publication status.
type SettingsDefaults struct {
	Rights       *obx.Rights `json:"rights,omitempty"`
	IngestStatus string      `json:"ingest_status,omitempty"`
}

// SettingsNode are read-only facts.
type SettingsNode struct {
	PublicURL                 string `json:"public_url,omitempty"`
	DiscoveryURL              string `json:"discovery_url,omitempty"`
	Software                  string `json:"software,omitempty"`
	Version                   string `json:"version,omitempty"`
	EmailConfigured           bool   `json:"email_configured"`
	RenditionsServerGenerated bool   `json:"renditions_server_generated"`
	MaxUploadBytes            int64  `json:"max_upload_bytes,omitempty"`
	CustomDomainHelp          string `json:"custom_domain_help,omitempty"`
}

// ProductSummary is a row of the admin product list.
type ProductSummary struct {
	ID                string            `json:"id"`
	ExternalID        string            `json:"external_id"`
	BrandExternalID   string            `json:"brand_external_id,omitempty"`
	StyleNumber       string            `json:"style_number"`
	Name              obx.LocalizedText `json:"name"`
	Status            string            `json:"status"`
	Visibility        string            `json:"visibility"`
	VariantCount      int               `json:"variant_count"`
	AssetCount        int               `json:"asset_count"`
	PendingAssetCount int               `json:"pending_asset_count"`
	ThumbURL          *string           `json:"thumb_url"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// ProductList is the answer of GET /admin/api/catalog/products.
type ProductList struct {
	Items      []ProductSummary `json:"items"`
	NextCursor *string          `json:"next_cursor"`
	Total      int              `json:"total"`
}

// Asset is an admin view of an asset.
type Asset struct {
	Record     obx.IngestRecord          `json:"record"`
	Renditions map[string]*obx.Rendition `json:"renditions,omitempty"`
}

// ProductDetail is the admin view of one product.
type ProductDetail struct {
	Record   obx.IngestRecord   `json:"record"`
	Variants []obx.IngestRecord `json:"variants"`
	Assets   []Asset            `json:"assets"`
}

// Retailer is a retailer company.
type Retailer struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Reference      *string    `json:"reference"`
	Website        *string    `json:"website"`
	VATNumber      *string    `json:"vat_number"`
	Country        *string    `json:"country"`
	Notes          *string    `json:"notes"`
	Status         string     `json:"status"`
	BrandIDs       []string   `json:"brand_ids"`
	AllowOriginal  *bool      `json:"allow_original"`
	UserCount      int        `json:"user_count"`
	APIClientCount int        `json:"api_client_count"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

// RetailerInput creates or patches a retailer. BrandIDs uses a pointer to a
// slice so a patch can distinguish "all brands" (null) from "unchanged".
type RetailerInput struct {
	Name          string    `json:"name,omitempty"`
	Reference     *string   `json:"reference,omitempty"`
	Website       *string   `json:"website,omitempty"`
	VATNumber     *string   `json:"vat_number,omitempty"`
	Country       *string   `json:"country,omitempty"`
	Notes         *string   `json:"notes,omitempty"`
	Status        string    `json:"status,omitempty"`
	BrandIDs      *[]string `json:"brand_ids,omitempty"`
	AllowOriginal *bool     `json:"allow_original,omitempty"`
}

// AccessRequest is the admin view of an access request.
type AccessRequest struct {
	ID                string                    `json:"id"`
	Status            string                    `json:"status"`
	CreatedAt         time.Time                 `json:"created_at"`
	DecidedAt         *time.Time                `json:"decided_at"`
	Reason            *string                   `json:"reason"`
	Retailer          obx.AccessRequestRetailer `json:"retailer"`
	Contact           obx.AccessRequestContact  `json:"contact"`
	Access            []string                  `json:"access"`
	RequestedBrandIDs []string                  `json:"requested_brand_ids"`
	Message           *string                   `json:"message"`
	RetailerID        *string                   `json:"retailer_id"`
	SourceIPCountry   *string                   `json:"source_ip_country"`
}

// ApproveInput is the body of POST /admin/api/access-requests/{id}/approve.
type ApproveInput struct {
	RetailerID       string    `json:"retailer_id,omitempty"`
	BrandIDs         *[]string `json:"brand_ids,omitempty"`
	InvitePortalUser *bool     `json:"invite_portal_user,omitempty"`
	CreateAPIClient  *bool     `json:"create_api_client,omitempty"`
}

// ApproveResult is the answer of an approval.
type ApproveResult struct {
	AccessRequest AccessRequest `json:"access_request"`
	Retailer      Retailer      `json:"retailer"`
	Invite        *InviteResult `json:"invite,omitempty"`
	APIClient     *APIClient    `json:"api_client,omitempty"`
}

// InviteInput is the body of POST /admin/api/users/invites.
type InviteInput struct {
	Email      string `json:"email"`
	Name       string `json:"name,omitempty"`
	Role       string `json:"role"`
	RetailerID string `json:"retailer_id,omitempty"`
	Language   string `json:"language,omitempty"`
}

// InviteResult is a created invitation.
type InviteResult struct {
	User      User      `json:"user"`
	InviteURL string    `json:"invite_url"`
	ExpiresAt time.Time `json:"expires_at"`
	EmailSent bool      `json:"email_sent"`
}

// InviteInfo is the public view of an invite token.
type InviteInfo struct {
	Email        string    `json:"email"`
	Name         *string   `json:"name"`
	Role         string    `json:"role"`
	RetailerName *string   `json:"retailer_name"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// APIClient is an API key for a retailer's software.
type APIClient struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"client_id"`
	Name       string     `json:"name"`
	RetailerID string     `json:"retailer_id"`
	Scopes     []string   `json:"scopes"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// APIClientInput creates an API client.
type APIClientInput struct {
	RetailerID string   `json:"retailer_id,omitempty"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes,omitempty"`
}

// APIClientWithSecret is shown once, on creation and rotation.
type APIClientWithSecret struct {
	Client        APIClient `json:"client"`
	ClientSecret  string    `json:"client_secret"`
	TokenEndpoint string    `json:"token_endpoint"`
	DiscoveryURL  string    `json:"discovery_url"`
}

// IngestToken is an automatic-upload key.
type IngestToken struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Prefix           string     `json:"prefix"`
	Scopes           []string   `json:"scopes"`
	BrandExternalIDs []string   `json:"brand_external_ids"`
	Status           string     `json:"status"`
	ExpiresAt        *time.Time `json:"expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
	LastUsedAt       *time.Time `json:"last_used_at"`
}

// IngestTokenInput creates an automatic-upload key.
type IngestTokenInput struct {
	Name             string     `json:"name"`
	Scopes           []string   `json:"scopes"`
	BrandExternalIDs []string   `json:"brand_external_ids,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

// IngestTokenWithSecret is shown once.
type IngestTokenWithSecret struct {
	Token     IngestToken `json:"token"`
	Secret    string      `json:"secret"`
	IngestURL string      `json:"ingest_url"`
}

// ImportSourceInput creates or patches a scheduled import.
type ImportSourceInput struct {
	Name               string          `json:"name,omitempty"`
	Kind               string          `json:"kind,omitempty"`
	Enabled            *bool           `json:"enabled,omitempty"`
	Schedule           any             `json:"schedule,omitempty"`
	Mode               string          `json:"mode,omitempty"`
	AllowMassUnpublish *bool           `json:"allow_mass_unpublish,omitempty"`
	Config             any             `json:"config,omitempty"`
	Credentials        json.RawMessage `json:"credentials,omitempty"`
}

// WebhookDelivery is one delivery attempt.
type WebhookDelivery struct {
	ID         string    `json:"id"`
	EventID    string    `json:"event_id"`
	EventType  string    `json:"event_type"`
	At         time.Time `json:"at"`
	StatusCode *int      `json:"status_code"`
	DurationMS *int      `json:"duration_ms"`
	Attempt    int       `json:"attempt"`
	Success    bool      `json:"success"`
	Error      *string   `json:"error"`
}

// DownloadEvent is one logged download.
type DownloadEvent struct {
	ID           string    `json:"id"`
	At           time.Time `json:"at"`
	Kind         string    `json:"kind"`
	RetailerID   *string   `json:"retailer_id"`
	RetailerName *string   `json:"retailer_name"`
	ActorType    string    `json:"actor_type,omitempty"`
	ActorName    *string   `json:"actor_name"`
	Variant      *string   `json:"variant"`
	ProductID    *string   `json:"product_id"`
	AssetID      *string   `json:"asset_id"`
	UsageEvent   *string   `json:"usage_event"`
	Files        int       `json:"files"`
	Bytes        *int64    `json:"bytes"`
}

// Download kinds.
const (
	DownloadImage        = "image"
	DownloadProductZip   = "product_zip"
	DownloadSelectionZip = "selection_zip"
	DownloadBulkZip      = "bulk_zip"
	DownloadCSV          = "csv"
	DownloadAPIRendition = "api_rendition"
	DownloadAPIExport    = "api_export"
	DownloadUsage        = "usage"
)

// StatsTotals are the totals of GET /admin/api/stats.
type StatsTotals struct {
	Downloads       int `json:"downloads"`
	Images          int `json:"images"`
	Zips            int `json:"zips"`
	CSVExports      int `json:"csv_exports"`
	APIDownloads    int `json:"api_downloads"`
	UsageEvents     int `json:"usage_events"`
	ActiveRetailers int `json:"active_retailers"`
}

// StatsDay is one day of stats.
type StatsDay struct {
	Date      string `json:"date"`
	Downloads int    `json:"downloads"`
	Images    int    `json:"images"`
}

// StatsRetailer is a top retailer.
type StatsRetailer struct {
	RetailerID     string     `json:"retailer_id"`
	Name           string     `json:"name"`
	Downloads      int        `json:"downloads"`
	Images         int        `json:"images"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

// StatsProduct is a top product.
type StatsProduct struct {
	ProductID   string            `json:"product_id"`
	StyleNumber string            `json:"style_number"`
	Name        obx.LocalizedText `json:"name,omitempty"`
	Downloads   int               `json:"downloads"`
}

// Stats is the answer of GET /admin/api/stats.
type Stats struct {
	Days         int             `json:"days"`
	Totals       StatsTotals     `json:"totals"`
	ByDay        []StatsDay      `json:"by_day"`
	TopRetailers []StatsRetailer `json:"top_retailers"`
	TopProducts  []StatsProduct  `json:"top_products"`
}

// PortalPermissions say what the viewer may do.
type PortalPermissions struct {
	OriginalDownload  bool `json:"original_download"`
	ArticleDataExport bool `json:"article_data_export"`
	APISelfService    bool `json:"api_self_service"`
}

// PortalLimits are the portal limits.
type PortalLimits struct {
	SelectionMaxProducts int `json:"selection_max_products,omitempty"`
	SyncZipMaxImages     int `json:"sync_zip_max_images,omitempty"`
	GTINLookupMax        int `json:"gtin_lookup_max,omitempty"`
}

// PortalViewer describes the current portal viewer.
type PortalViewer struct {
	Preview        bool              `json:"preview"`
	Retailer       *IDName           `json:"retailer"`
	Brands         []obx.Brand       `json:"brands"`
	Permissions    PortalPermissions `json:"permissions"`
	Branding       PublicBranding    `json:"branding"`
	Languages      []string          `json:"languages"`
	SelectionCount int               `json:"selection_count"`
	Limits         *PortalLimits     `json:"limits,omitempty"`
}

// PortalColor is a colour of a portal product.
type PortalColor struct {
	Code     string            `json:"code"`
	Name     obx.LocalizedText `json:"name"`
	ThumbURL *string           `json:"thumb_url"`
}

// PortalProduct is a row of the portal product list.
type PortalProduct struct {
	ID           string            `json:"id"`
	StyleNumber  string            `json:"style_number"`
	Name         obx.LocalizedText `json:"name"`
	Brand        IDName            `json:"brand"`
	ItemType     *string           `json:"item_type"`
	TargetGroup  *string           `json:"target_group"`
	Season       *string           `json:"season"`
	Collection   *string           `json:"collection"`
	ThumbURL     *string           `json:"thumb_url"`
	Colors       []PortalColor     `json:"colors"`
	ImageCount   int               `json:"image_count"`
	VariantCount int               `json:"variant_count"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// PortalProductList is a page of portal products.
type PortalProductList struct {
	Items      []PortalProduct `json:"items"`
	NextCursor *string         `json:"next_cursor"`
	Total      int             `json:"total"`
}

// FacetValue is one facet value with its count.
type FacetValue struct {
	Value string `json:"value"`
	Label any    `json:"label,omitempty"`
	Count int    `json:"count"`
}

// Facets are the portal filter options.
type Facets struct {
	Brands       []FacetValue `json:"brands"`
	ItemTypes    []FacetValue `json:"item_types"`
	Colors       []FacetValue `json:"colors"`
	Seasons      []FacetValue `json:"seasons"`
	Collections  []FacetValue `json:"collections"`
	TargetGroups []FacetValue `json:"target_groups"`
}

// PortalFilter are the portal product filters.
type PortalFilter struct {
	Q            string     `json:"q,omitempty"`
	BrandID      []string   `json:"brand_id,omitempty"`
	ItemType     []string   `json:"item_type,omitempty"`
	Color        []string   `json:"color,omitempty"`
	Season       []string   `json:"season,omitempty"`
	Collection   []string   `json:"collection,omitempty"`
	TargetGroup  []string   `json:"target_group,omitempty"`
	UpdatedSince *time.Time `json:"updated_since,omitempty"`
}

// Selection is the viewer's selection.
type Selection struct {
	ProductIDs []string   `json:"product_ids"`
	ImageCount int        `json:"image_count"`
	UpdatedAt  *time.Time `json:"updated_at"`
}

// DownloadJobInput starts a background zip.
type DownloadJobInput struct {
	Source         string        `json:"source"`
	ProductIDs     []string      `json:"product_ids,omitempty"`
	Filter         *PortalFilter `json:"filter,omitempty"`
	Variant        string        `json:"variant"`
	IncludeDataCSV *bool         `json:"include_data_csv,omitempty"`
}

// DownloadJob is a background zip.
type DownloadJob struct {
	ID             string       `json:"id"`
	Status         string       `json:"status"`
	Source         string       `json:"source"`
	Variant        string       `json:"variant"`
	IncludeDataCSV bool         `json:"include_data_csv"`
	CreatedAt      time.Time    `json:"created_at"`
	FinishedAt     *time.Time   `json:"finished_at"`
	ExpiresAt      *time.Time   `json:"expires_at"`
	Progress       obx.Progress `json:"progress"`
	FileURL        *string      `json:"file_url"`
	Bytes          *int64       `json:"bytes"`
	Files          *int         `json:"files"`
	Error          *string      `json:"error"`
}
