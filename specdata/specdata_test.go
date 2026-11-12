package specdata_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/specdata"
)

// Every schema compiles at its own $id (/schemas/v0.3/), resolving its
// relative $refs.
func TestSchemasCompile(t *testing.T) {
	v, err := specdata.DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(specdata.FS(), "schemas")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if _, err := v.Schema(e.Name()); err != nil {
			t.Errorf("schemas/%s: %v", e.Name(), err)
		}
		n++
	}
	if n < 40 {
		t.Errorf("only %d schemas", n)
	}
	if err := v.Validate("Measurements", []byte(`{"height_cm": 0}`)); err == nil {
		t.Error("Measurements accepts a height of 0")
	}
	// 0.3 materials are terms; the 0.2 shape is rejected.
	if err := v.Validate("Materials", []byte(`[{"part": "main", "fibers": [{"fiber": "cotton", "percentage": 100}]}]`)); err == nil {
		t.Error("Materials accepts the 0.2 shape")
	}
	if err := v.Validate("Materials", []byte(`[{"part": {"code": "main"}, "fibers": [{"material": {"code": "cotton"}, "percentage": 100, "qualifier": {"code": "organic"}}]}]`)); err != nil {
		t.Error(err)
	}
}

func TestVocabulary(t *testing.T) {
	v := specdata.Vocabulary()
	if n, ok := v.PartName("main"); !ok || n != nil {
		t.Errorf("main: %v %v", n, ok)
	}
	if n, ok := v.PartName("lining"); !ok || n["nl"] != "Voering" {
		t.Errorf("lining: %v", n)
	}
	if n, ok := v.MaterialName("cotton"); !ok || n["de"] != "Baumwolle" {
		t.Errorf("cotton: %v", n)
	}
	if n, ok := v.QualifierName("recycled"); !ok || n["en"] != "Recycled" {
		t.Errorf("recycled: %v", n)
	}
	if _, ok := v.MaterialName("recycled_polyester"); ok {
		t.Error("recycled_polyester is not a material")
	}
	c, ok := v.CareCode("wash_30")
	if !ok || *c.Category != "washing" || *c.Symbol != "1_D_30" || c.Name["en"] != "Wash at 30 °C" {
		t.Errorf("wash_30: %+v", c)
	}
	if c, _ := v.CareCode("wash_70"); c.Symbol != nil {
		t.Errorf("wash_70 has no symbol: %v", *c.Symbol)
	}
	for _, k := range []string{"parts", "materials", "qualifiers", "care"} {
		if len(v.Order[k]) == 0 {
			t.Errorf("vocabulary %s is empty", k)
		}
	}
}
