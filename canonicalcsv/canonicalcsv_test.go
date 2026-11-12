package canonicalcsv_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(specdata.Fixtures(), name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fakeCatalog struct {
	brands   []canonicalcsv.CatalogBrand
	products map[string]canonicalcsv.CatalogProduct
}

func (c fakeCatalog) Brands() []canonicalcsv.CatalogBrand { return c.brands }
func (c fakeCatalog) FindProduct(s string) (canonicalcsv.CatalogProduct, bool) {
	for _, p := range c.products {
		if p.ExternalID == s || strings.EqualFold(p.StyleNumber, s) {
			return p, true
		}
	}
	return canonicalcsv.CatalogProduct{}, false
}

func kestrelImages(t *testing.T) []canonicalcsv.Image {
	var info map[string]struct {
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	if err := json.Unmarshal(fixture(t, "kestrel/images.json"), &info); err != nil {
		t.Fatal(err)
	}
	var out []canonicalcsv.Image
	for name, i := range info {
		out = append(out, canonicalcsv.Image{Name: name, SHA256: i.SHA256, Bytes: i.Bytes, Width: i.Width, Height: i.Height, ContentType: "image/png"})
	}
	return out
}

func TestKestrelFixture(t *testing.T) {
	pf, err := canonicalcsv.ParseProducts(bytes.NewReader(fixture(t, "kestrel/products.csv")), "products.csv", canonicalcsv.Options{DefaultLanguage: "nl"})
	if err != nil {
		t.Fatal(err)
	}
	if pf.Errors() != 0 || len(pf.Problems) != 0 {
		t.Fatalf("unexpected problems: %+v", pf.Problems)
	}
	variants := 0
	for _, p := range pf.Products {
		variants += len(p.Variants)
	}
	if len(pf.Products) != 11 || variants != 46 || pf.Rows != 46 {
		t.Fatalf("got %d products, %d variants, %d rows", len(pf.Products), variants, pf.Rows)
	}
	ko1005 := pf.Products[4]
	if ko1005.ExternalID != "KO-1005" || obx.Str(ko1005.Input.ItemType) != "bag" || ko1005.Input.Name["nl"] != "Leren weekendtas" {
		t.Fatalf("KO-1005 parsed wrong: %+v", ko1005.Input)
	}
	if got := canonicalcsv.FormatMaterials(ko1005.Input.Materials); got != "outer: 100% leather; lining: 100% cotton" {
		t.Fatalf("materials %q", got)
	}
	v := ko1005.Variants[0]
	if v.ExternalID != "2000100501016" || obx.Str(v.Input.GTIN) != "2000100501016" || v.Input.SizeSystem != nil || obx.Str(v.Input.Size) != "ONE SIZE" {
		t.Fatalf("KO-1005 variant parsed wrong: %+v", v)
	}
	af, err := canonicalcsv.ParseAssets(bytes.NewReader(fixture(t, "kestrel/assets.csv")), "assets.csv", canonicalcsv.Options{})
	if err != nil || len(af.Problems) != 0 || len(af.Rows) != 2 {
		t.Fatalf("assets.csv: %v %+v", err, af)
	}

	cat := fakeCatalog{brands: []canonicalcsv.CatalogBrand{{ExternalID: "kestrel", Name: "Kestrel"}}}
	plan := canonicalcsv.BuildPlan(pf, af, kestrelImages(t), cat)
	if plan.Errors() != 0 {
		t.Fatalf("plan problems: %+v", plan.Problems)
	}
	if len(plan.Brands) != 1 || plan.Brands[0].ExternalID != "kestrel-trail" || plan.Brands[0].Name != "Kestrel Trail" {
		t.Fatalf("new brands %+v", plan.Brands)
	}
	if strings.Join(plan.MirrorBrands, ",") != "kestrel,kestrel-trail" {
		t.Fatalf("mirror brands %v", plan.MirrorBrands)
	}
	assets := map[string]canonicalcsv.PlannedAsset{}
	for _, p := range plan.Products {
		for _, a := range p.Assets {
			assets[a.ExternalID] = a
		}
	}
	if len(assets) != 35 {
		t.Fatalf("want 35 assets, got %d", len(assets))
	}
	model := assets["KO-1003_RST_3"].Input
	if model.Role != "model" || model.View != nil || model.Provenance == nil || !*model.Provenance.AIGenerated || obx.Str(model.Provenance.Generator) != "fictional-image-model 1.0" || model.AltText["nl"] == "" {
		t.Fatalf("model asset: %+v", model)
	}
	detail := assets["KO-1004_IND_3"].Input
	if detail.Role != "detail" || obx.Str(detail.View) != "detail" {
		t.Fatalf("detail asset: %+v", detail)
	}
	emb := assets["KO-1002_BLK_2"].Input
	if emb.Rights == nil || emb.Rights.EmbargoUntil == nil || emb.Rights.EmbargoUntil.Year() != 2030 || obx.Str(emb.Rights.Credit) != "Photo: Kestrel Studio" || obx.Str(emb.View) != "back" {
		t.Fatalf("embargoed asset: %+v", emb)
	}
	if obx.Str(assets["KO-1001_NVY_1"].Input.Filename) != "KO-1001_NVY_1_front.png" {
		t.Fatal("filename not kept")
	}
}

func TestImageNames(t *testing.T) {
	cases := []struct {
		name, style, color string
		pos                int
		role, view         string
		fail               bool
	}{
		{name: "KO-1001_NVY_1.jpg", style: "KO-1001", color: "NVY", pos: 1, role: "packshot"},
		{name: "KO-1001_NVY_2_back.jpg", style: "KO-1001", color: "NVY", pos: 2, role: "packshot", view: "back"},
		{name: "KO-1003_RST_3_model.png", style: "KO-1003", color: "RST", pos: 3, role: "model"},
		{name: "AB_12_34_BLK_2_packshot_back.JPG", style: "AB_12_34", color: "BLK", pos: 2, role: "packshot", view: "back"},
		{name: "KO-1004_IND_3_detail.jpg", style: "KO-1004", color: "IND", pos: 3, role: "detail", view: "detail"},
		{name: "folder/KO-1001_NVY_12.webp", style: "KO-1001", color: "NVY", pos: 12, role: "packshot"},
		{name: "KO-1001_ALL_4_detail.jpg", style: "KO-1001", color: "", pos: 4, role: "detail", view: "detail"},
		{name: "KO-1001_all_5.jpg", style: "KO-1001", color: "", pos: 5, role: "packshot"},
		{name: "KO-1001_NVY_front.jpg", fail: true},
		{name: "KO-1001_NVY_01.jpg", fail: true},
		{name: "KO-1001_NVY_1000.jpg", fail: true},
		{name: "KO-1001_NVY_1.gif", fail: true},
		{name: "photo.jpg", fail: true},
	}
	for _, c := range cases {
		n, err := canonicalcsv.ParseImageName(c.name)
		if c.fail {
			if err == nil {
				t.Errorf("%s: expected an error, got %+v", c.name, n)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if n.Style != c.style || n.Color != c.color || n.Position != c.pos || n.Role != c.role || obx.Str(n.View) != c.view {
			t.Errorf("%s: got %+v view=%q", c.name, n, obx.Str(n.View))
		}
	}
}

func TestProductRowProblems(t *testing.T) {
	csv := "\ufeffStyle Number;Name;EAN;Colour Code;Size;Materials;Care;Mystery\n" +
		"S1;Shirt;2000100101018;NVY;M;95% cotton, 5% elastane;wash_30|do_not_bleach;x\n" +
		"S1;Shirt;2.00010010102E+12;NVY;L;;;\n" +
		"S1;;2000100101018;NVY;XL;;;\n" +
		"S2;Trousers;2000100101032;;M;;;\n" +
		"S3;Coat;;BLK;;60% wool, 30% cotton;wash_30|mystery_code;\n" +
		"S4;Hat;;;;main 100% wool;;\n" +
		";;;;;;;\n"
	pf, err := canonicalcsv.ParseProducts(strings.NewReader(csv), "products.csv", canonicalcsv.Options{DefaultLanguage: "nl"})
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		row      int
		col      string
		code     string
		severity string
	}
	got := map[key]bool{}
	for _, p := range pf.Problems {
		r := -1
		if p.Row != nil {
			r = *p.Row
		}
		got[key{r, obx.Str(p.Column), p.ObxError, p.Severity}] = true
	}
	want := []key{
		{-1, "Mystery", obx.ErrValidationFailed, obx.SeverityWarning},
		{2, "gtin", obx.ErrInvalidGTIN, obx.SeverityError},
		{3, "gtin", obx.ErrGTINConflict, obx.SeverityError},
		{4, "color_code", obx.ErrValidationFailed, obx.SeverityError},
		{5, "care", obx.ErrValidationFailed, obx.SeverityWarning},
		{6, "materials", obx.ErrValidationFailed, obx.SeverityError},
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing problem %+v; got %+v", w, got)
		}
	}
	if len(pf.Problems) != len(want) {
		t.Errorf("want %d problems, got %d: %+v", len(want), len(pf.Problems), got)
	}
	if len(pf.Products) != 2 || pf.Products[0].ExternalID != "S1" || len(pf.Products[0].Variants) != 1 || pf.Products[1].ExternalID != "S3" {
		t.Fatalf("products %+v", pf.Products)
	}
	if pf.Products[0].Input.Name["nl"] != "Shirt" {
		t.Fatal("plain name must use the default language")
	}
	if v := pf.Products[1].Variants[0]; v.ExternalID != "S3-BLK" {
		t.Fatalf("variant id without GTIN and size: %q", v.ExternalID)
	}
}

func TestUnreadable(t *testing.T) {
	for _, in := range []string{"", "\xff\xfe\x00bad"} {
		if _, err := canonicalcsv.ParseProducts(strings.NewReader(in), "p.csv", canonicalcsv.Options{}); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
	pf, err := canonicalcsv.ParseProducts(strings.NewReader("name,gtin\nx,1\n"), "p.csv", canonicalcsv.Options{})
	if err != nil || pf.Errors() != 1 || pf.Problems[0].ObxError != obx.ErrImportInvalid {
		t.Fatalf("missing style_number column: %v %+v", err, pf)
	}
}

func TestExportRoundTrip(t *testing.T) {
	pf, _ := canonicalcsv.ParseProducts(bytes.NewReader(fixture(t, "kestrel/products.csv")), "products.csv", canonicalcsv.Options{})
	var details []obx.ProductDetail
	for _, p := range pf.Products {
		d := obx.ProductDetail{Product: obx.Product{ExternalID: p.ExternalID, BrandID: "b1", StyleNumber: p.Input.StyleNumber, Name: p.Input.Name,
			Description: p.Input.Description, ItemType: p.Input.ItemType, ItemSubtype: p.Input.ItemSubtype, TargetGroup: p.Input.TargetGroup, Season: p.Input.Season,
			Collection: p.Input.Collection, Materials: p.Input.Materials, ContainsAnimalOrigin: p.Input.ContainsAnimalOrigin,
			FootwearMaterials: p.Input.FootwearMaterials, Care: p.Input.Care, CountryOfOrigin: p.Input.CountryOfOrigin,
			Certifications: p.Input.Certifications, Features: p.Input.Features}}
		for _, v := range p.Variants {
			g14, _ := obx.ValidateGTIN(obx.Str(v.Input.GTIN))
			d.Variants = append(d.Variants, obx.Variant{ExternalID: v.ExternalID, GTIN: &g14, GTINOriginal: v.Input.GTIN, SKU: v.Input.SKU, Color: v.Input.Color,
				Size: v.Input.Size, SizeLength: v.Input.SizeLength, SizeSystem: v.Input.SizeSystem, Measurements: v.Input.Measurements})
		}
		details = append(details, d)
	}
	details[0].Name["en"] = "=HYPERLINK(\"http://x\")"
	var buf bytes.Buffer
	if err := canonicalcsv.WriteExport(&buf, details, canonicalcsv.ExportOptions{Languages: []string{"nl", "en"}, Delimiter: ';', BrandNames: map[string]string{"b1": "Kestrel"}, BOM: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "'=HYPERLINK") {
		t.Fatal("formula not escaped")
	}
	again, err := canonicalcsv.ParseProducts(&buf, "export.csv", canonicalcsv.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Problems) != 0 {
		t.Fatalf("re-import problems: %+v", again.Problems)
	}
	if len(again.Products) != 11 {
		t.Fatalf("re-import got %d products", len(again.Products))
	}
	for i, p := range again.Products {
		if i == 0 {
			continue
		}
		a, _ := json.Marshal(p.Input)
		b, _ := json.Marshal(pf.Products[i].Input)
		if !bytes.Equal(a, b) {
			t.Errorf("product %s differs after round trip:\n%s\n%s", p.ExternalID, a, b)
		}
		if len(p.Variants) != len(pf.Products[i].Variants) {
			t.Errorf("variants differ for %s", p.ExternalID)
			continue
		}
		for j, v := range p.Variants {
			a, _ := json.Marshal(v.Input)
			b, _ := json.Marshal(pf.Products[i].Variants[j].Input)
			if !bytes.Equal(a, b) {
				t.Errorf("variant %s differs after round trip:\n%s\n%s", v.ExternalID, a, b)
			}
		}
	}
}

func TestFormulaEscape(t *testing.T) {
	cases := map[string]string{"=1+1": "'=1+1", "+31 6": "'+31 6", "-5": "-5", "@SUM": "'@SUM", "2000100101018": "2000100101018", "Navy": "Navy", "\tx": "'\tx"}
	for in, want := range cases {
		if got := canonicalcsv.FormulaEscape(in); got != want {
			t.Errorf("FormulaEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanImageProblems(t *testing.T) {
	pf, _ := canonicalcsv.ParseProducts(strings.NewReader("style_number,name,gtin,color_code,size\nS1,Shirt,2000100101018,NVY,M\n"), "products.csv", canonicalcsv.Options{})
	af, _ := canonicalcsv.ParseAssets(strings.NewReader("file,position,color_code,product_id\nmissing.jpg,,,\nhero.jpg,4,,S1\n"), "assets.csv", canonicalcsv.Options{})
	images := []canonicalcsv.Image{
		{Name: "S1_NVY_1.jpg", SHA256: strings.Repeat("a", 64)},
		{Name: "s1_nvy_1_front.jpg", SHA256: strings.Repeat("b", 64)},
		{Name: "S1_RED_1.jpg", SHA256: strings.Repeat("c", 64)},
		{Name: "S9_NVY_1.jpg", SHA256: strings.Repeat("d", 64)},
		{Name: "random.jpg", SHA256: strings.Repeat("e", 64)},
		{Name: "hero.jpg", SHA256: strings.Repeat("f", 64)},
		{Name: "OLD_NVY_2.jpg", SHA256: strings.Repeat("1", 64)},
		{Name: "S1_ALL_2_back.jpg", SHA256: strings.Repeat("2", 64)},
	}
	cat := fakeCatalog{brands: []canonicalcsv.CatalogBrand{{ExternalID: "b", Name: "B"}}, products: map[string]canonicalcsv.CatalogProduct{
		"OLD": {ExternalID: "OLD", StyleNumber: "OLD", BrandID: "b", Colors: []string{"NVY"}}}}
	plan := canonicalcsv.BuildPlan(pf, af, images, cat)
	codes := map[string]string{}
	for _, p := range plan.Problems {
		codes[p.File+"/"+p.ObxError] = obx.Str(p.Column)
	}
	for _, want := range []string{"S1_NVY_1.jpg/validation_failed", "s1_nvy_1_front.jpg/validation_failed", "S1_RED_1.jpg/unknown_parent",
		"S9_NVY_1.jpg/unknown_parent", "random.jpg/validation_failed", "assets.csv/files_missing"} {
		if _, ok := codes[want]; !ok {
			t.Errorf("missing problem %s in %v", want, codes)
		}
	}
	// hero.jpg (assets.csv, no colour) and S1_ALL_2_back.jpg are images of every colour.
	ids := map[string]bool{}
	for _, a := range plan.Products[0].Assets {
		ids[a.ExternalID] = true
		if a.Input.ColorCode != nil {
			t.Errorf("%s has colour %q, want none", a.ExternalID, *a.Input.ColorCode)
		}
	}
	if len(plan.Products) != 2 || len(plan.Products[0].Assets) != 2 || !ids["S1_ALL_4"] || !ids["S1_ALL_2"] {
		t.Fatalf("S1 assets: %+v", plan.Products[0].Assets)
	}
	if p := plan.Products[1]; p.ExternalID != "OLD" || p.Input != nil || p.Assets[0].ExternalID != "OLD_NVY_2" {
		t.Fatalf("catalog product images: %+v", p)
	}
}

// TestImageNameVectors runs the specification's shared filename test vectors
// (fixtures/expected/image-filenames.json, embedded in specdata).
func TestImageNameVectors(t *testing.T) {
	data, err := fs.ReadFile(specdata.Fixtures(), "expected/image-filenames.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name       string  `json:"name"`
			Style      string  `json:"style"`
			Color      *string `json:"color"`
			Position   int     `json:"position"`
			Role       string  `json:"role"`
			View       *string `json:"view"`
			ExternalID string  `json:"external_id"`
			Error      string  `json:"error"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) < 20 {
		t.Fatalf("only %d vectors", len(doc.Cases))
	}
	for _, c := range doc.Cases {
		n, err := canonicalcsv.ParseImageName(c.Name)
		if c.Error != "" {
			if err == nil {
				t.Errorf("%s: want an error, got %+v", c.Name, n)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		wantColor := ""
		if c.Color != nil {
			wantColor = *c.Color
		}
		if n.Style != c.Style || n.Color != wantColor || n.Position != c.Position || n.Role != c.Role || obx.Str(n.View) != obx.Str(c.View) ||
			canonicalcsv.AssetExternalID(n.Style, n.Color, n.Position) != c.ExternalID {
			t.Errorf("%s: got style %s colour %q position %d role %s view %q id %s", c.Name, n.Style, n.Color, n.Position, n.Role, obx.Str(n.View), canonicalcsv.AssetExternalID(n.Style, n.Color, n.Position))
		}
	}
}

// TestWriteDownloadAssets: the assets.csv of a portal zip (import-format.md
// §2.3) carries rights, embargo and provenance, formula-escaped, and reads
// back as an assets.csv.
func TestWriteDownloadAssets(t *testing.T) {
	embargo := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	credit := "=HYPERLINK(\"x\")"
	view := "back"
	nvy := "NVY"
	images := []canonicalcsv.DownloadImage{
		{File: "KO-1002_BLK_2_back_web.jpg", StyleNumber: "KO-1002", Asset: obx.Asset{ColorCode: &nvy, Position: 2, Role: "packshot", View: &view,
			AltText:    obx.LocalizedText{"nl": "Achterkant", "en": "Back"},
			Rights:     obx.Rights{License: "retailer-use", Channels: []string{"webshop", "social"}, EmbargoUntil: &embargo, Credit: &credit},
			Provenance: obx.Provenance{AIGenerated: true, Source: "generated", C2PA: "none"}}},
		{File: "KO-1005_ALL_4_size_chart_web.jpg", StyleNumber: "KO-1005", Asset: obx.Asset{Position: 4, Role: "size_chart", Rights: obx.DefaultRights(), Provenance: obx.DefaultProvenance()}},
	}
	var buf bytes.Buffer
	if err := canonicalcsv.WriteDownloadAssets(&buf, images); err != nil {
		t.Fatal(err)
	}
	af, err := canonicalcsv.ParseAssets(bytes.NewReader(buf.Bytes()), "assets.csv", canonicalcsv.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range af.Problems {
		t.Errorf("reading it back as an assets.csv: %+v", p)
	}
	if len(af.Rows) != 2 {
		t.Fatalf("rows %d", len(af.Rows))
	}
	r := af.Rows[0]
	if r.Rights == nil || r.Rights.EmbargoUntil == nil || !r.Rights.EmbargoUntil.Equal(embargo) {
		t.Errorf("embargo did not travel: %+v", r.Rights)
	}
	if r.Provenance == nil || r.Provenance.AIGenerated == nil || !*r.Provenance.AIGenerated {
		t.Errorf("AI label did not travel: %+v", r.Provenance)
	}
	if !strings.Contains(buf.String(), "'=HYPERLINK") {
		t.Errorf("a credit starting with = must be formula-escaped:\n%s", buf.String())
	}
	if r.AltText["nl"] != "Achterkant" {
		t.Errorf("alt text: %v", r.AltText)
	}
	if af.Rows[1].ColorCode == nil || *af.Rows[1].ColorCode != "" {
		t.Errorf("a colourless image has an empty color_code: %v", af.Rows[1].ColorCode)
	}
}
