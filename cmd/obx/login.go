package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

func defineLogin(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	token := fs.String("token", "", e.t("login.flag_token"))
	email := fs.String("email", "", e.t("login.flag_email"))
	return func(ctx context.Context, pos []string) (int, error) {
		return exitOK, login(ctx, e, pos, *token, *email)
	}
}

func login(ctx context.Context, e *env, pos []string, token, email string) error {
	addr := e.getenv("OBX_URL")
	if len(pos) > 0 {
		addr = pos[0]
	}
	if len(pos) > 1 {
		return usageError{e.t("login.one_address")}
	}
	if addr == "" {
		return usageError{e.t("config.no_address")}
	}
	base, err := e.portalBase(addr)
	if err != nil {
		return err
	}
	c, err := client.New(client.Config{BaseURL: base, UserAgent: "obx-cli/" + Version})
	if err != nil {
		return err
	}
	disc, err := c.Discovery(ctx)
	if err != nil {
		return userErr(e.t("login.not_a_portal", "portal", base, "reason", friendly(e, err)))
	}
	fmt.Fprintln(e.stderr, e.t("login.portal", "name", disc.Publisher.Name, "portal", base))
	tok := token
	if tok == "" {
		tok = e.getenv("OBX_TOKEN")
	}
	var rd *bufio.Reader
	if tok == "" && email == "" {
		rd = bufio.NewReader(e.stdin)
		fmt.Fprint(e.stderr, e.t("login.ask_key")+" ")
		line, _ := rd.ReadString('\n')
		tok = strings.TrimSpace(line)
		if tok == "" {
			fmt.Fprint(e.stderr, e.t("login.ask_email")+" ")
			line, _ := rd.ReadString('\n')
			email = strings.TrimSpace(line)
		}
	}
	if tok == "" {
		if email == "" {
			return userErr(e.t("login.nothing_given"))
		}
		pw := e.getenv("OBX_PASSWORD")
		if f, isFile := e.stdin.(*os.File); pw == "" && rd != nil && !(isFile && isTerminal(f)) {
			// Piped input (a script): the password is the next line of the same stream. A
			// separate reader would miss what the buffered reader above already consumed.
			fmt.Fprint(e.stderr, e.t("login.ask_password")+" ")
			line, _ := rd.ReadString('\n')
			pw = strings.TrimSpace(line)
			fmt.Fprintln(e.stderr)
		}
		if pw == "" {
			if pw, err = e.readPass(e.t("login.ask_password") + " "); err != nil {
				return err
			}
		}
		adm, err := nodeadmin.New(base, nil)
		if err != nil {
			return err
		}
		if _, err := adm.Login(ctx, email, pw); err != nil {
			return userErr(e.t("login.sign_in_failed", "reason", friendly(e, err)))
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "obx"
		}
		created, err := adm.CreateIngestToken(ctx, nodeadmin.IngestTokenInput{Name: "obx on " + host + " (" + time.Now().Format("2006-01-02") + ")",
			Scopes: []string{obx.IngestScopeRead, obx.IngestScopeWrite, obx.IngestScopeDelete}})
		if err != nil {
			return userErr(e.t("login.create_key_failed", "reason", friendly(e, err)))
		}
		_ = adm.Logout(ctx) // the admin session is not needed any more; it expires anyway
		tok = created.Secret
		fmt.Fprintln(e.stderr, e.t("login.created_key", "name", created.Token.Name))
	}
	ic, err := e.ingestClient(base, tok)
	if err != nil {
		return err
	}
	info, err := ic.Info(ctx)
	if err != nil {
		return userErr(e.t("login.key_does_not_work", "reason", friendly(e, err)))
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
	fmt.Fprintln(e.stdout, e.t("login.connected", "name", disc.Publisher.Name, "key", info.Token.Name))
	fmt.Fprintln(e.stderr, e.t("login.stored", "path", e.configPath()))
	return nil
}

func defineLogout(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	return func(ctx context.Context, pos []string) (int, error) {
		cfg, err := e.loadConfig()
		if err != nil {
			return exitFailed, err
		}
		base := cfg.Default
		if len(pos) > 0 {
			if base, err = e.portalBase(pos[0]); err != nil {
				return exitFailed, err
			}
		}
		if _, ok := cfg.Nodes[base]; !ok || base == "" {
			fmt.Fprintln(e.stdout, e.t("logout.nothing", "portal", base))
			return exitOK, nil
		}
		delete(cfg.Nodes, base)
		if cfg.Default == base {
			cfg.Default = ""
		}
		if err := e.saveConfig(cfg); err != nil {
			return exitFailed, err
		}
		fmt.Fprintln(e.stdout, e.t("logout.done", "portal", base))
		return exitOK, nil
	}
}

func defineInfo(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	u := fs.String("url", "", e.t("flag.url"))
	tok := fs.String("token", "", e.t("flag.token"))
	asJSON := fs.Bool("json", false, e.t("flag.json"))
	return func(ctx context.Context, pos []string) (int, error) {
		if len(pos) > 0 {
			return exitFailed, usageError{e.t("main.no_arguments")}
		}
		base, token, err := e.target(*u, *tok)
		if err != nil {
			return exitFailed, err
		}
		ic, err := e.ingestClient(base, token)
		if err != nil {
			return exitFailed, err
		}
		info, err := ic.Info(ctx)
		if err != nil {
			return exitFailed, err
		}
		if *asJSON {
			enc := json.NewEncoder(e.stdout)
			enc.SetIndent("", "  ")
			return exitOK, enc.Encode(info)
		}
		w := e.stdout
		var brands []string
		for _, b := range info.Brands {
			brands = append(brands, fmt.Sprintf("%s (%s)", b.Name, b.ExternalID))
		}
		rows := [][2]string{
			{e.t("info.portal"), base},
			{e.t("info.key"), info.Token.Name + " (" + strings.Join(info.Token.Scopes, ", ") + ")"},
			{e.t("info.brands"), strings.Join(brands, ", ")},
			{e.t("info.languages"), strings.Join(info.Languages, ", ")},
		}
		if info.Renditions.ServerGenerated {
			rows = append(rows, [2]string{e.t("info.images"), e.t("info.images_portal")})
		} else {
			rows = append(rows, [2]string{e.t("info.images"), e.t("info.images_obx")})
		}
		rows = append(rows, [2]string{e.t("info.limits"), e.t("info.limits_value", "size", humanBytes(info.Limits.MaxUploadBytes), "rows", info.Limits.MaxBatchOperations)})
		if info.Token.ExpiresAt != nil {
			rows = append(rows, [2]string{e.t("info.expires"), info.Token.ExpiresAt.Format("2006-01-02")})
		}
		width := 0
		for _, r := range rows {
			width = max(width, len([]rune(r[0])))
		}
		for _, r := range rows {
			fmt.Fprintf(w, "%s:%s %s\n", r[0], strings.Repeat(" ", width-len([]rune(r[0]))), r[1])
		}
		return exitOK, nil
	}
}

var _ = register(map[string]string{
	"cmd.login.about": "Connects this computer to your portal, so the other commands know where to publish. Paste an access key (in your portal: Automatic upload → Access keys → New access key), or sign in with your admin e-mail address and password and obx creates a key for this computer.\n" +
		"The key is stored in your settings file, readable only by you (see \"obx config\"). In scripts and CI, set OBX_URL and OBX_TOKEN instead.",
	"cmd.logout.about":        "Removes the stored access key for a portal from this computer. The key itself keeps working until you switch it off in your portal.",
	"cmd.info.about":          "Checks that the portal address and access key work, and shows the brands, content languages and limits of the portal.",
	"login.flag_token":        "an access key from your portal (starts with obxi_); read from OBX_TOKEN when empty",
	"login.flag_email":        "sign in with your portal e-mail address and password instead; obx then creates an access key for this computer",
	"login.one_address":       "give one portal address.",
	"login.not_a_portal":      "{portal} does not look like an Open Brand Exchange portal: {reason}",
	"login.portal":            "Portal: {name} ({portal})",
	"login.ask_key":           "Paste your access key (starts with obxi_),\nor press Enter to sign in with your portal e-mail address and password:",
	"login.ask_email":         "E-mail address:",
	"login.ask_password":      "Password:",
	"login.nothing_given":     "no access key and no e-mail address given.",
	"login.sign_in_failed":    "signing in failed: {reason}",
	"login.create_key_failed": "creating an access key failed: {reason} (only admins can create keys)",
	"login.created_key":       "Created access key \"{name}\". You can switch it off in your portal under Automatic upload → Access keys.",
	"login.key_does_not_work": "the access key does not work: {reason}",
	"login.connected":         "Connected to {name} with access key \"{key}\". You can now run \"obx push\".",
	"login.stored":            "The key is stored in {path} (readable only by you).",
	"logout.nothing":          "Nothing stored for {portal}.",
	"logout.done":             "Forgot the access key for {portal}. The key keeps working elsewhere until you switch it off in your portal.",
	"main.no_arguments":       "this command takes no arguments.",
	"info.portal":             "Portal",
	"info.key":                "Access key",
	"info.brands":             "Brands",
	"info.languages":          "Languages",
	"info.images":             "Images",
	"info.images_portal":      "the portal makes the web and thumbnail versions itself",
	"info.images_obx":         "obx makes the web and thumbnail versions before uploading",
	"info.limits":             "Limits",
	"info.limits_value":       "{size} per file, {rows} rows per batch",
	"info.expires":            "Key valid until",
})
