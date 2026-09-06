// Package admin serves the internal REST API used by the web UI:
// session login, configuration management, and status/torrent views.
package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/deluge"
	"github.com/kipsilabs/seedstrem/internal/diskusage"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/prowlarr"
	"github.com/kipsilabs/seedstrem/internal/qbit"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

const passwordMask = "••••••••"

// Handler serves the admin API.
type Handler struct {
	config    *config.Manager
	store     *store.Store
	dc        *downloader.Swappable
	svc       *torrents.Service
	newClient func(config.Config) downloader.Client
	logger    *slog.Logger
	version   string

	// diskUsage reports (used, total) bytes for a local path. Injectable
	// for tests; defaults to diskusage.Stat. Used only as the fallback
	// when the download client cannot report its own free space.
	diskUsage func(path string) (used, total int64, err error)
}

// New creates the admin handler. dc must be the swappable client so
// download-client connection settings apply live; newClient builds a
// client for a given config (injected so main owns backend selection). svc is
// used to remove torrents on request.
func New(cm *config.Manager, st *store.Store, dc *downloader.Swappable, svc *torrents.Service, newClient func(config.Config) downloader.Client, version string, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{config: cm, store: st, dc: dc, svc: svc, newClient: newClient, logger: logger, version: version, diskUsage: diskusage.Stat}
}

// Router returns the router to mount at /api.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	r.Post("/session", h.login)
	r.Group(func(r chi.Router) {
		r.Use(h.requireSession)
		r.Delete("/session", h.logout)
		r.Get("/session", h.sessionInfo)
		r.Get("/config", h.getConfig)
		r.Put("/config", h.putConfig)
		r.Post("/config/test-qbittorrent", h.testQbittorrent)
		r.Post("/config/test-deluge", h.testDeluge)
		r.Post("/config/test-prowlarr", h.testProwlarr)
		r.Post("/config/prowlarr-indexers", h.prowlarrIndexers)
		r.Get("/status", h.status)
		r.Get("/torrents", h.torrents)
		r.Get("/deletions", h.deletions)
		r.Delete("/torrents/{id}", h.deleteTorrent)
	})
	return r
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	want := h.config.Get().Server.AdminPassword
	if want == "" || subtle.ConstantTimeCompare([]byte(body.Password), []byte(want)) != 1 {
		writeJSONError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	expiry := time.Now().Add(sessionLifetime)
	setSessionCookie(w, mintSession(want, expiry), expiry)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) logout(w http.ResponseWriter, _ *http.Request) {
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) sessionInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

// configDTO is the JSON shape exchanged with the UI. The qBittorrent and
// admin passwords are masked on read; sending the mask back (or an
// empty string) keeps the stored value.
type configDTO struct {
	Server struct {
		Listen        string `json:"listen"`
		ExternalURL   string `json:"external_url"`
		AdminPassword string `json:"admin_password"`
	} `json:"server"`
	Downloader struct {
		Type string `json:"type"`
	} `json:"downloader"`
	QBittorrent struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Category string `json:"category"`
	} `json:"qbittorrent"`
	Deluge struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		Label    string `json:"label"`
	} `json:"deluge"`
	Prowlarr struct {
		URL             string `json:"url"`
		APIKey          string `json:"api_key"`
		MovieCategories []int  `json:"movie_categories"`
		TVCategories    []int  `json:"tv_categories"`
		AnimeCategories []int  `json:"anime_categories"`
		IndexerIDs      []int  `json:"indexer_ids"`
		// SearchTimeoutSeconds is the global search budget; 0 on write keeps
		// the stored value (a search needs a positive budget).
		SearchTimeoutSeconds int `json:"search_timeout_seconds"`
	} `json:"prowlarr"`
	Addon struct {
		EnableMovies bool `json:"enable_movies"`
		EnableSeries bool `json:"enable_series"`
		EnableAnime  bool `json:"enable_anime"`
	} `json:"addon"`
	Filters struct {
		MinSeeders int   `json:"min_seeders"`
		MinSizeMB  int64 `json:"min_size_mb"`
		MaxSizeMB  int64 `json:"max_size_mb"`
		MaxResults int   `json:"max_results"`
	} `json:"filters"`
	Meta struct {
		CinemetaURL            string `json:"cinemeta_url"`
		MetadataTimeoutSeconds int    `json:"metadata_timeout_seconds"`
		TMDbAPIKey             string `json:"tmdb_api_key"`
	} `json:"meta"`
	Paths struct {
		Mappings []config.Mapping `json:"mappings"`
	} `json:"paths"`
	Storage struct {
		Database             string `json:"database"`
		DeleteFilesOnRemove  bool   `json:"delete_files_on_remove"`
		MaxDiskUsagePercent  int    `json:"max_disk_usage_percent"`
		MaxDownloadStorageGB int64  `json:"max_download_storage_gb"`
	} `json:"storage"`
	Stream struct {
		WaitTimeoutSeconds int   `json:"wait_timeout_seconds"`
		ReadChunk          int64 `json:"read_chunk"`
	} `json:"stream"`
	Cleanup struct {
		SeedTimeHours               int               `json:"seed_time_hours"`
		MinProgressForCancelPercent int               `json:"min_progress_for_cancel_percent"`
		TargetRatio                 float64           `json:"target_ratio"`
		DeletePolicy                string            `json:"delete_policy"`
		IndexerSeedTimes            []indexerSeedTime `json:"indexer_seed_times"`
	} `json:"cleanup"`
	Seeding struct {
		Full bool `json:"full"`
	} `json:"seeding"`
	RSS struct {
		Enabled                bool `json:"enabled"`
		IntervalMinutes        int  `json:"interval_minutes"`
		MaxGrabsPerCycle       int  `json:"max_grabs_per_cycle"`
		MaxConcurrentDownloads int  `json:"max_concurrent_downloads"`
		MaxActiveTorrents      int  `json:"max_active_torrents"`
		FreeleechOnly          bool `json:"freeleech_only"`
		Filters                struct {
			MinSizeMB       int64    `json:"min_size_mb"`
			MaxSizeMB       int64    `json:"max_size_mb"`
			Categories      []int    `json:"categories"`
			IncludeKeywords []string `json:"include_keywords"`
			ExcludeKeywords []string `json:"exclude_keywords"`
		} `json:"filters"`
	} `json:"rss"`
	Log struct {
		Level string `json:"level"`
	} `json:"log"`
}

func toDTO(cfg config.Config) configDTO {
	var dto configDTO
	dto.Server.Listen = cfg.Server.Listen
	dto.Server.ExternalURL = cfg.Server.ExternalURL
	dto.Server.AdminPassword = passwordMask
	dto.Downloader.Type = effectiveType(cfg)
	dto.QBittorrent.URL = cfg.QBittorrent.URL
	dto.QBittorrent.Username = cfg.QBittorrent.Username
	dto.QBittorrent.Category = cfg.QBittorrent.Category
	if cfg.QBittorrent.Password != "" {
		dto.QBittorrent.Password = passwordMask
	}
	dto.Deluge.Host = cfg.Deluge.Host
	dto.Deluge.Port = cfg.Deluge.Port
	dto.Deluge.Username = cfg.Deluge.Username
	dto.Deluge.Label = cfg.Deluge.Label
	if cfg.Deluge.Password != "" {
		dto.Deluge.Password = passwordMask
	}
	dto.Prowlarr.URL = cfg.Prowlarr.URL
	if cfg.Prowlarr.APIKey != "" {
		dto.Prowlarr.APIKey = passwordMask
	}
	dto.Prowlarr.MovieCategories = cfg.Prowlarr.MovieCategories
	dto.Prowlarr.TVCategories = cfg.Prowlarr.TVCategories
	dto.Prowlarr.AnimeCategories = cfg.Prowlarr.AnimeCategories
	// Emit [] rather than null so clients can treat it as an array.
	dto.Prowlarr.IndexerIDs = cfg.Prowlarr.IndexerIDs
	if dto.Prowlarr.IndexerIDs == nil {
		dto.Prowlarr.IndexerIDs = []int{}
	}
	dto.Prowlarr.SearchTimeoutSeconds = int(cfg.Prowlarr.SearchTimeout / time.Second)
	dto.Addon.EnableMovies = cfg.Addon.EnableMovies
	dto.Addon.EnableSeries = cfg.Addon.EnableSeries
	dto.Addon.EnableAnime = cfg.Addon.EnableAnime
	dto.Filters.MinSeeders = cfg.Filters.MinSeeders
	dto.Filters.MinSizeMB = cfg.Filters.MinSizeMB
	dto.Filters.MaxSizeMB = cfg.Filters.MaxSizeMB
	dto.Filters.MaxResults = cfg.Filters.MaxResults
	dto.Meta.CinemetaURL = cfg.Meta.CinemetaURL
	dto.Meta.MetadataTimeoutSeconds = int(cfg.Meta.MetadataTimeout / time.Second)
	if cfg.Meta.TMDbAPIKey != "" {
		dto.Meta.TMDbAPIKey = passwordMask
	}
	dto.Paths.Mappings = cfg.Paths.Mappings
	dto.Storage.Database = cfg.Storage.Database
	dto.Storage.DeleteFilesOnRemove = cfg.Storage.DeleteFilesOnRemove
	dto.Storage.MaxDiskUsagePercent = cfg.Storage.MaxDiskUsagePercent
	dto.Storage.MaxDownloadStorageGB = cfg.Storage.MaxDownloadStorageGB
	dto.Stream.WaitTimeoutSeconds = int(cfg.Stream.WaitTimeout / time.Second)
	dto.Stream.ReadChunk = cfg.Stream.ReadChunk
	dto.Cleanup.SeedTimeHours = int(cfg.Cleanup.SeedTime / time.Hour)
	dto.Cleanup.MinProgressForCancelPercent = int(cfg.Cleanup.MinProgressForCancel * 100)
	dto.Cleanup.TargetRatio = cfg.Cleanup.TargetRatio
	dto.Cleanup.DeletePolicy = cfg.Cleanup.DeletePolicy
	dto.Cleanup.IndexerSeedTimes = indexerSeedTimesToDTO(cfg.Cleanup.IndexerSeedTimes)
	dto.Seeding.Full = cfg.Seeding.Full
	dto.RSS.Enabled = cfg.RSS.Enabled
	dto.RSS.IntervalMinutes = int(cfg.RSS.Interval / time.Minute)
	dto.RSS.MaxGrabsPerCycle = cfg.RSS.MaxGrabsPerCycle
	dto.RSS.MaxConcurrentDownloads = cfg.RSS.MaxConcurrentDownloads
	dto.RSS.MaxActiveTorrents = cfg.RSS.MaxActiveTorrents
	dto.RSS.FreeleechOnly = cfg.RSS.FreeleechOnly
	dto.RSS.Filters.MinSizeMB = cfg.RSS.Filters.MinSizeMB
	dto.RSS.Filters.MaxSizeMB = cfg.RSS.Filters.MaxSizeMB
	// Emit [] rather than null so clients can treat these as arrays.
	dto.RSS.Filters.Categories = cfg.RSS.Filters.Categories
	if dto.RSS.Filters.Categories == nil {
		dto.RSS.Filters.Categories = []int{}
	}
	dto.RSS.Filters.IncludeKeywords = cfg.RSS.Filters.IncludeKeywords
	if dto.RSS.Filters.IncludeKeywords == nil {
		dto.RSS.Filters.IncludeKeywords = []string{}
	}
	dto.RSS.Filters.ExcludeKeywords = cfg.RSS.Filters.ExcludeKeywords
	if dto.RSS.Filters.ExcludeKeywords == nil {
		dto.RSS.Filters.ExcludeKeywords = []string{}
	}
	dto.Log.Level = cfg.Log.Level
	return dto
}

// indexerSeedTime is one per-indexer seed-time override. The map in the
// config is exchanged with the UI as a sorted list so rows keep a stable
// order (and a stable React key) while a name is being edited.
type indexerSeedTime struct {
	Indexer       string `json:"indexer"`
	SeedTimeHours int    `json:"seed_time_hours"`
}

// indexerSeedTimesToDTO converts the config map to a list sorted by
// indexer name. Always non-nil so clients can treat it as an array.
func indexerSeedTimesToDTO(m map[string]time.Duration) []indexerSeedTime {
	out := make([]indexerSeedTime, 0, len(m))
	for _, name := range slices.Sorted(maps.Keys(m)) {
		out = append(out, indexerSeedTime{Indexer: name, SeedTimeHours: int(m[name] / time.Hour)})
	}
	return out
}

// indexerSeedTimesFromDTO converts the UI list back to a config map.
// Rows with a blank indexer are dropped — an empty row is how the UI
// represents "not filled in yet", not a value worth rejecting. Negative
// hours are clamped to 0 rather than rejected here so the whole save
// doesn't fail on a transient input state; 0 means "never remove by seed
// time" for that indexer. Case-insensitive duplicates survive as separate
// keys and are rejected by config.Validate, where the ambiguity is real.
func indexerSeedTimesFromDTO(list []indexerSeedTime) map[string]time.Duration {
	out := make(map[string]time.Duration, len(list))
	for _, row := range list {
		name := strings.TrimSpace(row.Indexer)
		if name == "" {
			continue
		}
		out[name] = time.Duration(max(row.SeedTimeHours, 0)) * time.Hour
	}
	return out
}

// apply merges a DTO into an existing config, respecting mask/empty
// semantics for secrets.
func (dto configDTO) apply(cfg config.Config) config.Config {
	cfg.Server.Listen = dto.Server.Listen
	cfg.Server.ExternalURL = dto.Server.ExternalURL
	if pw := dto.Server.AdminPassword; pw != "" && pw != passwordMask {
		cfg.Server.AdminPassword = pw
	}
	// An absent/empty type (older UI payloads) keeps the stored value.
	if dto.Downloader.Type != "" {
		cfg.Downloader.Type = dto.Downloader.Type
	}
	cfg.QBittorrent.URL = dto.QBittorrent.URL
	cfg.QBittorrent.Username = dto.QBittorrent.Username
	cfg.QBittorrent.Category = dto.QBittorrent.Category
	// Empty or masked = keep the stored password.
	if pw := dto.QBittorrent.Password; pw != "" && pw != passwordMask {
		cfg.QBittorrent.Password = pw
	}
	if dto.Deluge.Host != "" {
		cfg.Deluge.Host = dto.Deluge.Host
	}
	if dto.Deluge.Port > 0 {
		cfg.Deluge.Port = dto.Deluge.Port
	}
	if dto.Deluge.Username != "" {
		cfg.Deluge.Username = dto.Deluge.Username
	}
	// Empty keeps the stored label too, so payloads from UIs predating
	// the Deluge section don't wipe it (and don't trigger a client swap).
	if dto.Deluge.Label != "" {
		cfg.Deluge.Label = dto.Deluge.Label
	}
	// Empty or masked = keep the stored password.
	if pw := dto.Deluge.Password; pw != "" && pw != passwordMask {
		cfg.Deluge.Password = pw
	}
	cfg.Prowlarr.URL = dto.Prowlarr.URL
	// Empty or masked API key = keep the stored key.
	if k := dto.Prowlarr.APIKey; k != "" && k != passwordMask {
		cfg.Prowlarr.APIKey = k
	}
	cfg.Prowlarr.MovieCategories = dto.Prowlarr.MovieCategories
	cfg.Prowlarr.TVCategories = dto.Prowlarr.TVCategories
	cfg.Prowlarr.AnimeCategories = dto.Prowlarr.AnimeCategories
	cfg.Prowlarr.IndexerIDs = dto.Prowlarr.IndexerIDs
	// A search needs a positive budget; 0 keeps the stored value, mirroring
	// the metadata timeout above.
	if dto.Prowlarr.SearchTimeoutSeconds > 0 {
		cfg.Prowlarr.SearchTimeout = time.Duration(dto.Prowlarr.SearchTimeoutSeconds) * time.Second
	}
	cfg.Addon.EnableMovies = dto.Addon.EnableMovies
	cfg.Addon.EnableSeries = dto.Addon.EnableSeries
	cfg.Addon.EnableAnime = dto.Addon.EnableAnime
	cfg.Filters.MinSeeders = dto.Filters.MinSeeders
	cfg.Filters.MinSizeMB = dto.Filters.MinSizeMB
	cfg.Filters.MaxSizeMB = dto.Filters.MaxSizeMB
	cfg.Filters.MaxResults = dto.Filters.MaxResults
	cfg.Meta.CinemetaURL = dto.Meta.CinemetaURL
	if dto.Meta.MetadataTimeoutSeconds > 0 {
		cfg.Meta.MetadataTimeout = time.Duration(dto.Meta.MetadataTimeoutSeconds) * time.Second
	}
	// Empty or masked = keep the stored key.
	if k := dto.Meta.TMDbAPIKey; k != "" && k != passwordMask {
		cfg.Meta.TMDbAPIKey = k
	}
	cfg.Paths.Mappings = dto.Paths.Mappings
	// Empty = keep the stored path, so older clients that don't send the
	// field can't blank it (validation rejects an empty database anyway).
	if dto.Storage.Database != "" {
		cfg.Storage.Database = dto.Storage.Database
	}
	cfg.Storage.DeleteFilesOnRemove = dto.Storage.DeleteFilesOnRemove
	// 0 is a meaningful, settable value (disables the disk-usage gate), so
	// it is always applied rather than treated as "keep existing value".
	cfg.Storage.MaxDiskUsagePercent = dto.Storage.MaxDiskUsagePercent
	// 0 meaningfully disables the absolute storage cap; always applied.
	cfg.Storage.MaxDownloadStorageGB = dto.Storage.MaxDownloadStorageGB
	if dto.Stream.WaitTimeoutSeconds > 0 {
		cfg.Stream.WaitTimeout = time.Duration(dto.Stream.WaitTimeoutSeconds) * time.Second
	}
	if dto.Stream.ReadChunk > 0 {
		cfg.Stream.ReadChunk = dto.Stream.ReadChunk
	}
	// Unlike other duration fields above, 0 is a meaningful, settable
	// value here (disables seed-time cleanup), so it is always applied
	// rather than treated as "keep existing value".
	if dto.Cleanup.SeedTimeHours >= 0 {
		cfg.Cleanup.SeedTime = time.Duration(dto.Cleanup.SeedTimeHours) * time.Hour
	}
	// Likewise 0 meaningfully disables the abandoned-download check.
	cfg.Cleanup.MinProgressForCancel = float64(dto.Cleanup.MinProgressForCancelPercent) / 100
	// 0 meaningfully disables the ratio trigger; always applied.
	cfg.Cleanup.TargetRatio = dto.Cleanup.TargetRatio
	// Empty = keep the stored policy (older clients don't send the field);
	// Validate rejects any non-empty value outside the allowed set.
	if dto.Cleanup.DeletePolicy != "" {
		cfg.Cleanup.DeletePolicy = dto.Cleanup.DeletePolicy
	}
	// The per-indexer overrides replace the stored map wholesale, so
	// removing a row in the UI removes the override. A nil list (older
	// clients that don't send the field) leaves the stored map alone.
	if dto.Cleanup.IndexerSeedTimes != nil {
		cfg.Cleanup.IndexerSeedTimes = indexerSeedTimesFromDTO(dto.Cleanup.IndexerSeedTimes)
	}
	cfg.Seeding.Full = dto.Seeding.Full
	cfg.RSS.Enabled = dto.RSS.Enabled
	// Interval is only meaningful when positive; 0 keeps the stored value so
	// a client clearing the field can't produce an invalid (<=0) interval.
	if dto.RSS.IntervalMinutes > 0 {
		cfg.RSS.Interval = time.Duration(dto.RSS.IntervalMinutes) * time.Minute
	}
	// 0 is a meaningful, settable value (disables grabbing), so always apply.
	cfg.RSS.MaxGrabsPerCycle = dto.RSS.MaxGrabsPerCycle
	// 0 meaningfully disables these gates; always applied.
	cfg.RSS.MaxConcurrentDownloads = dto.RSS.MaxConcurrentDownloads
	cfg.RSS.MaxActiveTorrents = dto.RSS.MaxActiveTorrents
	cfg.RSS.FreeleechOnly = dto.RSS.FreeleechOnly
	// RSS filters: sizes are always applied (0 = no bound / unbounded), and
	// the list fields replace the stored values wholesale.
	cfg.RSS.Filters.MinSizeMB = dto.RSS.Filters.MinSizeMB
	cfg.RSS.Filters.MaxSizeMB = dto.RSS.Filters.MaxSizeMB
	cfg.RSS.Filters.Categories = dto.RSS.Filters.Categories
	cfg.RSS.Filters.IncludeKeywords = dto.RSS.Filters.IncludeKeywords
	cfg.RSS.Filters.ExcludeKeywords = dto.RSS.Filters.ExcludeKeywords
	// Empty = keep the stored level (older clients don't send the field).
	if dto.Log.Level != "" {
		cfg.Log.Level = dto.Log.Level
	}
	return cfg
}

func (h *Handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toDTO(h.config.Get()))
}

func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request) {
	var dto configDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	current := h.config.Get()
	next := dto.apply(current)

	if err := h.config.Update(next); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Hot-apply the download client if its settings changed. Backends
	// holding a live connection (Deluge) are closed after the swap; a
	// torrent library moved between backends stays in the old client —
	// the syncer will flag it as gone.
	if next.Downloader != current.Downloader || next.QBittorrent != current.QBittorrent || next.Deluge != current.Deluge {
		old := h.dc.SwapAndReturn(h.newClient(next))
		if closer, ok := old.(io.Closer); ok {
			go closer.Close()
		}
		h.logger.Info("download client reconfigured", "type", effectiveType(next))
	}

	resp := map[string]any{"config": toDTO(next)}
	// These settings are read once at startup (listener, database handle,
	// logger, Cinemeta/TMDb client), so changes only apply after a restart.
	if next.Server.Listen != current.Server.Listen ||
		next.Storage.Database != current.Storage.Database ||
		next.Log.Level != current.Log.Level ||
		next.Meta.CinemetaURL != current.Meta.CinemetaURL ||
		next.Meta.TMDbAPIKey != current.Meta.TMDbAPIKey {
		resp["restart_required"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) testQbittorrent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Password == passwordMask {
		body.Password = h.config.Get().QBittorrent.Password
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	version, err := qbit.New(body.URL, body.Username, body.Password, body.Category).Version(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version})
}

// testDeluge probes a Deluge daemon with a version call so the UI can
// validate the connection settings before saving.
func (h *Handler) testDeluge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Password == passwordMask {
		body.Password = h.config.Get().Deluge.Password
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	client := deluge.New(body.Host, body.Port, body.Username, body.Password, "")
	defer func() {
		if closer, ok := client.(io.Closer); ok {
			closer.Close()
		}
	}()
	version, err := client.Version(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version})
}

// effectiveType normalizes the configured downloader type (empty means
// qBittorrent, for configs predating the downloader section).
func effectiveType(cfg config.Config) string {
	if cfg.Downloader.Type == config.DownloaderDeluge {
		return config.DownloaderDeluge
	}
	return config.DownloaderQBittorrent
}

// diskInfo reports seedstrem's on-disk footprint plus the space still
// available for downloads. free comes from the download client when it can
// answer — that is the filesystem the client actually writes to, which is
// not necessarily one seedstrem can stat — and falls back to a local
// statfs on the configured download root otherwise. When neither source
// answers, free is omitted rather than guessed; used is still reported,
// since it comes from data already in hand.
//
// used + free is deliberately not a disk total: used is seedstrem's
// footprint, free is the whole filesystem's headroom. Neither backend can
// report a total, so no percentage is possible.
func (h *Handler) diskInfo(ctx context.Context, cfg config.Config, used int64) map[string]any {
	disk := map[string]any{"used": used}

	if free, err := h.dc.FreeSpace(ctx); err == nil {
		disk["free"] = free
		disk["free_source"] = "client"
		return disk
	}

	path := cfg.Paths.FirstLocal()
	if path == "" {
		return disk
	}
	localUsed, total, err := h.diskUsage(path)
	if err != nil {
		return disk
	}
	disk["free"] = total - localUsed
	disk["free_source"] = "local"
	return disk
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	cfg := h.config.Get()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	qbStatus := map[string]any{"connected": false}
	if version, err := h.dc.Version(ctx); err == nil {
		qbStatus = map[string]any{"connected": true, "version": version}
	} else {
		qbStatus["error"] = err.Error()
	}
	dlStatus := map[string]any{"type": effectiveType(cfg)}
	for k, v := range qbStatus {
		dlStatus[k] = v
	}

	counts := map[string]int{}
	var totalUploaded, diskUsed int64
	if stored, err := h.store.AllTorrents(ctx); err == nil {
		live := h.liveByHash(ctx, stored)
		for _, tor := range stored {
			info, inQbit := live[tor.Hash]
			status := torrents.DeriveStatus(tor.Phase, info.State, tor.Error != "" || !inQbit, info.Size > 0, info.Progress)
			counts[status]++
			totalUploaded += info.Uploaded
			diskUsed += info.Completed
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":      h.version,
		"external_url": cfg.Server.ExternalURL,
		"manifest_url": strings.TrimSuffix(cfg.Server.ExternalURL, "/") + "/stremio/manifest.json",
		// "qbittorrent" is kept one release for older UIs; "downloader"
		// is the same connection status plus the backend type.
		"qbittorrent":    qbStatus,
		"downloader":     dlStatus,
		"torrents":       counts,
		"total_uploaded": totalUploaded,
		"disk":           h.diskInfo(ctx, cfg, diskUsed),
	})
}

// liveByHash fetches live qBittorrent state for exactly the hashes
// seedstrem already tracks in the store.
func (h *Handler) liveByHash(ctx context.Context, stored []store.Torrent) map[string]downloader.TorrentInfo {
	live := map[string]downloader.TorrentInfo{}
	if len(stored) == 0 {
		return live
	}
	hashes := make([]string, len(stored))
	for i, tor := range stored {
		hashes[i] = tor.Hash
	}
	if dcTorrents, err := h.dc.Torrents(ctx, hashes); err == nil {
		for _, t := range dcTorrents {
			live[strings.ToLower(t.Hash)] = t
		}
	}
	return live
}

// torrentItem is the UI torrent listing shape.
type torrentItem struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Hash     string  `json:"hash"`
	Status   string  `json:"status"`
	Progress float64 `json:"progress"`
	Speed    int64   `json:"speed"`
	Seeders  int64   `json:"seeders"`
	Size     int64   `json:"size"`
	Uploaded int64   `json:"uploaded"`
	Ratio    float64 `json:"ratio"`
	Indexer  string  `json:"indexer,omitempty"`
	// SeedTime is the seed time governing THIS torrent (its indexer's
	// override when it has one, else the global default), in seconds, so
	// the UI's time-left readout matches what cleanup will actually do.
	SeedTime    int64 `json:"seed_time"`
	SeedingTime int64 `json:"seeding_time"`
	AddedAt     int64 `json:"added_at"`
	// Origin is "native" (seedstrem added it) or "adopted" (discovered in
	// the download client by label).
	Origin string     `json:"origin"`
	Error  string     `json:"error,omitempty"`
	Links  []linkItem `json:"links"`
}

type linkItem struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	URL   string `json:"url"`
}

func (h *Handler) torrents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg := h.config.Get()

	stored, _, err := h.store.ListTorrents(ctx, 500, 0)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list torrents")
		return
	}

	live := h.liveByHash(ctx, stored)

	base := strings.TrimSuffix(cfg.Server.ExternalURL, "/")
	items := make([]torrentItem, 0, len(stored))
	for _, tor := range stored {
		info, inQbit := live[tor.Hash]
		links, err := h.store.LinksByTorrent(ctx, tor.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "load links")
			return
		}
		linkItems := make([]linkItem, 0, len(links))
		for _, l := range links {
			linkItems = append(linkItems, linkItem{Path: l.Path, Bytes: l.Bytes, URL: base + "/dl/" + l.Token})
		}
		name := tor.Name
		if info.Name != "" {
			name = info.Name
		}
		items = append(items, torrentItem{
			ID:          tor.ID,
			Name:        name,
			Hash:        tor.Hash,
			Status:      torrents.DeriveStatus(tor.Phase, info.State, tor.Error != "" || !inQbit, info.Size > 0, info.Progress),
			Progress:    info.Progress,
			Speed:       info.DlSpeed,
			Seeders:     info.NumSeeds,
			Size:        info.Size,
			Uploaded:    info.Uploaded,
			Ratio:       info.Ratio,
			Indexer:     tor.Indexer,
			SeedTime:    int64(cfg.Cleanup.EffectiveSeedTime(tor.Indexer) / time.Second),
			SeedingTime: int64(info.SeedingTime / time.Second),
			AddedAt:     tor.AddedAt,
			Origin:      tor.Origin,
			Error:       tor.Error,
			Links:       linkItems,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// deleteTorrent removes a tracked torrent from the download client and the
// local store. Downloaded files are deleted only when delete_files_on_remove
// is enabled. A torrent that is already gone is treated as success.
func (h *Handler) deleteTorrent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing torrent id")
		return
	}

	ctx := r.Context()
	tor, err := h.store.TorrentByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "torrent not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "look up torrent")
		return
	}

	if err := h.svc.Remove(ctx, tor, store.DeletionEvent{Reason: store.DeleteReasonManual}); err != nil {
		h.logger.Warn("admin: delete torrent failed", "id", id, "error", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testProwlarr probes a Prowlarr instance with a trivial search so the UI
// can validate the URL + API key before saving.
func (h *Handler) testProwlarr(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.APIKey == passwordMask || body.APIKey == "" {
		body.APIKey = h.config.Get().Prowlarr.APIKey
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := prowlarr.New(body.URL, body.APIKey).Search(ctx, "test", "", nil, nil); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// prowlarrIndexers lists the enabled indexers of a Prowlarr instance so the
// UI can offer them as search targets. Uses the posted URL/API key, falling
// back to the stored key when masked/empty (same as testProwlarr).
func (h *Handler) prowlarrIndexers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.APIKey == passwordMask || body.APIKey == "" {
		body.APIKey = h.config.Get().Prowlarr.APIKey
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	indexers, err := prowlarr.New(body.URL, body.APIKey).Indexers(ctx)
	if err != nil {
		h.logger.Debug("admin: prowlarr indexer list failed", "url", body.URL, "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	type item struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	out := make([]item, 0, len(indexers))
	for _, ix := range indexers {
		if !ix.Enable {
			continue
		}
		out = append(out, item{ID: ix.ID, Name: ix.Name})
	}
	h.logger.Debug("admin: prowlarr indexers listed", "total", len(indexers), "enabled", len(out))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "indexers": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// deletionItem is one row of the deletion history. Durations serialize as
// seconds, matching torrentItem.
type deletionItem struct {
	ID            string  `json:"id"`
	TorrentID     string  `json:"torrent_id"`
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	Indexer       string  `json:"indexer"`
	Origin        string  `json:"origin"`
	DeletedAt     int64   `json:"deleted_at"`
	Reason        string  `json:"reason"`
	SeedingTime   int64   `json:"seeding_time"`
	SeedLimit     int64   `json:"seed_limit"`
	Ratio         float64 `json:"ratio"`
	RatioLimit    float64 `json:"ratio_limit"`
	Progress      float64 `json:"progress"`
	ProgressLimit float64 `json:"progress_limit"`
	FilesDeleted  bool    `json:"files_deleted"`
}

// deletions returns the torrents removed within the retention window,
// newest first, with the reason and the evidence behind each removal.
func (h *Handler) deletions(w http.ResponseWriter, r *http.Request) {
	records, err := h.store.Deletions(r.Context())
	if err != nil {
		h.logger.Warn("admin: list deletions", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "list deletions")
		return
	}
	items := make([]deletionItem, 0, len(records))
	for _, d := range records {
		items = append(items, deletionItem{
			ID:            d.ID,
			TorrentID:     d.TorrentID,
			Hash:          d.Hash,
			Name:          d.Name,
			Indexer:       d.Indexer,
			Origin:        d.Origin,
			DeletedAt:     d.DeletedAt,
			Reason:        d.Reason,
			SeedingTime:   int64(d.SeedingTime / time.Second),
			SeedLimit:     int64(d.SeedLimit / time.Second),
			Ratio:         d.Ratio,
			RatioLimit:    d.RatioLimit,
			Progress:      d.Progress,
			ProgressLimit: d.ProgressLimit,
			FilesDeleted:  d.FilesDeleted,
		})
	}
	writeJSON(w, http.StatusOK, items)
}
