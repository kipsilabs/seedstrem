package stremio

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/kipsilabs/seedstrem/internal/prowlarr"
)

// searchCacheMax caps the number of cached search result sets so a burst
// of distinct searches cannot grow memory without bound. Oldest entries
// are evicted first.
const searchCacheMax = 256

type searchCacheEntry struct {
	results []prowlarr.Result
	at      time.Time
}

// searchCache is a small, bounded, TTL cache of Prowlarr search results
// keyed by search identity (see searchCacheKey), plus an in-flight
// dedupe: concurrent identical searches share one underlying Prowlarr
// call instead of fanning out N times. The TTL is passed per lookup so a
// live config change to prowlarr.search_cache_ttl takes effect without a
// restart; a TTL <= 0 disables caching (every call searches live) while
// the singleflight dedupe still applies.
type searchCache struct {
	mu      sync.Mutex
	entries map[string]searchCacheEntry
	now     func() time.Time // injectable for tests
	group   singleflight.Group
}

func newSearchCache() *searchCache {
	return &searchCache{
		entries: map[string]searchCacheEntry{},
		now:     time.Now,
	}
}

// do returns the cached results for key when present and younger than
// ttl (hit=true), otherwise runs search — collapsed across concurrent
// callers of the same key — and caches its results. Errors and empty
// result sets are never cached: SearchEach returns (nil, nil) on a pure
// budget timeout, and pinning that empty list for the TTL would hide
// results that a retry moments later would have found.
func (c *searchCache) do(key string, ttl time.Duration, search func() ([]prowlarr.Result, error)) (results []prowlarr.Result, hit bool, err error) {
	if ttl > 0 {
		if res, ok := c.get(key, ttl); ok {
			return res, true, nil
		}
	}
	v, err, _ := c.group.Do(key, func() (any, error) {
		// Re-check under singleflight: a caller queued behind the flight
		// that just populated the cache should not trigger a fresh search.
		if ttl > 0 {
			if res, ok := c.get(key, ttl); ok {
				return res, nil
			}
		}
		res, err := search()
		if err == nil && len(res) > 0 && ttl > 0 {
			c.put(key, res)
		}
		return res, err
	})
	if err != nil {
		return nil, false, err
	}
	return v.([]prowlarr.Result), false, nil
}

// get returns the cached results for key, or ok=false if absent or older
// than ttl. Expired entries are left for put's sweep to reclaim.
func (c *searchCache) get(key string, ttl time.Duration) ([]prowlarr.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().Sub(e.at) > ttl {
		return nil, false
	}
	return e.results, true
}

func (c *searchCache) put(key string, results []prowlarr.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= searchCacheMax {
		c.evictOldestLocked()
	}
	c.entries[key] = searchCacheEntry{results: results, at: c.now()}
}

func (c *searchCache) evictOldestLocked() {
	var oldestKey string
	var oldestAt time.Time
	for k, e := range c.entries {
		if oldestKey == "" || e.at.Before(oldestAt) {
			oldestKey, oldestAt = k, e.at
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// searchCacheKey builds the cache key from everything that identifies a
// Prowlarr search. The URL is included so a config change to the Prowlarr
// endpoint never serves results from the old one; categories and indexer
// ids are order-normalized (on copies) so equivalent searches share an
// entry.
func searchCacheKey(prowlarrURL, query, searchType string, categories, indexerIDs []int) string {
	cats := slices.Clone(categories)
	slices.Sort(cats)
	ids := slices.Clone(indexerIDs)
	slices.Sort(ids)
	return fmt.Sprintf("%s|%s|%s|%v|%v", prowlarrURL, query, searchType, cats, ids)
}
