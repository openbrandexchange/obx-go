package specdata_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/specdata"
)

// Every schema compiles at its own $id: the 0.2 schemas at /schemas/v0.2/ and
// the frozen 0.1 copies at /schemas/v0.1/, each resolving its relative $refs
// within its version.
func TestSchemasOfBothVersionsCompile(t *testing.T) {
	v, err := specdata.DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"schemas", "schemas/v0.1"} {
		entries, err := fs.ReadDir(specdata.FS(), dir)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimPrefix(strings.TrimPrefix(dir, "schemas"), "/")
			if name != "" {
				name += "/"
			}
			if _, err := v.Schema(name + e.Name()); err != nil {
				t.Errorf("%s/%s: %v", dir, e.Name(), err)
			}
			n++
		}
		if n < 40 {
			t.Errorf("%s: only %d schemas", dir, n)
		}
	}
	// The 0.2 Variant knows measurements; the 0.1 Variant does not.
	doc := []byte(`{"height_cm": 0}`)
	if err := v.Validate("Measurements", doc); err == nil {
		t.Error("Measurements accepts a height of 0")
	}
	if _, err := v.Schema("v0.1/Measurements.json"); err == nil {
		t.Error("0.1 has no Measurements schema")
	}
}
