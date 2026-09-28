package canonicalcsv

import (
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

// AssetRow is one parsed row of assets.csv. Nil fields mean "keep the value
// from the file name or the defaults".
type AssetRow struct {
	Row        int
	File       string // base name
	AssetID    *string
	ProductID  *string // product_id or style_number column
	ColorCode  *string // "" (non-nil) = all colours
	Position   *int
	Role       *string
	View       *string
	AltText    obx.LocalizedText
	Rights     *obx.RightsInput
	Provenance *obx.ProvenanceInput
	Status     *string
}

// AssetsFile is a parsed assets.csv.
type AssetsFile struct {
	File     string
	Rows     []*AssetRow
	Problems []obx.ImportRowError
}

var assetColumns = map[string]bool{
	"file": true, "asset_id": true, "product_id": true, "style_number": true, "color_code": true, "position": true,
	"role": true, "view": true, "alt_text": true, "license": true, "license_text": true, "channels": true,
	"territories": true, "embargo_until": true, "expires_at": true, "credit": true, "model_release": true,
	"ai_generated": true, "ai_edited": true, "source": true, "c2pa": true, "c2pa_manifest_url": true,
	"generator": true, "status": true,
}

// ParseAssets reads an assets.csv.
func ParseAssets(r io.Reader, file string, opts Options) (*AssetsFile, error) {
	if file == "" {
		file = "assets.csv"
	}
	t, err := readTable(r, file)
	if err != nil {
		return nil, err
	}
	out := &AssetsFile{File: file}
	defLang := opts.lang()
	idx := map[string]int{}
	var altCols []langCol
	for i, c := range t.columns {
		base := c.base
		switch base {
		case "style":
			base = "style_number"
		case "colour_code":
			base = "color_code"
		}
		switch {
		case base == "alt_text" && c.lang != "":
			altCols = append(altCols, langCol{c.lang, i})
		case assetColumns[base] && c.lang == "":
			if _, dup := idx[base]; !dup {
				idx[base] = i
			}
		default:
			if strings.TrimSpace(c.raw) != "" {
				out.Problems = append(out.Problems, problem(file, noRow, c.raw, obx.ErrValidationFailed, obx.SeverityWarning, fmt.Sprintf("unknown column %q ignored", strings.TrimSpace(c.raw)), ""))
			}
		}
	}
	col := func(name string) int {
		if i, ok := idx[name]; ok {
			return i
		}
		return -1
	}
	if col("file") < 0 {
		out.Problems = append(out.Problems, problem(file, noRow, "file", obx.ErrImportInvalid, obx.SeverityError, "the header has no file column", ""))
		return out, nil
	}
	seen := map[string]int{}
	for ri, row := range t.rows {
		rowNo := ri + 1
		if emptyRow(row) {
			continue
		}
		rp := rowParser{file: file, row: rowNo, cells: row}
		a := &AssetRow{Row: rowNo}
		a.File = path.Base(strings.ReplaceAll(rp.get(col("file")), "\\", "/"))
		if a.File == "" || a.File == "." {
			rp.fail("file", obx.ErrValidationFailed, "file is empty", "")
		} else if prev, dup := seen[strings.ToLower(a.File)]; dup {
			rp.fail("file", obx.ErrValidationFailed, fmt.Sprintf("file is also listed on row %d", prev), a.File)
		}
		a.AssetID = rp.opt(col("asset_id"))
		if a.AssetID != nil && !obx.ValidExternalID(*a.AssetID) {
			rp.fail("asset_id", obx.ErrValidationFailed, "asset_id must be 1-200 characters without \"/\"", *a.AssetID)
		}
		a.ProductID = rp.opt(col("product_id"))
		if a.ProductID == nil {
			a.ProductID = rp.opt(col("style_number"))
		}
		if i := col("color_code"); i >= 0 && a.ProductID != nil {
			// With an explicit product, an empty colour means "all colours".
			c := rp.get(i)
			a.ColorCode = &c
		} else if c := rp.opt(col("color_code")); c != nil {
			a.ColorCode = c
		}
		if v := rp.get(col("position")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 999 {
				rp.fail("position", obx.ErrValidationFailed, "position must be a number from 1 to 999", v)
			} else {
				a.Position = &n
			}
		}
		if v := rp.get(col("role")); v != "" {
			k := strings.ToLower(v)
			if !obx.IsRole(k) {
				rp.fail("role", obx.ErrValidationFailed, "role must be one of "+strings.Join(obx.Roles, ", "), v)
			}
			a.Role = &k
		}
		if v := rp.get(col("view")); v != "" {
			k := strings.ToLower(v)
			if !obx.IsView(k) {
				rp.fail("view", obx.ErrValidationFailed, "view must be one of "+strings.Join(obx.Views, ", "), v)
			}
			a.View = &k
		}
		a.AltText = rp.localized("alt_text", col("alt_text"), altCols, defLang)
		a.Rights = parseRights(&rp, col)
		a.Provenance = parseProvenance(&rp, col)
		a.Status = rp.status("status", col("status"))
		if rp.failed {
			out.Problems = append(out.Problems, rp.problems...)
			continue
		}
		seen[strings.ToLower(a.File)] = rowNo
		out.Rows = append(out.Rows, a)
		out.Problems = append(out.Problems, rp.problems...)
	}
	return out, nil
}

func parseRights(rp *rowParser, col func(string) int) *obx.RightsInput {
	var r obx.RightsInput
	set := false
	if v := rp.get(col("license")); v != "" {
		k := strings.ToLower(v)
		if k != "retailer-use" && k != "custom" {
			rp.fail("license", obx.ErrValidationFailed, "license must be retailer-use or custom", v)
		}
		r.License, set = &k, true
	}
	if v := rp.opt(col("license_text")); v != nil {
		r.LicenseText, set = v, true
	}
	if v := rp.get(col("channels")); v != "" {
		for _, c := range splitList(v) {
			c = strings.ToLower(c)
			if !obx.Contains(obx.Channels, c) {
				rp.fail("channels", obx.ErrValidationFailed, "channels must be webshop, marketplace, social or print, separated by |", v)
				break
			}
			if !obx.Contains(r.Channels, c) {
				r.Channels = append(r.Channels, c)
			}
		}
		set = true
	}
	if v := rp.get(col("territories")); v != "" {
		for _, c := range splitList(v) {
			c = strings.ToUpper(c)
			if !countryPattern.MatchString(c) {
				rp.fail("territories", obx.ErrValidationFailed, "territories must be two-letter country codes separated by |", v)
				break
			}
			if !obx.Contains(r.Territories, c) {
				r.Territories = append(r.Territories, c)
			}
		}
		set = true
	}
	for _, d := range []struct {
		col string
		dst **time.Time
	}{{"embargo_until", &r.EmbargoUntil}, {"expires_at", &r.ExpiresAt}} {
		if v := rp.get(col(d.col)); v != "" {
			t, err := ParseDate(v)
			if err != nil {
				rp.fail(d.col, obx.ErrValidationFailed, err.Error(), v)
				continue
			}
			*d.dst, set = &t, true
		}
	}
	if v := rp.opt(col("credit")); v != nil {
		r.Credit, set = v, true
	}
	if v := rp.get(col("model_release")); v != "" {
		b, err := ParseBool(v)
		if err != nil {
			rp.fail("model_release", obx.ErrValidationFailed, err.Error(), v)
		}
		r.ModelRelease, set = &b, true
	}
	if !set {
		return nil
	}
	return &r
}

func parseProvenance(rp *rowParser, col func(string) int) *obx.ProvenanceInput {
	var p obx.ProvenanceInput
	set := false
	for _, b := range []struct {
		col string
		dst **bool
	}{{"ai_generated", &p.AIGenerated}, {"ai_edited", &p.AIEdited}} {
		if v := rp.get(col(b.col)); v != "" {
			x, err := ParseBool(v)
			if err != nil {
				rp.fail(b.col, obx.ErrValidationFailed, err.Error(), v)
				continue
			}
			*b.dst, set = &x, true
		}
	}
	if v := rp.get(col("source")); v != "" {
		k := strings.ToLower(v)
		if k != "photo" && k != "generated" && k != "composite" {
			rp.fail("source", obx.ErrValidationFailed, "source must be photo, generated or composite", v)
		}
		p.Source, set = &k, true
	}
	if v := rp.get(col("c2pa")); v != "" {
		k := strings.ToLower(v)
		if k != "embedded" && k != "sidecar" && k != "none" {
			rp.fail("c2pa", obx.ErrValidationFailed, "c2pa must be embedded, sidecar or none", v)
		}
		p.C2PA, set = &k, true
	}
	if v := rp.opt(col("c2pa_manifest_url")); v != nil {
		p.C2PAManifestURL, set = v, true
	}
	if v := rp.opt(col("generator")); v != nil {
		p.Generator, set = v, true
	}
	if !set {
		return nil
	}
	return &p
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "|") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
