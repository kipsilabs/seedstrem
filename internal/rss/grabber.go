// Package rss implements a background grabber that periodically pulls the
// just-released items from the configured Prowlarr indexers and
// auto-downloads a filtered subset. Its purpose is twofold: to build
// seeding ratio (fresh, well-seeded, optionally freeleech releases), and
// to warm a cache so that when Stremio later requests that content the
// torrent is already downloading/complete and surfaces first as "ready".
//
// It reuses the existing on-demand machinery: Prowlarr's search endpoint
// (an empty query returns each indexer's recent releases), the
// prowlarr.Dedup/Filter/Sort ranking helpers, and torrents.Service to add
// downloads. Disk is bounded by the same disk-usage gate the stream path
// uses and by the seed-time cleanup sweep, which removes grabbed torrents
// once they have seeded long enough.
package rss

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kipsilabs/seedstrem/internal/diskusage"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/prowlarr"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

// Settings is the live configuration slice the grabber needs, fetched per
// poll so config hot-reload takes effect without a restart.
type Settings struct {
	Enabled        bool
	ProwlarrURL    string
	ProwlarrAPIKey string
	// Categories are the newznab category ids to poll, already combined
	// for whichever content types the addon serves.
	Categories []int
	// IndexerIDs scopes the poll to specific Prowlarr indexers; empty
	// means every enabled indexer.
	IndexerIDs []int
	// Filters constrains releases by seeders and size. Only MinSeeders is
	// inherited from the global filters.* (a ratio-safety floor); the size
	// bounds are populated from the independent rss.filters.* section.
	Filters prowlarr.Filters
	// FreeleechOnly restricts grabs to freeleech releases (ratio-safe).
	FreeleechOnly bool
	// IncludeKeywords keeps only releases whose title contains at least one
	// of these (case-insensitive substring). Empty allows all titles.
	IncludeKeywords []string
	// ExcludeKeywords drops releases whose title contains any of these
	// (case-insensitive substring). Exclude takes precedence over include.
	ExcludeKeywords []string
	// MaxGrabsPerCycle caps additions per poll. 0 disables grabbing.
	MaxGrabsPerCycle int
	// SearchTimeout is the global budget for the per-indexer fan-out
	// (prowlarr.Client.SearchEach); 0 waits for every indexer.
	SearchTimeout time.Duration
	// MaxConcurrentDownloads skips a cycle (or trims its budget) once this
	// many torrents are already downloading (progress < 1). 0 disables.
	MaxConcurrentDownloads int
	// MaxActiveTorrents skips a cycle (or trims its budget) once the store
	// holds at least this many torrents. 0 disables.
	MaxActiveTorrents int
	// DiskPath and MaxDiskUsagePercent gate grabbing on free disk space,
	// mirroring the stream handler's disk gate. 0 percent disables it.
	DiskPath            string
	MaxDiskUsagePercent int
	// MaxDownloadStorageBytes is an absolute cap on used download storage;
	// grabbing stops when used bytes reach it. Complements the percent gate
	// (the more restrictive wins). 0 disables.
	MaxDownloadStorageBytes int64
}

// searcher is the slice of *prowlarr.Client the grabber depends on. It fans
// out one request per indexer (SearchEach) so a slow or failing indexer
// can't blank the whole cycle and every indexer's recent releases are
// represented.
type searcher interface {
	SearchEach(ctx context.Context, query, searchType string, categories, indexerIDs []int, budget time.Duration) ([]prowlarr.Result, error)
}

// downloadAdder is the slice of *torrents.Service used to add a grabbed
// release. An empty Selector means no file is singled out, so the whole
// torrent downloads — exactly what seeding wants.
type downloadAdder interface {
	EnsureAdded(ctx context.Context, magnet string, torrentFile []byte, sel torrents.Selector) (store.Torrent, error)
}

// storeReader is the slice of *store.Store the grabber depends on:
// TorrentsByHashes reports which candidate infohashes are already stored
// (batched, keyed by lowercase infohash) so re-adds are skipped, and
// AllTorrents backs the active-torrent/concurrent-download admission gates.
type storeReader interface {
	TorrentsByHashes(ctx context.Context, hashes []string) (map[string]store.Torrent, error)
	AllTorrents(ctx context.Context) ([]store.Torrent, error)
}

// downloadStater is the slice of downloader.Client used to count how many
// managed torrents are still downloading, for the concurrent-download gate.
type downloadStater interface {
	Torrents(ctx context.Context, hashes []string) ([]downloader.TorrentInfo, error)
}

// Grabber periodically polls Prowlarr and grabs recent releases.
type Grabber struct {
	store    storeReader
	dc       downloadStater
	svc      downloadAdder
	settings func() Settings
	logger   *slog.Logger
	interval time.Duration

	// injectable for tests
	newSearcher func(url, apiKey string) searcher
	diskUsage   func(path string) (used, total int64, err error)
}

// New builds a Grabber. interval is fixed at startup (mirroring syncer and
// cleanup); changing rss.interval takes effect on the next restart. dc may
// be nil when the concurrent-download gate is never used.
func New(st storeReader, dc downloadStater, svc downloadAdder, settings func() Settings, logger *slog.Logger, interval time.Duration) *Grabber {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	return &Grabber{
		store:       st,
		dc:          dc,
		svc:         svc,
		settings:    settings,
		logger:      logger,
		interval:    interval,
		newSearcher: func(url, apiKey string) searcher { return prowlarr.New(url, apiKey) },
		diskUsage:   diskusage.Stat,
	}
}

// Run loops until ctx is cancelled, polling once per interval.
func (g *Grabber) Run(ctx context.Context) {
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.Poll(ctx); err != nil && ctx.Err() == nil {
				g.logger.Warn("rss poll failed", "error", err)
			}
		}
	}
}

// Poll performs one grab cycle: fetch recent releases, filter/rank them,
// and add the selected subset to the download client.
func (g *Grabber) Poll(ctx context.Context) error {
	s := g.settings()
	if !s.Enabled || s.MaxGrabsPerCycle <= 0 {
		return nil
	}
	if s.ProwlarrURL == "" {
		g.logger.Debug("rss: prowlarr not configured, skipping poll")
		return nil
	}
	if len(s.Categories) == 0 {
		g.logger.Debug("rss: no categories enabled, skipping poll")
		return nil
	}

	// Admission gates (RSS grabs only — on-demand streams are never
	// blocked): trim this cycle's budget by the number of active torrents
	// and in-flight downloads. maxGrabs starts at the configured cap and is
	// lowered by whichever gate has the least headroom; 0 headroom skips.
	maxGrabs, skip, err := g.gateBudget(ctx, s)
	if err != nil {
		// Fail closed: if we can't measure the limits, don't pile on.
		g.logger.Warn("rss: admission gate check failed; skipping grab cycle", "error", err)
		return nil
	}
	if skip {
		return nil
	}

	// An empty query triggers Prowlarr's recent-releases (RSS) behavior for
	// each indexer, reusing all the normalization/magnet-synthesis in the
	// existing client. SearchEach fans out per indexer so a slow/failing one
	// can't blank the cycle.
	results, err := g.newSearcher(s.ProwlarrURL, s.ProwlarrAPIKey).
		SearchEach(ctx, "", "search", s.Categories, s.IndexerIDs, s.SearchTimeout)
	if err != nil {
		return fmt.Errorf("rss: fetch recent releases: %w", err)
	}

	var used, total int64
	if s.MaxDiskUsagePercent > 0 || s.MaxDownloadStorageBytes > 0 {
		if s.DiskPath == "" {
			// A configured threshold with no measurable path can't gate
			// anything — surface it so an unbounded auto-downloader isn't
			// running with a silently-disabled disk gate.
			g.logger.Warn("rss: max_disk_usage_percent set but no local path mapping; disk gate disabled")
		} else {
			u, tot, derr := g.diskUsage(s.DiskPath)
			if derr != nil {
				// Fail closed: unlike serving an already-owned stream, adding
				// NEW downloads when free space can't be measured risks
				// filling the disk. Skip this cycle rather than grab blind.
				g.logger.Warn("rss: disk usage check failed; skipping grab cycle", "path", s.DiskPath, "error", derr)
				return nil
			}
			used, total = u, tot
		}
	}

	// One batched lookup of which candidates we already have, rather than a
	// per-candidate query. Fail closed: a store error means we can't tell
	// what's owned, so skip the cycle rather than risk re-downloading.
	hashes := make([]string, 0, len(results))
	for _, r := range results {
		if r.InfoHash != "" {
			hashes = append(hashes, r.InfoHash)
		}
	}
	owned, err := g.store.TorrentsByHashes(ctx, hashes)
	if err != nil {
		g.logger.Warn("rss: owned-lookup failed; skipping grab cycle", "error", err)
		return nil
	}
	have := func(hash string) bool {
		_, ok := owned[strings.ToLower(hash)]
		return ok
	}

	// Apply the gate-trimmed budget without mutating the live settings.
	sel := s
	sel.MaxGrabsPerCycle = maxGrabs
	grabs := selectGrabs(results, sel, used, total, have)
	if len(grabs) == 0 {
		g.logger.Debug("rss: nothing to grab this cycle", "candidates", len(results))
		return nil
	}

	added := 0
	for _, r := range grabs {
		if _, err := g.svc.EnsureAdded(ctx, r.MagnetURL, r.TorrentFile, torrents.Selector{Indexer: r.Indexer}); err != nil {
			g.logger.Warn("rss: grab failed", "title", r.Title, "hash", r.InfoHash, "error", err)
			continue
		}
		added++
		g.logger.Info("rss: grabbed release",
			"title", r.Title, "hash", r.InfoHash, "size", r.Size,
			"seeders", r.Seeders, "freeleech", r.Freeleech, "indexer", r.Indexer)
	}
	g.logger.Debug("rss: grab cycle complete", "candidates", len(results), "selected", len(grabs), "added", added)
	return nil
}

// gateBudget applies the RSS-only admission gates (max active torrents, max
// concurrent downloads) and returns the trimmed per-cycle grab budget. skip
// is true when a gate has no headroom (grab nothing this cycle). Both gates
// need the managed-torrent list, fetched once when either is enabled; when
// both are disabled the configured MaxGrabsPerCycle passes through untouched.
func (g *Grabber) gateBudget(ctx context.Context, s Settings) (maxGrabs int, skip bool, err error) {
	maxGrabs = s.MaxGrabsPerCycle
	if s.MaxActiveTorrents <= 0 && s.MaxConcurrentDownloads <= 0 {
		return maxGrabs, false, nil
	}

	stored, err := g.store.AllTorrents(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("active-torrent lookup: %w", err)
	}

	if s.MaxActiveTorrents > 0 {
		room := s.MaxActiveTorrents - len(stored)
		if room <= 0 {
			g.logger.Debug("rss: at max active torrents; skipping grab cycle",
				"active", len(stored), "limit", s.MaxActiveTorrents)
			return 0, true, nil
		}
		if room < maxGrabs {
			maxGrabs = room
		}
	}

	if s.MaxConcurrentDownloads > 0 {
		downloading, err := g.countDownloading(ctx, stored)
		if err != nil {
			return 0, false, fmt.Errorf("downloading-count lookup: %w", err)
		}
		room := s.MaxConcurrentDownloads - downloading
		if room <= 0 {
			g.logger.Debug("rss: at max concurrent downloads; skipping grab cycle",
				"downloading", downloading, "limit", s.MaxConcurrentDownloads)
			return 0, true, nil
		}
		if room < maxGrabs {
			maxGrabs = room
		}
	}
	return maxGrabs, false, nil
}

// countDownloading reports how many of the stored torrents are still
// downloading (Progress < 1), per the download client's live view.
func (g *Grabber) countDownloading(ctx context.Context, stored []store.Torrent) (int, error) {
	if len(stored) == 0 || g.dc == nil {
		return 0, nil
	}
	hashes := make([]string, len(stored))
	for i, t := range stored {
		hashes[i] = t.Hash
	}
	infos, err := g.dc.Torrents(ctx, hashes)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, in := range infos {
		if in.Progress < 1 {
			n++
		}
	}
	return n, nil
}

// selectGrabs is the pure decision core: given recent releases, live
// settings, and current disk usage, it returns the releases to grab this
// cycle. It dedups (by infohash, then by release title so the same release
// mirrored across indexers is grabbed once), applies the seeder/size
// filters, optionally keeps only freeleech, ranks (freeleech → seeders →
// size), then hands out the per-cycle budget round-robin across indexers so
// no single tracker dominates — skipping already-owned releases and any that
// would push disk usage past the limit, stopping at MaxGrabsPerCycle. have
// reports whether a release's infohash is already downloaded; a nil have
// treats nothing as owned.
func selectGrabs(results []prowlarr.Result, s Settings, used, total int64, have func(hash string) bool) []prowlarr.Result {
	if s.MaxGrabsPerCycle <= 0 {
		return nil
	}

	ranked := prowlarr.Filter(prowlarr.Dedup(results), s.Filters)
	ranked = filterByTitle(ranked, s.IncludeKeywords, s.ExcludeKeywords)
	if s.FreeleechOnly {
		fl := make([]prowlarr.Result, 0, len(ranked))
		for _, r := range ranked {
			if r.Freeleech {
				fl = append(fl, r)
			}
		}
		ranked = fl
	}
	ranked = prowlarr.Sort(ranked)
	// Collapse the same release listed on multiple indexers. Prowlarr's
	// infohash Dedup misses these because each tracker repacks the .torrent
	// (different trackers/comment ⇒ different infohash) for the same scene
	// release. Ranked is already best-first, so keeping the first occurrence
	// of each normalized title keeps the best-seeded/freeleech copy.
	ranked = dedupeByReleaseTitle(ranked)

	// Disk limit: bytes we must stay under. limit <= 0 means "no gate".
	limit := diskLimit(s, total)
	if limit > 0 && used >= limit {
		return nil // disk already at/over the limit: grab nothing
	}

	// Spread the budget across indexers: group the ranked candidates by
	// indexer (first-seen order, ranked within each group) and take one
	// grabbable release per indexer per round. Falls back to plain ranked
	// order when everything is on a single indexer.
	groups := groupByIndexer(ranked)
	out := make([]prowlarr.Result, 0, s.MaxGrabsPerCycle)
	projected := used
	cursors := make([]int, len(groups))
	for len(out) < s.MaxGrabsPerCycle {
		progressed := false
		for gi := range groups {
			if len(out) >= s.MaxGrabsPerCycle {
				break
			}
			grp := groups[gi]
			for cursors[gi] < len(grp) {
				r := grp[cursors[gi]]
				cursors[gi]++
				if r.InfoHash == "" {
					continue // nothing to dedup/track on
				}
				if have != nil && have(r.InfoHash) {
					continue // already downloaded
				}
				if limit > 0 && projected+r.Size > limit {
					continue // would push disk usage past the limit
				}
				out = append(out, r)
				projected += r.Size
				progressed = true
				break
			}
		}
		if !progressed {
			break // every group exhausted
		}
	}
	return out
}

// diskLimit resolves the effective byte ceiling from the percent gate and
// the absolute byte cap, returning the more restrictive of the two enabled
// limits (0 = no gate).
func diskLimit(s Settings, total int64) int64 {
	var limit int64
	if s.MaxDiskUsagePercent > 0 && total > 0 {
		limit = total * int64(s.MaxDiskUsagePercent) / 100
	}
	if s.MaxDownloadStorageBytes > 0 && (limit == 0 || s.MaxDownloadStorageBytes < limit) {
		limit = s.MaxDownloadStorageBytes
	}
	return limit
}

// groupByIndexer buckets ranked results by indexer, preserving the first-seen
// order of indexers and the ranked order within each bucket.
func groupByIndexer(ranked []prowlarr.Result) [][]prowlarr.Result {
	order := make(map[string]int, len(ranked))
	var groups [][]prowlarr.Result
	for _, r := range ranked {
		gi, ok := order[r.Indexer]
		if !ok {
			gi = len(groups)
			order[r.Indexer] = gi
			groups = append(groups, nil)
		}
		groups[gi] = append(groups[gi], r)
	}
	return groups
}

// filterByTitle applies the RSS-specific keyword gates to release titles,
// matched case-insensitively as substrings. A release is dropped if its
// title contains any exclude keyword (exclude wins), then kept only if it
// contains at least one include keyword. Empty lists disable their gate:
// no includes means "allow all", no excludes means "drop none".
func filterByTitle(results []prowlarr.Result, include, exclude []string) []prowlarr.Result {
	// Normalize each keyword list once (lower-case, trimmed, blanks dropped)
	// rather than per title comparison.
	inc := normalizeKeywords(include)
	exc := normalizeKeywords(exclude)
	if len(inc) == 0 && len(exc) == 0 {
		return results
	}
	out := make([]prowlarr.Result, 0, len(results))
	for _, r := range results {
		lower := strings.ToLower(r.Title)
		if containsAny(lower, exc) {
			continue
		}
		if len(inc) > 0 && !containsAny(lower, inc) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// normalizeKeywords lower-cases and trims each keyword, dropping blanks so a
// stray empty entry can't later match every title.
func normalizeKeywords(keywords []string) []string {
	out := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if kw = strings.TrimSpace(strings.ToLower(kw)); kw != "" {
			out = append(out, kw)
		}
	}
	return out
}

// containsAny reports whether lowered contains any of the (already
// normalized) keywords.
func containsAny(lowered string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(lowered, kw) {
			return true
		}
	}
	return false
}

// dedupeByReleaseTitle keeps only the first result for each normalized
// release title, preserving input order. Callers pass an already-ranked
// slice so the survivor is the best-ranked copy. Results whose title
// normalizes to empty are always kept (nothing reliable to group on).
func dedupeByReleaseTitle(results []prowlarr.Result) []prowlarr.Result {
	seen := make(map[string]struct{}, len(results))
	out := make([]prowlarr.Result, 0, len(results))
	for _, r := range results {
		key := normalizeReleaseTitle(r.Title)
		if key != "" {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, r)
	}
	return out
}

// normalizeReleaseTitle reduces a release name to a comparison key by
// lower-casing and keeping only alphanumerics. Scene/P2P release names are
// otherwise byte-identical across trackers, so this collapses separator and
// case differences ("The.Matrix.1999.1080p" vs "the matrix 1999 1080p")
// while keeping genuinely different releases (resolution, group, repack)
// distinct.
func normalizeReleaseTitle(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
