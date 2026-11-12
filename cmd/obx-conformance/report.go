package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/openbrandexchange/obx-go/conformance"
)

// profileInfo explains a profile to someone who does not read API specs.
var profileInfo = map[string]struct{ name, why string }{
	conformance.ProfileProtocol: {"Reading by retailers' software (protocol)",
		"Retailers' shops, PIMs and plugins read the catalog and images through this. If it fails, their software cannot keep up with your catalog."},
	conformance.ProfileIngest: {"Automatic upload (ingest)",
		"Your own systems and the obx tool publish through this. If it fails, automatic uploads may be lost or published twice."},
	conformance.ProfilePortal: {"Admin and retailer screens (portal)",
		"The shared admin screens and the retailer portal use this. If it fails, some screens will not work."},
	conformance.ProfileSecurity: {"Security",
		"Checks that one retailer cannot see another's data, that links cannot be misused and that the portal cannot be tricked into reaching internal addresses."},
}

func profileName(p string) string {
	if i, ok := profileInfo[p]; ok {
		return i.name
	}
	return p
}

type profileCount struct{ pass, fail, optFail, skip int }

func countProfiles(rep *conformance.Report) (map[string]*profileCount, []string) {
	counts := map[string]*profileCount{}
	var order []string
	for _, r := range rep.Results {
		c := counts[r.Profile]
		if c == nil {
			c = &profileCount{}
			counts[r.Profile] = c
			order = append(order, r.Profile)
		}
		switch {
		case r.Status == conformance.StatusPass:
			c.pass++
		case r.Status == conformance.StatusFail && r.Required:
			c.fail++
		case r.Status == conformance.StatusFail:
			c.optFail++
		default:
			c.skip++
		}
	}
	return counts, order
}

func (c *profileCount) text() string {
	parts := []string{fmt.Sprintf("%d passed", c.pass)}
	if c.fail > 0 {
		parts = append(parts, fmt.Sprintf("%d FAILED", c.fail))
	}
	if c.optFail > 0 {
		parts = append(parts, fmt.Sprintf("%d optional not met", c.optFail))
	}
	if c.skip > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", c.skip))
	}
	return strings.Join(parts, " · ")
}

func verdict(rep *conformance.Report) string {
	names := append([]string(nil), rep.Profiles...)
	if rep.ReadOnly && rep.SetupError == "" && rep.OK() {
		return fmt.Sprintf("every online check passed (%d of %d checks ran). The other checks need writes or brand-side steps; run obx-conformance locally for the full result.", rep.Passed, len(rep.Results))
	}
	if rep.OK() && rep.Partial {
		return "every selected check passed. Only some checks ran (--only), so this is not a conformance claim."
	}
	if rep.OK() {
		return "CONFORMANT: this portal follows the Open Brand Exchange specification for " + strings.Join(names, ", ") + "."
	}
	failed := 0
	for _, r := range rep.Results {
		if r.Required && r.Status == conformance.StatusFail {
			failed++
		}
	}
	return fmt.Sprintf("NOT CONFORMANT: %d required check(s) failed. Retailers' software or automatic uploads may not work correctly with this portal until they are fixed.", failed)
}

// nextStep is shown under a full conformant result: how to get it confirmed and
// where to read about the official partner listing.
func nextStep(rep *conformance.Report) string {
	if !rep.OK() || rep.Partial {
		return ""
	}
	return "All required checks passed. To have this confirmed as \"Compatible with Open Brand Exchange\" (free), send the JSON report (--json) and the address of this portal to hello@openbrandexchange.com. About partner listings: https://openbrandexchange.com/en/partners/"
}

// printReport writes the report for people: the verdict first, a summary per
// area in plain words, what failed and why it matters, then every check.
func printReport(w io.Writer, rep *conformance.Report) {
	sw := rep.Software
	if strings.TrimSpace(sw) == "" {
		sw = "unknown software"
	}
	fmt.Fprintln(w, "Open Brand Exchange conformance report")
	fmt.Fprintf(w, "Portal:         %s (%s)\n", rep.BaseURL, strings.TrimSpace(sw))
	fmt.Fprintf(w, "Specification:  %.12s, checked %s, took %.1f s\n", rep.SpecCommit, rep.StartedAt.Local().Format("2006-01-02 15:04"), float64(rep.Millis)/1000)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Result: "+verdict(rep))
	if n := nextStep(rep); n != "" {
		fmt.Fprintln(w, n)
	}
	fmt.Fprintln(w)
	counts, order := countProfiles(rep)
	width := 0
	for _, p := range order {
		width = max(width, len(profileName(p)))
	}
	fmt.Fprintln(w, "Summary")
	for _, p := range order {
		fmt.Fprintf(w, "  %-*s  %s\n", width, profileName(p), counts[p].text())
	}
	var failed, optional, skipped []conformance.Result
	for _, r := range rep.Results {
		switch {
		case r.Status == conformance.StatusFail && r.Required:
			failed = append(failed, r)
		case r.Status == conformance.StatusFail:
			optional = append(optional, r)
		case r.Status == conformance.StatusSkip:
			skipped = append(skipped, r)
		}
	}
	section := func(title, intro string, list []conformance.Result, why bool) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		if intro != "" {
			fmt.Fprintf(w, "  %s\n", intro)
		}
		for _, r := range list {
			fmt.Fprintf(w, "  %s  %s\n", r.ID, r.Title)
			if why {
				fmt.Fprintf(w, "      Why it matters: %s\n", profileInfo[r.Profile].why)
			}
			for _, m := range r.Messages {
				fmt.Fprintf(w, "      - %s\n", m)
			}
		}
	}
	section("What failed (required)", "Fix these to be conformant. The test id refers to docs/conformance.md of the specification.", failed, true)
	section("Optional checks not met", "Optional features this portal offers but that do not fully work.", optional, false)
	section("Skipped", "Not checked; usually because a credential or a switch was not given.", skipped, false)
	for _, s := range rep.Setup {
		if strings.HasPrefix(s, "warning") {
			fmt.Fprintf(w, "\n%s\n", s)
		}
	}
	fmt.Fprintln(w, "\nAll checks")
	profile := ""
	for _, r := range rep.Results {
		if r.Profile != profile {
			profile = r.Profile
			fmt.Fprintf(w, "  %s\n", profileName(profile))
		}
		label := map[string]string{conformance.StatusPass: "PASS", conformance.StatusFail: "FAIL", conformance.StatusSkip: "SKIP"}[r.Status]
		opt := ""
		if !r.Required {
			opt = " (optional)"
		}
		fmt.Fprintf(w, "    %s  %-10s %s%s\n", label, r.ID, r.Title, opt)
	}
	if len(rep.Setup) > 0 {
		fmt.Fprintln(w, "\nPreparation")
		for _, s := range rep.Setup {
			fmt.Fprintf(w, "  - %s\n", s)
		}
	}
	fmt.Fprintf(w, "\n%d passed, %d failed, %d skipped. Machine-readable: add --json; for an issue or pull request: --format markdown.\n", rep.Passed, rep.Failed, rep.Skipped)
}

// printMarkdown writes the report as Markdown (GitHub step summary, issues).
func printMarkdown(w io.Writer, rep *conformance.Report) {
	sw := strings.TrimSpace(rep.Software)
	if sw == "" {
		sw = "unknown software"
	}
	head := map[bool]string{true: "conformant", false: "NOT conformant"}[rep.OK()]
	if rep.OK() && rep.Partial {
		head = "selected checks passed"
	}
	fmt.Fprintf(w, "## Open Brand Exchange conformance: %s\n\n", head)
	fmt.Fprintf(w, "Portal `%s` (%s), specification `%.12s`.\n\n%s\n\n", rep.BaseURL, sw, rep.SpecCommit, verdict(rep))
	if n := nextStep(rep); n != "" {
		fmt.Fprintf(w, "%s\n\n", n)
	}
	counts, order := countProfiles(rep)
	fmt.Fprintln(w, "| Area | Result |")
	fmt.Fprintln(w, "|---|---|")
	for _, p := range order {
		fmt.Fprintf(w, "| %s | %s |\n", profileName(p), counts[p].text())
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| | Test | What it checks |")
	fmt.Fprintln(w, "|---|---|---|")
	for _, r := range rep.Results {
		icon := map[string]string{conformance.StatusPass: "pass", conformance.StatusFail: "**FAIL**", conformance.StatusSkip: "skip"}[r.Status]
		if r.Status == conformance.StatusFail && !r.Required {
			icon = "optional, not met"
		}
		title := strings.ReplaceAll(r.Title, "|", "\\|")
		if len(r.Messages) > 0 && r.Status != conformance.StatusPass {
			var ms []string
			for _, m := range r.Messages {
				ms = append(ms, strings.ReplaceAll(m, "|", "\\|"))
			}
			title += "<br>" + strings.Join(ms, "<br>")
		}
		fmt.Fprintf(w, "| %s | %s | %s |\n", icon, r.ID, title)
	}
}
