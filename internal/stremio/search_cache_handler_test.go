package stremio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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

	ttl := time.Minute
	h := New(nil, meta.New("", ""), func() Settings {
		return Settings{
			Prowlarr: ProwlarrSettings{
				URL: prow.URL, APIKey: "k", TVCategories: []int{5000},
				SearchCacheTTL: ttl,
			},
			Addon:      AddonSettings{EnableSeries: true},
			MaxResults: 20,
		}
	}, "test", nil)

	now := time.Unix(1000, 0)
	h.searchCache.now = func() time.Time { return now }

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
	if grew := searches.Load() - afterFirst; grew != 0 {
		t.Fatalf("cached request performed %d new searches, want none", grew)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached response differs: first=%+v second=%+v", first, second)
	}

	if len(first.Streams) == 0 {
		t.Fatal("expected non-empty streams")
	}

	// Expired entries must search again rather than hiding newer results.
	now = now.Add(time.Minute + time.Second)
	get()
	afterExpiry := searches.Load()
	if afterExpiry <= afterFirst {
		t.Fatal("expired cache did not perform a new search")
	}

	// A live config change disabling caching must bypass populated entries.
	ttl = 0
	get()
	if searches.Load() <= afterExpiry {
		t.Fatal("disabled cache did not perform a new search")
	}
}
