package store

import (
	"context"
	"testing"
)

func TestApplyTorrentSyncRollsBackOnInvalidLinks(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	original := Torrent{ID: "original", Hash: "aaaa", Name: "original", Phase: PhaseSelected, Origin: OriginNative}
	if err := st.InsertTorrent(ctx, original); err != nil {
		t.Fatal(err)
	}
	link := Link{Token: "stable", TorrentID: original.ID, FileIndex: 0, Path: "original.mkv", Bytes: 1}
	if err := st.InsertLinks(ctx, []Link{link}); err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Name = "changed"
	invalid := []Link{{Token: "duplicate", FileIndex: 0, Path: "one.mkv"}, {Token: "duplicate", FileIndex: 1, Path: "two.mkv"}}
	if err := st.ApplyTorrentSync(ctx, []TorrentSnapshot{{Torrent: updated, Links: invalid}}, []string{original.ID}); err == nil {
		t.Fatal("expected duplicate token error")
	}
	got, err := st.TorrentByID(ctx, original.ID)
	if err != nil || got != original {
		t.Fatalf("torrent changed after rollback: %+v, %v", got, err)
	}
	links, err := st.LinksByTorrent(ctx, original.ID)
	if err != nil || len(links) != 1 || links[0] != link {
		t.Fatalf("links changed after rollback: %+v, %v", links, err)
	}
}
