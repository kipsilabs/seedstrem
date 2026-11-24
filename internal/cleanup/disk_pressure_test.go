package cleanup

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

const gib = int64(1) << 30

// pressureCleanup builds a cleanup whose normal seed time (120h) is far off,
// with disk pressure at 75% / 48h, measuring a fake 100 GiB disk.
func pressureCleanup(t *testing.T, used *int64) (*Cleanup, *fake.Server, *store.Store) {
	t.Helper()
	c, fakeDC, db, _ := newCleanup(t, 120*time.Hour)
	c.settings = func() Settings {
		return Settings{SeedTime: 120 * time.Hour, DiskPressurePercent: 75, DiskPressureSeedTime: 48 * time.Hour, DiskPath: "/data"}
	}
	c.diskUsage = func(string) (int64, int64, error) { return *used, 100 * gib, nil }
	return c, fakeDC, db
}

func addSeeding(t *testing.T, db *store.Store, fakeDC *fake.Server, n int, seeded time.Duration, size int64) string {
	t.Helper()
	hash := fmt.Sprintf("%040x", n)
	if err := db.InsertTorrent(context.Background(), store.Torrent{ID: fmt.Sprintf("T%d", n), Hash: hash, AddedAt: int64(n)}); err != nil {
		t.Fatal(err)
	}
	fakeDC.Put(&fake.Torrent{Hash: hash, State: downloader.StateSeeding, Progress: 1, SeedingTime: seeded, Completed: size})
	return hash
}

func TestDiskPressureRemovesOldestPastMinimumUntilBelowThreshold(t *testing.T) {
	used := 80 * gib // 80% of 100 GiB, threshold 75%
	c, fakeDC, db := pressureCleanup(t, &used)
	oldest := addSeeding(t, db, fakeDC, 1, 60*time.Hour, 3*gib) // removed first: 80 -> 77
	second := addSeeding(t, db, fakeDC, 2, 50*time.Hour, 3*gib) // removed: 77 -> 74, below 75: stop
	third := addSeeding(t, db, fakeDC, 3, 49*time.Hour, 3*gib)  // eligible but not needed
	young := addSeeding(t, db, fakeDC, 4, 10*time.Hour, 30*gib) // under the 48h minimum: never

	if err := c.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, h := range []string{oldest, second} {
		if fakeDC.Get(h) != nil {
			t.Errorf("%s should have been removed for disk space", h[38:])
		}
	}
	for _, h := range []string{third, young} {
		if fakeDC.Get(h) == nil {
			t.Errorf("%s should have been kept", h[38:])
		}
	}
	dels, err := db.Deletions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dels) != 2 || dels[0].Reason != store.DeleteReasonDiskPressure || dels[0].SeedLimit != 48*time.Hour {
		t.Errorf("deletion history = %+v, want two disk_pressure rows with the 48h minimum", dels)
	}
}

func TestDiskPressureLeavesEverythingBelowThreshold(t *testing.T) {
	used := 74 * gib
	c, fakeDC, db := pressureCleanup(t, &used)
	h := addSeeding(t, db, fakeDC, 1, 100*time.Hour, 3*gib)
	if err := c.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(h) == nil {
		t.Error("torrent removed although the disk is below the pressure threshold")
	}
}

func TestDiskPressureNeverRemovesTorrentBeingWatched(t *testing.T) {
	used := 90 * gib
	c, fakeDC, db := pressureCleanup(t, &used)
	h := addSeeding(t, db, fakeDC, 1, 100*time.Hour, 3*gib)
	end := c.sessions.Begin(h)
	defer end()
	if err := c.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(h) == nil {
		t.Error("torrent being watched was removed for disk space")
	}
}

func TestDiskPressureSkippedWhenUsageUnknown(t *testing.T) {
	used := 90 * gib
	c, fakeDC, db := pressureCleanup(t, &used)
	c.diskUsage = func(string) (int64, int64, error) { return 0, 0, errors.New("statfs failed") }
	h := addSeeding(t, db, fakeDC, 1, 100*time.Hour, 3*gib)
	if err := c.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fakeDC.Get(h) == nil {
		t.Error("torrent removed although disk usage could not be measured")
	}
}

func TestSelectPressureRemovalsSkipsIncompleteAndAlreadyRemoved(t *testing.T) {
	s := Settings{DiskPressureSeedTime: 48 * time.Hour}
	stored := []store.Torrent{{ID: "a", Hash: "a", AddedAt: 1}, {ID: "b", Hash: "b", AddedAt: 2}, {ID: "c", Hash: "c", AddedAt: 3}}
	live := liveOf(
		downloader.TorrentInfo{Hash: "a", Progress: 1, SeedingTime: 50 * time.Hour},
		downloader.TorrentInfo{Hash: "b", Progress: 0.9, SeedingTime: 0},
		downloader.TorrentInfo{Hash: "c", Progress: 1, SeedingTime: 70 * time.Hour},
	)
	got := selectPressureRemovals(stored, live, s, map[string]bool{"c": true})
	if len(got) != 1 || got[0].Hash != "a" {
		t.Errorf("got %v, want only a", got)
	}
}
