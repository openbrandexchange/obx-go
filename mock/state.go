package mock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

// fileRef is one stored file of an asset.
type fileRef struct {
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// record is one catalog object with its stored ingest input.
type record struct {
	kind      string
	id        string
	extID     string
	status    string
	createdAt time.Time
	updatedAt time.Time // ingest record: last stored change
	protoAt   time.Time // protocol updated_at
	protoHash string
	input     json.RawMessage // canonical stored input (IngestRecord.data)
	brand     *obx.BrandInput
	product   *obx.ProductInput
	variant   *obx.VariantInput
	asset     *obx.AssetInput
	gtin14    string
	files     map[string]fileRef // effective files (asset)
	pending   map[string]fileRef // replacement waiting for renditions (asset)
	parentExt string             // brand ext (product) or product ext (variant/asset)
}

func (r *record) etag() string {
	h := sha256.New()
	h.Write(r.input)
	h.Write([]byte(r.status))
	if r.files != nil {
		b, _ := json.Marshal(r.files)
		h.Write(b)
	}
	return `"` + hex.EncodeToString(h.Sum(nil))[:20] + `"`
}

// blob is stored bytes.
type blob struct {
	sha         string
	data        []byte
	contentType string
	width       int
	height      int
	createdAt   time.Time
}

// entry is one change-log entry.
type entry struct {
	seq        int64
	typ        string
	op         string
	id         string
	extID      string
	brandID    string
	productID  string
	at         time.Time
	retailerID string // grant entries
	resync     bool
}

// visInfo is the visibility state of a visible object at commit time.
type visInfo struct {
	kind      string
	id        string
	extID     string
	brandID   string
	productID string
	hash      string
	color     string
	colorNull bool
	position  int
}

// state is everything the node stores. Guarded by Node.mu.
type state struct {
	n *Node

	counters map[string]int

	setupDone bool
	settings  settings

	brands   map[string]*record // by external id
	products map[string]*record
	variants map[string]*record
	assets   map[string]*record
	byID     map[string]*record
	gtins    map[string]string // gtin14 → variant external id

	blobs   map[string]*blob
	uploads map[string]*upload

	log         []entry
	seq         int64
	visible     map[string]visInfo
	lastSeq     map[string]int64
	cursorEpoch int
	nextExpiry  time.Time
	holdCommits int // >0: commit is deferred (atomic asset groups)

	retailers      map[string]*retailer
	clients        map[string]*apiClient // by id
	accessTokens   map[string]*accessToken
	ingestTokens   map[string]*ingestToken // by id
	accessRequests map[string]*accessRequest
	users          map[string]*user
	sessions       map[string]*session
	invites        map[string]*invite // by token hash
	resets         map[string]*invite
	webhooks       map[string]*webhookSub
	deliveries     map[string][]webhookDelivery
	exports        map[string]*export
	jobs           map[string]*job
	jobOrder       []string
	importRuns     map[string]*obx.ImportRun
	runErrors      map[string][]obx.ImportRowError
	runOrder       []string
	importSources  map[string]*importSource
	idempotency    map[string]idemEntry
	selections     map[string]*selection
	downloadJobs   map[string]*downloadJob
	events         []downloadEvent
	outbox         []Email
	rate           map[string]*rateWindow
}

// settings are the node settings.
type settings struct {
	companyName         string
	website             *string
	contactEmail        *string
	country             *string
	displayName         string
	logo                []byte
	logoType            string
	accent              *string
	welcome             obx.LocalizedText
	portalName          obx.LocalizedText
	accessRequests      bool
	allowOriginal       bool
	allowDataExport     bool
	apiSelfService      bool
	languages           []string
	defaultLang         string
	defaultRights       obx.Rights
	defaultIngestStatus string
}

func newState(n *Node) *state {
	s := &state{
		n:        n,
		counters: map[string]int{},
		brands:   map[string]*record{}, products: map[string]*record{}, variants: map[string]*record{}, assets: map[string]*record{},
		byID: map[string]*record{}, gtins: map[string]string{}, blobs: map[string]*blob{}, uploads: map[string]*upload{},
		visible: map[string]visInfo{}, lastSeq: map[string]int64{},
		retailers: map[string]*retailer{}, clients: map[string]*apiClient{}, accessTokens: map[string]*accessToken{},
		ingestTokens: map[string]*ingestToken{}, accessRequests: map[string]*accessRequest{}, users: map[string]*user{},
		sessions: map[string]*session{}, invites: map[string]*invite{}, resets: map[string]*invite{},
		webhooks: map[string]*webhookSub{}, deliveries: map[string][]webhookDelivery{}, exports: map[string]*export{},
		jobs: map[string]*job{}, importRuns: map[string]*obx.ImportRun{}, runErrors: map[string][]obx.ImportRowError{},
		importSources: map[string]*importSource{}, idempotency: map[string]idemEntry{}, selections: map[string]*selection{},
		downloadJobs: map[string]*downloadJob{}, rate: map[string]*rateWindow{},
	}
	s.settings = settings{
		accessRequests: true, allowOriginal: true, allowDataExport: true,
		languages: []string{"en"}, defaultLang: "en", defaultRights: obx.DefaultRights(), defaultIngestStatus: obx.StatusPublished,
	}
	return s
}

// newID returns prefix_000001-style ids (sortable, opaque to clients).
func (s *state) newID(prefix string) string {
	s.counters[prefix]++
	return fmt.Sprintf("%s_%06d", prefix, s.counters[prefix])
}

var idPrefix = map[string]string{obx.KindBrand: "brd", obx.KindProduct: "prd", obx.KindVariant: "var", obx.KindAsset: "ast"}

func (s *state) table(kind string) map[string]*record {
	switch kind {
	case obx.KindBrand:
		return s.brands
	case obx.KindProduct:
		return s.products
	case obx.KindVariant:
		return s.variants
	case obx.KindAsset:
		return s.assets
	}
	return nil
}

// ---- visibility

func (s *state) brandOf(r *record) *record {
	switch r.kind {
	case obx.KindBrand:
		return r
	case obx.KindProduct:
		return s.brands[r.parentExt]
	default:
		if p := s.products[r.parentExt]; p != nil {
			return s.brands[p.parentExt]
		}
	}
	return nil
}

func (s *state) productOf(r *record) *record {
	switch r.kind {
	case obx.KindVariant, obx.KindAsset:
		return s.products[r.parentExt]
	case obx.KindProduct:
		return r
	}
	return nil
}

func (s *state) filesComplete(r *record) bool {
	for _, n := range obx.RenditionNames {
		if _, ok := r.files[n]; !ok {
			return false
		}
	}
	return true
}

func (s *state) expired(r *record, now time.Time) bool {
	rights := s.effectiveRights(r)
	return rights.ExpiresAt != nil && !rights.ExpiresAt.After(now)
}

// isVisible applies protocol.md 5.8 (without the per-retailer grant).
func (s *state) isVisible(r *record, now time.Time) bool {
	if r == nil || r.status != obx.StatusPublished {
		return false
	}
	switch r.kind {
	case obx.KindBrand:
		return true
	case obx.KindProduct:
		return s.isVisible(s.brands[r.parentExt], now)
	case obx.KindVariant:
		return s.isVisible(s.products[r.parentExt], now)
	case obx.KindAsset:
		return s.isVisible(s.products[r.parentExt], now) && s.filesComplete(r) && !s.expired(r, now)
	}
	return false
}

// visibility returns the ingest visibility of a record.
func (s *state) visibility(r *record, now time.Time) string {
	if r.status != obx.StatusPublished {
		return obx.VisibilityHidden
	}
	var parent *record
	switch r.kind {
	case obx.KindProduct:
		parent = s.brands[r.parentExt]
	case obx.KindVariant, obx.KindAsset:
		parent = s.products[r.parentExt]
	}
	if r.kind != obx.KindBrand && !s.isVisible(parent, now) {
		return obx.VisibilityHidden
	}
	if r.kind == obx.KindAsset {
		if !s.filesComplete(r) {
			return obx.VisibilityPendingFiles
		}
		if s.expired(r, now) {
			return obx.VisibilityHidden
		}
	}
	return obx.VisibilityVisible
}

func (s *state) missingFiles(r *record) []string {
	if r.kind != obx.KindAsset {
		return nil
	}
	out := []string{}
	for _, n := range obx.RenditionNames {
		if _, ok := r.files[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

// ---- protocol representations

func (s *state) effectiveRights(r *record) obx.Rights {
	out := s.settings.defaultRights
	out.Channels = append([]string(nil), out.Channels...)
	if r.asset == nil || r.asset.Rights == nil {
		return out
	}
	in := r.asset.Rights
	if in.License != nil {
		out.License = *in.License
	}
	if in.LicenseText != nil {
		out.LicenseText = in.LicenseText
	}
	if in.Channels != nil {
		out.Channels = in.Channels
	}
	if in.Territories != nil {
		out.Territories = in.Territories
	}
	if in.EmbargoUntil != nil {
		out.EmbargoUntil = in.EmbargoUntil
	}
	if in.ExpiresAt != nil {
		out.ExpiresAt = in.ExpiresAt
	}
	if in.Credit != nil {
		out.Credit = in.Credit
	}
	if in.ModelRelease != nil {
		out.ModelRelease = in.ModelRelease
	}
	return out
}

func effectiveProvenance(in *obx.ProvenanceInput) obx.Provenance {
	p := obx.DefaultProvenance()
	if in == nil {
		return p
	}
	if in.AIGenerated != nil {
		p.AIGenerated = *in.AIGenerated
		if p.AIGenerated {
			p.Source = "generated"
		}
	}
	if in.AIEdited != nil {
		p.AIEdited = *in.AIEdited
	}
	if in.Source != nil {
		p.Source = *in.Source
	}
	if in.C2PA != nil {
		p.C2PA = *in.C2PA
	}
	p.C2PAManifestURL = in.C2PAManifestURL
	p.Generator = in.Generator
	return p
}

func (s *state) brandRepr(r *record) obx.Brand {
	return obx.Brand{ID: r.id, ExternalID: r.extID, Name: r.brand.Name, LogoURL: r.brand.LogoURL, Website: r.brand.Website, Description: r.brand.Description, UpdatedAt: r.protoAt}
}

func (s *state) productRepr(r *record) obx.Product {
	in := r.product
	brandID := ""
	if b := s.brands[r.parentExt]; b != nil {
		brandID = b.id
	}
	var attrs map[string]any
	if len(in.Attributes) > 0 {
		attrs = in.Attributes
	}
	return obx.Product{ID: r.id, ExternalID: r.extID, BrandID: brandID, StyleNumber: in.StyleNumber, Name: in.Name,
		Description: nilIfEmpty(in.Description), ItemType: in.ItemType, GPCBrick: in.GPCBrick, TargetGroup: in.TargetGroup,
		Season: in.Season, Collection: in.Collection, Materials: in.Materials, Care: in.Care, CountryOfOrigin: in.CountryOfOrigin,
		DPPURL: in.DPPURL, Attributes: attrs, CreatedAt: r.createdAt, UpdatedAt: r.protoAt}
}

// cleanText drops languages whose text is empty; nil when nothing is left.
func cleanText(t obx.LocalizedText) obx.LocalizedText {
	out := obx.LocalizedText{}
	for k, v := range t {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return nilIfEmpty(out)
}

func nilIfEmpty(t obx.LocalizedText) obx.LocalizedText {
	if len(t) == 0 {
		return nil
	}
	return t
}

func (s *state) variantRepr(r *record) obx.Variant {
	in := r.variant
	p := s.products[r.parentExt]
	v := obx.Variant{ID: r.id, ExternalID: r.extID, SKU: in.SKU, Color: in.Color, Size: in.Size, SizeSystem: in.SizeSystem, UpdatedAt: r.protoAt}
	v.Color.Name = nilIfEmpty(v.Color.Name)
	if p != nil {
		v.ProductID = p.id
		if b := s.brands[p.parentExt]; b != nil {
			v.BrandID = b.id
		}
	}
	if in.GTIN != nil {
		g := r.gtin14
		v.GTIN = &g
		v.GTINOriginal = in.GTIN
	}
	return v
}

// urlSigner produces rendition URLs for a caller. nil = representation for
// hashing (no URLs).
type urlSigner struct {
	base            string
	originalAllowed bool
	expires         time.Time
	n               *Node
}

func (u *urlSigner) url(assetID, name string) (string, *time.Time) {
	if u == nil {
		return "", nil
	}
	exp := u.expires
	p := obx.PathAPIBase + "/assets/" + assetID + "/renditions/" + name
	e := fmt.Sprint(exp.Unix())
	return u.base + p + "?expires=" + e + "&sig=" + u.n.sign("GET", p, e), &exp
}

func (s *state) assetRepr(r *record, signer *urlSigner) obx.Asset {
	in := r.asset
	p := s.products[r.parentExt]
	orig := r.files[obx.RenditionOriginal]
	ob := s.blobs[orig.SHA256]
	a := obx.Asset{ID: r.id, ExternalID: r.extID, MediaType: "image", ColorCode: in.ColorCode, Role: in.Role, View: in.View,
		Position: in.Position, AltText: nilIfEmpty(in.AltText), Rights: s.effectiveRights(r), Provenance: effectiveProvenance(in.Provenance),
		SHA256: orig.SHA256, Width: orig.Width, Height: orig.Height, UpdatedAt: r.protoAt}
	if a.Rights.Channels == nil {
		a.Rights.Channels = []string{}
	}
	if ob != nil {
		a.ContentType = ob.contentType
		a.Bytes = int64(len(ob.data))
	}
	if p != nil {
		a.ProductID = p.id
		if b := s.brands[p.parentExt]; b != nil {
			a.BrandID = b.id
		}
	}
	if in.Filename != nil && *in.Filename != "" {
		a.Filename = *in.Filename
	} else {
		ext := ".jpg"
		switch a.ContentType {
		case "image/png":
			ext = ".png"
		case "image/webp":
			ext = ".webp"
		case "image/tiff":
			ext = ".tif"
		}
		a.Filename = r.extID + ext
	}
	for _, vid := range in.VariantExternalIDs {
		if v := s.variants[vid]; v != nil {
			a.VariantIDs = append(a.VariantIDs, v.id)
		}
	}
	mk := func(name string) obx.Rendition {
		f := r.files[name]
		rd := obx.Rendition{Name: name, SHA256: f.SHA256, Width: f.Width, Height: f.Height, ContentType: "image/jpeg"}
		if b := s.blobs[f.SHA256]; b != nil {
			rd.Bytes = int64(len(b.data))
			rd.ContentType = b.contentType
		}
		rd.URL, rd.URLExpiresAt = signer.url(r.id, name)
		return rd
	}
	a.Renditions = obx.Renditions{Web: mk(obx.RenditionWeb), Square: mk(obx.RenditionSquare), Thumb: mk(obx.RenditionThumb)}
	if signer == nil || signer.originalAllowed {
		o := mk(obx.RenditionOriginal)
		a.Renditions.Original = &o
	}
	return a
}

// productDetail builds a ProductDetail with the visible children.
func (s *state) productDetail(p *record, signer *urlSigner, now time.Time) obx.ProductDetail {
	d := obx.ProductDetail{Product: s.productRepr(p), Variants: []obx.Variant{}, Assets: []obx.Asset{}}
	for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
		if s.isVisible(v, now) {
			d.Variants = append(d.Variants, s.variantRepr(v))
		}
	}
	for _, a := range s.childrenOf(p.extID, obx.KindAsset) {
		if s.isVisible(a, now) {
			d.Assets = append(d.Assets, s.assetRepr(a, signer))
		}
	}
	return d
}

// childrenOf returns variants (sorted by external id) or assets (by colour,
// null first, then position) of a product.
func (s *state) childrenOf(productExt, kind string) []*record {
	var out []*record
	for _, r := range s.table(kind) {
		if r.parentExt == productExt {
			out = append(out, r)
		}
	}
	sortRecords(out)
	return out
}

func sortRecords(rs []*record) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.kind == obx.KindAsset && b.kind == obx.KindAsset {
			ac, bc := a.asset.ColorCode, b.asset.ColorCode
			if (ac == nil) != (bc == nil) {
				return ac == nil
			}
			if ac != nil && *ac != *bc {
				return *ac < *bc
			}
			if a.asset.Position != b.asset.Position {
				return a.asset.Position < b.asset.Position
			}
		}
		return a.extID < b.extID
	})
}

func (s *state) reprHash(r *record) string {
	var v any
	switch r.kind {
	case obx.KindBrand:
		b := s.brandRepr(r)
		b.UpdatedAt = time.Time{}
		v = b
	case obx.KindProduct:
		p := s.productRepr(r)
		p.UpdatedAt = time.Time{}
		v = p
	case obx.KindVariant:
		x := s.variantRepr(r)
		x.UpdatedAt = time.Time{}
		v = x
	case obx.KindAsset:
		if !s.filesComplete(r) {
			v = map[string]any{"incomplete": r.files, "in": r.input}
			break
		}
		a := s.assetRepr(r, nil)
		a.UpdatedAt = time.Time{}
		v = a
	}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ---- change log

var typeRank = map[string]int{obx.ChangeBrand: 0, obx.ChangeProduct: 1, obx.ChangeVariant: 2, obx.ChangeAsset: 3, obx.ChangeGrant: 4}

// commit compares the visible set with the previous commit and appends the
// resulting change entries (ingest.md section 8).
func (s *state) commit() {
	if s.holdCommits > 0 {
		return
	}
	now := s.n.now()
	cur := map[string]visInfo{}
	s.nextExpiry = time.Time{}
	for _, t := range []map[string]*record{s.brands, s.products, s.variants, s.assets} {
		for _, r := range t {
			h := s.reprHash(r)
			if h != r.protoHash {
				r.protoHash = h
				r.protoAt = now
			}
			if !s.isVisible(r, now) {
				continue
			}
			vi := visInfo{kind: r.kind, id: r.id, extID: r.extID, hash: h}
			if b := s.brandOf(r); b != nil {
				vi.brandID = b.id
			}
			if p := s.productOf(r); p != nil && r.kind != obx.KindProduct {
				vi.productID = p.id
			}
			if r.kind == obx.KindAsset {
				vi.colorNull = r.asset.ColorCode == nil
				vi.color = obx.Str(r.asset.ColorCode)
				vi.position = r.asset.Position
				if exp := s.effectiveRights(r).ExpiresAt; exp != nil && (s.nextExpiry.IsZero() || exp.Before(s.nextExpiry)) {
					s.nextExpiry = *exp
				}
			}
			cur[r.id] = vi
		}
	}
	var changes []entry
	for id, vi := range cur {
		prev, ok := s.visible[id]
		if !ok || prev.hash != vi.hash {
			r := s.byID[id]
			changes = append(changes, entry{typ: vi.kind, op: obx.OpUpsert, id: id, extID: vi.extID, brandID: vi.brandID, productID: vi.productID, at: r.protoAt})
		}
	}
	for id, vi := range s.visible {
		if _, ok := cur[id]; !ok {
			changes = append(changes, entry{typ: vi.kind, op: obx.OpDelete, id: id, extID: vi.extID, brandID: vi.brandID, productID: vi.productID, at: now})
		}
	}
	info := func(e entry) visInfo {
		if v, ok := cur[e.id]; ok {
			return v
		}
		return s.visible[e.id]
	}
	sort.Slice(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if typeRank[a.typ] != typeRank[b.typ] {
			return typeRank[a.typ] < typeRank[b.typ]
		}
		if a.typ == obx.ChangeAsset {
			ai, bi := info(a), info(b)
			if ai.colorNull != bi.colorNull {
				return ai.colorNull
			}
			if ai.color != bi.color {
				return ai.color < bi.color
			}
			if ai.position != bi.position {
				return ai.position < bi.position
			}
		}
		return a.extID < b.extID
	})
	for _, e := range changes {
		s.appendEntry(e)
	}
	s.visible = cur
	if len(changes) > 0 {
		s.n.notifyChanges(s.seq)
	}
}

func (s *state) appendEntry(e entry) {
	s.seq++
	e.seq = s.seq
	s.log = append(s.log, e)
	s.lastSeq[e.id] = e.seq
}

// grantChanged writes a grant entry for a retailer.
func (s *state) grantChanged(r *retailer, resync bool) {
	r.grantUpdatedAt = s.n.now()
	s.appendEntry(entry{typ: obx.ChangeGrant, op: obx.OpUpsert, id: r.grantID, at: r.grantUpdatedAt, retailerID: r.id, resync: resync})
	s.n.notifyGrant(r)
}

// tick expires assets whose rights ran out.
func (s *state) tick() {
	if !s.nextExpiry.IsZero() && !s.n.now().Before(s.nextExpiry) {
		s.commit()
	}
}

// ---- helpers

func canonicalJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func fileExt(name string) string { return strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")) }
