package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

func defineStatus(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	u := fs.String("url", "", e.t("flag.url"))
	tok := fs.String("token", "", e.t("flag.token"))
	jobID := fs.String("job", "", e.t("status.flag_job"))
	limit := fs.Int("limit", 10, e.t("status.flag_limit"))
	asJSON := fs.Bool("json", false, e.t("flag.json"))
	return func(ctx context.Context, pos []string) (int, error) {
		if len(pos) > 0 {
			return exitFailed, usageError{e.t("main.no_arguments")}
		}
		base, token, err := e.target(*u, *tok)
		if err != nil {
			return exitFailed, err
		}
		ic, err := e.ingestClient(base, token)
		if err != nil {
			return exitFailed, err
		}
		if *jobID != "" {
			return exitOK, showJob(ctx, e, ic, *jobID, *asJSON)
		}
		page, err := ic.Jobs(ctx, "", "", *limit)
		if err != nil {
			return exitFailed, err
		}
		if *asJSON {
			return exitOK, json.NewEncoder(e.stdout).Encode(page.Items)
		}
		if len(page.Items) == 0 {
			fmt.Fprintln(e.stdout, e.t("status.none"))
			return exitOK, nil
		}
		tw := tabwriter.NewWriter(e.stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", strings.ToUpper(e.t("status.col_when")), strings.ToUpper(e.t("status.col_upload")), strings.ToUpper(e.t("status.col_status")), strings.ToUpper(e.t("status.col_result")))
		for _, j := range page.Items {
			result := ""
			if j.ImportRun != nil {
				c := j.ImportRun.Counts
				result = countsText(e, c)
				if c.Errors > 0 {
					result += " · " + e.t("count.problems", "n", c.Errors)
				}
				// A --dry-run changed nothing; without the label it reads like a real upload.
				if j.ImportRun.DryRun {
					result += " (" + e.t("status.dry_run") + ")"
				}
			} else if j.Summary != nil {
				result = strings.Join([]string{e.t("count.new", "n", j.Summary.Created), e.t("count.updated", "n", j.Summary.Updated),
					e.t("count.unchanged", "n", j.Summary.Unchanged), e.t("count.problems", "n", j.Summary.Errors)}, " · ")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ago(e, j.CreatedAt), j.ID, jobStatus(e, j.Status), result)
		}
		tw.Flush()
		fmt.Fprintln(e.stdout)
		fmt.Fprintln(e.stdout, e.t("status.details_hint"))
		return exitOK, nil
	}
}

func showJob(ctx context.Context, e *env, ic interface {
	Job(context.Context, string) (*obx.IngestJob, error)
	EachJobResult(context.Context, string, bool, func(json.RawMessage) error) error
}, id string, asJSON bool) error {
	job, err := ic.Job(ctx, id)
	if err != nil {
		return err
	}
	var problems []obx.ImportRowError
	var results []obx.IngestResult
	err = ic.EachJobResult(ctx, job.ID, false, func(raw json.RawMessage) error {
		if job.Kind == "import" {
			var p obx.ImportRowError
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			problems = append(problems, p)
		} else {
			var r obx.IngestResult
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			results = append(results, r)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(e.stdout).Encode(map[string]any{"job": job, "problems": problems, "results": results})
	}
	w := e.stdout
	fmt.Fprintln(w, e.t("status.job_head", "job", job.ID, "status", jobStatus(e, job.Status), "time", job.CreatedAt.Local().Format("2006-01-02 15:04")))
	if job.ImportRun != nil {
		c := job.ImportRun.Counts
		fmt.Fprintf(w, "  %s → %s · %s · %s\n", e.t("push.rows_images", "rows", c.Rows, "images", c.Files), countsText(e, c),
			e.t("count.problems", "n", c.Errors), e.t("count.warnings", "n", c.Warnings))
		if job.ImportRun.Error != nil {
			fmt.Fprintln(w, "  "+e.t("push.failed", "reason", friendly(e, job.ImportRun.Error)))
		}
	}
	for _, p := range problems {
		sev := e.t("status.problem")
		if p.Severity != obx.SeverityError {
			sev = e.t("status.warning")
		}
		fmt.Fprintf(w, "  - [%s] %s\n", sev, problemText(e, p))
	}
	for _, r := range results {
		if r.Result == obx.ResultError && r.Error != nil {
			fmt.Fprintf(w, "  - %s %s: %s\n", r.Kind, r.ExternalID, friendly(e, r.Error))
		}
	}
	return nil
}

func jobStatus(e *env, s string) string {
	switch s {
	case obx.JobQueued, obx.JobRunning, obx.JobSucceeded, obx.JobFailed:
		return e.t("status.job_" + s)
	}
	return s
}

func ago(e *env, t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return e.t("status.just_now")
	case d < time.Hour:
		return e.t("status.minutes_ago", "n", int(d.Minutes()))
	case d < 24*time.Hour:
		return e.t("status.hours_ago", "n", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02 15:04")
}

var _ = register(map[string]string{
	"cmd.status.about":     "Shows the recent uploads made with this access key and how they went. With --job it lists every problem of one upload, with file, row and column.",
	"status.flag_job":      "show one upload with all its problems",
	"status.flag_limit":    "how many recent uploads to show",
	"status.none":          "No uploads with this access key yet.",
	"status.col_when":      "When",
	"status.col_upload":    "Upload",
	"status.col_status":    "Status",
	"status.col_result":    "Result",
	"status.details_hint":  "Details and problems: obx status --job <upload>",
	"status.dry_run":       "dry run",
	"status.job_head":      "Upload {job}: {status}, started {time}",
	"status.problem":       "problem",
	"status.warning":       "warning",
	"status.job_queued":    "waiting",
	"status.job_running":   "busy",
	"status.job_succeeded": "done",
	"status.job_failed":    "failed",
	"status.just_now":      "just now",
	"status.minutes_ago":   "{n} min ago",
	"status.hours_ago":     "{n} h ago",
})
