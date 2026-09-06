package adopt

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

func enabled() Settings { return Settings{Enabled: true, Label: "seedstrem"} }

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newAdopter(t *testing.T) (*Adopter, *store.Store, *fake.Server) {
	t.Helper()
	db := newTestStore(t)
	dc := fake.New()
	return New(db, dc, enabled, nil, time.Minute), db, dc
}

func TestScanAdoptsLabelledTorrent(t *testing.T) {
	ctx := context.Background()
	a, st, dc := newAdopter(t)
	dc.Put(&fake.Torrent{
		Hash: "aaaa", Name: "Hand Added", Category: "seedstrem",
		AddedAt: time.Unix(1700000000, 0),
		Files: []fake.File{
			{Name: "Hand.Added.S01E01.mkv", Size: 100},
			{Name: "readme.nfo", Size: 5},
			{Name: "Hand.Added.S01E02.mkv", Size: 200},
		},
	})

	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	tor, err := st.TorrentByHash(ctx, "aaaa")
	if err != nil {
		t.Fatalf("by hash: %v", err)
	}
	if tor.Origin != store.OriginAdopted {
		t.Fatalf("origin = %q, want adopted", tor.Origin)
	}
	if tor.Name != "Hand Added" {
		t.Fatalf("name = %q, want %q", tor.Name, "Hand Added")
	}
	if tor.AddedAt != 1700000000 {
		t.Fatalf("added_at = %d, want the client's value 1700000000", tor.AddedAt)
	}
	links, err := st.LinksByTorrent(ctx, tor.ID)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2 (video files only)", len(links))
	}
}

func TestScanIssuesNoWritesToTheClient(t *testing.T) {
	ctx := context.Background()
	a, _, dc := newAdopter(t)
	dc.Put(&fake.Torrent{
		Hash: "aaaa", Category: "seedstrem",
		Files: []fake.File{{Name: "a.mkv", Size: 1}},
	})

	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	writes := []string{"add ", "filePrio", "setSequentialDownload", "setFirstLastPiecePrio", "start ", "delete ", "prioritizePieces"}
	for _, c := range dc.Calls() {
		for _, w := range writes {
			if strings.HasPrefix(c, w) {
				t.Fatalf("adoption wrote to the download client: %q (all calls: %v)", c, dc.Calls())
			}
		}
	}
}

func TestScanSkipsKnownHash(t *testing.T) {
	ctx := context.Background()
	a, st, dc := newAdopter(t)
	if err := st.InsertTorrent(ctx, store.Torrent{
		ID: "T1", Hash: "aaaa", Name: "native", Phase: store.PhaseSelected,
		AddedAt: 1, Origin: store.OriginNative,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dc.Put(&fake.Torrent{Hash: "aaaa", Name: "renamed", Category: "seedstrem",
		Files: []fake.File{{Name: "a.mkv", Size: 1}}})

	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	tor, err := st.TorrentByHash(ctx, "aaaa")
	if err != nil {
		t.Fatalf("by hash: %v", err)
	}
	if tor.ID != "T1" || tor.Origin != store.OriginNative || tor.Name != "native" {
		t.Fatalf("existing row was modified: %+v", tor)
	}
}

func TestScanDefersTorrentWithoutMetadata(t *testing.T) {
	ctx := context.Background()
	a, st, dc := newAdopter(t)
	dc.Put(&fake.Torrent{Hash: "aaaa", Category: "seedstrem"}) // no files yet

	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if _, err := st.TorrentByHash(ctx, "aaaa"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (adoption must wait for metadata)", err)
	}
}

func TestScanUnadoptsWhenLabelRemoved(t *testing.T) {
	ctx := context.Background()
	a, st, dc := newAdopter(t)
	dc.Put(&fake.Torrent{Hash: "aaaa", Category: "seedstrem",
		Files: []fake.File{{Name: "a.mkv", Size: 1}}})
	if err := a.Scan(ctx); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	dc.Update("aaaa", func(tr *fake.Torrent) { tr.Category = "something-else" })
	if err := a.Scan(ctx); err != nil {
		t.Fatalf("second scan: %v", err)
	}

	if _, err := st.TorrentByHash(ctx, "aaaa"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (row should be un-adopted)", err)
	}
}

func TestScanNeverDropsNativeRows(t *testing.T) {
	ctx := context.Background()
	a, st, _ := newAdopter(t)
	if err := st.InsertTorrent(ctx, store.Torrent{
		ID: "T1", Hash: "aaaa", Phase: store.PhaseSelected, AddedAt: 1,
		Origin: store.OriginNative,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The client reports no labelled torrents at all — the situation a
	// Deluge instance without the Label plugin would produce.

	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if _, err := st.TorrentByHash(ctx, "aaaa"); err != nil {
		t.Fatalf("native row was dropped: %v", err)
	}
}

func TestScanDropsNothingWhenListingFails(t *testing.T) {
	ctx := context.Background()
	a, st, dc := newAdopter(t)
	dc.Put(&fake.Torrent{Hash: "aaaa", Category: "seedstrem",
		Files: []fake.File{{Name: "a.mkv", Size: 1}}})
	if err := a.Scan(ctx); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	dc.SetTorrentsByLabelErr(errors.New("connection refused"))
	if err := a.Scan(ctx); err == nil {
		t.Fatal("scan returned nil, want the listing error")
	}

	if _, err := st.TorrentByHash(ctx, "aaaa"); err != nil {
		t.Fatalf("adopted row dropped on a failed listing: %v", err)
	}
}

func TestScanNotSupportedIsANoop(t *testing.T) {
	ctx := context.Background()
	a, st, dc := newAdopter(t)
	dc.Put(&fake.Torrent{Hash: "aaaa", Category: "seedstrem",
		Files: []fake.File{{Name: "a.mkv", Size: 1}}})
	if err := a.Scan(ctx); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	dc.SetTorrentsByLabelErr(downloader.ErrNotSupported)
	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v, want nil (unsupported is not a failure)", err)
	}

	if _, err := st.TorrentByHash(ctx, "aaaa"); err != nil {
		t.Fatalf("adopted row dropped by an unsupported backend: %v", err)
	}
}

func TestScanDisabledDoesNothing(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	dc := fake.New()
	dc.Put(&fake.Torrent{Hash: "aaaa", Category: "seedstrem",
		Files: []fake.File{{Name: "a.mkv", Size: 1}}})
	a := New(st, dc, func() Settings {
		return Settings{Enabled: false, Label: "seedstrem"}
	}, nil, time.Minute)

	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(dc.Calls()) != 0 {
		t.Fatalf("disabled scan talked to the client: %v", dc.Calls())
	}
	if _, err := st.TorrentByHash(ctx, "aaaa"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
