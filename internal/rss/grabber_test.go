package rss

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/prowlarr"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

func res(hash string, seeders int, size int64, freeleech bool) prowlarr.Result {
	return prowlarr.Result{
		Title:     hash,
		InfoHash:  hash,
		MagnetURL: "magnet:?xt=urn:btih:" + hash,
		Seeders:   seeders,
		Size:      size,
		Freeleech: freeleech,
	}
}

func hashesOf(rs []prowlarr.Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.InfoHash
	}
	return out
}

func TestSelectGrabs(t *testing.T) {
	const gb = 1 << 30

	tests := []struct {
		name       string
		results    []prowlarr.Result
		settings   Settings
		used, tot  int64
		have       func(string) bool
		wantHashes []string
	}{
		{
			name:       "ranks freeleech then seeders and caps per cycle",
			results:    []prowlarr.Result{res("a", 5, gb, false), res("b", 1, gb, true), res("c", 50, gb, false)},
			settings:   Settings{MaxGrabsPerCycle: 2},
			wantHashes: []string{"b", "c"}, // freeleech first, then highest seeders
		},
		{
			name:       "drops below min seeders",
			results:    []prowlarr.Result{res("a", 0, gb, false), res("b", 10, gb, false)},
			settings:   Settings{MaxGrabsPerCycle: 5, Filters: prowlarr.Filters{MinSeeders: 1}},
			wantHashes: []string{"b"},
		},
		{
			name:       "freeleech only keeps freeleech",
			results:    []prowlarr.Result{res("a", 99, gb, false), res("b", 1, gb, true)},
			settings:   Settings{MaxGrabsPerCycle: 5, FreeleechOnly: true},
			wantHashes: []string{"b"},
		},
		{
			name:       "skips already owned",
			results:    []prowlarr.Result{res("a", 5, gb, false), res("b", 10, gb, false)},
			settings:   Settings{MaxGrabsPerCycle: 5},
			have:       func(h string) bool { return h == "b" },
			wantHashes: []string{"a"},
		},
		{
			name:     "disk over threshold grabs nothing",
			results:  []prowlarr.Result{res("a", 5, gb, false)},
			settings: Settings{MaxGrabsPerCycle: 5, MaxDiskUsagePercent: 80},
			used:     90, tot: 100,
			wantHashes: nil,
		},
		{
			name: "disk gate drops the oversize but keeps what fits",
			// limit = 80% of 10GB = 8GB, used = 0. First (6GB) fits; the
			// 5GB one would push projected to 11GB (skip); 1GB fits.
			results:  []prowlarr.Result{res("a", 30, 6*gb, false), res("b", 20, 5*gb, false), res("c", 10, 1*gb, false)},
			settings: Settings{MaxGrabsPerCycle: 5, MaxDiskUsagePercent: 80},
			used:     0, tot: 10 * gb,
			wantHashes: []string{"a", "c"},
		},
		{
			name:       "zero max grabs nothing",
			results:    []prowlarr.Result{res("a", 5, gb, false)},
			settings:   Settings{MaxGrabsPerCycle: 0},
			wantHashes: nil,
		},
		{
			name:       "dedups by infohash keeping most seeders",
			results:    []prowlarr.Result{res("a", 5, gb, false), res("a", 40, gb, false)},
			settings:   Settings{MaxGrabsPerCycle: 5},
			wantHashes: []string{"a"},
		},
		{
			name: "collapses same release mirrored across indexers (different infohash)",
			// Same scene release on two trackers: byte-identical name modulo
			// case/separators, but different infohash. Keep the best-seeded.
			results: []prowlarr.Result{
				{Title: "The.Matrix.1999.1080p.BluRay.x264-GRP", InfoHash: "h1", MagnetURL: "m1", Seeders: 10, Size: gb},
				{Title: "the matrix 1999 1080p bluray x264-grp", InfoHash: "h2", MagnetURL: "m2", Seeders: 90, Size: gb},
				{Title: "Some.Other.Movie.2020.1080p", InfoHash: "h3", MagnetURL: "m3", Seeders: 5, Size: gb},
			},
			settings:   Settings{MaxGrabsPerCycle: 5},
			wantHashes: []string{"h2", "h3"}, // matrix collapsed to the 90-seeder copy
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hashesOf(selectGrabs(tc.results, tc.settings, tc.used, tc.tot, tc.have))
			if len(got) != len(tc.wantHashes) {
				t.Fatalf("got %v; want %v", got, tc.wantHashes)
			}
			for i := range got {
				if got[i] != tc.wantHashes[i] {
					t.Fatalf("got %v; want %v", got, tc.wantHashes)
				}
			}
		})
	}
}

func titled(hash, title string, seeders int, size int64) prowlarr.Result {
	r := res(hash, seeders, size, false)
	r.Title = title
	return r
}

func TestSelectGrabsTitleFilter(t *testing.T) {
	const gb = 1 << 30

	tests := []struct {
		name       string
		results    []prowlarr.Result
		settings   Settings
		wantHashes []string
	}{
		{
			name: "exclude drops matching titles (case-insensitive)",
			results: []prowlarr.Result{
				titled("a", "Movie.2020.1080p.BluRay", 10, gb),
				titled("b", "Movie.2020.CAM", 10, gb),
				titled("c", "Movie.2019.hdts.x264", 10, gb),
			},
			settings:   Settings{MaxGrabsPerCycle: 5, IncludeKeywords: nil, ExcludeKeywords: []string{"CAM", "HDTS"}},
			wantHashes: []string{"a"},
		},
		{
			name: "include keeps only titles matching at least one keyword",
			results: []prowlarr.Result{
				titled("a", "Movie.2020.1080p.BluRay", 10, gb),
				titled("b", "Movie.2020.720p.WEB", 10, gb),
				titled("c", "Movie.2020.2160p.UHD", 10, gb),
			},
			settings:   Settings{MaxGrabsPerCycle: 5, IncludeKeywords: []string{"1080p", "2160p"}},
			wantHashes: []string{"a", "c"},
		},
		{
			name: "empty include allows all",
			results: []prowlarr.Result{
				titled("a", "Anything.Goes", 10, gb),
				titled("b", "Also.Fine", 10, gb),
			},
			settings:   Settings{MaxGrabsPerCycle: 5},
			wantHashes: []string{"a", "b"},
		},
		{
			name: "exclude wins over include when a title matches both",
			results: []prowlarr.Result{
				titled("a", "Movie.2020.1080p.PROPER", 10, gb),
				titled("b", "Movie.2020.1080p.CAM", 10, gb),
			},
			settings:   Settings{MaxGrabsPerCycle: 5, IncludeKeywords: []string{"1080p"}, ExcludeKeywords: []string{"CAM"}},
			wantHashes: []string{"a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hashesOf(selectGrabs(tc.results, tc.settings, 0, 0, nil))
			if len(got) != len(tc.wantHashes) {
				t.Fatalf("got %v; want %v", got, tc.wantHashes)
			}
			for i := range got {
				if got[i] != tc.wantHashes[i] {
					t.Fatalf("got %v; want %v", got, tc.wantHashes)
				}
			}
		})
	}
}

func TestNormalizeReleaseTitle(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"The.Matrix.1999.1080p.BluRay.x264-GRP", "thematrix19991080pblurayx264grp"},
		{"the matrix 1999 1080p bluray x264-grp", "thematrix19991080pblurayx264grp"},
		{"", ""},
		{"---", ""},
	}
	for _, tc := range tests {
		if got := normalizeReleaseTitle(tc.in); got != tc.want {
			t.Errorf("normalizeReleaseTitle(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	// Genuinely different releases must not collapse together.
	if normalizeReleaseTitle("Movie 2020 1080p") == normalizeReleaseTitle("Movie 2020 720p") {
		t.Error("different resolutions should not normalize equal")
	}
}

// --- Poll wiring, with fakes ---

type fakeSearcher struct {
	results []prowlarr.Result
	err     error
	calls   int
	gotCats []int
}

func (f *fakeSearcher) SearchEach(_ context.Context, query, searchType string, categories, _ []int, _ time.Duration) ([]prowlarr.Result, error) {
	f.calls++
	f.gotCats = categories
	if query != "" || searchType != "search" {
		return nil, errors.New("expected empty query recent search")
	}
	return f.results, f.err
}

type fakeAdder struct{ added []string }

func (f *fakeAdder) EnsureAdded(_ context.Context, magnet string, _ []byte, _ torrents.Selector) (store.Torrent, error) {
	f.added = append(f.added, magnet)
	return store.Torrent{}, nil
}

type fakeOwner struct {
	owned map[string]bool
	err   error
	// all backs AllTorrents for the active-torrent gate; when nil it is
	// derived from owned so existing tests need no changes.
	all    []store.Torrent
	allErr error
}

func (f *fakeOwner) TorrentsByHashes(_ context.Context, hashes []string) (map[string]store.Torrent, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]store.Torrent{}
	for _, h := range hashes {
		lh := strings.ToLower(h)
		if f.owned[lh] {
			out[lh] = store.Torrent{Hash: lh}
		}
	}
	return out, nil
}

func (f *fakeOwner) AllTorrents(_ context.Context) ([]store.Torrent, error) {
	if f.allErr != nil {
		return nil, f.allErr
	}
	if f.all != nil {
		return f.all, nil
	}
	out := make([]store.Torrent, 0, len(f.owned))
	for h := range f.owned {
		out = append(out, store.Torrent{Hash: h})
	}
	return out, nil
}

// fakeStater backs the concurrent-download gate: it reports the given live
// torrent infos regardless of the requested hashes.
type fakeStater struct {
	infos []downloader.TorrentInfo
	err   error
}

func (f *fakeStater) Torrents(_ context.Context, _ []string) ([]downloader.TorrentInfo, error) {
	return f.infos, f.err
}

func newGrabber(t *testing.T, s Settings, search *fakeSearcher, add *fakeAdder, own *fakeOwner) *Grabber {
	t.Helper()
	return newGrabberDC(t, s, search, add, own, &fakeStater{})
}

func newGrabberDC(t *testing.T, s Settings, search *fakeSearcher, add *fakeAdder, own *fakeOwner, dc *fakeStater) *Grabber {
	t.Helper()
	g := New(own, dc, add, func() Settings { return s }, nil, 0)
	g.newSearcher = func(_, _ string) searcher { return search }
	g.diskUsage = func(string) (int64, int64, error) { return 0, 100 << 30, nil }
	return g
}

func TestPollGrabsSelectedReleases(t *testing.T) {
	s := Settings{
		Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000},
		MaxGrabsPerCycle: 2,
	}
	search := &fakeSearcher{results: []prowlarr.Result{
		res("aaa", 5, 1<<30, false), res("bbb", 50, 1<<30, false), res("ccc", 1, 1<<30, false),
	}}
	add := &fakeAdder{}
	own := &fakeOwner{}
	g := newGrabber(t, s, search, add, own)

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if search.calls != 1 {
		t.Errorf("search called %d times; want 1", search.calls)
	}
	if len(add.added) != 2 {
		t.Fatalf("added %d; want 2 (%v)", len(add.added), add.added)
	}
}

func TestPollDisabledDoesNothing(t *testing.T) {
	search := &fakeSearcher{}
	add := &fakeAdder{}
	g := newGrabber(t, Settings{Enabled: false, MaxGrabsPerCycle: 5}, search, add, &fakeOwner{})
	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if search.calls != 0 || len(add.added) != 0 {
		t.Errorf("disabled grabber acted: calls=%d added=%d", search.calls, len(add.added))
	}
}

func TestPollSkipsWhenProwlarrUnconfigured(t *testing.T) {
	search := &fakeSearcher{}
	g := newGrabber(t, Settings{Enabled: true, ProwlarrURL: "", Categories: []int{2000}, MaxGrabsPerCycle: 5}, search, &fakeAdder{}, &fakeOwner{})
	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if search.calls != 0 {
		t.Errorf("searched despite unconfigured prowlarr")
	}
}

func TestPollSkipsAlreadyOwned(t *testing.T) {
	s := Settings{Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000}, MaxGrabsPerCycle: 5}
	search := &fakeSearcher{results: []prowlarr.Result{res("aaa", 5, 1<<30, false), res("bbb", 5, 1<<30, false)}}
	add := &fakeAdder{}
	own := &fakeOwner{owned: map[string]bool{"aaa": true}}
	g := newGrabber(t, s, search, add, own)

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(add.added) != 1 {
		t.Fatalf("added %d; want 1 (owned should be skipped)", len(add.added))
	}
}

func TestPollFailsClosedOnOwnedLookupError(t *testing.T) {
	s := Settings{Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000}, MaxGrabsPerCycle: 5}
	search := &fakeSearcher{results: []prowlarr.Result{res("aaa", 5, 1<<30, false)}}
	add := &fakeAdder{}
	own := &fakeOwner{err: errors.New("db locked")}
	g := newGrabber(t, s, search, add, own)

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(add.added) != 0 {
		t.Errorf("grabbed despite owned-lookup failure (should fail closed): %v", add.added)
	}
}

func TestPollFailsClosedOnDiskError(t *testing.T) {
	s := Settings{
		Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000},
		MaxGrabsPerCycle: 5, DiskPath: "/data", MaxDiskUsagePercent: 80,
	}
	search := &fakeSearcher{results: []prowlarr.Result{res("aaa", 5, 1<<30, false)}}
	add := &fakeAdder{}
	g := newGrabber(t, s, search, add, &fakeOwner{})
	g.diskUsage = func(string) (int64, int64, error) { return 0, 0, errors.New("stat failed") }

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(add.added) != 0 {
		t.Errorf("grabbed despite disk stat failure: %v", add.added)
	}
}

func resI(hash, indexer string, seeders int, size int64) prowlarr.Result {
	r := res(hash, seeders, size, false)
	r.Indexer = indexer
	return r
}

func TestSelectGrabsRoundRobinAcrossIndexers(t *testing.T) {
	const gb = 1 << 30
	// Indexer A holds the three best-seeded releases; without round-robin it
	// would take every slot. Budget 4 should still pull from B and C.
	results := []prowlarr.Result{
		resI("a1", "A", 100, gb), resI("a2", "A", 90, gb), resI("a3", "A", 80, gb),
		resI("b1", "B", 70, gb), resI("b2", "B", 60, gb),
		resI("c1", "C", 50, gb),
	}
	got := hashesOf(selectGrabs(results, Settings{MaxGrabsPerCycle: 4}, 0, 100*gb, nil))

	// Round 1: a1 (A), b1 (B), c1 (C); Round 2: a2 (A). One per indexer per
	// round, best-ranked first within each.
	want := []string{"a1", "b1", "c1", "a2"}
	if len(got) != len(want) {
		t.Fatalf("got %v; want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v; want %v", got, want)
		}
	}

	// Every indexer represented — the whole point of the change.
	seen := map[string]bool{"a1": true, "b1": true, "c1": true}
	for _, h := range []string{"a1", "b1", "c1"} {
		if !seen[h] {
			t.Errorf("expected indexer coverage to include %s", h)
		}
	}
}

func TestPollSkipsAtMaxActiveTorrents(t *testing.T) {
	s := Settings{
		Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000},
		MaxGrabsPerCycle: 5, MaxActiveTorrents: 2,
	}
	search := &fakeSearcher{results: []prowlarr.Result{res("aaa", 5, 1<<30, false)}}
	add := &fakeAdder{}
	own := &fakeOwner{all: []store.Torrent{{Hash: "x"}, {Hash: "y"}}} // already at limit
	g := newGrabber(t, s, search, add, own)

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if search.calls != 0 || len(add.added) != 0 {
		t.Errorf("grabbed at max active torrents: calls=%d added=%v", search.calls, add.added)
	}
}

func TestPollTrimsBudgetByActiveTorrents(t *testing.T) {
	s := Settings{
		Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000},
		MaxGrabsPerCycle: 5, MaxActiveTorrents: 4,
	}
	search := &fakeSearcher{results: []prowlarr.Result{
		res("aaa", 30, 1<<30, false), res("bbb", 20, 1<<30, false), res("ccc", 10, 1<<30, false),
	}}
	add := &fakeAdder{}
	own := &fakeOwner{all: []store.Torrent{{Hash: "x"}, {Hash: "y"}}} // room for 2 more
	g := newGrabber(t, s, search, add, own)

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(add.added) != 2 {
		t.Fatalf("added %d; want 2 (budget trimmed to remaining active-torrent room)", len(add.added))
	}
}

func TestPollSkipsAtMaxConcurrentDownloads(t *testing.T) {
	s := Settings{
		Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000},
		MaxGrabsPerCycle: 5, MaxConcurrentDownloads: 2,
	}
	search := &fakeSearcher{results: []prowlarr.Result{res("aaa", 5, 1<<30, false)}}
	add := &fakeAdder{}
	own := &fakeOwner{all: []store.Torrent{{Hash: "x"}, {Hash: "y"}}}
	// Both managed torrents are still downloading → at the concurrent limit.
	dc := &fakeStater{infos: []downloader.TorrentInfo{
		{Hash: "x", Progress: 0.5}, {Hash: "y", Progress: 0.2},
	}}
	g := newGrabberDC(t, s, search, add, own, dc)

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if search.calls != 0 || len(add.added) != 0 {
		t.Errorf("grabbed at max concurrent downloads: calls=%d added=%v", search.calls, add.added)
	}
}

func TestPollAbsoluteByteCapStopsGrabbing(t *testing.T) {
	s := Settings{
		Enabled: true, ProwlarrURL: "http://prowlarr", Categories: []int{2000},
		MaxGrabsPerCycle: 5, DiskPath: "/data",
		MaxDownloadStorageBytes: 3 << 30, // 3 GB cap
	}
	search := &fakeSearcher{results: []prowlarr.Result{res("aaa", 5, 1<<30, false)}}
	add := &fakeAdder{}
	g := newGrabber(t, s, search, add, &fakeOwner{})
	// Already 3 GB used → at/over the absolute cap.
	g.diskUsage = func(string) (int64, int64, error) { return 3 << 30, 100 << 30, nil }

	if err := g.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(add.added) != 0 {
		t.Errorf("grabbed past absolute storage cap: %v", add.added)
	}
}

func TestDiskLimitPrefersMoreRestrictive(t *testing.T) {
	const gb int64 = 1 << 30
	// percent gate: 80% of 100GB = 80GB; byte cap: 10GB → 10GB wins.
	if got := diskLimit(Settings{MaxDiskUsagePercent: 80, MaxDownloadStorageBytes: 10 * gb}, 100*gb); got != 10*gb {
		t.Errorf("diskLimit = %d, want %d (byte cap more restrictive)", got, 10*gb)
	}
	// byte cap larger than percent gate → percent wins.
	if got := diskLimit(Settings{MaxDiskUsagePercent: 50, MaxDownloadStorageBytes: 90 * gb}, 100*gb); got != 50*gb {
		t.Errorf("diskLimit = %d, want %d (percent more restrictive)", got, 50*gb)
	}
	// neither set → no gate.
	if got := diskLimit(Settings{}, 100*gb); got != 0 {
		t.Errorf("diskLimit = %d, want 0 (no gate)", got)
	}
}
