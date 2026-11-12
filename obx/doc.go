// Package obx holds the Open Brand Exchange (OBX) data types, taken from the
// JSON Schemas in the specification (https://github.com/openbrandexchange/spec,
// schemas/*.json, version 0.2).
//
// JSON tags match the schemas exactly. Nullable schema properties are pointers
// (or nil slices/maps) without omitempty, so a marshalled response contains every
// property with null for unknown values, as docs/protocol.md section 2 requires.
// Properties that only exist in some responses (for example Change.Data, which
// deletes do not carry) use omitempty.
//
// Consumers must ignore unknown fields and enum values; the types here never
// reject a document because of them.
package obx

// Version is the OBX protocol version this package implements (the value of
// the OBX-Version header and DiscoveryDocument.ObxVersion).
const Version = "0.2"

// SpecVersion is the specification release the types were generated from.
const SpecVersion = "0.2.0"

// Header names used across the protocol and the ingest API.
const (
	HeaderVersion          = "OBX-Version"
	HeaderContentSHA256    = "OBX-Content-SHA256"
	HeaderWebhookID        = "OBX-Webhook-Id"
	HeaderWebhookTimestamp = "OBX-Webhook-Timestamp"
	HeaderWebhookSignature = "OBX-Webhook-Signature"
	HeaderIdempotencyKey   = "Idempotency-Key"
	HeaderCSRF             = "X-CSRF-Token"
)

// Well-known paths.
const (
	PathDiscovery = "/.well-known/obx"
	PathAPIBase   = "/obx/v1"
	PathIngest    = "/obx/v1/ingest"
	PathAdminAPI  = "/admin/api"
)

// Media types.
const (
	MediaTypeProblem    = "application/problem+json"
	MediaTypeMergePatch = "application/merge-patch+json"
	MediaTypeNDJSON     = "application/x-ndjson"
)
