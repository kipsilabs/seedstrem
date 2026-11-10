package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthWithoutAdmin(t *testing.T) {
	h := New(Options{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("health = %d %s", w.Code, w.Body.String())
	}
}

func TestMountedRouters(t *testing.T) {
	marker := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(name))
		})
	}
	h := New(Options{
		Stremio: marker("stremio"),
		Stream:  marker("stream"),
		Admin:   marker("admin"),
	})

	tests := []struct {
		path string
		want string
	}{
		{"/stremio/manifest.json", "stremio"},
		{"/dl/sometoken", "stream"},
		{"/api/anything", "admin"},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if w.Body.String() != tt.want {
			t.Errorf("%s routed to %q; want %q", tt.path, w.Body.String(), tt.want)
		}
	}
}

func TestSPAServesIndexAndFallback(t *testing.T) {
	h := New(Options{})

	// Root serves the built index.html (web/dist is embedded).
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Errorf("index: %d %.80s", w.Code, w.Body.String())
	}

	// Unknown client-side route falls back to index.html.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/some/client/route", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Errorf("spa fallback: %d %.80s", w.Code, w.Body.String())
	}

	// Non-GET methods are not served by the SPA handler.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/some/client/route", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("POST to spa route = %d; want 404", w.Code)
	}
}

func TestSPACacheHeaders(t *testing.T) {
	h := New(Options{})
	// The shell, service worker and manifest must always revalidate, or an
	// installed PWA can get stuck on a stale build.
	for _, path := range []string{"/", "/sw.js", "/manifest.json", "/some/client/route"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", path, got)
		}
	}
}

func TestSPAServesServiceWorker(t *testing.T) {
	h := New(Options{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "addEventListener") {
		t.Errorf("sw.js = %d %.80s", w.Code, w.Body.String())
	}
}
