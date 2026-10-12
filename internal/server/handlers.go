package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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

// events is the SSE stream: replay of current player+state on connect,
// then hub frames, with comment heartbeats every 15 s.
func (s *serverState) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]any{"error": "streaming unsupported", "code": "internal_error"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	id, ch := s.cfg.Hub.Subscribe()
	defer s.cfg.Hub.Unsubscribe(id)

	status := "offline"
	if s.cfg.Cache.Online() {
		status = "online"
	}
	fmt.Fprintf(w, "event: player\ndata: {\"status\":%q}\n\n", status)
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", s.cfg.Cache.State())
	flusher.Flush()

	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case f, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", f.Name, f.Data)
			flusher.Flush()
		}
	}
}
