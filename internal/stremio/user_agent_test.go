package stremio

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// The agent belongs to the request that first added the torrent. Later
// players, including requests without an agent, must not change attribution.
func TestPlayPersistsOriginalUserAgent(t *testing.T) {
	for _, first := range []string{"Stremio/4.4.168", ""} {
		t.Run("first="+first, func(t *testing.T) {
			h := newHarness(t)
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}
			playURL := h.server.URL + "/stremio/play/" + testHash +
				"?magnet=" + url.QueryEscape(testMagnet()) + "&user_agent=spoofed"
			for _, agent := range []string{first, "OtherPlayer/2.0", ""} {
				req, err := http.NewRequest(http.MethodGet, playURL, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header["User-Agent"] = []string{agent}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusFound {
					t.Fatalf("play status = %d, want 302", resp.StatusCode)
				}
				tor, err := h.db.TorrentByHash(context.Background(), testHash)
				if err != nil {
					t.Fatal(err)
				}
				if tor.UserAgent != first {
					t.Fatalf("stored user agent = %q, want original %q", tor.UserAgent, first)
				}
			}
		})
	}
}
