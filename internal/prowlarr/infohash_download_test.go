package prowlarr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/metainfo"
)

func TestSearchInfoHashWithDownloadURLPreservesTorrent(t *testing.T) {
	trackers := []string{"https://tracker.example/announce?passkey=test-only", "https://backup.example/announce?passkey=test-only"}
	bstr := func(s string) string { return fmt.Sprintf("%d:%s", len(s), s) }
	raw := []byte("d8:announce" + bstr(trackers[0]) + "13:announce-listll" + bstr(trackers[0]) + "el" + bstr(trackers[1]) + "ee4:infod4:name4:test7:privatei1eee")
	hash, _, _, err := metainfo.FromTorrent(raw)
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"title": "Private release", "infoHash": hash, "downloadUrl": "http://" + r.Host + "/file.torrent", "protocol": "torrent"}})
	})
	mux.HandleFunc("/file.torrent", func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing API key")
		}
		w.Write(raw)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	results, err := NewWithClient(srv.URL, "test-key", srv.Client()).Search(context.Background(), "q", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	result := results[0]
	if fetches.Load() != 1 {
		t.Errorf("torrent fetched %d times, want 1", fetches.Load())
	}
	if !bytes.Equal(result.TorrentFile, raw) {
		t.Error("original torrent bytes, including private flag and announce URLs, were not preserved")
	}
	if result.InfoHash != hash {
		t.Errorf("hash = %s, want %s", result.InfoHash, hash)
	}
	magnet, err := url.Parse(result.MagnetURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := magnet.Query()["tr"]; !reflect.DeepEqual(got, trackers) {
		t.Errorf("trackers = %v, want %v", got, trackers)
	}
}

func TestSearchDownloadURLFallbackBehavior(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:" + hexHash + "&tr=https%3A%2F%2Ftracker.example%2Fannounce"
	for _, tc := range []struct {
		name      string
		magnet    string
		download  bool
		wantFetch int32
	}{
		{"existing magnet", magnet, true, 0},
		{"infohash only", "", false, 0},
		{"failed torrent download", "", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fetches atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
				download := ""
				if tc.download {
					download = "http://" + r.Host + "/file.torrent"
				}
				json.NewEncoder(w).Encode([]map[string]string{{"title": "Release", "infoHash": hexHash, "magnetUrl": tc.magnet, "downloadUrl": download}})
			})
			mux.HandleFunc("/file.torrent", func(w http.ResponseWriter, r *http.Request) {
				fetches.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			results, err := NewWithClient(srv.URL, "key", srv.Client()).Search(context.Background(), "q", "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d results, want 1", len(results))
			}
			if fetches.Load() != tc.wantFetch {
				t.Errorf("fetches = %d, want %d", fetches.Load(), tc.wantFetch)
			}
			want := tc.magnet
			if want == "" {
				want = synthesizeMagnet(hexHash, "Release", nil)
			}
			if results[0].MagnetURL != want {
				t.Errorf("magnet = %q, want %q", results[0].MagnetURL, want)
			}
			if len(results[0].TorrentFile) != 0 {
				t.Error("unexpected torrent file")
			}
		})
	}
}
