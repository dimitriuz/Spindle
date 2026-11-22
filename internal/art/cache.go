// Package art resolves cover, disc and artist images for the web page.
package art

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind is which picture is wanted.
type Kind string

const (
	Cover  Kind = "cover"
	Disc   Kind = "disc"
	Artist Kind = "artist"
)

// ValidKind reports whether s names a Kind.
func ValidKind(s string) bool {
	switch Kind(s) {
	case Cover, Disc, Artist:
		return true
	}
	return false
}

// ErrNoArt means the source definitely has no such picture. Any other error
// is transient (server down, timeout) and is never remembered.
var ErrNoArt = errors.New("art: none")

const (
	// MaxImageBytes caps a single image, fetched or read.
	MaxImageBytes = 10 << 20
	noArtTTL      = 10 * time.Minute
)

// Image is one picture ready to serve. Key is safe to use as an ETag: a hash,
// never derived text.
type Image struct {
	Data        []byte
	ContentType string
	Key         string
}

type flight struct {
	done chan struct{}
	img  *Image
	err  error
}

// Cache keeps images on disk, shares concurrent fetches of one key, and
// remembers "no art" answers for a while.
type Cache struct {
	dir string
	max int64
	now func() time.Time

	mu      sync.Mutex
	flights map[string]*flight
	misses  map[string]time.Time
	total   int64
}

func NewCache(dir string, maxBytes int64, now func() time.Time) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &Cache{dir: dir, max: maxBytes, now: now, flights: map[string]*flight{}, misses: map[string]time.Time{}}
	for _, f := range c.files() {
		c.total += f.size
	}
	return c, nil
}

func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// Stats reports how many bytes the cache holds and the most it keeps.
func (c *Cache) Stats() (used, max int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total, c.max
}

func (c *Cache) path(h string) string { return filepath.Join(c.dir, h[:2], h) }

// Do returns the image stored under key, else runs fetch once for all
// concurrent callers. fetch returns raw bytes or ErrNoArt.
func (c *Cache) Do(key string, fetch func() ([]byte, error)) (*Image, error) {
	h := hashKey(key)
	if img := c.read(h); img != nil {
		return img, nil
	}
	c.mu.Lock()
	if until, ok := c.misses[h]; ok {
		if c.now().Before(until) {
			c.mu.Unlock()
			return nil, ErrNoArt
		}
		delete(c.misses, h)
	}
	if f, ok := c.flights[h]; ok {
		c.mu.Unlock()
		<-f.done
		return f.img, f.err
	}
	f := &flight{done: make(chan struct{})}
	c.flights[h] = f
	c.mu.Unlock()

	f.img, f.err = c.fill(h, fetch)

	c.mu.Lock()
	delete(c.flights, h)
	if errors.Is(f.err, ErrNoArt) {
		c.misses[h] = c.now().Add(noArtTTL)
	}
	c.mu.Unlock()
	close(f.done)
	return f.img, f.err
}

func (c *Cache) fill(h string, fetch func() ([]byte, error)) (*Image, error) {
	data, err := fetch()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxImageBytes {
		return nil, ErrNoArt
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return nil, ErrNoArt // an HTML login page or a JSON error is not a picture
	}
	p := c.path(h)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
		tmp := p + ".tmp"
		if os.WriteFile(tmp, data, 0o600) == nil && os.Rename(tmp, p) == nil {
			_ = os.Chtimes(p, c.now(), c.now())
			c.mu.Lock()
			c.total += int64(len(data))
			over := c.total > c.max
			c.mu.Unlock()
			if over {
				c.evict(p)
			}
		}
	}
	return &Image{Data: data, ContentType: ct, Key: h[:32]}, nil
}

func (c *Cache) read(h string) *Image {
	p := c.path(h)
	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return nil
	}
	_ = os.Chtimes(p, c.now(), c.now()) // recently used survives eviction longer
	return &Image{Data: data, ContentType: http.DetectContentType(data), Key: h[:32]}
}

type cached struct {
	path string
	size int64
	mod  time.Time
}

func (c *Cache) files() []cached {
	var out []cached
	_ = filepath.Walk(c.dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() && !strings.HasSuffix(p, ".tmp") {
			out = append(out, cached{p, fi.Size(), fi.ModTime()})
		}
		return nil
	})
	return out
}

// evict removes the least recently used files until the cache fits, never
// the file just written.
func (c *Cache) evict(keep string) {
	fs := c.files()
	sort.Slice(fs, func(i, j int) bool { return fs[i].mod.Before(fs[j].mod) })
	var total int64
	for _, f := range fs {
		total += f.size
	}
	for _, f := range fs {
		if total <= c.max {
			break
		}
		if f.path == keep {
			continue
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	c.mu.Lock()
	c.total = total
	c.mu.Unlock()
}
