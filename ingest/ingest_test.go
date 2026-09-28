package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/obx"
)

func node(t *testing.T, opts mock.Options) (*mock.Node, *ingest.Client) {
	t.Helper()
	opts.Fixtures = true
	n := mock.New(opts)
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	c, err := ingest.New(srv.URL, n.Credentials().IngestToken)
	if err != nil {
		t.Fatal(err)
	}
	return n, c
}

func pngBytes(t *testing.T, w, h, pad int) []byte {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	buf.Write(make([]byte, pad))
	return buf.Bytes()
}

func TestWritesAreIdempotent(t *testing.T) {
	_, c := node(t, mock.Options{})
	ctx := context.Background()
	in := obx.ProductInput{BrandExternalID: obx.Ptr("kestrel"), StyleNumber: "KO-7001", Name: obx.LocalizedText{"en": "Rain Hat"}}
	r, err := c.PutProduct(ctx, "KO-7001", in, nil)
	if err != nil || r.Result != obx.ResultCreated {
		t.Fatalf("create: %v %+v", err, r)
	}
	r, err = c.PutProduct(ctx, "KO-7001", in, nil)
	if err != nil || r.Result != obx.ResultUnchanged {
		t.Fatalf("repeat: %v %+v", err, r)
	}
	if _, err := c.PutProduct(ctx, "KO-7001", in, &ingest.WriteOptions{IfMatch: `"stale"`}); !obx.IsCode(err, obx.ErrPreconditionFailed) {
		t.Fatalf("If-Match: %v", err)
	}
	rec, _ := c.Get(ctx, obx.KindProduct, "KO-7001")
	if _, err := c.PutProduct(ctx, "KO-7001", in, &ingest.WriteOptions{IfMatch: rec.ETag}); err != nil {
		t.Fatalf("If-Match with the current ETag: %v", err)
	}
	if r, _ := c.Unpublish(ctx, obx.KindProduct, "KO-7001"); r.Result != obx.ResultUpdated || obx.Str(r.Visibility) != obx.VisibilityHidden {
		t.Fatalf("unpublish: %+v", r)
	}
	// A nightly sync without status must not publish it again.
	if r, _ := c.PutProduct(ctx, "KO-7001", in, nil); r.Result != obx.ResultUnchanged || obx.Str(r.Visibility) != obx.VisibilityHidden {
		t.Fatalf("sync after unpublish: %+v", r)
	}
	if r, _ := c.Delete(ctx, obx.KindProduct, "KO-7001"); r.Result != obx.ResultDeleted {
		t.Fatalf("delete: %+v", r)
	}
	if r, _ := c.Delete(ctx, obx.KindProduct, "KO-7001"); r.Result != obx.ResultUnchanged {
		t.Fatalf("delete again: %+v", r)
	}
}

func TestResumableUploadResumes(t *testing.T) {
	_, c := node(t, mock.Options{MaxUploadBytes: 1 << 20, PartBytes: 1 << 20})
	ctx := context.Background()
	data := pngBytes(t, 20, 20, 3<<20)
	sha := ingest.HashBytes(data)
	up, blob, err := c.CreateUpload(ctx, sha, int64(len(data)), "image/png")
	if err != nil || blob != nil || up.PartsTotal != 4 {
		t.Fatalf("create: %v %+v", err, up)
	}
	// An earlier run sent parts 2 and 4, then stopped.
	for _, n := range []int{2, 4} {
		start := int64(n-1) << 20
		if _, err := c.PutUploadPart(ctx, up.ID, n, data[start:min(start+1<<20, int64(len(data)))]); err != nil {
			t.Fatal(err)
		}
	}
	state := &ingest.MemoryUploadState{}
	state.Set(sha, up.ID)
	var sent []int64
	b, err := c.UploadResumable(ctx, bytes.NewReader(data), int64(len(data)), sha, "image/png", state, func(done, total int64) { sent = append(sent, done) })
	if err != nil {
		t.Fatal(err)
	}
	if b.SHA256 != sha || len(sent) != 2 {
		t.Fatalf("resumed upload sent %d parts (want 2): %+v", len(sent), b)
	}
	if state.Get(sha) != "" {
		t.Fatal("finished upload still remembered")
	}
	// EnsureBlob now finds it and uploads nothing.
	up2, err := c.EnsureBlob(ctx, bytes.NewReader(data), int64(len(data)), sha, "image/png", nil)
	if err != nil || up2 {
		t.Fatalf("EnsureBlob of a known blob: %v uploaded=%v", err, up2)
	}
	// A new large file goes through multipart automatically.
	other := pngBytes(t, 21, 21, 2<<20)
	up3, err := c.EnsureBlob(ctx, bytes.NewReader(other), int64(len(other)), ingest.HashBytes(other), "image/png", nil)
	if err != nil || !up3 {
		t.Fatalf("EnsureBlob of a large file: %v %v", err, up3)
	}
}

func TestPreparerGeneratesRenditionsOnlyWhenNeeded(t *testing.T) {
	ctx := context.Background()
	img := pngBytes(t, 800, 1000, 0)
	_, server := node(t, mock.Options{})
	p := &ingest.Preparer{Client: server}
	res, err := p.Prepare(ctx, ingest.ImageSource{Name: "KO-1001_NVY_9.png", Data: img})
	if err != nil || res.Manifest.Renditions != nil || res.Uploaded != 1 || res.Manifest.Width != 800 {
		t.Fatalf("server-rendering node: %v %+v", err, res)
	}
	_, clientNode := node(t, mock.Options{ClientRenditions: true})
	cache := &ingest.FileRenditionCache{Path: filepath.Join(t.TempDir(), "r.json")}
	p = &ingest.Preparer{Client: clientNode, Cache: cache}
	res, err = p.Prepare(ctx, ingest.ImageSource{Name: "KO-1001_NVY_9.png", Data: img})
	if err != nil || res.Manifest.Renditions == nil || res.Uploaded != 4 {
		t.Fatalf("client-rendering node: %v %+v", err, res)
	}
	r := res.Manifest.Renditions
	if r.Web.Width != 800 || r.Square.Width != 1000 || r.Thumb.Height != 600 {
		t.Fatalf("rendition sizes: %+v %+v %+v", r.Web, r.Square, r.Thumb)
	}
	again, err := p.Prepare(ctx, ingest.ImageSource{Name: "KO-1001_NVY_9.png", Data: img})
	if err != nil || again.Uploaded != 0 || again.Manifest.Renditions.Web.SHA256 != r.Web.SHA256 {
		t.Fatalf("second prepare: %v %+v", err, again)
	}
	if _, err := p.Prepare(ctx, ingest.ImageSource{Name: "x.svg", Data: []byte("<svg/>")}); err == nil {
		t.Fatal("SVG accepted")
	}
}

func TestBatchSyncAndAsync(t *testing.T) {
	_, c := node(t, mock.Options{})
	ctx := context.Background()
	data, _ := json.Marshal(obx.VariantInput{ProductExternalID: "KO-1001", GTIN: obx.Ptr("2000100101018"), Color: obx.Color{Code: "NVY"}, Size: obx.Ptr("S")})
	ops := []obx.IngestOperation{
		{Op: obx.IngestOpPatch, Kind: obx.KindProduct, ExternalID: "KO-1001", Data: json.RawMessage(`{"season":"AW27"}`)},
		{Op: obx.IngestOpUpsert, Kind: obx.KindVariant, ExternalID: "dup", Data: data},
		{Op: obx.IngestOpUnpublish, Kind: obx.KindAsset, ExternalID: "KO-1001_NVY_2"},
	}
	out, err := c.Batch(ctx, ops, ingest.BatchOptions{})
	if err != nil || out.Job != nil || len(out.Results) != 3 {
		t.Fatalf("sync batch: %v %+v", err, out)
	}
	if out.Results[1].Result != obx.ResultError || out.Results[1].Error.ObxError != obx.ErrGTINConflict || out.Summary.Updated != 2 || out.Summary.Errors != 1 {
		t.Fatalf("results: %+v", out)
	}
	out, err = c.Batch(ctx, ops, ingest.BatchOptions{Async: true, NDJSON: true})
	if err != nil || out.Job == nil || out.Job.Status != obx.JobSucceeded || len(out.Results) != 3 || out.Results[0].Result != obx.ResultUnchanged {
		t.Fatalf("async batch: %v %+v", err, out)
	}
}

func TestImportReportsRowProblems(t *testing.T) {
	_, c := node(t, mock.Options{})
	ctx := context.Background()
	csv := "brand,style_number,name:en,gtin,color_code,size\nKestrel,KO-8001,Wool Gloves,2000100801017,GRY,M\nKestrel,KO-8001,Wool Gloves,12345,GRY,L\n"
	job, err := c.Import(ctx, ingest.ImportRequest{CSV: &ingest.File{Name: "products.csv", Data: []byte(csv)}, Options: obx.ImportOptions{DryRun: true}})
	if err != nil {
		t.Fatal(err)
	}
	job, err = c.WaitJob(ctx, job.ID, 0)
	if err != nil || job.Status != obx.JobSucceeded || job.ImportRun.Counts.Created != 2 || !job.ImportRun.DryRun {
		t.Fatalf("dry run: %v %+v", err, job.ImportRun)
	}
	if _, err := c.Get(ctx, obx.KindProduct, "KO-8001"); !obx.IsCode(err, obx.ErrNotFound) {
		t.Fatalf("a dry run wrote data: %v", err)
	}
	probs, err := c.ImportProblems(ctx, job.ID)
	if err != nil || len(probs) != 1 || probs[0].ObxError != obx.ErrInvalidGTIN || *probs[0].Row != 2 {
		t.Fatalf("problems: %v %+v", err, probs)
	}
}
