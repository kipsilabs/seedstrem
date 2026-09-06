package stream

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
)

func TestRemap(t *testing.T) {
	mappings := []config.Mapping{
		{Remote: "/downloads", Local: "/data"},
		{Remote: "/downloads/movies", Local: "/mnt/movies"}, // longer prefix wins
	}

	tests := []struct {
		in   string
		want string
	}{
		{"/downloads/file.mkv", "/data/file.mkv"},
		{"/downloads/movies/file.mkv", "/mnt/movies/file.mkv"},
		{"/downloads", "/data"},
		{"/elsewhere/file.mkv", "/elsewhere/file.mkv"},       // passthrough
		{"/downloadsfoo/file.mkv", "/downloadsfoo/file.mkv"}, // not a path-segment match
	}
	for _, tt := range tests {
		if got := Remap(mappings, tt.in); got != tt.want {
			t.Errorf("Remap(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func newResolverEnv(t *testing.T, mappings []config.Mapping) (*Resolver, *fake.Server) {
	t.Helper()
	f := fake.New()
	return NewResolver(f, func() []config.Mapping { return mappings }), f
}

func TestFilePathMultiFileTorrent(t *testing.T) {
	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "Movie"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(local, "Movie", "movie.mkv")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _ := newResolverEnv(t, []config.Mapping{{Remote: "/downloads", Local: local}})

	info := downloader.TorrentInfo{Hash: testHash, SavePath: "/downloads"}
	file := downloader.FileInfo{Index: 0, Name: "Movie/movie.mkv", Size: 1}

	got, err := r.FilePath(context.Background(), info, file)
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}
	if got != target {
		t.Errorf("path = %q; want %q", got, target)
	}
}

func TestFilePathFindsIncompleteExtension(t *testing.T) {
	// A still-downloading single-file torrent with the ".!qB" suffix.
	local := t.TempDir()
	target := filepath.Join(local, "movie.mkv.!qB")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, f := newResolverEnv(t, []config.Mapping{{Remote: "/downloads", Local: local}})
	f.SetHints(downloader.IncompleteHints{IncompleteExt: ".!qB"})
	info := downloader.TorrentInfo{Hash: testHash, SavePath: "/downloads"}
	file := downloader.FileInfo{Index: 0, Name: "movie.mkv", Size: 1}

	got, err := r.FilePath(context.Background(), info, file)
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}
	if got != target {
		t.Errorf("path = %q; want incomplete file %q", got, target)
	}
}

func TestFilePathFindsViaContentPath(t *testing.T) {
	// While downloading, qBittorrent keeps content under a temp folder
	// reported as content_path; save_path points at the (empty) final
	// location.
	local := t.TempDir()
	incompleteDir := filepath.Join(local, "incomplete")
	if err := os.MkdirAll(incompleteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(incompleteDir, "movie.mkv")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _ := newResolverEnv(t, []config.Mapping{{Remote: "/downloads", Local: local}})
	// Single-file torrent: content_path IS the file. SavePath (final) has
	// no file yet.
	info := downloader.TorrentInfo{
		Hash:        testHash,
		SavePath:    "/downloads",
		ContentPath: "/downloads/incomplete/movie.mkv",
	}
	file := downloader.FileInfo{Index: 0, Name: "movie.mkv", Size: 1}

	got, err := r.FilePath(context.Background(), info, file)
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}
	if got != target {
		t.Errorf("path = %q; want content_path file %q", got, target)
	}
}

func TestFilePathRejectsTraversal(t *testing.T) {
	local := t.TempDir()
	// Plant a file outside the mapped root that a traversal would reach.
	outside := filepath.Join(filepath.Dir(local), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	r, _ := newResolverEnv(t, []config.Mapping{{Remote: "/downloads", Local: local}})
	info := downloader.TorrentInfo{Hash: testHash, SavePath: "/downloads"}
	file := downloader.FileInfo{Index: 0, Name: "../secret.txt", Size: 6}

	_, err := r.FilePath(context.Background(), info, file)
	if !errors.Is(err, ErrUnsafePath) {
		t.Errorf("want ErrUnsafePath for traversal name, got %v", err)
	}
}

func TestFilePathRejectsEscapeOutsideRoot(t *testing.T) {
	// Even without ".." in the file name, a candidate that resolves
	// outside every mapping root must not be served.
	local := t.TempDir()
	r, _ := newResolverEnv(t, []config.Mapping{{Remote: "/downloads", Local: local}})
	// SavePath points somewhere unmapped; the remap leaves it as-is, so
	// it lands outside local and must be rejected.
	info := downloader.TorrentInfo{Hash: testHash, SavePath: "/elsewhere"}
	file := downloader.FileInfo{Index: 0, Name: "movie.mkv", Size: 1}

	// Out-of-root candidates are skipped, so nothing is served: FilePath
	// returns an error and no path. (The specific sentinel is not part of
	// the contract — the security guarantee is "not served".)
	got, err := r.FilePath(context.Background(), info, file)
	if err == nil {
		t.Errorf("want error for unmapped path, got path %q", got)
	}
	if got != "" {
		t.Errorf("must not return a path for an out-of-root candidate, got %q", got)
	}
}

func TestWithinAnyRoot(t *testing.T) {
	roots := []string{"/data", "/mnt/media"}
	cases := map[string]bool{
		"/data/movie.mkv":    true,
		"/data":              true,
		"/mnt/media/a/b.mkv": true,
		"/etc/passwd":        false,
		"/datax/movie.mkv":   false, // prefix but not a path segment
		"/mnt/mediafoo/x":    false,
	}
	for p, want := range cases {
		if got := withinAnyRoot(p, roots); got != want {
			t.Errorf("withinAnyRoot(%q) = %v; want %v", p, got, want)
		}
	}
	// No roots configured → permissive.
	if !withinAnyRoot("/anything", nil) {
		t.Error("empty roots should permit any path")
	}
}

func TestFilePathNotFound(t *testing.T) {
	r, _ := newResolverEnv(t, []config.Mapping{{Remote: "/downloads", Local: t.TempDir()}})
	info := downloader.TorrentInfo{Hash: testHash, SavePath: "/downloads"}
	file := downloader.FileInfo{Index: 0, Name: "missing.mkv", Size: 1}

	_, err := r.FilePath(context.Background(), info, file)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("want ErrNotExist, got %v", err)
	}
}
