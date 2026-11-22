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

func TestArtWithoutResolverIsNoArt(t *testing.T) {
	cfg := guardedConfig(t)
	if rec := artGet(New(cfg), "/api/art/cover?path=%2Fx", nil); rec.Code != 404 {
		t.Fatalf("%d", rec.Code)
	}
}

type fakeInfo struct {
	info     art.Info
	err      error
	path     string
	duration float64
}

func (f *fakeInfo) Info(_ context.Context, p string, d float64) (art.Info, error) {
	f.path, f.duration = p, d
	return f.info, f.err
}

func infoServer(t *testing.T, fi *fakeInfo) http.Handler {
	cfg := guardedConfig(t)
	cfg.Info = fi
	return New(cfg)
}

func TestTrackInfoServesJSON(t *testing.T) {
	fi := &fakeInfo{info: art.Info{Format: "FLAC", BitrateKbps: 1161, BitDepth: 24, SampleRate: 44100, Channels: 2}}
	rec := artGet(infoServer(t, fi), "/api/trackinfo?path=%2Fm%2Fa.flac&duration=77.5", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	for _, want := range []string{`"format":"FLAC"`, `"bitrate_kbps":1161`, `"bit_depth":24`, `"sample_rate":44100`, `"channels":2`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, rec.Body)
		}
	}
	if fi.path != "/m/a.flac" || fi.duration != 77.5 {
		t.Fatalf("source got %q %v", fi.path, fi.duration)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=3600") {
		t.Fatalf("cache: %v", rec.Header())
	}
}

func TestTrackInfoOmitsUnknownFields(t *testing.T) {
	rec := artGet(infoServer(t, &fakeInfo{info: art.Info{Format: "MP3"}}), "/api/trackinfo?path=%2Fx", nil)
	if strings.Contains(rec.Body.String(), "bit_depth") || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("unknown fields must be left out: %s", rec.Body)
	}
}

func TestTrackInfo404AndBadInput(t *testing.T) {
	const secret = "https://nd.lan/rest/stream?id=1&s=SALT&t=TOKEN&u=bob"
	for _, err := range []error{art.ErrNoInfo, errors.New("boom " + secret)} {
		rec := artGet(infoServer(t, &fakeInfo{err: err}), "/api/trackinfo?path="+secret, nil)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"no_info"`) {
			t.Fatalf("%v: %d %s", err, rec.Code, rec.Body)
		}
		for _, s := range []string{"SALT", "TOKEN", "bob", "boom"} {
			if strings.Contains(rec.Body.String(), s) {
				t.Fatalf("404 leaks %q", s)
			}
		}
	}
	h := infoServer(t, &fakeInfo{})
	if rec := artGet(h, "/api/trackinfo", nil); rec.Code != 400 {
		t.Fatalf("missing path: %d", rec.Code)
	}
	if rec := artGet(New(guardedConfig(t)), "/api/trackinfo?path=%2Fx", nil); rec.Code != 404 {
		t.Fatalf("no source: %d", rec.Code)
	}
}

func TestTrackInfoBadDurationIsIgnored(t *testing.T) {
	fi := &fakeInfo{info: art.Info{Format: "MP3"}}
	if rec := artGet(infoServer(t, fi), "/api/trackinfo?path=%2Fx&duration=abc", nil); rec.Code != 200 || fi.duration != 0 {
		t.Fatalf("%d duration=%v", rec.Code, fi.duration)
	}
}

type fakeItemArt struct {
	img  *art.Image
	err  error
	kind string
	id   string
}

func (f *fakeItemArt) ItemArt(_ context.Context, kind, id string) (*art.Image, error) {
	f.kind, f.id = kind, id
	return f.img, f.err
}

func itemServer(t *testing.T, fi *fakeItemArt) http.Handler {
	cfg := guardedConfig(t)
	cfg.ItemArt = fi
	return New(cfg)
}

func TestItemArtRouteServesWithETag(t *testing.T) {
	fi := &fakeItemArt{img: &art.Image{Data: pngHead, ContentType: "image/png", Key: "k1"}}
	h := itemServer(t, fi)
	rec := artGet(h, "/api/art/item?provider=navidrome&kind=album&id=Abc_1-2.x", nil)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"k1"` || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if fi.kind != "album" || fi.id != "Abc_1-2.x" {
		t.Fatalf("source got %q %q", fi.kind, fi.id)
	}
	if rec := artGet(h, "/api/art/item?provider=navidrome&kind=artist&id=x", map[string]string{"If-None-Match": `"k1"`}); rec.Code != 304 {
		t.Fatalf("304: %d", rec.Code)
	}
}

func TestItemArtRouteRefusesBadInputBeforeTheSource(t *testing.T) {
	fi := &fakeItemArt{img: &art.Image{Data: pngHead, ContentType: "image/png", Key: "k"}}
	h := itemServer(t, fi)
	long := strings.Repeat("a", 81)
	for _, q := range []string{
		"provider=navidrome&kind=album", "provider=navidrome&kind=album&id=", "provider=spotify&kind=album&id=x", "provider=navidrome&kind=track&id=x",
		"provider=navidrome&kind=album&id=a%2Fb", "provider=navidrome&kind=album&id=a%3Fb", "provider=navidrome&kind=album&id=..", "provider=navidrome&kind=album&id=a%26b",
		"provider=navidrome&kind=album&id=a%0Ab", "provider=navidrome&kind=album&id=" + long, "kind=album&id=x",
	} {
		if rec := artGet(h, "/api/art/item?"+q, nil); rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d", q, rec.Code)
		}
	}
	if fi.id != "" {
		t.Fatalf("a refused request reached the source: %q", fi.id)
	}
}

func TestItemArt404HasNoCredentialsAndNilSourceIs404(t *testing.T) {
	for _, err := range []error{art.ErrNoArt, errors.New("GET https://nd/rest/getCoverArt?u=bob&t=TOKEN&s=SALT failed")} {
		rec := artGet(itemServer(t, &fakeItemArt{err: err}), "/api/art/item?provider=navidrome&kind=album&id=x", nil)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"no_art"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		for _, s := range []string{"bob", "TOKEN", "SALT", "getCoverArt"} {
			if strings.Contains(rec.Body.String(), s) {
				t.Fatalf("404 leaks %q", s)
			}
		}
	}
	if rec := artGet(New(guardedConfig(t)), "/api/art/item?provider=navidrome&kind=album&id=x", nil); rec.Code != 404 {
		t.Fatalf("no source: %d", rec.Code)
	}
}

type fakeStationArt struct {
	img  *art.Image
	err  error
	name string
}

func (f *fakeStationArt) StationArt(_ context.Context, name string) (*art.Image, error) {
	f.name = name
	return f.img, f.err
}

func (f *fakeStationArt) StationArtByID(_ context.Context, id string) (*art.Image, error) {
	f.name = "id:" + id
	return f.img, f.err
}

func (f *fakeStationArt) StationMeta(_ context.Context, names []string) []art.StationMeta {
	out := make([]art.StationMeta, len(names))
	for i, n := range names {
		out[i] = art.StationMeta{Name: n, Found: true}
	}
	return out
}

func (f *fakeStationArt) StationSearch(_ context.Context, q string) []art.StationHit {
	f.name = "q:" + q
	if q == "none" {
		return nil
	}
	return []art.StationHit{{ID: "u1", URL: "http://x/live", StationMeta: art.StationMeta{Name: "X", Found: true}}}
}

func TestRadioMetaAndSearchRoutes(t *testing.T) {
	fs := &fakeStationArt{}
	cfg := guardedConfig(t)
	cfg.StationMeta = fs
	cfg.StationArt = fs
	h := New(cfg)
	rec := artGet(h, "/api/radio/search?q=jazz", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"u1"`) || fs.name != "q:jazz" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := artGet(h, "/api/radio/search?q=none", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"stations":[]`) {
		t.Fatalf("empty result must be a list: %d %s", rec.Code, rec.Body)
	}
	for _, target := range []string{"/api/radio/search", "/api/radio/search?q=" + strings.Repeat("a", 101)} {
		if rec := artGet(h, target, nil); rec.Code != 400 {
			t.Fatalf("%s: %d", target, rec.Code)
		}
	}
	if rec := artGet(New(guardedConfig(t)), "/api/radio/search?q=x", nil); rec.Code != 404 {
		t.Fatalf("no source: %d", rec.Code)
	}
	fs.img = &art.Image{Data: []byte("p"), ContentType: "image/png", Key: "k"}
	if rec := artGet(h, "/api/art/station?id=u1", nil); rec.Code != 200 || fs.name != "id:u1" {
		t.Fatalf("by id: %d %q", rec.Code, fs.name)
	}
	if rec := artGet(h, "/api/art/station?id=u1&name=x", nil); rec.Code != 400 {
		t.Fatalf("id and name together: %d", rec.Code)
	}
}

func TestStationArtRoute(t *testing.T) {
	fs := &fakeStationArt{img: &art.Image{Data: []byte("png"), ContentType: "image/png", Key: "k1"}}
	cfg := guardedConfig(t)
	cfg.StationArt = fs
	h := New(cfg)
	rec := artGet(h, "/api/art/station?name="+"Classic+Vinyl+HD+%5B320k%5D+%C2%B7+USA", nil)
	if rec.Code != 200 || rec.Header().Get("ETag") == "" || fs.name != "Classic Vinyl HD [320k] · USA" {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), fs.name)
	}
	if rec := artGet(h, "/api/art/station", nil); rec.Code != 400 {
		t.Fatalf("missing name: %d", rec.Code)
	}
	if rec := artGet(h, "/api/art/station?name="+strings.Repeat("a", 201), nil); rec.Code != 400 {
		t.Fatalf("long name: %d", rec.Code)
	}
	fs.img, fs.err = nil, errors.New("no")
	if rec := artGet(h, "/api/art/station?name=x", nil); rec.Code != 404 || strings.Contains(rec.Body.String(), "no\"") {
		t.Fatalf("failure: %d %s", rec.Code, rec.Body)
	}
	if rec := artGet(New(guardedConfig(t)), "/api/art/station?name=x", nil); rec.Code != 404 {
		t.Fatalf("no source: %d", rec.Code)
	}
}

func TestRadioMetaRoute(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.StationMeta = &fakeStationArt{}
	h := New(cfg)
	rec := enqPost(h, "/api/radio/meta", `{"names":["A [128k] · DE","B"]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"B"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	many := `{"names":["` + strings.Repeat(`a","`, 60) + `a"]}`
	for _, body := range []string{`{"names":[]}`, `{}`, `nope`, many, `{"names":["` + strings.Repeat("a", 201) + `"]}`} {
		if rec := enqPost(h, "/api/radio/meta", body); rec.Code != 400 {
			t.Fatalf("%.30s: %d", body, rec.Code)
		}
	}
}
