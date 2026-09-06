package deluge

import (
	"errors"
	"strings"
	"time"

	"github.com/kipsilabs/seedstrem/internal/deluge/delugerpc"
	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// normalizeState maps a Deluge torrent state to the canonical
// downloader.StateXxx constants. seedstrem's canonical vocabulary was
// modeled on Deluge's, so the mapping is near-identity; unknown states
// fall back to StateDownloading (the safe default DeriveStatus uses).
func normalizeState(raw string) string {
	switch raw {
	case "Error":
		return downloader.StateError
	case "Seeding":
		return downloader.StateSeeding
	case "Paused":
		return downloader.StatePaused
	case "Queued":
		return downloader.StateQueued
	case "Checking":
		return downloader.StateChecking
	case "Allocating":
		return downloader.StateAllocating
	case "Moving":
		return downloader.StateMoving
	case "Active", "Downloading":
		return downloader.StateDownloading
	default:
		return downloader.StateDownloading
	}
}

// convertTorrent maps a Deluge status to the neutral TorrentInfo.
// Sequential/first-last flags are not part of Deluge's readable status;
// the client fills them from its best-effort cache.
func convertTorrent(hash string, ts *delugerpc.TorrentStatus) downloader.TorrentInfo {
	info := downloader.TorrentInfo{
		Hash:      strings.ToLower(hash),
		Name:      ts.Name,
		State:     normalizeState(ts.State),
		Progress:  float64(ts.Progress) / 100,
		Size:      wantedSize(ts),
		Completed: ts.TotalDone,
		DlSpeed:   ts.DownloadPayloadRate,
		NumSeeds:  ts.NumSeeds,
		Uploaded:  ts.TotalUploaded,
		Ratio:     float64(ts.Ratio),
		SavePath:  savePath(ts),
		// Deluge has no content-path concept; while downloading, files
		// live under SavePath with their final names.
		ContentPath: "",
		SeedingTime: time.Duration(ts.SeedingTime) * time.Second,
		AddedAt:     time.Unix(int64(ts.TimeAdded), 0),
	}
	if ts.Hash != "" {
		info.Hash = strings.ToLower(ts.Hash)
	}
	return info
}

// savePath prefers the v2 download_location field, falling back to the
// legacy save_path.
func savePath(ts *delugerpc.TorrentStatus) string {
	if ts.DownloadLocation != "" {
		return ts.DownloadLocation
	}
	return ts.SavePath
}

// wantedSize sums the sizes of files that are not skipped (priority 0),
// matching the qBittorrent "wanted size" semantics. When the priority
// array does not line up with the file list (metadata still resolving),
// it falls back to the torrent's total size.
func wantedSize(ts *delugerpc.TorrentStatus) int64 {
	if len(ts.Files) == 0 || len(ts.FilePriorities) != len(ts.Files) {
		return ts.TotalSize
	}
	var wanted int64
	for i, f := range ts.Files {
		if ts.FilePriorities[i] > 0 {
			wanted += f.Size
		}
	}
	return wanted
}

// convertFiles maps Deluge's parallel file arrays into FileInfo. Length
// mismatches (possible while metadata resolves) are guarded per index.
func convertFiles(ts *delugerpc.TorrentStatus) []downloader.FileInfo {
	out := make([]downloader.FileInfo, 0, len(ts.Files))
	for i, f := range ts.Files {
		fi := downloader.FileInfo{
			Index: int(f.Index),
			Name:  f.Path,
			Size:  f.Size,
		}
		if i < len(ts.FileProgress) {
			fi.Progress = float64(ts.FileProgress[i])
		}
		if i < len(ts.FilePriorities) {
			fi.Priority = int(ts.FilePriorities[i])
		}
		out = append(out, fi)
	}
	return out
}

// toDelugePriority maps the neutral file-priority scale (0 = skip,
// 1 = normal, 7 = maximum) onto Deluge's (0 = skip, 1 = low, 4 = normal,
// 7 = high).
func toDelugePriority(p int) int {
	switch {
	case p <= 0:
		return 0
	case p >= 6:
		return 7
	default:
		return 4
	}
}

// convertPieces maps Deluge piece values (deluge/core/torrent.py
// _get_pieces_info: 0 = missing, 1 = available in swarm, 2 = being
// downloaded, 3 = completed) onto the neutral PieceState scale.
func convertPieces(pieces []int) []downloader.PieceState {
	out := make([]downloader.PieceState, len(pieces))
	for i, p := range pieces {
		switch p {
		case 3:
			out[i] = downloader.PieceHave
		case 2:
			out[i] = downloader.PieceDownloading
		default:
			out[i] = downloader.PieceMissing
		}
	}
	return out
}

// isNotFound reports whether err is Deluge signalling an unknown torrent
// id (an InvalidTorrentError raised across RPC).
func isNotFound(err error) bool {
	var rpcErr delugerpc.RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	return strings.Contains(rpcErr.ExceptionType, "InvalidTorrentError") ||
		strings.Contains(rpcErr.ExceptionMessage, "InvalidTorrentError")
}
