package deluge

import (
	"errors"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/deluge/delugerpc"
	"github.com/kipsilabs/seedstrem/internal/downloader"
)

func TestNormalizeState(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"Error", downloader.StateError},
		{"Seeding", downloader.StateSeeding},
		{"Paused", downloader.StatePaused},
		{"Queued", downloader.StateQueued},
		{"Checking", downloader.StateChecking},
		{"Allocating", downloader.StateAllocating},
		{"Moving", downloader.StateMoving},
		{"Downloading", downloader.StateDownloading},
		{"Active", downloader.StateDownloading},
		{"", downloader.StateDownloading},
		{"SomethingNew", downloader.StateDownloading},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := normalizeState(tt.raw); got != tt.want {
				t.Errorf("normalizeState(%q) = %q; want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestConvertTorrent(t *testing.T) {
	ts := &delugerpc.TorrentStatus{
		Hash:                "ABC123",
		Name:                "Movie",
		State:               "Downloading",
		Progress:            42, // Deluge reports 0..100
		DownloadPayloadRate: 500,
		NumSeeds:            7,
		TotalUploaded:       999,
		Ratio:               1.5,
		SavePath:            "/downloads",
		DownloadLocation:    "/downloads/current",
		SeedingTime:         3600,
		TotalSize:           1000,
		TotalDone:           420,
	}
	info := convertTorrent("abc123", ts)
	if info.Hash != "abc123" {
		t.Errorf("hash = %q, want lowercase abc123", info.Hash)
	}
	if info.Progress != 0.42 {
		t.Errorf("progress = %v, want 0.42 (scaled from percent)", info.Progress)
	}
	if info.State != downloader.StateDownloading {
		t.Errorf("state = %q", info.State)
	}
	if info.SavePath != "/downloads/current" {
		t.Errorf("save path = %q, want v2 download_location", info.SavePath)
	}
	if info.ContentPath != "" {
		t.Errorf("content path = %q, want empty (no Deluge equivalent)", info.ContentPath)
	}
	if info.Size != 1000 {
		t.Errorf("size = %d, want TotalSize fallback with no files", info.Size)
	}
	if info.SeedingTime.Hours() != 1 {
		t.Errorf("seeding time = %v, want 1h", info.SeedingTime)
	}
	if info.Completed != 420 {
		t.Errorf("completed = %d, want 420 (from TotalDone)", info.Completed)
	}
}

func TestWantedSizeSkipsPriorityZero(t *testing.T) {
	ts := &delugerpc.TorrentStatus{
		TotalSize: 300,
		Files: []delugerpc.File{
			{Index: 0, Size: 100, Path: "a"},
			{Index: 1, Size: 200, Path: "b"},
		},
		FilePriorities: []int64{0, 4},
	}
	if got := wantedSize(ts); got != 200 {
		t.Errorf("wanted size = %d, want 200 (skip prio-0 files)", got)
	}
	// Mismatched arrays fall back to total size.
	ts.FilePriorities = []int64{4}
	if got := wantedSize(ts); got != 300 {
		t.Errorf("wanted size = %d, want TotalSize on mismatch", got)
	}
}

func TestConvertFilesGuardsMismatchedArrays(t *testing.T) {
	ts := &delugerpc.TorrentStatus{
		Files: []delugerpc.File{
			{Index: 0, Size: 100, Path: "Movie/a.mkv"},
			{Index: 1, Size: 200, Path: "Movie/b.mkv"},
		},
		FileProgress:   []float32{0.5}, // shorter than Files
		FilePriorities: []int64{4, 0},
	}
	files := convertFiles(ts)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if files[0].Progress != 0.5 || files[0].Priority != 4 {
		t.Errorf("file 0 = %+v", files[0])
	}
	if files[1].Progress != 0 || files[1].Priority != 0 {
		t.Errorf("file 1 = %+v (missing progress must default to 0)", files[1])
	}
}

func TestToDelugePriority(t *testing.T) {
	tests := []struct{ in, want int }{
		{0, 0}, {-1, 0}, {1, 4}, {2, 4}, {5, 4}, {6, 7}, {7, 7}, {9, 7},
	}
	for _, tt := range tests {
		if got := toDelugePriority(tt.in); got != tt.want {
			t.Errorf("toDelugePriority(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestConvertPieces(t *testing.T) {
	got := convertPieces([]int{0, 1, 2, 3})
	want := []downloader.PieceState{
		downloader.PieceMissing,     // 0: missing
		downloader.PieceMissing,     // 1: available in swarm, not downloaded
		downloader.PieceDownloading, // 2: being downloaded
		downloader.PieceHave,        // 3: completed
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("piece %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestIsNotFound(t *testing.T) {
	nf := delugerpc.RPCError{ExceptionType: "InvalidTorrentError", ExceptionMessage: "torrent_id not in session"}
	if !isNotFound(nf) {
		t.Error("InvalidTorrentError must map to not-found")
	}
	if !isNotFound(errors.Join(errors.New("wrap"), nf)) {
		t.Error("wrapped InvalidTorrentError must map to not-found")
	}
	if isNotFound(delugerpc.RPCError{ExceptionType: "AddTorrentError"}) {
		t.Error("other RPC errors must not map to not-found")
	}
	if isNotFound(errors.New("plain")) {
		t.Error("non-RPC errors must not map to not-found")
	}
}
