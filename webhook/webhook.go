// Package webhook signs and verifies OBX webhook deliveries
// (docs/protocol.md section 9).
//
// The signature header is "OBX-Webhook-Signature: v1=<hex>" where hex is the
// lowercase HMAC-SHA256 of "<timestamp>.<raw body>" keyed with the secret's
// UTF-8 bytes (including the "whsec_" prefix). During secret rotation the
// header may carry several comma-separated v1= values.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

// DefaultTolerance is the maximum clock difference a receiver accepts.
const DefaultTolerance = 300 * time.Second

// Errors returned by Verify.
var (
	ErrNoSignature      = errors.New("webhook: missing or malformed signature header")
	ErrBadTimestamp     = errors.New("webhook: missing or malformed timestamp header")
	ErrTimestampSkew    = errors.New("webhook: timestamp outside the tolerance")
	ErrSignatureInvalid = errors.New("webhook: signature does not match")
)

// Compute returns the lowercase hex HMAC for timestamp and body.
func Compute(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Sign returns the OBX-Webhook-Signature header value for body. Several
// secrets (rotation) give several comma-separated values.
func Sign(timestamp string, body []byte, secrets ...string) string {
	parts := make([]string, 0, len(secrets))
	for _, s := range secrets {
		parts = append(parts, "v1="+Compute(s, timestamp, body))
	}
	return strings.Join(parts, ",")
}

// Headers returns the three delivery headers for an event.
func Headers(eventID string, at time.Time, body []byte, secrets ...string) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(obx.HeaderWebhookID, eventID)
	h.Set(obx.HeaderWebhookTimestamp, ts)
	h.Set(obx.HeaderWebhookSignature, Sign(ts, body, secrets...))
	return h
}

// Verify checks a delivery: the signature header must contain at least one
// v1= value that matches one of the secrets (constant-time), and the
// timestamp must be within tolerance of now (DefaultTolerance when 0).
func Verify(signatureHeader, timestampHeader string, body []byte, now time.Time, tolerance time.Duration, secrets ...string) error {
	ts := strings.TrimSpace(timestampHeader)
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || ts == "" {
		return ErrBadTimestamp
	}
	if tolerance == 0 {
		tolerance = DefaultTolerance
	}
	skew := now.Sub(time.Unix(sec, 0))
	if skew > tolerance || skew < -tolerance {
		return ErrTimestampSkew
	}
	var candidates [][]byte
	for _, part := range strings.Split(signatureHeader, ",") {
		part = strings.TrimSpace(part)
		v, ok := strings.CutPrefix(part, "v1=")
		if !ok {
			continue
		}
		b, err := hex.DecodeString(v)
		if err != nil || len(b) != sha256.Size {
			continue
		}
		candidates = append(candidates, b)
	}
	if len(candidates) == 0 {
		return ErrNoSignature
	}
	for _, s := range secrets {
		mac := hmac.New(sha256.New, []byte(s))
		mac.Write([]byte(ts))
		mac.Write([]byte{'.'})
		mac.Write(body)
		want := mac.Sum(nil)
		for _, c := range candidates {
			if hmac.Equal(want, c) {
				return nil
			}
		}
	}
	return ErrSignatureInvalid
}

// VerifyRequest reads the body of r (up to maxBytes, default 1 MiB), verifies
// it and returns the decoded event and the raw body.
func VerifyRequest(r *http.Request, maxBytes int64, tolerance time.Duration, secrets ...string) (*obx.WebhookEvent, []byte, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, nil, fmt.Errorf("webhook: body larger than %d bytes", maxBytes)
	}
	if err := Verify(r.Header.Get(obx.HeaderWebhookSignature), r.Header.Get(obx.HeaderWebhookTimestamp), body, time.Now(), tolerance, secrets...); err != nil {
		return nil, body, err
	}
	var ev obx.WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, body, fmt.Errorf("webhook: invalid event JSON: %w", err)
	}
	return &ev, body, nil
}

// Handler returns an http.Handler that verifies deliveries and calls fn for
// each valid event. Invalid deliveries get 400; errors from fn get 500 so the
// node retries. Duplicate event ids are the receiver's business (fn).
func Handler(fn func(*obx.WebhookEvent) error, secrets ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ev, _, err := VerifyRequest(r, 0, 0, secrets...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := fn(ev); err != nil {
			http.Error(w, "handler failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
