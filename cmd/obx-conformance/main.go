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
  obx-conformance <profile> --url <node> [options]   profile: protocol, ingest, portal, security or all
  obx-conformance mock [--addr :8080]                run the in-memory mock node
  obx-conformance check --url <portal> [--client id:secret] [--json]
                                                     read-only online check (what openbrandexchange.com runs)
  obx-conformance check-server [--addr :8080]        the online check over HTTP (POST /check)
  obx-conformance list                               list all tests
  obx-conformance report [--format text|markdown] <report.json>
                                                     show a saved --json report for people
  obx-conformance version

Credentials (give what you have; with admin credentials or a setup code the
suite creates retailers, API keys and upload keys itself):
  --setup-code        first-run setup code of a fresh node (or OBX_SETUP_CODE)
  --admin-user        admin e-mail            --admin-password   admin password
  --client-id / --client-secret               API key with access to all brands
  --second-client-id / --second-client-secret API key restricted to brand "kestrel"
  --no-original-client-id / --no-original-client-secret   API key without originals
  (each pair also as one flag: --client id:secret, --second-client …, --no-original-client …)
  --token             upload key with ingest:read, ingest:write and ingest:delete

Nodes without the admin or ingest API (only the protocol is the standard):
load fixtures/kestrel with the node's own import, then let an operator do the
brand-side steps (create, restrict and revoke API keys, approve an access
request, change and unpublish a product, and optionally simulate a spec
upgrade for P-FEED-7):
  --operator-hook     program run for every step: action as argument, JSON on
                      stdin, JSON result on stdout (see docs/conformance.md)
  --operator-prompt   ask on the terminal to do each step by hand

"all" on a fresh node is the reference run: it completes setup (Kestrel
Outfitters, brand "kestrel", languages nl,en), imports fixtures/kestrel and
runs every profile.

Report: --format text (default, for people), markdown or json (also --json).
Exit codes: 0 = every required test passed, 1 = a required test failed,
2 = the suite could not run.
Run "obx-conformance <profile> --help" for every option.
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
			online := conformance.OnlineTier(t.ID)
			if online == "" {
				online = "-"
			}
			fmt.Fprintf(stdout, "%-10s %-9s %-8s %-9s %s\n", t.ID, t.Profile, req, online, t.Title)
		}
		return 0
	case "mock":
		return runMock(ctx, args[1:], stdout, stderr, getenv)
	case "check":
		return runCheck(ctx, args[1:], stdout, stderr, getenv)
	case "check-server":
		return runCheckServer(ctx, args[1:], stdout, stderr, getenv)
	case "report":
		return runReport(args[1:], stdout, stderr)
	case conformance.ProfileProtocol, conformance.ProfileIngest, conformance.ProfilePortal, conformance.ProfileSecurity, conformance.ProfileAll:
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
	pair := func(name string, id, secret *string) {
		fs.Func(name, "the same as --"+name+"-id and --"+name+"-secret, given as id:secret", func(v string) error {
			i := strings.Index(v, ":")
			if i <= 0 || i == len(v)-1 {
				return errors.New("use id:secret")
			}
			*id, *secret = v[:i], v[i+1:]
			return nil
		})
	}
	pair("client", &cfg.ClientID, &cfg.ClientSecret)
	pair("second-client", &cfg.SecondClientID, &cfg.SecondClientSecret)
	pair("no-original-client", &cfg.NoOriginalClientID, &cfg.NoOriginalClientSecret)
	format := fs.String("format", "text", "report format: text (for people), markdown (issues, CI summaries) or json")
	fixtures := fs.String("fixtures", "", "path to a spec fixtures directory (default: the embedded copy)")
	only := fs.String("only", "", "comma-separated test id prefixes to run (for example P-FEED,I-IMP-3)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	verbose := fs.Bool("verbose", false, "print progress")
	fs.BoolVar(&cfg.RateLimitTest, "rate-limit-test", false, "hammer an endpoint until it answers 429 (node must run with a low limit)")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout per HTTP request")
	opHook := fs.String("operator-hook", "", "program that does the brand-side steps on a node without the admin or ingest API (action as argument, JSON on stdin)")
	opPrompt := fs.Bool("operator-prompt", false, "ask on the terminal to do the brand-side steps by hand (nodes without the admin or ingest API)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if cfg.BaseURL == "" {
		fmt.Fprintln(stderr, "obx-conformance: --url is required (the address of the portal to check)")
		return 2
	}
	if *asJSON {
		*format = "json"
	}
	if *format != "text" && *format != "markdown" && *format != "json" {
		fmt.Fprintln(stderr, "obx-conformance: --format must be text, markdown or json")
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
	switch {
	case *opHook != "" && *opPrompt:
		fmt.Fprintln(stderr, "obx-conformance: use --operator-hook or --operator-prompt, not both")
		return 2
	case *opHook != "":
		cfg.Operator = conformance.HookOperator{Path: *opHook, Stderr: stderr}
	case *opPrompt:
		cfg.Operator = &conformance.PromptOperator{In: os.Stdin, Out: stderr}
	}
	cfg.HTTPClient = &http.Client{Timeout: *timeout}
	rep, err := conformance.Run(ctx, []string{profile}, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "obx-conformance: %v\n", err)
		return 2
	}
	if rep.SetupError != "" && *format != "json" {
		fmt.Fprintf(stderr, "obx-conformance: could not prepare the run: %s\n", rep.SetupError)
		fmt.Fprintln(stderr, "Check the address, that the portal is running, and the setup code or admin login.")
		return 2
	}
	switch *format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
	case "markdown":
		printMarkdown(stdout, rep)
	default:
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

func runMock(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("mock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	fresh := fs.Bool("fresh", false, "start without setup and fixtures (to test the all profile)")
	clientRenditions := fs.Bool("client-renditions", false, "behave like a node without an image library")
	allowPrivate := fs.Bool("allow-private-urls", true, "allow webhook/import targets on private addresses (development)")
	setupCode := fs.String("setup-code", "", "setup code for --fresh (default OBX_SETUP_CODE or obx-setup-code)")
	publicURL := fs.String("public-url", "", "public base URL (default OBX_PUBLIC_URL, else derived from each request)")
	verbose := fs.Bool("verbose", false, "log every request")
	readOnly := fs.Bool("read-only", false, "public demo: serve only the protocol's reads; writes, ingest, admin and portal are refused")
	rateLimit := fs.Int("rate-limit", 0, "protocol requests per client per minute (default 600)")
	var fullID, fullSecret string
	fs.Func("client", "fix the unrestricted API key as id:secret (or OBX_MOCK_CLIENT), for a public demo", func(v string) error {
		return splitPair(v, &fullID, &fullSecret)
	})
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if v := getenv("OBX_MOCK_CLIENT"); v != "" && fullID == "" {
		if err := splitPair(v, &fullID, &fullSecret); err != nil {
			fmt.Fprintf(stderr, "obx-conformance mock: OBX_MOCK_CLIENT: %v\n", err)
			return 2
		}
	}
	if *readOnly && *fresh {
		fmt.Fprintln(stderr, "obx-conformance mock: --read-only needs the fixtures (not --fresh)")
		return 2
	}
	if *publicURL == "" {
		*publicURL = getenv("OBX_PUBLIC_URL")
	}
	if *setupCode == "" {
		*setupCode = getenv("OBX_SETUP_CODE")
	}
	opts := mock.Options{Fixtures: !*fresh, ClientRenditions: *clientRenditions, AllowPrivateURLs: *allowPrivate, SetupCode: *setupCode, PublicURL: *publicURL,
		ReadOnly: *readOnly, RateLimit: *rateLimit, FullClientID: fullID, FullClientSecret: fullSecret}
	if *readOnly {
		opts.AllowPrivateURLs = false
	}
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
	if *readOnly {
		c := n.Credentials()
		fmt.Fprintf(stdout, "Read-only demo node with the Kestrel Outfitters fixtures. Public API key (read-only): %s (secret not printed)\n", c.Full.ClientID)
	} else if *fresh {
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
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "obx-conformance mock: %v\n", err)
		return 2
	}
	return 0
}

// runReport renders a saved --json report as text or Markdown.
func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "text or markdown")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || (*format != "text" && *format != "markdown") {
		fmt.Fprintln(stderr, "usage: obx-conformance report [--format text|markdown] <report.json>")
		return 2
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "obx-conformance: %v\n", err)
		return 2
	}
	var rep conformance.Report
	if err := json.Unmarshal(b, &rep); err != nil || rep.SpecCommit == "" {
		fmt.Fprintf(stderr, "obx-conformance: %s is not a report written with --json\n", fs.Arg(0))
		return 2
	}
	if *format == "markdown" {
		printMarkdown(stdout, &rep)
	} else {
		printReport(stdout, &rep)
	}
	if !rep.OK() {
		return 1
	}
	return 0
}
