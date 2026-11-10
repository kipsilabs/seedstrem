// Package downloader defines the backend-neutral download-client surface
// seedstrem programs against. Concrete backends (internal/qbit for
// qBittorrent, internal/deluge for Deluge) implement Client; everything
// else — torrent orchestration, streaming, syncing, cleanup — depends
// only on this package.
package downloader

import (
	"context"
	"errors"
)

// ErrTorrentNotFound is returned when the download client does not know
// the hash.
var ErrTorrentNotFound = errors.New("torrent not found in download client")

// ErrNotSupported is returned when the current backend cannot perform an
// operation (e.g. piece prioritization on qBittorrent, whose WebUI API
// has no per-piece primitive). Callers detect it with errors.Is and fall
// back. It is a dynamic capability signal on purpose: wrappers like
// Swappable forward it from whatever backend is live, which a static
// interface assertion could not express across hot-swaps.
var ErrNotSupported = errors.New("operation not supported by download client")

// ErrHintDeclined is returned by PrioritizePieces when the backend
// understood the call but did not apply the window (torrent not
// registered yet, metadata still incoming, per-piece failure). Unlike
// ErrNotSupported it says nothing about the backend's capabilities: the
// same call moments later usually succeeds, so callers must retry
// promptly rather than back off. The distinction matters most right
// after an add, when the first hint of a play — the one the playability
// grace depends on — is the one most likely to be declined.
var ErrHintDeclined = errors.New("download client declined piece prioritization")

// Client is the download-client surface used by seedstrem.
type Client interface {
	AddMagnet(ctx context.Context, magnet string, opts AddOptions) error
	AddTorrentFile(ctx context.Context, raw []byte, opts AddOptions) error
	Torrents(ctx context.Context, hashes []string) ([]TorrentInfo, error)

	// TorrentsByLabel lists the client's torrents carrying label (a
	// Deluge label, a qBittorrent category). Unlike Torrents it is a
	// server-side filter, so it never returns torrents unrelated to
	// seedstrem on a shared instance. An empty label returns no
	// torrents. Backends that cannot filter by label — Deluge without
	// the Label plugin — return ErrNotSupported.
	TorrentsByLabel(ctx context.Context, label string) ([]TorrentInfo, error)
	Torrent(ctx context.Context, hash string) (TorrentInfo, error)
	Files(ctx context.Context, hash string) ([]FileInfo, error)
	Properties(ctx context.Context, hash string) (Properties, error)
	PieceStates(ctx context.Context, hash string) ([]PieceState, error)
	SetFilePriority(ctx context.Context, hash string, indices []int, priority int) error

	// SetSequentialDownload and SetFirstLastPiecePrio set the streaming
	// flags to an absolute state. Backends whose native API only offers a
	// blind toggle (qBittorrent) read the current state and toggle as
	// needed internally.
	SetSequentialDownload(ctx context.Context, hash string, on bool) error
	SetFirstLastPiecePrio(ctx context.Context, hash string, on bool) error

	Start(ctx context.Context, hash string) error
	Delete(ctx context.Context, hash string, deleteFiles bool) error

	// IncompleteFileHints describes where the backend keeps files that
	// are still downloading: an optional separate temp directory and an
	// optional extension appended to in-progress files. Zero values mean
	// the backend writes incomplete files in place under their final
	// names.
	IncompleteFileHints(ctx context.Context) (IncompleteHints, error)

	// PrioritizePieces asks the backend to fetch pieces [first, last] of
	// the torrent as soon as possible, ahead of the regular (sequential)
	// piece order. Backends without a per-piece primitive return
	// ErrNotSupported.
	PrioritizePieces(ctx context.Context, hash string, first, last int) error

	// FreeSpace reports the bytes still available on the download
	// client's own filesystem. It is the client's view, not seedstrem's:
	// the two differ whenever the client runs on another host. Backends
	// without the concept return ErrNotSupported. Neither supported
	// backend can report the filesystem *total*, so callers get headroom
	// only — not a percentage.
	FreeSpace(ctx context.Context) (int64, error)

	// ExportTorrent returns the raw .torrent the client holds for hash,
	// used to keep a copy before the torrent is removed so it can be
	// re-added later. Backends that cannot export (Deluge) or old
	// versions (qBittorrent < 5.0) return ErrNotSupported; an unknown
	// hash returns ErrTorrentNotFound.
	ExportTorrent(ctx context.Context, hash string) ([]byte, error)

	Version(ctx context.Context) (string, error)
}
