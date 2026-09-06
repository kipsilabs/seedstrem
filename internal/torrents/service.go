package torrents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/metainfo"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// metaPollInterval is how often WaitForMetadata re-checks qBittorrent for a
// resolved file list. The first check happens immediately.
const metaPollInterval = 1 * time.Second

// Settings is the live configuration slice the service needs, fetched per
// call so config hot-reload takes effect without restart.
type Settings struct {
	MetadataTimeout     time.Duration
	DeleteFilesOnRemove bool
	// SeedFull downloads the whole torrent (the played file first, the
	// rest at normal priority afterwards) so a complete copy is available
	// to seed. When false, only the played file is downloaded.
	SeedFull bool
}

// Service owns the add → wait → select → link mechanics against qBittorrent
// and the local store.
type Service struct {
	store    *store.Store
	dc       downloader.Client
	settings func() Settings
	logger   *slog.Logger

	// prioAsserted / kickAsserted track (unix seconds) when streaming
	// priorities were last re-asserted / force-kicked per hash, so repeat
	// play resolves and heartbeat stall detection don't hammer
	// qBittorrent with toggle calls.
	prioMu       sync.Mutex
	prioAsserted map[string]int64
	kickAsserted map[string]int64

	// injectable for tests
	now   func() int64
	sleep func(ctx context.Context, d time.Duration) error
}

// New builds a Service.
func New(st *store.Store, dc downloader.Client, settings func() Settings, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:        st,
		dc:           dc,
		settings:     settings,
		logger:       logger,
		prioAsserted: map[string]int64{},
		kickAsserted: map[string]int64{},
		now:          func() int64 { return time.Now().Unix() },
		sleep:        sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// EnsureAdded adds a magnet to qBittorrent (running, sequential,
// first/last-piece priority) and persists the id↔hash mapping. It is
// idempotent on the infohash: a re-add returns the existing torrent.
//
// The torrent is added running (not stopped): qBittorrent does not fetch
// a magnet's metadata while stopped, so WaitForMetadata would never see a
// file list. The metadata (metaDL) phase downloads no file content, and
// SelectAndLink deselects the unwanted files the instant metadata
// resolves, so nothing unwanted is fetched.
//
// When torrentFile is non-empty (a .torrent seedstrem fetched from a
// magnet-less indexer), it is added directly instead of the magnet: the
// metadata is already embedded, so qBittorrent skips the metaDL fetch
// entirely — which is unreliable for private-tracker peers and otherwise
// leaves the torrent stuck in metaDL.
func (s *Service) EnsureAdded(ctx context.Context, magnet string, torrentFile []byte, sel Selector) (store.Torrent, error) {
	hash, name, err := metainfo.FromMagnet(magnet)
	if err != nil {
		return store.Torrent{}, fmt.Errorf("parse magnet: %w", err)
	}

	if existing, err := s.store.TorrentByHash(ctx, hash); err == nil {
		s.logger.Debug("torrents: reusing existing torrent", "hash", hash, "id", existing.ID)
		// Backfill the content identity if this torrent was first added
		// before we knew it (older row, or a re-add carrying it now).
		if existing.ContentRef == "" && sel.ContentRef != "" {
			if err := s.store.SetTorrentContent(ctx, existing.ID, sel.Source, sel.ContentRef, sel.Season, sel.Episode); err != nil {
				s.logger.Warn("torrents: backfill content identity", "id", existing.ID, "error", err)
			} else {
				existing.ContentSource, existing.ContentRef = sel.Source, sel.ContentRef
				existing.Season, existing.Episode = sel.Season, sel.Episode
			}
		}
		// Same for the indexer — but only when none is recorded yet. The
		// first attribution wins, so re-playing the same release found
		// through a different indexer cannot silently change how long
		// cleanup keeps it seeding.
		if existing.Indexer == "" && sel.Indexer != "" {
			if err := s.store.SetTorrentIndexer(ctx, existing.ID, sel.Indexer); err != nil {
				s.logger.Warn("torrents: backfill indexer", "id", existing.ID, "error", err)
			} else {
				existing.Indexer = sel.Indexer
			}
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Torrent{}, fmt.Errorf("lookup torrent by hash: %w", err)
	}

	opts := downloader.AddOptions{
		Stopped:            false,
		SequentialDownload: true,
		FirstLastPiecePrio: true,
	}
	if len(torrentFile) > 0 {
		s.logger.Debug("torrents: adding .torrent file to download client", "hash", hash, "name", name)
		if err := s.dc.AddTorrentFile(ctx, torrentFile, opts); err != nil {
			return store.Torrent{}, fmt.Errorf("add torrent file to download client: %w", err)
		}
	} else {
		s.logger.Debug("torrents: adding magnet to download client", "hash", hash, "name", name)
		if err := s.dc.AddMagnet(ctx, magnet, opts); err != nil {
			return store.Torrent{}, fmt.Errorf("add magnet to download client: %w", err)
		}
	}

	id, err := NewID()
	if err != nil {
		return store.Torrent{}, fmt.Errorf("generate id: %w", err)
	}
	tor := store.Torrent{
		ID:            id,
		Hash:          hash,
		Name:          name,
		Phase:         store.PhaseAdded,
		AddedAt:       s.now(),
		Magnet:        magnet,
		ContentSource: sel.Source,
		ContentRef:    sel.ContentRef,
		Season:        sel.Season,
		Episode:       sel.Episode,
		Indexer:       sel.Indexer,
	}
	if err := s.store.InsertTorrent(ctx, tor); err != nil {
		// A concurrent add of the same hash may have won the race (the
		// hash column is UNIQUE); fall back to the existing row.
		if existing, lookupErr := s.store.TorrentByHash(ctx, hash); lookupErr == nil {
			return existing, nil
		}
		return store.Torrent{}, fmt.Errorf("persist torrent: %w", err)
	}
	return tor, nil
}

// WaitForMetadata polls qBittorrent until it has resolved the torrent's file
// list (non-empty) or timeout elapses.
func (s *Service) WaitForMetadata(ctx context.Context, hash string, timeout time.Duration) ([]downloader.FileInfo, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		files, err := s.dc.Files(ctx, hash)
		if err != nil && !errors.Is(err, downloader.ErrTorrentNotFound) {
			return nil, fmt.Errorf("download client files: %w", err)
		}
		if len(files) > 0 {
			s.logger.Debug("torrents: metadata ready", "hash", hash, "files", len(files))
			return files, nil
		}
		if time.Now().After(deadline) {
			s.logger.Debug("torrents: metadata wait timed out", "hash", hash, "timeout", timeout)
			return nil, ErrMetadataTimeout
		}
		if err := s.sleep(ctx, metaPollInterval); err != nil {
			return nil, err
		}
	}
}

// ErrMetadataTimeout is returned when qBittorrent did not resolve the
// torrent's file list within the allotted time.
var ErrMetadataTimeout = errors.New("timed out waiting for torrent metadata")

// SelectAndLink marks fileIndex as wanted (others unwanted), starts the
// torrent, and mints a streaming link for that file. It is idempotent on
// (torrentID, fileIndex): a repeat call returns the existing link.
func (s *Service) SelectAndLink(ctx context.Context, tor store.Torrent, fileIndex int, files []downloader.FileInfo) (store.Link, error) {
	if existing, err := s.linkFor(ctx, tor.ID, fileIndex); err == nil {
		// Repeat play of an already-linked file: the first/last-piece
		// boost may have been dropped since the first select (qBittorrent
		// recomputes piece priorities on its own schedule), so re-assert
		// it while the download is still in flight.
		s.ensureStreamingPrioThrottled(ctx, tor.Hash)
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Link{}, err
	}

	var selectedIdx, unselectedIdx []int
	for _, f := range files {
		if f.Index == fileIndex {
			selectedIdx = append(selectedIdx, f.Index)
		} else {
			unselectedIdx = append(unselectedIdx, f.Index)
		}
	}
	if len(selectedIdx) == 0 {
		return store.Link{}, fmt.Errorf("file index %d not in torrent", fileIndex)
	}

	// qBittorrent file priorities: 0 = do not download, 1 = normal,
	// 7 = maximum. In full-seed mode the other files stay at normal so the
	// whole torrent downloads (for ratio) while the played file is boosted
	// to maximum so it downloads first for streaming. Otherwise the other
	// files are skipped entirely and only the played file is fetched.
	otherPrio, selectedPrio := 0, 1
	if s.settings().SeedFull {
		otherPrio, selectedPrio = 1, 7
	}
	if err := s.dc.SetFilePriority(ctx, tor.Hash, unselectedIdx, otherPrio); err != nil {
		return store.Link{}, fmt.Errorf("set other files priority: %w", err)
	}
	if err := s.dc.SetFilePriority(ctx, tor.Hash, selectedIdx, selectedPrio); err != nil {
		return store.Link{}, fmt.Errorf("select file: %w", err)
	}
	if err := s.dc.Start(ctx, tor.Hash); err != nil {
		return store.Link{}, fmt.Errorf("start torrent: %w", err)
	}
	// After Start, not before: qBittorrent applies the SetFilePriority
	// rewrite above asynchronously, and its recompute can drop the
	// first/last-piece boost. Re-asserting here sits behind that
	// recompute instead of racing it.
	s.ensureStreamingPrioThrottled(ctx, tor.Hash)

	token, err := NewLinkToken()
	if err != nil {
		return store.Link{}, fmt.Errorf("generate link token: %w", err)
	}
	var picked downloader.FileInfo
	for _, f := range files {
		if f.Index == fileIndex {
			picked = f
			break
		}
	}
	link := store.Link{
		Token:     token,
		TorrentID: tor.ID,
		FileIndex: fileIndex,
		Path:      picked.Name,
		Bytes:     picked.Size,
	}
	if err := s.store.InsertLinks(ctx, []store.Link{link}); err != nil {
		// Lost a race on the UNIQUE (torrent_id, file_index): return the
		// link the winner inserted.
		if existing, lookupErr := s.linkFor(ctx, tor.ID, fileIndex); lookupErr == nil {
			return existing, nil
		}
		return store.Link{}, fmt.Errorf("persist link: %w", err)
	}
	if tor.Phase != store.PhaseSelected {
		if err := s.store.SetTorrentPhase(ctx, tor.ID, store.PhaseSelected); err != nil {
			return store.Link{}, fmt.Errorf("set phase: %w", err)
		}
	}
	s.logger.Debug("torrents: file selected and linked",
		"hash", tor.Hash, "fileIndex", fileIndex, "path", picked.Name, "token", token)
	return link, nil
}

// streamingPrioReassertInterval rate-limits per-hash streaming-priority
// re-assertion (seconds): a player hammering the play endpoint re-asserts
// at most once per interval.
const streamingPrioReassertInterval = 30

// EnsureStreamingPrio makes sure the torrent downloads in an order fit
// for streaming: sequential download ON and the first/last-piece boost
// ON. The flags are set absolutely rather than assumed — the add-time
// flags may never have stuck, and qBittorrent silently drops the
// first/last boost when it recomputes piece priorities after a
// file-priority rewrite. The boost is set off and back on to force a
// recompute over the current file priorities. No-op once the download is
// complete.
func (s *Service) EnsureStreamingPrio(ctx context.Context, hash string) error {
	info, err := s.dc.Torrent(ctx, hash)
	if err != nil {
		return fmt.Errorf("read torrent flags: %w", err)
	}
	if info.Progress >= 1 {
		return nil
	}
	if err := s.dc.SetSequentialDownload(ctx, hash, true); err != nil {
		return fmt.Errorf("enable sequential download: %w", err)
	}
	if err := s.dc.SetFirstLastPiecePrio(ctx, hash, false); err != nil {
		return fmt.Errorf("re-assert first/last piece prio: %w", err)
	}
	if err := s.dc.SetFirstLastPiecePrio(ctx, hash, true); err != nil {
		return fmt.Errorf("re-assert first/last piece prio: %w", err)
	}
	return nil
}

// KickStreamingPrio force-resets libtorrent's piece picker: it sets
// sequential download AND the first/last-piece boost off and back on
// regardless of their current state (both end enabled). Used when the
// head piece a player is waiting on sits stalled for many seconds while
// the rest of the torrent downloads — a picker reset makes libtorrent
// re-request the stuck piece from healthy peers. No-op once complete.
func (s *Service) KickStreamingPrio(ctx context.Context, hash string) error {
	info, err := s.dc.Torrent(ctx, hash)
	if err != nil {
		return fmt.Errorf("read torrent flags: %w", err)
	}
	if info.Progress >= 1 {
		return nil
	}
	if err := s.dc.SetSequentialDownload(ctx, hash, false); err != nil {
		return fmt.Errorf("kick sequential download: %w", err)
	}
	if err := s.dc.SetSequentialDownload(ctx, hash, true); err != nil {
		return fmt.Errorf("kick sequential download: %w", err)
	}
	if err := s.dc.SetFirstLastPiecePrio(ctx, hash, false); err != nil {
		return fmt.Errorf("kick first/last piece prio: %w", err)
	}
	if err := s.dc.SetFirstLastPiecePrio(ctx, hash, true); err != nil {
		return fmt.Errorf("kick first/last piece prio: %w", err)
	}
	return nil
}

// KickStreamingPrioThrottled runs KickStreamingPrio at most once per
// streamingPrioReassertInterval per hash, reporting whether a kick was
// actually performed. Best-effort: failures are logged, not returned.
func (s *Service) KickStreamingPrioThrottled(ctx context.Context, hash string) bool {
	now := s.now()
	s.prioMu.Lock()
	if last, ok := s.kickAsserted[hash]; ok && now-last < streamingPrioReassertInterval {
		s.prioMu.Unlock()
		return false
	}
	s.kickAsserted[hash] = now
	s.prioMu.Unlock()

	if err := s.KickStreamingPrio(ctx, hash); err != nil {
		s.logger.Warn("torrents: kick streaming priorities", "hash", hash, "error", err)
		return false
	}
	return true
}

// ensureStreamingPrioThrottled runs EnsureStreamingPrio at most once per
// streamingPrioReassertInterval per hash. Best-effort: a failure only
// costs playback-startup latency, so it is logged rather than returned.
func (s *Service) ensureStreamingPrioThrottled(ctx context.Context, hash string) {
	now := s.now()
	s.prioMu.Lock()
	if last, ok := s.prioAsserted[hash]; ok && now-last < streamingPrioReassertInterval {
		s.prioMu.Unlock()
		return
	}
	s.prioAsserted[hash] = now
	s.prioMu.Unlock()

	if err := s.EnsureStreamingPrio(ctx, hash); err != nil {
		s.logger.Warn("torrents: ensure streaming priorities", "hash", hash, "error", err)
	}
}

// linkFor returns the existing link for a torrent's file index, or
// store.ErrNotFound.
func (s *Service) linkFor(ctx context.Context, torrentID string, fileIndex int) (store.Link, error) {
	links, err := s.store.LinksByTorrent(ctx, torrentID)
	if err != nil {
		return store.Link{}, fmt.Errorf("lookup links: %w", err)
	}
	for _, l := range links {
		if l.FileIndex == fileIndex {
			return l, nil
		}
	}
	return store.Link{}, store.ErrNotFound
}

// LiveProgress returns the download fraction (0..1) for each of the
// given infohashes that qBittorrent currently knows about. Best-effort:
// hashes it isn't tracking are simply absent from the result, and any
// backend error yields an empty map rather than failing the caller. Used
// to annotate the Stremio stream list with the progress of torrents the
// user has already started.
func (s *Service) LiveProgress(ctx context.Context, hashes []string) map[string]float64 {
	out := map[string]float64{}
	if s == nil || s.dc == nil || len(hashes) == 0 {
		return out
	}
	infos, err := s.dc.Torrents(ctx, hashes)
	if err != nil {
		return out
	}
	for _, t := range infos {
		out[strings.ToLower(t.Hash)] = t.Progress
	}
	return out
}

// OwnedForContent returns torrents the app already added for exactly this
// Stremio content identity (source, ref, season, episode) — used to
// surface already-downloaded / in-progress torrents as high-priority
// streams. Best-effort: any store error yields no rows rather than
// failing the stream request.
func (s *Service) OwnedForContent(ctx context.Context, source, ref string, season, episode int) []store.Torrent {
	if s == nil || s.store == nil {
		return nil
	}
	owned, err := s.store.TorrentsByContent(ctx, source, ref, season, episode)
	if err != nil {
		s.logger.Warn("torrents: owned-for-content lookup", "source", source, "ref", ref, "error", err)
		return nil
	}
	return owned
}

// StoredByHashes returns the torrents already in the local store whose
// infohash is among hashes, keyed by lowercase infohash. Unlike
// OwnedForContent it matches on infohash alone, independent of any Stremio
// content identity — used to surface torrents the app already downloaded
// (e.g. grabbed in the background by the RSS poller) as ready streams when
// a Prowlarr search turns up the same release. Best-effort: any store
// error yields an empty map rather than failing the stream request.
func (s *Service) StoredByHashes(ctx context.Context, hashes []string) map[string]store.Torrent {
	if s == nil || s.store == nil || len(hashes) == 0 {
		return map[string]store.Torrent{}
	}
	stored, err := s.store.TorrentsByHashes(ctx, hashes)
	if err != nil {
		s.logger.Warn("torrents: stored-by-hashes lookup", "error", err)
		return map[string]store.Torrent{}
	}
	return stored
}

// Remove deletes a torrent from qBittorrent and the local store. A torrent
// already missing on either side is treated as already-removed, not an
// error.
//
// ev records why the torrent went away. Callers supply the reason and the
// evidence only they hold — cleanup knows the seed time it enforced, the
// stream handler knows the progress threshold — while the torrent's
// identity and whether files were deleted are filled in here. Taking the
// event as a parameter makes it a compile error to add a removal path
// without stating a reason.
func (s *Service) Remove(ctx context.Context, tor store.Torrent, ev store.DeletionEvent) error {
	deleteFiles := s.settings().DeleteFilesOnRemove
	if err := s.dc.Delete(ctx, tor.Hash, deleteFiles); err != nil && !errors.Is(err, downloader.ErrTorrentNotFound) {
		return fmt.Errorf("delete from download client: %w", err)
	}
	if err := s.store.DeleteTorrent(ctx, tor.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("delete from store: %w", err)
	}
	s.prioMu.Lock()
	delete(s.prioAsserted, tor.Hash)
	delete(s.kickAsserted, tor.Hash)
	s.prioMu.Unlock()

	ev.TorrentID = tor.ID
	ev.Hash = tor.Hash
	ev.Name = tor.Name
	ev.Indexer = tor.Indexer
	ev.Origin = tor.Origin
	ev.FilesDeleted = deleteFiles
	// The removal itself succeeded; a failure to write the audit record
	// must not be reported as a failed removal.
	if err := s.store.RecordDeletion(ctx, ev); err != nil {
		s.logger.Warn("could not record deletion", "id", tor.ID, "hash", tor.Hash, "error", err)
	}
	return nil
}

// Resolve is the end-to-end resolve-on-play flow: add the torrent (via
// the raw .torrent file when available, else the magnet), wait for
// metadata, pick the file matching sel, and mint a streaming link.
func (s *Service) Resolve(ctx context.Context, magnet string, torrentFile []byte, sel Selector) (store.Link, error) {
	tor, err := s.EnsureAdded(ctx, magnet, torrentFile, sel)
	if err != nil {
		return store.Link{}, err
	}
	files, err := s.WaitForMetadata(ctx, tor.Hash, s.settings().MetadataTimeout)
	if err != nil {
		return store.Link{}, err
	}
	idx, err := PickFile(files, sel)
	if err != nil {
		return store.Link{}, err
	}
	return s.SelectAndLink(ctx, tor, idx, files)
}
