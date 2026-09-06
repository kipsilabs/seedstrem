package cleanup

import (
	"context"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/playsession"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

const testHash = "0123456789abcdef0123456789abcdef01234567"

func newCleanup(t *testing.T, seedTime time.Duration) (*Cleanup, *fake.Server, *store.Store, *playsession.Sessions) {
	t.Helper()
	fakeDC := fake.New()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := torrents.New(db, fakeDC, func() torrents.Settings {
		return torrents.Settings{DeleteFilesOnRemove: true}
	}, nil)
	sessions := playsession.New()

	c := New(db, fakeDC, svc, sessions, func() Settings {
		return Settings{SeedTime: seedTime}
	}, nil, time.Hour)

	return c, fakeDC, db, sessions
}

func TestSweepRemovesTorrentPastSeedTime(t *testing.T) {
	c, fakeDC, db, _ := newCleanup(t, 24*time.Hour)
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: downloader.StateSeeding,
		Progress: 1, SeedingTime: 48 * time.Hour,
	})

	if err := c.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(testHash) != nil {
		t.Error("expected torrent removed from qbittorrent")
	}
	if _, err := db.TorrentByID(ctx, "T1"); err == nil {
		t.Error("expected torrent removed from store")
	}
}

func TestSweepKeepsTorrentUnderSeedTime(t *testing.T) {
	c, fakeDC, db, _ := newCleanup(t, 24*time.Hour)
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: downloader.StateSeeding,
		Progress: 1, SeedingTime: 1 * time.Hour,
	})

	if err := c.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(testHash) == nil {
		t.Error("expected torrent to remain in qbittorrent")
	}
	if _, err := db.TorrentByID(ctx, "T1"); err != nil {
		t.Errorf("expected torrent to remain in store, got %v", err)
	}
}

func TestSweepKeepsIncompleteTorrent(t *testing.T) {
	c, fakeDC, db, _ := newCleanup(t, 24*time.Hour)
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// Still downloading, but somehow racked up a lot of "seeding" time —
	// should never happen in practice, but progress < 1 must still block
	// removal.
	fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: downloader.StateDownloading,
		Progress: 0.5, SeedingTime: 48 * time.Hour,
	})

	if err := c.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(testHash) == nil {
		t.Error("expected incomplete torrent to remain")
	}
}

func TestSweepSkipsTorrentBeingWatched(t *testing.T) {
	c, fakeDC, db, sessions := newCleanup(t, 24*time.Hour)
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: downloader.StateSeeding,
		Progress: 1, SeedingTime: 48 * time.Hour,
	})

	end := sessions.Begin(testHash)
	defer end()

	if err := c.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(testHash) == nil {
		t.Error("expected torrent past seed time to remain while actively being watched")
	}
	if _, err := db.TorrentByID(ctx, "T1"); err != nil {
		t.Errorf("expected torrent to remain in store, got %v", err)
	}
}

func TestSweepDisabledWhenSeedTimeZero(t *testing.T) {
	c, fakeDC, db, _ := newCleanup(t, 0)
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: downloader.StateSeeding,
		Progress: 1, SeedingTime: 1000 * time.Hour,
	})

	if err := c.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(testHash) == nil {
		t.Error("expected torrent to remain when seed time cleanup disabled")
	}
}

// selectRemovals is the pure decision core; these table tests cover the
// ratio/seed-time eligibility matrix and the delete-order policies without
// the store/download-client plumbing.
func liveOf(infos ...downloader.TorrentInfo) map[string]downloader.TorrentInfo {
	m := make(map[string]downloader.TorrentInfo, len(infos))
	for _, i := range infos {
		m[i.Hash] = i
	}
	return m
}

func TestSelectRemovalsEligibility(t *testing.T) {
	const h = "aa"
	tor := []store.Torrent{{ID: "T1", Hash: h, AddedAt: 1}}
	for _, tc := range []struct {
		name    string
		s       Settings
		info    downloader.TorrentInfo
		wantRem bool
	}{
		{"seed-time met", Settings{SeedTime: 24 * time.Hour}, downloader.TorrentInfo{Hash: h, Progress: 1, SeedingTime: 48 * time.Hour}, true},
		{"seed-time not met", Settings{SeedTime: 24 * time.Hour}, downloader.TorrentInfo{Hash: h, Progress: 1, SeedingTime: time.Hour}, false},
		{"ratio met", Settings{TargetRatio: 1.0}, downloader.TorrentInfo{Hash: h, Progress: 1, Ratio: 1.2}, true},
		{"ratio not met", Settings{TargetRatio: 1.0}, downloader.TorrentInfo{Hash: h, Progress: 1, Ratio: 0.5}, false},
		{"ratio OR time (ratio only)", Settings{SeedTime: 100 * time.Hour, TargetRatio: 1.0}, downloader.TorrentInfo{Hash: h, Progress: 1, SeedingTime: time.Hour, Ratio: 1.5}, true},
		{"ratio OR time (time only)", Settings{SeedTime: 24 * time.Hour, TargetRatio: 2.0}, downloader.TorrentInfo{Hash: h, Progress: 1, SeedingTime: 48 * time.Hour, Ratio: 0.1}, true},
		{"neither met", Settings{SeedTime: 24 * time.Hour, TargetRatio: 2.0}, downloader.TorrentInfo{Hash: h, Progress: 1, SeedingTime: time.Hour, Ratio: 0.1}, false},
		{"incomplete blocks removal", Settings{TargetRatio: 1.0}, downloader.TorrentInfo{Hash: h, Progress: 0.5, Ratio: 5}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectRemovals(tor, liveOf(tc.info), tc.s)
			if (len(got) == 1) != tc.wantRem {
				t.Errorf("selectRemovals returned %d torrents, wantRemoval=%v", len(got), tc.wantRem)
			}
		})
	}
}

func TestSelectRemovalsOrdering(t *testing.T) {
	// Add-time order (a,c,b) deliberately differs from upload order (b,c,a)
	// so the two policies produce distinguishable results.
	stored := []store.Torrent{
		{ID: "a", Hash: "a", AddedAt: 100},
		{ID: "b", Hash: "b", AddedAt: 300},
		{ID: "c", Hash: "c", AddedAt: 200},
	}
	live := liveOf(
		downloader.TorrentInfo{Hash: "a", Progress: 1, Ratio: 2, Uploaded: 500},
		downloader.TorrentInfo{Hash: "b", Progress: 1, Ratio: 2, Uploaded: 10},
		downloader.TorrentInfo{Hash: "c", Progress: 1, Ratio: 2, Uploaded: 50},
	)

	ids := func(ts []store.Torrent) []string {
		out := make([]string, len(ts))
		for i, tr := range ts {
			out[i] = tr.ID
		}
		return out
	}

	oldest := ids(selectRemovals(stored, live, Settings{TargetRatio: 1, DeletePolicy: config.DeletePolicyOldestFirst}))
	if got, want := oldest, []string{"a", "c", "b"}; !equalStrings(got, want) {
		t.Errorf("oldest_first order = %v, want %v", got, want)
	}

	lowest := ids(selectRemovals(stored, live, Settings{TargetRatio: 1, DeletePolicy: config.DeletePolicyLowestUpload}))
	if got, want := lowest, []string{"b", "c", "a"}; !equalStrings(got, want) {
		t.Errorf("lowest_upload order = %v, want %v", got, want)
	}

	// Empty policy falls back to oldest-first.
	def := ids(selectRemovals(stored, live, Settings{TargetRatio: 1}))
	if got, want := def, []string{"a", "c", "b"}; !equalStrings(got, want) {
		t.Errorf("default order = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
