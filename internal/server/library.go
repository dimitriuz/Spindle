package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spindle/internal/ipc"
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

// wantCompact reports whether the caller asked for the small list shape.
func wantCompact(r *http.Request) bool { return r.URL.Query().Get("compact") == "1" }

// libraryGET runs one provider op for a GET. hint names the kind of the rows
// ("album", "track", ...) for ?compact=1; "" means the result is never compacted.
func (s *serverState) libraryGET(w http.ResponseWriter, r *http.Request, cache *ttlcache, op, hint string, params map[string]any) {
	key := r.URL.Path + "?" + r.URL.RawQuery
	respond := func(body json.RawMessage) {
		if hint != "" && wantCompact(r) {
			offset, _ := params["offset"].(int)
			limit, _ := params["limit"].(int)
			c, err := compactList(hint, body, offset, limit)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error", "code": "internal_error"})
				return
			}
			body = c
		}
		writeJSON(w, http.StatusOK, body)
	}
	if body, ok := cache.get(key); ok {
		respond(body)
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
	respond(job.Result)
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
	s.libraryGET(w, r, s.cfg.providersCache, "provider.list", "", map[string]any{})
}

func (s *serverState) libraryList(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	// kind→op whitelist: only listing ops exist under this route; anything
	// else (provider.load…) never reaches the player over a GET.
	if sort := r.URL.Query().Get("sort"); sort != "" {
		// the provider's own album orders (alphabeticalByName, newest, ...): a plain word, passed on as is
		if r.PathValue("kind") != "albums" || len(sort) > 40 || strings.Trim(sort, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
			return
		}
		params["sort"] = sort
	}
	switch r.PathValue("kind") {
	case "playlists", "albums", "artists", "catalog":
		hint := map[string]string{"playlists": "playlist", "albums": "album", "artists": "artist", "catalog": "station"}[r.PathValue("kind")]
		s.libraryGET(w, r, s.cfg.listCache, "provider."+r.PathValue("kind"), hint, params)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
	}
}

func (s *serverState) libraryAlbumTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["album"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.album_tracks", "track", params)
}

func (s *serverState) libraryArtistTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["id"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.tracks", "track", params)
}

// libraryArtistAlbums lists an artist's albums. cliamp v2.4.0 takes the
// artist id as params.artist (params.id fails "Artist not found").
func (s *serverState) libraryArtistAlbums(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["artist"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.artist_albums", "album", params)
}

func (s *serverState) libraryPlaylistTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	// playlist-only by construction: this route cannot emit an id param.
	params["playlist"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.tracks", "track", params)
}

func (s *serverState) libraryLyrics(w http.ResponseWriter, r *http.Request) {
	// never cached: lyrics follow whatever track is loaded right now
	s.libraryGET(w, r, s.cfg.noCache, "lyrics", "", map[string]any{})
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
	s.libraryGET(w, r, s.cfg.noCache, "provider.search", "track", params)
}
