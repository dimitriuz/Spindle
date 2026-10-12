package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"cliamp-web/internal/ipc"
)

const (
	defaultLibraryTTL     = 60 * time.Second
	defaultLibraryTimeout = 30 * time.Second
	providersTTL          = 5 * time.Minute
	defaultLimit          = 50
	maxLimit              = 200
)

// libParams validates pagination. The provider key is passed upstream
// unvalidated: the player's error is the authority (spec §5.1).
func libParams(r *http.Request) (map[string]any, bool) {
	q := r.URL.Query()
	parseInt := func(name string, def int) (int, bool) {
		s := q.Get(name)
		if s == "" {
			return def, true
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	offset, ok := parseInt("offset", 0)
	if !ok || offset < 0 {
		return nil, false
	}
	limit, ok := parseInt("limit", defaultLimit)
	if !ok || limit < 1 {
		return nil, false
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return map[string]any{"provider": r.PathValue("provider"), "offset": offset, "limit": limit}, true
}

// libraryGET runs one provider op for a GET, with cache + error mapping.
func (s *serverState) libraryGET(w http.ResponseWriter, r *http.Request, cache *ttlcache, op string, params map[string]any) {
	key := r.URL.Path + "?" + r.URL.RawQuery
	if body, ok := cache.get(key); ok {
		writeJSON(w, http.StatusOK, body)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.libraryTimeout())
	defer cancel()
	job, err := s.cfg.Runner.SubmitJob(ctx, op, params)
	if err != nil {
		s.libraryError(w, err)
		return
	}
	if len(job.Result) == 0 {
		job.Result = json.RawMessage(`{"ok":true}`)
	}
	cache.put(key, job.Result)
	writeJSON(w, http.StatusOK, job.Result)
}

// libraryError maps provider failures: upstream errors keep the player's
// detail string so the UI can name the unreachable provider; an over-long
// job wait is a 504, not a hung request.
func (s *serverState) libraryError(w http.ResponseWriter, err error) {
	var ie *ipc.IPCError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "job_timeout", "code": "job_timeout"})
	case errors.As(err, &ie):
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": ie.Error(), "code": "upstream_error", "detail": ie.Detail})
	case errors.Is(err, ipc.ErrNotConnected):
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "player offline", "code": "upstream_error", "detail": "player offline"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "code": "internal_error"})
	}
}

func (s *serverState) libraryProviders(w http.ResponseWriter, r *http.Request) {
	s.libraryGET(w, r, s.cfg.providersCache, "provider.list", map[string]any{})
}

func (s *serverState) libraryList(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	s.libraryGET(w, r, s.cfg.listCache, "provider."+r.PathValue("kind"), params)
}

func (s *serverState) libraryAlbumTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["album"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.album_tracks", params)
}

func (s *serverState) libraryArtistTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["id"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.tracks", params)
}

func (s *serverState) librarySearch(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["query"] = q
	// search is never cached: the noCache instance has TTL 0 and always misses.
	s.libraryGET(w, r, s.cfg.noCache, "provider.search", params)
}
