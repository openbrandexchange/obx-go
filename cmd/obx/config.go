package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
)

// config is stored in <config dir>/config.json with mode 0600.
type config struct {
	Default string                `json:"default,omitempty"`
	Nodes   map[string]nodeConfig `json:"nodes,omitempty"`
}

type nodeConfig struct {
	Token   string    `json:"token"`
	Name    string    `json:"name,omitempty"`
	SavedAt time.Time `json:"saved_at"`
}

func (e *env) configPath() string { return filepath.Join(e.configDir, "config.json") }

func (e *env) loadConfig() (*config, error) {
	c := &config{Nodes: map[string]nodeConfig{}}
	b, err := os.ReadFile(e.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, userErr(e.t("config.damaged", "path", e.configPath()))
	}
	if c.Nodes == nil {
		c.Nodes = map[string]nodeConfig{}
	}
	return c, nil
}

func (e *env) saveConfig(c *config) error {
	if err := os.MkdirAll(e.configDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := e.configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.configPath())
}

// portalBase checks and normalises a portal address.
func (e *env) portalBase(addr string) (string, error) {
	base, err := client.NodeBase(addr)
	if err != nil {
		if strings.TrimSpace(addr) == "" {
			return "", usageError{e.t("config.no_address")}
		}
		return "", usageError{e.t("config.bad_address", "address", addr)}
	}
	return base, nil
}

// target resolves the portal address and access key: flags, then OBX_URL /
// OBX_TOKEN, then what "obx login" stored.
func (e *env) target(urlFlag, tokenFlag string) (string, string, error) {
	u := urlFlag
	if u == "" {
		u = e.getenv("OBX_URL")
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return "", "", err
	}
	if u == "" {
		u = cfg.Default
	}
	if u == "" {
		return "", "", userErr(e.t("config.which_portal"))
	}
	base, err := e.portalBase(u)
	if err != nil {
		return "", "", err
	}
	tok := tokenFlag
	if tok == "" {
		tok = e.getenv("OBX_TOKEN")
	}
	if tok == "" {
		tok = cfg.Nodes[base].Token
	}
	if tok == "" {
		return "", "", userErr(e.t("config.no_key", "portal", base))
	}
	return base, tok, nil
}

func (e *env) ingestClient(base, token string) (*ingest.Client, error) {
	return ingest.New(base, token, ingest.WithUserAgent("obx-cli/"+Version), ingest.WithRetryHook(func(_ *http.Request, status int, wait time.Duration) {
		if status == 429 {
			fmt.Fprintln(e.stderr, "  "+e.t("config.slow_down", "seconds", int(wait.Round(time.Second).Seconds())))
		}
	}))
}

// defineConfig: obx config [path].
func defineConfig(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	asJSON := fs.Bool("json", false, e.t("flag.json"))
	return func(ctx context.Context, pos []string) (int, error) {
		if len(pos) > 1 || (len(pos) == 1 && pos[0] != "path" && pos[0] != "show") {
			return exitFailed, usageError{e.t("config.usage")}
		}
		if len(pos) == 1 && pos[0] == "path" {
			fmt.Fprintln(e.stdout, e.configPath())
			return exitOK, nil
		}
		cfg, err := e.loadConfig()
		if err != nil {
			return exitFailed, err
		}
		type portal struct {
			Address string    `json:"address"`
			Name    string    `json:"name,omitempty"`
			Key     string    `json:"access_key"`
			SavedAt time.Time `json:"saved_at"`
			Default bool      `json:"default"`
		}
		var portals []portal
		for base, n := range cfg.Nodes {
			portals = append(portals, portal{Address: base, Name: n.Name, Key: maskKey(n.Token), SavedAt: n.SavedAt, Default: base == cfg.Default})
		}
		sort.Slice(portals, func(i, j int) bool { return portals[i].Address < portals[j].Address })
		lang := e.language()
		if *asJSON {
			enc := json.NewEncoder(e.stdout)
			enc.SetIndent("", "  ")
			return exitOK, enc.Encode(map[string]any{"config_file": e.configPath(), "cache_dir": e.cacheDir, "language": lang, "portals": portals})
		}
		w := e.stdout
		fmt.Fprintf(w, "%s: %s\n", e.t("config.file"), e.configPath())
		fmt.Fprintf(w, "%s: %s\n", e.t("config.cache"), e.cacheDir)
		fmt.Fprintf(w, "%s: %s\n", e.t("config.language"), lang)
		if len(portals) == 0 {
			fmt.Fprintln(w, e.t("config.no_portals"))
		}
		for _, p := range portals {
			mark := ""
			if p.Default {
				mark = " (" + e.t("config.default_portal") + ")"
			}
			fmt.Fprintf(w, "  %s%s: %s, %s %s\n", p.Address, mark, p.Name, e.t("config.key"), p.Key)
		}
		for _, v := range []string{"OBX_URL", "OBX_TOKEN", "OBX_CLIENT_ID", "OBX_CLIENT_SECRET", "OBX_LANG", "OBX_CONFIG_DIR"} {
			if val := e.getenv(v); val != "" {
				if strings.Contains(v, "TOKEN") || strings.Contains(v, "SECRET") {
					val = maskKey(val)
				}
				fmt.Fprintf(w, "%s: %s=%s\n", e.t("config.env_set"), v, val)
			}
		}
		return exitOK, nil
	}
}

// maskKey shows only the start and the last four characters of a secret.
func maskKey(s string) string {
	if len(s) <= 10 {
		return strings.Repeat("•", len(s))
	}
	prefix := s[:5]
	return prefix + "…" + s[len(s)-4:]
}

// ---- terminal helpers

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if !isTerminal(os.Stdin) {
		// A whole line: passphrases contain spaces ("correct horse battery staple").
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

var _ = register(map[string]string{
	"cmd.config.about": "Shows where obx keeps its settings and cache on this computer, which portals it is connected to (access keys shortened) and which language it uses.\n" +
		"Linux: ~/.config/obx/config.json (or $XDG_CONFIG_HOME/obx). macOS: ~/Library/Application Support/obx/config.json. Windows: %AppData%\\obx\\config.json. OBX_CONFIG_DIR sets another folder. \"obx config path\" prints only the file name.",
	"config.usage":          "use \"obx config\" or \"obx config path\".",
	"config.damaged":        "the settings file {path} is damaged. Delete it and run \"obx login\" again.",
	"config.no_address":     "give the address of your portal, for example https://brands.kestrel.example",
	"config.bad_address":    "\"{address}\" is not a web address. Use the address of your portal, for example https://brands.kestrel.example",
	"config.which_portal":   "which portal? Run \"obx login <portal address>\" first, or set OBX_URL.",
	"config.no_key":         "there is no access key for {portal}. Run \"obx login {portal}\", or set OBX_TOKEN.",
	"config.slow_down":      "the portal asks obx to slow down; waiting {seconds} s",
	"config.file":           "Settings file",
	"config.cache":          "Cache folder",
	"config.language":       "Language",
	"config.no_portals":     "Not connected to a portal yet. Run \"obx login <portal address>\".",
	"config.default_portal": "default",
	"config.key":            "access key",
	"config.env_set":        "Set in the environment",
	"flag.json":             "print the result as JSON (for scripts)",
	"flag.url":              "portal address (default: OBX_URL or the portal from \"obx login\")",
	"flag.token":            "access key (default: OBX_TOKEN or the key stored by \"obx login\")",
})
