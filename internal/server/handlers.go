package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"cliamp-web/internal/ipc"
)

// queue serves the cached live playlist.
func (s *serverState) queue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.Cache.Queue())
}

// capabilities serves the cached cliamp capabilities document.
func (s *serverState) capabilities(w http.ResponseWriter, r *http.Request) {
	caps := s.cfg.Cache.Caps()
	if len(caps) == 0 {
		caps = json.RawMessage(`{}`)
	}
	writeJSON(w, 200, caps)
}

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

// command forwards one operation to cliamp and waits for the job.
func (s *serverState) command(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(r.RemoteAddr) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate_limited", "code": "rate_limited"})
		return
	}
	var req struct {
		Op     string         `json:"op"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Op == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	job, err := s.cfg.Runner.SubmitJob(ctx, req.Op, req.Params)
	if err != nil {
		code := "internal_error"
		var ie *ipc.IPCError
		switch {
		case errors.As(err, &ie):
			code = ie.Code
		case errors.Is(err, ipc.ErrNotConnected):
			code = "unavailable"
		}
		writeJSON(w, statusForCode(code), map[string]any{"error": err.Error(), "code": code})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func statusForCode(code string) int {
	switch code {
	case "invalid_params":
		return http.StatusBadRequest
	case "conflict":
		return http.StatusConflict
	case "not_found", "unknown_operation":
		return http.StatusNotFound
	case "unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
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
