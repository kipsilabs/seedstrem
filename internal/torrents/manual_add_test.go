package torrents

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/metainfo"
)

func TestAddManualMagnet(t *testing.T) {
	svc, fakeDC, db := newService(t)
	ctx := context.Background()

	tor, err := svc.AddManual(ctx, testMagnet("Show"), nil, "browser")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if tor.Hash != testHash || tor.UserAgent != "browser" {
		t.Errorf("tor = %+v", tor)
	}
	if _, err := db.TorrentByHash(ctx, testHash); err != nil {
		t.Errorf("not stored: %v", err)
	}
	if fakeDC.Get(testHash) == nil {
		t.Error("not added to client")
	}
}

func TestAddManualFileKeepsTorrent(t *testing.T) {
	svc, _, db := newService(t)
	ctx := context.Background()

	tor, err := svc.AddManual(ctx, "", sampleTorrent, "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	wantHash, _, _, _ := metainfo.FromTorrent(sampleTorrent)
	if tor.Hash != wantHash {
		t.Errorf("hash = %q, want %q", tor.Hash, wantHash)
	}
	got, err := db.TorrentFile(ctx, tor.Hash)
	if err != nil || !bytes.Equal(got, sampleTorrent) {
		t.Errorf("saved file = %q, %v", got, err)
	}
}

func TestAddManualRejectsInvalidInput(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		magnet string
		raw    []byte
	}{
		"empty":       {},
		"bad magnet":  {magnet: "not-a-magnet"},
		"bad torrent": {raw: []byte("garbage")},
	} {
		if _, err := svc.AddManual(ctx, tc.magnet, tc.raw, ""); !errors.Is(err, ErrInvalidTorrent) {
			t.Errorf("%s: err = %v, want ErrInvalidTorrent", name, err)
		}
	}
}
