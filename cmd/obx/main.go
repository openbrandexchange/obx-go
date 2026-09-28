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
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
	tr        *translator
}

// t translates a message (see i18n.go).
func (e *env) t(id string, kv ...any) string {
	if e.tr == nil {
		e.tr = newTranslator(detectLanguage(e.getenv))
	}
	return e.tr.t(id, kv...)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], defaultEnv()))
}

// command is one obx subcommand. define registers the flags on fs and returns
// the function that runs the command with the positional arguments; help and
// the shell completions call define without running anything.
type command struct {
	name     string
	args     string // synopsis after the command name, e.g. "[<folder>]"
	argKind  string // completion of positional arguments: dir, path, url, shell, command or ""
	hidden   bool
	define   func(e *env, fs *flag.FlagSet) func(ctx context.Context, pos []string) (int, error)
	examples []string
}

// summary, description: messages "cmd.<name>.summary" and "cmd.<name>.about".
func (c *command) summary(e *env) string { return e.t("cmd." + c.name + ".summary") }

func commands() []*command {
	return []*command{
		{name: "login", args: "<portal address>", argKind: "url", define: defineLogin,
			examples: []string{"obx login https://brands.kestrel.example", "obx login https://brands.kestrel.example --token obxi_…", "OBX_TOKEN=obxi_… obx login https://brands.kestrel.example"}},
		{name: "logout", args: "[<portal address>]", argKind: "url", define: defineLogout,
			examples: []string{"obx logout"}},
		{name: "info", define: defineInfo, examples: []string{"obx info", "obx info --json"}},
		{name: "push", args: "[<folder>]", argKind: "dir", define: definePush,
			examples: []string{"obx push ./export", "obx push --csv products.csv --images ./images", "obx push --package export.zip --dry-run", "obx push ./export --mirror"}},
		{name: "watch", args: "<folder>", argKind: "dir", define: defineWatch,
			examples: []string{"obx watch /data/outbox", "obx watch ./outbox --once"}},
		{name: "status", define: defineStatus, examples: []string{"obx status", "obx status --job job_…"}},
		{name: "pull", args: "<portal address> --out <folder>", argKind: "url", define: definePull,
			examples: []string{"OBX_CLIENT_ID=obxc_… OBX_CLIENT_SECRET=obxs_… obx pull https://brands.kestrel.example --out ./kestrel", "obx pull https://brands.kestrel.example --out ./kestrel --renditions web,original --follow"}},
		{name: "config", args: "[path]", define: defineConfig, examples: []string{"obx config", "obx config path"}},
		{name: "completion", args: "<bash|zsh|fish|powershell>", argKind: "shell", define: defineCompletion,
			examples: []string{"obx completion bash > /etc/bash_completion.d/obx", "obx completion zsh > \"${fpath[1]}/_obx\"", "obx completion fish > ~/.config/fish/completions/obx.fish", "obx completion powershell >> $PROFILE"}},
		{name: "version", define: defineVersion, examples: []string{"obx version"}},
		{name: "help", args: "[<command>]", argKind: "command", define: defineHelp, examples: []string{"obx help push"}},
	}
}

func findCommand(name string) *command {
	for _, c := range commands() {
		if c.name == name {
			return c
		}
	}
	return nil
}

func run(ctx context.Context, args []string, e *env) int {
	if len(args) == 0 {
		printUsage(e.stderr, e)
		return exitFailed
	}
	name, rest := args[0], args[1:]
	switch name {
	case "--help", "-h":
		name = "help"
	case "--version", "-v":
		name = "version"
	}
	cmd := findCommand(name)
	if cmd == nil {
		fmt.Fprintln(e.stderr, "obx: "+e.t("main.unknown_command", "command", name))
		fmt.Fprintln(e.stderr)
		printUsage(e.stderr, e)
		return exitFailed
	}
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	runFn := cmd.define(e, fs)
	pos, err := parseFlags(fs, rest)
	if errors.Is(err, flag.ErrHelp) {
		printCommandHelp(e.stdout, e, cmd, fs)
		return exitOK
	}
	if err != nil {
		fmt.Fprintln(e.stderr, "obx "+cmd.name+": "+flagError(e, err))
		fmt.Fprintln(e.stderr, e.t("main.see_help", "command", cmd.name))
		return exitFailed
	}
	code, err := runFn(ctx, pos)
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(e.stderr, "obx "+cmd.name+": "+ue.msg)
			fmt.Fprintln(e.stderr, e.t("main.see_help", "command", cmd.name))
			return exitFailed
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(e.stderr, "obx: "+e.t("main.stopped"))
			return exitFailed
		}
		fmt.Fprintln(e.stderr, "obx: "+friendly(e, err))
		return exitFailed
	}
	return code
}

// usageError is a mistake in how the command was called; obx then points to
// the command's help.
type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

// flagError explains a flag.Parse error in the user's language.
func flagError(e *env, err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined: "):
		return e.t("main.unknown_option", "option", strings.TrimPrefix(msg, "flag provided but not defined: "))
	case strings.HasPrefix(msg, "flag needs an argument: "):
		return e.t("main.option_needs_value", "option", strings.TrimPrefix(msg, "flag needs an argument: "))
	case strings.HasPrefix(msg, "invalid value "), strings.HasPrefix(msg, "invalid boolean value "):
		return e.t("main.option_bad_value", "detail", msg)
	}
	return msg
}

func printUsage(w io.Writer, e *env) {
	fmt.Fprintln(w, e.t("main.intro"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, e.t("main.usage")+":")
	width := 0
	for _, c := range commands() {
		if n := len(c.name); !c.hidden && n > width {
			width = n
		}
	}
	for _, c := range commands() {
		if !c.hidden {
			fmt.Fprintf(w, "  obx %-*s  %s\n", width, c.name, c.summary(e))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, e.t("main.env_title")+":")
	fmt.Fprintln(w, "  OBX_URL        "+e.t("main.env_url"))
	fmt.Fprintln(w, "  OBX_TOKEN      "+e.t("main.env_token"))
	fmt.Fprintln(w, "  OBX_CLIENT_ID, OBX_CLIENT_SECRET   "+e.t("main.env_client"))
	fmt.Fprintln(w, "  OBX_LANG       "+e.t("main.env_lang"))
	fmt.Fprintln(w, "  OBX_CONFIG_DIR "+e.t("main.env_config"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, e.t("main.more_help"))
	fmt.Fprintln(w, e.t("main.exit_codes"))
}

// printCommandHelp prints "obx <cmd> --help".
func printCommandHelp(w io.Writer, e *env, c *command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "obx %s: %s\n\n", c.name, c.summary(e))
	synopsis := "obx " + c.name
	if c.args != "" {
		synopsis += " " + c.args
	}
	hasFlags := false
	fs.VisitAll(func(*flag.Flag) { hasFlags = true })
	if hasFlags {
		synopsis += " [" + e.t("main.options_word") + "]"
	}
	fmt.Fprintf(w, "%s:\n  %s\n\n", e.t("main.usage"), synopsis)
	fmt.Fprintln(w, wrapParagraphs(e.t("cmd."+c.name+".about"), 78))
	if hasFlags {
		fmt.Fprintf(w, "\n%s:\n", e.t("main.options_title"))
		type row struct{ left, right string }
		var rows []row
		width := 0
		fs.VisitAll(func(f *flag.Flag) {
			left := "--" + f.Name
			if v := valueName(f); v != "" {
				left += " <" + v + ">"
			}
			right := f.Usage
			if d := f.DefValue; d != "" && d != "false" && d != "0" && d != "0s" {
				right += " (" + e.t("main.default", "value", d) + ")"
			}
			rows = append(rows, row{left, right})
			width = max(width, len(left))
		})
		for _, r := range rows {
			fmt.Fprintf(w, "  %-*s  %s\n", width, r.left, r.right)
		}
	}
	if len(c.examples) > 0 {
		fmt.Fprintf(w, "\n%s:\n", e.t("main.examples"))
		for _, ex := range c.examples {
			fmt.Fprintln(w, "  "+ex)
		}
	}
}

// valueName is the placeholder shown after a flag in help ("" for switches).
func valueName(f *flag.Flag) string {
	if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
		return ""
	}
	switch flagKind(f.Name) {
	case "file":
		return "file"
	case "dir":
		return "folder"
	case "path":
		return "folder|zip"
	}
	if g, ok := f.Value.(flag.Getter); ok {
		switch g.Get().(type) {
		case int, int64, uint, float64:
			return "n"
		case time.Duration:
			return "duration"
		}
	}
	return "text"
}

// flagKind says what a flag's value is, for help and shell completion.
func flagKind(name string) string {
	switch name {
	case "csv", "assets-csv", "package":
		return "file"
	case "images":
		return "path"
	case "out":
		return "dir"
	}
	return ""
}

// wrap breaks text into lines of at most width columns (paragraphs kept).
func wrap(text string, width int) string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, w := range words {
			if line != "" && len([]rune(line))+1+len([]rune(w)) > width {
				out = append(out, line)
				line = w
				continue
			}
			if line != "" {
				line += " "
			}
			line += w
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// wrapParagraphs wraps every paragraph and leaves a blank line between them.
func wrapParagraphs(text string, width int) string {
	var paras []string
	for _, p := range strings.Split(text, "\n") {
		paras = append(paras, wrap(p, width))
	}
	return strings.Join(paras, "\n\n")
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
		if rest[0] == "--" {
			return append(positional, rest[1:]...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func defaultEnv() *env {
	e := &env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, isTTY: isTerminal(os.Stderr)}
	e.configDir, e.cacheDir = configDirs(os.Getenv)
	e.readPass = readPassword
	return e
}

// configDirs returns where obx keeps its settings and its cache:
//
//	Linux:   $XDG_CONFIG_HOME/obx (~/.config/obx) and $XDG_CACHE_HOME/obx (~/.cache/obx)
//	macOS:   ~/Library/Application Support/obx and ~/Library/Caches/obx
//	Windows: %AppData%\obx and %LocalAppData%\obx
//
// OBX_CONFIG_DIR puts both in one folder (used by the container image).
func configDirs(getenv func(string) string) (config, cache string) {
	if d := getenv("OBX_CONFIG_DIR"); d != "" {
		return d, d
	}
	cfg, err := os.UserConfigDir()
	if err != nil || cfg == "" {
		cfg = "."
	}
	c, err := os.UserCacheDir()
	if err != nil || c == "" {
		c = cfg
	}
	return filepath.Join(cfg, "obx"), filepath.Join(c, "obx")
}

func defineVersion(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	return func(ctx context.Context, pos []string) (int, error) {
		fmt.Fprintf(e.stdout, "obx %s\n", Version)
		return exitOK, nil
	}
}

func defineHelp(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	return func(ctx context.Context, pos []string) (int, error) {
		if len(pos) == 0 {
			printUsage(e.stdout, e)
			return exitOK, nil
		}
		c := findCommand(pos[0])
		if c == nil {
			return exitFailed, usageError{e.t("main.unknown_command", "command", pos[0])}
		}
		cfs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		c.define(e, cfs)
		printCommandHelp(e.stdout, e, c, cfs)
		return exitOK, nil
	}
}

var _ = register(map[string]string{
	"main.intro":              "obx publishes your product content to your Open Brand Exchange portal.",
	"main.usage":              "Usage",
	"main.options_word":       "options",
	"main.options_title":      "Options",
	"main.examples":           "Examples",
	"main.default":            "default: {value}",
	"main.env_title":          "Settings can also come from the environment",
	"main.env_url":            "your portal address, for example https://brands.kestrel.example",
	"main.env_token":          "your access key for automatic upload (starts with obxi_)",
	"main.env_client":         "API key for your software, for \"obx pull\"",
	"main.env_lang":           "language of obx's messages, for example nl or de (default: your computer's language)",
	"main.env_config":         "folder for obx's settings (default: see \"obx config\")",
	"main.more_help":          "Run \"obx help <command>\" or \"obx <command> --help\" for the options of a command.",
	"main.exit_codes":         "Exit codes: 0 = all good, 2 = done, but some rows or images had problems, 1 = failed.",
	"main.unknown_command":    "there is no command \"{command}\".",
	"main.see_help":           "Run \"obx {command} --help\" to see how to use it.",
	"main.unknown_option":     "unknown option {option}.",
	"main.option_needs_value": "option {option} needs a value.",
	"main.option_bad_value":   "an option has a wrong value ({detail}).",
	"main.stopped":            "stopped. Run the same command again to continue where it stopped.",

	"cmd.version.summary":    "show the version of obx",
	"cmd.version.about":      "Shows which version of obx this is.",
	"cmd.help.summary":       "show help for a command",
	"cmd.help.about":         "Shows what a command does, its options and examples.",
	"cmd.login.summary":      "connect this computer to your portal",
	"cmd.logout.summary":     "forget the stored access key",
	"cmd.info.summary":       "check the connection: brands, languages and limits",
	"cmd.push.summary":       "publish a spreadsheet and images",
	"cmd.watch.summary":      "keep publishing whatever lands in a folder",
	"cmd.status.summary":     "show recent uploads and their problems",
	"cmd.pull.summary":       "download a brand's catalog and images (for retailers)",
	"cmd.config.summary":     "show where obx keeps its settings",
	"cmd.completion.summary": "print a script for tab completion in your shell",
})
