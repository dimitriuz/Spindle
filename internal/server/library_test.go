package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"spindle/internal/ipc"
)

func libConfig(t *testing.T, fr *fakeRunner) Config {
	cfg := guardedConfig(t)
	cfg.Runner = fr
	return cfg
}

func libGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path, nil)
	h.ServeHTTP(rec, r)
	return rec
}

func TestLibraryPlaylistsPassesParamsThrough(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"total":120,"playlists":[{"id":"pl1","name":"One"}]}`)}}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/library/navidrome/playlists?offset=100&limit=50")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"pl1"`) {
		t.Fatalf("want verbatim passthrough, got %d %s", rec.Code, rec.Body)
	}
	if fr.lastOp != "provider.playlists" {
		t.Fatalf("op: %s", fr.lastOp)
	}
	if fr.lastParams["provider"] != "navidrome" || fr.lastParams["offset"] != 100 || fr.lastParams["limit"] != 50 {
		t.Fatalf("params must ride inside params: %v", fr.lastParams)
	}
}

func TestLibraryValidation(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true}`)}}
	h := New(libConfig(t, fr))
	for _, path := range []string{
		"/api/library/navidrome/playlists?offset=-1",
		"/api/library/navidrome/playlists?limit=0",
		"/api/library/navidrome/playlists?limit=abc",
		// unknown kinds never build provider.<kind> ops (GET must not reach
		// side-effecting ops like provider.load).
		"/api/library/navidrome/load",
		"/api/library/navidrome/albums2",
	} {
		if rec := libGet(t, h, path); rec.Code != 400 {
			t.Fatalf("%s want 400 got %d", path, rec.Code)
		}
	}
	// limit over 200 clamps rather than rejects
	_ = libGet(t, h, "/api/library/navidrome/playlists?limit=9999")
	if fr.lastParams["limit"] != 200 {
		t.Fatalf("limit must clamp to 200, got %v", fr.lastParams["limit"])
	}
}

func TestLibraryUpstreamErrorMaps502WithDetail(t *testing.T) {
	fr := &fakeRunner{err: &ipc.IPCError{Code: "internal_error", Detail: `navidrome: connection refused`}}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/library/navidrome/albums")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502 got %d", rec.Code)
	}
	var body map[string]any
	json.NewDecoder(rec.Body).Decode(&body)
	if body["detail"] != "navidrome: connection refused" || body["code"] != "upstream_error" {
		t.Fatalf("detail must pass through: %v", body)
	}
}

func TestLibraryOfflineMaps502(t *testing.T) {
	fr := &fakeRunner{err: ipc.ErrNotConnected}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/library/navidrome/albums")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502 got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "player offline") {
		t.Fatalf("offline detail: %s", rec.Body)
	}
}

func TestLibraryJobTimeout504(t *testing.T) {
	fr := &fakeRunner{errFn: func(ctx context.Context) error {
		<-ctx.Done() // never returns until the deadline
		return ctx.Err()
	}}
	cfg := libConfig(t, fr)
	cfg.LibraryTimeout = 20 * time.Millisecond
	h := New(cfg)
	rec := libGet(t, h, "/api/library/navidrome/albums")
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "job_timeout") {
		t.Fatalf("want 504 job_timeout got %d %s", rec.Code, rec.Body)
	}
}

func TestLibrarySearchUncached(t *testing.T) {
	calls := 0
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"total":0,"tracks":[]}`)}, okFn: func() { calls++ }}
	h := New(libConfig(t, fr))
	libGet(t, h, "/api/search/local?q=x")
	libGet(t, h, "/api/search/local?q=x")
	if calls != 2 {
		t.Fatalf("search must never cache: %d upstream calls", calls)
	}
}

func TestLibraryListingsCached(t *testing.T) {
	calls := 0
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"total":0,"playlists":[]}`)}, okFn: func() { calls++ }}
	h := New(libConfig(t, fr))
	libGet(t, h, "/api/library/local/playlists")
	libGet(t, h, "/api/library/local/playlists")
	if calls != 1 {
		t.Fatalf("second identical listing GET must be served from cache: %d", calls)
	}
}

func TestLibraryAlbumTracksParams(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"tracks":[]}`)}}
	h := New(libConfig(t, fr))
	libGet(t, h, "/api/library/navidrome/album/al1/tracks?offset=0&limit=50")
	if fr.lastOp != "provider.album_tracks" || fr.lastParams["album"] != "al1" {
		t.Fatalf("album tracks params: op=%s params=%v", fr.lastOp, fr.lastParams)
	}
	// the {kind} route must not shadow the album-tracks path
	libGet(t, h, "/api/library/navidrome/artist/ar2/tracks")
	if fr.lastOp != "provider.tracks" || fr.lastParams["id"] != "ar2" {
		t.Fatalf("artist tracks params: op=%s params=%v", fr.lastOp, fr.lastParams)
	}
}

func TestArtistAlbumsParams(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"albums":[]}`)}}
	h := New(libConfig(t, fr))
	libGet(t, h, "/api/library/navidrome/artist/ar1/albums?offset=0&limit=50")
	// cliamp v2.4.0 wants the artist id as params.artist (params.id → "Artist not found")
	if fr.lastOp != "provider.artist_albums" || fr.lastParams["artist"] != "ar1" {
		t.Fatalf("artist albums params: op=%s params=%v", fr.lastOp, fr.lastParams)
	}
}

func TestPlaylistTracksRoute(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"total":3,"tracks":[]}`)}}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/library/navidrome/playlist/pl1/tracks?offset=5&limit=10")
	if rec.Code != 200 || fr.lastOp != "provider.tracks" {
		t.Fatalf("want 200 provider.tracks, got %d %q", rec.Code, fr.lastOp)
	}
	if fr.lastParams["playlist"] != "pl1" || fr.lastParams["offset"] != 5 || fr.lastParams["limit"] != 10 {
		t.Fatalf("params: %v", fr.lastParams)
	}
	// Review Focus 4: this route can NEVER smuggle an id param upstream.
	if _, bad := fr.lastParams["id"]; bad {
		t.Fatalf("id must never reach provider.tracks params: %v", fr.lastParams)
	}
	if rec := libGet(t, h, "/api/library/navidrome/playlist/pl1/tracks?offset=-1"); rec.Code != 400 {
		t.Fatalf("offset -1 want 400 got %d", rec.Code)
	}
}

func TestPlaylistNotFoundMaps502(t *testing.T) {
	fr := &fakeRunner{err: &ipc.IPCError{Code: "internal_error", Detail: "navidrome: playlist not found (code 70)"}}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/library/navidrome/playlist/nope/tracks")
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "playlist not found (code 70)") {
		t.Fatalf("want 502+detail, got %d %s", rec.Code, rec.Body)
	}
}

func TestLyricsRoutePassthroughAndUncached(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"lyrics":[{"start":1.5,"text":"hi"}]}`)}}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/lyrics")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"hi"`) || fr.lastOp != "lyrics" {
		t.Fatalf("lyrics: %d %s op=%s", rec.Code, rec.Body, fr.lastOp)
	}
	_ = libGet(t, h, "/api/lyrics")
	_ = libGet(t, h, "/api/lyrics")
	if fr.calls != 3 { // Review Focus: lyrics must never cache
		t.Fatalf("lyrics must hit the runner every time, calls=%d", fr.calls)
	}
}

func TestLyricsNotFoundMaps502(t *testing.T) {
	fr := &fakeRunner{err: &ipc.IPCError{Code: "internal_error", Detail: "no lyrics found"}}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/lyrics")
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "no lyrics found") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestLibraryAlbumsPassTheProvidersSortOn(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"albums":[]}`)}}
	h := New(libConfig(t, fr))
	if rec := libGet(t, h, "/api/library/navidrome/albums?sort=newest"); rec.Code != 200 || fr.lastParams["sort"] != "newest" {
		t.Fatalf("%d %v", rec.Code, fr.lastParams)
	}
	for _, bad := range []string{"/api/library/navidrome/albums?sort=a/b", "/api/library/navidrome/albums?sort=" + strings.Repeat("a", 41), "/api/library/navidrome/artists?sort=newest", "/api/library/navidrome/albums?sort=a%20b"} {
		if rec := libGet(t, h, bad); rec.Code != 400 {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
}
