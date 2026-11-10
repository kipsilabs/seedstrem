// Package config defines the seedstrem configuration schema, YAML
// loading with environment-variable overrides, validation, and atomic
// persistence (used by the admin UI to save settings).
package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration document.
type Config struct {
	Server      Server      `yaml:"server"`
	Downloader  Downloader  `yaml:"downloader"`
	QBittorrent QBittorrent `yaml:"qbittorrent"`
	Deluge      Deluge      `yaml:"deluge"`
	Prowlarr    Prowlarr    `yaml:"prowlarr"`
	Addon       Addon       `yaml:"addon"`
	Filters     Filters     `yaml:"filters"`
	Meta        Meta        `yaml:"meta"`
	Paths       Paths       `yaml:"paths"`
	Storage     Storage     `yaml:"storage"`
	Stream      Stream      `yaml:"stream"`
	Cleanup     Cleanup     `yaml:"cleanup"`
	Seeding     Seeding     `yaml:"seeding"`
	RSS         RSS         `yaml:"rss"`
	Log         Log         `yaml:"log"`
}

type Server struct {
	Listen        string `yaml:"listen"`
	ExternalURL   string `yaml:"external_url"`
	AdminPassword string `yaml:"admin_password"`
}

// Downloader-type values accepted by downloader.type.
const (
	DownloaderQBittorrent = "qbittorrent"
	DownloaderDeluge      = "deluge"
)

// Downloader selects which download client seedstrem drives.
type Downloader struct {
	// Type is "qbittorrent" (default) or "deluge".
	Type string `yaml:"type"`
	// AdoptLabelled brings torrents added directly in the download
	// client under seedstrem's management when they carry its
	// label/category. Off by default: an adopted torrent is swept by
	// cleanup like any other, so enabling it is a deliberate choice.
	AdoptLabelled bool `yaml:"adopt_labelled"`
}

// ClientLabel returns the label the active download client tags
// seedstrem's torrents with — a Deluge label or a qBittorrent category
// depending on Downloader.Type.
func (c Config) ClientLabel() string {
	if c.Downloader.Type == DownloaderDeluge {
		return c.Deluge.Label
	}
	return c.QBittorrent.Category
}

// Deluge configures the connection to a Deluge 2 daemon (native RPC,
// not the web UI — the daemon port, 58846 by default).
type Deluge struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Label tags the torrents seedstrem adds (requires Deluge's Label
	// plugin; applied best-effort).
	Label string `yaml:"label"`
}

// QBittorrent configures the connection to the qBittorrent WebUI API.
type QBittorrent struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Category tags the torrents seedstrem adds, so they are
	// distinguishable on a shared qBittorrent instance.
	Category string `yaml:"category"`
}

// Prowlarr configures the torrent search backend.
type Prowlarr struct {
	URL             string `yaml:"url"`
	APIKey          string `yaml:"api_key"`
	MovieCategories []int  `yaml:"movie_categories"`
	TVCategories    []int  `yaml:"tv_categories"`
	AnimeCategories []int  `yaml:"anime_categories"`
	// IndexerIDs scopes searches to specific Prowlarr indexers. Empty
	// means search every enabled indexer.
	IndexerIDs []int `yaml:"indexer_ids"`
	// SearchTimeout is the global budget for a discovery search. Prowlarr
	// is queried one indexer per request concurrently; when this elapses,
	// indexers still in flight are abandoned and the results already
	// returned are used. 0 disables the budget (wait for every indexer).
	SearchTimeout time.Duration `yaml:"search_timeout"`
	// SearchCacheTTL is how long Stremio discovery search results are
	// served from memory before Prowlarr is queried again for the same
	// search. 0 disables caching (every request searches live).
	SearchCacheTTL time.Duration `yaml:"search_cache_ttl"`
}

// Addon toggles which Stremio content types the addon serves.
type Addon struct {
	EnableMovies bool `yaml:"enable_movies"`
	EnableSeries bool `yaml:"enable_series"`
	EnableAnime  bool `yaml:"enable_anime"`
}

// Filters constrains and ranks Prowlarr search results.
type Filters struct {
	MinSeeders int   `yaml:"min_seeders"`
	MinSizeMB  int64 `yaml:"min_size_mb"`
	MaxSizeMB  int64 `yaml:"max_size_mb"` // 0 = unbounded
	MaxResults int   `yaml:"max_results"`
}

// Meta configures metadata resolution (Cinemeta) and the qBittorrent
// metadata wait during resolve-on-play.
type Meta struct {
	CinemetaURL     string        `yaml:"cinemeta_url"`
	MetadataTimeout time.Duration `yaml:"metadata_timeout"`
	// TMDbAPIKey enables resolving IMDb ids to TMDb ids, for indexers
	// whose Prowlarr definition supports TmdbId search but not ImdbId.
	// Optional: those indexers fall back to free-text search without it.
	TMDbAPIKey string `yaml:"tmdb_api_key"`
}

// Mapping remaps a path prefix as seen by qBittorrent to a local path.
type Mapping struct {
	Remote string `yaml:"remote"`
	Local  string `yaml:"local"`
}

type Paths struct {
	Mappings []Mapping `yaml:"mappings"`
}

// FirstLocal returns the first configured local download root — the path
// whose filesystem usage gates streams and RSS grabs, and whose free
// space the admin API falls back to when the download client cannot
// report its own. Empty when no mapping is configured.
func (p Paths) FirstLocal() string {
	for _, m := range p.Mappings {
		if m.Local != "" {
			return m.Local
		}
	}
	return ""
}

type Storage struct {
	Database            string `yaml:"database"`
	DeleteFilesOnRemove bool   `yaml:"delete_files_on_remove"`
	// MaxDiskUsagePercent is the download-disk usage percentage (0..100)
	// at or above which the addon stops offering new streams; new
	// releases whose size would push usage past it are also filtered out.
	// 0 disables the gate. Already-downloaded/downloading content is never
	// affected. Usage is measured on the first configured paths.mappings
	// local root.
	MaxDiskUsagePercent int `yaml:"max_disk_usage_percent"`
	// MaxDownloadStorageGB is an absolute cap, in GB, on download-disk used
	// storage above which the RSS grabber stops adding new torrents. It
	// complements MaxDiskUsagePercent; whichever gate is more restrictive
	// wins. 0 disables the cap. Measured on the same first paths.mappings
	// local root and assumes a dedicated download volume. On-demand streams
	// are never blocked by this cap.
	MaxDownloadStorageGB int64 `yaml:"max_download_storage_gb"`
}

type Stream struct {
	WaitTimeout time.Duration `yaml:"wait_timeout"`
	ReadChunk   int64         `yaml:"read_chunk"`
}

// Delete-order policies for cleanup eviction. DeletePolicyOldestFirst
// removes the torrent added earliest; DeletePolicyLowestUpload removes the
// torrent that has uploaded the least.
const (
	DeletePolicyOldestFirst  = "oldest_first"
	DeletePolicyLowestUpload = "lowest_upload"
)

// Cleanup configures automatic removal of torrents that are no longer
// worth keeping around.
type Cleanup struct {
	// SeedTime is how long a completed torrent may seed before it is
	// removed. 0 disables seed-time cleanup entirely.
	SeedTime time.Duration `yaml:"seed_time"`
	// MinProgressForCancel is the download fraction (0..1) a torrent
	// must reach to survive an abandoned/canceled playback session.
	MinProgressForCancel float64 `yaml:"min_progress_for_cancel"`
	// TargetRatio is the seeding ratio at or above which a completed
	// torrent becomes removal-eligible, OR-ed with SeedTime (either
	// satisfying makes it eligible). 0 disables the ratio trigger.
	TargetRatio float64 `yaml:"target_ratio"`
	// DeletePolicy sets the order in which eligible torrents are removed:
	// "oldest_first" (default) or "lowest_upload".
	DeletePolicy string `yaml:"delete_policy"`
	// IndexerSeedTimes overrides SeedTime per Prowlarr indexer name —
	// private trackers impose minimum seed times that public ones do not.
	// Matching is case-insensitive; an indexer with no entry (including a
	// torrent whose indexer is unknown) uses SeedTime. A value of 0
	// disables seed-time removal for that indexer.
	IndexerSeedTimes map[string]time.Duration `yaml:"indexer_seed_times"`
	// HistoryRetention is how long removed torrents stay in History,
	// together with their saved .torrent files (so they can be re-added).
	HistoryRetention time.Duration `yaml:"history_retention"`
}

// EffectiveSeedTime returns the seed time governing a torrent grabbed from
// the named indexer: the per-indexer override when one exists, otherwise
// the global one. Matching is case-insensitive and whitespace-trimmed; an
// empty indexer always resolves to the global value. Shared by the cleanup
// loop and the admin torrent listing so both report the same number.
func EffectiveSeedTime(global time.Duration, overrides map[string]time.Duration, indexer string) time.Duration {
	key := strings.ToLower(strings.TrimSpace(indexer))
	if key == "" {
		return global
	}
	for name, d := range overrides {
		if strings.ToLower(strings.TrimSpace(name)) == key {
			return d
		}
	}
	return global
}

// HasSeedTimeTrigger reports whether seed-time removal is enabled at all:
// globally, or for at least one indexer. A global seed time of 0 does not
// disable cleanup when a per-indexer override is positive.
func HasSeedTimeTrigger(global time.Duration, overrides map[string]time.Duration) bool {
	if global > 0 {
		return true
	}
	for _, d := range overrides {
		if d > 0 {
			return true
		}
	}
	return false
}

// EffectiveSeedTime resolves the seed time for a torrent from the named
// indexer against this Cleanup config.
func (c Cleanup) EffectiveSeedTime(indexer string) time.Duration {
	return EffectiveSeedTime(c.SeedTime, c.IndexerSeedTimes, indexer)
}

// Seeding controls download/seed behavior for ratio management.
type Seeding struct {
	// Full downloads the entire torrent (not just the played file) so a
	// complete copy is available to seed — better for ratio on private
	// trackers. The played file is still prioritized so streaming starts
	// first; the rest downloads afterwards. When false, only the played
	// file is downloaded (minimal disk, but you seed only that file).
	Full bool `yaml:"full"`
}

// RSS configures the background grabber that periodically pulls
// just-released items from the Prowlarr indexers and auto-downloads a
// filtered subset — primarily to build seeding ratio, and secondarily so
// the content is already cached when a Stremio stream request arrives.
// Scope (indexers/categories) and quality/size/seeder filters are reused
// from the prowlarr.* and filters.* sections rather than duplicated.
type RSS struct {
	// Enabled turns the grabber on. Off by default: auto-downloading is a
	// deliberate departure from seedstrem's on-demand model.
	Enabled bool `yaml:"enabled"`
	// Interval is how often the grabber polls Prowlarr for recent
	// releases. Applied at startup (changing it takes effect on restart).
	Interval time.Duration `yaml:"interval"`
	// MaxGrabsPerCycle caps how many new releases are added per poll, to
	// bound the firehose. 0 disables grabbing (nothing is added).
	MaxGrabsPerCycle int `yaml:"max_grabs_per_cycle"`
	// FreeleechOnly restricts grabs to freeleech releases, whose download
	// does not count against ratio — the safest way to build ratio.
	FreeleechOnly bool `yaml:"freeleech_only"`
	// Filters constrains which releases the grabber picks up, independent of
	// the global filters.* used by on-demand stream search. The seeder floor
	// is still inherited from filters.min_seeders (a ratio-safety default);
	// only size/category/title are RSS-specific here.
	Filters RSSFilters `yaml:"filters"`
	// MaxConcurrentDownloads caps how many torrents may be actively
	// downloading (progress < 1) before the grabber skips a cycle rather
	// than piling on more. 0 disables the gate. Applies to RSS grabs only.
	MaxConcurrentDownloads int `yaml:"max_concurrent_downloads"`
	// MaxActiveTorrents caps the total number of managed torrents; the
	// grabber skips a cycle once the store holds at least this many.
	// 0 disables the gate. Applies to RSS grabs only.
	MaxActiveTorrents int `yaml:"max_active_torrents"`
}

// RSSFilters holds the RSS-grabber-specific filtering knobs. All are
// optional: a zero value means "no restriction" (grab-everything, matching
// the pre-filters behavior).
type RSSFilters struct {
	// MinSizeMB / MaxSizeMB bound release size in MiB. 0 min = no lower
	// bound; 0 max = unbounded.
	MinSizeMB int64 `yaml:"min_size_mb"`
	MaxSizeMB int64 `yaml:"max_size_mb"`
	// Categories restricts the poll to these raw newznab category ids.
	// Empty means "all enabled content types" (the addon-derived default).
	Categories []int `yaml:"categories"`
	// IncludeKeywords keeps only releases whose title contains at least one
	// of these (case-insensitive substring). Empty means allow all titles.
	IncludeKeywords []string `yaml:"include_keywords"`
	// ExcludeKeywords drops releases whose title contains any of these
	// (case-insensitive substring). Exclude takes precedence over include.
	ExcludeKeywords []string `yaml:"exclude_keywords"`
}

type Log struct {
	Level string `yaml:"level"`
}

// Default returns the configuration used when no file or overrides exist.
func Default() Config {
	return Config{
		Server: Server{
			Listen:      ":8080",
			ExternalURL: "http://localhost:8080",
		},
		Downloader: Downloader{Type: DownloaderQBittorrent},
		QBittorrent: QBittorrent{
			URL:      "http://qbittorrent:8080",
			Username: "admin",
			Category: "seedstrem",
		},
		Deluge: Deluge{
			Host:     "deluge",
			Port:     58846,
			Username: "localclient",
			Label:    "seedstrem",
		},
		Prowlarr: Prowlarr{
			MovieCategories: []int{2000},
			TVCategories:    []int{5000},
			AnimeCategories: []int{5070},
			SearchTimeout:   15 * time.Second,
			SearchCacheTTL:  5 * time.Minute,
		},
		Addon: Addon{
			EnableMovies: true,
			EnableSeries: true,
			EnableAnime:  false,
		},
		Filters: Filters{
			MinSeeders: 1,
			MaxResults: 20,
		},
		Meta: Meta{
			CinemetaURL:     "https://v3-cinemeta.strem.io",
			MetadataTimeout: 60 * time.Second,
		},
		Paths: Paths{
			Mappings: []Mapping{{Remote: "/downloads", Local: "/data"}},
		},
		Storage: Storage{
			Database:            "seedstrem.db",
			DeleteFilesOnRemove: true,
		},
		Stream: Stream{
			WaitTimeout: 60 * time.Second,
			ReadChunk:   4 << 20,
		},
		Cleanup: Cleanup{
			SeedTime:             72 * time.Hour,
			MinProgressForCancel: 0.05,
			DeletePolicy:         DeletePolicyOldestFirst,
			HistoryRetention:     30 * 24 * time.Hour,
		},
		Seeding: Seeding{Full: true},
		RSS: RSS{
			Enabled:          false,
			Interval:         15 * time.Minute,
			MaxGrabsPerCycle: 5,
			FreeleechOnly:    false,
		},
		Log: Log{Level: "info"},
	}
}

// Load reads the config file at path (if it exists), applies environment
// overrides, and validates the result. A missing file is not an error:
// defaults + env are used.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// fine: defaults + env
	default:
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	applyEnv(&cfg, os.Getenv)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overrides fields from SEEDSTREM_* variables. getenv is
// injectable for tests.
func applyEnv(cfg *Config, getenv func(string) string) {
	set := func(key string, dst *string) {
		if v := getenv("SEEDSTREM_" + key); v != "" {
			*dst = v
		}
	}
	set("SERVER_LISTEN", &cfg.Server.Listen)
	set("SERVER_EXTERNAL_URL", &cfg.Server.ExternalURL)
	set("SERVER_ADMIN_PASSWORD", &cfg.Server.AdminPassword)
	set("DOWNLOADER_TYPE", &cfg.Downloader.Type)
	set("QBITTORRENT_URL", &cfg.QBittorrent.URL)
	set("QBITTORRENT_USERNAME", &cfg.QBittorrent.Username)
	set("QBITTORRENT_PASSWORD", &cfg.QBittorrent.Password)
	set("QBITTORRENT_CATEGORY", &cfg.QBittorrent.Category)
	set("DELUGE_HOST", &cfg.Deluge.Host)
	set("DELUGE_USERNAME", &cfg.Deluge.Username)
	set("DELUGE_PASSWORD", &cfg.Deluge.Password)
	set("DELUGE_LABEL", &cfg.Deluge.Label)
	if v := getenv("SEEDSTREM_DELUGE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Deluge.Port = n
		}
	}
	if v := getenv("SEEDSTREM_STORAGE_MAX_DISK_USAGE_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Storage.MaxDiskUsagePercent = n
		}
	}
	if v := getenv("SEEDSTREM_STORAGE_MAX_DOWNLOAD_STORAGE_GB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Storage.MaxDownloadStorageGB = n
		}
	}
	set("PROWLARR_URL", &cfg.Prowlarr.URL)
	set("PROWLARR_API_KEY", &cfg.Prowlarr.APIKey)
	set("META_CINEMETA_URL", &cfg.Meta.CinemetaURL)
	set("META_TMDB_API_KEY", &cfg.Meta.TMDbAPIKey)
	set("STORAGE_DATABASE", &cfg.Storage.Database)
	set("LOG_LEVEL", &cfg.Log.Level)

	if v := getenv("SEEDSTREM_STREAM_WAIT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Stream.WaitTimeout = d
		}
	}
	if v := getenv("SEEDSTREM_META_METADATA_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Meta.MetadataTimeout = d
		}
	}
	if v := getenv("PROWLARR_SEARCH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Prowlarr.SearchTimeout = d
		}
	}
	if v := getenv("PROWLARR_SEARCH_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Prowlarr.SearchCacheTTL = d
		}
	}
	if v := getenv("SEEDSTREM_CLEANUP_SEED_TIME"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Cleanup.SeedTime = d
		}
	}
	if v := getenv("SEEDSTREM_CLEANUP_INDEXER_SEED_TIMES"); v != "" {
		cfg.Cleanup.IndexerSeedTimes = parseIndexerSeedTimes(v)
	}
	if v := getenv("SEEDSTREM_CLEANUP_MIN_PROGRESS_FOR_CANCEL"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Cleanup.MinProgressForCancel = f
		}
	}
	if v := getenv("SEEDSTREM_CLEANUP_TARGET_RATIO"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Cleanup.TargetRatio = f
		}
	}
	set("CLEANUP_DELETE_POLICY", &cfg.Cleanup.DeletePolicy)
	setBool := func(key string, dst *bool) {
		if v := getenv("SEEDSTREM_" + key); v != "" {
			*dst = v == "1" || strings.EqualFold(v, "true")
		}
	}
	setBool("ADDON_ENABLE_MOVIES", &cfg.Addon.EnableMovies)
	setBool("ADDON_ENABLE_SERIES", &cfg.Addon.EnableSeries)
	setBool("ADDON_ENABLE_ANIME", &cfg.Addon.EnableAnime)
	setBool("DOWNLOADER_ADOPT_LABELLED", &cfg.Downloader.AdoptLabelled)
	setBool("SEEDING_FULL", &cfg.Seeding.Full)
	setBool("RSS_ENABLED", &cfg.RSS.Enabled)
	setBool("RSS_FREELEECH_ONLY", &cfg.RSS.FreeleechOnly)
	if v := getenv("SEEDSTREM_RSS_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RSS.Interval = d
		}
	}
	if v := getenv("SEEDSTREM_RSS_MAX_GRABS_PER_CYCLE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RSS.MaxGrabsPerCycle = n
		}
	}
	if v := getenv("SEEDSTREM_RSS_MAX_CONCURRENT_DOWNLOADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RSS.MaxConcurrentDownloads = n
		}
	}
	if v := getenv("SEEDSTREM_RSS_MAX_ACTIVE_TORRENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RSS.MaxActiveTorrents = n
		}
	}
	// Comma-separated int lists, e.g. "2000,2010".
	setInts := func(key string, dst *[]int) {
		v := getenv("SEEDSTREM_" + key)
		if v == "" {
			return
		}
		var out []int
		for _, part := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				out = append(out, n)
			}
		}
		if len(out) > 0 {
			*dst = out
		}
	}
	setInts("PROWLARR_MOVIE_CATEGORIES", &cfg.Prowlarr.MovieCategories)
	setInts("PROWLARR_TV_CATEGORIES", &cfg.Prowlarr.TVCategories)
	setInts("PROWLARR_ANIME_CATEGORIES", &cfg.Prowlarr.AnimeCategories)
	setInts("PROWLARR_INDEXER_IDS", &cfg.Prowlarr.IndexerIDs)
	// Comma-separated string lists, e.g. "1080p,2160p". Blank parts are
	// dropped; an all-blank value leaves the destination untouched.
	setStrings := func(key string, dst *[]string) {
		v := getenv("SEEDSTREM_" + key)
		if v == "" {
			return
		}
		var out []string
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			*dst = out
		}
	}
	if v := getenv("SEEDSTREM_RSS_FILTERS_MIN_SIZE_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.RSS.Filters.MinSizeMB = n
		}
	}
	if v := getenv("SEEDSTREM_RSS_FILTERS_MAX_SIZE_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.RSS.Filters.MaxSizeMB = n
		}
	}
	setInts("RSS_FILTERS_CATEGORIES", &cfg.RSS.Filters.Categories)
	setStrings("RSS_FILTERS_INCLUDE_KEYWORDS", &cfg.RSS.Filters.IncludeKeywords)
	setStrings("RSS_FILTERS_EXCLUDE_KEYWORDS", &cfg.RSS.Filters.ExcludeKeywords)
	// SEEDSTREM_PATHS_MAPPINGS: comma-separated "remote:local" pairs,
	// e.g. "/downloads:/data,/media:/mnt/media".
	if v := getenv("SEEDSTREM_PATHS_MAPPINGS"); v != "" {
		var mappings []Mapping
		for _, pair := range strings.Split(v, ",") {
			remote, local, ok := strings.Cut(strings.TrimSpace(pair), ":")
			if ok && remote != "" && local != "" {
				mappings = append(mappings, Mapping{Remote: remote, Local: local})
			}
		}
		if len(mappings) > 0 {
			cfg.Paths.Mappings = mappings
		}
	}
}

// parseIndexerSeedTimes parses the SEEDSTREM_CLEANUP_INDEXER_SEED_TIMES
// form "Indexer=72h,Other=240h" into a map. Like the other env parsers it
// skips malformed entries rather than failing startup: a blank name or an
// unparseable duration drops just that entry.
func parseIndexerSeedTimes(v string) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, entry := range strings.Split(v, ",") {
		name, dur, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		d, err := time.ParseDuration(strings.TrimSpace(dur))
		if err != nil {
			continue
		}
		out[name] = d
	}
	return out
}

// Validate returns all problems found, joined into one error.
func (c Config) Validate() error {
	var errs []error
	if c.Server.Listen == "" {
		errs = append(errs, errors.New("server.listen must not be empty"))
	}
	if c.Server.ExternalURL == "" {
		errs = append(errs, errors.New("server.external_url must not be empty"))
	} else if !strings.HasPrefix(c.Server.ExternalURL, "http://") && !strings.HasPrefix(c.Server.ExternalURL, "https://") {
		errs = append(errs, fmt.Errorf("server.external_url must start with http:// or https://, got %q", c.Server.ExternalURL))
	}
	switch c.Downloader.Type {
	// An empty type (config predating the downloader section, or a
	// zero-value Config) means qBittorrent, matching the factory.
	case "", DownloaderQBittorrent:
		if c.QBittorrent.URL == "" {
			errs = append(errs, errors.New("qbittorrent.url must not be empty"))
		} else if !strings.HasPrefix(c.QBittorrent.URL, "http://") && !strings.HasPrefix(c.QBittorrent.URL, "https://") {
			errs = append(errs, fmt.Errorf("qbittorrent.url must start with http:// or https://, got %q", c.QBittorrent.URL))
		}
	case DownloaderDeluge:
		if c.Deluge.Host == "" {
			errs = append(errs, errors.New("deluge.host must not be empty"))
		}
		if c.Deluge.Port <= 0 || c.Deluge.Port > 65535 {
			errs = append(errs, fmt.Errorf("deluge.port must be between 1 and 65535, got %d", c.Deluge.Port))
		}
	default:
		errs = append(errs, fmt.Errorf("downloader.type must be %q or %q, got %q",
			DownloaderQBittorrent, DownloaderDeluge, c.Downloader.Type))
	}
	if c.Storage.Database == "" {
		errs = append(errs, errors.New("storage.database must not be empty"))
	}
	if c.Storage.MaxDiskUsagePercent < 0 || c.Storage.MaxDiskUsagePercent > 100 {
		errs = append(errs, fmt.Errorf("storage.max_disk_usage_percent must be between 0 and 100 (0 disables), got %d", c.Storage.MaxDiskUsagePercent))
	}
	if c.Storage.MaxDownloadStorageGB < 0 {
		errs = append(errs, fmt.Errorf("storage.max_download_storage_gb must not be negative (0 disables the cap), got %d", c.Storage.MaxDownloadStorageGB))
	}
	if c.Stream.WaitTimeout <= 0 {
		errs = append(errs, errors.New("stream.wait_timeout must be positive"))
	}
	// Prowlarr may be unconfigured on first boot (set later via the web
	// UI), so only the format is validated here — not presence.
	if c.Prowlarr.URL != "" && !strings.HasPrefix(c.Prowlarr.URL, "http://") && !strings.HasPrefix(c.Prowlarr.URL, "https://") {
		errs = append(errs, fmt.Errorf("prowlarr.url must start with http:// or https://, got %q", c.Prowlarr.URL))
	}
	if c.Meta.CinemetaURL != "" && !strings.HasPrefix(c.Meta.CinemetaURL, "http://") && !strings.HasPrefix(c.Meta.CinemetaURL, "https://") {
		errs = append(errs, fmt.Errorf("meta.cinemeta_url must start with http:// or https://, got %q", c.Meta.CinemetaURL))
	}
	if c.Meta.MetadataTimeout <= 0 {
		errs = append(errs, errors.New("meta.metadata_timeout must be positive"))
	}
	if c.Filters.MinSeeders < 0 {
		errs = append(errs, errors.New("filters.min_seeders must not be negative"))
	}
	if c.Filters.MinSizeMB < 0 || c.Filters.MaxSizeMB < 0 {
		errs = append(errs, errors.New("filters.min_size_mb and max_size_mb must not be negative"))
	}
	if c.Filters.MaxSizeMB != 0 && c.Filters.MaxSizeMB < c.Filters.MinSizeMB {
		errs = append(errs, errors.New("filters.max_size_mb must be >= min_size_mb (or 0 for unbounded)"))
	}
	if c.Filters.MaxResults < 0 {
		errs = append(errs, errors.New("filters.max_results must not be negative"))
	}
	if c.Stream.ReadChunk <= 0 {
		errs = append(errs, errors.New("stream.read_chunk must be positive"))
	}
	if c.Cleanup.SeedTime < 0 {
		errs = append(errs, errors.New("cleanup.seed_time must not be negative (0 disables seed-time cleanup)"))
	}
	if c.Cleanup.HistoryRetention <= 0 {
		errs = append(errs, errors.New("cleanup.history_retention must be positive"))
	}
	// Sorted so the error text is deterministic across map iterations.
	seenIndexers := make(map[string]string, len(c.Cleanup.IndexerSeedTimes))
	for _, name := range slices.Sorted(maps.Keys(c.Cleanup.IndexerSeedTimes)) {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			errs = append(errs, errors.New("cleanup.indexer_seed_times has an entry with a blank indexer name"))
			continue
		}
		// Lookup is case-insensitive, so two keys differing only in case
		// would make the effective seed time depend on map iteration order.
		if first, dup := seenIndexers[key]; dup {
			errs = append(errs, fmt.Errorf("cleanup.indexer_seed_times has duplicate entries for indexer %q and %q (matching is case-insensitive)", first, name))
			continue
		}
		seenIndexers[key] = name
		if c.Cleanup.IndexerSeedTimes[name] < 0 {
			errs = append(errs, fmt.Errorf("cleanup.indexer_seed_times[%q] must not be negative (0 disables seed-time cleanup for that indexer)", name))
		}
	}
	if c.Cleanup.MinProgressForCancel < 0 || c.Cleanup.MinProgressForCancel > 1 {
		errs = append(errs, errors.New("cleanup.min_progress_for_cancel must be between 0 and 1"))
	}
	if c.Cleanup.TargetRatio < 0 {
		errs = append(errs, errors.New("cleanup.target_ratio must not be negative (0 disables the ratio trigger)"))
	}
	switch c.Cleanup.DeletePolicy {
	// Empty means the Default() value (oldest_first); accept it so a config
	// predating this field still validates.
	case "", DeletePolicyOldestFirst, DeletePolicyLowestUpload:
	default:
		errs = append(errs, fmt.Errorf("cleanup.delete_policy must be %q or %q, got %q",
			DeletePolicyOldestFirst, DeletePolicyLowestUpload, c.Cleanup.DeletePolicy))
	}
	if c.RSS.Enabled && c.RSS.Interval <= 0 {
		errs = append(errs, errors.New("rss.interval must be positive when rss.enabled is true"))
	}
	if c.RSS.Filters.MinSizeMB < 0 || c.RSS.Filters.MaxSizeMB < 0 {
		errs = append(errs, errors.New("rss.filters.min_size_mb and max_size_mb must not be negative"))
	}
	if c.RSS.Filters.MaxSizeMB != 0 && c.RSS.Filters.MaxSizeMB < c.RSS.Filters.MinSizeMB {
		errs = append(errs, errors.New("rss.filters.max_size_mb must be >= min_size_mb (or 0 for unbounded)"))
	}
	if c.RSS.MaxGrabsPerCycle < 0 {
		errs = append(errs, errors.New("rss.max_grabs_per_cycle must not be negative (0 disables grabbing)"))
	}
	if c.RSS.MaxConcurrentDownloads < 0 {
		errs = append(errs, errors.New("rss.max_concurrent_downloads must not be negative (0 disables the gate)"))
	}
	if c.RSS.MaxActiveTorrents < 0 {
		errs = append(errs, errors.New("rss.max_active_torrents must not be negative (0 disables the gate)"))
	}
	for i, m := range c.Paths.Mappings {
		switch {
		case m.Remote == "" || m.Local == "":
			errs = append(errs, fmt.Errorf("paths.mappings[%d]: remote and local must both be set", i))
		case !filepath.IsAbs(m.Local):
			errs = append(errs, fmt.Errorf("paths.mappings[%d]: local %q must be an absolute path", i, m.Local))
		case strings.Contains(m.Local, ".."):
			errs = append(errs, fmt.Errorf("paths.mappings[%d]: local %q must not contain ..", i, m.Local))
		}
	}
	return errors.Join(errs...)
}

// Save writes the config to path atomically (temp file + rename).
func Save(cfg Config, path string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename config into place: %w", err)
	}
	return nil
}

const tokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// GenerateSecret returns a cryptographically random alphanumeric string.
func GenerateSecret(length int) (string, error) {
	var sb strings.Builder
	sb.Grow(length)
	max := big.NewInt(int64(len(tokenAlphabet)))
	for range length {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate secret: %w", err)
		}
		sb.WriteByte(tokenAlphabet[n.Int64()])
	}
	return sb.String(), nil
}

// EnsureSecrets fills in admin_password if empty.
// It reports whether the config was modified.
func EnsureSecrets(cfg *Config) (bool, error) {
	changed := false
	if cfg.Server.AdminPassword == "" {
		pw, err := GenerateSecret(16)
		if err != nil {
			return false, err
		}
		cfg.Server.AdminPassword = pw
		changed = true
	}
	return changed, nil
}
