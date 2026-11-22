package art

import (
	"context"
	"errors"
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
	}, nil
}

// Get returns the picture of the given kind for a track, or ErrNoArt.
// Transient failures (Navidrome unreachable) come back as other errors and
// are not remembered.
func (r *Resolver) Get(ctx context.Context, kind Kind, trackPath string) (*Image, error) {
	img, err := r.get(ctx, kind, trackPath)
	if kind == Disc && errors.Is(err, ErrNoArt) {
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
	return r.local.Info(trackPath, durationSecs)
}
