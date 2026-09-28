package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/obx"
)

// trace is everything one node answered during an interop run, already
// normalised. Keys starting with "_" are informational and not compared.
type trace struct {
	Info      map[string]any `json:"_info"`
	AfterPush *snapshot      `json:"after_push"`
	Steps     []step         `json:"steps"`
	Final     *snapshot      `json:"final"`
	Problems  []string       `json:"_problems,omitempty"`
}

// snapshot is the protocol view at one moment: the change feed read from
// the start, every product in full and the files of every asset.
type snapshot struct {
	Feed     []any                     `json:"feed"`
	Live     []any                     `json:"live,omitempty"`
	Products map[string]any            `json:"products"`
	Files    map[string]map[string]any `json:"files"`
	Derived  map[string]string         `json:"_derived_sha256,omitempty"`
}

// step is one line of fixtures/ingest/script.ndjson and what it did.
type step struct {
	Op      string         `json:"op"`
	Result  string         `json:"result"`
	Error   string         `json:"error,omitempty"`
	Record  any            `json:"ingest_record,omitempty"`
	Changes []any          `json:"changes"`
	Product any            `json:"product,omitempty"`
	Files   map[string]any `json:"files,omitempty"`
}

type tracer struct {
	c       *client.Client
	ing     *ingest.Client
	hc      *http.Client
	labels  map[string]string // node id → "type:external_id"
	info    *obx.IngestInfo
	log     io.Writer
	derived map[string]string
	token   string
}

func runTrace(ctx context.Context, base string, cr *creds, fixtures fs.FS, log io.Writer) (*trace, error) {
	hc := &http.Client{Timeout: 2 * time.Minute}
	c, err := client.New(client.Config{BaseURL: base, ClientID: cr.ClientID, ClientSecret: cr.ClientSecret, HTTPClient: hc, UserAgent: "obx-interop", MaxRetries: 3})
	if err != nil {
		return nil, err
	}
	ing, err := ingest.New(base, cr.IngestToken, ingest.WithHTTPClient(hc), ingest.WithUserAgent("obx-interop"))
	if err != nil {
		return nil, err
	}
	tr := &tracer{c: c, ing: ing, hc: hc, labels: map[string]string{}, log: log, token: cr.IngestToken}
	if tr.info, err = ing.Info(ctx); err != nil {
		return nil, fmt.Errorf("ingest info: %w", err)
	}
	t := &trace{Info: map[string]any{"url": base, "renditions_server_generated": tr.info.Renditions.ServerGenerated}}
	if d, err := c.Discovery(ctx); err == nil {
		t.Info["node"] = d.Node
	}

	fmt.Fprintln(log, "reading the catalog after obx push")
	if t.AfterPush, err = tr.snapshot(ctx); err != nil {
		return t, err
	}
	cursor, err := tr.liveCursor(ctx)
	if err != nil {
		return t, err
	}

	ops, results, err := loadScript(fixtures)
	if err != nil {
		return t, err
	}
	rend, err := tr.uploadScriptFiles(ctx, fixtures)
	if err != nil {
		return t, err
	}
	var kl9001 string
	for i, op := range ops {
		if !tr.info.Renditions.ServerGenerated && op.Kind == obx.KindAsset && op.Op == obx.IngestOpUpsert {
			op.Data = withRenditions(op.Data, rend)
		}
		s := step{Op: fmt.Sprintf("%d %s %s %s", i, op.Op, op.Kind, op.ExternalID)}
		res, err := tr.send(ctx, op)
		if err != nil {
			s.Result = obx.ResultError
			if p, ok := err.(*obx.Problem); ok {
				s.Error = p.ObxError
			} else {
				return t, fmt.Errorf("op %d: %w", i, err)
			}
		} else {
			s.Result = res.Result
			if res.ID != nil {
				tr.labels[*res.ID] = op.Kind + ":" + op.ExternalID
			}
		}
		if s.Result != results[i].Result || s.Error != results[i].ObxError {
			t.Problems = append(t.Problems, fmt.Sprintf("op %d: got %s %s, fixture expects %s %s", i, s.Result, s.Error, results[i].Result, results[i].ObxError))
		}
		if err == nil && op.Kind == obx.KindAsset && op.Op == obx.IngestOpUpsert {
			tr.waitNotPending(ctx, op.ExternalID)
		}
		if rec, err := tr.ingestRecord(ctx, op.Kind, op.ExternalID); err == nil {
			s.Record = rec
		}
		var entries []obx.Change
		entries, cursor, err = tr.readFrom(ctx, cursor)
		if err != nil {
			return t, fmt.Errorf("changes after op %d: %w", i, err)
		}
		for _, e := range entries {
			s.Changes = append(s.Changes, tr.rawChange(e))
			if e.Type == obx.ChangeProduct && e.ExternalIDValue() == "KL-9001" {
				kl9001 = e.ID
			}
		}
		if kl9001 != "" {
			if p, files, err := tr.productWithFiles(ctx, kl9001); err == nil {
				s.Product, s.Files = p, files
			}
		}
		t.Steps = append(t.Steps, s)
	}
	fmt.Fprintln(log, "reading the catalog after the ingest script")
	if t.Final, err = tr.snapshot(ctx); err != nil {
		return t, err
	}
	if len(t.Problems) > 0 {
		for _, p := range t.Problems {
			fmt.Fprintln(log, "  problem:", p)
		}
	}
	tr.normaliseTrace(t)
	if len(t.Problems) > 0 {
		return t, errFailed
	}
	return t, nil
}

// ---- protocol reads

func (tr *tracer) page(ctx context.Context, cursor string) (*obx.ChangesPage, error) {
	return tr.c.Changes(ctx, cursor, 100)
}

// snapshot reads the whole feed without a cursor plus every product and file.
func (tr *tracer) snapshot(ctx context.Context) (*snapshot, error) {
	s := &snapshot{Products: map[string]any{}, Files: map[string]map[string]any{}, Derived: map[string]string{}}
	tr.derived = s.Derived
	cursor := ""
	var assetIDs, productIDs []string
	for {
		p, err := tr.page(ctx, cursor)
		if err != nil {
			return nil, fmt.Errorf("changes: %w", err)
		}
		for _, e := range p.Changes {
			tr.learn(e)
			raw := tr.rawChange(e)
			raw["phase"] = p.Phase
			if p.Phase == obx.PhaseSnapshot {
				s.Feed = append(s.Feed, raw)
				switch e.Type {
				case obx.ChangeProduct:
					productIDs = append(productIDs, e.ID)
				case obx.ChangeAsset:
					assetIDs = append(assetIDs, e.ID)
				}
			} else {
				s.Live = append(s.Live, raw)
			}
		}
		cursor = p.NextCursor
		if !p.HasMore {
			break
		}
	}
	for _, id := range productIDs {
		raw, err := tr.getRaw(ctx, "/products/"+url.PathEscape(id))
		if err != nil {
			return nil, fmt.Errorf("product %s: %w", id, err)
		}
		s.Products[tr.labelOf(id)] = raw
	}
	for _, id := range assetIDs {
		files, err := tr.assetFiles(ctx, id)
		if err != nil {
			return nil, err
		}
		s.Files[tr.labelOf(id)] = files
	}
	return s, nil
}

// liveCursor drains the snapshot and returns the first live cursor.
func (tr *tracer) liveCursor(ctx context.Context) (string, error) {
	cursor := ""
	for {
		p, err := tr.page(ctx, cursor)
		if err != nil {
			return "", err
		}
		cursor = p.NextCursor
		if p.Phase == obx.PhaseLive && !p.HasMore {
			return cursor, nil
		}
	}
}

// readFrom returns the live entries after cursor.
func (tr *tracer) readFrom(ctx context.Context, cursor string) ([]obx.Change, string, error) {
	var out []obx.Change
	for {
		p, err := tr.page(ctx, cursor)
		if err != nil {
			return nil, cursor, err
		}
		for _, e := range p.Changes {
			tr.learn(e)
		}
		out = append(out, p.Changes...)
		cursor = p.NextCursor
		if !p.HasMore {
			return out, cursor, nil
		}
	}
}

func (tr *tracer) learn(e obx.Change) {
	if e.ExternalID != nil && e.ID != "" {
		tr.labels[e.ID] = e.Type + ":" + *e.ExternalID
	}
	if e.Type == obx.ChangeGrant && e.ID != "" {
		tr.labels[e.ID] = "grant"
	}
}

func (tr *tracer) labelOf(id string) string {
	if l, ok := tr.labels[id]; ok {
		return l
	}
	return id
}

// rawChange keeps an entry as the node sent it (as generic JSON).
func (tr *tracer) rawChange(e obx.Change) map[string]any {
	b, _ := json.Marshal(e)
	var m map[string]any
	_ = json.Unmarshal(b, &m) // b was just marshalled
	if len(e.Data) > 0 {
		var d any
		if err := json.Unmarshal(e.Data, &d); err != nil {
			d = string(e.Data) // keep what the node sent; the comparison shows it
		}
		m["data"] = d
	}
	return m
}

func (tr *tracer) getRaw(ctx context.Context, path string) (any, error) {
	resp, err := tr.c.Do(ctx, http.MethodGet, path, nil, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, bytes.TrimSpace(b))
	}
	var v any
	return v, json.Unmarshal(b, &v)
}

// assetFiles downloads every rendition of an asset and checks its sha256.
// Originals must be byte-identical everywhere; web, square and thumb are
// made by each node's own image library, so for those only the declared
// dimensions and the self-consistency of the hash are compared.
func (tr *tracer) assetFiles(ctx context.Context, id string) (map[string]any, error) {
	a, err := tr.c.Asset(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("asset %s: %w", id, err)
	}
	out := map[string]any{}
	for _, name := range obx.RenditionNames {
		r := a.Renditions.Get(name)
		if r == nil {
			continue
		}
		var buf bytes.Buffer
		if _, err := tr.c.Download(ctx, r, &buf); err != nil {
			out[name] = map[string]any{"download_error": err.Error()}
			continue
		}
		sum := sha256.Sum256(buf.Bytes())
		got := hex.EncodeToString(sum[:])
		f := map[string]any{"content_type": r.ContentType, "width": r.Width, "height": r.Height, "sha256_matches_bytes": got == r.SHA256, "bytes_match": int64(buf.Len()) == r.Bytes}
		if name == obx.RenditionOriginal {
			f["sha256"] = got
			f["bytes"] = buf.Len()
		} else if tr.derived != nil {
			tr.derived[a.ExternalID+"/"+name] = got
		}
		out[name] = f
	}
	return out, nil
}

func (tr *tracer) productWithFiles(ctx context.Context, id string) (any, map[string]any, error) {
	raw, err := tr.getRaw(ctx, "/products/"+url.PathEscape(id))
	if err != nil {
		return nil, nil, err
	}
	var p obx.ProductDetail
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &p) // a round trip of a decoded document
	files := map[string]any{}
	saved := tr.derived
	tr.derived = nil
	for _, a := range p.Assets {
		tr.labels[a.ID] = "asset:" + a.ExternalID
		f, err := tr.assetFiles(ctx, a.ID)
		if err != nil {
			return nil, nil, err
		}
		files["asset:"+a.ExternalID] = f
	}
	tr.derived = saved
	return raw, files, nil
}

// ---- ingest

type scriptOp struct {
	Op         string          `json:"op"`
	Kind       string          `json:"kind"`
	ExternalID string          `json:"external_id"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type expectedResult struct {
	Result   string `json:"result"`
	ObxError string `json:"obx_error"`
}

func loadScript(fixtures fs.FS) ([]scriptOp, []expectedResult, error) {
	raw, err := fs.ReadFile(fixtures, "ingest/script.ndjson")
	if err != nil {
		return nil, nil, err
	}
	var ops []scriptOp
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var op scriptOp
		if err := json.Unmarshal([]byte(line), &op); err != nil {
			return nil, nil, err
		}
		ops = append(ops, op)
	}
	var results []expectedResult
	b, err := fs.ReadFile(fixtures, "ingest/expected-results.json")
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(b, &results); err != nil {
		return nil, nil, err
	}
	if len(results) != len(ops) {
		return nil, nil, fmt.Errorf("script has %d operations, expected-results.json %d", len(ops), len(results))
	}
	return ops, results, nil
}

func (tr *tracer) uploadScriptFiles(ctx context.Context, fixtures fs.FS) (map[string]*obx.ManifestRenditions, error) {
	entries, err := fs.ReadDir(fixtures, "ingest/files")
	if err != nil {
		return nil, err
	}
	prep := &ingest.Preparer{Client: tr.ing}
	out := map[string]*obx.ManifestRenditions{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		data, err := fs.ReadFile(fixtures, "ingest/files/"+e.Name())
		if err != nil {
			return nil, err
		}
		p, err := prep.Prepare(ctx, ingest.ImageSource{Name: e.Name(), Data: data})
		if err != nil {
			return nil, fmt.Errorf("upload %s: %w", e.Name(), err)
		}
		out[p.Manifest.SHA256] = p.Manifest.Renditions
	}
	return out, nil
}

func withRenditions(data json.RawMessage, rend map[string]*obx.ManifestRenditions) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	files, _ := m["files"].(map[string]any)
	orig, _ := files["original"].(map[string]any)
	sha, _ := orig["sha256"].(string)
	r := rend[sha]
	if r == nil {
		return data
	}
	for name, ref := range map[string]*obx.BlobRef{"web": r.Web, "square": r.Square, "thumb": r.Thumb} {
		if ref != nil {
			files[name] = map[string]any{"sha256": ref.SHA256, "width": ref.Width, "height": ref.Height}
		}
	}
	out, _ := json.Marshal(m)
	return out
}

func (tr *tracer) send(ctx context.Context, op scriptOp) (*obx.IngestResult, error) {
	switch op.Op {
	case obx.IngestOpUpsert:
		return tr.ing.Put(ctx, op.Kind, op.ExternalID, op.Data, nil)
	case obx.IngestOpPatch:
		return tr.ing.Patch(ctx, op.Kind, op.ExternalID, op.Data, nil)
	case obx.IngestOpPublish:
		return tr.ing.Publish(ctx, op.Kind, op.ExternalID)
	case obx.IngestOpUnpublish:
		return tr.ing.Unpublish(ctx, op.Kind, op.ExternalID)
	case obx.IngestOpDelete:
		return tr.ing.Delete(ctx, op.Kind, op.ExternalID)
	}
	return nil, fmt.Errorf("unknown op %q", op.Op)
}

func (tr *tracer) waitNotPending(ctx context.Context, ext string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := tr.ing.Get(ctx, obx.KindAsset, ext)
		if err != nil || rec.Visibility != obx.VisibilityPendingFiles {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// ingestRecord reads GET /obx/v1/ingest/{kind}s/{external_id} as raw JSON.
func (tr *tracer) ingestRecord(ctx context.Context, kind, ext string) (any, error) {
	u := strings.TrimRight(tr.c.Base(), "/") + obx.PathIngest + "/" + kind + "s/" + url.PathEscape(ext)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+tr.ingToken())
	resp, err := tr.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return map[string]any{"status": 404}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d", u, resp.StatusCode)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	if m, ok := v.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			tr.labels[id] = kind + ":" + ext
		}
	}
	return v, nil
}

func (tr *tracer) ingToken() string { return tr.token }

// ---- normalisation

func (tr *tracer) normaliseTrace(t *trace) {
	for _, s := range []*snapshot{t.AfterPush, t.Final} {
		if s == nil {
			continue
		}
		for i := range s.Feed {
			s.Feed[i] = normalise(s.Feed[i], tr.labels)
		}
		sortSnapshot(s.Feed)
		for i := range s.Live {
			s.Live[i] = normalise(s.Live[i], tr.labels)
		}
		for k, v := range s.Products {
			s.Products[k] = normalise(v, tr.labels)
		}
	}
	for i := range t.Steps {
		st := &t.Steps[i]
		for j := range st.Changes {
			st.Changes[j] = normalise(st.Changes[j], tr.labels)
		}
		st.Record = normalise(st.Record, tr.labels)
		st.Product = normalise(st.Product, tr.labels)
	}
}

var typeOrder = map[string]int{obx.ChangeBrand: 0, obx.ChangeProduct: 1, obx.ChangeVariant: 2, obx.ChangeAsset: 3, obx.ChangeGrant: 4}

// sortSnapshot orders snapshot entries by type, then label: the spec orders
// by node id within a type, and node ids differ per implementation.
func sortSnapshot(entries []any) {
	key := func(v any) (int, string) {
		m, _ := v.(map[string]any)
		typ, _ := m["type"].(string)
		id, _ := m["id"].(string)
		return typeOrder[typ], id
	}
	sort.SliceStable(entries, func(i, j int) bool {
		ti, ii := key(entries[i])
		tj, ij := key(entries[j])
		if ti != tj {
			return ti < tj
		}
		return ii < ij
	})
}
