package server

import (
	"net/http"
	"runtime"
	"runtime/debug"
	"time"
)

// AboutInfo is what main knows about this bridge and the server hides nothing
// of: no paths, no logins.
type AboutInfo struct {
	Listen        string `json:"listen"`          // host:port the bridge serves on
	LAN           bool   `json:"lan"`             // reachable from other machines
	Navidrome     bool   `json:"navidrome"`       // cliamp's Navidrome login was found, so list covers work
	MusicRoot     string `json:"music_root"`      // name of the music folder, if the file browser is on
	ArtCacheBytes int64  `json:"art_cache_bytes"` // pictures kept on disk
	ArtCacheMax   int64  `json:"art_cache_max"`
}

// AboutSource reports AboutInfo when asked; nil means nothing to add.
type AboutSource func() AboutInfo

// about serves GET /api/about: the bridge's version and state for the Settings page.
func (s *serverState) about(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"name":   "cliamp-web",
		"go":     runtime.Version(),
		"os":     runtime.GOOS + "/" + runtime.GOARCH,
		"uptime": int64(time.Since(s.started).Seconds()),
		"player": map[string]any{"online": s.cfg.Cache.Online()},
		"features": map[string]bool{
			"art": s.cfg.Art != nil, "files": s.cfg.Files != nil, "favorites": s.cfg.Favorites != nil,
			"radio_directory": s.cfg.StationMeta != nil,
		},
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				if len(kv.Value) > 7 {
					kv.Value = kv.Value[:7]
				}
				out["commit"] = kv.Value
			case "vcs.time":
				out["built"] = kv.Value
			case "vcs.modified":
				out["modified"] = kv.Value == "true"
			}
		}
	}
	if s.cfg.Hub != nil {
		out["clients"] = s.cfg.Hub.Count()
	}
	if s.cfg.About != nil {
		out["bridge"] = s.cfg.About()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
