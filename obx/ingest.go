package obx

import (
	"encoding/json"
	"time"
)

// BrandInput is the ingest body for a brand (schemas/BrandInput.json).
type BrandInput struct {
	Name        string        `json:"name"`
	LogoURL     *string       `json:"logo_url,omitempty"`
	Website     *string       `json:"website,omitempty"`
	Description LocalizedText `json:"description,omitempty"`
	Status      *string       `json:"status,omitempty"`
}

// ProductInput is the ingest body for a product (schemas/ProductInput.json).
// Omitted optional fields become null on PUT; an omitted Status keeps the
// current status.
type ProductInput struct {
	BrandExternalID *string        `json:"brand_external_id,omitempty"`
	StyleNumber     string         `json:"style_number"`
	Name            LocalizedText  `json:"name"`
	Description     LocalizedText  `json:"description,omitempty"`
	ItemType        *string        `json:"item_type,omitempty"`
	GPCBrick        *string        `json:"gpc_brick,omitempty"`
	TargetGroup     *string        `json:"target_group,omitempty"`
	Season          *string        `json:"season,omitempty"`
	Collection      *string        `json:"collection,omitempty"`
	Materials       Materials      `json:"materials,omitempty"`
	Care            Care           `json:"care,omitempty"`
	CountryOfOrigin *string        `json:"country_of_origin,omitempty"`
	DPPURL          *string        `json:"dpp_url,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	Status          *string        `json:"status,omitempty"`
}

// VariantInput is the ingest body for a variant (schemas/VariantInput.json).
type VariantInput struct {
	ProductExternalID string  `json:"product_external_id"`
	GTIN              *string `json:"gtin,omitempty"`
	SKU               *string `json:"sku,omitempty"`
	Color             Color   `json:"color"`
	Size              *string `json:"size,omitempty"`
	SizeSystem        *string `json:"size_system,omitempty"`
	Status            *string `json:"status,omitempty"`
}

// RightsInput are asset rights on ingest; nil fields take the node's default rights.
type RightsInput struct {
	License      *string    `json:"license,omitempty"`
	LicenseText  *string    `json:"license_text,omitempty"`
	Channels     []string   `json:"channels,omitempty"`
	Territories  []string   `json:"territories,omitempty"`
	EmbargoUntil *time.Time `json:"embargo_until,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Credit       *string    `json:"credit,omitempty"`
	ModelRelease *bool      `json:"model_release,omitempty"`
}

// ProvenanceInput is asset provenance on ingest; nil fields take the defaults
// (ai_generated false, ai_edited false, source photo, or generated when
// ai_generated is true, c2pa none).
type ProvenanceInput struct {
	AIGenerated     *bool   `json:"ai_generated,omitempty"`
	AIEdited        *bool   `json:"ai_edited,omitempty"`
	Source          *string `json:"source,omitempty"`
	C2PA            *string `json:"c2pa,omitempty"`
	C2PAManifestURL *string `json:"c2pa_manifest_url,omitempty"`
	Generator       *string `json:"generator,omitempty"`
}

// FileRef references uploaded bytes by sha256.
type FileRef struct {
	SHA256 string `json:"sha256"`
	Width  *int   `json:"width,omitempty"`
	Height *int   `json:"height,omitempty"`
}

// AssetFiles attaches blobs to an asset.
type AssetFiles struct {
	Original *FileRef `json:"original,omitempty"`
	Web      *FileRef `json:"web,omitempty"`
	Square   *FileRef `json:"square,omitempty"`
	Thumb    *FileRef `json:"thumb,omitempty"`
}

// Get returns the reference for a rendition name.
func (f *AssetFiles) Get(name string) *FileRef {
	if f == nil {
		return nil
	}
	switch name {
	case RenditionOriginal:
		return f.Original
	case RenditionWeb:
		return f.Web
	case RenditionSquare:
		return f.Square
	case RenditionThumb:
		return f.Thumb
	}
	return nil
}

// Set sets the reference for a rendition name.
func (f *AssetFiles) Set(name string, ref *FileRef) {
	switch name {
	case RenditionOriginal:
		f.Original = ref
	case RenditionWeb:
		f.Web = ref
	case RenditionSquare:
		f.Square = ref
	case RenditionThumb:
		f.Thumb = ref
	}
}

// AssetInput is the ingest body for asset metadata (schemas/AssetInput.json).
// An omitted Files keeps the current files.
type AssetInput struct {
	ProductExternalID  string           `json:"product_external_id"`
	ColorCode          *string          `json:"color_code,omitempty"`
	VariantExternalIDs []string         `json:"variant_external_ids,omitempty"`
	MediaType          string           `json:"media_type,omitempty"`
	Role               string           `json:"role"`
	View               *string          `json:"view,omitempty"`
	Position           int              `json:"position"`
	Filename           *string          `json:"filename,omitempty"`
	AltText            LocalizedText    `json:"alt_text,omitempty"`
	Rights             *RightsInput     `json:"rights,omitempty"`
	Provenance         *ProvenanceInput `json:"provenance,omitempty"`
	Files              *AssetFiles      `json:"files,omitempty"`
	Status             *string          `json:"status,omitempty"`
}

// Blob is stored bytes (schemas/Blob.json).
type Blob struct {
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"content_type"`
	Width       *int   `json:"width"`
	Height      *int   `json:"height"`
	Created     bool   `json:"created"`
}

// UploadInput starts a resumable upload.
type UploadInput struct {
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"content_type"`
}

// Upload is a resumable multipart upload session (schemas/Upload.json).
type Upload struct {
	ID            string    `json:"id"`
	SHA256        string    `json:"sha256"`
	Bytes         int64     `json:"bytes"`
	ContentType   string    `json:"content_type"`
	PartBytes     int64     `json:"part_bytes"`
	PartsTotal    int       `json:"parts_total"`
	ReceivedParts []int     `json:"received_parts"`
	Status        string    `json:"status"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// IngestToken describes the calling token in IngestInfo.
type IngestTokenInfo struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Scopes           []string   `json:"scopes"`
	BrandExternalIDs []string   `json:"brand_external_ids"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

// IngestLimits are the node's ingest limits.
type IngestLimits struct {
	MaxUploadBytes     int64 `json:"max_upload_bytes"`
	MaxObjectBytes     int64 `json:"max_object_bytes"`
	PartBytes          int64 `json:"part_bytes"`
	MaxBatchOperations int   `json:"max_batch_operations"`
	MaxImportBytes     int64 `json:"max_import_bytes"`
}

// IngestRenditions says who produces web/square/thumb.
type IngestRenditions struct {
	ServerGenerated bool     `json:"server_generated"`
	Required        []string `json:"required"`
}

// IngestBrand is a brand as listed in IngestInfo.
type IngestBrand struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
}

// IngestInfo is the answer of GET /obx/v1/ingest (schemas/IngestInfo.json).
type IngestInfo struct {
	Token                IngestTokenInfo  `json:"token"`
	Limits               IngestLimits     `json:"limits"`
	Renditions           IngestRenditions `json:"renditions"`
	AcceptedContentTypes []string         `json:"accepted_content_types"`
	DefaultStatus        string           `json:"default_status"`
	Brands               []IngestBrand    `json:"brands"`
	Languages            []string         `json:"languages"`
}

// IngestOperation is one operation of a batch (schemas/IngestOperation.json).
type IngestOperation struct {
	Op         string          `json:"op"`
	Kind       string          `json:"kind"`
	ExternalID string          `json:"external_id"`
	Data       json.RawMessage `json:"data,omitempty"`
	IfMatch    *string         `json:"if_match,omitempty"`
}

// IngestRecord is a stored object as read through the ingest API
// (schemas/IngestRecord.json). Data is the stored input document.
type IngestRecord struct {
	Kind       string          `json:"kind"`
	ID         string          `json:"id"`
	ExternalID string          `json:"external_id"`
	Status     string          `json:"status"`
	Visibility string          `json:"visibility"`
	ETag       string          `json:"etag"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Data       json.RawMessage `json:"data"`
	// MissingFiles is for assets only (an empty list when nothing is
	// missing); it is left out for other kinds.
	MissingFiles []string `json:"missing_files,omitempty"`
}

// MarshalJSON writes missing_files as [] for a complete asset and leaves it
// out for brands, products and variants (schemas/IngestRecord.json).
func (r IngestRecord) MarshalJSON() ([]byte, error) {
	type plain IngestRecord
	if r.Kind != KindAsset {
		return json.Marshal(plain(r))
	}
	mf := r.MissingFiles
	if mf == nil {
		mf = []string{}
	}
	return json.Marshal(struct {
		plain
		MissingFiles []string `json:"missing_files"`
	}{plain(r), mf})
}

// IngestResult is the result of one write (schemas/IngestResult.json).
type IngestResult struct {
	Index      *int     `json:"index"`
	Op         string   `json:"op"`
	Kind       string   `json:"kind"`
	ExternalID string   `json:"external_id"`
	Result     string   `json:"result"`
	ID         *string  `json:"id"`
	Visibility *string  `json:"visibility"`
	ETag       *string  `json:"etag"`
	Error      *Problem `json:"error"`
	Warnings   []string `json:"warnings"`
}

// MarshalJSON keeps Warnings an array.
func (r IngestResult) MarshalJSON() ([]byte, error) {
	type plain IngestResult
	q := plain(r)
	if q.Warnings == nil {
		q.Warnings = []string{}
	}
	return json.Marshal(q)
}

// BatchSummary counts batch results.
type BatchSummary struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Deleted   int `json:"deleted"`
	Errors    int `json:"errors"`
}

// Add counts one result.
func (s *BatchSummary) Add(result string) {
	switch result {
	case ResultCreated:
		s.Created++
	case ResultUpdated:
		s.Updated++
	case ResultUnchanged:
		s.Unchanged++
	case ResultDeleted:
		s.Deleted++
	case ResultError:
		s.Errors++
	}
}

// BatchResponse is the synchronous answer of POST /batch.
type BatchResponse struct {
	Results []IngestResult `json:"results"`
	Summary BatchSummary   `json:"summary"`
}

// IngestJob is an asynchronous batch or import (schemas/IngestJob.json).
type IngestJob struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"`
	Status     string        `json:"status"`
	CreatedAt  time.Time     `json:"created_at"`
	FinishedAt *time.Time    `json:"finished_at"`
	Progress   Progress      `json:"progress"`
	Summary    *BatchSummary `json:"summary"`
	ImportRun  *ImportRun    `json:"import_run"`
	ResultsURL string        `json:"results_url"`
}

// Done reports whether the job finished (succeeded or failed).
func (j *IngestJob) Done() bool { return j.Status == JobSucceeded || j.Status == JobFailed }

// ImportRowError is one row or file problem of an import (schemas/ImportRowError.json).
type ImportRowError struct {
	File     string  `json:"file"`
	Row      *int    `json:"row"`
	Column   *string `json:"column"`
	Severity string  `json:"severity"`
	ObxError string  `json:"obx_error"`
	Detail   string  `json:"detail"`
	Value    *string `json:"value"`
}

// Severities of import problems.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// ImportRunFile is one input file of an import run.
type ImportRunFile struct {
	Name   string  `json:"name"`
	Bytes  int64   `json:"bytes"`
	SHA256 *string `json:"sha256"`
}

// ImportCounts are the counts of an import run.
type ImportCounts struct {
	Rows        int `json:"rows"`
	Files       int `json:"files"`
	Created     int `json:"created"`
	Updated     int `json:"updated"`
	Unchanged   int `json:"unchanged"`
	Unpublished int `json:"unpublished"`
	Errors      int `json:"errors"`
	Warnings    int `json:"warnings"`
}

// ImportRun is one execution of the canonical import (schemas/ImportRun.json).
type ImportRun struct {
	ID            string           `json:"id"`
	SourceID      *string          `json:"source_id"`
	Trigger       string           `json:"trigger"`
	Status        string           `json:"status"`
	Mode          string           `json:"mode"`
	DryRun        bool             `json:"dry_run"`
	CreatedAt     time.Time        `json:"created_at"`
	StartedAt     *time.Time       `json:"started_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
	Files         []ImportRunFile  `json:"files"`
	Counts        ImportCounts     `json:"counts"`
	Error         *Problem         `json:"error"`
	ErrorsPreview []ImportRowError `json:"errors_preview,omitempty"`
}

// ImportOptions is the `options` part of POST /imports.
type ImportOptions struct {
	Mode               string `json:"mode,omitempty"`
	AllowMassUnpublish bool   `json:"allow_mass_unpublish,omitempty"`
	DryRun             bool   `json:"dry_run,omitempty"`
	DefaultLanguage    string `json:"default_language,omitempty"`
}

// BlobRef references an uploaded rendition in an images manifest.
type BlobRef struct {
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ManifestRenditions are client-generated renditions of a manifest image.
type ManifestRenditions struct {
	Web    *BlobRef `json:"web,omitempty"`
	Square *BlobRef `json:"square,omitempty"`
	Thumb  *BlobRef `json:"thumb,omitempty"`
}

// ManifestImage is one entry of an images manifest: an image already uploaded as a blob.
type ManifestImage struct {
	File       string              `json:"file"`
	SHA256     string              `json:"sha256"`
	Width      int                 `json:"width,omitempty"`
	Height     int                 `json:"height,omitempty"`
	Renditions *ManifestRenditions `json:"renditions,omitempty"`
}

// ImportSourceSchedule is when a scheduled import runs.
type ImportSourceSchedule struct {
	Frequency string  `json:"frequency"`
	Time      *string `json:"time"`
	Weekday   *int    `json:"weekday"`
	Timezone  string  `json:"timezone,omitempty"`
}

// ImportSourceCredentialsSet says which credentials are stored (never their values).
type ImportSourceCredentialsSet struct {
	Password        bool     `json:"password"`
	PrivateKey      bool     `json:"private_key"`
	AccessKeyID     bool     `json:"access_key_id"`
	SecretAccessKey bool     `json:"secret_access_key"`
	Headers         []string `json:"headers"`
}

// ImportSource is a scheduled pull import (schemas/ImportSource.json).
// Config is kept raw because its shape depends on Kind.
type ImportSource struct {
	ID                 string                     `json:"id"`
	Name               string                     `json:"name"`
	Kind               string                     `json:"kind"`
	Enabled            bool                       `json:"enabled"`
	Schedule           ImportSourceSchedule       `json:"schedule"`
	Mode               string                     `json:"mode"`
	AllowMassUnpublish bool                       `json:"allow_mass_unpublish"`
	Config             json.RawMessage            `json:"config"`
	CredentialsSet     ImportSourceCredentialsSet `json:"credentials_set"`
	LastRun            *ImportRun                 `json:"last_run"`
	NextRunAt          *time.Time                 `json:"next_run_at"`
	CreatedAt          time.Time                  `json:"created_at"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}

// AssetOrder is the body of PUT /products/{external_id}/asset-order.
type AssetOrder struct {
	ColorCode        *string  `json:"color_code"`
	AssetExternalIDs []string `json:"asset_external_ids"`
}
