package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"cliamp-web/internal/enqueue"
)

// EnqueueSource runs queue actions in the background; *enqueue.Service is the real one.
type EnqueueSource interface {
	Start(enqueue.Request) error
	Cancel()
}

// enqueueStart serves POST /api/enqueue. The work runs in the background;
// progress and the result arrive as SSE "enqueue" events.
func (s *serverState) enqueueStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Enqueue == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_enqueue"})
		return
	}
	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(clientKey(r.RemoteAddr)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate_limited", "code": "rate_limited"})
		return
	}
	var req struct {
		Kind     string         `json:"kind"`
		Provider string         `json:"provider"`
		ID       string         `json:"id"`
		Mode     string         `json:"mode"`
		Track    map[string]any `json:"track"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	err := s.cfg.Enqueue.Start(enqueue.Request{Kind: req.Kind, Provider: req.Provider, ID: req.ID, Mode: req.Mode, Track: req.Track})
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	case errors.Is(err, enqueue.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
	case errors.Is(err, enqueue.ErrUntrusted):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "unknown track", "code": "forbidden"})
	case errors.Is(err, enqueue.ErrBusy):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "busy: already adding to the queue", "code": "busy"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error", "code": "internal_error"})
	}
}

// enqueueCancel serves POST /api/enqueue/cancel.
func (s *serverState) enqueueCancel(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Enqueue == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_enqueue"})
		return
	}
	s.cfg.Enqueue.Cancel()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
