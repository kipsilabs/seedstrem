package stream

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/playsession"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

const (
	pieceSize = 1024
	fileSize  = 4 * pieceSize
	testToken = "streamtoken00000000000000000000a"
)

type streamEnv struct {
	handler http.Handler
	h       *Handler
	fake    *fake.Server
	avail   *Availability
	content []byte
	dir     string
}

// newStreamEnv creates a torrent with one 4-piece file on disk plus a
// link token for it. Piece states start as given.
func newStreamEnv(t *testing.T, pieceStates []int, fileProgress float64) *streamEnv {
	t.Helper()

	dir := t.TempDir()
	content := make([]byte, fileSize)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.mkv"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	f := fake.New()
	f.Put(&fake.Torrent{
		Hash: testHash, Name: "movie.mkv", State: "downloading",
		SavePath:  "/downloads",
		PieceSize: pieceSize, PieceStates: pieceStates,
		Files: []fake.File{{Name: "movie.mkv", Size: fileSize, Priority: 1, Progress: fileProgress}},
	})

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.InsertTorrent(ctx, store.Torrent{ID: "STREAMTEST000", Hash: testHash, Phase: store.PhaseSelected, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertLinks(ctx, []store.Link{{Token: testToken, TorrentID: "STREAMTEST000", FileIndex: 0, Path: "movie.mkv", Bytes: fileSize}}); err != nil {
		t.Fatal(err)
	}

	avail := NewAvailability(f)
	resolver := NewResolver(f, func() []config.Mapping {
		return []config.Mapping{{Remote: "/downloads", Local: dir}}
	})
	svc := torrents.New(st, f, func() torrents.Settings { return torrents.Settings{} }, nil)
	settings := func() Settings { return Settings{WaitTimeout: 5 * time.Second, ReadChunk: pieceSize} }
	h := NewHandler(st, f, svc, resolver, avail, playsession.New(), settings, nil)

	return &streamEnv{handler: h.Router(), h: h, fake: f, avail: avail, content: content, dir: dir}
}

// fakeClock replaces avail's clock with a mutex-guarded fake (the
// playability gate polls from two goroutines) that advances on every
// poll sleep and invokes onSleep (if non-nil) per sleep. Returns a
// counter of how many sleeps happened.
//
// Caution: the head and tail pollers sleep concurrently but each
// advances this one shared clock, so it runs at up to twice the rate
// real time would, by an amount that depends on how the two goroutines
// interleave. Use it to drive state changes past a threshold (as
// onSleep does), never to assert how long something waited — such an
// assertion measures the scheduler.
func fakeClock(e *streamEnv, onSleep func()) *int {
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	polls := new(int)
	e.avail.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	e.avail.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		now = now.Add(d)
		*polls++
		mu.Unlock()
		if onSleep != nil {
			onSleep()
		}
		return nil
	}
	return polls
}

func (e *streamEnv) get(t *testing.T, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/"+testToken+"/movie.mkv", nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w
}

func TestServeCompletedFile(t *testing.T) {
	e := newStreamEnv(t, []int{2, 2, 2, 2}, 1)

	w := e.get(t, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), e.content) {
		t.Error("full body mismatch")
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestServeRangeOnCompletedFile(t *testing.T) {
	e := newStreamEnv(t, []int{2, 2, 2, 2}, 1)

	w := e.get(t, "bytes=100-299")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), e.content[100:300]) {
		t.Error("range body mismatch")
	}
	if got := w.Header().Get("Content-Range"); got != "bytes 100-299/4096" {
		t.Errorf("Content-Range = %q", got)
	}
}

func TestServeRangeOnPartialFileWithAvailablePieces(t *testing.T) {
	// Head and tail downloaded (playability gate passes); request stays
	// inside the available head pieces.
	e := newStreamEnv(t, []int{2, 2, 0, 2}, 0.5)

	w := e.get(t, "bytes=0-2047")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), e.content[:2048]) {
		t.Error("partial range body mismatch")
	}
}

func TestServeWaitsForPiecesArrivingMidRequest(t *testing.T) {
	// Only piece 0 available; request spans all 4 pieces. Pieces flip
	// to downloaded as the availability poller sleeps.
	e := newStreamEnv(t, []int{2, 0, 0, 0}, 0.25)

	polls := fakeClock(e, func() {
		e.fake.Update(testHash, func(tor *fake.Torrent) {
			// One more piece per poll.
			for i, st := range tor.PieceStates {
				if st != 2 {
					tor.PieceStates[i] = 2
					break
				}
			}
		})
	})

	w := e.get(t, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), e.content) {
		t.Error("body mismatch after in-flight piece arrival")
	}
	if *polls == 0 {
		t.Error("expected the reader to poll for pieces")
	}
}

func TestServePiecesNeverArriveServesPlaceholder(t *testing.T) {
	// No pieces ever arrive: the playability gate (head+tail) times out
	// and the bundled "still downloading" clip is served instead of
	// leaving the player buffering until it errors out.
	e := newStreamEnv(t, []int{0, 0, 0, 0}, 0)
	fakeClock(e, nil)

	w := e.get(t, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (placeholder)", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("content type = %q, want video/mp4", got)
	}
	if want := placeholderFor(0); !bytes.Equal(w.Body.Bytes(), want) {
		t.Errorf("body = %d bytes, want the %d-byte 0%% downloading placeholder", w.Body.Len(), len(want))
	}
}

func TestServeTailMissingServesPlaceholder(t *testing.T) {
	// Head available but the tail (MKV cues) never arrives — the
	// super-seeding scenario. The placeholder must be served rather than
	// a stream the player cannot start.
	e := newStreamEnv(t, []int{2, 2, 2, 0}, 0.75)
	fakeClock(e, nil)

	w := e.get(t, "")
	if want := placeholderFor(0.75); !bytes.Equal(w.Body.Bytes(), want) {
		t.Errorf("body = %d bytes, want the 70%% downloading placeholder", w.Body.Len())
	}
}

func TestServeTailInFlightServesStream(t *testing.T) {
	// Head on disk, tail (MKV cues) requested from a peer but not landed
	// yet — the tail of every cold add's success path. A tail already in
	// flight is one the swarm is willing to serve, so the gate must open
	// and let the player's tail probe block in partialReader as ordinary
	// buffering, instead of burning the whole grace and serving the
	// placeholder.
	e := newStreamEnv(t, []int{2, 2, 2, 1}, 0.75)
	fakeClock(e, nil)

	w := e.get(t, "bytes=0-2047")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body len=%d; want 206 with the real stream", w.Code, w.Body.Len())
	}
	if !bytes.Equal(w.Body.Bytes(), e.content[:2048]) {
		t.Error("body mismatch: want the real stream, not the placeholder")
	}
}

func TestHeadProbeTailInFlightReportsStream(t *testing.T) {
	// The instant HEAD probe (grace 0) must apply the same relaxed gate:
	// head on disk + tail in flight is a playable stream.
	e := newStreamEnv(t, []int{2, 2, 2, 1}, 0.75)

	req := httptest.NewRequest(http.MethodHead, "/"+testToken+"/movie.mkv", nil)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	if got := w.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Errorf("Content-Type = %q, want the real stream's video/x-matroska", got)
	}
}

func TestPlaceholderWaitHintsHeadAndTailTogether(t *testing.T) {
	// Neither head nor tail ever arrives (super-seeding seeder). The
	// playability gate must deadline-hint BOTH ranges from the start of
	// the grace window: waiting for the head first and only then hinting
	// the tail gives the tail piece zero time to be fetched.
	e := newStreamEnv(t, []int{0, 0, 0, 0}, 0)
	e.fake.SetPrioritizeErr(nil) // capable backend
	fakeClock(e, nil)

	w := e.get(t, "")
	if want := placeholderFor(0); !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("body = %d bytes, want the downloading placeholder", w.Body.Len())
	}
	headHint := fmt.Sprintf("prioritizePieces hash=%s first=0 last=0", testHash)
	tailHint := fmt.Sprintf("prioritizePieces hash=%s first=3 last=3", testHash)
	var haveHead, haveTail bool
	for _, c := range e.fake.Calls() {
		switch c {
		case headHint:
			haveHead = true
		case tailHint:
			haveTail = true
		}
	}
	if !haveHead {
		t.Errorf("head range was never prioritized during the gate: %v", e.fake.Calls())
	}
	if !haveTail {
		t.Errorf("tail range was never prioritized during the gate: %v", e.fake.Calls())
	}
}

func TestPlaceholderGraceExtendsWhileAwaitedPiecesInFlight(t *testing.T) {
	// Head and tail are already requested from a peer (downloading), they
	// are just slower than the base grace — the normal case for a cold
	// add, where the swarm has not formed yet when the gate starts
	// counting. Sending the viewer to the placeholder while the pieces
	// they need are in flight throws away a play that was about to work.
	e := newStreamEnv(t, []int{1, 1, 1, 1}, 0)
	start := time.Unix(1000, 0)
	fakeClock(e, func() {
		// Arrive past the base grace but inside the extended budget.
		if e.avail.now().Sub(start) >= readyGrace+3*time.Second {
			e.fake.Update(testHash, func(tor *fake.Torrent) {
				for i := range tor.PieceStates {
					tor.PieceStates[i] = 2
				}
			})
		}
	})

	w := e.get(t, "")
	if !bytes.Equal(w.Body.Bytes(), e.content) {
		t.Errorf("body = %d bytes, want the real stream (placeholder served instead of waiting out in-flight pieces)", w.Body.Len())
	}
}

func TestPlaceholderGraceNotExtendedWhenAwaitedPiecesMissing(t *testing.T) {
	// Nothing is in flight: the swarm is not serving these pieces at all
	// (super-seeding seeder). Waiting longer cannot help, so the grace
	// must not be extended — the viewer sees the placeholder promptly.
	e := newStreamEnv(t, []int{0, 0, 0, 0}, 0)
	var logs bytes.Buffer
	e.h.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fakeClock(e, nil)

	w := e.get(t, "")
	if want := placeholderFor(0); !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("body = %d bytes, want the downloading placeholder", w.Body.Len())
	}
	// Assert on the extension decision itself rather than on elapsed fake
	// time: the gate polls from two goroutines that share one fake clock
	// (see fakeClock), so measured elapsed time reflects how those two
	// interleave, not whether the grace was extended.
	if strings.Contains(logs.String(), "extending playability grace") {
		t.Errorf("gate extended the grace on pieces nobody is sending: %s", logs.String())
	}
}

func TestPlaceholderLogsPieceDiagnosis(t *testing.T) {
	// The gate is the one place that decides a play is not viable, and
	// until now it logged only the piece numbers — leaving "the swarm is
	// slow" and "the hints never reached the picker" indistinguishable
	// after the fact. The decision must record the piece states it saw.
	e := newStreamEnv(t, []int{0, 0, 0, 0}, 0)
	var logs bytes.Buffer
	e.h.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fakeClock(e, nil)

	e.get(t, "")

	line := ""
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "serving downloading placeholder") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no placeholder decision logged: %s", logs.String())
	}
	for _, want := range []string{"headState=missing", "tailState=missing", "frontier=0", "have=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("placeholder log missing %q: %s", want, line)
		}
	}
}

func TestPlaceholderForBuckets(t *testing.T) {
	cases := []struct {
		progress float64
		pct      int
	}{
		{-0.1, 0}, {0, 0}, {0.05, 0}, {0.1, 10}, {0.29, 20}, {0.75, 70}, {0.99, 90}, {1.5, 90},
	}
	for _, tc := range cases {
		want, err := placeholderFS.ReadFile(fmt.Sprintf("assets/downloading_%d.mp4", tc.pct))
		if err != nil {
			t.Fatalf("bucket %d missing from embed: %v", tc.pct, err)
		}
		if got := placeholderFor(tc.progress); !bytes.Equal(got, want) {
			t.Errorf("placeholderFor(%v) picked wrong bucket, want %d%%", tc.progress, tc.pct)
		}
	}
}

func TestServeSeekAheadWaitsOnlyForRequestedPieces(t *testing.T) {
	// Head and tail available (gate passes), middle missing: a request
	// for the file tail must succeed without waiting for middle pieces.
	e := newStreamEnv(t, []int{2, 0, 0, 2}, 0.5)

	w := e.get(t, "bytes=3072-4095")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), e.content[3072:]) {
		t.Error("tail range mismatch")
	}
}

func TestServeSuffixRange(t *testing.T) {
	e := newStreamEnv(t, []int{2, 0, 0, 2}, 0.5)

	w := e.get(t, "bytes=-1024")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), e.content[3072:]) {
		t.Error("suffix range mismatch")
	}
}

func TestServeUnknownToken(t *testing.T) {
	e := newStreamEnv(t, []int{2, 2, 2, 2}, 1)
	req := httptest.NewRequest(http.MethodGet, "/nosuchtoken/movie.mkv", nil)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", w.Code)
	}
}

// newAbandonEnv builds a minimal Handler (no on-disk files/HTTP needed)
// for exercising checkAbandoned directly.
func newAbandonEnv(t *testing.T, progress, minProgressForCancel float64) (*Handler, store.Torrent, *fake.Server) {
	t.Helper()

	f := fake.New()
	f.Put(&fake.Torrent{Hash: testHash, State: "downloading", Progress: progress})

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	tor := store.Torrent{ID: "ABANDONTEST0", Hash: testHash, AddedAt: 1}
	if err := st.InsertTorrent(ctx, tor); err != nil {
		t.Fatal(err)
	}

	svc := torrents.New(st, f, func() torrents.Settings { return torrents.Settings{DeleteFilesOnRemove: true} }, nil)
	sessions := playsession.New()
	settings := func() Settings { return Settings{MinProgressForCancel: minProgressForCancel} }
	h := NewHandler(st, f, svc, NewResolver(f, func() []config.Mapping { return nil }), NewAvailability(f), sessions, settings, nil)

	return h, tor, f
}

func TestCheckAbandonedRemovesLowProgressTorrent(t *testing.T) {
	h, tor, f := newAbandonEnv(t, 0.02, 0.05)

	h.checkAbandoned(tor)

	if f.Get(testHash) != nil {
		t.Error("expected torrent removed from qbittorrent")
	}
	if _, err := h.store.TorrentByID(context.Background(), tor.ID); err == nil {
		t.Error("expected torrent removed from store")
	}
}

func TestCheckAbandonedKeepsTorrentAboveThreshold(t *testing.T) {
	h, tor, f := newAbandonEnv(t, 0.5, 0.05)

	h.checkAbandoned(tor)

	if f.Get(testHash) == nil {
		t.Error("expected torrent to remain: progress above threshold")
	}
}

func TestCheckAbandonedDisabledWhenThresholdZero(t *testing.T) {
	h, tor, f := newAbandonEnv(t, 0, 0)

	h.checkAbandoned(tor)

	if f.Get(testHash) == nil {
		t.Error("expected torrent to remain: cancel-cleanup disabled")
	}
}

func TestCheckAbandonedSkipsWhenSomeoneElseIsWatching(t *testing.T) {
	h, tor, f := newAbandonEnv(t, 0.02, 0.05)
	end := h.sessions.Begin(tor.Hash) // another viewer started watching
	defer end()

	h.checkAbandoned(tor)

	if f.Get(testHash) == nil {
		t.Error("expected torrent to remain: another session is active")
	}
}

// arrivingPieces wires fake time into avail and flips one missing piece
// to downloaded per availability poll, simulating download progress.
func arrivingPieces(e *streamEnv) {
	fakeClock(e, func() {
		e.fake.Update(testHash, func(tor *fake.Torrent) {
			for i, st := range tor.PieceStates {
				if st != 2 {
					tor.PieceStates[i] = 2
					break
				}
			}
		})
	})
}

func TestServeRequestsPiecePrioritization(t *testing.T) {
	// Head and tail available, middle missing. A request that blocks on a
	// missing piece must ask a capable backend to prioritize the awaited
	// pieces (+readahead) before settling in to wait.
	e := newStreamEnv(t, []int{2, 0, 0, 2}, 0.5)
	e.fake.SetPrioritizeErr(nil) // capable backend
	arrivingPieces(e)

	w := e.get(t, "bytes=1024-2047")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	want := fmt.Sprintf("prioritizePieces hash=%s first=1 last=%d", testHash, 1+readaheadPieces(pieceSize))
	found := false
	for _, c := range e.fake.Calls() {
		if c == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no %q in calls: %v", want, e.fake.Calls())
	}
}

func TestServeSkipsPrioritizationForAvailablePieces(t *testing.T) {
	// A request entirely inside already-downloaded pieces must not send
	// any deadline hint: re-deadlining pieces on disk floods the daemon's
	// request queue (observed as libtorrent's
	// outstanding_request_limit_reached warning storm).
	e := newStreamEnv(t, []int{2, 2, 0, 2}, 0.5)
	e.fake.SetPrioritizeErr(nil) // capable backend — a hint would go through

	w := e.get(t, "bytes=0-2047")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	for _, c := range e.fake.Calls() {
		if strings.HasPrefix(c, "prioritizePieces ") {
			t.Errorf("unexpected prioritization of available pieces: %v", c)
		}
	}
}

func TestServeSilencesPrioritizationWhenUnsupported(t *testing.T) {
	// Default fake behavior is ErrNotSupported (like qBittorrent): after
	// the first attempt the prioritizer must back off — one call total.
	e := newStreamEnv(t, []int{2, 0, 0, 2}, 0.5)
	arrivingPieces(e)

	if w := e.get(t, "bytes=1024-2047"); w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", w.Code)
	}
	if w := e.get(t, "bytes=2048-3071"); w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d", w.Code)
	}
	count := 0
	for _, c := range e.fake.Calls() {
		if strings.HasPrefix(c, "prioritizePieces ") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("prioritizePieces called %d times, want 1 (backoff after ErrNotSupported)", count)
	}
}

// TestHeartbeatWatchesRangeReaderIsBlockedOn covers the mid-playback
// stall: once playback has moved past the file's head, the head pieces
// are on disk forever, so a detector gated on them can never fire again
// no matter how long the reader starves. The heartbeat must follow the
// reader.
func TestHeartbeatWatchesRangeReaderIsBlockedOn(t *testing.T) {
	// Head piece (0) on disk, playback blocked at piece 2.
	e := newStreamEnv(t, []int{2, 2, 0, 0}, 0.5)
	e.fake.SetPrioritizeErr(nil) // capable backend, like Deluge + plugin

	prev := heartbeatInterval
	heartbeatInterval = time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	blocked := &blockedRange{}
	blocked.enter(2, 2)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.h.heartbeat(context.Background(), stop, testHash, 0, blocked)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(strings.Join(e.fake.Calls(), "\n"), "prioritizePieces hash="+testHash+" first=2 last=2") {
			break
		}
		select {
		case <-deadline:
			close(stop)
			<-done
			t.Fatalf("stall on pieces [2,2] never detected; calls: %v", e.fake.Calls())
		case <-time.After(time.Millisecond):
		}
	}
	close(stop)
	<-done
}

// A reader that is not blocked is not stalled: no pieces to chase, so
// the heartbeat must stay quiet rather than deadline-fetch at random.
func TestHeartbeatQuietWhenReaderNotBlocked(t *testing.T) {
	e := newStreamEnv(t, []int{2, 2, 0, 0}, 0.5)
	e.fake.SetPrioritizeErr(nil)

	prev := heartbeatInterval
	heartbeatInterval = time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.h.heartbeat(context.Background(), stop, testHash, 0, &blockedRange{})
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done

	for _, c := range e.fake.Calls() {
		if strings.Contains(c, "prioritizePieces") {
			t.Errorf("hinted while no reader was blocked: %q", c)
		}
	}
}
