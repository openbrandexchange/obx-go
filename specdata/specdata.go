// Package specdata embeds the JSON Schemas and fixtures of the OBX
// specification (https://github.com/openbrandexchange/spec, CC-BY-4.0) so the
// SDK, the mock node and the conformance suite work without a spec checkout.
//
// Refresh with scripts/sync-spec.sh. SPEC_COMMIT records the spec revision.
package specdata

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/*.json schemas/v0.1/*.json fixtures SPEC_COMMIT
var files embed.FS

// FS returns the embedded tree: schemas/*.json (0.2), schemas/v0.1/*.json
// (the frozen 0.1 schemas) and fixtures/{kestrel,expected,ingest,examples,imports}.
func FS() fs.FS { return files }

// Fixtures returns the fixtures directory as an fs.FS.
func Fixtures() fs.FS {
	sub, err := fs.Sub(files, "fixtures")
	if err != nil {
		panic(err)
	}
	return sub
}

// SpecCommit is the spec repository commit the data was copied from.
func SpecCommit() string {
	b, _ := files.ReadFile("SPEC_COMMIT")
	return strings.TrimSpace(string(b))
}

// SchemaBase is the $id prefix of the current (0.2) schemas.
const SchemaBase = "https://openbrandexchange.com/schemas/v0.2/"

// SchemaBaseV01 is the $id prefix of the frozen 0.1 schemas, which the spec
// keeps unchanged so their $ids keep resolving.
const SchemaBaseV01 = "https://openbrandexchange.com/schemas/v0.1/"

// Validator validates JSON documents against the OBX schemas.
type Validator struct {
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	cache    map[string]*jsonschema.Schema
}

var (
	defaultValidator     *Validator
	defaultValidatorErr  error
	defaultValidatorOnce sync.Once
)

// DefaultValidator returns a validator over the embedded schemas: the 0.2
// schemas at their /schemas/v0.2/ $ids and the 0.1 schemas at /schemas/v0.1/.
func DefaultValidator() (*Validator, error) {
	defaultValidatorOnce.Do(func() {
		defaultValidator, defaultValidatorErr = NewValidator(files, "schemas")
	})
	return defaultValidator, defaultValidatorErr
}

// NewValidator loads every *.json schema in dir of fsys, and in its
// subdirectory v0.1 when there is one. Each schema is registered under its
// own $id (a schema without one under SchemaBase plus its path below dir),
// so relative $refs resolve within its version.
func NewValidator(fsys fs.FS, dir string) (*Validator, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	add := func(sub, base string) error {
		entries, err := fs.ReadDir(fsys, path.Join(dir, sub))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := fs.ReadFile(fsys, path.Join(dir, sub, e.Name()))
			if err != nil {
				return err
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err != nil {
				return fmt.Errorf("%s: %w", path.Join(sub, e.Name()), err)
			}
			id := base + e.Name()
			if m, ok := doc.(map[string]any); ok {
				if s, ok := m["$id"].(string); ok && s != "" {
					id = s
				}
			}
			if err := c.AddResource(id, doc); err != nil {
				return fmt.Errorf("%s: %w", path.Join(sub, e.Name()), err)
			}
		}
		return nil
	}
	if err := add("", SchemaBase); err != nil {
		return nil, err
	}
	if _, err := fs.Stat(fsys, path.Join(dir, "v0.1")); err == nil {
		if err := add("v0.1", SchemaBaseV01); err != nil {
			return nil, err
		}
	}
	return &Validator{compiler: c, cache: map[string]*jsonschema.Schema{}}, nil
}

// Schema returns the compiled schema by file name ("Asset.json" or "Asset"),
// optionally with a JSON pointer fragment ("Common.json#/$defs/Id"). Names
// are the 0.2 schemas; "v0.1/Asset.json" names a frozen 0.1 schema.
func (v *Validator) Schema(name string) (*jsonschema.Schema, error) {
	if !strings.Contains(name, ".json") {
		name += ".json"
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.cache[name]; ok {
		return s, nil
	}
	url := SchemaBase + name
	if rest, ok := strings.CutPrefix(name, "v0.1/"); ok {
		url = SchemaBaseV01 + rest
	}
	s, err := v.compiler.Compile(url)
	if err != nil {
		return nil, err
	}
	v.cache[name] = s
	return s, nil
}

// Validate checks raw JSON against the named schema.
func (v *Validator) Validate(name string, raw []byte) error {
	s, err := v.Schema(name)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := s.Validate(doc); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// ValidateValue marshals v and validates it.
func (v *Validator) ValidateValue(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return v.Validate(name, raw)
}
