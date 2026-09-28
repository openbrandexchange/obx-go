package mock

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// adminWriter may write every brand.
var adminWriter = writer{}

func (n *Node) handleAdminBrands(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	out := obx.Items[obx.IngestRecord]{Items: []obx.IngestRecord{}}
	for _, b := range sortedByID(n.st.brands) {
		out.Items = append(out.Items, n.st.ingestRecord(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (n *Node) adminRecord(kind string) func(http.ResponseWriter, *http.Request, *adminCtx) {
	return func(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
		n.recordEndpoint(w, r, adminWriter, kind, r.PathValue("external_id"))
	}
}

func (n *Node) handleAdminProducts(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	q := r.URL.Query()
	now := n.now()
	needle := strings.ToLower(q.Get("q"))
	var list []nodeadmin.ProductSummary
	for _, p := range sortedByID(s.products) {
		if b := q.Get("brand"); b != "" && p.parentExt != b {
			continue
		}
		if st := q.Get("status"); st != "" && p.status != st {
			continue
		}
		vis := s.visibility(p, now)
		if v := q.Get("visibility"); v != "" && vis != v {
			continue
		}
		if needle != "" && !productMatches(s, p, needle) {
			continue
		}
		sum := nodeadmin.ProductSummary{ID: p.id, ExternalID: p.extID, BrandExternalID: p.parentExt, StyleNumber: p.product.StyleNumber, Name: p.product.Name,
			Status: p.status, Visibility: vis, UpdatedAt: p.updatedAt}
		sum.VariantCount = len(s.childrenOf(p.extID, obx.KindVariant))
		for _, a := range s.childrenOf(p.extID, obx.KindAsset) {
			sum.AssetCount++
			if !s.filesComplete(a) {
				sum.PendingAssetCount++
			} else if sum.ThumbURL == nil {
				u, _ := (&urlSigner{base: n.baseURL(r), expires: now.Add(time.Hour).Truncate(time.Second), n: n}).url(a.id, obx.RenditionThumb)
				sum.ThumbURL = &u
			}
		}
		list = append(list, sum)
	}
	if q.Get("sort") == "updated" {
		for i := 1; i < len(list); i++ {
			for j := i; j > 0 && list[j].UpdatedAt.After(list[j-1].UpdatedAt); j-- {
				list[j], list[j-1] = list[j-1], list[j]
			}
		}
	}
	items := make([]any, len(list))
	for i := range list {
		items[i] = list[i]
	}
	pagedWithTotal(w, r, items, 50, 200)
}

// pagedWithTotal is writePaged plus a total count.
func pagedWithTotal(w http.ResponseWriter, r *http.Request, items []any, def, max int) {
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
		Total      int     `json:"total"`
	}{Items: []any{}, Total: len(items)}
	for i := offset; i < len(items) && len(out.Items) < limit; i++ {
		out.Items = append(out.Items, items[i])
	}
	if offset+limit < len(items) {
		c := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
		out.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, out)
}

func productMatches(s *state, p *record, needle string) bool {
	if strings.Contains(strings.ToLower(p.product.StyleNumber), needle) || strings.Contains(strings.ToLower(p.extID), needle) {
		return true
	}
	for _, v := range p.product.Name {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
		in := v.variant
		if strings.Contains(strings.ToLower(obx.Str(in.GTIN)), needle) || strings.Contains(v.gtin14, needle) || strings.Contains(strings.ToLower(obx.Str(in.SKU)), needle) || strings.EqualFold(in.Color.Code, needle) {
			return true
		}
		for _, cn := range in.Color.Name {
			if strings.Contains(strings.ToLower(cn), needle) {
				return true
			}
		}
	}
	return false
}

func (n *Node) handleAdminProduct(w http.ResponseWriter, r *http.Request, ac *adminCtx) {
	if r.Method != http.MethodGet {
		n.adminRecord(obx.KindProduct)(w, r, ac)
		return
	}
	s := n.st
	p := s.products[r.PathValue("external_id")]
	if p == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	now := n.now()
	out := nodeadmin.ProductDetail{Record: s.ingestRecord(p), Variants: []obx.IngestRecord{}, Assets: []nodeadmin.Asset{}}
	for _, v := range s.childrenOf(p.extID, obx.KindVariant) {
		out.Variants = append(out.Variants, s.ingestRecord(v))
	}
	signer := &urlSigner{base: n.baseURL(r), originalAllowed: true, expires: now.Add(time.Hour).Truncate(time.Second), n: n}
	for _, a := range s.childrenOf(p.extID, obx.KindAsset) {
		aa := nodeadmin.Asset{Record: s.ingestRecord(a), Renditions: map[string]*obx.Rendition{}}
		for name, f := range a.files {
			rd := &obx.Rendition{Name: name, SHA256: f.SHA256, Width: f.Width, Height: f.Height}
			if b := s.blobs[f.SHA256]; b != nil {
				rd.Bytes, rd.ContentType = int64(len(b.data)), b.contentType
			}
			rd.URL, rd.URLExpiresAt = signer.url(a.id, name)
			aa.Renditions[name] = rd
		}
		out.Assets = append(out.Assets, aa)
	}
	writeJSON(w, http.StatusOK, out)
}

func (n *Node) handleAdminAssetOrder(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	n.assetOrderEndpoint(w, r, adminWriter)
}

func (n *Node) handleAdminAssetFile(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	n.assetFileEndpoint(w, r, adminWriter)
}

func (n *Node) handleAdminBlob(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	n.blobEndpoint(w, r)
}

// ---- imports

func (n *Node) runView(run *obx.ImportRun) obx.ImportRun {
	v := *run
	return v
}

func (n *Node) handleAdminImports(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	if r.Method == http.MethodGet {
		var items []any
		for i := len(s.runOrder) - 1; i >= 0; i-- {
			run := s.importRuns[s.runOrder[i]]
			if src := r.URL.Query().Get("source_id"); src != "" && obx.Str(run.SourceID) != src {
				continue
			}
			items = append(items, n.runView(run))
		}
		writePaged(w, r, items, 50, 200)
		return
	}
	n.mu.Unlock()
	in, p := n.parseImportForm(r)
	n.mu.Lock()
	if p != nil {
		writeProblem(w, p)
		return
	}
	run, _ := n.runImport(in, "upload", adminWriter)
	writeJSON(w, http.StatusAccepted, n.runView(run))
}

func (n *Node) handleAdminImport(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	run := n.st.importRuns[r.PathValue("id")]
	if run == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	writeJSON(w, http.StatusOK, n.runView(run))
}

func (n *Node) handleAdminImportErrors(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	run := n.st.importRuns[r.PathValue("id")]
	if run == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	var items []any
	for _, e := range n.st.runErrors[run.ID] {
		items = append(items, e)
	}
	writePaged(w, r, items, 50, 200)
}

func (n *Node) handleImportTemplate(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="products.csv"`)
	cols := []string{"brand", "style_number"}
	for _, l := range n.st.settings.languages {
		cols = append(cols, "name:"+l)
	}
	for _, l := range n.st.settings.languages {
		cols = append(cols, "description:"+l)
	}
	cols = append(cols, "item_type", "target_group", "season", "collection", "materials", "care", "country_of_origin", "gtin", "sku", "color_code")
	for _, l := range n.st.settings.languages {
		cols = append(cols, "color_name:"+l)
	}
	cols = append(cols, "size", "size_system")
	io.WriteString(w, strings.Join(cols, ",")+"\n")
}

// ---- import sources

type importSource struct {
	obx.ImportSource
	credentials map[string]any
}

func (n *Node) handleImportSources(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	if r.Method == http.MethodGet {
		out := obx.Items[obx.ImportSource]{Items: []obx.ImportSource{}}
		for _, id := range sortedKeys(s.importSources) {
			out.Items = append(out.Items, s.importSources[id].ImportSource)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	raw, p := readRaw(r)
	if p != nil {
		writeProblem(w, p)
		return
	}
	now := n.now()
	src := &importSource{ImportSource: obx.ImportSource{ID: s.newID("src"), Enabled: true, Mode: obx.ImportModeUpsert,
		Schedule: obx.ImportSourceSchedule{Frequency: "manual", Timezone: "UTC"}, CreatedAt: now, UpdatedAt: now}}
	if p := n.patchSource(src, raw, true); p != nil {
		writeProblem(w, p)
		return
	}
	s.importSources[src.ID] = src
	writeJSON(w, http.StatusCreated, src.ImportSource)
}

// patchSource applies input fields; credentials are stored, never returned.
func (n *Node) patchSource(src *importSource, raw json.RawMessage, create bool) *obx.Problem {
	var in struct {
		Name               *string                   `json:"name"`
		Kind               *string                   `json:"kind"`
		Enabled            *bool                     `json:"enabled"`
		Schedule           *obx.ImportSourceSchedule `json:"schedule"`
		Mode               *string                   `json:"mode"`
		AllowMassUnpublish *bool                     `json:"allow_mass_unpublish"`
		Config             map[string]any            `json:"config"`
		Credentials        map[string]any            `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return problem(obx.ErrValidationFailed, "the body must be a JSON object")
	}
	if in.Kind != nil {
		if !create && *in.Kind != src.Kind {
			return problem(obx.ErrValidationFailed, "the kind of a scheduled import cannot change")
		}
		if !obx.Contains([]string{"https_url", "sftp", "s3"}, *in.Kind) {
			return problem(obx.ErrValidationFailed, "kind must be https_url, sftp or s3")
		}
		src.Kind = *in.Kind
	}
	if create && src.Kind == "" {
		return problem(obx.ErrValidationFailed, "kind is required")
	}
	if in.Name != nil {
		src.Name = *in.Name
	}
	if src.Name == "" {
		return problem(obx.ErrValidationFailed, "name is required")
	}
	if in.Enabled != nil {
		src.Enabled = *in.Enabled
	}
	if in.Schedule != nil {
		if !obx.Contains([]string{"manual", "hourly", "daily", "weekly"}, in.Schedule.Frequency) {
			return problem(obx.ErrValidationFailed, "schedule.frequency must be manual, hourly, daily or weekly")
		}
		if in.Schedule.Timezone == "" {
			in.Schedule.Timezone = "UTC"
		}
		src.Schedule = *in.Schedule
	}
	if in.Mode != nil {
		src.Mode = *in.Mode
	}
	if in.AllowMassUnpublish != nil {
		src.AllowMassUnpublish = *in.AllowMassUnpublish
	}
	if in.Config != nil {
		in.Config["kind"] = src.Kind
		switch src.Kind {
		case "https_url":
			u, _ := in.Config["url"].(string)
			if p := n.checkOutboundURL(u); p != nil {
				return p
			}
			if iu, ok := in.Config["images_url"].(string); ok && iu != "" {
				if p := n.checkOutboundURL(iu); p != nil {
					return p
				}
			}
		case "sftp":
			host, _ := in.Config["host"].(string)
			if host == "" {
				return problem(obx.ErrValidationFailed, "config.host is required")
			}
			if p := n.checkOutboundURL("sftp://"+host, "sftp"); p != nil {
				return p
			}
		case "s3":
			ep, _ := in.Config["endpoint"].(string)
			if ep != "" {
				if p := n.checkOutboundURL(ep); p != nil {
					return p
				}
			}
		}
		src.Config = canonicalJSON(in.Config)
	}
	if src.Config == nil {
		return problem(obx.ErrValidationFailed, "config is required")
	}
	if in.Credentials != nil {
		src.credentials = in.Credentials
	}
	cs := obx.ImportSourceCredentialsSet{Headers: []string{}}
	for k, v := range src.credentials {
		switch k {
		case "password":
			cs.Password = v != nil
		case "private_key":
			cs.PrivateKey = v != nil
		case "access_key_id":
			cs.AccessKeyID = v != nil
		case "secret_access_key":
			cs.SecretAccessKey = v != nil
		case "headers":
			if h, ok := v.(map[string]any); ok {
				for name := range h {
					cs.Headers = append(cs.Headers, name)
				}
			}
		}
	}
	src.CredentialsSet = cs
	src.UpdatedAt = n.now()
	return nil
}

func (n *Node) handleImportSource(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	s := n.st
	src := s.importSources[r.PathValue("id")]
	if src == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, src.ImportSource)
	case http.MethodPatch:
		raw, p := readRaw(r)
		if p != nil {
			writeProblem(w, p)
			return
		}
		cp := *src
		if p := n.patchSource(&cp, raw, false); p != nil {
			writeProblem(w, p)
			return
		}
		*src = cp
		writeJSON(w, http.StatusOK, src.ImportSource)
	case http.MethodDelete:
		delete(s.importSources, src.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// fetchSource downloads what an https_url source points at. It releases
// n.mu while fetching.
func (n *Node) fetchSource(src *importSource) (*importInput, *obx.Problem) {
	if src.Kind != "https_url" {
		return nil, problem(obx.ErrNotImplemented, "the mock node only runs https_url sources")
	}
	var cfg struct {
		URL       string `json:"url"`
		ImagesURL string `json:"images_url"`
	}
	json.Unmarshal(src.Config, &cfg)
	headers := map[string]string{}
	if h, ok := src.credentials["headers"].(map[string]any); ok {
		for k, v := range h {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}
	}
	get := func(u string) ([]byte, *obx.Problem) {
		if p := n.checkOutboundURL(u); p != nil {
			return nil, p
		}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, problem(obx.ErrUnsafeURL, "invalid URL")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		n.mu.Unlock()
		resp, err := n.webhookHTTP().Do(req)
		var body []byte
		if err == nil {
			body, err = io.ReadAll(io.LimitReader(resp.Body, 10*n.opts.MaxUploadBytes))
			resp.Body.Close()
		}
		n.mu.Lock()
		if err != nil {
			return nil, problemf(obx.ErrServiceUnavailable, "could not fetch %s: %v", u, err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, problemf(obx.ErrServiceUnavailable, "%s answered %s", u, resp.Status)
		}
		return body, nil
	}
	body, p := get(cfg.URL)
	if p != nil {
		return nil, p
	}
	in := &importInput{options: obx.ImportOptions{Mode: src.Mode, AllowMassUnpublish: src.AllowMassUnpublish}}
	if bytes.HasPrefix(body, []byte("PK\x03\x04")) {
		in.pkg = body
	} else {
		in.csv, in.csvName = body, path.Base(cfg.URL)
	}
	in.fileInfos = []obx.ImportRunFile{{Name: path.Base(cfg.URL), Bytes: int64(len(body)), SHA256: ptr(hashBytes(body))}}
	if cfg.ImagesURL != "" {
		imgs, p := get(cfg.ImagesURL)
		if p != nil {
			return nil, p
		}
		in.images = []namedFile{{name: "images.zip", data: imgs}}
	}
	if in.options.Mode == "" {
		in.options.Mode = obx.ImportModeUpsert
	}
	return in, nil
}

func (n *Node) handleRunSource(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	src := n.st.importSources[r.PathValue("id")]
	if src == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	var opts struct {
		DryRun bool `json:"dry_run"`
	}
	readJSON(r, &opts)
	in, p := n.fetchSource(src)
	if p != nil {
		now := n.now()
		run := &obx.ImportRun{ID: n.st.newID("imp"), SourceID: ptr(src.ID), Trigger: "manual_run", Mode: src.Mode, CreatedAt: now, StartedAt: &now, Files: []obx.ImportRunFile{}, Error: p}
		run, _ = n.finishRun(run, []obx.ImportRowError{{File: "source", Severity: obx.SeverityError, ObxError: p.ObxError, Detail: p.DetailString()}}, obx.JobFailed)
		src.LastRun = run
		writeJSON(w, http.StatusAccepted, *run)
		return
	}
	in.options.DryRun = opts.DryRun
	run, _ := n.runImport(in, "manual_run", adminWriter)
	run.SourceID = ptr(src.ID)
	src.LastRun = run
	writeJSON(w, http.StatusAccepted, *run)
}

func (n *Node) handleTestSource(w http.ResponseWriter, r *http.Request, _ *adminCtx) {
	src := n.st.importSources[r.PathValue("id")]
	if src == nil {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	type file struct {
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
	}
	out := struct {
		OK                 bool    `json:"ok"`
		Message            *string `json:"message"`
		HostKeyFingerprint *string `json:"host_key_fingerprint"`
		Files              []file  `json:"files"`
	}{Files: []file{}}
	in, p := n.fetchSource(src)
	if p != nil {
		out.Message = ptr(p.DetailString())
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.OK = true
	for _, f := range in.fileInfos {
		out.Files = append(out.Files, file{Name: f.Name, Bytes: f.Bytes})
	}
	if in.pkg != nil {
		if entries, err := unzip(in.pkg, 10*n.opts.MaxUploadBytes); err == nil {
			out.Files = out.Files[:0]
			for _, e := range entries {
				if len(out.Files) < 100 && (canonicalcsv.IsImageFile(e.name) || strings.HasSuffix(strings.ToLower(e.name), ".csv")) {
					out.Files = append(out.Files, file{Name: e.name, Bytes: int64(len(e.data))})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
