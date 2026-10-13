package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"cliamp-web/webui2"
)

func fakeNext() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><body><div id="player"></div>`)},
		"app.css":    &fstest.MapFile{Data: []byte(`:root{--bg:#000}`)},
		"app.js":     &fstest.MapFile{Data: []byte(`'use strict';`)},
	}
}

func nextGet(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// The next UI lives beside the Vue app and needs no token, same rule as the
// static SPA assets.
func TestNextServesWithoutToken(t *testing.T) {
	cfg := authedConfig(t)
	cfg.Next = fakeNext()
	h := New(cfg)

	rec := nextGet(h, "/next/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="player"`) {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("page content type: %s", ct)
	}
	rec = nextGet(h, "/next/app.css")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = nextGet(h, "/next/app.js")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "use strict") {
		t.Fatalf("js: %d", rec.Code)
	}
	rec = nextGet(h, "/next/nope")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="player"`) {
		t.Fatalf("unknown path must fall back to the page: %d", rec.Code)
	}
	rec = nextGet(h, "/next")
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/next/" {
		t.Fatalf("bare /next: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = nextGet(h, "/")
	if rec.Code == 200 && strings.Contains(rec.Body.String(), `id="player"`) {
		t.Fatal("root served the next page")
	}
}

// The real embedded webui2 files must satisfy what the page references.
func TestNextEmbeddedPageWired(t *testing.T) {
	cfg := authedConfig(t)
	cfg.Next = webui2.FS()
	h := New(cfg)
	rec := nextGet(h, "/next/")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `href="app.css"`) || !strings.Contains(body, `src="app.js"`) {
		t.Fatalf("embedded page: %d", rec.Code)
	}
	for _, f := range []string{"/next/app.css", "/next/app.js"} {
		if rec := nextGet(h, f); rec.Code != 200 || rec.Body.Len() == 0 {
			t.Fatalf("%s: %d len=%d", f, rec.Code, rec.Body.Len())
		}
	}
}
