package main

import (
	"bytes"
	"context"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
}

// freshNode starts a mock node, completes setup and returns an upload key.
func freshNode(t *testing.T, opts mock.Options) (*harness, string) {
	t.Helper()
	n := mock.New(opts)
	srv := httptest.NewServer(n)
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
	code := run(context.Background(), args, e)
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
	if !strings.Contains(out, "40 rows, 27 images") || !strings.Contains(errOut, "27 uploaded") {
		t.Fatalf("first push output:\n%s\n%s", out, errOut)
	}
	code, out, errOut = h.run("push", "--csv", filepath.Join(dir, "products.csv"), "--assets-csv", filepath.Join(dir, "assets.csv"), "--images", filepath.Join(dir, "images"))
	if code != 0 || !strings.Contains(out, "0 new, 0 updated") || !strings.Contains(errOut, "0 uploaded") {
		t.Fatalf("second push should change nothing and upload nothing: %d\n%s\n%s", code, out, errOut)
	}
	csv, _ := os.ReadFile(filepath.Join(dir, "products.csv"))
	bad := string(csv) + "Kestrel,KO-1001,Merino Crew Sweater,Merino trui met ronde hals,,,sweater,unisex,AW26,Highland,main: 100% merino_wool,,PT,,2000100109999,,NVY,Navy,Marineblauw,,XXL,INT\n"
	os.WriteFile(filepath.Join(dir, "products.csv"), []byte(bad), 0o644)
	os.WriteFile(filepath.Join(dir, "images", "notes_NVY_1.jpg"), []byte("not an image"), 0o644)
	code, out, errOut = h.run("push", dir)
	if code != exitProblems {
		t.Fatalf("push with a bad row: want exit 2, got %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "products.csv row 41, column gtin") || !strings.Contains(out, "notes_NVY_1.jpg") {
		t.Fatalf("problems not listed:\n%s", out)
	}
	code, out, _ = h.run("status")
	if code != 0 || strings.Count(out, "job_") < 3 {
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
	if code != 0 || !strings.Contains(out, "row 41") {
		t.Fatalf("status --job: %d\n%s", code, out)
	}
	code, out, errOut = h.run("push", dir, "--mirror", "--dry-run")
	if code != exitProblems && code != exitOK {
		t.Fatalf("mirror dry run: %d\n%s\n%s", code, out, errOut)
	}
	os.WriteFile(filepath.Join(dir, "products.csv"), []byte(strings.Join(strings.Split(string(csv), "\n")[:5], "\n")+"\n"), 0o644)
	code, _, errOut = h.run("push", "--csv", filepath.Join(dir, "products.csv"), "--mirror")
	if code != exitFailed || !strings.Contains(errOut+"", "") {
		t.Fatalf("mirror that would empty the catalog: want exit 1, got %d %s", code, errOut)
	}
	code, out, _ = h.run("info")
	if code != 0 || !strings.Contains(out, "Kestrel Trail") {
		t.Fatalf("info: %d %s", code, out)
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
	if !strings.Contains(out, `"uploaded_files": 27`) {
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
	if code != 0 || !strings.Contains(errOut, "done: ") {
		t.Fatalf("first watch: %d %s", code, errOut)
	}
	code, _, errOut = h.run("watch", dir, "--once")
	if code != 0 || !strings.Contains(errOut, "nothing changed") {
		t.Fatalf("second watch: %d %s", code, errOut)
	}
	os.WriteFile(filepath.Join(dir, "assets.csv"), []byte("file,alt_text:en\nKO-1001_NVY_1_front.png,Navy sweater seen from the front\n"), 0o644)
	code, _, errOut = h.run("watch", dir, "--once")
	if code != 0 || !strings.Contains(errOut, "updated") || strings.Contains(errOut, " 0 updated") {
		t.Fatalf("watch after a change: %d %s", code, errOut)
	}
}

func TestLoginWithPassword(t *testing.T) {
	h, _ := freshNode(t, mock.Options{})
	h.stdin = "\nadmin@kestrel.example\n"
	h.vars["OBX_PASSWORD"] = "admin-password-1"
	code, out, errOut := h.run("login", h.srv.URL)
	if code != 0 || !strings.Contains(errOut, "Created upload key") {
		t.Fatalf("login with password: %d %s %s", code, out, errOut)
	}
	// A script can pipe all three answers; the password is read from the same stream.
	delete(h.vars, "OBX_PASSWORD")
	h.stdin = "\nadmin@kestrel.example\nadmin-password-1\n"
	if code, out, errOut := h.run("login", h.srv.URL); code != 0 || !strings.Contains(errOut, "Created upload key") {
		t.Fatalf("login with piped password: %d %s %s", code, out, errOut)
	}
	h.vars["OBX_PASSWORD"] = "wrong-password-1"
	if code, _, _ := h.run("login", h.srv.URL, "--email", "admin@kestrel.example"); code != exitFailed {
		t.Fatalf("wrong password: %d", code)
	}
	if code, _, errOut := h.run("login", "http://127.0.0.1:1"); code != exitFailed || !strings.Contains(errOut, "does not look like") {
		t.Fatalf("login to a non-portal: %d %s", code, errOut)
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
	if !strings.Contains(stdout, "54 images downloaded") {
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
	if len(products) != 8 {
		t.Fatalf("%d product files", len(products))
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
	if code != 0 || !strings.Contains(stdout, "5 removed") {
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
