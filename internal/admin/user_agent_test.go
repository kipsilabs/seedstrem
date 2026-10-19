package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/store"
)

func TestTorrentsExposesUserAgent(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	if err := e.store.InsertTorrent(context.Background(), store.Torrent{
		ID: "T1", Hash: "aaaa", Name: "Movie", Phase: store.PhaseAdded,
		UserAgent: "Stremio/4.4.168",
	}); err != nil {
		t.Fatal(err)
	}
	w := e.do(t, http.MethodGet, "/torrents", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var items []struct {
		UserAgent string `json:"user_agent"`
	}
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].UserAgent != "Stremio/4.4.168" {
		t.Fatalf("torrent list = %+v, want original user agent", items)
	}
}
