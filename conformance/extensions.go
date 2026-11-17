package conformance

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/obx"
)

// Ingest tests of the 0.3.0 release: extensions and unknown members
// (I-EXT-1), the draft single qualifier (I-MAT-1) and variant identity in
// imports (I-IMP-12). They are registered in ingestprofile.go.

// jsonPath is a path into a decoded JSON value: string keys and int indexes.
type jsonPath []any

func (p jsonPath) String() string {
	var b strings.Builder
	for _, s := range p {
		fmt.Fprintf(&b, "/%v", s)
	}
	return b.String()
}

// lookup returns the value at path, and whether it is there.
func lookup(v any, path jsonPath) (any, bool) {
	for _, step := range path {
		switch k := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[k]; !ok {
				return nil, false
			}
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil, false
			}
			v = a[k]
		}
	}
	return v, true
}

// findKey returns the paths of every member named key, at any depth.
func findKey(v any, key string, at jsonPath) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := append(append(jsonPath{}, at...), k)
			if k == key {
				out = append(out, p.String())
			}
			out = append(out, findKey(e, key, p)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, findKey(e, key, append(append(jsonPath{}, at...), i))...)
		}
	}
	sort.Strings(out)
	return out
}

func decodeMap(raw []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// extCheck is one expected extension member: at path, value want (nil:
// the member must be absent).
type extCheck struct {
	path jsonPath
	want any
}

// checkCarrier checks the extension members and that no unknown member
// was kept, in one place the object is returned.
func (t *T) checkCarrier(where string, obj map[string]any, checks []extCheck, unknown []string) {
	if obj == nil {
		t.Errorf("%s: no object", where)
		return
	}
	for _, c := range checks {
		got, ok := lookup(obj, c.path)
		switch {
		case c.want == nil && ok:
			t.Errorf("%s: %s is still there (%s) but was removed", where, c.path, short(got))
		case c.want == nil:
		case !ok:
			t.Errorf("%s: the extension member %s is missing (ingest.md 3.1: stored and returned unchanged)", where, c.path)
		case !reflect.DeepEqual(toAny(got), toAny(c.want)):
			t.Errorf("%s: %s is %s, want %s", where, c.path, short(got), short(c.want))
		}
	}
	for _, k := range unknown {
		if paths := findKey(obj, k, nil); len(paths) > 0 {
			t.Errorf("%s: the unknown member %s was kept at %s; only x-<vendor>-<name> members are stored", where, k, strings.Join(paths, ", "))
		}
	}
}

// lastEntry returns the data of the last entry of the object in live.
func lastEntry(live []obx.Change, kind, id string) map[string]any {
	var out map[string]any
	for _, ch := range live {
		if ch.Type == kind && ch.ID == id && ch.Op == obx.OpUpsert {
			out = decodeMap(ch.Data)
		}
	}
	return out
}

func countEntries(live []obx.Change, kind, id string) int {
	n := 0
	for _, ch := range live {
		if ch.Type == kind && ch.ID == id {
			n++
		}
	}
	return n
}

// carriers returns the object as the Ingest GET, the protocol and the new
// /changes entries give it (protocol parts only with an API key). get reads
// the protocol object for the protocol id.
type carrier struct {
	where string
	obj   map[string]any
}

func (t *T) carriers(kind, ext string, cursor string, protocol func(id string) map[string]any) ([]carrier, string, []obx.Change) {
	rec, err := t.r.ing.Get(t.ctx, kind, ext)
	t.Check(err, "Ingest GET "+kind+" "+ext)
	out := []carrier{{"Ingest GET " + kind + " " + ext, decodeMap(rec.Data)}}
	if t.r.full == nil {
		return out, cursor, nil
	}
	out = append(out, carrier{"protocol " + kind + " " + ext, protocol(rec.ID)})
	live, next := t.newEntries(t.r.full, cursor)
	entry := lastEntry(live, kind, rec.ID)
	if entry == nil {
		t.Errorf("/changes has no upsert entry for %s %s", kind, ext)
	} else {
		out = append(out, carrier{"/changes entry of " + kind + " " + ext, entry})
	}
	return out, next, live
}

func (t *T) protocolProduct(id string) map[string]any {
	res := t.get(t.r.full, "/products/"+id)
	if res.status != http.StatusOK {
		t.Errorf("GET /products/%s: %d %s", id, res.status, truncate(res.body))
		return nil
	}
	return decodeMap(res.body)
}

func (t *T) protocolVariant(id string) map[string]any {
	rec := t.protocolVariantParent(id)
	if rec == nil {
		return nil
	}
	vs, _ := rec["variants"].([]any)
	for _, v := range vs {
		if m, ok := v.(map[string]any); ok && m["id"] == id {
			return m
		}
	}
	t.Errorf("variant %s is not in its product's variants", id)
	return nil
}

func (t *T) protocolVariantParent(variantID string) map[string]any {
	// The protocol has no variant endpoint: find the product by GTIN-less
	// search over the products list (the test product is small).
	var found map[string]any
	t.Check(t.r.full.EachProduct(t.ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error {
		for _, v := range p.Variants {
			if v.ID == variantID {
				found = toAny(p).(map[string]any)
			}
		}
		return nil
	}), "list products")
	return found
}

func (t *T) protocolAsset(id string) map[string]any {
	res := t.get(t.r.full, "/assets/"+id)
	if res.status != http.StatusOK {
		t.Errorf("GET /assets/%s: %d %s", id, res.status, truncate(res.body))
		return nil
	}
	return decodeMap(res.body)
}

// expectResult checks the result of a write.
func (t *T) expectResult(what string, res *obx.IngestResult, err error, want string) {
	if err != nil {
		if p, ok := err.(*obx.Problem); ok && p.Status == http.StatusUnprocessableEntity {
			t.Fatalf("%s: 422 %s; an unknown member or extension is never a reason to refuse a write (ingest.md 3.1): %s", what, p.ObxError, obx.Str(p.Detail))
		}
		t.Fatalf("%s: %v", what, err)
	}
	if res.Result != want {
		t.Errorf("%s: result %s, want %s", what, res.Result, want)
	}
}

// testExtensions is I-EXT-1 (ingest.md 3.1): x-<vendor>-<name> members are
// stored and returned everywhere, every other unknown member is ignored
// with a warning, never a 422.
func testExtensions(t *T) {
	const pext, vext, aext = "KO-1007", "KO-1007-SND-M", "KO-1007_SND_1"
	raw, err := t.r.fixture("examples/ProductInput.extension.json")
	t.Check(err, "read fixtures/examples/ProductInput.extension.json")
	product := decodeMap(raw)
	if product == nil {
		t.Fatalf("ProductInput.extension.json is not an object")
	}
	if _, ok := product["brand_external_id"]; !ok {
		product["brand_external_id"] = "kestrel"
	}
	cleanup := func() {
		_, _ = t.r.ing.Delete(t.ctx, obx.KindAsset, aext)
		_, _ = t.r.ing.Delete(t.ctx, obx.KindVariant, vext)
		_, _ = t.r.ing.Delete(t.ctx, obx.KindProduct, pext)
	}
	cleanup()
	defer cleanup()
	careNote := product["x-kestrel-care-note"]
	mill, _ := lookup(product, jsonPath{"materials", 0, "fibers", 0, "material", "x-kestrel-mill"})
	if careNote == nil || mill == nil || product["x-kestrel-fabric-weight-gsm"] == nil {
		t.Fatalf("ProductInput.extension.json lacks the extension members this test checks")
	}
	sent := toAny(product).(map[string]any)
	sent["unknown_member"] = "not stored"
	fibre, _ := lookup(sent, jsonPath{"materials", 0, "fibers", 0})
	fibre.(map[string]any)["unknown_nested"] = true
	unknown := []string{"unknown_member", "unknown_nested"}

	var cursor string
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	res, err := t.r.ing.Put(t.ctx, obx.KindProduct, pext, sent, nil)
	t.expectResult("PUT "+pext+" with extensions and unknown members", res, err, obx.ResultCreated)
	warn := strings.Join(res.Warnings, "\n")
	for _, k := range unknown {
		if !strings.Contains(warn, k) {
			t.Errorf("PUT %s: the warnings do not name the unknown member %s: %q", pext, k, res.Warnings)
		}
	}
	all := []extCheck{
		{jsonPath{"x-kestrel-fabric-weight-gsm"}, product["x-kestrel-fabric-weight-gsm"]},
		{jsonPath{"x-kestrel-care-note"}, careNote},
		{jsonPath{"materials", 0, "fibers", 0, "material", "x-kestrel-mill"}, mill},
	}
	cs, cursor, _ := t.carriers(obx.KindProduct, pext, cursor, t.protocolProduct)
	for _, c := range cs {
		t.checkCarrier(c.where, c.obj, all, unknown)
	}
	res, err = t.r.ing.Put(t.ctx, obx.KindProduct, pext, sent, nil)
	t.expectResult("the same PUT of "+pext+" again", res, err, obx.ResultUnchanged)
	if t.r.full != nil {
		_, cursor = t.newEntries(t.r.full, cursor)
	}
	res, err = t.r.ing.Patch(t.ctx, obx.KindProduct, pext, map[string]any{"x-kestrel-fabric-weight-gsm": nil}, nil)
	t.expectResult("PATCH "+pext+" {\"x-kestrel-fabric-weight-gsm\": null}", res, err, obx.ResultUpdated)
	all[0].want = nil
	cs, cursor, live := t.carriers(obx.KindProduct, pext, cursor, t.protocolProduct)
	for _, c := range cs {
		t.checkCarrier(c.where+" after the PATCH", c.obj, all, nil)
	}
	if t.r.full != nil && res.ID != nil {
		if n := countEntries(live, obx.ChangeProduct, *res.ID); n != 1 {
			t.Errorf("PATCH that removes an extension: %d product entries, want one upsert", n)
		}
	}

	// A variant: x-kestrel-bin on the variant, x-kestrel-pantone in color.
	raw, err = t.r.fixture("examples/VariantInput.example.json")
	t.Check(err, "read fixtures/examples/VariantInput.example.json")
	variant := decodeMap(raw)
	variant["product_external_id"] = pext
	variant["x-kestrel-bin"] = "A-12-3"
	if c, ok := variant["color"].(map[string]any); ok {
		c["x-kestrel-pantone"] = "13-1008 TCX"
	}
	variant["unknown_member"] = "not stored"
	vchecks := []extCheck{{jsonPath{"x-kestrel-bin"}, "A-12-3"}, {jsonPath{"color", "x-kestrel-pantone"}, "13-1008 TCX"}}
	t.extensionRoundTrip(obx.KindVariant, vext, variant, vchecks, cursor, t.protocolVariant)

	// An asset: x-kestrel-shoot.
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	prep := &ingest.Preparer{Client: t.r.ing}
	img, err := prep.Prepare(t.ctx, ingest.ImageSource{Name: aext + ".png", Data: uniquePNG(0)})
	t.Check(err, "upload an image")
	files := map[string]any{"original": map[string]any{"sha256": img.Manifest.SHA256}}
	if r := img.Manifest.Renditions; r != nil && t.r.info != nil && !t.r.info.Renditions.ServerGenerated {
		for name, ref := range map[string]*obx.BlobRef{"web": r.Web, "square": r.Square, "thumb": r.Thumb} {
			if ref != nil {
				files[name] = map[string]any{"sha256": ref.SHA256, "width": ref.Width, "height": ref.Height}
			}
		}
	}
	asset := map[string]any{"product_external_id": pext, "color_code": "SND", "role": obx.RolePackshot, "position": 1, "files": files,
		"x-kestrel-shoot": "SS27 studio 4", "unknown_member": "not stored"}
	t.extensionRoundTrip(obx.KindAsset, aext, asset, []extCheck{{jsonPath{"x-kestrel-shoot"}, "SS27 studio 4"}}, cursor, t.protocolAsset)
}

// extensionRoundTrip PUTs body (with extensions and unknown_member), checks
// the carriers, PUTs it again (unchanged), PATCHes the first extension to
// null (updated, gone, the others stay).
func (t *T) extensionRoundTrip(kind, ext string, body map[string]any, checks []extCheck, cursor string, protocol func(string) map[string]any) {
	res, err := t.r.ing.Put(t.ctx, kind, ext, body, nil)
	t.expectResult("PUT "+kind+" "+ext+" with extensions and an unknown member", res, err, obx.ResultCreated)
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "unknown_member") {
		t.Errorf("PUT %s %s: the warnings do not name the unknown member unknown_member: %q", kind, ext, res.Warnings)
	}
	if kind == obx.KindAsset {
		t.waitNotPending(ext)
	}
	cs, cursor, _ := t.carriers(kind, ext, cursor, protocol)
	for _, c := range cs {
		t.checkCarrier(c.where, c.obj, checks, []string{"unknown_member"})
	}
	res, err = t.r.ing.Put(t.ctx, kind, ext, body, nil)
	t.expectResult("the same PUT of "+kind+" "+ext+" again", res, err, obx.ResultUnchanged)
	if t.r.full != nil {
		_, cursor = t.newEntries(t.r.full, cursor)
	}
	name := fmt.Sprint(checks[0].path[0])
	res, err = t.r.ing.Patch(t.ctx, kind, ext, map[string]any{name: nil}, nil)
	t.expectResult(fmt.Sprintf("PATCH %s %s {%q: null}", kind, ext, name), res, err, obx.ResultUpdated)
	after := append([]extCheck{{checks[0].path, nil}}, checks[1:]...)
	cs, _, live := t.carriers(kind, ext, cursor, protocol)
	for _, c := range cs {
		t.checkCarrier(c.where+" after the PATCH", c.obj, after, nil)
	}
	if t.r.full != nil && res.ID != nil {
		if n := countEntries(live, kind, *res.ID); n != 1 {
			t.Errorf("PATCH that removes an extension of %s %s: %d entries, want one upsert", kind, ext, n)
		}
	}
}

// testDraftQualifier is I-MAT-1 (ingest.md 3.1): the draft single
// qualifier is read as a list of one (qualifiers wins, null is none), with
// one warning qualifier_deprecated, never a 422; only qualifiers is stored
// and returned.
func testDraftQualifier(t *T) {
	const ext = "OBX-CONF-MAT1"
	fibres := func(draft bool) []any {
		if draft {
			return []any{
				map[string]any{"material": map[string]any{"code": "polyester"}, "percentage": 60, "qualifier": map[string]any{"code": "recycled"}},
				map[string]any{"material": map[string]any{"code": "cotton"}, "percentage": 30, "qualifier": map[string]any{"code": "organic"}, "qualifiers": []any{map[string]any{"code": "regenerative"}}},
				map[string]any{"material": map[string]any{"code": "elastane"}, "percentage": 10, "qualifier": nil},
			}
		}
		return []any{
			map[string]any{"material": map[string]any{"code": "polyester"}, "percentage": 60, "qualifiers": []any{map[string]any{"code": "recycled"}}},
			map[string]any{"material": map[string]any{"code": "cotton"}, "percentage": 30, "qualifiers": []any{map[string]any{"code": "regenerative"}}},
			map[string]any{"material": map[string]any{"code": "elastane"}, "percentage": 10, "qualifiers": []any{}},
		}
	}
	body := func(draft bool) map[string]any {
		return map[string]any{"brand_external_id": "kestrel", "style_number": ext, "name": map[string]any{"en": "Conformance Draft Qualifier Tee"},
			"materials": []any{map[string]any{"part": map[string]any{"code": "main"}, "fibers": fibres(draft)}}}
	}
	_, _ = t.r.ing.Delete(t.ctx, obx.KindProduct, ext)
	defer func() { _, _ = t.r.ing.Delete(t.ctx, obx.KindProduct, ext) }()
	var cursor string
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	res, err := t.r.ing.Put(t.ctx, obx.KindProduct, ext, body(true), nil)
	if p, ok := err.(*obx.Problem); ok && p.Status == http.StatusUnprocessableEntity {
		t.Fatalf("PUT with the draft single qualifier: 422 %s; it is read as a list of one, never refused (ingest.md 3.1): %s", p.ObxError, obx.Str(p.Detail))
	}
	t.expectResult("PUT "+ext+" with the draft single qualifier", res, err, obx.ResultCreated)
	var dep []obx.WarningDetail
	for _, w := range res.WarningDetails {
		if w.Code == obx.WarnQualifierDeprecated {
			dep = append(dep, w)
		}
	}
	switch {
	case len(dep) != 1:
		t.Errorf("PUT with the draft single qualifier: %d warning_details %s, want exactly one per write: %s", len(dep), obx.WarnQualifierDeprecated, short(res.WarningDetails))
	case dep[0].Pointer != nil && *dep[0].Pointer != "/materials/0/fibers/0/qualifier":
		t.Errorf("the warning %s points to %s, want /materials/0/fibers/0/qualifier (the first such fibre)", obx.WarnQualifierDeprecated, *dep[0].Pointer)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "qualifier") {
		t.Errorf("the warning %s is not listed as text in warnings: %q", obx.WarnQualifierDeprecated, res.Warnings)
	}
	want := [][]string{{"recycled"}, {"regenerative"}, {}}
	cs, _, _ := t.carriers(obx.KindProduct, ext, cursor, t.protocolProduct)
	for _, c := range cs {
		if paths := findKey(c.obj, "qualifier", nil); len(paths) > 0 {
			t.Errorf("%s has a qualifier member at %s; only qualifiers is stored and returned", c.where, strings.Join(paths, ", "))
		}
		fibers, _ := lookup(c.obj, jsonPath{"materials", 0, "fibers"})
		list, _ := fibers.([]any)
		if len(list) != len(want) {
			t.Errorf("%s: %d fibres, want 3", c.where, len(list))
			continue
		}
		for i, f := range list {
			fm, _ := f.(map[string]any)
			qs, isList := fm["qualifiers"].([]any)
			got := []string{}
			for _, q := range qs {
				got = append(got, termCode(q))
			}
			if !isList || strings.Join(got, ",") != strings.Join(want[i], ",") {
				t.Errorf("%s: fibre %d (%s) has the qualifier codes %v, want %v", c.where, i, termCode(fm["material"]), short(fm["qualifiers"]), want[i])
			}
		}
	}
	res, err = t.r.ing.Put(t.ctx, obx.KindProduct, ext, body(false), nil)
	t.expectResult("the same product written with qualifiers only", res, err, obx.ResultUnchanged)
}

// testVariantIdentity is I-IMP-12 (import-format.md 1.2.1): the files of
// fixtures/imports/variant-identity.expected.json in order, and the default
// external ids of the Kestrel variants.
func testVariantIdentity(t *T) {
	raw, err := t.r.fixture("imports/variant-identity.expected.json")
	t.Check(err, "read fixtures/imports/variant-identity.expected.json")
	var exp struct {
		Cases []importCase `json:"cases"`
	}
	t.Check(json.Unmarshal(raw, &exp), "parse variant-identity.expected.json")
	const ext = "KL-8101"
	_, _ = t.r.ing.Delete(t.ctx, obx.KindProduct, ext)
	defer func() { _, _ = t.r.ing.Delete(t.ctx, obx.KindProduct, ext) }()
	ids := map[string]string{} // variant external id → protocol id after the first file
	for _, c := range exp.Cases {
		var cursor string
		if t.r.full != nil {
			cursor = t.liveCursor(t.r.full)
		}
		job, probs := t.productsCSVImport(c.File, false)
		name := c.File
		if job.Status != obx.JobSucceeded || job.ImportRun == nil {
			t.Errorf("%s: job %s (%s)", name, job.Status, short(probs))
			continue
		}
		got := job.ImportRun.Counts
		if got.Rows != c.Counts.Rows || got.Created != c.Counts.Created || got.Updated != c.Counts.Updated || got.Unchanged != c.Counts.Unchanged || got.Errors != c.Counts.Errors {
			t.Errorf("%s (%s): counts rows %d, created %d, updated %d, unchanged %d, errors %d; want %d, %d, %d, %d, %d (%s)", name, c.What,
				got.Rows, got.Created, got.Updated, got.Unchanged, got.Errors,
				c.Counts.Rows, c.Counts.Created, c.Counts.Updated, c.Counts.Unchanged, c.Counts.Errors, short(errorsOf(probs)))
		}
		t.matchRowErrors(name, errorsOf(probs), c.Errors)
		if t.r.full == nil {
			continue
		}
		live, _ := t.newEntries(t.r.full, cursor)
		var gotKeys, wantKeys []string
		for _, ch := range live {
			gotKeys = append(gotKeys, ch.Type+" "+ch.Op+" "+ch.ExternalIDValue())
		}
		for _, w := range c.Changes {
			wantKeys = append(wantKeys, w.Type+" "+w.Op+" "+w.ExternalID)
		}
		if strings.Join(gotKeys, "; ") != strings.Join(wantKeys, "; ") {
			t.Errorf("%s: change entries [%s], want [%s]", name, strings.Join(gotKeys, "; "), strings.Join(wantKeys, "; "))
		}
		for pext, fields := range c.Products {
			rec, err := t.r.ing.Get(t.ctx, obx.KindProduct, pext)
			if err != nil {
				t.Errorf("%s: product %s: %v", name, pext, err)
				continue
			}
			obj := t.protocolProduct(rec.ID)
			for field, want := range fields {
				t.matchSubset(fmt.Sprintf("%s: product %s /%s", name, pext, field), obj[field], want)
			}
			vs, _ := obj["variants"].([]any)
			for _, v := range vs {
				vm, _ := v.(map[string]any)
				vext, id := fmt.Sprint(vm["external_id"]), fmt.Sprint(vm["id"])
				if prev, ok := ids[vext]; ok && prev != id {
					t.Errorf("%s: variant %s changed its protocol id from %s to %s; a variant keeps its id", name, vext, prev, id)
				}
				ids[vext] = id
			}
		}
	}
	if t.r.full != nil {
		t.checkKestrelVariantIDs()
	}
}

// matchSubset matches like fixtures/expected, but objects (also inside
// arrays) are compared only on the expected members.
func (t *T) matchSubset(where string, actual, want any) {
	switch w := want.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			t.Errorf("%s: got %s, want an object", where, short(actual))
			return
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			av, ok := a[k]
			if !ok {
				t.Errorf("%s/%s: missing", where, k)
				continue
			}
			t.matchSubset(where+"/"+k, av, w[k])
		}
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(w) {
			t.Errorf("%s: got %s, want %d elements: %s", where, short(actual), len(w), short(want))
			return
		}
		for i := range w {
			t.matchSubset(fmt.Sprintf("%s/%d", where, i), a[i], w[i])
		}
	default:
		for _, d := range matchExpected(actual, want, where, nil) {
			t.Errorf("%s", d)
		}
	}
}

// checkKestrelVariantIDs: the variants imported from fixtures/kestrel have
// the default external ids <style>-<color>-<size>[-<size_length>]
// (import-format.md 1.2.1), although products.csv has their GTINs.
func (t *T) checkKestrelVariantIDs() {
	data, err := t.r.fixture("kestrel/products.csv")
	t.Check(err, "read kestrel/products.csv")
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	t.Check(err, "parse kestrel/products.csv")
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	cell := func(r []string, name string) string {
		if i, ok := col[name]; ok && i < len(r) {
			return r[i]
		}
		return ""
	}
	want := map[string]bool{}
	styles := map[string]bool{}
	for _, r := range rows[1:] {
		styles[cell(r, "style_number")] = true
		id := cell(r, "variant_id")
		if id == "" {
			id = canonicalcsv.DefaultVariantID(cell(r, "style_number"), cell(r, "color_code"), cell(r, "size"), cell(r, "size_length"))
		}
		want[id] = true
	}
	got := map[string]bool{}
	t.Check(t.r.full.EachProduct(t.ctx, client.ProductsQuery{Limit: 100}, func(p *obx.ProductDetail) error {
		if !styles[p.StyleNumber] {
			return nil
		}
		for _, v := range p.Variants {
			got[v.ExternalID] = true
		}
		return nil
	}), "list products")
	var missing, extra []string
	for id := range want {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	for id := range got {
		if !want[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("the Kestrel variants must have the external ids <style>-<color>-<size>[-<size_length>] (import-format.md 1.2.1): missing %v, unexpected %v", missing, extra)
	}
}
