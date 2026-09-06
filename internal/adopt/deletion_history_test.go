package adopt

import (
	"context"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// Un-adopting drops the store row while the torrent keeps seeding in the
// client. That is the easiest disappearance to misread as a seed-time
// bug, so the history must record it — and must record that no files were
// deleted.
func TestUnadoptRecordsDeletion(t *testing.T) {
	ctx := context.Background()
	a, st, dc := newAdopter(t)

	dc.Put(&fake.Torrent{
		Hash: "aaaa", Name: "Hand Added", Category: "seedstrem",
		AddedAt: time.Unix(1700000000, 0),
		Files:   []fake.File{{Name: "Hand.Added.S01E01.mkv", Size: 100}},
	})
	if err := a.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// The label goes away; the torrent stays in the client.
	dc.Put(&fake.Torrent{
		Hash: "aaaa", Name: "Hand Added", Category: "",
		AddedAt: time.Unix(1700000000, 0),
		Files:   []fake.File{{Name: "Hand.Added.S01E01.mkv", Size: 100}},
	})
	if err := a.Scan(ctx); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	got, err := st.Deletions(ctx)
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deletion records, want 1", len(got))
	}
	d := got[0]
	if d.Reason != store.DeleteReasonUnadopted {
		t.Errorf("reason = %q, want %q", d.Reason, store.DeleteReasonUnadopted)
	}
	if d.Hash != "aaaa" {
		t.Errorf("hash = %q, want aaaa", d.Hash)
	}
	if d.FilesDeleted {
		t.Error("files deleted = true, want false: un-adopting leaves the torrent in the client")
	}
	if d.Origin != store.OriginAdopted {
		t.Errorf("origin = %q, want %q", d.Origin, store.OriginAdopted)
	}
}
