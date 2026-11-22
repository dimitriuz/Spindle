package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	pngpkg "image/png"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cliamp-web/internal/fakeplayer"
)

var testHTTP = &http.Client{Timeout: 2 * time.Second}

// startFakePlayer answers the V2 methods the bridge uses at startup with a
// fixed "playing" world: enough to prove the wiring end to end.
func startFakePlayer(t *testing.T) string {
	f, err := fakeplayer.Start(t.TempDir(), fakeplayer.Options{
		State: `{"state":"playing","revision":5,"playlist_revision":7}`,
		Track: `{"title":"Fake Song"}`,
		Caps:  `[{"name":"toggle"},{"name":"queue.list"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f.Path
}

// TestPlaylistSignalFollowsCacheSwap pins the contract the browser relies on:
// when an SSE "playlist" frame lands, GET /api/queue must already serve the
// new rows. The fake swaps its queue rows and only then pushes the playlist
// event, so broadcasting the signal before absorbing would let the test
// observe the old rows.
func TestPlaylistSignalFollowsCacheSwap(t *testing.T) {
	dir := t.TempDir()
	f, err := fakeplayer.Start(dir, fakeplayer.Options{
		State: `{"state":"playing","revision":5,"playlist_revision":7}`,
		Caps:  `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	sock := f.Path

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	go func() { _ = run(ctx, runConfig{Socket: sock, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()
	readStatus(t, dir)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// initial queue: Row A
	deadline := time.Now().Add(5 * time.Second)
	for {
		if q := getJSON(t, base+"/api/queue"); len(q["tracks"].([]any)) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial queue never arrived")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// open SSE before stirring, so the playlist frame is observed
	sseResp, err := testHTTP.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	frames := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(sseResp.Body)
		for sc.Scan() {
			frames <- sc.Text()
		}
	}()

	if r := postCommand(t, base, `{"op":"queue.clear"}`); r["job"] == nil {
		t.Fatalf("queue.clear rejected: %v", r)
	}

	sawPlaylist := false
	deadline = time.Now().Add(5 * time.Second)
	for !sawPlaylist && time.Now().Before(deadline) {
		select {
		case line := <-frames:
			if line == "event: playlist" {
				sawPlaylist = true
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
	if !sawPlaylist {
		t.Fatal("no playlist frame on SSE")
	}

	// the signal just landed: the cache must already hold Row B
	q := getJSON(t, base+"/api/queue")
	tracks, _ := q["tracks"].([]any)
	if len(tracks) != 1 {
		t.Fatalf("queue lost rows after signal: %v", q)
	}
	if row, _ := tracks[0].(map[string]any); row["title"] != "Row B" {
		t.Fatalf("playlist signal outran the queue cache: %v", row)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func readStatus(t *testing.T, dir string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(dir, "status.json"))
		if err == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				return m
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("status.json never appeared")
	return nil
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := testHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunWiresFakePlayer(t *testing.T) {
	sock := startFakePlayer(t)
	dir := t.TempDir()
	port := freePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, runConfig{Socket: sock, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()

	st := readStatus(t, dir)
	if _, has := st["token"]; has {
		t.Fatalf("status.json must not carry a token any more: %v", st)
	}
	if p, _ := st["port"].(float64); int(p) != port {
		t.Fatalf("status port %v != %d", st["port"], port)
	}
	if fi, err := os.Stat(filepath.Join(dir, "status.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("status.json mode: %v %v", fi, err)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// state prefetched through subscriber retention or state.get
	var snap map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap = getJSON(t, base+"/api/state")
		if snap["state"] == "playing" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap["state"] != "playing" {
		t.Fatalf("state never prefetched: %v", snap)
	}

	q := getJSON(t, base+"/api/queue")
	tracks, _ := q["tracks"].([]any)
	if len(tracks) != 1 {
		t.Fatalf("queue never prefetched: %v", q)
	}

	caps := getJSON(t, base+"/api/capabilities")
	if ops, _ := caps["result"].([]any); len(ops) != 2 {
		t.Fatalf("capabilities envelope not passed through: %v", caps)
	}

	// command path reaches the fake player
	resp := postCommand(t, base, `{"op":"toggle"}`)
	if resp["job"] == nil {
		t.Fatalf("command got no job: %v", resp)
	}

	// health reports the player online
	h := getJSONNoAuth(t, base+"/api/health")
	if h["player"] != "online" {
		t.Fatalf("health: %v", h)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not exit after ctx cancel")
	}
}

func postCommand(t *testing.T, base, body string) map[string]any {
	t.Helper()
	r, err := http.NewRequest("POST", base+"/api/command", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := testHTTP.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func getJSONNoAuth(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func TestRunServesLibraryAndSearch(t *testing.T) {
	f, err := fakeplayer.Start(t.TempDir(), fakeplayer.Options{
		State: `{"state":"playing","revision":5,"playlist_revision":7}`,
		Track: `{"title":"Fake Song"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	port := freePort(t)
	go func() { _ = run(ctx, runConfig{Socket: f.Path, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()
	readStatus(t, dir)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	deadline := time.Now().Add(5 * time.Second)
	for {
		r := getJSON(t, base+"/api/library/providers")
		if r["ok"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("providers never came up: %v", r)
		}
		time.Sleep(50 * time.Millisecond)
	}
	r := getJSON(t, base+"/api/library/navidrome/playlists?offset=0&limit=50")
	if total, _ := r["total"].(float64); total != 120 {
		t.Fatalf("want 120 playlists, got %v", r["total"])
	}
	s := getJSON(t, base+"/api/search/ytmusic?q=lofi")
	tracks, _ := s["tracks"].([]any)
	if len(tracks) != 3 || tracks[0].(map[string]any)["stream"] != true {
		t.Fatalf("search passthrough: %v", s)
	}
}

func TestRunServesArtForLocalTrack(t *testing.T) {
	music := t.TempDir()
	track := filepath.Join(music, "Artist", "Album", "01.flac")
	if err := os.MkdirAll(filepath.Dir(track), 0o755); err != nil {
		t.Fatal(err)
	}
	var png bytes.Buffer
	_ = pngpkg.Encode(&png, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	for name, data := range map[string][]byte{"01.flac": []byte("x"), "cover.png": png.Bytes()} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(track), name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := fakeplayer.Start(t.TempDir(), fakeplayer.Options{
		State: `{"state":"playing","revision":5,"playlist_revision":7}`,
		Track: `{"title":"Local","path":"` + track + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	port := freePort(t)
	go func() { _ = run(ctx, runConfig{Socket: f.Path, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()
	readStatus(t, dir)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err = testHTTP.Get(base + "/api/art/cover?path=" + url.QueryEscape(track))
		if err == nil && resp.StatusCode == 200 {
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("cover never served: %v %v", err, resp)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type %q", ct)
	}
	// a file the player never reported is not readable through the route
	other := filepath.Join(music, "secret.flac")
	_ = os.WriteFile(other, []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(music, "cover.png"), png.Bytes(), 0o644)
	r2, err := testHTTP.Get(base + "/api/art/cover?path=" + url.QueryEscape(other))
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 404 {
		t.Fatalf("unknown track: %d", r2.StatusCode)
	}
}

func TestRunServesTrackInfoForLocalFLAC(t *testing.T) {
	music := t.TempDir()
	track := filepath.Join(music, "01.flac")
	// "fLaC" + STREAMINFO block header + 34 bytes: 96 kHz, 2 ch, 24 bit
	hdr := append([]byte("fLaC\x80\x00\x00\x22"), make([]byte, 34)...)
	v := uint64(96000)<<44 | uint64(1)<<41 | uint64(23)<<36
	for i := 0; i < 8; i++ {
		hdr[8+10+i] = byte(v >> (56 - 8*uint(i)))
	}
	if err := os.WriteFile(track, hdr, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := fakeplayer.Start(t.TempDir(), fakeplayer.Options{
		State: `{"state":"playing","revision":5,"playlist_revision":7}`,
		Track: `{"title":"Local","path":"` + track + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	port := freePort(t)
	go func() { _ = run(ctx, runConfig{Socket: f.Path, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()
	readStatus(t, dir)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := testHTTP.Get(base + "/api/trackinfo?path=" + url.QueryEscape(track))
		if err == nil && resp.StatusCode == 200 {
			var got map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&got)
			resp.Body.Close()
			if got["format"] != "FLAC" || got["sample_rate"] != float64(96000) || got["bit_depth"] != float64(24) {
				t.Fatalf("info: %v", got)
			}
			return
		}
		if err == nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("never served: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func putJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("PUT", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := testHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestRunFavoritesRoundTrip(t *testing.T) {
	f, err := fakeplayer.Start(t.TempDir(), fakeplayer.Options{State: `{"state":"playing","revision":5,"playlist_revision":7}`})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	port := freePort(t)
	go func() { _ = run(ctx, runConfig{Socket: f.Path, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()
	readStatus(t, dir)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	track := `{"kind":"track","favorite":true,"track":{"title":"Song","artist":"Band","path":"https://nd.lan/rest/stream?id=s1","provider_meta":{"navidrome.id":"s1"}}}`
	for name, body := range map[string]string{
		"track": track,
		"radio": `{"kind":"radio","provider":"radio","id":"c:2","favorite":true}`,
		"album": `{"kind":"album","provider":"navidrome","id":"al1","title":"Album","subtitle":"Band","favorite":true}`,
	} {
		code, m := putJSON(t, base+"/api/favorites", body)
		if code != 200 || m["favorite"] != true {
			t.Fatalf("%s: %d %v", name, code, m)
		}
	}
	got := getJSON(t, base+"/api/favorites")
	items, _ := got["items"].([]any)
	kinds := map[string]bool{}
	for _, it := range items {
		kinds[it.(map[string]any)["kind"].(string)] = true
	}
	if len(items) != 3 || !kinds["track"] || !kinds["radio"] || !kinds["album"] {
		t.Fatalf("list: %v", got)
	}
	// toggling off through the API removes it from every home
	code, m := putJSON(t, base+"/api/favorites", strings.Replace(track, `"favorite":true`, `"favorite":false`, 1))
	if code != 200 || m["favorite"] != false {
		t.Fatalf("off: %d %v", code, m)
	}
	// the album survives a bridge restart: it is in the file
	if b, err := os.ReadFile(filepath.Join(dir, "favorites.json")); err != nil || !strings.Contains(string(b), `"al1"`) {
		t.Fatalf("favorites.json: %v %s", err, b)
	}
}
