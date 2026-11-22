package server

import (
	"errors"
	"net/http"

	"cliamp-web/internal/files"
)

// FilesSource is the music folder; *files.Browser is the real one.
type FilesSource interface {
	RootName() string
	List(rel string) (files.Listing, error)
}

func (s *serverState) filesOff(w http.ResponseWriter) bool {
	if s.cfg.Files == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_files"})
		return true
	}
	return false
}

// filesRoot serves GET /api/files/root: the page asks whether to show the Files tile.
func (s *serverState) filesRoot(w http.ResponseWriter, r *http.Request) {
	if s.filesOff(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": s.cfg.Files.RootName()})
}

// filesList serves GET /api/files?path=<relative path>. Paths are relative to
// the music root and never absolute in either direction.
func (s *serverState) filesList(w http.ResponseWriter, r *http.Request) {
	if s.filesOff(w) {
		return
	}
	l, err := s.cfg.Files.List(r.URL.Query().Get("path"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, l)
	case errors.Is(err, files.ErrOutside):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
	case errors.Is(err, files.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error", "code": "internal_error"})
	}
}
