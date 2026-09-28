package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
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

func definePull(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	out := fs.String("out", "", e.t("pull.flag_out"))
	id := fs.String("client-id", "", e.t("pull.flag_client_id"))
	secret := fs.String("client-secret", "", e.t("pull.flag_client_secret"))
	rends := fs.String("renditions", "web", e.t("pull.flag_renditions"))
	follow := fs.Bool("follow", false, e.t("pull.flag_follow"))
	interval := fs.Duration("interval", 60*time.Second, e.t("pull.flag_interval"))
	return func(ctx context.Context, pos []string) (int, error) {
		addr := e.getenv("OBX_URL")
		if len(pos) > 0 {
			addr = pos[0]
		}
		if len(pos) > 1 || addr == "" || *out == "" {
			return exitFailed, usageError{e.t("pull.usage")}
		}
		if *id == "" {
			*id = e.getenv("OBX_CLIENT_ID")
		}
		if *secret == "" {
			*secret = e.getenv("OBX_CLIENT_SECRET")
		}
		if *id == "" || *secret == "" {
			return exitFailed, userErr(e.t("pull.need_key"))
		}
		var names []string
		for _, r := range strings.Split(*rends, ",") {
			if r = strings.TrimSpace(r); r != "" {
				if !obx.Contains(obx.RenditionNames, r) {
					return exitFailed, usageError{e.t("pull.unknown_rendition", "name", r)}
				}
				names = append(names, r)
			}
		}
		base, err := e.portalBase(addr)
		if err != nil {
			return exitFailed, err
		}
		c, err := client.New(client.Config{BaseURL: base, ClientID: *id, ClientSecret: *secret, UserAgent: "obx-cli/" + Version})
		if err != nil {
			return exitFailed, err
		}
		m := &pullMirror{dir: *out, c: c, renditions: names, e: e}
		if err := m.open(); err != nil {
			return exitFailed, err
		}
		fmt.Fprintln(e.stderr, e.t("pull.mirroring", "portal", c.Base(), "folder", *out))
		opts := client.SyncOptions{Limit: 500, Follow: *follow, PollInterval: *interval}
		err = c.Sync(ctx, client.FileCursorStore(filepath.Join(*out, ".obx", "cursor")), opts, m.handle)
		if saveErr := m.saveIndex(); err == nil {
			err = saveErr
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return exitOK, nil
			}
			return exitFailed, err
		}
		return exitOK, nil
	}
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
		if json.Unmarshal(b, &m.index) != nil {
			// A damaged index only means files are downloaded again.
			m.index.Files = nil
		}
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
		fmt.Fprintln(m.e.stderr, "  "+m.e.t("pull.resync", "reason", ev.Reason))
	case client.EventSnapshotComplete:
		m.sweep()
		m.seen = nil
	case client.EventCaughtUp:
		if !m.changed && !m.reported {
			fmt.Fprintln(m.e.stdout, m.e.t("pull.nothing_changed"))
		}
		m.reported = true
		if m.changed {
			fmt.Fprintln(m.e.stdout, m.e.t("pull.up_to_date", "updated", m.stats.upserts, "removed", m.stats.deletes, "downloaded", m.stats.downloads, "kept", m.stats.kept))
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
	if json.Indent(&pretty, ch.Data, "", "  ") != nil {
		pretty.Reset()
		pretty.Write(ch.Data) // keep what the portal sent
	}
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
				return fmt.Errorf("%s (%s): %w", a.ExternalID, name, err)
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

var _ = register(map[string]string{
	"cmd.pull.about":          "For retailers and developers: keeps a folder with a copy of a brand's catalog up to date, with the API key for your software that the brand gave you (client id and secret). Products, variants (with EAN) and images are stored as JSON files, the images under images/<style>/<version>/, every file checked against its checksum. Run it again, or with --follow, to fetch only what changed; what the brand took offline is removed. Without --renditions it downloads the web version of every image.",
	"pull.flag_out":           "folder to keep the catalog in (created when missing)",
	"pull.flag_client_id":     "API key for your software: client id (default: OBX_CLIENT_ID)",
	"pull.flag_client_secret": "API key for your software: client secret (default: OBX_CLIENT_SECRET)",
	"pull.flag_renditions":    "image versions to download, separated by commas: original, web, square, thumb (empty: none)",
	"pull.flag_follow":        "keep running and pick up changes as they happen",
	"pull.flag_interval":      "with --follow: how often to check for changes",
	"pull.usage":              "give the portal address and --out <folder>.",
	"pull.need_key":           "an API key for your software is needed (--client-id and --client-secret, or OBX_CLIENT_ID and OBX_CLIENT_SECRET). Ask the brand for one.",
	"pull.unknown_rendition":  "unknown image version \"{name}\" (use original, web, square or thumb).",
	"pull.mirroring":          "Copying {portal} into {folder}",
	"pull.resync":             "the brand asked for a full refresh ({reason})",
	"pull.nothing_changed":    "Up to date: nothing changed since the last pull.",
	"pull.up_to_date":         "Up to date: updated: {updated} · removed: {removed} · images downloaded: {downloaded} · images already present: {kept}",
})
