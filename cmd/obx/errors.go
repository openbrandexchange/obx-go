package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"

	"github.com/openbrandexchange/obx-go/obx"
)

// userError carries a message that is already written for people.
type userError struct{ msg string }

func (u userError) Error() string { return u.msg }

func userErr(msg string) error { return userError{msg} }

// language returns the language obx speaks.
func (e *env) language() string {
	if e.tr == nil {
		e.tr = newTranslator(detectLanguage(e.getenv))
	}
	return e.tr.lang
}

// friendly turns an error into a sentence for people, in their language:
// what went wrong and, where possible, what to do about it.
func friendly(e *env, err error) string {
	var ue userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	var p *obx.Problem
	if errors.As(err, &p) {
		return friendlyProblem(e, p)
	}
	var oe *obx.OAuthError
	if errors.As(err, &oe) {
		if oe.Code == "invalid_client" {
			return e.t("err.invalid_client")
		}
		return e.t("err.oauth", "detail", oe.Error())
	}
	host := ""
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if u, perr := url.Parse(urlErr.URL); perr == nil {
			host = u.Host
		}
	}
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var netErr net.Error
	var pathErr *fs.PathError
	switch {
	case errors.As(err, &dnsErr):
		return e.t("err.dns", "host", strings.TrimSuffix(dnsErr.Name, "."))
	case errors.Is(err, syscall.ECONNREFUSED):
		return e.t("err.refused", "host", host)
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr), errors.As(err, &invalidCert):
		return e.t("err.certificate", "host", host)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return e.t("err.timeout", "host", host)
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF) && urlErr != nil:
		return e.t("err.connection_lost", "host", host)
	case errors.As(err, &pathErr) && errors.Is(err, fs.ErrNotExist):
		return e.t("err.no_such_file", "path", pathErr.Path)
	case errors.As(err, &pathErr) && errors.Is(err, fs.ErrPermission):
		return e.t("err.permission", "path", pathErr.Path)
	case errors.Is(err, os.ErrNotExist):
		return e.t("err.no_such_file", "path", "")
	}
	return err.Error()
}

func friendlyProblem(e *env, p *obx.Problem) string {
	detail := p.DetailString()
	switch p.ObxError {
	case obx.ErrUnauthorized, obx.ErrInvalidToken:
		return e.t("err.invalid_key")
	case obx.ErrAccessRevoked:
		return e.t("err.revoked_key")
	case obx.ErrInsufficientScope:
		return e.t("err.scope", "detail", detail)
	case obx.ErrForbidden:
		return e.t("err.forbidden", "detail", detail)
	case obx.ErrSetupRequired:
		return e.t("err.setup_required")
	case obx.ErrMassUnpublishRefused:
		return e.t("err.mass_unpublish", "detail", detail)
	case obx.ErrPayloadTooLarge:
		return e.t("err.too_large", "detail", detail)
	case obx.ErrUnsupportedMediaType:
		return e.t("err.media_type", "detail", detail)
	case obx.ErrRateLimited:
		return e.t("err.rate_limited")
	case obx.ErrInvalidCredentials:
		return e.t("err.invalid_credentials")
	case obx.ErrAccountBlocked:
		return e.t("err.account_blocked")
	case obx.ErrNotImplemented:
		return e.t("err.not_implemented", "detail", detail)
	case obx.ErrInternal, obx.ErrServiceUnavailable:
		return e.t("err.server", "status", p.Status)
	}
	if detail != "" {
		return detail
	}
	if p.Status >= 500 {
		return e.t("err.server", "status", p.Status)
	}
	return p.Title
}

// problemText is one import problem ("products.csv row 12, column gtin: …")
// with the reason in the user's language where obx knows the code.
func problemText(e *env, p obx.ImportRowError) string {
	var b strings.Builder
	b.WriteString(p.File)
	if p.Row != nil {
		b.WriteString(", " + e.t("problem.row", "row", *p.Row))
	}
	if p.Column != nil && *p.Column != "" {
		b.WriteString(", " + e.t("problem.column", "column", *p.Column))
	}
	b.WriteString(": ")
	reason := ""
	switch p.ObxError {
	case obx.ErrInvalidGTIN:
		reason = e.t("problem.invalid_gtin")
	case obx.ErrGTINConflict:
		reason = e.t("problem.gtin_conflict")
	case obx.ErrPositionConflict:
		reason = e.t("problem.position_conflict")
	case obx.ErrUnknownParent:
		reason = e.t("problem.unknown_parent")
	case obx.ErrFilesMissing:
		reason = e.t("problem.files_missing")
	case obx.ErrUnsupportedMediaType:
		reason = e.t("problem.media_type")
	case obx.ErrPayloadTooLarge:
		reason = e.t("problem.too_large")
	case obx.WarnFibreCompositionMissing:
		reason = e.t("problem.fibre_composition_missing")
	case obx.WarnAnimalOriginUnknown:
		reason = e.t("problem.animal_origin_unknown")
	case obx.WarnFootwearMaterialsMissing:
		reason = e.t("problem.footwear_materials_missing")
	case obx.WarnFibrePercentagesNot100:
		reason = e.t("problem.fibre_percentages_not_100")
	}
	// The portal's own detail is English and often more precise: English
	// readers get it instead of obx's reason, others get it after it.
	switch {
	case p.Detail == "":
	case reason == "" || e.language() == "en":
		reason = p.Detail
	default:
		reason += " (" + p.Detail + ")"
	}
	b.WriteString(reason)
	if p.Value != nil && *p.Value != "" && !strings.Contains(reason, *p.Value) {
		b.WriteString(" [" + *p.Value + "]")
	}
	return b.String()
}

var _ = register(map[string]string{
	"err.invalid_client":      "the API key for your software (client id and secret) is not valid or was switched off. Ask the brand for a new one.",
	"err.oauth":               "the portal did not accept the API key: {detail}",
	"err.dns":                 "the address {host} does not exist. Check it for typing mistakes.",
	"err.refused":             "nothing answers at {host}. Check the address and whether the portal is running.",
	"err.certificate":         "the security certificate of {host} is not valid, so obx does not trust it. Check the address; if it is right, the portal's administrator has to renew the certificate.",
	"err.timeout":             "{host} did not answer in time. Check your internet connection and try again.",
	"err.connection_lost":     "the connection to {host} was lost. Try again; obx continues where it stopped.",
	"err.no_such_file":        "{path} does not exist.",
	"err.permission":          "obx may not read or write {path}. Check the file's permissions.",
	"err.invalid_key":         "the access key is not valid. Create a new one in your portal (Automatic upload → Access keys) and run \"obx login\" again.",
	"err.revoked_key":         "this access key was switched off or has expired. Create a new one in your portal (Automatic upload → Access keys) and run \"obx login\" again.",
	"err.scope":               "this access key may not do that ({detail}). Create a key with more rights in your portal.",
	"err.forbidden":           "not allowed: {detail}",
	"err.setup_required":      "the portal has not been set up yet. Open it in your browser first.",
	"err.mass_unpublish":      "{detail}. Nothing was changed. If you really want this, add --allow-mass-unpublish.",
	"err.too_large":           "too large for the portal: {detail}",
	"err.media_type":          "the portal does not accept this kind of file: {detail}",
	"err.rate_limited":        "the portal is busy and asked obx to wait. Try again in a minute.",
	"err.invalid_credentials": "the e-mail address or password is not right.",
	"err.account_blocked":     "this account is blocked. Ask the portal's administrator.",
	"err.not_implemented":     "this portal does not offer that ({detail}).",
	"err.server":              "the portal had a problem (error {status}). Try again later; if it keeps happening, tell the portal's administrator.",

	"problem.row": "row {row}",
	// Label completeness warnings: the portal still publishes the product.
	"problem.fibre_composition_missing":  "the fibre composition is missing; EU law requires it on the label of textile products",
	"problem.animal_origin_unknown":      "state whether the product contains non-textile parts of animal origin (yes or no)",
	"problem.footwear_materials_missing": "the material of the upper, the lining and sock or the outer sole is missing; EU law requires it on footwear labels",
	"problem.fibre_percentages_not_100":  "the fibre percentages of a part do not add up to 100",
	"problem.column":                     "column {column}",
	"problem.invalid_gtin":               "this EAN is not valid",
	"problem.gtin_conflict":              "this EAN is already used by another article",
	"problem.position_conflict":          "two images have the same position",
	"problem.unknown_parent":             "the article or colour is not in the catalog",
	"problem.files_missing":              "files are missing",
	"problem.media_type":                 "this kind of file is not accepted",
	"problem.too_large":                  "the file is too large",
})
