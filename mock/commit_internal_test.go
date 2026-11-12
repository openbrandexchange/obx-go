package mock

import (
	"context"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"testing"

	"github.com/openbrandexchange/obx-go/ingest"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/specdata"
)

// The incremental commit (only changed records and the children of changed
// products) writes exactly the change log that comparing every record on
// every commit writes, for a mix of imports, publication changes, patches,
// asset order changes and deletes.
func TestIncrementalCommitEqualsFullCommit(t *testing.T) {
	run := func(full bool) ([]string, map[string]string) {
		n := New(Options{Fixtures: true})
		n.st.fullCommits = full
		srv := httptest.NewServer(n)
		defer srv.Close()
		ctx := context.Background()
		ic, _ := ingest.New(srv.URL, n.Credentials().IngestToken)
		csv, err := fs.ReadFile(specdata.Fixtures(), "kestrel/products.csv")
		if err != nil {
			t.Fatal(err)
		}
		must := func(what string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s (full=%v): %v", what, full, err)
			}
		}
		// Re-import unchanged, then with an extra variant and a renamed product.
		for _, data := range [][]byte{csv, append(append([]byte(nil), csv...), []byte("Kestrel,KO-1001,Merino Crew Sweater (new),Merino trui,,,sweater,unisex,AW26,Highland,main: 100% merino_wool,,PT,,2000100109997,,NVY,Navy,Marineblauw,,XXL,INT\n")...)} {
			job, err := ic.Import(ctx, ingest.ImportRequest{CSV: &ingest.File{Name: "products.csv", Data: data}})
			must("import", err)
			_, err = ic.WaitJob(ctx, job.ID, 0)
			must("wait", err)
		}
		_, err = ic.Unpublish(ctx, obx.KindProduct, "KO-1002")
		must("unpublish", err)
		_, err = ic.Patch(ctx, obx.KindAsset, "KO-1001_NVY_1", map[string]any{"alt_text": map[string]string{"en": "Front"}}, nil)
		must("patch asset", err)
		_, err = ic.Publish(ctx, obx.KindProduct, "KO-1002")
		must("publish", err)
		_, err = ic.SetAssetOrder(ctx, "KO-1001", obx.AssetOrder{ColorCode: obx.Ptr("NVY"), AssetExternalIDs: []string{"KO-1001_NVY_2", "KO-1001_NVY_1"}})
		must("asset order", err)
		_, err = ic.Delete(ctx, obx.KindVariant, "2000100109997")
		must("delete variant", err)
		_, err = ic.Delete(ctx, obx.KindProduct, "KO-1006")
		must("delete product", err)

		n.mu.Lock()
		defer n.mu.Unlock()
		var log []string
		for _, e := range n.st.log {
			ext := e.extID
			if e.typ == obx.ChangeGrant {
				ext = "grant"
			}
			log = append(log, fmt.Sprintf("%s %s %s", e.op, e.typ, ext))
		}
		// Every stored representation hash is current (ids differ between
		// nodes, so hashes are compared within one node).
		stale := map[string]string{}
		for _, t := range []map[string]*record{n.st.brands, n.st.products, n.st.variants, n.st.assets} {
			for _, r := range t {
				if h := n.st.reprHash(r); h != r.protoHash {
					stale[r.kind+"/"+r.extID] = "stale"
				}
			}
		}
		return log, stale
	}
	incLog, incStale := run(false)
	fullLog, fullStale := run(true)
	if len(incLog) != len(fullLog) {
		t.Fatalf("incremental log has %d entries, full log %d", len(incLog), len(fullLog))
	}
	for i := range incLog {
		if incLog[i] != fullLog[i] {
			t.Fatalf("entry %d: incremental %q, full %q", i, incLog[i], fullLog[i])
		}
	}
	if len(incStale) > 0 || len(fullStale) > 0 {
		t.Errorf("records with a stale representation hash: incremental %v, full %v", incStale, fullStale)
	}
	if len(incLog) < 20 {
		t.Fatalf("the scenario wrote only %d entries", len(incLog))
	}
}

func TestNoneMatchIsWeak(t *testing.T) {
	for _, c := range []struct {
		header string
		want   bool
	}{
		{`"abc"`, true}, {`W/"abc"`, true}, {`"x", W/"abc"`, true}, {`*`, true}, {`"abd"`, false}, {``, false},
	} {
		if got := noneMatch(c.header, `"abc"`); got != c.want {
			t.Errorf("If-None-Match %q: %t, want %t", c.header, got, c.want)
		}
	}
}
