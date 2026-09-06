// Command seedstrem runs a Stremio addon that searches Prowlarr indexers
// and streams torrents through qBittorrent while they download.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kipsilabs/seedstrem/internal/admin"
	"github.com/kipsilabs/seedstrem/internal/adopt"
	"github.com/kipsilabs/seedstrem/internal/cleanup"
	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/deluge"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/meta"
	"github.com/kipsilabs/seedstrem/internal/playsession"
	"github.com/kipsilabs/seedstrem/internal/prowlarr"
	"github.com/kipsilabs/seedstrem/internal/qbit"
	"github.com/kipsilabs/seedstrem/internal/rss"
	"github.com/kipsilabs/seedstrem/internal/server"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/stream"
	"github.com/kipsilabs/seedstrem/internal/stremio"
	"github.com/kipsilabs/seedstrem/internal/syncer"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seedstrem:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	healthcheck := flag.Bool("healthcheck", false, "probe the local /api/health endpoint and exit (for Docker HEALTHCHECK)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("seedstrem", version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	if *healthcheck {
		return probeHealth(cfg.Server.Listen)
	}

	logger := newLogger(cfg.Log.Level)
	slog.SetDefault(logger)

	// Generate api token / admin password on first run and persist them.
	changed, err := config.EnsureSecrets(&cfg)
	if err != nil {
		return err
	}
	if changed {
		if err := config.Save(cfg, *configPath); err != nil {
			logger.Warn("could not persist generated secrets; they will change on restart",
				"error", err, "config", *configPath)
		}
		logger.Info("generated admin password (also saved to config)",
			"admin_password", cfg.Server.AdminPassword,
			"config", *configPath)
	}

	db, err := store.Open(cfg.Storage.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	cm := config.NewManager(cfg, *configPath)
	dc := downloader.NewSwappable(buildDownloadClient(cfg))

	torrentSvc := torrents.New(db, dc, func() torrents.Settings {
		c := cm.Get()
		return torrents.Settings{
			MetadataTimeout:     c.Meta.MetadataTimeout,
			DeleteFilesOnRemove: c.Storage.DeleteFilesOnRemove,
			SeedFull:            c.Seeding.Full,
		}
	}, logger)

	metaClient := meta.New(cfg.Meta.CinemetaURL, cfg.Meta.TMDbAPIKey)
	stremioHandler := stremio.New(torrentSvc, metaClient, func() stremio.Settings {
		c := cm.Get()
		return stremio.Settings{
			ExternalURL: c.Server.ExternalURL,
			Prowlarr: stremio.ProwlarrSettings{
				URL:             c.Prowlarr.URL,
				APIKey:          c.Prowlarr.APIKey,
				MovieCategories: c.Prowlarr.MovieCategories,
				TVCategories:    c.Prowlarr.TVCategories,
				AnimeCategories: c.Prowlarr.AnimeCategories,
				IndexerIDs:      c.Prowlarr.IndexerIDs,
				SearchTimeout:   c.Prowlarr.SearchTimeout,
				SearchCacheTTL:  c.Prowlarr.SearchCacheTTL,
			},
			Addon: stremio.AddonSettings{
				EnableMovies: c.Addon.EnableMovies,
				EnableSeries: c.Addon.EnableSeries,
				EnableAnime:  c.Addon.EnableAnime,
			},
			Filters: prowlarr.Filters{
				MinSeeders:   c.Filters.MinSeeders,
				MinSizeBytes: c.Filters.MinSizeMB << 20,
				MaxSizeBytes: c.Filters.MaxSizeMB << 20,
			},
			MaxResults: c.Filters.MaxResults,
			Disk: stremio.DiskSettings{
				MaxUsagePercent: c.Storage.MaxDiskUsagePercent,
				Path:            firstLocalMapping(c.Paths.Mappings),
			},
		}
	}, version, logger)

	resolver := stream.NewResolver(dc, func() []config.Mapping { return cm.Get().Paths.Mappings })
	avail := stream.NewAvailability(dc)
	sessions := playsession.New()
	streamHandler := stream.NewHandler(db, dc, torrentSvc, resolver, avail, sessions, func() stream.Settings {
		c := cm.Get()
		return stream.Settings{
			WaitTimeout:          c.Stream.WaitTimeout,
			ReadChunk:            c.Stream.ReadChunk,
			MinProgressForCancel: c.Cleanup.MinProgressForCancel,
		}
	}, logger)

	adminHandler := admin.New(cm, db, dc, torrentSvc, buildDownloadClient, version, logger)

	syncCtx, stopSync := context.WithCancel(context.Background())
	defer stopSync()
	go syncer.New(db, dc, logger, 30*time.Second).Run(syncCtx)

	cleanCtx, stopClean := context.WithCancel(context.Background())
	defer stopClean()
	go cleanup.New(db, dc, torrentSvc, sessions, func() cleanup.Settings {
		c := cm.Get()
		return cleanup.Settings{
			SeedTime:         c.Cleanup.SeedTime,
			TargetRatio:      c.Cleanup.TargetRatio,
			DeletePolicy:     c.Cleanup.DeletePolicy,
			IndexerSeedTimes: c.Cleanup.IndexerSeedTimes,
		}
	}, logger, 30*time.Minute).Run(cleanCtx)

	adoptCtx, stopAdopt := context.WithCancel(context.Background())
	defer stopAdopt()
	go adopt.New(db, dc, func() adopt.Settings {
		c := cm.Get()
		return adopt.Settings{
			Enabled: c.Downloader.AdoptLabelled,
			Label:   c.ClientLabel(),
		}
	}, logger, time.Minute).Run(adoptCtx)

	rssCtx, stopRSS := context.WithCancel(context.Background())
	defer stopRSS()
	go rss.New(db, dc, torrentSvc, func() rss.Settings {
		c := cm.Get()
		return rss.Settings{
			Enabled:        c.RSS.Enabled,
			ProwlarrURL:    c.Prowlarr.URL,
			ProwlarrAPIKey: c.Prowlarr.APIKey,
			Categories:     rssCategories(c),
			IndexerIDs:     c.Prowlarr.IndexerIDs,
			// RSS uses its own size bounds but inherits the global seeder
			// floor as a ratio-safety default.
			Filters:                 prowlarr.Filters{MinSeeders: c.Filters.MinSeeders, MinSizeBytes: c.RSS.Filters.MinSizeMB << 20, MaxSizeBytes: c.RSS.Filters.MaxSizeMB << 20},
			FreeleechOnly:           c.RSS.FreeleechOnly,
			IncludeKeywords:         c.RSS.Filters.IncludeKeywords,
			ExcludeKeywords:         c.RSS.Filters.ExcludeKeywords,
			MaxGrabsPerCycle:        c.RSS.MaxGrabsPerCycle,
			SearchTimeout:           c.Prowlarr.SearchTimeout,
			MaxConcurrentDownloads:  c.RSS.MaxConcurrentDownloads,
			MaxActiveTorrents:       c.RSS.MaxActiveTorrents,
			DiskPath:                firstLocalMapping(c.Paths.Mappings),
			MaxDiskUsagePercent:     c.Storage.MaxDiskUsagePercent,
			MaxDownloadStorageBytes: c.Storage.MaxDownloadStorageGB << 30,
		}
	}, logger, cfg.RSS.Interval).Run(rssCtx)

	handler := server.New(server.Options{
		Logger:  logger,
		Stremio: stremioHandler.Router(),
		Stream:  streamHandler.Router(),
		Admin:   adminHandler.Router(),
	})

	httpServer := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("seedstrem listening", "addr", cfg.Server.Listen, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		logger.Info("shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return httpServer.Shutdown(ctx)
}

// buildDownloadClient constructs the download client selected by
// downloader.type. Anything but "deluge" (including the empty legacy
// value) means qBittorrent.
func buildDownloadClient(cfg config.Config) downloader.Client {
	if cfg.Downloader.Type == config.DownloaderDeluge {
		return deluge.New(cfg.Deluge.Host, cfg.Deluge.Port, cfg.Deluge.Username, cfg.Deluge.Password, cfg.Deluge.Label)
	}
	return qbit.New(cfg.QBittorrent.URL, cfg.QBittorrent.Username, cfg.QBittorrent.Password, cfg.QBittorrent.Category)
}

// rssCategories combines the newznab category ids to poll for recent
// releases, restricted to the content types the addon currently serves so
// the grabber never fetches categories the addon can't stream.
func rssCategories(c config.Config) []int {
	// An explicit RSS category list overrides the addon-derived default,
	// letting the grabber poll a narrower (or different) set than what the
	// addon serves.
	if len(c.RSS.Filters.Categories) > 0 {
		return c.RSS.Filters.Categories
	}
	var cats []int
	if c.Addon.EnableMovies {
		cats = append(cats, c.Prowlarr.MovieCategories...)
	}
	if c.Addon.EnableSeries {
		cats = append(cats, c.Prowlarr.TVCategories...)
	}
	if c.Addon.EnableAnime {
		cats = append(cats, c.Prowlarr.AnimeCategories...)
	}
	return cats
}

// firstLocalMapping returns the first configured local download root, used
// as the path whose disk usage gates new streams. Empty when no mapping is
// configured, which disables the gate.
func firstLocalMapping(mappings []config.Mapping) string {
	return config.Paths{Mappings: mappings}.FirstLocal()
}

func defaultConfigPath() string {
	if _, err := os.Stat("/config"); err == nil {
		return "/config/config.yaml"
	}
	return "config.yaml"
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func probeHealth(listen string) error {
	addr := listen
	if addr == "" {
		addr = ":8080"
	}
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}
