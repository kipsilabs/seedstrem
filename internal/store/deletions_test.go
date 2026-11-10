package store

import (
	"context"
	"testing"
	"time"
)

func TestRecordDeletionRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ev := DeletionEvent{
		TorrentID:    "tor1",
		Hash:         "aabbcc",
		Name:         "Some Show S01E01",
		Indexer:      "MyIndexer",
		Origin:       OriginNative,
		Reason:       DeleteReasonSeedTime,
		SeedingTime:  49 * time.Hour,
		SeedLimit:    48 * time.Hour,
		Ratio:        1.25,
		RatioLimit:   2,
		Progress:     1,
		FilesDeleted: true,
	}
	if err := s.RecordDeletion(ctx, ev); err != nil {
		t.Fatalf("record deletion: %v", err)
	}

	got, err := s.Deletions(ctx)
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deletions, want 1", len(got))
	}
	d := got[0]
	if d.ID == "" {
		t.Error("deletion id is empty, want a generated id")
	}
	if d.TorrentID != "tor1" || d.Hash != "aabbcc" || d.Name != "Some Show S01E01" {
		t.Errorf("identity = %q/%q/%q, want tor1/aabbcc/Some Show S01E01", d.TorrentID, d.Hash, d.Name)
	}
	if d.Indexer != "MyIndexer" || d.Origin != OriginNative {
		t.Errorf("indexer/origin = %q/%q, want MyIndexer/%s", d.Indexer, d.Origin, OriginNative)
	}
	if d.Reason != DeleteReasonSeedTime {
		t.Errorf("reason = %q, want %q", d.Reason, DeleteReasonSeedTime)
	}
	if d.SeedingTime != 49*time.Hour || d.SeedLimit != 48*time.Hour {
		t.Errorf("seed evidence = %v/%v, want 49h/48h", d.SeedingTime, d.SeedLimit)
	}
	if d.Ratio != 1.25 || d.RatioLimit != 2 {
		t.Errorf("ratio evidence = %v/%v, want 1.25/2", d.Ratio, d.RatioLimit)
	}
	if d.Progress != 1 {
		t.Errorf("progress = %v, want 1", d.Progress)
	}
	if !d.FilesDeleted {
		t.Error("files deleted = false, want true")
	}
	if d.DeletedAt == 0 {
		t.Error("deleted at is zero, want it stamped at record time")
	}
}

// A deletion record must outlive the torrent it describes: the row is
// written as the torrent goes away, so a foreign key would delete the
// evidence along with it.
func TestRecordDeletionSurvivesTorrentRemoval(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tor := Torrent{ID: "tor1", Hash: "aabbcc", Name: "Show", Phase: "ready", AddedAt: time.Now().Unix()}
	if err := s.InsertTorrent(ctx, tor); err != nil {
		t.Fatalf("create torrent: %v", err)
	}
	if err := s.RecordDeletion(ctx, DeletionEvent{
		TorrentID: tor.ID, Hash: tor.Hash, Reason: DeleteReasonManual,
	}); err != nil {
		t.Fatalf("record deletion: %v", err)
	}
	if err := s.DeleteTorrent(ctx, tor.ID); err != nil {
		t.Fatalf("delete torrent: %v", err)
	}

	got, err := s.Deletions(ctx)
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deletions after torrent removal, want the record to survive", len(got))
	}
}

func TestDeletionsHidesRecordsOlderThanRetention(t *testing.T) {
	s := newTestStore(t)
	s.SetDeletionRetention(48 * time.Hour)
	ctx := context.Background()
	now := time.Now()

	fresh := DeletionEvent{
		TorrentID: "fresh", Hash: "aa", Reason: DeleteReasonSeedTime,
		DeletedAt: now.Add(-47 * time.Hour).Unix(),
	}
	stale := DeletionEvent{
		TorrentID: "stale", Hash: "bb", Reason: DeleteReasonSeedTime,
		DeletedAt: now.Add(-49 * time.Hour).Unix(),
	}
	for _, ev := range []DeletionEvent{fresh, stale} {
		if err := s.RecordDeletion(ctx, ev); err != nil {
			t.Fatalf("record deletion %s: %v", ev.TorrentID, err)
		}
	}

	got, err := s.Deletions(ctx)
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deletions, want only the 47h-old one", len(got))
	}
	if got[0].TorrentID != "fresh" {
		t.Errorf("kept %q, want the 47h-old record", got[0].TorrentID)
	}
}

func TestRecordDeletionPrunesExpiredRows(t *testing.T) {
	s := newTestStore(t)
	s.SetDeletionRetention(48 * time.Hour)
	ctx := context.Background()

	if err := s.RecordDeletion(ctx, DeletionEvent{
		TorrentID: "stale", Hash: "bb", Reason: DeleteReasonManual,
		DeletedAt: time.Now().Add(-49 * time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("record stale: %v", err)
	}
	if err := s.RecordDeletion(ctx, DeletionEvent{
		TorrentID: "fresh", Hash: "aa", Reason: DeleteReasonManual,
	}); err != nil {
		t.Fatalf("record fresh: %v", err)
	}

	// The read filter would hide the stale row anyway; assert it is
	// physically gone so the table cannot grow without bound.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrent_deletions`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Errorf("table holds %d rows, want the expired one pruned", n)
	}
}

func TestDeletionsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, ev := range []DeletionEvent{
		{TorrentID: "older", Hash: "aa", Reason: DeleteReasonManual, DeletedAt: now.Add(-3 * time.Hour).Unix()},
		{TorrentID: "newer", Hash: "bb", Reason: DeleteReasonManual, DeletedAt: now.Add(-1 * time.Hour).Unix()},
	} {
		if err := s.RecordDeletion(ctx, ev); err != nil {
			t.Fatalf("record %s: %v", ev.TorrentID, err)
		}
	}

	got, err := s.Deletions(ctx)
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d deletions, want 2", len(got))
	}
	if got[0].TorrentID != "newer" || got[1].TorrentID != "older" {
		t.Errorf("order = %q,%q, want newer,older", got[0].TorrentID, got[1].TorrentID)
	}
}
