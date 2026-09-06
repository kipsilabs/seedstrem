// Package server assembles the seedstrem HTTP server: the Stremio addon,
// the streaming endpoint, the admin API, and the embedded SPA.
package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/kipsilabs/seedstrem/web"
)

// Options carries the sub-routers mounted onto the top-level server.
// Nil fields are skipped, which keeps early milestones bootable.
type Options struct {
	Logger  *slog.Logger
	Stremio http.Handler // mounted at /stremio
	Stream  http.Handler // mounted at /dl
	Admin   http.Handler // mounted at /api
}

// New builds the top-level http.Handler.
func New(opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	if opts.Stremio != nil {
		r.Mount("/stremio", opts.Stremio)
	}
	if opts.Stream != nil {
		r.Mount("/dl", opts.Stream)
	}
	if opts.Admin != nil {
		// The admin router serves /api/health as a public route.
		r.Mount("/api", opts.Admin)
	} else {
		r.Get("/api/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})
	}

	r.NotFound(spaHandler())
	return r
}

// spaHandler serves the embedded frontend with an index.html fallback
// for client-side routes.
func spaHandler() http.HandlerFunc {
	dist, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		panic("web dist sub fs: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(dist))

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dist, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// Client-side route: fall back to index.html if the UI is built.
		if _, err := fs.Stat(dist, "index.html"); err == nil {
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		http.Error(w, "seedstrem: web UI not built (run make web-build)", http.StatusNotFound)
	}
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Debug("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
