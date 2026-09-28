package mock_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openbrandexchange/obx-go/client"
	"github.com/openbrandexchange/obx-go/mock"
)

func TestSmoke(t *testing.T) {
	start := time.Now()
	n := mock.New(mock.Options{Fixtures: true})
	t.Logf("fixtures loaded in %s", time.Since(start))
	srv := httptest.NewServer(n)
	defer srv.Close()
	creds := n.Credentials()
	c, _ := client.New(client.Config{BaseURL: srv.URL, ClientID: creds.Full.ClientID, ClientSecret: creds.Full.ClientSecret})
	counts := map[string]int{}
	err := c.Sync(context.Background(), nil, client.SyncOptions{Limit: 7}, func(ctx context.Context, ev client.Event) error {
		if ev.Kind == client.EventChange && ev.Phase == "snapshot" {
			counts[ev.Change.Type]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("counts %v", counts)
}
