// Package deluge implements downloader.Client against a Deluge 2 daemon
// using the vendored native RPC client (internal/deluge/delugerpc).
package deluge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gdm85/go-rencode"

	"github.com/kipsilabs/seedstrem/internal/deluge/delugerpc"
	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// api is the slice of the vendored client the adapter uses, extracted so
// tests can fake the RPC boundary without a live daemon.
type api interface {
	Connect(ctx context.Context) error
	Close() error
	TorrentStatus(ctx context.Context, hash string) (*delugerpc.TorrentStatus, error)
	TorrentsStatus(ctx context.Context, state delugerpc.TorrentState, ids []string) (map[string]*delugerpc.TorrentStatus, error)
	TorrentsStatusByLabel(ctx context.Context, label string) (map[string]*delugerpc.TorrentStatus, error)
	AddTorrentMagnet(ctx context.Context, magnetURI string, options *delugerpc.Options) (string, error)
	AddTorrentFile(ctx context.Context, fileName, fileContentBase64 string, options *delugerpc.Options) (string, error)
	RemoveTorrent(ctx context.Context, id string, rmFiles bool) (bool, error)
	SetTorrentOptions(ctx context.Context, id string, options *delugerpc.Options) error
	SetFilePriorities(ctx context.Context, hash string, priorities []int) error
	PieceStates(ctx context.Context, hash string) ([]int, error)
	ResumeTorrents(ctx context.Context, ids ...string) error
	DaemonVersion(ctx context.Context) (string, error)
	GetFreeSpace(ctx context.Context, path string) (int64, error)
	GetEnabledPlugins(ctx context.Context) ([]string, error)
	RPC(ctx context.Context, method string, args rencode.List, kwargs rencode.Dictionary) (rencode.List, error)
}

type flags struct {
	seq, flp bool
}

type client struct {
	// mu serializes every RPC call: the vendored client multiplexes one
	// TCP connection with a shared serial counter and has no internal
	// locking.
	mu        sync.Mutex
	rpc       api
	connected bool
	label     string

	// flagCache remembers the last sequential/first-last values written
	// per hash. Deluge's readable status does not include them, so this
	// best-effort cache backs the TorrentInfo flag fields (which
	// seedstrem only logs since the move to absolute setters).
	flagMu    sync.Mutex
	flagCache map[string]flags

	// Seedstream-plugin probe cache (see plugin.go).
	pluginMu      sync.Mutex
	pluginOK      bool
	pluginChecked time.Time

	// now is injectable for tests.
	now func() time.Time
}

// New creates a downloader.Client for the Deluge 2 daemon at host:port.
// The TCP connection and daemon login happen lazily on first use, so New
// cannot fail — matching seedstrem's hot-swappable client pattern. label
// is applied to torrents seedstrem adds (best-effort, requires Deluge's
// Label plugin).
func New(host string, port int, username, password, label string) downloader.Client {
	return &client{
		rpc: delugerpc.NewV2(delugerpc.Settings{
			Hostname: host,
			Port:     uint(port),
			Login:    username,
			Password: password,
		}),
		label:     label,
		flagCache: map[string]flags{},
		now:       time.Now,
	}
}

var _ downloader.Client = (*client)(nil)

// Close terminates the daemon connection. Called by the admin hot-swap
// when this client is replaced.
func (c *client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	return c.rpc.Close()
}

// do runs fn with the connection established, holding the client mutex.
// Any error other than a daemon-side RPCError is treated as a transport
// failure: the connection is dropped so the next call redials.
func (c *client) do(ctx context.Context, fn func(context.Context) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		if err := c.rpc.Connect(ctx); err != nil {
			return fmt.Errorf("deluge connect: %w", err)
		}
		c.connected = true
	}
	err := fn(ctx)
	if err != nil && !isDaemonError(err) {
		c.connected = false
		_ = c.rpc.Close()
	}
	return err
}

// isDaemonError reports whether err came back from a live daemon (an
// RPC-level failure, possibly already mapped to ErrTorrentNotFound or
// ErrHintDeclined) as opposed to a transport failure that should drop
// the connection.
//
// ErrHintDeclined belongs here: the daemon answered, the plugin just
// said "not this window". It is also the one daemon answer a caller
// retries within milliseconds, so misreading it as a transport failure
// redials the daemon on every retry — during the first seconds after an
// add, when the availability poller shares that same connection and the
// playability grace is already running out.
// ErrNotSupported belongs here for the same reason: it means the daemon
// answered and lacks a capability (no Label plugin), which no amount of
// redialling fixes — dropping the connection would only redial on every
// scan.
func isDaemonError(err error) bool {
	return errors.As(err, new(delugerpc.RPCError)) ||
		errors.Is(err, downloader.ErrTorrentNotFound) ||
		errors.Is(err, downloader.ErrHintDeclined) ||
		errors.Is(err, downloader.ErrNotSupported)
}

func (c *client) cachedFlags(hash string) flags {
	c.flagMu.Lock()
	defer c.flagMu.Unlock()
	return c.flagCache[strings.ToLower(hash)]
}

func (c *client) rememberFlags(hash string, update func(*flags)) {
	c.flagMu.Lock()
	defer c.flagMu.Unlock()
	f := c.flagCache[strings.ToLower(hash)]
	update(&f)
	c.flagCache[strings.ToLower(hash)] = f
}

func (c *client) addOptions(opts downloader.AddOptions) *delugerpc.Options {
	paused := opts.Stopped
	seq := opts.SequentialDownload
	flp := opts.FirstLastPiecePrio
	o := &delugerpc.Options{
		AddPaused:                 &paused,
		PrioritizeFirstLastPieces: &flp,
	}
	o.V2.SequentialDownload = &seq
	return o
}

// applyLabel tags the torrent with the configured label, best-effort:
// Deluge without the Label plugin (or a label RPC failure) only costs
// the tag, so errors are swallowed.
func (c *client) applyLabel(ctx context.Context, hash, label string) {
	if label == "" {
		return
	}
	plugins, err := c.rpc.GetEnabledPlugins(ctx)
	if err != nil {
		return
	}
	if !slices.Contains(plugins, "Label") {
		return
	}
	// label.add fails when the label already exists; ignore and proceed.
	_, _ = c.rpc.RPC(ctx, "label.add", rencode.NewList(label), rencode.Dictionary{})
	_, _ = c.rpc.RPC(ctx, "label.set_torrent", rencode.NewList(strings.ToLower(hash), label), rencode.Dictionary{})
}

func (c *client) AddMagnet(ctx context.Context, magnet string, opts downloader.AddOptions) error {
	return c.do(ctx, func(ctx context.Context) error {
		hash, err := c.rpc.AddTorrentMagnet(ctx, magnet, c.addOptions(opts))
		if err != nil {
			if isAlreadyAdded(err) {
				return nil
			}
			return fmt.Errorf("deluge add magnet: %w", err)
		}
		c.rememberAddFlags(hash, opts)
		label := opts.Category
		if label == "" {
			label = c.label
		}
		if hash != "" {
			c.applyLabel(ctx, hash, label)
		}
		return nil
	})
}

func (c *client) AddTorrentFile(ctx context.Context, raw []byte, opts downloader.AddOptions) error {
	return c.do(ctx, func(ctx context.Context) error {
		encoded := base64.StdEncoding.EncodeToString(raw)
		hash, err := c.rpc.AddTorrentFile(ctx, "seedstrem.torrent", encoded, c.addOptions(opts))
		if err != nil {
			if isAlreadyAdded(err) {
				return nil
			}
			return fmt.Errorf("deluge add torrent file: %w", err)
		}
		c.rememberAddFlags(hash, opts)
		label := opts.Category
		if label == "" {
			label = c.label
		}
		if hash != "" {
			c.applyLabel(ctx, hash, label)
		}
		return nil
	})
}

func (c *client) rememberAddFlags(hash string, opts downloader.AddOptions) {
	if hash == "" {
		return
	}
	c.rememberFlags(hash, func(f *flags) {
		f.seq = opts.SequentialDownload
		f.flp = opts.FirstLastPiecePrio
	})
}

// isAlreadyAdded reports whether err is Deluge rejecting a duplicate add
// — seedstrem's EnsureAdded treats re-adds as success.
func isAlreadyAdded(err error) bool {
	var rpcErr delugerpc.RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	msg := strings.ToLower(rpcErr.ExceptionMessage)
	return strings.Contains(rpcErr.ExceptionType, "AddTorrentError") &&
		strings.Contains(msg, "already")
}

func (c *client) Torrents(ctx context.Context, hashes []string) ([]downloader.TorrentInfo, error) {
	if len(hashes) == 0 {
		// An empty filter means "every torrent" to Deluge, which could
		// include torrents unrelated to seedstrem on a shared instance.
		return nil, nil
	}
	lower := make([]string, len(hashes))
	for i, h := range hashes {
		lower[i] = strings.ToLower(h)
	}
	var infos []downloader.TorrentInfo
	err := c.do(ctx, func(ctx context.Context) error {
		statuses, err := c.rpc.TorrentsStatus(ctx, delugerpc.StateUnspecified, lower)
		if err != nil {
			return fmt.Errorf("deluge list torrents: %w", err)
		}
		infos = make([]downloader.TorrentInfo, 0, len(statuses))
		for hash, ts := range statuses {
			info := convertTorrent(hash, ts)
			f := c.cachedFlags(info.Hash)
			info.SequentialDownload, info.FirstLastPiecePrio = f.seq, f.flp
			infos = append(infos, info)
		}
		return nil
	})
	return infos, err
}

func (c *client) TorrentsByLabel(ctx context.Context, label string) ([]downloader.TorrentInfo, error) {
	if label == "" {
		return nil, nil
	}
	var infos []downloader.TorrentInfo
	err := c.do(ctx, func(ctx context.Context) error {
		plugins, err := c.rpc.GetEnabledPlugins(ctx)
		if err != nil {
			return fmt.Errorf("deluge enabled plugins: %w", err)
		}
		if !slices.Contains(plugins, "Label") {
			// Without the Label plugin no torrent carries a label at
			// all, so a label query cannot be answered — not even
			// negatively.
			return downloader.ErrNotSupported
		}
		statuses, err := c.rpc.TorrentsStatusByLabel(ctx, strings.ToLower(label))
		if err != nil {
			return fmt.Errorf("deluge list torrents by label: %w", err)
		}
		infos = make([]downloader.TorrentInfo, 0, len(statuses))
		for hash, ts := range statuses {
			info := convertTorrent(hash, ts)
			f := c.cachedFlags(info.Hash)
			info.SequentialDownload, info.FirstLastPiecePrio = f.seq, f.flp
			infos = append(infos, info)
		}
		return nil
	})
	return infos, err
}

func (c *client) Torrent(ctx context.Context, hash string) (downloader.TorrentInfo, error) {
	var info downloader.TorrentInfo
	err := c.do(ctx, func(ctx context.Context) error {
		ts, err := c.torrentStatus(ctx, hash)
		if err != nil {
			return err
		}
		info = convertTorrent(hash, ts)
		f := c.cachedFlags(info.Hash)
		info.SequentialDownload, info.FirstLastPiecePrio = f.seq, f.flp
		return nil
	})
	return info, err
}

// torrentStatus fetches a single torrent's status, mapping unknown-hash
// signals to downloader.ErrTorrentNotFound. Deluge returns an empty
// status dict (not an error) for ids it does not track, detected by the
// zero Name+TotalSize.
func (c *client) torrentStatus(ctx context.Context, hash string) (*delugerpc.TorrentStatus, error) {
	ts, err := c.rpc.TorrentStatus(ctx, strings.ToLower(hash))
	if err != nil {
		if isNotFound(err) {
			return nil, downloader.ErrTorrentNotFound
		}
		return nil, fmt.Errorf("deluge torrent status %s: %w", hash, err)
	}
	if ts == nil || (ts.Name == "" && ts.TotalSize == 0 && ts.State == "") {
		return nil, downloader.ErrTorrentNotFound
	}
	return ts, nil
}

func (c *client) Files(ctx context.Context, hash string) ([]downloader.FileInfo, error) {
	var files []downloader.FileInfo
	err := c.do(ctx, func(ctx context.Context) error {
		ts, err := c.torrentStatus(ctx, hash)
		if err != nil {
			return err
		}
		files = convertFiles(ts)
		return nil
	})
	return files, err
}

func (c *client) Properties(ctx context.Context, hash string) (downloader.Properties, error) {
	var props downloader.Properties
	err := c.do(ctx, func(ctx context.Context) error {
		ts, err := c.torrentStatus(ctx, hash)
		if err != nil {
			return err
		}
		props = downloader.Properties{
			PieceSize: ts.PieceLength,
			PiecesNum: int(ts.NumPieces),
			SavePath:  savePath(ts),
		}
		return nil
	})
	return props, err
}

func (c *client) PieceStates(ctx context.Context, hash string) ([]downloader.PieceState, error) {
	var states []downloader.PieceState
	err := c.do(ctx, func(ctx context.Context) error {
		pieces, err := c.rpc.PieceStates(ctx, strings.ToLower(hash))
		if err != nil {
			if isNotFound(err) {
				return downloader.ErrTorrentNotFound
			}
			return fmt.Errorf("deluge piece states %s: %w", hash, err)
		}
		if pieces != nil {
			states = convertPieces(pieces)
			return nil
		}
		// Deluge reports None instead of a piece list for torrents
		// without metadata and for finished (seeding) torrents. Only the
		// finished case may synthesize an all-have bitfield — the
		// availability gate must never spin on a complete torrent.
		ts, err := c.torrentStatus(ctx, hash)
		if err != nil {
			return err
		}
		if ts.IsFinished || ts.Progress >= 100 {
			states = make([]downloader.PieceState, ts.NumPieces)
			for i := range states {
				states[i] = downloader.PieceHave
			}
		}
		return nil
	})
	return states, err
}

func (c *client) SetFilePriority(ctx context.Context, hash string, indices []int, priority int) error {
	if len(indices) == 0 {
		return nil
	}
	return c.do(ctx, func(ctx context.Context) error {
		ts, err := c.torrentStatus(ctx, hash)
		if err != nil {
			return err
		}
		if len(ts.FilePriorities) == 0 {
			return fmt.Errorf("deluge set file priority %s: no file priorities available yet", hash)
		}
		prios := make([]int, len(ts.FilePriorities))
		for i, p := range ts.FilePriorities {
			prios[i] = int(p)
		}
		mapped := toDelugePriority(priority)
		for _, idx := range indices {
			if idx < 0 || idx >= len(prios) {
				continue
			}
			prios[idx] = mapped
		}
		if err := c.rpc.SetFilePriorities(ctx, strings.ToLower(hash), prios); err != nil {
			if isNotFound(err) {
				return downloader.ErrTorrentNotFound
			}
			return fmt.Errorf("deluge set file priority %s: %w", hash, err)
		}
		return nil
	})
}

func (c *client) SetSequentialDownload(ctx context.Context, hash string, on bool) error {
	return c.do(ctx, func(ctx context.Context) error {
		o := &delugerpc.Options{}
		o.V2.SequentialDownload = &on
		if err := c.rpc.SetTorrentOptions(ctx, strings.ToLower(hash), o); err != nil {
			if isNotFound(err) {
				return downloader.ErrTorrentNotFound
			}
			return fmt.Errorf("deluge set sequential download %s: %w", hash, err)
		}
		c.rememberFlags(hash, func(f *flags) { f.seq = on })
		return nil
	})
}

func (c *client) SetFirstLastPiecePrio(ctx context.Context, hash string, on bool) error {
	return c.do(ctx, func(ctx context.Context) error {
		o := &delugerpc.Options{PrioritizeFirstLastPieces: &on}
		if err := c.rpc.SetTorrentOptions(ctx, strings.ToLower(hash), o); err != nil {
			if isNotFound(err) {
				return downloader.ErrTorrentNotFound
			}
			return fmt.Errorf("deluge set first/last piece prio %s: %w", hash, err)
		}
		c.rememberFlags(hash, func(f *flags) { f.flp = on })
		return nil
	})
}

func (c *client) Start(ctx context.Context, hash string) error {
	return c.do(ctx, func(ctx context.Context) error {
		if err := c.rpc.ResumeTorrents(ctx, strings.ToLower(hash)); err != nil {
			if isNotFound(err) {
				return downloader.ErrTorrentNotFound
			}
			return fmt.Errorf("deluge start %s: %w", hash, err)
		}
		return nil
	})
}

func (c *client) Delete(ctx context.Context, hash string, deleteFiles bool) error {
	err := c.do(ctx, func(ctx context.Context) error {
		if _, err := c.rpc.RemoveTorrent(ctx, strings.ToLower(hash), deleteFiles); err != nil {
			if isNotFound(err) {
				return downloader.ErrTorrentNotFound
			}
			return fmt.Errorf("deluge delete %s: %w", hash, err)
		}
		return nil
	})
	if err == nil {
		c.flagMu.Lock()
		delete(c.flagCache, strings.ToLower(hash))
		c.flagMu.Unlock()
	}
	return err
}

// IncompleteFileHints: Deluge writes in-progress files in place under
// their final names — no temp dir, no extension.
func (c *client) IncompleteFileHints(context.Context) (downloader.IncompleteHints, error) {
	return downloader.IncompleteHints{}, nil
}

// FreeSpace asks the daemon for free space at its default download
// location; core.get_free_space treats an empty path as "the default".
func (c *client) FreeSpace(ctx context.Context) (int64, error) {
	var free int64
	err := c.do(ctx, func(ctx context.Context) error {
		n, err := c.rpc.GetFreeSpace(ctx, "")
		if err != nil {
			return fmt.Errorf("deluge free space: %w", err)
		}
		free = n
		return nil
	})
	return free, err
}

func (c *client) Version(ctx context.Context) (string, error) {
	var version string
	err := c.do(ctx, func(ctx context.Context) error {
		v, err := c.rpc.DaemonVersion(ctx)
		if err != nil {
			return fmt.Errorf("deluge version: %w", err)
		}
		version = "deluge " + v
		return nil
	})
	return version, err
}
