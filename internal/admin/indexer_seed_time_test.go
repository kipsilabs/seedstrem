package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
)

func TestConfigDTORoundTripsIndexerSeedTimes(t *testing.T) {
	cfg := config.Default()
	cfg.Cleanup.IndexerSeedTimes = map[string]time.Duration{
		"TorrentLeech": 240 * time.Hour,
		"Nyaa":         12 * time.Hour,
		"KeepForever":  0,
	}

	dto := toDTO(cfg)
	// Sorted by indexer name so the UI's row order (and React keys) stay
	// stable across reads.
	want := []indexerSeedTime{
		{Indexer: "KeepForever", SeedTimeHours: 0},
		{Indexer: "Nyaa", SeedTimeHours: 12},
		{Indexer: "TorrentLeech", SeedTimeHours: 240},
	}
	if len(dto.Cleanup.IndexerSeedTimes) != len(want) {
		t.Fatalf("toDTO indexer_seed_times = %+v, want %+v", dto.Cleanup.IndexerSeedTimes, want)
	}
	for i, w := range want {
		if dto.Cleanup.IndexerSeedTimes[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, dto.Cleanup.IndexerSeedTimes[i], w)
		}
	}

	got := dto.apply(config.Default()).Cleanup.IndexerSeedTimes
	if len(got) != 3 {
		t.Fatalf("apply indexer_seed_times = %v, want 3 entries", got)
	}
	for name, wantDur := range map[string]time.Duration{
		"TorrentLeech": 240 * time.Hour,
		"Nyaa":         12 * time.Hour,
		"KeepForever":  0,
	} {
		if got[name] != wantDur {
			t.Errorf("%s = %v, want %v", name, got[name], wantDur)
		}
	}
}

// An empty list clears the stored overrides — that is how the UI removes
// the last row.
func TestConfigDTOEmptyIndexerSeedTimesClears(t *testing.T) {
	cfg := config.Default()
	cfg.Cleanup.IndexerSeedTimes = map[string]time.Duration{"Nyaa": 12 * time.Hour}

	var dto configDTO
	dto.Cleanup.IndexerSeedTimes = []indexerSeedTime{}
	if got := dto.apply(cfg).Cleanup.IndexerSeedTimes; len(got) != 0 {
		t.Errorf("apply with empty list = %v, want the overrides cleared", got)
	}
}

// A payload from a client that predates the field (nil list) must leave
// the stored overrides alone rather than wiping them.
func TestConfigDTONilIndexerSeedTimesKeepsStored(t *testing.T) {
	cfg := config.Default()
	cfg.Cleanup.IndexerSeedTimes = map[string]time.Duration{"Nyaa": 12 * time.Hour}

	var dto configDTO // IndexerSeedTimes stays nil
	got := dto.apply(cfg).Cleanup.IndexerSeedTimes
	if got["Nyaa"] != 12*time.Hour {
		t.Errorf("apply with nil list = %v, want the stored overrides kept", got)
	}
}

func TestIndexerSeedTimesFromDTONormalizes(t *testing.T) {
	got := indexerSeedTimesFromDTO([]indexerSeedTime{
		{Indexer: "  Nyaa  ", SeedTimeHours: 12},
		{Indexer: "", SeedTimeHours: 48},   // blank row: dropped
		{Indexer: "   ", SeedTimeHours: 5}, // whitespace-only row: dropped
		{Indexer: "Neg", SeedTimeHours: -3},
	})
	if len(got) != 2 {
		t.Fatalf("got %v, want only the two named entries", got)
	}
	if got["Nyaa"] != 12*time.Hour {
		t.Errorf("Nyaa = %v, want 12h (name trimmed)", got["Nyaa"])
	}
	// Negative hours are clamped rather than rejected, so a transient
	// input state cannot fail the whole save.
	if got["Neg"] != 0 {
		t.Errorf("Neg = %v, want 0 (clamped)", got["Neg"])
	}
}

// Saving case-insensitive duplicates is rejected: the lookup could not
// resolve them deterministically.
func TestPutConfigRejectsDuplicateIndexerNames(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	current := toDTO(e.config.Get())
	current.Server.AdminPassword = ""
	current.Cleanup.IndexerSeedTimes = []indexerSeedTime{
		{Indexer: "Nyaa", SeedTimeHours: 12},
		{Indexer: "nyaa", SeedTimeHours: 48},
	}
	body, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}

	w := e.do(t, http.MethodPut, "/config", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("put config: got %d %s, want 400", w.Code, w.Body.String())
	}
}

// The torrent listing must report the seed time that governs each torrent,
// not the global one — the UI derives its "available until" from it.
func TestTorrentsReportEffectiveSeedTime(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	cfg := e.config.Get()
	cfg.Cleanup.SeedTime = 72 * time.Hour
	cfg.Cleanup.IndexerSeedTimes = map[string]time.Duration{"TorrentLeech": 240 * time.Hour}
	if err := e.config.Update(cfg); err != nil {
		t.Fatal(err)
	}

	const overridden = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const plain = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ctx := context.Background()
	for _, tor := range []store.Torrent{
		{ID: "id-1", Hash: overridden, Name: "A", Phase: store.PhaseAdded, Indexer: "TorrentLeech"},
		{ID: "id-2", Hash: plain, Name: "B", Phase: store.PhaseAdded},
	} {
		e.fake.Put(&fake.Torrent{Hash: tor.Hash, Name: tor.Name, State: "seeding"})
		if err := e.store.InsertTorrent(ctx, tor); err != nil {
			t.Fatal(err)
		}
	}

	w := e.do(t, http.MethodGet, "/torrents", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list torrents: got %d %s, want 200", w.Code, w.Body.String())
	}
	var items []torrentItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}

	byID := map[string]torrentItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if got, want := byID["id-1"].SeedTime, int64((240 * time.Hour).Seconds()); got != want {
		t.Errorf("overridden torrent seed_time = %d, want %d", got, want)
	}
	if got, want := byID["id-1"].Indexer, "TorrentLeech"; got != want {
		t.Errorf("overridden torrent indexer = %q, want %q", got, want)
	}
	if got, want := byID["id-2"].SeedTime, int64((72 * time.Hour).Seconds()); got != want {
		t.Errorf("unattributed torrent seed_time = %d, want the global %d", got, want)
	}
	if byID["id-2"].Indexer != "" {
		t.Errorf("unattributed torrent indexer = %q, want empty", byID["id-2"].Indexer)
	}
}
