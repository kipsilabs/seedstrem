package stremio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kipsilabs/seedstrem/internal/meta"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// TestStreamRepeatedRequestServedFromSearchCache asserts that with a
// search-cache TTL configured, a repeated identical discovery request is
// answered from memory without any new Prowlarr /search calls.
func TestStreamRepeatedRequestServedFromSearchCache(t *testing.T) {
	var searches atomic.Int64

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			w.Write([]byte(`[{"id":1,"name":"Cap","protocol":"torrent","enable":true,` +
				`"capabilities":{"tvSearchParams":["Q","ImdbId"]}}]`))
		case "/api/v1/search":
			searches.Add(1)
			if strings.Contains(r.URL.Query().Get("query"), "Episode") {
				w.Write([]byte(`[{"title":"Show S01E01 1080p","magnetUrl":"` + testMagnet() +
					`","size":100,"seeders":10,"protocol":"torrent","indexer":"Cap"}]`))
			} else {
				w.Write([]byte(`[]`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	h := New(nil, meta.New("", ""), func() Settings {
		return Settings{
			Prowlarr: ProwlarrSettings{
				URL: prow.URL, APIKey: "k", TVCategories: []int{5000},
				SearchCacheTTL: time.Minute,
			},
			Addon:      AddonSettings{EnableSeries: true},
			MaxResults: 20,
		}
	}, "test", nil)

	root := chi.NewRouter()
	root.Mount("/stremio", h.Router())
	server := httptest.NewServer(root)
	defer server.Close()

	get := func() streamResponse {
		t.Helper()
		resp, err := http.Get(server.URL + "/stremio/stream/series/tt1375666:1:1.json")
		if err != nil {
			t.Fatalf("get stream: %v", err)
		}
		defer resp.Body.Close()
		var sr streamResponse
		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return sr
	}

	first := get()
	afterFirst := searches.Load()
	if afterFirst == 0 {
		t.Fatal("first request performed no prowlarr searches")
	}

	second := get()
	// The episode search (non-empty results) is cached; only the empty
	// season-pack search may repeat — but no more searches than the first
	// request needed, and at least one fewer thanks to the cache.
	afterSecond := searches.Load()
	if grew := afterSecond - afterFirst; grew >= afterFirst {
		t.Fatalf("second request re-searched everything: %d new searches after %d initial", grew, afterFirst)
	}

	if len(first.Streams) == 0 || len(second.Streams) != len(first.Streams) {
		t.Fatalf("cached response differs: first=%d second=%d streams", len(first.Streams), len(second.Streams))
	}
}

// TestStreamOwnedFastPathOffersCachedResults asserts that once content
// becomes owned, a repeat discovery request within the search-cache TTL
// still lists the earlier search's other results below the owned entry —
// read from memory, with no new Prowlarr search.
func TestStreamOwnedFastPathOffersCachedResults(t *testing.T) {
	h := newHarness(t)
	h.searchCacheTTL = time.Minute

	get := func() streamResponse {
		t.Helper()
		resp, err := http.Get(h.server.URL + "/stremio/stream/movie/tt1375666.json")
		if err != nil {
			t.Fatalf("get stream: %v", err)
		}
		defer resp.Body.Close()
		var sr streamResponse
		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return sr
	}

	if first := get(); len(first.Streams) != 2 {
		t.Fatalf("first request: want 2 streams, got %d: %+v", len(first.Streams), first.Streams)
	}
	searched := h.prowlarrHits.Load()

	// The user plays one of the results: it is now owned for this content.
	err := h.db.InsertTorrent(context.Background(), store.Torrent{
		ID: "OWNED000000001", Hash: testHash, Name: "The Matrix (owned)",
		Phase: store.PhaseSelected, AddedAt: 1, Magnet: testMagnet(),
		ContentSource: "tt", ContentRef: "tt1375666",
	})
	if err != nil {
		t.Fatalf("seed owned: %v", err)
	}

	sr := get()
	if got := h.prowlarrHits.Load(); got != searched {
		t.Errorf("owned fast path searched Prowlarr: hits %d -> %d", searched, got)
	}
	// Owned entry first, then the other cached result; the owned release's
	// own Prowlarr result is deduped away.
	if len(sr.Streams) != 2 {
		t.Fatalf("want 2 streams (owned + cached), got %d: %+v", len(sr.Streams), sr.Streams)
	}
	if !strings.Contains(sr.Streams[0].Title, "The Matrix (owned)") {
		t.Errorf("first stream should be the owned entry, got %q", sr.Streams[0].Title)
	}
	if !strings.Contains(sr.Streams[1].URL, "/stremio/play/ffffffffffffffffffffffffffffffffffffffff") {
		t.Errorf("second stream should be the other cached result, got %q", sr.Streams[1].URL)
	}
}

// TestStreamOwnedFastPathRefreshesColdCache asserts that when content is
// owned but no search results are cached (e.g. after a restart), the
// request is answered with the owned entry immediately while a background
// search fills the cache, so a later request lists the other results too.
func TestStreamOwnedFastPathRefreshesColdCache(t *testing.T) {
	h := newHarness(t)
	h.searchCacheTTL = time.Minute

	err := h.db.InsertTorrent(context.Background(), store.Torrent{
		ID: "OWNED000000001", Hash: testHash, Name: "The Matrix (owned)",
		Phase: store.PhaseSelected, AddedAt: 1, Magnet: testMagnet(),
		ContentSource: "tt", ContentRef: "tt1375666",
	})
	if err != nil {
		t.Fatalf("seed owned: %v", err)
	}

	get := func() streamResponse {
		t.Helper()
		resp, err := http.Get(h.server.URL + "/stremio/stream/movie/tt1375666.json")
		if err != nil {
			t.Fatalf("get stream: %v", err)
		}
		defer resp.Body.Close()
		var sr streamResponse
		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return sr
	}

	if first := get(); len(first.Streams) != 1 {
		t.Fatalf("cold cache: want 1 stream (owned only), got %d: %+v", len(first.Streams), first.Streams)
	}

	// The background refresh lands asynchronously; poll until a request
	// is served the cached results.
	deadline := time.Now().Add(5 * time.Second)
	for {
		sr := get()
		if len(sr.Streams) == 2 {
			if !strings.Contains(sr.Streams[0].Title, "The Matrix (owned)") {
				t.Errorf("first stream should be the owned entry, got %q", sr.Streams[0].Title)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background refresh never populated the cache; last response: %+v", sr.Streams)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.prowlarrHits.Load() == 0 {
		t.Error("background refresh should have searched Prowlarr")
	}
}
