package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const hexHash = "0123456789abcdef0123456789abcdef01234567"

func TestSearch(t *testing.T) {
	var gotKey, gotQuery, gotType string
	var gotCategories, gotIndexers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotQuery = r.URL.Query().Get("query")
		gotType = r.URL.Query().Get("type")
		gotCategories = r.URL.Query()["categories"]
		gotIndexers = r.URL.Query()["indexerIds"]
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"title":"Has Magnet 1080p","magnetUrl":"magnet:?xt=urn:btih:` + hexHash + `&dn=x","size":100,"seeders":10,"protocol":"torrent","indexer":"idx","categories":[2040]},
			{"title":"Infohash Only 720p","infoHash":"` + strings.ToUpper(hexHash) + `","size":50,"seeders":5,"protocol":"torrent","categories":[{"id":5040}]},
			{"title":"Torrent File Only","downloadUrl":"http://x/file.torrent","size":30,"seeders":3,"protocol":"torrent"},
			{"title":"Usenet Release","infoHash":"deadbeef","protocol":"usenet","size":40,"seeders":99}
		]`))
	}))
	defer srv.Close()

	c := NewWithClient(srv.URL, "my-key", srv.Client())
	results, err := c.Search(context.Background(), "the matrix 1999", "", []int{2000, 5000}, []int{3, 7})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if gotKey != "my-key" {
		t.Errorf("X-Api-Key = %q, want my-key", gotKey)
	}
	if gotQuery != "the matrix 1999" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotType != "search" {
		t.Errorf("type = %q, want default \"search\"", gotType)
	}
	if len(gotCategories) != 2 {
		t.Errorf("categories params = %v, want 2", gotCategories)
	}
	if len(gotIndexers) != 2 || gotIndexers[0] != "3" || gotIndexers[1] != "7" {
		t.Errorf("indexerIds params = %v, want [3 7]", gotIndexers)
	}

	// Kept: magnet result + infohash-only (synthesized magnet). Dropped:
	// torrent-file-only (no magnet/infohash) and usenet.
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(results), results)
	}

	if results[0].InfoHash != hexHash {
		t.Errorf("magnet result infohash = %q, want %q", results[0].InfoHash, hexHash)
	}
	if len(results[0].Categories) != 1 || results[0].Categories[0] != 2040 {
		t.Errorf("int categories not parsed: %v", results[0].Categories)
	}

	second := results[1]
	if second.InfoHash != hexHash {
		t.Errorf("infohash-only result should lowercase hash, got %q", second.InfoHash)
	}
	if !strings.HasPrefix(second.MagnetURL, "magnet:?xt=urn:btih:"+hexHash) {
		t.Errorf("infohash-only result should synthesize magnet, got %q", second.MagnetURL)
	}
	if len(second.Categories) != 1 || second.Categories[0] != 5040 {
		t.Errorf("object categories not parsed: %v", second.Categories)
	}
}

func TestSearchTorrentFileFallback(t *testing.T) {
	// Minimal valid bencoded .torrent with an announce tracker. The
	// tracker must survive into the synthesized magnet — without it a
	// private-tracker torrent has no peers.
	const tracker = "http://tr.example/announce"
	torrentBytes := []byte("d8:announce26:" + tracker + "4:infod4:name4:testee")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"title":"Torrent File Only","downloadUrl":"` + "http://" + r.Host + `/file.torrent` + `","size":30,"seeders":3,"protocol":"torrent"}]`))
	})
	mux.HandleFunc("/file.torrent", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "my-key" {
			t.Errorf("torrent fetch X-Api-Key = %q, want my-key", got)
		}
		w.Write(torrentBytes)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithClient(srv.URL, "my-key", srv.Client())
	results, err := c.Search(context.Background(), "q", "", nil, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result recovered via torrent-file fallback, got %d: %+v", len(results), results)
	}
	r := results[0]
	if r.InfoHash == "" {
		t.Error("expected infohash derived from fetched .torrent file")
	}
	if !strings.HasPrefix(r.MagnetURL, "magnet:?xt=urn:btih:"+r.InfoHash) {
		t.Errorf("expected synthesized magnet, got %q", r.MagnetURL)
	}
	mu, err := url.Parse(r.MagnetURL)
	if err != nil {
		t.Fatalf("parse synthesized magnet: %v", err)
	}
	if got := mu.Query()["tr"]; len(got) != 1 || got[0] != tracker {
		t.Errorf("synthesized magnet trackers = %v; want [%s]", got, tracker)
	}
}

func TestSearchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := NewWithClient(srv.URL, "bad", srv.Client())
	if _, err := c.Search(context.Background(), "q", "", nil, nil); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestSearchNoBaseURL(t *testing.T) {
	c := New("", "key")
	if _, err := c.Search(context.Background(), "q", "", nil, nil); err == nil {
		t.Fatal("expected error when base URL unset")
	}
}

func TestSearchCustomType(t *testing.T) {
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.URL.Query().Get("type")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := NewWithClient(srv.URL, "k", srv.Client())
	if _, err := c.Search(context.Background(), "{ImdbId:tt1375666}", "movie", nil, nil); err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotType != "movie" {
		t.Errorf("type = %q, want movie", gotType)
	}
}

func TestSearchOmitsIndexersWhenEmpty(t *testing.T) {
	var hadIndexers bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadIndexers = r.URL.Query()["indexerIds"]
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := NewWithClient(srv.URL, "k", srv.Client())
	if _, err := c.Search(context.Background(), "q", "", nil, nil); err != nil {
		t.Fatalf("search: %v", err)
	}
	if hadIndexers {
		t.Error("indexerIds should be absent when no ids given")
	}
}

func TestIndexers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			t.Errorf("path = %q, want /api/v1/indexer", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "my-key" {
			t.Errorf("X-Api-Key = %q", r.Header.Get("X-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id":1,"name":"Alpha","protocol":"torrent","enable":true,
			 "capabilities":{"movieSearchParams":["Q","ImdbId"],"tvSearchParams":["Q"]}},
			{"id":2,"name":"Beta","protocol":"usenet","enable":false,
			 "capabilities":{"movieSearchParams":["Q"],"tvSearchParams":["Q","ImdbId","Season","Episode"]}}
		]`))
	}))
	defer srv.Close()

	c := NewWithClient(srv.URL, "my-key", srv.Client())
	got, err := c.Indexers(context.Background())
	if err != nil {
		t.Fatalf("indexers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 indexers, got %d", len(got))
	}
	if got[0].ID != 1 || got[0].Name != "Alpha" || !got[0].Enable {
		t.Errorf("indexer[0] = %+v", got[0])
	}
	if got[1].Enable {
		t.Errorf("indexer[1] should be disabled: %+v", got[1])
	}
	if !got[0].SupportsMovieImdb() {
		t.Errorf("indexer[0] should support movie imdb search: %+v", got[0].Capabilities)
	}
	if got[0].SupportsTvImdb() {
		t.Errorf("indexer[0] should not support tv imdb search: %+v", got[0].Capabilities)
	}
	if got[1].SupportsMovieImdb() {
		t.Errorf("indexer[1] should not support movie imdb search: %+v", got[1].Capabilities)
	}
	if !got[1].SupportsTvImdb() {
		t.Errorf("indexer[1] should support tv imdb search: %+v", got[1].Capabilities)
	}
}

func TestIndexersNoBaseURL(t *testing.T) {
	c := New("", "key")
	if _, err := c.Indexers(context.Background()); err == nil {
		t.Fatal("expected error when base URL unset")
	}
}

// magnetResult returns a JSON search response with a single magnet release
// whose title carries id so a test can tell which indexer answered.
func magnetResult(id string) string {
	return `[{"title":"idx-` + id + `","magnetUrl":"magnet:?xt=urn:btih:` + hexHash + `&dn=x","size":100,"seeders":10,"protocol":"torrent","indexer":"` + id + `"}]`
}

// TestSearchEachFanOut verifies each indexer is queried in its own request
// (one indexerIds value per call) and the union of results is returned.
func TestSearchEachFanOut(t *testing.T) {
	var (
		mu    sync.Mutex
		calls [][]string // indexerIds carried by each /search request
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["indexerIds"]
		mu.Lock()
		calls = append(calls, ids)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if len(ids) == 1 {
			w.Write([]byte(magnetResult(ids[0])))
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewWithClient(srv.URL, "k", srv.Client())
	results, err := c.SearchEach(context.Background(), "q", "search", []int{2000}, []int{3, 7, 9}, time.Second)
	if err != nil {
		t.Fatalf("SearchEach: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results (one per indexer), got %d: %+v", len(results), results)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("want 3 requests (one per indexer), got %d: %v", len(calls), calls)
	}
	for _, ids := range calls {
		if len(ids) != 1 {
			t.Errorf("each request should carry exactly one indexerId, got %v", ids)
		}
	}
}

// TestSearchEachReturnsPartialsOnBudget verifies that when the global
// budget elapses, results from indexers that already answered are returned
// and the slow indexer is dropped without failing the search.
func TestSearchEachReturnsPartialsOnBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("indexerIds")
		if id == "2" { // the slow indexer blocks past the budget
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(magnetResult(id)))
	}))
	defer srv.Close()

	c := NewWithClient(srv.URL, "k", srv.Client())
	results, err := c.SearchEach(context.Background(), "q", "search", nil, []int{1, 2, 3}, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("SearchEach should not fail when some indexers answered: %v", err)
	}
	// Indexers 1 and 3 answer immediately; indexer 2 is abandoned at budget.
	got := indexerTitles(results)
	if len(got) != 2 || !slices.Contains(got, "idx-1") || !slices.Contains(got, "idx-3") {
		t.Fatalf("want partial results [idx-1 idx-3], got %v", got)
	}
	if slices.Contains(got, "idx-2") {
		t.Errorf("slow indexer 2 should have been dropped, got %v", got)
	}
}

// TestSearchEachEnumeratesWhenNoIDs verifies that an empty indexer list
// triggers enumeration and only enabled torrent indexers are searched.
func TestSearchEachEnumeratesWhenNoIDs(t *testing.T) {
	var (
		mu       sync.Mutex
		searched []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/indexer", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id":1,"name":"Alpha","protocol":"torrent","enable":true},
			{"id":2,"name":"Beta","protocol":"usenet","enable":true},
			{"id":3,"name":"Gamma","protocol":"torrent","enable":false},
			{"id":4,"name":"Delta","protocol":"torrent","enable":true}
		]`))
	})
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("indexerIds")
		mu.Lock()
		searched = append(searched, id)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(magnetResult(id)))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithClient(srv.URL, "k", srv.Client())
	results, err := c.SearchEach(context.Background(), "q", "search", nil, nil, time.Second)
	if err != nil {
		t.Fatalf("SearchEach: %v", err)
	}
	mu.Lock()
	sort.Strings(searched)
	mu.Unlock()
	// Only enabled torrent indexers (1 and 4); usenet (2) and disabled (3) skipped.
	if len(searched) != 2 || searched[0] != "1" || searched[1] != "4" {
		t.Fatalf("want enabled torrent indexers [1 4] searched, got %v", searched)
	}
	if len(results) != 2 {
		t.Errorf("want 2 results, got %d", len(results))
	}
}

// TestSearchEachAllFail verifies an error surfaces only when every indexer
// fails and nothing was collected.
func TestSearchEachAllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewWithClient(srv.URL, "k", srv.Client())
	if _, err := c.SearchEach(context.Background(), "q", "search", nil, []int{1, 2}, time.Second); err == nil {
		t.Fatal("expected error when every indexer fails")
	}
}

func TestSearchEachNoBaseURL(t *testing.T) {
	c := New("", "key")
	if _, err := c.SearchEach(context.Background(), "q", "search", nil, []int{1}, time.Second); err == nil {
		t.Fatal("expected error when base URL unset")
	}
}

// TestSearchEachEnumerationBoundedByBudget verifies the indexer enumeration
// that happens when no ids are given is itself capped by the budget — a
// hung /api/v1/indexer must not add the full HTTP-client timeout on top.
func TestSearchEachEnumerationBoundedByBudget(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/indexer", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
			w.Write([]byte(`[{"id":1,"protocol":"torrent","enable":true}]`))
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(magnetResult(r.URL.Query().Get("indexerIds"))))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithClient(srv.URL, "k", srv.Client())
	start := time.Now()
	_, err := c.SearchEach(context.Background(), "q", "search", nil, nil, 150*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error when indexer enumeration exceeds budget")
	}
	if elapsed > time.Second {
		t.Fatalf("enumeration should be bounded by the budget (~150ms), took %v", elapsed)
	}
}

// TestSearchEachAllTimeoutIsNotError verifies that when every indexer is
// simply slower than the budget (nothing failed for real), the search
// returns no results and no error — a too-tight budget is the feature
// working, not an outage to alarm on.
func TestSearchEachAllTimeoutIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
			w.Write([]byte(magnetResult(r.URL.Query().Get("indexerIds"))))
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	c := NewWithClient(srv.URL, "k", srv.Client())
	results, err := c.SearchEach(context.Background(), "q", "search", nil, []int{1, 2}, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("a pure budget timeout with no results should not be an error, got %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("want no results on total timeout, got %d", len(results))
	}
}

// TestSearchEachNoBudget verifies budget <= 0 disables the deadline and the
// search still runs to completion.
func TestSearchEachNoBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(magnetResult(r.URL.Query().Get("indexerIds"))))
	}))
	defer srv.Close()

	c := NewWithClient(srv.URL, "k", srv.Client())
	results, err := c.SearchEach(context.Background(), "q", "search", nil, []int{1, 2}, 0)
	if err != nil {
		t.Fatalf("SearchEach: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results with no budget, got %d", len(results))
	}
}

func indexerTitles(results []Result) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Title)
	}
	sort.Strings(out)
	return out
}

// TestSearchDefersTorrentFetchForTrackerlessHash asserts that a release
// carrying an infoHash but no magnet (so no trackers) is not fetched during
// search — indexers rate-limit .torrent downloads — and instead exposes its
// download link for a play-time fetch.
func TestSearchDefersTorrentFetchForTrackerlessHash(t *testing.T) {
	var fetches int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"title":"Hash Only","infoHash":"` + strings.ToUpper(hexHash) + `","downloadUrl":"http://` + r.Host + `/file.torrent","size":30,"seeders":3,"protocol":"torrent"}]`))
	})
	mux.HandleFunc("/file.torrent", func(w http.ResponseWriter, r *http.Request) {
		fetches++
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithClient(srv.URL, "my-key", srv.Client())
	results, err := c.Search(context.Background(), "q", "", nil, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if fetches != 0 {
		t.Errorf("search fetched the .torrent %d times, want 0 (deferred)", fetches)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d: %+v", len(results), results)
	}
	r := results[0]
	if r.InfoHash != hexHash {
		t.Errorf("infohash = %q, want %q", r.InfoHash, hexHash)
	}
	if MagnetHasTrackers(r.MagnetURL) {
		t.Errorf("magnet unexpectedly has trackers: %q", r.MagnetURL)
	}
	if r.DownloadURL != srv.URL+"/file.torrent" {
		t.Errorf("DownloadURL = %q, want the deferred .torrent link", r.DownloadURL)
	}
}
