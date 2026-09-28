package canonicalcsv

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// IsKey reports whether s is a valid vocabulary key (^[a-z][a-z0-9_]{0,63}$).
func IsKey(s string) bool { return keyPattern.MatchString(s) }

// NormalizeKey lowercases s and turns runs of spaces and "-" into "_"
// ("Organic Cotton" → "organic_cotton", "T-shirt" → "t_shirt").
func NormalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	under := false
	for _, r := range s {
		if r == ' ' || r == '-' || r == '_' || r == '\t' {
			if !under && b.Len() > 0 {
				b.WriteByte('_')
			}
			under = true
			continue
		}
		b.WriteRune(r)
		under = false
	}
	return strings.TrimRight(b.String(), "_")
}

// Recommended fibre and part keys (docs/import-format.md section 1.4).
var (
	FiberKeys = []string{"cotton", "organic_cotton", "recycled_cotton", "linen", "hemp", "silk", "wool", "merino_wool", "lambswool", "cashmere", "alpaca", "mohair", "viscose", "lyocell", "modal", "cupro", "acetate", "polyester", "recycled_polyester", "polyamide", "recycled_polyamide", "elastane", "acrylic", "polypropylene", "polyurethane", "leather", "suede", "down", "feather", "rubber", "metal", "other"}
	PartKeys  = []string{"main", "outer", "lining", "filling", "trim", "sole", "upper", "insole"}
)

// CareCodes are the ISO 3758-aligned care codes (docs/import-format.md section 1.5).
var CareCodes = []string{
	"wash_95", "wash_70", "wash_60", "wash_60_mild", "wash_50", "wash_50_mild", "wash_40", "wash_40_mild", "wash_40_very_mild", "wash_30", "wash_30_mild", "wash_30_very_mild", "hand_wash", "do_not_wash",
	"bleach_any", "bleach_oxygen_only", "do_not_bleach",
	"tumble_dry_normal", "tumble_dry_low", "do_not_tumble_dry",
	"line_dry", "drip_line_dry", "dry_flat", "drip_dry_flat", "line_dry_shade", "drip_line_dry_shade", "dry_flat_shade", "drip_dry_flat_shade",
	"iron_high", "iron_medium", "iron_low", "do_not_iron",
	"dry_clean_p", "dry_clean_p_mild", "dry_clean_p_very_mild", "dry_clean_f", "dry_clean_f_mild", "dry_clean_f_very_mild", "do_not_dry_clean", "wet_clean_w", "wet_clean_w_mild", "wet_clean_w_very_mild", "do_not_wet_clean",
}

// ItemTypes and TargetGroups are the recommended keys (docs/protocol.md 5.2).
var (
	ItemTypes    = []string{"t_shirt", "top", "shirt", "blouse", "polo", "sweater", "cardigan", "hoodie", "sweatshirt", "vest", "jacket", "coat", "blazer", "suit", "dress", "skirt", "trousers", "jeans", "shorts", "jumpsuit", "leggings", "underwear", "lingerie", "nightwear", "swimwear", "socks", "shoes", "sneakers", "boots", "sandals", "bag", "backpack", "wallet", "belt", "hat", "cap", "scarf", "gloves", "tie", "jewelry", "watch", "eyewear", "accessory", "home", "generic_product"}
	TargetGroups = []string{"women", "men", "unisex", "girls", "boys", "kids", "baby"}
)

// ParseMaterials parses the materials grammar:
//
//	materials := part (";" part)*
//	part      := [part_name ":"] fiber ("," fiber)*
//	fiber     := number "%" fiber_key
//
// It returns warnings (for example percentages that do not sum to 100) next
// to the parsed value; err is set when the text does not follow the grammar.
func ParseMaterials(s string) (obx.Materials, []string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil, nil
	}
	var out obx.Materials
	var warnings []string
	for _, rawPart := range strings.Split(s, ";") {
		rawPart = strings.TrimSpace(rawPart)
		if rawPart == "" {
			continue
		}
		partName := "main"
		body := rawPart
		if name, rest, ok := strings.Cut(rawPart, ":"); ok {
			partName = NormalizeKey(name)
			body = rest
			if !IsKey(partName) {
				return nil, nil, fmt.Errorf("part name %q is not a valid key", strings.TrimSpace(name))
			}
		}
		part := obx.MaterialPart{Part: partName}
		sum := 0.0
		for _, rawFiber := range strings.Split(body, ",") {
			rawFiber = strings.TrimSpace(rawFiber)
			if rawFiber == "" {
				continue
			}
			num, key, ok := strings.Cut(rawFiber, "%")
			if !ok {
				return nil, nil, fmt.Errorf("%q: expected \"<percentage>%% <fibre>\"", rawFiber)
			}
			pct, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil || pct < 0 || pct > 100 || math.IsNaN(pct) {
				return nil, nil, fmt.Errorf("%q: %q is not a percentage between 0 and 100", rawFiber, strings.TrimSpace(num))
			}
			fiber := NormalizeKey(key)
			if !IsKey(fiber) {
				return nil, nil, fmt.Errorf("%q: fibre %q is not a valid key", rawFiber, strings.TrimSpace(key))
			}
			part.Fibers = append(part.Fibers, obx.Fiber{Fiber: fiber, Percentage: pct})
			sum += pct
		}
		if len(part.Fibers) == 0 {
			return nil, nil, fmt.Errorf("part %q has no fibres", partName)
		}
		if math.Abs(sum-100) > 0.01 {
			warnings = append(warnings, fmt.Sprintf("percentages of %s add up to %g, not 100", partName, sum))
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("no materials found")
	}
	return out, warnings, nil
}

// FormatMaterials writes materials in the grammar ParseMaterials reads.
func FormatMaterials(m obx.Materials) string {
	parts := make([]string, 0, len(m))
	for _, p := range m {
		fibers := make([]string, 0, len(p.Fibers))
		for _, f := range p.Fibers {
			fibers = append(fibers, strconv.FormatFloat(f.Percentage, 'f', -1, 64)+"% "+f.Fiber)
		}
		parts = append(parts, p.Part+": "+strings.Join(fibers, ", "))
	}
	return strings.Join(parts, "; ")
}

// ParseCare parses "|"-separated care codes. Unknown codes that look like
// keys are kept with a warning; anything else is an error.
func ParseCare(s string) (obx.Care, []string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil, nil
	}
	var out obx.Care
	var warnings []string
	for _, raw := range strings.Split(s, "|") {
		code := strings.ToLower(strings.TrimSpace(raw))
		if code == "" {
			continue
		}
		if !IsKey(code) {
			return nil, nil, fmt.Errorf("care code %q is not valid", strings.TrimSpace(raw))
		}
		if !obx.Contains(CareCodes, code) {
			warnings = append(warnings, fmt.Sprintf("unknown care code %q kept as is", code))
		}
		out = append(out, obx.CareInstruction{Code: code})
	}
	return out, warnings, nil
}

// FormatCare writes care codes separated by "|".
func FormatCare(c obx.Care) string {
	codes := make([]string, 0, len(c))
	for _, i := range c {
		codes = append(codes, i.Code)
	}
	return strings.Join(codes, "|")
}

// ParseBool accepts true/false, yes/no, ja/nee and 1/0 (case-insensitive).
func ParseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "ja", "1", "y", "j":
		return true, nil
	case "false", "no", "nee", "0", "n":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a yes/no value", s)
}

// ParseDate accepts RFC 3339 or YYYY-MM-DD (midnight UTC).
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is not a date (use 2026-09-28 or 2026-09-28T10:00:00Z)", s)
}

// NormalizeGTINCell removes spaces from a GTIN cell and validates it. Values
// that Excel turned into scientific notation are rejected, never guessed.
func NormalizeGTINCell(s string) (supplied, gtin14 string, err error) {
	compact := strings.Join(strings.Fields(s), "")
	if strings.ContainsAny(compact, "eE+.,") {
		return "", "", fmt.Errorf("%q looks like a number Excel changed; format the column as text and export again", s)
	}
	g, err := obx.ValidateGTIN(compact)
	if err != nil {
		return "", "", err
	}
	return compact, g, nil
}

// ImageExtensions are the file extensions of importable images.
var ImageExtensions = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp", "tif": "image/tiff", "tiff": "image/tiff",
}

// IsImageFile reports whether name has an image extension and is not a hidden
// or system file (names starting with ".", Thumbs.db, __MACOSX paths).
func IsImageFile(name string) bool {
	if IgnoredPath(name) {
		return false
	}
	_, ok := ImageExtensions[strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))]
	return ok
}

// ContentTypeForName returns the image content type for a file name, or "".
func ContentTypeForName(name string) string {
	return ImageExtensions[strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))]
}

// IgnoredPath reports whether a path inside a package or folder must be
// ignored: __MACOSX/, .DS_Store, Thumbs.db and names starting with ".".
func IgnoredPath(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == "__MACOSX" || strings.HasPrefix(seg, ".") || strings.EqualFold(seg, "Thumbs.db") {
			return true
		}
	}
	return false
}

// ImageName is a parsed image file name.
type ImageName struct {
	Style    string  // style number as written
	Color    string  // colour code as written; "" for {style}_ALL_{position} (all colours)
	Position int     // 1–999
	Role     string  // "packshot" unless a role qualifier says otherwise
	View     *string // nil unless a view qualifier is present
	HasRole  bool    // a role qualifier was present
	Ext      string  // lowercase extension without dot
}

// ExternalID returns {style}_{color}_{position} ({style}_ALL_{position} for
// an image of all colours).
func (n ImageName) ExternalID() string {
	return AssetExternalID(n.Style, n.Color, n.Position)
}

// AllColours is the colour part of the file name of an image that belongs to
// every colour of a product ({style}_ALL_{position}.jpg). It is reserved:
// matched case-insensitively and never looked up as a colour code.
const AllColours = "ALL"

// AssetExternalID builds the asset external id convention. Without a colour
// (an image for all colours) it is {style}_ALL_{position}, the same name the
// image file has.
func AssetExternalID(style, color string, position int) string {
	if color == "" {
		color = AllColours
	}
	return style + "_" + color + "_" + strconv.Itoa(position)
}

// ParseImageName parses {style}_{color}_{position}[_{qualifier}…].{ext}
// from the right (docs/import-format.md section 2.1). The name may include
// folders; only the base name counts.
func ParseImageName(name string) (ImageName, error) {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(base), "."))
	if _, ok := ImageExtensions[ext]; !ok {
		return ImageName{}, fmt.Errorf("%q is not an image file (jpg, png, webp or tif)", base)
	}
	stem := base[:len(base)-len(ext)-1]
	parts := strings.Split(stem, "_")
	n := ImageName{Role: obx.RolePackshot, Ext: ext}
	// Pop qualifiers while more than three parts remain (a two-part key such as
	// size_chart first), then apply them right to left, each filling only what
	// is still empty: the rightmost role or view wins.
	isQualifier := func(k string) bool { return obx.IsRole(k) || obx.IsView(k) }
	var quals []string // left to right
	for {
		if len(parts) > 4 {
			if two := strings.ToLower(parts[len(parts)-2] + "_" + parts[len(parts)-1]); isQualifier(two) {
				quals = append([]string{two}, quals...)
				parts = parts[:len(parts)-2]
				continue
			}
		}
		if len(parts) > 3 {
			if last := strings.ToLower(parts[len(parts)-1]); isQualifier(last) {
				quals = append([]string{last}, quals...)
				parts = parts[:len(parts)-1]
				continue
			}
		}
		break
	}
	role, view := roleAndView(quals)
	if len(parts) < 3 {
		return ImageName{}, fmt.Errorf("%q does not follow {style}_{colour}_{position}.jpg (for example KO-1001_NVY_1.jpg)", base)
	}
	posText := parts[len(parts)-1]
	pos, err := strconv.Atoi(posText)
	if err != nil || pos < 1 || pos > 999 || strings.HasPrefix(posText, "0") || strings.HasPrefix(posText, "+") {
		return ImageName{}, fmt.Errorf("%q has no position (a number 1-999 after the colour code, for example KO-1001_NVY_1.jpg)", base)
	}
	n.Position = pos
	n.Color = parts[len(parts)-2]
	n.Style = strings.Join(parts[:len(parts)-2], "_")
	if n.Style == "" || n.Color == "" {
		return ImageName{}, fmt.Errorf("%q has an empty style number or colour code", base)
	}
	if strings.EqualFold(n.Color, AllColours) {
		n.Color = "" // an image of every colour
	}
	if role != "" {
		n.Role = role
		n.HasRole = true
	}
	if view != "" {
		v := view
		n.View = &v
	}
	return n, nil
}

// roleAndView applies qualifiers right to left, each filling only what is
// still empty. "other" is the view when the name also has another role,
// otherwise the role; "detail" sets both.
func roleAndView(quals []string) (role, view string) {
	otherIsView := false
	for _, q := range quals {
		if q != "other" && obx.IsRole(q) {
			otherIsView = true
		}
	}
	for i := len(quals) - 1; i >= 0; i-- {
		switch q := quals[i]; {
		case q == "detail":
			if role == "" {
				role = "detail"
			}
			if view == "" {
				view = "detail"
			}
		case q == "other":
			if otherIsView {
				if view == "" {
					view = "other"
				}
			} else if role == "" {
				role = "other"
			}
		case obx.IsRole(q):
			if role == "" {
				role = q
			}
		case view == "":
			view = q
		}
	}
	return role, view
}

// RenditionFilename returns the download name of a rendition: the original
// name for "original", else the name without extension plus _web, _square or
// _thumb and ".jpg".
func RenditionFilename(original, rendition string) string {
	if rendition == obx.RenditionOriginal || rendition == "" {
		return original
	}
	stem := strings.TrimSuffix(original, path.Ext(original))
	return stem + "_" + rendition + ".jpg"
}

// FormulaEscape protects a CSV cell against formula injection: cells starting
// with =, +, -, @, tab or CR get a leading apostrophe, except purely numeric
// values such as GTINs and negative numbers.
func FormulaEscape(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		if _, err := strconv.ParseFloat(s, 64); err == nil && s[0] != '=' && s[0] != '@' {
			return s
		}
		return "'" + s
	}
	return s
}
