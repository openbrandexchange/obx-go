package mock

import (
	"archive/zip"
	"bytes"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// portalCtx is who looks at the portal: a retailer user, or an admin
// previewing everything that is published.
type portalCtx struct {
	user     *user
	rt       *retailer
	preview  bool
	original bool
}

func (n *Node) portal(ac *adminCtx) *portalCtx {
	s := n.st
	if ac.isAdmin() {
		return &portalCtx{user: ac.user, rt: &retailer{status: "active"}, preview: true, original: true}
	}
	rt := s.retailers[ac.user.retailerID]
	return &portalCtx{user: ac.user, rt: rt, original: s.originalAllowed(rt)}
}

// logged returns the retailer for statistics (nil for previews).
func (p *portalCtx) logged() *retailer {
	if p.preview {
		return nil
	}
	return p.rt
}

func (n *Node) portalSigner(r *http.Request, pc *portalCtx) *urlSigner {
	return &urlSigner{base: n.baseURL(r), originalAllowed: pc.original, expires: n.now().Add(time.Hour).Truncate(time.Second), n: n}
}

type selection struct {
	productIDs []string
	updatedAt  time.Time
}

func (n *Node) handlePortalViewer(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	s := n.st
	pc := n.portal(ac)
	now := n.now()
	v := nodeadmin.PortalViewer{Preview: pc.preview, Brands: []obx.Brand{}, Branding: n.publicBranding(r), Languages: s.settings.languages,
		Permissions: nodeadmin.PortalPermissions{OriginalDownload: pc.original, ArticleDataExport: pc.preview || s.settings.allowDataExport, APISelfService: !pc.preview && s.settings.apiSelfService},
		Limits:      &nodeadmin.PortalLimits{SelectionMaxProducts: 1000, SyncZipMaxImages: 400, GTINLookupMax: 2000}}
	if !pc.preview {
		v.Retailer = &nodeadmin.IDName{ID: pc.rt.id, Name: pc.rt.name}
	}
	for _, b := range sortedByID(s.brands) {
		if s.visibleTo(b, pc.rt, now) {
			v.Brands = append(v.Brands, s.brandRepr(b))
		}
	}
	if sel := s.selections[pc.user.id]; sel != nil {
		v.SelectionCount = len(n.visibleSelection(pc, sel))
	}
	writeJSON(w, http.StatusOK, v)
}

// portalFilter reads the product filters from the query.
func portalFilter(r *http.Request) nodeadmin.PortalFilter {
	q := r.URL.Query()
	f := nodeadmin.PortalFilter{Q: strings.TrimSpace(q.Get("q")), BrandID: q["brand_id"], ItemType: q["item_type"], Color: q["color"],
		Season: q["season"], Collection: q["collection"], TargetGroup: q["target_group"]}
	if v := q.Get("updated_since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.UpdatedSince = &t
		}
	}
	return f
}

// filteredProducts returns the visible products matching f, sorted by style
// number.
func (n *Node) filteredProducts(pc *portalCtx, f nodeadmin.PortalFilter) []*record {
	s := n.st
	now := n.now()
	var out []*record
	needle := strings.ToLower(f.Q)
	for _, p := range sortedByID(s.products) {
		if !s.visibleTo(p, pc.rt, now) {
			continue
		}
		in := p.product
		if len(f.BrandID) > 0 && !obx.Contains(f.BrandID, s.brands[p.parentExt].id) {
			continue
		}
		if len(f.ItemType) > 0 && !obx.Contains(f.ItemType, obx.Str(in.ItemType)) {
			continue
		}
		if len(f.Season) > 0 && !obx.Contains(f.Season, obx.Str(in.Season)) {
			continue
		}
		if len(f.Collection) > 0 && !obx.Contains(f.Collection, obx.Str(in.Collection)) {
			continue
		}
		if len(f.TargetGroup) > 0 && !obx.Contains(f.TargetGroup, obx.Str(in.TargetGroup)) {
			continue
		}
		if len(f.Color) > 0 {
			match := false
			for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
				if s.isVisible(v, now) && obx.Contains(f.Color, v.variant.Color.Code) {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if f.UpdatedSince != nil {
			latest := p.protoAt
			for _, k := range []string{obx.KindVariant, obx.KindAsset} {
				for _, c := range s.childrenOf(p.extID, k) {
					if s.isVisible(c, now) && c.protoAt.After(latest) {
						latest = c.protoAt
					}
				}
			}
			if latest.Before(*f.UpdatedSince) {
				continue
			}
		}
		if needle != "" && !productMatches(s, p, needle) {
			if g, ok := obx.NormalizeLookupGTIN(needle); !ok || !strings.Contains(strings.Join(variantGTINs(s, p), " "), g) {
				continue
			}
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].product.StyleNumber < out[j].product.StyleNumber })
	return out
}

func variantGTINs(s *state, p *record) []string {
	var out []string
	for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
		out = append(out, v.gtin14)
	}
	return out
}

func (n *Node) portalSummary(r *http.Request, pc *portalCtx, p *record) nodeadmin.PortalProduct {
	s := n.st
	now := n.now()
	b := s.brands[p.parentExt]
	in := p.product
	sum := nodeadmin.PortalProduct{ID: p.id, StyleNumber: in.StyleNumber, Name: in.Name, Brand: nodeadmin.IDName{ID: b.id, Name: b.brand.Name},
		ItemType: in.ItemType, TargetGroup: in.TargetGroup, Season: in.Season, Collection: in.Collection, Colors: []nodeadmin.PortalColor{}, UpdatedAt: p.protoAt}
	signer := n.portalSigner(r, pc)
	colorIdx := map[string]int{}
	for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
		if !s.isVisible(v, now) {
			continue
		}
		sum.VariantCount++
		code := v.variant.Color.Code
		if _, ok := colorIdx[code]; !ok {
			colorIdx[code] = len(sum.Colors)
			sum.Colors = append(sum.Colors, nodeadmin.PortalColor{Code: code, Name: nilIfEmpty(v.variant.Color.Name)})
		}
	}
	for _, a := range s.childrenOf(p.extID, obx.KindAsset) {
		if !s.isVisible(a, now) {
			continue
		}
		sum.ImageCount++
		u, _ := signer.url(a.id, obx.RenditionThumb)
		if sum.ThumbURL == nil {
			sum.ThumbURL = ptr(u)
		}
		if a.asset.ColorCode != nil {
			if i, ok := colorIdx[*a.asset.ColorCode]; ok && sum.Colors[i].ThumbURL == nil {
				sum.Colors[i].ThumbURL = ptr(u)
			}
		}
	}
	return sum
}

func (n *Node) handlePortalProducts(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	list := n.filteredProducts(pc, portalFilter(r))
	items := make([]any, len(list))
	for i, p := range list {
		items[i] = n.portalSummary(r, pc, p)
	}
	pagedWithTotal(w, r, items, 48, 200)
}

func (n *Node) handlePortalFacets(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	s := n.st
	pc := n.portal(ac)
	now := n.now()
	type counter struct {
		order  []string
		counts map[string]int
		labels map[string]any
	}
	mk := func() *counter { return &counter{counts: map[string]int{}, labels: map[string]any{}} }
	brands, types, colors, seasons, collections, groups := mk(), mk(), mk(), mk(), mk(), mk()
	add := func(c *counter, v string, label any) {
		if v == "" {
			return
		}
		if _, ok := c.counts[v]; !ok {
			c.order = append(c.order, v)
			c.labels[v] = label
		}
		c.counts[v]++
	}
	for _, p := range n.filteredProducts(pc, portalFilter(r)) {
		in := p.product
		b := s.brands[p.parentExt]
		add(brands, b.id, b.brand.Name)
		add(types, obx.Str(in.ItemType), nil)
		add(seasons, obx.Str(in.Season), nil)
		add(collections, obx.Str(in.Collection), nil)
		add(groups, obx.Str(in.TargetGroup), nil)
		seen := map[string]bool{}
		for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
			if s.isVisible(v, now) && !seen[v.variant.Color.Code] {
				seen[v.variant.Color.Code] = true
				var label any
				if len(v.variant.Color.Name) > 0 {
					label = v.variant.Color.Name
				}
				add(colors, v.variant.Color.Code, label)
			}
		}
	}
	list := func(c *counter) []nodeadmin.FacetValue {
		out := []nodeadmin.FacetValue{}
		sort.Strings(c.order)
		for _, v := range c.order {
			out = append(out, nodeadmin.FacetValue{Value: v, Label: c.labels[v], Count: c.counts[v]})
		}
		return out
	}
	writeJSON(w, http.StatusOK, nodeadmin.Facets{Brands: list(brands), ItemTypes: list(types), Colors: list(colors), Seasons: list(seasons), Collections: list(collections), TargetGroups: list(groups)})
}

func (n *Node) portalProduct(w http.ResponseWriter, pc *portalCtx, id string) *record {
	p := n.st.byID[id]
	if p == nil || p.kind != obx.KindProduct || !n.st.visibleTo(p, pc.rt, n.now()) {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return nil
	}
	return p
}

func (n *Node) handlePortalProduct(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	if p := n.portalProduct(w, pc, r.PathValue("id")); p != nil {
		writeJSON(w, http.StatusOK, n.st.productDetail(p, n.portalSigner(r, pc), n.now()))
	}
}

func (n *Node) handlePortalGTIN(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	var in struct {
		Text  string   `json:"text"`
		GTINs []string `json:"gtins"`
	}
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	inputs := in.GTINs
	if in.Text != "" {
		inputs = append(inputs, splitCodes(in.Text)...)
	}
	if len(inputs) > 2000 {
		writeProblem(w, problem(obx.ErrLimitExceeded, "at most 2000 EANs at once"))
		return
	}
	writeJSON(w, http.StatusOK, n.lookupGTINs(inputs, pc.rt, n.portalSigner(r, pc)))
}

// splitCodes splits pasted text on newlines, tabs, commas and semicolons
// (not on spaces, which may sit inside a code).
func splitCodes(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' || r == ',' || r == ';' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ---- selection

func (n *Node) visibleSelection(pc *portalCtx, sel *selection) []*record {
	var out []*record
	for _, id := range sel.productIDs {
		if p := n.st.byID[id]; p != nil && p.kind == obx.KindProduct && n.st.visibleTo(p, pc.rt, n.now()) {
			out = append(out, p)
		}
	}
	return out
}

func (n *Node) selectionView(pc *portalCtx) nodeadmin.Selection {
	out := nodeadmin.Selection{ProductIDs: []string{}}
	sel := n.st.selections[pc.user.id]
	if sel == nil {
		return out
	}
	now := n.now()
	for _, p := range n.visibleSelection(pc, sel) {
		out.ProductIDs = append(out.ProductIDs, p.id)
		for _, a := range n.st.childrenOf(p.extID, obx.KindAsset) {
			if n.st.isVisible(a, now) {
				out.ImageCount++
			}
		}
	}
	at := sel.updatedAt
	out.UpdatedAt = &at
	return out
}

func (n *Node) handleSelection(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, n.selectionView(pc))
	case http.MethodDelete:
		delete(n.st.selections, pc.user.id)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPut:
		var in struct {
			ProductIDs []string `json:"product_ids"`
		}
		if p := readJSON(r, &in); p != nil {
			writeProblem(w, p)
			return
		}
		if len(in.ProductIDs) > 1000 {
			writeProblem(w, problem(obx.ErrLimitExceeded, "a selection holds at most 1000 products"))
			return
		}
		seen := map[string]bool{}
		sel := &selection{updatedAt: n.now()}
		for _, id := range in.ProductIDs {
			if !seen[id] {
				seen[id] = true
				sel.productIDs = append(sel.productIDs, id)
			}
		}
		n.st.selections[pc.user.id] = sel
		writeJSON(w, http.StatusOK, n.selectionView(pc))
	}
}

// ---- downloads

// portalVariant resolves ?variant= (default original when allowed, else web).
func portalVariant(r *http.Request, pc *portalCtx) (string, *obx.Problem) {
	v := r.URL.Query().Get("variant")
	if v == "" {
		if pc.original {
			return obx.RenditionOriginal, nil
		}
		return obx.RenditionWeb, nil
	}
	if v != obx.RenditionOriginal && v != obx.RenditionWeb && v != obx.RenditionSquare {
		return "", problem(obx.ErrValidationFailed, "variant must be original, web or square")
	}
	if v == obx.RenditionOriginal && !pc.original {
		return "", problem(obx.ErrOriginalNotAllowed, "original files are not available to you")
	}
	return v, nil
}

type zipEntry struct {
	name string
	data []byte
}

func buildZip(entries []zipEntry, at time.Time) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := map[string]int{}
	for _, e := range entries {
		name := e.name
		if c := used[name]; c > 0 {
			ext := ""
			if i := strings.LastIndex(name, "."); i > 0 {
				name, ext = name[:i], name[i:]
			}
			name = name + "_" + string(rune('1'+c)) + ext
		}
		used[e.name]++
		fw, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: at})
		fw.Write(e.data)
	}
	zw.Close()
	return buf.Bytes()
}

// productFiles returns the zip entries of a product's visible images.
func (n *Node) productFiles(p *record, variant, folder string) []zipEntry {
	s := n.st
	now := n.now()
	var out []zipEntry
	for _, a := range s.childrenOf(p.extID, obx.KindAsset) {
		if !s.isVisible(a, now) {
			continue
		}
		b := s.blobs[a.files[variant].SHA256]
		name := canonicalcsv.RenditionFilename(s.assetRepr(a, nil).Filename, variant)
		if folder != "" {
			name = safeZipName(folder) + "/" + safeZipName(name)
		}
		out = append(out, zipEntry{name: name, data: b.data})
	}
	return out
}

func (n *Node) sendZip(w http.ResponseWriter, r *http.Request, data []byte, name string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", name))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", n.now(), bytes.NewReader(data))
}

func (n *Node) handlePortalAssetDownload(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	s := n.st
	pc := n.portal(ac)
	a := s.byID[r.PathValue("id")]
	if a == nil || a.kind != obx.KindAsset || !s.visibleTo(a, pc.rt, n.now()) {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	variant, p := portalVariant(r, pc)
	if p != nil {
		writeProblem(w, p)
		return
	}
	b := s.blobs[a.files[variant].SHA256]
	s.logDownload(downloadEvent{kind: "image", retailer: pc.logged(), actorType: "user", actorName: pc.user.name, variant: variant, productID: s.products[a.parentExt].id, assetID: a.id, files: 1, bytes: int64(len(b.data))})
	n.serveBlob(w, r, b, canonicalcsv.RenditionFilename(s.assetRepr(a, nil).Filename, variant), "attachment")
}

func (n *Node) handlePortalProductZip(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	p := n.portalProduct(w, pc, r.PathValue("id"))
	if p == nil {
		return
	}
	variant, prob := portalVariant(r, pc)
	if prob != nil {
		writeProblem(w, prob)
		return
	}
	files := n.productFiles(p, variant, "")
	data := buildZip(files, n.now())
	n.st.logDownload(downloadEvent{kind: "product_zip", retailer: pc.logged(), actorType: "user", actorName: pc.user.name, variant: variant, productID: p.id, files: len(files), bytes: int64(len(data))})
	n.sendZip(w, r, data, safeZipName(p.product.StyleNumber)+".zip")
}

func (n *Node) handleSelectionZip(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	variant, prob := portalVariant(r, pc)
	if prob != nil {
		writeProblem(w, prob)
		return
	}
	sel := n.st.selections[pc.user.id]
	if sel == nil || len(sel.productIDs) == 0 {
		writeProblem(w, problem(obx.ErrInvalidRequest, "the selection is empty"))
		return
	}
	var files []zipEntry
	for _, p := range n.visibleSelection(pc, sel) {
		files = append(files, n.productFiles(p, variant, p.product.StyleNumber)...)
	}
	if len(files) > 400 {
		writeProblem(w, problem(obx.ErrLimitExceeded, "more than 400 images; use a background download"))
		return
	}
	data := buildZip(files, n.now())
	n.st.logDownload(downloadEvent{kind: "selection_zip", retailer: pc.logged(), actorType: "user", actorName: pc.user.name, variant: variant, files: len(files), bytes: int64(len(data))})
	n.sendZip(w, r, data, "selection.zip")
}

func (n *Node) exportCSV(r *http.Request, pc *portalCtx, products []*record) []byte {
	s := n.st
	now := n.now()
	var details []obx.ProductDetail
	names := map[string]string{}
	for _, p := range products {
		details = append(details, s.productDetail(p, nil, now))
		b := s.brands[p.parentExt]
		names[b.id] = b.brand.Name
	}
	delim := ','
	if r.URL.Query().Get("delimiter") == "semicolon" {
		delim = ';'
	}
	var buf bytes.Buffer
	_ = canonicalcsv.WriteExport(&buf, details, canonicalcsv.ExportOptions{Languages: s.settings.languages, Delimiter: delim, BrandNames: names, BOM: true})
	return buf.Bytes()
}

func (n *Node) sendCSV(w http.ResponseWriter, r *http.Request, pc *portalCtx, products []*record, name string) {
	if !pc.preview && !n.st.settings.allowDataExport {
		writeProblem(w, problem(obx.ErrForbidden, "this brand does not offer article data downloads"))
		return
	}
	data := n.exportCSV(r, pc, products)
	n.st.logDownload(downloadEvent{kind: "csv", retailer: pc.logged(), actorType: "user", actorName: pc.user.name, files: len(products), bytes: int64(len(data))})
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", name))
	w.Write(data)
}

func (n *Node) handleSelectionCSV(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	var products []*record
	if sel := n.st.selections[pc.user.id]; sel != nil {
		products = n.visibleSelection(pc, sel)
	}
	n.sendCSV(w, r, pc, products, "selection.csv")
}

func (n *Node) handleProductsCSV(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	pc := n.portal(ac)
	n.sendCSV(w, r, pc, n.filteredProducts(pc, portalFilter(r)), "products.csv")
}

// ---- download jobs

type downloadJob struct {
	nodeadmin.DownloadJob
	userID string
	file   []byte
}

func (n *Node) handleDownloadJobs(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	s := n.st
	pc := n.portal(ac)
	if r.Method == http.MethodGet {
		out := obx.Items[nodeadmin.DownloadJob]{Items: []nodeadmin.DownloadJob{}}
		for _, id := range sortedKeys(s.downloadJobs) {
			if j := s.downloadJobs[id]; j.userID == pc.user.id {
				out.Items = append(out.Items, n.jobState(j))
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var in nodeadmin.DownloadJobInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if in.Variant == "" {
		in.Variant = obx.RenditionWeb
	}
	if in.Variant != obx.RenditionOriginal && in.Variant != obx.RenditionWeb && in.Variant != obx.RenditionSquare {
		writeProblem(w, problem(obx.ErrValidationFailed, "variant must be original, web or square"))
		return
	}
	if in.Variant == obx.RenditionOriginal && !pc.original {
		writeProblem(w, problem(obx.ErrOriginalNotAllowed, "original files are not available to you"))
		return
	}
	var products []*record
	switch in.Source {
	case "selection":
		if sel := s.selections[pc.user.id]; sel != nil {
			products = n.visibleSelection(pc, sel)
		}
	case "products":
		if len(in.ProductIDs) > 10000 {
			writeProblem(w, problem(obx.ErrLimitExceeded, "at most 10 000 products"))
			return
		}
		products = n.visibleSelection(pc, &selection{productIDs: in.ProductIDs})
	case "filter":
		f := nodeadmin.PortalFilter{}
		if in.Filter != nil {
			f = *in.Filter
		}
		products = n.filteredProducts(pc, f)
	default:
		writeProblem(w, problem(obx.ErrValidationFailed, "source must be selection, products or filter"))
		return
	}
	includeCSV := in.IncludeDataCSV == nil || *in.IncludeDataCSV
	if !pc.preview && !s.settings.allowDataExport {
		includeCSV = false
	}
	var files []zipEntry
	for _, p := range products {
		files = append(files, n.productFiles(p, in.Variant, p.product.StyleNumber)...)
	}
	if includeCSV {
		files = append(files, zipEntry{name: "products.csv", data: n.exportCSV(r, pc, products)})
	}
	now := n.now()
	data := buildZip(files, now)
	exp := now.Add(48 * time.Hour)
	j := &downloadJob{userID: pc.user.id, file: data}
	j.DownloadJob = nodeadmin.DownloadJob{ID: s.newID("dlj"), Status: obx.JobSucceeded, Source: in.Source, Variant: in.Variant, IncludeDataCSV: includeCSV,
		CreatedAt: now, FinishedAt: &now, ExpiresAt: &exp, Progress: obx.Progress{Done: len(files), Total: ptr(len(files))},
		FileURL: ptr(obx.PathAdminAPI + "/portal/download-jobs/" + "%s/file"), Bytes: ptr(int64(len(data))), Files: ptr(len(files))}
	j.FileURL = ptr(obx.PathAdminAPI + "/portal/download-jobs/" + j.ID + "/file")
	s.downloadJobs[j.ID] = j
	s.logDownload(downloadEvent{kind: "bulk_zip", retailer: pc.logged(), actorType: "user", actorName: pc.user.name, variant: in.Variant, files: len(files), bytes: int64(len(data))})
	n.email(pc.user.email, "zip_ready", pc.user.language, n.baseURL(r)+*j.FileURL, nil)
	writeJSON(w, http.StatusAccepted, n.jobState(j))
}

func (n *Node) jobState(j *downloadJob) nodeadmin.DownloadJob {
	v := j.DownloadJob
	if v.ExpiresAt != nil && n.now().After(*v.ExpiresAt) {
		v.Status, v.FileURL = obx.JobExpired, nil
	}
	return v
}

func (n *Node) ownedDownloadJob(w http.ResponseWriter, r *http.Request, ac *adminCtx) *downloadJob {
	j := n.st.downloadJobs[r.PathValue("id")]
	if j == nil || j.userID != ac.user.id {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return nil
	}
	return j
}

func (n *Node) handleDownloadJob(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	if j := n.ownedDownloadJob(w, r, ac); j != nil {
		writeJSON(w, http.StatusOK, n.jobState(j))
	}
}

func (n *Node) handleDownloadJobFile(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	j := n.ownedDownloadJob(w, r, ac)
	if j == nil {
		return
	}
	if n.jobState(j).Status == obx.JobExpired {
		writeProblem(w, problem(obx.ErrExportExpired, "this download expired; start a new one"))
		return
	}
	n.sendZip(w, r, j.file, "download-"+j.ID+".zip")
}

// ---- self-service API keys

func (n *Node) selfService(w http.ResponseWriter, ac *adminCtx) *retailer {
	if ac.isAdmin() || !n.st.settings.apiSelfService {
		writeProblem(w, problem(obx.ErrForbidden, "API keys are created by the brand"))
		return nil
	}
	return n.st.retailers[ac.user.retailerID]
}

func (n *Node) handlePortalClients(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	rt := n.selfService(w, ac)
	if rt == nil {
		return
	}
	s := n.st
	if r.Method == http.MethodGet {
		out := obx.Items[nodeadmin.APIClient]{Items: []nodeadmin.APIClient{}}
		for _, id := range sortedKeys(s.clients) {
			if c := s.clients[id]; c.retailerID == rt.id {
				out.Items = append(out.Items, n.clientView(c))
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var in nodeadmin.APIClientInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if strings.TrimSpace(in.Name) == "" || !validScopes(in.Scopes) {
		writeProblem(w, problem(obx.ErrValidationFailed, "a name and valid scopes are required"))
		return
	}
	active := 0
	for _, c := range s.clients {
		if c.retailerID == rt.id && c.status == "active" {
			active++
		}
	}
	if active >= 5 {
		writeProblem(w, problem(obx.ErrConflict, "at most 5 API keys; revoke one first"))
		return
	}
	var scopes []string
	if len(in.Scopes) > 0 {
		scopes = in.Scopes
	}
	c, secret := n.newClient(rt, in.Name, scopes)
	writeJSON(w, http.StatusCreated, n.withSecret(r, c, secret))
}

func (n *Node) handlePortalClient(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	rt := n.selfService(w, ac)
	if rt == nil {
		return
	}
	c := n.st.clients[r.PathValue("id")]
	if c == nil || c.retailerID != rt.id {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	n.clientAction(w, r, c)
}
