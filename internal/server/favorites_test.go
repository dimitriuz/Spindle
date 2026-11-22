package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"spindle/internal/favorites"
	"spindle/internal/ipc"
)

type fakeFav struct {
	items []favorites.Item
	err   error
	got   favorites.SetRequest
	calls int
	state bool
}

func (f *fakeFav) List(context.Context) ([]favorites.Item, error) { return f.items, f.err }
func (f *fakeFav) Set(_ context.Context, q favorites.SetRequest) (bool, error) {
	f.calls++
	f.got = q
	if f.err != nil && !errors.Is(f.err, favorites.ErrPartial) {
		return false, f.err
	}
	f.state = q.Favorite
	return q.Favorite, nil
}

func favServer(t *testing.T, ff *fakeFav) (http.Handler, *Hub) {
	cfg := guardedConfig(t)
	cfg.Favorites = ff
	cfg.Runner = &fakeRunner{}
	cfg.Limiter = NewLimiter(20, 40, time.Now)
	return New(cfg), cfg.Hub
}

func favPut(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/favorites", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, r)
	return rec
}

func TestFavoritesListShapeAndScrubbing(t *testing.T) {
	const real = "https://nd.lan/rest/stream?id=1&s=SALT&t=TOKEN&u=bob"
	raw, _ := json.Marshal(map[string]any{"title": "T", "path": real, "provider_meta": map[string]any{"navidrome.id": "1"}})
	ff := &fakeFav{items: []favorites.Item{
		{Kind: favorites.Album, Provider: "navidrome", ID: "a1", Title: "A"},
		{Kind: favorites.Track, Provider: "navidrome", ID: "1", Title: "T", Track: raw},
	}}
	h, _ := favServer(t, ff)
	rec := artGet(h, "/api/favorites", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"items"`) || !strings.Contains(body, `"kind":"album"`) {
		t.Fatalf("%d %s", rec.Code, body)
	}
	for _, secret := range []string{"SALT", "TOKEN", "bob"} {
		if strings.Contains(body, secret) {
			t.Fatalf("the listing leaks %q: %s", secret, body)
		}
	}
	// an empty list is [] and never null
	rec = artGet(func() http.Handler { h, _ := favServer(t, &fakeFav{}); return h }(), "/api/favorites", nil)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty: %s", rec.Body)
	}
}

func TestFavoritesListFlagsAPartialResult(t *testing.T) {
	ff := &fakeFav{items: []favorites.Item{{Kind: favorites.Album, Provider: "p", ID: "a", Title: "A"}}, err: favorites.ErrPartial}
	h, _ := favServer(t, ff)
	rec := artGet(h, "/api/favorites", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"partial":true`) || !strings.Contains(rec.Body.String(), `"A"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestFavoritesPutSetsAndBroadcastsOnce(t *testing.T) {
	ff := &fakeFav{}
	h, hub := favServer(t, ff)
	_, ch := hub.Subscribe()
	rec := favPut(h, `{"kind":"album","provider":"navidrome","id":"a1","favorite":true,"title":"Album","subtitle":"Artist"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"favorite":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if ff.got.Kind != favorites.Album || ff.got.ID != "a1" || ff.got.Title != "Album" || ff.got.Subtitle != "Artist" || !ff.got.Favorite {
		t.Fatalf("service got %+v", ff.got)
	}
	select {
	case f := <-ch:
		if f.Name != "favorites" {
			t.Fatalf("frame %q", f.Name)
		}
	default:
		t.Fatal("a change must broadcast a favorites frame")
	}
	if len(ch) != 0 {
		t.Fatal("exactly one frame")
	}
}

// A PUT may have changed something (the service reports the state after the
// call either way), so other pages always get one frame to refetch.
func TestFavoritesPutAlwaysBroadcastsOneFrame(t *testing.T) {
	ff := &fakeFav{}
	h, hub := favServer(t, ff)
	_, ch := hub.Subscribe()
	favPut(h, `{"kind":"album","provider":"p","id":"a","favorite":false}`)
	if len(ch) != 1 {
		t.Fatalf("frames: %d", len(ch))
	}
}

func TestFavoritesPutRestoresScrubbedTrackPath(t *testing.T) {
	const real = "https://nd.lan/rest/stream?id=1&s=SALT&t=TOKEN&u=bob"
	scrubbed, _ := scrubURL(real)
	urls.remember(scrubbed, real)
	ff := &fakeFav{}
	h, _ := favServer(t, ff)
	body, _ := json.Marshal(map[string]any{"kind": "track", "favorite": true,
		"track": map[string]any{"title": "T", "path": scrubbed, "provider_meta": map[string]any{"navidrome.id": "1"}}})
	if rec := favPut(h, string(body)); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if ff.got.Track["path"] != real {
		t.Fatalf("service must receive the original URL, got %v", ff.got.Track["path"])
	}
}

func TestFavoritesPutValidation(t *testing.T) {
	h, _ := favServer(t, &fakeFav{})
	for name, body := range map[string]string{
		"unknown kind": `{"kind":"song","provider":"p","id":"x","favorite":true}`,
		"no favorite":  `{"kind":"album","provider":"p","id":"x"}`,
		"no id":        `{"kind":"album","provider":"p","favorite":true}`,
		"not json":     `nope`,
	} {
		if rec := favPut(h, body); rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d", name, rec.Code)
		}
	}
	// a track needs its object, the others need their provider
	if rec := favPut(h, `{"kind":"track","favorite":true}`); rec.Code != 400 {
		t.Fatalf("track without object: %d", rec.Code)
	}
}

func TestFavoritesPutIsGuarded(t *testing.T) {
	h, _ := favServer(t, &fakeFav{})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/favorites", strings.NewReader(`{"kind":"album","provider":"p","id":"x","favorite":true}`))
	h.ServeHTTP(rec, r) // no JSON content type
	if rec.Code != 415 {
		t.Fatalf("content type: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("PUT", "/api/favorites", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://evil.example")
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("origin: %d", rec.Code)
	}
}

func TestFavoritesPutReportsUpstreamFailure(t *testing.T) {
	ff := &fakeFav{err: &ipc.IPCError{Code: "internal_error", Detail: "unknown favorite station"}}
	h, hub := favServer(t, ff)
	_, ch := hub.Subscribe()
	rec := favPut(h, `{"kind":"radio","provider":"radio","id":"c:9","favorite":true}`)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "unknown favorite station") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(ch) != 0 {
		t.Fatal("a failed change broadcasts nothing")
	}
}

func TestFavoriteOpsAreNotReachableThroughCommand(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded"}}
	h := New(commandConfig(t, fr))
	for _, op := range []string{"playlist.bookmark", "provider.favorite"} {
		if rec := postCommand(h, `{"op":"`+op+`","params":{}}`); rec.Code != 403 || fr.calls != 0 {
			t.Fatalf("%s: %d calls=%d", op, rec.Code, fr.calls)
		}
	}
}

func TestFavoritesWithoutServiceIs404(t *testing.T) {
	if rec := artGet(New(guardedConfig(t)), "/api/favorites", nil); rec.Code != 404 {
		t.Fatalf("%d", rec.Code)
	}
}
