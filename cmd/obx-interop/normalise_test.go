package main

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestNormaliseReplacesIDsAndVolatileValues(t *testing.T) {
	labels := map[string]string{"01HX": "product:KO-1001"}
	in := map[string]any{
		"id": "01HX", "seq": 42.0, "updated_at": "2026-09-28T10:00:00Z", "embargo_until": "2030-01-01T00:00:00Z",
		"renditions": map[string]any{
			"original": map[string]any{"sha256": "aa", "bytes": 10.0, "url": "https://x/1?sig=1"},
			"web":      map[string]any{"sha256": "bb", "bytes": 5.0, "width": 1200.0},
		},
		"deleted_at": nil,
	}
	out := normalise(in, labels).(map[string]any)
	if out["id"] != "product:KO-1001" || out["seq"] != "{{seq}}" || out["updated_at"] != "{{datetime}}" {
		t.Fatalf("ids and volatile keys: %v", out)
	}
	if out["embargo_until"] != "2030-01-01T00:00:00Z" {
		t.Errorf("data dates must stay: %v", out["embargo_until"])
	}
	r := out["renditions"].(map[string]any)
	if o := r["original"].(map[string]any); o["sha256"] != "aa" || o["bytes"] != 10.0 || o["url"] != "{{url}}" {
		t.Errorf("originals are compared byte for byte: %v", o)
	}
	if w := r["web"].(map[string]any); w["sha256"] != "{{derived}}" || w["width"] != 1200.0 {
		t.Errorf("derived renditions keep only their dimensions: %v", w)
	}
	if _, ok := out["deleted_at"]; !ok {
		t.Error("an explicit null must stay visible")
	}
}

func TestDiffReportsNullVersusMissingAndOrder(t *testing.T) {
	a := map[string]any{"x": nil, "list": []any{"a", "b"}, "_info": 1.0}
	b := map[string]any{"list": []any{"b", "a"}, "_info": 2.0}
	d := diff(a, b, "")
	got := strings.Join(d, "\n")
	if !strings.Contains(got, "/x: null ≠ missing") || !strings.Contains(got, "/list[0]") {
		t.Fatalf("diff: %s", got)
	}
	if strings.Contains(got, "_info") {
		t.Errorf("keys starting with _ are informational: %s", got)
	}
	if len(diff(a, a, "")) != 0 {
		t.Error("identical documents must not differ")
	}
}

func TestMirrorUsesExternalIDs(t *testing.T) {
	fsys := fstest.MapFS{
		"products/01A.json":                   {Data: []byte(`{"id":"01A","external_id":"KO-1001","brand_id":"01B"}`)},
		"brands/01B.json":                     {Data: []byte(`{"id":"01B","external_id":"kestrel"}`)},
		"images/KO-1001/original/KO-1001.png": {Data: []byte("png")},
		"images/KO-1001/web/KO-1001.jpg":      {Data: []byte("jpg")},
	}
	m, err := normaliseMirror(fsys)
	if err != nil {
		t.Fatal(err)
	}
	p := m["products"].(map[string]any)["product:KO-1001"].(map[string]any)
	if p["brand_id"] != "brand:kestrel" {
		t.Errorf("ids must become labels: %v", p)
	}
	imgs := m["images"].(map[string]any)
	if imgs["images/KO-1001/web/KO-1001.jpg"] != "{{derived}}" || imgs["images/KO-1001/original/KO-1001.png"] == "{{derived}}" {
		t.Errorf("images: %v", imgs)
	}
}
