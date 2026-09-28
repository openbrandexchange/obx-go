package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/openbrandexchange/obx-go/internal/httpx"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/renditions"
)

// HashBytes returns the lowercase hex sha256 of b.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// HashFile returns the sha256 and size of a file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// HasBlob asks the node whether it already has these bytes.
func (c *Client) HasBlob(ctx context.Context, sha string) (bool, error) {
	resp, err := c.Do(ctx, http.MethodHead, "/blobs/"+url.PathEscape(sha), nil, "", nil)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, obx.NewProblem(obx.ErrInternal, "HEAD blob: "+resp.Status).WithStatus(resp.StatusCode)
}

// PutBlob uploads bytes in one request (at most limits.max_upload_bytes).
func (c *Client) PutBlob(ctx context.Context, sha, contentType string, data []byte) (*obx.Blob, error) {
	if sha == "" {
		sha = HashBytes(data)
	}
	resp, err := c.Do(ctx, http.MethodPut, "/blobs/"+url.PathEscape(sha), data, contentType, http.Header{obx.HeaderContentSHA256: {sha}})
	if err != nil {
		return nil, err
	}
	var b obx.Blob
	return &b, httpx.Decode(resp, &b)
}

// UploadState remembers open multipart uploads per sha256 so an interrupted
// upload can be resumed by a later run.
type UploadState interface {
	Get(sha string) string
	Set(sha, uploadID string)
	Delete(sha string)
}

// MemoryUploadState is an in-memory UploadState.
type MemoryUploadState struct {
	mu sync.Mutex
	m  map[string]string
}

// Get returns the upload id for sha.
func (s *MemoryUploadState) Get(sha string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[sha]
}

// Set stores an upload id.
func (s *MemoryUploadState) Set(sha, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[sha] = id
}

// Delete forgets an upload.
func (s *MemoryUploadState) Delete(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, sha)
}

// CreateUpload starts a resumable upload. When the node already has the
// bytes it returns the Blob and a nil Upload.
func (c *Client) CreateUpload(ctx context.Context, sha string, size int64, contentType string) (*obx.Upload, *obx.Blob, error) {
	resp, err := c.Do(ctx, http.MethodPost, "/uploads", mustJSON(obx.UploadInput{SHA256: sha, Bytes: size, ContentType: contentType}), "application/json", nil)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusOK {
		var b obx.Blob
		return nil, &b, httpx.Decode(resp, &b)
	}
	var u obx.Upload
	return &u, nil, httpx.Decode(resp, &u)
}

// Upload reads an upload session.
func (c *Client) Upload(ctx context.Context, id string) (*obx.Upload, error) {
	var u obx.Upload
	_, err := c.doJSON(ctx, http.MethodGet, "/uploads/"+url.PathEscape(id), nil, "", &u, nil)
	return &u, err
}

// PutUploadPart sends part number n (1-based).
func (c *Client) PutUploadPart(ctx context.Context, id string, n int, data []byte) (*obx.Upload, error) {
	resp, err := c.Do(ctx, http.MethodPut, fmt.Sprintf("/uploads/%s/parts/%d", url.PathEscape(id), n), data, "application/octet-stream", nil)
	if err != nil {
		return nil, err
	}
	var u obx.Upload
	return &u, httpx.Decode(resp, &u)
}

// CompleteUpload assembles the parts into a blob.
func (c *Client) CompleteUpload(ctx context.Context, id string) (*obx.Blob, error) {
	var b obx.Blob
	_, err := c.doJSON(ctx, http.MethodPost, "/uploads/"+url.PathEscape(id)+"/complete", nil, "", &b, nil)
	return &b, err
}

// AbortUpload cancels an upload.
func (c *Client) AbortUpload(ctx context.Context, id string) error {
	resp, err := c.Do(ctx, http.MethodDelete, "/uploads/"+url.PathEscape(id), nil, "", nil)
	if err != nil {
		return err
	}
	return httpx.Decode(resp, nil)
}

// UploadResumable uploads r (size bytes, known sha256) in parts, resuming a
// session remembered in state (may be nil). Parts the node already has are
// skipped.
func (c *Client) UploadResumable(ctx context.Context, r io.ReaderAt, size int64, sha, contentType string, state UploadState, progress func(done, total int64)) (*obx.Blob, error) {
	var up *obx.Upload
	if state != nil {
		if id := state.Get(sha); id != "" {
			if u, err := c.Upload(ctx, id); err == nil && u.Status == "open" && u.SHA256 == sha {
				up = u
			}
		}
	}
	if up == nil {
		u, blob, err := c.CreateUpload(ctx, sha, size, contentType)
		if err != nil {
			return nil, err
		}
		if blob != nil {
			return blob, nil
		}
		up = u
		if state != nil {
			state.Set(sha, up.ID)
		}
	}
	have := map[int]bool{}
	for _, n := range up.ReceivedParts {
		have[n] = true
	}
	var done int64
	for n := 1; n <= up.PartsTotal; n++ {
		start := int64(n-1) * up.PartBytes
		end := min(start+up.PartBytes, size)
		if have[n] {
			done += end - start
			continue
		}
		buf := make([]byte, end-start)
		if _, err := r.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if _, err := c.PutUploadPart(ctx, up.ID, n, buf); err != nil {
			return nil, err
		}
		done += end - start
		if progress != nil {
			progress(done, size)
		}
	}
	blob, err := c.CompleteUpload(ctx, up.ID)
	if err != nil {
		if obx.IsCode(err, obx.ErrChecksumMismatch) && state != nil {
			state.Delete(sha)
		}
		return nil, err
	}
	if state != nil {
		state.Delete(sha)
	}
	return blob, nil
}

// EnsureBlob makes sure the node has the bytes of r: it asks first (HEAD) and
// uploads only when missing, in one request or resumably for large files.
// uploaded reports whether bytes were sent.
func (c *Client) EnsureBlob(ctx context.Context, r io.ReaderAt, size int64, sha, contentType string, state UploadState) (uploaded bool, err error) {
	return c.EnsureBlobProgress(ctx, r, size, sha, contentType, state, nil)
}

// EnsureBlobProgress is EnsureBlob that reports every chunk of bytes sent
// (the whole file, or each part of a resumable upload) to progress.
func (c *Client) EnsureBlobProgress(ctx context.Context, r io.ReaderAt, size int64, sha, contentType string, state UploadState, progress func(sent int64)) (uploaded bool, err error) {
	ok, err := c.HasBlob(ctx, sha)
	if err != nil {
		return false, err
	}
	if ok {
		return false, nil
	}
	info, err := c.Info(ctx)
	if err != nil {
		return false, err
	}
	max := info.Limits.MaxUploadBytes
	if max <= 0 {
		max = 25 << 20
	}
	if size <= max {
		buf := make([]byte, size)
		if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if _, err := c.PutBlob(ctx, sha, contentType, buf); err != nil {
			return false, err
		}
		if progress != nil {
			progress(size)
		}
		return true, nil
	}
	if info.Limits.MaxObjectBytes > 0 && size > info.Limits.MaxObjectBytes {
		return false, obx.Problemf(obx.ErrPayloadTooLarge, "file has %d bytes; the node accepts at most %d", size, info.Limits.MaxObjectBytes)
	}
	var reported int64
	_, err = c.UploadResumable(ctx, r, size, sha, contentType, state, func(done, _ int64) {
		if progress != nil {
			progress(done - reported)
		}
		reported = done
	})
	return err == nil, err
}

// RenditionCache remembers the renditions generated for an original, so a
// re-run does not decode and resize unchanged images again.
type RenditionCache interface {
	Get(originalSHA string) (*obx.ManifestRenditions, bool)
	Put(originalSHA string, r *obx.ManifestRenditions)
}

// FileRenditionCache is a RenditionCache stored as JSON in a file.
type FileRenditionCache struct {
	Path string
	mu   sync.Mutex
	m    map[string]*obx.ManifestRenditions
}

func (f *FileRenditionCache) load() {
	if f.m != nil {
		return
	}
	f.m = map[string]*obx.ManifestRenditions{}
	if b, err := os.ReadFile(f.Path); err == nil {
		_ = json.Unmarshal(b, &f.m)
	}
}

// Get returns cached renditions.
func (f *FileRenditionCache) Get(sha string) (*obx.ManifestRenditions, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	r, ok := f.m[sha]
	return r, ok
}

// Put stores renditions and writes the file.
func (f *FileRenditionCache) Put(sha string, r *obx.ManifestRenditions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	f.m[sha] = r
	if b, err := json.Marshal(f.m); err == nil {
		_ = os.MkdirAll(filepath.Dir(f.Path), 0o755)
		tmp := f.Path + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, f.Path)
		}
	}
}

// ImageSource is an image to prepare: a file on disk or bytes in memory.
type ImageSource struct {
	Name string // file name used in the import (base name)
	Path string // read from disk when Data is nil
	Data []byte
}

func (s ImageSource) bytes() ([]byte, error) {
	if s.Data != nil {
		return s.Data, nil
	}
	return os.ReadFile(s.Path)
}

// PreparedImage is an image that is on the node.
type PreparedImage struct {
	Manifest obx.ManifestImage
	Uploaded int // number of blobs sent (0 when everything was already there)
	Bytes    int64
}

// Preparer uploads images for an import: it hashes them, uploads only what
// the node does not have and, when the node does not render images itself,
// generates and uploads web, square and thumb.
type Preparer struct {
	Client  *Client
	Cache   RenditionCache // optional
	Uploads UploadState    // optional
	// ForceRenditions generates renditions even on nodes that render.
	ForceRenditions bool
	// OnBytes, when set, is told about every chunk of bytes uploaded.
	OnBytes func(sent int64)
}

// Prepare makes one image available as a blob and returns its manifest entry.
func (p *Preparer) Prepare(ctx context.Context, src ImageSource) (*PreparedImage, error) {
	info, err := p.Client.Info(ctx)
	if err != nil {
		return nil, err
	}
	data, err := src.bytes()
	if err != nil {
		return nil, err
	}
	sha := HashBytes(data)
	meta, err := renditions.ReadInfo(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Name, err)
	}
	if len(info.AcceptedContentTypes) > 0 && !obx.Contains(info.AcceptedContentTypes, meta.ContentType) {
		return nil, fmt.Errorf("%s: the node does not accept %s images", src.Name, meta.ContentType)
	}
	out := &PreparedImage{Bytes: int64(len(data)), Manifest: obx.ManifestImage{File: filepath.Base(src.Name), SHA256: sha, Width: meta.Width, Height: meta.Height}}
	up, err := p.Client.EnsureBlobProgress(ctx, bytes.NewReader(data), int64(len(data)), sha, meta.ContentType, p.Uploads, p.OnBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Name, err)
	}
	if up {
		out.Uploaded++
	}
	if info.Renditions.ServerGenerated && !p.ForceRenditions {
		return out, nil
	}
	if p.Cache != nil {
		if r, ok := p.Cache.Get(sha); ok && r.Web != nil && r.Square != nil && r.Thumb != nil {
			all := true
			for _, ref := range []*obx.BlobRef{r.Web, r.Square, r.Thumb} {
				has, err := p.Client.HasBlob(ctx, ref.SHA256)
				if err != nil {
					return nil, err
				}
				all = all && has
			}
			if all {
				out.Manifest.Renditions = r
				return out, nil
			}
		}
	}
	set, err := renditions.Generate(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Name, err)
	}
	for _, f := range set.All() {
		up, err := p.Client.EnsureBlobProgress(ctx, bytes.NewReader(f.Data), int64(len(f.Data)), f.SHA256, f.ContentType, p.Uploads, p.OnBytes)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", src.Name, f.Name, err)
		}
		if up {
			out.Uploaded++
		}
	}
	out.Manifest.Renditions = set.Manifest()
	if p.Cache != nil {
		p.Cache.Put(sha, out.Manifest.Renditions)
	}
	return out, nil
}

// File is a named file for an import request.
type File struct {
	Name string
	Data []byte
}

// ImportRequest is the multipart body of POST /imports.
type ImportRequest struct {
	Package        *File
	CSV            *File
	AssetsCSV      *File
	Images         []File
	Manifest       []obx.ManifestImage
	Options        obx.ImportOptions
	IdempotencyKey string
}

// Import starts an import job (always asynchronous; use WaitJob).
func (c *Client) Import(ctx context.Context, in ImportRequest) (*obx.IngestJob, error) {
	body, ct, err := EncodeImport(in)
	if err != nil {
		return nil, err
	}
	var h http.Header
	if in.IdempotencyKey != "" {
		h = http.Header{obx.HeaderIdempotencyKey: {in.IdempotencyKey}}
	}
	resp, err := c.Do(ctx, http.MethodPost, "/imports", body, ct, h)
	if err != nil {
		return nil, err
	}
	var job obx.IngestJob
	return &job, httpx.Decode(resp, &job)
}

// EncodeImport builds the multipart/form-data body of an import request (the
// same fields work for the node admin API's POST /admin/api/imports).
func EncodeImport(in ImportRequest) ([]byte, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	addFile := func(field string, f *File, ct string) error {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filepath.Base(f.Name)))
		h.Set("Content-Type", ct)
		w, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		_, err = w.Write(f.Data)
		return err
	}
	if in.Package != nil {
		if err := addFile("package", in.Package, "application/zip"); err != nil {
			return nil, "", err
		}
	}
	if in.CSV != nil {
		if err := addFile("csv", in.CSV, "text/csv"); err != nil {
			return nil, "", err
		}
	}
	if in.AssetsCSV != nil {
		if err := addFile("assets_csv", in.AssetsCSV, "text/csv"); err != nil {
			return nil, "", err
		}
	}
	images := append([]File(nil), in.Images...)
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	for i := range images {
		ct := renditions.DetectContentType(images[i].Data)
		if ct == "" {
			ct = "application/octet-stream"
		}
		if err := addFile("images", &images[i], ct); err != nil {
			return nil, "", err
		}
	}
	addJSON := func(field string, v any) error {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, field))
		h.Set("Content-Type", "application/json")
		w, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		return json.NewEncoder(w).Encode(v)
	}
	if in.Manifest != nil {
		if err := addJSON("images_manifest", in.Manifest); err != nil {
			return nil, "", err
		}
	}
	if err := addJSON("options", in.Options); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
