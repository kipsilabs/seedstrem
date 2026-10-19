package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

func TestForceSyncReplacesLocalMetadata(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	ctx := context.Background()
	old := store.Torrent{ID: "existing", Hash: "aaaa", Name: "old", Error: "stale", Phase: store.PhaseAdded, AddedAt: 1, Indexer: "tracker", UserAgent: "Stremio", ContentRef: "tt123", Origin: store.OriginNative}
	if err := e.store.InsertTorrent(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := e.store.InsertTorrent(ctx, store.Torrent{ID: "gone", Hash: "bbbb", Phase: store.PhaseSelected}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.InsertLinks(ctx, []store.Link{{Token: "stable", TorrentID: old.ID, FileIndex: 0, Path: "old.mkv", Bytes: 1}, {Token: "obsolete", TorrentID: old.ID, FileIndex: 5, Path: "gone.mkv", Bytes: 1}}); err != nil {
		t.Fatal(err)
	}
	label := e.config.Get().ClientLabel()
	e.fake.Put(&fake.Torrent{Hash: "aaaa", Name: "provider name", AddedAt: time.Unix(123, 0), Files: []fake.File{{Name: "renamed.mkv", Size: 200}, {Name: "extra.mp4", Size: 300}, {Name: "notes.txt", Size: 10}}})
	e.fake.Put(&fake.Torrent{Hash: "cccc", Name: "imported", Category: label, Files: []fake.File{{Name: "imported.mkv", Size: 50}}})
	e.fake.Put(&fake.Torrent{Hash: "dddd", Name: "unrelated", Category: "other", Files: []fake.File{{Name: "unrelated.mkv", Size: 50}}})
	w := e.do(t, http.MethodPost, "/torrents/sync", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("sync = %d: %s", w.Code, w.Body.String())
	}
	got, err := e.store.TorrentByID(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "provider name" || got.Error != "" || got.Phase != store.PhaseSelected || got.AddedAt != 123 || got.Indexer != old.Indexer || got.ContentRef != old.ContentRef || got.Origin != old.Origin || got.UserAgent != old.UserAgent {
		t.Fatalf("torrent = %+v", got)
	}
	links, err := e.store.LinksByTorrent(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links[0].Token != "stable" || links[0].Path != "renamed.mkv" || links[0].Bytes != 200 || links[1].FileIndex != 1 {
		t.Fatalf("links = %+v", links)
	}
	if _, err := e.store.LinkByToken(ctx, "obsolete"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("obsolete link: %v", err)
	}
	if _, err := e.store.TorrentByID(ctx, "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("gone torrent: %v", err)
	}
	imported, err := e.store.TorrentByHash(ctx, "cccc")
	if err != nil || imported.Origin != store.OriginAdopted {
		t.Fatalf("imported = %+v, %v", imported, err)
	}
	if _, err := e.store.TorrentByHash(ctx, "dddd"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unrelated torrent: %v", err)
	}
	for _, call := range e.fake.Calls() {
		for _, prefix := range []string{"delete ", "add ", "filePrio", "start ", "setSequentialDownload", "setFirstLastPiecePrio"} {
			if strings.HasPrefix(call, prefix) {
				t.Fatalf("provider write: %s", call)
			}
		}
	}
}

type failingSyncClient struct{ downloader.Client }

func (c failingSyncClient) Files(ctx context.Context, hash string) ([]downloader.FileInfo, error) {
	return nil, errors.New("files unavailable")
}

type failingTrackedClient struct{ downloader.Client }

func (c failingTrackedClient) Torrents(context.Context, []string) ([]downloader.TorrentInfo, error) {
	return nil, errors.New("tracked listing unavailable")
}

func TestForceSyncFailurePreservesLocalCopy(t *testing.T) {
	for _, kind := range []string{"listing", "unsupported", "files", "tracked"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			e.login(t)
			ctx := context.Background()
			if err := e.store.InsertTorrent(ctx, store.Torrent{ID: "old", Hash: "aaaa", Name: "keep", Phase: store.PhaseSelected}); err != nil {
				t.Fatal(err)
			}
			if kind == "tracked" {
				e.swappable.Swap(failingTrackedClient{Client: e.fake})
			} else if kind == "files" {
				e.fake.Put(&fake.Torrent{Hash: "aaaa", Name: "changed"})
				e.swappable.Swap(failingSyncClient{Client: e.fake})
			} else if kind == "unsupported" {
				e.fake.SetTorrentsByLabelErr(downloader.ErrNotSupported)
			} else {
				e.fake.SetTorrentsByLabelErr(errors.New("offline"))
			}
			w := e.do(t, http.MethodPost, "/torrents/sync", "")
			if w.Code != http.StatusBadGateway {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			got, err := e.store.TorrentByID(ctx, "old")
			if err != nil || got.Name != "keep" {
				t.Fatalf("local copy changed: %+v, %v", got, err)
			}
		})
	}
}

func TestForceSyncRequiresSession(t *testing.T) {
	e := newEnv(t)
	if w := e.do(t, http.MethodPost, "/torrents/sync", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}
