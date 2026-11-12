package conformance

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/renditions"
)

func init() {
	p := ProfileIngest
	register(p, "I-INFO-1", "GET /obx/v1/ingest is a valid IngestInfo with at least the minimum limits", true, testIngestInfo)
	register(p, "I-AUTH-1", "missing scope → insufficient_scope; brand-restricted key cannot write another brand", true, testIngestAuth)
	register(p, "I-REPLAY-1", "replaying fixtures/ingest one operation at a time gives the expected results and change entries", true, testReplay)
	register(p, "I-REPLAY-2", "the same script as one JSON batch and as NDJSON gives the same results and entries", true, testReplayBatch)
	register(p, "I-IDEM-1", "repeated upsert is unchanged with no entry; Idempotency-Key replays the answer", true, testIdempotency)
	register(p, "I-FILE-1", "blob HEAD/PUT dedupe, checksum_mismatch, blob_missing", true, testBlobs)
	register(p, "I-FILE-2", "resumable multipart upload of a 12 MiB file with a skipped part", true, testMultipart)
	register(p, "I-VAL-1", "invalid GTIN, duplicate GTIN, position conflict, unknown parent", true, testValidation)
	register(p, "I-IMP-1", "re-importing fixtures/kestrel as package and as files is unchanged with no entries", true, testReimport)
	register(p, "I-IMP-2", "import with an images_manifest gives the same result", true, testManifestImport)
	register(p, "I-IMP-3", "mirror import unpublishes exactly what is missing; one of the nine Kestrel products → mass_unpublish_refused; zip slip refused", true, testMirror)
	register(p, "I-IMP-4", "row problems carry file, row, column and code; the job still succeeds", true, testRowErrors)
	register(p, "I-IMP-5", "an import without assets.csv keeps the images' embargo, credit, alt text and AI label", true, testImportKeepsImageFacts)
	register(p, "I-IMP-6", "a column the file does not have keeps the field; an empty cell removes it", true, testImportKeepsAbsentColumns)
	register(p, "I-IMP-7", "the 0.2 columns: fixtures/imports 1 to 6 give the expected counts, errors, change entries and fields (rule 9 per key)", true, testImportArticleColumns)
	register(p, "I-IMP-8", "label completeness warnings: label-warnings.csv as a dry run gives exactly the expected codes; fixtures/kestrel none", true, testLabelWarnings)
}

func testIngestInfo(t *T) {
	res, err := t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathIngest, nil, bearerHeader(t.r.cfg.IngestToken))
	t.Check(err, "GET /obx/v1/ingest")
	if res.status != http.StatusOK {
		t.Fatalf("status %d %s", res.status, truncate(res.body))
	}
	t.validate("IngestInfo", res.body, "the ingest info")
	var info obx.IngestInfo
	res.decode(&info)
	l := info.Limits
	checks := []struct {
		name      string
		got, want int64
	}{
		{"max_upload_bytes", l.MaxUploadBytes, 25 << 20},
		{"max_object_bytes", l.MaxObjectBytes, 500 << 20},
		{"part_bytes", l.PartBytes, 5 << 20},
		{"max_batch_operations", int64(l.MaxBatchOperations), 1000},
		{"max_import_bytes", l.MaxImportBytes, 500 << 20},
	}
	for _, c := range checks {
		if c.got < c.want {
			t.Errorf("limits.%s is %d, the minimum is %d", c.name, c.got, c.want)
		}
	}
	if !info.Renditions.ServerGenerated && len(info.Renditions.Required) != 4 {
		t.Errorf("a node without server renditions must require all four renditions, got %v", info.Renditions.Required)
	}
	for _, ct := range []string{"image/jpeg", "image/png", "image/webp"} {
		if !obx.Contains(info.AcceptedContentTypes, ct) {
			t.Errorf("accepted_content_types lacks %s", ct)
		}
	}
	if t.r.discovery != nil && t.r.discovery.Has(obx.CapServerRenditions) != info.Renditions.ServerGenerated {
		t.Errorf("discovery capability ingest.server_renditions and renditions.server_generated disagree")
	}
}

func testIngestAuth(t *T) {
	res, err := t.r.do(t.ctx, http.MethodGet, t.r.cfg.BaseURL+obx.PathIngest, nil, nil)
	t.Check(err, "GET")
	t.expectStatus(res, http.StatusUnauthorized, obx.ErrUnauthorized, "no token")
	t.needAdmin("creating restricted upload keys")
	ro, err := t.r.admin.CreateIngestToken(t.ctx, nodeadmin.IngestTokenInput{Name: "OBX conformance: read only", Scopes: []string{obx.IngestScopeRead}})
	t.Check(err, "create read-only key")
	c := t.r.newIngest(ro.Secret)
	_, err = c.PutBrand(t.ctx, "kestrel", obx.BrandInput{Name: "Kestrel"}, nil)
	if !obx.IsCode(err, obx.ErrInsufficientScope) {
		t.Errorf("write with an ingest:read key: want 403 insufficient_scope, got %v", err)
	}
	_, err = c.Delete(t.ctx, obx.KindProduct, "KO-1001")
	if !obx.IsCode(err, obx.ErrInsufficientScope) {
		t.Errorf("delete with an ingest:read key: want 403 insufficient_scope, got %v", err)
	}
	rs, err := t.r.admin.CreateIngestToken(t.ctx, nodeadmin.IngestTokenInput{Name: "OBX conformance: kestrel only", Scopes: []string{obx.IngestScopeRead, obx.IngestScopeWrite}, BrandExternalIDs: []string{"kestrel"}})
	t.Check(err, "create restricted key")
	c = t.r.newIngest(rs.Secret)
	cur, err := t.r.ing.Get(t.ctx, obx.KindProduct, "KT-2001")
	t.Check(err, "read KT-2001")
	_, err = c.Put(t.ctx, obx.KindProduct, "KT-2001", cur.Data, nil)
	if !obx.IsCode(err, obx.ErrForbidden) {
		t.Errorf("restricted key writing brand kestrel-trail: want 403 forbidden, got %v", err)
	}
	own, err := t.r.ing.Get(t.ctx, obx.KindProduct, "KO-1001")
	t.Check(err, "read KO-1001")
	if res, err := c.Put(t.ctx, obx.KindProduct, "KO-1001", own.Data, nil); err != nil || res.Result != obx.ResultUnchanged {
		t.Errorf("restricted key writing its own brand: want unchanged, got %v %v", res, err)
	}
	t.Check(t.r.admin.RevokeIngestToken(t.ctx, ro.Token.ID), "revoke")
	t.Check(t.r.admin.RevokeIngestToken(t.ctx, rs.Token.ID), "revoke")
	_, err = c.Info(t.ctx)
	if p, ok := err.(*obx.Problem); !ok || p.Status != http.StatusUnauthorized {
		t.Errorf("a revoked upload key must get 401, got %v", err)
	}
}

// ---- replay

type scriptOp struct {
	Op         string          `json:"op"`
	Kind       string          `json:"kind"`
	ExternalID string          `json:"external_id"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type expectedResult struct {
	Result   string `json:"result"`
	ObxError string `json:"obx_error"`
}

type expectedChange struct {
	AfterOp    int    `json:"after_op"`
	Type       string `json:"type"`
	Op         string `json:"op"`
	ExternalID string `json:"external_id"`
}

func (t *T) loadScript() ([]scriptOp, []expectedResult, []expectedChange) {
	raw, err := t.r.fixture("ingest/script.ndjson")
	t.Check(err, "script.ndjson")
	var ops []scriptOp
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var op scriptOp
		t.Check(json.Unmarshal([]byte(line), &op), "script line")
		ops = append(ops, op)
	}
	var results []expectedResult
	b, err := t.r.fixture("ingest/expected-results.json")
	t.Check(err, "expected-results.json")
	t.Check(json.Unmarshal(b, &results), "expected-results.json")
	var changes struct {
		Changes []expectedChange `json:"changes"`
	}
	b, err = t.r.fixture("ingest/expected-changes.json")
	t.Check(err, "expected-changes.json")
	t.Check(json.Unmarshal(b, &changes), "expected-changes.json")
	return ops, results, changes.Changes
}

// uploadScriptFiles uploads ingest/files and returns, for client-rendering
// nodes, the renditions per original sha256.
func (t *T) uploadScriptFiles() map[string]*obx.ManifestRenditions {
	b, err := t.r.fixture("ingest/files/files.json")
	t.Check(err, "files.json")
	var files map[string]struct {
		SHA256 string `json:"sha256"`
	}
	t.Check(json.Unmarshal(b, &files), "files.json")
	prep := &ingest.Preparer{Client: t.r.ing}
	out := map[string]*obx.ManifestRenditions{}
	for name, f := range files {
		data, err := t.r.fixture("ingest/files/" + name)
		t.Check(err, name)
		p, err := prep.Prepare(t.ctx, ingest.ImageSource{Name: name, Data: data})
		t.Check(err, "upload "+name)
		if p.Manifest.SHA256 != f.SHA256 {
			t.Fatalf("%s hashes to %s, files.json says %s", name, p.Manifest.SHA256, f.SHA256)
		}
		out[f.SHA256] = p.Manifest.Renditions
	}
	return out
}

// withRenditions adds client-generated renditions to data.files.
func withRenditions(data json.RawMessage, rend map[string]*obx.ManifestRenditions) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	files, _ := m["files"].(map[string]any)
	orig, _ := files["original"].(map[string]any)
	sha, _ := orig["sha256"].(string)
	r := rend[sha]
	if r == nil {
		return data
	}
	for name, ref := range map[string]*obx.BlobRef{"web": r.Web, "square": r.Square, "thumb": r.Thumb} {
		files[name] = map[string]any{"sha256": ref.SHA256, "width": ref.Width, "height": ref.Height}
	}
	out, _ := json.Marshal(m)
	return out
}

// suffixed returns op with every external id reference suffixed.
func suffixed(op scriptOp, sfx string) scriptOp {
	op.ExternalID += sfx
	if len(op.Data) > 0 {
		var m map[string]any
		json.Unmarshal(op.Data, &m)
		for _, k := range []string{"brand_external_id", "product_external_id"} {
			if v, ok := m[k].(string); ok {
				m[k] = v + sfx
			}
		}
		op.Data, _ = json.Marshal(m)
	}
	return op
}

// sendOp performs one script operation through the matching endpoint.
func (t *T) sendOp(op scriptOp) (*obx.IngestResult, error) {
	switch op.Op {
	case obx.IngestOpUpsert:
		return t.r.ing.Put(t.ctx, op.Kind, op.ExternalID, op.Data, nil)
	case obx.IngestOpPatch:
		return t.r.ing.Patch(t.ctx, op.Kind, op.ExternalID, op.Data, nil)
	case obx.IngestOpPublish:
		return t.r.ing.Publish(t.ctx, op.Kind, op.ExternalID)
	case obx.IngestOpUnpublish:
		return t.r.ing.Unpublish(t.ctx, op.Kind, op.ExternalID)
	case obx.IngestOpDelete:
		return t.r.ing.Delete(t.ctx, op.Kind, op.ExternalID)
	}
	return nil, fmt.Errorf("unknown op %q", op.Op)
}

// waitNotPending waits (max 60 s) until an asset is no longer pending_files.
func (t *T) waitNotPending(ext string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := t.r.ing.Get(t.ctx, obx.KindAsset, ext)
		if err != nil || rec.Visibility != obx.VisibilityPendingFiles {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Errorf("asset %s stayed pending_files for 60 s", ext)
}

func changeKeys(chs []obx.Change) []string {
	var out []string
	for _, c := range chs {
		k := c.Type + " " + c.Op + " " + c.ExternalIDValue()
		if len(out) > 0 && out[len(out)-1] == k {
			continue // at-least-once duplicates
		}
		out = append(out, k)
	}
	return out
}

func expectedKeys(exp []expectedChange, op int, sfx string) []string {
	var out []string
	for _, e := range exp {
		if op < 0 || e.AfterOp == op {
			out = append(out, e.Type+" "+e.Op+" "+e.ExternalID+sfx)
		}
	}
	return out
}

func (t *T) cleanupBrand(ext string) {
	_, err := t.r.ing.Delete(t.ctx, obx.KindBrand, ext)
	if err != nil {
		t.Notef("cleanup of brand %s failed: %v", ext, err)
	}
}

func testReplay(t *T) {
	if t.r.full == nil {
		t.Skip("needs an API key to read /changes")
	}
	ops, results, changes := t.loadScript()
	rend := t.uploadScriptFiles()
	client := t.r.info != nil && !t.r.info.Renditions.ServerGenerated
	cursor := t.liveCursor(t.r.full)
	var feedProblems []string
	for i, op := range ops {
		if client && op.Kind == obx.KindAsset && op.Op == obx.IngestOpUpsert {
			op.Data = withRenditions(op.Data, rend)
		}
		res, err := t.sendOp(op)
		want := results[i]
		if want.Result == obx.ResultError {
			if !obx.IsCode(err, want.ObxError) {
				t.Errorf("op %d (%s %s %s): want error %s, got %v %v", i, op.Op, op.Kind, op.ExternalID, want.ObxError, res, err)
			}
		} else if err != nil {
			t.Errorf("op %d (%s %s %s): want %s, got error %v", i, op.Op, op.Kind, op.ExternalID, want.Result, err)
		} else if res.Result != want.Result {
			t.Errorf("op %d (%s %s %s): want result %s, got %s", i, op.Op, op.Kind, op.ExternalID, want.Result, res.Result)
		}
		if err == nil && op.Kind == obx.KindAsset && op.Op == obx.IngestOpUpsert {
			t.waitNotPending(op.ExternalID)
		}
		var live []obx.Change
		live, cursor = t.newEntries(t.r.full, cursor)
		got, exp := strings.Join(changeKeys(live), "; "), strings.Join(expectedKeys(changes, i, ""), "; ")
		if got != exp {
			msg := fmt.Sprintf("after op %d (%s %s %s): /changes gave [%s], want [%s]", i, op.Op, op.Kind, op.ExternalID, got, exp)
			t.Errorf("%s", msg)
			feedProblems = append(feedProblems, msg)
		}
	}
	t.r.replayChanges = feedProblems
	t.r.replayRan = true
	t.cleanupBrand("kestrel-lab")
}

func testReplayFeed(t *T) {
	if !obx.Contains(t.r.report.Profiles, ProfileIngest) {
		t.Skip("needs the ingest profile too (the replay runs there)")
	}
	if !t.r.replayRan {
		t.Skip("I-REPLAY-1 did not run")
	}
	for _, p := range t.r.replayChanges {
		t.Errorf("%s", p)
	}
}

func testReplayBatch(t *T) {
	if t.r.full == nil {
		t.Skip("needs an API key to read /changes")
	}
	ops, results, changes := t.loadScript()
	rend := t.uploadScriptFiles()
	client := t.r.info != nil && !t.r.info.Renditions.ServerGenerated
	for _, variant := range []struct {
		sfx    string
		ndjson bool
	}{{"-b", false}, {"-c", true}} {
		var batch []obx.IngestOperation
		for _, op := range ops {
			op = suffixed(op, variant.sfx)
			if client && op.Kind == obx.KindAsset && op.Op == obx.IngestOpUpsert {
				op.Data = withRenditions(op.Data, rend)
			}
			batch = append(batch, obx.IngestOperation{Op: op.Op, Kind: op.Kind, ExternalID: op.ExternalID, Data: op.Data})
		}
		cursor := t.liveCursor(t.r.full)
		out, err := t.r.ing.Batch(t.ctx, batch, ingest.BatchOptions{NDJSON: variant.ndjson, PollInterval: 200 * time.Millisecond})
		t.Check(err, "POST /batch "+variant.sfx)
		if len(out.Results) != len(ops) {
			t.Fatalf("batch %s: %d results for %d operations", variant.sfx, len(out.Results), len(ops))
		}
		for i, res := range out.Results {
			want := results[i]
			if res.Index == nil || *res.Index != i {
				t.Errorf("batch %s result %d has index %v", variant.sfx, i, res.Index)
			}
			if res.Result != want.Result || (want.ObxError != "" && (res.Error == nil || res.Error.ObxError != want.ObxError)) {
				code := ""
				if res.Error != nil {
					code = res.Error.ObxError
				}
				t.Errorf("batch %s op %d: want %s %s, got %s %s", variant.sfx, i, want.Result, want.ObxError, res.Result, code)
			}
		}
		for _, op := range batch {
			if op.Kind == obx.KindAsset && op.Op == obx.IngestOpUpsert {
				if _, err := t.r.ing.Get(t.ctx, obx.KindAsset, op.ExternalID); err == nil {
					t.waitNotPending(op.ExternalID)
				}
			}
		}
		live, _ := t.newEntries(t.r.full, cursor)
		// Upserts of objects that are no longer visible when /changes is
		// read are omitted (protocol.md 6.2), so compare with what is still
		// visible after the whole batch.
		var exp []expectedChange
		for _, e := range changes {
			if e.Op == obx.OpUpsert {
				rec, err := t.r.ing.Get(t.ctx, e.Type, e.ExternalID+variant.sfx)
				if err != nil || rec.Visibility != obx.VisibilityVisible {
					continue
				}
			}
			exp = append(exp, e)
		}
		got, want := strings.Join(changeKeys(live), "; "), strings.Join(expectedKeys(exp, -1, variant.sfx), "; ")
		exp2 := want
		if got != exp2 {
			t.Errorf("batch %s: /changes gave [%s], want [%s]", variant.sfx, got, exp2)
		}
		t.cleanupBrand("kestrel-lab" + variant.sfx)
	}
}

func testIdempotency(t *T) {
	rec, err := t.r.ing.Get(t.ctx, obx.KindProduct, "KO-1001")
	t.Check(err, "GET KO-1001")
	var cursor string
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	res, err := t.r.ing.Put(t.ctx, obx.KindProduct, "KO-1001", rec.Data, nil)
	t.Check(err, "PUT KO-1001 again")
	if res.Result != obx.ResultUnchanged {
		t.Errorf("repeating an upsert: want unchanged, got %s", res.Result)
	}
	asset, err := t.r.ing.Get(t.ctx, obx.KindAsset, "KO-1001_NVY_1")
	t.Check(err, "GET asset")
	res, err = t.r.ing.Put(t.ctx, obx.KindAsset, "KO-1001_NVY_1", asset.Data, nil)
	t.Check(err, "PUT asset again")
	if res.Result != obx.ResultUnchanged {
		t.Errorf("repeating an asset upsert: want unchanged, got %s", res.Result)
	}
	if t.r.full != nil {
		if live, _ := t.newEntries(t.r.full, cursor); len(live) > 0 {
			t.Errorf("unchanged upserts wrote change entries: %v", changeKeys(live))
		}
	}
	key := "conformance-" + randomSuffix()
	ops := []obx.IngestOperation{{Op: obx.IngestOpUpsert, Kind: obx.KindProduct, ExternalID: "KO-1001", Data: rec.Data}}
	a, err := t.r.ing.Batch(t.ctx, ops, ingest.BatchOptions{IdempotencyKey: key, NoWait: true})
	t.Check(err, "batch")
	b, err := t.r.ing.Batch(t.ctx, ops, ingest.BatchOptions{IdempotencyKey: key, NoWait: true})
	t.Check(err, "batch again")
	switch {
	case a.Job != nil || b.Job != nil:
		if a.Job == nil || b.Job == nil || a.Job.ID != b.Job.ID {
			t.Errorf("the same Idempotency-Key must return the same job")
		}
	default:
		ja, _ := json.Marshal(a.Results)
		jb, _ := json.Marshal(b.Results)
		if !bytes.Equal(ja, jb) {
			t.Errorf("the same Idempotency-Key returned different answers")
		}
	}
	a, err = t.r.ing.Batch(t.ctx, ops, ingest.BatchOptions{IdempotencyKey: key + "-async", Async: true, NoWait: true})
	t.Check(err, "async batch")
	b, err = t.r.ing.Batch(t.ctx, ops, ingest.BatchOptions{IdempotencyKey: key + "-async", Async: true, NoWait: true})
	t.Check(err, "async batch again")
	if a.Job == nil || b.Job == nil {
		t.Errorf("Prefer: respond-async must answer 202 with a job")
	} else if a.Job.ID != b.Job.ID {
		t.Errorf("the same Idempotency-Key must return the same job (%s vs %s)", a.Job.ID, b.Job.ID)
	}
}

// uniquePNG returns a small PNG that no node has seen before, optionally
// padded after IEND to size bytes.
func uniquePNG(size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	seed := uint64(time.Now().UnixNano())
	rng := rand.New(rand.NewPCG(seed, seed>>7))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	for buf.Len() < size {
		chunk := make([]byte, min(1<<20, size-buf.Len()))
		for i := range chunk {
			chunk[i] = byte(rng.IntN(256))
		}
		buf.Write(chunk)
	}
	return buf.Bytes()
}

func testBlobs(t *T) {
	data := uniquePNG(0)
	sha := ingest.HashBytes(data)
	has, err := t.r.ing.HasBlob(t.ctx, sha)
	t.Check(err, "HEAD")
	if has {
		t.Errorf("HEAD of new bytes: want 404")
	}
	res, err := t.r.do(t.ctx, http.MethodPut, t.r.cfg.BaseURL+obx.PathIngest+"/blobs/"+sha, data, mergeHeader(bearerHeader(t.r.cfg.IngestToken), "Content-Type", "image/png"))
	t.Check(err, "PUT blob")
	if res.status != http.StatusCreated {
		t.Errorf("first PUT: want 201, got %d %s", res.status, truncate(res.body))
	}
	t.validate("Blob", res.body, "the blob")
	var bl obx.Blob
	res.decode(&bl)
	if bl.SHA256 != sha || bl.Width == nil || *bl.Width != 32 || !bl.Created {
		t.Errorf("blob answer %s", truncate(res.body))
	}
	if has, _ := t.r.ing.HasBlob(t.ctx, sha); !has {
		t.Errorf("HEAD after upload: want 200")
	}
	res, err = t.r.do(t.ctx, http.MethodPut, t.r.cfg.BaseURL+obx.PathIngest+"/blobs/"+sha, data, mergeHeader(bearerHeader(t.r.cfg.IngestToken), "Content-Type", "image/png"))
	t.Check(err, "PUT again")
	res.decode(&bl)
	if res.status != http.StatusOK || bl.Created {
		t.Errorf("second PUT: want 200 created=false, got %d %s", res.status, truncate(res.body))
	}
	other := uniquePNG(0)
	res, err = t.r.do(t.ctx, http.MethodPut, t.r.cfg.BaseURL+obx.PathIngest+"/blobs/"+sha, other, mergeHeader(bearerHeader(t.r.cfg.IngestToken), "Content-Type", "image/png"))
	t.Check(err, "PUT wrong bytes")
	t.expectStatus(res, http.StatusUnprocessableEntity, obx.ErrChecksumMismatch, "PUT with bytes that do not match the sha256")
	_, err = t.r.ing.PutAsset(t.ctx, "KO-1001_NVY_98", obx.AssetInput{ProductExternalID: "KO-1001", ColorCode: obx.Ptr("NVY"), Role: obx.RolePackshot, Position: 98,
		Files: &obx.AssetFiles{Original: &obx.FileRef{SHA256: strings.Repeat("0", 64)}}}, nil)
	if !obx.IsCode(err, obx.ErrBlobMissing) {
		t.Errorf("asset with an unknown sha256: want 422 blob_missing, got %v", err)
		t.r.ing.Delete(t.ctx, obx.KindAsset, "KO-1001_NVY_98")
	}
}

func testMultipart(t *T) {
	data := uniquePNG(12 << 20)
	sha := ingest.HashBytes(data)
	up, blob, err := t.r.ing.CreateUpload(t.ctx, sha, int64(len(data)), "image/png")
	t.Check(err, "POST /uploads")
	if blob != nil {
		t.Fatalf("the node claims to have random new bytes already")
	}
	raw, _ := json.Marshal(up)
	t.validate("Upload", raw, "the upload")
	if up.PartBytes < 5<<20 {
		t.Errorf("part_bytes %d is below 5 MiB", up.PartBytes)
	}
	part := func(n int) []byte {
		start := int64(n-1) * up.PartBytes
		return data[start:min(start+up.PartBytes, int64(len(data)))]
	}
	for n := 2; n <= up.PartsTotal; n++ {
		_, err := t.r.ing.PutUploadPart(t.ctx, up.ID, n, part(n))
		t.Check(err, fmt.Sprintf("part %d", n))
	}
	_, err = t.r.ing.CompleteUpload(t.ctx, up.ID)
	if !obx.IsCode(err, obx.ErrUploadIncomplete) {
		t.Errorf("complete with part 1 missing: want 409 upload_incomplete, got %v", err)
	}
	st, err := t.r.ing.Upload(t.ctx, up.ID)
	t.Check(err, "GET upload")
	if len(st.ReceivedParts) != up.PartsTotal-1 || obx.Contains(intsToStrings(st.ReceivedParts), "1") {
		t.Errorf("received_parts %v, want every part but 1", st.ReceivedParts)
	}
	_, err = t.r.ing.PutUploadPart(t.ctx, up.ID, 1, part(1))
	t.Check(err, "part 1")
	b, err := t.r.ing.CompleteUpload(t.ctx, up.ID)
	t.Check(err, "complete")
	if b.SHA256 != sha || b.Bytes != int64(len(data)) {
		t.Errorf("completed blob %+v", b)
	}
	if has, _ := t.r.ing.HasBlob(t.ctx, sha); !has {
		t.Errorf("HEAD after multipart: want 200")
	}
}

func intsToStrings(in []int) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = fmt.Sprint(n)
	}
	return out
}

func testValidation(t *T) {
	_, err := t.r.ing.PutVariant(t.ctx, "OBX-CONF-BAD", obx.VariantInput{ProductExternalID: "KO-1001", GTIN: obx.Ptr("2000100101019"), Color: obx.Color{Code: "NVY"}, Size: obx.Ptr("XXL")}, nil)
	if !obx.IsCode(err, obx.ErrInvalidGTIN) {
		t.Errorf("wrong check digit: want 422 invalid_gtin, got %v", err)
	}
	_, err = t.r.ing.PutVariant(t.ctx, "OBX-CONF-DUP", obx.VariantInput{ProductExternalID: "KO-1001", GTIN: obx.Ptr("2000100101018"), Color: obx.Color{Code: "NVY"}, Size: obx.Ptr("XXL")}, nil)
	if !obx.IsCode(err, obx.ErrGTINConflict) {
		t.Errorf("duplicate GTIN: want 409 gtin_conflict, got %v", err)
		t.r.ing.Delete(t.ctx, obx.KindVariant, "OBX-CONF-DUP")
	}
	existing, err := t.r.ing.Get(t.ctx, obx.KindAsset, "KO-1001_NVY_1")
	t.Check(err, "GET asset")
	var in obx.AssetInput
	json.Unmarshal(existing.Data, &in)
	in.Status = nil
	_, err = t.r.ing.PutAsset(t.ctx, "KO-1001_NVY_1b", in, nil)
	if !obx.IsCode(err, obx.ErrPositionConflict) {
		t.Errorf("second image at position 1: want 409 position_conflict, got %v", err)
		t.r.ing.Delete(t.ctx, obx.KindAsset, "KO-1001_NVY_1b")
	}
	_, err = t.r.ing.PutVariant(t.ctx, "OBX-CONF-ORPHAN", obx.VariantInput{ProductExternalID: "NO-SUCH-PRODUCT", Color: obx.Color{Code: "NVY"}}, nil)
	if !obx.IsCode(err, obx.ErrUnknownParent) {
		t.Errorf("unknown product: want 422 unknown_parent, got %v", err)
	}
	_, err = t.r.ing.PutProduct(t.ctx, "OBX-CONF-NOBRAND", obx.ProductInput{BrandExternalID: obx.Ptr("no-such-brand"), StyleNumber: "OBX-CONF-NOBRAND", Name: obx.LocalizedText{"en": "x"}}, nil)
	if !obx.IsCode(err, obx.ErrUnknownParent) {
		t.Errorf("unknown brand: want 422 unknown_parent, got %v", err)
	}
	res, err := t.r.do(t.ctx, http.MethodPut, t.r.cfg.BaseURL+obx.PathIngest+"/products/OBX-CONF-X", []byte(`{"name":`), mergeHeader(bearerHeader(t.r.cfg.IngestToken), "Content-Type", "application/json"))
	t.Check(err, "PUT malformed")
	if res.status != http.StatusBadRequest && res.status != http.StatusUnprocessableEntity {
		t.Errorf("malformed JSON: want 400 invalid_request, got %d", res.status)
	}
}

// ---- imports

// runImport sends an import and waits for the job.
func (t *T) runImport(req *ingest.ImportRequest) (*obx.IngestJob, []obx.ImportRowError) {
	job, err := t.r.ing.Import(t.ctx, *req)
	t.Check(err, "POST /imports")
	if job.Kind != "import" {
		t.Errorf("import job kind %q", job.Kind)
	}
	job, err = t.r.ing.WaitJob(t.ctx, job.ID, 200*time.Millisecond)
	t.Check(err, "wait for the import job")
	probs, err := t.r.ing.ImportProblems(t.ctx, job.ID)
	t.Check(err, "job results")
	return job, probs
}

func (t *T) expectUnchangedImport(name string, req *ingest.ImportRequest) {
	var cursor string
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	job, probs := t.runImport(req)
	if job.Status != obx.JobSucceeded || job.ImportRun == nil {
		t.Fatalf("%s: job %s (%s)", name, job.Status, short(probs))
	}
	c := job.ImportRun.Counts
	if c.Created != 0 || c.Updated != 0 || c.Errors != 0 {
		t.Errorf("%s of unchanged data: created %d, updated %d, errors %d (%s)", name, c.Created, c.Updated, c.Errors, short(probs))
	}
	if c.Unchanged == 0 {
		t.Errorf("%s: counts.unchanged is 0", name)
	}
	raw, _ := json.Marshal(job.ImportRun)
	t.validate("ImportRun", raw, name+" run")
	if t.r.full != nil {
		if live, _ := t.newEntries(t.r.full, cursor); len(live) > 0 {
			t.Errorf("%s of unchanged data wrote change entries: %v", name, changeKeys(live))
		}
	}
}

func testReimport(t *T) {
	for _, how := range []string{"package", "files"} {
		req, err := t.r.fixtureImport(t.ctx, how, nil)
		t.Check(err, "build import")
		t.expectUnchangedImport("import as "+how, req)
	}
	rec, err := t.r.ing.Get(t.ctx, obx.KindAsset, "KO-1003_RST_3")
	t.Check(err, "GET imported asset")
	if rec.Visibility != obx.VisibilityVisible {
		t.Errorf("imported asset KO-1003_RST_3 is %s", rec.Visibility)
	}
}

// testImportKeepsImageFacts: import-format.md rule 8. An import of products.csv
// and the images without assets.csv must not take away what the images have:
// the embargo and credit of KO-1002_BLK_2 and the alt text and AI label of
// KO-1003_RST_3 (both from the fixture's assets.csv), and a credit set on
// KO-1001_NVY_1 through the Ingest API, as the admin would.
func testImportKeepsImageFacts(t *T) {
	const credit = "Photo: Conformance Studio"
	_, err := t.r.ing.Patch(t.ctx, obx.KindAsset, "KO-1001_NVY_1", map[string]any{"rights": map[string]any{"credit": credit}}, nil)
	t.Check(err, "PATCH a credit onto KO-1001_NVY_1")
	defer func() {
		// Put the fixture state back for the checks that follow, also when this one failed.
		if _, err := t.r.ing.Patch(t.ctx, obx.KindAsset, "KO-1001_NVY_1", map[string]any{"rights": map[string]any{"credit": nil}}, nil); err != nil {
			t.Errorf("could not remove the test credit again: %v", err)
		}
		if req, err := t.r.fixtureImport(t.ctx, "files", nil); err == nil {
			t.runImport(req)
		}
	}()
	req, err := t.r.fixtureImport(t.ctx, "files", nil)
	t.Check(err, "build import")
	req.AssetsCSV = nil
	t.expectUnchangedImport("import of products.csv and the images without assets.csv", req)

	var data struct {
		AltText    map[string]string `json:"alt_text"`
		Rights     map[string]any    `json:"rights"`
		Provenance map[string]any    `json:"provenance"`
	}
	get := func(ext string) {
		data.AltText, data.Rights, data.Provenance = nil, nil, nil
		rec, err := t.r.ing.Get(t.ctx, obx.KindAsset, ext)
		t.Check(err, "GET "+ext)
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			t.Fatalf("%s: data is not an asset: %v", ext, err)
		}
	}
	get("KO-1002_BLK_2")
	if e, _ := data.Rights["embargo_until"].(string); !strings.HasPrefix(e, "2030-01-01") {
		t.Errorf("KO-1002_BLK_2 lost its embargo: rights.embargo_until is %v, want 2030-01-01", data.Rights["embargo_until"])
	}
	if c, _ := data.Rights["credit"].(string); c != "Photo: Kestrel Studio" {
		t.Errorf("KO-1002_BLK_2 lost its credit: rights.credit is %v", data.Rights["credit"])
	}
	get("KO-1003_RST_3")
	if ai, _ := data.Provenance["ai_generated"].(bool); !ai {
		t.Errorf("KO-1003_RST_3 lost its AI label: provenance.ai_generated is %v", data.Provenance["ai_generated"])
	}
	if data.AltText["nl"] == "" || data.AltText["en"] == "" {
		t.Errorf("KO-1003_RST_3 lost its alt text: %v", data.AltText)
	}
	get("KO-1001_NVY_1")
	if c, _ := data.Rights["credit"].(string); c != credit {
		t.Errorf("KO-1001_NVY_1 lost the credit set through the Ingest API: rights.credit is %v", data.Rights["credit"])
	}
}

// testImportKeepsAbsentColumns: import-format.md rule 9. A products.csv with
// only brand, style_number and name:en (the fixture's values) must keep every
// other field of the products (name:nl, descriptions, materials, care); an
// empty season cell in a file that has the column removes the season.
func testImportKeepsAbsentColumns(t *T) {
	data, err := t.r.fixture("kestrel/products.csv")
	t.Check(err, "read kestrel/products.csv")
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	t.Check(err, "parse kestrel/products.csv")
	col := func(name string) int {
		for i, h := range rows[0] {
			if h == name {
				return i
			}
		}
		t.Fatalf("kestrel/products.csv has no %s column", name)
		return -1
	}
	brand, style, nameEN, season, nameNL := col("brand"), col("style_number"), col("name:en"), col("season"), col("name:nl")
	var wantNL string
	build := func(withSeason bool, emptySeasonFor string) []byte {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		head := []string{"brand", "style_number", "name:en"}
		if withSeason {
			head = append(head, "season")
		}
		_ = w.Write(head)
		seen := map[string]bool{}
		for _, r := range rows[1:] {
			if seen[r[style]] {
				continue
			}
			seen[r[style]] = true
			if r[style] == "KO-1001" {
				wantNL = r[nameNL]
			}
			rec := []string{r[brand], r[style], r[nameEN]}
			if withSeason {
				v := r[season]
				if r[style] == emptySeasonFor {
					v = ""
				}
				rec = append(rec, v)
			}
			_ = w.Write(rec)
		}
		w.Flush()
		return buf.Bytes()
	}
	defer func() {
		// Put the fixture state back for the checks that follow, also when this one failed.
		if req, err := t.r.fixtureImport(t.ctx, "files", nil); err == nil {
			t.runImport(req)
		}
	}()
	var product struct {
		Name        map[string]string `json:"name"`
		Description map[string]string `json:"description"`
		Season      *string           `json:"season"`
		Materials   []any             `json:"materials"`
		Care        []any             `json:"care"`
	}
	get := func() {
		product.Name, product.Description, product.Season, product.Materials, product.Care = nil, nil, nil, nil, nil
		rec, err := t.r.ing.Get(t.ctx, obx.KindProduct, "KO-1001")
		t.Check(err, "GET product KO-1001")
		if err := json.Unmarshal(rec.Data, &product); err != nil {
			t.Fatalf("KO-1001: data is not a product: %v", err)
		}
	}
	only := &ingest.ImportRequest{Options: obx.ImportOptions{Mode: obx.ImportModeUpsert}, CSV: &ingest.File{Name: "products.csv", Data: build(false, "")}}
	t.expectUnchangedImport("products.csv with only brand, style_number and name:en", only)
	get()
	if product.Name["nl"] != wantNL || product.Description["en"] == "" || len(product.Materials) == 0 || len(product.Care) == 0 {
		t.Errorf("KO-1001 lost fields whose columns the file did not have: name %v, description %v, materials %d, care %d", product.Name, product.Description, len(product.Materials), len(product.Care))
	}
	withSeason := &ingest.ImportRequest{Options: obx.ImportOptions{Mode: obx.ImportModeUpsert}, CSV: &ingest.File{Name: "products.csv", Data: build(true, "KO-1001")}}
	job, probs := t.runImport(withSeason)
	if job.Status != obx.JobSucceeded || job.ImportRun == nil || job.ImportRun.Counts.Updated != 1 {
		t.Errorf("an empty season cell for KO-1001: want succeeded with updated 1, got %s %s (%s)", job.Status, short(job.ImportRun), short(probs))
	}
	get()
	if product.Season != nil && *product.Season != "" {
		t.Errorf("an empty season cell must remove the season, KO-1001 still has %q", *product.Season)
	}
	if product.Name["nl"] != wantNL {
		t.Errorf("KO-1001 lost name:nl in the season import: %v", product.Name)
	}
}

func testManifestImport(t *T) {
	req, err := t.r.fixtureImport(t.ctx, "manifest", nil)
	t.Check(err, "build import")
	t.expectUnchangedImport("import with images_manifest", req)
}

func testMirror(t *T) {
	var cursor string
	if t.r.full != nil {
		cursor = t.liveCursor(t.r.full)
	}
	// (a) a mirror without KO-1006 unpublishes exactly KO-1006.
	req, err := t.r.fixtureImport(t.ctx, "files", func(row []string) bool { return row[1] != "KO-1006" })
	t.Check(err, "build import")
	req.Images = nil
	req.AssetsCSV = nil
	req.Manifest = nil
	req.Options.Mode = obx.ImportModeMirror
	job, probs := t.runImport(req)
	if job.Status != obx.JobSucceeded || job.ImportRun == nil || job.ImportRun.Counts.Unpublished != 1 {
		t.Errorf("mirror without KO-1006: want succeeded with unpublished 1, got %s %s (%s)", job.Status, short(job.ImportRun), short(probs))
	}
	if rec, err := t.r.ing.Get(t.ctx, obx.KindProduct, "KO-1006"); err != nil || rec.Status != obx.StatusUnpublished {
		t.Errorf("KO-1006 should be unpublished after the mirror import")
	}
	if rec, err := t.r.ing.Get(t.ctx, obx.KindProduct, "KO-1005"); err != nil || rec.Status != obx.StatusPublished {
		t.Errorf("KO-1005 must stay published")
	}
	if t.r.full != nil {
		live, c2 := t.newEntries(t.r.full, cursor)
		cursor = c2
		want := "product delete KO-1006; variant delete 2000100601013; variant delete 2000100602010; asset delete KO-1006_NVY_1; asset delete KO-1006_RST_1"
		if got := strings.Join(changeKeys(live), "; "); got != want {
			t.Errorf("mirror entries: got [%s], want [%s]", got, want)
		}
	}
	_, err = t.r.ing.Publish(t.ctx, obx.KindProduct, "KO-1006")
	t.Check(err, "publish KO-1006 again")
	if t.r.full != nil {
		_, cursor = t.newEntries(t.r.full, cursor)
	}
	// (b) a mirror with only KO-1001 of the nine Kestrel products is refused.
	req, err = t.r.fixtureImport(t.ctx, "files", func(row []string) bool { return row[1] == "KO-1001" })
	t.Check(err, "build import")
	req.Images, req.AssetsCSV, req.Manifest = nil, nil, nil
	req.Options.Mode = obx.ImportModeMirror
	job, probs = t.runImport(req)
	refused := (job.ImportRun != nil && job.ImportRun.Error != nil && job.ImportRun.Error.ObxError == obx.ErrMassUnpublishRefused) || hasCode(probs, obx.ErrMassUnpublishRefused)
	if !refused {
		t.Errorf("mirror with one of the nine Kestrel products: want mass_unpublish_refused, got %s %s", job.Status, short(probs))
	}
	if rec, err := t.r.ing.Get(t.ctx, obx.KindProduct, "KO-1002"); err != nil || rec.Status != obx.StatusPublished {
		t.Errorf("a refused mirror import unpublished KO-1002")
	}
	// (c) zip slip.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	csvData, _ := t.r.fixture("kestrel/products.csv")
	w, _ := zw.Create("products.csv")
	w.Write(csvData)
	w, _ = zw.Create("../evil.jpg")
	w.Write(uniquePNG(0))
	zw.Close()
	job, probs = t.runImport(&ingest.ImportRequest{Package: &ingest.File{Name: "evil.zip", Data: buf.Bytes()}, Options: obx.ImportOptions{Mode: obx.ImportModeUpsert}})
	invalid := (job.ImportRun != nil && job.ImportRun.Error != nil && job.ImportRun.Error.ObxError == obx.ErrImportInvalid) || hasCode(probs, obx.ErrImportInvalid)
	if !invalid {
		t.Errorf("zip with ../evil.jpg: want import_invalid, got %s %s", job.Status, short(probs))
	}
	if job.ImportRun != nil && (job.ImportRun.Counts.Created > 0 || job.ImportRun.Counts.Updated > 0) {
		t.Errorf("a zip-slip package wrote data")
	}
	if t.r.full != nil {
		if live, _ := t.newEntries(t.r.full, cursor); len(live) > 0 {
			t.Errorf("refused imports wrote change entries: %v", changeKeys(live))
		}
	}
}

func hasCode(probs []obx.ImportRowError, code string) bool {
	for _, p := range probs {
		if p.ObxError == code {
			return true
		}
	}
	return false
}

func testRowErrors(t *T) {
	csv := "brand,style_number,name:en,gtin,color_code,size,mystery\n" +
		"Kestrel,OBX-CONF-IMP4,Conformance Test Scarf,2000199901018,GRY,S,x\n" +
		"Kestrel,OBX-CONF-IMP4,Conformance Test Scarf,2000199901025,GRY,M,x\n" +
		"Kestrel,OBX-CONF-IMP4,Conformance Test Scarf,2000199901029,GRY,L,x\n"
	job, probs := t.runImport(&ingest.ImportRequest{CSV: &ingest.File{Name: "products.csv", Data: []byte(csv)}, Options: obx.ImportOptions{Mode: obx.ImportModeUpsert}})
	defer t.r.ing.Delete(t.ctx, obx.KindProduct, "OBX-CONF-IMP4")
	if job.Status != obx.JobSucceeded {
		t.Errorf("an import with a bad row must still succeed, got %s", job.Status)
	}
	var found, warned bool
	for _, p := range probs {
		raw, _ := json.Marshal(p)
		t.validate("ImportRowError", raw, "a row problem")
		if p.ObxError == obx.ErrInvalidGTIN {
			found = true
			if p.File != "products.csv" || p.Row == nil || *p.Row != 3 || p.Column == nil || !strings.Contains(*p.Column, "gtin") || p.Severity != obx.SeverityError {
				t.Errorf("row problem lacks file/row/column: %s", short(p))
			}
		}
		if p.Severity == obx.SeverityWarning && p.Column != nil && strings.Contains(*p.Column, "mystery") {
			warned = true
		}
	}
	if !found {
		t.Errorf("no invalid_gtin problem for row 3 (%s)", short(probs))
	}
	if !warned {
		t.Notef("no warning for the unknown column \"mystery\"")
	}
	if job.ImportRun != nil {
		if job.ImportRun.Counts.Errors != 1 {
			t.Errorf("counts.errors is %d, want 1", job.ImportRun.Counts.Errors)
		}
		if job.ImportRun.Counts.Created < 3 {
			t.Errorf("the good rows were not imported (created %d, want product + 2 variants)", job.ImportRun.Counts.Created)
		}
	}
	errsOnly, err := t.r.ing.JobResults(t.ctx, job.ID, true, "", 100)
	t.Check(err, "errors_only")
	for _, raw := range errsOnly.Items {
		var p obx.ImportRowError
		json.Unmarshal(raw, &p)
		if p.Severity != obx.SeverityError {
			t.Errorf("errors_only returned a %s", p.Severity)
		}
	}
	if _, err := t.r.ing.Get(t.ctx, obx.KindVariant, "2000199901025"); err != nil {
		t.Errorf("the good row M was not imported: %v", err)
	}
}

var (
	_ = errors.New
	_ = renditions.Fit
)
