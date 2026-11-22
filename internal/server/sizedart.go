package server

import (
	"net/http"
	"strconv"
	"sync"

	"spindle/internal/art"
)

const (
	minArtSize    = 32
	maxArtSize    = 512
	sizedCacheMax = 64
)

// parseSize reads ?size=N. Absent is (0, false, true); anything that is not a
// whole number is not ok; numbers are clamped to 32..512.
func parseSize(r *http.Request) (n int, set, ok bool) {
	v := r.URL.Query().Get("size")
	if v == "" {
		return 0, false, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false, false
	}
	return min(max(n, minArtSize), maxArtSize), true, true
}

// checkSize refuses a malformed ?size= before the picture is looked up.
func checkSize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := parseSize(r); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sizedCache remembers the last few resized pictures so a device that asks
// for the same cover again does not make the bridge decode it again. The zero
// value is ready to use.
type sizedCache struct {
	mu    sync.Mutex
	m     map[string]*art.Image
	order []string
}

func (c *sizedCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

func (c *sizedCache) get(k string) (*art.Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	img, ok := c.m[k]
	return img, ok
}

func (c *sizedCache) put(k string, img *art.Image) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*art.Image{}
	}
	if _, dup := c.m[k]; !dup {
		c.order = append(c.order, k)
		for len(c.order) > sizedCacheMax {
			delete(c.m, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.m[k] = img
}

// sizedImage shrinks img to at most n pixels on its long side, as JPEG.
func (s *serverState) sizedImage(img *art.Image, n int) (*art.Image, error) {
	key := img.Key + "@" + strconv.Itoa(n)
	if out, ok := s.sizes.get(key); ok {
		return out, nil
	}
	data, err := art.Thumb(img.Data, n)
	if err != nil {
		return nil, err
	}
	out := &art.Image{Data: data, ContentType: "image/jpeg", Key: key}
	s.sizes.put(key, out)
	return out, nil
}

// sendImage answers with img, resized when the request carries ?size=. A
// picture that cannot be decoded is the same quiet 404 as a missing one.
func (s *serverState) sendImage(w http.ResponseWriter, r *http.Request, img *art.Image) {
	if n, set, _ := parseSize(r); set {
		small, err := s.sizedImage(img, n)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_art"})
			return
		}
		img = small
	}
	writeImage(w, r, img)
}
