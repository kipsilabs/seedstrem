// Package cleanup periodically removes torrents that have finished
// seeding — either past their configured seed time or past a target
// seeding ratio — in a configurable delete order.
package cleanup

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/diskusage"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/playsession"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

// Settings is the live configuration slice the cleanup loop needs.
type Settings struct {
	// SeedTime is how long a completed torrent may seed before removal.
	// SeedTime <= 0 disables the seed-time trigger.
	SeedTime time.Duration
	// TargetRatio is the seeding ratio at or above which a completed
	// torrent becomes removal-eligible, OR-ed with SeedTime. <= 0 disables
	// the ratio trigger.
	TargetRatio float64
	// DeletePolicy orders eligible torrents before removal:
	// config.DeletePolicyOldestFirst (default) or
	// config.DeletePolicyLowestUpload.
	DeletePolicy string
	// IndexerSeedTimes overrides SeedTime per Prowlarr indexer name,
	// matched case-insensitively against store.Torrent.Indexer.
	IndexerSeedTimes map[string]time.Duration
	// DiskPressurePercent, DiskPressureSeedTime: see config.Cleanup. DiskPath
	// is the local download directory whose filesystem is measured.
	DiskPressurePercent  int
	DiskPressureSeedTime time.Duration
	DiskPath             string
}

// pressureEnabled reports whether the disk-pressure pass can run.
func (s Settings) pressureEnabled() bool {
	return s.DiskPressurePercent > 0 && s.DiskPressureSeedTime > 0 && s.DiskPath != ""
}

// EffectiveSeedTime returns the seed time governing a torrent grabbed from
// the named indexer: the per-indexer override when one exists, otherwise
// SeedTime.
func (s Settings) EffectiveSeedTime(indexer string) time.Duration {
	return config.EffectiveSeedTime(s.SeedTime, s.IndexerSeedTimes, indexer)
}

// Cleanup periodically sweeps stored torrents and removes finished ones
// that have seeded past Settings.SeedTime.
type Cleanup struct {
	store    *store.Store
	dc       downloader.Client
	svc      *torrents.Service
	sessions *playsession.Sessions
	settings func() Settings
	logger   *slog.Logger
	interval time.Duration
	// diskUsage measures the download disk (used, total bytes); a field so
	// tests can fake it.
	diskUsage func(path string) (used, total int64, err error)
}

// New creates a Cleanup. sessions is consulted before removing a
// torrent so an actively-streamed torrent is never deleted out from
// under a viewer just because it has seeded past its time limit.
func New(st *store.Store, dc downloader.Client, svc *torrents.Service, sessions *playsession.Sessions, settings func() Settings, logger *slog.Logger, interval time.Duration) *Cleanup {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	return &Cleanup{store: st, dc: dc, svc: svc, sessions: sessions, settings: settings, logger: logger, interval: interval,
		diskUsage: diskusage.Stat}
}

// Run loops until ctx is cancelled.
func (c *Cleanup) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Sweep(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("cleanup sweep failed", "error", err)
			}
		}
	}
}

// Sweep performs one cleanup pass: completed torrents that have met their
// removal trigger (seed time OR target ratio) are removed, in the order
// set by Settings.DeletePolicy.
func (c *Cleanup) Sweep(ctx context.Context) error {
	s := c.settings()
	// Nothing to do when both removal triggers are disabled. A global
	// seed time of 0 still sweeps when some indexer overrides it upward.
	if !config.HasSeedTimeTrigger(s.SeedTime, s.IndexerSeedTimes) && s.TargetRatio <= 0 && !s.pressureEnabled() {
		return nil
	}

	stored, err := c.store.AllTorrents(ctx)
	if err != nil {
		return err
	}
	if len(stored) == 0 {
		return nil
	}

	hashes := make([]string, len(stored))
	for i, tor := range stored {
		hashes[i] = tor.Hash
	}
	dcTorrents, err := c.dc.Torrents(ctx, hashes)
	if err != nil {
		return err
	}
	live := make(map[string]downloader.TorrentInfo, len(dcTorrents))
	for _, t := range dcTorrents {
		live[strings.ToLower(t.Hash)] = t
	}

	removed := make(map[string]bool)
	for _, tor := range selectRemovals(stored, live, s) {
		info := live[tor.Hash]
		done, ok := c.sessions.BeginRemoval(tor.Hash)
		if !ok {
			c.logger.Debug("cleanup: skipping torrent past removal trigger, currently being watched",
				"id", tor.ID, "hash", tor.Hash)
			continue
		}
		c.logger.Info("cleanup: removing torrent past removal trigger",
			"id", tor.ID, "hash", tor.Hash, "seedingTime", info.SeedingTime,
			"indexer", tor.Indexer, "seedTime", s.EffectiveSeedTime(tor.Indexer),
			"ratio", info.Ratio, "uploaded", info.Uploaded, "policy", s.DeletePolicy)
		if err := c.svc.Remove(ctx, tor, deletionEvent(info, s, tor.Indexer)); err != nil {
			c.logger.Warn("cleanup: remove torrent", "id", tor.ID, "hash", tor.Hash, "error", err)
		} else {
			removed[tor.Hash] = true
		}
		done()
	}
	if s.pressureEnabled() {
		c.relievePressure(ctx, stored, live, s, removed)
	}
	return nil
}

// relievePressure removes completed torrents that have seeded at least
// DiskPressureSeedTime, in DeletePolicy order, while the download disk is
// at or above DiskPressurePercent. Freed space is estimated from each
// torrent's on-disk bytes (the client deletes files asynchronously), so a
// pass stops once enough is queued; the next sweep measures again.
func (c *Cleanup) relievePressure(ctx context.Context, stored []store.Torrent, live map[string]downloader.TorrentInfo, s Settings, removed map[string]bool) {
	used, total, err := c.diskUsage(s.DiskPath)
	if err != nil || total <= 0 {
		c.logger.Warn("cleanup: disk usage check failed; skipping disk-pressure pass", "path", s.DiskPath, "error", err)
		return
	}
	limit := total * int64(s.DiskPressurePercent) / 100
	if used < limit {
		return
	}
	candidates := selectPressureRemovals(stored, live, s, removed)
	c.logger.Info("cleanup: download disk over pressure threshold",
		"used", used, "total", total, "percent", s.DiskPressurePercent, "candidates", len(candidates))
	for _, tor := range candidates {
		if used < limit {
			break
		}
		info := live[tor.Hash]
		done, ok := c.sessions.BeginRemoval(tor.Hash)
		if !ok {
			c.logger.Debug("cleanup: skipping disk-pressure candidate, currently being watched", "id", tor.ID, "hash", tor.Hash)
			continue
		}
		c.logger.Info("cleanup: removing torrent to free disk space",
			"id", tor.ID, "hash", tor.Hash, "seedingTime", info.SeedingTime,
			"minSeedTime", s.DiskPressureSeedTime, "indexer", tor.Indexer,
			"ratio", info.Ratio, "bytes", info.Completed, "policy", s.DeletePolicy)
		if err := c.svc.Remove(ctx, tor, pressureEvent(info, s)); err != nil {
			c.logger.Warn("cleanup: remove torrent", "id", tor.ID, "hash", tor.Hash, "error", err)
		} else {
			used -= info.Completed
		}
		done()
	}
	if used >= limit {
		c.logger.Warn("cleanup: download disk still over pressure threshold; nothing else has seeded long enough",
			"used", used, "limit", limit, "minSeedTime", s.DiskPressureSeedTime)
	}
}

// selectPressureRemovals returns the torrents disk pressure may remove, in
// DeletePolicy order: finished downloading, seeded at least
// DiskPressureSeedTime, and not already removed by this sweep.
func selectPressureRemovals(stored []store.Torrent, live map[string]downloader.TorrentInfo, s Settings, removed map[string]bool) []store.Torrent {
	eligible := make([]store.Torrent, 0, len(stored))
	for _, tor := range stored {
		if removed[tor.Hash] {
			continue
		}
		info, ok := live[tor.Hash]
		if !ok || info.Progress < 1 || info.SeedingTime < s.DiskPressureSeedTime {
			continue
		}
		eligible = append(eligible, tor)
	}
	orderRemovals(eligible, live, s.DeletePolicy)
	return eligible
}

// pressureEvent builds the audit record for a disk-pressure removal: the
// seeding time against the disk-pressure minimum that allowed it.
func pressureEvent(info downloader.TorrentInfo, s Settings) store.DeletionEvent {
	return store.DeletionEvent{
		Reason:      store.DeleteReasonDiskPressure,
		SeedingTime: info.SeedingTime,
		SeedLimit:   s.DiskPressureSeedTime,
		Ratio:       info.Ratio,
		Progress:    info.Progress,
	}
}

// deletionEvent builds the audit record for a cleanup removal. Both
// triggers can fire in the same pass; seed time takes precedence for the
// reason, but both evidence pairs are recorded either way so the decision
// stays reconstructible from the history alone.
func deletionEvent(info downloader.TorrentInfo, s Settings, indexer string) store.DeletionEvent {
	seedTime := s.EffectiveSeedTime(indexer)
	reason := store.DeleteReasonRatio
	if seedTime > 0 && info.SeedingTime >= seedTime {
		reason = store.DeleteReasonSeedTime
	}
	return store.DeletionEvent{
		Reason:      reason,
		SeedingTime: info.SeedingTime,
		SeedLimit:   seedTime,
		Ratio:       info.Ratio,
		RatioLimit:  s.TargetRatio,
		Progress:    info.Progress,
	}
}

// selectRemovals is the pure decision core: given the stored torrents, the
// live download-client state keyed by lowercase infohash, and settings, it
// returns the removal-eligible torrents in the configured delete order. A
// torrent is eligible when it has finished downloading (Progress >= 1) and
// meets at least one enabled trigger: seeded past its indexer's effective
// seed time, or reached TargetRatio. Torrents with no live info are skipped
// (nothing to evaluate).
func selectRemovals(stored []store.Torrent, live map[string]downloader.TorrentInfo, s Settings) []store.Torrent {
	eligible := make([]store.Torrent, 0, len(stored))
	for _, tor := range stored {
		info, ok := live[tor.Hash]
		if !ok || info.Progress < 1 {
			continue
		}
		seedTime := s.EffectiveSeedTime(tor.Indexer)
		byTime := seedTime > 0 && info.SeedingTime >= seedTime
		byRatio := s.TargetRatio > 0 && info.Ratio >= s.TargetRatio
		if byTime || byRatio {
			eligible = append(eligible, tor)
		}
	}
	orderRemovals(eligible, live, s.DeletePolicy)
	return eligible
}

// orderRemovals sorts eligible in place by the delete policy. lowest_upload
// removes the least-uploaded torrents first; anything else (including the
// default/empty policy) is oldest-first by add time. Ties fall back to
// add time then hash for a stable, deterministic order.
func orderRemovals(eligible []store.Torrent, live map[string]downloader.TorrentInfo, policy string) {
	switch policy {
	case config.DeletePolicyLowestUpload:
		sort.SliceStable(eligible, func(i, j int) bool {
			ui, uj := live[eligible[i].Hash].Uploaded, live[eligible[j].Hash].Uploaded
			if ui != uj {
				return ui < uj
			}
			if eligible[i].AddedAt != eligible[j].AddedAt {
				return eligible[i].AddedAt < eligible[j].AddedAt
			}
			return eligible[i].Hash < eligible[j].Hash
		})
	default:
		sort.SliceStable(eligible, func(i, j int) bool {
			if eligible[i].AddedAt != eligible[j].AddedAt {
				return eligible[i].AddedAt < eligible[j].AddedAt
			}
			return eligible[i].Hash < eligible[j].Hash
		})
	}
}
