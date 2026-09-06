package stremio

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/prowlarr"
)

func fakeSearch(counter *atomic.Int64, results []prowlarr.Result, err error) func() ([]prowlarr.Result, error) {
	return func() ([]prowlarr.Result, error) {
		counter.Add(1)
		return results, err
	}
}

func TestSearchCacheHitWithinTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newSearchCache()
	c.now = func() time.Time { return now }

	var calls atomic.Int64
	want := []prowlarr.Result{{Title: "a"}, {Title: "b"}}
	fn := fakeSearch(&calls, want, nil)

	res, hit, err := c.do("k", time.Minute, fn)
	if err != nil || hit {
		t.Fatalf("first do: hit=%v err=%v, want miss and nil error", hit, err)
	}
	if len(res) != 2 {
		t.Fatalf("first do returned %d results, want 2", len(res))
	}

	res, hit, err = c.do("k", time.Minute, fn)
	if err != nil || !hit {
		t.Fatalf("second do: hit=%v err=%v, want hit and nil error", hit, err)
	}
	if len(res) != 2 {
		t.Fatalf("second do returned %d results, want 2", len(res))
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("search invoked %d times, want 1", got)
	}
}

func TestSearchCacheExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newSearchCache()
	c.now = func() time.Time { return now }

	var calls atomic.Int64
	fn := fakeSearch(&calls, []prowlarr.Result{{Title: "a"}}, nil)

	if _, _, err := c.do("k", time.Minute, fn); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + time.Second)
	if _, hit, err := c.do("k", time.Minute, fn); err != nil || hit {
		t.Fatalf("post-expiry do: hit=%v err=%v, want miss", hit, err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("search invoked %d times, want 2", got)
	}
}

func TestSearchCacheZeroTTLAlwaysSearches(t *testing.T) {
	c := newSearchCache()
	var calls atomic.Int64
	fn := fakeSearch(&calls, []prowlarr.Result{{Title: "a"}}, nil)

	for i := 0; i < 3; i++ {
		if _, hit, err := c.do("k", 0, fn); err != nil || hit {
			t.Fatalf("do %d: hit=%v err=%v, want miss", i, hit, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("search invoked %d times, want 3", got)
	}
}

func TestSearchCacheDoesNotCacheEmptyOrError(t *testing.T) {
	c := newSearchCache()

	var emptyCalls atomic.Int64
	empty := fakeSearch(&emptyCalls, nil, nil)
	c.do("empty", time.Minute, empty)
	c.do("empty", time.Minute, empty)
	if got := emptyCalls.Load(); got != 2 {
		t.Errorf("empty result cached: search invoked %d times, want 2", got)
	}

	var errCalls atomic.Int64
	boom := fakeSearch(&errCalls, nil, errors.New("boom"))
	if _, _, err := c.do("err", time.Minute, boom); err == nil {
		t.Fatal("expected error surfaced")
	}
	c.do("err", time.Minute, boom)
	if got := errCalls.Load(); got != 2 {
		t.Errorf("error cached: search invoked %d times, want 2", got)
	}
}

func TestSearchCacheSingleflight(t *testing.T) {
	c := newSearchCache()

	var calls atomic.Int64
	release := make(chan struct{})
	fn := func() ([]prowlarr.Result, error) {
		calls.Add(1)
		<-release
		return []prowlarr.Result{{Title: "a"}}, nil
	}

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, _, err := c.do("k", time.Minute, fn)
			if err != nil || len(res) != 1 {
				t.Errorf("do: res=%v err=%v", res, err)
			}
		}()
	}
	close(start)
	// Give the goroutines a moment to pile onto the same key, then let
	// the single underlying search finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("search invoked %d times, want 1 (singleflight)", got)
	}
}

func TestSearchCacheEvictsOldestOverCap(t *testing.T) {
	now := time.Unix(0, 0)
	c := newSearchCache()
	c.now = func() time.Time { return now }

	one := []prowlarr.Result{{Title: "x"}}
	c.do("oldest", time.Hour, func() ([]prowlarr.Result, error) { return one, nil })
	for i := 0; i < searchCacheMax; i++ {
		now = now.Add(time.Second)
		c.do(time.Duration(i).String(), time.Hour, func() ([]prowlarr.Result, error) { return one, nil })
	}
	if _, hit, _ := c.do("oldest", time.Hour, func() ([]prowlarr.Result, error) { return one, nil }); hit {
		t.Error("oldest entry should have been evicted once over capacity")
	}
}

func TestSearchCacheKey(t *testing.T) {
	base := searchCacheKey("http://p", "q", "movie", []int{2000, 2010}, []int{1, 2})

	// Order-insensitive on slices.
	if got := searchCacheKey("http://p", "q", "movie", []int{2010, 2000}, []int{2, 1}); got != base {
		t.Errorf("key order-sensitive: %q vs %q", got, base)
	}
	// Sensitive to every component.
	for name, other := range map[string]string{
		"url":        searchCacheKey("http://other", "q", "movie", []int{2000, 2010}, []int{1, 2}),
		"query":      searchCacheKey("http://p", "q2", "movie", []int{2000, 2010}, []int{1, 2}),
		"type":       searchCacheKey("http://p", "q", "tvsearch", []int{2000, 2010}, []int{1, 2}),
		"categories": searchCacheKey("http://p", "q", "movie", []int{2000}, []int{1, 2}),
		"indexers":   searchCacheKey("http://p", "q", "movie", []int{2000, 2010}, []int{1}),
	} {
		if other == base {
			t.Errorf("key not sensitive to %s", name)
		}
	}
	// Sorting must not mutate the caller's slices.
	cats := []int{2010, 2000}
	searchCacheKey("http://p", "q", "movie", cats, nil)
	if cats[0] != 2010 {
		t.Error("searchCacheKey mutated caller's categories slice")
	}
}
