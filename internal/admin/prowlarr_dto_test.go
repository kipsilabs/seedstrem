package admin

import (
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/config"
)

func TestConfigDTORoundTripsProwlarrSearchTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.Prowlarr.SearchTimeout = 25 * time.Second

	dto := toDTO(cfg)
	if dto.Prowlarr.SearchTimeoutSeconds != 25 {
		t.Fatalf("toDTO prowlarr.search_timeout_seconds = %d; want 25", dto.Prowlarr.SearchTimeoutSeconds)
	}

	got := dto.apply(config.Default())
	if got.Prowlarr.SearchTimeout != 25*time.Second {
		t.Fatalf("apply prowlarr.SearchTimeout = %v; want 25s", got.Prowlarr.SearchTimeout)
	}
}

func TestApplyProwlarrZeroSearchTimeoutKeepsStored(t *testing.T) {
	// A client clearing the timeout (0) must not produce an invalid <=0
	// budget — the stored value is kept, mirroring the metadata timeout.
	current := config.Default()
	current.Prowlarr.SearchTimeout = 40 * time.Second

	var dto configDTO
	dto.Prowlarr.SearchTimeoutSeconds = 0

	got := dto.apply(current)
	if got.Prowlarr.SearchTimeout != 40*time.Second {
		t.Errorf("SearchTimeout = %v; want kept 40s", got.Prowlarr.SearchTimeout)
	}
}
