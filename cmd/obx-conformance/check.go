package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/conformance"
)

// runCheck is the read-only online check: the protocol profile without
// writes or brand-side steps, over a transport that refuses private
// addresses, with a capped number of requests. It is what the compatibility
// checker on openbrandexchange.com runs; the result is never a conformance
// claim.
func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg conformance.Config
	o := &conformance.Online{}
	fs.StringVar(&cfg.BaseURL, "url", "", "portal base URL (https)")
	pair := func(name string, id, secret *string) {
		fs.Func(name, "API key as id:secret (or OBX_CHECK_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_"))+")", func(v string) error {
			return splitPair(v, id, secret)
		})
	}
	pair("client", &cfg.ClientID, &cfg.ClientSecret)
	pair("second-client", &cfg.SecondClientID, &cfg.SecondClientSecret)
	pair("no-original-client", &cfg.NoOriginalClientID, &cfg.NoOriginalClientSecret)
	format := fs.String("format", "text", "text, markdown or json")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.IntVar(&o.MaxRequests, "max-requests", 800, "maximum HTTP requests for the whole check")
	maxMiB := fs.Int64("max-mib", 64, "maximum MiB of responses for the whole check")
	fs.DurationVar(&o.MinInterval, "min-interval", 20*time.Millisecond, "minimum time between two requests")
	fs.BoolVar(&o.AllowInsecure, "allow-insecure", false, "allow http and private addresses (local portals only)")
	timeout := fs.Duration("timeout", 20*time.Second, "timeout per HTTP request")
	total := fs.Duration("total-timeout", 3*time.Minute, "timeout for the whole check")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// Secrets from the environment keep them out of the process list.
	for name, dst := range map[string][2]*string{
		"OBX_CHECK_CLIENT":             {&cfg.ClientID, &cfg.ClientSecret},
		"OBX_CHECK_SECOND_CLIENT":      {&cfg.SecondClientID, &cfg.SecondClientSecret},
		"OBX_CHECK_NO_ORIGINAL_CLIENT": {&cfg.NoOriginalClientID, &cfg.NoOriginalClientSecret},
	} {
		if v := getenv(name); v != "" && *dst[0] == "" {
			if err := splitPair(v, dst[0], dst[1]); err != nil {
				fmt.Fprintf(stderr, "obx-conformance: %s: %v\n", name, err)
				return 2
			}
		}
	}
	if cfg.BaseURL == "" {
		fmt.Fprintln(stderr, "obx-conformance check: --url is required")
		return 2
	}
	if !o.AllowInsecure && !strings.HasPrefix(strings.ToLower(cfg.BaseURL), "https://") {
		fmt.Fprintln(stderr, "obx-conformance check: the portal address must start with https://")
		return 2
	}
	if *asJSON {
		*format = "json"
	}
	o.MaxBytes = *maxMiB << 20
	cfg.Online = o
	// No Transport: the suite uses its safe dialer (private addresses
	// refused unless --allow-insecure) under the request budget.
	cfg.HTTPClient = &http.Client{Timeout: *timeout}
	ctx, cancel := context.WithTimeout(ctx, *total)
	defer cancel()
	rep, err := conformance.Run(ctx, []string{conformance.ProfileProtocol}, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "obx-conformance: %v\n", err)
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
		fmt.Fprintf(stdout, "Online check: %d requests, %.1f MiB received.\n", rep.Requests, float64(rep.Bytes)/(1<<20))
	}
	if rep.SetupError != "" {
		return 2
	}
	if !rep.OK() {
		return 1
	}
	return 0
}

func splitPair(v string, id, secret *string) error {
	i := strings.Index(v, ":")
	if i <= 0 || i == len(v)-1 {
		return errors.New("use id:secret")
	}
	*id, *secret = v[:i], v[i+1:]
	return nil
}
