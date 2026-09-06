// Package adopt discovers torrents added directly in the download client
// and brings them under seedstrem's management. A torrent carrying
// seedstrem's configured label (a Deluge label, a qBittorrent category)
// is adopted: a store row and one streaming link per video file. Removing
// the label un-adopts it again.
//
// Adoption is read-only towards the download client. It never changes
// file priorities, streaming flags, or labels — the user's own selection
// stands, and the torrent is left exactly as they set it up.
package adopt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

// Settings is the live configuration slice the adopt loop needs.
type Settings struct {
	// Enabled turns label adoption on. Off by default: an adopted
	// torrent is swept by cleanup like any other, so enabling it must be
	// a deliberate choice rather than something an upgrade does.
	Enabled bool
	// Label is the download client's label/category marking a torrent as
	// seedstrem's. Empty disables adoption regardless of Enabled.
	Label string
}

// Adopter periodically reconciles the store against the set of torrents
// carrying seedstrem's label in the download client.
type Adopter struct {
	store    *store.Store
	dc       downloader.Client
	settings func() Settings
	logger   *slog.Logger
	interval time.Duration
}

// New creates an Adopter.
func New(st *store.Store, dc downloader.Client, settings func() Settings, logger *slog.Logger, interval time.Duration) *Adopter {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	return &Adopter{store: st, dc: dc, settings: settings, logger: logger, interval: interval}
}

// Run loops until ctx is cancelled.
func (a *Adopter) Run(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.Scan(ctx); err != nil && ctx.Err() == nil {
				a.logger.Warn("adopt scan failed", "error", err)
			}
		}
	}
}

// Scan performs one adoption pass: labelled torrents seedstrem does not
// know are adopted, and previously-adopted torrents that no longer carry
// the label are dropped.
//
// A listing failure aborts the pass before anything is deleted. "The
// client did not answer" and "the label is gone" are indistinguishable
// from the response alone, and only one of them may remove rows.
func (a *Adopter) Scan(ctx context.Context) error {
	s := a.settings()
	if !s.Enabled || s.Label == "" {
		return nil
	}

	live, err := a.dc.TorrentsByLabel(ctx, s.Label)
	if err != nil {
		if errors.Is(err, downloader.ErrNotSupported) {
			// No label support (Deluge without the Label plugin). Not a
			// failure, but nothing can be concluded either — least of
			// all that every adopted torrent lost its label.
			a.logger.Debug("adopt: download client cannot filter by label")
			return nil
		}
		return fmt.Errorf("list torrents by label: %w", err)
	}

	labelled := make(map[string]downloader.TorrentInfo, len(live))
	hashes := make([]string, 0, len(live))
	for _, info := range live {
		h := strings.ToLower(info.Hash)
		labelled[h] = info
		hashes = append(hashes, h)
	}

	known, err := a.store.TorrentsByHashes(ctx, hashes)
	if err != nil {
		return fmt.Errorf("look up known hashes: %w", err)
	}
	for hash, info := range labelled {
		if _, ok := known[hash]; ok {
			continue
		}
		if err := a.adopt(ctx, hash, info); err != nil {
			// One bad torrent must not abort the pass; the next tick
			// retries it.
			a.logger.Warn("adopt: could not adopt torrent", "hash", hash, "error", err)
		}
	}

	return a.unadopt(ctx, labelled)
}

// adopt inserts the store row and one link per video file.
func (a *Adopter) adopt(ctx context.Context, hash string, info downloader.TorrentInfo) error {
	files, err := a.dc.Files(ctx, hash)
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}
	if len(files) == 0 {
		// Metadata has not resolved yet. Adopting now would mint a
		// torrent with no links and never revisit it, so wait.
		a.logger.Debug("adopt: metadata not ready, deferring", "hash", hash)
		return nil
	}

	id, err := torrents.NewID()
	if err != nil {
		return fmt.Errorf("generate id: %w", err)
	}
	addedAt := info.AddedAt.Unix()
	if info.AddedAt.IsZero() {
		addedAt = time.Now().Unix()
	}
	tor := store.Torrent{
		ID:      id,
		Hash:    hash,
		Name:    info.Name,
		Phase:   store.PhaseSelected,
		AddedAt: addedAt,
		Origin:  store.OriginAdopted,
	}
	if err := a.store.InsertTorrent(ctx, tor); err != nil {
		return fmt.Errorf("persist torrent: %w", err)
	}

	var links []store.Link
	for _, f := range files {
		if !torrents.IsVideo(f.Name) {
			continue
		}
		token, err := torrents.NewLinkToken()
		if err != nil {
			return fmt.Errorf("generate link token: %w", err)
		}
		links = append(links, store.Link{
			Token:     token,
			TorrentID: id,
			FileIndex: f.Index,
			Path:      f.Name,
			Bytes:     f.Size,
		})
	}
	if len(links) > 0 {
		if err := a.store.InsertLinks(ctx, links); err != nil {
			return fmt.Errorf("persist links: %w", err)
		}
	}

	a.logger.Info("adopt: adopted torrent from download client",
		"hash", hash, "id", id, "name", info.Name, "links", len(links))
	return nil
}

// unadopt drops adopted rows whose hash is absent from a successful
// listing. Native rows are not considered: seedstrem added them, and a
// missing label — including on a client with no label support at all —
// says nothing about whether they should exist.
func (a *Adopter) unadopt(ctx context.Context, labelled map[string]downloader.TorrentInfo) error {
	adopted, err := a.store.AdoptedTorrents(ctx)
	if err != nil {
		return fmt.Errorf("list adopted torrents: %w", err)
	}
	for _, tor := range adopted {
		if _, ok := labelled[strings.ToLower(tor.Hash)]; ok {
			continue
		}
		if err := a.store.DeleteTorrent(ctx, tor.ID); err != nil {
			a.logger.Warn("adopt: could not un-adopt torrent", "id", tor.ID, "error", err)
			continue
		}
		// This path bypasses torrents.Service, so it records its own
		// removal. FilesDeleted stays false: only the store row goes
		// away, and the torrent keeps seeding in the client.
		if err := a.store.RecordDeletion(ctx, store.DeletionEvent{
			TorrentID: tor.ID,
			Hash:      tor.Hash,
			Name:      tor.Name,
			Indexer:   tor.Indexer,
			Origin:    tor.Origin,
			Reason:    store.DeleteReasonUnadopted,
		}); err != nil {
			a.logger.Warn("adopt: could not record un-adoption", "id", tor.ID, "error", err)
		}
		a.logger.Info("adopt: un-adopted torrent, label no longer set",
			"id", tor.ID, "hash", tor.Hash)
	}
	return nil
}
