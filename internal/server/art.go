package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"spindle/internal/art"
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
	s.sendImage(w, r, img)
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
	s.sendImage(w, r, img)
}

// StationArtSource finds the logo of an internet radio station by name.
type StationArtSource interface {
	StationArt(ctx context.Context, name string) (*art.Image, error)
	StationArtByID(ctx context.Context, id string) (*art.Image, error)
}

// stationArt serves GET /api/art/station?name=<catalog name>: the logo shown on
// radio tiles. The name only ever goes to the public radio directory.
func (s *serverState) stationArt(w http.ResponseWriter, r *http.Request) {
	name, id := r.URL.Query().Get("name"), r.URL.Query().Get("id")
	if (name == "") == (id == "") || len(name) > 200 || len(id) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	if s.cfg.StationArt == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	var img *art.Image
	var err error
	if id != "" {
		img, err = s.cfg.StationArt.StationArtByID(r.Context(), id)
	} else {
		img, err = s.cfg.StationArt.StationArt(r.Context(), name)
	}
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
		return
	}
	s.sendImage(w, r, img)
}

// StationMetaSource describes catalog stations (codec, bitrate, tags, country, votes).
type StationMetaSource interface {
	StationMeta(ctx context.Context, names []string) []art.StationMeta
	StationSearch(ctx context.Context, q string) []art.StationHit
}

// radioSearch serves GET /api/radio/search?q=: stations of the public radio
// directory matching a name, tag or country, most voted first.
func (s *serverState) radioSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	if s.cfg.StationMeta == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_meta"})
		return
	}
	hits := s.cfg.StationMeta.StationSearch(r.Context(), q)
	if hits == nil {
		hits = []art.StationHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stations": hits})
}

// radioMeta serves POST /api/radio/meta {names:[...]} for the radio page's
// tile details, sorting and filters. At most 60 names of at most 200 bytes.
func (s *serverState) radioMeta(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || len(body.Names) == 0 || len(body.Names) > 60 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	for _, n := range body.Names {
		if len(n) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
			return
		}
	}
	if s.cfg.StationMeta == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_meta"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stations": s.cfg.StationMeta.StationMeta(r.Context(), body.Names)})
}
