package torrents

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

var sampleTorrent = []byte("d4:infod6:lengthi1e4:name4:test12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")

func TestEnsureAddedSavesTorrentFile(t *testing.T) {
	svc, _, db := newService(t)
	ctx := context.Background()

	tor, err := svc.EnsureAdded(ctx, testMagnet("Show"), sampleTorrent, Selector{})
	if err != nil {
		t.Fatalf("ensure added: %v", err)
	}
	got, err := db.TorrentFile(ctx, tor.Hash)
	if err != nil || !bytes.Equal(got, sampleTorrent) {
		t.Fatalf("saved file = %q, %v", got, err)
	}
}

func TestRemoveExportsTorrentFromClient(t *testing.T) {
	svc, fakeDC, db := newService(t)
	ctx := context.Background()

	fakeDC.Put(&fake.Torrent{Hash: testHash, State: downloader.StateSeeding})
	fakeDC.SetExport(testHash, sampleTorrent)
	tor, err := svc.EnsureAdded(ctx, testMagnet("Show"), nil, Selector{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(ctx, tor, store.DeletionEvent{Reason: store.DeleteReasonManual}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got, err := db.TorrentFile(ctx, tor.Hash); err != nil || !bytes.Equal(got, sampleTorrent) {
		t.Fatalf("exported file = %q, %v", got, err)
	}
}

func TestRemoveWithoutExportSupportStillRemoves(t *testing.T) {
	svc, fakeDC, db := newService(t)
	ctx := context.Background()

	fakeDC.Put(&fake.Torrent{Hash: testHash, State: downloader.StateSeeding})
	tor, _ := svc.EnsureAdded(ctx, testMagnet("Show"), nil, Selector{})
	if err := svc.Remove(ctx, tor, store.DeletionEvent{Reason: store.DeleteReasonManual}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := db.TorrentFile(ctx, tor.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("file err = %v, want ErrNotFound", err)
	}
}

func TestReAddFromHistory(t *testing.T) {
	svc, fakeDC, db := newService(t)
	ctx := context.Background()

	tor, _ := svc.EnsureAdded(ctx, testMagnet("Show"), sampleTorrent, Selector{})
	if err := svc.Remove(ctx, tor, store.DeletionEvent{Reason: store.DeleteReasonManual}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ReAdd(ctx, tor.Hash)
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if got.Hash != tor.Hash {
		t.Errorf("hash = %q, want %q", got.Hash, tor.Hash)
	}
	if _, err := db.TorrentByHash(ctx, tor.Hash); err != nil {
		t.Errorf("torrent not stored again: %v", err)
	}
	var fileAdds int
	for _, c := range fakeDC.Calls() {
		if strings.HasPrefix(c, "add torrentfile=") {
			fileAdds++
		}
	}
	if fileAdds != 2 {
		t.Errorf("torrent-file adds = %d, want 2 (initial + re-add)", fileAdds)
	}
}

func TestReAddWithoutSavedFile(t *testing.T) {
	svc, _, _ := newService(t)
	if _, err := svc.ReAdd(context.Background(), testHash); !errors.Is(err, ErrNoTorrentFile) {
		t.Fatalf("err = %v, want ErrNoTorrentFile", err)
	}
}
