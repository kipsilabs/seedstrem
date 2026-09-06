package stremio

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/prowlarr"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func hashesOf(results []prowlarr.Result) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.InfoHash
	}
	return out
}

func TestApplyDiskGate(t *testing.T) {
	// 100-byte disk, threshold 80% → limit 80 bytes.
	const total = int64(100)
	results := []prowlarr.Result{
		{InfoHash: "small", Size: 10},
		{InfoHash: "big", Size: 50},
		{InfoHash: "tiny", Size: 5},
	}

	tests := []struct {
		name     string
		settings DiskSettings
		usage    func(string) (int64, int64, error)
		want     []string
	}{
		{
			name:     "disabled when percent zero",
			settings: DiskSettings{MaxUsagePercent: 0, Path: "/data"},
			usage:    func(string) (int64, int64, error) { return 95, total, nil },
			want:     []string{"small", "big", "tiny"},
		},
		{
			name:     "disabled when path empty",
			settings: DiskSettings{MaxUsagePercent: 80, Path: ""},
			usage:    func(string) (int64, int64, error) { return 95, total, nil },
			want:     []string{"small", "big", "tiny"},
		},
		{
			name:     "over threshold withholds all",
			settings: DiskSettings{MaxUsagePercent: 80, Path: "/data"},
			usage:    func(string) (int64, int64, error) { return 80, total, nil },
			want:     []string{},
		},
		{
			name:     "drops candidates that would cross threshold",
			settings: DiskSettings{MaxUsagePercent: 80, Path: "/data"},
			// used 40, limit 80 → room for 40 bytes: small(10)→50 ok,
			// big(50)→90 dropped, tiny(5)→45 ok.
			usage: func(string) (int64, int64, error) { return 40, total, nil },
			want:  []string{"small", "tiny"},
		},
		{
			name:     "fails open on stat error",
			settings: DiskSettings{MaxUsagePercent: 80, Path: "/data"},
			usage:    func(string) (int64, int64, error) { return 0, 0, errors.New("boom") },
			want:     []string{"small", "big", "tiny"},
		},
		{
			name:     "fails open on zero total",
			settings: DiskSettings{MaxUsagePercent: 80, Path: "/data"},
			usage:    func(string) (int64, int64, error) { return 0, 0, nil },
			want:     []string{"small", "big", "tiny"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{logger: discardLogger(), diskUsage: tc.usage}
			got := hashesOf(h.applyDiskGate(results, Settings{Disk: tc.settings}))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
