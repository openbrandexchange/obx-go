package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

func cmdStatus(ctx context.Context, args []string, e *env) error {
	fs := newFlags("status", "status [--job <id>] [--limit 10] [--json]", e)
	u := fs.String("url", "", "portal address (default: OBX_URL or the one from obx login)")
	tok := fs.String("token", "", "upload key (default: OBX_TOKEN or the stored key)")
	jobID := fs.String("job", "", "show one upload with all its problems")
	limit := fs.Int("limit", 10, "how many recent uploads to show")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	base, token, err := e.target(*u, *tok)
	if err != nil {
		return err
	}
	ic, err := e.ingestClient(base, token)
	if err != nil {
		return err
	}
	if *jobID != "" {
		job, err := ic.Job(ctx, *jobID)
		if err != nil {
			return err
		}
		var problems []obx.ImportRowError
		var results []obx.IngestResult
		err = ic.EachJobResult(ctx, job.ID, false, func(raw json.RawMessage) error {
			if job.Kind == "import" {
				var p obx.ImportRowError
				json.Unmarshal(raw, &p)
				problems = append(problems, p)
			} else {
				var r obx.IngestResult
				json.Unmarshal(raw, &r)
				results = append(results, r)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(e.stdout).Encode(map[string]any{"job": job, "problems": problems, "results": results})
		}
		fmt.Fprintf(e.stdout, "Upload %s (%s): %s, started %s\n", job.ID, job.Kind, job.Status, job.CreatedAt.Local().Format("2006-01-02 15:04"))
		if job.ImportRun != nil {
			c := job.ImportRun.Counts
			fmt.Fprintf(e.stdout, "  %d rows, %d images: %d new, %d updated, %d unchanged, %d taken offline, %d problems, %d warnings\n",
				c.Rows, c.Files, c.Created, c.Updated, c.Unchanged, c.Unpublished, c.Errors, c.Warnings)
		}
		for _, p := range problems {
			fmt.Fprintf(e.stdout, "  - [%s] %s\n", p.Severity, describeProblem(p))
		}
		for _, r := range results {
			if r.Result == obx.ResultError && r.Error != nil {
				fmt.Fprintf(e.stdout, "  - %s %s: %s\n", r.Kind, r.ExternalID, friendly(r.Error))
			}
		}
		return nil
	}
	page, err := ic.Jobs(ctx, "", "", *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(e.stdout).Encode(page.Items)
	}
	if len(page.Items) == 0 {
		fmt.Fprintln(e.stdout, "No uploads yet with this key.")
		return nil
	}
	tw := tabwriter.NewWriter(e.stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tUPLOAD\tSTATUS\tRESULT")
	for _, j := range page.Items {
		result := ""
		if j.ImportRun != nil {
			c := j.ImportRun.Counts
			parts := []string{fmt.Sprintf("%d new", c.Created), fmt.Sprintf("%d updated", c.Updated), fmt.Sprintf("%d unchanged", c.Unchanged)}
			if c.Errors > 0 {
				parts = append(parts, fmt.Sprintf("%d problems", c.Errors))
			}
			result = strings.Join(parts, ", ")
		} else if j.Summary != nil {
			result = fmt.Sprintf("%d created, %d updated, %d unchanged, %d errors", j.Summary.Created, j.Summary.Updated, j.Summary.Unchanged, j.Summary.Errors)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ago(j.CreatedAt), j.ID, j.Status, result)
	}
	tw.Flush()
	fmt.Fprintln(e.stdout, "\nDetails and problems: obx status --job <upload>")
	return nil
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02 15:04")
}
