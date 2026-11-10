package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestTorrentFileRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.TorrentFile(ctx, "aabb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file err = %v, want ErrNotFound", err)
	}
	if err := s.SaveTorrentFile(ctx, "aabb", []byte("d4:infoe")); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Saving again replaces, never errors.
	if err := s.SaveTorrentFile(ctx, "aabb", []byte("d4:info2e")); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, err := s.TorrentFile(ctx, "aabb")
	if err != nil || !bytes.Equal(got, []byte("d4:info2e")) {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := s.SaveTorrentFile(ctx, "ccdd", nil); err == nil {
		t.Error("saving empty data should fail")
	}
}

func TestDeletionRetentionIsConfigurable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-10 * 24 * time.Hour).Unix()
	if err := s.RecordDeletion(ctx, DeletionEvent{Hash: "h1", DeletedAt: old, Reason: DeleteReasonManual}); err != nil {
		t.Fatal(err)
	}
	// Default retention is 30 days, so a 10-day-old record is visible.
	got, _ := s.Deletions(ctx)
	if len(got) != 1 {
		t.Fatalf("default retention: got %d, want 1", len(got))
	}
	s.SetDeletionRetention(48 * time.Hour)
	got, _ = s.Deletions(ctx)
	if len(got) != 0 {
		t.Fatalf("48h retention: got %d, want 0", len(got))
	}
}

func TestPruneDropsOrphanedTorrentFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.SetDeletionRetention(48 * time.Hour)

	_ = s.SaveTorrentFile(ctx, "expired", []byte("x"))
	_ = s.SaveTorrentFile(ctx, "kept", []byte("y"))
	old := time.Now().Add(-72 * time.Hour).Unix()
	_ = s.RecordDeletion(ctx, DeletionEvent{Hash: "kept", Reason: DeleteReasonManual})
	// Recording an already-expired deletion triggers the prune.
	_ = s.RecordDeletion(ctx, DeletionEvent{Hash: "expired", DeletedAt: old, Reason: DeleteReasonManual})

	if _, err := s.TorrentFile(ctx, "expired"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired file err = %v, want ErrNotFound", err)
	}
	if _, err := s.TorrentFile(ctx, "kept"); err != nil {
		t.Errorf("kept file: %v", err)
	}
}

func TestDeletionsReportHasTorrentFile(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.SaveTorrentFile(ctx, "withfile", []byte("x"))
	_ = s.RecordDeletion(ctx, DeletionEvent{Hash: "withfile", Reason: DeleteReasonManual})
	_ = s.RecordDeletion(ctx, DeletionEvent{Hash: "nofile", Reason: DeleteReasonManual})

	got, err := s.Deletions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, d := range got {
		has[d.Hash] = d.HasTorrentFile
	}
	if !has["withfile"] || has["nofile"] {
		t.Errorf("HasTorrentFile = %v", has)
	}
}
