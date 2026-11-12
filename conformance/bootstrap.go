package conformance

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// Setup defaults for a fresh node (docs/conformance.md).
const (
	fixtureCompany   = "Kestrel Outfitters"
	fixtureBrand     = "Kestrel"
	defaultAdminMail = "conformance@kestrel.example"
)

// setupNotes collects the A-SETUP-1 observations made during bootstrap.
type setupNotes struct {
	ran      bool
	problems []string
}

func (r *runner) note(format string, args ...any) {
	r.report.Setup = append(r.report.Setup, fmt.Sprintf(format, args...))
	r.logf("setup: "+format, args...)
}

// bootstrap prepares credentials and fixture data for the requested
// profiles.
func (r *runner) bootstrap(ctx context.Context, want map[string]bool) error {
	cfg := &r.cfg
	notes := &setupNotes{}
	r.setup = notes
	if cfg.AdminEmail != "" || cfg.SetupCode != "" {
		r.admin = r.newAdmin()
		required, err := r.admin.SetupRequired(ctx)
		if err != nil {
			return fmt.Errorf("admin API: %w", err)
		}
		switch {
		case required:
			if cfg.SetupCode == "" {
				return errors.New("the node needs first-run setup; pass the setup code")
			}
			notes.ran = true
			notes.problems = r.checkBeforeSetup(ctx)
			if cfg.AdminEmail == "" {
				cfg.AdminEmail = defaultAdminMail
			}
			if cfg.AdminPassword == "" {
				cfg.AdminPassword = "conformance-" + randomSuffix()
			}
			_, err := r.admin.Setup(ctx, nodeadmin.SetupInput{SetupCode: cfg.SetupCode,
				Company: nodeadmin.SetupCompany{Name: fixtureCompany, FirstBrandName: fixtureBrand, DefaultLanguage: "nl", Languages: []string{"nl", "en"}},
				Admin:   nodeadmin.SetupAdmin{Name: "Conformance Admin", Email: cfg.AdminEmail, Password: cfg.AdminPassword}})
			if err != nil {
				return fmt.Errorf("first-run setup: %w", err)
			}
			r.note("completed first-run setup (company %q, brand %q, languages nl,en, admin %s)", fixtureCompany, fixtureBrand, cfg.AdminEmail)
			again, err := r.admin.Setup(ctx, nodeadmin.SetupInput{SetupCode: cfg.SetupCode, Company: nodeadmin.SetupCompany{Name: "x", DefaultLanguage: "en"}, Admin: nodeadmin.SetupAdmin{Name: "x", Email: "x@x.example", Password: "xxxxxxxxxxxx"}})
			if again != nil || !obx.IsCode(err, obx.ErrSetupCompleted) {
				notes.problems = append(notes.problems, fmt.Sprintf("setup after completion: want 409 setup_completed, got %v", err))
			}
			if _, err := r.admin.Login(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
				return fmt.Errorf("login after setup: %w", err)
			}
		case cfg.AdminEmail != "":
			if _, err := r.admin.Login(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
				return fmt.Errorf("admin login: %w", err)
			}
		default:
			return errors.New("the node is already set up; pass admin e-mail and password instead of the setup code")
		}
	}
	if d, err := r.newClient("", "").Discovery(ctx); err == nil {
		r.discovery = d
	}
	if r.admin != nil {
		kestrel, err := r.admin.BrandID(ctx, "kestrel")
		if err != nil {
			return fmt.Errorf("the node has no brand with external id \"kestrel\" (first brand of the fixture setup): %w", err)
		}
		if cfg.IngestToken == "" && (want[ProfileIngest] || want[ProfileProtocol] || want[ProfileSecurity]) {
			tok, err := r.admin.CreateIngestToken(ctx, nodeadmin.IngestTokenInput{Name: "OBX conformance", Scopes: []string{obx.IngestScopeRead, obx.IngestScopeWrite, obx.IngestScopeDelete}})
			if err != nil {
				return fmt.Errorf("create upload key: %w", err)
			}
			cfg.IngestToken = tok.Secret
			r.note("created upload key %s", tok.Token.Prefix)
		}
		mk := func(name string, brands *[]string, original *bool, scopes []string) (string, string, error) {
			rt, err := r.admin.CreateRetailer(ctx, nodeadmin.RetailerInput{Name: name, BrandIDs: brands, AllowOriginal: original})
			if err != nil {
				return "", "", fmt.Errorf("create retailer %s: %w", name, err)
			}
			c, err := r.admin.CreateAPIClient(ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: name + " software", Scopes: scopes})
			if err != nil {
				return "", "", fmt.Errorf("create API key for %s: %w", name, err)
			}
			return c.Client.ClientID, c.ClientSecret, nil
		}
		all := []string{obx.ScopeCatalogRead, obx.ScopeMediaRead, obx.ScopeMediaOriginal, obx.ScopeUsageWrite}
		if cfg.ClientID == "" {
			if cfg.ClientID, cfg.ClientSecret, err = mk("OBX conformance: all brands", nil, nil, all); err != nil {
				return err
			}
		}
		if cfg.SecondClientID == "" {
			if cfg.SecondClientID, cfg.SecondClientSecret, err = mk("OBX conformance: Kestrel only", &[]string{kestrel}, nil, all); err != nil {
				return err
			}
		}
		if cfg.NoOriginalClientID == "" {
			f := false
			if cfg.NoOriginalClientID, cfg.NoOriginalClientSecret, err = mk("OBX conformance: no originals", nil, &f, []string{obx.ScopeCatalogRead, obx.ScopeMediaRead, obx.ScopeUsageWrite}); err != nil {
				return err
			}
		}
	}
	if r.admin == nil && cfg.Operator != nil && want[ProfileProtocol] {
		type need struct {
			id, secret *string
			name       string
			brands     []string
			originals  bool
		}
		for _, n := range []need{
			{&cfg.ClientID, &cfg.ClientSecret, "OBX conformance: all brands", nil, true},
			{&cfg.SecondClientID, &cfg.SecondClientSecret, "OBX conformance: Kestrel only", []string{"kestrel"}, true},
			{&cfg.NoOriginalClientID, &cfg.NoOriginalClientSecret, "OBX conformance: no originals", nil, false},
		} {
			if *n.id != "" {
				continue
			}
			k, err := r.operatorKey(ctx, n.name, n.brands, n.originals)
			if err != nil {
				return fmt.Errorf("operator: %w", err)
			}
			*n.id, *n.secret = k.id, k.secret
			r.note("the operator created API key %q", n.name)
		}
	}
	if cfg.ClientID != "" {
		r.full = r.newClient(cfg.ClientID, cfg.ClientSecret)
		r.fullCreds = [2]string{cfg.ClientID, cfg.ClientSecret}
	}
	if cfg.SecondClientID != "" {
		r.second = r.newClient(cfg.SecondClientID, cfg.SecondClientSecret)
	}
	if cfg.NoOriginalClientID != "" {
		r.noOrig = r.newClient(cfg.NoOriginalClientID, cfg.NoOriginalClientSecret)
	}
	if cfg.IngestToken != "" {
		r.ing = r.newIngest(cfg.IngestToken)
		if info, err := r.ing.Info(ctx); err == nil {
			r.info = info
		}
	}
	if want[ProfileProtocol] && r.full == nil {
		return errors.New("the protocol profile needs an API key (--client-id/--client-secret) or admin credentials")
	}
	if want[ProfileIngest] && r.ing == nil {
		return errors.New("the ingest profile needs an upload key (--token) or admin credentials")
	}
	if (want[ProfilePortal] || want[ProfileSecurity]) && r.admin == nil {
		return errors.New("the portal profile needs admin credentials or a setup code")
	}
	if r.full != nil || r.ing != nil {
		if err := r.ensureFixtures(ctx); err != nil {
			return err
		}
	}
	return nil
}

// checkBeforeSetup observes a fresh node (A-SETUP-1).
func (r *runner) checkBeforeSetup(ctx context.Context) []string {
	var probs []string
	res, err := r.do(ctx, http.MethodGet, r.cfg.AdminURL+obx.PathAdminAPI+"/settings", nil, nil)
	if err != nil || res.status != http.StatusConflict || res.code() != obx.ErrSetupRequired {
		probs = append(probs, fmt.Sprintf("GET /admin/api/settings before setup: want 409 setup_required, got %s", describe(res, err)))
	}
	res, err = r.do(ctx, http.MethodGet, r.cfg.BaseURL+obx.PathDiscovery, nil, nil)
	if err != nil || res.status != http.StatusServiceUnavailable || res.code() != obx.ErrSetupRequired {
		probs = append(probs, fmt.Sprintf("GET /.well-known/obx before setup: want 503 setup_required, got %s", describe(res, err)))
	}
	_, err = r.admin.Setup(ctx, nodeadmin.SetupInput{SetupCode: "wrong-code-" + randomSuffix(),
		Company: nodeadmin.SetupCompany{Name: "Intruder", DefaultLanguage: "en"}, Admin: nodeadmin.SetupAdmin{Name: "x", Email: "intruder@evil.example", Password: "intruder-password"}})
	if err == nil {
		probs = append(probs, "setup with a wrong setup code succeeded")
	} else if p, ok := err.(*obx.Problem); !ok || (p.Status != http.StatusForbidden && p.Status != http.StatusUnprocessableEntity) {
		probs = append(probs, fmt.Sprintf("setup with a wrong setup code: want 403, got %v", err))
	}
	return probs
}

func describe(res *response, err error) string {
	if err != nil {
		return err.Error()
	}
	if c := res.code(); c != "" {
		return fmt.Sprintf("%d %s", res.status, c)
	}
	return fmt.Sprint(res.status)
}

func randomSuffix() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()&0xffffffffff)
}

// ensureFixtures imports fixtures/kestrel when the node does not have it yet
// and loads the ids of the fixture objects.
func (r *runner) ensureFixtures(ctx context.Context) error {
	present := false
	if r.ing != nil {
		if _, err := r.ing.Get(ctx, obx.KindProduct, "KO-1001"); err == nil {
			present = true
		}
	} else if r.full != nil {
		if res, err := r.full.LookupGTINs(ctx, []string{"2000100101025"}); err == nil && len(res.Products) > 0 {
			present = true
		}
	}
	if !present {
		if r.ing == nil {
			return errors.New("the node has no fixture data: load fixtures/kestrel with the node's own import, or give an upload key (--token) so the suite imports it")
		}
		job, err := r.importFixtures(ctx, "package")
		if err != nil {
			return fmt.Errorf("import fixtures/kestrel: %w", err)
		}
		if job.Status != obx.JobSucceeded || job.ImportRun == nil || job.ImportRun.Counts.Errors > 0 {
			probs, _ := r.ing.ImportProblems(ctx, job.ID)
			return fmt.Errorf("import fixtures/kestrel: job %s, problems: %s", job.Status, short(probs))
		}
		r.note("imported fixtures/kestrel (%d created)", job.ImportRun.Counts.Created)
	}
	if r.full != nil {
		return r.loadIDs(ctx)
	}
	return nil
}

// loadIDs maps fixture external ids to protocol ids.
func (r *runner) loadIDs(ctx context.Context) error {
	r.productIDs, r.assetIDs, r.brandIDs, r.products = map[string]string{}, map[string]string{}, map[string]string{}, map[string]*obx.ProductDetail{}
	brands, err := r.full.Brands(ctx)
	if err != nil {
		return fmt.Errorf("list brands: %w", err)
	}
	for _, b := range brands {
		r.brandIDs[b.ExternalID] = b.ID
	}
	return r.full.EachProduct(ctx, r.productsQuery(), func(p *obx.ProductDetail) error {
		cp := *p
		r.productIDs[p.ExternalID] = p.ID
		r.products[p.ExternalID] = &cp
		for _, a := range p.Assets {
			r.assetIDs[a.ExternalID] = a.ID
		}
		return nil
	})
}

// importFixtures imports fixtures/kestrel through the ingest API. how is
// "package" (zip), "files" (csv + assets_csv + images) or "manifest" (blobs
// first). On nodes without server renditions every form falls back to the
// manifest with client-generated renditions when the images are new.
func (r *runner) importFixtures(ctx context.Context, how string) (*obx.IngestJob, error) {
	req, err := r.fixtureImport(ctx, how, nil)
	if err != nil {
		return nil, err
	}
	job, err := r.ing.Import(ctx, *req)
	if err != nil {
		return nil, err
	}
	return r.ing.WaitJob(ctx, job.ID, 200*time.Millisecond)
}

// fixtureImport builds an import request for fixtures/kestrel. csvFilter,
// when set, keeps only the products.csv rows it returns true for.
func (r *runner) fixtureImport(ctx context.Context, how string, csvFilter func(row []string) bool) (*ingest.ImportRequest, error) {
	csvData, err := r.fixture("kestrel/products.csv")
	if err != nil {
		return nil, err
	}
	if csvFilter != nil {
		csvData = filterCSV(csvData, csvFilter)
	}
	assetsData, err := r.fixture("kestrel/assets.csv")
	if err != nil {
		return nil, err
	}
	images, err := r.fixtureImages()
	if err != nil {
		return nil, err
	}
	serverRenders := r.info == nil || r.info.Renditions.ServerGenerated
	if !serverRenders {
		how = "manifest"
	}
	req := &ingest.ImportRequest{Options: obx.ImportOptions{Mode: obx.ImportModeUpsert}}
	switch how {
	case "package":
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		add := func(name string, data []byte) {
			w, _ := zw.Create(name)
			w.Write(data)
		}
		add("products.csv", csvData)
		add("assets.csv", assetsData)
		for _, img := range images {
			add("images/"+img.Name, img.Data)
		}
		zw.Close()
		req.Package = &ingest.File{Name: "kestrel.zip", Data: buf.Bytes()}
	case "files":
		req.CSV = &ingest.File{Name: "products.csv", Data: csvData}
		req.AssetsCSV = &ingest.File{Name: "assets.csv", Data: assetsData}
		req.Images = images
	case "manifest":
		req.CSV = &ingest.File{Name: "products.csv", Data: csvData}
		req.AssetsCSV = &ingest.File{Name: "assets.csv", Data: assetsData}
		prep := &ingest.Preparer{Client: r.ing}
		req.Manifest = []obx.ManifestImage{}
		for _, img := range images {
			p, err := prep.Prepare(ctx, ingest.ImageSource{Name: img.Name, Data: img.Data})
			if err != nil {
				return nil, err
			}
			req.Manifest = append(req.Manifest, p.Manifest)
		}
	}
	return req, nil
}

func (r *runner) fixtureImages() ([]ingest.File, error) {
	entries, err := fs.ReadDir(r.fixtures, "kestrel/images")
	if err != nil {
		return nil, err
	}
	var out []ingest.File
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := r.fixture(path.Join("kestrel/images", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, ingest.File{Name: e.Name(), Data: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// filterCSV keeps the header and the rows keep() accepts (naive split: the
// fixture has no quoted newlines).
func filterCSV(data []byte, keep func(row []string) bool) []byte {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := []string{lines[0]}
	for _, l := range lines[1:] {
		if keep(strings.Split(l, ",")) {
			out = append(out, l)
		}
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// fixtureExpect returns expected/<name> as any.
func (r *runner) fixtureExpect(name string) (any, error) { return r.fixtureJSON("expected/" + name) }

// productExpectation resolves {{product:<ext>}}: the expected fixture when
// one exists, else the shape of the product as the node serves it.
func (r *runner) productExpectation(ctx context.Context) func(string) (any, bool) {
	return func(ext string) (any, bool) {
		if v, err := r.fixtureExpect("product-" + ext + ".json"); err == nil {
			return v, true
		}
		p, ok := r.products[ext]
		if !ok {
			return nil, false
		}
		v := toAny(p)
		scrubURLs(v)
		return v, true
	}
}

// scrubURLs replaces rendition URLs and their expiry with placeholders.
func scrubURLs(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch k {
			case "url":
				x[k] = "{{url}}"
			case "url_expires_at":
				x[k] = "{{datetime|null}}"
			case "updated_at", "created_at":
				x[k] = "{{datetime}}"
			default:
				scrubURLs(val)
			}
		}
	case []any:
		for _, e := range x {
			scrubURLs(e)
		}
	}
}

// productsQuery pages through all products.
func (r *runner) productsQuery() client.ProductsQuery { return client.ProductsQuery{Limit: 100} }
