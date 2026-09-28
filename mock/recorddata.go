package mock

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"sync"

	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

var (
	inputPropsOnce sync.Once
	inputProps     map[string][]string // kind → nullable top-level properties of <Kind>Input
)

// nullableInputProps lists the properties of <Kind>Input.json that may be
// null. The IngestRecord shows each of them, as null when the brand did
// not send it, like every node does.
func nullableInputProps(kind string) []string {
	inputPropsOnce.Do(func() {
		inputProps = map[string][]string{}
		for _, k := range []string{obx.KindBrand, obx.KindProduct, obx.KindVariant, obx.KindAsset} {
			name := strings.ToUpper(k[:1]) + k[1:] + "Input.json"
			b, err := fs.ReadFile(specdata.FS(), "schemas/"+name)
			if err != nil {
				continue
			}
			var s struct {
				Properties map[string]struct {
					Type  any `json:"type"`
					AnyOf []struct {
						Type any `json:"type"`
					} `json:"anyOf"`
					OneOf []struct {
						Type any `json:"type"`
					} `json:"oneOf"`
				} `json:"properties"`
			}
			if json.Unmarshal(b, &s) != nil {
				continue
			}
			for p, def := range s.Properties {
				types := []any{def.Type}
				for _, alt := range append(def.AnyOf, def.OneOf...) {
					types = append(types, alt.Type)
				}
				if allowsNull(types) {
					inputProps[k] = append(inputProps[k], p)
				}
			}
		}
	})
	return inputProps[kind]
}

// recordData is IngestRecord.data: the stored input with defaults applied.
// For assets that is the effective rights, the provenance with its
// defaults and every current file with its dimensions.
func (s *state) recordData(rec *record) json.RawMessage {
	return s.withDefaults(rec, false)
}

// sameData reports whether a new version of a record reads the same as
// the existing one once defaults are applied, so that PUTting back what a
// GET returned (rights, provenance and every current file spelled out) is
// "unchanged". The files sent must all be current files of the asset.
func (s *state) sameData(existing, nr *record) bool {
	if !bytes.Equal(s.withDefaults(existing, true), s.withDefaults(nr, true)) {
		return false
	}
	if nr.kind != obx.KindAsset {
		return true
	}
	var sent *obx.AssetFiles
	if nr.asset != nil {
		sent = nr.asset.Files
	}
	for _, name := range obx.RenditionNames {
		f := sent.Get(name)
		cur, ok := existing.files[name]
		switch {
		case f == nil && name == obx.RenditionOriginal:
			return false
		case f != nil && (!ok || cur.SHA256 != f.SHA256):
			return false
		}
	}
	return true
}

// withDefaults is the record data with defaults applied; without files
// when noFiles is set (sameData compares those separately).
func (s *state) withDefaults(rec *record, noFiles bool) json.RawMessage {
	m := map[string]any{}
	if json.Unmarshal(rec.input, &m) != nil {
		return rec.input
	}
	for _, p := range nullableInputProps(rec.kind) {
		if _, ok := m[p]; !ok {
			m[p] = nil
		}
	}
	m["status"] = rec.status
	if rec.kind == obx.KindAsset {
		if _, ok := m["media_type"]; !ok {
			m["media_type"] = "image"
		}
		m["rights"] = s.effectiveRights(rec)
		var prov *obx.ProvenanceInput
		if rec.asset != nil {
			prov = rec.asset.Provenance
		}
		m["provenance"] = effectiveProvenance(prov)
		delete(m, "files")
		if !noFiles {
			files := map[string]fileRef{}
			for n, f := range rec.files {
				files[n] = f
			}
			m["files"] = files
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return rec.input
	}
	return b
}

func allowsNull(types []any) bool {
	for _, t := range types {
		switch x := t.(type) {
		case string:
			if x == "null" {
				return true
			}
		case []any:
			if allowsNull(x) {
				return true
			}
		}
	}
	return false
}
