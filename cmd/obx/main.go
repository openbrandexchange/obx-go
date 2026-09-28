// Command obx publishes a brand's product content to its Open Brand
// Exchange portal and mirrors a brand's catalog for retailers.
//
//	obx login https://brands.kestrel.example
//	obx push --csv products.csv --images ./images
//	obx watch ./outbox
//	obx status
//	obx pull https://brands.kestrel.example --out ./kestrel
//
// Exit codes: 0 = everything worked, 2 = done but some rows or images had
// problems (they are listed), 1 = failed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Version is set by the release build (-ldflags "-X main.Version=…").
var Version = "dev"

// Exit codes.
const (
	exitOK       = 0
	exitFailed   = 1
	exitProblems = 2
)

// env is everything a command needs from the outside world, so tests can run
// commands in-process.
type env struct {
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	getenv    func(string) string
	configDir string
	cacheDir  string
	isTTY     bool
	readPass  func(prompt string) (string, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], defaultEnv()))
}

const usage = `obx publishes your product content to your Open Brand Exchange portal.

Usage:
  obx login <portal address>    connect this computer to your portal
  obx logout [<portal address>] forget the stored upload key
  obx info                      check the connection (brands, languages, limits)
  obx push [<folder>] [--csv products.csv] [--assets-csv assets.csv]
           [--images <folder or zip>] [--package export.zip]
           [--mirror] [--dry-run] [--language nl] [--no-wait] [--json]
                                publish a CSV and images
  obx watch <folder>            keep publishing whatever lands in a folder
  obx status [--job <id>]       recent uploads and their problems
  obx pull <portal address> --out <folder>
                                download a brand's catalog (for retailers)
  obx version

Settings can also come from the environment:
  OBX_URL        your portal address, for example https://brands.kestrel.example
  OBX_TOKEN      your automatic upload key (starts with obxi_)
  OBX_CLIENT_ID, OBX_CLIENT_SECRET   API key for "obx pull"

Run "obx <command> --help" for the options of a command.
Exit codes: 0 = all good, 2 = done but some rows had problems, 1 = failed.
`

func run(ctx context.Context, args []string, e *env) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, usage)
		return exitFailed
	}
	cmd, rest := args[0], args[1:]
	var err error
	code := exitOK
	switch cmd {
	case "login":
		err = cmdLogin(ctx, rest, e)
	case "logout":
		err = cmdLogout(rest, e)
	case "info":
		err = cmdInfo(ctx, rest, e)
	case "push":
		code, err = cmdPush(ctx, rest, e)
	case "watch":
		err = cmdWatch(ctx, rest, e)
	case "status":
		err = cmdStatus(ctx, rest, e)
	case "pull":
		err = cmdPull(ctx, rest, e)
	case "version", "--version", "-v":
		fmt.Fprintf(e.stdout, "obx %s\n", Version)
	case "help", "--help", "-h":
		fmt.Fprint(e.stdout, usage)
	default:
		fmt.Fprintf(e.stderr, "obx: unknown command %q\n\n%s", cmd, usage)
		return exitFailed
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(e.stderr, "obx: stopped")
			return exitFailed
		}
		fmt.Fprintf(e.stderr, "obx: %s\n", friendly(err))
		return exitFailed
	}
	return code
}

// newFlags creates a flag set that prints its usage to e.stderr.
func newFlags(name, synopsis string, e *env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprintf(e.stderr, "Usage: obx %s\n\nOptions:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses flags that may come before or after positional
// arguments ("obx push ./export --mirror" and "obx push --mirror ./export").
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func defaultEnv() *env {
	cfg, _ := os.UserConfigDir()
	cache, _ := os.UserCacheDir()
	e := &env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, isTTY: isTerminal(os.Stderr)}
	if d := os.Getenv("OBX_CONFIG_DIR"); d != "" {
		cfg, cache = d, d
	} else {
		cfg = joinDir(cfg, "obx")
		cache = joinDir(cache, "obx")
	}
	e.configDir, e.cacheDir = cfg, cache
	e.readPass = readPassword
	return e
}

func joinDir(base, sub string) string {
	if base == "" {
		base = "."
	}
	return strings.TrimRight(base, "/\\") + string(os.PathSeparator) + sub
}
