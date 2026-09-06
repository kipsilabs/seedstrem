// Package fake provides an in-memory fake implementing downloader.Client
// directly. Every consumer depends on the downloader.Client Go interface,
// not a concrete backend transport, so faking at that boundary is
// sufficient and far simpler than standing up a fake WebUI/RPC server.
package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/metainfo"
)

// File is one file of a fake torrent.
type File struct {
	Name     string
	Size     int64
	Progress float64
	Priority int
}

// Torrent is the mutable state of one fake torrent. Tests adjust fields
// directly via Server.Update.
type Torrent struct {
	Hash        string
	Name        string
	State       string
	Progress    float64
	Completed   int64
	DlSpeed     int64
	NumSeeds    int64
	Uploaded    int64
	Ratio       float64
	SavePath    string
	ContentPath string
	SeedingTime time.Duration
	AddedAt     time.Time
	Files       []File

	PieceSize   int64
	PieceStates []int // downloader.PieceState values

	Category           string
	Stopped            bool
	SequentialDownload bool
	FirstLastPiecePrio bool
}

// Server is an in-memory fake downloader.Client. Tests construct one
// directly and pass it wherever a downloader.Client is expected — no
// separate adapter/URL is needed since it already satisfies the
// interface.
type Server struct {
	mu       sync.Mutex
	torrents map[string]*Torrent
	calls    []string
	hints    downloader.IncompleteHints
	// prioritizeErr is returned by PrioritizePieces; defaults to
	// downloader.ErrNotSupported like the qBittorrent backend.
	prioritizeErr error
	// freeSpace/freeSpaceErr back FreeSpace. The error defaults to
	// ErrNotSupported so tests opt in to a capable backend explicitly.
	freeSpace    int64
	freeSpaceErr error
	// byLabelErr is returned by TorrentsByLabel, so callers can be tested
	// against an unreachable or incapable download client.
	byLabelErr error
}

// SetHints sets the value returned by IncompleteFileHints.
func (s *Server) SetHints(h downloader.IncompleteHints) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hints = h
}

// SetPrioritizeErr sets the error returned by PrioritizePieces (nil makes
// the fake accept prioritization like a capable backend).
func (s *Server) SetPrioritizeErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prioritizeErr = err
}

// SetFreeSpace makes FreeSpace report n bytes and clears any error.
func (s *Server) SetFreeSpace(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeSpace = n
	s.freeSpaceErr = nil
}

// SetFreeSpaceErr sets the error returned by FreeSpace.
func (s *Server) SetFreeSpaceErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeSpaceErr = err
}

var _ downloader.Client = (*Server)(nil)

// New creates an empty fake.
func New() *Server {
	return &Server{
		torrents:      map[string]*Torrent{},
		prioritizeErr: downloader.ErrNotSupported,
		freeSpaceErr:  downloader.ErrNotSupported,
	}
}

// Put inserts or replaces a torrent.
func (s *Server) Put(t *Torrent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.torrents[strings.ToLower(t.Hash)] = t
}

// Get returns a copy of the torrent, or nil if absent.
func (s *Server) Get(hash string) *Torrent {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return nil
	}
	cp := *t
	cp.Files = append([]File(nil), t.Files...)
	cp.PieceStates = append([]int(nil), t.PieceStates...)
	return &cp
}

// Update mutates a torrent under the lock.
func (s *Server) Update(hash string, fn func(*Torrent)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return false
	}
	fn(t)
	return true
}

// Calls returns a copy of the call log, in order.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *Server) record(format string, args ...any) {
	s.calls = append(s.calls, fmt.Sprintf(format, args...))
}

// --- downloader.Client ---

func (s *Server) AddMagnet(_ context.Context, magnet string, opts downloader.AddOptions) error {
	hash, name, err := metainfo.FromMagnet(magnet)
	if err != nil {
		return fmt.Errorf("fake: invalid magnet: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("add magnet=%s category=%s stopped=%v seq=%v flp=%v",
		hash, opts.Category, opts.Stopped, opts.SequentialDownload, opts.FirstLastPiecePrio)
	if _, exists := s.torrents[hash]; !exists {
		state := downloader.StateDownloading
		if opts.Stopped {
			state = downloader.StatePaused
		}
		s.torrents[hash] = &Torrent{
			Hash:               hash,
			Name:               name,
			State:              state,
			Category:           opts.Category,
			Stopped:            opts.Stopped,
			SequentialDownload: opts.SequentialDownload,
			FirstLastPiecePrio: opts.FirstLastPiecePrio,
		}
	}
	return nil
}

func (s *Server) AddTorrentFile(_ context.Context, raw []byte, opts downloader.AddOptions) error {
	hash, name, _, err := metainfo.FromTorrent(raw)
	if err != nil {
		return fmt.Errorf("fake: invalid torrent file: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("add torrentfile=%s category=%s stopped=%v seq=%v flp=%v",
		hash, opts.Category, opts.Stopped, opts.SequentialDownload, opts.FirstLastPiecePrio)
	if _, exists := s.torrents[hash]; !exists {
		state := downloader.StateDownloading
		if opts.Stopped {
			state = downloader.StatePaused
		}
		s.torrents[hash] = &Torrent{
			Hash:               hash,
			Name:               name,
			State:              state,
			Category:           opts.Category,
			Stopped:            opts.Stopped,
			SequentialDownload: opts.SequentialDownload,
			FirstLastPiecePrio: opts.FirstLastPiecePrio,
		}
	}
	return nil
}

func (s *Server) Torrents(_ context.Context, hashes []string) ([]downloader.TorrentInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, h := range hashes {
		want[strings.ToLower(h)] = true
	}
	out := make([]downloader.TorrentInfo, 0, len(hashes))
	for hash, t := range s.torrents {
		if !want[hash] {
			continue
		}
		out = append(out, toTorrentInfo(t))
	}
	return out, nil
}

// SetTorrentsByLabelErr makes TorrentsByLabel fail, so callers can be
// tested against an unreachable or incapable download client.
func (s *Server) SetTorrentsByLabelErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byLabelErr = err
}

// TorrentsByLabel returns the fake torrents whose Category matches label.
func (s *Server) TorrentsByLabel(_ context.Context, label string) ([]downloader.TorrentInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("torrentsByLabel label=%s", label)
	if label == "" {
		return nil, nil
	}
	if s.byLabelErr != nil {
		return nil, s.byLabelErr
	}
	var out []downloader.TorrentInfo
	for _, t := range s.torrents {
		if strings.EqualFold(t.Category, label) {
			out = append(out, toTorrentInfo(t))
		}
	}
	return out, nil
}

func (s *Server) Torrent(_ context.Context, hash string) (downloader.TorrentInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return downloader.TorrentInfo{}, downloader.ErrTorrentNotFound
	}
	return toTorrentInfo(t), nil
}

func toTorrentInfo(t *Torrent) downloader.TorrentInfo {
	return downloader.TorrentInfo{
		Hash:        t.Hash,
		Name:        t.Name,
		State:       t.State,
		Progress:    t.Progress,
		Completed:   t.Completed,
		DlSpeed:     t.DlSpeed,
		NumSeeds:    t.NumSeeds,
		Uploaded:    t.Uploaded,
		Ratio:       t.Ratio,
		SavePath:    t.SavePath,
		ContentPath: t.ContentPath,
		SeedingTime: t.SeedingTime,
		AddedAt:     t.AddedAt,

		SequentialDownload: t.SequentialDownload,
		FirstLastPiecePrio: t.FirstLastPiecePrio,
	}
}

func (s *Server) Files(_ context.Context, hash string) ([]downloader.FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return nil, downloader.ErrTorrentNotFound
	}
	out := make([]downloader.FileInfo, len(t.Files))
	for i, f := range t.Files {
		out[i] = downloader.FileInfo{Index: i, Name: f.Name, Size: f.Size, Progress: f.Progress, Priority: f.Priority}
	}
	return out, nil
}

func (s *Server) Properties(_ context.Context, hash string) (downloader.Properties, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return downloader.Properties{}, downloader.ErrTorrentNotFound
	}
	return downloader.Properties{PieceSize: t.PieceSize, SavePath: t.SavePath}, nil
}

func (s *Server) PieceStates(_ context.Context, hash string) ([]downloader.PieceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return nil, downloader.ErrTorrentNotFound
	}
	out := make([]downloader.PieceState, len(t.PieceStates))
	for i, v := range t.PieceStates {
		out[i] = downloader.PieceState(v)
	}
	return out, nil
}

// Remove deletes a torrent entirely, as if the download client forgot it.
func (s *Server) Remove(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.torrents, strings.ToLower(hash))
}

func (s *Server) SetFilePriority(_ context.Context, hash string, indices []int, priority int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return downloader.ErrTorrentNotFound
	}
	s.record("filePrio hash=%s indices=%v priority=%d", hash, indices, priority)
	for _, idx := range indices {
		if idx < 0 || idx >= len(t.Files) {
			continue
		}
		t.Files[idx].Priority = priority
	}
	return nil
}

func (s *Server) SetSequentialDownload(_ context.Context, hash string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return downloader.ErrTorrentNotFound
	}
	t.SequentialDownload = on
	s.record("setSequentialDownload hash=%s on=%v", hash, on)
	return nil
}

func (s *Server) SetFirstLastPiecePrio(_ context.Context, hash string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return downloader.ErrTorrentNotFound
	}
	t.FirstLastPiecePrio = on
	s.record("setFirstLastPiecePrio hash=%s on=%v", hash, on)
	return nil
}

func (s *Server) Start(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[strings.ToLower(hash)]
	if !ok {
		return downloader.ErrTorrentNotFound
	}
	s.record("start hash=%s", hash)
	t.Stopped = false
	t.State = downloader.StateDownloading
	return nil
}

func (s *Server) Delete(_ context.Context, hash string, deleteFiles bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(hash)
	if _, ok := s.torrents[key]; !ok {
		return downloader.ErrTorrentNotFound
	}
	s.record("delete hash=%s deleteFiles=%v", hash, deleteFiles)
	delete(s.torrents, key)
	return nil
}

func (s *Server) IncompleteFileHints(context.Context) (downloader.IncompleteHints, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hints, nil
}

func (s *Server) PrioritizePieces(_ context.Context, hash string, first, last int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("prioritizePieces hash=%s first=%d last=%d", hash, first, last)
	return s.prioritizeErr
}

func (s *Server) FreeSpace(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freeSpace, s.freeSpaceErr
}

func (s *Server) Version(context.Context) (string, error) {
	return "4.6.5", nil
}
