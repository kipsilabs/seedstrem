package stream

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

const (
	// prioMinInterval rate-limits PrioritizePieces per hash; identical
	// back-to-back ranges within it are dropped (every blocking read of
	// the same stalled chunk would otherwise re-send the same range).
	prioMinInterval = 2 * time.Second
	// prioUnsupportedBackoff silences the prioritizer after the backend
	// reported ErrNotSupported. Long enough to stop hammering a backend
	// that can't do it, short enough that enabling the Seedstream plugin
	// (or hot-swapping backends) is picked up while a seek's piece wait
	// is still running — a 60s mute here once turned an entire 60s wait
	// into a silent no-prioritization death.
	prioUnsupportedBackoff = 10 * time.Second
	// prioRefreshInterval is how often a blocking piece wait re-sends its
	// deadline hint (WaitForRangeHint), so a hint lost to a stale plugin
	// probe or transient RPC failure is recovered mid-wait.
	prioRefreshInterval = 5 * time.Second
)

// prioKey identifies one hinted range: the playability gate hints the
// head and tail ranges of the same hash concurrently, so dedup state
// must be per (hash, range) — a single per-hash slot would be clobbered
// by the alternating ranges and never dedupe either of them.
type prioKey struct {
	hash        string
	first, last int
}

// prioMaxEntries bounds the dedup map: past it, entries older than
// prioMinInterval (which can no longer suppress anything) are pruned.
const prioMaxEntries = 1024

// prioritizer throttles downloader.PrioritizePieces calls. Fire-and-
// forget: prioritization is a best-effort hint, so failures are logged
// at debug and never surface to the stream.
type prioritizer struct {
	dc     downloader.Client
	logger *slog.Logger

	mu               sync.Mutex
	last             map[prioKey]time.Time
	unsupportedUntil time.Time

	// now is injectable for tests.
	now func() time.Time
}

func newPrioritizer(dc downloader.Client, logger *slog.Logger) *prioritizer {
	if logger == nil {
		logger = slog.Default()
	}
	return &prioritizer{dc: dc, logger: logger, last: map[prioKey]time.Time{}, now: time.Now}
}

// request asks the backend to fetch pieces [first, last] of hash ahead
// of the sequential order, deduplicated and rate-limited per range. It
// reports whether the hint was actually delivered: a false return means
// the window never reached the piece picker (declined, unsupported, RPC
// failure, or suppressed by dedup) and the caller should try again
// promptly rather than assume the pieces are now on their way.
func (p *prioritizer) request(ctx context.Context, hash string, first, last int) bool {
	if p == nil || first > last {
		return false
	}
	now := p.now()
	key := prioKey{hash: hash, first: first, last: last}

	p.mu.Lock()
	if now.Before(p.unsupportedUntil) {
		p.mu.Unlock()
		return false
	}
	if at, ok := p.last[key]; ok && now.Sub(at) < prioMinInterval {
		p.mu.Unlock()
		return false
	}
	if len(p.last) >= prioMaxEntries {
		for k, at := range p.last {
			if now.Sub(at) >= prioMinInterval {
				delete(p.last, k)
			}
		}
	}
	p.last[key] = now
	p.mu.Unlock()

	err := p.dc.PrioritizePieces(ctx, hash, first, last)
	switch {
	case err == nil:
		p.logger.Debug("stream: prioritized pieces", "hash", hash, "first", first, "last", last)
		return true
	case errors.Is(err, downloader.ErrHintDeclined):
		// Never reached the picker, and the backend is perfectly capable
		// — so drop the dedup entry too, or the immediate retry this
		// return asks for would be suppressed as a duplicate.
		p.mu.Lock()
		delete(p.last, key)
		p.mu.Unlock()
		p.logger.Debug("stream: piece prioritization declined", "hash", hash, "first", first, "last", last)
	case errors.Is(err, downloader.ErrNotSupported):
		p.mu.Lock()
		p.unsupportedUntil = now.Add(prioUnsupportedBackoff)
		p.mu.Unlock()
	default:
		p.logger.Debug("stream: prioritize pieces failed", "hash", hash, "first", first, "last", last, "error", err)
	}
	return false
}

// readaheadPieces is how many pieces past the requested range get
// prioritized along with it (~32 MiB, at least 8 pieces), so a seek
// lands with enough deadline-fetched runway that playback keeps flowing
// while the next hint batch is requested. Deadlines are staggered
// plugin-side, so a wide window still delivers the awaited piece first.
func readaheadPieces(pieceSize int64) int {
	if pieceSize <= 0 {
		return 8
	}
	n := int((32 << 20) / pieceSize)
	if n < 8 {
		n = 8
	}
	return n
}
