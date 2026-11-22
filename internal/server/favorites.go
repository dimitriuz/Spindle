package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"spindle/internal/favorites"
)

// FavoritesSource is the favorites list and its writer; *favorites.Service is the real one.
type FavoritesSource interface {
	List(ctx context.Context) ([]favorites.Item, error)
	Set(ctx context.Context, q favorites.SetRequest) (bool, error)
}

// favoritesList serves GET /api/favorites. A list missing cliamp's part (the
// player is down) is still served, flagged partial.
func (s *serverState) favoritesList(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Favorites == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_favorites"})
		return
	}
	items, err := s.cfg.Favorites.List(r.Context())
	out := map[string]any{"items": items}
	if items == nil {
		out["items"] = []favorites.Item{}
	}
	switch {
	case err == nil:
	case errors.Is(err, favorites.ErrPartial):
		out["partial"] = true
	default:
		s.libraryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// favoritesSet serves PUT /api/favorites: set one item's state idempotently.
// The bridge makes the cliamp calls itself; the browser cannot.
func (s *serverState) favoritesSet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Favorites == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_favorites"})
		return
	}
	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(clientKey(r.RemoteAddr)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate_limited", "code": "rate_limited"})
		return
	}
	var req struct {
		Kind     favorites.Kind `json:"kind"`
		Provider string         `json:"provider"`
		ID       string         `json:"id"`
		Favorite *bool          `json:"favorite"`
		Title    string         `json:"title"`
		Subtitle string         `json:"subtitle"`
		Track    map[string]any `json:"track"`
	}
	bad := func() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil || req.Favorite == nil {
		bad()
		return
	}
	switch req.Kind {
	case favorites.Track:
		if req.Track == nil {
			bad()
			return
		}
		// the page only ever holds scrubbed stream URLs; cliamp needs the real one
		if p, ok := req.Track["path"].(string); ok {
			req.Track["path"] = urls.Restore(p)
		}
	case favorites.Album, favorites.Artist, favorites.Playlist, favorites.Radio:
		if req.ID == "" || (req.Kind != favorites.Radio && req.Provider == "") {
			bad()
			return
		}
	default:
		bad()
		return
	}
	on, err := s.cfg.Favorites.Set(r.Context(), favorites.SetRequest{
		Kind: req.Kind, Provider: req.Provider, ID: req.ID, Title: req.Title, Subtitle: req.Subtitle,
		Favorite: *req.Favorite, Track: req.Track,
	})
	if err != nil {
		s.libraryError(w, err)
		return
	}
	s.cfg.Hub.Broadcast("favorites", map[string]any{})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "favorite": on})
}
