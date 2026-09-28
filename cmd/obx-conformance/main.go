// Command obx-conformance checks an Open Brand Exchange node against the
// specification (docs/conformance.md) and can run the in-memory mock node.
//
//	obx-conformance all --url http://localhost:8080 --setup-code 123456
//	obx-conformance protocol --url https://node.example --client-id obxc_… --client-secret obxs_…
//	obx-conformance protocol --url https://node.example --admin-user admin@brand.example --admin-password …
//	obx-conformance mock --addr :8080
//
// Exit codes: 0 = every required test passed, 1 = a required test failed,
// 2 = the suite could not run (usage or connection problem).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/specdata"
)

// Version is set by the release build.
var Version = "dev"

const usage = `obx-conformance checks an Open Brand Exchange node against the specification.

Usage:
  obx-conformance <profile> --url <node> [options]   profile: protocol, ingest, portal or all
  obx-conformance mock [--addr :8080]                run the in-memory mock node
  obx-conformance list                               list all tests
  obx-conformance version

Credentials (give what you have; with admin credentials or a setup code the
suite creates retailers, API keys and upload keys itself):
  --setup-code        first-run setup code of a fresh node (or OBX_SETUP_CODE)
  --admin-user        admin e-mail            --admin-password   admin password
  --client-id / --client-secret               API key with access to all brands
  --second-client-id / --second-client-secret API key restricted to brand "kestrel"
  --no-original-client-id / --no-original-client-secret   API key without originals
  --token             upload key with ingest:read, ingest:write and ingest:delete

"all" on a fresh node is the reference run: it completes setup (Kestrel
Outfitters, brand "kestrel", languages nl,en), imports fixtures/kestrel and
runs every profile.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(stdout, "obx-conformance %s (spec %s)\n", Version, specdata.SpecCommit())
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "list":
		for _, t := range conformance.Tests() {
			req := "required"
			if !t.Required {
				req = "optional"
			}
			fmt.Fprintf(stdout, "%-10s %-9s %-8s %s\n", t.ID, t.Profile, req, t.Title)
		}
		return 0
	case "mock":
		return runMock(ctx, args[1:], stdout, stderr, getenv)
	case conformance.ProfileProtocol, conformance.ProfileIngest, conformance.ProfilePortal, conformance.ProfileAll:
		return runSuite(ctx, args[0], args[1:], stdout, stderr, getenv)
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

func runSuite(ctx context.Context, profile string, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet(profile, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg conformance.Config
	fs.StringVar(&cfg.BaseURL, "url", "", "node base URL (also --base)")
	fs.StringVar(&cfg.BaseURL, "base", "", "node base URL (alias of --url)")
	fs.StringVar(&cfg.AdminURL, "admin-url", "", "admin API base URL when it differs from --url")
	fs.StringVar(&cfg.AdminEmail, "admin-user", "", "admin e-mail")
	fs.StringVar(&cfg.AdminEmail, "admin-email", "", "admin e-mail (alias of --admin-user)")
	fs.StringVar(&cfg.AdminPassword, "admin-password", "", "admin password")
	fs.StringVar(&cfg.SetupCode, "setup-code", "", "first-run setup code of a fresh node (default OBX_SETUP_CODE)")
	fs.StringVar(&cfg.ClientID, "client-id", "", "API key (all brands, with originals): client id")
	fs.StringVar(&cfg.ClientSecret, "client-secret", "", "API key (all brands, with originals): client secret")
	fs.StringVar(&cfg.SecondClientID, "second-client-id", "", "API key restricted to brand kestrel: client id")
	fs.StringVar(&cfg.SecondClientSecret, "second-client-secret", "", "API key restricted to brand kestrel: client secret")
	fs.StringVar(&cfg.NoOriginalClientID, "no-original-client-id", "", "API key without originals: client id")
	fs.StringVar(&cfg.NoOriginalClientSecret, "no-original-client-secret", "", "API key without originals: client secret")
	fs.StringVar(&cfg.IngestToken, "token", "", "upload key (obxi_…) with all ingest scopes")
	fixtures := fs.String("fixtures", "", "path to a spec fixtures directory (default: the embedded copy)")
	only := fs.String("only", "", "comma-separated test id prefixes to run (for example P-FEED,I-IMP-3)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	verbose := fs.Bool("verbose", false, "print progress")
	fs.BoolVar(&cfg.RateLimitTest, "rate-limit-test", false, "hammer an endpoint until it answers 429 (node must run with a low limit)")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout per HTTP request")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if cfg.BaseURL == "" {
		fmt.Fprintln(stderr, "obx-conformance: --url is required")
		return 2
	}
	if cfg.SetupCode == "" {
		cfg.SetupCode = getenv("OBX_SETUP_CODE")
	}
	if *fixtures != "" {
		cfg.Fixtures = os.DirFS(*fixtures)
	}
	if *only != "" {
		cfg.Only = strings.Split(*only, ",")
	}
	if *verbose {
		cfg.Log = stderr
	}
	cfg.HTTPClient = &http.Client{Timeout: *timeout}
	rep, err := conformance.Run(ctx, []string{profile}, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "obx-conformance: %v\n", err)
		return 2
	}
	if rep.SetupError != "" && !*asJSON {
		fmt.Fprintf(stderr, "obx-conformance: could not prepare the run: %s\n", rep.SetupError)
		return 2
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
	} else {
		printReport(stdout, rep)
	}
	if rep.SetupError != "" {
		return 2
	}
	if !rep.OK() {
		return 1
	}
	return 0
}

func printReport(w io.Writer, rep *conformance.Report) {
	sw := rep.Software
	if sw == "" {
		sw = "unknown software"
	}
	fmt.Fprintf(w, "OBX conformance: %s (%s), spec %.12s\n", rep.BaseURL, sw, rep.SpecCommit)
	for _, s := range rep.Setup {
		fmt.Fprintf(w, "  setup: %s\n", s)
	}
	profile := ""
	for _, r := range rep.Results {
		if r.Profile != profile {
			profile = r.Profile
			fmt.Fprintf(w, "\n%s\n", profile)
		}
		label := map[string]string{conformance.StatusPass: "PASS", conformance.StatusFail: "FAIL", conformance.StatusSkip: "SKIP"}[r.Status]
		opt := ""
		if !r.Required {
			opt = " (optional)"
		}
		fmt.Fprintf(w, "  %s  %-10s %s%s\n", label, r.ID, r.Title, opt)
		for _, m := range r.Messages {
			fmt.Fprintf(w, "        - %s\n", m)
		}
	}
	verdict := "CONFORMANT for " + strings.Join(rep.Profiles, ", ")
	if !rep.OK() {
		verdict = "NOT CONFORMANT (a required test failed)"
	}
	fmt.Fprintf(w, "\n%d passed, %d failed, %d skipped in %.1fs: %s\n", rep.Passed, rep.Failed, rep.Skipped, float64(rep.Millis)/1000, verdict)
}

func runMock(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("mock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	fresh := fs.Bool("fresh", false, "start without setup and fixtures (to test the all profile)")
	clientRenditions := fs.Bool("client-renditions", false, "behave like a node without an image library")
	allowPrivate := fs.Bool("allow-private-urls", true, "allow webhook/import targets on private addresses (development)")
	setupCode := fs.String("setup-code", "", "setup code for --fresh (default OBX_SETUP_CODE or obx-setup-code)")
	publicURL := fs.String("public-url", "", "public base URL (default: derived from each request)")
	verbose := fs.Bool("verbose", false, "log every request")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *setupCode == "" {
		*setupCode = getenv("OBX_SETUP_CODE")
	}
	opts := mock.Options{Fixtures: !*fresh, ClientRenditions: *clientRenditions, AllowPrivateURLs: *allowPrivate, SetupCode: *setupCode, PublicURL: *publicURL}
	if *verbose {
		opts.Logger = log.New(stderr, "mock ", log.LstdFlags)
	}
	n := mock.New(opts)
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(stderr, "obx-conformance mock: %v\n", err)
		return 2
	}
	base := "http://" + ln.Addr().String()
	fmt.Fprintf(stdout, "OBX mock node listening on %s (discovery: %s/.well-known/obx)\n", base, base)
	if *fresh {
		fmt.Fprintf(stdout, "Fresh node. Setup code: %s\n  obx-conformance all --url %s --setup-code %s\n", n.SetupCode(), base, n.SetupCode())
	} else {
		c := n.Credentials()
		fmt.Fprintf(stdout, `Fixtures loaded (Kestrel Outfitters). Test accounts:
  admin login:            %s / %s
  portal login (retailer): %s / %s
  upload key:             %s
  API key, all brands:    %s / %s
  API key, kestrel only:  %s / %s
  API key, no originals:  %s / %s
Try:
  obx-conformance protocol --url %s --admin-user %s --admin-password %s
  OBX_URL=%s OBX_TOKEN=%s obx info
`, c.AdminEmail, c.AdminPassword, c.RetailerEmail, c.RetailerPassword, c.IngestToken, c.Full.ClientID, c.Full.ClientSecret,
			c.Restricted.ClientID, c.Restricted.ClientSecret, c.NoOriginals.ClientID, c.NoOriginals.ClientSecret, base, c.AdminEmail, c.AdminPassword, base, c.IngestToken)
	}
	srv := &http.Server{Handler: n, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "obx-conformance mock: %v\n", err)
		return 2
	}
	return 0
}
