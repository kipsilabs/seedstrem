package torrents

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

const (
	// warmupHeadBytes / warmupTailBytes are how much of the played file's
	// start and end are deadline-fetched as soon as the file is selected.
	// Containers keep their header (MP4 moov, MKV segment info) at the
	// head and their seek index (MKV cues, MP4 moov when not faststart)
	// at the tail; a player cannot start until it has both, and a single
	// piece is often too small to hold them.
	warmupHeadBytes = 8 << 20
	warmupTailBytes = 4 << 20

	// warmupRetryInterval / warmupMaxAttempts bound how long a declined
	// hint is retried (the plugin declines until the torrent's metadata
	// is fully loaded, which on a cold magnet can take a while).
	warmupRetryInterval = 2 * time.Second
	warmupMaxAttempts   = 15
)

// warmupTimeout caps the whole background warm-up, including sleeps.
const warmupTimeout = warmupMaxAttempts*warmupRetryInterval + 10*time.Second

// pieceRange is an inclusive range of torrent piece indices.
type pieceRange struct{ first, last int }

// warmupRanges returns the piece ranges holding the head and tail of the
// file with the given index, merged into one when they touch or overlap.
// It returns nil when the ranges cannot be computed (unknown piece size,
// unknown or empty file).
func warmupRanges(files []downloader.FileInfo, fileIndex int, pieceSize int64) []pieceRange {
	if pieceSize <= 0 {
		return nil
	}
	sorted := make([]downloader.FileInfo, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })

	var offset, size int64
	found := false
	for _, f := range sorted {
		if f.Index == fileIndex {
			size, found = f.Size, true
			break
		}
		offset += f.Size
	}
	if !found || size <= 0 {
		return nil
	}

	pieceAt := func(fileByte int64) int { return int((offset + fileByte) / pieceSize) }
	headEnd := min(warmupHeadBytes, size) - 1
	tailStart := max(0, size-warmupTailBytes)

	head := pieceRange{pieceAt(0), pieceAt(headEnd)}
	tail := pieceRange{pieceAt(tailStart), pieceAt(size - 1)}
	if head.last+1 >= tail.first {
		return []pieceRange{{head.first, max(head.last, tail.last)}}
	}
	return []pieceRange{head, tail}
}

// startWarmup kicks off the background warm-up for a freshly selected
// file. It never blocks the caller and never fails it: warm-up is a
// best-effort head start for playback.
func (s *Service) startWarmup(ctx context.Context, hash string, fileIndex int, files []downloader.FileInfo) {
	// Detach from the request: the resolve call returns long before the
	// swarm has delivered anything.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), warmupTimeout)
	go func() {
		defer cancel()
		s.runWarmup(wctx, hash, fileIndex, files)
	}()
}

// runWarmup asks the download client to fetch the file's head and tail
// ahead of the regular piece order, retrying hints the backend declined.
// Backends without piece prioritization (qBittorrent, Deluge without the
// Seedstream plugin) report ErrNotSupported and are left alone.
func (s *Service) runWarmup(ctx context.Context, hash string, fileIndex int, files []downloader.FileInfo) {
	props, err := s.dc.Properties(ctx, hash)
	if err != nil {
		s.logger.Debug("torrents: warm-up skipped, properties unavailable", "hash", hash, "error", err)
		return
	}
	ranges := warmupRanges(files, fileIndex, props.PieceSize)
	if len(ranges) == 0 {
		return
	}

	// Concurrent, so a slow head cannot starve the tail's retries.
	var wg sync.WaitGroup
	for _, rng := range ranges {
		wg.Add(1)
		go func(rng pieceRange) {
			defer wg.Done()
			s.hintRange(ctx, hash, rng)
		}(rng)
	}
	wg.Wait()
}

func (s *Service) hintRange(ctx context.Context, hash string, rng pieceRange) {
	for attempt := 1; attempt <= warmupMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		err := s.dc.PrioritizePieces(ctx, hash, rng.first, rng.last)
		switch {
		case err == nil:
			s.logger.Debug("torrents: warm-up hinted pieces", "hash", hash, "first", rng.first, "last", rng.last)
			return
		case errors.Is(err, downloader.ErrHintDeclined):
			if s.sleep(ctx, warmupRetryInterval) != nil {
				return
			}
		case errors.Is(err, downloader.ErrNotSupported):
			return
		default:
			s.logger.Debug("torrents: warm-up hint failed", "hash", hash, "first", rng.first, "last", rng.last, "error", err)
			return
		}
	}
	s.logger.Debug("torrents: warm-up gave up on declined hint", "hash", hash, "first", rng.first, "last", rng.last)
}
