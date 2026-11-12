package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"image"
	"image/png"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	node   *mock.Node
	config string
	vars   map[string]string
	stdin  string
	ctx    context.Context
}

// freshNode starts a mock node (optionally behind wrap), completes setup and
// returns an access key.
func freshNode(t *testing.T, opts mock.Options, wrap ...func(http.Handler) http.Handler) (*harness, string) {
	t.Helper()
	n := mock.New(opts)
	var h http.Handler = n
	for _, w := range wrap {
		h = w(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	adm, _ := nodeadmin.New(srv.URL, nil)
	ctx := context.Background()
	if _, err := adm.Setup(ctx, nodeadmin.SetupInput{SetupCode: n.SetupCode(),
		Company: nodeadmin.SetupCompany{Name: "Kestrel Outfitters", FirstBrandName: "Kestrel", DefaultLanguage: "nl", Languages: []string{"nl", "en"}},
		Admin:   nodeadmin.SetupAdmin{Name: "Admin", Email: "admin@kestrel.example", Password: "admin-password-1"}}); err != nil {
		t.Fatal(err)
	}
	tok, err := adm.CreateIngestToken(ctx, nodeadmin.IngestTokenInput{Name: "test", Scopes: []string{obx.IngestScopeRead, obx.IngestScopeWrite, obx.IngestScopeDelete}})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, node: n, config: t.TempDir(), vars: map[string]string{}}, tok.Secret
}

func (h *harness) run(args ...string) (int, string, string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	e := &env{stdin: strings.NewReader(h.stdin), stdout: &out, stderr: &errb, getenv: func(k string) string { return h.vars[k] },
		configDir: h.config, cacheDir: h.config, readPass: func(string) (string, error) { return h.vars["OBX_PASSWORD"], nil }}
	ctx := h.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	code := run(ctx, args, e)
	if s := out.String() + errb.String(); strings.Contains(s, "⟦") {
		h.t.Errorf("obx %s printed an unknown message id:\n%s", strings.Join(args, " "), s)
	}
	return code, out.String(), errb.String()
}

// fixtureFolder copies spec fixtures/kestrel into a temporary folder.
func fixtureFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := fs.WalkDir(specdata.Fixtures(), "kestrel", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, "images.json") {
			return err
		}
		b, err := fs.ReadFile(specdata.Fixtures(), p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, strings.TrimPrefix(p, "kestrel/"))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoginPushRerunStatus(t *testing.T) {
	h, tok := freshNode(t, mock.Options{})
	code, out, errOut := h.run("login", h.srv.URL, "--token", tok)
	if code != 0 || !strings.Contains(out, "Connected to Kestrel Outfitters") {
		t.Fatalf("login: %d %s %s", code, out, errOut)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(h.config, "config.json"))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("config file permissions: %v %v", st.Mode(), err)
		}
	}
	dir := fixtureFolder(t)
	code, out, errOut = h.run("push", dir)
	if code != 0 {
		t.Fatalf("first push: %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "rows: 46, images: 35") || !strings.Contains(errOut, "35 uploaded") {
		t.Fatalf("first push output:\n%s\n%s", out, errOut)
	}
	// The 0.2 article data arrives with the push.
	ic, _ := ingest.New(h.srv.URL, tok)
	rec, err := ic.Get(context.Background(), obx.KindProduct, "KO-1008")
	if err != nil {
		t.Fatal(err)
	}
	var pin obx.ProductInput
	_ = json.Unmarshal(rec.Data, &pin)
	if pin.Features == nil || pin.Features.Wheels == nil || *pin.Features.Wheels != 4 || obx.Str(pin.ItemSubtype) != "cabin" {
		t.Fatalf("pushed KO-1008 lacks its article data: %s", rec.Data)
	}
	rec, err = ic.Get(context.Background(), obx.KindVariant, "2000100901014")
	if err != nil {
		t.Fatal(err)
	}
	var vin obx.VariantInput
	_ = json.Unmarshal(rec.Data, &vin)
	if vin.Measurements == nil || vin.Measurements.StrapLengthCM == nil || vin.Measurements.StrapLengthCM.Max != 140 || obx.Str(vin.Color.Family) != "grey" {
		t.Fatalf("pushed variant 2000100901014 lacks its measurements: %s", rec.Data)
	}
	code, out, errOut = h.run("push", "--csv", filepath.Join(dir, "products.csv"), "--assets-csv", filepath.Join(dir, "assets.csv"), "--images", filepath.Join(dir, "images"))
	if code != 0 || !strings.Contains(out, "new: 0 · updated: 0") || !strings.Contains(errOut, "0 uploaded") {
		t.Fatalf("second push should change nothing and upload nothing: %d\n%s\n%s", code, out, errOut)
	}
	// --csv without --assets-csv still sends the assets.csv next to it: otherwise the embargo and
	// the AI label in it would silently fall back to the portal defaults.
	code, out, errOut = h.run("push", "--csv", filepath.Join(dir, "products.csv"), "--images", filepath.Join(dir, "images"))
	if code != 0 || !strings.Contains(out, "new: 0 · updated: 0") || !strings.Contains(errOut, "products.csv + assets.csv") {
		t.Fatalf("push --csv should pick up the assets.csv next to it: %d\n%s\n%s", code, out, errOut)
	}
	// A copy of the first row with an invalid EAN, size XXL and no SKU.
	data, _ := os.ReadFile(filepath.Join(dir, "products.csv"))
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	extra := append([]string(nil), rows[1]...)
	for i, h := range rows[0] {
		switch h {
		case "gtin":
			extra[i] = "2000100109999"
		case "sku":
			extra[i] = ""
		case "size":
			extra[i] = "XXL"
		}
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.WriteAll(append(rows, extra))
	os.WriteFile(filepath.Join(dir, "products.csv"), buf.Bytes(), 0o644)
	os.WriteFile(filepath.Join(dir, "images", "notes_NVY_1.jpg"), []byte("not an image"), 0o644)
	code, out, errOut = h.run("push", dir)
	if code != exitProblems {
		t.Fatalf("push with a bad row: want exit 2, got %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "products.csv, row 47, column gtin") || !strings.Contains(out, "notes_NVY_1.jpg") {
		t.Fatalf("problems not listed:\n%s", out)
	}
	code, out, _ = h.run("status")
	if code != 0 || strings.Count(out, "job_") < 3 || !strings.Contains(out, "done") {
		t.Fatalf("status: %d\n%s", code, out)
	}
	fields := strings.Fields(strings.Split(out, "\n")[1])
	jobID := ""
	for _, f := range fields {
		if strings.HasPrefix(f, "job_") {
			jobID = f
		}
	}
	code, out, _ = h.run("status", "--job", jobID)
	if code != 0 || !strings.Contains(out, "row 47") {
		t.Fatalf("status --job: %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(dir, "products.csv"), []byte(strings.Join(strings.Split(string(data), "\n")[:5], "\n")+"\n"), 0o644)
	code, out, errOut = h.run("push", "--csv", filepath.Join(dir, "products.csv"), "--mirror")
	if code != exitFailed || !strings.Contains(out, "Nothing was changed") || !strings.Contains(out, "--allow-mass-unpublish") {
		t.Fatalf("mirror that would empty the catalog: want exit 1 and a hint, got %d %s", code, errOut)
	}
	code, out, _ = h.run("info")
	if code != 0 || !strings.Contains(out, "Kestrel Trail") {
		t.Fatalf("info: %d %s", code, out)
	}
}

// A dry run shows what would happen and changes nothing: no image reaches
// the portal and no product is created. The real run afterwards uploads.
func TestPushDryRunChangesNothing(t *testing.T) {
	for _, clientRenditions := range []bool{false, true} {
		h, tok := freshNode(t, mock.Options{ClientRenditions: clientRenditions})
		h.vars["OBX_URL"], h.vars["OBX_TOKEN"] = h.srv.URL, tok
		dir := fixtureFolder(t)
		code, out, errOut := h.run("push", dir, "--dry-run")
		if code != exitOK || !strings.Contains(out, "Checked (dry run, nothing changed): rows: 46, images: 35 → new: 93") ||
			!strings.Contains(out, "would upload new or changed images: 35") {
			t.Fatalf("dry run (client renditions %v): %d\n%s\n%s", clientRenditions, code, out, errOut)
		}
		ic, _ := ingest.New(h.srv.URL, tok)
		ctx := context.Background()
		img, _ := os.ReadFile(filepath.Join(dir, "images", "KO-1001_NVY_1_front.png"))
		if has, err := ic.HasBlob(ctx, ingest.HashBytes(img)); err != nil || has {
			t.Fatalf("a dry run uploaded an image: %v %v", has, err)
		}
		if _, err := ic.Get(ctx, obx.KindProduct, "KO-1001"); !obx.IsCode(err, obx.ErrNotFound) {
			t.Fatalf("a dry run created a product: %v", err)
		}
		if code, out, _ := h.run("push", dir, "--json"); code != exitOK || !strings.Contains(out, `"uploaded_files": 35`) {
			t.Fatalf("real run after the dry run: %d %s", code, out)
		}
		if code, out, _ := h.run("push", dir, "--dry-run"); code != exitOK || !strings.Contains(out, "new: 0 · updated: 0 · unchanged: ") || strings.Contains(out, "would upload") {
			t.Fatalf("dry run after publishing: %d %s", code, out)
		}
	}
}

// On a portal that only checks uploaded images in a dry run, obx says which
// images the counts leave out instead of listing them as problems.
func TestDryRunOnPortalThatNeedsBlobs(t *testing.T) {
	h, tok := freshNode(t, mock.Options{DryRunNeedsBlobs: true})
	h.vars["OBX_URL"], h.vars["OBX_TOKEN"] = h.srv.URL, tok
	code, out, errOut := h.run("push", fixtureFolder(t), "--dry-run")
	if code != exitOK || !strings.Contains(out, "not in the counts above: 35") || strings.Contains(out, "Problems") {
		t.Fatalf("dry run: %d\n%s\n%s", code, out, errOut)
	}
}

// bigPNG is a valid PNG of more than 3 MiB (padding after the image data).
func bigPNG(t *testing.T) []byte {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 40, 50))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 13)
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	buf.Write(make([]byte, 3<<20+12345))
	return buf.Bytes()
}

// An upload that is interrupted halfway (Ctrl-C, a lost connection, a
// reboot) continues on the next run with only the parts that are missing.
func TestInterruptedUploadResumes(t *testing.T) {
	var mu sync.Mutex
	var parts []string
	var cancel context.CancelFunc
	count := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isPart := r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/ingest/uploads/") && strings.Contains(r.URL.Path, "/parts/")
			next.ServeHTTP(w, r)
			if !isPart {
				return
			}
			mu.Lock()
			parts = append(parts, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
			if len(parts) == 2 && cancel != nil {
				cancel() // the user presses Ctrl-C after two parts
				cancel = nil
			}
			mu.Unlock()
		})
	}
	h, tok := freshNode(t, mock.Options{MaxUploadBytes: 1 << 20, PartBytes: 1 << 20}, count)
	h.vars["OBX_URL"], h.vars["OBX_TOKEN"] = h.srv.URL, tok
	dir := fixtureFolder(t)
	big := bigPNG(t)
	os.WriteFile(filepath.Join(dir, "images", "KO-1001_NVY_9.png"), big, 0o644)

	ctx, c := context.WithCancel(context.Background())
	mu.Lock()
	cancel = c
	mu.Unlock()
	h.ctx = ctx
	code, out, errOut := h.run("push", dir, "--parallel", "1")
	if code != exitFailed || !strings.Contains(errOut, "continue where it stopped") {
		t.Fatalf("interrupted push: want exit 1 and a hint, got %d\n%s\n%s", code, out, errOut)
	}
	mu.Lock()
	first := append([]string(nil), parts...)
	parts = nil
	mu.Unlock()
	if len(first) != 2 {
		t.Fatalf("parts sent before the interruption: %v", first)
	}
	ic, _ := ingest.New(h.srv.URL, tok)
	if has, _ := ic.HasBlob(context.Background(), ingest.HashBytes(big)); has {
		t.Fatal("the big image is complete although the upload was interrupted")
	}

	h.ctx = nil
	code, out, errOut = h.run("push", dir)
	if code != exitOK {
		t.Fatalf("resumed push: %d\n%s\n%s", code, out, errOut)
	}
	mu.Lock()
	second := append([]string(nil), parts...)
	mu.Unlock()
	total := (len(big) + 1<<20 - 1) / (1 << 20)
	if len(second) != total-2 {
		t.Fatalf("the resumed run sent parts %v; want only the %d that were missing (not %v again)", second, total-2, first)
	}
	for _, p := range second {
		for _, f := range first {
			if p == f {
				t.Fatalf("part %s was sent twice", p)
			}
		}
	}
	if has, _ := ic.HasBlob(context.Background(), ingest.HashBytes(big)); !has {
		t.Fatal("the big image is not on the portal after the resumed run")
	}
	rec, err := ic.Get(context.Background(), obx.KindAsset, "KO-1001_NVY_9")
	if err != nil || rec.Visibility != obx.VisibilityVisible {
		t.Fatalf("asset of the big image: %+v %v", rec, err)
	}
}

func TestPushMakesRenditionsForClientRenderingNodes(t *testing.T) {
	h, tok := freshNode(t, mock.Options{ClientRenditions: true})
	h.vars["OBX_URL"], h.vars["OBX_TOKEN"] = h.srv.URL, tok
	dir := fixtureFolder(t)
	code, out, errOut := h.run("push", dir, "--json")
	if code != 0 {
		t.Fatalf("push: %d\n%s\n%s", code, out, errOut)
	}
	ic, _ := ingest.New(h.srv.URL, tok)
	rec, err := ic.Get(context.Background(), obx.KindAsset, "KO-1002_OLV_1")
	if err != nil || rec.Visibility != obx.VisibilityVisible || len(rec.MissingFiles) != 0 {
		t.Fatalf("asset after push on a client-rendering node: %+v %v", rec, err)
	}
	if !strings.Contains(out, `"uploaded_files": 35`) {
		t.Fatalf("json output: %s", out)
	}
	code, out, _ = h.run("push", dir, "--json")
	if code != 0 || !strings.Contains(out, `"uploaded_files": 0`) {
		t.Fatalf("re-run uploaded again: %s", out)
	}
}

func TestWatchOnce(t *testing.T) {
	h, tok := freshNode(t, mock.Options{})
	h.vars["OBX_URL"], h.vars["OBX_TOKEN"] = h.srv.URL, tok
	dir := fixtureFolder(t)
	code, _, errOut := h.run("watch", dir, "--once")
	if code != 0 || !strings.Contains(errOut, "done: new: 93") {
		t.Fatalf("first watch: %d %s", code, errOut)
	}
	code, _, errOut = h.run("watch", dir, "--once")
	if code != 0 || !strings.Contains(errOut, "nothing changed") {
		t.Fatalf("second watch: %d %s", code, errOut)
	}
	// Only the image with the new alt text changes: the images that lost their
	// assets.csv row keep their embargo, credit and AI label (import rule 8).
	os.WriteFile(filepath.Join(dir, "assets.csv"), []byte("file,alt_text:en\nKO-1001_NVY_1_front.png,Navy sweater seen from the front\n"), 0o644)
	code, _, errOut = h.run("watch", dir, "--once")
	if code != 0 || !strings.Contains(errOut, "updated: 1 ") {
		t.Fatalf("watch after a change: %d %s", code, errOut)
	}
}

func TestLoginWithPassword(t *testing.T) {
	h, _ := freshNode(t, mock.Options{})
	h.stdin = "\nadmin@kestrel.example\n"
	h.vars["OBX_PASSWORD"] = "admin-password-1"
	code, out, errOut := h.run("login", h.srv.URL)
	if code != 0 || !strings.Contains(errOut, "Created access key") {
		t.Fatalf("login with password: %d %s %s", code, out, errOut)
	}
	// A script can pipe all three answers; the password is read from the same stream.
	delete(h.vars, "OBX_PASSWORD")
	h.stdin = "\nadmin@kestrel.example\nadmin-password-1\n"
	if code, out, errOut := h.run("login", h.srv.URL); code != 0 || !strings.Contains(errOut, "Created access key") {
		t.Fatalf("login with piped password: %d %s %s", code, out, errOut)
	}
	h.vars["OBX_PASSWORD"] = "wrong-password-1"
	if code, _, errOut := h.run("login", h.srv.URL, "--email", "admin@kestrel.example"); code != exitFailed || !strings.Contains(errOut, "e-mail address or password is not right") {
		t.Fatalf("wrong password: %d %s", code, errOut)
	}
	if code, out, _ := h.run("logout"); code != 0 || !strings.Contains(out, "Forgot") {
		t.Fatalf("logout: %s", out)
	}
	if code, _, errOut := h.run("push", "."); code != exitFailed || !strings.Contains(errOut, "obx login") {
		t.Fatalf("push without login: %d %s", code, errOut)
	}
}

func TestPull(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	creds := n.Credentials()
	h := &harness{t: t, srv: srv, node: n, config: t.TempDir(), vars: map[string]string{"OBX_CLIENT_ID": creds.Full.ClientID, "OBX_CLIENT_SECRET": creds.Full.ClientSecret}}
	out := t.TempDir()
	code, stdout, errOut := h.run("pull", srv.URL, "--out", out, "--renditions", "web,original")
	if code != 0 {
		t.Fatalf("pull: %d %s %s", code, stdout, errOut)
	}
	if !strings.Contains(stdout, "images downloaded: 70") {
		t.Fatalf("first pull: %s", stdout)
	}
	img := filepath.Join(out, "images", "KO-1001", "web", "KO-1001_NVY_1_front_web.jpg")
	if _, err := os.Stat(img); err != nil {
		t.Fatalf("missing %s: %v", img, err)
	}
	orig, _ := os.ReadFile(filepath.Join(out, "images", "KO-1001", "original", "KO-1001_NVY_1_front.png"))
	fixture, _ := fs.ReadFile(specdata.Fixtures(), "kestrel/images/KO-1001_NVY_1_front.png")
	if !bytes.Equal(orig, fixture) {
		t.Fatal("original differs from the fixture")
	}
	products, _ := os.ReadDir(filepath.Join(out, "products"))
	if len(products) != 11 {
		t.Fatalf("%d product files", len(products))
	}
	// The pulled objects carry the 0.2 article data as the portal sent it.
	var suitcase *obx.Product
	for _, e := range products {
		b, _ := os.ReadFile(filepath.Join(out, "products", e.Name()))
		var p obx.Product
		if json.Unmarshal(b, &p) == nil && p.ExternalID == "KO-1008" {
			suitcase = &p
			if !strings.Contains(string(b), `"item_subtype": "cabin"`) || !strings.Contains(string(b), `"contains_animal_origin": null`) {
				t.Fatalf("pulled KO-1008: %s", b)
			}
		}
	}
	if suitcase == nil || suitcase.Features == nil || obx.Str(suitcase.Features.Lock) != "tsa_combination" || !recycledPolyester(suitcase.Materials[1].Fibers[0]) {
		t.Fatalf("pulled KO-1008 lacks its article data: %+v", suitcase)
	}
	variants, _ := os.ReadDir(filepath.Join(out, "variants"))
	found := false
	for _, e := range variants {
		b, _ := os.ReadFile(filepath.Join(out, "variants", e.Name()))
		var v obx.Variant
		if json.Unmarshal(b, &v) == nil && v.ExternalID == "2000100801017" {
			found = v.Measurements != nil && v.Measurements.VolumeExpandedL != nil && *v.Measurements.VolumeExpandedL == 42 && obx.Str(v.Color.Family) == "black"
		}
	}
	if !found {
		t.Fatal("pulled variant 2000100801017 lacks its measurements or colour family")
	}
	code, stdout, _ = h.run("pull", srv.URL, "--out", out, "--renditions", "web,original")
	if code != 0 || !strings.Contains(stdout, "nothing changed") {
		t.Fatalf("second pull should download nothing and say so: %s", stdout)
	}
	ic, _ := ingest.New(srv.URL, creds.IngestToken)
	if _, err := ic.Unpublish(context.Background(), obx.KindProduct, "KO-1006"); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = h.run("pull", srv.URL, "--out", out, "--renditions", "web,original")
	if code != 0 || !strings.Contains(stdout, "removed: 5") {
		t.Fatalf("pull after unpublish: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(out, "images", "KO-1006", "web", "KO-1006_NVY_1_front_web.jpg")); !os.IsNotExist(err) {
		t.Fatalf("image of an unpublished product still there: %v", err)
	}
	h.vars["OBX_CLIENT_SECRET"] = "obxs_wrong"
	if code, _, errOut := h.run("pull", srv.URL, "--out", out); code != exitFailed || !strings.Contains(errOut, "not valid") {
		t.Fatalf("pull with a wrong secret: %d %s", code, errOut)
	}
}

// Every command explains itself with --help and "obx help <command>", lists
// every option, and a wrong option points to the help.
func TestHelpForEveryCommand(t *testing.T) {
	h := &harness{t: t, config: t.TempDir(), vars: map[string]string{}}
	for _, c := range commands() {
		code, out, errOut := h.run(c.name, "--help")
		if code != exitOK || !strings.Contains(out, "obx "+c.name+": ") || !strings.Contains(out, "Usage:") || errOut != "" {
			t.Errorf("obx %s --help: %d\n%s\n%s", c.name, code, out, errOut)
			continue
		}
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		c.define(&env{getenv: func(string) string { return "" }}, fs)
		fs.VisitAll(func(f *flag.Flag) {
			if !strings.Contains(out, "--"+f.Name) {
				t.Errorf("obx %s --help does not list --%s", c.name, f.Name)
			}
		})
		if len(c.examples) > 0 && !strings.Contains(out, "Examples:") {
			t.Errorf("obx %s --help has no examples", c.name)
		}
		if code, out2, _ := h.run("help", c.name); code != exitOK || out2 != out {
			t.Errorf("obx help %s differs from obx %s --help", c.name, c.name)
		}
	}
	code, out, _ := h.run("--help")
	for _, c := range commands() {
		if !strings.Contains(out, "obx "+c.name) {
			t.Errorf("obx --help does not mention %s", c.name)
		}
	}
	if code != exitOK {
		t.Errorf("obx --help: %d", code)
	}
	code, _, errOut := h.run("push", "--mirrror")
	if code != exitFailed || !strings.Contains(errOut, "unknown option -mirrror") || !strings.Contains(errOut, "obx push --help") {
		t.Errorf("unknown option: %d %s", code, errOut)
	}
	if code, _, errOut := h.run("pusj"); code != exitFailed || !strings.Contains(errOut, `there is no command "pusj"`) {
		t.Errorf("unknown command: %d %s", code, errOut)
	}
	if code, out, _ := h.run("version"); code != exitOK || !strings.HasPrefix(out, "obx ") {
		t.Errorf("version: %d %s", code, out)
	}
}

// Error messages say what is wrong in plain words, not Go error strings.
func TestPlainErrors(t *testing.T) {
	h := &harness{t: t, config: t.TempDir(), vars: map[string]string{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	ln.Close()
	if code, _, errOut := h.run("login", closed, "--token", "obxi_x"); code != exitFailed || !strings.Contains(errOut, "nothing answers at "+ln.Addr().String()) {
		t.Errorf("closed port: %d %s", code, errOut)
	}
	tlsSrv := httptest.NewUnstartedServer(http.NotFoundHandler())
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake is expected
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	if code, _, errOut := h.run("login", tlsSrv.URL, "--token", "obxi_x"); code != exitFailed || !strings.Contains(errOut, "security certificate") {
		t.Errorf("untrusted certificate: %d %s", code, errOut)
	}
	if code, _, errOut := h.run("login", "https://no-such-portal.invalid", "--token", "obxi_x"); code != exitFailed || !strings.Contains(errOut, "does not exist") {
		t.Errorf("unknown host: %d %s", code, errOut)
	}
	if code, _, errOut := h.run("login", "ftp://brands.example"); code != exitFailed || !strings.Contains(errOut, "is not a web address") {
		t.Errorf("bad address: %d %s", code, errOut)
	}
	h2, tok := freshNode(t, mock.Options{})
	h2.vars["OBX_URL"], h2.vars["OBX_TOKEN"] = h2.srv.URL, tok+"x"
	if code, _, errOut := h2.run("info"); code != exitFailed || !strings.Contains(errOut, "Automatic upload → Access keys") {
		t.Errorf("wrong key: %d %s", code, errOut)
	}
	h2.vars["OBX_TOKEN"] = tok
	if code, _, errOut := h2.run("push", filepath.Join(t.TempDir(), "missing")); code != exitFailed || !strings.Contains(errOut, "does not exist") {
		t.Errorf("missing folder: %d %s", code, errOut)
	}
}

// obx speaks the user's language (OBX_LANG, LANG) and falls back to English.
func TestLanguage(t *testing.T) {
	h, tok := freshNode(t, mock.Options{})
	h.vars["OBX_URL"], h.vars["OBX_TOKEN"] = h.srv.URL, tok
	dir := fixtureFolder(t)
	h.vars["LANG"] = "nl_NL.UTF-8"
	code, out, errOut := h.run("push", dir)
	if code != exitOK || !strings.Contains(out, "Klaar:") || !strings.Contains(errOut, "Publiceren naar") {
		t.Fatalf("Dutch push: %d\n%s\n%s", code, out, errOut)
	}
	h.vars["OBX_LANG"] = "de"
	if code, out, _ := h.run("push", "--help"); code != exitOK || !strings.Contains(out, "Optionen:") {
		t.Fatalf("German help: %d %s", code, out)
	}
	h.vars["OBX_LANG"], h.vars["LANG"] = "", "ja_JP.UTF-8"
	if code, out, _ := h.run("push", "--help"); code != exitOK || !strings.Contains(out, "Options:") {
		t.Fatalf("unsupported language falls back to English: %d %s", code, out)
	}
	for in, want := range map[string]string{"nl_BE.UTF-8": "nl", "de-AT": "de", "nn_NO": "nb", "pt_BR": "pt", "C": "", "POSIX": "", "sv": "sv", "ga_IE@euro": "ga"} {
		if got := normaliseLanguage(in); got != want {
			t.Errorf("normaliseLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigLocation(t *testing.T) {
	if c, k := configDirs(func(v string) string { return map[string]string{"OBX_CONFIG_DIR": "/srv/obx"}[v] }); c != "/srv/obx" || k != "/srv/obx" {
		t.Fatalf("OBX_CONFIG_DIR: %s %s", c, k)
	}
	c, k := configDirs(func(string) string { return "" })
	if filepath.Base(c) != "obx" || filepath.Base(k) != "obx" {
		t.Fatalf("default folders: %s %s", c, k)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(c, filepath.Join("Library", "Application Support")) {
		t.Fatalf("macOS settings folder: %s", c)
	}
	h, tok := freshNode(t, mock.Options{})
	if code, _, _ := h.run("login", h.srv.URL, "--token", tok); code != 0 {
		t.Fatal("login")
	}
	code, out, _ := h.run("config", "path")
	if code != 0 || strings.TrimSpace(out) != filepath.Join(h.config, "config.json") {
		t.Fatalf("config path: %d %q", code, out)
	}
	code, out, _ = h.run("config")
	if code != 0 || !strings.Contains(out, h.srv.URL) || strings.Contains(out, tok) || !strings.Contains(out, tok[len(tok)-4:]) {
		t.Fatalf("config shows the portal with a shortened key: %d\n%s", code, out)
	}
}

func TestCompletionScripts(t *testing.T) {
	h := &harness{t: t, config: t.TempDir(), vars: map[string]string{}}
	check := map[string][]string{"bash": {"bash", "-n"}, "zsh": {"zsh", "-n"}, "fish": {"fish", "-n"}}
	for _, shell := range shells {
		code, out, errOut := h.run("completion", shell)
		if code != exitOK || out == "" {
			t.Fatalf("completion %s: %d %s", shell, code, errOut)
		}
		for _, c := range commands() {
			if !strings.Contains(out, c.name) {
				t.Errorf("%s completion misses the command %s", shell, c.name)
			}
		}
		for _, f := range []string{"dry-run", "assets-csv", "allow-mass-unpublish", "renditions", "once"} {
			if !strings.Contains(out, f) {
				t.Errorf("%s completion misses --%s", shell, f)
			}
		}
		argv, ok := check[shell]
		if !ok {
			continue
		}
		if _, err := exec.LookPath(argv[0]); err != nil {
			t.Logf("%s not installed; syntax not checked", argv[0])
			continue
		}
		f := filepath.Join(t.TempDir(), "obx."+shell)
		os.WriteFile(f, []byte(out), 0o644)
		if msg, err := exec.Command(argv[0], append(argv[1:], f)...).CombinedOutput(); err != nil {
			t.Errorf("%s completion script has a syntax error: %v\n%s", shell, err, msg)
		}
	}
	if code, _, errOut := h.run("completion", "tcsh"); code != exitFailed || !strings.Contains(errOut, "bash, zsh, fish or powershell") {
		t.Errorf("unknown shell: %d %s", code, errOut)
	}
}

func TestProgressBar(t *testing.T) {
	e := &env{getenv: func(string) string { return "" }}
	var tty bytes.Buffer
	b := newBar(e, &tty, true, "images", 4)
	b.set(1, "1 MB uploaded")
	b.set(4, "4 MB uploaded")
	b.done("  images: 4 checked")
	if s := tty.String(); !strings.Contains(s, "\r  images [██████░░░░░░░░░░░░░░░░░░]  25%  1/4  ·  1 MB uploaded") || !strings.Contains(s, "100%  4/4") || !strings.HasSuffix(s, "\n") {
		t.Fatalf("terminal bar: %q", s)
	}
	var log bytes.Buffer
	b = newBar(e, &log, false, "images", 1000)
	for i := int64(1); i <= 1000; i++ {
		b.set(i, "")
	}
	if lines := strings.Count(log.String(), "\n"); lines != 2 || strings.Contains(log.String(), "\r") {
		t.Fatalf("a log gets the first and the last line only within 5 s, got %d lines: %q", lines, log.String())
	}
}

// obx status tells a check-only run apart from a real upload.
func TestStatusMarksDryRuns(t *testing.T) {
	h, tok := freshNode(t, mock.Options{})
	dir := fixtureFolder(t)
	if code, out, errOut := h.run("push", dir, "--url", h.srv.URL, "--token", tok, "--dry-run"); code != 0 {
		t.Fatalf("dry run: %d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := h.run("push", dir, "--url", h.srv.URL, "--token", tok); code != 0 {
		t.Fatalf("push: %d\n%s\n%s", code, out, errOut)
	}
	code, out, _ := h.run("status", "--url", h.srv.URL, "--token", tok)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) < 3 {
		t.Fatalf("status: %d\n%s", code, out)
	}
	// Newest first: the real push, then the dry run.
	if strings.Contains(lines[1], "(dry run)") || !strings.Contains(lines[2], "(dry run)") {
		t.Fatalf("dry run not labelled:\n%s", out)
	}
}

// Label completeness warnings (0.2) are listed as warnings, in the user's
// language; the products are still published.
func TestPushShowsLabelWarnings(t *testing.T) {
	h, tok := freshNode(t, mock.Options{})
	h.vars["OBX_URL"], h.vars["OBX_TOKEN"], h.vars["OBX_LANG"] = h.srv.URL, tok, "nl"
	dir := t.TempDir()
	data, _ := fs.ReadFile(specdata.Fixtures(), "imports/label-warnings.csv")
	csvPath := filepath.Join(dir, "products.csv")
	os.WriteFile(csvPath, data, 0o644)
	code, out, errOut := h.run("push", "--csv", csvPath)
	if code != exitOK {
		t.Fatalf("push: %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"regel 2, kolom contains_animal_origin: geef aan of het product niet-textiele delen van dierlijke oorsprong bevat", "regel 4, kolom materials: de percentages van een onderdeel tellen niet op tot 100"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// recycledPolyester: the KO-1008 lining fibre as a 0.3 node returns it, with
// the vocabulary's names.
func recycledPolyester(f obx.Fiber) bool {
	return f.Material.Code == "polyester" && f.Material.Name["nl"] == "Polyester" && f.Percentage == 100 &&
		f.Qualifier != nil && f.Qualifier.Code == "recycled" && f.Qualifier.Name["nl"] == "Gerecycled"
}
