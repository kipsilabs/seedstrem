package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/store"
)

const readdTorrent = "d4:infod6:lengthi1e4:name4:test12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"
const readdHash = "0123456789abcdef0123456789abcdef01234567"

func recordDeletion(t *testing.T, e *env, hash string) string {
	t.Helper()
	if err := e.store.RecordDeletion(context.Background(), store.DeletionEvent{
		TorrentID: "T1", Hash: hash, Name: "Example", Reason: store.DeleteReasonSeedTime,
	}); err != nil {
		t.Fatal(err)
	}
	ds, err := e.store.Deletions(context.Background())
	if err != nil || len(ds) == 0 {
		t.Fatalf("deletions: %v %v", ds, err)
	}
	return ds[0].ID
}

func TestDeletionsExposeHasTorrentFile(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	_ = e.store.SaveTorrentFile(context.Background(), readdHash, []byte(readdTorrent))
	recordDeletion(t, e, readdHash)

	w := e.do(t, http.MethodGet, "/deletions", "")
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatalf("decode: %v %s", err, w.Body.String())
	}
	if items[0]["has_torrent_file"] != true {
		t.Errorf("has_torrent_file = %v, want true", items[0]["has_torrent_file"])
	}
}

func TestReAddDeletion(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	_ = e.store.SaveTorrentFile(context.Background(), readdHash, []byte(readdTorrent))
	id := recordDeletion(t, e, readdHash)

	w := e.do(t, http.MethodPost, "/deletions/"+id+"/readd", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("readd = %d %s, want 204", w.Code, w.Body.String())
	}
	if _, err := e.store.TorrentByHash(context.Background(), readdHash); err != nil {
		t.Errorf("torrent not re-added to store: %v", err)
	}
}

func TestReAddDeletionNotFound(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	if w := e.do(t, http.MethodPost, "/deletions/nope/readd", ""); w.Code != http.StatusNotFound {
		t.Fatalf("readd missing = %d, want 404", w.Code)
	}
}

func TestReAddDeletionWithoutFile(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	id := recordDeletion(t, e, readdHash)
	if w := e.do(t, http.MethodPost, "/deletions/"+id+"/readd", ""); w.Code != http.StatusConflict {
		t.Fatalf("readd without file = %d, want 409", w.Code)
	}
}

func TestReAddRequiresSession(t *testing.T) {
	e := newEnv(t)
	if w := e.do(t, http.MethodPost, "/deletions/x/readd", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}
