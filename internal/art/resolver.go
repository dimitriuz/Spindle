package art

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Options configures a Resolver.
type Options struct {
	Dir      string // cache directory
	MaxBytes int64  // cache cap
	// Known says whether a local path is a track the bridge has been shown.
	Known func(path string) bool
	// Restore maps a path as the browser sees it back to the real one (the
	// stream URLs reach the page with their credentials removed).
	Restore func(string) string
}

// Resolver turns a track path into a picture, from Navidrome or the local disk.
type Resolver struct {
	cache   *Cache
	nd      *Navidrome
	local   *Local
	restore func(string) string
	known   func(string) bool
	folders CoverFinder
	st      *Stations
	ndCfg   *NavidromeConfig // the user's server login from cliamp's config, for covers by id
}

func NewResolver(o Options) (*Resolver, error) {
	c, err := NewCache(o.Dir, o.MaxBytes, time.Now)
	if err != nil {
		return nil, err
	}
	restore := o.Restore
	if restore == nil {
		restore = func(s string) string { return s }
	}
	return &Resolver{
		cache:   c,
		nd:      &Navidrome{HTTP: newClient()},
		local:   &Local{Known: o.Known},
		restore: restore,
		known:   o.Known,
		st:      &Stations{},
	}, nil
}

// Get returns the picture of the given kind for a track, or ErrNoArt.
// Transient failures (Navidrome unreachable) come back as other errors and
// are not remembered.
func (r *Resolver) Get(ctx context.Context, kind Kind, trackPath string) (*Image, error) {
	img, err := r.get(ctx, kind, trackPath)
	if kind == Disc && errors.Is(err, ErrNoArt) && !isStation(r.restore(trackPath)) {
		return r.synthDisc(ctx, trackPath) // no disc image anywhere: draw one from the cover
	}
	return img, err
}

// synthDisc makes (and caches) a disc from the album cover. No cover, no disc.
func (r *Resolver) synthDisc(ctx context.Context, trackPath string) (*Image, error) {
	cover, err := r.get(ctx, Cover, trackPath)
	if err != nil {
		return nil, err
	}
	// v1 in the key: bump it when the artwork of SynthDisc changes
	return r.cache.Do("synth:v1:"+cover.Key, func() ([]byte, error) {
		out, err := SynthDisc(cover.Data)
		if err != nil {
			return nil, ErrNoArt // a cover we cannot decode (webp, ...) gives no disc
		}
		return out, nil
	})
}

func (r *Resolver) get(ctx context.Context, kind Kind, trackPath string) (*Image, error) {
	// the fetch is shared between callers: one browser giving up must not
	// fail the others, so it runs on its own clock
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	// Only paths the bridge itself handed to the browser are looked up: a
	// scrubbed URL it remembers (restore changes it) or a track it knows.
	// Otherwise a client could point the bridge at any host.
	orig := r.restore(trackPath)
	if orig == trackPath && (r.known == nil || !r.known(trackPath)) {
		return nil, ErrNoArt
	}
	if _, _, _, ok := parseStream(orig); ok {
		key, fetch, err := r.nd.Resolve(ctx, kind, orig)
		if err != nil {
			return nil, err
		}
		return r.cache.Do(key, fetch)
	}
	if isStation(orig) {
		if kind != Cover {
			return nil, ErrNoArt // a radio station has a logo, no disc or artist photo
		}
		return r.cache.Do("station:u:"+hashKey(orig), func() ([]byte, error) {
			logo, err := r.st.Logo(ctx, orig, "")
			if err != nil {
				return nil, err
			}
			return r.st.Image(ctx, logo)
		})
	}
	key, fetch, err := r.local.Resolve(kind, trackPath)
	if err != nil {
		return nil, err
	}
	return r.cache.Do(key, fetch)
}

// Info returns technical details for a track, gated exactly like Get.
func (r *Resolver) Info(ctx context.Context, trackPath string, durationSecs float64) (Info, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	orig := r.restore(trackPath)
	if orig == trackPath && (r.known == nil || !r.known(trackPath)) {
		return Info{}, ErrNoInfo
	}
	if _, _, _, ok := parseStream(orig); ok {
		return r.nd.Info(ctx, orig)
	}
	if isStation(orig) {
		return r.st.Info(ctx, orig)
	}
	return r.local.Info(trackPath, durationSecs)
}

// isStation tells an internet radio stream (any http(s) URL that is not a
// Navidrome stream) from a local file path.
func isStation(p string) bool {
	if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
		return false
	}
	_, _, _, nd := parseStream(p)
	return !nd
}

// StationArt returns the logo of a station by its catalog name, for lists.
func (r *Resolver) StationArt(ctx context.Context, name string) (*Image, error) {
	clean := CleanStationName(name)
	if clean == "" || len(clean) > 120 {
		return nil, ErrNoArt
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return r.cache.Do("station:n:"+hashKey(strings.ToLower(clean)), func() ([]byte, error) {
		rec, ok, err := r.st.ByName(ctx, clean)
		if err != nil {
			return nil, err
		}
		if !ok || rec.Favicon == "" {
			return nil, ErrNoArt
		}
		return r.st.Image(ctx, rec.Favicon)
	})
}

// SetNavidrome gives the resolver the login for covers by album, artist and
// playlist id (the track-based routes get theirs from the stream URLs).
func (r *Resolver) SetNavidrome(c NavidromeConfig) { r.ndCfg = &c }

var itemPrefix = map[string]string{"album": "al-", "artist": "ar-", "playlist": "pl-"}

// ItemArt returns the 300 px cover of a Navidrome album, artist or playlist.
// ErrNoArt when none exists, nothing is configured, or the id is not an id.
func (r *Resolver) ItemArt(ctx context.Context, kind, id string) (*Image, error) {
	prefix, ok := itemPrefix[kind]
	if !ok || !ValidItemID(id) || r.ndCfg == nil {
		return nil, ErrNoArt
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	base := r.ndCfg.URL + "/rest"
	srv := sha256.Sum256([]byte(base))
	key := "nd:item:" + hex.EncodeToString(srv[:4]) + ":" + kind + ":" + id
	return r.cache.Do(key, func() ([]byte, error) {
		target := base + "/getCoverArt?" + r.ndCfg.query(url.Values{"id": {prefix + id}, "size": {"300"}}).Encode()
		return r.nd.download(ctx, target)
	})
}

// StationMeta describes catalog stations by name for the radio page.
func (r *Resolver) StationMeta(ctx context.Context, names []string) []StationMeta {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	return r.st.Meta(ctx, names)
}

// StationSearch finds stations in the public directory by name, tag or country.
func (r *Resolver) StationSearch(ctx context.Context, q string) []StationHit {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	return r.st.Search(ctx, q)
}

// StationArtByID returns the logo of a station an earlier search returned.
func (r *Resolver) StationArtByID(ctx context.Context, id string) (*Image, error) {
	logo, ok := r.st.FaviconOf(id)
	if !ok {
		return nil, ErrNoArt
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return r.cache.Do("station:id:"+hashKey(id), func() ([]byte, error) { return r.st.Image(ctx, logo) })
}

// CacheStats is the picture cache's size and cap in bytes.
func (r *Resolver) CacheStats() (used, max int64) { return r.cache.Stats() }
