package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

func cmdLogin(ctx context.Context, args []string, e *env) error {
	fs := newFlags("login", "login <portal address> [--token obxi_…] [--email you@brand.example]", e)
	token := fs.String("token", "", "an automatic upload key from your portal (starts with obxi_); read from OBX_TOKEN when empty")
	email := fs.String("email", "", "sign in with your portal e-mail and password instead; obx then creates an upload key for this computer")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	addr := ""
	if len(pos) > 0 {
		addr = pos[0]
	} else {
		addr = e.getenv("OBX_URL")
	}
	if addr == "" {
		fs.Usage()
		return errors.New("give the address of your portal, for example: obx login https://brands.kestrel.example")
	}
	base, err := client.NodeBase(addr)
	if err != nil {
		return err
	}
	disc, err := (func() (*obx.DiscoveryDocument, error) {
		c, err := client.New(client.Config{BaseURL: base, UserAgent: "obx-cli/" + Version})
		if err != nil {
			return nil, err
		}
		return c.Discovery(ctx)
	})()
	if err != nil {
		return fmt.Errorf("%s does not look like an Open Brand Exchange portal (%s)", base, friendly(err))
	}
	fmt.Fprintf(e.stderr, "Portal: %s (%s)\n", disc.Publisher.Name, base)
	tok := *token
	if tok == "" {
		tok = e.getenv("OBX_TOKEN")
	}
	if tok == "" && *email == "" {
		rd := bufio.NewReader(e.stdin)
		fmt.Fprint(e.stderr, "Paste your automatic upload key (starts with obxi_),\nor press Enter to sign in with your portal e-mail and password: ")
		line, _ := rd.ReadString('\n')
		tok = strings.TrimSpace(line)
		if tok == "" {
			fmt.Fprint(e.stderr, "E-mail: ")
			line, _ := rd.ReadString('\n')
			*email = strings.TrimSpace(line)
		}
	}
	if tok == "" {
		if *email == "" {
			return errors.New("no upload key and no e-mail given")
		}
		pw := e.getenv("OBX_PASSWORD")
		if pw == "" {
			if pw, err = e.readPass("Password: "); err != nil {
				return err
			}
		}
		adm, err := nodeadmin.New(base, nil)
		if err != nil {
			return err
		}
		if _, err := adm.Login(ctx, *email, pw); err != nil {
			return fmt.Errorf("sign in failed: %s", friendly(err))
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "this computer"
		}
		created, err := adm.CreateIngestToken(ctx, nodeadmin.IngestTokenInput{Name: "obx on " + host + " (" + time.Now().Format("2006-01-02") + ")",
			Scopes: []string{obx.IngestScopeRead, obx.IngestScopeWrite, obx.IngestScopeDelete}})
		if err != nil {
			return fmt.Errorf("creating an upload key failed: %s (only admins can create keys)", friendly(err))
		}
		adm.Logout(ctx)
		tok = created.Secret
		fmt.Fprintf(e.stderr, "Created upload key %q (you can revoke it in the portal under Automation).\n", created.Token.Name)
	}
	ic, err := e.ingestClient(base, tok)
	if err != nil {
		return err
	}
	info, err := ic.Info(ctx)
	if err != nil {
		return fmt.Errorf("the upload key does not work: %s", friendly(err))
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	cfg.Nodes[base] = nodeConfig{Token: tok, Name: disc.Publisher.Name, SavedAt: time.Now().UTC()}
	cfg.Default = base
	if err := e.saveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Connected to %s as %q. You can now run \"obx push\".\n", disc.Publisher.Name, info.Token.Name)
	fmt.Fprintf(e.stderr, "The key is stored in %s (only readable by you).\n", e.configPath())
	return nil
}

func cmdLogout(args []string, e *env) error {
	fs := newFlags("logout", "logout [<portal address>]", e)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	base := cfg.Default
	if len(pos) > 0 {
		if base, err = client.NodeBase(pos[0]); err != nil {
			return err
		}
	}
	if _, ok := cfg.Nodes[base]; !ok {
		fmt.Fprintln(e.stdout, "Nothing stored for", base)
		return nil
	}
	delete(cfg.Nodes, base)
	if cfg.Default == base {
		cfg.Default = ""
	}
	if err := e.saveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Forgot the upload key for %s. It still works elsewhere until you revoke it in the portal.\n", base)
	return nil
}

func cmdInfo(ctx context.Context, args []string, e *env) error {
	fs := newFlags("info", "info [--url …] [--token …] [--json]", e)
	u := fs.String("url", "", "portal address (default: OBX_URL or the one from obx login)")
	tok := fs.String("token", "", "upload key (default: OBX_TOKEN or the stored key)")
	asJSON := fs.Bool("json", false, "print the raw answer as JSON")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	base, token, err := e.target(*u, *tok)
	if err != nil {
		return err
	}
	ic, err := e.ingestClient(base, token)
	if err != nil {
		return err
	}
	info, err := ic.Info(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	w := e.stdout
	fmt.Fprintf(w, "Portal:     %s\n", base)
	fmt.Fprintf(w, "Upload key: %s (%s)\n", info.Token.Name, strings.Join(info.Token.Scopes, ", "))
	var brands []string
	for _, b := range info.Brands {
		brands = append(brands, fmt.Sprintf("%s (%s)", b.Name, b.ExternalID))
	}
	fmt.Fprintf(w, "Brands:     %s\n", strings.Join(brands, ", "))
	fmt.Fprintf(w, "Languages:  %s\n", strings.Join(info.Languages, ", "))
	if info.Renditions.ServerGenerated {
		fmt.Fprintln(w, "Images:     the portal makes web and thumbnail versions itself")
	} else {
		fmt.Fprintln(w, "Images:     obx makes web and thumbnail versions before uploading")
	}
	fmt.Fprintf(w, "Limits:     %s per file, %d rows per batch\n", humanBytes(info.Limits.MaxUploadBytes), info.Limits.MaxBatchOperations)
	if info.Token.ExpiresAt != nil {
		fmt.Fprintf(w, "Key expires: %s\n", info.Token.ExpiresAt.Format("2006-01-02"))
	}
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
