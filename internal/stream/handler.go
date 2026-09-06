package stream

import (
	"context"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/playsession"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

// Settings is the live configuration slice the streaming handler needs.
type Settings struct {
	WaitTimeout time.Duration
	ReadChunk   int64
	// MinProgressForCancel is the download fraction (0..1) a torrent
	// must reach to survive a playback session ending with nobody else
	// watching. <= 0 disables the check.
	MinProgressForCancel float64
}

// Handler serves /dl/{token}/{filename} with Range support over files
// qBittorrent may still be downloading.
type Handler struct {
	store    *store.Store
	dc       downloader.Client
	svc      *torrents.Service
	resolver *Resolver
	avail    *Availability
	sessions *playsession.Sessions
	settings func() Settings
	prio     *prioritizer
	logger   *slog.Logger
}

// NewHandler creates the streaming handler.
func NewHandler(st *store.Store, dc downloader.Client, svc *torrents.Service, resolver *Resolver, avail *Availability, sessions *playsession.Sessions, settings func() Settings, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{store: st, dc: dc, svc: svc, resolver: resolver, avail: avail, sessions: sessions, settings: settings, prio: newPrioritizer(dc, logger), logger: logger}
}

// Router returns the router to mount at /dl.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/{token}", h.serve)
	r.Head("/{token}", h.serve)
	r.Get("/{token}/*", h.serve)
	r.Head("/{token}/*", h.serve)
	return r
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings := h.settings()

	token := chi.URLParam(r, "token")
	h.logger.Debug("stream: serve request", "token", token, "range", r.Header.Get("Range"))

	link, err := h.store.LinkByToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		h.logger.Debug("stream: unknown token", "token", token)
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.internalError(w, "lookup link", err)
		return
	}

	tor, err := h.store.TorrentByID(ctx, link.TorrentID)
	if err != nil || tor.Error != "" {
		http.NotFound(w, r)
		return
	}

	// A serve that only delivered the "still downloading" placeholder is
	// not a playback session the viewer abandoned: they were explicitly
	// told to come back later, so the torrent must survive them leaving.
	servedPlaceholder := false
	endSession := h.sessions.Begin(tor.Hash)
	defer func() {
		if endSession() == 0 && !servedPlaceholder {
			go h.checkAbandoned(tor)
		}
	}()

	info, err := h.dc.Torrent(ctx, tor.Hash)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	files, err := h.dc.Files(ctx, tor.Hash)
	if err != nil {
		h.internalError(w, "download client files", err)
		return
	}
	var file downloader.FileInfo
	found := false
	for _, f := range files {
		if f.Index == link.FileIndex {
			file, found = f, true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	complete := file.Progress >= 1

	contentType := mime.TypeByExtension(path.Ext(file.Name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)

	h.logger.Debug("stream: serving file",
		"hash", tor.Hash, "file", file.Name, "complete", complete, "progress", file.Progress,
		"seqDl", info.SequentialDownload, "flPiecePrio", info.FirstLastPiecePrio)

	if complete {
		localPath, err := h.resolver.FilePath(ctx, info, file)
		if err != nil {
			h.retryLater(w, "file not on disk yet", err)
			return
		}
		f, err := os.Open(localPath)
		if err != nil {
			h.internalError(w, "open completed file", err)
			return
		}
		defer f.Close()
		http.ServeContent(w, r, path.Base(file.Name), time.Time{}, f)
		return
	}

	// Streaming while downloading.
	props, err := h.dc.Properties(ctx, tor.Hash)
	if err != nil || props.PieceSize <= 0 {
		h.retryLater(w, "piece size unavailable", err)
		return
	}
	fileOffset := FileOffset(files, file.Index)
	if fileOffset < 0 {
		h.internalError(w, "file offset", errors.New("file index not in torrent"))
		return
	}
	chunk := settings.ReadChunk
	if chunk <= 0 {
		chunk = 4 << 20
	}

	// Playability gate: a player needs the file's head (container header)
	// on disk, and the tail (MKV cues/seek index) on disk or at least in
	// flight, before it can start at all. Give them a short grace; when
	// they don't get there — typically a
	// super-seeding initial seeder handing out pieces in its own order,
	// which no client-side flag can override — serve the bundled
	// "still downloading, come back later" clip instead of leaving the
	// player spinning until it times out.
	headFirst, headLast := PiecesForRange(fileOffset, props.PieceSize, 0, 0)
	tailFirst, tailLast := PiecesForRange(fileOffset, props.PieceSize, file.Size-1, file.Size-1)
	grace := readyGrace
	if r.Method == http.MethodHead {
		grace = 0 // answer probes instantly with the current state
	}
	if !h.waitStreamReady(ctx, tor.Hash, headFirst, headLast, tailFirst, tailLast, grace) {
		servedPlaceholder = true
		// The piece states behind the decision, not just the piece
		// numbers: "downloading" means the swarm was serving them and the
		// grace was too short, "missing" means the window never reached
		// the picker (declined hint, super-seeding peer, no peers at all).
		// Without them a placeholder in the log is undiagnosable after
		// the fact — the heartbeat that would show it only starts once
		// this gate passes.
		head, tail, sum, sumErr := h.awaitedStates(ctx, tor.Hash, headFirst, headLast, tailFirst, tailLast)
		h.logger.Info("stream: head/tail pieces not available, serving downloading placeholder",
			"hash", tor.Hash, "progress", file.Progress,
			"headPieces", [2]int{headFirst, headLast}, "tailPieces", [2]int{tailFirst, tailLast},
			"headState", pieceStateName(head), "tailState", pieceStateName(tail),
			"pieces", sum.TotalPieces, "have", sum.Have, "downloading", sum.Downloading,
			"frontier", sum.FirstMissing, "summaryErr", sumErr)
		servePlaceholder(w, r, file.Progress)
		return
	}

	// Wait only for qBittorrent to create the file on disk (bounded), then
	// hand off to ServeContent immediately: headers go out right away and
	// partialReader blocks per-read as pieces arrive, so the player shows
	// a buffering spinner. A previous pre-flight piece-wait answered 503
	// *before* any headers on warm-up/forward-seek, which players treat as
	// a hard "loading failed" rather than buffering.
	localPath, err := h.waitForFile(ctx, tor.Hash, file, settings.WaitTimeout)
	if err != nil {
		h.retryLater(w, "file not on disk yet", err)
		return
	}
	f, err := os.Open(localPath)
	if err != nil {
		h.retryLater(w, "open partial file", err)
		return
	}

	blocked := &blockedRange{}
	pr := &partialReader{
		ctx:        ctx,
		file:       f,
		blocked:    blocked,
		size:       file.Size,
		fileOffset: fileOffset,
		pieceSize:  props.PieceSize,
		hash:       tor.Hash,
		chunkSize:  chunk,
		logger:     h.logger,
		reopen: func() (*os.File, error) {
			freshInfo, err := h.dc.Torrent(ctx, tor.Hash)
			if err != nil {
				return nil, err
			}
			p, err := h.resolver.FilePath(ctx, freshInfo, file)
			if err != nil {
				return nil, err
			}
			return os.Open(p)
		},
		waitFor: func(ctx context.Context, hash string, first, last int) error {
			// Every blocking read — first play and each seek's first read
			// into a missing region — passes through here: ask capable
			// backends (Deluge + Seedstream plugin) to deadline-fetch the
			// awaited window plus readahead, re-hinting periodically while
			// the wait lasts. Reads of pieces already on disk never hint.
			return h.avail.WaitForRangeHint(ctx, hash, first, last, settings.WaitTimeout, prioRefreshInterval, func() bool {
				return h.prio.request(ctx, hash, first, last+readaheadPieces(props.PieceSize))
			})
		},
	}
	defer pr.Close()

	// Heartbeat: while the partial-file serve runs, log the torrent's
	// overall download progress and whether the pieces the reader is
	// blocked on are on disk yet, so we can see whether the torrent is
	// actually pulling bytes during a stall or feeding some other file.
	// Stops when the serve returns (stopBeat) or the client disconnects
	// (ctx).
	stopBeat := make(chan struct{})
	go h.heartbeat(ctx, stopBeat, tor.Hash, file.Index, blocked)

	serveStart := time.Now()
	http.ServeContent(w, r, path.Base(file.Name), time.Time{}, pr)
	close(stopBeat)
	// ServeContent swallows read errors (headers are already sent), so this
	// is the only place we learn how a partial-file stream actually ended:
	// bytes delivered, how long it ran, and whether the client hung up.
	h.logger.Debug("stream: serve finished",
		"hash", tor.Hash, "bytesDelivered", pr.delivered, "elapsed", time.Since(serveStart).Round(time.Millisecond),
		"ctxErr", ctx.Err())
}

// readyGrace is how long a play request may wait for the file's head
// and tail pieces before the "still downloading" placeholder is served
// instead. Long enough for a healthy sequential swarm to deliver both
// (first/last-piece priority makes them the first requests), short
// enough that the viewer sees the placeholder before the player's own
// request timeout turns the wait into a hard error.
const readyGrace = 12 * time.Second

// readyGraceExtension / readyGraceMax extend the gate while the awaited
// pieces are already in flight.
const (
	readyGraceExtension = 6 * time.Second
	readyGraceMax       = 30 * time.Second
)

// waitStreamReady reports whether playback can start: the head pieces
// on disk, and the tail pieces (MKV cues) on disk *or at least in
// flight*. It waits up to grace for that to happen (grace 0 = check the
// current state only). A tail a peer is already sending will land while
// the head plays out — the player's tail probe just blocks inside
// partialReader as ordinary buffering — so holding headers for it
// trades a play that works for the placeholder. A tail that stays plain
// missing keeps the gate closed: nobody is sending it, and serving
// headers then would leave the player spinning on a probe that cannot
// complete.
//
// When grace runs out the wait is extended in readyGraceExtension slices
// — up to readyGraceMax in total — for as long as every awaited range is
// at least in flight. A piece libtorrent has already requested from a
// peer is one the swarm is willing to serve, so the only thing between
// the viewer and playback is time; bailing out then trades a play that
// was about to work for a "come back later" clip. That is the common
// shape of a cold add, where the gate starts counting before a swarm
// exists. A range that is still plain missing gets no extension: nobody
// is sending it, and waiting longer cannot change that.
func (h *Handler) waitStreamReady(ctx context.Context, hash string, headFirst, headLast, tailFirst, tailLast int, grace time.Duration) bool {
	if grace <= 0 {
		haveHead, _ := h.avail.HaveRange(ctx, hash, headFirst, headLast)
		if !haveHead {
			return false
		}
		tailServed, _ := h.avail.RangeAtLeast(ctx, hash, tailFirst, tailLast, downloader.PieceDownloading)
		return tailServed
	}
	spent := time.Duration(0)
	for slice := grace; ; slice = readyGraceExtension {
		if h.waitHeadAndTail(ctx, hash, headFirst, headLast, tailFirst, tailLast, slice) {
			return true
		}
		spent += slice
		if spent+readyGraceExtension > readyGraceMax {
			return false
		}
		head, tail, _, err := h.awaitedStates(ctx, hash, headFirst, headLast, tailFirst, tailLast)
		if err != nil || head == downloader.PieceMissing || tail == downloader.PieceMissing {
			return false
		}
		h.logger.Debug("stream: extending playability grace, awaited pieces in flight",
			"hash", hash, "waited", spent, "headState", pieceStateName(head), "tailState", pieceStateName(tail))
	}
}

// awaitedStates reports the worst piece state across the head range and
// across the tail range, plus a bitfield summary of the whole torrent.
func (h *Handler) awaitedStates(ctx context.Context, hash string, headFirst, headLast, tailFirst, tailLast int) (head, tail downloader.PieceState, sum Summary, err error) {
	sum, err = h.avail.Summary(ctx, hash, headFirst, headLast)
	if err != nil {
		return downloader.PieceMissing, downloader.PieceMissing, Summary{}, err
	}
	tailSum, err := h.avail.Summary(ctx, hash, tailFirst, tailLast)
	if err != nil {
		return downloader.PieceMissing, downloader.PieceMissing, Summary{}, err
	}
	return sum.HeadState, tailSum.HeadState, sum, nil
}

// waitHeadAndTail waits up to grace for the head to land on disk and
// the tail to be at least in flight.
func (h *Handler) waitHeadAndTail(ctx context.Context, hash string, headFirst, headLast, tailFirst, tailLast int, grace time.Duration) bool {
	// Deadline-fetch the head and tail on capable backends when they are
	// missing: first/last-piece priority covers them eventually, but an
	// explicit deadline makes the MKV cues arrive deterministically fast.
	// Both ranges are waited on (and hinted) concurrently: waiting for
	// the head first would leave the tail unhinted — and with no time
	// left in the grace window — whenever the head is slow to arrive.
	ranges := [2]struct {
		first, last int
		min         downloader.PieceState
	}{
		{headFirst, headLast, downloader.PieceHave},
		{tailFirst, tailLast, downloader.PieceDownloading},
	}
	var wg sync.WaitGroup
	var errs [2]error
	for i, rng := range ranges {
		wg.Add(1)
		go func(i, first, last int, min downloader.PieceState) {
			defer wg.Done()
			errs[i] = h.avail.WaitForRangeAtLeast(ctx, hash, first, last, min, grace, prioRefreshInterval, func() bool {
				return h.prio.request(ctx, hash, first, last)
			})
		}(i, rng.first, rng.last, rng.min)
	}
	wg.Wait()
	return errs[0] == nil && errs[1] == nil
}

// waitForFile polls for the torrent's file to appear on disk, up to
// timeout. qBittorrent creates the file shortly after a torrent starts;
// until then FilePath returns not-found. Torrent info is re-fetched each
// poll so a content-path change (temp → final location) is picked up.
func (h *Handler) waitForFile(ctx context.Context, hash string, file downloader.FileInfo, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if info, err := h.dc.Torrent(ctx, hash); err == nil {
			if p, ferr := h.resolver.FilePath(ctx, info, file); ferr == nil {
				return p, nil
			} else {
				lastErr = ferr
			}
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return "", lastErr
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// headStallBeats is how many consecutive heartbeats the awaited pieces
// may stay off disk before the piece picker is kicked.
const headStallBeats = 3

// heartbeatInterval is the beat period. A var so tests can shrink it.
var heartbeatInterval = 3 * time.Second

// heartbeat logs the torrent's download progress and a piece-bitfield
// summary every few seconds until the serve finishes (stop closed) or
// the client disconnects (ctx). If the pieces the reader is currently
// blocked on stay off disk for headStallBeats consecutive beats while
// the rest of the torrent downloads, it kicks libtorrent's piece picker
// (sequential + first/last toggled off and back on) so the stuck piece
// is re-requested from healthy peers.
//
// The watched range comes from the reader rather than being fixed at
// the file's head: past the first read the head is permanently on disk,
// so a head-gated detector reports "healthy" through any mid-file
// starvation — which is exactly when a season pack strands a viewer,
// the played file frozen while the swarm feeds pieces elsewhere.
func (h *Handler) heartbeat(ctx context.Context, stop <-chan struct{}, hash string, fileIndex int, blocked *blockedRange) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	stalledBeats := 0
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			var progress, fileProgress float64
			if info, err := h.dc.Torrent(ctx, hash); err == nil {
				progress = info.Progress
			}
			if files, err := h.dc.Files(ctx, hash); err == nil {
				for _, f := range files {
					if f.Index == fileIndex {
						fileProgress = f.Progress
						break
					}
				}
			}
			first, last, waiting := blocked.get()
			sum, sumErr := h.avail.Summary(ctx, hash, first, last)
			// Only a blocked reader can be starving: with the read
			// flowing there is nothing to chase and nothing to kick.
			stalled := waiting && (sumErr != nil || sum.HeadState != downloader.PieceHave)
			h.logger.Debug("stream: download heartbeat",
				"hash", hash, "progress", progress, "fileProgress", fileProgress,
				"blockedOn", [2]int{first, last}, "readerBlocked", waiting, "stalled", stalled,
				"blockedState", pieceStateName(sum.HeadState), "lastState", pieceStateName(sum.LastState),
				"pieces", sum.TotalPieces, "have", sum.Have, "downloading", sum.Downloading,
				"frontier", sum.FirstMissing, "summaryErr", sumErr)

			if !stalled {
				stalledBeats = 0
				continue
			}
			stalledBeats++
			// A piece deadline is a far more surgical unstick than the
			// picker reset below; the kick stays as the fallback for
			// backends without piece prioritization.
			h.prio.request(ctx, hash, first, last)
			if stalledBeats >= headStallBeats && h.svc.KickStreamingPrioThrottled(ctx, hash) {
				h.logger.Warn("stream: reader stalled, kicking piece picker",
					"hash", hash, "blockedOn", [2]int{first, last},
					"blockedState", pieceStateName(sum.HeadState), "frontier", sum.FirstMissing,
					"stalledBeats", stalledBeats)
			}
		}
	}
}

// pieceStateName renders a downloader.PieceState for logs.
func pieceStateName(s downloader.PieceState) string {
	switch s {
	case downloader.PieceHave:
		return "have"
	case downloader.PieceDownloading:
		return "downloading"
	default:
		return "missing"
	}
}

func (h *Handler) retryLater(w http.ResponseWriter, msg string, err error) {
	h.logger.Debug("stream retry-later", "reason", msg, "error", err)
	w.Header().Set("Retry-After", "10")
	http.Error(w, "content not available yet, retry shortly", http.StatusServiceUnavailable)
}

func (h *Handler) internalError(w http.ResponseWriter, msg string, err error) {
	h.logger.Error("stream "+msg, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// checkAbandoned runs after the last active streaming session for a
// torrent ends. If the torrent never reached the configured minimum
// progress and nobody else has started watching it in the meantime, it
// is removed. Called from a goroutine, so it uses its own context
// rather than the (by-then-cancelled) request context.
func (h *Handler) checkAbandoned(tor store.Torrent) {
	threshold := h.settings().MinProgressForCancel
	if threshold <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := h.dc.Torrent(ctx, tor.Hash)
	if err != nil {
		return
	}
	if info.Progress >= threshold {
		return
	}

	// BeginRemoval atomically re-checks that nobody has started
	// watching since the last check and blocks concurrent Begin(hash)
	// calls until this removal attempt finishes, closing the race where
	// a new session could open a file being deleted underneath it.
	done, ok := h.sessions.BeginRemoval(tor.Hash)
	if !ok {
		return
	}
	defer done()

	if err := h.svc.Remove(ctx, tor, store.DeletionEvent{
		Reason:        store.DeleteReasonAbandoned,
		Progress:      info.Progress,
		ProgressLimit: threshold,
	}); err != nil {
		h.logger.Warn("stream: remove abandoned torrent", "hash", tor.Hash, "error", err)
		return
	}
	h.logger.Info("stream: removed abandoned torrent below progress threshold",
		"hash", tor.Hash, "progress", info.Progress, "threshold", threshold)
}
