package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/store"
)

func TestDeletionsListing(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	if err := e.store.RecordDeletion(context.Background(), store.DeletionEvent{
		TorrentID: "T1", Hash: "aabb", Name: "Example", Indexer: "MyIndexer",
		Origin: store.OriginNative, Reason: store.DeleteReasonSeedTime,
		SeedingTime: 49 * time.Hour, SeedLimit: 48 * time.Hour,
		Ratio: 1.5, RatioLimit: 2, Progress: 1, FilesDeleted: true,
	}); err != nil {
		t.Fatal(err)
	}

	w := e.do(t, http.MethodGet, "/deletions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("deletions = %d body=%s", w.Code, w.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d: %v", len(items), items)
	}
	it := items[0]
	if it["reason"] != store.DeleteReasonSeedTime {
		t.Errorf("reason = %v, want %q", it["reason"], store.DeleteReasonSeedTime)
	}
	if it["name"] != "Example" || it["indexer"] != "MyIndexer" {
		t.Errorf("identity = %v/%v, want Example/MyIndexer", it["name"], it["indexer"])
	}
	// Durations serialize as seconds, matching the torrents listing.
	if got := it["seeding_time"]; got != float64(49*3600) {
		t.Errorf("seeding_time = %v, want %d", got, 49*3600)
	}
	if got := it["seed_limit"]; got != float64(48*3600) {
		t.Errorf("seed_limit = %v, want %d", got, 48*3600)
	}
	if it["files_deleted"] != true {
		t.Errorf("files_deleted = %v, want true", it["files_deleted"])
	}
	if it["deleted_at"] == nil {
		t.Error("deleted_at missing")
	}
}

func TestDeletionsRequiresAuth(t *testing.T) {
	e := newEnv(t)

	w := e.do(t, http.MethodGet, "/deletions", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("deletions without session = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// An empty history must serialize as [], not null: the UI maps over it.
func TestDeletionsEmptyIsEmptyArray(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	w := e.do(t, http.MethodGet, "/deletions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("deletions = %d", w.Code)
	}
	if got := w.Body.String(); got != "[]\n" && got != "[]" {
		t.Errorf("empty body = %q, want []", got)
	}
}
