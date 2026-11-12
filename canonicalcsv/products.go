package canonicalcsv

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/openbrandexchange/obx-go/obx"
)

// Options control parsing.
type Options struct {
	// DefaultLanguage is the language of plain (untagged) text columns:
	// the import's default_language, else the node's first content language.
	// Empty means "en".
	DefaultLanguage string
}

func (o Options) lang() string {
	if l := NormalizeLanguageTag(o.DefaultLanguage); l != "" {
		return l
	}
	return "en"
}

// Variant is one parsed variant row.
type Variant struct {
	Row        int
	ExternalID string
	Input      obx.VariantInput
}

// Product is one product with its variants, in file order.
type Product struct {
	Row        int    // first data row of the product
	ExternalID string // product_id, else the style number
	Brand      string // the brand cell as written ("" when absent)
	Input      obx.ProductInput
	Variants   []Variant
}

// ProductsFile is a parsed products.csv.
type ProductsFile struct {
	File     string
	Products []*Product
	// Rows counts non-empty data rows.
	Rows     int
	Problems []obx.ImportRowError
	// Present says which columns the file has. A node keeps the stored value
	// of every field whose column is missing (import-format.md rule 9).
	Present Present
}

// Present lists the columns of a products.csv, by canonical name.
type Present struct {
	// Plain holds the untagged columns (aliases resolved).
	Plain map[string]bool
	// Langs holds, per text column (name, description, color_name), the
	// languages the file has a column for; the plain column counts as the
	// import's default language.
	Langs map[string]map[string]bool
	// Attrs holds the keys of the attr:<key> columns.
	Attrs map[string]bool
	// Features holds the keys of the feature:<key> columns. Rule 9 holds per
	// key for features and per column for measurements (the measurement
	// columns are in Plain).
	Features map[string]bool
}

// Errors counts problems with severity error.
func (f *ProductsFile) Errors() int { return countSeverity(f.Problems, obx.SeverityError) }

func countSeverity(ps []obx.ImportRowError, sev string) int {
	n := 0
	for _, p := range ps {
		if p.Severity == sev {
			n++
		}
	}
	return n
}

var productColumns = map[string]bool{
	"brand": true, "product_id": true, "style_number": true, "name": true, "description": true,
	"item_type": true, "target_group": true, "season": true, "collection": true, "gpc_brick": true,
	"materials": true, "care": true, "country_of_origin": true, "dpp_url": true, "status": true,
	"item_subtype": true, "contains_animal_origin": true, "footwear_materials": true, "certifications": true,
	// image_files is written by exports; imports read images from files, not from this column.
	"image_files": true,
}

var variantColumns = map[string]bool{
	"variant_id": true, "gtin": true, "sku": true, "color_code": true, "color_name": true,
	"supplier_color": true, "size": true, "size_system": true, "variant_status": true,
	"color_family": true, "size_length": true,
	"height_cm": true, "width_cm": true, "depth_cm": true, "weight_kg": true, "volume_l": true, "volume_expanded_l": true,
	"handle_drop_cm": true, "strap_length_cm": true, "laptop_max_inch": true, "heel_height_cm": true, "shaft_height_cm": true, "inseam_cm": true,
}

var productAliases = map[string]string{
	"style": "style_number", "ean": "gtin", "barcode": "gtin", "colour_code": "color_code", "colour_name": "color_name",
	"colour_family": "color_family",
}

var localizedColumns = map[string]bool{"name": true, "description": true, "color_name": true, "alt_text": true}

var (
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
	gpcPattern     = regexp.MustCompile(`^[0-9]{8}$`)
)

// ParseProducts reads a products.csv. The error is only non-nil when the file
// cannot be read at all (wrapping ErrUnreadable); row problems are in
// ProductsFile.Problems.
func ParseProducts(r io.Reader, file string, opts Options) (*ProductsFile, error) {
	if file == "" {
		file = "products.csv"
	}
	t, err := readTable(r, file)
	if err != nil {
		return nil, err
	}
	out := &ProductsFile{File: file}
	defLang := opts.lang()

	// Resolve the header.
	idx := map[string]int{}           // plain column → index
	langIdx := map[string][]langCol{} // localized base → tagged columns
	attrIdx := map[string]int{}
	featIdx := map[string]int{}
	for i, c := range t.columns {
		base := c.base
		if a, ok := productAliases[base]; ok {
			base = a
		}
		switch {
		case base == "attr" && c.lang != "":
			key := c.lang
			if !IsKey(key) {
				out.Problems = append(out.Problems, problem(file, noRow, c.raw, obx.ErrValidationFailed, obx.SeverityWarning, fmt.Sprintf("attribute key %q must be lowercase letters, digits and _; column ignored", key), ""))
				continue
			}
			attrIdx[key] = i
		case base == featurePrefix && c.lang != "":
			key := c.lang
			if !IsKey(key) {
				out.Problems = append(out.Problems, problem(file, noRow, c.raw, obx.ErrValidationFailed, obx.SeverityWarning, fmt.Sprintf("feature key %q must be lowercase letters, digits and _; column ignored", key), ""))
				continue
			}
			if _, dup := featIdx[key]; dup {
				out.Problems = append(out.Problems, problem(file, noRow, c.raw, obx.ErrValidationFailed, obx.SeverityWarning, fmt.Sprintf("column %q appears twice; the first one is used", featurePrefix+":"+key), ""))
				continue
			}
			if _, reg := obx.FeatureRegistry[key]; !reg {
				out.Problems = append(out.Problems, problem(file, noRow, featurePrefix+":"+key, obx.ErrValidationFailed, obx.SeverityWarning, fmt.Sprintf("feature %q is not in the feature registry; its cells are stored as text", key), ""))
			}
			featIdx[key] = i
		case localizedColumns[base] && base != "alt_text" && c.lang != "":
			langIdx[base] = append(langIdx[base], langCol{c.lang, i})
		case (productColumns[base] || variantColumns[base]) && c.lang == "":
			if _, dup := idx[base]; dup {
				out.Problems = append(out.Problems, problem(file, noRow, c.raw, obx.ErrValidationFailed, obx.SeverityWarning, fmt.Sprintf("column %q appears twice; the first one is used", base), ""))
				continue
			}
			idx[base] = i
		default:
			if emptyHeader(c.raw) {
				continue
			}
			out.Problems = append(out.Problems, problem(file, noRow, c.raw, obx.ErrValidationFailed, obx.SeverityWarning, fmt.Sprintf("unknown column %q ignored", strings.TrimSpace(c.raw)), ""))
		}
	}
	out.Present = Present{Plain: map[string]bool{}, Langs: map[string]map[string]bool{}, Attrs: map[string]bool{}, Features: map[string]bool{}}
	for base := range idx {
		out.Present.Plain[base] = true
	}
	for _, base := range []string{"name", "description", "color_name"} {
		langs := map[string]bool{}
		if _, ok := idx[base]; ok {
			langs[defLang] = true
		}
		for _, lc := range langIdx[base] {
			langs[lc.lang] = true
		}
		out.Present.Langs[base] = langs
	}
	for k := range attrIdx {
		out.Present.Attrs[k] = true
	}
	for k := range featIdx {
		out.Present.Features[k] = true
	}
	col := func(name string) int {
		if i, ok := idx[name]; ok {
			return i
		}
		return -1
	}
	if col("style_number") < 0 {
		out.Problems = append(out.Problems, problem(file, noRow, "style_number", obx.ErrImportInvalid, obx.SeverityError, "the header has no style_number column", ""))
		return out, nil
	}
	if col("name") < 0 && len(langIdx["name"]) == 0 {
		out.Problems = append(out.Problems, problem(file, noRow, "name", obx.ErrImportInvalid, obx.SeverityError, "the header has no name column (name or name:<language>)", ""))
		return out, nil
	}
	attrKeys := make([]string, 0, len(attrIdx))
	for k := range attrIdx {
		attrKeys = append(attrKeys, k)
	}
	sort.Strings(attrKeys)
	featKeys := make([]string, 0, len(featIdx))
	for k := range featIdx {
		featKeys = append(featKeys, k)
	}
	sort.Strings(featKeys)

	byKey := map[string]*Product{}
	families := map[string]string{} // product key + colour code → colour family
	seenVariant := map[string]int{} // external id → row
	seenGTIN := map[string]int{}    // gtin14 → row

	for ri, row := range t.rows {
		rowNo := ri + 1
		if emptyRow(row) {
			continue
		}
		out.Rows++
		rp := rowParser{file: file, row: rowNo, cells: row}

		style := rp.get(col("style_number"))
		if style == "" {
			rp.fail("style_number", obx.ErrValidationFailed, "style_number is empty", "")
			out.Problems = append(out.Problems, rp.problems...)
			continue
		}
		if len(style) > 200 {
			rp.fail("style_number", obx.ErrValidationFailed, "style_number is longer than 200 characters", style)
		}
		key := rp.get(col("product_id"))
		if key == "" {
			key = style
		}
		if !obx.ValidExternalID(key) {
			rp.fail("product_id", obx.ErrValidationFailed, "product id must be 1-200 characters without \"/\"", key)
		}

		// Product fields of this row.
		in := obx.ProductInput{StyleNumber: style}
		in.Name = rp.localized("name", col("name"), langIdx["name"], defLang)
		in.Description = rp.localized("description", col("description"), langIdx["description"], defLang)
		if v := rp.get(col("item_type")); v != "" {
			k := NormalizeKey(v)
			if !IsKey(k) {
				rp.fail("item_type", obx.ErrValidationFailed, "item_type must be a key such as sweater or t_shirt", v)
			}
			in.ItemType = &k
		}
		if v := rp.get(col("target_group")); v != "" {
			k := NormalizeKey(v)
			if !IsKey(k) {
				rp.fail("target_group", obx.ErrValidationFailed, "target_group must be a key such as women, men or unisex", v)
			}
			in.TargetGroup = &k
		}
		in.Season = rp.optMax(col("season"), "season", 64)
		in.Collection = rp.optMax(col("collection"), "collection", 200)
		if v := rp.get(col("gpc_brick")); v != "" {
			if !gpcPattern.MatchString(v) {
				rp.fail("gpc_brick", obx.ErrValidationFailed, "gpc_brick must be 8 digits", v)
			}
			in.GPCBrick = &v
		}
		if v := rp.get(col("materials")); v != "" {
			m, warns, err := ParseMaterials(v)
			if err != nil {
				rp.fail("materials", obx.ErrValidationFailed, err.Error(), v)
			}
			for _, w := range warns {
				rp.warn("materials", w, v)
			}
			in.Materials = m
		}
		if v := rp.get(col("item_subtype")); v != "" {
			k := NormalizeKey(v)
			if !IsKey(k) {
				rp.fail("item_subtype", obx.ErrValidationFailed, "item_subtype must be a key such as cabin or crossbody", v)
			}
			in.ItemSubtype = &k
		}
		if v := rp.get(col("contains_animal_origin")); v != "" {
			b, err := ParseBool(v)
			if err != nil {
				rp.fail("contains_animal_origin", obx.ErrValidationFailed, "contains_animal_origin must be yes or no", v)
			} else {
				in.ContainsAnimalOrigin = &b
			}
		}
		if v := rp.get(col("footwear_materials")); v != "" {
			f, err := ParseFootwearMaterials(v)
			if err != nil {
				rp.fail("footwear_materials", obx.ErrValidationFailed, err.Error(), v)
			}
			in.FootwearMaterials = f
		}
		if v := rp.get(col("certifications")); v != "" {
			c, err := ParseCertifications(v)
			if err != nil {
				rp.fail("certifications", obx.ErrValidationFailed, err.Error(), v)
			}
			in.Certifications = c
		}
		for _, k := range featKeys {
			v := rp.get(featIdx[k])
			if v == "" {
				continue
			}
			val, _, err := ParseFeature(k, v)
			if err != nil {
				rp.fail(featurePrefix+":"+k, obx.ErrValidationFailed, fmt.Sprintf("feature %s: %v", k, err), v)
				continue
			}
			if in.Features == nil {
				in.Features = &obx.Features{}
			}
			in.Features.Set(k, val)
		}
		if v := rp.get(col("care")); v != "" {
			c, warns, err := ParseCare(v)
			if err != nil {
				rp.fail("care", obx.ErrValidationFailed, err.Error(), v)
			}
			for _, w := range warns {
				rp.warn("care", w, v)
			}
			in.Care = c
		}
		if v := rp.get(col("country_of_origin")); v != "" {
			u := strings.ToUpper(v)
			if !countryPattern.MatchString(u) {
				rp.fail("country_of_origin", obx.ErrValidationFailed, "country_of_origin must be a two-letter country code such as NL or PT", v)
			}
			in.CountryOfOrigin = &u
		}
		if v := rp.get(col("dpp_url")); v != "" {
			if !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
				rp.fail("dpp_url", obx.ErrValidationFailed, "dpp_url must be a web address", v)
			}
			in.DPPURL = &v
		}
		in.Status = rp.status("status", col("status"))
		for _, k := range attrKeys {
			if v := rp.get(attrIdx[k]); v != "" {
				if in.Attributes == nil {
					in.Attributes = map[string]any{}
				}
				in.Attributes[k] = v
			}
		}
		brand := rp.get(col("brand"))

		// Variant fields of this row.
		var variant *Variant
		hasVariant := false
		for _, c := range []string{"gtin", "variant_id", "sku", "color_code", "size"} {
			if rp.get(col(c)) != "" {
				hasVariant = true
			}
		}
		variantOnly := []string{"color_family", "size_length"}
		variantOnly = append(variantOnly, MeasurementColumns...)
		if !hasVariant {
			for _, c := range variantOnly {
				if v := rp.get(col(c)); v != "" {
					rp.warn(c, c+" is ignored on a row without variant columns (gtin, variant_id, sku, color_code or size)", v)
				}
			}
		}
		if hasVariant {
			vi := obx.VariantInput{ProductExternalID: key}
			color := rp.get(col("color_code"))
			if color == "" {
				rp.fail("color_code", obx.ErrValidationFailed, "color_code is required on a row with variant data", "")
			} else if len(color) > 64 || strings.Contains(color, "_") {
				rp.fail("color_code", obx.ErrValidationFailed, "color_code must be at most 64 characters without \"_\"", color)
			}
			vi.Color = obx.Color{Code: color, Name: rp.localized("color_name", col("color_name"), langIdx["color_name"], defLang)}
			vi.Color.SupplierColor = rp.opt(col("supplier_color"))
			var supplied string
			if g := rp.get(col("gtin")); g != "" {
				s, g14, err := NormalizeGTINCell(g)
				if err != nil {
					rp.fail("gtin", obx.ErrInvalidGTIN, "EAN "+err.Error(), g)
				} else {
					supplied = s
					if prev, dup := seenGTIN[g14]; dup {
						rp.fail("gtin", obx.ErrGTINConflict, fmt.Sprintf("EAN is also used on row %d", prev), g)
					} else {
						seenGTIN[g14] = rowNo
					}
					vi.GTIN = &s
				}
			}
			vi.SKU = rp.opt(col("sku"))
			vi.Size = rp.opt(col("size"))
			vi.SizeLength = rp.optMax(col("size_length"), "size_length", 64)
			vi.SizeSystem = rp.opt(col("size_system"))
			if v := rp.get(col("color_family")); v != "" {
				fam, err := ParseColorFamily(v)
				if err != nil {
					rp.fail("color_family", obx.ErrValidationFailed, err.Error(), v)
				} else {
					vi.Color.Family = &fam
					fk := key + "\x00" + color
					if prev, ok := families[fk]; ok && prev != fam {
						rp.warn("color_family", fmt.Sprintf("differs from an earlier row of colour %s (%s); every variant of one colour should have the same family", color, prev), v)
					} else if !ok {
						families[fk] = fam
					}
				}
			}
			var ms obx.Measurements
			for _, c := range MeasurementColumns {
				v := rp.get(col(c))
				if v == "" {
					continue
				}
				if c == "strap_length_cm" {
					r, err := ParseStrapLength(v)
					if err != nil {
						rp.fail(c, obx.ErrValidationFailed, err.Error(), v)
						continue
					}
					ms.StrapLengthCM = r
					continue
				}
				n, err := ParseMeasurement(v)
				if err != nil {
					rp.fail(c, obx.ErrValidationFailed, err.Error(), v)
					continue
				}
				ms.SetNumber(c, &n)
			}
			if !ms.IsEmpty() {
				vi.Measurements = &ms
			}
			vi.Status = rp.status("variant_status", col("variant_status"))
			vid := rp.get(col("variant_id"))
			if vid == "" {
				switch {
				case supplied != "":
					vid = supplied
				case vi.Size != nil:
					vid = style + "-" + color + "-" + strings.ReplaceAll(*vi.Size, "/", "-")
				default:
					vid = style + "-" + color
				}
			}
			if !obx.ValidExternalID(vid) {
				rp.fail("variant_id", obx.ErrValidationFailed, "variant id must be 1-200 characters without \"/\"", vid)
			} else if prev, dup := seenVariant[vid]; dup && !rp.failed {
				rp.fail("variant_id", obx.ErrValidationFailed, fmt.Sprintf("variant %q is also on row %d", vid, prev), vid)
			}
			variant = &Variant{Row: rowNo, ExternalID: vid, Input: vi}
		}

		if rp.failed {
			out.Problems = append(out.Problems, rp.problems...)
			continue
		}
		if variant != nil {
			seenVariant[variant.ExternalID] = rowNo
		}

		p, exists := byKey[key]
		if !exists {
			p = &Product{Row: rowNo, ExternalID: key, Brand: brand, Input: in}
			byKey[key] = p
			out.Products = append(out.Products, p)
		} else {
			mergeProduct(&rp, p, in, brand)
		}
		if variant != nil {
			p.Variants = append(p.Variants, *variant)
		}
		out.Problems = append(out.Problems, rp.problems...)
	}
	for _, p := range out.Products {
		if len(p.Input.Name) == 0 {
			out.Problems = append(out.Problems, problem(file, p.Row, "name", obx.ErrValidationFailed, obx.SeverityError, fmt.Sprintf("product %s has no name", p.ExternalID), ""))
		}
	}
	// Drop products without a name (they cannot be written).
	kept := out.Products[:0]
	for _, p := range out.Products {
		if len(p.Input.Name) > 0 {
			kept = append(kept, p)
		}
	}
	out.Products = kept
	return out, nil
}

// mergeProduct fills empty fields of p from a later row and warns when a
// later row has a different non-empty value (the first value wins).
func mergeProduct(rp *rowParser, p *Product, in obx.ProductInput, brand string) {
	warnDiff := func(col, first, later string) {
		if first != "" && later != "" && first != later {
			rp.warn(col, fmt.Sprintf("differs from the first row of product %s; the first value is used", p.ExternalID), later)
		}
	}
	if p.Brand == "" {
		p.Brand = brand
	} else {
		warnDiff("brand", p.Brand, brand)
	}
	mergeText := func(col string, dst *obx.LocalizedText, src obx.LocalizedText) {
		for lang, v := range src {
			if cur, ok := (*dst)[lang]; ok {
				warnDiff(col, cur, v)
				continue
			}
			if *dst == nil {
				*dst = obx.LocalizedText{}
			}
			(*dst)[lang] = v
		}
	}
	mergeText("name", &p.Input.Name, in.Name)
	mergeText("description", &p.Input.Description, in.Description)
	mergeStr := func(col string, dst **string, src *string) {
		if src == nil {
			return
		}
		if *dst == nil {
			*dst = src
			return
		}
		warnDiff(col, **dst, *src)
	}
	mergeStr("item_type", &p.Input.ItemType, in.ItemType)
	mergeStr("item_subtype", &p.Input.ItemSubtype, in.ItemSubtype)
	mergeStr("target_group", &p.Input.TargetGroup, in.TargetGroup)
	mergeStr("season", &p.Input.Season, in.Season)
	mergeStr("collection", &p.Input.Collection, in.Collection)
	mergeStr("gpc_brick", &p.Input.GPCBrick, in.GPCBrick)
	mergeStr("country_of_origin", &p.Input.CountryOfOrigin, in.CountryOfOrigin)
	mergeStr("dpp_url", &p.Input.DPPURL, in.DPPURL)
	mergeStr("status", &p.Input.Status, in.Status)
	if p.Input.Materials == nil {
		p.Input.Materials = in.Materials
	} else if in.Materials != nil {
		warnDiff("materials", FormatMaterials(p.Input.Materials), FormatMaterials(in.Materials))
	}
	if p.Input.Care == nil {
		p.Input.Care = in.Care
	} else if in.Care != nil {
		warnDiff("care", FormatCare(p.Input.Care), FormatCare(in.Care))
	}
	if p.Input.ContainsAnimalOrigin == nil {
		p.Input.ContainsAnimalOrigin = in.ContainsAnimalOrigin
	} else if in.ContainsAnimalOrigin != nil {
		warnDiff("contains_animal_origin", fmt.Sprint(*p.Input.ContainsAnimalOrigin), fmt.Sprint(*in.ContainsAnimalOrigin))
	}
	if p.Input.FootwearMaterials == nil {
		p.Input.FootwearMaterials = in.FootwearMaterials
	} else if in.FootwearMaterials != nil {
		warnDiff("footwear_materials", FormatFootwearMaterials(p.Input.FootwearMaterials), FormatFootwearMaterials(in.FootwearMaterials))
	}
	if p.Input.Certifications == nil {
		p.Input.Certifications = in.Certifications
	} else if in.Certifications != nil {
		warnDiff("certifications", FormatCertifications(p.Input.Certifications), FormatCertifications(in.Certifications))
	}
	for _, k := range in.Features.Keys() {
		v, _ := in.Features.Get(k)
		if p.Input.Features == nil {
			p.Input.Features = &obx.Features{}
		}
		if cur, ok := p.Input.Features.Get(k); ok {
			warnDiff(featurePrefix+":"+k, FormatFeature(cur), FormatFeature(v))
			continue
		}
		p.Input.Features.Set(k, v)
	}
	for k, v := range in.Attributes {
		if p.Input.Attributes == nil {
			p.Input.Attributes = map[string]any{}
		}
		if cur, ok := p.Input.Attributes[k]; ok {
			warnDiff("attr:"+k, fmt.Sprint(cur), fmt.Sprint(v))
			continue
		}
		p.Input.Attributes[k] = v
	}
}

type langCol struct {
	lang string
	idx  int
}

// rowParser collects the problems of one row.
type rowParser struct {
	file     string
	row      int
	cells    []string
	problems []obx.ImportRowError
	failed   bool
}

func (rp *rowParser) get(i int) string { return cell(rp.cells, i) }

func (rp *rowParser) opt(i int) *string {
	if v := rp.get(i); v != "" {
		return &v
	}
	return nil
}

func (rp *rowParser) optMax(i int, col string, max int) *string {
	v := rp.opt(i)
	if v != nil && len([]rune(*v)) > max {
		rp.fail(col, obx.ErrValidationFailed, fmt.Sprintf("%s is longer than %d characters", col, max), *v)
	}
	return v
}

func (rp *rowParser) fail(col, code, detail, value string) {
	rp.failed = true
	rp.problems = append(rp.problems, problem(rp.file, rp.row, col, code, obx.SeverityError, detail, value))
}

func (rp *rowParser) warn(col, detail, value string) {
	rp.problems = append(rp.problems, problem(rp.file, rp.row, col, obx.ErrValidationFailed, obx.SeverityWarning, detail, value))
}

// localized combines a plain column (default language) and tagged columns;
// a tagged column wins over the plain one for the same language.
func (rp *rowParser) localized(name string, plain int, tagged []langCol, defLang string) obx.LocalizedText {
	var out obx.LocalizedText
	if v := rp.get(plain); v != "" {
		out = obx.LocalizedText{defLang: v}
	}
	for _, lc := range tagged {
		if lc.lang == "" {
			continue
		}
		if v := rp.get(lc.idx); v != "" {
			if out == nil {
				out = obx.LocalizedText{}
			}
			out[lc.lang] = v
		}
	}
	return out
}

func (rp *rowParser) status(col string, i int) *string {
	v := strings.ToLower(rp.get(i))
	switch v {
	case "":
		return nil
	case obx.StatusPublished, obx.StatusUnpublished:
		return &v
	}
	rp.fail(col, obx.ErrValidationFailed, col+" must be published or unpublished", v)
	return nil
}
