package qbit

import (
	"errors"
	"fmt"
	"testing"
	"time"

	qbt "github.com/autobrr/go-qbittorrent"
	pkgerrors "github.com/pkg/errors"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

func TestIsNotFound(t *testing.T) {
	if !isNotFound(qbt.ErrTorrentNotFound) {
		t.Error("bare ErrTorrentNotFound should be not-found")
	}
	// The library wraps the sentinel with github.com/pkg/errors.Wrap
	// (as GetFilesInformationCtx does on a 404) — isNotFound must see
	// through it, which is the whole point of this fix.
	wrapped := pkgerrors.Wrap(qbt.ErrTorrentNotFound, "could not get files info; torrent hash not found")
	if !isNotFound(wrapped) {
		t.Error("pkg/errors-wrapped ErrTorrentNotFound should be not-found")
	}
	if !isNotFound(fmt.Errorf("ctx: %w", qbt.ErrTorrentMetadataNotDownloadedYet)) {
		t.Error("metadata-not-ready should be treated as not-found (keep polling)")
	}
	if isNotFound(errors.New("some other error")) {
		t.Error("unrelated error must not be not-found")
	}
}

func TestConvertTorrent(t *testing.T) {
	info := convertTorrent(qbt.Torrent{
		Hash:        "abc123",
		Name:        "Movie",
		State:       qbt.TorrentState("downloading"),
		Progress:    0.42,
		Size:        1000,
		Completed:   420,
		DlSpeed:     500,
		NumSeeds:    7,
		SavePath:    "/downloads",
		SeedingTime: 3600,
	})
	if info.Hash != "abc123" || info.Name != "Movie" {
		t.Errorf("basic fields lost: %+v", info)
	}
	if info.State != downloader.StateDownloading {
		t.Errorf("state = %q, want %q", info.State, downloader.StateDownloading)
	}
	if info.Progress != 0.42 || info.Size != 1000 || info.DlSpeed != 500 || info.NumSeeds != 7 || info.SavePath != "/downloads" {
		t.Errorf("fields lost: %+v", info)
	}
	if info.SeedingTime != time.Hour {
		t.Errorf("seeding time = %v, want 1h (converted from seconds)", info.SeedingTime)
	}
	if info.Completed != 420 {
		t.Errorf("completed = %d, want 420", info.Completed)
	}
}

func TestNormalizeState(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"error", downloader.StateError},
		{"missingFiles", downloader.StateError},
		{"downloading", downloader.StateDownloading},
		{"forcedDL", downloader.StateDownloading},
		{"metaDL", downloader.StateDownloading},
		{"stalledDL", downloader.StateDownloading},
		{"uploading", downloader.StateSeeding},
		{"stalledUP", downloader.StateSeeding},
		{"forcedUP", downloader.StateSeeding},
		{"pausedDL", downloader.StatePaused},
		{"pausedUP", downloader.StatePaused},
		{"stoppedDL", downloader.StatePaused},
		{"stoppedUP", downloader.StatePaused},
		{"queuedDL", downloader.StateQueued},
		{"queuedUP", downloader.StateQueued},
		{"checkingDL", downloader.StateChecking},
		{"checkingUP", downloader.StateChecking},
		{"checkingResumeData", downloader.StateChecking},
		{"allocating", downloader.StateAllocating},
		{"moving", downloader.StateMoving},
		{"unknown", downloader.StateDownloading},
		{"something-new", downloader.StateDownloading},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := normalizeState(tt.raw); got != tt.want {
				t.Errorf("normalizeState(%q) = %q; want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestAddOptionsMap(t *testing.T) {
	c := &client{category: "default-cat"}

	// Client's configured category is used when opts.Category is empty.
	m := c.addOptionsMap(downloader.AddOptions{Stopped: true, SequentialDownload: true, FirstLastPiecePrio: true})
	if m["category"] != "default-cat" {
		t.Errorf("category = %q, want default-cat", m["category"])
	}
	if m["stopped"] != "true" || m["paused"] != "true" {
		t.Errorf("stopped/paused not both set: %v", m)
	}
	if m["sequentialDownload"] != "true" || m["firstLastPiecePrio"] != "true" {
		t.Errorf("sequential/firstLast not set: %v", m)
	}

	// Explicit opts.Category overrides the client default.
	m = c.addOptionsMap(downloader.AddOptions{Category: "explicit"})
	if m["category"] != "explicit" {
		t.Errorf("category = %q, want explicit", m["category"])
	}
	if _, ok := m["stopped"]; ok {
		t.Errorf("stopped should be absent when not requested: %v", m)
	}
}
