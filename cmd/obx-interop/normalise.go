package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"
)

// volatile keys differ per node or per run and are replaced by placeholders.
var volatile = map[string]string{
	"seq":            "{{seq}}",
	"updated_at":     "{{datetime}}",
	"created_at":     "{{datetime}}",
	"url_expires_at": "{{datetime}}",
	"url":            "{{url}}",
	"etag":           "{{etag}}",
	"next_cursor":    "{{cursor}}",
	"last_seen_at":   "{{datetime}}",
	"published_at":   "{{datetime}}",
}

// normalise returns v with node ids replaced by labels and volatile values
// by placeholders. A key that is present with null keeps null, so "null
// versus left out" differences stay visible.
func normalise(v any, labels map[string]string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if ph, ok := volatile[k]; ok && val != nil {
				out[k] = ph
				continue
			}
			out[k] = normalise(val, labels)
			if k == "renditions" || k == "files" {
				blankDerived(out[k])
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalise(e, labels)
		}
		return out
	case string:
		if l, ok := labels[x]; ok {
			return l
		}
		return x
	}
	return v
}

// blankDerived hides the bytes and sha256 of web, square and thumb: each
// node makes them with its own image library (docs/protocol.md: renditions
// are node-generated JPEGs), so only their dimensions must agree. Originals
// are compared byte for byte.
func blankDerived(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	for name, r := range m {
		if name == "original" {
			continue
		}
		if rm, ok := r.(map[string]any); ok {
			for _, k := range []string{"sha256", "bytes"} {
				if _, ok := rm[k]; ok {
					rm[k] = "{{derived}}"
				}
			}
		}
	}
}

// normaliseMirror turns an `obx pull` folder into one comparable document:
// object files keyed by type and external id instead of node id, and every
// downloaded image by its path and sha256.
func normaliseMirror(fsys fs.FS) (map[string]any, error) {
	labels := map[string]string{}
	objects := map[string]map[string]any{}
	for _, dir := range []string{"brands", "products", "variants", "assets"} {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return nil, fmt.Errorf("%s/%s: %w", dir, e.Name(), err)
			}
			typ := strings.TrimSuffix(dir, "s")
			ext, _ := m["external_id"].(string)
			id := strings.TrimSuffix(e.Name(), ".json")
			labels[id] = typ + ":" + ext
			if objects[dir] == nil {
				objects[dir] = map[string]any{}
			}
			objects[dir][id] = m
		}
	}
	out := map[string]any{}
	for dir, objs := range objects {
		byLabel := map[string]any{}
		for id, m := range objs {
			byLabel[labels[id]] = normalise(m, labels)
		}
		out[dir] = byLabel
	}
	images := map[string]any{}
	fs.WalkDir(fsys, "images", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		images[p] = hex.EncodeToString(sum[:])
		if parts := strings.Split(p, "/"); len(parts) == 4 && parts[2] != "original" {
			// web, square and thumb come from each node's own image library
			images[p] = "{{derived}}"
		}
		return nil
	})
	out["images"] = images
	return out, nil
}

// diff lists every difference between a and b as "path: a-value ≠ b-value".
// Keys starting with "_" are informational and skipped.
func diff(a, b any, at string) []string {
	if reflect.DeepEqual(a, b) {
		return nil
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		var sorted []string
		for k := range keys {
			if !strings.HasPrefix(k, "_") {
				sorted = append(sorted, k)
			}
		}
		sort.Strings(sorted)
		var out []string
		for _, k := range sorted {
			va, oka := x[k]
			vb, okb := y[k]
			p := at + "/" + k
			switch {
			case !oka:
				out = append(out, fmt.Sprintf("%s: missing ≠ %s", p, short(vb)))
			case !okb:
				out = append(out, fmt.Sprintf("%s: %s ≠ missing", p, short(va)))
			default:
				out = append(out, diff(va, vb, p)...)
			}
		}
		return out
	case []any:
		y, ok := b.([]any)
		if !ok {
			break
		}
		var out []string
		n := len(x)
		if len(y) > n {
			n = len(y)
		}
		for i := 0; i < n; i++ {
			p := fmt.Sprintf("%s[%d]", at, i)
			switch {
			case i >= len(x):
				out = append(out, fmt.Sprintf("%s: missing ≠ %s", p, short(y[i])))
			case i >= len(y):
				out = append(out, fmt.Sprintf("%s: %s ≠ missing", p, short(x[i])))
			default:
				out = append(out, diff(x[i], y[i], p)...)
			}
		}
		return out
	}
	if at == "" {
		at = "/"
	}
	return []string{fmt.Sprintf("%s: %s ≠ %s", at, short(a), short(b))}
}

func short(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 160 {
		return string(b[:157]) + "…"
	}
	return string(b)
}
