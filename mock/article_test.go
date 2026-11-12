package mock_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/obx"
)

// The Ingest API side of the 0.2 article data (ingest.md 3.4).
func TestIngestArticleData(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	creds := n.Credentials()
	ctx := context.Background()
	ing, _ := ingest.New(srv.URL, creds.IngestToken)
	c, _ := client.New(client.Config{BaseURL: srv.URL, ClientID: creds.Full.ClientID, ClientSecret: creds.Full.ClientSecret})

	// 0.1 fibre keys are normalised to the qualifier, known codes get the
	// vocabulary's names (unknown codes keep what was sent), {} is null, an
	// unknown feature key is kept as sent, and label warnings come with codes.
	body := map[string]any{
		"brand_external_id": "kestrel", "style_number": "KX-1", "name": map[string]string{"en": "Test scarf"}, "item_type": "scarf",
		"materials": []any{map[string]any{"part": map[string]any{"code": "main"}, "fibers": []any{
			map[string]any{"material": map[string]any{"code": "organic_cotton"}, "percentage": 60},
			map[string]any{"material": map[string]any{"code": "wool", "name": map[string]any{"en": "Mine"}}, "percentage": 30, "qualifier": nil},
			map[string]any{"material": map[string]any{"code": "seacell", "name": map[string]any{"en": "SeaCell"}}, "percentage": 5, "qualifier": map[string]any{"code": "in_conversion"}},
		}}},
		"care":           []any{map[string]any{"code": "wash_30", "text": map[string]any{"en": "Wash inside out"}}, map[string]any{"code": "steam_only", "category": "ironing"}},
		"features":       map[string]any{"fit": "regular", "fringe_cm": 5},
		"certifications": []any{},
	}
	res, err := ing.Put(ctx, obx.KindProduct, "KX-1", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, w := range res.WarningDetails {
		codes = append(codes, w.Code+" "+obx.Str(w.Pointer))
	}
	if strings.Join(codes, ", ") != "animal_origin_unknown /contains_animal_origin, fibre_percentages_not_100 /materials/0" || len(res.Warnings) < 2 || res.Result != obx.ResultCreated {
		t.Fatalf("warnings %s / %v (%s)", codes, res.Warnings, res.Result)
	}
	rec, _ := ing.Get(ctx, obx.KindProduct, "KX-1")
	var data map[string]any
	_ = json.Unmarshal(rec.Data, &data)
	if b, _ := json.Marshal(data["materials"]); string(b) != `[{"fibers":[{"material":{"code":"cotton","name":{"de":"Baumwolle","en":"Cotton","fr":"Coton","nl":"Katoen"}},"percentage":60,"qualifier":{"code":"organic","name":{"de":"Bio","en":"Organic","fr":"Biologique","nl":"Biologisch"}}},`+
		`{"material":{"code":"wool","name":{"de":"Wolle","en":"Wool","fr":"Laine","nl":"Wol"}},"percentage":30,"qualifier":null},`+
		`{"material":{"code":"seacell","name":{"en":"SeaCell"}},"percentage":5,"qualifier":{"code":"in_conversion","name":null}}],"part":{"code":"main","name":null}}]` {
		t.Fatalf("stored materials %s", b)
	}
	if b, _ := json.Marshal(data["care"]); string(b) != `[{"category":"washing","code":"wash_30","name":{"de":"Waschen bei 30 °C","en":"Wash at 30 °C","fr":"Lavage à 30 °C","nl":"Wassen op 30 °C"},"symbol":"1_D_30","text":{"en":"Wash inside out"}},`+
		`{"category":"ironing","code":"steam_only","name":null,"symbol":null,"text":null}]` {
		t.Fatalf("stored care %s", b)
	}
	if b, _ := json.Marshal(data["features"]); string(b) != `{"fit":"regular","fringe_cm":5}` {
		t.Fatalf("stored features %s", b)
	}
	if data["certifications"] != nil {
		t.Fatalf("an empty certifications list must be stored as null: %v", data["certifications"])
	}
	// PUTting back the data of a GET is unchanged.
	again, err := ing.Put(ctx, obx.KindProduct, "KX-1", rec.Data, nil)
	if err != nil || again.Result != obx.ResultUnchanged {
		t.Fatalf("round trip: %v %+v", err, again)
	}
	// A 0.1 key and the qualifier are the same product, with or without names.
	body["materials"] = []any{map[string]any{"part": map[string]any{"code": "main"}, "fibers": []any{
		map[string]any{"material": map[string]any{"code": "cotton"}, "percentage": 60, "qualifier": map[string]any{"code": "organic"}},
		map[string]any{"material": map[string]any{"code": "wool"}, "percentage": 30},
		map[string]any{"material": map[string]any{"code": "seacell", "name": map[string]any{"en": "SeaCell"}}, "percentage": 5, "qualifier": map[string]any{"code": "in_conversion"}},
	}}}
	if r, err := ing.Put(ctx, obx.KindProduct, "KX-1", body, nil); err != nil || r.Result != obx.ResultUnchanged {
		t.Fatalf("qualifier form after 0.1 key: %v %+v", err, r)
	}
	// The protocol object carries the same names.
	prod, err := c.Product(ctx, obx.Str(res.ID))
	if err != nil {
		t.Fatal(err)
	}
	if f := prod.Materials[0].Fibers[0]; f.Material.Name["nl"] != "Katoen" || f.Qualifier == nil || f.Qualifier.Name["fr"] != "Biologique" || obx.Str(prod.Care[0].Symbol) != "1_D_30" || prod.Materials[0].Part.Name != nil {
		t.Fatalf("protocol product: %+v", prod.Materials)
	}

	// Registry values outside the registry are validation_failed.
	for _, bad := range []map[string]any{{"wheels": 3}, {"shell": "glass"}, {"detachable_parts": []string{"hood", "hood"}}} {
		body["features"] = bad
		if _, err := ing.Put(ctx, obx.KindProduct, "KX-1", body, nil); !obx.IsCode(err, obx.ErrValidationFailed) {
			t.Errorf("features %v: want validation_failed, got %v", bad, err)
		}
	}
	body["features"] = map[string]any{}
	body["footwear_materials"] = map[string]any{"upper": "plastic"}
	if _, err := ing.Put(ctx, obx.KindProduct, "KX-1", body, nil); !obx.IsCode(err, obx.ErrValidationFailed) {
		t.Errorf("footwear material plastic: want validation_failed, got %v", err)
	}
	delete(body, "footwear_materials")
	if _, err := ing.Put(ctx, obx.KindProduct, "KX-1", body, nil); err != nil {
		t.Fatalf("features {}: %v", err)
	}

	// Variant: measurements {} is null; a measurement of 0 and an unknown
	// colour family are refused.
	v := map[string]any{"product_external_id": "KX-1", "gtin": "2000199901018", "color": map[string]any{"code": "RED", "family": "red"}, "measurements": map[string]any{}}
	if _, err := ing.Put(ctx, obx.KindVariant, "2000199901018", v, nil); err != nil {
		t.Fatal(err)
	}
	v["measurements"] = map[string]any{"weight_kg": 0}
	if _, err := ing.Put(ctx, obx.KindVariant, "2000199901018", v, nil); !obx.IsCode(err, obx.ErrValidationFailed) {
		t.Errorf("weight 0: want validation_failed, got %v", err)
	}
	v["measurements"] = map[string]any{"strap_length_cm": map[string]any{"min": 130, "max": 80}}
	if _, err := ing.Put(ctx, obx.KindVariant, "2000199901018", v, nil); !obx.IsCode(err, obx.ErrValidationFailed) {
		t.Errorf("strap 130-80: want validation_failed, got %v", err)
	}
	v["measurements"] = nil
	v["color"] = map[string]any{"code": "RED", "family": "navy"}
	if _, err := ing.Put(ctx, obx.KindVariant, "2000199901018", v, nil); !obx.IsCode(err, obx.ErrValidationFailed) {
		t.Errorf("family navy: want validation_failed, got %v", err)
	}

	// The protocol object has every article data property, null when not stated, and
	// the unknown feature as sent.
	var id string
	if err := c.EachProduct(ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error {
		if p.ExternalID == "KX-1" {
			id = p.ID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := c.Product(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	for _, want := range []string{`"features":null`, `"footwear_materials":null`, `"contains_animal_origin":null`, `"item_subtype":null`, `"certifications":null`, `"measurements":null`, `"family":"red"`, `"size_length":null`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("product lacks %s: %s", want, b)
		}
	}
}
