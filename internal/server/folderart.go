package server

import (
	"context"
	"errors"
	"net/http"

	"spindle/internal/art"
	"spindle/internal/files"
)

// FolderArtSource makes a thumbnail of a music folder's cover; *art.Resolver is the real one.
type FolderArtSource interface {
	FolderArt(ctx context.Context, rel string) (*art.Image, error)
}

// folderArt serves GET /api/files/art?path=<relative folder>.
func (s *serverState) folderArt(w http.ResponseWriter, r *http.Request) {
	if s.cfg.FolderArt == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	img, err := s.cfg.FolderArt.FolderArt(r.Context(), r.URL.Query().Get("path"))
	switch {
	case err == nil:
		s.sendImage(w, r, img)
	case errors.Is(err, files.ErrOutside):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
	}
}
