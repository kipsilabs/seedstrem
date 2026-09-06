package torrents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// 40-char hex infohash → magnet. metainfo.FromMagnet and the fake both
// lowercase it, so the store mapping and qbittorrent lookups align.
const testHash = "0123456789abcdef0123456789abcdef01234567"

func testMagnet(name string) string {
	return "magnet:?xt=urn:btih:" + testHash + "&dn=" + name
}

func newService(t *testing.T) (*Service, *fake.Server, *store.Store) {
	t.Helper()
	fakeDC := fake.New()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := New(db, fakeDC, func() Settings {
		return Settings{MetadataTimeout: 2 * time.Second}
	}, nil)
	return svc, fakeDC, db
}

func TestResolveIdempotent(t *testing.T) {
	svc, fakeDC, db := newService(t)
	ctx := context.Background()

	// Pre-seed the fake so metadata is immediately available with a
	// season pack; add() won't overwrite an existing hash.
	fakeDC.Put(&fake.Torrent{
		Hash:  testHash,
		State: downloader.StatePaused,
		Files: []fake.File{
			{Name: "Show.S01E04.1080p.mkv", Size: 500 << 20},
			{Name: "Show.S01E05.1080p.mkv", Size: 480 << 20},
		},
	})

	sel := Selector{IsSeries: true, Season: 1, Episode: 5}
	link1, err := svc.Resolve(ctx, testMagnet("Show.S01"), nil, sel)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if link1.FileIndex != 1 {
		t.Errorf("picked file index = %d, want 1", link1.FileIndex)
	}

	link2, err := svc.Resolve(ctx, testMagnet("Show.S01"), nil, sel)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if link2.Token != link1.Token {
		t.Errorf("resolve not idempotent: tokens %q vs %q", link1.Token, link2.Token)
	}

	// Torrent added exactly once, running (so qBittorrent fetches
	// metadata), sequential + first/last prio.
	var addCalls int
	for _, c := range fakeDC.Calls() {
		if strings.HasPrefix(c, "add magnet=") {
			addCalls++
			for _, want := range []string{"stopped=false", "seq=true", "flp=true"} {
				if !strings.Contains(c, want) {
					t.Errorf("add call %q missing %q", c, want)
				}
			}
		}
	}
	if addCalls != 1 {
		t.Errorf("magnet added %d times, want 1", addCalls)
	}

	// Phase advanced to selected and the link persists.
	tor, err := db.TorrentByHash(ctx, testHash)
	if err != nil {
		t.Fatalf("torrent by hash: %v", err)
	}
	if tor.Phase != store.PhaseSelected {
		t.Errorf("phase = %q, want %q", tor.Phase, store.PhaseSelected)
	}
	if _, err := db.LinkByToken(ctx, link1.Token); err != nil {
		t.Errorf("link not found by token: %v", err)
	}
}

// Changing file priorities makes qBittorrent recompute piece priorities,
// which on several versions silently drops the first/last-piece boost even
// though the torrent's flag stays on — and the add-time flags may never
// stick at all. SelectAndLink must read back the actual state and leave
// sequential download and the first/last boost enabled, after the
// SetFilePriority calls, so the file's tail piece (MKV index) is fetched
// up front instead of in sequential order.
func TestSelectAndLinkReassertsFirstLastPiecePrio(t *testing.T) {
	cases := []struct {
		name        string
		flPiecePrio bool
		seqDl       bool
	}{
		{"flags initially on", true, true},
		{"flags initially off", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fakeDC, _ := newService(t)
			ctx := context.Background()

			fakeDC.Put(&fake.Torrent{
				Hash:               testHash,
				State:              downloader.StatePaused,
				SequentialDownload: tc.seqDl,
				FirstLastPiecePrio: tc.flPiecePrio,
				Files: []fake.File{
					{Name: "Movie.2026.1080p.mkv", Size: 8 << 30},
				},
			})

			if _, err := svc.Resolve(ctx, testMagnet("Movie"), nil, Selector{}); err != nil {
				t.Fatalf("resolve: %v", err)
			}

			var lastFilePrio, reasserts []int
			for i, c := range fakeDC.Calls() {
				if strings.HasPrefix(c, "filePrio ") {
					lastFilePrio = append(lastFilePrio, i)
				}
				if strings.HasPrefix(c, "setFirstLastPiecePrio ") {
					reasserts = append(reasserts, i)
				}
			}
			if len(reasserts) == 0 {
				t.Fatalf("setFirstLastPiecePrio never called: calls=%v", fakeDC.Calls())
			}
			if len(lastFilePrio) == 0 || reasserts[0] < lastFilePrio[len(lastFilePrio)-1] {
				t.Errorf("re-assert must happen after the file-priority rewrite: calls=%v", fakeDC.Calls())
			}
			tor := fakeDC.Get(testHash)
			if !tor.FirstLastPiecePrio {
				t.Error("first/last piece priority must end enabled")
			}
			if !tor.SequentialDownload {
				t.Error("sequential download must end enabled")
			}
		})
	}
}

// A repeat play of an already-linked, still-downloading file must
// re-assert the streaming flags (qBittorrent can drop the boost at any
// point mid-download), but back-to-back resolves within the throttle
// window must not hammer qBittorrent with toggle calls.
func TestRepeatResolveReassertsStreamingPrio(t *testing.T) {
	svc, fakeDC, _ := newService(t)
	ctx := context.Background()

	clock := int64(1_000_000)
	svc.now = func() int64 { return clock }

	fakeDC.Put(&fake.Torrent{
		Hash:  testHash,
		State: downloader.StatePaused,
		Files: []fake.File{
			{Name: "Movie.2026.1080p.mkv", Size: 8 << 30},
		},
	})

	if _, err := svc.Resolve(ctx, testMagnet("Movie"), nil, Selector{}); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if tor := fakeDC.Get(testHash); !tor.FirstLastPiecePrio {
		t.Fatal("first resolve must leave first/last piece priority enabled")
	}

	// Simulate qBittorrent dropping the boost mid-download.
	fakeDC.Update(testHash, func(tor *fake.Torrent) { tor.FirstLastPiecePrio = false })

	// Within the throttle window: no re-assert, flag stays dropped.
	if _, err := svc.Resolve(ctx, testMagnet("Movie"), nil, Selector{}); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if tor := fakeDC.Get(testHash); tor.FirstLastPiecePrio {
		t.Error("resolve inside throttle window must not toggle")
	}

	// Past the throttle window: the repeat play restores the boost.
	clock += streamingPrioReassertInterval
	if _, err := svc.Resolve(ctx, testMagnet("Movie"), nil, Selector{}); err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if tor := fakeDC.Get(testHash); !tor.FirstLastPiecePrio {
		t.Error("repeat resolve must re-assert first/last piece priority")
	}
}

// KickStreamingPrio must force a full piece-picker reset — both flags
// toggled off and back on even when already enabled — end with both
// flags ON, and be throttled on back-to-back calls.
func TestKickStreamingPrio(t *testing.T) {
	svc, fakeDC, _ := newService(t)
	ctx := context.Background()

	clock := int64(1_000_000)
	svc.now = func() int64 { return clock }

	fakeDC.Put(&fake.Torrent{
		Hash:               testHash,
		State:              downloader.StateDownloading,
		SequentialDownload: true,
		FirstLastPiecePrio: true,
		Files:              []fake.File{{Name: "Movie.mkv", Size: 8 << 30}},
	})

	if !svc.KickStreamingPrioThrottled(ctx, testHash) {
		t.Fatal("first kick must fire")
	}
	var seqSets, flSets int
	for _, c := range fakeDC.Calls() {
		if strings.HasPrefix(c, "setSequentialDownload ") {
			seqSets++
		}
		if strings.HasPrefix(c, "setFirstLastPiecePrio ") {
			flSets++
		}
	}
	if seqSets != 2 || flSets != 2 {
		t.Errorf("sets seq=%d fl=%d, want 2 and 2 (off+on picker reset): calls=%v",
			seqSets, flSets, fakeDC.Calls())
	}
	tor := fakeDC.Get(testHash)
	if !tor.SequentialDownload || !tor.FirstLastPiecePrio {
		t.Errorf("flags must end enabled: seq=%v fl=%v", tor.SequentialDownload, tor.FirstLastPiecePrio)
	}

	if svc.KickStreamingPrioThrottled(ctx, testHash) {
		t.Error("kick inside throttle window must not fire")
	}
	clock += streamingPrioReassertInterval
	if !svc.KickStreamingPrioThrottled(ctx, testHash) {
		t.Error("kick past throttle window must fire again")
	}
}

// EnsureStreamingPrio is a no-op once the download is complete: toggling
// flags on a finished torrent is pointless qBittorrent churn.
func TestEnsureStreamingPrioSkipsCompleted(t *testing.T) {
	svc, fakeDC, _ := newService(t)
	ctx := context.Background()

	fakeDC.Put(&fake.Torrent{
		Hash:     testHash,
		State:    downloader.StateSeeding,
		Progress: 1,
		Files:    []fake.File{{Name: "Movie.mkv", Size: 8 << 30, Progress: 1}},
	})

	if err := svc.EnsureStreamingPrio(ctx, testHash); err != nil {
		t.Fatalf("ensure streaming prio: %v", err)
	}
	for _, c := range fakeDC.Calls() {
		if strings.HasPrefix(c, "toggle") {
			t.Errorf("completed torrent must not be toggled: %v", fakeDC.Calls())
		}
	}
}

func TestEnsureAddedPersistsContentIdentity(t *testing.T) {
	svc, _, db := newService(t)
	ctx := context.Background()

	sel := Selector{IsSeries: true, Season: 1, Episode: 5, Source: "tt", ContentRef: "tt0944947"}
	tor, err := svc.EnsureAdded(ctx, testMagnet("Show.S01E05"), nil, sel)
	if err != nil {
		t.Fatalf("ensure added: %v", err)
	}

	got, err := db.TorrentByID(ctx, tor.ID)
	if err != nil {
		t.Fatalf("by id: %v", err)
	}
	if got.ContentSource != "tt" || got.ContentRef != "tt0944947" || got.Season != 1 || got.Episode != 5 {
		t.Errorf("content identity not persisted: %+v", got)
	}

	// OwnedForContent finds it for exactly this identity, and not for another.
	if owned := svc.OwnedForContent(ctx, "tt", "tt0944947", 1, 5); len(owned) != 1 || owned[0].ID != tor.ID {
		t.Errorf("OwnedForContent = %+v, want the added torrent", owned)
	}
	if owned := svc.OwnedForContent(ctx, "tt", "tt0944947", 1, 6); len(owned) != 0 {
		t.Errorf("OwnedForContent for other episode = %+v, want none", owned)
	}
}

func TestEnsureAddedBackfillsContentIdentity(t *testing.T) {
	svc, _, db := newService(t)
	ctx := context.Background()

	// First add without a content identity (e.g. a pre-migration play URL).
	tor, err := svc.EnsureAdded(ctx, testMagnet("Movie"), nil, Selector{})
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Re-add (idempotent on hash) now carrying the identity — it backfills.
	got, err := svc.EnsureAdded(ctx, testMagnet("Movie"), nil, Selector{Source: "tt", ContentRef: "tt1375666"})
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if got.ID != tor.ID {
		t.Fatalf("re-add created a new row: %q vs %q", got.ID, tor.ID)
	}
	if got.ContentRef != "tt1375666" {
		t.Errorf("returned torrent not backfilled: %+v", got)
	}
	persisted, _ := db.TorrentByID(ctx, tor.ID)
	if persisted.ContentSource != "tt" || persisted.ContentRef != "tt1375666" {
		t.Errorf("content identity not backfilled in store: %+v", persisted)
	}
}

func TestRemove(t *testing.T) {
	svc, fakeDC, db := newService(t)
	ctx := context.Background()

	fakeDC.Put(&fake.Torrent{Hash: testHash, State: downloader.StateSeeding})
	tor, err := svc.EnsureAdded(ctx, testMagnet("Show"), nil, Selector{})
	if err != nil {
		t.Fatalf("ensure added: %v", err)
	}

	if err := svc.Remove(ctx, tor, store.DeletionEvent{Reason: store.DeleteReasonManual}); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if fakeDC.Get(testHash) != nil {
		t.Error("torrent still present in qbittorrent after remove")
	}
	if _, err := db.TorrentByID(ctx, tor.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("torrent by id error = %v, want ErrNotFound", err)
	}
}

func TestRemoveMissingFromqBittorrentIsNotAnError(t *testing.T) {
	svc, fakeDC, db := newService(t)
	ctx := context.Background()

	fakeDC.Put(&fake.Torrent{Hash: testHash, State: downloader.StateSeeding})
	tor, err := svc.EnsureAdded(ctx, testMagnet("Show"), nil, Selector{})
	if err != nil {
		t.Fatalf("ensure added: %v", err)
	}
	fakeDC.Remove(testHash) // simulate it having vanished from qBittorrent already

	if err := svc.Remove(ctx, tor, store.DeletionEvent{Reason: store.DeleteReasonManual}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := db.TorrentByID(ctx, tor.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("torrent by id error = %v, want ErrNotFound", err)
	}
}

func TestEnsureAddedUsesTorrentFileWhenPresent(t *testing.T) {
	svc, fakeDC, _ := newService(t)
	ctx := context.Background()

	// A minimal valid .torrent whose infohash matches testMagnet's hash
	// is not required here: EnsureAdded keys the store record off the
	// magnet's hash, while the fake keys off the .torrent's own hash. We
	// only assert which client method was used, so any valid .torrent
	// works.
	torrentFile := []byte("d4:infod6:lengthi1e4:name4:test12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")

	if _, err := svc.EnsureAdded(ctx, testMagnet("Show"), torrentFile, Selector{}); err != nil {
		t.Fatalf("ensure added: %v", err)
	}

	var fileAdds, magnetAdds int
	for _, c := range fakeDC.Calls() {
		if strings.HasPrefix(c, "add torrentfile=") {
			fileAdds++
			if !strings.Contains(c, "stopped=false") {
				t.Errorf("torrent-file add should be running: %q", c)
			}
		}
		if strings.HasPrefix(c, "add magnet=") {
			magnetAdds++
		}
	}
	if fileAdds != 1 || magnetAdds != 0 {
		t.Errorf("want 1 torrent-file add and 0 magnet adds, got file=%d magnet=%d", fileAdds, magnetAdds)
	}
}

func TestLiveProgress(t *testing.T) {
	svc, fakeDC, _ := newService(t)
	ctx := context.Background()
	fakeDC.Put(&fake.Torrent{Hash: testHash, State: downloader.StateDownloading, Progress: 0.5})

	got := svc.LiveProgress(ctx, []string{testHash, "ffffffffffffffffffffffffffffffffffffffff"})
	if got[testHash] != 0.5 {
		t.Errorf("progress[%s] = %v, want 0.5", testHash, got[testHash])
	}
	if _, ok := got["ffffffffffffffffffffffffffffffffffffffff"]; ok {
		t.Error("unknown hash should be absent from progress map")
	}

	// Empty input and nil receiver are safe no-ops.
	if len(svc.LiveProgress(ctx, nil)) != 0 {
		t.Error("empty hashes should yield empty map")
	}
	var nilSvc *Service
	if len(nilSvc.LiveProgress(ctx, []string{testHash})) != 0 {
		t.Error("nil service should yield empty map, not panic")
	}
}

func TestWaitForMetadataTimeout(t *testing.T) {
	svc, fakeDC, _ := newService(t)
	// Torrent exists but never resolves files.
	fakeDC.Put(&fake.Torrent{Hash: testHash, State: downloader.StateDownloading})

	// Speed up the poll loop.
	svc.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

	_, err := svc.WaitForMetadata(context.Background(), testHash, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
