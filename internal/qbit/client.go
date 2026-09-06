// Package qbit implements downloader.Client against the qBittorrent
// WebUI API. The adapter delegates to github.com/autobrr/go-qbittorrent,
// which handles cookie login, re-authentication, and qBittorrent 4.x/5.x
// endpoint renames.
package qbit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	qbt "github.com/autobrr/go-qbittorrent"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// incompleteExt is appended by qBittorrent when "Append .!qB extension
// to incomplete files" is enabled.
const incompleteExt = ".!qB"

// isNotFound reports whether err is qBittorrent signalling that the hash
// is unknown. This happens for a brief window right after adding a
// torrent (the add is accepted asynchronously, before the torrent is
// queryable) and while a magnet's metadata has not resolved yet. Callers
// map it to downloader.ErrTorrentNotFound so WaitForMetadata polls
// instead of failing hard.
func isNotFound(err error) bool {
	return errors.Is(err, qbt.ErrTorrentNotFound) ||
		errors.Is(err, qbt.ErrTorrentMetadataNotDownloadedYet)
}

type client struct {
	qb       *qbt.Client
	category string
}

// New creates a downloader.Client for the qBittorrent WebUI at url.
// Login happens lazily inside the underlying library on first use, so
// New cannot fail — matching seedstrem's hot-swappable client pattern.
// category is the default category applied to torrents seedstrem adds.
func New(url, username, password, category string) downloader.Client {
	return &client{
		qb: qbt.NewClient(qbt.Config{
			Host:     url,
			Username: username,
			Password: password,
		}),
		category: category,
	}
}

func (c *client) addOptionsMap(opts downloader.AddOptions) map[string]string {
	m := map[string]string{}
	category := opts.Category
	if category == "" {
		category = c.category
	}
	if category != "" {
		m["category"] = category
	}
	if opts.Stopped {
		// qBittorrent 5.x uses "stopped", 4.x uses "paused"; unknown form
		// fields are ignored, so send both.
		m["stopped"] = "true"
		m["paused"] = "true"
	}
	if opts.SequentialDownload {
		m["sequentialDownload"] = "true"
	}
	if opts.FirstLastPiecePrio {
		m["firstLastPiecePrio"] = "true"
	}
	return m
}

func (c *client) AddMagnet(ctx context.Context, magnet string, opts downloader.AddOptions) error {
	if _, err := c.qb.AddTorrentFromUrlCtx(ctx, magnet, c.addOptionsMap(opts)); err != nil {
		return fmt.Errorf("qbit add magnet: %w", err)
	}
	return nil
}

// AddTorrentFile adds a torrent from raw .torrent bytes. Unlike a magnet,
// the metadata is already present, so qBittorrent skips the metadata
// (metaDL) fetch entirely — essential for private trackers whose peers
// won't reliably serve metadata over the wire.
func (c *client) AddTorrentFile(ctx context.Context, raw []byte, opts downloader.AddOptions) error {
	if _, err := c.qb.AddTorrentFromMemoryCtx(ctx, raw, c.addOptionsMap(opts)); err != nil {
		return fmt.Errorf("qbit add torrent file: %w", err)
	}
	return nil
}

func convertTorrent(t qbt.Torrent) downloader.TorrentInfo {
	return downloader.TorrentInfo{
		Hash:        t.Hash,
		Name:        t.Name,
		State:       normalizeState(string(t.State)),
		Progress:    t.Progress,
		Size:        t.Size,
		Completed:   t.Completed,
		DlSpeed:     t.DlSpeed,
		NumSeeds:    t.NumSeeds,
		Uploaded:    t.Uploaded,
		Ratio:       t.Ratio,
		SavePath:    t.SavePath,
		ContentPath: t.ContentPath,
		SeedingTime: time.Duration(t.SeedingTime) * time.Second,
		AddedAt:     time.Unix(t.AddedOn, 0),

		SequentialDownload: t.SequentialDownload,
		FirstLastPiecePrio: t.FirstLastPiecePrio,
	}
}

func (c *client) Torrents(ctx context.Context, hashes []string) ([]downloader.TorrentInfo, error) {
	if len(hashes) == 0 {
		// An empty filter means "every torrent" to qBittorrent, which could
		// include torrents unrelated to seedstrem on a shared instance.
		return nil, nil
	}
	list, err := c.qb.GetTorrentsCtx(ctx, qbt.TorrentFilterOptions{Hashes: hashes})
	if err != nil {
		return nil, fmt.Errorf("qbit list torrents: %w", err)
	}
	infos := make([]downloader.TorrentInfo, 0, len(list))
	for _, t := range list {
		infos = append(infos, convertTorrent(t))
	}
	return infos, nil
}

func (c *client) TorrentsByLabel(ctx context.Context, label string) ([]downloader.TorrentInfo, error) {
	if label == "" {
		// An empty category means "uncategorised" to qBittorrent, which
		// is not what a caller with no label configured is asking for.
		return nil, nil
	}
	list, err := c.qb.GetTorrentsCtx(ctx, qbt.TorrentFilterOptions{Category: label})
	if err != nil {
		return nil, fmt.Errorf("qbit list torrents by category: %w", err)
	}
	infos := make([]downloader.TorrentInfo, 0, len(list))
	for _, t := range list {
		infos = append(infos, convertTorrent(t))
	}
	return infos, nil
}

func (c *client) Torrent(ctx context.Context, hash string) (downloader.TorrentInfo, error) {
	list, err := c.qb.GetTorrentsCtx(ctx, qbt.TorrentFilterOptions{Hashes: []string{hash}})
	if err != nil {
		return downloader.TorrentInfo{}, fmt.Errorf("qbit get torrent %s: %w", hash, err)
	}
	for _, t := range list {
		if strings.EqualFold(t.Hash, hash) {
			return convertTorrent(t), nil
		}
	}
	return downloader.TorrentInfo{}, downloader.ErrTorrentNotFound
}

func (c *client) Files(ctx context.Context, hash string) ([]downloader.FileInfo, error) {
	files, err := c.qb.GetFilesInformationCtx(ctx, hash)
	if err != nil {
		if isNotFound(err) {
			return nil, downloader.ErrTorrentNotFound
		}
		return nil, fmt.Errorf("qbit files %s: %w", hash, err)
	}
	if files == nil {
		return nil, nil
	}
	infos := make([]downloader.FileInfo, 0, len(*files))
	for _, f := range *files {
		infos = append(infos, downloader.FileInfo{
			Index:    f.Index,
			Name:     f.Name,
			Size:     f.Size,
			Progress: float64(f.Progress),
			Priority: int(f.Priority),
		})
	}
	return infos, nil
}

func (c *client) Properties(ctx context.Context, hash string) (downloader.Properties, error) {
	p, err := c.qb.GetTorrentPropertiesCtx(ctx, hash)
	if err != nil {
		if isNotFound(err) {
			return downloader.Properties{}, downloader.ErrTorrentNotFound
		}
		return downloader.Properties{}, fmt.Errorf("qbit properties %s: %w", hash, err)
	}
	return downloader.Properties{
		PieceSize: int64(p.PieceSize),
		PiecesNum: p.PiecesNum,
		SavePath:  p.SavePath,
	}, nil
}

func (c *client) PieceStates(ctx context.Context, hash string) ([]downloader.PieceState, error) {
	states, err := c.qb.GetTorrentPieceStatesCtx(ctx, hash)
	if err != nil {
		if isNotFound(err) {
			return nil, downloader.ErrTorrentNotFound
		}
		return nil, fmt.Errorf("qbit piece states %s: %w", hash, err)
	}
	out := make([]downloader.PieceState, len(states))
	for i, s := range states {
		out[i] = downloader.PieceState(s)
	}
	return out, nil
}

func (c *client) SetFilePriority(ctx context.Context, hash string, indices []int, priority int) error {
	if len(indices) == 0 {
		return nil
	}
	ids := make([]string, len(indices))
	for i, idx := range indices {
		ids[i] = strconv.Itoa(idx)
	}
	if err := c.qb.SetFilePriorityCtx(ctx, hash, strings.Join(ids, "|"), priority); err != nil {
		if isNotFound(err) {
			return downloader.ErrTorrentNotFound
		}
		return fmt.Errorf("qbit set file priority %s: %w", hash, err)
	}
	return nil
}

// SetSequentialDownload sets the sequential-download flag to an absolute
// state. qBittorrent's WebUI only offers a blind toggle, so the current
// flag is read back first and toggled only when it differs.
func (c *client) SetSequentialDownload(ctx context.Context, hash string, on bool) error {
	info, err := c.Torrent(ctx, hash)
	if err != nil {
		return fmt.Errorf("read sequential download flag: %w", err)
	}
	if info.SequentialDownload == on {
		return nil
	}
	if err := c.qb.ToggleTorrentSequentialDownloadCtx(ctx, []string{hash}); err != nil {
		if isNotFound(err) {
			return downloader.ErrTorrentNotFound
		}
		return fmt.Errorf("qbit toggle sequential download %s: %w", hash, err)
	}
	return nil
}

// SetFirstLastPiecePrio sets the first/last-piece-priority flag to an
// absolute state, via qBittorrent's blind toggle (see
// SetSequentialDownload).
func (c *client) SetFirstLastPiecePrio(ctx context.Context, hash string, on bool) error {
	info, err := c.Torrent(ctx, hash)
	if err != nil {
		return fmt.Errorf("read first/last piece prio flag: %w", err)
	}
	if info.FirstLastPiecePrio == on {
		return nil
	}
	if err := c.qb.ToggleFirstLastPiecePrioCtx(ctx, []string{hash}); err != nil {
		if isNotFound(err) {
			return downloader.ErrTorrentNotFound
		}
		return fmt.Errorf("qbit toggle first/last piece prio %s: %w", hash, err)
	}
	return nil
}

func (c *client) Start(ctx context.Context, hash string) error {
	if err := c.qb.StartCtx(ctx, []string{hash}); err != nil {
		return fmt.Errorf("qbit start %s: %w", hash, err)
	}
	return nil
}

func (c *client) Delete(ctx context.Context, hash string, deleteFiles bool) error {
	if err := c.qb.DeleteTorrentsCtx(ctx, []string{hash}, deleteFiles); err != nil {
		return fmt.Errorf("qbit delete %s: %w", hash, err)
	}
	return nil
}

// IncompleteFileHints reads the qBittorrent settings that determine where
// in-progress files live: the optional temp download folder and the
// optional .!qB extension.
func (c *client) IncompleteFileHints(ctx context.Context) (downloader.IncompleteHints, error) {
	p, err := c.qb.GetAppPreferencesCtx(ctx)
	if err != nil {
		return downloader.IncompleteHints{}, fmt.Errorf("qbit app preferences: %w", err)
	}
	h := downloader.IncompleteHints{}
	if p.TempPathEnabled {
		h.TempDir = p.TempPath
	}
	if p.IncompleteFilesExt {
		h.IncompleteExt = incompleteExt
	}
	return h, nil
}

// PrioritizePieces is unsupported: qBittorrent's WebUI API exposes no
// per-piece priority primitive (qBittorrent#13612).
func (c *client) PrioritizePieces(context.Context, string, int, int) error {
	return downloader.ErrNotSupported
}

// FreeSpace reads qBittorrent's own free-space figure out of the sync
// endpoint's server state. rid 0 asks for a full update, which is the
// only variant that reliably carries server_state.
func (c *client) FreeSpace(ctx context.Context) (int64, error) {
	main, err := c.qb.SyncMainDataCtx(ctx, 0)
	if err != nil {
		return 0, fmt.Errorf("qbit sync maindata: %w", err)
	}
	return main.ServerState.FreeSpaceOnDisk, nil
}

func (c *client) Version(ctx context.Context) (string, error) {
	v, err := c.qb.GetAppVersionCtx(ctx)
	if err != nil {
		return "", fmt.Errorf("qbit version: %w", err)
	}
	return v, nil
}
