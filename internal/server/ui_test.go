package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"cliamp-web/webui"
)

func fakeUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><body><div id="player"></div>`)},
		"app.css":    &fstest.MapFile{Data: []byte(`:root{--bg:#000}`)},
		"app.js":     &fstest.MapFile{Data: []byte(`'use strict';`)},
	}
}

func uiGet(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// The UI lives at the root and is open: only /api/* carries player
// state and guards on it.
func TestUIServesAtRoot(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.UI = fakeUI()
	h := New(cfg)

	rec := uiGet(h, "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="player"`) {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("page content type: %s", ct)
	}
	rec = uiGet(h, "/app.css")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = uiGet(h, "/app.js")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "use strict") {
		t.Fatalf("js: %d", rec.Code)
	}
	rec = uiGet(h, "/some/unknown/route")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="player"`) {
		t.Fatalf("unknown path must fall back to the page: %d", rec.Code)
	}
	// the old /next/ bookmark must land on the root page
	rec = uiGet(h, "/next/")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/" {
		t.Fatalf("old /next/: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// unknown API paths are a 404, not the HTML page
	rec = uiGet(h, "/api/nope")
	if rec.Code != 404 {
		t.Fatalf("unknown api path: want 404, got %d", rec.Code)
	}
}

// The real embedded files must satisfy what the page references.
func TestUIEmbeddedPageWired(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.UI = webui.FS()
	h := New(cfg)
	rec := uiGet(h, "/")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `href="app.css"`) || !strings.Contains(body, `src="app.js"`) {
		t.Fatalf("embedded page: %d", rec.Code)
	}
	for _, f := range []string{"/app.css", "/app.js"} {
		if rec := uiGet(h, f); rec.Code != 200 || rec.Body.Len() == 0 {
			t.Fatalf("%s: %d len=%d", f, rec.Code, rec.Body.Len())
		}
	}
}

// Without a UI configured, root is 404 — but never a token wall.
func TestUINilIsPlain404(t *testing.T) {
	h := New(guardedConfig(t))
	if rec := uiGet(h, "/"); rec.Code != 404 {
		t.Fatalf("nil UI: %d", rec.Code)
	}
}

func TestAppIconsAndManifestAreEmbeddedAndServed(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.UI = webui.FS()
	h := New(cfg)
	want := map[string]string{
		"/icon.svg": "image/svg+xml", "/icon-192.png": "image/png", "/icon-512.png": "image/png",
		"/icon-maskable-512.png": "image/png", "/apple-touch-icon.png": "image/png", "/favicon-32.png": "image/png",
		"/manifest.json": "application/json",
	}
	for path, ct := range want {
		rec := uiGet(h, path)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), ct) || rec.Body.Len() < 200 {
			t.Errorf("%s: %d %q %d bytes", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
	page := uiGet(h, "/").Body.String()
	for _, link := range []string{`rel="icon"`, `rel="apple-touch-icon"`, `rel="manifest"`} {
		if !strings.Contains(page, link) {
			t.Errorf("page lacks %s", link)
		}
	}
}
