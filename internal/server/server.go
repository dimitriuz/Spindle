package server

import (
	"io/fs"
	"log/slog"
	"net/http"

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
	mux.Handle("POST /api/command", s.protect(http.HandlerFunc(s.command)))
	mux.Handle("GET /api/events", s.protect(http.HandlerFunc(s.events)))
	return mux
}
