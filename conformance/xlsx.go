package conformance

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/obx"
)

// The Excel import (import-format.md §5) is an optional node feature: nodes
// that answer a workbook with unsupported_media_type skip both tests.
func init() {
	p := ProfileIngest
	register(p, "I-IMP-9", "Excel import (optional): each converting workbook of fixtures/imports/xlsx as a dry run gives exactly the expected errors with Excel row numbers and locators; the templates import all unchanged", false, testXlsxImport)
	register(p, "I-IMP-10", "Excel refusals (optional): each workbook to refuse fails the run with one ImportRowError (row null) of the expected code and writes nothing", false, testXlsxRefusals)
}

type xlsxError struct {
	File         string  `json:"file"`
	Row          *int    `json:"row"`
	Column       *string `json:"column"`
	Severity     string  `json:"severity"`
	ObxError     string  `json:"obx_error"`
	DetailPrefix string  `json:"detail_prefix"`
	Value        *string `json:"value"`
}

type xlsxCase struct {
	File  string `json:"file"`
	Role  string `json:"role"`
	Sheet string `json:"sheet"`
	What  string `json:"what"`
	Send  *struct {
		ImagesManifest []string `json:"images_manifest"`
	} `json:"send"`
	Errors       []xlsxError `json:"errors"`
	Warnings     []xlsxError `json:"warnings"`
	ImportErrors []xlsxError `json:"import_errors"`
	Error        *xlsxError  `json:"error"`
}

func (t *T) xlsxCases() []xlsxCase {
	raw, err := t.r.fixture("imports/xlsx/expected.json")
	t.Check(err, "read fixtures/imports/xlsx/expected.json")
	var exp struct {
		Cases []xlsxCase `json:"cases"`
	}
	t.Check(json.Unmarshal(raw, &exp), "parse fixtures/imports/xlsx/expected.json")
	return exp.Cases
}

// xlsxRequest is the import of one workbook as products.xlsx or assets.xlsx,
// with the images_manifest the case asks for.
func (t *T) xlsxRequest(c xlsxCase, dryRun bool) *ingest.ImportRequest {
	data, err := t.r.fixture("imports/xlsx/" + c.File)
	t.Check(err, "read fixtures/imports/xlsx/"+c.File)
	req := &ingest.ImportRequest{Options: obx.ImportOptions{Mode: obx.ImportModeUpsert, DryRun: dryRun}}
	f := &ingest.File{Name: c.Role + ".xlsx", Data: data}
	if c.Role == "assets" {
		req.AssetsCSV = f
	} else {
		req.CSV = f
	}
	if c.Send != nil && len(c.Send.ImagesManifest) > 0 {
		prep := &ingest.Preparer{Client: t.r.ing}
		req.Manifest = []obx.ManifestImage{}
		for _, name := range c.Send.ImagesManifest {
			b, err := t.r.fixture(path.Join("kestrel/images", name))
			t.Check(err, "read fixtures/kestrel/images/"+name)
			p, err := prep.Prepare(t.ctx, ingest.ImageSource{Name: name, Data: b})
			t.Check(err, "upload "+name)
			req.Manifest = append(req.Manifest, p.Manifest)
		}
	}
	return req
}

// sendWorkbook starts the import and waits for it. It skips the test when the
// node does not import workbooks; a problem answered at once is returned as
// refused (a node MAY check an upload while it is sent, §5.5).
func (t *T) sendWorkbook(name string, req *ingest.ImportRequest) (job *obx.IngestJob, probs []obx.ImportRowError, refused *obx.Problem) {
	j, err := t.r.ing.Import(t.ctx, *req)
	if err != nil {
		if obx.IsCode(err, obx.ErrUnsupportedMediaType) {
			t.Skip("the node does not import Excel workbooks (%s: unsupported_media_type)", name)
		}
		var p *obx.Problem
		if pp, ok := err.(*obx.Problem); ok {
			p = pp
		}
		if p == nil {
			t.Fatalf("POST /imports with %s: %v", name, err)
		}
		return nil, nil, p
	}
	j, err = t.r.ing.WaitJob(t.ctx, j.ID, 200*time.Millisecond)
	t.Check(err, "wait for the import job of "+name)
	probs, err = t.r.ing.ImportProblems(t.ctx, j.ID)
	t.Check(err, "results of the import job of "+name)
	if (j.ImportRun != nil && j.ImportRun.Error != nil && j.ImportRun.Error.ObxError == obx.ErrUnsupportedMediaType) || hasCode(probs, obx.ErrUnsupportedMediaType) {
		t.Skip("the node does not import Excel workbooks (%s: unsupported_media_type)", name)
	}
	return j, probs, nil
}

func xlsxMatches(g obx.ImportRowError, w xlsxError) bool {
	return g.File == w.File && sameRow(g.Row, w.Row) && obx.Str(g.Column) == obx.Str(w.Column) && g.Severity == w.Severity &&
		g.ObxError == w.ObxError && (w.Value == nil || obx.Str(g.Value) == *w.Value) && strings.HasPrefix(g.Detail, w.DetailPrefix)
}

func describeXlsx(w xlsxError) string {
	row := "null"
	if w.Row != nil {
		row = fmt.Sprint(*w.Row)
	}
	return fmt.Sprintf("%s row %s column %q %s %s %q value %q", w.File, row, obx.Str(w.Column), w.Severity, w.ObxError, w.DetailPrefix, obx.Str(w.Value))
}

// testXlsxImport is I-IMP-9.
func testXlsxImport(t *T) {
	for _, c := range t.xlsxCases() {
		if c.Error != nil {
			continue
		}
		var cursor string
		if t.r.full != nil {
			cursor = t.liveCursor(t.r.full)
		}
		job, probs, refused := t.sendWorkbook(c.File, t.xlsxRequest(c, true))
		if refused != nil {
			t.Errorf("%s: refused with %s (%s)", c.File, refused.ObxError, obx.Str(refused.Detail))
			continue
		}
		if job.Status != obx.JobSucceeded {
			t.Errorf("%s: dry run %s (%s)", c.File, job.Status, short(probs))
			continue
		}
		// Errors: exactly import_errors, in any order.
		errs := errorsOf(probs)
		used := make([]bool, len(errs))
		for _, w := range c.ImportErrors {
			found := false
			for i, g := range errs {
				if !used[i] && xlsxMatches(g, w) {
					used[i], found = true, true
					break
				}
			}
			if !found {
				t.Errorf("%s: missing error %s", c.File, describeXlsx(w))
			}
		}
		for i, g := range errs {
			if !used[i] {
				t.Errorf("%s: unexpected error %s", c.File, short(g))
			}
		}
		// Warnings: at least the conversion warnings.
		for _, w := range c.Warnings {
			found := false
			for _, g := range probs {
				if xlsxMatches(g, w) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: missing warning %s", c.File, describeXlsx(w))
			}
		}
		if t.r.full != nil {
			if live, _ := t.newEntries(t.r.full, cursor); len(live) > 0 {
				t.Errorf("%s: the dry run wrote change entries: %v", c.File, changeKeys(live))
			}
		}
	}
	// The templates as they are import all unchanged on a node that holds the fixtures.
	for _, c := range t.xlsxCases() {
		if strings.HasSuffix(c.File, "-template.xlsx") {
			t.expectUnchangedImport(c.File, t.xlsxRequest(c, false))
		}
	}
}

// testXlsxRefusals is I-IMP-10.
func testXlsxRefusals(t *T) {
	for _, c := range t.xlsxCases() {
		if c.Error == nil {
			continue
		}
		var cursor string
		if t.r.full != nil {
			cursor = t.liveCursor(t.r.full)
		}
		job, probs, refused := t.sendWorkbook(c.File, t.xlsxRequest(c, false))
		want := c.Error.ObxError
		switch {
		case refused != nil:
			if refused.ObxError != want {
				t.Errorf("%s: answered %s (%s), want %s", c.File, refused.ObxError, obx.Str(refused.Detail), want)
			}
		default:
			if job.Status != obx.JobFailed {
				t.Errorf("%s: run %s, want failed with %s (%s)", c.File, job.Status, want, short(probs))
			}
			if job.ImportRun == nil || job.ImportRun.Error == nil || job.ImportRun.Error.ObxError != want {
				var got any
				if job.ImportRun != nil {
					got = job.ImportRun.Error
				}
				t.Errorf("%s: ImportRun.error %s, want %s", c.File, short(got), want)
			}
			errs := errorsOf(probs)
			if len(errs) != 1 || errs[0].Row != nil || errs[0].Column != nil || errs[0].ObxError != want || errs[0].File != c.Error.File {
				t.Errorf("%s: want exactly one error row (%s, row and column null, %s), got %s", c.File, c.Error.File, want, short(errs))
			}
			if job.ImportRun != nil && (job.ImportRun.Counts.Created > 0 || job.ImportRun.Counts.Updated > 0) {
				t.Errorf("%s: a refused workbook wrote data (%s)", c.File, short(job.ImportRun.Counts))
			}
		}
		if t.r.full != nil {
			if live, _ := t.newEntries(t.r.full, cursor); len(live) > 0 {
				t.Errorf("%s: a refused workbook wrote change entries: %v", c.File, changeKeys(live))
			}
		}
		// The refusal fixtures hold products KX-9901 to KX-9907; none may exist.
		for i := 1; i <= 7; i++ {
			ext := fmt.Sprintf("KX-990%d", i)
			if _, err := t.r.ing.Get(t.ctx, obx.KindProduct, ext); err == nil {
				t.Errorf("%s: product %s was written", c.File, ext)
				_, _ = t.r.ing.Delete(t.ctx, obx.KindProduct, ext)
			}
		}
	}
}
