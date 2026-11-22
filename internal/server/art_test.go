package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cliamp-web/internal/art"
)

type fakeArt struct {
	img  *art.Image
	err  error
	kind art.Kind
	path string
}

func (f *fakeArt) Get(_ context.Context, k art.Kind, p string) (*art.Image, error) {
	f.kind, f.path = k, p
	return f.img, f.err
}

func artServer(t *testing.T, fa *fakeArt) http.Handler {
	cfg := guardedConfig(t)
	cfg.Art = fa
	return New(cfg)
}

func artGet(h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	h.ServeHTTP(rec, r)
	return rec
}

var pngHead = []byte("\x89PNG\r\n\x1a\n")

func TestArtRouteServesImageWithETag(t *testing.T) {
	fa := &fakeArt{img: &art.Image{Data: pngHead, ContentType: "image/png", Key: "abc123"}}
	rec := artGet(artServer(t, fa), "/api/art/disc?path="+"%2Fmusic%2Fa%20b.flac", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("%d %s", rec.Code, rec.Header())
	}
	if rec.Header().Get("ETag") != `"abc123"` || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=86400") {
		t.Fatalf("caching headers: %v", rec.Header())
	}
	if fa.kind != art.Disc || fa.path != "/music/a b.flac" {
		t.Fatalf("resolver got %q %q", fa.kind, fa.path)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("images must be served nosniff")
	}
}

func TestArt304OnMatchingETag(t *testing.T) {
	fa := &fakeArt{img: &art.Image{Data: pngHead, ContentType: "image/png", Key: "abc123"}}
	rec := artGet(artServer(t, fa), "/api/art/cover?path=%2Fx", map[string]string{"If-None-Match": `"abc123"`})
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("want empty 304, got %d (%d bytes)", rec.Code, rec.Body.Len())
	}
}

func TestArt404NoArtBodyHasNoCredentials(t *testing.T) {
	const secret = "https://nd.lan/rest/stream?id=1&s=SALT&t=TOKEN&u=bob"
	for _, err := range []error{art.ErrNoArt, errors.New("boom " + secret)} {
		fa := &fakeArt{err: err}
		rec := artGet(artServer(t, fa), "/api/art/cover?path="+secret, nil)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"no_art"`) {
			t.Fatalf("%v: %d %s", err, rec.Code, rec.Body)
		}
		for _, s := range []string{"SALT", "TOKEN", "bob", "boom"} {
			if strings.Contains(rec.Body.String(), s) {
				t.Fatalf("404 body leaks %q: %s", s, rec.Body)
			}
		}
	}
}

func TestArt400OnBadKindOrMissingPath(t *testing.T) {
	h := artServer(t, &fakeArt{})
	for _, u := range []string{"/api/art/nope?path=%2Fx", "/api/art/cover", "/api/art/cover?path="} {
		if rec := artGet(h, u, nil); rec.Code != 400 {
			t.Fatalf("%s: %d", u, rec.Code)
		}
	}
}

func TestArtRouteIsBehindTheGuard(t *testing.T) {
	h := artServer(t, &fakeArt{img: &art.Image{Data: pngHead, ContentType: "image/png", Key: "k"}})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/art/cover?path=%2Fx", nil)
	r.Host = "evil.example.org"
	h.ServeHTTP(rec, r)
	if rec.Code != 400 {
		t.Fatalf("foreign Host: %d", rec.Code)
	}
}

func TestArtWithoutResolverIsNoArt(t *testing.T) {
	cfg := guardedConfig(t)
	if rec := artGet(New(cfg), "/api/art/cover?path=%2Fx", nil); rec.Code != 404 {
		t.Fatalf("%d", rec.Code)
	}
}
