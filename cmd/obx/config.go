package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/obx"
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
		return nil, fmt.Errorf("%s is damaged: %w", e.configPath(), err)
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

// target resolves the portal address and upload key: flags, then OBX_URL /
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
		return "", "", errors.New("which portal? Run \"obx login <portal address>\" or set OBX_URL")
	}
	base, err := client.NodeBase(u)
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
		return "", "", fmt.Errorf("no upload key for %s. Run \"obx login %s\" or set OBX_TOKEN", base, base)
	}
	return base, tok, nil
}

func (e *env) ingestClient(base, token string) (*ingest.Client, error) {
	return ingest.New(base, token, ingest.WithUserAgent("obx-cli/"+Version), ingest.WithRetryHook(func(_ *http.Request, status int, wait time.Duration) {
		if status == 429 {
			fmt.Fprintf(e.stderr, "  the portal asks to slow down; waiting %s\n", wait.Round(time.Second))
		}
	}))
}

// friendly turns API errors into sentences for people.
func friendly(err error) string {
	var p *obx.Problem
	if errors.As(err, &p) {
		switch p.ObxError {
		case obx.ErrUnauthorized, obx.ErrInvalidToken:
			return "the upload key is not valid. Create a new one in your portal (Automation → Upload keys) and run \"obx login\" again."
		case obx.ErrAccessRevoked:
			return "this upload key was revoked or has expired. Create a new one in your portal and run \"obx login\" again."
		case obx.ErrInsufficientScope:
			return "this upload key may not do that (" + p.DetailString() + "). Create a key with more rights in your portal."
		case obx.ErrForbidden:
			return "not allowed: " + p.DetailString()
		case obx.ErrSetupRequired:
			return "the portal has not been set up yet; open it in your browser first."
		case obx.ErrMassUnpublishRefused:
			return p.DetailString() + ". Nothing was changed. If this is really what you want, add --allow-mass-unpublish."
		}
		if d := p.DetailString(); d != "" {
			return d
		}
		return p.Title
	}
	var oe *obx.OAuthError
	if errors.As(err, &oe) {
		if oe.Code == "invalid_client" {
			return "the API key (client id and secret) is not valid or was revoked."
		}
		return oe.Error()
	}
	return err.Error()
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

// progress prints a status line that updates in place on a terminal and
// every few seconds otherwise.
type progress struct {
	mu    sync.Mutex
	w     io.Writer
	tty   bool
	last  time.Time
	width int
}

func (p *progress) set(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if p.tty {
		pad := ""
		if n := p.width - len(msg); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		fmt.Fprintf(p.w, "\r%s%s", msg, pad)
		p.width = len(msg)
		return
	}
	if time.Since(p.last) >= 5*time.Second {
		fmt.Fprintln(p.w, msg)
		p.last = time.Now()
	}
}

func (p *progress) done(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if p.tty {
		pad := ""
		if n := p.width - len(msg); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		fmt.Fprintf(p.w, "\r%s%s\n", msg, pad)
		p.width = 0
		return
	}
	fmt.Fprintln(p.w, msg)
}
