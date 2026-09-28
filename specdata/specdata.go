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

//go:embed schemas/*.json fixtures SPEC_COMMIT
var files embed.FS

// FS returns the embedded tree: schemas/*.json and fixtures/{kestrel,expected,ingest,examples}.
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

// SchemaBase is the $id prefix of every schema.
const SchemaBase = "https://openbrandexchange.com/schemas/v0.1/"

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

// DefaultValidator returns a validator over the embedded schemas.
func DefaultValidator() (*Validator, error) {
	defaultValidatorOnce.Do(func() {
		defaultValidator, defaultValidatorErr = NewValidator(files, "schemas")
	})
	return defaultValidator, defaultValidatorErr
}

// NewValidator loads every *.json schema in dir of fsys.
func NewValidator(fsys fs.FS, dir string) (*Validator, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := c.AddResource(SchemaBase+e.Name(), doc); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return &Validator{compiler: c, cache: map[string]*jsonschema.Schema{}}, nil
}

// Schema returns the compiled schema by file name ("Asset.json" or "Asset"),
// optionally with a JSON pointer fragment ("Common.json#/$defs/Id").
func (v *Validator) Schema(name string) (*jsonschema.Schema, error) {
	if !strings.Contains(name, ".json") {
		name += ".json"
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.cache[name]; ok {
		return s, nil
	}
	s, err := v.compiler.Compile(SchemaBase + name)
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
