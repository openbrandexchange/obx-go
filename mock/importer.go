package mock

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/obx"
)

// importInput is one canonical import, however it arrived.
type importInput struct {
	pkg       []byte
	csv       []byte
	csvName   string
	assets    []byte
	images    []namedFile
	manifest  []obx.ManifestImage
	options   obx.ImportOptions
	fileInfos []obx.ImportRunFile
}

type namedFile struct {
	name string
	data []byte
}

// parseImportForm reads the multipart body of POST /imports.
func (n *Node) parseImportForm(r *http.Request) (*importInput, *obx.Problem) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return nil, problem(obx.ErrUnsupportedMediaType, "send multipart/form-data")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 2<<30)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, problem(obx.ErrPayloadTooLarge, "the import is larger than 2 GiB")
		}
		return nil, problemf(obx.ErrInvalidRequest, "could not read the form: %v", err)
	}
	in := &importInput{}
	read := func(fh *multipart.FileHeader) ([]byte, error) {
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(f)
	}
	one := func(field string) (*multipart.FileHeader, []byte, *obx.Problem) {
		fhs := r.MultipartForm.File[field]
		if len(fhs) == 0 {
			if v := r.MultipartForm.Value[field]; len(v) > 0 {
				return &multipart.FileHeader{Filename: field}, []byte(v[0]), nil
			}
			return nil, nil, nil
		}
		b, err := read(fhs[0])
		if err != nil {
			return nil, nil, problemf(obx.ErrInvalidRequest, "could not read %s", field)
		}
		in.fileInfos = append(in.fileInfos, obx.ImportRunFile{Name: fhs[0].Filename, Bytes: int64(len(b)), SHA256: ptr(hashBytes(b))})
		return fhs[0], b, nil
	}
	var p *obx.Problem
	if _, in.pkg, p = one("package"); p != nil {
		return nil, p
	}
	var fh *multipart.FileHeader
	if fh, in.csv, p = one("csv"); p != nil {
		return nil, p
	}
	if fh != nil {
		in.csvName = path.Base(fh.Filename)
	}
	if _, in.assets, p = one("assets_csv"); p != nil {
		return nil, p
	}
	for _, fh := range r.MultipartForm.File["images"] {
		b, err := read(fh)
		if err != nil {
			return nil, problem(obx.ErrInvalidRequest, "could not read an image")
		}
		in.images = append(in.images, namedFile{name: path.Base(strings.ReplaceAll(fh.Filename, "\\", "/")), data: b})
		in.fileInfos = append(in.fileInfos, obx.ImportRunFile{Name: fh.Filename, Bytes: int64(len(b)), SHA256: ptr(hashBytes(b))})
	}
	if raw := formJSON(r, "images_manifest"); raw != nil {
		if err := json.Unmarshal(raw, &in.manifest); err != nil {
			return nil, problem(obx.ErrInvalidRequest, "images_manifest must be a JSON array")
		}
	}
	if raw := formJSON(r, "options"); raw != nil {
		if err := json.Unmarshal(raw, &in.options); err != nil {
			return nil, problem(obx.ErrInvalidRequest, "options must be a JSON object")
		}
	}
	if in.options.Mode == "" {
		in.options.Mode = obx.ImportModeUpsert
	}
	if in.options.Mode != obx.ImportModeUpsert && in.options.Mode != obx.ImportModeMirror {
		return nil, problem(obx.ErrValidationFailed, "options.mode must be upsert or mirror")
	}
	if in.pkg == nil && in.csv == nil && in.assets == nil && len(in.images) == 0 && len(in.manifest) == 0 {
		return nil, problem(obx.ErrInvalidRequest, "send a package, a csv, images or an images_manifest")
	}
	return in, nil
}

// formJSON returns a JSON form field sent as a value or as a file part.
func formJSON(r *http.Request, field string) []byte {
	if v := r.MultipartForm.Value[field]; len(v) > 0 {
		return []byte(v[0])
	}
	if fhs := r.MultipartForm.File[field]; len(fhs) > 0 {
		f, err := fhs[0].Open()
		if err != nil {
			return nil
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return b
	}
	return nil
}

// errImportInvalid marks a file that makes the whole import unusable.
type errImportInvalid struct{ msg string }

func (e errImportInvalid) Error() string { return e.msg }

// unzip reads a package, refusing unsafe entries (zip slip) and bombs.
func unzip(data []byte, maxTotal int64) ([]namedFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errImportInvalid{"the package is not a zip file"}
	}
	if len(zr.File) > 100000 {
		return nil, errImportInvalid{"the package has more than 100 000 entries"}
	}
	var out []namedFile
	var total int64
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
			return nil, errImportInvalid{fmt.Sprintf("entry %q has an absolute path", f.Name)}
		}
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				return nil, errImportInvalid{fmt.Sprintf("entry %q leaves the package (..)", f.Name)}
			}
		}
		mode := f.Mode()
		if mode&0o170000 == 0o120000 || mode.Type()&(1<<27) != 0 {
			return nil, errImportInvalid{fmt.Sprintf("entry %q is a symbolic link", f.Name)}
		}
		if f.FileInfo().IsDir() || canonicalcsv.IgnoredPath(name) {
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxTotal {
			return nil, errImportInvalid{"the package unpacks to more than the node accepts"}
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errImportInvalid{fmt.Sprintf("entry %q cannot be read", f.Name)}
		}
		b, err := io.ReadAll(io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		rc.Close()
		if err != nil {
			return nil, errImportInvalid{fmt.Sprintf("entry %q cannot be read", f.Name)}
		}
		out = append(out, namedFile{name: name, data: b})
	}
	return out, nil
}

// catalogView adapts the state for canonicalcsv.BuildPlan.
type catalogView struct{ s *state }

func (c catalogView) Brands() []canonicalcsv.CatalogBrand {
	var out []canonicalcsv.CatalogBrand
	for _, b := range sortedByID(c.s.brands) {
		out = append(out, canonicalcsv.CatalogBrand{ExternalID: b.extID, Name: b.brand.Name})
	}
	return out
}

func (c catalogView) FindProduct(key string) (canonicalcsv.CatalogProduct, bool) {
	p := c.s.products[key]
	if p == nil {
		for _, x := range sortedByID(c.s.products) {
			if strings.EqualFold(x.product.StyleNumber, key) {
				p = x
				break
			}
		}
	}
	if p == nil {
		return canonicalcsv.CatalogProduct{}, false
	}
	out := canonicalcsv.CatalogProduct{ExternalID: p.extID, StyleNumber: p.product.StyleNumber, BrandID: p.parentExt}
	seen := map[string]bool{}
	for _, v := range c.s.childrenOf(p.extID, obx.KindVariant) {
		if !seen[v.variant.Color.Code] {
			seen[v.variant.Color.Code] = true
			out.Colors = append(out.Colors, v.variant.Color.Code)
		}
	}
	return out, true
}

// runImport executes a canonical import and returns the run and its
// problems. It holds n.mu.
func (n *Node) runImport(in *importInput, trigger string, wr writer) (*obx.ImportRun, []obx.ImportRowError) {
	s := n.st
	now := n.now()
	run := &obx.ImportRun{ID: s.newID("imp"), Trigger: trigger, Status: obx.JobRunning, Mode: in.options.Mode, DryRun: in.options.DryRun,
		CreatedAt: now, StartedAt: &now, Files: in.fileInfos, ErrorsPreview: []obx.ImportRowError{}}
	if run.Files == nil {
		run.Files = []obx.ImportRunFile{}
	}
	var problems []obx.ImportRowError
	fail := func(code, detail string) (*obx.ImportRun, []obx.ImportRowError) {
		p := problem(code, detail)
		run.Error = p
		problems = append(problems, obx.ImportRowError{File: "import", Severity: obx.SeverityError, ObxError: code, Detail: detail})
		return n.finishRun(run, problems, obx.JobFailed)
	}

	images := append([]namedFile(nil), in.images...)
	csvData, csvName, assetsData := in.csv, in.csvName, in.assets
	maxTotal := 10 * n.opts.MaxUploadBytes
	expand := func(files []namedFile) ([]namedFile, error) {
		var out []namedFile
		for _, f := range files {
			if bytes.HasPrefix(f.data, []byte("PK\x03\x04")) {
				inner, err := unzip(f.data, maxTotal)
				if err != nil {
					return nil, err
				}
				for _, e := range inner {
					if canonicalcsv.IsImageFile(e.name) {
						out = append(out, namedFile{name: path.Base(e.name), data: e.data})
					}
				}
				continue
			}
			out = append(out, f)
		}
		return out, nil
	}
	var err error
	if images, err = expand(images); err != nil {
		return fail(obx.ErrImportInvalid, err.Error())
	}
	if in.pkg != nil {
		entries, err := unzip(in.pkg, maxTotal)
		if err != nil {
			return fail(obx.ErrImportInvalid, err.Error())
		}
		var csvs []namedFile
		for _, e := range entries {
			base := strings.ToLower(path.Base(e.name))
			switch {
			case base == "products.csv":
				csvData, csvName = e.data, path.Base(e.name)
			case base == "assets.csv":
				assetsData = e.data
			case strings.HasSuffix(base, ".csv"):
				csvs = append(csvs, e)
			case canonicalcsv.IsImageFile(e.name):
				images = append(images, namedFile{name: path.Base(e.name), data: e.data})
			}
		}
		if csvData == nil && len(csvs) == 1 {
			csvData, csvName = csvs[0].data, path.Base(csvs[0].name)
		}
	}
	lang := in.options.DefaultLanguage
	if lang == "" {
		lang = s.settings.languages[0]
	}
	opts := canonicalcsv.Options{DefaultLanguage: lang}
	var pf *canonicalcsv.ProductsFile
	if csvData != nil {
		if csvName == "" {
			csvName = "products.csv"
		}
		if pf, err = canonicalcsv.ParseProducts(bytes.NewReader(csvData), csvName, opts); err != nil {
			return fail(obx.ErrImportInvalid, err.Error())
		}
	}
	var af *canonicalcsv.AssetsFile
	if assetsData != nil {
		if af, err = canonicalcsv.ParseAssets(bytes.NewReader(assetsData), "assets.csv", opts); err != nil {
			return fail(obx.ErrImportInvalid, err.Error())
		}
	}

	// Images: store uploaded bytes, resolve the manifest.
	var planImages []canonicalcsv.Image
	for _, img := range images {
		b, p := s.storeChecked(img.data)
		if p != nil {
			problems = append(problems, obx.ImportRowError{File: img.name, Severity: obx.SeverityError, ObxError: p.ObxError, Detail: p.DetailString()})
			continue
		}
		planImages = append(planImages, canonicalcsv.Image{Name: img.name, SHA256: b.sha, Bytes: int64(len(b.data)), ContentType: b.contentType, Width: b.width, Height: b.height})
	}
	for _, m := range in.manifest {
		b := s.blobs[strings.ToLower(m.SHA256)]
		if b == nil && in.options.DryRun && !n.opts.DryRunNeedsBlobs {
			// A dry run checks what the import would do once the client has
			// uploaded its images (obx push --dry-run uploads nothing): a
			// placeholder stands in for the bytes until the run ends.
			sha := strings.ToLower(m.SHA256)
			ct := canonicalcsv.ImageExtensions[strings.ToLower(strings.TrimPrefix(path.Ext(m.File), "."))]
			b = &blob{sha: sha, contentType: ct, width: m.Width, height: m.Height, placeholder: true}
			s.blobs[sha] = b
			defer delete(s.blobs, sha)
			if r := m.Renditions; r != nil {
				for _, ref := range []*obx.BlobRef{r.Web, r.Square, r.Thumb} {
					if ref != nil && s.blobs[ref.SHA256] == nil {
						s.blobs[ref.SHA256] = &blob{sha: ref.SHA256, contentType: "image/jpeg", width: ref.Width, height: ref.Height, placeholder: true}
						defer delete(s.blobs, ref.SHA256)
					}
				}
			}
		}
		if b == nil {
			problems = append(problems, obx.ImportRowError{File: m.File, Severity: obx.SeverityError, ObxError: obx.ErrBlobMissing, Detail: "this image has not been uploaded (sha256 " + m.SHA256 + ")"})
			continue
		}
		img := canonicalcsv.Image{Name: path.Base(m.File), SHA256: b.sha, Bytes: int64(len(b.data)), ContentType: b.contentType, Width: b.width, Height: b.height, Renditions: m.Renditions}
		if img.Width == 0 {
			img.Width, img.Height = m.Width, m.Height
		}
		missing := false
		if r := m.Renditions; r != nil {
			for _, ref := range []*obx.BlobRef{r.Web, r.Square, r.Thumb} {
				if ref != nil && s.blobs[ref.SHA256] == nil {
					problems = append(problems, obx.ImportRowError{File: m.File, Severity: obx.SeverityError, ObxError: obx.ErrBlobMissing, Detail: "a rendition of this image has not been uploaded (sha256 " + ref.SHA256 + ")"})
					missing = true
				}
			}
		}
		if !missing {
			planImages = append(planImages, img)
		}
	}
	plan := canonicalcsv.BuildPlan(pf, af, planImages, catalogView{s})
	problems = append(problems, plan.Problems...)
	run.Counts.Rows, run.Counts.Files = plan.Rows, len(images)+len(in.manifest)

	// Mirror: decide what would be unpublished before writing anything.
	var toUnpublish []*record
	if in.options.Mode == obx.ImportModeMirror {
		inFile := map[string]bool{}
		for _, p := range plan.Products {
			if p.Input != nil {
				inFile[p.ExternalID] = true
			}
		}
		total := 0
		for _, p := range sortedByID(s.products) {
			if !obx.Contains(plan.MirrorBrands, p.parentExt) || p.status != obx.StatusPublished {
				continue
			}
			total++
			if !inFile[p.extID] {
				toUnpublish = append(toUnpublish, p)
			}
		}
		if len(toUnpublish)*2 > total && !in.options.AllowMassUnpublish {
			return fail(obx.ErrMassUnpublishRefused, fmt.Sprintf("this mirror import would take %d of %d products offline; check the file, or allow it explicitly", len(toUnpublish), total))
		}
	}

	var snap *catalogSnapshot
	if in.options.DryRun {
		snap = s.snapshotCatalog()
	}
	count := func(res string) {
		switch res {
		case obx.ResultCreated:
			run.Counts.Created++
		case obx.ResultUpdated:
			run.Counts.Updated++
		case obx.ResultUnchanged:
			run.Counts.Unchanged++
		}
	}
	csvFile := "products.csv"
	if pf != nil {
		csvFile = pf.File
	}
	rowErr := func(file string, row int, p *obx.Problem) {
		e := obx.ImportRowError{File: file, Severity: obx.SeverityError, ObxError: p.ObxError, Detail: p.DetailString()}
		if row > 0 {
			e.Row = ptr(row)
		}
		if len(p.Errors) > 0 && p.Errors[0].Pointer != nil {
			e.Column = ptr(strings.TrimPrefix(*p.Errors[0].Pointer, "/"))
		}
		problems = append(problems, e)
	}
	for _, b := range plan.Brands {
		res, p := s.apply(wr, obx.IngestOpUpsert, obx.KindBrand, b.ExternalID, canonicalJSON(obx.BrandInput{Name: b.Name}), "")
		if p != nil {
			rowErr(csvFile, 0, p)
			continue
		}
		count(res.res.Result)
	}
	for _, pp := range plan.Products {
		if pp.Input != nil {
			data := canonicalJSON(pp.Input)
			if old := s.products[pp.ExternalID]; old != nil && old.product != nil && pf != nil {
				data = keepAbsentColumns(data, canonicalJSON(old.product), productColumnFields, pf.Present)
			}
			res, p := s.apply(wr, obx.IngestOpUpsert, obx.KindProduct, pp.ExternalID, data, "")
			if p != nil {
				rowErr(csvFile, pp.Row, p)
				continue
			}
			count(res.res.Result)
			// import-format.md rule 10: the product as stored (the file's
			// values plus what rule 9 kept), at the product's first row.
			if stored := s.products[pp.ExternalID]; stored != nil && stored.product != nil {
				for _, lw := range stored.product.LabelWarnings() {
					problems = append(problems, obx.ImportRowError{File: csvFile, Row: ptr(pp.Row), Column: ptr(lw.Column),
						Severity: obx.SeverityWarning, ObxError: lw.Code, Detail: lw.Detail})
				}
			}
		}
		for _, v := range pp.Variants {
			data := canonicalJSON(v.Input)
			if old := s.variants[v.ExternalID]; old != nil && old.variant != nil && pf != nil {
				data = keepAbsentColumns(data, canonicalJSON(old.variant), variantColumnFields, pf.Present)
			}
			res, p := s.apply(wr, obx.IngestOpUpsert, obx.KindVariant, v.ExternalID, data, "")
			if p != nil {
				rowErr(csvFile, v.Row, p)
				continue
			}
			count(res.res.Result)
		}
		if s.products[pp.ExternalID] == nil {
			for _, a := range pp.Assets {
				rowErr(a.Image.Name, 0, problemf(obx.ErrUnknownParent, "product %s was not created", pp.ExternalID))
			}
			continue
		}
		s.holdCommits++
		restore := n.freePositions(pp)
		for _, a := range pp.Assets {
			data := canonicalJSON(a.Input)
			if old := s.assets[a.ExternalID]; old != nil && old.asset != nil {
				data = keepImageFacts(data, old.asset)
			}
			res, p := s.apply(wr, obx.IngestOpUpsert, obx.KindAsset, a.ExternalID, data, "")
			if p != nil {
				rowErr(a.Image.Name, 0, p)
				continue
			}
			count(res.res.Result)
		}
		restore()
		s.holdCommits--
		s.commit()
	}
	for _, p := range toUnpublish {
		res, perr := s.apply(wr, obx.IngestOpUnpublish, obx.KindProduct, p.extID, nil, "")
		if perr != nil {
			rowErr(csvFile, 0, perr)
			continue
		}
		if res.res.Result == obx.ResultUpdated {
			run.Counts.Unpublished++
		}
	}
	if snap != nil {
		s.restoreCatalog(snap)
	}
	return n.finishRun(run, problems, obx.JobSucceeded)
}

// freePositions moves the existing assets of a product that the import
// gives a different position out of the way, so positions can be swapped
// within one import. The returned function puts back any asset whose write
// failed.
func (n *Node) freePositions(pp *canonicalcsv.PlannedProduct) func() {
	s := n.st
	type moved struct {
		ext string
		in  *record
	}
	var list []moved
	for i, a := range pp.Assets {
		cur := s.assets[a.ExternalID]
		if cur == nil || cur.parentExt != pp.ExternalID || cur.asset.Position == a.Input.Position {
			continue
		}
		cp := *cur
		in := *cur.asset
		in.Position = 1000 + i
		cp.asset = &in
		cp.input = s.storedInput(&cp)
		s.store(cur, &cp)
		list = append(list, moved{a.ExternalID, cur})
	}
	return func() {
		for _, m := range list {
			if cur := s.assets[m.ext]; cur != nil && cur.asset.Position >= 1000 {
				s.store(cur, m.in)
			}
		}
	}
}

func (n *Node) finishRun(run *obx.ImportRun, problems []obx.ImportRowError, status string) (*obx.ImportRun, []obx.ImportRowError) {
	s := n.st
	sort.SliceStable(problems, func(i, j int) bool {
		return problems[i].Severity == obx.SeverityError && problems[j].Severity != obx.SeverityError
	})
	for _, p := range problems {
		if p.Severity == obx.SeverityError {
			run.Counts.Errors++
		} else {
			run.Counts.Warnings++
		}
	}
	if problems == nil {
		problems = []obx.ImportRowError{}
	}
	now := n.now()
	run.Status, run.FinishedAt = status, &now
	run.ErrorsPreview = problems[:min(20, len(problems))]
	s.importRuns[run.ID] = run
	s.runErrors[run.ID] = problems
	s.runOrder = append(s.runOrder, run.ID)
	n.notifyAdmin(obx.EventImportRunCompleted, map[string]any{"import_run_id": run.ID, "status": run.Status, "counts": run.Counts})
	return run, problems
}

// handleIngestImport serves POST /obx/v1/ingest/imports.
func (n *Node) handleIngestImport(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	t, ok := n.authIngest(w, r, obx.IngestScopeWrite)
	n.mu.Unlock()
	if !ok {
		return
	}
	in, p := n.parseImportForm(r)
	if p != nil {
		writeProblem(w, p)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	key := r.Header.Get(obx.HeaderIdempotencyKey)
	if key != "" {
		if e, ok := s.idempotency["import:"+t.id+":"+key]; ok {
			w.Header().Set("Location", n.baseURL(r)+obx.PathIngest+"/jobs/"+e.ref)
			writeJSON(w, http.StatusAccepted, n.jobView(r, s.jobs[e.ref]))
			return
		}
	}
	if in.options.Mode == obx.ImportModeMirror && !obx.Contains(t.scopes, obx.IngestScopeDelete) {
		writeProblem(w, problem(obx.ErrInsufficientScope, "a mirror import unpublishes products and needs the ingest:delete scope"))
		return
	}
	trigger := "api"
	if strings.Contains(r.Header.Get("User-Agent"), "obx-cli") {
		trigger = "cli"
	}
	j := n.newJob("import", t.id)
	run, problems := n.runImport(in, trigger, t.writer())
	j.ImportRun = run
	j.problems = problems
	j.Summary = &obx.BatchSummary{Created: run.Counts.Created, Updated: run.Counts.Updated, Unchanged: run.Counts.Unchanged, Errors: run.Counts.Errors}
	j.Progress = obx.Progress{Done: run.Counts.Rows + run.Counts.Files, Total: ptr(run.Counts.Rows + run.Counts.Files)}
	n.finishJob(j, run.Status)
	if key != "" {
		s.idempotency["import:"+t.id+":"+key] = idemEntry{ref: j.ID, at: n.now()}
	}
	n.notifyAdmin(obx.EventIngestJobCompleted, map[string]string{"job_id": j.ID, "status": j.Status})
	w.Header().Set("Location", n.baseURL(r)+obx.PathIngest+"/jobs/"+j.ID)
	writeJSON(w, http.StatusAccepted, n.jobView(r, j))
}

// keepImageFacts applies import-format.md rule 8 to an image the node already
// has: alt text (per language), rights and provenance fields that the import
// does not state keep their stored values. The file name and the assets.csv
// row still decide the file, product, colour, position, role and view, and a
// PUT without status keeps the stored status anyway.
func keepImageFacts(data json.RawMessage, old *obx.AssetInput) json.RawMessage {
	var cur, prev map[string]any
	if json.Unmarshal(data, &cur) != nil || json.Unmarshal(canonicalJSON(old), &prev) != nil {
		return data
	}
	for _, k := range []string{"alt_text", "rights", "provenance"} {
		stored, _ := prev[k].(map[string]any)
		if len(stored) == 0 {
			continue
		}
		merged := map[string]any{}
		for f, v := range stored {
			merged[f] = v
		}
		stated, _ := cur[k].(map[string]any)
		for f, v := range stated {
			merged[f] = v
		}
		cur[k] = merged
	}
	return canonicalJSON(cur)
}

// columnField maps a field of the write model to the products.csv column it
// comes from: path is the JSON path in the input ("color.name"), column the
// canonical column name; text fields are kept per language, attributes,
// features and measurements per key (keyed says whether the file has the
// column of a key).
type columnField struct {
	path, column string
	text         bool
	keyed        func(present canonicalcsv.Present, key string) bool
}

var (
	attrColumn        = func(p canonicalcsv.Present, k string) bool { return p.Attrs[k] }
	featureColumn     = func(p canonicalcsv.Present, k string) bool { return p.Features[k] }
	measurementColumn = func(p canonicalcsv.Present, k string) bool { return p.Plain[k] }
)

var productColumnFields = []columnField{
	{path: "name", column: "name", text: true}, {path: "description", column: "description", text: true},
	{path: "item_type", column: "item_type"}, {path: "item_subtype", column: "item_subtype"}, {path: "target_group", column: "target_group"},
	{path: "season", column: "season"}, {path: "collection", column: "collection"},
	{path: "gpc_brick", column: "gpc_brick"}, {path: "materials", column: "materials"},
	{path: "contains_animal_origin", column: "contains_animal_origin"}, {path: "footwear_materials", column: "footwear_materials"},
	{path: "care", column: "care"}, {path: "country_of_origin", column: "country_of_origin"},
	{path: "certifications", column: "certifications"}, {path: "dpp_url", column: "dpp_url"},
	{path: "features", keyed: featureColumn}, {path: "attributes", keyed: attrColumn},
}

var variantColumnFields = []columnField{
	{path: "gtin", column: "gtin"}, {path: "sku", column: "sku"}, {path: "size", column: "size"},
	{path: "size_length", column: "size_length"}, {path: "size_system", column: "size_system"},
	{path: "color.name", column: "color_name", text: true}, {path: "color.family", column: "color_family"},
	{path: "color.supplier_color", column: "supplier_color"}, {path: "measurements", keyed: measurementColumn},
}

// keepAbsentColumns applies import-format.md rule 9 to a product or variant
// the node already has: a field whose column the file does not have keeps
// its stored value (texts per language; attributes, features and
// measurements per key). An empty cell in a column the file has still
// removes the value; when the last feature or measurement is removed the
// object is null.
func keepAbsentColumns(data, stored json.RawMessage, fields []columnField, present canonicalcsv.Present) json.RawMessage {
	var cur, old map[string]any
	if json.Unmarshal(data, &cur) != nil || json.Unmarshal(stored, &old) != nil {
		return data
	}
	for _, f := range fields {
		parent, key := cur, f.path
		oldParent := old
		if i := strings.IndexByte(f.path, '.'); i > 0 {
			p, _ := cur[f.path[:i]].(map[string]any)
			op, _ := old[f.path[:i]].(map[string]any)
			if p == nil || op == nil {
				continue
			}
			parent, oldParent, key = p, op, f.path[i+1:]
		}
		was, ok := oldParent[key]
		if !ok || was == nil {
			continue
		}
		switch {
		case f.keyed != nil:
			oldMap, _ := was.(map[string]any)
			now, _ := parent[key].(map[string]any)
			if now == nil {
				now = map[string]any{}
			}
			for k, v := range oldMap {
				if _, stated := now[k]; !stated && !f.keyed(present, k) {
					now[k] = v
				}
			}
			if len(now) > 0 {
				parent[key] = now
			}
		case f.text:
			oldMap, _ := was.(map[string]any)
			now, _ := parent[key].(map[string]any)
			if now == nil {
				now = map[string]any{}
			}
			for lang, v := range oldMap {
				if _, stated := now[lang]; !stated && !present.Langs[f.column][lang] {
					now[lang] = v
				}
			}
			if len(now) > 0 {
				parent[key] = now
			}
		default:
			if _, stated := parent[key]; !stated && !present.Plain[f.column] {
				parent[key] = was
			}
		}
	}
	return canonicalJSON(cur)
}
