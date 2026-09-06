package stremio

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/prowlarr"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// playURLIndexer extracts the ix query param from a play URL.
func playURLIndexer(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse play url %q: %v", rawURL, err)
	}
	return u.Query().Get("ix")
}

// The play URL carries the indexer so the resolve handler can persist it
// and cleanup can apply that indexer's seed time.
func TestStreamItemPlayURLCarriesIndexer(t *testing.T) {
	h := &Handler{}
	res := prowlarr.Result{
		Title: "The Matrix 1999 1080p", InfoHash: testHash,
		MagnetURL: testMagnet(), Indexer: "TorrentLeech",
	}
	item := h.toStreamItem("http://x", movieQuery(), res, 0)
	if got := playURLIndexer(t, item.URL); got != "TorrentLeech" {
		t.Errorf("play url ix = %q, want %q (url: %s)", got, "TorrentLeech", item.URL)
	}
}

// An indexer name with a space must survive URL encoding intact.
func TestStreamItemPlayURLEncodesIndexerWithSpaces(t *testing.T) {
	h := &Handler{}
	res := prowlarr.Result{
		Title: "The Matrix 1999 1080p", InfoHash: testHash,
		MagnetURL: testMagnet(), Indexer: "Some Private Tracker",
	}
	item := h.toStreamItem("http://x", movieQuery(), res, 0)
	if got := playURLIndexer(t, item.URL); got != "Some Private Tracker" {
		t.Errorf("play url ix = %q, want the name round-tripped (url: %s)", got, item.URL)
	}
}

func TestStreamItemWithoutIndexerOmitsParam(t *testing.T) {
	h := &Handler{}
	res := prowlarr.Result{Title: "The Matrix 1999", InfoHash: testHash, MagnetURL: testMagnet()}
	item := h.toStreamItem("http://x", movieQuery(), res, 0)
	if got := playURLIndexer(t, item.URL); got != "" {
		t.Errorf("play url ix = %q, want empty (url: %s)", got, item.URL)
	}
}

// Re-playing an owned torrent must carry its recorded indexer back through,
// so the resolve path neither clears nor re-attributes it.
func TestOwnedStreamItemPlayURLCarriesStoredIndexer(t *testing.T) {
	h := &Handler{}
	tor := store.Torrent{Hash: testHash, Magnet: testMagnet(), Indexer: "TorrentLeech"}
	item := h.toOwnedStreamItem("http://x", movieQuery(), tor, 1)
	if got := playURLIndexer(t, item.URL); got != "TorrentLeech" {
		t.Errorf("owned play url ix = %q, want %q (url: %s)", got, "TorrentLeech", item.URL)
	}
}

func TestOwnedStreamItemWithoutIndexerOmitsParam(t *testing.T) {
	h := &Handler{}
	tor := store.Torrent{Hash: testHash, Magnet: testMagnet()}
	item := h.toOwnedStreamItem("http://x", movieQuery(), tor, 1)
	if got := playURLIndexer(t, item.URL); got != "" {
		t.Errorf("owned play url ix = %q, want empty (url: %s)", got, item.URL)
	}
}

// End to end: the resolve handler reads ix off the play URL and persists it
// on the torrent row cleanup will later read.
func TestPlayPersistsIndexer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"indexer carried through", "&ix=" + url.QueryEscape("Some Private Tracker"), "Some Private Tracker"},
		{"absent for URLs predating the param", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}
			playURL := h.server.URL + "/stremio/play/" + testHash +
				"?magnet=" + url.QueryEscape(testMagnet()) + tc.query
			resp, err := client.Get(playURL)
			if err != nil {
				t.Fatalf("play: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status = %d, want 302", resp.StatusCode)
			}

			tor, err := h.db.TorrentByHash(context.Background(), testHash)
			if err != nil {
				t.Fatalf("torrent by hash: %v", err)
			}
			if tor.Indexer != tc.want {
				t.Errorf("persisted indexer = %q, want %q", tor.Indexer, tc.want)
			}
		})
	}
}
