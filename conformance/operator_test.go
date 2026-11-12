package conformance_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// mockOperator does the brand-side steps the way a node without the Node
// Admin and Ingest APIs would do them internally. Here that happens to be the
// mock's own admin and ingest APIs, which the suite itself is not given.
func mockOperator(t *testing.T, n *mock.Node, base string) conformance.Operator {
	creds := n.Credentials()
	ctx := context.Background()
	admin, err := nodeadmin.New(base, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Login(ctx, creds.AdminEmail, creds.AdminPassword); err != nil {
		t.Fatal(err)
	}
	ing, err := ingest.New(base, creds.IngestToken)
	if err != nil {
		t.Fatal(err)
	}
	brandIDs := func(ctx context.Context, s string) (*[]string, error) {
		if s == "all" {
			return nil, nil
		}
		var ids []string
		for _, ext := range strings.Split(s, ",") {
			id, err := admin.BrandID(ctx, ext)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return &ids, nil
	}
	clientByID := func(ctx context.Context, clientID string) (*nodeadmin.APIClient, error) {
		var out obx.Items[nodeadmin.APIClient]
		if err := admin.Call(ctx, http.MethodGet, obx.PathAdminAPI+"/api-clients", nil, &out); err != nil {
			return nil, err
		}
		for _, c := range out.Items {
			if c.ClientID == clientID {
				return &c, nil
			}
		}
		return nil, fmt.Errorf("no API key %s", clientID)
	}
	return conformance.OperatorFunc(func(ctx context.Context, a conformance.Action) (map[string]string, error) {
		p := a.Params
		switch a.Name {
		case conformance.ActCreateAPIKey:
			brands, err := brandIDs(ctx, p["brands"])
			if err != nil {
				return nil, err
			}
			originals := p["originals"] == "true"
			rt, err := admin.CreateRetailer(ctx, nodeadmin.RetailerInput{Name: p["name"], BrandIDs: brands, AllowOriginal: &originals})
			if err != nil {
				return nil, err
			}
			scopes := []string{obx.ScopeCatalogRead, obx.ScopeMediaRead, obx.ScopeUsageWrite}
			if originals {
				scopes = append(scopes, obx.ScopeMediaOriginal)
			}
			c, err := admin.CreateAPIClient(ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: p["name"], Scopes: scopes})
			if err != nil {
				return nil, err
			}
			return map[string]string{"client_id": c.Client.ClientID, "client_secret": c.ClientSecret}, nil
		case conformance.ActRevokeAPIKey:
			c, err := clientByID(ctx, p["client_id"])
			if err != nil {
				return nil, err
			}
			return nil, admin.RevokeAPIClient(ctx, c.ID)
		case conformance.ActApproveAccessRequest:
			_, err := admin.Approve(ctx, p["request_id"], nodeadmin.ApproveInput{CreateAPIClient: obx.Ptr(true)})
			return nil, err
		case conformance.ActSetGrant:
			c, err := clientByID(ctx, p["client_id"])
			if err != nil {
				return nil, err
			}
			brands, err := brandIDs(ctx, p["brands"])
			if err != nil {
				return nil, err
			}
			_, err = admin.PatchRetailer(ctx, c.RetailerID, map[string]any{"brand_ids": brands})
			return nil, err
		case conformance.ActUpdateProduct:
			_, err := ing.Patch(ctx, obx.KindProduct, p["external_id"], map[string]any{p["field"]: p["value"]}, nil)
			return nil, err
		case conformance.ActUnpublishProduct:
			_, err := ing.Unpublish(ctx, obx.KindProduct, p["external_id"])
			return nil, err
		case conformance.ActPublishProduct:
			_, err := ing.Publish(ctx, obx.KindProduct, p["external_id"])
			return nil, err
		case conformance.ActSimulateSpecUpgrade:
			n.SimulateSpecUpgrade()
			return map[string]string{"simulated": "true"}, nil
		}
		return nil, errors.New("unknown action " + a.Name)
	})
}

// A node with the fixtures loaded by its own import, no admin credentials and
// no upload key passes the protocol profile with an operator. Only P-FEED-5
// (the ingest replay) is skipped.
func TestProtocolWithOperatorOnly(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true, AllowPrivateURLs: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	var actions []string
	op := mockOperator(t, n, srv.URL)
	counted := conformance.OperatorFunc(func(ctx context.Context, a conformance.Action) (map[string]string, error) {
		actions = append(actions, a.Name)
		return op.Do(ctx, a)
	})
	rep, err := conformance.Run(context.Background(), []string{conformance.ProfileProtocol}, conformance.Config{BaseURL: srv.URL, Operator: counted})
	if err != nil {
		t.Fatal(err)
	}
	if rep.SetupError != "" {
		t.Fatalf("setup: %s", rep.SetupError)
	}
	for _, r := range rep.Results {
		switch {
		case r.ID == "P-FEED-5" || r.ID == "P-USE-1":
			if r.Status == conformance.StatusFail {
				t.Errorf("%s failed: %s", r.ID, strings.Join(r.Messages, "; "))
			}
		case r.Status != conformance.StatusPass:
			t.Errorf("%s %s: %s\n    %s", r.ID, r.Status, r.Title, strings.Join(r.Messages, "\n    "))
		}
	}
	for _, want := range []string{conformance.ActCreateAPIKey, conformance.ActRevokeAPIKey, conformance.ActApproveAccessRequest, conformance.ActSetGrant, conformance.ActUpdateProduct, conformance.ActUnpublishProduct, conformance.ActPublishProduct, conformance.ActSimulateSpecUpgrade} {
		found := false
		for _, a := range actions {
			found = found || a == want
		}
		if !found {
			t.Errorf("the suite never asked the operator to %s", want)
		}
	}
}

// Without an operator, admin credentials or upload key the brand-side tests
// are skipped, never failed.
func TestProtocolWithoutOperatorSkips(t *testing.T) {
	n := mock.New(mock.Options{Fixtures: true, AllowPrivateURLs: true})
	srv := httptest.NewServer(n)
	defer srv.Close()
	c := n.Credentials()
	rep, err := conformance.Run(context.Background(), []string{conformance.ProfileProtocol}, conformance.Config{BaseURL: srv.URL,
		ClientID: c.Full.ClientID, ClientSecret: c.Full.ClientSecret,
		SecondClientID: c.Restricted.ClientID, SecondClientSecret: c.Restricted.ClientSecret,
		NoOriginalClientID: c.NoOriginals.ClientID, NoOriginalClientSecret: c.NoOriginals.ClientSecret})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Status == conformance.StatusFail {
			t.Errorf("%s failed: %s", r.ID, strings.Join(r.Messages, "; "))
		}
	}
	skipped := map[string]bool{}
	for _, r := range rep.Results {
		if r.Status == conformance.StatusSkip {
			skipped[r.ID] = true
		}
	}
	for _, id := range []string{"P-AUTH-2", "P-ACC-1", "P-FEED-6", "P-FEED-7"} {
		if !skipped[id] {
			t.Errorf("%s should be skipped without an operator", id)
		}
	}
}

// The hook gets the action as its argument and as JSON on stdin, and its
// stdout is the result.
func TestHookOperator(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/hook.sh"
	body := "#!/bin/sh\nread -r in\ncase \"$1\" in\n  create_api_key) echo \"$in\" | grep -q '\"name\":\"x\"' && echo '{\"client_id\":\"c1\",\"client_secret\":\"s1\"}' ;;\n  revoke_api_key) exit 3 ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	h := conformance.HookOperator{Path: script}
	res, err := h.Do(context.Background(), conformance.Action{Name: conformance.ActCreateAPIKey, Params: map[string]string{"name": "x"}})
	if err != nil || res["client_id"] != "c1" || res["client_secret"] != "s1" {
		t.Fatalf("create_api_key: %v %v", res, err)
	}
	if _, err := h.Do(context.Background(), conformance.Action{Name: conformance.ActRevokeAPIKey}); err == nil {
		t.Fatal("a failing hook must return an error")
	}
}

// P-FEED-7 is skipped when the operator does not offer simulate_spec_upgrade
// (ErrActionNotSupported, or no simulated: true as from a hook that prints
// nothing), passes when it expires the cursors and fails when old cursors
// keep working.
func TestSpecUpgradeCursor(t *testing.T) {
	cases := []struct {
		name string
		do   func(n *mock.Node) (map[string]string, error)
		want string
	}{
		{"not offered", func(*mock.Node) (map[string]string, error) { return nil, conformance.ErrActionNotSupported }, conformance.StatusSkip},
		{"no result", func(*mock.Node) (map[string]string, error) { return nil, nil }, conformance.StatusSkip},
		{"upgraded", func(n *mock.Node) (map[string]string, error) {
			n.SimulateSpecUpgrade()
			return map[string]string{"simulated": "true"}, nil
		}, conformance.StatusPass},
		{"cursors survive", func(*mock.Node) (map[string]string, error) { return map[string]string{"simulated": "true"}, nil }, conformance.StatusFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := mock.New(mock.Options{AllowPrivateURLs: true})
			srv := httptest.NewServer(n)
			defer srv.Close()
			op := conformance.OperatorFunc(func(_ context.Context, a conformance.Action) (map[string]string, error) {
				if a.Name != conformance.ActSimulateSpecUpgrade {
					return nil, conformance.ErrActionNotSupported
				}
				return tc.do(n)
			})
			rep, err := conformance.Run(context.Background(), []string{conformance.ProfileProtocol}, conformance.Config{BaseURL: srv.URL, SetupCode: n.SetupCode(), Operator: op, Only: []string{"P-FEED-7"}})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rep.Results {
				if r.ID == "P-FEED-7" {
					if r.Status != tc.want {
						t.Errorf("P-FEED-7 %s, want %s: %s", r.Status, tc.want, strings.Join(r.Messages, "; "))
					}
					return
				}
			}
			t.Fatal("P-FEED-7 did not run")
		})
	}
}
