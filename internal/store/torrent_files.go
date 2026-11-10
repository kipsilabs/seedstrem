package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SaveTorrentFile stores (or replaces) the raw .torrent for an infohash.
func (s *Store) SaveTorrentFile(ctx context.Context, hash string, data []byte) error {
	if len(data) == 0 {
		return errors.New("save torrent file: empty data")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO torrent_files (hash, data, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(hash) DO UPDATE SET data = excluded.data`,
		hash, data, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save torrent file %s: %w", hash, err)
	}
	return nil
}

// TorrentFile returns the saved .torrent for an infohash, or ErrNotFound.
func (s *Store) TorrentFile(ctx context.Context, hash string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM torrent_files WHERE hash = ?`, hash).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("torrent file %s: %w", hash, err)
	}
	return data, nil
}
