package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cliamp-web/internal/ipc"
	"cliamp-web/internal/snapshot"
)

type Config struct {
	Cache   *snapshot.Cache
	Runner  ipc.OpRunner
	Hub     *Hub
	Guard   *HostGuard
	Limiter *Limiter
	// Art finds cover/disc/artist pictures for /api/art; nil means none.
	Art ArtSource
	// Info reports codec/bitrate for /api/trackinfo; nil means none.
	Info InfoSource
	// Favorites is the favorites list for /api/favorites; nil means none.
	Favorites FavoritesSource
	// Enqueue runs Play / Add to queue in the background; nil means none.
	Enqueue EnqueueSource
	// Files is the music folder for /api/files; nil means off.
	Files FilesSource
	// ItemArt finds covers of Navidrome albums/artists/playlists by id; nil means none.
	ItemArt ItemArtSource
	// StationArt finds radio station logos by name for /api/art/station; nil means none.
	StationArt StationArtSource
	// StationMeta describes radio catalog stations for /api/radio/meta; nil means none.
	StationMeta StationMetaSource
	// FolderArt makes thumbnails of music folder covers for /api/files/art; nil means none.
	FolderArt FolderArtSource
	// UI is the embedded web interface (webui: index.html, app.css, app.js),
	// served at the root; nil disables it.
	UI     fs.FS
	Logger *slog.Logger
	// LibraryTTL bounds library listing cache freshness (0 ⇒ 60 s).
	LibraryTTL time.Duration
	// LibraryTimeout caps one provider job wait (0 ⇒ 30 s).
	LibraryTimeout time.Duration

	// HeartbeatInterval is the SSE keepalive cadence (0 ⇒ 15 s).
	HeartbeatInterval time.Duration

	listCache      *ttlcache
	providersCache *ttlcache
	noCache        *ttlcache
}

func (c *Config) libraryTimeout() time.Duration {
	if c.LibraryTimeout == 0 {
		return defaultLibraryTimeout
	}
	return c.LibraryTimeout
}

func (c *Config) heartbeat() time.Duration {
	if c.HeartbeatInterval == 0 {
		return 15 * time.Second
	}
	return c.HeartbeatInterval
}

type serverState struct{ cfg Config }

// New assembles the HTTP handler. The request guard (protect) wraps every
// data endpoint; /api/health is open by design.
func New(cfg Config) http.Handler {
	if cfg.LibraryTTL == 0 {
		cfg.LibraryTTL = defaultLibraryTTL
	}
	cfg.listCache = newTTLCache(cfg.LibraryTTL, 128, time.Now)
	cfg.providersCache = newTTLCache(providersTTL, 4, time.Now)
	cfg.noCache = newTTLCache(0, 1, time.Now)
	s := &serverState{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.Handle("GET /api/state", s.protect(http.HandlerFunc(s.state)))
	mux.Handle("GET /api/queue", s.protect(http.HandlerFunc(s.queue)))
	mux.Handle("GET /api/capabilities", s.protect(http.HandlerFunc(s.capabilities)))
	mux.Handle("GET /api/events", s.protect(http.HandlerFunc(s.events)))
	mux.Handle("POST /api/command", s.protect(http.HandlerFunc(s.command)))
	mux.Handle("GET /api/library/providers", s.protect(http.HandlerFunc(s.libraryProviders)))
	mux.Handle("GET /api/library/{provider}/{kind}", s.protect(http.HandlerFunc(s.libraryList)))
	mux.Handle("GET /api/library/{provider}/album/{id}/tracks", s.protect(http.HandlerFunc(s.libraryAlbumTracks)))
	mux.Handle("GET /api/library/{provider}/artist/{id}/tracks", s.protect(http.HandlerFunc(s.libraryArtistTracks)))
	mux.Handle("GET /api/library/{provider}/artist/{id}/albums", s.protect(http.HandlerFunc(s.libraryArtistAlbums)))
	mux.Handle("GET /api/library/{provider}/playlist/{id}/tracks", s.protect(http.HandlerFunc(s.libraryPlaylistTracks)))
	mux.Handle("GET /api/radio/search", s.protect(http.HandlerFunc(s.radioSearch)))
	mux.Handle("POST /api/radio/meta", s.protect(http.HandlerFunc(s.radioMeta)))
	mux.Handle("GET /api/art/station", s.protect(http.HandlerFunc(s.stationArt)))
	mux.Handle("GET /api/art/item", s.protect(http.HandlerFunc(s.itemArt)))
	mux.Handle("GET /api/art/{kind}", s.protect(http.HandlerFunc(s.art)))
	mux.Handle("GET /api/trackinfo", s.protect(http.HandlerFunc(s.trackInfo)))
	mux.Handle("GET /api/favorites", s.protect(http.HandlerFunc(s.favoritesList)))
	mux.Handle("PUT /api/favorites", s.protect(http.HandlerFunc(s.favoritesSet)))
	mux.Handle("POST /api/enqueue", s.protect(http.HandlerFunc(s.enqueueStart)))
	mux.Handle("POST /api/enqueue/cancel", s.protect(http.HandlerFunc(s.enqueueCancel)))
	mux.Handle("GET /api/files/root", s.protect(http.HandlerFunc(s.filesRoot)))
	mux.Handle("GET /api/files/art", s.protect(http.HandlerFunc(s.folderArt)))
	mux.Handle("GET /api/files", s.protect(http.HandlerFunc(s.filesList)))
	mux.Handle("GET /api/lyrics", s.protect(http.HandlerFunc(s.libraryLyrics)))
	mux.Handle("GET /api/search/{provider}", s.protect(http.HandlerFunc(s.librarySearch)))
	if cfg.UI != nil {
		mux.Handle("GET /next/", http.RedirectHandler("/", http.StatusMovedPermanently)) // old bookmark
	}
	mux.Handle("/", s.uiHandler())
	return mux
}

// uiHandler serves the embedded interface: assets carry no player state. Unknown paths fall back to the page
// so hash routes survive reloads. The page is served as "/" because
// serveFile redirects any request path ending in /index.html to "./".
func (s *serverState) uiHandler() http.Handler {
	if s.cfg.UI == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r) // never answer a mistyped API call with the HTML page
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if f, err := s.cfg.UI.Open(name); err == nil {
				f.Close()
				http.ServeFileFS(w, r, s.cfg.UI, name)
				return
			}
		}
		r.URL.Path = "/"
		http.ServeFileFS(w, r, s.cfg.UI, "index.html")
	})
}
