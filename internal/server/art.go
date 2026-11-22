package server

import (
	"context"
	"net/http"
	"strconv"

	"cliamp-web/internal/art"
)

// ArtSource finds a picture for a track; *art.Resolver is the real one.
type ArtSource interface {
	Get(ctx context.Context, kind art.Kind, trackPath string) (*art.Image, error)
}

// RestoreURL gives art lookups the original stream URL for a scrubbed one.
func RestoreURL(scrubbed string) string { return urls.Restore(scrubbed) }

// art serves GET /api/art/{kind}?path=<track path as the browser has it>.
// Every failure to find a picture is the same quiet 404: no reason text, no
// echo of the path, since a stream URL can carry a login.
func (s *serverState) art(w http.ResponseWriter, r *http.Request) {
	kind, path := r.PathValue("kind"), r.URL.Query().Get("path")
	if !art.ValidKind(kind) || path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	if s.cfg.Art == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	img, err := s.cfg.Art.Get(r.Context(), art.Kind(kind), path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	etag := `"` + img.Key + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", img.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(img.Data)))
	_, _ = w.Write(img.Data)
}
