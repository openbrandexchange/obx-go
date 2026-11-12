package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// An Operator does, on the brand's side, the few things the protocol tests
// need changed while they run: create, restrict and revoke an API key,
// approve an access request, change or unpublish a product.
//
// The OBX standard is the read side (the protocol profile). How a node gets
// its data and how a brand manages retailers is up to the implementation, so
// a node does not need the Ingest API or the Node Admin API to be
// conformant. Nodes that have those APIs let the suite do these steps itself;
// for every other node an Operator does them, in the node's own way: a Go
// function in the node's tests, a script (HookOperator) or a person
// (PromptOperator).
//
// The node must hold the fixtures/kestrel catalog (loaded through its own
// import) before the run.
type Operator interface {
	Do(ctx context.Context, a Action) (map[string]string, error)
}

// OperatorFunc adapts a function to Operator.
type OperatorFunc func(ctx context.Context, a Action) (map[string]string, error)

// Do calls f.
func (f OperatorFunc) Do(ctx context.Context, a Action) (map[string]string, error) { return f(ctx, a) }

// Action names and their parameters. Brands are external ids ("kestrel",
// "kestrel-trail") or "all"; products are external ids from fixtures/kestrel.
const (
	// ActCreateAPIKey: a new retailer with one API key. Params name, brands,
	// originals ("true" or "false"). Result client_id and client_secret.
	ActCreateAPIKey = "create_api_key"
	// ActRevokeAPIKey: params client_id.
	ActRevokeAPIKey = "revoke_api_key"
	// ActApproveAccessRequest: approve with API access. Params request_id,
	// retailer_name.
	ActApproveAccessRequest = "approve_access_request"
	// ActSetGrant: change which brands the retailer of an API key sees.
	// Params client_id, brands.
	ActSetGrant = "set_grant"
	// ActUpdateProduct: params external_id, field ("season"), value.
	ActUpdateProduct = "update_product"
	// ActUnpublishProduct and ActPublishProduct: params external_id.
	ActUnpublishProduct = "unpublish_product"
	ActPublishProduct   = "publish_product"
	// ActSimulateSpecUpgrade: do to the node what an upgrade to a spec version
	// with incompatible shapes (0.2 → 0.3) does to its cursors: every cursor
	// issued so far must answer 410 cursor_expired (protocol.md 6.4), for
	// example by bumping a cursor epoch. No params. Result simulated: "true"
	// when done. An operator that does not offer it returns
	// ErrActionNotSupported, or no "true" (a hook that prints nothing, a
	// person who presses Enter), and P-FEED-7 is skipped.
	ActSimulateSpecUpgrade = "simulate_spec_upgrade"
)

// ErrActionNotSupported is what an Operator returns for an optional action
// it does not offer (ActSimulateSpecUpgrade); the test that needs it is
// skipped.
var ErrActionNotSupported = errors.New("conformance: the operator does not offer this action")

// Action is one step for the operator.
type Action struct {
	Name   string            `json:"action"`
	Params map[string]string `json:"params"`
	// Result lists the keys the operator must return.
	Result []string `json:"result,omitempty"`
	// Hint says in words what to do.
	Hint string `json:"hint"`
}

// HookOperator runs a program for every action: the action name is its only
// argument, the Action as JSON on stdin, and it prints a JSON object with
// the result keys on stdout (or nothing). A non-zero exit fails the test.
type HookOperator struct {
	Path   string
	Stderr io.Writer
}

// Do runs the hook.
func (h HookOperator) Do(ctx context.Context, a Action) (map[string]string, error) {
	in, _ := json.Marshal(a)
	cmd := exec.CommandContext(ctx, h.Path, a.Name)
	cmd.Stdin = bytes.NewReader(in)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = h.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("operator hook %s %s: %w", h.Path, a.Name, err)
	}
	res := map[string]string{}
	if b := bytes.TrimSpace(out.Bytes()); len(b) > 0 {
		if err := json.Unmarshal(b, &res); err != nil {
			return nil, fmt.Errorf("operator hook %s %s: stdout is not a JSON object of strings: %w", h.Path, a.Name, err)
		}
	}
	return res, nil
}

// PromptOperator asks a person: it prints each action and reads the result
// values (or Enter when there are none) from In.
type PromptOperator struct {
	In  io.Reader
	Out io.Writer
	rd  *bufio.Reader
}

// Do asks.
func (p *PromptOperator) Do(_ context.Context, a Action) (map[string]string, error) {
	if p.rd == nil {
		p.rd = bufio.NewReader(p.In)
	}
	fmt.Fprintf(p.Out, "\n>> operator step: %s\n   %s\n", a.Name, a.Hint)
	keys := make([]string, 0, len(a.Params))
	for k := range a.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(p.Out, "   %s: %s\n", k, a.Params[k])
	}
	res := map[string]string{}
	if len(a.Result) == 0 {
		fmt.Fprint(p.Out, "   Press Enter when done (or type fail): ")
		line, err := p.rd.ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("operator: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(line), "fail") {
			return nil, errors.New("operator: step reported as failed")
		}
		return res, nil
	}
	for _, k := range a.Result {
		fmt.Fprintf(p.Out, "   %s: ", k)
		line, err := p.rd.ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("operator: %w", err)
		}
		res[k] = strings.TrimSpace(line)
	}
	return res, nil
}

// ---- runner side

// canOperate reports whether brand-side steps can be done, by the admin API
// or an operator.
func (r *runner) canOperate() bool { return r.admin != nil || r.cfg.Operator != nil }

func (r *runner) operate(ctx context.Context, a Action) (map[string]string, error) {
	r.logf("operator: %s %v", a.Name, a.Params)
	res, err := r.cfg.Operator.Do(ctx, a)
	if err != nil {
		return nil, err
	}
	for _, k := range a.Result {
		if res[k] == "" {
			return nil, fmt.Errorf("operator %s returned no %s", a.Name, k)
		}
	}
	return res, nil
}

func brandsParam(brands []string) string {
	if brands == nil {
		return "all"
	}
	return strings.Join(brands, ",")
}

// operatorKey creates a retailer and API key through the operator. brands
// are external ids; nil means all brands.
func (r *runner) operatorKey(ctx context.Context, name string, brands []string, originals bool) (*apiKey, error) {
	res, err := r.operate(ctx, Action{Name: ActCreateAPIKey,
		Params: map[string]string{"name": name, "brands": brandsParam(brands), "originals": fmt.Sprint(originals)},
		Result: []string{"client_id", "client_secret"},
		Hint:   fmt.Sprintf("Create a retailer named %q that sees brands %s (originals: %v) and give it an API key with catalog:read and media:read (and media:original when originals is true).", name, brandsParam(brands), originals)})
	if err != nil {
		return nil, err
	}
	return r.newClient(res["client_id"], res["client_secret"]), nil
}

// newThrowawayKey is a fresh retailer with an API key for one test, made by
// the admin API or the operator.
func (t *T) newThrowawayKey(name string, brands []string) *apiKey {
	if t.r.admin != nil {
		var ids *[]string
		if brands != nil {
			b := make([]string, len(brands))
			for i, ext := range brands {
				b[i] = t.r.brandIDs[ext]
			}
			ids = &b
		}
		return t.r.throwawayClient(name, ids)
	}
	c, err := t.r.operatorKey(t.ctx, name, brands, false)
	t.Check(err, "create API key")
	return c
}

func (t *T) needOperator(what string) {
	if t.r.online() {
		t.onlineStop("%s needs a brand-side step", what)
	}
	if !t.r.canOperate() {
		t.Skip("%s needs admin credentials (--admin-user/--admin-password or --setup-code) or an operator (--operator-hook, --operator-prompt)", what)
	}
}

// opStep runs one brand-side step through the operator and fails the test
// when it does not work.
func (t *T) opStep(a Action) {
	_, err := t.r.operate(t.ctx, a)
	t.Check(err, "operator "+a.Name)
}

func productAction(name, ext, hint string, extra ...string) Action {
	p := map[string]string{"external_id": ext}
	for i := 0; i+1 < len(extra); i += 2 {
		p[extra[i]] = extra[i+1]
	}
	return Action{Name: name, Params: p, Hint: hint}
}
