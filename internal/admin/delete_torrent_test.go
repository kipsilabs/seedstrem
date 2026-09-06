package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// seedTorrent inserts a torrent into both the store and the fake download
// client so it can be exercised by the delete endpoint.
func seedTorrent(t *testing.T, e *env, id, hash string) {
	t.Helper()
	e.fake.Put(&fake.Torrent{Hash: hash, Name: "Example", State: "downloading"})
	if err := e.store.InsertTorrent(context.Background(), store.Torrent{
		ID:    id,
		Hash:  hash,
		Name:  "Example",
		Phase: store.PhaseAdded,
	}); err != nil {
		t.Fatalf("seed torrent: %v", err)
	}
}

func TestDeleteTorrent(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	seedTorrent(t, e, "id-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	w := e.do(t, http.MethodDelete, "/torrents/id-1", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d %s, want 204", w.Code, w.Body.String())
	}

	// Gone from the store.
	if _, err := e.store.TorrentByID(context.Background(), "id-1"); err == nil {
		t.Fatal("torrent still present in store after delete")
	}
	// Gone from the download client.
	if e.fake.Get("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") != nil {
		t.Fatal("torrent still present in download client after delete")
	}
}

func TestDeleteTorrentNotFound(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	w := e.do(t, http.MethodDelete, "/torrents/missing", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete missing: got %d %s, want 404", w.Code, w.Body.String())
	}
}

func TestDeleteTorrentRequiresSession(t *testing.T) {
	e := newEnv(t)
	// No login.
	w := e.do(t, http.MethodDelete, "/torrents/id-1", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("delete without session: got %d, want 401", w.Code)
	}
}
