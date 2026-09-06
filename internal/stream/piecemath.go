// Package stream serves torrent files over HTTP with Range support
// while qBittorrent is still downloading them. Byte ranges are mapped to
// torrent pieces; reads block until the pieces they need exist.
package stream

import (
	"sort"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// FileOffset returns the absolute byte offset of the file with the
// given index inside the torrent's piece space. Piece space covers ALL
// files (selected or not) in torrent order.
func FileOffset(files []downloader.FileInfo, index int) int64 {
	sorted := make([]downloader.FileInfo, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })

	var offset int64
	for _, f := range sorted {
		if f.Index == index {
			return offset
		}
		offset += f.Size
	}
	return -1
}

// PiecesForRange returns the inclusive piece index range covering the
// file-local byte range [start, end] (end inclusive) of a file that
// begins at fileOffset in torrent space.
func PiecesForRange(fileOffset, pieceSize, start, end int64) (first, last int) {
	if pieceSize <= 0 {
		return 0, 0
	}
	first = int((fileOffset + start) / pieceSize)
	last = int((fileOffset + end) / pieceSize)
	return first, last
}
