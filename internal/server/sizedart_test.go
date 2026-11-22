package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"strconv"
	"testing"

	"spindle/internal/art"
)

func TestParseSize(t *testing.T) {
	for _, c := range []struct {
		q       string
		n       int
		set, ok bool
	}{
		{"", 0, false, true},
		{"abc", 0, false, false},
		{"1.5", 0, false, false},
		{"0", 32, true, true},
		{"-5", 32, true, true},
		{"99999", 512, true, true},
		{"128", 128, true, true},
	} {
		r := httptest.NewRequest("GET", "/api/art/cover?path=x&size="+c.q, nil)
		if c.q == "" {
			r = httptest.NewRequest("GET", "/api/art/cover?path=x", nil)
		}
		n, set, ok := parseSize(r)
		if n != c.n || set != c.set || ok != c.ok {
			t.Errorf("size=%q: got (%d,%v,%v) want (%d,%v,%v)", c.q, n, set, ok, c.n, c.set, c.ok)
		}
	}
}

func bigPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestArtSized(t *testing.T) {
	fa := &fakeArt{img: &art.Image{Data: bigPNG(t, 400, 200), ContentType: "image/png", Key: "abc"}}
	h := artServer(t, fa)
	plain := artGet(h, "/api/art/cover?path=/m/a.flac", nil)
	rec := artGet(h, "/api/art/cover?path=/m/a.flac&size=64", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("got %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || cfg.Width != 64 || cfg.Height != 32 {
		t.Fatalf("want 64x32 JPEG, got %+v %v", cfg, err)
	}
	if rec.Header().Get("ETag") == plain.Header().Get("ETag") || rec.Header().Get("ETag") == "" {
		t.Fatalf("sized image needs its own ETag: %q vs %q", rec.Header().Get("ETag"), plain.Header().Get("ETag"))
	}
	if again := artGet(h, "/api/art/cover?path=/m/a.flac&size=64", map[string]string{"If-None-Match": rec.Header().Get("ETag")}); again.Code != 304 {
		t.Fatalf("want 304, got %d", again.Code)
	}
	// without size the image is untouched
	if plain.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("unsized image changed: %s", plain.Header().Get("Content-Type"))
	}
}

func TestArtSizeInvalid(t *testing.T) {
	h := artServer(t, &fakeArt{img: &art.Image{Data: bigPNG(t, 40, 40), ContentType: "image/png", Key: "abc"}})
	if rec := artGet(h, "/api/art/cover?path=/m/a.flac&size=abc", nil); rec.Code != 400 {
		t.Fatalf("want 400 got %d", rec.Code)
	}
	// a bad size is refused even when the picture does not exist
	if rec := artGet(artServer(t, &fakeArt{err: art.ErrNoArt}), "/api/art/cover?path=/m/a.flac&size=abc", nil); rec.Code != 400 {
		t.Fatalf("want 400 got %d", rec.Code)
	}
}

func TestArtSizeUndecodableIs404(t *testing.T) {
	h := artServer(t, &fakeArt{img: &art.Image{Data: pngHead, ContentType: "image/png", Key: "abc"}})
	if rec := artGet(h, "/api/art/cover?path=/m/a.flac&size=64", nil); rec.Code != 404 {
		t.Fatalf("want 404 got %d", rec.Code)
	}
}

func TestSizedImageMemoized(t *testing.T) {
	s := &serverState{}
	img := &art.Image{Data: bigPNG(t, 300, 300), ContentType: "image/png", Key: "k"}
	a, err := s.sizedImage(img, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.sizedImage(img, 100)
	if &a.Data[0] != &b.Data[0] {
		t.Fatal("second call must reuse the first result")
	}
	if c, _ := s.sizedImage(img, 50); &c.Data[0] == &a.Data[0] {
		t.Fatal("a different size is a different image")
	}
	if a.Key != "k@"+strconv.Itoa(100) {
		t.Fatalf("key: %s", a.Key)
	}
}

func TestSizedCacheIsBounded(t *testing.T) {
	s := &serverState{}
	img := &art.Image{Data: bigPNG(t, 40, 40), ContentType: "image/png"}
	for i := 0; i < 200; i++ {
		img.Key = strconv.Itoa(i)
		if _, err := s.sizedImage(img, 32); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.sizes.len(); n > sizedCacheMax {
		t.Fatalf("cache grew to %d", n)
	}
}
