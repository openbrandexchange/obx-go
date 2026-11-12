package conformance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

// Article data: docs/protocol.md 5.9, import-format.md 1.6 and rules 9
// and 10. P-CAT-5 and P-CAT-6 are registered with the protocol profile,
// I-IMP-7 and I-IMP-8 with the ingest profile.

// matchProducts fetches products by external id and matches them with
// expected/product-<ext>.json.
func (t *T) matchProducts(exts ...string) {
	for _, ext := range exts {
		want, err := t.r.fixtureExpect("product-" + ext + ".json")
		t.Check(err, "fixture expected/product-"+ext+".json")
		id := t.productID(ext)
		res := t.get(t.r.full, "/products/"+id)
		if res.status != http.StatusOK {
			t.Errorf("GET %s: %d", ext, res.status)
			continue
		}
		t.validate("ProductDetail", res.body, ext)
		var got any
		_ = json.Unmarshal(res.body, &got)
		for _, d := range matchExpected(got, want, ext, t.r.productExpectation(t.ctx)) {
			t.Errorf("%s", d)
		}
	}
}

// testArticleRoundTrip is P-CAT-5: the article data of the suitcase, the
// laptop bag, the Chelsea boot and KT-2001 (0.1 fibre key) round-trips,
// with the vocabulary's names for parts, materials, qualifiers and care.
func testArticleRoundTrip(t *T) {
	t.matchProducts("KO-1008", "KO-1009", "KO-1010", "KT-2001")
}

var (
	productArticleProps = []string{"item_subtype", "contains_animal_origin", "footwear_materials", "certifications", "features"}
	variantArticleProps = []string{"size_length", "measurements"}
)

// testArticleShape is P-CAT-6: the shape of the article data over the whole
// snapshot.
func testArticleShape(t *T) {
	f := t.readFeed(t.r.full, "", 500)
	v, err := specdata.DefaultValidator()
	t.Check(err, "schemas")
	schemaFor := map[string]string{obx.ChangeProduct: "Product", obx.ChangeVariant: "Variant", obx.ChangeBrand: "Brand", obx.ChangeAsset: "Asset"}
	products := map[string]map[string]any{}   // external id → data
	variants := map[string][]map[string]any{} // product id → data
	productExt := map[string]string{}         // product id → external id
	for _, ch := range f.snapshot {
		s := schemaFor[ch.Type]
		if s == "" {
			continue
		}
		if err := v.Validate(s, ch.Data); err != nil {
			t.Errorf("%s %s does not validate against the 0.3 %s schema: %v", ch.Type, ch.ExternalIDValue(), s, err)
		}
		var data map[string]any
		if json.Unmarshal(ch.Data, &data) != nil {
			continue
		}
		ext := ch.ExternalIDValue()
		switch ch.Type {
		case obx.ChangeProduct:
			products[ext] = data
			productExt[ch.ID] = ext
			for _, p := range productArticleProps {
				if _, ok := data[p]; !ok {
					t.Errorf("product %s has no property %s (null when not stated)", ext, p)
				}
			}
			checkMaterials(t, ext, data["materials"])
			checkCare(t, ext, data["care"])
			if obj, ok := data["features"].(map[string]any); ok && len(obj) == 0 {
				t.Errorf("product %s: features is an empty object, want null", ext)
			}
			if fw, ok := data["footwear_materials"].(map[string]any); ok {
				for _, part := range obx.FootwearParts {
					if _, ok := fw[part]; !ok {
						t.Errorf("product %s: footwear_materials has no %s (all three parts, null when not stated)", ext, part)
					}
				}
			}
		case obx.ChangeVariant:
			pid, _ := data["product_id"].(string)
			variants[pid] = append(variants[pid], data)
			for _, p := range variantArticleProps {
				if _, ok := data[p]; !ok {
					t.Errorf("variant %s has no property %s (null when not stated)", ext, p)
				}
			}
			if c, ok := data["color"].(map[string]any); !ok {
				t.Errorf("variant %s has no color", ext)
			} else if _, ok := c["family"]; !ok {
				t.Errorf("variant %s: color has no family (null when not stated)", ext)
			}
			if obj, ok := data["measurements"].(map[string]any); ok && len(obj) == 0 {
				t.Errorf("variant %s: measurements is an empty object, want null", ext)
			}
		}
	}

	if t.r.online() && !t.r.fixturesPresent {
		t.partialf("KO-1003 and KO-1004 (needs the Kestrel test data); the shape of every object was checked")
		return
	}
	// KO-1004: three sizes with a second size and the inseam.
	var ko1004 []map[string]any
	for pid, ext := range productExt {
		if ext == "KO-1004" {
			ko1004 = variants[pid]
		}
	}
	sizes := map[string]bool{}
	for _, d := range ko1004 {
		sizes[fmt.Sprint(d["size"])] = true
		if d["size_length"] != "32" {
			t.Errorf("KO-1004 variant %v: size_length %s, want \"32\"", d["external_id"], short(d["size_length"]))
		}
		for _, p := range matchExpected(d["measurements"], map[string]any{"inseam_cm": 81.0}, fmt.Sprintf("KO-1004 variant %v /measurements", d["external_id"]), nil) {
			t.Errorf("%s", p)
		}
	}
	if len(ko1004) != 3 || !sizes["30"] || !sizes["32"] || !sizes["34"] {
		t.Errorf("KO-1004 has variants with sizes %v, want 30, 32 and 34", sizes)
	}

	// KO-1003: organic cotton and two certifications in order.
	ko1003 := products["KO-1003"]
	if ko1003 == nil {
		t.Errorf("KO-1003 is not in the snapshot")
		return
	}
	if got := materialCodes(ko1003["materials"]); got != "main: 100% cotton (organic)" {
		t.Errorf("KO-1003/materials: %s, want a main fibre with material code cotton, percentage 100 and qualifier code organic", got)
	}
	wantCerts := []any{
		map[string]any{"scheme": "gots", "licence_number": "CU 200301", "scope": "product"},
		map[string]any{"scheme": "oeko_tex_standard_100", "licence_number": "26.KST.20003", "scope": "product"},
	}
	for _, p := range matchExpected(ko1003["certifications"], wantCerts, "KO-1003/certifications", nil) {
		t.Errorf("%s", p)
	}
}

// termCode returns the code of a term object ("" when it is not one).
func termCode(v any) string {
	m, _ := v.(map[string]any)
	c, _ := m["code"].(string)
	return c
}

// materialCodes writes materials in the import grammar (codes only), for
// messages and comparisons.
func materialCodes(materials any) string {
	parts, _ := materials.([]any)
	var out []string
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		fibers, _ := pm["fibers"].([]any)
		var fs []string
		for _, f := range fibers {
			fm, _ := f.(map[string]any)
			s := fmt.Sprintf("%v%% %s", fm["percentage"], termCode(fm["material"]))
			if q := termCode(fm["qualifier"]); q != "" {
				s += " (" + q + ")"
			}
			fs = append(fs, s)
		}
		out = append(out, termCode(pm["part"])+": "+strings.Join(fs, ", "))
	}
	return strings.Join(out, "; ")
}

// checkName checks a name (LocalizedText or null) for empty strings, and
// when required that it has at least one language.
func checkName(t *T, where string, name any, required bool) {
	m, ok := name.(map[string]any)
	if !ok {
		if required {
			t.Errorf("%s has the name %s; a code of the vocabularies has a name", where, short(name))
		}
		return
	}
	if required && len(m) == 0 {
		t.Errorf("%s has an empty name; a code of the vocabularies has a name", where)
	}
	for lang, v := range m {
		if v == "" {
			t.Errorf("%s: the name in %s is an empty string; a language without a name is omitted", where, lang)
		}
	}
}

// checkTerm checks one part, material or qualifier against the vocabulary
// (P-CAT-6): a known code has a name, main has the name null.
func checkTerm(t *T, where, kind string, term any, lookup func(string) (obx.LocalizedText, bool)) {
	m, ok := term.(map[string]any)
	if !ok {
		t.Errorf("%s: %s is %s, want an object {code, name}", where, kind, short(term))
		return
	}
	code, _ := m["code"].(string)
	where = fmt.Sprintf("%s %s %q", where, kind, code)
	if _, ok := m["name"]; !ok {
		t.Errorf("%s has no property name (null when there is none)", where)
	}
	if kind == "part" && code == "main" {
		if m["name"] != nil {
			t.Errorf("%s has the name %s, want null", where, short(m["name"]))
		}
		return
	}
	_, known := lookup(code)
	checkName(t, where, m["name"], known)
}

// checkMaterials: no 0.1 material key, names of the vocabularies' codes,
// main without a name, no empty strings.
func checkMaterials(t *T, ext string, materials any) {
	v := specdata.Vocabulary()
	parts, _ := materials.([]any)
	where := "product " + ext
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		checkTerm(t, where, "part", pm["part"], v.PartName)
		fibers, _ := pm["fibers"].([]any)
		for _, f := range fibers {
			fm, _ := f.(map[string]any)
			code := termCode(fm["material"])
			if strings.HasPrefix(code, "recycled_") || strings.HasPrefix(code, "organic_") {
				t.Errorf("%s: material %q is a 0.1 key; a 0.3 node returns the material with the qualifier recycled or organic", where, code)
			}
			for _, gone := range []string{"fiber", "recycled", "organic"} {
				if _, ok := fm[gone]; ok {
					t.Errorf("%s: fibre %s has the 0.2 property %s", where, code, gone)
				}
			}
			checkTerm(t, where, "material", fm["material"], v.MaterialName)
			if _, ok := fm["qualifier"]; !ok {
				t.Errorf("%s: fibre %s has no property qualifier (null when there is none)", where, code)
			} else if fm["qualifier"] != nil {
				checkTerm(t, where, "qualifier", fm["qualifier"], v.QualifierName)
			}
		}
	}
}

// checkCare: a care code of the vocabulary has exactly its category and
// symbol and a name; every entry has all properties; no empty strings.
func checkCare(t *T, ext string, care any) {
	v := specdata.Vocabulary()
	entries, _ := care.([]any)
	for _, e := range entries {
		m, _ := e.(map[string]any)
		code, _ := m["code"].(string)
		where := fmt.Sprintf("product %s care %q", ext, code)
		for _, p := range []string{"category", "symbol", "name", "text"} {
			if _, ok := m[p]; !ok {
				t.Errorf("%s has no property %s (null when unknown)", where, p)
			}
		}
		want, known := v.CareCode(code)
		if known {
			if got, _ := m["category"].(string); m["category"] == nil || got != obx.Str(want.Category) {
				t.Errorf("%s: category %s, want %q", where, short(m["category"]), obx.Str(want.Category))
			}
			if want.Symbol == nil && m["symbol"] != nil {
				t.Errorf("%s: symbol %s, want null", where, short(m["symbol"]))
			} else if got, _ := m["symbol"].(string); want.Symbol != nil && got != *want.Symbol {
				t.Errorf("%s: symbol %s, want %q", where, short(m["symbol"]), *want.Symbol)
			}
		}
		checkName(t, where, m["name"], known)
		checkName(t, where+" text", m["text"], false)
	}
}

// importCase is one case of fixtures/imports/expected.json.
type importCase struct {
	File   string `json:"file"`
	What   string `json:"what"`
	Counts struct {
		Rows, Created, Updated, Unchanged, Errors int
	} `json:"counts"`
	Errors   []obx.ImportRowError      `json:"errors"`
	Changes  []expectedChange          `json:"changes"`
	Products map[string]map[string]any `json:"products"`
	Variants map[string]map[string]any `json:"variants"`
}

// productsCSVImport is an import of one products.csv (mode upsert, no images).
func (t *T) productsCSVImport(file string, dryRun bool) (*obx.IngestJob, []obx.ImportRowError) {
	data, err := t.r.fixture("imports/" + file)
	t.Check(err, "read fixtures/imports/"+file)
	return t.runImport(&ingest.ImportRequest{Options: obx.ImportOptions{Mode: obx.ImportModeUpsert, DryRun: dryRun},
		CSV: &ingest.File{Name: "products.csv", Data: data}})
}

// testImportArticleColumns is I-IMP-7: the article data columns and rule 9 per key.
func testImportArticleColumns(t *T) {
	raw, err := t.r.fixture("imports/expected.json")
	t.Check(err, "read fixtures/imports/expected.json")
	var exp struct {
		Cases []importCase `json:"cases"`
	}
	t.Check(json.Unmarshal(raw, &exp), "parse fixtures/imports/expected.json")
	restore := func() {
		if req, err := t.r.fixtureImport(t.ctx, "files", nil); err == nil {
			t.runImport(req)
		}
	}
	// The node must hold exactly the import of fixtures/kestrel.
	restore()
	defer restore()
	for _, c := range exp.Cases {
		var cursor string
		if t.r.full != nil {
			cursor = t.liveCursor(t.r.full)
		}
		job, probs := t.productsCSVImport(c.File, false)
		name := c.File
		if job.Status != obx.JobSucceeded || job.ImportRun == nil {
			t.Errorf("%s: job %s (%s)", name, job.Status, short(probs))
			continue
		}
		got := job.ImportRun.Counts
		if got.Rows != c.Counts.Rows || got.Created != c.Counts.Created || got.Updated != c.Counts.Updated || got.Unchanged != c.Counts.Unchanged || got.Errors != c.Counts.Errors {
			t.Errorf("%s (%s): counts rows %d, created %d, updated %d, unchanged %d, errors %d; want %d, %d, %d, %d, %d (%s)", name, c.What,
				got.Rows, got.Created, got.Updated, got.Unchanged, got.Errors,
				c.Counts.Rows, c.Counts.Created, c.Counts.Updated, c.Counts.Unchanged, c.Counts.Errors, short(errorsOf(probs)))
		}
		errs := errorsOf(probs)
		if len(errs) != len(c.Errors) {
			t.Errorf("%s: %d error rows, want %d: %s", name, len(errs), len(c.Errors), short(errs))
		} else {
			for i, w := range c.Errors {
				g := errs[i]
				if g.File != w.File || !sameRow(g.Row, w.Row) || obx.Str(g.Column) != obx.Str(w.Column) || g.Severity != w.Severity || g.ObxError != w.ObxError || (w.Value != nil && obx.Str(g.Value) != *w.Value) {
					t.Errorf("%s: error %d is %s, want %s", name, i+1, short(g), short(w))
				}
			}
		}
		if t.r.full != nil {
			live, _ := t.newEntries(t.r.full, cursor)
			var gotKeys, wantKeys []string
			for _, ch := range live {
				gotKeys = append(gotKeys, ch.Type+" "+ch.Op+" "+ch.ExternalIDValue())
			}
			for _, w := range c.Changes {
				wantKeys = append(wantKeys, w.Type+" "+w.Op+" "+w.ExternalID)
			}
			if strings.Join(gotKeys, "; ") != strings.Join(wantKeys, "; ") {
				t.Errorf("%s: change entries [%s], want [%s]", name, strings.Join(gotKeys, "; "), strings.Join(wantKeys, "; "))
			}
		}
		t.checkFields(name, c.Products, c.Variants)
	}
}

func errorsOf(probs []obx.ImportRowError) []obx.ImportRowError {
	var out []obx.ImportRowError
	for _, p := range probs {
		if p.Severity == obx.SeverityError {
			out = append(out, p)
		}
	}
	return out
}

func sameRow(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// checkFields matches the listed fields of products and variants (by
// external id) with the protocol objects, or without a protocol client with
// the Ingest API records, whose data has the same field names.
func (t *T) checkFields(name string, products, variants map[string]map[string]any) {
	if len(products) == 0 && len(variants) == 0 {
		return
	}
	actual := map[string]map[string]any{} // "product:ext" / "variant:ext" → object
	if t.r.full != nil {
		t.Check(t.r.full.EachProduct(t.ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error {
			b, _ := json.Marshal(p)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			actual["product:"+p.ExternalID] = m
			vs, _ := m["variants"].([]any)
			for _, v := range vs {
				if vm, ok := v.(map[string]any); ok {
					actual[fmt.Sprintf("variant:%v", vm["external_id"])] = vm
				}
			}
			return nil
		}), "list products")
	} else {
		for kind, list := range map[string]map[string]map[string]any{obx.KindProduct: products, obx.KindVariant: variants} {
			for ext := range list {
				rec, err := t.r.ing.Get(t.ctx, kind, ext)
				t.Check(err, "GET "+kind+" "+ext)
				var m map[string]any
				_ = json.Unmarshal(rec.Data, &m)
				actual[kind+":"+ext] = m
			}
		}
	}
	for kind, list := range map[string]map[string]map[string]any{obx.KindProduct: products, obx.KindVariant: variants} {
		for ext, fields := range list {
			obj := actual[kind+":"+ext]
			if obj == nil {
				t.Errorf("%s: %s %s not found", name, kind, ext)
				continue
			}
			for field, want := range fields {
				for _, d := range matchExpected(obj[field], want, fmt.Sprintf("%s: %s %s /%s", name, kind, ext, field), nil) {
					t.Errorf("%s", d)
				}
			}
		}
	}
}

// testLabelWarnings is I-IMP-8: label completeness warnings with stable codes.
func testLabelWarnings(t *T) {
	raw, err := t.r.fixture("imports/label-warnings.expected.json")
	t.Check(err, "read fixtures/imports/label-warnings.expected.json")
	var exp struct {
		Warnings []obx.ImportRowError `json:"warnings"`
	}
	t.Check(json.Unmarshal(raw, &exp), "parse label-warnings.expected.json")
	var cursor string
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	job, probs := t.productsCSVImport("label-warnings.csv", true)
	if job.Status != obx.JobSucceeded {
		t.Errorf("dry run of label-warnings.csv: job %s (%s)", job.Status, short(probs))
	}
	if errs := errorsOf(probs); len(errs) > 0 {
		t.Errorf("label-warnings.csv has errors: %s", short(errs))
	}
	got := labelCoded(probs)
	if len(got) != len(exp.Warnings) {
		t.Errorf("label warnings: got %d, want %d: %s", len(got), len(exp.Warnings), short(got))
	} else {
		for i, w := range exp.Warnings {
			g := got[i]
			if g.File != w.File || !sameRow(g.Row, w.Row) || obx.Str(g.Column) != obx.Str(w.Column) || g.Severity != w.Severity || g.ObxError != w.ObxError {
				t.Errorf("label warning %d is %s, want %s", i+1, short(g), short(w))
			}
		}
	}
	for i := 1; i <= 6; i++ {
		ext := fmt.Sprintf("KL-800%d", i)
		if _, err := t.r.ing.Get(t.ctx, obx.KindProduct, ext); err == nil {
			t.Errorf("the dry run wrote product %s", ext)
			_, _ = t.r.ing.Delete(t.ctx, obx.KindProduct, ext)
		}
	}
	if t.r.full != nil {
		if live, _ := t.newEntries(t.r.full, cursor); len(live) > 0 {
			t.Errorf("the dry run wrote change entries: %v", changeKeys(live))
		}
	}
	req, err := t.r.fixtureImport(t.ctx, "files", nil)
	t.Check(err, "build the import of fixtures/kestrel")
	_, probs = t.runImport(req)
	if coded := labelCoded(probs); len(coded) > 0 {
		t.Errorf("the import of fixtures/kestrel has label warnings: %s", short(coded))
	}
}

func labelCoded(probs []obx.ImportRowError) []obx.ImportRowError {
	var out []obx.ImportRowError
	for _, p := range probs {
		if obx.Contains(obx.WarningCodes, p.ObxError) {
			out = append(out, p)
		}
	}
	return out
}
