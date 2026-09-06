package torrents

import (
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/store"
)

// Lifecycle status values seedstrem reports for a torrent. These mirror
// the RealDebrid vocabulary the admin UI already understands.
const (
	StatusMagnetConversion = "magnet_conversion"
	StatusWaitingFiles     = "waiting_files_selection"
	StatusQueued           = "queued"
	StatusDownloading      = "downloading"
	StatusDownloaded       = "downloaded"
	StatusError            = "error"
)

// DeriveStatus maps our lifecycle phase plus live qBittorrent state to a
// coarse status string.
//
//   - stickyError: a persisted local error (e.g. torrent vanished from qBittorrent)
//   - filesKnown: qBittorrent has resolved metadata (file list non-empty)
//   - progress: torrent progress, 0..1
func DeriveStatus(phase, qbittorrentState string, stickyError bool, filesKnown bool, progress float64) string {
	if stickyError {
		return StatusError
	}

	if qbittorrentState == downloader.StateError {
		return StatusError
	}

	if phase == store.PhaseAdded {
		if !filesKnown {
			return StatusMagnetConversion
		}
		return StatusWaitingFiles
	}

	// phase == selected
	if progress >= 1 {
		return StatusDownloaded
	}

	switch qbittorrentState {
	case downloader.StateQueued, downloader.StateChecking, downloader.StateAllocating, downloader.StatePaused:
		return StatusQueued
	case downloader.StateDownloading:
		return StatusDownloading
	case downloader.StateSeeding, downloader.StateMoving, downloader.StateActive:
		// Upload-side state but selected files not at 100%: the wanted
		// files finished (progress is over the whole torrent, but
		// unselected files don't count toward it) — treat as downloaded.
		return StatusDownloaded
	default:
		return StatusDownloading
	}
}
