package mock

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

// ingestToken is an automatic-upload key.
type ingestToken struct {
	id, name, prefix, secretHash string
	scopes                       []string
	brands                       []string // nil = all
	status                       string
	expiresAt                    *time.Time
	createdAt                    time.Time
	lastUsedAt                   *time.Time
}

type idemEntry struct {
	ref    string
	status int
	body   []byte
	at     time.Time
}

type upload struct {
	obx.Upload
	parts map[int][]byte
	owner string
}

// job is an ingest job (batch or import).
type job struct {
	obx.IngestJob
	results  []obx.IngestResult
	problems []obx.ImportRowError
	owner    string
}

// ---- helpers shared by handlers

func readRaw(r *http.Request) (json.RawMessage, *obx.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20+1))
	if err != nil {
		return nil, problem(obx.ErrInvalidRequest, "could not read the body")
	}
	if len(body) > 10<<20 {
		return nil, problem(obx.ErrPayloadTooLarge, "body larger than 10 MiB")
	}
	if !json.Valid(body) {
		return nil, problem(obx.ErrInvalidRequest, "the body is not valid JSON")
	}
	return body, nil
}

func decodeRaw(raw json.RawMessage, v any) *obx.Problem {
	if err := json.Unmarshal(raw, v); err != nil {
		return problemf(obx.ErrValidationFailed, "%v", err)
	}
	return nil
}

// validateSchema validates raw JSON against a spec schema.
func validateSchema(name string, raw json.RawMessage) *obx.Problem {
	v, err := specdata.DefaultValidator()
	if err != nil {
		return problem(obx.ErrInternal, err.Error())
	}
	if err := v.Validate(name, raw); err != nil {
		p := problem(obx.ErrValidationFailed, "the body does not match "+name)
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			for _, leaf := range leaves(ve) {
				p.WithField("/"+strings.Join(leaf.InstanceLocation, "/"), obx.ErrValidationFailed, leaf.ErrorKind.LocalizedString(nil))
			}
		}
		return p
	}
	return nil
}

// authIngest authenticates an ingest call with the given scope ("" = any).
func (n *Node) authIngest(w http.ResponseWriter, r *http.Request, scope string) (*ingestToken, bool) {
	s := n.st
	if !n.capability(obx.CapIngest) {
		writeProblem(w, problem(obx.ErrNotImplemented, "automatic upload is not offered"))
		return nil, false
	}
	if !s.setupDone {
		writeProblem(w, problem(obx.ErrSetupRequired, "this node has not been set up yet").WithStatus(http.StatusServiceUnavailable))
		return nil, false
	}
	tok := bearer(r)
	if tok == "" {
		writeProblem(w, problem(obx.ErrUnauthorized, "send Authorization: Bearer obxi_…"))
		return nil, false
	}
	var t *ingestToken
	h := hashSecret(tok)
	for _, x := range s.ingestTokens {
		if constantEqual(x.secretHash, h) {
			t = x
		}
	}
	if t == nil {
		writeProblem(w, problem(obx.ErrInvalidToken, "unknown key"))
		return nil, false
	}
	if t.status != "active" || (t.expiresAt != nil && n.now().After(*t.expiresAt)) {
		writeProblem(w, problem(obx.ErrAccessRevoked, "this key was revoked or has expired"))
		return nil, false
	}
	if !n.limit(w, "ingest:"+t.id, 6000) {
		return nil, false
	}
	if scope != "" && !obx.Contains(t.scopes, scope) {
		writeProblem(w, problemf(obx.ErrInsufficientScope, "this key lacks the %s scope", scope))
		return nil, false
	}
	now := n.now()
	t.lastUsedAt = &now
	s.tick()
	return t, true
}

func (t *ingestToken) writer() writer { return writer{brands: t.brands} }

func (n *Node) ingestInfo(t *ingestToken) obx.IngestInfo {
	s := n.st
	info := obx.IngestInfo{
		Token: obx.IngestTokenInfo{ID: t.id, Name: t.name, Scopes: t.scopes, BrandExternalIDs: t.brands, ExpiresAt: t.expiresAt},
		Limits: obx.IngestLimits{MaxUploadBytes: n.opts.MaxUploadBytes, MaxObjectBytes: 2 << 30, PartBytes: n.opts.PartBytes,
			MaxBatchOperations: 1000, MaxImportBytes: 2 << 30},
		Renditions:           obx.IngestRenditions{ServerGenerated: !n.opts.ClientRenditions, Required: []string{obx.RenditionOriginal}},
		AcceptedContentTypes: []string{"image/jpeg", "image/png", "image/webp", "image/tiff"},
		DefaultStatus:        s.settings.defaultIngestStatus,
		Brands:               []obx.IngestBrand{},
		Languages:            s.settings.languages,
	}
	if n.opts.ClientRenditions {
		info.Renditions.Required = obx.RenditionNames
	}
	for _, b := range sortedByID(s.brands) {
		if t.brands == nil || obx.Contains(t.brands, b.extID) {
			info.Brands = append(info.Brands, obx.IngestBrand{ExternalID: b.extID, Name: b.brand.Name})
		}
	}
	return info
}

func (n *Node) handleIngestInfo(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.authIngest(w, r, obx.IngestScopeRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, n.ingestInfo(t))
}

func kindFromPath(r *http.Request) string {
	seg := strings.TrimPrefix(r.URL.Path, obx.PathIngest+"/")
	if i := strings.Index(seg, "/"); i >= 0 {
		seg = seg[:i]
	}
	return strings.TrimSuffix(seg, "s")
}

func (s *state) ingestRecord(rec *record) obx.IngestRecord {
	return obx.IngestRecord{Kind: rec.kind, ID: rec.id, ExternalID: rec.extID, Status: rec.status, Visibility: s.visibility(rec, s.n.now()),
		ETag: rec.etag(), CreatedAt: rec.createdAt, UpdatedAt: rec.updatedAt, Data: rec.input, MissingFiles: s.missingFiles(rec)}
}

func (n *Node) handleIngestRecord(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	kind := kindFromPath(r)
	ext := r.PathValue("external_id")
	scope := obx.IngestScopeWrite
	switch r.Method {
	case http.MethodGet:
		scope = obx.IngestScopeRead
	case http.MethodDelete:
		scope = obx.IngestScopeDelete
	}
	t, ok := n.authIngest(w, r, scope)
	if !ok {
		return
	}
	n.recordEndpoint(w, r, t.writer(), kind, ext)
}

// recordEndpoint serves GET/PUT/PATCH/DELETE of one record (shared with the
// admin catalog API).
func (n *Node) recordEndpoint(w http.ResponseWriter, r *http.Request, wr writer, kind, ext string) {
	s := n.st
	if r.Method == http.MethodGet {
		rec := s.table(kind)[ext]
		if rec == nil || !wr.allows(s.brandOf(rec).extID) {
			writeProblem(w, problem(obx.ErrNotFound, ""))
			return
		}
		w.Header().Set("ETag", rec.etag())
		writeJSON(w, http.StatusOK, s.ingestRecord(rec))
		return
	}
	var op string
	var data json.RawMessage
	switch r.Method {
	case http.MethodPut:
		op = obx.IngestOpUpsert
	case http.MethodPatch:
		op = obx.IngestOpPatch
	case http.MethodDelete:
		op = obx.IngestOpDelete
	}
	if op != obx.IngestOpDelete {
		raw, p := readRaw(r)
		if p != nil {
			writeProblem(w, p)
			return
		}
		data = raw
	}
	res, p := s.apply(wr, op, kind, ext, data, r.Header.Get("If-Match"))
	if p != nil {
		writeProblem(w, p)
		return
	}
	if res.res.ETag != nil {
		w.Header().Set("ETag", *res.res.ETag)
	}
	if res.status == http.StatusCreated {
		w.Header().Set("Location", n.baseURL(r)+r.URL.Path)
	}
	writeJSON(w, res.status, res.res)
}

func (n *Node) handleIngestAssetOrder(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.authIngest(w, r, obx.IngestScopeWrite)
	if !ok {
		return
	}
	n.assetOrderEndpoint(w, r, t.writer())
}

func (n *Node) assetOrderEndpoint(w http.ResponseWriter, r *http.Request, wr writer) {
	var in obx.AssetOrder
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	if len(in.AssetExternalIDs) == 0 {
		writeProblem(w, problem(obx.ErrValidationFailed, "asset_external_ids must list the images").WithField("/asset_external_ids", obx.ErrValidationFailed, "required"))
		return
	}
	res, p := n.st.assetOrder(wr, r.PathValue("external_id"), in)
	if p != nil {
		writeProblem(w, p)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func readBody(r *http.Request, max int64) ([]byte, *obx.Problem) {
	if r.ContentLength > max {
		return nil, problemf(obx.ErrPayloadTooLarge, "the body is larger than %d bytes", max)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, problem(obx.ErrInvalidRequest, "could not read the body")
	}
	if int64(len(b)) > max {
		return nil, problemf(obx.ErrPayloadTooLarge, "the body is larger than %d bytes", max)
	}
	return b, nil
}

func (n *Node) handleIngestAssetFile(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.authIngest(w, r, obx.IngestScopeWrite)
	if !ok {
		return
	}
	n.assetFileEndpoint(w, r, t.writer())
}

func (n *Node) assetFileEndpoint(w http.ResponseWriter, r *http.Request, wr writer) {
	name := r.PathValue("rendition")
	if !obx.Contains(obx.RenditionNames, name) {
		writeProblem(w, problem(obx.ErrNotFound, "unknown rendition"))
		return
	}
	data, p := readBody(r, n.opts.MaxUploadBytes)
	if p != nil {
		writeProblem(w, p)
		return
	}
	res, p := n.st.putAssetFile(wr, r.PathValue("external_id"), name, data, r.Header.Get("Content-Type"), r.Header.Get(obx.HeaderContentSHA256))
	if p != nil {
		writeProblem(w, p)
		return
	}
	writeJSON(w, res.status, res.res)
}

func (n *Node) blobView(b *blob, created bool) obx.Blob {
	out := obx.Blob{SHA256: b.sha, Bytes: int64(len(b.data)), ContentType: b.contentType, Created: created}
	if b.width > 0 {
		out.Width, out.Height = ptr(b.width), ptr(b.height)
	}
	return out
}

func (n *Node) handleIngestBlob(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	scope := obx.IngestScopeWrite
	if r.Method == http.MethodHead {
		scope = obx.IngestScopeRead
	}
	if _, ok := n.authIngest(w, r, scope); !ok {
		return
	}
	n.blobEndpoint(w, r)
}

func (n *Node) blobEndpoint(w http.ResponseWriter, r *http.Request) {
	sha := strings.ToLower(r.PathValue("sha256"))
	if len(sha) != 64 {
		writeProblem(w, problem(obx.ErrInvalidRequest, "sha256 must be 64 hex characters"))
		return
	}
	s := n.st
	if r.Method == http.MethodHead {
		if s.blobs[sha] == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(s.blobs[sha].data)))
		w.WriteHeader(http.StatusOK)
		return
	}
	data, p := readBody(r, n.opts.MaxUploadBytes)
	if p != nil {
		writeProblem(w, p)
		return
	}
	if got := hashBytes(data); got != sha {
		writeProblem(w, problemf(obx.ErrChecksumMismatch, "the bytes hash to %s, not %s", got, sha))
		return
	}
	existed := s.blobs[sha] != nil
	b, p := s.storeChecked(data)
	if p != nil {
		writeProblem(w, p)
		return
	}
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeJSON(w, status, n.blobView(b, !existed))
}

// ---- resumable uploads

func (n *Node) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.authIngest(w, r, obx.IngestScopeWrite)
	if !ok {
		return
	}
	var in obx.UploadInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	in.SHA256 = strings.ToLower(in.SHA256)
	if len(in.SHA256) != 64 || in.Bytes < 1 {
		writeProblem(w, problem(obx.ErrValidationFailed, "sha256 (64 hex) and bytes (> 0) are required"))
		return
	}
	if in.Bytes > 2<<30 {
		writeProblem(w, problem(obx.ErrPayloadTooLarge, "files up to 2 GiB are accepted"))
		return
	}
	if !obx.Contains([]string{"image/jpeg", "image/png", "image/webp", "image/tiff"}, in.ContentType) {
		writeProblem(w, problem(obx.ErrUnsupportedMediaType, "only JPEG, PNG, WebP and TIFF images are accepted"))
		return
	}
	if b := n.st.blobs[in.SHA256]; b != nil {
		writeJSON(w, http.StatusOK, n.blobView(b, false))
		return
	}
	pb := n.opts.PartBytes
	total := int((in.Bytes + pb - 1) / pb)
	u := &upload{owner: t.id, parts: map[int][]byte{}}
	u.Upload = obx.Upload{ID: n.st.newID("upl"), SHA256: in.SHA256, Bytes: in.Bytes, ContentType: in.ContentType, PartBytes: pb, PartsTotal: total,
		ReceivedParts: []int{}, Status: "open", ExpiresAt: n.now().Add(24 * time.Hour)}
	n.st.uploads[u.ID] = u
	writeJSON(w, http.StatusCreated, u.Upload)
}

func (n *Node) ownedUpload(w http.ResponseWriter, r *http.Request) (*upload, bool) {
	t, ok := n.authIngest(w, r, obx.IngestScopeWrite)
	if !ok {
		return nil, false
	}
	u := n.st.uploads[r.PathValue("id")]
	if u == nil || u.owner != t.id {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return nil, false
	}
	if u.Status == "open" && n.now().After(u.ExpiresAt) {
		u.Status = "expired"
		u.parts = nil
	}
	return u, true
}

func (u *upload) refresh() {
	u.ReceivedParts = []int{}
	for i := 1; i <= u.PartsTotal; i++ {
		if _, ok := u.parts[i]; ok {
			u.ReceivedParts = append(u.ReceivedParts, i)
		}
	}
}

func (n *Node) handleUpload(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	u, ok := n.ownedUpload(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodDelete {
		u.Status, u.parts = "aborted", nil
		w.WriteHeader(http.StatusNoContent)
		return
	}
	u.refresh()
	writeJSON(w, http.StatusOK, u.Upload)
}

func (n *Node) handleUploadPart(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	u, ok := n.ownedUpload(w, r)
	if !ok {
		return
	}
	if u.Status != "open" {
		writeProblem(w, problemf(obx.ErrConflict, "this upload is %s", u.Status))
		return
	}
	num, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || num < 1 || num > u.PartsTotal {
		writeProblem(w, problemf(obx.ErrValidationFailed, "part must be 1-%d", u.PartsTotal))
		return
	}
	data, p := readBody(r, u.PartBytes)
	if p != nil {
		writeProblem(w, p)
		return
	}
	want := u.PartBytes
	if num == u.PartsTotal {
		want = u.Bytes - int64(u.PartsTotal-1)*u.PartBytes
	}
	if int64(len(data)) != want {
		writeProblem(w, problemf(obx.ErrValidationFailed, "part %d must be exactly %d bytes, got %d", num, want, len(data)))
		return
	}
	u.parts[num] = data
	u.refresh()
	writeJSON(w, http.StatusOK, u.Upload)
}

func (n *Node) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	u, ok := n.ownedUpload(w, r)
	if !ok {
		return
	}
	if u.Status != "open" {
		writeProblem(w, problemf(obx.ErrConflict, "this upload is %s", u.Status))
		return
	}
	u.refresh()
	if len(u.ReceivedParts) != u.PartsTotal {
		var missing []string
		for i := 1; i <= u.PartsTotal; i++ {
			if _, ok := u.parts[i]; !ok {
				missing = append(missing, strconv.Itoa(i))
			}
		}
		writeProblem(w, problemf(obx.ErrUploadIncomplete, "parts missing: %s", strings.Join(missing, ", ")))
		return
	}
	var buf bytes.Buffer
	for i := 1; i <= u.PartsTotal; i++ {
		buf.Write(u.parts[i])
	}
	data := buf.Bytes()
	if hashBytes(data) != u.SHA256 {
		u.Status, u.parts = "aborted", nil
		writeProblem(w, problem(obx.ErrChecksumMismatch, "the assembled bytes do not match the declared sha256; the upload was aborted"))
		return
	}
	b, p := n.st.storeChecked(data)
	if p != nil {
		u.Status, u.parts = "aborted", nil
		writeProblem(w, p)
		return
	}
	u.Status, u.parts = "completed", map[int][]byte{}
	writeJSON(w, http.StatusOK, n.blobView(b, true))
}

// ---- batch

func parseOperations(r *http.Request) ([]obx.IngestOperation, *obx.Problem) {
	body, p := readBody(r, 50<<20)
	if p != nil {
		return nil, p
	}
	ct := r.Header.Get("Content-Type")
	var ops []obx.IngestOperation
	if strings.HasPrefix(ct, obx.MediaTypeNDJSON) || strings.HasPrefix(ct, "application/ndjson") {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		line := 0
		for sc.Scan() {
			line++
			t := strings.TrimSpace(sc.Text())
			if t == "" {
				continue
			}
			var op obx.IngestOperation
			if err := json.Unmarshal([]byte(t), &op); err != nil {
				return nil, problemf(obx.ErrInvalidRequest, "line %d is not valid JSON", line)
			}
			ops = append(ops, op)
		}
	} else if err := json.Unmarshal(body, &ops); err != nil {
		return nil, problem(obx.ErrInvalidRequest, "the body must be a JSON array of operations or NDJSON")
	}
	if len(ops) > 1000 {
		return nil, problem(obx.ErrLimitExceeded, "at most 1000 operations per batch")
	}
	return ops, nil
}

func (n *Node) handleBatch(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.authIngest(w, r, obx.IngestScopeWrite)
	if !ok {
		return
	}
	s := n.st
	key := r.Header.Get(obx.HeaderIdempotencyKey)
	if key != "" {
		if e, ok := s.idempotency["batch:"+t.id+":"+key]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(e.status)
			w.Write(e.body)
			return
		}
	}
	ops, p := parseOperations(r)
	if p != nil {
		writeProblem(w, p)
		return
	}
	results, summary := n.runOperations(t, ops)
	var status int
	var body []byte
	if strings.Contains(r.Header.Get("Prefer"), "respond-async") {
		j := n.newJob("batch", t.id)
		j.results, j.Summary = results, &summary
		n.finishJob(j, obx.JobSucceeded)
		j.Progress = obx.Progress{Done: len(ops), Total: ptr(len(ops))}
		status, body = http.StatusAccepted, canonicalJSON(n.jobView(r, j))
		w.Header().Set("Location", n.baseURL(r)+obx.PathIngest+"/jobs/"+j.ID)
		n.notifyAdmin(obx.EventIngestJobCompleted, map[string]string{"job_id": j.ID, "status": j.Status})
	} else {
		status, body = http.StatusOK, canonicalJSON(obx.BatchResponse{Results: results, Summary: summary})
	}
	if key != "" {
		s.idempotency["batch:"+t.id+":"+key] = idemEntry{status: status, body: body, at: n.now()}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

func (n *Node) runOperations(t *ingestToken, ops []obx.IngestOperation) ([]obx.IngestResult, obx.BatchSummary) {
	var results []obx.IngestResult
	var summary obx.BatchSummary
	for i, op := range ops {
		var res writeResult
		scope := obx.IngestScopeWrite
		if op.Op == obx.IngestOpDelete {
			scope = obx.IngestScopeDelete
		}
		if !obx.Contains(t.scopes, scope) {
			res, _ = errResult(op.Op, op.Kind, op.ExternalID, problemf(obx.ErrInsufficientScope, "this key lacks the %s scope", scope))
		} else {
			res, _ = n.st.apply(t.writer(), op.Op, op.Kind, op.ExternalID, op.Data, obx.Str(op.IfMatch))
		}
		res.res.Index = ptr(i)
		results = append(results, res.res)
		summary.Add(res.res.Result)
	}
	if results == nil {
		results = []obx.IngestResult{}
	}
	return results, summary
}

// ---- jobs

func (n *Node) newJob(kind, owner string) *job {
	j := &job{owner: owner}
	j.IngestJob = obx.IngestJob{ID: n.st.newID("job"), Kind: kind, Status: obx.JobRunning, CreatedAt: n.now()}
	n.st.jobs[j.ID] = j
	n.st.jobOrder = append(n.st.jobOrder, j.ID)
	return j
}

func (n *Node) finishJob(j *job, status string) {
	now := n.now()
	j.Status, j.FinishedAt = status, &now
}

func (n *Node) jobView(r *http.Request, j *job) obx.IngestJob {
	v := j.IngestJob
	v.ResultsURL = n.baseURL(r) + obx.PathIngest + "/jobs/" + j.ID + "/results"
	if j.Kind == "import" && j.ImportRun != nil {
		run := *n.st.importRuns[j.ImportRun.ID]
		v.ImportRun = &run
	}
	return v
}

func (n *Node) handleJobs(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.authIngest(w, r, obx.IngestScopeRead)
	if !ok {
		return
	}
	s := n.st
	q := r.URL.Query()
	limit := 25
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, 100)
	}
	offset := 0
	if c := q.Get("cursor"); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			writeProblem(w, problem(obx.ErrCursorInvalid, "malformed cursor"))
			return
		}
		offset, _ = strconv.Atoi(string(b))
	}
	var list []*job
	for i := len(s.jobOrder) - 1; i >= 0; i-- {
		j := s.jobs[s.jobOrder[i]]
		if j.owner != t.id {
			continue
		}
		if st := q.Get("status"); st != "" && j.Status != st {
			continue
		}
		list = append(list, j)
	}
	page := obx.Page[obx.IngestJob]{Items: []obx.IngestJob{}}
	for i := offset; i < len(list) && len(page.Items) < limit; i++ {
		page.Items = append(page.Items, n.jobView(r, list[i]))
	}
	if offset+limit < len(list) {
		c := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
		page.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, page)
}

func (n *Node) ownedJob(w http.ResponseWriter, r *http.Request) (*job, bool) {
	t, ok := n.authIngest(w, r, obx.IngestScopeRead)
	if !ok {
		return nil, false
	}
	j := n.st.jobs[r.PathValue("id")]
	if j == nil || j.owner != t.id {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return nil, false
	}
	return j, true
}

func (n *Node) handleJob(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if j, ok := n.ownedJob(w, r); ok {
		writeJSON(w, http.StatusOK, n.jobView(r, j))
	}
}

func (n *Node) handleJobResults(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	j, ok := n.ownedJob(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	errorsOnly := q.Get("errors_only") == "true"
	var items []any
	if j.Kind == "batch" {
		for _, res := range j.results {
			if !errorsOnly || res.Result == obx.ResultError {
				items = append(items, res)
			}
		}
	} else {
		for _, e := range j.problems {
			if !errorsOnly || e.Severity == obx.SeverityError {
				items = append(items, e)
			}
		}
	}
	writePaged(w, r, items, 100, 1000)
}

// writePaged answers {items, next_cursor} with offset cursors.
func writePaged(w http.ResponseWriter, r *http.Request, items []any, def, max int) {
	q := r.URL.Query()
	limit := def
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, max)
	}
	offset := 0
	if c := q.Get("cursor"); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			writeProblem(w, problem(obx.ErrCursorInvalid, "malformed cursor"))
			return
		}
		offset, _ = strconv.Atoi(string(b))
	}
	out := struct {
		Items      []any   `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}{Items: []any{}}
	for i := offset; i < len(items) && len(out.Items) < limit; i++ {
		out.Items = append(out.Items, items[i])
	}
	if offset+limit < len(items) {
		c := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(offset + limit)))
		out.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, out)
}
