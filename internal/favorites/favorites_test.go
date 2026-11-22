package favorites

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindle/internal/ipc"
)

// fakeCliamp models what the real player does with favorites: both ops only
// TOGGLE, tracks live in a Favorites playlist, stations become f:<url> rows
// and show a star on their catalog row.
type fakeCliamp struct {
	mu       sync.Mutex
	tracks   []map[string]any
	starred  map[string]bool // catalog id -> favorited
	catalog  map[string]string
	calls    map[string]int
	lastBook map[string]any
	err      error // returned by every call when set
	ignore   bool  // toggles do nothing (a broken player)
}

func newFake() *fakeCliamp {
	return &fakeCliamp{starred: map[string]bool{}, calls: map[string]int{},
		catalog: map[string]string{"c:0": "MANGO [128k]", "c:1": "Dance Wave", "c:2": "Radio Paradise"}}
}

func (f *fakeCliamp) Call(context.Context, string, any) (*ipc.Response, error) {
	return &ipc.Response{OK: true}, nil
}

func (f *fakeCliamp) SubmitJob(_ context.Context, op string, params any) (*ipc.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[op]++
	if f.err != nil {
		return nil, f.err
	}
	p, _ := params.(map[string]any)
	res := func(v any) (*ipc.Job, error) {
		b, _ := json.Marshal(v)
		return &ipc.Job{State: "succeeded", Result: b}, nil
	}
	switch op {
	case "provider.tracks":
		off, lim := intOf(p["offset"]), intOf(p["limit"])
		if lim == 0 {
			lim = 50
		}
		end := min(off+lim, len(f.tracks))
		rows := []map[string]any{}
		if off < len(f.tracks) {
			rows = f.tracks[off:end]
		}
		return res(map[string]any{"ok": true, "total": len(f.tracks), "tracks": rows})
	case "provider.playlists":
		var rows []map[string]any
		for _, id := range []string{"c:0", "c:1", "c:2"} {
			name := f.catalog[id]
			if f.starred[id] {
				name = "★ " + name
			}
			rows = append(rows, map[string]any{"id": id, "name": name, "provider": "radio", "favoritable": true})
		}
		for id, on := range f.starred {
			if on {
				rows = append(rows, map[string]any{"id": "f:http://stream/" + id, "name": "★ " + f.catalog[id], "provider": "radio", "favorite": true})
			}
		}
		return res(map[string]any{"ok": true, "total": len(rows), "playlists": rows})
	case "playlist.bookmark":
		f.lastBook = p
		if f.ignore {
			return res(map[string]any{"ok": true})
		}
		tr, _ := p["track"].(map[string]any)
		_, key := TrackKey(tr)
		for i, t := range f.tracks {
			if _, k := TrackKey(t); k == key {
				f.tracks = append(f.tracks[:i], f.tracks[i+1:]...)
				return res(map[string]any{"ok": true})
			}
		}
		f.tracks = append(f.tracks, tr)
		return res(map[string]any{"ok": true})
	case "provider.favorite":
		id, _ := p["playlist"].(string)
		if strings.HasPrefix(id, "f:") {
			for cid := range f.starred {
				if "f:http://stream/"+cid == id && !f.ignore {
					f.starred[cid] = false
				}
			}
			return res(map[string]any{"ok": true})
		}
		if _, ok := f.catalog[id]; !ok {
			return nil, &ipc.IPCError{Code: "internal_error", Detail: "unknown favorite station"}
		}
		if !f.ignore {
			f.starred[id] = !f.starred[id]
		}
		return res(map[string]any{"ok": true})
	}
	return nil, errors.New("unexpected op " + op)
}

func intOf(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

func nd(id, title string) map[string]any {
	return map[string]any{"title": title, "artist": "A", "path": "https://nd/rest/stream?id=" + id + "&s=S&t=T&u=bob",
		"provider_meta": map[string]any{"navidrome.id": id}}
}

func newSvc(t *testing.T, f *fakeCliamp) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Unix(1_000_000, 0)
	s, err := New(dir, f, func(p string) string { return strings.ReplaceAll(p, "&s=S&t=T&u=bob", "") + "#restored" }, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func set(t *testing.T, s *Service, q SetRequest) bool {
	t.Helper()
	on, err := s.Set(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return on
}

func TestTrackFavoriteReadsThenToggles(t *testing.T) {
	f := newFake()
	s, _ := newSvc(t, f)
	q := SetRequest{Kind: Track, Favorite: true, Track: nd("s1", "One")}
	if !set(t, s, q) || f.calls["playlist.bookmark"] != 1 {
		t.Fatalf("on: calls=%v", f.calls)
	}
	if !set(t, s, q) || f.calls["playlist.bookmark"] != 1 {
		t.Fatalf("already favorited: must not toggle again (calls=%v)", f.calls)
	}
	q.Favorite = false
	if set(t, s, q) || f.calls["playlist.bookmark"] != 2 {
		t.Fatalf("off: calls=%v", f.calls)
	}
	if set(t, s, q) || f.calls["playlist.bookmark"] != 2 {
		t.Fatal("already off: no call")
	}
}

func TestConcurrentSetsDoNotDoubleToggle(t *testing.T) {
	f := newFake()
	s, _ := newSvc(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Set(context.Background(), SetRequest{Kind: Track, Favorite: true, Track: nd("s1", "One")}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.calls["playlist.bookmark"] != 1 || len(f.tracks) != 1 {
		t.Fatalf("20 taps for 'on' = one toggle; calls=%d tracks=%d", f.calls["playlist.bookmark"], len(f.tracks))
	}
}

func TestSetReportsTheStateAfterConfirm(t *testing.T) {
	f := newFake()
	f.ignore = true
	s, _ := newSvc(t, f)
	on, err := s.Set(context.Background(), SetRequest{Kind: Track, Favorite: true, Track: nd("s1", "One")})
	if err != nil || on {
		t.Fatalf("a player that ignores the toggle must read back as off, got on=%v err=%v", on, err)
	}
	if f.calls["playlist.bookmark"] != 1 {
		t.Fatalf("no retry loop: %v", f.calls)
	}
}

func TestTrackPathIsRestoredBeforeBookmark(t *testing.T) {
	f := newFake()
	s, _ := newSvc(t, f)
	set(t, s, SetRequest{Kind: Track, Favorite: true, Track: nd("s1", "One")})
	tr, _ := f.lastBook["track"].(map[string]any)
	if !strings.HasSuffix(tr["path"].(string), "#restored") || f.lastBook["provider"] != "navidrome" {
		t.Fatalf("bookmark params: %v", f.lastBook)
	}
}

func TestStationFavoriteByCatalogRow(t *testing.T) {
	f := newFake()
	s, _ := newSvc(t, f)
	if !set(t, s, SetRequest{Kind: Radio, Provider: "radio", ID: "c:2", Favorite: true}) {
		t.Fatal("c:2 should be favorited")
	}
	if !set(t, s, SetRequest{Kind: Radio, Provider: "radio", ID: "c:2", Favorite: true}) || f.calls["provider.favorite"] != 1 {
		t.Fatalf("already starred: no second toggle (%v)", f.calls)
	}
	items, err := s.List(context.Background())
	if err != nil || len(items) != 1 || items[0].Kind != Radio || !strings.HasPrefix(items[0].ID, "f:") || items[0].Title != "Radio Paradise" {
		t.Fatalf("list: %+v %v", items, err)
	}
	// removing from the favorites list uses the f: id; asking for "on" there is a no-op
	if !set(t, s, SetRequest{Kind: Radio, Provider: "radio", ID: items[0].ID, Favorite: true}) || f.calls["provider.favorite"] != 1 {
		t.Fatal("f: row asked to be favorited is already favorited")
	}
	if set(t, s, SetRequest{Kind: Radio, Provider: "radio", ID: items[0].ID, Favorite: false}) {
		t.Fatal("f: row asked to be removed must be off")
	}
}

func TestUnknownStationErrorSurfaces(t *testing.T) {
	s, _ := newSvc(t, newFake())
	if _, err := s.Set(context.Background(), SetRequest{Kind: Radio, Provider: "radio", ID: "c:99", Favorite: true}); err == nil {
		t.Fatal("unknown station must fail")
	}
}

func TestAlbumArtistPlaylistFileRoundTrip(t *testing.T) {
	f := newFake()
	s, dir := newSvc(t, f)
	for _, k := range []Kind{Album, Artist, Playlist} {
		set(t, s, SetRequest{Kind: k, Provider: "navidrome", ID: "id-" + string(k), Title: "T-" + string(k), Subtitle: "sub", Favorite: true})
	}
	set(t, s, SetRequest{Kind: Album, Provider: "navidrome", ID: "id-album", Title: "T-album", Favorite: true}) // twice = once
	s2, err := New(dir, f, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := s2.List(context.Background())
	if len(items) != 3 {
		t.Fatalf("reloaded: %+v", items)
	}
	if set(t, s2, SetRequest{Kind: Artist, Provider: "navidrome", ID: "id-artist", Favorite: false}) {
		t.Fatal("removed")
	}
	items, _ = s2.List(context.Background())
	if len(items) != 2 {
		t.Fatalf("after remove: %+v", items)
	}
	if len(f.calls) != 0 && f.calls["playlist.bookmark"]+f.calls["provider.favorite"] != 0 {
		t.Fatal("albums, artists and playlists never touch cliamp's favorites")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "favorites.json"))
	for _, secret := range []string{"SALT", "TOKEN", "bob", "&t="} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("favorites.json holds %q", secret)
		}
	}
}

func TestCorruptFileStartsEmptyAndKeepsBad(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "favorites.json"), []byte("{not json"), 0o600)
	s, err := New(dir, newFake(), nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if items, _ := s.List(context.Background()); len(items) != 0 {
		t.Fatalf("%+v", items)
	}
	if _, err := os.Stat(filepath.Join(dir, "favorites.json.bad")); err != nil {
		t.Fatalf("the unreadable file must be kept: %v", err)
	}
	set(t, s, SetRequest{Kind: Album, Provider: "p", ID: "x", Title: "X", Favorite: true}) // and it still works
}

func TestListMergesSourcesInOrder(t *testing.T) {
	f := newFake()
	s, _ := newSvc(t, f)
	set(t, s, SetRequest{Kind: Playlist, Provider: "navidrome", ID: "p1", Title: "P1", Favorite: true})
	set(t, s, SetRequest{Kind: Album, Provider: "navidrome", ID: "a1", Title: "A1", Favorite: true})
	set(t, s, SetRequest{Kind: Album, Provider: "navidrome", ID: "a2", Title: "A2", Favorite: true})
	set(t, s, SetRequest{Kind: Radio, Provider: "radio", ID: "c:1", Favorite: true})
	set(t, s, SetRequest{Kind: Track, Favorite: true, Track: nd("t1", "T1")})
	set(t, s, SetRequest{Kind: Track, Favorite: true, Track: nd("t2", "T2")})
	items, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, string(it.Kind)+":"+it.Title)
	}
	want := "album:A2 album:A1 playlist:P1 radio:Dance Wave track:T2 track:T1"
	if strings.Join(got, " ") != want {
		t.Fatalf("order:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if items[4].Track == nil || items[4].Provider != "navidrome" || items[4].ID != "t2" {
		t.Fatalf("track item: %+v", items[4])
	}
}

func TestListPagesCliampFavorites(t *testing.T) {
	f := newFake()
	for i := 0; i < 450; i++ {
		f.tracks = append(f.tracks, nd("id"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "T"))
	}
	s, _ := newSvc(t, f)
	items, err := s.List(context.Background())
	if err != nil || len(items) != 450 {
		t.Fatalf("%d items, err %v", len(items), err)
	}
	if f.calls["provider.tracks"] < 3 {
		t.Fatalf("450 rows need several pages, calls=%d", f.calls["provider.tracks"])
	}
}

func TestTrackKey(t *testing.T) {
	if p, id := TrackKey(nd("s9", "x")); p != "navidrome" || id != "s9" {
		t.Fatalf("%s %s", p, id)
	}
	if p, id := TrackKey(map[string]any{"path": "/music/a.flac"}); p != "local" || id != "/music/a.flac" {
		t.Fatalf("%s %s", p, id)
	}
	if p, id := TrackKey(nil); p != "" || id != "" {
		t.Fatalf("nil: %s %s", p, id)
	}
}

func TestCliampErrorIsReturnedAndNothingCached(t *testing.T) {
	f := newFake()
	s, _ := newSvc(t, f)
	set(t, s, SetRequest{Kind: Album, Provider: "p", ID: "a", Title: "A", Favorite: true})
	f.err = errors.New("player offline")
	items, err := s.List(context.Background())
	if !errors.Is(err, ErrPartial) || len(items) != 1 {
		t.Fatalf("bridge items still listed, error flagged: %+v %v", items, err)
	}
	if _, err := s.Set(context.Background(), SetRequest{Kind: Track, Favorite: true, Track: nd("s1", "One")}); err == nil {
		t.Fatal("a track toggle while cliamp is down must fail")
	}
	f.err = nil
	if items, err := s.List(context.Background()); err != nil || len(items) != 1 {
		t.Fatalf("recovers, partial result was not cached: %+v %v", items, err)
	}
}

func TestListCachedFor30sAndDroppedOnSet(t *testing.T) {
	f := newFake()
	now := time.Unix(1_000_000, 0)
	s, err := New(t.TempDir(), f, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.List(context.Background())
	_, _ = s.List(context.Background())
	if f.calls["provider.tracks"] != 1 {
		t.Fatalf("cached: %d reads", f.calls["provider.tracks"])
	}
	now = now.Add(31 * time.Second)
	_, _ = s.List(context.Background())
	if f.calls["provider.tracks"] != 2 {
		t.Fatal("expired after 30 s")
	}
	set(t, s, SetRequest{Kind: Album, Provider: "p", ID: "a", Title: "A", Favorite: true})
	before := f.calls["provider.tracks"]
	_, _ = s.List(context.Background())
	if f.calls["provider.tracks"] == before {
		t.Fatal("a change drops the cache")
	}
}
