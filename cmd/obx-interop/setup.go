package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// creds are what the interop run needs from a node. The file is written
// with mode 0600 and holds real secrets of a throwaway test node.
type creds struct {
	URL           string `json:"url"`
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password"`
	IngestToken   string `json:"ingest_token"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
}

// setup completes first-run setup the same way on every node (the
// conformance fixture setup: Kestrel Outfitters, brand "kestrel",
// languages nl,en) and creates an upload key and an API key that sees
// every brand, originals included.
func setup(ctx context.Context, base, code string) (*creds, error) {
	hc := &http.Client{Timeout: 2 * time.Minute}
	admin, err := nodeadmin.New(base, hc)
	if err != nil {
		return nil, err
	}
	pw := make([]byte, 12)
	rand.Read(pw)
	c := &creds{URL: base, AdminEmail: "interop@kestrel.example", AdminPassword: "interop-" + hex.EncodeToString(pw)}
	if _, err := admin.Setup(ctx, nodeadmin.SetupInput{SetupCode: code,
		Company: nodeadmin.SetupCompany{Name: "Kestrel Outfitters", FirstBrandName: "Kestrel", DefaultLanguage: "nl", Languages: []string{"nl", "en"}},
		Admin:   nodeadmin.SetupAdmin{Name: "Interop Admin", Email: c.AdminEmail, Password: c.AdminPassword}}); err != nil {
		return nil, fmt.Errorf("first-run setup: %w", err)
	}
	if _, err := admin.Login(ctx, c.AdminEmail, c.AdminPassword); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	tok, err := admin.CreateIngestToken(ctx, nodeadmin.IngestTokenInput{Name: "Interop", Scopes: []string{obx.IngestScopeRead, obx.IngestScopeWrite, obx.IngestScopeDelete}})
	if err != nil {
		return nil, fmt.Errorf("create upload key: %w", err)
	}
	c.IngestToken = tok.Secret
	rt, err := admin.CreateRetailer(ctx, nodeadmin.RetailerInput{Name: "Interop Retailer"})
	if err != nil {
		return nil, fmt.Errorf("create retailer: %w", err)
	}
	key, err := admin.CreateAPIClient(ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: "Interop software",
		Scopes: []string{obx.ScopeCatalogRead, obx.ScopeMediaRead, obx.ScopeMediaOriginal, obx.ScopeUsageWrite}})
	if err != nil {
		return nil, fmt.Errorf("create API key: %w", err)
	}
	c.ClientID, c.ClientSecret = key.Client.ClientID, key.ClientSecret
	return c, nil
}
