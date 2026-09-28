package obx

// Scope is an OBX Protocol OAuth2 scope.
type Scope = string

// Protocol scopes.
const (
	ScopeCatalogRead   Scope = "catalog:read"
	ScopeMediaRead     Scope = "media:read"
	ScopeMediaOriginal Scope = "media:original"
	ScopeUsageWrite    Scope = "usage:write"
)

// AllScopes lists the protocol scopes in their canonical order.
var AllScopes = []Scope{ScopeCatalogRead, ScopeMediaRead, ScopeMediaOriginal, ScopeUsageWrite}

// IngestScope is an ingest token scope.
type IngestScope = string

// Ingest scopes.
const (
	IngestScopeRead   IngestScope = "ingest:read"
	IngestScopeWrite  IngestScope = "ingest:write"
	IngestScopeDelete IngestScope = "ingest:delete"
)

// Asset roles (what the image shows).
const (
	RolePackshot  = "packshot"
	RoleModel     = "model"
	RoleDetail    = "detail"
	RoleGhost     = "ghost"
	RoleFlatlay   = "flatlay"
	RoleScene     = "scene"
	RoleSwatch    = "swatch"
	RoleSizeChart = "size_chart"
	RoleOther     = "other"
)

// Roles lists every role key.
var Roles = []string{RolePackshot, RoleModel, RoleDetail, RoleGhost, RoleFlatlay, RoleScene, RoleSwatch, RoleSizeChart, RoleOther}

// Views lists every view key.
var Views = []string{"front", "back", "side", "left", "right", "top", "bottom", "tilted_left", "tilted_right", "inside", "detail", "other"}

// Rendition names.
const (
	RenditionOriginal = "original"
	RenditionWeb      = "web"
	RenditionSquare   = "square"
	RenditionThumb    = "thumb"
)

// RenditionNames lists every rendition in canonical order.
var RenditionNames = []string{RenditionOriginal, RenditionWeb, RenditionSquare, RenditionThumb}

// DerivedRenditionNames are the renditions produced from the original.
var DerivedRenditionNames = []string{RenditionWeb, RenditionSquare, RenditionThumb}

// Channels a rights grant can cover.
const (
	ChannelWebshop     = "webshop"
	ChannelMarketplace = "marketplace"
	ChannelSocial      = "social"
	ChannelPrint       = "print"
)

// Channels lists every channel.
var Channels = []string{ChannelWebshop, ChannelMarketplace, ChannelSocial, ChannelPrint}

// Publication statuses.
const (
	StatusPublished   = "published"
	StatusUnpublished = "unpublished"
)

// Change types and operations.
const (
	ChangeBrand   = "brand"
	ChangeProduct = "product"
	ChangeVariant = "variant"
	ChangeAsset   = "asset"
	ChangeGrant   = "grant"

	OpUpsert = "upsert"
	OpDelete = "delete"

	PhaseSnapshot = "snapshot"
	PhaseLive     = "live"
)

// Ingest operation names and record kinds.
const (
	IngestOpUpsert    = "upsert"
	IngestOpPatch     = "patch"
	IngestOpDelete    = "delete"
	IngestOpPublish   = "publish"
	IngestOpUnpublish = "unpublish"

	KindBrand   = "brand"
	KindProduct = "product"
	KindVariant = "variant"
	KindAsset   = "asset"
)

// Ingest results.
const (
	ResultCreated   = "created"
	ResultUpdated   = "updated"
	ResultUnchanged = "unchanged"
	ResultDeleted   = "deleted"
	ResultError     = "error"
)

// Record visibility as reported by the ingest API.
const (
	VisibilityVisible      = "visible"
	VisibilityHidden       = "hidden"
	VisibilityPendingFiles = "pending_files"
)

// Job statuses (ingest jobs, import runs, exports).
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobExpired   = "expired"
)

// Capabilities advertised in the discovery document.
const (
	CapChanges          = "changes"
	CapProducts         = "products"
	CapByGTIN           = "by_gtin"
	CapRenditions       = "renditions"
	CapExports          = "exports"
	CapWebhooks         = "webhooks"
	CapUsage            = "usage"
	CapAccessRequests   = "access_requests"
	CapIngest           = "ingest"
	CapServerRenditions = "ingest.server_renditions"
	CapPortal           = "portal"
)

// Webhook event types.
const (
	EventChangesAvailable     = "changes.available"
	EventGrantUpdated         = "grant.updated"
	EventExportCompleted      = "export.completed"
	EventWebhookTest          = "webhook.test"
	EventAccessRequestCreated = "access_request.created"
	EventImportRunCompleted   = "import_run.completed"
	EventIngestJobCompleted   = "ingest_job.completed"
)

// Usage event kinds.
const (
	UsageSynced     = "synced"
	UsagePublished  = "published"
	UsageDownloaded = "downloaded"
	UsageRemoved    = "removed"
)

// Import modes.
const (
	ImportModeUpsert = "upsert"
	ImportModeMirror = "mirror"
)

// Secret prefixes (security.md section 2).
const (
	PrefixIngestToken   = "obxi_"
	PrefixClientID      = "obxc_"
	PrefixClientSecret  = "obxs_"
	PrefixRequestToken  = "obxr_"
	PrefixWebhookSecret = "whsec_"
)

// Contains reports whether list contains s.
func Contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// IsRole reports whether s is a known role key.
func IsRole(s string) bool { return Contains(Roles, s) }

// IsView reports whether s is a known view key.
func IsView(s string) bool { return Contains(Views, s) }
