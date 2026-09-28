package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
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

func cmdPush(ctx context.Context, args []string, e *env) (int, error) {
	fs := newFlags("push", "push [<folder>] [--csv products.csv] [--assets-csv assets.csv] [--images <folder or zip>] [--package export.zip] [options]", e)
	var o pushOptions
	fs.StringVar(&o.url, "url", "", "portal address (default: OBX_URL or the one from obx login)")
	fs.StringVar(&o.token, "token", "", "upload key (default: OBX_TOKEN or the stored key)")
	fs.StringVar(&o.csv, "csv", "", "products.csv: one row per size/colour (EAN)")
	fs.StringVar(&o.assetsCSV, "assets-csv", "", "optional assets.csv with rights, AI information or alt text per image")
	fs.StringVar(&o.images, "images", "", "folder or zip with images named {style}_{colour}_{position}.jpg")
	fs.StringVar(&o.pkg, "package", "", "one zip with products.csv, optional assets.csv and images")
	fs.BoolVar(&o.mirror, "mirror", false, "also take offline the products of these brands that are not in the file")
	fs.BoolVar(&o.allowMass, "allow-mass-unpublish", false, "with --mirror: allow taking more than half of the products offline")
	fs.BoolVar(&o.dryRun, "dry-run", false, "only check; change nothing")
	fs.StringVar(&o.language, "language", "", "language of columns without a language (name, description, color_name); default: the portal's first language")
	fs.BoolVar(&o.noWait, "no-wait", false, "do not wait for the portal to finish")
	fs.BoolVar(&o.asJSON, "json", false, "print the result as JSON")
	fs.IntVar(&o.workers, "parallel", 4, "images checked/uploaded at the same time")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitFailed, err
	}
	if len(pos) > 1 {
		return exitFailed, fmt.Errorf("give at most one folder (got %s)", strings.Join(pos, ", "))
	}
	if len(pos) == 1 {
		o.folder = pos[0]
	}
	if o.folder == "" && o.csv == "" && o.images == "" && o.pkg == "" && o.assetsCSV == "" {
		fs.Usage()
		return exitFailed, errors.New("nothing to publish: give a folder, --csv, --images or --package")
	}
	res, err := push(ctx, o, e)
	if err != nil {
		return exitFailed, err
	}
	return res.exitCode, nil
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
func gather(o pushOptions) (*pushInput, error) {
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
		if err := in.addFolder(o.folder, true); err != nil {
			return nil, err
		}
	}
	if o.pkg != "" {
		if err := in.addZip(o.pkg, true); err != nil {
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
			err = in.addZip(o.images, false)
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
			in.skipped = append(in.skipped, fmt.Sprintf("%s appears twice; only the first one (%s) is used", img.name, firstPath(in.images, key)))
			continue
		}
		seen[key] = true
		unique = append(unique, img)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i].name < unique[j].name })
	in.images = unique
	if in.csv == nil && in.assets == nil && len(in.images) == 0 {
		return nil, errors.New("found no products.csv and no images")
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
func (in *pushInput) addZip(path string, withCSV bool) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%s is not a zip file: %w", path, err)
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
	Job        *obx.IngestJob       `json:"job"`
	Problems   []obx.ImportRowError `json:"problems"`
	Images     int                  `json:"images"`
	Uploaded   int                  `json:"uploaded_files"`
	LocalNotes []string             `json:"local_problems,omitempty"`
	exitCode   int
}

// push publishes once: it checks and uploads images, starts the import and
// waits for it.
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
	in, err := gather(o)
	if err != nil {
		return nil, err
	}
	defer in.close()
	out := &pushResult{Images: len(in.images), LocalNotes: in.skipped}
	say := e.stderr
	if o.asJSON {
		say = io.Discard
	}
	names := make([]string, 0, len(info.Brands))
	for _, b := range info.Brands {
		names = append(names, b.Name)
	}
	fmt.Fprintf(say, "Publishing to %s (%s)\n", base, strings.Join(names, ", "))
	if in.csv != nil {
		fmt.Fprintf(say, "  %s", in.csv.Name)
		if in.assets != nil {
			fmt.Fprint(say, " + assets.csv")
		}
		fmt.Fprintln(say)
	}

	// Images: hash, ask the portal, upload what is new.
	prep := &ingest.Preparer{Client: ic, Cache: &ingest.FileRenditionCache{Path: filepath.Join(e.cacheDir, "renditions.json")}, Uploads: &fileUploadState{path: filepath.Join(e.cacheDir, "uploads.json")}}
	manifest := make([]obx.ManifestImage, len(in.images))
	ok := make([]bool, len(in.images))
	var done, uploaded atomic.Int64
	var bytesUp atomic.Int64
	prog := &progress{w: say, tty: e.isTTY && !o.asJSON}
	var mu sync.Mutex
	jobs := make(chan int)
	workers := max(1, min(o.workers, 16))
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				img := in.images[i]
				data, err := img.read()
				if err == nil {
					var p *ingest.PreparedImage
					p, err = prep.Prepare(ctx, ingest.ImageSource{Name: img.name, Data: data})
					if err == nil {
						manifest[i], ok[i] = p.Manifest, true
						if p.Uploaded > 0 {
							uploaded.Add(1)
							bytesUp.Add(p.Bytes)
						}
					}
				}
				if err != nil {
					mu.Lock()
					out.LocalNotes = append(out.LocalNotes, fmt.Sprintf("%s: %s", img.name, friendly(err)))
					mu.Unlock()
				}
				n := done.Add(1)
				prog.set("  images: %d/%d checked, %d new or changed uploaded (%s)", n, len(in.images), uploaded.Load(), humanBytes(bytesUp.Load()))
			}
		}()
	}
	for i := range in.images {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(in.images) > 0 {
		prog.done("  images: %d checked, %d uploaded (%s) in %s", len(in.images), uploaded.Load(), humanBytes(bytesUp.Load()), time.Since(start).Round(100*time.Millisecond))
	}
	out.Uploaded = int(uploaded.Load())
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
		fmt.Fprintf(say, "Started upload %s; see \"obx status --job %s\".\n", job.ID, job.ID)
		out.exitCode = exitOK
		return out, printJSON(o, e, out)
	}
	prog.set("  the portal is processing…")
	if job, err = ic.WaitJob(ctx, job.ID, 500*time.Millisecond); err != nil {
		return nil, err
	}
	out.Job = job
	if out.Problems, err = ic.ImportProblems(ctx, job.ID); err != nil {
		return nil, err
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
	printSummary(e.stdout, out, o)
	return out, nil
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

func printSummary(w io.Writer, out *pushResult, o pushOptions) {
	run := out.Job.ImportRun
	prefix := "Done"
	if o.dryRun {
		prefix = "Checked (dry run, nothing changed)"
	}
	if out.Job.Status == obx.JobFailed {
		msg := "the upload failed"
		if run != nil && run.Error != nil {
			msg = friendly(run.Error)
		}
		fmt.Fprintf(w, "Failed: %s\n", msg)
	} else if run != nil {
		c := run.Counts
		parts := []string{fmt.Sprintf("%d new", c.Created), fmt.Sprintf("%d updated", c.Updated), fmt.Sprintf("%d unchanged", c.Unchanged)}
		if c.Unpublished > 0 {
			parts = append(parts, fmt.Sprintf("%d taken offline", c.Unpublished))
		}
		fmt.Fprintf(w, "%s: %d rows, %d images → %s.\n", prefix, c.Rows, c.Files, strings.Join(parts, ", "))
	}
	var errs, warns []string
	for _, n := range out.LocalNotes {
		errs = append(errs, n)
	}
	for _, p := range out.Problems {
		line := describeProblem(p)
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
				fmt.Fprintf(w, "  … and %d more (obx status --job %s shows all)\n", len(items)-50, out.Job.ID)
				break
			}
			fmt.Fprintf(w, "  - %s\n", it)
		}
	}
	list("Problems, these rows or images were skipped", errs)
	list("Warnings", warns)
}

// describeProblem turns an ImportRowError into "products.csv row 12, column
// gtin: … (value)".
func describeProblem(p obx.ImportRowError) string {
	var b strings.Builder
	b.WriteString(p.File)
	if p.Row != nil {
		fmt.Fprintf(&b, " row %d", *p.Row)
	}
	if p.Column != nil && *p.Column != "" {
		fmt.Fprintf(&b, ", column %s", *p.Column)
	}
	b.WriteString(": ")
	b.WriteString(p.Detail)
	if p.Value != nil && *p.Value != "" && !strings.Contains(p.Detail, *p.Value) {
		fmt.Fprintf(&b, " (%q)", *p.Value)
	}
	return b.String()
}

// fileUploadState remembers open multipart uploads between runs.
type fileUploadState struct {
	path string
	mu   sync.Mutex
}

func (f *fileUploadState) load() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(f.path); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func (f *fileUploadState) save(m map[string]string) {
	os.MkdirAll(filepath.Dir(f.path), 0o700)
	b, _ := json.Marshal(m)
	os.WriteFile(f.path, b, 0o600)
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
