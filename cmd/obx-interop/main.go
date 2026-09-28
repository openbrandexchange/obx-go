// Command obx-interop checks that different OBX node implementations answer
// the same automation with the same protocol output.
//
// It is a developer tool for people who build nodes; scripts/interop.sh
// drives it against node-go, node-php and node-ts:
//
//	obx-interop setup   --url U --setup-code C --out creds.json   first-run setup, upload key, API key
//	obx-interop trace   --url U --creds creds.json --out trace.json
//	                    records /changes, products and file hashes, replays
//	                    fixtures/ingest/script.ndjson and records every step
//	obx-interop mirror  --dir D --out mirror.json                 normalises an `obx pull` folder
//	obx-interop compare a.json b.json [c.json …]                  prints every difference
//
// Node-assigned ids, timestamps, cursors and signed URLs are replaced by
// stable labels ("product:KO-1001") before anything is compared.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openbrandexchange/obx-go/specdata"
)

const usage = `obx-interop compares OBX node implementations.

Usage:
  obx-interop setup   --url <node> --setup-code <code> --out creds.json
  obx-interop trace   --url <node> --creds creds.json --out trace.json [--fixtures <spec>/fixtures]
  obx-interop mirror  --dir <obx pull folder> --out mirror.json
  obx-interop compare <a.json> <b.json> [more.json …]

Exit codes: 0 = ok / identical, 1 = differences or a failed step, 2 = usage.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "node base URL")
	code := fs.String("setup-code", "", "first-run setup code")
	credsPath := fs.String("creds", "", "credentials file written by setup")
	out := fs.String("out", "", "output file")
	dir := fs.String("dir", "", "obx pull folder")
	fixtures := fs.String("fixtures", "", "spec fixtures directory (default: the embedded copy)")
	timeout := fs.Duration("timeout", 10*time.Minute, "overall timeout")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var err error
	switch args[0] {
	case "setup":
		if *url == "" || *code == "" || *out == "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		var c *creds
		if c, err = setup(ctx, *url, *code); err == nil {
			err = writeJSON(*out, c)
		}
	case "trace":
		if *url == "" || *credsPath == "" || *out == "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		var c creds
		if err = readJSON(*credsPath, &c); err != nil {
			break
		}
		fsys := specdata.Fixtures()
		if *fixtures != "" {
			fsys = os.DirFS(*fixtures)
		}
		var t *trace
		t, err = runTrace(ctx, *url, &c, fsys, stderr)
		if t != nil {
			if werr := writeJSON(*out, t); err == nil {
				err = werr
			}
		}
	case "mirror":
		if *dir == "" || *out == "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		var m map[string]any
		if m, err = normaliseMirror(os.DirFS(*dir)); err == nil {
			err = writeJSON(*out, m)
		}
	case "compare":
		files := fs.Args()
		if len(files) < 2 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		docs := make([]any, len(files))
		for i, f := range files {
			if err := readJSON(f, &docs[i]); err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return 1
			}
		}
		diffs := 0
		for i := 1; i < len(docs); i++ {
			d := diff(docs[0], docs[i], "")
			for _, line := range d {
				fmt.Fprintf(stdout, "%s vs %s: %s\n", files[0], files[i], line)
			}
			diffs += len(d)
		}
		if diffs > 0 {
			fmt.Fprintf(stdout, "%d difference(s)\n", diffs)
			return 1
		}
		fmt.Fprintf(stdout, "identical (%d files)\n", len(files))
		return 0
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

var errFailed = errors.New("one or more steps did not give the expected result")
