package stremio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kipsilabs/seedstrem/internal/meta"
)

// concTracker records the peak number of Prowlarr /search requests in
// flight at once. A sequential search flow never exceeds 1; concurrent
// searches overlap during the fixed hold window and push the peak to >=2.
type concTracker struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	searches int
}

// enter marks a search as started (and records peak), hold keeps it in
// flight long enough for a genuinely concurrent sibling to overlap, and
// the returned func marks it done.
func (c *concTracker) track(hold time.Duration) func() {
	c.mu.Lock()
	c.inFlight++
	c.searches++
	if c.inFlight > c.peak {
		c.peak = c.inFlight
	}
	c.mu.Unlock()

	time.Sleep(hold) // overlap window — only bounded, not a sync primitive

	return func() {
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
	}
}

func (c *concTracker) snapshot() (peak, searches int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak, c.searches
}

// TestStreamEpisodeAndSeasonPackSearchesRunConcurrently asserts the
// per-episode discovery request issues its episode search and its
// season-pack search concurrently rather than back-to-back — the fix for
// the ~2x latency that was tripping the AIOStreams fetch timeout.
func TestStreamEpisodeAndSeasonPackSearchesRunConcurrently(t *testing.T) {
	const seasonPackHash = "fedcba9876543210fedcba9876543210fedcba98"
	var tr concTracker

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			w.Write([]byte(`[{"id":1,"name":"Cap","protocol":"torrent","enable":true,` +
				`"capabilities":{"tvSearchParams":["Q","ImdbId"]}}]`))
		case "/api/v1/search":
			done := tr.track(60 * time.Millisecond)
			defer done()
			if strings.Contains(r.URL.Query().Get("query"), "Episode") {
				w.Write([]byte(`[{"title":"Show S01E01 1080p","magnetUrl":"` + testMagnet() +
					`","size":100,"seeders":10,"protocol":"torrent","indexer":"Cap"}]`))
			} else {
				w.Write([]byte(`[{"title":"Show S01 COMPLETE 1080p","infoHash":"` + seasonPackHash +
					`","size":200,"seeders":20,"protocol":"torrent","indexer":"Cap"}]`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	h := New(nil, meta.New("", ""), func() Settings {
		return Settings{
			Prowlarr:   ProwlarrSettings{URL: prow.URL, APIKey: "k", TVCategories: []int{5000}},
			Addon:      AddonSettings{EnableSeries: true},
			MaxResults: 20,
		}
	}, "test", nil)

	root := chi.NewRouter()
	root.Mount("/stremio", h.Router())
	server := httptest.NewServer(root)
	defer server.Close()

	resp, err := http.Get(server.URL + "/stremio/stream/series/tt1375666:1:1.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	peak, searches := tr.snapshot()
	if searches != 2 {
		t.Fatalf("want 2 prowlarr searches (episode + season pack), got %d", searches)
	}
	if peak < 2 {
		t.Fatalf("episode and season-pack searches ran sequentially (peak in-flight %d); want them to overlap", peak)
	}
	// Merging must still work: the episode hit and the season pack both land.
	if len(sr.Streams) != 2 {
		t.Fatalf("want 2 merged streams (episode + season pack), got %d: %+v", len(sr.Streams), sr.Streams)
	}
}

// TestTTSearchRunsIDAndTextFallbackConcurrently asserts a single tt search
// fires its id-token search and its free-text fallback search (for
// indexers that can't do id search) concurrently instead of one after the
// other.
func TestTTSearchRunsIDAndTextFallbackConcurrently(t *testing.T) {
	const textHash = "fedcba9876543210fedcba9876543210fedcba98"
	var tr concTracker

	cinemeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/meta/series/tt1375666") {
			w.Write([]byte(`{"meta":{"name":"The Show","releaseInfo":"2010"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer cinemeta.Close()

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			w.Write([]byte(`[
				{"id":1,"name":"Cap","protocol":"torrent","enable":true,
				 "capabilities":{"tvSearchParams":["Q","ImdbId"]}},
				{"id":2,"name":"Text","protocol":"torrent","enable":true,
				 "capabilities":{"tvSearchParams":["Q"]}}
			]`))
		case "/api/v1/search":
			done := tr.track(60 * time.Millisecond)
			defer done()
			if r.URL.Query()["indexerIds"][0] == "1" {
				w.Write([]byte(`[{"title":"Show S01E01 1080p","magnetUrl":"` + testMagnet() +
					`","size":100,"seeders":10,"protocol":"torrent","indexer":"Cap"}]`))
			} else {
				w.Write([]byte(`[{"title":"The Show S01E01 720p","infoHash":"` + textHash +
					`","size":200,"seeders":20,"protocol":"torrent","indexer":"Text"}]`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	h := New(nil, meta.New(cinemeta.URL, ""), func() Settings {
		return Settings{
			Prowlarr:   ProwlarrSettings{URL: prow.URL, APIKey: "k", TVCategories: []int{5000}},
			Addon:      AddonSettings{EnableSeries: true},
			MaxResults: 20,
		}
	}, "test", nil)

	q, err := meta.ParseID("series", "tt1375666:1:1")
	if err != nil {
		t.Fatalf("parse id: %v", err)
	}
	res, err := h.ttSearch(context.Background(), q, h.settings())
	if err != nil {
		t.Fatalf("ttSearch: %v", err)
	}

	peak, searches := tr.snapshot()
	if searches != 2 {
		t.Fatalf("want 2 prowlarr searches (id + text fallback), got %d", searches)
	}
	if peak < 2 {
		t.Fatalf("id search and text fallback ran sequentially (peak in-flight %d); want them to overlap", peak)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 merged results (id + text fallback), got %d: %+v", len(res), res)
	}
}
