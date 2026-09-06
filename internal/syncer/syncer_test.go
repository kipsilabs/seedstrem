package syncer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

func TestReconcile(t *testing.T) {
	f := fake.New()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	hashLive := strings.Repeat("a", 40)
	hashGone := strings.Repeat("b", 40)

	f.Put(&fake.Torrent{Hash: hashLive, Name: "live torrent", State: "downloading"})

	if err := st.InsertTorrent(ctx, store.Torrent{ID: "LIVE0000000000", Hash: hashLive, Phase: store.PhaseAdded, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertTorrent(ctx, store.Torrent{ID: "GONE0000000000", Hash: hashGone, Phase: store.PhaseSelected, AddedAt: 2}); err != nil {
		t.Fatal(err)
	}

	s := New(st, f, nil, 0)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	gone, _ := st.TorrentByID(ctx, "GONE0000000000")
	if gone.Error == "" {
		t.Error("vanished torrent not marked with sticky error")
	}

	live, _ := st.TorrentByID(ctx, "LIVE0000000000")
	if live.Error != "" {
		t.Errorf("live torrent wrongly marked: %q", live.Error)
	}
	if live.Name != "live torrent" {
		t.Errorf("name not backfilled: %q", live.Name)
	}

	// Torrent comes back: error must clear.
	f.Put(&fake.Torrent{Hash: hashGone, Name: "returned", State: "downloading"})
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	gone, _ = st.TorrentByID(ctx, "GONE0000000000")
	if gone.Error != "" {
		t.Errorf("error not cleared after torrent returned: %q", gone.Error)
	}
}
