package stream

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// prioSpy records PrioritizePieces calls with a scriptable error.
type prioSpy struct {
	downloader.Client
	mu    sync.Mutex
	calls []string
	err   error
}

func (s *prioSpy) PrioritizePieces(_ context.Context, hash string, first, last int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, hashRange(hash, first, last))
	return s.err
}

func hashRange(hash string, first, last int) string {
	return hash + ":" + itoa(first) + "-" + itoa(last)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (s *prioSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func TestPrioritizerDedupesIdenticalRange(t *testing.T) {
	spy := &prioSpy{}
	p := newPrioritizer(spy, nil)
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }
	ctx := context.Background()

	p.request(ctx, "abc", 10, 20)
	p.request(ctx, "abc", 10, 20) // identical, within interval → dropped
	if spy.count() != 1 {
		t.Fatalf("calls = %d, want 1 (dedupe)", spy.count())
	}

	// A different range (the reader moved) goes through immediately.
	p.request(ctx, "abc", 30, 40)
	if spy.count() != 2 {
		t.Fatalf("calls = %d, want 2 (retarget)", spy.count())
	}

	// The identical range goes through again after the interval.
	now = now.Add(prioMinInterval + time.Millisecond)
	p.request(ctx, "abc", 30, 40)
	if spy.count() != 3 {
		t.Fatalf("calls = %d, want 3 (interval elapsed)", spy.count())
	}
}

func TestPrioritizerDedupesInterleavedRanges(t *testing.T) {
	// The playability gate hints the head and tail ranges of the same
	// hash concurrently. Alternating ranges must not clobber each
	// other's dedup slot: a repeat of either range within the interval
	// stays deduplicated.
	spy := &prioSpy{}
	p := newPrioritizer(spy, nil)
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }
	ctx := context.Background()

	p.request(ctx, "abc", 0, 0)       // head
	p.request(ctx, "abc", 8768, 8768) // tail
	p.request(ctx, "abc", 0, 0)       // head repeat within interval → dropped
	p.request(ctx, "abc", 8768, 8768) // tail repeat within interval → dropped
	if spy.count() != 2 {
		t.Fatalf("calls = %d, want 2 (interleaved ranges dedupe independently)", spy.count())
	}
}

func TestPrioritizerBacksOffWhenUnsupported(t *testing.T) {
	spy := &prioSpy{err: downloader.ErrNotSupported}
	p := newPrioritizer(spy, nil)
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }
	ctx := context.Background()

	p.request(ctx, "abc", 10, 20)
	p.request(ctx, "abc", 30, 40) // silenced by the backoff
	p.request(ctx, "def", 0, 5)   // other hashes silenced too
	if spy.count() != 1 {
		t.Fatalf("calls = %d, want 1 (unsupported backoff)", spy.count())
	}

	// After the backoff (e.g. hot-swap to a capable backend) it retries.
	spy.err = nil
	now = now.Add(prioUnsupportedBackoff + time.Second)
	p.request(ctx, "abc", 10, 20)
	if spy.count() != 2 {
		t.Fatalf("calls = %d, want 2 (retry after backoff)", spy.count())
	}
	if !strings.HasPrefix(spy.calls[1], "abc:") {
		t.Errorf("unexpected call %q", spy.calls[1])
	}
}

func TestPrioritizerRetriesDeclinedHint(t *testing.T) {
	// A hint the plugin declined (torrent not registered yet — the usual
	// answer in the first moments after an add) never reached libtorrent,
	// so it must neither be reported as accepted nor occupy the dedup
	// slot that would suppress the immediate retry.
	spy := &prioSpy{err: downloader.ErrHintDeclined}
	p := newPrioritizer(spy, nil)
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }
	ctx := context.Background()

	if p.request(ctx, "abc", 10, 20) {
		t.Error("declined hint reported as accepted")
	}
	if p.request(ctx, "abc", 10, 20) { // identical range, well within prioMinInterval
		t.Error("declined hint reported as accepted on retry")
	}
	if spy.count() != 2 {
		t.Fatalf("calls = %d, want 2 (a declined hint is retried, not deduped)", spy.count())
	}

	// Once the plugin accepts, normal dedup resumes.
	spy.err = nil
	if !p.request(ctx, "abc", 10, 20) {
		t.Error("accepted hint reported as declined")
	}
	p.request(ctx, "abc", 10, 20)
	if spy.count() != 3 {
		t.Fatalf("calls = %d, want 3 (dedup resumes after acceptance)", spy.count())
	}
}

func TestPrioritizerSwallowsOtherErrors(t *testing.T) {
	spy := &prioSpy{err: errors.New("transport down")}
	p := newPrioritizer(spy, nil)
	ctx := context.Background()

	p.request(ctx, "abc", 10, 20) // must not panic or backoff
	p.request(ctx, "abc", 30, 40)
	if spy.count() != 2 {
		t.Fatalf("calls = %d, want 2 (no backoff on transient errors)", spy.count())
	}
}

func TestReadaheadPieces(t *testing.T) {
	if got := readaheadPieces(2 << 20); got != 16 {
		t.Errorf("2MiB pieces → %d, want 16 (32MiB window)", got)
	}
	if got := readaheadPieces(16 << 20); got != 8 {
		t.Errorf("16MiB pieces → %d, want 8 (floor)", got)
	}
	if got := readaheadPieces(1 << 20); got != 32 {
		t.Errorf("1MiB pieces → %d, want 32", got)
	}
	if got := readaheadPieces(0); got != 8 {
		t.Errorf("unknown piece size → %d, want 8", got)
	}
}
