package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("got listen %q; want :8080", cfg.Server.Listen)
	}
	if cfg.Stream.WaitTimeout != 60*time.Second {
		t.Errorf("got wait_timeout %v; want 60s", cfg.Stream.WaitTimeout)
	}
	if cfg.Cleanup.SeedTime != 72*time.Hour {
		t.Errorf("got cleanup.seed_time %v; want 72h", cfg.Cleanup.SeedTime)
	}
	if cfg.Cleanup.MinProgressForCancel != 0.05 {
		t.Errorf("got cleanup.min_progress_for_cancel %v; want 0.05", cfg.Cleanup.MinProgressForCancel)
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := `
server:
  listen: ":9090"
  external_url: "https://media.example.com"
qbittorrent:
  url: "http://qbittorrent-host:8080"
  username: "u"
  password: "p"
  category: "seedstrem"
stream:
  wait_timeout: 30s
paths:
  mappings:
    - remote: /dl
      local: /mnt/dl
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Listen != ":9090" {
		t.Errorf("got listen %q; want :9090", cfg.Server.Listen)
	}
	if cfg.Stream.WaitTimeout != 30*time.Second {
		t.Errorf("got wait_timeout %v; want 30s", cfg.Stream.WaitTimeout)
	}
	if cfg.QBittorrent.URL != "http://qbittorrent-host:8080" || cfg.QBittorrent.Category != "seedstrem" {
		t.Errorf("qbittorrent connection not parsed: %+v", cfg.QBittorrent)
	}
	if len(cfg.Paths.Mappings) != 1 || cfg.Paths.Mappings[0].Local != "/mnt/dl" {
		t.Errorf("mappings not parsed: %+v", cfg.Paths.Mappings)
	}
}

func TestApplyEnv(t *testing.T) {
	cfg := Default()
	env := map[string]string{
		"SEEDSTREM_QBITTORRENT_URL":                 "http://other-host:9091",
		"SEEDSTREM_QBITTORRENT_CATEGORY":            "other-cat",
		"SEEDSTREM_STREAM_WAIT_TIMEOUT":             "90s",
		"SEEDSTREM_PATHS_MAPPINGS":                  "/a:/b,/c:/d",
		"SEEDSTREM_CLEANUP_SEED_TIME":               "48h",
		"SEEDSTREM_CLEANUP_MIN_PROGRESS_FOR_CANCEL": "0.1",
	}
	applyEnv(&cfg, func(k string) string { return env[k] })

	if cfg.QBittorrent.URL != "http://other-host:9091" || cfg.QBittorrent.Category != "other-cat" {
		t.Errorf("qbittorrent override failed: %+v", cfg.QBittorrent)
	}
	if cfg.Stream.WaitTimeout != 90*time.Second {
		t.Errorf("wait timeout override failed: %v", cfg.Stream.WaitTimeout)
	}
	if len(cfg.Paths.Mappings) != 2 || cfg.Paths.Mappings[1].Remote != "/c" {
		t.Errorf("mappings override failed: %+v", cfg.Paths.Mappings)
	}
	if cfg.Cleanup.SeedTime != 48*time.Hour {
		t.Errorf("seed time override failed: %v", cfg.Cleanup.SeedTime)
	}
	if cfg.Cleanup.MinProgressForCancel != 0.1 {
		t.Errorf("min progress for cancel override failed: %v", cfg.Cleanup.MinProgressForCancel)
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	cfg := Config{} // everything empty/zero
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	for _, want := range []string{"server.listen", "server.external_url", "qbittorrent.url", "stream.wait_timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error mentioning %q, got: %s", want, msg)
		}
	}
}

func TestValidateCleanup(t *testing.T) {
	cfg := Default()
	cfg.Cleanup.SeedTime = -time.Hour
	cfg.Cleanup.MinProgressForCancel = 1.5
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	for _, want := range []string{"cleanup.seed_time", "cleanup.min_progress_for_cancel"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error mentioning %q, got: %s", want, msg)
		}
	}
}

func TestApplyEnvMaxDiskUsagePercent(t *testing.T) {
	cfg := Default()
	applyEnv(&cfg, func(k string) string {
		if k == "SEEDSTREM_STORAGE_MAX_DISK_USAGE_PERCENT" {
			return "85"
		}
		return ""
	})
	if cfg.Storage.MaxDiskUsagePercent != 85 {
		t.Errorf("max_disk_usage_percent override failed: %d", cfg.Storage.MaxDiskUsagePercent)
	}
}

func TestValidateMaxDiskUsagePercent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		percent int
		wantErr bool
	}{
		{"disabled", 0, false},
		{"mid", 90, false},
		{"max", 100, false},
		{"negative", -1, true},
		{"over", 101, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Storage.MaxDiskUsagePercent = tc.percent
			err := cfg.Validate()
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "storage.max_disk_usage_percent") {
					t.Errorf("percent=%d: expected max_disk_usage_percent error, got %v", tc.percent, err)
				}
			} else if err != nil {
				t.Errorf("percent=%d: expected valid, got %v", tc.percent, err)
			}
		})
	}
}

func TestValidateAllowsZeroSeedTimeToDisableCleanup(t *testing.T) {
	cfg := Default()
	cfg.Cleanup.SeedTime = 0
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected seed_time=0 to be valid (disables cleanup), got %v", err)
	}
}

func TestValidateExternalURLScheme(t *testing.T) {
	cfg := Default()
	cfg.Server.ExternalURL = "ftp://nope"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "external_url") {
		t.Errorf("expected external_url scheme error, got %v", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.Server.AdminPassword = "secret-pass"
	if err := Save(cfg, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Server.AdminPassword != "secret-pass" {
		t.Errorf("round trip lost admin password: %q", loaded.Server.AdminPassword)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	cfg := Config{}
	if err := Save(cfg, filepath.Join(t.TempDir(), "c.yaml")); err == nil {
		t.Fatal("expected validation error on save")
	}
}

func TestEnsureSecrets(t *testing.T) {
	cfg := Default()
	changed, err := EnsureSecrets(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true")
	}
	if len(cfg.Server.AdminPassword) != 16 {
		t.Errorf("admin password length %d; want 16", len(cfg.Server.AdminPassword))
	}

	changed, err = EnsureSecrets(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false on second call")
	}
}

func TestGenerateSecretUnique(t *testing.T) {
	a, err := GenerateSecret(32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSecret(32)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two generated secrets are equal")
	}
}

func TestDownloaderDefaultsAndLegacyConfig(t *testing.T) {
	// A config file predating the downloader/deluge sections loads as
	// qBittorrent with the Deluge defaults intact.
	path := filepath.Join(t.TempDir(), "config.yaml")
	legacy := `
qbittorrent:
  url: "http://qb:8080"
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	if cfg.Downloader.Type != DownloaderQBittorrent {
		t.Errorf("type = %q, want qbittorrent default", cfg.Downloader.Type)
	}
	if cfg.Deluge.Port != 58846 || cfg.Deluge.Username != "localclient" || cfg.Deluge.Label != "seedstrem" {
		t.Errorf("deluge defaults = %+v", cfg.Deluge)
	}
}

func TestDownloaderEnvOverrides(t *testing.T) {
	cfg := Default()
	env := map[string]string{
		"SEEDSTREM_DOWNLOADER_TYPE": "deluge",
		"SEEDSTREM_DELUGE_HOST":     "10.0.0.5",
		"SEEDSTREM_DELUGE_PORT":     "12345",
		"SEEDSTREM_DELUGE_USERNAME": "user",
		"SEEDSTREM_DELUGE_PASSWORD": "pass",
		"SEEDSTREM_DELUGE_LABEL":    "media",
	}
	applyEnv(&cfg, func(k string) string { return env[k] })
	if cfg.Downloader.Type != DownloaderDeluge {
		t.Errorf("type = %q", cfg.Downloader.Type)
	}
	if cfg.Deluge.Host != "10.0.0.5" || cfg.Deluge.Port != 12345 || cfg.Deluge.Username != "user" ||
		cfg.Deluge.Password != "pass" || cfg.Deluge.Label != "media" {
		t.Errorf("deluge = %+v", cfg.Deluge)
	}
}

func TestValidateDownloader(t *testing.T) {
	// Unknown type rejected.
	cfg := Default()
	cfg.Downloader.Type = "transmission"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "downloader.type") {
		t.Errorf("want downloader.type error, got %v", err)
	}

	// Deluge selected: deluge fields validated, qbittorrent.url ignored.
	cfg = Default()
	cfg.Downloader.Type = DownloaderDeluge
	cfg.QBittorrent.URL = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("qbittorrent.url must not be required for deluge: %v", err)
	}
	cfg.Deluge.Host = ""
	cfg.Deluge.Port = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "deluge.host") || !strings.Contains(err.Error(), "deluge.port") {
		t.Errorf("want deluge.host and deluge.port errors, got %v", err)
	}

	// qBittorrent selected: deluge fields not validated.
	cfg = Default()
	cfg.Deluge.Host = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("deluge fields must not be required for qbittorrent: %v", err)
	}
}

func TestDefaultDeletePolicy(t *testing.T) {
	if got := Default().Cleanup.DeletePolicy; got != DeletePolicyOldestFirst {
		t.Errorf("default delete_policy = %q, want %q", got, DeletePolicyOldestFirst)
	}
}

func TestApplyEnvDiskManagementFields(t *testing.T) {
	env := map[string]string{
		"SEEDSTREM_STORAGE_MAX_DOWNLOAD_STORAGE_GB": "3072",
		"SEEDSTREM_CLEANUP_TARGET_RATIO":            "1.5",
		"SEEDSTREM_CLEANUP_DELETE_POLICY":           DeletePolicyLowestUpload,
		"SEEDSTREM_RSS_MAX_CONCURRENT_DOWNLOADS":    "8",
		"SEEDSTREM_RSS_MAX_ACTIVE_TORRENTS":         "300",
	}
	cfg := Default()
	applyEnv(&cfg, func(k string) string { return env[k] })

	if cfg.Storage.MaxDownloadStorageGB != 3072 {
		t.Errorf("max_download_storage_gb = %d, want 3072", cfg.Storage.MaxDownloadStorageGB)
	}
	if cfg.Cleanup.TargetRatio != 1.5 {
		t.Errorf("target_ratio = %v, want 1.5", cfg.Cleanup.TargetRatio)
	}
	if cfg.Cleanup.DeletePolicy != DeletePolicyLowestUpload {
		t.Errorf("delete_policy = %q, want %q", cfg.Cleanup.DeletePolicy, DeletePolicyLowestUpload)
	}
	if cfg.RSS.MaxConcurrentDownloads != 8 {
		t.Errorf("max_concurrent_downloads = %d, want 8", cfg.RSS.MaxConcurrentDownloads)
	}
	if cfg.RSS.MaxActiveTorrents != 300 {
		t.Errorf("max_active_torrents = %d, want 300", cfg.RSS.MaxActiveTorrents)
	}
}

func TestValidateDiskManagementFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string // substring expected in error; "" means must be valid
	}{
		{"negative storage cap", func(c *Config) { c.Storage.MaxDownloadStorageGB = -1 }, "storage.max_download_storage_gb"},
		{"zero storage cap ok", func(c *Config) { c.Storage.MaxDownloadStorageGB = 0 }, ""},
		{"negative target ratio", func(c *Config) { c.Cleanup.TargetRatio = -0.1 }, "cleanup.target_ratio"},
		{"zero target ratio ok", func(c *Config) { c.Cleanup.TargetRatio = 0 }, ""},
		{"bad delete policy", func(c *Config) { c.Cleanup.DeletePolicy = "random" }, "cleanup.delete_policy"},
		{"empty delete policy ok", func(c *Config) { c.Cleanup.DeletePolicy = "" }, ""},
		{"lowest_upload ok", func(c *Config) { c.Cleanup.DeletePolicy = DeletePolicyLowestUpload }, ""},
		{"negative concurrent", func(c *Config) { c.RSS.MaxConcurrentDownloads = -1 }, "rss.max_concurrent_downloads"},
		{"negative active", func(c *Config) { c.RSS.MaxActiveTorrents = -1 }, "rss.max_active_torrents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Errorf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAdoptLabelledDefaultsOff(t *testing.T) {
	// Adoption subjects a hand-added torrent to cleanup's delete rules,
	// so an upgrade must never switch it on.
	if Default().Downloader.AdoptLabelled {
		t.Fatal("adopt_labelled defaults on, want off")
	}
}

func TestClientLabelFollowsDownloaderType(t *testing.T) {
	cfg := Default()

	cfg.Downloader.Type = DownloaderQBittorrent
	cfg.QBittorrent.Category = "qbit-cat"
	if got := cfg.ClientLabel(); got != "qbit-cat" {
		t.Fatalf("ClientLabel() = %q, want %q", got, "qbit-cat")
	}

	cfg.Downloader.Type = DownloaderDeluge
	cfg.Deluge.Label = "deluge-label"
	if got := cfg.ClientLabel(); got != "deluge-label" {
		t.Fatalf("ClientLabel() = %q, want %q", got, "deluge-label")
	}
}

func TestValidateDiskPressure(t *testing.T) {
	base := Default()
	if err := base.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}

	on := Default()
	on.Cleanup.DiskPressurePercent = 75
	on.Cleanup.DiskPressureSeedTime = 48 * time.Hour
	if err := on.Validate(); err != nil {
		t.Errorf("75%% / 48h should be valid: %v", err)
	}

	noMin := on
	noMin.Cleanup.DiskPressureSeedTime = 0
	if err := noMin.Validate(); err == nil {
		t.Error("disk pressure without a minimum seed time should be rejected")
	}

	noDelete := on
	noDelete.Storage.DeleteFilesOnRemove = false
	if err := noDelete.Validate(); err == nil {
		t.Error("disk pressure without delete_files_on_remove should be rejected")
	}

	tooHigh := on
	tooHigh.Cleanup.DiskPressurePercent = 101
	if err := tooHigh.Validate(); err == nil {
		t.Error("disk_pressure_percent above 100 should be rejected")
	}
}
