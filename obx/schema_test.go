package obx_test

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

// newFor maps a schema name to a fresh Go value of the matching type.
var newFor = map[string]func() any{
	"AccessRequest":       func() any { return new(obx.AccessRequest) },
	"AccessRequestInput":  func() any { return new(obx.AccessRequestInput) },
	"AssetInput":          func() any { return new(obx.AssetInput) },
	"Blob":                func() any { return new(obx.Blob) },
	"BrandInput":          func() any { return new(obx.BrandInput) },
	"ChangesPage":         func() any { return new(obx.ChangesPage) },
	"Directory":           func() any { return new(obx.Directory) },
	"DiscoveryDocument":   func() any { return new(obx.DiscoveryDocument) },
	"Export":              func() any { return new(obx.Export) },
	"ExportRequest":       func() any { return new(obx.ExportRequest) },
	"Grant":               func() any { return new(obx.Grant) },
	"ImportRowError":      func() any { return new(obx.ImportRowError) },
	"ImportRun":           func() any { return new(obx.ImportRun) },
	"ImportSource":        func() any { return new(obx.ImportSource) },
	"IngestInfo":          func() any { return new(obx.IngestInfo) },
	"IngestJob":           func() any { return new(obx.IngestJob) },
	"IngestOperation":     func() any { return new(obx.IngestOperation) },
	"IngestRecord":        func() any { return new(obx.IngestRecord) },
	"IngestResult":        func() any { return new(obx.IngestResult) },
	"Problem":             func() any { return new(obx.Problem) },
	"ProductInput":        func() any { return new(obx.ProductInput) },
	"TokenResponse":       func() any { return new(obx.TokenResponse) },
	"Upload":              func() any { return new(obx.Upload) },
	"Usage":               func() any { return new(obx.Usage) },
	"VariantInput":        func() any { return new(obx.VariantInput) },
	"WebhookEvent":        func() any { return new(obx.WebhookEvent) },
	"WebhookSubscription": func() any { return new(obx.WebhookSubscription) },
	"ExportManifestLine":  func() any { return new(obx.ExportManifestLine) },
}

// TestExamplesRoundTrip decodes every spec example into its Go type, encodes
// it again and checks that the result is valid against the schema and still
// carries every member of the example with the same value.
func TestExamplesRoundTrip(t *testing.T) {
	v, err := specdata.DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	fix := specdata.Fixtures()
	entries, err := fs.ReadDir(fix, "examples")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		name := e.Name()
		schema := strings.SplitN(name, ".", 2)[0]
		mk, ok := newFor[schema]
		if !ok {
			continue
		}
		raw, err := fs.ReadFile(fix, "examples/"+name)
		if err != nil {
			t.Fatal(err)
		}
		docs := [][]byte{raw}
		if strings.HasSuffix(name, ".ndjson") {
			docs = nil
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				docs = append(docs, []byte(line))
			}
		}
		for _, doc := range docs {
			seen++
			val := mk()
			if err := json.Unmarshal(doc, val); err != nil {
				t.Errorf("%s: decode: %v", name, err)
				continue
			}
			out, err := json.Marshal(val)
			if err != nil {
				t.Errorf("%s: encode: %v", name, err)
				continue
			}
			if err := v.Validate(schema, out); err != nil {
				t.Errorf("%s: re-encoded document invalid: %v\n%s", name, err, out)
			}
			var want, got any
			_ = json.Unmarshal(doc, &want)
			_ = json.Unmarshal(out, &got)
			if path, ok := superset(got, want, ""); !ok {
				t.Errorf("%s: re-encoded document lost or changed %s\n%s", name, path, out)
			}
		}
	}
	if seen < 25 {
		t.Fatalf("only %d examples checked", seen)
	}
}

func superset(got, want any, path string) (string, bool) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return path, false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				if wv == nil {
					// An input member sent as null equals the member left
					// out (ingest.md: omitted optional fields become null).
					continue
				}
				return path + "/" + k, false
			}
			if p, ok := superset(gv, wv, path+"/"+k); !ok {
				return p, false
			}
		}
		return "", true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return path, false
		}
		for i := range w {
			if p, ok := superset(g[i], w[i], path); !ok {
				return p, false
			}
		}
		return "", true
	default:
		return path, reflect.DeepEqual(got, want)
	}
}

func TestProblemCodes(t *testing.T) {
	p := obx.NewProblem(obx.ErrCursorExpired, "restart")
	if p.Status != 410 || p.Type != "https://openbrandexchange.com/errors/cursor_expired" || !obx.IsCode(p, obx.ErrCursorExpired) {
		t.Fatalf("unexpected problem %+v", p)
	}
	v, _ := specdata.DefaultValidator()
	if err := v.ValidateValue("Problem", p); err != nil {
		t.Fatal(err)
	}
}
