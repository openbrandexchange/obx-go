package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
)

func cmdWatch(ctx context.Context, args []string, e *env) error {
	fs := newFlags("watch", "watch <folder> [--interval 5s] [--settle 10s] [--mirror] [--once]", e)
	var o pushOptions
	fs.StringVar(&o.url, "url", "", "portal address (default: OBX_URL or the one from obx login)")
	fs.StringVar(&o.token, "token", "", "upload key (default: OBX_TOKEN or the stored key)")
	fs.BoolVar(&o.mirror, "mirror", false, "also take offline products of these brands that are not in the file")
	fs.BoolVar(&o.allowMass, "allow-mass-unpublish", false, "with --mirror: allow taking more than half of the products offline")
	fs.StringVar(&o.language, "language", "", "language of columns without a language")
	fs.IntVar(&o.workers, "parallel", 4, "images checked/uploaded at the same time")
	interval := fs.Duration("interval", 5*time.Second, "how often to look for changes")
	settle := fs.Duration("settle", 10*time.Second, "wait until the folder has been quiet this long before publishing")
	once := fs.Bool("once", false, "publish once if there is anything, then stop")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return fmt.Errorf("give exactly one folder to watch")
	}
	o.folder = pos[0]
	if st, err := os.Stat(o.folder); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", o.folder)
	}
	if _, _, err := e.target(o.url, o.token); err != nil {
		return err
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(e.stderr, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	}
	statePath := filepath.Join(e.cacheDir, "watch-"+shortHash(absPath(o.folder))+".txt")
	published, _ := os.ReadFile(statePath)
	lastPublished := strings.TrimSpace(string(published))
	if !*once {
		logf("watching %s (Ctrl-C to stop)", o.folder)
	}
	var seen string
	var quietSince time.Time
	for {
		sig, files, err := folderSignature(o.folder)
		if err != nil {
			logf("cannot read the folder: %v", err)
		}
		now := time.Now()
		if sig != seen {
			seen, quietSince = sig, now
		}
		ready := err == nil && files > 0 && sig != lastPublished && (now.Sub(quietSince) >= *settle || *once)
		if ready {
			logf("publishing %d files…", files)
			res, perr := push(ctx, o, e)
			switch {
			case perr != nil:
				logf("failed: %s (will retry on the next change)", friendly(perr))
			default:
				if res.Job != nil && res.Job.ImportRun != nil {
					c := res.Job.ImportRun.Counts
					logf("done: %d new, %d updated, %d unchanged, %d problems", c.Created, c.Updated, c.Unchanged, c.Errors+len(res.LocalNotes))
				}
				lastPublished = sig
				os.MkdirAll(filepath.Dir(statePath), 0o700)
				os.WriteFile(statePath, []byte(sig+"\n"), 0o600)
			}
			if *once {
				return perr
			}
		} else if *once {
			if files == 0 {
				logf("nothing to publish in %s", o.folder)
			} else {
				logf("nothing changed since the last push")
			}
			return err
		}
		t := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			t.Stop()
			logf("stopped")
			return nil
		case <-t.C:
		}
	}
}

// folderSignature summarises the CSV and image files (name, size, mtime).
func folderSignature(dir string) (string, int, error) {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel != "." && canonicalcsv.IgnoredPath(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if !strings.HasSuffix(name, ".csv") && !canonicalcsv.IsImageFile(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s|%d|%d", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	sort.Strings(lines)
	return shortHash(strings.Join(lines, "\n")), len(lines), err
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:12])
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
