package conformance

import (
	"encoding/json"
	"strings"
	"testing"
)

// P-CAT-6 rejects the 0.2 shapes, a single qualifier, a missing or repeated
// qualifier list, missing or empty names of vocabulary
// codes, a name on main and a wrong care category or symbol, and accepts
// unknown codes without a name and a node's own names for known codes.
func TestArticleTermChecks(t *testing.T) {
	cases := []struct {
		name, materials, care, want string
	}{
		{"ok", `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"cotton","name":{"en":"Own cotton"}},"percentage":95,"qualifiers":[{"code":"organic","name":{"en":"Organic"}},{"code":"regenerative","name":{"en":"Regenerative"}}]},{"material":{"code":"seacell","name":null},"percentage":5,"qualifiers":[{"code":"upcycled","name":null}]}]}]`,
			`[{"code":"wash_30","category":"washing","symbol":"1_D_30","name":{"en":"Wash at 30 °C"},"text":null},{"code":"steam_only","category":null,"symbol":null,"name":null,"text":null}]`, ""},
		{"0.2 shape", `[{"part":"main","fibers":[{"fiber":"cotton","percentage":100,"organic":true}]}]`, `[]`, "0.2 property fiber"},
		{"0.1 key", `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"recycled_polyester","name":null},"percentage":100,"qualifiers":[]}]}]`, `[]`, "is a 0.1 key"},
		{"main named", `[{"part":{"code":"main","name":{"en":"Main"}},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100,"qualifiers":[]}]}]`, `[]`, "want null"},
		{"known without name", `[{"part":{"code":"lining","name":null},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100,"qualifiers":[]}]}]`, `[]`, "a code of the vocabularies has a name"},
		{"empty string", `[{"part":{"code":"lining","name":{"en":"Lining","nl":""}},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100,"qualifiers":[]}]}]`, `[]`, "empty string"},
		{"no qualifiers property", `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100}]}]`, `[]`, "want a list ([] when there is none)"},
		{"qualifiers null", `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100,"qualifiers":null}]}]`, `[]`, "want a list ([] when there is none)"},
		{"single qualifier", `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100,"qualifier":{"code":"organic","name":null},"qualifiers":[]}]}]`, `[]`, "has the property qualifier"},
		{"repeated qualifier", `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100,"qualifiers":[{"code":"upcycled","name":null},{"code":"upcycled","name":null}]}]}]`, `[]`, "twice"},
		{"known qualifier without name", `[{"part":{"code":"main","name":null},"fibers":[{"material":{"code":"seacell","name":null},"percentage":100,"qualifiers":[{"code":"post_consumer","name":null}]}]}]`, `[]`, "a code of the vocabularies has a name"},
		{"care symbol", `[]`, `[{"code":"wash_30","category":"washing","symbol":"1_C_30","name":{"en":"Wash"},"text":null}]`, `symbol "1_C_30", want "1_D_30"`},
		{"care category", `[]`, `[{"code":"wash_70","category":"drying","symbol":null,"name":{"en":"Wash"},"text":null}]`, `category "drying", want "washing"`},
		{"care symbol null", `[]`, `[{"code":"wash_70","category":"washing","symbol":"1_A_70","name":{"en":"Wash"},"text":null}]`, "want null"},
		{"care without name", `[]`, `[{"code":"wash_30","category":"washing","symbol":"1_D_30","name":null,"text":null}]`, "has a name"},
	}
	for _, c := range cases {
		var m, care any
		if err := json.Unmarshal([]byte(c.materials), &m); err != nil {
			t.Fatal(c.name, err)
		}
		if err := json.Unmarshal([]byte(c.care), &care); err != nil {
			t.Fatal(c.name, err)
		}
		tt := &T{res: &Result{}}
		checkMaterials(tt, "X", m)
		checkCare(tt, "X", care)
		msgs := strings.Join(tt.res.Messages, "\n")
		switch {
		case c.want == "" && tt.failed:
			t.Errorf("%s: unexpected failure:\n%s", c.name, msgs)
		case c.want != "" && !strings.Contains(msgs, c.want):
			t.Errorf("%s: want a message with %q, got:\n%s", c.name, c.want, msgs)
		}
	}
}
