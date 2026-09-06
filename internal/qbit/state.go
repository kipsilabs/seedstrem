package qbit

import "github.com/kipsilabs/seedstrem/internal/downloader"

// normalizeState maps a raw qBittorrent WebUI state string to one of the
// canonical downloader.StateXxx constants. Unknown states fall back to
// StateDownloading (the safe default DeriveStatus also uses).
func normalizeState(raw string) string {
	switch raw {
	case "error", "missingFiles":
		return downloader.StateError
	case "uploading", "forcedUP", "stalledUP":
		return downloader.StateSeeding
	case "pausedDL", "pausedUP", "stoppedDL", "stoppedUP":
		return downloader.StatePaused
	case "queuedDL", "queuedUP":
		return downloader.StateQueued
	case "checkingDL", "checkingUP", "checkingResumeData":
		return downloader.StateChecking
	case "allocating":
		return downloader.StateAllocating
	case "moving":
		return downloader.StateMoving
	case "downloading", "forcedDL", "metaDL", "stalledDL":
		return downloader.StateDownloading
	default:
		return downloader.StateDownloading
	}
}
