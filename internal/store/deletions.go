package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Deletion reasons: the code path that removed a torrent. Every removal
// records exactly one, so a vanished torrent can be traced back to the
// component responsible.
const (
	// DeleteReasonSeedTime: the cleanup sweep found the torrent seeded
	// past its effective seed time.
	DeleteReasonSeedTime = "seed_time"
	// DeleteReasonRatio: the cleanup sweep found the torrent at or above
	// the target ratio (and not past its seed time).
	DeleteReasonRatio = "ratio"
	// DeleteReasonManual: removed from the admin UI.
	DeleteReasonManual = "manual"
	// DeleteReasonAbandoned: a stream was abandoned below the progress
	// threshold with nobody watching.
	DeleteReasonAbandoned = "abandoned"
	// DeleteReasonUnadopted: an adopted torrent lost its label. Only the
	// store row goes away — the torrent keeps seeding in the client.
	DeleteReasonUnadopted = "unadopted"
)

// DefaultDeletionRetention is how long deletion records (and their saved
// .torrent files) are kept and shown unless SetDeletionRetention says
// otherwise.
const DefaultDeletionRetention = 30 * 24 * time.Hour

// DeletionEvent is one recorded removal. The evidence fields carry
// whatever the removing component knew at decision time; a field the
// caller has no opinion on stays zero.
type DeletionEvent struct {
	TorrentID string
	Hash      string
	Name      string
	Indexer   string
	Origin    string
	// DeletedAt is unix seconds; zero means "now".
	DeletedAt int64
	Reason    string
	// SeedingTime and SeedLimit are the observed seeding time and the
	// effective seed time that governed this torrent.
	SeedingTime time.Duration
	SeedLimit   time.Duration
	// Ratio and RatioLimit are the observed ratio and the target ratio.
	Ratio      float64
	RatioLimit float64
	// Progress is the download progress (0-1) at removal, and
	// ProgressLimit the threshold it was measured against (the abandoned
	// path's minimum progress).
	Progress      float64
	ProgressLimit float64
	// FilesDeleted reports whether the data was removed from disk, as
	// opposed to only the store row going away.
	FilesDeleted bool
}

// Deletion is a stored DeletionEvent with its generated id.
type Deletion struct {
	ID string
	DeletionEvent
	// HasTorrentFile reports whether a saved .torrent exists for the
	// hash, i.e. whether the torrent can be re-added.
	HasTorrentFile bool
}

const deletionCols = `id, torrent_id, hash, name, indexer, origin, deleted_at, reason, ` +
	`seeding_time, seed_limit, ratio, ratio_limit, progress, progress_limit, files_deleted`

const deletionIDAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// newDeletionID generates a record id. It cannot reuse torrents.NewID:
// internal/torrents already imports this package, so depending on it here
// would be an import cycle.
func newDeletionID() (string, error) {
	const length = 16
	max := big.NewInt(int64(len(deletionIDAlphabet)))
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("deletion id: %w", err)
		}
		b[i] = deletionIDAlphabet[n.Int64()]
	}
	return string(b), nil
}

// RecordDeletion writes one removal to the audit log and prunes records
// past DeletionRetention, keeping the table bounded without a separate
// sweeper.
func (s *Store) RecordDeletion(ctx context.Context, ev DeletionEvent) error {
	id, err := newDeletionID()
	if err != nil {
		return err
	}
	now := time.Now()
	if ev.DeletedAt == 0 {
		ev.DeletedAt = now.Unix()
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO torrent_deletions (`+deletionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, ev.TorrentID, ev.Hash, ev.Name, ev.Indexer, ev.Origin, ev.DeletedAt, ev.Reason,
		int64(ev.SeedingTime/time.Second), int64(ev.SeedLimit/time.Second),
		ev.Ratio, ev.RatioLimit, ev.Progress, ev.ProgressLimit, ev.FilesDeleted)
	if err != nil {
		return fmt.Errorf("record deletion %s: %w", ev.Hash, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM torrent_deletions WHERE deleted_at < ?`,
		now.Add(-s.DeletionRetention()).Unix()); err != nil {
		return fmt.Errorf("prune deletions: %w", err)
	}
	// Drop saved .torrent files no live torrent or history entry needs.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM torrent_files
		 WHERE hash NOT IN (SELECT hash FROM torrents)
		   AND hash NOT IN (SELECT hash FROM torrent_deletions)`); err != nil {
		return fmt.Errorf("prune torrent files: %w", err)
	}
	return nil
}

// Deletions returns the removals recorded within the retention window,
// newest first. The cutoff is applied on read so an idle instance — one
// where no write has triggered a prune — still shows only the window.
func (s *Store) Deletions(ctx context.Context) ([]Deletion, error) {
	cutoff := time.Now().Add(-s.DeletionRetention()).Unix()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+prefixed("d.", deletionCols)+`, EXISTS(SELECT 1 FROM torrent_files f WHERE f.hash = d.hash)
		 FROM torrent_deletions d WHERE d.deleted_at >= ? ORDER BY d.deleted_at DESC`,
		cutoff)
	if err != nil {
		return nil, fmt.Errorf("query deletions: %w", err)
	}
	defer rows.Close()

	var out []Deletion
	for rows.Next() {
		var d Deletion
		var seedingTime, seedLimit int64
		if err := rows.Scan(&d.ID, &d.TorrentID, &d.Hash, &d.Name, &d.Indexer, &d.Origin,
			&d.DeletedAt, &d.Reason, &seedingTime, &seedLimit,
			&d.Ratio, &d.RatioLimit, &d.Progress, &d.ProgressLimit, &d.FilesDeleted, &d.HasTorrentFile); err != nil {
			return nil, fmt.Errorf("scan deletion: %w", err)
		}
		d.SeedingTime = time.Duration(seedingTime) * time.Second
		d.SeedLimit = time.Duration(seedLimit) * time.Second
		out = append(out, d)
	}
	return out, rows.Err()
}

// prefixed qualifies each column in a comma-separated list with prefix.
func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// DeletionRetention returns the current history retention window.
func (s *Store) DeletionRetention() time.Duration {
	if d := s.retention.Load(); d > 0 {
		return time.Duration(d)
	}
	return DefaultDeletionRetention
}

// SetDeletionRetention changes the history retention window; d <= 0
// restores the default. Safe for concurrent use (config hot-reload).
func (s *Store) SetDeletionRetention(d time.Duration) {
	s.retention.Store(int64(d))
}

// DeletionByID returns one history entry, or ErrNotFound.
func (s *Store) DeletionByID(ctx context.Context, id string) (Deletion, error) {
	all, err := s.Deletions(ctx)
	if err != nil {
		return Deletion{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return Deletion{}, ErrNotFound
}
