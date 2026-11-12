package canonicalcsv_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

func parse(t *testing.T, csv string) *canonicalcsv.ProductsFile {
	t.Helper()
	pf, err := canonicalcsv.ParseProducts(strings.NewReader(csv), "products.csv", canonicalcsv.Options{DefaultLanguage: "en"})
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func product(t *testing.T, pf *canonicalcsv.ProductsFile, ext string) *canonicalcsv.Product {
	t.Helper()
	for _, p := range pf.Products {
		if p.ExternalID == ext {
			return p
		}
	}
	t.Fatalf("no product %s", ext)
	return nil
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The article data of the Kestrel fixture parses to what the expected
// protocol objects hold (fixtures/expected/product-*.json).
func TestKestrelArticleData(t *testing.T) {
	pf, err := canonicalcsv.ParseProducts(bytes.NewReader(fixture(t, "kestrel/products.csv")), "products.csv", canonicalcsv.Options{})
	if err != nil || len(pf.Problems) != 0 {
		t.Fatalf("%v %+v", err, pf.Problems)
	}
	ko1008 := product(t, pf, "KO-1008")
	in := ko1008.Input
	if obx.Str(in.ItemType) != "luggage" || obx.Str(in.ItemSubtype) != "cabin" || in.ContainsAnimalOrigin != nil {
		t.Errorf("KO-1008: %s", js(in))
	}
	if got := js(in.Features); got != `{"shell":"hard","wheels":4,"lock":"tsa_combination","expandable":true,"closure":"zip"}` {
		t.Errorf("KO-1008 features %s", got)
	}
	if got := js(in.Materials); got != `[{"part":{"code":"shell","name":null},"fibers":[{"material":{"code":"polypropylene","name":null},"percentage":100,"qualifier":null}]},{"part":{"code":"lining","name":null},"fibers":[{"material":{"code":"polyester","name":null},"percentage":100,"qualifier":{"code":"recycled","name":null}}]}]` {
		t.Errorf("KO-1008 materials %s", got)
	}
	if got := js(ko1008.Variants[1].Input.Measurements); got != `{"height_cm":55,"width_cm":40,"depth_cm":23,"weight_kg":2.8,"volume_l":42,"volume_expanded_l":48}` {
		t.Errorf("KO-1008 CABIN PLUS measurements %s", got)
	}
	if obx.Str(ko1008.Variants[0].Input.Color.Family) != "black" {
		t.Errorf("KO-1008 colour family %s", js(ko1008.Variants[0].Input.Color))
	}
	ko1009 := product(t, pf, "KO-1009")
	if got := js(ko1009.Input.Certifications); got != `[{"scheme":"grs","licence_number":"CU 200901","scope":"material"}]` {
		t.Errorf("KO-1009 certifications %s", got)
	}
	if got := js(ko1009.Variants[0].Input.Measurements); got != `{"height_cm":30,"width_cm":40,"depth_cm":12,"weight_kg":0.9,"volume_l":14,"handle_drop_cm":10,"strap_length_cm":{"min":85,"max":140},"laptop_max_inch":15.6}` {
		t.Errorf("KO-1009 measurements %s", got)
	}
	if got := js(ko1009.Input.Features); got != `{"closure":"flap","laptop_compartment":true,"water_resistance":"water_repellent","detachable_parts":["shoulder_strap"],"adjustable_parts":["shoulder_strap"]}` {
		t.Errorf("KO-1009 features %s", got)
	}
	ko1010 := product(t, pf, "KO-1010")
	if got := js(ko1010.Input.FootwearMaterials); got != `{"upper":"leather","lining_and_sock":"leather","outer_sole":"other"}` {
		t.Errorf("KO-1010 footwear %s", got)
	}
	if got := js(ko1010.Variants[2].Input.Measurements); got != `{"heel_height_cm":3.5,"shaft_height_cm":15}` {
		t.Errorf("KO-1010 43 measurements %s", got)
	}
	ko1003 := product(t, pf, "KO-1003")
	if got := js(ko1003.Input.Certifications); got != `[{"scheme":"gots","licence_number":"CU 200301","scope":"product"},{"scheme":"oeko_tex_standard_100","licence_number":"26.KST.20003","scope":"product"}]` {
		t.Errorf("KO-1003 certifications %s", got)
	}
	if got := js(ko1003.Input.Materials); got != `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"cotton","name":null},"percentage":100,"qualifier":{"code":"organic","name":null}}]}]` {
		t.Errorf("KO-1003 materials %s", got)
	}
	ko1004 := product(t, pf, "KO-1004").Variants[0].Input
	if obx.Str(ko1004.Size) != "30" || obx.Str(ko1004.SizeLength) != "32" || js(ko1004.Measurements) != `{"inseam_cm":81}` {
		t.Errorf("KO-1004 %s", js(ko1004))
	}
	kt2001 := product(t, pf, "KT-2001")
	if got := js(kt2001.Input.Materials[0]); got != `{"part":{"code":"outer","name":null},"fibers":[{"material":{"code":"polyester","name":null},"percentage":100,"qualifier":{"code":"recycled","name":null}}]}` {
		t.Errorf("KT-2001 0.1 key not normalised: %s", got)
	}
	ko1001 := product(t, pf, "KO-1001")
	if ko1001.Input.ContainsAnimalOrigin == nil || *ko1001.Input.ContainsAnimalOrigin || ko1001.Input.FootwearMaterials != nil || ko1001.Variants[0].Input.Measurements != nil {
		t.Errorf("KO-1001 %s", js(ko1001.Input))
	}
	if ko1006 := product(t, pf, "KO-1006"); ko1006.Input.Features != nil {
		t.Errorf("KO-1006 without feature cells must have features null: %s", js(ko1006.Input.Features))
	}
	// The fixture is complete: no label completeness warning.
	for _, p := range pf.Products {
		if w := p.Input.LabelWarnings(); len(w) != 0 {
			t.Errorf("%s has label warnings %+v", p.ExternalID, w)
		}
	}
}

// fixtures/imports/5-invalid.csv: the error rows of expected.json.
func TestImportVectorInvalid(t *testing.T) {
	var exp struct {
		Cases []struct {
			File   string               `json:"file"`
			Errors []obx.ImportRowError `json:"errors"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixture(t, "imports/expected.json"), &exp); err != nil {
		t.Fatal(err)
	}
	for _, c := range exp.Cases {
		pf, err := canonicalcsv.ParseProducts(bytes.NewReader(fixture(t, "imports/"+c.File)), "products.csv", canonicalcsv.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var errs []obx.ImportRowError
		for _, p := range pf.Problems {
			if p.Severity == obx.SeverityError {
				errs = append(errs, p)
			}
		}
		if len(errs) != len(c.Errors) {
			t.Errorf("%s: %d errors, want %d: %+v", c.File, len(errs), len(c.Errors), pf.Problems)
			continue
		}
		for i, w := range c.Errors {
			g := errs[i]
			if g.File != w.File || obx.Str(g.Column) != obx.Str(w.Column) || g.ObxError != w.ObxError || obx.Str(g.Value) != obx.Str(w.Value) || g.Row == nil || *g.Row != *w.Row {
				t.Errorf("%s: error %d is %s, want %s", c.File, i, js(g), js(w))
			}
		}
		if len(c.Errors) > 0 && len(pf.Products) != 0 {
			t.Errorf("%s: rows with errors must be skipped, got %d products", c.File, len(pf.Products))
		}
	}
	pf, _ := canonicalcsv.ParseProducts(bytes.NewReader(fixture(t, "imports/3-measurement-update.csv")), "products.csv", canonicalcsv.Options{})
	if got := js(pf.Products[0].Variants[0].Input.Measurements); got != `{"weight_kg":2.7}` {
		t.Errorf("decimal comma: %s", got)
	}
	if !pf.Present.Plain["weight_kg"] || pf.Present.Plain["height_cm"] {
		t.Errorf("present measurement columns %v", pf.Present.Plain)
	}
	pf, _ = canonicalcsv.ParseProducts(bytes.NewReader(fixture(t, "imports/2-feature-clear.csv")), "products.csv", canonicalcsv.Options{})
	if !pf.Present.Features["wheels"] || pf.Products[0].Input.Features != nil {
		t.Errorf("an empty feature cell: present %v, features %s", pf.Present.Features, js(pf.Products[0].Input.Features))
	}
}

func TestArticleColumnRules(t *testing.T) {
	csv := "style_number,name,item_type,item_subtype,certifications,footwear_materials,contains_animal_origin,Feature:Wheels,feature:pockets,feature:detachable_parts,gtin,color_code,colour_family,size,strap_length_cm,height_cm,size_length\n" +
		"B1,Bag,Bag,Laptop Bag,\"GOTS:CU 1 2 3 (Material); fsc; rds (product); oeko tex standard 100:21.HUS.12345\",,Ja,4,3 inside,Hood|hood|Belt,2000100101018,BLK,Gray,M,120,\"12,5\",\n" +
		"B1,Bag,,,,,,,,,2000100101025,BLK,black,L,80 - 130,7,\n" +
		"B1,Bag,,,,,,,,,,,,,,9,34\n" +
		"S1,Shoe,shoes,,,UPPER: Coated Leather; outer sole: other,,,,,,,,,,,\n"
	pf := parse(t, csv)
	if pf.Errors() != 0 {
		t.Fatalf("errors: %s", js(pf.Problems))
	}
	b1 := product(t, pf, "B1").Input
	if obx.Str(b1.ItemSubtype) != "laptop_bag" || b1.ContainsAnimalOrigin == nil || !*b1.ContainsAnimalOrigin {
		t.Errorf("B1 %s", js(b1))
	}
	if got := js(b1.Certifications); got != `[{"scheme":"gots","licence_number":"CU 1 2 3","scope":"material"},{"scheme":"fsc","licence_number":null,"scope":"product"},{"scheme":"rds","licence_number":null,"scope":"product"},{"scheme":"oeko_tex_standard_100","licence_number":"21.HUS.12345","scope":"product"}]` {
		t.Errorf("certifications %s", got)
	}
	if got := js(b1.Features); got != `{"wheels":4,"detachable_parts":["hood","belt"],"pockets":"3 inside"}` {
		t.Errorf("features %s", got)
	}
	vs := product(t, pf, "B1").Variants
	if got := js(vs[0].Input.Measurements); got != `{"height_cm":12.5,"strap_length_cm":{"min":120,"max":120}}` {
		t.Errorf("variant 1 measurements %s", got)
	}
	if got := js(vs[1].Input.Measurements); got != `{"height_cm":7,"strap_length_cm":{"min":80,"max":130}}` {
		t.Errorf("variant 2 measurements %s", got)
	}
	if obx.Str(vs[0].Input.Color.Family) != "grey" {
		t.Errorf("gray is grey: %s", js(vs[0].Input.Color))
	}
	if got := js(product(t, pf, "S1").Input.FootwearMaterials); got != `{"upper":"coated_leather","lining_and_sock":null,"outer_sole":"other"}` {
		t.Errorf("footwear %s", got)
	}
	warned := map[string]int{}
	for _, p := range pf.Problems {
		warned[obx.Str(p.Column)]++
	}
	// One warning for the column of a key outside the registry, one for a
	// colour family that differs within a colour, one per ignored cell of a
	// row without variant columns.
	if warned["feature:pockets"] != 1 || warned["color_family"] != 1 || warned["height_cm"] != 1 || warned["size_length"] != 1 || len(pf.Problems) != 4 {
		t.Errorf("warnings %s", js(pf.Problems))
	}

	bad := map[string]string{
		"feature:wheels":           "5",
		"feature:shell":            "glass",
		"feature:expandable":       "maybe",
		"feature:adjustable_parts": "hood",
		"contains_animal_origin":   "perhaps",
		"footwear_materials":       "sole: leather",
		"certifications":           "GOTS!:1",
		"strap_length_cm":          "130-80",
		"weight_kg":                "1.200,5",
		"depth_cm":                 "-3",
		"inseam_cm":                "0",
		"color_family":             "navy",
		"item_subtype":             "!",
	}
	for col, v := range bad {
		pf := parse(t, "style_number,name,gtin,color_code,"+col+"\nX1,X,2000100101018,BLK,\""+v+"\"\n")
		if pf.Errors() != 1 || obx.Str(pf.Problems[0].Column) != col || obx.Str(pf.Problems[0].Value) != v {
			t.Errorf("%s = %q: %s", col, v, js(pf.Problems))
		}
	}
}

func TestArticleValueFormatsRoundTrip(t *testing.T) {
	for _, s := range []string{"gots:CU 123456; oeko_tex_standard_100:21.HUS.12345", "rws:CU 200101 (material)", "fsc (material); rds"} {
		c, err := canonicalcsv.ParseCertifications(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := canonicalcsv.FormatCertifications(c); got != s {
			t.Errorf("certifications %q → %q", s, got)
		}
	}
	for _, s := range []string{"upper: leather; lining_and_sock: textile; outer_sole: other", "upper: leather"} {
		f, err := canonicalcsv.ParseFootwearMaterials(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := canonicalcsv.FormatFootwearMaterials(f); got != s {
			t.Errorf("footwear %q → %q", s, got)
		}
	}
	for _, s := range []string{"outer: 100% polyester (recycled); lining: 88% polyamide, 12% elastane", "100% cotton (organic)", "70% wool (regenerative), 30% seacell (in_conversion)"} {
		m, _, err := canonicalcsv.ParseMaterials(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := canonicalcsv.FormatMaterials(m); strings.TrimPrefix(got, "main: ") != s && got != s {
			t.Errorf("materials %q → %q", s, got)
		}
	}
	a, _, _ := canonicalcsv.ParseMaterials("100% organic_cotton")
	b, _, _ := canonicalcsv.ParseMaterials("100% Cotton (Organic)")
	if js(a) != js(b) {
		t.Errorf("0.1 key and qualifier differ: %s %s", js(a), js(b))
	}
	// The 0.1 key wins over a qualifier in brackets.
	if c, _, _ := canonicalcsv.ParseMaterials("100% recycled_polyester (organic)"); canonicalcsv.FormatMaterials(c) != "main: 100% polyester (recycled)" {
		t.Errorf("0.1 key with qualifier: %s", js(c))
	}
	for _, bad := range []string{"100% cotton (organic) (recycled)", "100% cotton (organic)(recycled)", "100% cotton (Ä)", "100% (organic)"} {
		if _, _, err := canonicalcsv.ParseMaterials(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// The code lists of the package are the spec's vocabularies.
func TestCodesMatchVocabulary(t *testing.T) {
	v := specdata.Vocabulary()
	for name, list := range map[string][]string{"parts": canonicalcsv.PartKeys, "materials": canonicalcsv.FiberKeys, "care": canonicalcsv.CareCodes} {
		if strings.Join(list, ",") != strings.Join(v.Order[name], ",") {
			t.Errorf("%s: %v, vocabulary %v", name, list, v.Order[name])
		}
	}
}
