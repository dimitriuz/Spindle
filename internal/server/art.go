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

// InfoSource reports a track's technical details; *art.Resolver is the real one.
type InfoSource interface {
	Info(ctx context.Context, trackPath string, durationSecs float64) (art.Info, error)
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
	writeImage(w, r, img)
}

// trackInfo serves GET /api/trackinfo?path=&duration=: codec, bitrate and so
// on for the fullscreen player. Failures are one quiet 404, like art.
func (s *serverState) trackInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	if s.cfg.Info == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_info"})
		return
	}
	dur, err := strconv.ParseFloat(r.URL.Query().Get("duration"), 64)
	if err != nil || dur < 0 {
		dur = 0
	}
	info, err := s.cfg.Info.Info(r.Context(), path, dur)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_info"})
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, info)
}

// writeImage sends a picture with the caching headers every art route shares.
func writeImage(w http.ResponseWriter, r *http.Request, img *art.Image) {
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

// ItemArtSource finds the cover of a Navidrome album, artist or playlist by id.
type ItemArtSource interface {
	ItemArt(ctx context.Context, kind, id string) (*art.Image, error)
}

// itemArt serves GET /api/art/item?provider=navidrome&kind=album|artist|playlist&id=.
// The id is checked here before anything is asked of the server; any failure
// to find a picture is the same quiet 404.
func (s *serverState) itemArt(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, id := q.Get("kind"), q.Get("id")
	if q.Get("provider") != "navidrome" || (kind != "album" && kind != "artist" && kind != "playlist") || !art.ValidItemID(id) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	if s.cfg.ItemArt == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	img, err := s.cfg.ItemArt.ItemArt(r.Context(), kind, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	writeImage(w, r, img)
}

// StationArtSource finds the logo of an internet radio station by name.
type StationArtSource interface {
	StationArt(ctx context.Context, name string) (*art.Image, error)
}

// stationArt serves GET /api/art/station?name=<catalog name>: the logo shown on
// radio tiles. The name only ever goes to the public radio directory.
func (s *serverState) stationArt(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" || len(name) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	if s.cfg.StationArt == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	img, err := s.cfg.StationArt.StationArt(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	writeImage(w, r, img)
}
