package stremio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/meta"
	"github.com/kipsilabs/seedstrem/internal/prowlarr"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

const testHash = "0123456789abcdef0123456789abcdef01234567"

func testMagnet() string {
	return "magnet:?xt=urn:btih:" + testHash + "&dn=The.Matrix.1999.1080p"
}

// harness wires a Handler over fakes: cinemeta, prowlarr, and qBittorrent.
type harness struct {
	handler      *Handler
	server       *httptest.Server
	prowlarr     *httptest.Server
	prowlarrHits atomic.Int64 // number of Prowlarr search requests served
	cinemeta     *httptest.Server
	fakeDC       *fake.Server
	db           *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{}

	cinemeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/meta/movie/tt1375666") {
			w.Write([]byte(`{"meta":{"name":"The Matrix","releaseInfo":"1999"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(cinemeta.Close)

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			// One enabled torrent indexer; ImdbId-capable so tt queries route
			// through the id search. SearchEach fans out one request per
			// indexer, so a realistic indexer list is required.
			w.Write([]byte(`[
				{"id":1,"name":"idx1","protocol":"torrent","enable":true,
				 "capabilities":{"movieSearchParams":["Q","ImdbId"],"tvSearchParams":["Q","ImdbId","Season","Episode"]}}
			]`))
		default:
			h.prowlarrHits.Add(1)
			w.Write([]byte(`[
				{"title":"The Matrix 1999 1080p BluRay","magnetUrl":"` + testMagnet() + `","size":8589934592,"seeders":42,"protocol":"torrent","indexer":"idx1"},
				{"title":"The Matrix 1999 720p","infoHash":"ffffffffffffffffffffffffffffffffffffffff","size":2000000000,"seeders":10,"protocol":"torrent","indexer":"idx2"}
			]`))
		}
	}))
	t.Cleanup(prow.Close)

	fakeDC := fake.New()
	fakeDC.Put(&fake.Torrent{
		Hash:  testHash,
		State: "Paused",
		Files: []fake.File{{Name: "The.Matrix.1999.1080p.BluRay.mkv", Size: 8 << 30}},
	})

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := torrents.New(db, fakeDC, func() torrents.Settings {
		return torrents.Settings{MetadataTimeout: 2 * time.Second}
	}, nil)

	metaClient := meta.New(cinemeta.URL, "")

	h.prowlarr = prow
	h.cinemeta = cinemeta
	h.fakeDC = fakeDC
	h.db = db
	h.handler = New(svc, metaClient, func() Settings {
		return Settings{
			ExternalURL: h.server.URL,
			Prowlarr:    ProwlarrSettings{URL: prow.URL, APIKey: "k", MovieCategories: []int{2000}},
			Addon:       AddonSettings{EnableMovies: true, EnableSeries: true},
			Filters:     prowlarr.Filters{MinSeeders: 1},
			MaxResults:  20,
		}
	}, "test", nil)

	// Mount under /stremio exactly as the production server does.
	root := chi.NewRouter()
	root.Mount("/stremio", h.handler.Router())
	h.server = httptest.NewServer(root)
	t.Cleanup(h.server.Close)
	return h
}

func TestManifest(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.server.URL + "/stremio/manifest.json")
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("manifest missing CORS header")
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(m.Resources) != 1 || m.Resources[0] != "stream" {
		t.Errorf("resources = %v", m.Resources)
	}
	if len(m.Types) != 2 {
		t.Errorf("types = %v, want movie+series", m.Types)
	}
}

func TestBuildIDSearch(t *testing.T) {
	p := ProwlarrSettings{MovieCategories: []int{2000}, TVCategories: []int{5000}}

	movie := meta.Query{Source: "tt", ID: "tt1375666", Kind: meta.KindMovie}
	query, typ, cats := buildIDSearch(movie, p)
	if query != "{ImdbId:tt1375666}" {
		t.Errorf("movie query = %q, want id token", query)
	}
	if typ != "movie" {
		t.Errorf("movie type = %q, want movie", typ)
	}
	if len(cats) != 1 || cats[0] != 2000 {
		t.Errorf("movie categories = %v, want [2000]", cats)
	}

	series := meta.Query{Source: "tt", ID: "tt0944947", Kind: meta.KindSeries, Season: 1, Episode: 5}
	query, typ, cats = buildIDSearch(series, p)
	if query != "{ImdbId:tt0944947}{Season:01}{Episode:05}" {
		t.Errorf("series query = %q, want id+season+episode tokens", query)
	}
	if typ != "tvsearch" {
		t.Errorf("series type = %q, want tvsearch", typ)
	}
	if len(cats) != 1 || cats[0] != 5000 {
		t.Errorf("series categories = %v, want [5000]", cats)
	}

	seriesNoSE := meta.Query{Source: "tt", ID: "tt0944947", Kind: meta.KindSeries}
	if query, _, _ := buildIDSearch(seriesNoSE, p); query != "{ImdbId:tt0944947}" {
		t.Errorf("series without season/episode query = %q, want bare id token", query)
	}
}

func TestBuildTextSearch(t *testing.T) {
	p := ProwlarrSettings{MovieCategories: []int{2000}, TVCategories: []int{5000}}

	movie := meta.Query{Source: "tt", ID: "tt1375666", Kind: meta.KindMovie}
	if query, cats := buildTextSearch(movie, "The Matrix", 1999, p); query != "The Matrix 1999" || cats[0] != 2000 {
		t.Errorf("movie text search = %q, %v", query, cats)
	}

	series := meta.Query{Source: "tt", ID: "tt0944947", Kind: meta.KindSeries, Season: 1, Episode: 5}
	if query, cats := buildTextSearch(series, "Chernobyl", 0, p); query != "Chernobyl S01E05" || cats[0] != 5000 {
		t.Errorf("series text search = %q, %v", query, cats)
	}

	// Season-only search (episode dropped) queries the whole season so
	// full-season packs surface, not "S01E00".
	seasonOnly := meta.Query{Source: "tt", ID: "tt0944947", Kind: meta.KindSeries, Season: 1}
	if query, _ := buildTextSearch(seasonOnly, "Chernobyl", 0, p); query != "Chernobyl S01" {
		t.Errorf("season-only text search = %q, want %q", query, "Chernobyl S01")
	}
}

func TestBuildAnimeSearch(t *testing.T) {
	p := ProwlarrSettings{AnimeCategories: []int{5070}}

	anime := meta.Query{Source: "kitsu", ID: "12", Kind: meta.KindMovie}
	if query, cats := buildAnimeSearch(anime, "Anime Movie", p); query != "Anime Movie" || cats[0] != 5070 {
		t.Errorf("anime search = %q, %v", query, cats)
	}

	animeEp := meta.Query{Source: "kitsu", ID: "44081", Kind: meta.KindSeries, Episode: 5}
	if query, _ := buildAnimeSearch(animeEp, "Anime Series", p); query != "Anime Series 05" {
		t.Errorf("anime series query = %q, want title + episode", query)
	}
}

func TestSplitByIDCapability(t *testing.T) {
	indexers := []prowlarr.IndexerInfo{
		{ID: 1, Enable: true, Capabilities: prowlarr.Capabilities{MovieSearchParams: []string{"Q", "ImdbId"}, TvSearchParams: []string{"Q"}}},
		{ID: 2, Enable: true, Capabilities: prowlarr.Capabilities{MovieSearchParams: []string{"Q"}, TvSearchParams: []string{"Q", "ImdbId"}}},
		{ID: 3, Enable: false, Capabilities: prowlarr.Capabilities{MovieSearchParams: []string{"Q", "ImdbId"}}},
		{ID: 4, Enable: true, Capabilities: prowlarr.Capabilities{MovieSearchParams: []string{"Q", "TmdbId"}, TvSearchParams: []string{"Q"}}},
		{ID: 5, Enable: true, Capabilities: prowlarr.Capabilities{MovieSearchParams: []string{"Q", "ImdbId", "TmdbId"}, TvSearchParams: []string{"Q"}}},
	}

	// Empty configured scope = every enabled indexer, split by capability,
	// Imdb preferred over Tmdb over plain text. Indexer 5 supports both,
	// so it lands in imdbCapable but still flips needsTmdb.
	imdb, tmdb, text, needsTmdb := splitByIDCapability(indexers, nil, false)
	if len(imdb) != 2 || imdb[0] != 1 || imdb[1] != 5 {
		t.Errorf("movie imdb-capable = %v, want [1 5]", imdb)
	}
	if len(tmdb) != 1 || tmdb[0] != 4 {
		t.Errorf("movie tmdb-capable = %v, want [4]", tmdb)
	}
	if len(text) != 1 || text[0] != 2 {
		t.Errorf("movie text-only = %v, want [2]", text)
	}
	if !needsTmdb {
		t.Error("needsTmdb = false, want true (indexer 4 and dual-capable indexer 5)")
	}

	imdb, tmdb, text, needsTmdb = splitByIDCapability(indexers, nil, true)
	if len(imdb) != 1 || imdb[0] != 2 {
		t.Errorf("tv imdb-capable = %v, want [2]", imdb)
	}
	if len(tmdb) != 0 {
		t.Errorf("tv tmdb-capable = %v, want none", tmdb)
	}
	if len(text) != 3 {
		t.Errorf("tv text-only = %v, want 3 (indexers 1, 4, and 5 lack tv ImdbId/TmdbId)", text)
	}
	if needsTmdb {
		t.Error("needsTmdb = true, want false (no tv indexer supports TmdbId)")
	}

	// A configured (disabled) indexer is still honored explicitly.
	imdb, _, _, _ = splitByIDCapability(indexers, []int{3}, false)
	if len(imdb) != 1 || imdb[0] != 3 {
		t.Errorf("explicit scope imdb-capable = %v, want [3]", imdb)
	}

	// Configured ids that match nothing known yield all-empty.
	imdb, tmdb, text, needsTmdb = splitByIDCapability(indexers, []int{99}, false)
	if len(imdb) != 0 || len(tmdb) != 0 || len(text) != 0 || needsTmdb {
		t.Errorf("unknown scope = imdb %v tmdb %v text %v needsTmdb %v, want all empty/false", imdb, tmdb, text, needsTmdb)
	}
}

func TestCombineIDBuckets(t *testing.T) {
	// No Tmdb-capable indexers at all: passthrough, Imdb-only query.
	bucket, query, extraText := combineIDBuckets([]int{1}, nil, "{ImdbId:tt1}", "")
	if len(bucket) != 1 || bucket[0] != 1 || query != "{ImdbId:tt1}" || len(extraText) != 0 {
		t.Errorf("no-tmdb case = bucket %v query %q extraText %v", bucket, query, extraText)
	}

	// Tmdb-capable indexers present and resolution succeeded: merge into
	// one bucket with both tokens present in the query.
	bucket, query, extraText = combineIDBuckets([]int{1}, []int{4}, "{ImdbId:tt1}", "{TmdbId:603}")
	if len(bucket) != 2 || bucket[0] != 1 || bucket[1] != 4 {
		t.Errorf("merged bucket = %v, want [1 4]", bucket)
	}
	if query != "{ImdbId:tt1}{TmdbId:603}" {
		t.Errorf("combined query = %q, want both tokens", query)
	}
	if len(extraText) != 0 {
		t.Errorf("extraText = %v, want none when resolution succeeded", extraText)
	}

	// Tmdb-capable indexers present but resolution failed: they can't
	// use this search at all (no usable token) and demote to text.
	bucket, query, extraText = combineIDBuckets([]int{1}, []int{4}, "{ImdbId:tt1}", "")
	if len(bucket) != 1 || bucket[0] != 1 {
		t.Errorf("unresolved bucket = %v, want [1] (imdb only)", bucket)
	}
	if query != "{ImdbId:tt1}" {
		t.Errorf("unresolved query = %q, want imdb-only", query)
	}
	if len(extraText) != 1 || extraText[0] != 4 {
		t.Errorf("extraText = %v, want [4]", extraText)
	}

	// No Tmdb-only indexers at all, but resolution succeeded anyway (a
	// dual-capable indexer triggered it): still append the Tmdb token to
	// the same bucket, matching Radarr sending both tokens together.
	bucket, query, extraText = combineIDBuckets([]int{5}, nil, "{ImdbId:tt1}", "{TmdbId:603}")
	if len(bucket) != 1 || bucket[0] != 5 {
		t.Errorf("dual-capable bucket = %v, want [5]", bucket)
	}
	if query != "{ImdbId:tt1}{TmdbId:603}" {
		t.Errorf("dual-capable query = %q, want both tokens", query)
	}
	if len(extraText) != 0 {
		t.Errorf("dual-capable extraText = %v, want none", extraText)
	}
}

// TestStreamSplitsSearchByCapability verifies the end-to-end split: an
// ImdbId-capable indexer gets the id-token search, an incapable one gets
// a free-text fallback (requiring a title lookup), and both result sets
// are merged into the response.
func TestStreamSplitsSearchByCapability(t *testing.T) {
	const secondHash = "fedcba9876543210fedcba9876543210fedcba98"

	cinemeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/meta/movie/tt1375666") {
			w.Write([]byte(`{"meta":{"name":"The Matrix","releaseInfo":"1999"}}`))
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
				{"id":1,"name":"Capable","protocol":"torrent","enable":true,
				 "capabilities":{"movieSearchParams":["Q","ImdbId"]}},
				{"id":2,"name":"Incapable","protocol":"torrent","enable":true,
				 "capabilities":{"movieSearchParams":["Q"]}}
			]`))
		case "/api/v1/search":
			q := r.URL.Query()
			switch {
			case q.Get("type") == "movie" && q["indexerIds"][0] == "1":
				w.Write([]byte(`[{"title":"ID Search Hit","magnetUrl":"` + testMagnet() + `","size":100,"seeders":10,"protocol":"torrent","indexer":"Capable"}]`))
			case q.Get("type") == "search" && q["indexerIds"][0] == "2":
				w.Write([]byte(`[{"title":"Text Search Hit","infoHash":"` + secondHash + `","size":200,"seeders":20,"protocol":"torrent","indexer":"Incapable"}]`))
			default:
				w.Write([]byte(`[]`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	metaClient := meta.New(cinemeta.URL, "")
	h := New(nil, metaClient, func() Settings {
		return Settings{
			Prowlarr:   ProwlarrSettings{URL: prow.URL, APIKey: "k", MovieCategories: []int{2000}},
			Addon:      AddonSettings{EnableMovies: true},
			MaxResults: 20,
		}
	}, "test", nil)

	root := chi.NewRouter()
	root.Mount("/stremio", h.Router())
	server := httptest.NewServer(root)
	defer server.Close()

	resp, err := http.Get(server.URL + "/stremio/stream/movie/tt1375666.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sr.Streams) != 2 {
		t.Fatalf("want 2 streams (id search + text fallback merged), got %d: %+v", len(sr.Streams), sr.Streams)
	}
	var sawID, sawText bool
	for _, s := range sr.Streams {
		if strings.Contains(s.Title, "ID Search Hit") {
			sawID = true
		}
		if strings.Contains(s.Title, "Text Search Hit") {
			sawText = true
		}
	}
	if !sawID || !sawText {
		t.Errorf("expected both id-search and text-fallback results, got: %+v", sr.Streams)
	}
}

// TestStreamReturnsPartialResultsWithinSearchTimeout verifies the headline
// global-timeout behavior end-to-end: with two ImdbId-capable indexers, one
// of which hangs well past the configured SearchTimeout, the stream request
// returns promptly with the fast indexer's result rather than blocking on
// the slow one.
func TestStreamReturnsPartialResultsWithinSearchTimeout(t *testing.T) {
	cinemeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // id search covers both indexers; no title lookup needed
	}))
	defer cinemeta.Close()

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			w.Write([]byte(`[
				{"id":1,"name":"Fast","protocol":"torrent","enable":true,
				 "capabilities":{"movieSearchParams":["Q","ImdbId"]}},
				{"id":2,"name":"Slow","protocol":"torrent","enable":true,
				 "capabilities":{"movieSearchParams":["Q","ImdbId"]}}
			]`))
		case "/api/v1/search":
			if r.URL.Query().Get("indexerIds") == "2" {
				// The slow indexer hangs past the budget; it must be
				// abandoned, honoring request cancellation so the test
				// server shuts down cleanly.
				select {
				case <-time.After(3 * time.Second):
				case <-r.Context().Done():
					return
				}
			}
			w.Write([]byte(`[{"title":"Fast Indexer Hit","magnetUrl":"` + testMagnet() + `","size":100,"seeders":10,"protocol":"torrent","indexer":"Fast"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	metaClient := meta.New(cinemeta.URL, "")
	h := New(nil, metaClient, func() Settings {
		return Settings{
			Prowlarr: ProwlarrSettings{
				URL: prow.URL, APIKey: "k", MovieCategories: []int{2000},
				SearchTimeout: 250 * time.Millisecond,
			},
			Addon:      AddonSettings{EnableMovies: true},
			MaxResults: 20,
		}
	}, "test", nil)

	root := chi.NewRouter()
	root.Mount("/stremio", h.Router())
	server := httptest.NewServer(root)
	defer server.Close()

	start := time.Now()
	resp, err := http.Get(server.URL + "/stremio/stream/movie/tt1375666.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("request should return within the search budget, took %v", elapsed)
	}
	if len(sr.Streams) != 1 {
		t.Fatalf("want 1 partial result (fast indexer only), got %d: %+v", len(sr.Streams), sr.Streams)
	}
	if !strings.Contains(sr.Streams[0].Title, "Fast Indexer Hit") {
		t.Errorf("expected the fast indexer's result, got %q", sr.Streams[0].Title)
	}
}

// TestResolveIndexerIDsUsesCachedEnabledTorrentIndexers verifies that an
// unconfigured indexer scope resolves to the enabled torrent indexers from
// the shared cache (so SearchEach's per-indexer fan-out doesn't re-enumerate
// Prowlarr on every request), and that a configured scope passes through
// untouched.
func TestResolveIndexerIDsUsesCachedEnabledTorrentIndexers(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/indexer" {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id":1,"name":"A","protocol":"torrent","enable":true},
			{"id":2,"name":"B","protocol":"usenet","enable":true},
			{"id":3,"name":"C","protocol":"torrent","enable":false},
			{"id":4,"name":"D","protocol":"torrent","enable":true}
		]`))
	}))
	defer srv.Close()

	h := New(nil, meta.New("", ""), func() Settings { return Settings{} }, "test", nil)
	pc := prowlarr.New(srv.URL, "k")

	// Empty configured scope → enabled torrent indexers only (1 and 4);
	// usenet (2) and disabled (3) are excluded.
	got := h.resolveIndexerIDs(context.Background(), pc, Settings{})
	if len(got) != 2 || got[0] != 1 || got[1] != 4 {
		t.Fatalf("want enabled torrent indexers [1 4], got %v", got)
	}
	// A second resolution reuses the cache — no extra /indexer round-trip.
	_ = h.resolveIndexerIDs(context.Background(), pc, Settings{})
	if n := hits.Load(); n != 1 {
		t.Fatalf("indexer endpoint should be hit once (cached), got %d", n)
	}

	// A configured scope is returned as-is without touching Prowlarr.
	got = h.resolveIndexerIDs(context.Background(), pc, Settings{Prowlarr: ProwlarrSettings{IndexerIDs: []int{9}}})
	if len(got) != 1 || got[0] != 9 {
		t.Fatalf("configured scope should pass through, got %v", got)
	}
}

// TestStreamTmdbOnlyFallsBackToText verifies that a TmdbId-only indexer,
// when TMDb resolution isn't possible (no API key configured here),
// falls back to the free-text search rather than being dropped.
func TestStreamTmdbOnlyFallsBackToText(t *testing.T) {
	cinemeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/meta/movie/tt1375666") {
			w.Write([]byte(`{"meta":{"name":"The Matrix","releaseInfo":"1999"}}`))
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
				{"id":1,"name":"TmdbOnly","protocol":"torrent","enable":true,
				 "capabilities":{"movieSearchParams":["Q","TmdbId"]}}
			]`))
		case "/api/v1/search":
			q := r.URL.Query()
			if q.Get("type") == "search" && q["indexerIds"][0] == "1" {
				w.Write([]byte(`[{"title":"Text Fallback Hit","magnetUrl":"` + testMagnet() + `","size":100,"seeders":10,"protocol":"torrent","indexer":"TmdbOnly"}]`))
				return
			}
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	// No TMDb API key configured: resolution errors, so the TmdbId-only
	// indexer should fall back to the text-search bucket, not be dropped.
	metaClient := meta.New(cinemeta.URL, "")
	h := New(nil, metaClient, func() Settings {
		return Settings{
			Prowlarr:   ProwlarrSettings{URL: prow.URL, APIKey: "k", MovieCategories: []int{2000}},
			Addon:      AddonSettings{EnableMovies: true},
			MaxResults: 20,
		}
	}, "test", nil)

	root := chi.NewRouter()
	root.Mount("/stremio", h.Router())
	server := httptest.NewServer(root)
	defer server.Close()

	resp, err := http.Get(server.URL + "/stremio/stream/movie/tt1375666.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sr.Streams) != 1 || !strings.Contains(sr.Streams[0].Title, "Text Fallback Hit") {
		t.Fatalf("want text-fallback result for tmdb-only indexer, got: %+v", sr.Streams)
	}
}

// TestStreamDecodesEncodedSeriesID verifies that a series id arriving
// with percent-encoded colons (Stremio sends tt31849235%3A1%3A2) is
// decoded before parsing, so season/episode survive into the Prowlarr
// id-token query instead of the raw encoded blob.
func TestStreamDecodesEncodedSeriesID(t *testing.T) {
	var gotQuery string

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			w.Write([]byte(`[
				{"id":1,"name":"Capable","protocol":"torrent","enable":true,
				 "capabilities":{"tvSearchParams":["Q","ImdbId"]}}
			]`))
		case "/api/v1/search":
			q := r.URL.Query()
			// A specific-episode request now fans out into an
			// episode-scoped and a season-only tvsearch; capture the
			// episode-scoped one (the id decoding under test).
			if q.Get("type") == "tvsearch" && strings.Contains(q.Get("query"), "{Episode:") {
				gotQuery = q.Get("query")
			}
			w.Write([]byte(`[{"title":"Series Hit S01E02","magnetUrl":"` + testMagnet() + `","size":100,"seeders":10,"protocol":"torrent","indexer":"Capable"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	metaClient := meta.New("http://cinemeta.invalid", "")
	h := New(nil, metaClient, func() Settings {
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

	resp, err := http.Get(server.URL + "/stremio/stream/series/tt31849235%3A1%3A2.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := "{ImdbId:tt31849235}{Season:01}{Episode:02}"
	if gotQuery != want {
		t.Errorf("prowlarr id query = %q, want %q", gotQuery, want)
	}
	if len(sr.Streams) != 1 {
		t.Errorf("want 1 stream, got %d: %+v", len(sr.Streams), sr.Streams)
	}
}

// TestStreamSurfacesSeasonPacks verifies that a specific-episode request
// also issues a season-only search (no Episode token) and merges in
// full-season packs, while dropping the stray other-episode releases that
// season search returns. This is what makes season packs show up at all —
// the episode-scoped search alone never returns them.
func TestStreamSurfacesSeasonPacks(t *testing.T) {
	const (
		episodeHash = "1111111111111111111111111111111111111111"
		packHash    = "2222222222222222222222222222222222222222"
		otherEpHash = "3333333333333333333333333333333333333333"
	)
	magnet := func(h string) string { return "magnet:?xt=urn:btih:" + h + "&dn=x" }

	var sawSeasonOnlySearch bool

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/indexer":
			w.Write([]byte(`[
				{"id":1,"name":"Capable","protocol":"torrent","enable":true,
				 "capabilities":{"tvSearchParams":["Q","ImdbId","Season","Episode"]}}
			]`))
		case "/api/v1/search":
			query := r.URL.Query().Get("query")
			if strings.Contains(query, "{Episode:") {
				// Episode-scoped search: only the single episode.
				w.Write([]byte(`[{"title":"The Show S01E02 1080p","magnetUrl":"` + magnet(episodeHash) + `","size":100,"seeders":10,"protocol":"torrent","indexer":"Capable"}]`))
				return
			}
			// Season-only search: a full-season pack plus a stray other
			// episode that must be filtered out.
			sawSeasonOnlySearch = true
			w.Write([]byte(`[
				{"title":"The Show S01 1080p WEB-DL","magnetUrl":"` + magnet(packHash) + `","size":5000,"seeders":30,"protocol":"torrent","indexer":"Capable"},
				{"title":"The Show S01E07 1080p","magnetUrl":"` + magnet(otherEpHash) + `","size":120,"seeders":50,"protocol":"torrent","indexer":"Capable"}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prow.Close()

	metaClient := meta.New("http://cinemeta.invalid", "")
	h := New(nil, metaClient, func() Settings {
		return Settings{
			Prowlarr:   ProwlarrSettings{URL: prow.URL, APIKey: "k", TVCategories: []int{5000}},
			Addon:      AddonSettings{EnableSeries: true},
			Filters:    prowlarr.Filters{MinSeeders: 1},
			MaxResults: 20,
		}
	}, "test", nil)

	root := chi.NewRouter()
	root.Mount("/stremio", h.Router())
	server := httptest.NewServer(root)
	defer server.Close()

	resp, err := http.Get(server.URL + "/stremio/stream/series/tt0944947%3A1%3A2.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !sawSeasonOnlySearch {
		t.Fatal("no season-only search was issued")
	}

	var titles []string
	for _, s := range sr.Streams {
		titles = append(titles, s.Title)
	}
	joined := strings.Join(titles, "\n")
	if !strings.Contains(joined, "The Show S01E02") {
		t.Errorf("missing the requested episode; got:\n%s", joined)
	}
	if !strings.Contains(joined, "The Show S01 1080p") {
		t.Errorf("missing the season pack; got:\n%s", joined)
	}
	if strings.Contains(joined, "S01E07") {
		t.Errorf("stray other-episode should have been filtered out; got:\n%s", joined)
	}
}

func TestManifestVersion(t *testing.T) {
	tests := map[string]string{
		"1.2.3":          "1.2.3",      // plain semver
		"v1.2.3":         "1.2.3",      // leading v stripped
		"1.2.3-rc.1":     "1.2.3-rc.1", // prerelease kept
		"0.0.0-main.abc": "0.0.0-main.abc",
		"main":           fallbackVersion,
		"docker":         fallbackVersion,
		"dev":            fallbackVersion,
		"a1b2c3d":        fallbackVersion,
		"1.2":            fallbackVersion,
		"":               fallbackVersion,
	}
	for in, want := range tests {
		if got := manifestVersion(in); got != want {
			t.Errorf("manifestVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStreamDiscovery(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.server.URL + "/stremio/stream/movie/tt1375666.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sr.Streams) != 2 {
		t.Fatalf("want 2 streams, got %d: %+v", len(sr.Streams), sr.Streams)
	}
	// Highest seeders first.
	if !strings.Contains(sr.Streams[0].Title, "42") {
		t.Errorf("expected top stream to have 42 seeders: %q", sr.Streams[0].Title)
	}
	if !strings.Contains(sr.Streams[0].URL, "/stremio/play/"+testHash) {
		t.Errorf("play URL = %q", sr.Streams[0].URL)
	}
	// The parsed resolution is surfaced on the name badge (top result is
	// "The Matrix 1999 1080p BluRay") and the description mirrors the title.
	if !strings.Contains(sr.Streams[0].Name, "1080p") {
		t.Errorf("expected resolution badge in name: %q", sr.Streams[0].Name)
	}
	// The originating indexer is surfaced in the name label so it stays
	// visible even when clients truncate the description.
	if !strings.Contains(sr.Streams[0].Name, "idx1") {
		t.Errorf("expected indexer in name: %q", sr.Streams[0].Name)
	}
	if sr.Streams[0].Description != sr.Streams[0].Title {
		t.Errorf("description should mirror title: desc=%q title=%q", sr.Streams[0].Description, sr.Streams[0].Title)
	}
}

// TestStreamPrioritizesOwnedTorrents verifies that a torrent the app
// already added for this content is surfaced first as a high-priority
// stream, that a Prowlarr result sharing its infohash is deduped away, and
// that unrelated Prowlarr results remain as fallback.
func TestStreamPrioritizesOwnedTorrents(t *testing.T) {
	h := newHarness(t)

	// Re-put testHash fully downloaded so the owned entry reports
	// "downloaded" (Get returns a copy, so Put is how tests mutate state).
	h.fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: "Paused", Progress: 1,
		Files: []fake.File{{Name: "The.Matrix.1999.1080p.BluRay.mkv", Size: 8 << 30}},
	})

	// Seed an owned torrent for tt1375666 keyed to testHash — the same
	// release the harness's Prowlarr also returns (testMagnet).
	err := h.db.InsertTorrent(context.Background(), store.Torrent{
		ID: "OWNED000000001", Hash: testHash, Name: "The Matrix (owned)",
		Phase: store.PhaseSelected, AddedAt: 1, Magnet: testMagnet(),
		ContentSource: "tt", ContentRef: "tt1375666",
	})
	if err != nil {
		t.Fatalf("seed owned: %v", err)
	}

	resp, err := http.Get(h.server.URL + "/stremio/stream/movie/tt1375666.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// With owned content present, the Prowlarr search is skipped entirely,
	// so only the owned entry is returned.
	if got := h.prowlarrHits.Load(); got != 0 {
		t.Errorf("Prowlarr should not be searched when content is owned, got %d hits", got)
	}
	if len(sr.Streams) != 1 {
		t.Fatalf("want 1 stream (owned only), got %d: %+v", len(sr.Streams), sr.Streams)
	}
	first := sr.Streams[0]
	if !strings.Contains(first.Title, "⚡") || !strings.Contains(first.Title, "The Matrix (owned)") {
		t.Errorf("first stream should be the owned high-priority entry, got %q", first.Title)
	}
	if !strings.Contains(first.Title, "downloaded") {
		t.Errorf("owned entry should report download state, got %q", first.Title)
	}
	if !strings.Contains(first.URL, "/stremio/play/"+testHash) {
		t.Errorf("owned play URL = %q", first.URL)
	}
	if !strings.Contains(first.URL, "cid=tt1375666") {
		t.Errorf("owned play URL missing content identity: %q", first.URL)
	}
	// testHash must appear exactly once across all streams (deduped).
	var testHashCount int
	for _, s := range sr.Streams {
		if strings.Contains(s.URL, "/stremio/play/"+testHash+"?") {
			testHashCount++
		}
	}
	if testHashCount != 1 {
		t.Errorf("testHash appeared %d times, want 1 (deduped): %+v", testHashCount, sr.Streams)
	}
}

// TestStreamSurfacesCachedByInfohash verifies that a release already in the
// store but WITHOUT a matching content id (e.g. one grabbed in the
// background by the RSS poller) is still surfaced first as a ready stream
// when a Prowlarr search turns up the same infohash. Unlike content-owned
// torrents, the Prowlarr search still runs (we can't know the infohash
// beforehand), and the cached release is ordered ahead of fresh candidates.
func TestStreamSurfacesCachedByInfohash(t *testing.T) {
	h := newHarness(t)

	h.fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: "Paused", Progress: 1,
		Files: []fake.File{{Name: "The.Matrix.1999.1080p.BluRay.mkv", Size: 8 << 30}},
	})

	// A grabbed torrent: real infohash + magnet, but no content identity.
	err := h.db.InsertTorrent(context.Background(), store.Torrent{
		ID: "GRABBED0000001", Hash: testHash, Name: "The Matrix (grabbed)",
		Phase: store.PhaseAdded, AddedAt: 1, Magnet: testMagnet(),
	})
	if err != nil {
		t.Fatalf("seed grabbed: %v", err)
	}

	resp, err := http.Get(h.server.URL + "/stremio/stream/movie/tt1375666.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The content id doesn't match a stored torrent, so Prowlarr IS searched.
	if got := h.prowlarrHits.Load(); got == 0 {
		t.Error("Prowlarr should be searched when no content-id match exists")
	}
	// Both results come back; the cached one is surfaced ready, the other is
	// a fresh fallback.
	if len(sr.Streams) != 2 {
		t.Fatalf("want 2 streams, got %d: %+v", len(sr.Streams), sr.Streams)
	}
	first := sr.Streams[0]
	if !strings.Contains(first.Name, "⚡") {
		t.Errorf("cached release should be surfaced as a ready (⚡) stream first, got %q", first.Name)
	}
	if !strings.Contains(first.URL, "/stremio/play/"+testHash) {
		t.Errorf("cached play URL should target the grabbed infohash: %q", first.URL)
	}
	if !strings.Contains(first.URL, "cid=tt1375666") {
		t.Errorf("cached play URL should carry the requested content id so play backfills it: %q", first.URL)
	}
	// The fresh fallback (the other infohash) should be a plain, non-ready
	// stream.
	if strings.Contains(sr.Streams[1].Name, "⚡") {
		t.Errorf("second stream should be a fresh (non-ready) candidate, got %q", sr.Streams[1].Name)
	}
}

func TestPlayRedirects(t *testing.T) {
	h := newHarness(t)

	// Don't follow the redirect — inspect the 302 target.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	playURL := h.server.URL + "/stremio/play/" + testHash + "?magnet=" + url.QueryEscape(testMagnet())
	resp, err := client.Get(playURL)
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/dl/") {
		t.Errorf("redirect location = %q, want /dl/{token}", loc)
	}

	// The torrent was added to qBittorrent, stopped + sequential.
	var added bool
	for _, c := range h.fakeDC.Calls() {
		if strings.HasPrefix(c, "add magnet=") && strings.Contains(c, "seq=true") {
			added = true
		}
	}
	if !added {
		t.Errorf("magnet not added correctly: %v", h.fakeDC.Calls())
	}
}

func TestPlayRejectsMismatchedMagnet(t *testing.T) {
	h := newHarness(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	// Magnet hash differs from the path infohash.
	other := "magnet:?xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff"
	resp, err := client.Get(h.server.URL + "/stremio/play/" + testHash + "?magnet=" + url.QueryEscape(other))
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
