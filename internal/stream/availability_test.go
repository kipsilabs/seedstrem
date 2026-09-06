package stream

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
)

const testHash = "0123456789abcdef0123456789abcdef01234567"

func newAvail(t *testing.T) (*Availability, *fake.Server) {
	t.Helper()
	f := fake.New()
	return NewAvailability(f), f
}

func TestHaveRange(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{2, 2, 1, 0}})

	ctx := context.Background()
	tests := []struct {
		first, last int
		want        bool
		wantErr     bool
	}{
		{0, 1, true, false},
		{0, 2, false, false},
		{3, 3, false, false},
		{0, 10, false, false}, // past known states → not ready yet (buffer), not an error
		{-1, 0, false, true},  // negative index is a real error
	}
	for _, tt := range tests {
		// Fresh availability per case to defeat the 1s cache when the
		// fake state changes between cases (it doesn't here, but keeps
		// the test honest).
		have, err := a.HaveRange(ctx, testHash, tt.first, tt.last)
		if tt.wantErr {
			if err == nil {
				t.Errorf("HaveRange(%d,%d): expected error", tt.first, tt.last)
			}
			continue
		}
		if err != nil {
			t.Fatalf("HaveRange(%d,%d): %v", tt.first, tt.last, err)
		}
		if have != tt.want {
			t.Errorf("HaveRange(%d,%d) = %v; want %v", tt.first, tt.last, have, tt.want)
		}
	}
}

func TestRangeAtLeast(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{2, 2, 1, 0}})

	ctx := context.Background()
	tests := []struct {
		first, last int
		min         downloader.PieceState
		want        bool
	}{
		{0, 2, downloader.PieceDownloading, true},  // in flight counts
		{0, 3, downloader.PieceDownloading, false}, // piece 3 is plain missing
		{2, 2, downloader.PieceHave, false},        // downloading is not on disk
		{0, 1, downloader.PieceHave, true},
		{0, 10, downloader.PieceDownloading, false}, // past known states → not ready yet
	}
	for _, tt := range tests {
		got, err := a.RangeAtLeast(ctx, testHash, tt.first, tt.last, tt.min)
		if err != nil {
			t.Fatalf("RangeAtLeast(%d,%d,%d): %v", tt.first, tt.last, tt.min, err)
		}
		if got != tt.want {
			t.Errorf("RangeAtLeast(%d,%d,%d) = %v; want %v", tt.first, tt.last, tt.min, got, tt.want)
		}
	}
}

func TestWaitForRangeAtLeastReturnsOnceInFlight(t *testing.T) {
	// A piece the swarm starts serving (missing → downloading) satisfies
	// a min-state-downloading wait without ever landing on disk.
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{0}})

	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	var sleeps atomic.Int32
	a.sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		if sleeps.Add(1) == 2 {
			f.Update(testHash, func(tor *fake.Torrent) { tor.PieceStates = []int{1} })
		}
		return nil
	}

	err := a.WaitForRangeAtLeast(context.Background(), testHash, 0, 0, downloader.PieceDownloading, 10*time.Second, 0, nil)
	if err != nil {
		t.Fatalf("WaitForRangeAtLeast: %v", err)
	}
}

func TestWaitForRangeSucceedsWhenPiecesArrive(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{0, 0}})

	// Deterministic clock: each sleep advances fake time and flips a
	// piece, simulating download progress.
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	var sleeps atomic.Int32
	a.sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		n := sleeps.Add(1)
		if n == 2 {
			f.Update(testHash, func(tor *fake.Torrent) { tor.PieceStates = []int{2, 2} })
		}
		return nil
	}

	err := a.WaitForRange(context.Background(), testHash, 0, 1, 10*time.Second)
	if err != nil {
		t.Fatalf("WaitForRange: %v", err)
	}
	if sleeps.Load() < 2 {
		t.Errorf("expected at least 2 polls, got %d", sleeps.Load())
	}
}

func TestWaitForRangeTimesOut(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{0}})

	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	a.sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}

	err := a.WaitForRange(context.Background(), testHash, 0, 0, 3*time.Second)
	if !errors.Is(err, ErrWaitTimeout) {
		t.Errorf("want ErrWaitTimeout, got %v", err)
	}
}

func TestWaitForRangeHintRehintsWhileWaiting(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{0}})

	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	a.sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}

	hints := 0
	err := a.WaitForRangeHint(context.Background(), testHash, 0, 0, 12*time.Second, 5*time.Second, func() bool { hints++; return true })
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("want ErrWaitTimeout, got %v", err)
	}
	// Hinted immediately, then again at ~5s and ~10s of waiting: a hint
	// dropped by a stale plugin probe or backoff is recovered mid-wait.
	if hints != 3 {
		t.Errorf("hints = %d, want 3 (initial + refresh every 5s over 12s)", hints)
	}
}

func TestWaitForRangeHintRetriesDeclinedHintOnNextPoll(t *testing.T) {
	// The first hint of a play lands ~70ms after the torrent was added,
	// when the plugin still declines it. Waiting the full refresh
	// interval to try again burns a third of the playability grace on a
	// hint that was never delivered, so a declined hint re-fires on the
	// next poll instead.
	for _, tt := range []struct {
		name      string
		accepted  bool
		wantHints int
	}{
		{"accepted hint refreshes on interval", true, 1},
		{"declined hint retries every poll", false, 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, f := newAvail(t)
			f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{0}})

			now := time.Unix(1000, 0)
			a.now = func() time.Time { return now }
			a.sleep = func(_ context.Context, d time.Duration) error {
				now = now.Add(d)
				return nil
			}

			hints := 0
			// 2s window, 5s refresh: an accepted hint fires once, so any
			// extra hint is the declined-retry path and nothing else.
			err := a.WaitForRangeHint(context.Background(), testHash, 0, 0, 2*time.Second, 5*time.Second,
				func() bool { hints++; return tt.accepted })
			if !errors.Is(err, ErrWaitTimeout) {
				t.Fatalf("want ErrWaitTimeout, got %v", err)
			}
			if hints != tt.wantHints {
				t.Errorf("hints = %d, want %d", hints, tt.wantHints)
			}
		})
	}
}

func TestWaitForRangeHintSkipsAvailableRange(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{2, 2}})

	hints := 0
	if err := a.WaitForRangeHint(context.Background(), testHash, 0, 1, time.Second, 5*time.Second, func() bool { hints++; return true }); err != nil {
		t.Fatalf("WaitForRangeHint: %v", err)
	}
	if hints != 0 {
		t.Errorf("hints = %d, want 0 (range already on disk)", hints)
	}
}

func TestWaitForRangeContextCancelled(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{0}})

	ctx, cancel := context.WithCancel(context.Background())
	a.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	err := a.WaitForRange(ctx, testHash, 0, 0, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestStatesCacheReducesFetches(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{2}})

	ctx := context.Background()
	for range 5 {
		if _, err := a.HaveRange(ctx, testHash, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	// The fake doesn't count reads, but a wrong cache would show up as
	// an error after Forget + server-side removal.
	a.Forget(testHash)
	f.Remove(testHash)
	if _, err := a.HaveRange(ctx, testHash, 0, 0); err == nil {
		t.Error("expected error after torrent removed and cache forgotten")
	} else if !strings.Contains(err.Error(), "piece states") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSummary(t *testing.T) {
	a, f := newAvail(t)
	// pieces: 0=have 1=downloading 2=missing 3=have 4=missing(last)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{2, 1, 0, 2, 0}})

	sum, err := a.Summary(context.Background(), testHash, 0, 1)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.TotalPieces != 5 || sum.Have != 2 || sum.Downloading != 1 {
		t.Errorf("counts = %+v, want total=5 have=2 downloading=1", sum)
	}
	if sum.FirstMissing != 1 {
		t.Errorf("frontier = %d, want 1 (first non-have piece)", sum.FirstMissing)
	}
	if got := pieceStateName(sum.HeadState); got != "downloading" {
		t.Errorf("head state = %q, want downloading (worst of pieces 0-1)", got)
	}
	if got := pieceStateName(sum.LastState); got != "missing" {
		t.Errorf("last state = %q, want missing", got)
	}
}

func TestSummaryHeadBeyondKnownBitfield(t *testing.T) {
	a, f := newAvail(t)
	f.Put(&fake.Torrent{Hash: testHash, PieceStates: []int{2}})

	sum, err := a.Summary(context.Background(), testHash, 0, 3)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got := pieceStateName(sum.HeadState); got != "missing" {
		t.Errorf("head state past bitfield = %q, want missing", got)
	}
	if sum.FirstMissing != -1 {
		t.Errorf("frontier = %d, want -1 (all known pieces downloaded)", sum.FirstMissing)
	}
}
