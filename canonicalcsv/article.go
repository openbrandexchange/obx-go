package canonicalcsv

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openbrandexchange/obx-go/obx"
)

// The article data columns of 0.2 (docs/import-format.md section 1.6).

// ItemSubtypes are the recommended item_subtype keys per item type
// (docs/protocol.md 5.2).
var ItemSubtypes = map[string][]string{
	"bag":     {"handbag", "shoulder_bag", "crossbody", "tote", "clutch", "belt_bag", "backpack", "laptop_bag", "briefcase", "weekender", "toiletry_bag", "wallet", "card_holder"},
	"luggage": {"underseat", "cabin", "check_in_medium", "check_in_large", "pilot_case", "garment_bag", "duffel"},
	"shoes":   {"sneaker", "running", "hiking", "lace_up", "loafer", "pump", "sandal", "slipper", "ankle_boot", "boot", "knee_boot"},
}

// CertificationSchemes are the recommended certification schemes.
var CertificationSchemes = []string{"gots", "ocs", "grs", "rcs", "oeko_tex_standard_100", "oeko_tex_made_in_green", "bluesign", "fsc", "lwg", "rws", "rds"}

// MeasurementColumns are the measurement columns of products.csv, in order
// (the keys of obx.Measurements).
var MeasurementColumns = obx.MeasurementKeys

// featurePrefix is the column prefix of the feature columns (feature:<key>).
const featurePrefix = "feature"

// ParseFootwearMaterials parses "upper: leather; lining_and_sock: textile;
// outer_sole: other". Parts and values are lowercased (spaces → "_"); a part
// that is not in the cell is nil.
func ParseFootwearMaterials(s string) (*obx.FootwearMaterials, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := &obx.FootwearMaterials{}
	seen := map[string]bool{}
	for _, raw := range strings.Split(s, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		name, value, ok := strings.Cut(raw, ":")
		if !ok {
			return nil, fmt.Errorf("%q: expected \"<part>: <material>\" with part upper, lining_and_sock or outer_sole", raw)
		}
		part, v := NormalizeKey(name), NormalizeKey(value)
		if !obx.Contains(obx.FootwearParts, part) {
			return nil, fmt.Errorf("%q is not a footwear part (upper, lining_and_sock, outer_sole)", strings.TrimSpace(name))
		}
		if !obx.Contains(obx.FootwearMaterialValues, v) {
			return nil, fmt.Errorf("%q is not a footwear material (leather, coated_leather, textile, other)", strings.TrimSpace(value))
		}
		if seen[part] {
			return nil, fmt.Errorf("part %s appears twice", part)
		}
		seen[part] = true
		out.SetPart(part, &v)
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no footwear materials found")
	}
	return out, nil
}

// FormatFootwearMaterials writes footwear materials as ParseFootwearMaterials reads them.
func FormatFootwearMaterials(f *obx.FootwearMaterials) string {
	if f == nil {
		return ""
	}
	var parts []string
	for _, k := range obx.FootwearParts {
		if v := f.Part(k); v != nil {
			parts = append(parts, k+": "+*v)
		}
	}
	return strings.Join(parts, "; ")
}

var scopeSuffix = regexp.MustCompile(`(?i)\s*\((material|product)\)\s*$`)

// ParseCertifications parses "gots:CU 123456; rws:CU 200101 (material)".
// The scheme is lowercased (spaces → "_"); the licence number is everything
// after the first ":", trimmed (nil without ":"); the scope is product unless
// "(material)" is given. Entries keep their order.
func ParseCertifications(s string) ([]obx.Certification, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []obx.Certification
	for _, raw := range strings.Split(s, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		c := obx.Certification{Scope: obx.CertificationScopeProduct}
		if m := scopeSuffix.FindStringSubmatch(raw); m != nil {
			c.Scope = strings.ToLower(m[1])
			raw = strings.TrimSpace(raw[:len(raw)-len(m[0])])
		}
		scheme, licence, hasLicence := strings.Cut(raw, ":")
		c.Scheme = NormalizeKey(scheme)
		if !IsKey(c.Scheme) {
			return nil, fmt.Errorf("certification scheme %q is not a key such as gots or oeko_tex_standard_100", strings.TrimSpace(scheme))
		}
		if licence = strings.TrimSpace(licence); hasLicence && licence != "" {
			if len([]rune(licence)) > 200 {
				return nil, fmt.Errorf("the licence number of %s is longer than 200 characters", c.Scheme)
			}
			c.LicenceNumber = &licence
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no certifications found")
	}
	return out, nil
}

// FormatCertifications writes certifications as ParseCertifications reads them.
func FormatCertifications(cs []obx.Certification) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		s := c.Scheme
		if c.LicenceNumber != nil {
			s += ":" + *c.LicenceNumber
		}
		if c.Scope == obx.CertificationScopeMaterial {
			s += " (material)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

var measurementNumber = regexp.MustCompile(`^([0-9]+([.,][0-9]+)?|[.,][0-9]+)$`)

// ParseMeasurement parses a positive number with "." or "," as the decimal
// separator, without thousands separator and without unit ("2.6", "2,6").
func ParseMeasurement(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if !measurementNumber.MatchString(s) {
		return 0, fmt.Errorf("%q is not a number (use for example 2.6 or 2,6, without unit)", s)
	}
	v, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil || !(v > 0) {
		return 0, fmt.Errorf("%q is not a positive number", s)
	}
	return v, nil
}

// ParseStrapLength parses strap_length_cm: "min-max" (80-130) or one number
// for a strap that cannot be adjusted (120 = min 120, max 120).
func ParseStrapLength(s string) (*obx.Range, error) {
	s = strings.TrimSpace(s)
	lo, hi, ok := strings.Cut(s, "-")
	if !ok {
		v, err := ParseMeasurement(s)
		if err != nil {
			return nil, err
		}
		return &obx.Range{Min: v, Max: v}, nil
	}
	min, err := ParseMeasurement(lo)
	if err != nil {
		return nil, fmt.Errorf("%q: %v (use min-max, for example 80-130)", s, err)
	}
	max, err := ParseMeasurement(hi)
	if err != nil {
		return nil, fmt.Errorf("%q: %v (use min-max, for example 80-130)", s, err)
	}
	if min > max {
		return nil, fmt.Errorf("%q: the shortest setting comes first (min-max, for example 80-130)", s)
	}
	return &obx.Range{Min: min, Max: max}, nil
}

// FormatNumber writes a measurement with "." and no trailing zeros.
func FormatNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// FormatMeasurement writes one measurement of m by column, "" when not stated.
func FormatMeasurement(m *obx.Measurements, key string) string {
	if m == nil {
		return ""
	}
	if key == "strap_length_cm" {
		r := m.StrapLengthCM
		if r == nil {
			return ""
		}
		if r.Min == r.Max {
			return FormatNumber(r.Min)
		}
		return FormatNumber(r.Min) + "-" + FormatNumber(r.Max)
	}
	if v := m.Number(key); v != nil {
		return FormatNumber(*v)
	}
	return ""
}

// ParseColorFamily lowercases a colour family and accepts gray and
// multicolor for grey and multicolour.
func ParseColorFamily(s string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "gray":
		v = "grey"
	case "multicolor":
		v = "multicolour"
	}
	if !obx.Contains(obx.ColorFamilies, v) {
		return "", fmt.Errorf("%q is not a colour family (%s)", strings.TrimSpace(s), strings.Join(obx.ColorFamilies, ", "))
	}
	return v, nil
}

// ParseFeature parses the cell of a feature:<key> column. For a registry key
// the value has the registry type (enums lowercased with spaces → "_",
// integers as digits, booleans as yes/no, lists separated by "|" with
// duplicates dropped) and anything else is an error. A key outside the
// registry is kept as the string in the cell and registry is false.
func ParseFeature(key, s string) (value any, registry bool, err error) {
	s = strings.TrimSpace(s)
	spec, ok := obx.FeatureRegistry[key]
	if !ok {
		return s, false, nil
	}
	switch spec.Type {
	case obx.FeatureEnum:
		v := NormalizeKey(s)
		if !obx.Contains(spec.Values, v) {
			return nil, true, fmt.Errorf("%q is not one of %s", s, strings.Join(spec.Values, ", "))
		}
		return v, true, nil
	case obx.FeatureInteger:
		if !digits(s) || !obx.Contains(spec.Values, strings.TrimLeft(s, "0")+zeroIfAll(s)) {
			return nil, true, fmt.Errorf("%q is not one of %s", s, strings.Join(spec.Values, ", "))
		}
		n, _ := strconv.Atoi(s)
		return n, true, nil
	case obx.FeatureBoolean:
		b, err := ParseBool(s)
		if err != nil {
			return nil, true, err
		}
		return b, true, nil
	default:
		var list []string
		for _, raw := range strings.Split(s, "|") {
			v := NormalizeKey(raw)
			if v == "" {
				continue
			}
			if !obx.Contains(spec.Values, v) {
				return nil, true, fmt.Errorf("%q is not one of %s", strings.TrimSpace(raw), strings.Join(spec.Values, ", "))
			}
			if !obx.Contains(list, v) {
				list = append(list, v)
			}
		}
		if len(list) == 0 {
			return nil, true, fmt.Errorf("%q is not a list of %s", s, strings.Join(spec.Values, ", "))
		}
		return list, true, nil
	}
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func zeroIfAll(s string) string {
	if strings.TrimLeft(s, "0") == "" {
		return "0"
	}
	return ""
}

// FormatFeature writes a feature value as ParseFeature reads it (booleans as
// yes/no, lists separated by "|").
func FormatFeature(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case []string:
		return strings.Join(x, "|")
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, FormatFeature(e))
		}
		return strings.Join(parts, "|")
	case float64:
		return FormatNumber(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// featureKeysOf lists the feature keys of the products: the registry keys
// first (all of them), then the other keys they have, sorted.
func featureKeysOf(products []obx.ProductDetail) []string {
	keys := append([]string(nil), obx.FeatureKeys...)
	other := map[string]bool{}
	for _, p := range products {
		for _, k := range p.Features.Keys() {
			if _, reg := obx.FeatureRegistry[k]; !reg {
				other[k] = true
			}
		}
	}
	extra := make([]string, 0, len(other))
	for k := range other {
		extra = append(extra, k)
	}
	sort.Strings(extra)
	return append(keys, extra...)
}
