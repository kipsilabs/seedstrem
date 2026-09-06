package torrents

import (
	"errors"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// ErrNoFileMatch is returned when no file in a torrent matches the selector.
var ErrNoFileMatch = errors.New("no matching file in torrent")

// Selector describes which file to stream out of a (possibly multi-file)
// torrent. For movies IsSeries is false and the largest video file wins.
// For series/anime the file whose name matches Season/Episode wins.
//
// Source and ContentRef carry the Stremio content identity the play was
// requested for (e.g. "tt"/"tt0944947"); they are not used for file
// picking but are persisted with the torrent so later stream requests for
// the same content can surface it as already-owned. Both empty when
// unknown.
//
// Indexer is the Prowlarr indexer the release came from; like the content
// identity it is not used for file picking, only persisted — cleanup reads
// it to apply a per-indexer seed time. Empty when unknown.
type Selector struct {
	IsSeries   bool
	Season     int // 0 when unknown (anime absolute numbering)
	Episode    int
	Source     string
	ContentRef string
	Indexer    string
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".ts": true, ".webm": true, ".mov": true, ".wmv": true, ".flv": true,
}

var (
	// S01E05 / s1e5 / S01.E05 / S01 E05
	reSeasonEp = regexp.MustCompile(`(?i)s0*(\d{1,3})[\s._-]*e0*(\d{1,4})`)
	// 1x05
	reNxM = regexp.MustCompile(`(?i)\b(\d{1,3})x0*(\d{1,4})\b`)
	// E05 / Ep05 / Episode 5 / Cap 5 / Capitulo 5 (episode only, no season)
	reEpOnly = regexp.MustCompile(`(?i)(?:\bep|\bepisode|\bcap(?:itulo)?|\be)[\s._-]*0*(\d{1,4})\b`)
	// " - 05 " / " - 05." anime absolute numbering after a dash
	reDashNum = regexp.MustCompile(`[\s._-]-[\s._-]*0*(\d{1,4})(?:[\s._-]|$)`)
	// [05] bracketed absolute numbering
	reBracketNum = regexp.MustCompile(`\[0*(\d{1,4})\]`)
)

// IsVideo reports whether name has a known video extension.
func IsVideo(name string) bool {
	return videoExts[strings.ToLower(path.Ext(name))]
}

// isSample reports whether name looks like a throwaway sample file.
func isSample(name string) bool {
	return strings.Contains(strings.ToLower(name), "sample")
}

// matchEpisode reports whether a release filename refers to the given
// season/episode. season <= 0 means "season unknown" (common for anime),
// in which case only the episode number must match.
func matchEpisode(name string, season, episode int) bool {
	if episode <= 0 {
		return false
	}
	if m := reSeasonEp.FindStringSubmatch(name); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return (season <= 0 || s == season) && e == episode
	}
	if m := reNxM.FindStringSubmatch(name); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return (season <= 0 || s == season) && e == episode
	}
	if m := reEpOnly.FindStringSubmatch(name); m != nil {
		e, _ := strconv.Atoi(m[1])
		return e == episode
	}
	// Absolute numbering is only trusted when the season is unknown or 1
	// (anime), to avoid matching resolutions/years in movie packs.
	if season <= 1 {
		if m := reDashNum.FindStringSubmatch(name); m != nil {
			e, _ := strconv.Atoi(m[1])
			return e == episode
		}
		if m := reBracketNum.FindStringSubmatch(name); m != nil {
			e, _ := strconv.Atoi(m[1])
			return e == episode
		}
	}
	return false
}

// PickFile returns the index of the file to stream for sel, or an error
// when nothing suitable is found.
func PickFile(files []downloader.FileInfo, sel Selector) (int, error) {
	best := -1
	var bestSize int64 = -1
	for _, f := range files {
		if !IsVideo(f.Name) || isSample(f.Name) {
			continue
		}
		if sel.IsSeries && !matchEpisode(f.Name, sel.Season, sel.Episode) {
			continue
		}
		if f.Size > bestSize {
			best = f.Index
			bestSize = f.Size
		}
	}
	if best >= 0 {
		return best, nil
	}
	return 0, ErrNoFileMatch
}
