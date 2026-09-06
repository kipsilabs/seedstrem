package admin

import (
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/config"
)

func TestConfigDTORoundTripsRSS(t *testing.T) {
	cfg := config.Default()
	cfg.RSS.Enabled = true
	cfg.RSS.Interval = 20 * time.Minute
	cfg.RSS.MaxGrabsPerCycle = 8
	cfg.RSS.FreeleechOnly = true
	cfg.RSS.Filters = config.RSSFilters{
		MinSizeMB:       500,
		MaxSizeMB:       20000,
		Categories:      []int{2000, 5000},
		IncludeKeywords: []string{"1080p", "2160p"},
		ExcludeKeywords: []string{"CAM"},
	}

	dto := toDTO(cfg)
	if !dto.RSS.Enabled || dto.RSS.IntervalMinutes != 20 || dto.RSS.MaxGrabsPerCycle != 8 || !dto.RSS.FreeleechOnly {
		t.Fatalf("toDTO rss = %+v", dto.RSS)
	}
	if dto.RSS.Filters.MinSizeMB != 500 || dto.RSS.Filters.MaxSizeMB != 20000 {
		t.Fatalf("toDTO rss.filters sizes = %+v", dto.RSS.Filters)
	}
	if len(dto.RSS.Filters.Categories) != 2 || len(dto.RSS.Filters.IncludeKeywords) != 2 || len(dto.RSS.Filters.ExcludeKeywords) != 1 {
		t.Fatalf("toDTO rss.filters lists = %+v", dto.RSS.Filters)
	}

	got := dto.apply(config.Default())
	if !got.RSS.Enabled || got.RSS.Interval != 20*time.Minute || got.RSS.MaxGrabsPerCycle != 8 || !got.RSS.FreeleechOnly {
		t.Fatalf("apply rss = %+v", got.RSS)
	}
	gf := got.RSS.Filters
	if gf.MinSizeMB != 500 || gf.MaxSizeMB != 20000 {
		t.Fatalf("apply rss.filters sizes = %+v", gf)
	}
	if len(gf.Categories) != 2 || gf.Categories[1] != 5000 {
		t.Fatalf("apply rss.filters.categories = %v", gf.Categories)
	}
	if len(gf.IncludeKeywords) != 2 || gf.IncludeKeywords[0] != "1080p" {
		t.Fatalf("apply rss.filters.include_keywords = %v", gf.IncludeKeywords)
	}
	if len(gf.ExcludeKeywords) != 1 || gf.ExcludeKeywords[0] != "CAM" {
		t.Fatalf("apply rss.filters.exclude_keywords = %v", gf.ExcludeKeywords)
	}
}

func TestConfigDTORoundTripsDiskManagement(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.MaxDownloadStorageGB = 3072
	cfg.Cleanup.TargetRatio = 1.5
	cfg.Cleanup.DeletePolicy = config.DeletePolicyLowestUpload
	cfg.RSS.MaxConcurrentDownloads = 8
	cfg.RSS.MaxActiveTorrents = 300

	dto := toDTO(cfg)
	if dto.Storage.MaxDownloadStorageGB != 3072 {
		t.Errorf("toDTO storage.max_download_storage_gb = %d; want 3072", dto.Storage.MaxDownloadStorageGB)
	}
	if dto.Cleanup.TargetRatio != 1.5 || dto.Cleanup.DeletePolicy != config.DeletePolicyLowestUpload {
		t.Errorf("toDTO cleanup = %+v", dto.Cleanup)
	}
	if dto.RSS.MaxConcurrentDownloads != 8 || dto.RSS.MaxActiveTorrents != 300 {
		t.Errorf("toDTO rss gates = %+v", dto.RSS)
	}

	got := dto.apply(config.Default())
	if got.Storage.MaxDownloadStorageGB != 3072 {
		t.Errorf("apply storage.max_download_storage_gb = %d; want 3072", got.Storage.MaxDownloadStorageGB)
	}
	if got.Cleanup.TargetRatio != 1.5 || got.Cleanup.DeletePolicy != config.DeletePolicyLowestUpload {
		t.Errorf("apply cleanup = %+v", got.Cleanup)
	}
	if got.RSS.MaxConcurrentDownloads != 8 || got.RSS.MaxActiveTorrents != 300 {
		t.Errorf("apply rss gates = %+v", got.RSS)
	}
}

func TestApplyEmptyDeletePolicyKeepsStored(t *testing.T) {
	// A client that doesn't send delete_policy (empty) must not blank the
	// stored default.
	current := config.Default()
	current.Cleanup.DeletePolicy = config.DeletePolicyLowestUpload

	var dto configDTO
	dto.Cleanup.DeletePolicy = ""

	if got := dto.apply(current).Cleanup.DeletePolicy; got != config.DeletePolicyLowestUpload {
		t.Errorf("delete_policy = %q; want kept %q", got, config.DeletePolicyLowestUpload)
	}
}

func TestApplyRSSZeroIntervalKeepsStored(t *testing.T) {
	// A client clearing the interval (0) must not produce an invalid <=0
	// interval — the stored value is kept.
	current := config.Default()
	current.RSS.Interval = 45 * time.Minute

	var dto configDTO
	dto.RSS.IntervalMinutes = 0
	dto.RSS.MaxGrabsPerCycle = 0 // meaningful: disables grabbing, must apply

	got := dto.apply(current)
	if got.RSS.Interval != 45*time.Minute {
		t.Errorf("interval = %v; want kept 45m", got.RSS.Interval)
	}
	if got.RSS.MaxGrabsPerCycle != 0 {
		t.Errorf("max_grabs_per_cycle = %d; want 0 (applied)", got.RSS.MaxGrabsPerCycle)
	}
}
