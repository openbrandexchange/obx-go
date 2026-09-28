package obx

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// GTINCheckDigit computes the GS1 check digit for the given digits (without
// the check digit).
func GTINCheckDigit(body string) (byte, error) {
	sum := 0
	n := len(body)
	for i := 0; i < n; i++ {
		c := body[n-1-i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("gtin: non-digit %q", c)
		}
		d := int(c - '0')
		if i%2 == 0 {
			d *= 3
		}
		sum += d
	}
	return byte('0' + (10-sum%10)%10), nil
}

// ValidateGTIN checks a GTIN as supplied on ingest: 8, 12, 13 or 14 digits
// with a correct check digit. It returns the 14-digit normalised form.
func ValidateGTIN(s string) (string, error) {
	if !isDigits(s) {
		return "", fmt.Errorf("GTIN must contain digits only")
	}
	switch len(s) {
	case 8, 12, 13, 14:
	default:
		return "", fmt.Errorf("GTIN must have 8, 12, 13 or 14 digits, got %d", len(s))
	}
	want, _ := GTINCheckDigit(s[:len(s)-1])
	if s[len(s)-1] != want {
		return "", fmt.Errorf("check digit should be %c", want)
	}
	return PadGTIN14(s), nil
}

// NormalizeLookupGTIN normalises a lookup input the lenient way
// (docs/protocol.md section 7): trim, remove inner spaces, accept 8/12/13/14
// digits and left-pad to 14. The check digit is not validated. ok is false
// for anything else.
func NormalizeLookupGTIN(input string) (gtin string, ok bool) {
	s := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, input)
	if !isDigits(s) {
		return "", false
	}
	switch len(s) {
	case 8, 12, 13, 14:
		return PadGTIN14(s), true
	}
	return "", false
}

// PadGTIN14 left-pads a digit string to 14 characters.
func PadGTIN14(s string) string {
	if len(s) >= 14 {
		return s
	}
	return strings.Repeat("0", 14-len(s)) + s
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Slug turns a name into an external id (docs/ingest.md section 2): diacritics
// stripped, lowercase, every run of characters outside a-z0-9 becomes "-",
// leading and trailing "-" removed.
func Slug(name string) string {
	decomposed := norm.NFD.String(name)
	var b strings.Builder
	dash := false
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == 'ß':
			b.WriteString("ss")
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// ValidExternalID reports whether s is a valid external id: 1-200 characters,
// no control characters and no "/".
func ValidExternalID(s string) bool {
	if s == "" || len([]rune(s)) > 200 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '/' {
			return false
		}
	}
	return true
}
