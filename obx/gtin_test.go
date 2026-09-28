package obx

import "testing"

func TestValidateGTIN(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"2000100101025", "02000100101025", true},
		{"2009001001014", "02009001001014", true},
		{"2009001001015", "", false}, // wrong check digit (fixture bad-gtin)
		{"96385074", "00000096385074", true},
		{"036000291452", "00036000291452", true},
		{"12345", "", false},
		{"20001001010x5", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, err := ValidateGTIN(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("ValidateGTIN(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestNormalizeLookupGTIN(t *testing.T) {
	cases := map[string]string{
		"2000100101025":   "02000100101025",
		" 2000 200101031": "02000200101031",
		"02000100101025":  "02000100101025",
		"2000100101026":   "02000100101026", // check digit not validated
		"12345":           "",
		"ABC":             "",
	}
	for in, want := range cases {
		got, ok := NormalizeLookupGTIN(in)
		if got != want || ok != (want != "") {
			t.Errorf("NormalizeLookupGTIN(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Kestrel Trail":  "kestrel-trail",
		"Kestrel":        "kestrel",
		"Élan  & Co.":    "elan-co",
		"--Straße 9--":   "strasse-9",
		"Café Noir/Lab ": "cafe-noir-lab",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidExternalID(t *testing.T) {
	if !ValidExternalID("KO-1001_NVY_1") || ValidExternalID("a/b") || ValidExternalID("") || ValidExternalID("a\nb") {
		t.Fatal("ValidExternalID wrong")
	}
}

func TestStatusFor(t *testing.T) {
	if StatusFor(ErrCursorExpired) != 410 || StatusFor(ErrPositionConflict) != 409 || StatusFor("nope") != 500 {
		t.Fatal("StatusFor wrong")
	}
	if len(ErrorCodes()) != 39 {
		t.Fatalf("want 39 error codes, got %d", len(ErrorCodes()))
	}
}

func TestLocalizedTextPick(t *testing.T) {
	txt := LocalizedText{"nl": "Trui", "en": "Sweater", "de": "Pullover"}
	cases := map[string]string{"nl": "Trui", "nl-BE": "Trui", "fr": "Sweater", "": "Sweater", "de-AT": "Pullover"}
	for lang, want := range cases {
		if got := txt.Pick(lang); got != want {
			t.Errorf("Pick(%q) = %q, want %q", lang, got, want)
		}
	}
	if got := (LocalizedText{"fr": "Pull", "de": "Pullover"}).Pick("nl"); got != "Pullover" {
		t.Errorf("first available: got %q", got)
	}
	if got := (LocalizedText{"en-GB": "Jumper"}).Pick("en"); got != "Jumper" {
		t.Errorf("base match: got %q", got)
	}
	if (LocalizedText(nil)).Pick("nl") != "" {
		t.Error("nil text")
	}
}
