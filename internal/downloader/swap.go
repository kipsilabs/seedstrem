package downloader

import (
	"context"
	"sync/atomic"
)

// Swappable is a Client whose backing client can be replaced at runtime
// (when the admin UI changes download-client connection settings).
type Swappable struct {
	current atomic.Pointer[Client]
}

// NewSwappable wraps an initial client.
func NewSwappable(c Client) *Swappable {
	s := &Swappable{}
	s.current.Store(&c)
	return s
}

// Swap replaces the backing client.
func (s *Swappable) Swap(c Client) {
	s.current.Store(&c)
}

// SwapAndReturn replaces the backing client and returns the previous one,
// so callers can close backends that hold connections (Deluge).
func (s *Swappable) SwapAndReturn(c Client) Client {
	return *s.current.Swap(&c)
}

func (s *Swappable) get() Client { return *s.current.Load() }

func (s *Swappable) AddMagnet(ctx context.Context, magnet string, opts AddOptions) error {
	return s.get().AddMagnet(ctx, magnet, opts)
}

func (s *Swappable) AddTorrentFile(ctx context.Context, raw []byte, opts AddOptions) error {
	return s.get().AddTorrentFile(ctx, raw, opts)
}

func (s *Swappable) Torrents(ctx context.Context, hashes []string) ([]TorrentInfo, error) {
	return s.get().Torrents(ctx, hashes)
}

func (s *Swappable) TorrentsByLabel(ctx context.Context, label string) ([]TorrentInfo, error) {
	return s.get().TorrentsByLabel(ctx, label)
}

func (s *Swappable) Torrent(ctx context.Context, hash string) (TorrentInfo, error) {
	return s.get().Torrent(ctx, hash)
}

func (s *Swappable) Files(ctx context.Context, hash string) ([]FileInfo, error) {
	return s.get().Files(ctx, hash)
}

func (s *Swappable) Properties(ctx context.Context, hash string) (Properties, error) {
	return s.get().Properties(ctx, hash)
}

func (s *Swappable) PieceStates(ctx context.Context, hash string) ([]PieceState, error) {
	return s.get().PieceStates(ctx, hash)
}

func (s *Swappable) SetFilePriority(ctx context.Context, hash string, indices []int, priority int) error {
	return s.get().SetFilePriority(ctx, hash, indices, priority)
}

func (s *Swappable) SetSequentialDownload(ctx context.Context, hash string, on bool) error {
	return s.get().SetSequentialDownload(ctx, hash, on)
}

func (s *Swappable) SetFirstLastPiecePrio(ctx context.Context, hash string, on bool) error {
	return s.get().SetFirstLastPiecePrio(ctx, hash, on)
}

func (s *Swappable) Start(ctx context.Context, hash string) error {
	return s.get().Start(ctx, hash)
}

func (s *Swappable) Delete(ctx context.Context, hash string, deleteFiles bool) error {
	return s.get().Delete(ctx, hash, deleteFiles)
}

func (s *Swappable) IncompleteFileHints(ctx context.Context) (IncompleteHints, error) {
	return s.get().IncompleteFileHints(ctx)
}

func (s *Swappable) PrioritizePieces(ctx context.Context, hash string, first, last int) error {
	return s.get().PrioritizePieces(ctx, hash, first, last)
}

func (s *Swappable) FreeSpace(ctx context.Context) (int64, error) {
	return s.get().FreeSpace(ctx)
}

func (s *Swappable) ExportTorrent(ctx context.Context, hash string) ([]byte, error) {
	return s.get().ExportTorrent(ctx, hash)
}

func (s *Swappable) Version(ctx context.Context) (string, error) {
	return s.get().Version(ctx)
}
