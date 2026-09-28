package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openbrandexchange/obx-go/canonicalcsv"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/renditions"
)

// pushOptions are the options of obx push (and obx watch).
type pushOptions struct {
	url, token     string
	folder         string
	csv, assetsCSV string
	images         string
	pkg            string
	mirror         bool
	allowMass      bool
	dryRun         bool
	language       string
	noWait         bool
	asJSON         bool
	workers        int
}

func definePush(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	var o pushOptions
	fs.StringVar(&o.url, "url", "", e.t("flag.url"))
	fs.StringVar(&o.token, "token", "", e.t("flag.token"))
	fs.StringVar(&o.csv, "csv", "", e.t("push.flag_csv"))
	fs.StringVar(&o.assetsCSV, "assets-csv", "", e.t("push.flag_assets"))
	fs.StringVar(&o.images, "images", "", e.t("push.flag_images"))
	fs.StringVar(&o.pkg, "package", "", e.t("push.flag_package"))
	fs.BoolVar(&o.mirror, "mirror", false, e.t("push.flag_mirror"))
	fs.BoolVar(&o.allowMass, "allow-mass-unpublish", false, e.t("push.flag_mass"))
	fs.BoolVar(&o.dryRun, "dry-run", false, e.t("push.flag_dry_run"))
	fs.StringVar(&o.language, "language", "", e.t("push.flag_language"))
	fs.BoolVar(&o.noWait, "no-wait", false, e.t("push.flag_no_wait"))
	fs.BoolVar(&o.asJSON, "json", false, e.t("flag.json"))
	fs.IntVar(&o.workers, "parallel", 4, e.t("push.flag_parallel"))
	return func(ctx context.Context, pos []string) (int, error) {
		if len(pos) > 1 {
			return exitFailed, usageError{e.t("push.one_folder", "folders", strings.Join(pos, ", "))}
		}
		if len(pos) == 1 {
			o.folder = pos[0]
		}
		if o.folder == "" && o.csv == "" && o.images == "" && o.pkg == "" && o.assetsCSV == "" {
			return exitFailed, usageError{e.t("push.nothing_given")}
		}
		res, err := push(ctx, o, e)
		if err != nil {
			return exitFailed, err
		}
		return res.exitCode, nil
	}
}

// imageFile is an image to publish: a file on disk or a zip entry.
type imageFile struct {
	name string
	path string
	zf   *zip.File
	size int64
}

func (f imageFile) read() ([]byte, error) {
	if f.zf != nil {
		rc, err := f.zf.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 4<<30))
	}
	return os.ReadFile(f.path)
}

// pushInput is what one push sends.
type pushInput struct {
	csv, assets *ingest.File
	images      []imageFile
	closers     []io.Closer
	skipped     []string
}

func (in *pushInput) close() {
	for _, c := range in.closers {
		c.Close()
	}
}

// gather reads the CSV files and lists the images.
func gather(e *env, o pushOptions) (*pushInput, error) {
	in := &pushInput{}
	readFile := func(path string) (*ingest.File, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return &ingest.File{Name: filepath.Base(path), Data: b}, nil
	}
	var err error
	if o.folder != "" {
		if st, err := os.Stat(o.folder); err != nil {
			return nil, err
		} else if !st.IsDir() {
			return nil, userErr(e.t("push.not_a_folder", "path", o.folder))
		}
		if err := in.addFolder(o.folder, true); err != nil {
			return nil, err
		}
	}
	if o.pkg != "" {
		if err := in.addZip(e, o.pkg, true); err != nil {
			return nil, err
		}
	}
	if o.csv != "" {
		if in.csv, err = readFile(o.csv); err != nil {
			return nil, err
		}
	}
	if o.assetsCSV != "" {
		if in.assets, err = readFile(o.assetsCSV); err != nil {
			return nil, err
		}
	}
	if o.images != "" {
		st, err := os.Stat(o.images)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			err = in.addFolder(o.images, false)
		} else {
			err = in.addZip(e, o.images, false)
		}
		if err != nil {
			return nil, err
		}
	}
	// One image per file name: the name decides style, colour and position.
	seen := map[string]bool{}
	var unique []imageFile
	for _, img := range in.images {
		key := strings.ToLower(img.name)
		if seen[key] {
			in.skipped = append(in.skipped, e.t("push.twice", "name", img.name, "first", firstPath(in.images, key)))
			continue
		}
		seen[key] = true
		unique = append(unique, img)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i].name < unique[j].name })
	in.images = unique
	if in.csv == nil && in.assets == nil && len(in.images) == 0 {
		return nil, userErr(e.t("push.found_nothing"))
	}
	return in, nil
}

func firstPath(images []imageFile, key string) string {
	for _, img := range images {
		if strings.ToLower(img.name) == key {
			if img.path != "" {
				return img.path
			}
			return img.zf.Name
		}
	}
	return ""
}

// addFolder adds a folder laid out like a package: products.csv (or the only
// CSV), optional assets.csv and images anywhere below it.
func (in *pushInput) addFolder(dir string, withCSV bool) error {
	var csvs []string
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
		name := d.Name()
		lower := strings.ToLower(name)
		switch {
		case withCSV && filepath.Dir(p) == filepath.Clean(dir) && strings.HasSuffix(lower, ".csv"):
			csvs = append(csvs, p)
		case canonicalcsv.IsImageFile(name):
			info, err := d.Info()
			if err != nil {
				return err
			}
			in.images = append(in.images, imageFile{name: name, path: p, size: info.Size()})
		}
		return nil
	})
	if err != nil {
		return err
	}
	return in.pickCSVs(csvs, func(p string) ([]byte, error) { return os.ReadFile(p) })
}

func (in *pushInput) pickCSVs(paths []string, read func(string) ([]byte, error)) error {
	var products, assets string
	var others []string
	for _, p := range paths {
		switch strings.ToLower(filepath.Base(p)) {
		case "products.csv":
			products = p
		case "assets.csv":
			assets = p
		default:
			others = append(others, p)
		}
	}
	if products == "" && len(others) == 1 {
		products = others[0]
	}
	if products != "" && in.csv == nil {
		b, err := read(products)
		if err != nil {
			return err
		}
		in.csv = &ingest.File{Name: filepath.Base(products), Data: b}
	}
	if assets != "" && in.assets == nil {
		b, err := read(assets)
		if err != nil {
			return err
		}
		in.assets = &ingest.File{Name: "assets.csv", Data: b}
	}
	return nil
}

// addZip adds a package zip or an images zip.
func (in *pushInput) addZip(e *env, path string, withCSV bool) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return userErr(e.t("push.not_a_zip", "path", path))
	}
	in.closers = append(in.closers, zr)
	var csvs []string
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if f.FileInfo().IsDir() || canonicalcsv.IgnoredPath(name) {
			continue
		}
		base := filepath.Base(name)
		switch {
		case withCSV && !strings.Contains(strings.Trim(name, "/"), "/") && strings.HasSuffix(strings.ToLower(base), ".csv"):
			csvs = append(csvs, name)
			files[name] = f
		case canonicalcsv.IsImageFile(base):
			in.images = append(in.images, imageFile{name: base, zf: f, size: int64(f.UncompressedSize64)})
		}
	}
	return in.pickCSVs(csvs, func(p string) ([]byte, error) {
		rc, err := files[p].Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, canonicalcsv.MaxFileBytes))
	})
}

// pushResult is the outcome of one push.
type pushResult struct {
	Job         *obx.IngestJob       `json:"job"`
	DryRun      bool                 `json:"dry_run"`
	Problems    []obx.ImportRowError `json:"problems"`
	Images      int                  `json:"images"`
	Uploaded    int                  `json:"uploaded_files"`
	WouldUpload int                  `json:"would_upload_images,omitempty"`
	LocalNotes  []string             `json:"local_problems,omitempty"`
	exitCode    int
}

// push publishes once: it checks and uploads images, starts the import and
// waits for it. With dryRun it only hashes the images and asks the portal
// which it already has: nothing is uploaded and nothing changes.
func push(ctx context.Context, o pushOptions, e *env) (*pushResult, error) {
	base, token, err := e.target(o.url, o.token)
	if err != nil {
		return nil, err
	}
	ic, err := e.ingestClient(base, token)
	if err != nil {
		return nil, err
	}
	info, err := ic.Info(ctx)
	if err != nil {
		return nil, err
	}
	in, err := gather(e, o)
	if err != nil {
		return nil, err
	}
	defer in.close()
	out := &pushResult{Images: len(in.images), LocalNotes: in.skipped, DryRun: o.dryRun}
	say := e.stderr
	if o.asJSON {
		say = io.Discard
	}
	names := make([]string, 0, len(info.Brands))
	for _, b := range info.Brands {
		names = append(names, b.Name)
	}
	fmt.Fprintln(say, e.t("push.publishing", "portal", base, "brands", strings.Join(names, ", ")))
	if o.dryRun {
		fmt.Fprintln(say, "  "+e.t("push.dry_run_note"))
	}
	if in.csv != nil {
		files := in.csv.Name
		if in.assets != nil {
			files += " + assets.csv"
		}
		fmt.Fprintln(say, "  "+files)
	}

	// Images: hash, ask the portal, upload what is new.
	var bytesUp, uploaded, wouldUpload atomic.Int64
	missing := map[string]bool{} // dry run: image names the portal does not have yet
	cache := &ingest.FileRenditionCache{Path: filepath.Join(e.cacheDir, "renditions.json")}
	defer func() { _ = cache.Flush() }() // best effort: a lost cache only means renditions are made again
	prep := &ingest.Preparer{Client: ic, Cache: cache,
		Uploads: &fileUploadState{path: filepath.Join(e.cacheDir, "uploads.json")}, OnBytes: func(n int64) { bytesUp.Add(n) }}
	manifest := make([]obx.ManifestImage, len(in.images))
	ok := make([]bool, len(in.images))
	var done atomic.Int64
	prog := newBar(e, say, e.isTTY && !o.asJSON, e.t("push.images_label"), int64(len(in.images)))
	extra := func() string {
		if o.dryRun {
			return e.t("push.would_upload_extra", "count", wouldUpload.Load())
		}
		return e.t("push.uploaded_extra", "size", humanBytes(bytesUp.Load()))
	}
	var mu sync.Mutex
	jobs := make(chan int)
	workers := max(1, min(o.workers, 16))
	var wg sync.WaitGroup
	start := time.Now()
	stopTicker := make(chan struct{})
	if e.isTTY && !o.asJSON && len(in.images) > 0 {
		// Keep the bar moving during one long upload.
		go func() {
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stopTicker:
					return
				case <-t.C:
					prog.set(done.Load(), extra())
				}
			}
		}()
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				img := in.images[i]
				data, err := img.read()
				if err == nil {
					if o.dryRun {
						var m obx.ManifestImage
						var has bool
						if m, has, err = checkImage(ctx, ic, info, img.name, data); err == nil {
							manifest[i], ok[i] = m, true
							if !has {
								wouldUpload.Add(1)
								mu.Lock()
								missing[strings.ToLower(img.name)] = true
								mu.Unlock()
							}
						}
					} else {
						var p *ingest.PreparedImage
						if p, err = prep.Prepare(ctx, ingest.ImageSource{Name: img.name, Data: data}); err == nil {
							manifest[i], ok[i] = p.Manifest, true
							if p.Uploaded > 0 {
								uploaded.Add(1)
							}
						}
					}
				}
				if err != nil && ctx.Err() == nil {
					mu.Lock()
					out.LocalNotes = append(out.LocalNotes, img.name+": "+friendly(e, err))
					mu.Unlock()
				}
				prog.set(done.Add(1), extra())
			}
		}()
	}
feed:
	for i := range in.images {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(stopTicker)
	if err := ctx.Err(); err != nil {
		prog.clear()
		return nil, err
	}
	took := time.Since(start).Round(100 * time.Millisecond).String()
	if len(in.images) > 0 {
		if o.dryRun {
			prog.done("  " + e.t("push.images_checked_dry", "count", len(in.images), "new", wouldUpload.Load()))
		} else {
			prog.done("  " + e.t("push.images_done", "count", len(in.images), "uploaded", uploaded.Load(), "size", humanBytes(bytesUp.Load()), "time", took))
		}
	}
	out.Uploaded = int(uploaded.Load())
	out.WouldUpload = int(wouldUpload.Load())
	req := ingest.ImportRequest{CSV: in.csv, AssetsCSV: in.assets, Manifest: []obx.ManifestImage{},
		Options: obx.ImportOptions{Mode: obx.ImportModeUpsert, AllowMassUnpublish: o.allowMass, DryRun: o.dryRun, DefaultLanguage: o.language}}
	if o.mirror {
		req.Options.Mode = obx.ImportModeMirror
	}
	for i := range manifest {
		if ok[i] {
			req.Manifest = append(req.Manifest, manifest[i])
		}
	}
	job, err := ic.Import(ctx, req)
	if err != nil {
		return nil, err
	}
	out.Job = job
	if o.noWait {
		fmt.Fprintln(say, e.t("push.started", "job", job.ID))
		out.exitCode = exitOK
		return out, printJSON(o, e, out)
	}
	if job, err = waitJob(ctx, e, ic, job.ID, say, e.isTTY && !o.asJSON); err != nil {
		return nil, err
	}
	out.Job = job
	problems, err := ic.ImportProblems(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	for _, p := range problems {
		// A dry run does not upload, so the portal cannot know the new images
		// yet; a real run uploads them first.
		if o.dryRun && (p.ObxError == obx.ErrBlobMissing || p.ObxError == obx.ErrFilesMissing) &&
			(missing[strings.ToLower(filepath.Base(p.File))] || p.Value != nil && missing[strings.ToLower(filepath.Base(*p.Value))]) {
			continue
		}
		out.Problems = append(out.Problems, p)
	}
	out.exitCode = exitOK
	errorsN := len(out.LocalNotes)
	for _, p := range out.Problems {
		if p.Severity == obx.SeverityError {
			errorsN++
		}
	}
	if job.Status == obx.JobFailed {
		out.exitCode = exitFailed
	} else if errorsN > 0 {
		out.exitCode = exitProblems
	}
	if o.asJSON {
		return out, printJSON(o, e, out)
	}
	printSummary(e, e.stdout, out, o)
	return out, nil
}

// checkImage is the dry-run part of Preparer.Prepare: hash the image, read
// its header and ask the portal whether it has it. Nothing is uploaded.
func checkImage(ctx context.Context, ic *ingest.Client, info *obx.IngestInfo, name string, data []byte) (obx.ManifestImage, bool, error) {
	meta, err := renditions.ReadInfo(data)
	if err != nil {
		return obx.ManifestImage{}, false, err
	}
	if len(info.AcceptedContentTypes) > 0 && !obx.Contains(info.AcceptedContentTypes, meta.ContentType) {
		return obx.ManifestImage{}, false, obx.Problemf(obx.ErrUnsupportedMediaType, "%s", meta.ContentType)
	}
	sha := ingest.HashBytes(data)
	has, err := ic.HasBlob(ctx, sha)
	if err != nil {
		return obx.ManifestImage{}, false, err
	}
	return obx.ManifestImage{File: filepath.Base(name), SHA256: sha, Width: meta.Width, Height: meta.Height}, has, nil
}

// waitJob waits for an import job and shows its progress.
func waitJob(ctx context.Context, e *env, ic *ingest.Client, id string, w io.Writer, tty bool) (*obx.IngestJob, error) {
	prog := newBar(e, w, tty, e.t("push.processing_label"), 0)
	every := 300 * time.Millisecond
	for {
		j, err := ic.Job(ctx, id)
		if err != nil {
			prog.clear()
			return nil, err
		}
		if j.Done() {
			prog.clear()
			return j, nil
		}
		if j.Progress.Total != nil {
			prog.total = int64(*j.Progress.Total)
		}
		prog.set(int64(j.Progress.Done), "")
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			prog.clear()
			return nil, ctx.Err()
		case <-t.C:
		}
		if every < 5*time.Second {
			every += every / 2
		}
	}
}

func printJSON(o pushOptions, e *env, out *pushResult) error {
	if !o.asJSON {
		return nil
	}
	if out.Problems == nil {
		out.Problems = []obx.ImportRowError{}
	}
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// countsText is "new: 3 · updated: 2 · unchanged: 35".
func countsText(e *env, c obx.ImportCounts) string {
	parts := []string{e.t("count.new", "n", c.Created), e.t("count.updated", "n", c.Updated), e.t("count.unchanged", "n", c.Unchanged)}
	if c.Unpublished > 0 {
		parts = append(parts, e.t("count.offline", "n", c.Unpublished))
	}
	return strings.Join(parts, " · ")
}

func printSummary(e *env, w io.Writer, out *pushResult, o pushOptions) {
	run := out.Job.ImportRun
	if out.Job.Status == obx.JobFailed {
		msg := e.t("push.failed_generic")
		if run != nil && run.Error != nil {
			msg = friendly(e, run.Error)
		}
		fmt.Fprintln(w, e.t("push.failed", "reason", msg))
	} else if run != nil {
		c := run.Counts
		head := e.t("push.done_head")
		if o.dryRun {
			head = e.t("push.dry_run_head")
		}
		fmt.Fprintf(w, "%s %s → %s\n", head, e.t("push.rows_images", "rows", c.Rows, "images", c.Files), countsText(e, c))
		if o.dryRun && out.WouldUpload > 0 {
			fmt.Fprintln(w, e.t("push.dry_run_uploads", "count", out.WouldUpload))
		}
	}
	var errs, warns []string
	errs = append(errs, out.LocalNotes...)
	for _, p := range out.Problems {
		line := problemText(e, p)
		if p.Severity == obx.SeverityError {
			errs = append(errs, line)
		} else {
			warns = append(warns, line)
		}
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(w, "%s (%d):\n", title, len(items))
		for i, it := range items {
			if i == 50 {
				fmt.Fprintln(w, "  "+e.t("push.and_more", "count", len(items)-50, "job", out.Job.ID))
				break
			}
			fmt.Fprintf(w, "  - %s\n", it)
		}
	}
	list(e.t("push.problems_title"), errs)
	list(e.t("push.warnings_title"), warns)
}

// fileUploadState remembers open multipart uploads between runs, so an
// interrupted upload continues with the parts that are still missing.
type fileUploadState struct {
	path string
	mu   sync.Mutex
}

func (f *fileUploadState) load() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(f.path); err == nil && json.Unmarshal(b, &m) != nil {
		m = map[string]string{} // damaged: uploads start from the first part again
	}
	return m
}

// save is best effort: without the file, an interrupted upload simply starts
// again from the first part on the next run.
func (f *fileUploadState) save(m map[string]string) {
	_ = os.MkdirAll(filepath.Dir(f.path), 0o700)
	b, _ := json.Marshal(m)
	tmp := f.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, f.path)
	}
}

func (f *fileUploadState) Get(sha string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.load()[sha]
}

func (f *fileUploadState) Set(sha, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.load()
	m[sha] = id
	f.save(m)
}

func (f *fileUploadState) Delete(sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.load()
	delete(m, sha)
	f.save(m)
}

var _ = register(map[string]string{
	"cmd.push.about": "Publishes product data and images to your portal. Give a folder with products.csv (one row per size and colour, with the EAN), an optional assets.csv and images named {style}_{colour}_{position}.jpg (for example KO-1001_NVY_1.jpg; {style}_ALL_{position}.jpg for an image of every colour), or the files separately, or one zip.\n" +
		"obx checks every image, uploads only what is new or changed (large images in parts that continue where they stopped), makes the web and thumbnail versions when your portal needs that, and prints a summary with the rows that need attention. Running it again costs nothing when nothing changed. With --dry-run nothing is uploaded or changed: you see what would happen.",
	"push.flag_csv":           "products.csv: one row per size and colour, with the EAN",
	"push.flag_assets":        "optional assets.csv with rights, AI information or alt text per image",
	"push.flag_images":        "folder or zip with images named {style}_{colour}_{position}.jpg",
	"push.flag_package":       "one zip with products.csv, an optional assets.csv and images",
	"push.flag_mirror":        "also take offline the products of these brands that the file no longer contains",
	"push.flag_mass":          "with --mirror: allow taking more than half of the products offline",
	"push.flag_dry_run":       "only check and show what would happen; upload and change nothing",
	"push.flag_language":      "language of columns without a language (name, description, color_name); default: the portal's first language",
	"push.flag_no_wait":       "do not wait until the portal has processed the upload",
	"push.flag_parallel":      "how many images to check and upload at the same time",
	"push.one_folder":         "give at most one folder (got: {folders}).",
	"push.nothing_given":      "nothing to publish: give a folder, --csv, --images or --package.",
	"push.not_a_folder":       "{path} is not a folder. Use --csv for a CSV file or --package for a zip.",
	"push.not_a_zip":          "{path} is not a zip file.",
	"push.twice":              "{name} appears twice; only the first one is used ({first})",
	"push.found_nothing":      "found no products.csv and no images.",
	"push.publishing":         "Publishing to {portal} ({brands})",
	"push.dry_run_note":       "dry run: nothing is uploaded or changed",
	"push.images_label":       "images",
	"push.processing_label":   "the portal is processing",
	"push.uploaded_extra":     "{size} uploaded",
	"push.would_upload_extra": "new or changed: {count}",
	"push.images_checked_dry": "images: {count} checked, new or changed (would be uploaded): {new}",
	"push.images_done":        "images: {count} checked, {uploaded} uploaded ({size}) in {time}",
	"push.started":            "Upload {job} started; see \"obx status --job {job}\".",
	"push.failed_generic":     "the upload failed",
	"push.failed":             "Failed: {reason}",
	"push.done_head":          "Done:",
	"push.dry_run_head":       "Checked (dry run, nothing changed):",
	"push.rows_images":        "rows: {rows}, images: {images}",
	"push.dry_run_uploads":    "A real run would upload new or changed images: {count}.",
	"push.and_more":           "… and {count} more (\"obx status --job {job}\" shows all)",
	"push.problems_title":     "Problems, these rows or images were skipped",
	"push.warnings_title":     "Warnings",
	"count.new":               "new: {n}",
	"count.updated":           "updated: {n}",
	"count.unchanged":         "unchanged: {n}",
	"count.offline":           "taken offline: {n}",
	"count.problems":          "problems: {n}",
	"count.warnings":          "warnings: {n}",
})
