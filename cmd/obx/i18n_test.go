package main

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var placeholderRE = regexp.MustCompile(`\{[a-z_]+\}`)

func placeholders(s string) string {
	p := placeholderRE.FindAllString(s, -1)
	sort.Strings(p)
	return strings.Join(p, " ")
}

// Every language has every message, with the same {placeholders}, the
// commands and options untranslated, and no protocol words (glossary).
func TestLocalesComplete(t *testing.T) {
	ids := messageIDs()
	if len(ids) < 100 {
		t.Fatalf("only %d messages registered", len(ids))
	}
	for _, lang := range languages {
		if lang == "en" {
			continue
		}
		b, err := localeFS.ReadFile("locales/" + lang + ".json")
		if err != nil {
			t.Errorf("%s: no locale file: %v", lang, err)
			continue
		}
		var table map[string]string
		if err := json.Unmarshal(b, &table); err != nil {
			t.Errorf("%s: %v", lang, err)
			continue
		}
		for _, id := range ids {
			text, ok := table[id]
			switch {
			case !ok:
				t.Errorf("%s: missing %s", lang, id)
				continue
			case strings.TrimSpace(text) == "":
				t.Errorf("%s: %s is empty", lang, id)
				continue
			}
			en := messages[id]
			if placeholders(text) != placeholders(en) {
				t.Errorf("%s: %s has placeholders %q, English has %q", lang, id, placeholders(text), placeholders(en))
			}
			for _, keep := range []string{"\"obx ", "--", "OBX_", "EAN", "products.csv", "assets.csv", "Ctrl-C", "obxi_"} {
				if strings.Count(en, keep) > 0 && !strings.Contains(text, keep) {
					t.Errorf("%s: %s lost %q: %s", lang, id, keep, text)
				}
			}
		}
		for id := range table {
			if _, ok := messages[id]; !ok {
				t.Errorf("%s: unknown message %s (remove it)", lang, id)
			}
		}
	}
}

// The glossary: people never read protocol terms (GTIN, ingest, node, token).
func TestGlossaryWords(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(gtin|ingest|node|nodes|tokens?|upload key)\b`)
	check := func(lang, id, text string) {
		clean := strings.NewReplacer("OBX_TOKEN", "", "--token", "", "obxi_", "").Replace(text)
		if m := banned.FindString(clean); m != "" {
			t.Errorf("%s: %s uses the protocol word %q: %s", lang, id, m, text)
		}
	}
	for id, text := range messages {
		check("en", id, text)
	}
	for _, lang := range languages {
		if lang == "en" {
			continue
		}
		for id, text := range loadTable(lang) {
			check(lang, id, text)
		}
	}
}
