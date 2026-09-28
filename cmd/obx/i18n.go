package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The CLI speaks the 25 languages of the admin UI. English is the source:
// every message is registered in Go (see messages_*.go) with an id and its
// English text; cmd/obx/locales/<lang>.json holds the translations. Texts use
// {name} placeholders; plural forms are avoided ("images: 3") so one text
// fits every number in every language. Wording follows the project glossary
// (spec/docs/glossary.md): "access key", "automatic upload", "EAN", "API key
// for your software".

//go:embed locales/*.json
var localeFS embed.FS

// languages are the supported languages, as in the admin UI.
var languages = []string{"bg", "cs", "da", "de", "el", "en", "es", "et", "fi", "fr", "ga", "hr", "hu", "it", "lt", "lv", "mt", "nb", "nl", "pl", "pt", "ro", "sk", "sl", "sv"}

var (
	messagesMu sync.Mutex
	messages   = map[string]string{}
)

// register adds English source texts; ids must be unique.
func register(m map[string]string) bool {
	messagesMu.Lock()
	defer messagesMu.Unlock()
	for id, text := range m {
		if _, dup := messages[id]; dup {
			panic("obx: message id registered twice: " + id)
		}
		messages[id] = text
	}
	return true
}

// translator renders messages in one language.
type translator struct {
	lang  string
	table map[string]string
}

var (
	tableMu sync.Mutex
	tables  = map[string]map[string]string{}
)

func loadTable(lang string) map[string]string {
	tableMu.Lock()
	defer tableMu.Unlock()
	if t, ok := tables[lang]; ok {
		return t
	}
	t := map[string]string{}
	if lang != "en" {
		if b, err := localeFS.ReadFile("locales/" + lang + ".json"); err == nil {
			json.Unmarshal(b, &t)
		}
	}
	tables[lang] = t
	return t
}

func newTranslator(lang string) *translator {
	if !supported(lang) {
		lang = "en"
	}
	return &translator{lang: lang, table: loadTable(lang)}
}

func supported(lang string) bool {
	for _, l := range languages {
		if l == lang {
			return true
		}
	}
	return false
}

// t returns message id in the translator's language (English when the
// language has no text for it) with {name} placeholders filled from kv
// pairs: t("push.publishing", "portal", base).
func (tr *translator) t(id string, kv ...any) string {
	s, ok := tr.table[id]
	if !ok || s == "" {
		messagesMu.Lock()
		s, ok = messages[id]
		messagesMu.Unlock()
	}
	if !ok {
		return "⟦" + id + "⟧" // tests fail on this marker
	}
	if len(kv) == 0 {
		return s
	}
	pairs := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, "{"+fmt.Sprint(kv[i])+"}", fmt.Sprint(kv[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// detectLanguage picks the language: OBX_LANG, then the POSIX locale
// variables, then the operating system's preferred languages, else English.
func detectLanguage(getenv func(string) string) string {
	candidates := []string{getenv("OBX_LANG"), getenv("LC_ALL"), getenv("LC_MESSAGES"), getenv("LANG")}
	candidates = append(candidates, strings.Split(getenv("LANGUAGE"), ":")...)
	for _, c := range candidates {
		if l := normaliseLanguage(c); l != "" {
			return l
		}
	}
	for _, c := range systemLanguages() {
		if l := normaliseLanguage(c); l != "" {
			return l
		}
	}
	return "en"
}

// normaliseLanguage turns "nl_NL.UTF-8", "de-AT" or "nn" into a supported
// language, or "" when it is none ("C", "POSIX", "ja_JP").
func normaliseLanguage(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexAny(s, "_-"); i >= 0 {
		s = s[:i]
	}
	switch s {
	case "", "c", "posix":
		return ""
	case "no", "nn":
		return "nb"
	}
	if supported(s) {
		return s
	}
	return ""
}

// messageIDs lists every registered id, sorted (for the locale tests).
func messageIDs() []string {
	messagesMu.Lock()
	defer messagesMu.Unlock()
	ids := make([]string, 0, len(messages))
	for id := range messages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
