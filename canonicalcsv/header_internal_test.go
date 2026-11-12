package canonicalcsv

import "testing"

// import-format.md §1.1: trimmed (space, tab, CR, LF), case ignored, each run
// of white space and "-" is one "_"; an empty header names no column.
func TestHeaderNormalisation(t *testing.T) {
	for raw, want := range map[string]string{
		"Style Number":      "style_number",
		"  Style - Number ": "style_number",
		"Style\r\nNumber":   "style_number",
		"style\t-\tnumber":  "style_number",
		"Colour--Code":      "colour_code",
		"\ufeffbrand":       "brand",
		" Name : NL-be ":    "name",
		"weight_kg":         "weight_kg",
		"\u00a0brand":       "\u00a0brand",
	} {
		if got := normalizeHeader(raw).base; got != want {
			t.Errorf("normalizeHeader(%q) = %q, want %q", raw, got, want)
		}
	}
	if c := normalizeHeader(" Name : NL-be "); c.lang != "nl-BE" {
		t.Errorf("language of %q: %q", " Name : NL-be ", c.lang)
	}
	for _, raw := range []string{"", " ", "\t\r\n", "\ufeff "} {
		if !emptyHeader(raw) {
			t.Errorf("emptyHeader(%q) = false", raw)
		}
	}
}
