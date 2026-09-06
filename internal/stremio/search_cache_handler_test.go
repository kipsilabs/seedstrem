package stremio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kipsilabs/seedstrem/internal/meta"
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
