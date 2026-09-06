// Package syncer reconciles the local store with qBittorrent in the
// background: torrents deleted out-of-band in qBittorrent get a sticky
// error, and display names are backfilled once metadata resolves.
package syncer

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// Syncer periodically reconciles store state against qBittorrent.
type Syncer struct {
	store    *store.Store
	dc       downloader.Client
	logger   *slog.Logger
	interval time.Duration
}

// New creates a Syncer.
func New(st *store.Store, dc downloader.Client, logger *slog.Logger, interval time.Duration) *Syncer {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Syncer{store: st, dc: dc, logger: logger, interval: interval}
}

// Run loops until ctx is cancelled.
func (s *Syncer) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("sync failed", "error", err)
			}
		}
	}
}

// Reconcile performs one reconciliation pass. qBittorrent has no
// category/label concept comparable to qBittorrent's, so rather than
// listing "torrents in our category" this queries qBittorrent directly for
// the hashes we already know about from the store.
func (s *Syncer) Reconcile(ctx context.Context) error {
	stored, err := s.store.AllTorrents(ctx)
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
	dcTorrents, err := s.dc.Torrents(ctx, hashes)
	if err != nil {
		return err
	}
	live := make(map[string]downloader.TorrentInfo, len(dcTorrents))
	for _, t := range dcTorrents {
		live[strings.ToLower(t.Hash)] = t
	}

	for _, tor := range stored {
		info, ok := live[tor.Hash]
		if !ok {
			if tor.Error == "" {
				s.logger.Info("torrent vanished from qbittorrent", "id", tor.ID, "hash", tor.Hash)
				if err := s.store.SetTorrentError(ctx, tor.ID, "removed from qBittorrent"); err != nil {
					s.logger.Warn("mark vanished torrent", "id", tor.ID, "error", err)
				}
			}
			continue
		}
		if tor.Error != "" {
			// Torrent is back (e.g. re-added manually): clear the error.
			if err := s.store.SetTorrentError(ctx, tor.ID, ""); err != nil {
				s.logger.Warn("clear torrent error", "id", tor.ID, "error", err)
			}
		}
		if tor.Name == "" && info.Name != "" {
			if err := s.store.SetTorrentName(ctx, tor.ID, info.Name); err != nil {
				s.logger.Warn("backfill torrent name", "id", tor.ID, "error", err)
			}
		}
	}
	return nil
}
