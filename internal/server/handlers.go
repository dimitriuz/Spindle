package server

import (
	"encoding/json"
	"net/http"
)

func (s *serverState) health(w http.ResponseWriter, r *http.Request) {
	status := "offline"
	if s.cfg.Cache.Online() {
		status = "online"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "true", "player": status})
}

// writeJSON is the single JSON response path for every data endpoint.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// state serves the cached runtime snapshot (never proxies to cliamp).
func (s *serverState) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.Cache.State())
}

// command: body lands in Task 7 (rate limit + operation.submit); the route
// is registered now so the auth contract is pinned early.
func (s *serverState) command(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 503, map[string]any{"error": "not wired yet", "code": "unavailable"})
}
