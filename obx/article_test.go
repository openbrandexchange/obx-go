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
	cases := map[string]obx.Fiber{
		"recycled_polyester":      {Fiber: "polyester", Recycled: true},
		"organic_cotton":          {Fiber: "cotton", Organic: true},
		"recycled_polyamide":      {Fiber: "polyamide", Recycled: true},
		"organic_recycled_cotton": {Fiber: "cotton", Recycled: true, Organic: true},
		"recycled_":               {Fiber: "recycled_"},
		"merino_wool":             {Fiber: "merino_wool"},
	}
	for in, want := range cases {
		if got := obx.NormalizeFiber(obx.Fiber{Fiber: in}); got != want {
			t.Errorf("%s: got %+v, want %+v", in, got, want)
		}
	}
	m := obx.Materials{{Part: "outer", Fibers: []obx.Fiber{{Fiber: "recycled_polyester", Percentage: 100}}}}
	n := m.Normalized()
	if m[0].Fibers[0].Fiber != "recycled_polyester" {
		t.Fatal("Normalized changed its receiver")
	}
	b, _ := json.Marshal(n)
	if string(b) != `[{"part":"outer","fibers":[{"fiber":"polyester","percentage":100,"recycled":true}]}]` {
		t.Fatalf("normalised materials: %s", b)
	}
}

// The fibre flags are present only when true: false is not written.
func TestFiberFlagsOnlyWhenTrue(t *testing.T) {
	var f obx.Fiber
	if err := json.Unmarshal([]byte(`{"fiber":"cotton","percentage":100,"organic":false,"recycled":false}`), &f); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	if string(b) != `{"fiber":"cotton","percentage":100}` {
		t.Fatalf("got %s", b)
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
		p := obx.MaterialPart{Part: "main"}
		for i := 0; i < len(s); i += 2 {
			p.Fibers = append(p.Fibers, obx.Fiber{Fiber: s[i].(string), Percentage: float64(s[i+1].(int))})
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
