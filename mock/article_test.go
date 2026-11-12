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

	// 0.1 fibre keys are normalised, a flag false is not stored, {} is null,
	// an unknown feature key is kept as sent, and label warnings come with codes.
	body := map[string]any{
		"brand_external_id": "kestrel", "style_number": "KX-1", "name": map[string]string{"en": "Test scarf"}, "item_type": "scarf",
		"materials": []any{map[string]any{"part": "main", "fibers": []any{
			map[string]any{"fiber": "organic_cotton", "percentage": 60},
			map[string]any{"fiber": "wool", "percentage": 30, "recycled": false},
		}}},
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
	if b, _ := json.Marshal(data["materials"]); string(b) != `[{"fibers":[{"fiber":"cotton","organic":true,"percentage":60},{"fiber":"wool","percentage":30}],"part":"main"}]` {
		t.Fatalf("stored materials %s", b)
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
	// A 0.1 key and the flag are the same product.
	body["materials"] = []any{map[string]any{"part": "main", "fibers": []any{
		map[string]any{"fiber": "cotton", "percentage": 60, "organic": true},
		map[string]any{"fiber": "wool", "percentage": 30},
	}}}
	if r, err := ing.Put(ctx, obx.KindProduct, "KX-1", body, nil); err != nil || r.Result != obx.ResultUnchanged {
		t.Fatalf("flag form after 0.1 key: %v %+v", err, r)
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

	// The protocol object has every 0.2 property, null when not stated, and
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
