package mock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/renditions"
	"github.com/openbrandexchange/obx-go/specdata"
)

// writer describes who writes: an ingest token (brand restriction) or an
// admin session (everything).
type writer struct {
	brands []string // allowed brand external ids; nil = all
}

func (w writer) allows(brandExt string) bool {
	return w.brands == nil || obx.Contains(w.brands, brandExt)
}

var inputSchema = map[string]string{obx.KindBrand: "BrandInput", obx.KindProduct: "ProductInput", obx.KindVariant: "VariantInput", obx.KindAsset: "AssetInput"}

var (
	knownFieldsOnce sync.Once
	knownFields     map[string]map[string]bool
)

// fieldsOf returns the top-level properties of an input schema.
func fieldsOf(kind string) map[string]bool {
	knownFieldsOnce.Do(func() {
		knownFields = map[string]map[string]bool{}
		for k, name := range inputSchema {
			raw, err := fs.ReadFile(specdata.FS(), "schemas/"+name+".json")
			if err != nil {
				panic(err)
			}
			var s struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			json.Unmarshal(raw, &s)
			m := map[string]bool{}
			for p := range s.Properties {
				m[p] = true
			}
			knownFields[k] = m
		}
	})
	return knownFields[kind]
}

// validateInput strips unknown top-level fields (as warnings) and validates
// the rest against the input schema.
func validateInput(kind string, raw json.RawMessage) (json.RawMessage, []string, *obx.Problem) {
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, nil, problem(obx.ErrValidationFailed, "the body must be a JSON object")
	}
	var warnings []string
	known := fieldsOf(kind)
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !known[k] {
			if !strings.HasPrefix(k, "x-") {
				warnings = append(warnings, fmt.Sprintf("unknown field %q ignored", k))
			}
			delete(doc, k)
		}
	}
	// ingest.md 3.4: an empty measurements or features object is stored as
	// null (the schemas ask for at least one member).
	switch kind {
	case obx.KindProduct:
		emptyToNull(doc, "features")
	case obx.KindVariant:
		emptyToNull(doc, "measurements")
	}
	clean, _ := json.Marshal(doc)
	v, err := specdata.DefaultValidator()
	if err != nil {
		return nil, nil, problem(obx.ErrInternal, err.Error())
	}
	if err := v.Validate(inputSchema[kind], clean); err != nil {
		p := problem(obx.ErrValidationFailed, "the body does not match "+inputSchema[kind])
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			for _, leaf := range leaves(ve) {
				ptr := "/" + strings.Join(leaf.InstanceLocation, "/")
				_ = p.WithField(ptr, obx.ErrValidationFailed, leaf.ErrorKind.LocalizedString(englishPrinter))
			}
		}
		return nil, warnings, p
	}
	return clean, warnings, nil
}

// emptyToNull replaces an empty object member with null.
func emptyToNull(doc map[string]any, key string) {
	if m, ok := doc[key].(map[string]any); ok && len(m) == 0 {
		doc[key] = nil
	}
}

// normalizeProduct applies the 0.2 write rules (ingest.md 3.4) to a valid
// product input: 0.1 fibre keys become the fibre with its flag, and empty
// features or certifications are stored as null.
func normalizeProduct(in *obx.ProductInput) {
	in.Materials = in.Materials.Normalized()
	if in.Features.IsEmpty() {
		in.Features = nil
	}
	if len(in.Certifications) == 0 {
		in.Certifications = nil
	}
	if in.FootwearMaterials.IsEmpty() {
		in.FootwearMaterials = nil
	}
}

// normalizeVariant stores an empty measurements object as null.
func normalizeVariant(in *obx.VariantInput) {
	if in.Measurements.IsEmpty() {
		in.Measurements = nil
	}
}

// labelWarnings are the label completeness warnings of a product write
// (ingest.md 3.4, import-format.md rule 10) as warning texts and details.
func labelWarnings(in *obx.ProductInput) ([]string, []obx.WarningDetail) {
	var texts []string
	var details []obx.WarningDetail
	for _, w := range in.LabelWarnings() {
		texts = append(texts, w.Code+": "+w.Detail)
		details = append(details, w.WarningDetail())
	}
	return texts, details
}

// englishPrinter renders schema errors (LocalizedString panics on a nil printer).
var englishPrinter = message.NewPrinter(language.English)

func leaves(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(ve.Causes) == 0 {
		return []*jsonschema.ValidationError{ve}
	}
	var out []*jsonschema.ValidationError
	for _, c := range ve.Causes {
		out = append(out, leaves(c)...)
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// writeResult is the outcome of one write.
type writeResult struct {
	res    obx.IngestResult
	status int
}

func errResult(op, kind, ext string, p *obx.Problem) (writeResult, *obx.Problem) {
	return writeResult{res: obx.IngestResult{Op: op, Kind: kind, ExternalID: ext, Result: obx.ResultError, Error: p}, status: p.Status}, p
}

// apply runs one ingest operation (upsert/patch/delete/publish/unpublish) and
// commits the change log. It never panics on bad input.
func (s *state) apply(w writer, op, kind, ext string, data json.RawMessage, ifMatch string) (writeResult, *obx.Problem) {
	if inputSchema[kind] == "" {
		return errResult(op, kind, ext, problemf(obx.ErrValidationFailed, "unknown kind %q", kind))
	}
	if !obx.ValidExternalID(ext) {
		return errResult(op, kind, ext, problem(obx.ErrValidationFailed, "external_id must be 1-200 characters without control characters or \"/\""))
	}
	var wr writeResult
	var p *obx.Problem
	switch op {
	case obx.IngestOpUpsert:
		wr, p = s.put(w, kind, ext, data, ifMatch, op)
	case obx.IngestOpPatch:
		wr, p = s.patch(w, kind, ext, data, ifMatch, op)
	case obx.IngestOpPublish, obx.IngestOpUnpublish:
		st := obx.StatusPublished
		if op == obx.IngestOpUnpublish {
			st = obx.StatusUnpublished
		}
		wr, p = s.patch(w, kind, ext, canonicalJSON(map[string]string{"status": st}), ifMatch, op)
	case obx.IngestOpDelete:
		wr, p = s.del(w, kind, ext)
	default:
		return errResult(op, kind, ext, problemf(obx.ErrValidationFailed, "unknown op %q", op))
	}
	if p != nil {
		return errResult(op, kind, ext, p)
	}
	return wr, nil
}

func (s *state) put(w writer, kind, ext string, data json.RawMessage, ifMatch, op string) (writeResult, *obx.Problem) {
	clean, warnings, p := validateInput(kind, data)
	if p != nil {
		return writeResult{}, p
	}
	existing := s.table(kind)[ext]
	if ifMatch != "" && (existing == nil || ifMatch != existing.etag()) {
		return writeResult{}, problem(obx.ErrPreconditionFailed, "If-Match does not match the current version")
	}
	now := s.n.now()
	nr := &record{kind: kind, extID: ext}
	if existing != nil {
		cp := *existing
		nr = &cp
	}
	var statusIn *string
	var details []obx.WarningDetail
	switch kind {
	case obx.KindBrand:
		var in obx.BrandInput
		json.Unmarshal(clean, &in)
		if !w.allows(ext) {
			return writeResult{}, problemf(obx.ErrForbidden, "this key may not write brand %s", ext)
		}
		statusIn, in.Status = in.Status, nil
		nr.brand = &in
	case obx.KindProduct:
		var in obx.ProductInput
		json.Unmarshal(clean, &in)
		normalizeProduct(&in)
		if p := in.Features.Validate(); len(p) > 0 {
			// The schema already checks the registry; this catches what it cannot.
			return writeResult{}, problem(obx.ErrValidationFailed, "features: "+strings.Join(p, "; ")).WithField("/features", obx.ErrValidationFailed, p[0])
		}
		if in.BrandExternalID == nil {
			if len(s.brands) != 1 {
				return writeResult{}, problem(obx.ErrValidationFailed, "brand_external_id is required when the node has more than one brand").WithField("/brand_external_id", obx.ErrValidationFailed, "required")
			}
			for id := range s.brands {
				in.BrandExternalID = ptr(id)
			}
		}
		b := s.brands[*in.BrandExternalID]
		if b == nil {
			return writeResult{}, problemf(obx.ErrUnknownParent, "brand %s does not exist", *in.BrandExternalID)
		}
		if !w.allows(b.extID) || (existing != nil && !w.allows(existing.parentExt)) {
			return writeResult{}, problemf(obx.ErrForbidden, "this key may not write brand %s", b.extID)
		}
		statusIn, in.Status = in.Status, nil
		nr.product, nr.parentExt = &in, b.extID
		texts, d := labelWarnings(&in)
		warnings, details = append(warnings, texts...), d
	case obx.KindVariant:
		var in obx.VariantInput
		json.Unmarshal(clean, &in)
		normalizeVariant(&in)
		if m := in.Measurements; m != nil && m.StrapLengthCM != nil && m.StrapLengthCM.Min > m.StrapLengthCM.Max {
			// The schema cannot compare two numbers; protocol.md §5.9 asks min <= max.
			return writeResult{}, problem(obx.ErrValidationFailed, "measurements: strap_length_cm min is larger than max").WithField("/measurements/strap_length_cm", obx.ErrValidationFailed, "min > max")
		}
		prod := s.products[in.ProductExternalID]
		if prod == nil {
			return writeResult{}, problemf(obx.ErrUnknownParent, "product %s does not exist", in.ProductExternalID)
		}
		if !w.allows(prod.parentExt) || (existing != nil && !w.allows(s.products[existing.parentExt].parentExt)) {
			return writeResult{}, problemf(obx.ErrForbidden, "this key may not write brand %s", prod.parentExt)
		}
		nr.gtin14 = ""
		if in.GTIN != nil {
			g, err := obx.ValidateGTIN(*in.GTIN)
			if err != nil {
				return writeResult{}, problemf(obx.ErrInvalidGTIN, "EAN %s: %v", *in.GTIN, err).WithField("/gtin", obx.ErrInvalidGTIN, err.Error())
			}
			if other, ok := s.gtins[g]; ok && other != ext {
				return writeResult{}, problemf(obx.ErrGTINConflict, "EAN %s already belongs to variant %s", *in.GTIN, other)
			}
			nr.gtin14 = g
		}
		statusIn, in.Status = in.Status, nil
		nr.variant, nr.parentExt = &in, prod.extID
	case obx.KindAsset:
		var in obx.AssetInput
		json.Unmarshal(clean, &in)
		prod := s.products[in.ProductExternalID]
		if prod == nil {
			return writeResult{}, problemf(obx.ErrUnknownParent, "product %s does not exist", in.ProductExternalID)
		}
		if !w.allows(prod.parentExt) || (existing != nil && !w.allows(s.products[existing.parentExt].parentExt)) {
			return writeResult{}, problemf(obx.ErrForbidden, "this key may not write brand %s", prod.parentExt)
		}
		if in.MediaType == "" {
			in.MediaType = "image"
		}
		if other := s.positionTaken(prod.extID, in.ColorCode, in.Position, ext); other != "" {
			return writeResult{}, problemf(obx.ErrPositionConflict, "image %s already has position %d for this colour; reorder with asset-order", other, in.Position)
		}
		if in.Files == nil && existing != nil {
			in.Files = existing.asset.Files
		} else if in.Files != nil {
			if p := s.normalizeFiles(in.Files); p != nil {
				return writeResult{}, p
			}
			if existing != nil && existing.asset.Files != nil && s.n.opts.ClientRenditions {
				old := existing.asset.Files
				if in.Files.Original != nil && old.Original != nil && in.Files.Original.SHA256 == old.Original.SHA256 {
					for _, name := range obx.DerivedRenditionNames {
						if in.Files.Get(name) == nil && old.Get(name) != nil {
							in.Files.Set(name, old.Get(name))
						}
					}
				}
			}
		}
		statusIn, in.Status = in.Status, nil
		nr.asset, nr.parentExt = &in, prod.extID
	}
	status := s.settings.defaultIngestStatus
	if existing != nil {
		status = existing.status
	}
	if statusIn != nil {
		status = *statusIn
	}
	nr.status = status
	nr.input = s.storedInput(nr)
	if existing != nil && (bytes.Equal(existing.input, nr.input) || s.sameData(existing, nr)) && existing.status == nr.status {
		return s.result(existing, op, obx.ResultUnchanged, warnings, details), nil
	}
	if kind == obx.KindAsset {
		if p := s.applyAssetFiles(nr, existing); p != nil {
			return writeResult{}, p
		}
	}
	res := obx.ResultUpdated
	if existing == nil {
		nr.id = s.newID(idPrefix[kind])
		nr.createdAt = now
		res = obx.ResultCreated
	}
	nr.updatedAt = now
	s.store(existing, nr)
	s.commit()
	return s.result(nr, op, res, warnings, details), nil
}

// storedInput is the canonical input document (with status) kept for
// IngestRecord.data and unchanged detection.
func (s *state) storedInput(r *record) json.RawMessage {
	st := r.status
	switch r.kind {
	case obx.KindBrand:
		in := *r.brand
		in.Status = &st
		return canonicalJSON(in)
	case obx.KindProduct:
		in := *r.product
		in.Status = &st
		return canonicalJSON(in)
	case obx.KindVariant:
		in := *r.variant
		in.Status = &st
		return canonicalJSON(in)
	default:
		in := *r.asset
		in.Status = &st
		return canonicalJSON(in)
	}
}

func (s *state) store(existing, nr *record) {
	if existing != nil && existing.kind == obx.KindVariant && existing.gtin14 != "" {
		delete(s.gtins, existing.gtin14)
	}
	if nr.kind == obx.KindVariant && nr.gtin14 != "" {
		s.gtins[nr.gtin14] = nr.extID
	}
	if existing != nil {
		s.unindexChild(existing)
	}
	s.table(nr.kind)[nr.extID] = nr
	s.byID[nr.id] = nr
	s.indexChild(nr)
	s.noteChange(nr)
}

// noteChange marks a stored record for the next commit. A brand change can
// change everything below it; a product change the visibility of its
// variants and assets.
func (s *state) noteChange(r *record) {
	switch r.kind {
	case obx.KindBrand:
		s.dirtyAll = true
	case obx.KindProduct:
		for id, c := range s.childIndex()[r.extID] {
			s.markDirty(id, c)
		}
	}
	s.markDirty(r.id, r)
}

func (s *state) result(r *record, op, res string, warnings []string, details []obx.WarningDetail) writeResult {
	status := http.StatusOK
	if res == obx.ResultCreated {
		status = http.StatusCreated
	}
	vis := s.visibility(r, s.n.now())
	return writeResult{status: status, res: obx.IngestResult{Op: op, Kind: r.kind, ExternalID: r.extID, Result: res, ID: ptr(r.id), Visibility: &vis, ETag: ptr(r.etag()), Warnings: warnings, WarningDetails: details}}
}

// positionTaken returns the external id of another asset of the product with
// the same colour and position, or "".
func (s *state) positionTaken(productExt string, color *string, pos int, self string) string {
	for _, a := range s.assets {
		if a.extID == self || a.parentExt != productExt || a.asset.Position != pos {
			continue
		}
		if (a.asset.ColorCode == nil) != (color == nil) {
			continue
		}
		if color != nil && *a.asset.ColorCode != *color {
			continue
		}
		return a.extID
	}
	return ""
}

// normalizeFiles checks referenced blobs and fills in their dimensions.
func (s *state) normalizeFiles(f *obx.AssetFiles) *obx.Problem {
	for _, name := range obx.RenditionNames {
		ref := f.Get(name)
		if ref == nil {
			continue
		}
		b := s.blobs[ref.SHA256]
		if b == nil {
			return problemf(obx.ErrBlobMissing, "blob %s (%s) has not been uploaded", ref.SHA256, name).WithField("/files/"+name+"/sha256", obx.ErrBlobMissing, "unknown sha256")
		}
		w, h := b.width, b.height
		if w == 0 && ref.Width != nil {
			w = *ref.Width
		}
		if h == 0 && ref.Height != nil {
			h = *ref.Height
		}
		nref := &obx.FileRef{SHA256: ref.SHA256}
		if w > 0 && h > 0 {
			nref.Width, nref.Height = &w, &h
		}
		f.Set(name, nref)
	}
	return nil
}

func refOf(f *obx.FileRef) fileRef {
	r := fileRef{SHA256: f.SHA256}
	if f.Width != nil {
		r.Width = *f.Width
	}
	if f.Height != nil {
		r.Height = *f.Height
	}
	return r
}

// applyAssetFiles computes the effective files of an asset after a PUT or
// PATCH (see ingest.md section 4.1).
func (s *state) applyAssetFiles(nr, existing *record) *obx.Problem {
	var old map[string]fileRef
	if existing != nil {
		old = existing.files
	}
	in := nr.asset.Files
	given := map[string]fileRef{}
	if in != nil {
		for _, name := range obx.RenditionNames {
			if ref := in.Get(name); ref != nil {
				given[name] = refOf(ref)
			}
		}
	}
	complete := func(m map[string]fileRef) bool {
		for _, n := range obx.RenditionNames {
			if _, ok := m[n]; !ok {
				return false
			}
		}
		return true
	}
	nr.pending = nil
	switch {
	case len(given) == 0:
		nr.files = old
	case complete(given):
		nr.files = given
	case !s.n.opts.ClientRenditions:
		orig, ok := given[obx.RenditionOriginal]
		if !ok {
			merged := copyFiles(old)
			for k, v := range given {
				merged[k] = v
			}
			nr.files = merged
			break
		}
		if old != nil && old[obx.RenditionOriginal] == orig && complete(old) {
			nr.files = old
			break
		}
		files, p := s.render(orig)
		if p != nil {
			return p
		}
		nr.files = files
	default:
		orig, hasOrig := given[obx.RenditionOriginal]
		if !hasOrig || (old != nil && old[obx.RenditionOriginal] == orig) {
			merged := copyFiles(old)
			for k, v := range given {
				merged[k] = v
			}
			nr.files = merged
		} else if old != nil && complete(old) {
			nr.files = old
			nr.pending = given
		} else {
			nr.files = given
		}
	}
	return nil
}

func copyFiles(m map[string]fileRef) map[string]fileRef {
	out := map[string]fileRef{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

var (
	renderCacheMu sync.Mutex
	renderCache   = map[string]*renditions.Set{}
)

// render produces web/square/thumb for an original blob (server rendering).
func (s *state) render(orig fileRef) (map[string]fileRef, *obx.Problem) {
	b := s.blobs[orig.SHA256]
	if b == nil {
		return nil, problemf(obx.ErrBlobMissing, "blob %s has not been uploaded", orig.SHA256)
	}
	if b.placeholder {
		return map[string]fileRef{obx.RenditionOriginal: {SHA256: b.sha, Width: b.width, Height: b.height}}, nil
	}
	renderCacheMu.Lock()
	set := renderCache[orig.SHA256]
	renderCacheMu.Unlock()
	if set == nil {
		var err error
		set, err = renditions.Generate(b.data)
		if err != nil {
			return nil, problemf(obx.ErrValidationFailed, "the image cannot be rendered: %v", err)
		}
		renderCacheMu.Lock()
		renderCache[orig.SHA256] = set
		renderCacheMu.Unlock()
	}
	files := map[string]fileRef{obx.RenditionOriginal: {SHA256: b.sha, Width: b.width, Height: b.height}}
	for _, f := range set.All() {
		s.putBlob(f.Data, f.ContentType, f.Width, f.Height)
		files[f.Name] = fileRef{SHA256: f.SHA256, Width: f.Width, Height: f.Height}
	}
	return files, nil
}

// putBlob stores bytes (idempotent) and returns the blob.
func (s *state) putBlob(data []byte, contentType string, w, h int) (*blob, bool) {
	sha := hashBytes(data)
	if b := s.blobs[sha]; b != nil {
		return b, false
	}
	b := &blob{sha: sha, data: data, contentType: contentType, width: w, height: h, createdAt: s.n.now()}
	s.blobs[sha] = b
	return b, true
}

func (s *state) patch(w writer, kind, ext string, data json.RawMessage, ifMatch, op string) (writeResult, *obx.Problem) {
	existing := s.table(kind)[ext]
	if existing == nil {
		return writeResult{}, problemf(obx.ErrNotFound, "%s %s does not exist", kind, ext)
	}
	var patch map[string]any
	if err := json.Unmarshal(data, &patch); err != nil || patch == nil {
		return writeResult{}, problem(obx.ErrValidationFailed, "a merge patch must be a JSON object")
	}
	var cur map[string]any
	json.Unmarshal(existing.input, &cur)
	merged := mergePatch(cur, patch)
	raw := canonicalJSON(merged)
	return s.put(w, kind, ext, raw, ifMatch, op)
}

// mergePatch applies RFC 7396.
func mergePatch(target any, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	out := map[string]any{}
	for k, v := range tm {
		out[k] = v
	}
	for k, v := range pm {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = mergePatch(out[k], v)
	}
	return out
}

func (s *state) del(w writer, kind, ext string) (writeResult, *obx.Problem) {
	existing := s.table(kind)[ext]
	if existing == nil {
		return writeResult{status: http.StatusOK, res: obx.IngestResult{Op: obx.IngestOpDelete, Kind: kind, ExternalID: ext, Result: obx.ResultUnchanged}}, nil
	}
	b := s.brandOf(existing)
	if b != nil && !w.allows(b.extID) {
		return writeResult{}, problemf(obx.ErrForbidden, "this key may not write brand %s", b.extID)
	}
	switch kind {
	case obx.KindBrand:
		for _, p := range s.products {
			if p.parentExt == ext {
				return writeResult{}, problemf(obx.ErrConflict, "brand %s still has products; delete or move them first", ext)
			}
		}
	case obx.KindProduct:
		for _, c := range s.childrenOf(ext, obx.KindVariant) {
			s.remove(c)
		}
		for _, c := range s.childrenOf(ext, obx.KindAsset) {
			s.remove(c)
		}
	}
	s.remove(existing)
	s.commit()
	return writeResult{status: http.StatusOK, res: obx.IngestResult{Op: obx.IngestOpDelete, Kind: kind, ExternalID: ext, Result: obx.ResultDeleted, ID: ptr(existing.id)}}, nil
}

func (s *state) remove(r *record) {
	delete(s.table(r.kind), r.extID)
	delete(s.byID, r.id)
	if r.kind == obx.KindVariant && r.gtin14 != "" {
		delete(s.gtins, r.gtin14)
	}
	s.unindexChild(r)
	if r.kind == obx.KindBrand {
		s.dirtyAll = true
	}
	if r.kind == obx.KindProduct {
		for id, c := range s.childIndex()[r.extID] {
			s.markDirty(id, c)
		}
	}
	s.markDirty(r.id, nil)
}

// assetOrder assigns positions 1..n atomically.
func (s *state) assetOrder(w writer, productExt string, order obx.AssetOrder) ([]obx.IngestResult, *obx.Problem) {
	prod := s.products[productExt]
	if prod == nil {
		return nil, problemf(obx.ErrNotFound, "product %s does not exist", productExt)
	}
	if !w.allows(prod.parentExt) {
		return nil, problemf(obx.ErrForbidden, "this key may not write brand %s", prod.parentExt)
	}
	var group []*record
	for _, a := range s.childrenOf(productExt, obx.KindAsset) {
		if (a.asset.ColorCode == nil) == (order.ColorCode == nil) && (order.ColorCode == nil || *a.asset.ColorCode == *order.ColorCode) {
			group = append(group, a)
		}
	}
	listed := map[string]bool{}
	for _, id := range order.AssetExternalIDs {
		if listed[id] {
			return nil, problemf(obx.ErrValidationFailed, "%s is listed twice", id)
		}
		listed[id] = true
	}
	if len(group) != len(order.AssetExternalIDs) {
		return nil, problem(obx.ErrValidationFailed, "list every image of this product and colour exactly once")
	}
	for _, a := range group {
		if !listed[a.extID] {
			return nil, problemf(obx.ErrValidationFailed, "image %s is missing from the list", a.extID)
		}
	}
	now := s.n.now()
	var out []obx.IngestResult
	for i, id := range order.AssetExternalIDs {
		a := s.assets[id]
		if a.asset.Position == i+1 {
			out = append(out, s.result(a, obx.IngestOpPatch, obx.ResultUnchanged, nil, nil).res)
			continue
		}
		cp := *a
		in := *a.asset
		in.Position = i + 1
		cp.asset = &in
		cp.input = s.storedInput(&cp)
		cp.updatedAt = now
		s.store(a, &cp)
		out = append(out, s.result(&cp, obx.IngestOpPatch, obx.ResultUpdated, nil, nil).res)
	}
	s.commit()
	return out, nil
}

// putAssetFile stores one file of an asset (method 1 in ingest.md 4).
func (s *state) putAssetFile(w writer, ext, name string, data []byte, contentType, declaredSHA string) (writeResult, *obx.Problem) {
	a := s.assets[ext]
	if a == nil {
		return writeResult{}, problemf(obx.ErrNotFound, "asset %s does not exist; PUT its metadata first", ext)
	}
	if !w.allows(s.products[a.parentExt].parentExt) {
		return writeResult{}, problem(obx.ErrForbidden, "this key may not write this brand")
	}
	b, p := s.acceptBytes(data, declaredSHA, name != obx.RenditionOriginal)
	if p != nil {
		return writeResult{}, p
	}
	ref := fileRef{SHA256: b.sha, Width: b.width, Height: b.height}
	cp := *a
	in := *a.asset
	files := &obx.AssetFiles{}
	if in.Files != nil {
		*files = *in.Files
	}
	cur := a.files[name]
	if cur == ref && (a.pending == nil) {
		return s.result(a, obx.IngestOpUpsert, obx.ResultUnchanged, nil, nil), nil
	}
	fr := &obx.FileRef{SHA256: ref.SHA256, Width: ptr(ref.Width), Height: ptr(ref.Height)}
	if name == obx.RenditionOriginal {
		files = &obx.AssetFiles{Original: fr}
		if s.n.opts.ClientRenditions {
			if s.filesComplete(a) && a.files[obx.RenditionOriginal].SHA256 != ref.SHA256 {
				cp.pending = map[string]fileRef{obx.RenditionOriginal: ref}
				cp.files = a.files
			} else {
				cp.files = map[string]fileRef{obx.RenditionOriginal: ref}
				cp.pending = nil
			}
		} else {
			rendered, p := s.render(ref)
			if p != nil {
				return writeResult{}, p
			}
			cp.files, cp.pending = rendered, nil
		}
	} else {
		files.Set(name, fr)
		if a.pending != nil {
			cp.pending = copyFiles(a.pending)
			cp.pending[name] = ref
			if s.filesComplete(&record{files: cp.pending}) {
				cp.files, cp.pending = cp.pending, nil
			}
		} else {
			cp.files = copyFiles(a.files)
			cp.files[name] = ref
		}
	}
	in.Files = files
	cp.asset = &in
	cp.input = s.storedInput(&cp)
	cp.updatedAt = s.n.now()
	s.store(a, &cp)
	s.commit()
	return s.result(&cp, obx.IngestOpUpsert, obx.ResultUpdated, nil, nil), nil
}

// acceptBytes validates and stores an uploaded file.
func (s *state) acceptBytes(data []byte, declaredSHA string, rendition bool) (*blob, *obx.Problem) {
	if int64(len(data)) > s.n.opts.MaxUploadBytes {
		return nil, problemf(obx.ErrPayloadTooLarge, "files larger than %d bytes go through /uploads", s.n.opts.MaxUploadBytes)
	}
	sha := hashBytes(data)
	if declaredSHA != "" && !strings.EqualFold(declaredSHA, sha) {
		return nil, problemf(obx.ErrChecksumMismatch, "the bytes hash to %s, not %s", sha, declaredSHA)
	}
	return s.storeChecked(data)
}

// storeChecked sniffs the type, reads dimensions and stores the blob.
func (s *state) storeChecked(data []byte) (*blob, *obx.Problem) {
	info, err := renditions.ReadInfo(data)
	if err != nil {
		if info.ContentType == "" {
			return nil, problem(obx.ErrUnsupportedMediaType, "only JPEG, PNG, WebP and TIFF images are accepted")
		}
		return nil, problemf(obx.ErrValidationFailed, "the %s file cannot be read: %v", info.ContentType, err)
	}
	if int64(info.Width)*int64(info.Height) > renditions.MaxPixels {
		return nil, problem(obx.ErrValidationFailed, "images above 100 megapixels are not accepted")
	}
	b, _ := s.putBlob(data, info.ContentType, info.Width, info.Height)
	return b, nil
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// snapshotCatalog captures the catalog for a dry run.
type catalogSnapshot struct {
	brands, products, variants, assets map[string]*record
	gtins                              map[string]string
	log                                []entry
	seq                                int64
	visible                            map[string]visInfo
	lastSeq                            map[string]int64
	counters                           map[string]int
}

func cloneRecords(m map[string]*record) map[string]*record {
	out := make(map[string]*record, len(m))
	for k, v := range m {
		cp := *v
		out[k] = &cp
	}
	return out
}

func (s *state) snapshotCatalog() *catalogSnapshot {
	cs := &catalogSnapshot{brands: cloneRecords(s.brands), products: cloneRecords(s.products), variants: cloneRecords(s.variants), assets: cloneRecords(s.assets),
		gtins: map[string]string{}, log: append([]entry(nil), s.log...), seq: s.seq, visible: map[string]visInfo{}, lastSeq: map[string]int64{}, counters: map[string]int{}}
	for k, v := range s.gtins {
		cs.gtins[k] = v
	}
	for k, v := range s.visible {
		cs.visible[k] = v
	}
	for k, v := range s.lastSeq {
		cs.lastSeq[k] = v
	}
	for k, v := range s.counters {
		cs.counters[k] = v
	}
	return cs
}

func (s *state) restoreCatalog(cs *catalogSnapshot) {
	s.brands, s.products, s.variants, s.assets = cs.brands, cs.products, cs.variants, cs.assets
	s.byID = map[string]*record{}
	for _, t := range []map[string]*record{s.brands, s.products, s.variants, s.assets} {
		for _, r := range t {
			s.byID[r.id] = r
		}
	}
	s.gtins, s.log, s.seq, s.visible, s.lastSeq, s.counters = cs.gtins, cs.log, cs.seq, cs.visible, cs.lastSeq, cs.counters
	// The catalog is back to the snapshot; compare everything once more.
	s.children, s.dirty, s.dirtyAll = nil, nil, true
}
