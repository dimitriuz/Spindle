package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

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
	Static  fs.FS
	Logger  *slog.Logger
}

type serverState struct{ cfg Config }

// New assembles the HTTP handler. Auth + Host guard (protect) wrap every
// data endpoint; /api/health is token-free by design. Rate limiting joins
// in Task 7, static SPA serving lands with it.
func New(cfg Config) http.Handler {
	s := &serverState{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.Handle("GET /api/state", s.protect(http.HandlerFunc(s.state)))
	mux.Handle("GET /api/queue", s.protect(http.HandlerFunc(s.queue)))
	mux.Handle("GET /api/capabilities", s.protect(http.HandlerFunc(s.capabilities)))
	mux.Handle("GET /api/events", s.protect(http.HandlerFunc(s.events)))
	mux.Handle("POST /api/command", s.protect(http.HandlerFunc(s.command)))
	mux.Handle("/", s.staticHandler())
	return mux
}

// staticHandler serves the embedded SPA without a token: assets carry no
// player state; only /api/* is guarded. Unknown non-API paths fall back to
// index.html so client-side routes survive reloads.
func (s *serverState) staticHandler() http.Handler {
	if s.cfg.Static == nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(http.FS(s.cfg.Static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		f, err := s.cfg.Static.Open(strings.TrimPrefix(path, "/"))
		if err != nil {
			http.ServeFileFS(w, r, s.cfg.Static, "index.html")
			return
		}
		f.Close()
		files.ServeHTTP(w, r)
	})
}
