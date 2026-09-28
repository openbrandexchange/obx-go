package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/mock"
	"github.com/openbrandexchange/obx-go/nodeadmin"
	"github.com/openbrandexchange/obx-go/obx"
)

// TestLoad pushes a seeded catalog of 10,000 products and 60,000 variants
// with obx push, pushes it again (nothing changed), and mirrors it with
// obx pull, twice. It is skipped unless OBX_LOAD=1 (it takes a minute or
// more); the numbers are written to the test log and NOTES.md keeps the last
// measured ones.
func TestLoad(t *testing.T) {
	if os.Getenv("OBX_LOAD") == "" {
		t.Skip("set OBX_LOAD=1 to run the load test")
	}
	const products, perProduct = 10000, 6
	h, tok := freshNode(t, mock.Options{})
	h.vars["OBX_URL"], h.vars["OBX_TOKEN"] = h.srv.URL, tok
	dir := t.TempDir()
	var b bytes.Buffer
	b.WriteString("brand,style_number,name:en,name:nl,item_type,color_code,color_name:en,size,gtin\n")
	colors := []string{"NVY", "BLK"}
	sizes := []string{"S", "M", "L"}
	n := 0
	for p := 0; p < products; p++ {
		for v := 0; v < perProduct; v++ {
			body := fmt.Sprintf("200%09d", n)
			d, err := obx.GTINCheckDigit(body)
			if err != nil {
				t.Fatal(err)
			}
			n++
			fmt.Fprintf(&b, "Kestrel,LT-%05d,Load test %d,Laadtest %d,sweater,%s,Colour,%s,%s%c\n", p, p, p, colors[v/3], sizes[v%3], body, d)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "products.csv"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	timed := func(what string, args ...string) (string, time.Duration) {
		start := time.Now()
		code, out, errOut := h.run(args...)
		took := time.Since(start)
		if code != exitOK {
			t.Fatalf("%s: exit %d\n%s\n%s", what, code, out, errOut)
		}
		t.Logf("%-28s %8s", what, took.Round(time.Millisecond))
		return out, took
	}
	out, _ := timed("push (10k products, 60k EAN)", "push", "--csv", filepath.Join(dir, "products.csv"))
	if !strings.Contains(out, fmt.Sprintf("rows: %d", products*perProduct)) {
		t.Fatalf("first push: %s", out)
	}
	out, _ = timed("push again (unchanged)", "push", "--csv", filepath.Join(dir, "products.csv"))
	if !strings.Contains(out, "new: 0 · updated: 0") {
		t.Fatalf("second push changed something: %s", out)
	}

	// A retailer mirrors it.
	ctx := context.Background()
	adm, _ := nodeadmin.New(h.srv.URL, nil)
	if _, err := adm.Login(ctx, "admin@kestrel.example", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	rt, err := adm.CreateRetailer(ctx, nodeadmin.RetailerInput{Name: "Load test retailer"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := adm.CreateAPIClient(ctx, nodeadmin.APIClientInput{RetailerID: rt.ID, Name: "load test"})
	if err != nil {
		t.Fatal(err)
	}
	mirror := t.TempDir()
	h.vars["OBX_CLIENT_ID"], h.vars["OBX_CLIENT_SECRET"] = key.Client.ClientID, key.ClientSecret
	timed("pull (snapshot)", "pull", h.srv.URL, "--out", mirror, "--renditions", "")
	variants, _ := os.ReadDir(filepath.Join(mirror, "variants"))
	if len(variants) != products*perProduct {
		t.Fatalf("pulled %d variants", len(variants))
	}
	timed("pull again (no changes)", "pull", h.srv.URL, "--out", mirror, "--renditions", "")
}
