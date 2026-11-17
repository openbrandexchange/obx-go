package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/mock"
)

func noEnv(string) string { return "" }

func TestProtocolWithAPIKeysOnly(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	c := n.Credentials()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"protocol", "--url", srv.URL, "--client-id", c.Full.ClientID, "--client-secret", c.Full.ClientSecret,
		"--second-client-id", c.Restricted.ClientID, "--second-client-secret", c.Restricted.ClientSecret,
		"--no-original-client-id", c.NoOriginals.ClientID, "--no-original-client-secret", c.NoOriginals.ClientSecret, "--token", c.IngestToken, "--json"}, &out, &errb, noEnv)
	// Without admin credentials or an operator the brand-side tests are
	// skipped: no required test fails, but the run is INCOMPLETE (exit 2).
	if code != 2 {
		t.Fatalf("exit %d, want 2 (INCOMPLETE)\n%s\n%s", code, out.String(), errb.String())
	}
	var rep conformance.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 0 || rep.Passed < 20 || rep.Verdict != conformance.VerdictIncomplete {
		t.Fatalf("passed %d failed %d verdict %q", rep.Passed, rep.Failed, rep.Verdict)
	}
	// P-FEED-5 needs the ingest replay, P-FEED-7 an operator that offers
	// simulate_spec_upgrade (and then counts as optional).
	for _, r := range rep.Results {
		if r.Status != conformance.StatusSkip {
			continue
		}
		if !strings.Contains(strings.Join(r.Messages, " "), "admin") && r.ID != "P-FEED-5" && r.ID != "P-FEED-7" {
			t.Errorf("%s skipped for another reason than missing admin credentials: %v", r.ID, r.Messages)
		}
		if r.Needs == "" {
			t.Errorf("%s skipped without needs", r.ID)
		}
		if r.ID == "P-FEED-7" && r.Required {
			t.Errorf("P-FEED-7 without an operator that offers simulate_spec_upgrade must count as optional")
		}
	}
}

// The three verdicts and their exit codes (spec docs/conformance.md).
func TestVerdicts(t *testing.T) {
	res := func(status string, required bool) conformance.Result {
		return conformance.Result{ID: "X-1", Profile: conformance.ProfileProtocol, Status: status, Required: required, Needs: "an operator"}
	}
	cases := []struct {
		name    string
		rep     conformance.Report
		verdict string
		code    int
	}{
		{"all pass", conformance.Report{Results: []conformance.Result{res(conformance.StatusPass, true), res(conformance.StatusNA, true), res(conformance.StatusSkip, false)}}, conformance.VerdictConformant, 0},
		{"required skipped", conformance.Report{Results: []conformance.Result{res(conformance.StatusPass, true), res(conformance.StatusSkip, true)}}, conformance.VerdictIncomplete, 2},
		{"required failed", conformance.Report{Results: []conformance.Result{res(conformance.StatusFail, true), res(conformance.StatusSkip, true)}}, conformance.VerdictNotConformant, 1},
		{"optional failed", conformance.Report{Results: []conformance.Result{res(conformance.StatusPass, true), res(conformance.StatusFail, false)}}, conformance.VerdictConformant, 0},
		{"only", conformance.Report{Partial: true, Results: []conformance.Result{res(conformance.StatusPass, true)}}, conformance.VerdictIncomplete, 0},
		{"online", conformance.Report{Partial: true, ReadOnly: true, Results: []conformance.Result{res(conformance.StatusPass, true)}}, conformance.VerdictIncomplete, 0},
	}
	for _, c := range cases {
		if v := c.rep.ComputeVerdict(); v != c.verdict {
			t.Errorf("%s: verdict %s, want %s", c.name, v, c.verdict)
		}
		if code := c.rep.ExitCode(); code != c.code {
			t.Errorf("%s: exit %d, want %d", c.name, code, c.code)
		}
	}
	var buf bytes.Buffer
	inc := cases[1].rep
	printReport(&buf, &inc)
	if out := buf.String(); !strings.Contains(out, "INCOMPLETE") || !strings.Contains(out, "Skipped required checks") || !strings.Contains(out, "Needs: an operator") || strings.Contains(out, "partners") {
		t.Errorf("text report of an INCOMPLETE run:\n%s", out)
	}
	buf.Reset()
	printMarkdown(&buf, &inc)
	if out := buf.String(); !strings.Contains(out, "conformance: INCOMPLETE") || !strings.Contains(out, "skipped (required)") {
		t.Errorf("markdown report of an INCOMPLETE run:\n%s", out)
	}
}

func TestBrokenNodeFails(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	broken := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/obx" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"obx_version":"0.1"}`))
			return
		}
		n.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(broken)
	defer srv.Close()
	c := n.Credentials()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"protocol", "--url", srv.URL, "--admin-user", c.AdminEmail, "--admin-password", c.AdminPassword, "--only", "P-DISC-1"}, &out, &errb, noEnv)
	if code != 1 || !strings.Contains(out.String(), "FAIL  P-DISC-1") || !strings.Contains(out.String(), "NOT CONFORMANT") || !strings.Contains(out.String(), "Why it matters") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
}

func TestUsageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"protocol"}, &out, &errb, noEnv); code != 2 {
		t.Fatalf("missing --url: %d", code)
	}
	if code := run(context.Background(), []string{"list"}, &out, &errb, noEnv); code != 0 || !strings.Contains(out.String(), "I-REPLAY-1") {
		t.Fatalf("list: %d %s", code, out.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"all", "--url", "http://127.0.0.1:1", "--setup-code", "x"}, &out, &errb, noEnv); code != 2 {
		t.Fatalf("unreachable node: %d %s", code, errb.String())
	}
}

// The id:secret flags of docs/conformance.md and the Markdown report.
func TestPairFlagsAndMarkdown(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	c := n.Credentials()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"protocol", "--url", srv.URL, "--client", c.Full.ClientID + ":" + c.Full.ClientSecret,
		"--second-client", c.Restricted.ClientID + ":" + c.Restricted.ClientSecret, "--only", "P-FEED-4,P-DISC", "--format", "markdown"}, &out, &errb, noEnv)
	if code != 0 || !strings.Contains(out.String(), "## Open Brand Exchange conformance: selected checks passed") || !strings.Contains(out.String(), "| pass | P-FEED-4 |") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"protocol", "--url", srv.URL, "--client", "no-colon"}, &out, &errb, noEnv); code != 2 || !strings.Contains(errb.String(), "id:secret") {
		t.Fatalf("bad --client: %d %s", code, errb.String())
	}
}

// A saved --json report can be shown for people later.
func TestReportFromJSON(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	c := n.Credentials()
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"protocol", "--url", srv.URL, "--admin-user", c.AdminEmail, "--admin-password", c.AdminPassword, "--only", "P-DISC", "--json"}, &out, &errb, noEnv); code != 0 {
		t.Fatalf("run: %d %s", code, errb.String())
	}
	file := filepath.Join(t.TempDir(), "report.json")
	os.WriteFile(file, out.Bytes(), 0o644)
	out.Reset()
	if code := run(context.Background(), []string{"report", "--format", "markdown", file}, &out, &errb, noEnv); code != 0 || !strings.Contains(out.String(), "| pass | P-DISC-1 |") {
		t.Fatalf("markdown from json: %d %s", code, out.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"report", file}, &out, &errb, noEnv); code != 0 || !strings.Contains(out.String(), "Open Brand Exchange conformance report") {
		t.Fatalf("text from json: %d %s", code, out.String())
	}
	os.WriteFile(file, []byte(`{"hello": 1}`), 0o644)
	if code := run(context.Background(), []string{"report", file}, &out, &errb, noEnv); code != 2 {
		t.Fatalf("not a report: %d", code)
	}
}

// A full pass points to the free confirmation and the partner page; a failed or
// partial run does not.
func TestNextStepOnlyAfterFullPass(t *testing.T) {
	pass := &conformance.Report{Results: []conformance.Result{{Required: true, Status: conformance.StatusPass}}}
	var buf bytes.Buffer
	printReport(&buf, pass)
	if !strings.Contains(buf.String(), "All required checks passed.") || !strings.Contains(buf.String(), "/en/partners/") {
		t.Fatalf("passing report has no next step:\n%s", buf.String())
	}
	buf.Reset()
	printMarkdown(&buf, pass)
	if !strings.Contains(buf.String(), "/en/partners/") {
		t.Fatalf("markdown report has no next step:\n%s", buf.String())
	}
	fail := &conformance.Report{Results: []conformance.Result{{Required: true, Status: conformance.StatusFail}}}
	partial := &conformance.Report{Partial: true, Results: pass.Results}
	for _, rep := range []*conformance.Report{fail, partial} {
		buf.Reset()
		printReport(&buf, rep)
		if strings.Contains(buf.String(), "partners") {
			t.Fatalf("next step shown for a failed or partial run:\n%s", buf.String())
		}
	}
}
