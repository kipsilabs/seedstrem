package torrents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
)

func TestWarmupRanges(t *testing.T) {
	const mib = 1 << 20
	cases := []struct {
		name      string
		files     []downloader.FileInfo
		index     int
		pieceSize int64
		want      []pieceRange
	}{
		{
			name:      "single big file, 4MiB pieces",
			files:     []downloader.FileInfo{{Index: 0, Size: 8 << 30}},
			index:     0,
			pieceSize: 4 * mib,
			// head: bytes [0,8MiB) -> pieces 0-1; tail: last 4MiB -> last piece.
			want: []pieceRange{{0, 1}, {2047, 2047}},
		},
		{
			name:      "file after another one shifts the piece space",
			files:     []downloader.FileInfo{{Index: 0, Size: 100 * mib}, {Index: 1, Size: 400 * mib}},
			index:     1,
			pieceSize: 1 * mib,
			// offset 100MiB: head 100-107, tail 496-499.
			want: []pieceRange{{100, 107}, {496, 499}},
		},
		{
			name:      "small file: head and tail overlap and merge",
			files:     []downloader.FileInfo{{Index: 0, Size: 6 * mib}},
			index:     0,
			pieceSize: 1 * mib,
			want:      []pieceRange{{0, 5}},
		},
		{
			name:      "huge pieces: one piece covers both ends",
			files:     []downloader.FileInfo{{Index: 0, Size: 50 * mib}},
			index:     0,
			pieceSize: 16 * mib,
			want:      []pieceRange{{0, 0}, {2, 3}},
		},
		{
			name:      "unsorted file list still resolves the offset by index",
			files:     []downloader.FileInfo{{Index: 1, Size: 400 * mib}, {Index: 0, Size: 100 * mib}},
			index:     1,
			pieceSize: 1 * mib,
			want:      []pieceRange{{100, 107}, {496, 499}},
		},
		{name: "unknown piece size", files: []downloader.FileInfo{{Index: 0, Size: mib}}, index: 0, pieceSize: 0, want: nil},
		{name: "unknown file", files: []downloader.FileInfo{{Index: 0, Size: mib}}, index: 3, pieceSize: mib, want: nil},
		{name: "empty file", files: []downloader.FileInfo{{Index: 0, Size: 0}}, index: 0, pieceSize: mib, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := warmupRanges(tc.files, tc.index, tc.pieceSize)
			if len(got) != len(tc.want) {
				t.Fatalf("ranges = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ranges = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func prioCalls(fakeDC *fake.Server) []string {
	var out []string
	for _, c := range fakeDC.Calls() {
		if strings.HasPrefix(c, "prioritizePieces ") {
			out = append(out, c)
		}
	}
	return out
}

func newWarmupService(t *testing.T) (*Service, *fake.Server) {
	t.Helper()
	svc, fakeDC, _ := newService(t)
	// No real sleeping between retries.
	svc.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	fakeDC.SetPrioritizeErr(nil) // capable backend by default
	fakeDC.Put(&fake.Torrent{
		Hash:      testHash,
		State:     downloader.StatePaused,
		PieceSize: 4 << 20,
		Files:     []fake.File{{Name: "Movie.mkv", Size: 8 << 30}},
	})
	return svc, fakeDC
}

var warmupFiles = []downloader.FileInfo{{Index: 0, Name: "Movie.mkv", Size: 8 << 30}}

func TestRunWarmupHintsHeadAndTail(t *testing.T) {
	svc, fakeDC := newWarmupService(t)

	svc.runWarmup(context.Background(), testHash, 0, warmupFiles)

	got := prioCalls(fakeDC)
	if len(got) != 2 {
		t.Fatalf("prioritize calls = %v, want head and tail", got)
	}
	// Head and tail are hinted concurrently, so order is not defined.
	joined := strings.Join(got, "\n")
	for _, want := range []string{"first=0 last=1", "first=2047 last=2047"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing hint %q in %v", want, got)
		}
	}
}

func TestRunWarmupRetriesDeclinedHint(t *testing.T) {
	svc, fakeDC := newWarmupService(t)
	fakeDC.SetPrioritizeErr(downloader.ErrHintDeclined)

	svc.runWarmup(context.Background(), testHash, 0, warmupFiles)

	got := prioCalls(fakeDC)
	if len(got) < 4 {
		t.Fatalf("declined hints must be retried, got %d calls: %v", len(got), got)
	}
	if len(got) > 2*warmupMaxAttempts {
		t.Errorf("retries must be bounded: %d calls", len(got))
	}
}

func TestRunWarmupStopsWhenUnsupported(t *testing.T) {
	svc, fakeDC := newWarmupService(t)
	fakeDC.SetPrioritizeErr(downloader.ErrNotSupported)

	svc.runWarmup(context.Background(), testHash, 0, warmupFiles)

	if got := prioCalls(fakeDC); len(got) != 2 {
		t.Fatalf("unsupported backend must stop after one try per range, got %v", got)
	}
}

func TestRunWarmupStopsOnOtherErrors(t *testing.T) {
	svc, fakeDC := newWarmupService(t)
	fakeDC.SetPrioritizeErr(errors.New("rpc exploded"))

	svc.runWarmup(context.Background(), testHash, 0, warmupFiles)

	if got := prioCalls(fakeDC); len(got) != 2 {
		t.Fatalf("hard error must not be retried (one try per range), got %v", got)
	}
}

func TestRunWarmupStopsWhenContextCancelled(t *testing.T) {
	svc, fakeDC := newWarmupService(t)
	fakeDC.SetPrioritizeErr(downloader.ErrHintDeclined)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc.runWarmup(ctx, testHash, 0, warmupFiles)

	if got := prioCalls(fakeDC); len(got) != 0 {
		t.Fatalf("cancelled warm-up must not hint, got %v", got)
	}
}

func TestSelectAndLinkStartsWarmup(t *testing.T) {
	svc, fakeDC := newWarmupService(t)

	if _, err := svc.Resolve(context.Background(), testMagnet("Movie"), nil, Selector{}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(prioCalls(fakeDC)) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("warm-up never hinted head and tail: calls=%v", fakeDC.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
