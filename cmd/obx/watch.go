package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
)

func defineWatch(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	var o pushOptions
	fs.StringVar(&o.url, "url", "", e.t("flag.url"))
	fs.StringVar(&o.token, "token", "", e.t("flag.token"))
	fs.BoolVar(&o.mirror, "mirror", false, e.t("push.flag_mirror"))
	fs.BoolVar(&o.allowMass, "allow-mass-unpublish", false, e.t("push.flag_mass"))
	fs.StringVar(&o.language, "language", "", e.t("push.flag_language"))
	fs.IntVar(&o.workers, "parallel", 4, e.t("push.flag_parallel"))
	interval := fs.Duration("interval", 5*time.Second, e.t("watch.flag_interval"))
	settle := fs.Duration("settle", 10*time.Second, e.t("watch.flag_settle"))
	once := fs.Bool("once", false, e.t("watch.flag_once"))
	return func(ctx context.Context, pos []string) (int, error) {
		if len(pos) != 1 {
			return exitFailed, usageError{e.t("watch.one_folder")}
		}
		o.folder = pos[0]
		return exitOK, watch(ctx, e, o, *interval, *settle, *once)
	}
}

func watch(ctx context.Context, e *env, o pushOptions, interval, settle time.Duration, once bool) error {
	if st, err := os.Stat(o.folder); err != nil || !st.IsDir() {
		return userErr(e.t("watch.not_a_folder", "path", o.folder))
	}
	if _, _, err := e.target(o.url, o.token); err != nil {
		return err
	}
	logf := func(msg string) {
		fmt.Fprintf(e.stderr, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	}
	quiet := *e
	quiet.isTTY = false // one line per event in a log, no progress bars
	statePath := filepath.Join(e.cacheDir, "watch-"+shortHash(absPath(o.folder))+".txt")
	published, _ := os.ReadFile(statePath)
	lastPublished := strings.TrimSpace(string(published))
	if !once {
		logf(e.t("watch.watching", "folder", o.folder))
	}
	var seen string
	var quietSince time.Time
	for {
		sig, files, err := folderSignature(o.folder)
		if err != nil {
			logf(e.t("watch.cannot_read", "reason", friendly(e, err)))
		}
		now := time.Now()
		if sig != seen {
			seen, quietSince = sig, now
		}
		ready := err == nil && files > 0 && sig != lastPublished && (now.Sub(quietSince) >= settle || once)
		if ready {
			logf(e.t("watch.publishing", "count", files))
			res, perr := push(ctx, o, &quiet)
			switch {
			case perr != nil && ctx.Err() != nil:
				logf(e.t("watch.stopped"))
				return nil
			case perr != nil:
				logf(e.t("watch.failed", "reason", friendly(e, perr)))
			default:
				if res.Job != nil && res.Job.ImportRun != nil {
					c := res.Job.ImportRun.Counts
					logf(e.t("watch.done") + " " + countsText(e, c) + " · " + e.t("count.problems", "n", c.Errors+len(res.LocalNotes)))
				}
				lastPublished = sig
				// Best effort: without the state file the next start publishes
				// once more, which changes nothing at the portal.
				_ = os.MkdirAll(filepath.Dir(statePath), 0o700)
				_ = os.WriteFile(statePath, []byte(sig+"\n"), 0o600)
			}
			if once {
				return perr
			}
		} else if once {
			if files == 0 {
				logf(e.t("watch.nothing", "folder", o.folder))
			} else {
				logf(e.t("watch.unchanged"))
			}
			return err
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			logf(e.t("watch.stopped"))
			return nil
		case <-t.C:
		}
	}
}

var _ = register(map[string]string{
	"cmd.watch.about":     "Watches a folder where your PIM, ERP or DAM drops its exports, and publishes whenever something changes (the same as \"obx push <folder>\"). It waits until the folder has been quiet for a while, so half-written files are never sent. Run it as a service (systemd, Docker, Windows Task Scheduler); stop it with Ctrl-C.",
	"watch.flag_interval": "how often to look for changes",
	"watch.flag_settle":   "wait until the folder has been quiet this long before publishing",
	"watch.flag_once":     "publish once if anything changed, then stop",
	"watch.one_folder":    "give exactly one folder to watch.",
	"watch.not_a_folder":  "{path} is not a folder.",
	"watch.watching":      "watching {folder} (Ctrl-C to stop)",
	"watch.cannot_read":   "cannot read the folder: {reason}",
	"watch.publishing":    "publishing files: {count}",
	"watch.failed":        "failed: {reason} (obx tries again on the next change)",
	"watch.done":          "done:",
	"watch.nothing":       "nothing to publish in {folder}",
	"watch.unchanged":     "nothing changed since the last push",
	"watch.stopped":       "stopped",
})

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
