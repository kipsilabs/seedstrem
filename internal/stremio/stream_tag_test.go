package stremio

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/meta"
	"github.com/kipsilabs/seedstrem/internal/prowlarr"
	"github.com/kipsilabs/seedstrem/internal/store"
)

func movieQuery() meta.Query { return meta.Query{Source: "tt", ID: "tt1375666"} }

// AIOStreams (which reformats streams and drops our custom name) parses an
// "indexer" tag from the description as the text following one of the emojis
// 🌐 ⚙️ 🔗 🔎 🔍 ☁️. seedstrem rides its provenance + readiness on that tag so
// the state survives AIOStreams' reformatting. The exact emoji must be ⚙️
// (U+2699 U+FE0F) — the one in AIOStreams' set.
const aioIndexerLinePrefix = "⚙️ seedstrem"

func TestFreshStreamItemCarriesIndexerTag(t *testing.T) {
	h := &Handler{}
	res := prowlarr.Result{
		Title: "The Matrix 1999 1080p BluRay", InfoHash: testHash,
		MagnetURL: testMagnet(), Seeders: 42, Size: 8 << 30, Indexer: "Peerflix",
	}
	item := h.toStreamItem("http://x", movieQuery(), res, 0)
	if !strings.Contains(item.Description, aioIndexerLinePrefix+" · Peerflix") {
		t.Errorf("fresh stream should carry AIOStreams indexer tag, got description:\n%s", item.Description)
	}
}

func TestFreshStreamItemWithoutIndexerOmitsSeparator(t *testing.T) {
	h := &Handler{}
	res := prowlarr.Result{
		Title: "The Matrix 1999 1080p", InfoHash: testHash, MagnetURL: testMagnet(), Seeders: 5,
	}
	item := h.toStreamItem("http://x", movieQuery(), res, 0)
	if !strings.Contains(item.Description, aioIndexerLinePrefix) {
		t.Errorf("expected seedstrem indexer tag, got:\n%s", item.Description)
	}
	if strings.Contains(item.Description, aioIndexerLinePrefix+" · ") {
		t.Errorf("empty indexer must not leave a dangling separator, got:\n%s", item.Description)
	}
}

func TestOwnedStreamItemTagReflectsProgress(t *testing.T) {
	tests := []struct {
		name     string
		progress float64
		want     string
	}{
		{"downloaded is cached", 1, aioIndexerLinePrefix + " · cached"},
		{"in progress shows percent", 0.45, aioIndexerLinePrefix + " · 45%"},
		{"queued at zero", 0, aioIndexerLinePrefix + " · queued"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{}
			tor := store.Torrent{Name: "The Matrix 1999 1080p BluRay", Hash: testHash, Magnet: testMagnet()}
			item := h.toOwnedStreamItem("http://x", movieQuery(), tor, tt.progress)
			if !strings.Contains(item.Description, tt.want) {
				t.Errorf("want tag %q, got description:\n%s", tt.want, item.Description)
			}
		})
	}
}

// Regression: the raw release title must remain the first description line so
// AIOStreams still selects it for filename/quality parsing, and the existing
// human-facing stat line must survive.
func TestStreamItemKeepsTitleOnFirstLine(t *testing.T) {
	h := &Handler{}
	res := prowlarr.Result{Title: "The Matrix 1999 1080p BluRay", InfoHash: testHash, MagnetURL: testMagnet(), Seeders: 42, Size: 8 << 30, Indexer: "Peerflix"}
	item := h.toStreamItem("http://x", movieQuery(), res, 0)
	if first := strings.SplitN(item.Description, "\n", 2)[0]; first != res.Title {
		t.Errorf("first description line = %q, want raw title %q", first, res.Title)
	}
}

// TestStreamOrdersCachedFirst seeds two owned torrents for the same content —
// one still downloading (added first), one fully downloaded (added second) —
// and asserts the fully-downloaded (cached) one is surfaced first regardless
// of insertion order.
func TestStreamOrdersCachedFirst(t *testing.T) {
	h := newHarness(t)

	const downloadingHash = "1111111111111111111111111111111111111111"
	downloadingMagnet := "magnet:?xt=urn:btih:" + downloadingHash + "&dn=The.Matrix.480p"

	// The store returns owned rows newest-first (added_at DESC). Give the
	// downloading torrent the HIGHER added_at so it sorts first naturally —
	// the progress sort must then move the fully-downloaded one ahead of it.
	h.fakeDC.Put(&fake.Torrent{Hash: downloadingHash, State: "Downloading", Progress: 0.3,
		Files: []fake.File{{Name: "The.Matrix.1999.480p.mkv", Size: 1 << 30}}})
	if err := h.db.InsertTorrent(context.Background(), store.Torrent{
		ID: "DOWNLOADING001", Hash: downloadingHash, Name: "The Matrix 480p",
		Phase: store.PhaseSelected, AddedAt: 2, Magnet: downloadingMagnet,
		ContentSource: "tt", ContentRef: "tt1375666",
	}); err != nil {
		t.Fatalf("seed downloading: %v", err)
	}

	// Fully-downloaded torrent, older (lower added_at → naturally second).
	h.fakeDC.Put(&fake.Torrent{Hash: testHash, State: "Paused", Progress: 1,
		Files: []fake.File{{Name: "The.Matrix.1999.1080p.BluRay.mkv", Size: 8 << 30}}})
	if err := h.db.InsertTorrent(context.Background(), store.Torrent{
		ID: "CACHED00000001", Hash: testHash, Name: "The Matrix 1080p",
		Phase: store.PhaseSelected, AddedAt: 1, Magnet: testMagnet(),
		ContentSource: "tt", ContentRef: "tt1375666",
	}); err != nil {
		t.Fatalf("seed cached: %v", err)
	}

	resp, err := http.Get(h.server.URL + "/stremio/stream/movie/tt1375666.json")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()

	var sr streamResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sr.Streams) != 2 {
		t.Fatalf("want 2 owned streams, got %d: %+v", len(sr.Streams), sr.Streams)
	}
	if !strings.Contains(sr.Streams[0].Description, "· cached") {
		t.Errorf("cached stream should be first, got first=%q", sr.Streams[0].Description)
	}
	if !strings.Contains(sr.Streams[1].Description, "· 30%") {
		t.Errorf("downloading stream should be second, got second=%q", sr.Streams[1].Description)
	}
}
