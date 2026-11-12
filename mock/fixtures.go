package mock

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/renditions"
	"github.com/openbrandexchange/obx-go/specdata"
)

// ClientCredentials are an API key for a retailer's software.
type ClientCredentials struct {
	RetailerID   string
	ClientID     string
	ClientSecret string
}

// Credentials are the accounts a node with Fixtures has.
type Credentials struct {
	AdminEmail    string
	AdminPassword string
	// IngestToken has all ingest scopes and no brand restriction.
	IngestToken string
	// Full sees every brand and may download originals.
	Full ClientCredentials
	// Restricted only sees brand "kestrel".
	Restricted ClientCredentials
	// NoOriginals sees every brand but never gets originals.
	NoOriginals ClientCredentials
	// RetailerEmail/RetailerPassword log in to the portal as a user of the
	// Full retailer.
	RetailerEmail    string
	RetailerPassword string
	// KestrelBrandID and TrailBrandID are the protocol ids of the brands.
	KestrelBrandID string
	TrailBrandID   string
}

// Fixture account defaults. They only exist on mock nodes with Fixtures.
const (
	FixtureAdminEmail       = "admin@kestrel.example"
	FixtureAdminPassword    = "kestrel-admin-password"
	FixtureRetailerEmail    = "buyer@store.example"
	FixtureRetailerPassword = "store-buyer-password"
)

// Credentials returns the fixture accounts (zero value without Fixtures).
func (n *Node) Credentials() Credentials {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.creds
}

// loadFixtures completes setup like the conformance suite does and imports
// the Kestrel Outfitters fixtures.
func (n *Node) loadFixtures() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	if _, p := n.completeSetup(nodeadmin.SetupInput{SetupCode: n.opts.SetupCode,
		Company: nodeadmin.SetupCompany{Name: "Kestrel Outfitters", FirstBrandName: "Kestrel", DefaultLanguage: "nl", Languages: []string{"nl", "en"}, ContactEmail: "hello@kestrel.example"},
		Admin:   nodeadmin.SetupAdmin{Name: "Kestrel Admin", Email: FixtureAdminEmail, Password: FixtureAdminPassword}}); p != nil {
		return p
	}
	c := &n.creds
	c.AdminEmail, c.AdminPassword = FixtureAdminEmail, FixtureAdminPassword
	_, c.IngestToken = n.newIngestToken("Fixtures", []string{obx.IngestScopeRead, obx.IngestScopeWrite, obx.IngestScopeDelete}, nil, nil)

	in, err := fixtureImportFor(!n.opts.ClientRenditions)
	if err != nil {
		return err
	}
	for _, b := range in.blobs {
		s.putBlob(b.data, b.contentType, b.width, b.height)
	}
	run, problems := n.runImport(&in.importInput, "api", writer{})
	if run.Status != obx.JobSucceeded || run.Counts.Errors > 0 {
		return fmt.Errorf("fixture import: %s, %d errors: %+v", run.Status, run.Counts.Errors, problems)
	}
	c.KestrelBrandID = s.brands["kestrel"].id
	c.TrailBrandID = s.brands["kestrel-trail"].id

	mk := func(name string, brands []string, original *bool) ClientCredentials {
		rt := n.newRetailer(name)
		rt.brandIDs, rt.allowOriginal = brands, original
		scopes := []string{obx.ScopeCatalogRead, obx.ScopeMediaRead, obx.ScopeUsageWrite}
		if s.originalAllowed(rt) {
			scopes = append(scopes, obx.ScopeMediaOriginal)
		}
		cl, secret := n.newClient(rt, name+" software", scopes)
		if name == "Example Store" && n.opts.FullClientID != "" && n.opts.FullClientSecret != "" {
			cl.clientID, cl.secretHash, secret = n.opts.FullClientID, hashSecret(n.opts.FullClientSecret), n.opts.FullClientSecret
		}
		return ClientCredentials{RetailerID: rt.id, ClientID: cl.clientID, ClientSecret: secret}
	}
	c.Full = mk("Example Store", nil, nil)
	c.Restricted = mk("Restricted Store", []string{c.KestrelBrandID}, nil)
	c.NoOriginals = mk("No Originals Store", nil, ptr(false))
	hash, _ := bcrypt.GenerateFromPassword([]byte(FixtureRetailerPassword), bcryptCost)
	u := &user{id: s.newID("usr"), email: FixtureRetailerEmail, name: "Store Buyer", role: nodeadmin.RoleRetailer, retailerID: c.Full.RetailerID, status: "active", language: "nl", passwordHash: hash, createdAt: n.now()}
	s.users[u.id] = u
	c.RetailerEmail, c.RetailerPassword = FixtureRetailerEmail, FixtureRetailerPassword
	return nil
}

// fixtureImport is the Kestrel import with its blobs.
type fixtureImport struct {
	importInput
	blobs []blob
}

// fixtureImportFor builds the Kestrel import. With serverRenditions false the
// images go in as a manifest with pre-generated renditions, like the CLI
// does for nodes without an image library.
func fixtureImportFor(serverRenditions bool) (*fixtureImport, error) {
	fx := specdata.Fixtures()
	csv, err := fs.ReadFile(fx, "kestrel/products.csv")
	if err != nil {
		return nil, err
	}
	assets, err := fs.ReadFile(fx, "kestrel/assets.csv")
	if err != nil {
		return nil, err
	}
	out := &fixtureImport{importInput: importInput{csv: csv, csvName: "products.csv", assets: assets, options: obx.ImportOptions{Mode: obx.ImportModeUpsert}}}
	entries, err := fs.ReadDir(fx, "kestrel/images")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		data, err := fs.ReadFile(fx, path.Join("kestrel/images", e.Name()))
		if err != nil {
			return nil, err
		}
		if serverRenditions {
			out.images = append(out.images, namedFile{name: e.Name(), data: data})
			continue
		}
		info, err := renditions.ReadInfo(data)
		if err != nil {
			return nil, err
		}
		set, err := cachedRenditions(data)
		if err != nil {
			return nil, err
		}
		out.blobs = append(out.blobs, blob{sha: hashBytes(data), data: data, contentType: info.ContentType, width: info.Width, height: info.Height})
		for _, f := range set.All() {
			out.blobs = append(out.blobs, blob{sha: f.SHA256, data: f.Data, contentType: f.ContentType, width: f.Width, height: f.Height})
		}
		out.manifest = append(out.manifest, obx.ManifestImage{File: e.Name(), SHA256: hashBytes(data), Width: info.Width, Height: info.Height, Renditions: set.Manifest()})
	}
	return out, nil
}

// cachedRenditions generates renditions once per original per process.
func cachedRenditions(data []byte) (*renditions.Set, error) {
	sha := hashBytes(data)
	renderCacheMu.Lock()
	set := renderCache[sha]
	renderCacheMu.Unlock()
	if set != nil {
		return set, nil
	}
	set, err := renditions.Generate(data)
	if err != nil {
		return nil, err
	}
	renderCacheMu.Lock()
	renderCache[sha] = set
	renderCacheMu.Unlock()
	return set, nil
}
