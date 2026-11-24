package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSearchReportsEachResolvedResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"title":"One 1080p","magnetUrl":"magnet:?xt=urn:btih:` + hexHash + `&dn=x","size":100,"seeders":10,"protocol":"torrent"},
			{"title":"Usenet","infoHash":"deadbeef","protocol":"usenet","size":40,"seeders":99},
			{"title":"Two 720p","infoHash":"` + hexHash + `","size":50,"seeders":5,"protocol":"torrent"}
		]`))
	}))
	defer srv.Close()

	var mu sync.Mutex
	var seen []string
	ctx := WithProgress(context.Background(), func(r Result) {
		mu.Lock()
		seen = append(seen, r.Title)
		mu.Unlock()
	})
	c := NewWithClient(srv.URL, "k", srv.Client())
	results, err := c.Search(ctx, "q", "", nil, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(seen) != len(results) || len(seen) != 2 || seen[0] != "One 1080p" || seen[1] != "Two 720p" {
		t.Errorf("reported %v, want the 2 kept results in order (results: %d)", seen, len(results))
	}
}

func TestReportWithoutProgressIsNoop(t *testing.T) {
	Report(context.Background(), Result{Title: "x"}) // must not panic
}
