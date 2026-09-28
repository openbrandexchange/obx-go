package conformance

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)
	sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// matchExpected compares an actual JSON value with an expected fixture
// (docs/conformance.md "Fixture conventions"): every expected key must be
// present with an equal value, actual objects may only add keys prefixed
// "x-", arrays match element by element, and placeholders stand for classes
// of values. products resolves {{product:<external_id>}}.
func matchExpected(actual, expected any, path string, products func(string) (any, bool)) []string {
	switch e := expected.(type) {
	case string:
		if strings.HasPrefix(e, "{{") && strings.HasSuffix(e, "}}") {
			return matchPlaceholder(actual, e[2:len(e)-2], path, products)
		}
		if a, ok := actual.(string); !ok || a != e {
			return []string{fmt.Sprintf("%s: got %s, want %q", path, short(actual), e)}
		}
		return nil
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: got %s, want an object", path, short(actual))}
		}
		var out []string
		keys := make([]string, 0, len(e))
		for k := range e {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			av, present := a[k]
			if !present {
				out = append(out, fmt.Sprintf("%s/%s: missing", path, k))
				continue
			}
			out = append(out, matchExpected(av, e[k], path+"/"+k, products)...)
		}
		for k := range a {
			if _, ok := e[k]; !ok && !strings.HasPrefix(k, "x-") {
				out = append(out, fmt.Sprintf("%s/%s: unexpected member (extensions must start with x-)", path, k))
			}
		}
		return out
	case []any:
		a, ok := actual.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: got %s, want an array", path, short(actual))}
		}
		if len(a) != len(e) {
			return []string{fmt.Sprintf("%s: got %d elements, want %d", path, len(a), len(e))}
		}
		var out []string
		for i := range e {
			out = append(out, matchExpected(a[i], e[i], fmt.Sprintf("%s/%d", path, i), products)...)
		}
		return out
	case float64:
		if a, ok := actual.(float64); !ok || a != e {
			return []string{fmt.Sprintf("%s: got %s, want %v", path, short(actual), e)}
		}
		return nil
	case bool:
		if a, ok := actual.(bool); !ok || a != e {
			return []string{fmt.Sprintf("%s: got %s, want %v", path, short(actual), e)}
		}
		return nil
	case nil:
		if actual != nil {
			return []string{fmt.Sprintf("%s: got %s, want null", path, short(actual))}
		}
		return nil
	}
	return []string{fmt.Sprintf("%s: unsupported expected value %v", path, expected)}
}

func matchPlaceholder(actual any, ph, path string, products func(string) (any, bool)) []string {
	fail := func(what string) []string {
		return []string{fmt.Sprintf("%s: got %s, want %s", path, short(actual), what)}
	}
	s, isStr := actual.(string)
	switch {
	case ph == "id":
		if !isStr || !idPattern.MatchString(s) {
			return fail("an id")
		}
	case ph == "datetime":
		if !isStr {
			return fail("a timestamp")
		}
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			return fail("an RFC 3339 timestamp")
		}
	case ph == "datetime|null":
		if actual == nil {
			return nil
		}
		if _, err := time.Parse(time.RFC3339Nano, s); !isStr || err != nil {
			return fail("a timestamp or null")
		}
	case ph == "url":
		u, err := url.Parse(s)
		if !isStr || err != nil || !u.IsAbs() {
			return fail("an absolute URL")
		}
	case ph == "sha256":
		if !isStr || !sha256Pattern.MatchString(s) {
			return fail("a sha256")
		}
	case ph == "int":
		f, ok := actual.(float64)
		if !ok || f != float64(int64(f)) {
			return fail("an integer")
		}
	case strings.HasPrefix(ph, "product:"):
		want, ok := products(strings.TrimPrefix(ph, "product:"))
		if !ok {
			return fail("product " + strings.TrimPrefix(ph, "product:"))
		}
		return matchExpected(actual, want, path, products)
	default:
		return []string{fmt.Sprintf("%s: unknown placeholder {{%s}}", path, ph)}
	}
	return nil
}

func short(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// toAny round-trips v through JSON.
func toAny(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	return out
}
