package obx_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

func TestNormalizeFiber(t *testing.T) {
	codes := func(f obx.Fiber) string {
		var out []string
		for _, q := range f.Qualifiers {
			out = append(out, q.Code)
		}
		return strings.Join(out, ",")
	}
	cases := map[string][2]string{
		"recycled_polyester":      {"polyester", "recycled"},
		"organic_cotton":          {"cotton", "organic"},
		"recycled_polyamide":      {"polyamide", "recycled"},
		"organic_recycled_cotton": {"cotton", "organic,recycled"},
		"recycled_":               {"recycled_", ""},
		"merino_wool":             {"merino_wool", ""},
	}
	for in, want := range cases {
		got := obx.NormalizeFiber(obx.Fiber{Material: obx.Term{Code: in}})
		if got.Material.Code != want[0] || codes(got) != want[1] {
			t.Errorf("%s: got %s (%s), want %v", in, got.Material.Code, codes(got), want)
		}
	}
	// The qualifier of a 0.1 key comes first; repeats are dropped, keeping
	// the first; the order is kept.
	got := obx.NormalizeFiber(obx.Fiber{Material: obx.Term{Code: "recycled_polyester"}, Qualifiers: []obx.Term{{Code: "post_consumer"}, {Code: "recycled"}, {Code: "post_consumer"}}})
	if got.Material.Code != "polyester" || codes(got) != "recycled,post_consumer" {
		t.Errorf("0.1 key with qualifiers: %+v", got)
	}
	if got := obx.NormalizeFiber(obx.Fiber{Material: obx.Term{Code: "cotton"}, Qualifiers: []obx.Term{}}); got.Qualifiers != nil {
		t.Errorf("an empty list is not nil: %#v", got.Qualifiers)
	}
	m := obx.Materials{{Part: obx.Term{Code: "outer"}, Fibers: []obx.Fiber{{Material: obx.Term{Code: "recycled_polyester"}, Percentage: obx.Ptr(100.0)}, {Material: obx.Term{Code: "elastane"}, Percentage: obx.Ptr(0.5)}}}}
	n := m.Normalized()
	if m[0].Fibers[0].Material.Code != "recycled_polyester" {
		t.Fatal("Normalized changed its receiver")
	}
	b, _ := json.Marshal(n)
	if string(b) != `[{"part":{"code":"outer","name":null},"fibers":[{"material":{"code":"polyester","name":null},"percentage":100,"qualifiers":[{"code":"recycled","name":null}]},{"material":{"code":"elastane","name":null},"percentage":0.5,"qualifiers":[]}]}]` {
		t.Fatalf("normalised materials: %s", b)
	}
	// A document without qualifiers reads as none.
	var f obx.Fiber
	if err := json.Unmarshal([]byte(`{"material":{"code":"cotton"},"percentage":100}`), &f); err != nil || f.Qualifiers != nil {
		t.Fatalf("fibre without qualifiers: %v %+v", err, f)
	}
}

// WithNames fills the vocabulary's names for known codes, keeps what was
// written for others, and never writes an empty string.
func TestMaterialsWithNames(t *testing.T) {
	v := specdata.Vocabulary()
	var m obx.Materials
	in := `[{"part":{"code":"main"},"fibers":[{"material":{"code":"cotton","name":{"en":"Mine"}},"percentage":95,"qualifiers":[{"code":"organic"},{"code":"in_conversion","name":{"en":"Mine"}}]},{"material":{"code":"seacell","name":{"en":"SeaCell","nl":""}},"percentage":5,"qualifiers":[{"code":"upcycled","name":{"en":"Upcycled","nl":""}}]}]},` +
		`{"part":{"code":"lining"},"fibers":[{"material":{"code":"recycled_polyester"},"percentage":100,"qualifiers":[{"code":"post_consumer"},{"code":"recycled"}]}]},{"part":{"code":"collar","name":{"nl":""}},"fibers":[{"material":{"code":"wool"},"percentage":100,"qualifiers":[]}]}]`
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m.WithNames(v))
	want := `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"cotton","name":{"de":"Baumwolle","en":"Cotton","fr":"Coton","nl":"Katoen"}},"percentage":95,"qualifiers":[{"code":"organic","name":{"de":"Bio","en":"Organic","fr":"Biologique","nl":"Biologisch"}},{"code":"in_conversion","name":{"de":"In Umstellung","en":"In conversion","fr":"En conversion","nl":"In omschakeling"}}]},` +
		`{"material":{"code":"seacell","name":{"en":"SeaCell"}},"percentage":5,"qualifiers":[{"code":"upcycled","name":{"en":"Upcycled"}}]}]},` +
		`{"part":{"code":"lining","name":{"de":"Futter","en":"Lining","fr":"Doublure","nl":"Voering"}},"fibers":[{"material":{"code":"polyester","name":{"de":"Polyester","en":"Polyester","fr":"Polyester","nl":"Polyester"}},"percentage":100,"qualifiers":[{"code":"recycled","name":{"de":"Recycelt","en":"Recycled","fr":"Recyclé","nl":"Gerecycled"}},{"code":"post_consumer","name":{"de":"Post-Consumer","en":"Post-consumer","fr":"Post-consommation","nl":"Post-consumer"}}]}]},` +
		`{"part":{"code":"collar","name":null},"fibers":[{"material":{"code":"wool","name":{"de":"Wolle","en":"Wool","fr":"Laine","nl":"Wol"}},"percentage":100,"qualifiers":[]}]}]`
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
	care := obx.Care{{Code: "wash_30", Text: obx.LocalizedText{"en": "Wash inside out", "nl": ""}}, {Code: "steam_only", Category: obx.Ptr("ironing"), Name: obx.LocalizedText{"en": "Steam only"}}}
	b, _ = json.Marshal(care.WithNames(v))
	if string(b) != `[{"code":"wash_30","category":"washing","symbol":"1_D_30","name":{"de":"Waschen bei 30 °C","en":"Wash at 30 °C","fr":"Lavage à 30 °C","nl":"Wassen op 30 °C"},"text":{"en":"Wash inside out"}},{"code":"steam_only","category":"ironing","symbol":null,"name":{"en":"Steam only"},"text":null}]` {
		t.Fatalf("care: %s", b)
	}
}

func TestFeaturesRoundTrip(t *testing.T) {
	in := `{"shell":"hard","wheels":4,"lock":"tsa_combination","expandable":true,"closure":"zip","detachable_parts":["shoulder_strap"],"x_ray":"no","zip_count":3,"tags":["a",1,true]}`
	var f obx.Features
	if err := json.Unmarshal([]byte(in), &f); err != nil {
		t.Fatal(err)
	}
	if f.Wheels == nil || *f.Wheels != 4 || obx.Str(f.Shell) != "hard" || f.Expandable == nil || !*f.Expandable {
		t.Fatalf("typed fields not decoded: %+v", f)
	}
	if f.Other["x_ray"] != "no" || f.Other["zip_count"] != 3.0 {
		t.Fatalf("unknown keys not kept: %v", f.Other)
	}
	if p := f.Validate(); len(p) != 0 {
		t.Fatalf("valid features reported %v", p)
	}
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	// Registry keys in registry order, then the others sorted.
	want := `{"shell":"hard","wheels":4,"lock":"tsa_combination","expandable":true,"closure":"zip","detachable_parts":["shoulder_strap"],"tags":["a",1,true],"x_ray":"no","zip_count":3}`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	v, _ := specdata.DefaultValidator()
	if err := v.Validate("Features", out); err != nil {
		t.Fatal(err)
	}
	if !(&obx.Features{}).IsEmpty() || f.IsEmpty() {
		t.Fatal("IsEmpty")
	}
	f.Set("wheels", nil)
	if _, ok := f.Get("wheels"); ok || obx.Contains(f.Keys(), "wheels") {
		t.Fatal("Set(nil) did not remove wheels")
	}
}

func TestFeaturesValidate(t *testing.T) {
	cases := map[string]string{
		`{"wheels":3}`:                         "wheels",
		`{"wheels":"four"}`:                    "wheels",
		`{"wheels":4.5}`:                       "wheels",
		`{"shell":"glass"}`:                    "shell",
		`{"expandable":"yes"}`:                 "expandable",
		`{"detachable_parts":["hood","hood"]}`: "appears twice",
		`{"detachable_parts":["wings"]}`:       "wings",
		`{"Bad":1}`:                            "not a valid feature key",
		`{"other":{"a":1}}`:                    "string, number, boolean",
	}
	for in, want := range cases {
		var f obx.Features
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		p := f.Validate()
		if len(p) == 0 || !strings.Contains(strings.Join(p, "; "), want) {
			t.Errorf("%s: problems %v, want one about %q", in, p, want)
		}
		// A value of the wrong type survives the round trip.
		out, _ := json.Marshal(f)
		var back, orig any
		_ = json.Unmarshal(out, &back)
		_ = json.Unmarshal([]byte(in), &orig)
		if !reflect.DeepEqual(back, orig) {
			t.Errorf("%s: re-encoded as %s", in, out)
		}
	}
	var f obx.Features
	_ = json.Unmarshal([]byte(`{"wheels":4.0}`), &f)
	if f.Wheels == nil || *f.Wheels != 4 || len(f.Validate()) != 0 {
		t.Errorf("4.0 is the integer 4: %+v %v", f, f.Validate())
	}
}

func TestMeasurementsRoundTrip(t *testing.T) {
	in := `{"height_cm":30,"weight_kg":0.9,"strap_length_cm":{"min":85,"max":140},"laptop_max_inch":15.6}`
	var m obx.Measurements
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(m)
	if string(out) != in {
		t.Fatalf("got %s", out)
	}
	if m.IsEmpty() || !(&obx.Measurements{}).IsEmpty() {
		t.Fatal("IsEmpty")
	}
	if p := (&obx.Measurements{WeightKG: obx.Ptr(0.0), StrapLengthCM: &obx.Range{Min: 9, Max: 2}}).Validate(); len(p) != 2 {
		t.Fatalf("Validate: %v", p)
	}
}

// Protocol objects always carry the 0.2 properties, as null when not stated.
func TestNewPropertiesAlwaysPresent(t *testing.T) {
	b, _ := json.Marshal(obx.Product{})
	for _, k := range []string{"item_subtype", "contains_animal_origin", "footwear_materials", "certifications", "features"} {
		if !strings.Contains(string(b), `"`+k+`":null`) {
			t.Errorf("Product lacks %s: %s", k, b)
		}
	}
	b, _ = json.Marshal(obx.Variant{})
	for _, k := range []string{"size_length", "measurements", "family"} {
		if !strings.Contains(string(b), `"`+k+`":null`) {
			t.Errorf("Variant lacks %s: %s", k, b)
		}
	}
	b, _ = json.Marshal(obx.FootwearMaterials{Upper: obx.Ptr("leather")})
	if string(b) != `{"upper":"leather","lining_and_sock":null,"outer_sole":null}` {
		t.Errorf("footwear materials: %s", b)
	}
}

func TestLabelWarnings(t *testing.T) {
	no := obx.Ptr(false)
	mat := func(s ...any) obx.Materials {
		p := obx.MaterialPart{Part: obx.Term{Code: "main"}}
		for i := 0; i < len(s); i += 2 {
			f := obx.Fiber{Material: obx.Term{Code: s[i].(string)}}
			if n, ok := s[i+1].(int); ok {
				f.Percentage = obx.Ptr(float64(n))
			}
			p.Fibers = append(p.Fibers, f)
		}
		return obx.Materials{p}
	}
	codes := func(ws []obx.LabelWarning) string {
		var out []string
		for _, w := range ws {
			out = append(out, w.Code+"@"+w.Column+w.Pointer)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name, itemType string
		m              obx.Materials
		animal         *bool
		fw             *obx.FootwearMaterials
		want           string
	}{
		{"tee without materials", "t_shirt", nil, no, nil, "fibre_composition_missing@materials/materials"},
		{"wool scarf, origin not stated", "scarf", mat("wool", 100), nil, nil, "animal_origin_unknown@contains_animal_origin/contains_animal_origin"},
		{"merino counts as wool", "sweater", mat("merino_wool", 100), nil, nil, "animal_origin_unknown@contains_animal_origin/contains_animal_origin"},
		{"sneaker without footwear materials", "sneakers", mat("polyester", 100), nil, nil, "footwear_materials_missing@footwear_materials/footwear_materials"},
		{"shirt at 90 %", "shirt", mat("cotton", 60, "polyester", 30), no, nil, "fibre_percentages_not_100@materials/materials/0"},
		{"sandal with two of three parts", "sandals", nil, nil, &obx.FootwearMaterials{Upper: obx.Ptr("leather"), LiningAndSock: obx.Ptr("leather")}, "footwear_materials_missing@footwear_materials/footwear_materials"},
		{"complete tee", "t_shirt", mat("cotton", 100), no, nil, ""},
		{"unknown item type", "wallet", nil, nil, nil, ""},
		{"no item type", "", nil, nil, nil, ""},
		{"luggage of plastic", "luggage", mat("polypropylene", 100), nil, nil, ""},
		{"a fibre without a percentage", "bag", mat("cotton", 60, "polyester", nil), no, nil, ""},
		{"stated ones above 100", "shirt", mat("cotton", 70, "polyester", 40, "elastane", nil), no, nil, "fibre_percentages_not_100@materials/materials/0"},
	}
	for _, c := range cases {
		var it *string
		if c.itemType != "" {
			it = &c.itemType
		}
		if got := codes(obx.LabelWarnings(it, c.m, c.animal, c.fw)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMaterialUnknownWarnings(t *testing.T) {
	v := specdata.Vocabulary()
	m := obx.Materials{{Part: obx.Term{Code: "main"}, Fibers: []obx.Fiber{{Material: obx.Term{Code: "cotton"}}, {Material: obx.Term{Code: "katoen"}}}},
		{Part: obx.Term{Code: "lining"}, Fibers: []obx.Fiber{{Material: obx.Term{Code: "katoen"}}, {Material: obx.Term{Code: "recycled_co"}}}}}
	ws := obx.MaterialUnknownWarnings(m, func(c string) bool { _, ok := v.MaterialName(c); return ok })
	var got []string
	for _, w := range ws {
		got = append(got, w.Value+"@"+w.Pointer)
	}
	if strings.Join(got, ",") != "katoen@/materials/0/fibers/1/material/code,co@/materials/1/fibers/1/material/code" {
		t.Fatalf("material_unknown: %v", got)
	}
}

// Extension members survive a decode and encode at every level that allows
// them; a percentage that is not stated is null.
func TestExtensionsRoundTrip(t *testing.T) {
	in := `{"style_number":"MN-1007","name":{"en":"Linen Shirt"},"materials":[{"part":{"code":"main"},"fibers":[{"material":{"code":"linen","x-noor-mill":"Mill 7"},"percentage":null,"qualifiers":[],"x-noor-a":[1,2]}]}],"x-noor-care-note":{"en":"Wrinkles"},"x-noor-fabric-weight-gsm":180}`
	var p obx.ProductInput
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	if string(p.X["x-noor-fabric-weight-gsm"]) != "180" || string(p.Materials[0].Fibers[0].Material.X["x-noor-mill"]) != `"Mill 7"` || p.Materials[0].Fibers[0].Percentage != nil {
		t.Fatalf("decoded %+v", p)
	}
	b, _ := json.Marshal(p)
	if string(b) != `{"style_number":"MN-1007","name":{"en":"Linen Shirt"},"materials":[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"linen","name":null,"x-noor-mill":"Mill 7"},"percentage":null,"qualifiers":[],"x-noor-a":[1,2]}]}],"x-noor-care-note":{"en":"Wrinkles"},"x-noor-fabric-weight-gsm":180}` {
		t.Fatalf("encoded %s", b)
	}
	var d obx.ProductDetail
	if err := json.Unmarshal([]byte(`{"id":"p1","style_number":"S","x-acme-b":true,"variants":[{"id":"v1","color":{"code":"BLK","x-acme-pantone":"19-4007"},"x-acme-bin":"A1"}],"assets":[]}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != "p1" || len(d.Variants) != 1 || string(d.Variants[0].X["x-acme-bin"]) != `"A1"` || string(d.Variants[0].Color.X["x-acme-pantone"]) != `"19-4007"` || string(d.X["x-acme-b"]) != "true" {
		t.Fatalf("detail %+v", d)
	}
	b, _ = json.Marshal(d)
	if !strings.Contains(string(b), `"x-acme-b":true,"variants":[{`) || !strings.Contains(string(b), `"x-acme-bin":"A1"`) {
		t.Fatalf("detail encoded %s", b)
	}
}
