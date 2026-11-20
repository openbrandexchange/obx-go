package mock_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

// fixture node with an ingest and a protocol client.
type fixtureNode struct {
	n     *mock.Node
	srv   *httptest.Server
	ing   *ingest.Client
	c     *client.Client
	creds mock.Credentials
}

func newFixtureNode(t *testing.T, opts mock.Options) *fixtureNode {
	t.Helper()
	opts.Fixtures = true
	n := mock.New(opts)
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	creds := n.Credentials()
	ing, err := ingest.New(srv.URL, creds.IngestToken)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(client.Config{BaseURL: srv.URL, ClientID: creds.Full.ClientID, ClientSecret: creds.Full.ClientSecret})
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureNode{n: n, srv: srv, ing: ing, c: c, creds: creds}
}

// raw sends a request with the ingest token (or the given header) and
// returns status and body.
func (f *fixtureNode) raw(t *testing.T, method, path string, body []byte, h http.Header) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, bytes.NewReader(body))
	if h == nil {
		h = http.Header{}
	}
	if _, ok := h["Authorization"]; !ok {
		h.Set("Authorization", "Bearer "+f.creds.IngestToken)
	}
	if body != nil && h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// importCSV imports a products.csv and returns the run and its problems.
func (f *fixtureNode) importCSV(t *testing.T, data []byte, dryRun bool) (*obx.ImportRun, []obx.ImportRowError) {
	t.Helper()
	ctx := context.Background()
	job, err := f.ing.Import(ctx, ingest.ImportRequest{CSV: &ingest.File{Name: "products.csv", Data: data}, Options: obx.ImportOptions{Mode: obx.ImportModeUpsert, DryRun: dryRun}})
	if err != nil {
		t.Fatal(err)
	}
	job, err = f.ing.WaitJob(ctx, job.ID, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	probs, err := f.ing.ImportProblems(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job.ImportRun, probs
}

func fixtureFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(specdata.Fixtures(), name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func errorsOnly(ps []obx.ImportRowError) []obx.ImportRowError {
	var out []obx.ImportRowError
	for _, p := range ps {
		if p.Severity == obx.SeverityError {
			out = append(out, p)
		}
	}
	return out
}

// I-IMP-12: which variant a row is (import-format.md 1.2.1), with the
// spec's fixtures and expected results.
func TestImportVariantIdentity(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	var exp struct {
		Cases []struct {
			File   string `json:"file"`
			Counts struct {
				Rows, Created, Updated, Unchanged, Errors int
			} `json:"counts"`
			Errors   []obx.ImportRowError `json:"errors"`
			Products map[string]struct {
				Variants []map[string]any `json:"variants"`
			} `json:"products"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixtureFile(t, "imports/variant-identity.expected.json"), &exp); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{} // external id → protocol id, stable over the files
	for _, cs := range exp.Cases {
		run, probs := f.importCSV(t, fixtureFile(t, "imports/"+cs.File), false)
		c := run.Counts
		if c.Rows != cs.Counts.Rows || c.Created != cs.Counts.Created || c.Updated != cs.Counts.Updated || c.Unchanged != cs.Counts.Unchanged || c.Errors != cs.Counts.Errors {
			t.Errorf("%s: counts %+v, want %+v", cs.File, c, cs.Counts)
		}
		errs := errorsOnly(probs)
		if len(errs) != len(cs.Errors) {
			t.Fatalf("%s: errors %+v, want %+v", cs.File, errs, cs.Errors)
		}
		for i, want := range cs.Errors {
			got := errs[i]
			if got.File != want.File || obx.Str(got.Column) != obx.Str(want.Column) || got.ObxError != want.ObxError || obx.Str(got.Value) != obx.Str(want.Value) || got.Row == nil || *got.Row != *want.Row {
				t.Errorf("%s: error %d = %+v, want %+v", cs.File, i, got, want)
			}
		}
		var detail *obx.ProductDetail
		if err := f.c.EachProduct(ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error {
			if p.ExternalID == "ML-8101" {
				detail = p
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if detail == nil {
			t.Fatalf("%s: ML-8101 not served", cs.File)
		}
		want := cs.Products["ML-8101"].Variants
		if len(detail.Variants) != len(want) {
			t.Fatalf("%s: %d variants, want %d", cs.File, len(detail.Variants), len(want))
		}
		for i, wv := range want {
			gv := detail.Variants[i]
			b, _ := json.Marshal(gv)
			var got map[string]any
			json.Unmarshal(b, &got)
			for k, v := range wv {
				if got[k] != v {
					t.Errorf("%s: variant %d %s = %v, want %v", cs.File, i, k, got[k], v)
				}
			}
			if prev, ok := ids[gv.ExternalID]; ok && prev != gv.ID {
				t.Errorf("%s: variant %s changed its id", cs.File, gv.ExternalID)
			}
			ids[gv.ExternalID] = gv.ID
		}
	}
	// I-IMP-7 case 3: a row without size finds MN-1008-BLK-CABIN by its GTIN.
	run, probs := f.importCSV(t, fixtureFile(t, "imports/3-measurement-update.csv"), false)
	if run.Counts.Updated != 1 || run.Counts.Unchanged != 1 || run.Counts.Created != 0 || len(errorsOnly(probs)) != 0 {
		t.Fatalf("3-measurement-update: %+v %+v", run.Counts, probs)
	}
	rec, err := f.ing.Get(ctx, obx.KindVariant, "MN-1008-BLK-CABIN")
	if err != nil || !strings.Contains(string(rec.Data), `"weight_kg":2.7`) {
		t.Fatalf("MN-1008-BLK-CABIN: %v %s", err, rec.Data)
	}
	if _, err := f.ing.Get(ctx, obx.KindVariant, "MN-1008-BLK"); !obx.IsCode(err, obx.ErrNotFound) {
		t.Fatalf("no default-id variant may be created: %v", err)
	}
}

// The MAISON NOOR fixtures use default variant ids and give no material_unknown;
// label-warnings.csv gives material_unknown on the product's first row.
func TestImportMaterialUnknown(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	for _, id := range []string{"MN-1001-NVY-M", "MN-1004-IND-30-32", "MN-1005-COG-ONE SIZE"} {
		if _, err := f.ing.Get(ctx, obx.KindVariant, id); err != nil {
			t.Errorf("fixture variant %s: %v", id, err)
		}
	}
	_, probs := f.importCSV(t, fixtureFile(t, "imports/6-restore.csv"), false)
	for _, p := range probs {
		if p.ObxError == obx.WarnMaterialUnknown {
			t.Errorf("the MAISON NOOR import must have no material_unknown: %+v", p)
		}
	}
	var exp struct {
		Codes    []string             `json:"codes"`
		Warnings []obx.ImportRowError `json:"warnings"`
	}
	json.Unmarshal(fixtureFile(t, "imports/label-warnings.expected.json"), &exp)
	_, probs = f.importCSV(t, fixtureFile(t, "imports/label-warnings.csv"), true)
	var got []obx.ImportRowError
	for _, p := range probs {
		if obx.Contains(exp.Codes, p.ObxError) {
			got = append(got, p)
		}
	}
	if len(got) != len(exp.Warnings) {
		t.Fatalf("warnings %+v, want %+v", got, exp.Warnings)
	}
	for i, w := range exp.Warnings {
		g := got[i]
		if g.ObxError != w.ObxError || *g.Row != *w.Row || obx.Str(g.Column) != obx.Str(w.Column) || obx.Str(g.Value) != obx.Str(w.Value) {
			t.Errorf("warning %d = %+v, want %+v", i, g, w)
		}
	}
	// The decimal comma of 5-invalid.csv row 5.
	_, probs = f.importCSV(t, fixtureFile(t, "imports/5-invalid.csv"), false)
	errs := errorsOnly(probs)
	if len(errs) != 5 || !strings.HasPrefix(errs[4].Detail, "materials: decimal comma") {
		t.Fatalf("5-invalid: %+v", errs)
	}
}

// ingest.md 3.4: a GTIN of another variant is 409 gtin_conflict with
// pointer /gtin and a detail naming that variant and its product.
func TestIngestGTINConflict(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	status, body := f.raw(t, http.MethodPut, obx.PathIngest+"/variants/MN-1002-X-1",
		[]byte(`{"product_external_id":"MN-1002","gtin":"2000100101025","color":{"code":"X"}}`), nil)
	var p obx.Problem
	json.Unmarshal(body, &p)
	if status != http.StatusConflict || p.ObxError != obx.ErrGTINConflict || len(p.Errors) == 0 || obx.Str(p.Errors[0].Pointer) != "/gtin" ||
		!strings.Contains(p.DetailString(), "MN-1001-NVY-M") || !strings.Contains(p.DetailString(), "MN-1001") {
		t.Fatalf("%d %s", status, body)
	}
}

// ingest.md 3.1: extensions are stored and returned everywhere; other
// unknown members at any depth are ignored with a warning (I-EXT-1).
func TestExtensionsAndUnknownMembers(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	var doc map[string]any
	json.Unmarshal(fixtureFile(t, "examples/ProductInput.extension.json"), &doc)
	doc["brand_external_id"] = "maison-noor"
	doc["unknown_member"] = "x"
	fiber := doc["materials"].([]any)[0].(map[string]any)["fibers"].([]any)[0].(map[string]any)
	fiber["unknown_nested"] = true
	big := strings.Repeat("a", 9000)
	doc["x-noor-big"] = big
	res, err := f.ing.Put(ctx, obx.KindProduct, "KX-EXT", doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := strings.Join(res.Warnings, "\n")
	if res.Result != obx.ResultCreated || !strings.Contains(ws, "/unknown_member") || !strings.Contains(ws, "/materials/0/fibers/0/unknown_nested") {
		t.Fatalf("%+v", res)
	}
	vres, err := f.ing.Put(ctx, obx.KindVariant, "KX-EXT-NVY-M", map[string]any{"product_external_id": "KX-EXT", "x-noor-bin": "A-12",
		"color": map[string]any{"code": "NVY", "x-noor-pantone": "19-4023", "bogus": 1}, "size": "M"}, nil)
	if err != nil || len(vres.Warnings) != 1 || !strings.Contains(vres.Warnings[0], "/color/bogus") {
		t.Fatalf("variant: %v %+v", err, vres)
	}
	rec, _ := f.ing.Get(ctx, obx.KindProduct, "KX-EXT")
	data := string(rec.Data)
	for _, want := range []string{`"x-noor-fabric-weight-gsm":180`, `"x-noor-mill":"Mill 7"`, `"x-noor-care-note":{"en":"Wrinkles are part of the look"}`} {
		if !strings.Contains(data, want) {
			t.Errorf("Ingest GET lacks %s: %s", want, data)
		}
	}
	if strings.Contains(data, "unknown_") {
		t.Errorf("unknown members stored: %s", data)
	}
	// Protocol and /changes.
	var detail *obx.ProductDetail
	f.c.EachProduct(ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error {
		if p.ExternalID == "KX-EXT" {
			detail = p
		}
		return nil
	})
	if detail == nil || string(detail.X["x-noor-fabric-weight-gsm"]) != "180" || len(detail.Variants) != 1 ||
		string(detail.Variants[0].X["x-noor-bin"]) != `"A-12"` || string(detail.Variants[0].Color.X["x-noor-pantone"]) != `"19-4023"` ||
		string(detail.Materials[0].Fibers[0].Material.X["x-noor-mill"]) != `"Mill 7"` || len(detail.X["x-noor-big"]) < 9000 {
		t.Fatalf("protocol product: %+v", detail)
	}
	var feed []byte
	cursor := ""
	for {
		p, err := f.c.Changes(ctx, cursor, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, ch := range p.Changes {
			if ch.ExternalIDValue() == "KX-EXT" {
				feed = ch.Data
			}
		}
		cursor = p.NextCursor
		if !p.HasMore {
			break
		}
	}
	if !strings.Contains(string(feed), `"x-noor-mill":"Mill 7"`) {
		t.Fatalf("/changes data: %s", feed)
	}
	// Repeat = unchanged; change = updated; PATCH null removes.
	if r, err := f.ing.Put(ctx, obx.KindProduct, "KX-EXT", doc, nil); err != nil || r.Result != obx.ResultUnchanged {
		t.Fatalf("repeat: %v %+v", err, r)
	}
	doc["x-noor-fabric-weight-gsm"] = 200
	if r, err := f.ing.Put(ctx, obx.KindProduct, "KX-EXT", doc, nil); err != nil || r.Result != obx.ResultUpdated {
		t.Fatalf("change: %v %+v", err, r)
	}
	if r, err := f.ing.Patch(ctx, obx.KindProduct, "KX-EXT", json.RawMessage(`{"x-noor-big":null}`), nil); err != nil || r.Result != obx.ResultUpdated {
		t.Fatalf("patch null: %v %+v", err, r)
	}
	rec, _ = f.ing.Get(ctx, obx.KindProduct, "KX-EXT")
	if strings.Contains(string(rec.Data), "x-noor-big") || !strings.Contains(string(rec.Data), `"x-noor-fabric-weight-gsm":200`) {
		t.Fatalf("after patch: %s", rec.Data)
	}
	// An asset keeps its extension; an import keeps the extensions of the
	// objects it writes (rule 9).
	if _, err := f.ing.Patch(ctx, obx.KindAsset, "MN-1001_NVY_1", map[string]any{"x-noor-shoot": "SS26-03", "rights": map[string]any{"x-noor-contract": "C-1"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ing.Patch(ctx, obx.KindVariant, "MN-1001-NVY-M", map[string]any{"x-noor-bin": "B-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ing.Patch(ctx, obx.KindProduct, "MN-1001", map[string]any{"x-noor-line": "core"}, nil); err != nil {
		t.Fatal(err)
	}
	f.importCSV(t, fixtureFile(t, "imports/6-restore.csv"), false)
	for kind, ext := range map[string]string{obx.KindAsset: "MN-1001_NVY_1", obx.KindVariant: "MN-1001-NVY-M", obx.KindProduct: "MN-1001"} {
		rec, _ := f.ing.Get(ctx, kind, ext)
		if !strings.Contains(string(rec.Data), `"x-noor-`) {
			t.Errorf("%s %s lost its extension in the import: %s", kind, ext, rec.Data)
		}
	}
	rec, _ = f.ing.Get(ctx, obx.KindAsset, "MN-1001_NVY_1")
	if !strings.Contains(string(rec.Data), `"x-noor-contract":"C-1"`) || !strings.Contains(string(rec.Data), `"x-noor-shoot":"SS26-03"`) {
		t.Errorf("asset: %s", rec.Data)
	}
	a, err := f.c.Asset(ctx, rec.ID)
	if err != nil || string(a.X["x-noor-shoot"]) != `"SS26-03"` || string(a.Rights.X["x-noor-contract"]) != `"C-1"` {
		t.Errorf("protocol asset: %v %+v", err, a)
	}
}

// ingest.md 3.1, I-MAT-1: the draft single qualifier on write.
func TestDraftQualifier(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	body := func(withDraft bool) map[string]any {
		fibers := []any{
			map[string]any{"material": map[string]any{"code": "polyester"}, "percentage": 60, "qualifier": map[string]any{"code": "recycled"}},
			map[string]any{"material": map[string]any{"code": "cotton"}, "percentage": 35, "qualifier": map[string]any{"code": "organic"}, "qualifiers": []any{map[string]any{"code": "regenerative"}}},
			map[string]any{"material": map[string]any{"code": "elastane"}, "percentage": 5, "qualifier": nil},
		}
		if !withDraft {
			fibers = []any{
				map[string]any{"material": map[string]any{"code": "polyester"}, "percentage": 60, "qualifiers": []any{map[string]any{"code": "recycled"}}},
				map[string]any{"material": map[string]any{"code": "cotton"}, "percentage": 35, "qualifiers": []any{map[string]any{"code": "regenerative"}}},
				map[string]any{"material": map[string]any{"code": "elastane"}, "percentage": 5},
			}
		}
		return map[string]any{"brand_external_id": "maison-noor", "style_number": "KX-Q", "name": map[string]string{"en": "Q"}, "item_type": "t_shirt",
			"contains_animal_origin": false, "materials": []any{map[string]any{"part": map[string]any{"code": "main"}, "fibers": fibers}}}
	}
	res, err := f.ing.Put(ctx, obx.KindProduct, "KX-Q", body(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range res.WarningDetails {
		if d.Code == obx.WarnQualifierDeprecated {
			n++
			if obx.Str(d.Pointer) != "/materials/0/fibers/0/qualifier" {
				t.Errorf("pointer %s", obx.Str(d.Pointer))
			}
		}
	}
	if n != 1 || res.Result != obx.ResultCreated || !strings.Contains(strings.Join(res.Warnings, "\n"), obx.WarnQualifierDeprecated) {
		t.Fatalf("%+v", res)
	}
	rec, _ := f.ing.Get(ctx, obx.KindProduct, "KX-Q")
	if strings.Contains(string(rec.Data), `"qualifier"`) {
		t.Fatalf("qualifier stored: %s", rec.Data)
	}
	var in obx.ProductInput
	json.Unmarshal(rec.Data, &in)
	fs := in.Materials[0].Fibers
	if len(fs[0].Qualifiers) != 1 || fs[0].Qualifiers[0].Code != "recycled" || len(fs[1].Qualifiers) != 1 || fs[1].Qualifiers[0].Code != "regenerative" || len(fs[2].Qualifiers) != 0 {
		t.Fatalf("qualifiers %s", rec.Data)
	}
	if r, err := f.ing.Put(ctx, obx.KindProduct, "KX-Q", body(false), nil); err != nil || r.Result != obx.ResultUnchanged {
		t.Fatalf("rewritten with qualifiers only: %v %+v", err, r)
	}
	if r, err := f.ing.Put(ctx, obx.KindProduct, "KX-Q", body(true), nil); err != nil || r.Result != obx.ResultUnchanged {
		t.Fatalf("draft again: %v %+v", err, r)
	}
	// The admin catalog API reads it too.
	adm, _ := nodeadmin.New(f.srv.URL, nil)
	if _, err := adm.Login(ctx, f.creds.AdminEmail, f.creds.AdminPassword); err != nil {
		t.Fatal(err)
	}
	var ar obx.IngestResult
	if err := adm.Call(ctx, http.MethodPut, obx.PathAdminAPI+"/catalog/products/KX-Q", body(true), &ar); err != nil || ar.Result != obx.ResultUnchanged || len(ar.WarningDetails) == 0 {
		t.Fatalf("admin: %v %+v", err, ar)
	}
}

// protocol.md 2.1: limit above the maximum is clamped, others are 400.
func TestLimitClamp(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	tok, err := f.c.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h := func() http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }
	for _, path := range []string{"/products", "/changes"} {
		for _, c := range []struct {
			limit  string
			status int
		}{{"100000", 200}, {"99999999999999999999999", 200}, {"0", 400}, {"-1", 400}, {"abc", 400}, {"2.5", 400}} {
			status, body := f.raw(t, http.MethodGet, obx.PathAPIBase+path+"?limit="+c.limit, nil, h())
			if status != c.status {
				t.Errorf("%s limit=%s: %d %s", path, c.limit, status, body)
			}
			if status == 400 && !strings.Contains(string(body), obx.ErrInvalidRequest) {
				t.Errorf("%s limit=%s: %s", path, c.limit, body)
			}
		}
	}
}

// protocol.md 5.5: signed URLs.
func TestSignedURLErrors(t *testing.T) {
	ck := &clock{t: time.Now()}
	f := newFixtureNode(t, mock.Options{Clock: ck.now})
	ctx := context.Background()
	rec, _ := f.ing.Get(ctx, obx.KindAsset, "MN-1001_NVY_1")
	a, err := f.c.Asset(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	u := a.Renditions.Thumb.URL
	get := func(url string, h http.Header) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		for k, v := range h {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	plain := u[:strings.Index(u, "?")]
	tok, _ := f.c.Token(ctx)
	if s := get(u, nil); s != 200 {
		t.Errorf("valid signed URL: %d", s)
	}
	if s := get(strings.Replace(u, "sig=", "sig=0", 1), nil); s != 403 {
		t.Errorf("tampered: %d", s)
	}
	if s := get(plain, nil); s != 401 {
		t.Errorf("no signature, no token: %d", s)
	}
	if s := get(strings.Replace(u, "sig=", "sig=0", 1), http.Header{"Authorization": {"Bearer " + tok}}); s != 200 {
		t.Errorf("token with a bad signature: %d", s)
	}
	ck.add(2 * time.Hour)
	if s := get(u, nil); s != 403 {
		t.Errorf("expired: %d", s)
	}
}

// protocol.md 4.3: rotating a secret is not a revocation.
func TestRotateClientSecret(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	p, err := f.c.Changes(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := f.c.Token(ctx)
	secret, err := f.n.RotateClientSecret(f.creds.Full.ClientID)
	if err != nil || secret == "" {
		t.Fatal(err)
	}
	status, body := f.raw(t, http.MethodGet, obx.PathAPIBase+"/grant", nil, http.Header{"Authorization": {"Bearer " + old}})
	if status != 401 || !strings.Contains(string(body), obx.ErrInvalidToken) {
		t.Fatalf("old token: %d %s", status, body)
	}
	oldClient, _ := client.New(client.Config{BaseURL: f.srv.URL, ClientID: f.creds.Full.ClientID, ClientSecret: f.creds.Full.ClientSecret})
	if _, err := oldClient.Token(ctx); err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("old secret: %v", err)
	}
	nc, _ := client.New(client.Config{BaseURL: f.srv.URL, ClientID: f.creds.Full.ClientID, ClientSecret: secret})
	if _, err := nc.Changes(ctx, p.NextCursor, 10); err != nil {
		t.Fatalf("new token with the old cursor: %v", err)
	}
	if _, err := f.n.RotateClientSecret("nope"); err == nil {
		t.Fatal("unknown client")
	}
}

// ingest.md 5, protocol.md 8: Idempotency-Key.
func TestIdempotencyKey(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	// batch
	ops := []byte(`[{"op":"upsert","kind":"product","external_id":"KX-IDEM","data":{"brand_external_id":"maison-noor","style_number":"KX-IDEM","name":{"en":"A"}}}]`)
	h := func(key string) http.Header { return http.Header{obx.HeaderIdempotencyKey: {key}} }
	s1, b1 := f.raw(t, http.MethodPost, obx.PathIngest+"/batch", ops, h("k1"))
	s2, b2 := f.raw(t, http.MethodPost, obx.PathIngest+"/batch", ops, h("k1"))
	if s1 != 200 || s2 != 200 || !bytes.Equal(b1, b2) {
		t.Fatalf("batch replay: %d %s / %d %s", s1, b1, s2, b2)
	}
	other := bytes.Replace(ops, []byte(`"A"`), []byte(`"B"`), 1)
	s3, b3 := f.raw(t, http.MethodPost, obx.PathIngest+"/batch", other, h("k1"))
	if s3 != 409 || !strings.Contains(string(b3), obx.ErrIdempotencyKeyReused) {
		t.Fatalf("batch other body: %d %s", s3, b3)
	}
	rec, _ := f.ing.Get(ctx, obx.KindProduct, "KX-IDEM")
	if !strings.Contains(string(rec.Data), `"en":"A"`) {
		t.Fatalf("nothing may be written: %s", rec.Data)
	}
	// imports
	csv := []byte("brand,style_number,name:en\nMAISON NOOR,KX-IMP,Import\n")
	imp := func(data []byte) (*obx.IngestJob, error) {
		return f.ing.Import(ctx, ingest.ImportRequest{CSV: &ingest.File{Name: "products.csv", Data: data}, IdempotencyKey: "k2"})
	}
	j1, err := imp(csv)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := imp(csv)
	if err != nil || j2.ID != j1.ID {
		t.Fatalf("import replay: %v %v %v", err, j1.ID, j2.ID)
	}
	if _, err := imp(append(csv, []byte("MAISON NOOR,KX-IMP2,Other\n")...)); !obx.IsCode(err, obx.ErrIdempotencyKeyReused) {
		t.Fatalf("import other body: %v", err)
	}
	if _, err := f.ing.Get(ctx, obx.KindProduct, "KX-IMP2"); !obx.IsCode(err, obx.ErrNotFound) {
		t.Fatalf("the refused import wrote: %v", err)
	}
	// exports
	e1, err := f.c.CreateExport(ctx, obx.ExportRequest{}, "k3")
	if err != nil {
		t.Fatal(err)
	}
	e2, err := f.c.CreateExport(ctx, obx.ExportRequest{}, "k3")
	if err != nil || e2.ID != e1.ID {
		t.Fatalf("export replay: %v %s %s", err, e1.ID, e2.ID)
	}
	if _, err := f.c.CreateExport(ctx, obx.ExportRequest{Renditions: []string{obx.RenditionThumb}}, "k3"); !obx.IsCode(err, obx.ErrIdempotencyKeyReused) {
		t.Fatalf("export other body: %v", err)
	}
}

// Nullable percentage and language tags with script and numeric region.
func TestPercentageNullAndLanguageTags(t *testing.T) {
	f := newFixtureNode(t, mock.Options{})
	ctx := context.Background()
	body := map[string]any{"brand_external_id": "maison-noor", "style_number": "KX-P", "name": map[string]string{"en": "Bag", "zh-Hant": "包", "es-419": "Bolso"}, "item_type": "bag",
		"contains_animal_origin": true,
		"materials":              []any{map[string]any{"part": map[string]any{"code": "outer"}, "fibers": []any{map[string]any{"material": map[string]any{"code": "leather"}}}}}}
	res, err := f.ing.Put(ctx, obx.KindProduct, "KX-P", body, nil)
	if err != nil || res.Result != obx.ResultCreated {
		t.Fatalf("%v %+v", err, res)
	}
	p, err := f.c.Product(ctx, obx.Str(res.ID))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p.Materials)
	if !strings.Contains(string(b), `"percentage":null`) || p.Name["zh-Hant"] != "包" || p.Name["es-419"] != "Bolso" {
		t.Fatalf("%s %v", b, p.Name)
	}
	csv := []byte("brand,style_number,name:en,name:ZH_hant,item_type,materials,contains_animal_origin\nMAISON NOOR,KX-L,Tee,T恤,bag,outer: leather,yes\n")
	run, probs := f.importCSV(t, csv, false)
	if run.Counts.Created != 1 || len(errorsOnly(probs)) != 0 {
		t.Fatalf("%+v %+v", run.Counts, probs)
	}
	rec, _ := f.ing.Get(ctx, obx.KindProduct, "KX-L")
	if !strings.Contains(string(rec.Data), `"zh-Hant":"T恤"`) || !strings.Contains(string(rec.Data), `"percentage":null`) {
		t.Fatalf("%s", rec.Data)
	}
}
