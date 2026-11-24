package stremio

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/prowlarr"
)

func progressiveHandler() *Handler {
	return &Handler{searchCache: newSearchCache(), logger: slog.New(slog.DiscardHandler)}
}

func progressiveSettings(quick time.Duration) Settings {
	return Settings{Prowlarr: ProwlarrSettings{QuickTimeout: quick, SearchTimeout: time.Second, SearchCacheTTL: time.Hour}}
}

func titles(rs []prowlarr.Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

// waitFlightDone waits until no background flight is left for key.
func waitFlightDone(t *testing.T, h *Handler, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.flightsMu.Lock()
		_, running := h.flights[key]
		h.flightsMu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background search did not finish")
}

func TestProgressiveSearchAnswersEarlyThenCachesFullSet(t *testing.T) {
	h := progressiveHandler()
	release := make(chan struct{})
	search := func(ctx context.Context) ([]prowlarr.Result, error) {
		first := prowlarr.Result{Title: "first"}
		prowlarr.Report(ctx, first)
		<-release
		second := prowlarr.Result{Title: "second"}
		prowlarr.Report(ctx, second)
		return []prowlarr.Result{first, second}, nil
	}

	start := time.Now()
	got, err := h.progressiveSearch(context.Background(), "k", progressiveSettings(50*time.Millisecond), search)
	if err != nil || len(got) != 1 || got[0].Title != "first" {
		t.Fatalf("early answer = %v, %v; want [first]", titles(got), err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("early answer took %v, want ~QuickTimeout", took)
	}

	close(release)
	waitFlightDone(t, h, "k")
	calledAgain := false
	got, err = h.progressiveSearch(context.Background(), "k", progressiveSettings(50*time.Millisecond), func(context.Context) ([]prowlarr.Result, error) {
		calledAgain = true
		return nil, nil
	})
	if err != nil || len(got) != 2 || calledAgain {
		t.Errorf("after the background search: %v (searched again: %v), want both results from cache", titles(got), calledAgain)
	}
}

func TestProgressiveSearchReturnsFullSetWhenFast(t *testing.T) {
	h := progressiveHandler()
	got, err := h.progressiveSearch(context.Background(), "k", progressiveSettings(time.Second), func(context.Context) ([]prowlarr.Result, error) {
		return []prowlarr.Result{{Title: "a"}, {Title: "b"}}, nil
	})
	if err != nil || len(got) != 2 {
		t.Errorf("got %v, %v; want both results", titles(got), err)
	}
}

func TestProgressiveSearchSharesOneFlightAndSurvivesClientCancel(t *testing.T) {
	h := progressiveHandler()
	var calls atomic.Int32
	release := make(chan struct{})
	searchCtxErr := make(chan error, 1)
	search := func(ctx context.Context) ([]prowlarr.Result, error) {
		calls.Add(1)
		<-release
		searchCtxErr <- ctx.Err()
		return []prowlarr.Result{{Title: "x"}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := progressiveSettings(20 * time.Millisecond)
	if _, err := h.progressiveSearch(ctx, "k", s, search); err != nil {
		t.Fatal(err)
	}
	if _, err := h.progressiveSearch(context.Background(), "k", s, search); err != nil {
		t.Fatal(err)
	}
	cancel() // the first client went away
	close(release)
	if err := <-searchCtxErr; err != nil {
		t.Errorf("background search saw its context end (%v): a departing client must not cancel it", err)
	}
	waitFlightDone(t, h, "k")
	if n := calls.Load(); n != 1 {
		t.Errorf("search ran %d times for concurrent callers, want 1", n)
	}
}

func TestProgressiveSearchDoesNotCacheEmptyResult(t *testing.T) {
	h := progressiveHandler()
	if _, err := h.progressiveSearch(context.Background(), "k", progressiveSettings(time.Second), func(context.Context) ([]prowlarr.Result, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.searchCache.get("k", time.Hour); ok {
		t.Error("an empty result was cached; a retry would be hidden for the whole TTL")
	}
}
