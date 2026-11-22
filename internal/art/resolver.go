package art

import (
	"context"
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
