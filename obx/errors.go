package obx

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Error codes (docs/protocol.md section 11).
const (
	ErrInvalidRequest       = "invalid_request"
	ErrValidationFailed     = "validation_failed"
	ErrUnauthorized         = "unauthorized"
	ErrInvalidToken         = "invalid_token"
	ErrAccessRevoked        = "access_revoked"
	ErrInsufficientScope    = "insufficient_scope"
	ErrForbidden            = "forbidden"
	ErrNotFound             = "not_found"
	ErrMethodNotAllowed     = "method_not_allowed"
	ErrConflict             = "conflict"
	ErrPreconditionFailed   = "precondition_failed"
	ErrPayloadTooLarge      = "payload_too_large"
	ErrUnsupportedMediaType = "unsupported_media_type"
	ErrRateLimited          = "rate_limited"
	ErrLimitExceeded        = "limit_exceeded"
	ErrCursorInvalid        = "cursor_invalid"
	ErrCursorExpired        = "cursor_expired"
	ErrExportExpired        = "export_expired"
	ErrOriginalNotAllowed   = "original_not_allowed"
	ErrInvalidGTIN          = "invalid_gtin"
	ErrGTINConflict         = "gtin_conflict"
	ErrPositionConflict     = "position_conflict"
	ErrChecksumMismatch     = "checksum_mismatch"
	ErrBlobMissing          = "blob_missing"
	ErrUploadIncomplete     = "upload_incomplete"
	ErrUnknownParent        = "unknown_parent"
	ErrFilesMissing         = "files_missing"
	ErrImportInvalid        = "import_invalid"
	ErrMassUnpublishRefused = "mass_unpublish_refused"
	ErrUnsafeURL            = "unsafe_url"
	ErrCSRFFailed           = "csrf_failed"
	ErrInvalidCredentials   = "invalid_credentials"
	ErrAccountBlocked       = "account_blocked"
	ErrSetupRequired        = "setup_required"
	ErrSetupCompleted       = "setup_completed"
	ErrInviteInvalid        = "invite_invalid"
	ErrNotImplemented       = "not_implemented"
	ErrInternal             = "internal_error"
	ErrServiceUnavailable   = "service_unavailable"
)

type codeInfo struct {
	status int
	title  string
}

var codes = map[string]codeInfo{
	ErrInvalidRequest:       {400, "Invalid request"},
	ErrValidationFailed:     {422, "Validation failed"},
	ErrUnauthorized:         {401, "Unauthorized"},
	ErrInvalidToken:         {401, "Invalid token"},
	ErrAccessRevoked:        {401, "Access revoked"},
	ErrInsufficientScope:    {403, "Insufficient scope"},
	ErrForbidden:            {403, "Forbidden"},
	ErrNotFound:             {404, "Not found"},
	ErrMethodNotAllowed:     {405, "Method not allowed"},
	ErrConflict:             {409, "Conflict"},
	ErrPreconditionFailed:   {412, "Precondition failed"},
	ErrPayloadTooLarge:      {413, "Payload too large"},
	ErrUnsupportedMediaType: {415, "Unsupported media type"},
	ErrRateLimited:          {429, "Rate limited"},
	ErrLimitExceeded:        {400, "Limit exceeded"},
	ErrCursorInvalid:        {400, "Cursor invalid"},
	ErrCursorExpired:        {410, "Cursor expired"},
	ErrExportExpired:        {410, "Export expired"},
	ErrOriginalNotAllowed:   {403, "Original not allowed"},
	ErrInvalidGTIN:          {422, "Invalid GTIN"},
	ErrGTINConflict:         {409, "GTIN conflict"},
	ErrPositionConflict:     {409, "Position conflict"},
	ErrChecksumMismatch:     {422, "Checksum mismatch"},
	ErrBlobMissing:          {422, "Blob missing"},
	ErrUploadIncomplete:     {409, "Upload incomplete"},
	ErrUnknownParent:        {422, "Unknown parent"},
	ErrFilesMissing:         {422, "Files missing"},
	ErrImportInvalid:        {422, "Import invalid"},
	ErrMassUnpublishRefused: {409, "Mass unpublish refused"},
	ErrUnsafeURL:            {422, "Unsafe URL"},
	ErrCSRFFailed:           {403, "CSRF check failed"},
	ErrInvalidCredentials:   {401, "Invalid credentials"},
	ErrAccountBlocked:       {403, "Account blocked"},
	ErrSetupRequired:        {409, "Setup required"},
	ErrSetupCompleted:       {409, "Setup completed"},
	ErrInviteInvalid:        {400, "Invite invalid"},
	ErrNotImplemented:       {501, "Not implemented"},
	ErrInternal:             {500, "Internal error"},
	ErrServiceUnavailable:   {503, "Service unavailable"},
}

// ErrorCodes returns every known error code.
func ErrorCodes() []string {
	out := make([]string, 0, len(codes))
	for c := range codes {
		out = append(out, c)
	}
	return out
}

// KnownErrorCode reports whether code is defined by the specification.
func KnownErrorCode(code string) bool { _, ok := codes[code]; return ok }

// StatusFor returns the HTTP status of an error code (500 for unknown codes).
// setup_required is 409 on the admin API and 503 on discovery; this returns 409.
func StatusFor(code string) int {
	if c, ok := codes[code]; ok {
		return c.status
	}
	return http.StatusInternalServerError
}

// TitleFor returns the human title of an error code.
func TitleFor(code string) string {
	if c, ok := codes[code]; ok {
		return c.title
	}
	return "Error"
}

// ErrorTypeBase is the prefix of Problem.Type URIs.
const ErrorTypeBase = "https://openbrandexchange.com/errors/"

// FieldError is one entry of Problem.Errors.
type FieldError struct {
	Pointer  *string `json:"pointer"`
	ObxError string  `json:"obx_error"`
	Detail   string  `json:"detail"`
}

// Problem is an RFC 9457 problem document (schemas/Problem.json). It
// implements error so clients can return it directly.
type Problem struct {
	Type       string       `json:"type"`
	Title      string       `json:"title"`
	Status     int          `json:"status"`
	Detail     *string      `json:"detail"`
	Instance   *string      `json:"instance"`
	ObxError   string       `json:"obx_error"`
	Errors     []FieldError `json:"errors,omitempty"`
	RetryAfter *int         `json:"retry_after"`
}

// NewProblem builds a problem for code with an optional detail.
func NewProblem(code, detail string) *Problem {
	p := &Problem{Type: ErrorTypeBase + code, Title: TitleFor(code), Status: StatusFor(code), ObxError: code}
	if detail != "" {
		p.Detail = &detail
	}
	return p
}

// Problemf builds a problem with a formatted detail.
func Problemf(code, format string, args ...any) *Problem {
	return NewProblem(code, fmt.Sprintf(format, args...))
}

// WithField adds a field error and returns p.
func (p *Problem) WithField(pointer, code, detail string) *Problem {
	fe := FieldError{ObxError: code, Detail: detail}
	if pointer != "" {
		fe.Pointer = &pointer
	}
	p.Errors = append(p.Errors, fe)
	return p
}

// WithStatus overrides the HTTP status and returns p.
func (p *Problem) WithStatus(status int) *Problem { p.Status = status; return p }

func (p *Problem) Error() string {
	var b strings.Builder
	b.WriteString(p.ObxError)
	if p.Status != 0 {
		fmt.Fprintf(&b, " (%d)", p.Status)
	}
	if p.Detail != nil && *p.Detail != "" {
		b.WriteString(": ")
		b.WriteString(*p.Detail)
	}
	for _, e := range p.Errors {
		b.WriteString("; ")
		if e.Pointer != nil {
			b.WriteString(*e.Pointer)
			b.WriteString(": ")
		}
		b.WriteString(e.Detail)
	}
	return b.String()
}

// DetailString returns the detail or "".
func (p *Problem) DetailString() string { return Str(p.Detail) }

// ErrorCode returns the obx_error of err when it is (or wraps) a *Problem, else "".
func ErrorCode(err error) string {
	var p *Problem
	if errors.As(err, &p) {
		return p.ObxError
	}
	return ""
}

// IsCode reports whether err is a *Problem with the given obx_error.
func IsCode(err error, code string) bool { return ErrorCode(err) == code }
