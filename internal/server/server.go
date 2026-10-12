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

// New assembles the HTTP handler. Auth/Host/Limit middleware wrap /api/*;
// the token-free health route is exempt.
func New(cfg Config) http.Handler {
	s := &serverState{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	return mux
}
