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

func TestSweepRecordsSeedTimeDeletion(t *testing.T) {
	c, fakeDC, db, _ := newCleanup(t, 24*time.Hour)
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{
		ID: "T1", Hash: testHash, Name: "Some Show", Indexer: "MyIndexer",
		Origin: store.OriginNative, AddedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: downloader.StateSeeding,
		Progress: 1, SeedingTime: 48 * time.Hour, Ratio: 0.5,
	})

	if err := c.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	got, err := db.Deletions(ctx)
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deletion records, want 1", len(got))
	}
	d := got[0]
	if d.Reason != store.DeleteReasonSeedTime {
		t.Errorf("reason = %q, want %q", d.Reason, store.DeleteReasonSeedTime)
	}
	if d.SeedingTime != 48*time.Hour {
		t.Errorf("seeding time = %v, want 48h", d.SeedingTime)
	}
	if d.SeedLimit != 24*time.Hour {
		t.Errorf("seed limit = %v, want 24h", d.SeedLimit)
	}
	if d.Name != "Some Show" || d.Indexer != "MyIndexer" {
		t.Errorf("identity = %q/%q, want Some Show/MyIndexer", d.Name, d.Indexer)
	}
	if !d.FilesDeleted {
		t.Error("files deleted = false, want true (DeleteFilesOnRemove is set)")
	}
}

// Ratio and seed time can both fire in one pass. Seed time takes
// precedence for the reason, but both evidence pairs are recorded so the
// decision stays reconstructible.
func TestSweepRecordsRatioDeletionWhenSeedTimeNotMet(t *testing.T) {
	c, fakeDC, db, _ := newCleanupRatio(t, 2.0)
	ctx := context.Background()

	if err := db.InsertTorrent(ctx, store.Torrent{ID: "T1", Hash: testHash, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	fakeDC.Put(&fake.Torrent{
		Hash: testHash, State: downloader.StateSeeding,
		Progress: 1, SeedingTime: time.Hour, Ratio: 2.5,
	})

	if err := c.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	got, err := db.Deletions(ctx)
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deletion records, want 1", len(got))
	}
	d := got[0]
	if d.Reason != store.DeleteReasonRatio {
		t.Errorf("reason = %q, want %q", d.Reason, store.DeleteReasonRatio)
	}
	if d.Ratio != 2.5 || d.RatioLimit != 2.0 {
		t.Errorf("ratio evidence = %v/%v, want 2.5/2", d.Ratio, d.RatioLimit)
	}
}

// newCleanupRatio builds a Cleanup whose only enabled trigger is the
// target ratio, mirroring newCleanup in cleanup_test.go.
func newCleanupRatio(t *testing.T, ratio float64) (*Cleanup, *fake.Server, *store.Store, *playsession.Sessions) {
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
		return Settings{TargetRatio: ratio}
	}, nil, time.Hour)

	return c, fakeDC, db, sessions
}
