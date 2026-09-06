package cleanup

import (
	"context"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/playsession"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

func TestEffectiveSeedTime(t *testing.T) {
	s := Settings{
		SeedTime: 72 * time.Hour,
		IndexerSeedTimes: map[string]time.Duration{
			"TorrentLeech": 240 * time.Hour,
			"  Nyaa  ":     12 * time.Hour,
			"KeepForever":  0,
		},
	}
	for _, tc := range []struct {
		name    string
		indexer string
		want    time.Duration
	}{
		{"exact match", "TorrentLeech", 240 * time.Hour},
		{"case-insensitive match", "torrentleech", 240 * time.Hour},
		{"whitespace trimmed on both sides", " nyaa ", 12 * time.Hour},
		{"unknown indexer falls back to global", "SomeOtherTracker", 72 * time.Hour},
		{"empty indexer falls back to global", "", 72 * time.Hour},
		{"zero override disables removal for that indexer", "KeepForever", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.EffectiveSeedTime(tc.indexer); got != tc.want {
				t.Errorf("EffectiveSeedTime(%q) = %v, want %v", tc.indexer, got, tc.want)
			}
		})
	}
}

// A nil override map must behave exactly like the pre-feature config.
func TestEffectiveSeedTimeWithoutOverrides(t *testing.T) {
	s := Settings{SeedTime: 72 * time.Hour}
	if got := s.EffectiveSeedTime("TorrentLeech"); got != 72*time.Hour {
		t.Errorf("EffectiveSeedTime with nil map = %v, want the global 72h", got)
	}
}

// An override applies even when the global seed time is 0 (which on its
// own disables seed-time cleanup entirely).
func TestEffectiveSeedTimeOverridesZeroGlobal(t *testing.T) {
	s := Settings{IndexerSeedTimes: map[string]time.Duration{"TorrentLeech": 240 * time.Hour}}
	if got := s.EffectiveSeedTime("TorrentLeech"); got != 240*time.Hour {
		t.Errorf("EffectiveSeedTime = %v, want the 240h override", got)
	}
	if got := s.EffectiveSeedTime("Other"); got != 0 {
		t.Errorf("EffectiveSeedTime for an unlisted indexer = %v, want 0", got)
	}
}

func TestSelectRemovalsPerIndexerSeedTime(t *testing.T) {
	s := Settings{
		SeedTime: 72 * time.Hour,
		IndexerSeedTimes: map[string]time.Duration{
			"TorrentLeech": 240 * time.Hour, // longer than global
			"Nyaa":         12 * time.Hour,  // shorter than global
			"KeepForever":  0,               // never by seed time
		},
	}
	// Every torrent has seeded 100h: past the global and Nyaa limits,
	// short of TorrentLeech's, and irrelevant for KeepForever.
	const seeded = 100 * time.Hour
	for _, tc := range []struct {
		name    string
		indexer string
		wantRem bool
	}{
		{"longer override keeps it", "TorrentLeech", false},
		{"longer override matched case-insensitively", "torrentleech", false},
		{"shorter override removes it", "Nyaa", true},
		{"zero override keeps it forever", "KeepForever", false},
		{"unlisted indexer uses the global", "SomeOtherTracker", true},
		{"unknown indexer uses the global", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := []store.Torrent{{ID: "T1", Hash: "aa", AddedAt: 1, Indexer: tc.indexer}}
			live := liveOf(downloader.TorrentInfo{Hash: "aa", Progress: 1, SeedingTime: seeded})
			got := selectRemovals(stored, live, s)
			if (len(got) == 1) != tc.wantRem {
				t.Errorf("selectRemovals returned %d torrents, wantRemoval=%v", len(got), tc.wantRem)
			}
		})
	}
}

// The ratio trigger is global and still OR-ed in for an indexer whose
// seed-time override has not been reached.
func TestSelectRemovalsRatioStillAppliesUnderIndexerOverride(t *testing.T) {
	s := Settings{
		SeedTime:         72 * time.Hour,
		TargetRatio:      1.0,
		IndexerSeedTimes: map[string]time.Duration{"TorrentLeech": 240 * time.Hour},
	}
	stored := []store.Torrent{{ID: "T1", Hash: "aa", AddedAt: 1, Indexer: "TorrentLeech"}}
	live := liveOf(downloader.TorrentInfo{Hash: "aa", Progress: 1, SeedingTime: 100 * time.Hour, Ratio: 1.5})
	if got := selectRemovals(stored, live, s); len(got) != 1 {
		t.Errorf("selectRemovals returned %d torrents, want 1 (ratio met)", len(got))
	}
}

// newCleanupWith builds a Cleanup over the given settings, so the Sweep
// early-out can be exercised with per-indexer overrides in place.
func newCleanupWith(t *testing.T, s Settings) (*Cleanup, *fake.Server, *store.Store) {
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

	c := New(db, fakeDC, svc, playsession.New(), func() Settings { return s }, nil, time.Hour)
	return c, fakeDC, db
}

// A global seed_time of 0 disables the trigger globally but must not skip
// the sweep when an indexer overrides it upward.
func TestSweepRunsWhenOnlyIndexerOverrideIsSet(t *testing.T) {
	c, fakeDC, db := newCleanupWith(t, Settings{
		IndexerSeedTimes: map[string]time.Duration{"Nyaa": 12 * time.Hour},
	})
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1, Indexer: "Nyaa"}); err != nil {
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
		t.Error("expected torrent past its indexer override to be removed despite seed_time=0")
	}
}

// The same sweep must leave alone a torrent from an indexer with no
// override, since the global trigger is off.
func TestSweepKeepsUnlistedIndexerWhenGlobalDisabled(t *testing.T) {
	c, fakeDC, db := newCleanupWith(t, Settings{
		IndexerSeedTimes: map[string]time.Duration{"Nyaa": 12 * time.Hour},
	})
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1, Indexer: "Other"}); err != nil {
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
		t.Error("expected torrent from an unlisted indexer to remain when seed_time=0")
	}
}
