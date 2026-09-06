package stream

import (
	"testing"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

func TestFileOffset(t *testing.T) {
	files := []downloader.FileInfo{
		{Index: 0, Size: 100},
		{Index: 1, Size: 250},
		{Index: 2, Size: 0}, // zero-length file
		{Index: 3, Size: 50},
	}

	tests := []struct {
		index int
		want  int64
	}{
		{0, 0},
		{1, 100},
		{2, 350},
		{3, 350}, // zero-length predecessor adds nothing
		{9, -1},  // unknown index
	}
	for _, tt := range tests {
		if got := FileOffset(files, tt.index); got != tt.want {
			t.Errorf("FileOffset(index=%d) = %d; want %d", tt.index, got, tt.want)
		}
	}
}

func TestFileOffsetUnorderedInput(t *testing.T) {
	// qbittorrent should return files in index order, but don't depend on it.
	files := []downloader.FileInfo{
		{Index: 2, Size: 30},
		{Index: 0, Size: 10},
		{Index: 1, Size: 20},
	}
	if got := FileOffset(files, 2); got != 30 {
		t.Errorf("FileOffset = %d; want 30", got)
	}
}

func TestPiecesForRange(t *testing.T) {
	tests := []struct {
		name       string
		fileOffset int64
		pieceSize  int64
		start, end int64
		wantFirst  int
		wantLast   int
	}{
		{"file at torrent start, first byte", 0, 100, 0, 0, 0, 0},
		{"whole first piece", 0, 100, 0, 99, 0, 0},
		{"straddles piece boundary", 0, 100, 50, 150, 0, 1},
		{"exactly second piece", 0, 100, 100, 199, 1, 1},
		{"file offset shifts pieces", 250, 100, 0, 0, 2, 2},
		{"offset + range straddle", 250, 100, 40, 60, 2, 3},
		{"mid-torrent big range", 1000, 100, 0, 999, 10, 19},
		{"last partial piece", 0, 100, 950, 987, 9, 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, last := PiecesForRange(tt.fileOffset, tt.pieceSize, tt.start, tt.end)
			if first != tt.wantFirst || last != tt.wantLast {
				t.Errorf("PiecesForRange(%d, %d, %d, %d) = (%d, %d); want (%d, %d)",
					tt.fileOffset, tt.pieceSize, tt.start, tt.end, first, last, tt.wantFirst, tt.wantLast)
			}
		})
	}
}

func TestPiecesForRangeZeroPieceSize(t *testing.T) {
	// Defensive: metadata not resolved yet must not divide by zero.
	first, last := PiecesForRange(0, 0, 0, 100)
	if first != 0 || last != 0 {
		t.Errorf("got (%d, %d); want (0, 0)", first, last)
	}
}
