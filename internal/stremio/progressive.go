package stremio

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/kipsilabs/seedstrem/internal/prowlarr"
)

// discoveryFlight is one uncached discovery search running in the
// background. partial grows as results resolve; results/err are set once
// the search ends, just before done is closed.
type discoveryFlight struct {
	mu      sync.Mutex
	partial []prowlarr.Result
	done    chan struct{}
	results []prowlarr.Result
	err     error
}

func (f *discoveryFlight) add(r prowlarr.Result) {
	f.mu.Lock()
	f.partial = append(f.partial, r)
	f.mu.Unlock()
}

func (f *discoveryFlight) snapshot() []prowlarr.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.partial)
}

// flightBackstop bounds a background search beyond its own budget, in case
// a backend ignores the per-search deadline.
const flightBackstop = 30 * time.Second

// progressiveSearch answers a discovery search within QuickTimeout. A
// cached result is returned at once. Otherwise the search starts (or is
// joined, for concurrent callers of the same key) in the background,
// detached from this request so a client that stops waiting cannot cancel
// it. The caller gets the complete result if it finishes within
// QuickTimeout, else the results resolved so far. The complete set is
// cached when the search ends (errors and empty sets are not, as in
// searchCache.do), so the next request gets everything.
func (h *Handler) progressiveSearch(ctx context.Context, key string, s Settings, search func(context.Context) ([]prowlarr.Result, error)) ([]prowlarr.Result, error) {
	ttl := s.Prowlarr.SearchCacheTTL
	if ttl > 0 {
		if res, ok := h.searchCache.get(key, ttl); ok {
			return slices.Clone(res), nil
		}
	}
	f := h.joinFlight(ctx, key, s, search)

	timer := time.NewTimer(s.Prowlarr.QuickTimeout)
	defer timer.Stop()
	select {
	case <-f.done:
		return slices.Clone(f.results), f.err
	case <-timer.C:
	case <-ctx.Done():
	}
	partial := f.snapshot()
	h.logger.Info("stremio: answering with partial search results, full search continues in the background",
		"partial", len(partial), "waited", s.Prowlarr.QuickTimeout)
	return partial, nil
}

func (h *Handler) joinFlight(ctx context.Context, key string, s Settings, search func(context.Context) ([]prowlarr.Result, error)) *discoveryFlight {
	h.flightsMu.Lock()
	defer h.flightsMu.Unlock()
	if f, ok := h.flights[key]; ok {
		return f
	}
	if h.flights == nil {
		h.flights = make(map[string]*discoveryFlight)
	}
	f := &discoveryFlight{done: make(chan struct{})}
	h.flights[key] = f

	limit := flightBackstop
	if s.Prowlarr.SearchTimeout > 0 {
		limit += 2 * s.Prowlarr.SearchTimeout
	} else {
		limit += 5 * time.Minute
	}
	fctx, cancel := context.WithTimeout(prowlarr.WithProgress(context.WithoutCancel(ctx), f.add), limit)
	go func() {
		defer cancel()
		started := time.Now()
		res, err := search(fctx)
		if err == nil && len(res) > 0 && s.Prowlarr.SearchCacheTTL > 0 {
			h.searchCache.put(key, res)
		}
		f.results, f.err = res, err
		h.flightsMu.Lock()
		delete(h.flights, key)
		h.flightsMu.Unlock()
		close(f.done)
		h.logger.Debug("stremio: background discovery search finished",
			"results", len(res), "took", time.Since(started), "error", err)
	}()
	return f
}
