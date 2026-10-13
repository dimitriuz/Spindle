package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cliamp-web/internal/ipc"
	"cliamp-web/internal/snapshot"
)

type Config struct {
	Cache   *snapshot.Cache
	Runner  ipc.OpRunner
	Hub     *Hub
	Auth    *Auth
	Guard   *HostGuard
	Limiter *Limiter
	// UI is the embedded web interface (webui: index.html, app.css, app.js),
	// served at the root without a token; nil disables it.
	UI     fs.FS
	Logger *slog.Logger
	// LibraryTTL bounds library listing cache freshness (0 ⇒ 60 s).
	LibraryTTL time.Duration
	// LibraryTimeout caps one provider job wait (0 ⇒ 30 s).
	LibraryTimeout time.Duration

	// HeartbeatInterval is the SSE keepalive cadence (0 ⇒ 15 s).
	HeartbeatInterval time.Duration

	listCache      *ttlcache
	providersCache *ttlcache
	noCache        *ttlcache
}

func (c *Config) libraryTimeout() time.Duration {
	if c.LibraryTimeout == 0 {
		return defaultLibraryTimeout
	}
	return c.LibraryTimeout
}

func (c *Config) heartbeat() time.Duration {
	if c.HeartbeatInterval == 0 {
		return 15 * time.Second
	}
	return c.HeartbeatInterval
}

type serverState struct{ cfg Config }

// New assembles the HTTP handler. Auth + Host guard (protect) wrap every
// data endpoint; /api/health is token-free by design. Rate limiting joins
// in Task 7, static SPA serving lands with it.
func New(cfg Config) http.Handler {
	if cfg.LibraryTTL == 0 {
		cfg.LibraryTTL = defaultLibraryTTL
	}
	cfg.listCache = newTTLCache(cfg.LibraryTTL, 128, time.Now)
	cfg.providersCache = newTTLCache(providersTTL, 4, time.Now)
	cfg.noCache = newTTLCache(0, 1, time.Now)
	s := &serverState{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.Handle("GET /api/state", s.protect(http.HandlerFunc(s.state)))
	mux.Handle("GET /api/queue", s.protect(http.HandlerFunc(s.queue)))
	mux.Handle("GET /api/capabilities", s.protect(http.HandlerFunc(s.capabilities)))
	mux.Handle("GET /api/events", s.protect(http.HandlerFunc(s.events)))
	mux.Handle("POST /api/command", s.protect(http.HandlerFunc(s.command)))
	mux.Handle("GET /api/library/providers", s.protect(http.HandlerFunc(s.libraryProviders)))
	mux.Handle("GET /api/library/{provider}/{kind}", s.protect(http.HandlerFunc(s.libraryList)))
	mux.Handle("GET /api/library/{provider}/album/{id}/tracks", s.protect(http.HandlerFunc(s.libraryAlbumTracks)))
	mux.Handle("GET /api/library/{provider}/artist/{id}/tracks", s.protect(http.HandlerFunc(s.libraryArtistTracks)))
	mux.Handle("GET /api/library/{provider}/playlist/{id}/tracks", s.protect(http.HandlerFunc(s.libraryPlaylistTracks)))
	mux.Handle("GET /api/lyrics", s.protect(http.HandlerFunc(s.libraryLyrics)))
	mux.Handle("GET /api/search/{provider}", s.protect(http.HandlerFunc(s.librarySearch)))
	if cfg.UI != nil {
		mux.Handle("GET /next/", http.RedirectHandler("/", http.StatusMovedPermanently)) // old bookmark
	}
	mux.Handle("/", s.uiHandler())
	return mux
}

// uiHandler serves the embedded interface without a token: assets carry no
// player state; only /api/* is guarded. Unknown paths fall back to the page
// so hash routes survive reloads. The page is served as "/" because
// serveFile redirects any request path ending in /index.html to "./".
func (s *serverState) uiHandler() http.Handler {
	if s.cfg.UI == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if f, err := s.cfg.UI.Open(name); err == nil {
				f.Close()
				http.ServeFileFS(w, r, s.cfg.UI, name)
				return
			}
		}
		r.URL.Path = "/"
		http.ServeFileFS(w, r, s.cfg.UI, "index.html")
	})
}
