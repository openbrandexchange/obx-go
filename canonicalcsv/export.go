package canonicalcsv

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/openbrandexchange/obx-go/obx"
)

// ExportOptions control WriteExport.
type ExportOptions struct {
	// Languages are the content languages; every localized column is written
	// once per language (name:nl, name:en, …).
	Languages []string
	// Delimiter is ',' (default) or ';' (what Dutch Excel expects).
	Delimiter rune
	// BrandNames maps brand ids to names for the brand column.
	BrandNames map[string]string
	// BOM writes a UTF-8 byte order mark so Excel opens the file as UTF-8.
	BOM bool
}

// ExportHeader returns the export columns for the given languages and
// attribute keys: the canonical import columns in documented order (without
// status columns, since an export only contains published content) plus
// image_files.
func ExportHeader(languages, attrKeys []string) []string {
	h := []string{"brand", "product_id", "style_number"}
	for _, l := range languages {
		h = append(h, "name:"+l)
	}
	for _, l := range languages {
		h = append(h, "description:"+l)
	}
	h = append(h, "item_type", "target_group", "season", "collection", "gpc_brick", "materials", "care", "country_of_origin", "dpp_url")
	for _, k := range attrKeys {
		h = append(h, "attr:"+k)
	}
	h = append(h, "variant_id", "gtin", "sku", "color_code")
	for _, l := range languages {
		h = append(h, "color_name:"+l)
	}
	h = append(h, "supplier_color", "size", "size_system", "image_files")
	return h
}

// WriteExport writes products as a canonical CSV that can be imported again:
// one row per variant (a product without variants gets one row), all cells
// formula-escaped.
func WriteExport(w io.Writer, products []obx.ProductDetail, opts ExportOptions) error {
	if opts.BOM {
		if _, err := io.WriteString(w, "\ufeff"); err != nil {
			return err
		}
	}
	langs := opts.Languages
	if len(langs) == 0 {
		langs = []string{"en"}
	}
	keySet := map[string]bool{}
	for _, p := range products {
		for k := range p.Attributes {
			keySet[k] = true
		}
	}
	attrKeys := make([]string, 0, len(keySet))
	for k := range keySet {
		attrKeys = append(attrKeys, k)
	}
	sort.Strings(attrKeys)
	cw := csv.NewWriter(w)
	if opts.Delimiter != 0 {
		cw.Comma = opts.Delimiter
	}
	if err := cw.Write(ExportHeader(langs, attrKeys)); err != nil {
		return err
	}
	for _, p := range products {
		base := []string{opts.BrandNames[p.BrandID], p.ExternalID, p.StyleNumber}
		for _, l := range langs {
			base = append(base, p.Name[l])
		}
		for _, l := range langs {
			base = append(base, p.Description[l])
		}
		base = append(base, obx.Str(p.ItemType), obx.Str(p.TargetGroup), obx.Str(p.Season), obx.Str(p.Collection),
			obx.Str(p.GPCBrick), FormatMaterials(p.Materials), FormatCare(p.Care), obx.Str(p.CountryOfOrigin), obx.Str(p.DPPURL))
		for _, k := range attrKeys {
			v := ""
			if a, ok := p.Attributes[k]; ok && a != nil {
				switch x := a.(type) {
				case map[string]any:
					v = fmt.Sprint(x["en"])
				default:
					v = fmt.Sprint(x)
				}
			}
			base = append(base, v)
		}
		imagesFor := func(color string) string {
			var names []string
			for _, a := range p.Assets {
				if a.ColorCode == nil || *a.ColorCode == color {
					names = append(names, a.Filename)
				}
			}
			return strings.Join(names, "|")
		}
		rows := [][]string{}
		for _, v := range p.Variants {
			gtin := obx.Str(v.GTINOriginal)
			if gtin == "" {
				gtin = obx.Str(v.GTIN)
			}
			r := append([]string(nil), base...)
			r = append(r, v.ExternalID, gtin, obx.Str(v.SKU), v.Color.Code)
			for _, l := range langs {
				r = append(r, v.Color.Name[l])
			}
			r = append(r, obx.Str(v.Color.SupplierColor), obx.Str(v.Size), obx.Str(v.SizeSystem), imagesFor(v.Color.Code))
			rows = append(rows, r)
		}
		if len(rows) == 0 {
			r := append([]string(nil), base...)
			r = append(r, "", "", "", "")
			for range langs {
				r = append(r, "")
			}
			r = append(r, "", "", "", imagesFor(""))
			rows = append(rows, r)
		}
		for _, r := range rows {
			for i := range r {
				r[i] = FormulaEscape(r[i])
			}
			if err := cw.Write(r); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}
