package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/obx"
)

// cmdPull mirrors a brand's catalog to a folder through the OBX Protocol.
func cmdPull(ctx context.Context, args []string, e *env) error {
	fs := newFlags("pull", "pull <portal address> --out <folder> [--client-id …] [--client-secret …] [--renditions web,thumb] [--follow]", e)
	out := fs.String("out", "", "folder to keep the catalog in (created when missing)")
	id := fs.String("client-id", "", "API key for your software: client id (default OBX_CLIENT_ID)")
	secret := fs.String("client-secret", "", "API key for your software: client secret (default OBX_CLIENT_SECRET)")
	rends := fs.String("renditions", "web", "comma-separated image versions to download: original, web, square, thumb (empty = none)")
	follow := fs.Bool("follow", false, "keep running and pick up changes as they happen")
	interval := fs.Duration("interval", 60*time.Second, "with --follow: how often to check for changes")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	addr := e.getenv("OBX_URL")
	if len(pos) > 0 {
		addr = pos[0]
	}
	if addr == "" || *out == "" {
		fs.Usage()
		return errors.New("give the portal address and --out <folder>")
	}
	if *id == "" {
		*id = e.getenv("OBX_CLIENT_ID")
	}
	if *secret == "" {
		*secret = e.getenv("OBX_CLIENT_SECRET")
	}
	if *id == "" || *secret == "" {
		return errors.New("an API key for your software is needed (--client-id and --client-secret, or OBX_CLIENT_ID and OBX_CLIENT_SECRET); ask the brand for one")
	}
	var names []string
	for _, r := range strings.Split(*rends, ",") {
		if r = strings.TrimSpace(r); r != "" {
			if !obx.Contains(obx.RenditionNames, r) {
				return fmt.Errorf("unknown image version %q (use original, web, square or thumb)", r)
			}
			names = append(names, r)
		}
	}
	c, err := client.New(client.Config{BaseURL: addr, ClientID: *id, ClientSecret: *secret, UserAgent: "obx-cli/" + Version})
	if err != nil {
		return err
	}
	m := &pullMirror{dir: *out, c: c, renditions: names, e: e}
	if err := m.open(); err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "Mirroring %s into %s\n", c.Base(), *out)
	opts := client.SyncOptions{Limit: 500, Follow: *follow, PollInterval: *interval}
	err = c.Sync(ctx, client.FileCursorStore(filepath.Join(*out, ".obx", "cursor")), opts, m.handle)
	if saveErr := m.saveIndex(); err == nil {
		err = saveErr
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// pullMirror keeps a folder in sync with the change feed.
type pullMirror struct {
	dir        string
	c          *client.Client
	renditions []string
	e          *env

	index struct {
		Files map[string][]pulledFile `json:"files"` // asset id → files
	}
	seen     map[string]bool
	changed  bool
	reported bool // said something at least once, so a run without changes is not silent
	stats    struct{ upserts, deletes, downloads, kept int }
}

type pulledFile struct {
	Rendition string `json:"rendition"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
}

var typeDirs = map[string]string{obx.ChangeBrand: "brands", obx.ChangeProduct: "products", obx.ChangeVariant: "variants", obx.ChangeAsset: "assets"}

func (m *pullMirror) open() error {
	if err := os.MkdirAll(filepath.Join(m.dir, ".obx"), 0o755); err != nil {
		return err
	}
	m.index.Files = map[string][]pulledFile{}
	if b, err := os.ReadFile(filepath.Join(m.dir, ".obx", "index.json")); err == nil {
		json.Unmarshal(b, &m.index)
		if m.index.Files == nil {
			m.index.Files = map[string][]pulledFile{}
		}
	}
	return nil
}

func (m *pullMirror) saveIndex() error {
	b, _ := json.MarshalIndent(m.index, "", " ")
	return writeAtomic(filepath.Join(m.dir, ".obx", "index.json"), b)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// safeName makes a single path element from untrusted text.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.Trim(s, ". ")
	if s == "" {
		return "_"
	}
	return s
}

func (m *pullMirror) objectPath(typ, id string) string {
	return filepath.Join(m.dir, typeDirs[typ], safeName(id)+".json")
}

func (m *pullMirror) handle(ctx context.Context, ev client.Event) error {
	switch ev.Kind {
	case client.EventSnapshotStarted:
		m.seen = map[string]bool{}
	case client.EventResync:
		fmt.Fprintf(m.e.stderr, "  the brand asked for a full refresh (%s)\n", ev.Reason)
	case client.EventSnapshotComplete:
		m.sweep()
		m.seen = nil
	case client.EventCaughtUp:
		if !m.changed && !m.reported {
			fmt.Fprintln(m.e.stdout, "Up to date: nothing changed since the last pull.")
		}
		m.reported = true
		if m.changed {
			fmt.Fprintf(m.e.stdout, "Up to date: %d updated, %d removed, %d images downloaded, %d images already present.\n", m.stats.upserts, m.stats.deletes, m.stats.downloads, m.stats.kept)
			m.stats = struct{ upserts, deletes, downloads, kept int }{}
			m.changed = false
		}
		return m.saveIndex()
	case client.EventChange:
		return m.apply(ctx, ev.Change)
	}
	return nil
}

func (m *pullMirror) apply(ctx context.Context, ch *obx.Change) error {
	m.changed = true
	if ch.Type == obx.ChangeGrant {
		return writeAtomic(filepath.Join(m.dir, "grant.json"), ch.Data)
	}
	if typeDirs[ch.Type] == "" {
		return nil
	}
	if m.seen != nil {
		m.seen[ch.Type+"/"+ch.ID] = true
	}
	if ch.Op == obx.OpDelete {
		m.stats.deletes++
		m.remove(ch.Type, ch.ID)
		return nil
	}
	m.stats.upserts++
	var pretty bytes.Buffer
	json.Indent(&pretty, ch.Data, "", "  ")
	if err := writeAtomic(m.objectPath(ch.Type, ch.ID), pretty.Bytes()); err != nil {
		return err
	}
	if ch.Type == obx.ChangeAsset && len(m.renditions) > 0 {
		var a obx.Asset
		if err := ch.Decode(&a); err != nil {
			return err
		}
		return m.download(ctx, &a)
	}
	return nil
}

// download fetches the wanted renditions of an asset that are missing or
// changed; every file is checked against its sha256.
func (m *pullMirror) download(ctx context.Context, a *obx.Asset) error {
	style := a.ProductID
	if b, err := os.ReadFile(m.objectPath(obx.ChangeProduct, a.ProductID)); err == nil {
		var p obx.Product
		if json.Unmarshal(b, &p) == nil && p.StyleNumber != "" {
			style = p.StyleNumber
		}
	}
	var kept []pulledFile
	old := m.index.Files[a.ID]
	for _, name := range m.renditions {
		r := a.Renditions.Get(name)
		if r == nil {
			continue // original not offered to this key
		}
		path := filepath.Join(m.dir, "images", safeName(style), name, safeName(canonicalcsv.RenditionFilename(a.Filename, name)))
		if fileHash(path) == r.SHA256 {
			m.stats.kept++
			kept = append(kept, pulledFile{name, path, r.SHA256})
			continue
		}
		var buf bytes.Buffer
		if _, err := m.c.Download(ctx, r, &buf); err != nil {
			if fresh, ferr := m.c.Asset(ctx, a.ID); ferr == nil {
				if fr := fresh.Renditions.Get(name); fr != nil {
					buf.Reset()
					_, err = m.c.Download(ctx, fr, &buf)
				}
			}
			if err != nil {
				return fmt.Errorf("download %s (%s): %w", a.ExternalID, name, err)
			}
		}
		if err := writeAtomic(path, buf.Bytes()); err != nil {
			return err
		}
		m.stats.downloads++
		kept = append(kept, pulledFile{name, path, r.SHA256})
	}
	for _, f := range old {
		stillUsed := false
		for _, k := range kept {
			if k.Path == f.Path {
				stillUsed = true
			}
		}
		if !stillUsed {
			os.Remove(f.Path)
		}
	}
	m.index.Files[a.ID] = kept
	return nil
}

func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (m *pullMirror) remove(typ, id string) {
	os.Remove(m.objectPath(typ, id))
	if typ == obx.ChangeAsset {
		for _, f := range m.index.Files[id] {
			os.Remove(f.Path)
		}
		delete(m.index.Files, id)
	}
}

// sweep removes everything the snapshot did not mention.
func (m *pullMirror) sweep() {
	for typ, dir := range typeDirs {
		entries, _ := os.ReadDir(filepath.Join(m.dir, dir))
		for _, en := range entries {
			id := strings.TrimSuffix(en.Name(), ".json")
			if !strings.HasSuffix(en.Name(), ".json") || m.seen[typ+"/"+id] {
				continue
			}
			m.stats.deletes++
			m.changed = true
			m.remove(typ, id)
		}
	}
}
