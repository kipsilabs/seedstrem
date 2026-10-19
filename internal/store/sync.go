package store

import "context"

// TorrentSnapshot contains provider-owned metadata and streaming links for
// one torrent. Nil Links means metadata is pending: retain existing links.
type TorrentSnapshot struct {
	Torrent Torrent
	Links   []Link
	New     bool
}

// ApplyTorrentSync atomically applies a fully fetched provider snapshot.
// Only provider-owned fields of existing torrents are replaced; content
// identity, origin, indexer, and IDs remain seedstrem's responsibility.
func (s *Store) ApplyTorrentSync(ctx context.Context, snapshots []TorrentSnapshot, missingIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, snapshot := range snapshots {
		t := snapshot.Torrent
		if snapshot.New {
			_, err = tx.ExecContext(ctx, `INSERT INTO torrents (`+torrentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, t.Hash, t.Name, t.Phase, t.AddedAt, t.Magnet, t.Error, t.ContentSource, t.ContentRef, t.Season, t.Episode, t.Indexer, t.Origin, t.UserAgent)
		} else {
			res, updateErr := tx.ExecContext(ctx, `UPDATE torrents SET name = ?, phase = ?, added_at = ?, error = ? WHERE id = ?`, t.Name, t.Phase, t.AddedAt, t.Error, t.ID)
			err = updateErr
			if err == nil {
				n, countErr := res.RowsAffected()
				if countErr != nil {
					return countErr
				}
				if n == 0 {
					return ErrNotFound
				}
			}
		}
		if err != nil {
			return err
		}
		if snapshot.Links == nil {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM links WHERE torrent_id = ?`, t.ID); err != nil {
			return err
		}
		for _, l := range snapshot.Links {
			if _, err = tx.ExecContext(ctx, `INSERT INTO links (token, torrent_id, file_index, path, bytes) VALUES (?, ?, ?, ?, ?)`, l.Token, t.ID, l.FileIndex, l.Path, l.Bytes); err != nil {
				return err
			}
		}
	}
	for _, id := range missingIDs {
		if _, err = tx.ExecContext(ctx, `DELETE FROM torrents WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
