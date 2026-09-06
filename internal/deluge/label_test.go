package deluge

import (
	"context"
	"errors"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/deluge/delugerpc"
	"github.com/kipsilabs/seedstrem/internal/downloader"
)

func TestTorrentsByLabelRequiresLabelPlugin(t *testing.T) {
	// Without the Label plugin no torrent carries a label at all, so a
	// label query cannot be answered — not even negatively.
	f := newFakeAPI()
	f.plugins = []string{"Execute"}
	c := newTestClient(f)

	if _, err := c.TorrentsByLabel(context.Background(), "seedstrem"); !errors.Is(err, downloader.ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}
}

func TestTorrentsByLabelReturnsLabelledTorrents(t *testing.T) {
	f := newFakeAPI()
	f.plugins = []string{"Label"}
	f.byLabel = map[string]map[string]*delugerpc.TorrentStatus{
		"seedstrem": {"aaaa": {Name: "hand added", TimeAdded: 1700000000}},
	}
	c := newTestClient(f)

	got, err := c.TorrentsByLabel(context.Background(), "seedstrem")
	if err != nil {
		t.Fatalf("by label: %v", err)
	}
	if len(got) != 1 || got[0].Hash != "aaaa" {
		t.Fatalf("got %+v, want one torrent aaaa", got)
	}
	if got[0].AddedAt.Unix() != 1700000000 {
		t.Fatalf("AddedAt = %v, want unix 1700000000", got[0].AddedAt)
	}
}

func TestTorrentsByLabelEmptyLabelReturnsNothing(t *testing.T) {
	f := newFakeAPI()
	f.plugins = []string{"Label"}
	c := newTestClient(f)

	got, err := c.TorrentsByLabel(context.Background(), "")
	if err != nil {
		t.Fatalf("by label: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d torrents, want 0", len(got))
	}
	for _, call := range f.calls {
		if call != "" {
			t.Fatalf("empty label reached the daemon: %v", f.calls)
		}
	}
}
