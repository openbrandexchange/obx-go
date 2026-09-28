package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openbrandexchange/obx-go/obx"
)

// Changes reads one page of the change feed. An empty cursor starts a
// snapshot.
func (c *Client) Changes(ctx context.Context, cursor string, limit int) (*obx.ChangesPage, error) {
	v := url.Values{}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	if limit > 0 {
		v.Set("limit", fmt.Sprint(limit))
	}
	path := "/changes"
	if enc := v.Encode(); enc != "" {
		path += "?" + enc
	}
	var p obx.ChangesPage
	if err := c.getJSON(ctx, path, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// CursorStore persists the change-feed cursor between runs. Save is called
// after every page has been handled, so a crash replays at most one page
// (the feed is at-least-once anyway).
type CursorStore interface {
	Load(ctx context.Context) (string, error)
	Save(ctx context.Context, cursor string) error
}

// MemoryCursorStore keeps the cursor in memory.
type MemoryCursorStore struct {
	mu     sync.Mutex
	cursor string
}

// Load returns the cursor.
func (m *MemoryCursorStore) Load(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursor, nil
}

// Save stores the cursor.
func (m *MemoryCursorStore) Save(_ context.Context, c string) error {
	m.mu.Lock()
	m.cursor = c
	m.mu.Unlock()
	return nil
}

// FileCursorStore keeps the cursor in a file, written atomically.
type FileCursorStore string

// Load returns the stored cursor or "" when the file does not exist.
func (f FileCursorStore) Load(context.Context) (string, error) {
	b, err := os.ReadFile(string(f))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// Save writes the cursor.
func (f FileCursorStore) Save(_ context.Context, c string) error {
	dir := filepath.Dir(string(f))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cursor-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(c + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), string(f))
}

// EventKind says what a sync Event is about.
type EventKind int

// Event kinds.
const (
	// EventChange carries one change entry (upsert or delete).
	EventChange EventKind = iota
	// EventSnapshotStarted: the feed restarted from scratch. Remember what
	// you see until EventSnapshotComplete so you can remove the rest.
	EventSnapshotStarted
	// EventSnapshotComplete: every visible object has been sent since
	// EventSnapshotStarted; objects not seen since then are gone (mark and
	// sweep).
	EventSnapshotComplete
	// EventResync: the node asked for a restart (grant changed, or the cursor
	// expired). A new snapshot follows.
	EventResync
	// EventCaughtUp: has_more is false; the consumer is up to date.
	EventCaughtUp
)

func (k EventKind) String() string {
	switch k {
	case EventChange:
		return "change"
	case EventSnapshotStarted:
		return "snapshot_started"
	case EventSnapshotComplete:
		return "snapshot_complete"
	case EventResync:
		return "resync"
	case EventCaughtUp:
		return "caught_up"
	}
	return "unknown"
}

// Event is passed to the sync handler.
type Event struct {
	Kind   EventKind
	Change *obx.Change // for EventChange
	Phase  string      // phase of the page the event came from
	Reason string      // for EventResync: "grant" or "cursor_expired"
}

// SyncOptions control Sync.
type SyncOptions struct {
	// Limit is the page size (default 500).
	Limit int
	// Follow keeps polling after catching up instead of returning.
	Follow bool
	// PollInterval between polls when following (default 60 s).
	PollInterval time.Duration
	// Wake, when set, makes a follower poll immediately (for example from a
	// changes.available webhook).
	Wake <-chan struct{}
	// SkipUnknownTypes drops entries with a type this package does not know
	// (the spec requires consumers to ignore them). Default true.
	KeepUnknownTypes bool
}

// knownTypes are the change types a consumer understands.
var knownTypes = map[string]bool{obx.ChangeBrand: true, obx.ChangeProduct: true, obx.ChangeVariant: true, obx.ChangeAsset: true, obx.ChangeGrant: true}

// Sync reads the change feed from the stored cursor and calls handle for
// every event. It handles snapshots, grant resyncs and expired cursors
// (restart without cursor) and saves the cursor after each page. It returns
// nil once caught up (unless opts.Follow), or the first handler error.
func (c *Client) Sync(ctx context.Context, store CursorStore, opts SyncOptions, handle func(context.Context, Event) error) error {
	if store == nil {
		store = &MemoryCursorStore{}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = 60 * time.Second
	}
	cursor, err := store.Load(ctx)
	if err != nil {
		return err
	}
	inSnapshot := false
	restart := func(reason string) error {
		if err := handle(ctx, Event{Kind: EventResync, Reason: reason}); err != nil {
			return err
		}
		cursor = ""
		return store.Save(ctx, "")
	}
	for {
		if cursor == "" && !inSnapshot {
			inSnapshot = true
			if err := handle(ctx, Event{Kind: EventSnapshotStarted, Phase: obx.PhaseSnapshot}); err != nil {
				return err
			}
		}
		page, err := c.Changes(ctx, cursor, limit)
		if obx.IsCode(err, obx.ErrCursorExpired) || (obx.IsCode(err, obx.ErrCursorInvalid) && cursor != "") {
			inSnapshot = false
			if err := restart("cursor_expired"); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if inSnapshot && page.Phase == obx.PhaseLive {
			inSnapshot = false
			if err := handle(ctx, Event{Kind: EventSnapshotComplete, Phase: page.Phase}); err != nil {
				return err
			}
		}
		resync := false
		for i := range page.Changes {
			ch := &page.Changes[i]
			if !opts.KeepUnknownTypes && !knownTypes[ch.Type] {
				continue
			}
			if ch.Type == obx.ChangeGrant && ch.IsResync() {
				resync = true
				break
			}
			if err := handle(ctx, Event{Kind: EventChange, Change: ch, Phase: page.Phase}); err != nil {
				return err
			}
		}
		if resync {
			inSnapshot = false
			if err := restart("grant"); err != nil {
				return err
			}
			continue
		}
		if page.NextCursor != "" {
			cursor = page.NextCursor
			if err := store.Save(ctx, cursor); err != nil {
				return err
			}
		}
		if page.HasMore {
			continue
		}
		if err := handle(ctx, Event{Kind: EventCaughtUp, Phase: page.Phase}); err != nil {
			return err
		}
		if !opts.Follow {
			return nil
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-opts.Wake:
			t.Stop()
		case <-t.C:
		}
	}
}
