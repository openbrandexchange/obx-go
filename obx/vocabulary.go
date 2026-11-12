package obx

import (
	"encoding/json"
	"fmt"
)

// Vocabulary holds the codes of the specification's vocabularies/*.json
// with their names (docs/protocol.md 5.9.1): parts, materials and qualifiers
// of materials, and care codes with their category and symbol. The embedded
// copy of the spec's vocabularies is specdata.Vocabulary().
type Vocabulary struct {
	Parts      map[string]LocalizedText
	Materials  map[string]LocalizedText
	Qualifiers map[string]LocalizedText
	Care       map[string]CareInstruction
	// Order lists the codes of each vocabulary in file order, keyed by
	// "parts", "materials", "qualifiers" and "care".
	Order map[string][]string
}

type vocabularyFile struct {
	Entries []struct {
		Code     string        `json:"code"`
		Category *string       `json:"category"`
		Symbol   *string       `json:"symbol"`
		Name     LocalizedText `json:"name"`
	} `json:"entries"`
}

// ParseVocabulary reads the four vocabulary files (parts.json,
// materials.json, qualifiers.json, care.json).
func ParseVocabulary(parts, materials, qualifiers, care []byte) (*Vocabulary, error) {
	v := &Vocabulary{Parts: map[string]LocalizedText{}, Materials: map[string]LocalizedText{},
		Qualifiers: map[string]LocalizedText{}, Care: map[string]CareInstruction{}, Order: map[string][]string{}}
	for _, f := range []struct {
		name string
		raw  []byte
	}{{"parts", parts}, {"materials", materials}, {"qualifiers", qualifiers}, {"care", care}} {
		var doc vocabularyFile
		if err := json.Unmarshal(f.raw, &doc); err != nil {
			return nil, fmt.Errorf("vocabularies/%s.json: %w", f.name, err)
		}
		for _, e := range doc.Entries {
			if !IsKeyCode(e.Code) {
				return nil, fmt.Errorf("vocabularies/%s.json: code %q is not a key", f.name, e.Code)
			}
			v.Order[f.name] = append(v.Order[f.name], e.Code)
			name := cleanText(e.Name)
			switch f.name {
			case "parts":
				v.Parts[e.Code] = name
			case "materials":
				v.Materials[e.Code] = name
			case "qualifiers":
				v.Qualifiers[e.Code] = name
			case "care":
				v.Care[e.Code] = CareInstruction{Code: e.Code, Category: e.Category, Symbol: e.Symbol, Name: name}
			}
		}
	}
	return v, nil
}

// PartName returns the name of a part code and whether the code is known.
// The part "main" is known and has no name.
func (v *Vocabulary) PartName(code string) (LocalizedText, bool) {
	if v == nil {
		return nil, false
	}
	n, ok := v.Parts[code]
	return n, ok
}

// MaterialName returns the name of a material code and whether it is known.
func (v *Vocabulary) MaterialName(code string) (LocalizedText, bool) {
	if v == nil {
		return nil, false
	}
	n, ok := v.Materials[code]
	return n, ok
}

// QualifierName returns the name of a qualifier code and whether it is known.
func (v *Vocabulary) QualifierName(code string) (LocalizedText, bool) {
	if v == nil {
		return nil, false
	}
	n, ok := v.Qualifiers[code]
	return n, ok
}

// CareCode returns the vocabulary entry of a care code (Code, Category,
// Symbol, Name; Text is nil) and whether the code is known.
func (v *Vocabulary) CareCode(code string) (CareInstruction, bool) {
	if v == nil {
		return CareInstruction{}, false
	}
	c, ok := v.Care[code]
	return c, ok
}

// IsKeyCode reports whether s matches the code pattern ^[a-z][a-z0-9_]{0,63}$.
func IsKeyCode(s string) bool {
	if len(s) == 0 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// cleanText drops empty values; an empty result is nil (a language without
// a name is omitted, never an empty string).
func cleanText(t LocalizedText) LocalizedText {
	if t == nil {
		return nil
	}
	var out LocalizedText
	for k, v := range t {
		if v == "" {
			continue
		}
		if out == nil {
			out = LocalizedText{}
		}
		out[k] = v
	}
	return out
}

func cloneText(t LocalizedText) LocalizedText {
	if t == nil {
		return nil
	}
	out := make(LocalizedText, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}

// term returns the term with the vocabulary's name for a known code and
// otherwise the name it was given (without empty strings).
func term(t Term, lookup func(string) (LocalizedText, bool)) Term {
	if n, ok := lookup(t.Code); ok {
		return Term{Code: t.Code, Name: cloneText(n)}
	}
	return Term{Code: t.Code, Name: cleanText(t.Name)}
}

// WithNames returns a copy of m as a node stores and returns it: every fibre
// normalised (NormalizeFiber), and for every part, material and qualifier
// the vocabulary's name when its code is known, otherwise the name that was
// written (empty strings dropped, nil when there is none). v may be nil,
// which only normalises and cleans.
func (m Materials) WithNames(v *Vocabulary) Materials {
	if m == nil {
		return nil
	}
	out := make(Materials, len(m))
	for i, p := range m {
		q := MaterialPart{Part: term(p.Part, v.PartName)}
		if p.Fibers != nil {
			q.Fibers = make([]Fiber, len(p.Fibers))
			for j, f := range p.Fibers {
				f = NormalizeFiber(f)
				g := Fiber{Material: term(f.Material, v.MaterialName), Percentage: f.Percentage}
				if f.Qualifier != nil {
					qt := term(*f.Qualifier, v.QualifierName)
					g.Qualifier = &qt
				}
				q.Fibers[j] = g
			}
		}
		out[i] = q
	}
	return out
}

// WithNames returns a copy of c as a node stores and returns it: for a
// code in the care vocabulary its category, symbol and name, otherwise what
// was written. Text is kept (empty strings dropped). v may be nil.
func (c Care) WithNames(v *Vocabulary) Care {
	if c == nil {
		return nil
	}
	out := make(Care, len(c))
	for i, e := range c {
		n := CareInstruction{Code: e.Code, Category: e.Category, Symbol: e.Symbol, Name: cleanText(e.Name), Text: cleanText(e.Text)}
		if k, ok := v.CareCode(e.Code); ok {
			n.Category, n.Symbol, n.Name = k.Category, k.Symbol, cloneText(k.Name)
		}
		out[i] = n
	}
	return out
}
