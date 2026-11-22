package enqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"spindle/internal/files"
	"strings"
	"sync"
	"testing"
	"time"

	"spindle/internal/ipc"
)

// fakeCliamp records every call in order and serves paged listings.
type fakeCliamp struct {
	mu        sync.Mutex
	calls     []string
	albums    map[string][]map[string]any
	playlists map[string][]map[string]any
	artists   map[string][]string // artist -> album ids
	failAt    int                 // fail the Nth "queue" call (1-based), 0 = never
	failErr   error
	queueN    int
	gate      chan struct{} // when set, every "queue" call waits for a token
	// playback model: after track.play the player says "stopped" for startPolls
	// state polls, then "playing"
	startPolls          int
	polls               int
	playRequested       bool
	queuedBeforePlaying int // queue/track.queue calls that arrived while it was not playing
	rows                int // live playlist length, for the local-file ops
}

func trk(title, path string) map[string]any {
	return map[string]any{"title": title, "path": path, "artist": "A"}
}

func tracks(prefix string, n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = trk(fmt.Sprintf("%s%d", prefix, i), fmt.Sprintf("/m/%s%d.flac", prefix, i))
	}
	return out
}

func (f *fakeCliamp) Call(_ context.Context, method string, _ any) (*ipc.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := "stopped"
	if method == "state.get" && f.playRequested {
		f.polls++
		if f.polls > f.startPolls {
			state = "playing"
		}
	}
	return &ipc.Response{OK: true, Snapshot: json.RawMessage(fmt.Sprintf(`{"state":%q,"total":%d}`, state, f.rows))}, nil
}

func (f *fakeCliamp) playing() bool { return f.playRequested && f.polls > f.startPolls }

func page(rows []map[string]any, p map[string]any, field string) (*ipc.Job, error) {
	off, lim := intOf(p["offset"]), intOf(p["limit"])
	if lim == 0 {
		lim = 50
	}
	end := min(off+lim, len(rows))
	out := []map[string]any{}
	if off < len(rows) {
		out = rows[off:end]
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "total": len(rows), field: out})
	return &ipc.Job{State: "succeeded", Result: b}, nil
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

func (f *fakeCliamp) SubmitJob(ctx context.Context, op string, params any) (*ipc.Job, error) {
	p, _ := params.(map[string]any)
	if op == "track.queue" && f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true}`)}
	switch op {
	case "provider.album_tracks":
		return page(f.albums[p["album"].(string)], p, "tracks")
	case "provider.tracks":
		return page(f.playlists[p["playlist"].(string)], p, "tracks")
	case "provider.artist_albums":
		var rows []map[string]any
		for _, id := range f.artists[p["artist"].(string)] {
			rows = append(rows, map[string]any{"id": id, "name": id})
		}
		return page(rows, p, "albums")
	case "queue.clear":
		f.playRequested, f.polls, f.rows = false, 0, 0
		f.calls = append(f.calls, "clear")
	case "queue":
		if f.playRequested && !f.playing() {
			f.queuedBeforePlaying++
		}
		f.rows++
		f.calls = append(f.calls, "queue:"+p["path"].(string))
	case "queue.enqueue":
		f.calls = append(f.calls, fmt.Sprintf("enq:%d", intOf(p["index"])))
	case "queue.play":
		f.playRequested = true
		f.calls = append(f.calls, fmt.Sprintf("play@%d", intOf(p["index"])))
	case "track.queue":
		f.queueN++
		if f.failAt > 0 && f.queueN == f.failAt {
			return nil, f.failErr
		}
		tr, _ := p["track"].(map[string]any)
		if f.playRequested && !f.playing() {
			f.queuedBeforePlaying++
		}
		f.calls = append(f.calls, "queue:"+tr["path"].(string))
	case "track.play":
		f.playRequested = true
		tr, _ := p["track"].(map[string]any)
		f.calls = append(f.calls, "play:"+tr["title"].(string)+"@"+tr["path"].(string))
	case "provider.load_album":
		f.calls = append(f.calls, "load_album:"+p["album"].(string))
	case "provider.load":
		f.calls = append(f.calls, "load:"+p["playlist"].(string))
	default:
		return nil, errors.New("unexpected op " + op)
	}
	return ok, nil
}

func (f *fakeCliamp) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type events struct {
	mu sync.Mutex
	ev []Event
}

func (e *events) emit(x Event) { e.mu.Lock(); e.ev = append(e.ev, x); e.mu.Unlock() }
func (e *events) all() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.ev...)
}
func (e *events) last() Event { a := e.all(); return a[len(a)-1] }

func newSvc(f *fakeCliamp, known func(string) bool) (*Service, *events, *time.Time) {
	ev := &events{}
	now := time.Unix(1000, 0)
	s := New(f, func(p string) string {
		if p == "https://nd/s?id=1" {
			return "https://nd/s?id=1&u=bob&t=TOKEN" // what the bridge remembers for a scrubbed URL
		}
		return p
	}, known, ev.emit, func() time.Time { return now })
	return s, ev, &now
}

func run(t *testing.T, s *Service, r Request) {
	t.Helper()
	if err := s.Start(r); err != nil {
		t.Fatalf("start: %v", err)
	}
	s.Wait()
}

func TestTrackPlayClearsThenPlays(t *testing.T) {
	f := &fakeCliamp{}
	s, ev, _ := newSvc(f, func(string) bool { return true })
	run(t, s, Request{Kind: "track", Mode: "play", Track: trk("One", "/m/one.flac")})
	if got := strings.Join(f.snapshot(), " "); got != "clear play:One@/m/one.flac" {
		t.Fatalf("calls: %s", got)
	}
	if l := ev.last(); l.State != "done" || l.Done != 1 || l.Total != 1 {
		t.Fatalf("final event %+v", l)
	}
}

// Add to queue keeps the whole track (title, artist, provider metadata): the
// bare-path append of the live playlist would lose all of it.
func TestTrackAppendKeepsTheTrackObject(t *testing.T) {
	f := &fakeCliamp{}
	s, _, _ := newSvc(f, func(string) bool { return true })
	run(t, s, Request{Kind: "track", Mode: "append", Track: trk("One", "/m/one.flac")})
	if got := strings.Join(f.snapshot(), " "); got != "queue:/m/one.flac" {
		t.Fatalf("calls: %s", got)
	}
}

func TestAlbumAndPlaylistPlayUseCliampsLoaders(t *testing.T) {
	f := &fakeCliamp{}
	s, _, _ := newSvc(f, nil)
	run(t, s, Request{Kind: "album", Mode: "play", Provider: "navidrome", ID: "al1"})
	run(t, s, Request{Kind: "playlist", Mode: "play", Provider: "navidrome", ID: "pl1"})
	if got := strings.Join(f.snapshot(), " "); got != "load_album:al1 load:pl1" {
		t.Fatalf("calls: %s (no listing, no per-track calls)", got)
	}
}

func TestAlbumAppendKeepsOrderAcrossPages(t *testing.T) {
	f := &fakeCliamp{albums: map[string][]map[string]any{"al1": tracks("t", 450)}}
	s, ev, _ := newSvc(f, nil)
	run(t, s, Request{Kind: "album", Mode: "append", Provider: "navidrome", ID: "al1"})
	got := f.snapshot()
	if len(got) != 450 || got[0] != "queue:/m/t0.flac" || got[449] != "queue:/m/t449.flac" || got[225] != "queue:/m/t225.flac" {
		t.Fatalf("%d calls, first %q last %q", len(got), got[0], got[len(got)-1])
	}
	if l := ev.last(); l.State != "done" || l.Done != 450 || l.Total != 450 {
		t.Fatalf("final %+v", l)
	}
}

func TestNoCapOnAHugePlaylist(t *testing.T) {
	f := &fakeCliamp{playlists: map[string][]map[string]any{"big": tracks("b", 12000)}}
	s, ev, _ := newSvc(f, nil)
	run(t, s, Request{Kind: "playlist", Mode: "append", Provider: "navidrome", ID: "big"})
	if n := len(f.snapshot()); n != 12000 {
		t.Fatalf("every track must be appended, got %d", n)
	}
	if l := ev.last(); l.Done != 12000 || l.Total != 12000 || l.State != "done" {
		t.Fatalf("%+v", l)
	}
}

func TestArtistAppendWalksAlbumsInOrder(t *testing.T) {
	f := &fakeCliamp{
		artists: map[string][]string{"ar": {"a1", "a2"}},
		albums:  map[string][]map[string]any{"a1": tracks("x", 3), "a2": tracks("y", 2)},
	}
	s, _, _ := newSvc(f, nil)
	run(t, s, Request{Kind: "artist", Mode: "append", Provider: "navidrome", ID: "ar"})
	want := "queue:/m/x0.flac queue:/m/x1.flac queue:/m/x2.flac queue:/m/y0.flac queue:/m/y1.flac"
	if got := strings.Join(f.snapshot(), " "); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestArtistPlayClearsPlaysFirstThenQueuesTheRest(t *testing.T) {
	f := &fakeCliamp{
		artists: map[string][]string{"ar": {"a1", "a2"}},
		albums:  map[string][]map[string]any{"a1": tracks("x", 2), "a2": tracks("y", 1)},
	}
	s, _, _ := newSvc(f, nil)
	run(t, s, Request{Kind: "artist", Mode: "play", Provider: "navidrome", ID: "ar"})
	want := "clear play:x0@/m/x0.flac queue:/m/x1.flac queue:/m/y0.flac"
	if got := strings.Join(f.snapshot(), " "); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestSecondStartIsBusyAndTheFirstIsUndisturbed(t *testing.T) {
	f := &fakeCliamp{albums: map[string][]map[string]any{"al1": tracks("t", 5)}, gate: make(chan struct{}, 5)}
	s, ev, _ := newSvc(f, nil)
	if err := s.Start(Request{Kind: "album", Mode: "append", Provider: "p", ID: "al1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(Request{Kind: "album", Mode: "append", Provider: "p", ID: "al1"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	for i := 0; i < 5; i++ {
		f.gate <- struct{}{}
	}
	s.Wait()
	if l := ev.last(); l.State != "done" || l.Done != 5 {
		t.Fatalf("%+v", l)
	}
	// and the service is free again
	if err := s.Start(Request{Kind: "album", Mode: "play", Provider: "p", ID: "al1"}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
}

func TestCancelStopsAfterTheCurrentItem(t *testing.T) {
	f := &fakeCliamp{albums: map[string][]map[string]any{"al1": tracks("t", 100)}}
	ev := &events{}
	now := time.Unix(1000, 0)
	var s *Service
	s = New(f, nil, nil, func(e Event) {
		ev.emit(e)
		if e.Done >= 10 && e.State == "running" {
			s.Cancel()
		}
	}, func() time.Time { now = now.Add(time.Second); return now })
	run(t, s, Request{Kind: "album", Mode: "append", Provider: "p", ID: "al1"})
	l := ev.last()
	if l.State != "cancelled" || l.Done < 10 || l.Done >= 100 || len(f.snapshot()) != l.Done {
		t.Fatalf("%+v with %d calls", l, len(f.snapshot()))
	}
}

func TestErrorMidwayEndsFailedAndFreesTheService(t *testing.T) {
	f := &fakeCliamp{albums: map[string][]map[string]any{"al1": tracks("t", 10)}, failAt: 4,
		failErr: &ipc.IPCError{Code: "internal_error", Detail: "navidrome: down"}}
	s, ev, _ := newSvc(f, func(string) bool { return true })
	run(t, s, Request{Kind: "album", Mode: "append", Provider: "p", ID: "al1"})
	l := ev.last()
	if l.State != "failed" || l.Done != 3 || !strings.Contains(l.Error, "navidrome: down") {
		t.Fatalf("%+v", l)
	}
	f.failAt = 0
	run(t, s, Request{Kind: "track", Mode: "append", Track: trk("x", "/m/x.flac")}) // not stuck busy
}

func TestEventsAreThrottledAndTheFinalOneAlwaysSent(t *testing.T) {
	f := &fakeCliamp{albums: map[string][]map[string]any{"al1": tracks("t", 450)}}
	s, ev, _ := newSvc(f, nil) // the clock never moves
	run(t, s, Request{Kind: "album", Mode: "append", Provider: "p", ID: "al1"})
	all := ev.all()
	if len(all) > 4 {
		t.Fatalf("a frozen clock allows few events, got %d", len(all))
	}
	if all[0].State != "running" || all[0].Total != 450 {
		t.Fatalf("first event carries the total: %+v", all[0])
	}
	if l := all[len(all)-1]; l.State != "done" || l.Done != 450 {
		t.Fatalf("final: %+v", l)
	}
}

func TestUntrustedTrackPathIsRefusedWithoutAnyCall(t *testing.T) {
	f := &fakeCliamp{}
	s, _, _ := newSvc(f, func(string) bool { return false })
	err := s.Start(Request{Kind: "track", Mode: "play", Track: trk("x", "/etc/passwd")})
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("want ErrUntrusted, got %v", err)
	}
	if len(f.snapshot()) != 0 {
		t.Fatalf("no cliamp call may happen: %v", f.snapshot())
	}
}

// Radio and podcast results are plain stream URLs with no login in them: they
// are never scrubbed or "known", yet they must be playable. Only file paths on
// this machine need the gate.
func TestPlainStreamURLsAreAllowedButFilePathsAreNot(t *testing.T) {
	f := &fakeCliamp{}
	s, _, _ := newSvc(f, func(string) bool { return false })
	for _, url := range []string{"https://radio.example/live.mp3", "http://stream.example:8000/aac"} {
		run(t, s, Request{Kind: "track", Mode: "append", Track: trk("Station", url)})
	}
	if got := strings.Join(f.snapshot(), " "); got != "queue:https://radio.example/live.mp3 queue:http://stream.example:8000/aac" {
		t.Fatalf("got %s", got)
	}
	for _, p := range []string{"/etc/passwd", "file:///etc/passwd", "../../x.flac", "relative/a.mp3", "ftp://host/a.mp3"} {
		if err := s.Start(Request{Kind: "track", Mode: "append", Track: trk("x", p)}); !errors.Is(err, ErrUntrusted) {
			t.Fatalf("%q: want ErrUntrusted, got %v", p, err)
		}
	}
}

func TestScrubbedPathIsRestoredBeforeTheCall(t *testing.T) {
	f := &fakeCliamp{}
	s, _, _ := newSvc(f, func(string) bool { return false }) // not "known", but the bridge remembers it
	run(t, s, Request{Kind: "track", Mode: "append", Track: trk("x", "https://nd/s?id=1")})
	if got := strings.Join(f.snapshot(), " "); got != "queue:https://nd/s?id=1&u=bob&t=TOKEN" {
		t.Fatalf("got %s", got)
	}
}

func TestInvalidRequests(t *testing.T) {
	s, _, _ := newSvc(&fakeCliamp{}, func(string) bool { return true })
	for name, r := range map[string]Request{
		"kind":          {Kind: "song", Mode: "play", Provider: "p", ID: "x"},
		"mode":          {Kind: "album", Mode: "shuffle", Provider: "p", ID: "x"},
		"no id":         {Kind: "album", Mode: "play", Provider: "p"},
		"no provider":   {Kind: "playlist", Mode: "append", ID: "x"},
		"no track":      {Kind: "track", Mode: "play"},
		"track no path": {Kind: "track", Mode: "play", Track: map[string]any{"title": "t"}},
	} {
		if err := s.Start(r); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestEventsNeverCarryCredentials(t *testing.T) {
	f := &fakeCliamp{albums: map[string][]map[string]any{"al1": tracks("t", 3)}, failAt: 2,
		failErr: &ipc.IPCError{Code: "internal_error", Detail: "GET https://nd.lan/rest/stream?id=1&u=bob&t=TOKEN&s=SALT failed"}}
	s, ev, _ := newSvc(f, nil)
	run(t, s, Request{Kind: "album", Mode: "append", Provider: "p", ID: "al1"})
	b, _ := json.Marshal(ev.all())
	for _, secret := range []string{"bob", "TOKEN", "SALT", "nd.lan"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("events leak %q: %s", secret, b)
		}
	}
}

// cliamp needs a few seconds to start a stream after track.play. While it is
// still "stopped", every track.queue is treated as an append that moves the
// current-track cursor, so the page flickers through every track and playback
// finally starts on the LAST one. The rest of an artist must wait until the
// first track is really playing.
func TestArtistPlayWaitsUntilPlayingBeforeQueuingTheRest(t *testing.T) {
	f := &fakeCliamp{
		artists:    map[string][]string{"ar": {"a1"}},
		albums:     map[string][]map[string]any{"a1": tracks("x", 6)},
		startPolls: 7,
	}
	s, ev, _ := newSvc(f, nil)
	s.pollEvery, s.playTimeout = time.Millisecond, 2*time.Second
	run(t, s, Request{Kind: "artist", Mode: "play", Provider: "p", ID: "ar"})
	if f.queuedBeforePlaying != 0 {
		t.Fatalf("%d tracks were queued while the player was still starting", f.queuedBeforePlaying)
	}
	if got := len(f.snapshot()); got != 1+1+5 { // clear, play, five queued
		t.Fatalf("calls: %v", f.snapshot())
	}
	if l := ev.last(); l.State != "done" || l.Done != 6 || l.Total != 6 {
		t.Fatalf("%+v", l)
	}
}

func TestWaitForPlaybackIsBoundedAndThenCarriesOn(t *testing.T) {
	f := &fakeCliamp{
		artists:    map[string][]string{"ar": {"a1"}},
		albums:     map[string][]map[string]any{"a1": tracks("x", 3)},
		startPolls: 1 << 30, // never starts
	}
	s, ev, _ := newSvc(f, nil)
	s.pollEvery, s.playTimeout = time.Millisecond, 30*time.Millisecond
	start := time.Now()
	run(t, s, Request{Kind: "artist", Mode: "play", Provider: "p", ID: "ar"})
	if time.Since(start) > 2*time.Second {
		t.Fatal("waiting for playback must be bounded")
	}
	if l := ev.last(); l.State != "done" || l.Done != 3 {
		t.Fatalf("the rest is still queued after the wait gives up: %+v", l)
	}
}

func TestCancelWhileWaitingForPlayback(t *testing.T) {
	f := &fakeCliamp{
		artists:    map[string][]string{"ar": {"a1"}},
		albums:     map[string][]map[string]any{"a1": tracks("x", 3)},
		startPolls: 1 << 30,
	}
	s, ev, _ := newSvc(f, nil)
	s.pollEvery, s.playTimeout = 5*time.Millisecond, time.Minute
	if err := s.Start(Request{Kind: "artist", Mode: "play", Provider: "p", ID: "ar"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	s.Cancel()
	s.Wait()
	if l := ev.last(); l.State != "cancelled" {
		t.Fatalf("%+v", l)
	}
}

// ---- local files and folders ----

type fakeFiles struct {
	paths   map[string][]string // rel -> absolute audio paths (a file maps to itself)
	dirs    map[string]bool
	err     map[string]error
	walkErr error
}

func (f *fakeFiles) Resolve(rel string) (string, bool, error) {
	if err := f.err[rel]; err != nil {
		return "", false, err
	}
	if _, ok := f.paths[rel]; !ok {
		return "", false, files.ErrNotFound
	}
	return "/music/" + rel, f.dirs[rel], nil
}

func (f *fakeFiles) Walk(_ context.Context, rel string) ([]string, error) {
	if f.walkErr != nil {
		return nil, f.walkErr
	}
	return f.paths[rel], nil
}

func localSvc(f *fakeCliamp, ff *fakeFiles) (*Service, *events, *time.Time) {
	s, ev, now := newSvc(f, nil)
	s.SetFiles(ff)
	s.pollEvery, s.playTimeout = time.Millisecond, 2*time.Second
	return s, ev, now
}

func album(n int) (rel string, ff *fakeFiles) {
	var ps []string
	for i := 0; i < n; i++ {
		ps = append(ps, fmt.Sprintf("/music/A/%02d.flac", i+1))
	}
	return "A", &fakeFiles{paths: map[string][]string{"A": ps, "A/01.flac": {"/music/A/01.flac"}, "empty": nil, "X": ps}, dirs: map[string]bool{"A": true, "empty": true, "X": true}}
}

func TestFileAddQueuesThenEnqueuesTheNewRow(t *testing.T) {
	f := &fakeCliamp{rows: 5}
	_, ff := album(3)
	s, ev, _ := localSvc(f, ff)
	run(t, s, Request{Kind: "file", Provider: "files", ID: "A/01.flac", Mode: "append"})
	if got := strings.Join(f.snapshot(), " "); got != "queue:/music/A/01.flac enq:5" {
		t.Fatalf("got %s", got)
	}
	if l := ev.last(); l.State != "done" || l.Done != 1 || l.Total != 1 {
		t.Fatalf("%+v", l)
	}
}

func TestFolderAddKeepsOrderAndEnqueuesEachRowByItsIndex(t *testing.T) {
	f := &fakeCliamp{rows: 5}
	rel, ff := album(40)
	s, ev, _ := localSvc(f, ff)
	run(t, s, Request{Kind: "folder", Provider: "files", ID: rel, Mode: "append"})
	calls := f.snapshot()
	if len(calls) != 80 {
		t.Fatalf("%d calls", len(calls))
	}
	for k := 0; k < 40; k++ {
		if calls[2*k] != fmt.Sprintf("queue:/music/A/%02d.flac", k+1) || calls[2*k+1] != fmt.Sprintf("enq:%d", 5+k) {
			t.Fatalf("at %d: %s %s", k, calls[2*k], calls[2*k+1])
		}
	}
	if l := ev.last(); l.State != "done" || l.Done != 40 || l.Total != 40 {
		t.Fatalf("%+v", l)
	}
}

func TestFolderPlayClearsPlaysFirstThenQueuesTheRest(t *testing.T) {
	f := &fakeCliamp{rows: 9, startPolls: 6}
	rel, ff := album(5)
	s, ev, _ := localSvc(f, ff)
	run(t, s, Request{Kind: "folder", Provider: "files", ID: rel, Mode: "play"})
	calls := f.snapshot()
	want := "clear queue:/music/A/01.flac play@0 queue:/music/A/02.flac queue:/music/A/03.flac queue:/music/A/04.flac queue:/music/A/05.flac"
	if strings.Join(calls, " ") != want {
		t.Fatalf("got %s want %s", strings.Join(calls, " "), want)
	}
	if f.queuedBeforePlaying != 0 {
		t.Fatalf("%d rows were queued while the first was still starting", f.queuedBeforePlaying)
	}
	if l := ev.last(); l.State != "done" || l.Done != 5 || l.Total != 5 {
		t.Fatalf("%+v", l)
	}
}

func TestFilePlayIsClearQueuePlay(t *testing.T) {
	f := &fakeCliamp{rows: 3}
	_, ff := album(2)
	s, _, _ := localSvc(f, ff)
	run(t, s, Request{Kind: "file", Provider: "files", ID: "A/01.flac", Mode: "play"})
	if got := strings.Join(f.snapshot(), " "); got != "clear queue:/music/A/01.flac play@0" {
		t.Fatalf("got %s", got)
	}
}

func TestEmptyFolderFinishesDoneZeroAndKeepsTheQueue(t *testing.T) {
	f := &fakeCliamp{rows: 3}
	_, ff := album(1)
	s, ev, _ := localSvc(f, ff)
	run(t, s, Request{Kind: "folder", Provider: "files", ID: "empty", Mode: "play"})
	if len(f.snapshot()) != 0 {
		t.Fatalf("an empty folder must not clear the queue: %v", f.snapshot())
	}
	if l := ev.last(); l.State != "done" || l.Total != 0 {
		t.Fatalf("%+v", l)
	}
}

func TestOutsideAndMissingPathsAreRefusedWithoutCalls(t *testing.T) {
	f := &fakeCliamp{}
	_, ff := album(1)
	ff.err = map[string]error{"../x": files.ErrOutside}
	s, _, _ := localSvc(f, ff)
	if err := s.Start(Request{Kind: "folder", Provider: "files", ID: "../x", Mode: "append"}); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("want ErrUntrusted, got %v", err)
	}
	if err := s.Start(Request{Kind: "folder", Provider: "files", ID: "nope", Mode: "append"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(f.snapshot()) != 0 {
		t.Fatalf("no cliamp call may happen: %v", f.snapshot())
	}
}

func TestKindMustMatchWhatThePathIs(t *testing.T) {
	_, ff := album(1)
	s, _, _ := localSvc(&fakeCliamp{}, ff)
	if err := s.Start(Request{Kind: "file", Provider: "files", ID: "A", Mode: "append"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a folder is not a file: %v", err)
	}
	if err := s.Start(Request{Kind: "folder", Provider: "files", ID: "A/01.flac", Mode: "append"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a file is not a folder: %v", err)
	}
}

func TestWalkErrorEndsFailedAndFreesTheService(t *testing.T) {
	f := &fakeCliamp{}
	rel, ff := album(3)
	ff.walkErr = errors.New("nfs: stale file handle")
	s, ev, _ := localSvc(f, ff)
	run(t, s, Request{Kind: "folder", Provider: "files", ID: rel, Mode: "append"})
	if l := ev.last(); l.State != "failed" {
		t.Fatalf("%+v", l)
	}
	ff.walkErr = nil
	run(t, s, Request{Kind: "folder", Provider: "files", ID: rel, Mode: "append"}) // not stuck busy
	if l := ev.last(); l.State != "done" || l.Done != 3 {
		t.Fatalf("%+v", l)
	}
}

func TestCancelStopsAFolderAdd(t *testing.T) {
	f := &fakeCliamp{}
	rel, ff := album(300)
	ev := &events{}
	now := time.Unix(1000, 0)
	var s *Service
	s = New(f, nil, nil, func(e Event) {
		ev.emit(e)
		if e.Done >= 10 && e.State == "running" {
			s.Cancel()
		}
	}, func() time.Time { now = now.Add(time.Second); return now })
	s.SetFiles(ff)
	run(t, s, Request{Kind: "folder", Provider: "files", ID: rel, Mode: "append"})
	if l := ev.last(); l.State != "cancelled" || l.Done < 10 || l.Done >= 300 {
		t.Fatalf("%+v", l)
	}
}

func TestFileKindsNeedAFilesService(t *testing.T) {
	s, _, _ := newSvc(&fakeCliamp{}, nil)
	if err := s.Start(Request{Kind: "folder", Provider: "files", ID: "A", Mode: "play"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}
