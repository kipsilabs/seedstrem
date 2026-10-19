package syncer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

// Force replaces local provider-owned metadata with a fresh snapshot of
// tracked torrents plus the configured label. It only reads the provider.
// All remote reads must succeed before any local changes are committed.
func (s *Syncer) Force(ctx context.Context, label string) error {
	stored, err := s.store.AllTorrents(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]store.Torrent, len(stored))
	hashes := make([]string, 0, len(stored))
	for _, t := range stored {
		hash := strings.ToLower(t.Hash)
		known[hash] = t
		hashes = append(hashes, hash)
	}
	live := make(map[string]downloader.TorrentInfo)
	if len(hashes) > 0 {
		tracked, err := s.dc.Torrents(ctx, hashes)
		if err != nil {
			return fmt.Errorf("list tracked torrents: %w", err)
		}
		for _, info := range tracked {
			hash := strings.ToLower(info.Hash)
			if _, ok := known[hash]; ok {
				live[hash] = info
			}
		}
	}
	if label != "" {
		labelled, err := s.dc.TorrentsByLabel(ctx, label)
		if err != nil {
			return fmt.Errorf("list labelled torrents: %w", err)
		}
		for _, info := range labelled {
			live[strings.ToLower(info.Hash)] = info
		}
	}
	var snapshots []store.TorrentSnapshot
	var missing []string
	for hash, t := range known {
		if _, ok := live[hash]; !ok {
			missing = append(missing, t.ID)
		}
	}
	for hash, info := range live {
		files, err := s.dc.Files(ctx, hash)
		if err != nil {
			return fmt.Errorf("files for %s: %w", hash, err)
		}
		t, exists := known[hash]
		if !exists {
			// Wait for metadata, just like automatic adoption.
			if len(files) == 0 {
				continue
			}
			id, err := torrents.NewID()
			if err != nil {
				return err
			}
			t = store.Torrent{ID: id, Hash: hash, Origin: store.OriginAdopted, AddedAt: time.Now().Unix()}
		}
		t.Name = info.Name
		t.Error = ""
		if !info.AddedAt.IsZero() {
			t.AddedAt = info.AddedAt.Unix()
		}
		snapshot := store.TorrentSnapshot{Torrent: t, New: !exists}
		if len(files) > 0 {
			snapshot.Torrent.Phase = store.PhaseSelected
			previous, err := s.store.LinksByTorrent(ctx, t.ID)
			if err != nil {
				return err
			}
			tokens := make(map[int]string, len(previous))
			for _, l := range previous {
				tokens[l.FileIndex] = l.Token
			}
			snapshot.Links = make([]store.Link, 0, len(files))
			for _, f := range files {
				if !torrents.IsVideo(f.Name) {
					continue
				}
				token := tokens[f.Index]
				if token == "" {
					token, err = torrents.NewLinkToken()
					if err != nil {
						return err
					}
				}
				snapshot.Links = append(snapshot.Links, store.Link{Token: token, TorrentID: t.ID, FileIndex: f.Index, Path: f.Name, Bytes: f.Size})
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	return s.store.ApplyTorrentSync(ctx, snapshots, missing)
}
