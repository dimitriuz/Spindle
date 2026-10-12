# cliamp-plugin-web v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add provider browse/search (play now / queue next / load playlist / play album) and a dedicated Now Playing page with a tabbed shell (Now Playing · Queue · Browse, opens on Now Playing) to the existing LAN remote.

**Architecture:** One new Go handler file (`internal/server/library.go`) wraps the player's async provider jobs as token-guarded, TTL-cached GETs on the existing mux — no new IPC connection, no SSE change. Play actions keep flowing through the existing `POST /api/command`. The Vue app gains a view-switching shell (`view` ref, no router), a `NowPlayingHero` view, and a `BrowseView` with provider chips, infinite scroll, and a drill stack. The test fake player moves from `wiring_test.go` into `internal/fakeplayer` and grows the provider ops so Go tests and browser smokes share one wire-accurate fake.

**Tech Stack:** Go 1.23+ (module `cliamp-web`, stdlib only), Vue 3 + Vite + TypeScript + Reka UI (committed `webui/dist` embed artifact).

**Spec:** `docs/superpowers/specs/2026-10-12-cliamp-web-v2-design.md` (read it; §2 is the live-probed wire table this plan argues from)

## Global Constraints

- Default port 8730; bind `127.0.0.1`, `--lan` ⇒ `0.0.0.0`; token 32-byte hex at `<data-dir>/token` `0600`; queue cap 5000 + `truncated`; SSE heartbeat 15 s; rate limit 20/s burst 40 on `/api/command`; IPC V2 only; NDJSON frames ≤ 1 MiB. (unchanged from v1)
- Every `/api/*` except `/api/health` stays behind `protect` (HostGuard + token); POST requires `X-Token`, query token allowed for GET — new library GETs inherit this, they are data-only.
- `net.JoinHostPort(host, strconv.Itoa(port))` — never `fmt.Sprintf("%s:%d", …)`; `for i := range n` idioms; gofmt + `go vet` clean.
- TS: no one-line wrapper functions unless multi-call-site; static string maps via `Record`, dynamic membership via `Set`; `vue-tsc` strict must stay green.
- **`provider` (and `playlist`, `album`, `id`, `query`, `track`) always ride inside `params`**, never the envelope (spec §2 — envelope placement fails `unknown provider ""`).
- Library GETs forward the player's `result` **verbatim** — no renaming, no aggregation.
- Queue-replacement actions (`provider.load`, `provider.load_album`) require `window.confirm` in the UI before submitting.
- The bridge never contacts music providers; all library data flows through `cliamp.sock`.
- `webui/dist` is a committed embed artifact: after any UI change run `cd webui && npm run build`, then `make build`, and commit `dist`.

## Review Focus

Failure modes no single task's tests fully cover — pin these where the named task owns the code:

1. **Provider unreachable mid-browse** (Navidrome down): library GET returns 502 with the upstream `detail` string; the Browse view shows an inline retry banner and KEEPS the last loaded list instead of blanking it (Task 4, Task 8).
2. **`if_revision` conflict between browsing and tapping a track row**: `track.play`/`track.queue` hit 409 → the existing `runCommand` 409 path must fire (toast + queue refetch) — browser-verified, not mocked (Task 9).
3. **Empty results in both observed shapes** (`{"ok":true,"total":0,"tracks":[]}` and ok-only with the array absent): UI renders "no results", never crashes on `undefined` (Task 4 pins the Go side, Task 8 the UI side).
4. **Provider job that never terminates** (slow internet): 504 `job_timeout` at `LibraryTimeout` (injectable), UI shows retry on that list only (Task 4).
5. **Player offline mid-browse**: 502 + `detail: "player offline"`, global offline banner covers the view, in-flight lists keep their rows (Task 4, Task 8).
6. **End of list**: the infinite-scroll sentinel must stop firing once `offset ≥ total` — no request storm past the last page (Task 8).

---

### Task 1: Extract the fake player into `internal/fakeplayer`

The fake lives inline twice in `cmd/cliamp-web/wiring_test.go`; v2 grows it (providers) and reuses it for browser smokes, so it becomes a package first. Pure move + parameterize — behavior identical.

**Files:**
- Create: `internal/fakeplayer/fakeplayer.go`
- Modify: `cmd/cliamp-web/wiring_test.go` (replace `startFakePlayer` and the inline swap fake with the package)

**Interfaces:**
- Consumes: nothing (leaf).
- Produces: `fakeplayer.Start(dir string, opts fakeplayer.Options) (*fakeplayer.Fake, error)`; `Fake.Path string`; `Fake.Close()`; `Fake.Push(eventJSON string)` (writes to the current subscriber conn); `Fake.SetRow(title string)` (queue.list row title, live); `Options{State, Caps, Track string}` — raw JSON fragments with defaults (see code).

- [ ] **Step 1: Write the package**

Create `internal/fakeplayer/fakeplayer.go`:

```go
// Package fakeplayer is a wire-accurate stand-in for a running cliamp: it
// answers the V2 methods the bridge uses so Go tests and browser smokes run
// without a real player. Shapes mirror live cliamp v2.0.1 probes:
// operation/job_id ride the envelope, empty list results are {"ok":true}.
package fakeplayer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

type Options struct {
	// State is the raw snapshot JSON for state.get.
	State string
	// Caps is the raw capabilities result array JSON.
	Caps string
	// Track is the raw track JSON embedded in the snapshot, "" for none.
	Track string
}

type Fake struct {
	Path string

	mu       sync.Mutex
	ln       net.Listener
	rowTitle string
	subConn  net.Conn
}

func Start(dir string, opts Options) (*Fake, error) {
	if opts.State == "" {
		opts.State = `{"state":"playing","revision":5,"playlist_revision":7}`
	}
	if opts.Caps == "" {
		opts.Caps = `[{"name":"toggle"},{"name":"queue.list"}]`
	}
	f := &Fake{rowTitle: "Row A"}
	path := dir + "/s"
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	f.Path = path
	f.ln = ln
	go f.serve(opts)
	return f, nil
}

func (f *Fake) Close() { f.ln.Close() }

func (f *Fake) SetRow(title string) {
	f.mu.Lock()
	f.rowTitle = title
	f.mu.Unlock()
}

func (f *Fake) row() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rowTitle
}

// Push sends one raw event line to the most recent subscriber.
func (f *Fake) Push(eventJSON string) {
	f.mu.Lock()
	c := f.subConn
	f.mu.Unlock()
	if c != nil {
		fmt.Fprintln(c, eventJSON)
	}
}

func (f *Fake) serve(opts Options) {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c, opts)
	}
}

func (f *Fake) handle(c net.Conn, opts Options) {
	defer c.Close()
	sc := bufio.NewScanner(bufio.NewReader(c))
	for sc.Scan() {
		var req map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		var id, method, op string
		_ = json.Unmarshal(req["id"], &id)
		_ = json.Unmarshal(req["method"], &method)
		_ = json.Unmarshal(req["operation"], &op)
		switch method {
		case "subscribe":
			f.mu.Lock()
			f.subConn = c
			f.mu.Unlock()
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true}\n", id)
			snap := opts.State
			if opts.Track != "" {
				snap = trimBrace(snap) + `,"track":` + opts.Track + "}"
			}
			fmt.Fprintf(c, "{\"event\":\"runtime.state\",\"seq\":1,\"retained\":true,\"data\":%s}\n", snap)
		case "state.get":
			snap := opts.State
			if opts.Track != "" {
				snap = trimBrace(snap) + `,"track":` + opts.Track + "}"
			}
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"snapshot\":%s}\n", id, snap)
		case "capabilities":
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"result\":%s}\n", id, opts.Caps)
		case "operation.submit":
			// real cliamp validates the envelope: "operation" must be
			// top-level, a nested one is invalid_request.
			if op == "" {
				fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":false,\"error\":{\"code\":\"invalid_request\",\"message\":\"invalid request\"}}\n", id)
				break
			}
			f.submit(c, id, op, req["params"])
		default:
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true}\n", id)
		}
	}
}

func trimBrace(s string) string {
	if len(s) > 0 && s[len(s)-1] == '}' {
		return s[:len(s)-1]
	}
	return s
}
```

`Fake.submit` (append to the same file — the op switch; provider ops come in Task 2):

```go
func (f *Fake) submit(c net.Conn, id, op string, params json.RawMessage) {
	switch op {
	case "queue.list":
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j1\",\"operation\":\"queue.list\",\"state\":\"succeeded\",\"result\":{\"ok\":true,\"tracks\":[{\"title\":%q}]}}}\n", id, f.row())
	case "playnext.list":
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j2\",\"operation\":\"playnext.list\",\"state\":\"succeeded\",\"result\":{\"ok\":true}}}\n", id)
	case "make-change":
		// rows change BEFORE the event exists on the wire
		f.SetRow("Row B")
		f.Push(`{"event":"runtime.playlist","seq":9,"data":{"revision":8,"playlist_revision":8}}`)
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j3\",\"operation\":\"make-change\",\"state\":\"succeeded\",\"result\":{\"ok\":true}}}\n", id)
	default:
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j9\",\"operation\":%q,\"state\":\"succeeded\",\"result\":{\"ok\":true}}}\n", id, op)
	}
}
```

- [ ] **Step 2: Rewire `wiring_test.go` to the package**

Delete the bodies of `startFakePlayer` and the inline listener inside `TestPlaylistSignalFollowsCacheSwap`. Replace `startFakePlayer(t)` with:

```go
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
```

In `TestPlaylistSignalFollowsCacheSwap`, replace the hand-rolled listener with:

```go
	f, err := fakeplayer.Start(dir, fakeplayer.Options{
		Caps: `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	sock := f.Path
```

and drop the now-unused imports (`bufio` stays if the SSE scanner still needs it; `sync` may go). The `make-change` op and `Row A`/`Row B` assertions stay exactly as they are — the package reproduces them.

- [ ] **Step 3: Run the suite**

Run: `go test ./... -race -count=1`
Expected: PASS, same test names as before, none deleted.

- [ ] **Step 4: Commit**

```bash
git add internal/fakeplayer cmd/cliamp-web/wiring_test.go
git commit -m "refactor: fake player moves to internal/fakeplayer, shared by tests"
```

---

### Task 2: Fake player gains provider ops (wire shapes from spec §2)

**Files:**
- Create: `internal/fakeplayer/providers.go`
- Modify: `internal/fakeplayer/fakeplayer.go` (`submit` gains a `default:` fallthrough to provider ops)
- Test: `internal/fakeplayer/providers_test.go`

**Interfaces:**
- Consumes: `Fake.submit`.
- Produces: `operation.submit` answers for `provider.list|playlists|albums|artists|catalog|album_tracks|tracks|search` with player-shaped results (envelope job object, `result` per spec §2 table), plus failed jobs with `{"code":"internal_error","detail":"unknown provider \"\""}` / `detail` containing `missing parameter: 'id'`. Fixtures (deterministic, exported for browser smoke reuse): providers `navidrome` (searchable, browse_albums, browse_artists), `radio` (searchable, catalog), `local` (searchable); 120 playlists, 3 albums, 2 artists, 7 album tracks, 4 artist tracks, radio catalog 4 entries; search returns 3 tracks for any non-empty query, empty array for `query:"zzzznone"`.

- [ ] **Step 1: Write the failing test**

Create `internal/fakeplayer/providers_test.go` — drives the fake through the real client so wire shapes are pinned end-to-end:

```go
package fakeplayer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cliamp-web/internal/ipc"
)

func startProviders(t *testing.T) *ipc.Conn {
	f, err := Start(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return ipc.NewConn(f.Path)
}

func call(t *testing.T, c *ipc.Conn, op string, params map[string]any) json.RawMessage {
	t.Helper()
	job, err := c.SubmitJob(context.Background(), op, params)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return job.Result
}

func TestProviderListShape(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.list", nil)
	var r struct {
		OK        bool `json:"ok"`
		Providers []struct {
			Key          string `json:"key"`
			Name         string `json:"name"`
			Searchable   bool   `json:"searchable"`
			Catalog      bool   `json:"catalog"`
			BrowseAlbums bool   `json:"browse_albums"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || !r.OK || len(r.Providers) != 3 {
		t.Fatalf("bad providers: %s", raw)
	}
}

func TestProviderPagination(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.playlists", map[string]any{"provider": "navidrome", "offset": 100, "limit": 50})
	var r struct {
		Total     int `json:"total"`
		Playlists []struct {
			Name string `json:"name"`
		} `json:"playlists"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Total != 120 || len(r.Playlists) != 20 {
		t.Fatalf("offset 100 limit 50 of 120 should yield 20 rows: %s", raw)
	}
	if r.Playlists[0].Name != "Playlist 100" {
		t.Fatalf("offset ignored: %s", raw)
	}
}

func TestUnknownProviderFailsLikeLive(t *testing.T) {
	c := startProviders(t)
	_, err := c.SubmitJob(context.Background(), "provider.playlists", map[string]any{"provider": "bogus"})
	if err == nil || !strings.Contains(err.Error(), `unknown provider ""`) {
		t.Fatalf("want internal_error unknown provider, got %v", err)
	}
}

func TestMissingIDParamFailsLikeLive(t *testing.T) {
	c := startProviders(t)
	_, err := c.SubmitJob(context.Background(), "provider.tracks", map[string]any{"provider": "navidrome"})
	if err == nil || !strings.Contains(err.Error(), "missing parameter: 'id'") {
		t.Fatalf("want missing id error, got %v", err)
	}
}

func TestSearchEmptyAndNonEmpty(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.search", map[string]any{"provider": "ytmusic", "query": "lofi", "limit": 2})
	if !strings.Contains(string(raw), `"total":3`) || !strings.Contains(string(raw), `"duration_secs"`) {
		t.Fatalf("search shape: %s", raw)
	}
	raw = call(t, c, "provider.search", map[string]any{"provider": "ytmusic", "query": "zzzznone"})
	var r struct {
		Total  int           `json:"total"`
		Tracks []interface{} `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Total != 0 || r.Tracks == nil || len(r.Tracks) != 0 {
		t.Fatalf("empty search must be total:0 with an empty array: %s", raw)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/fakeplayer -run TestProvider -count=1`
Expected: FAIL — `provider.list` returns the generic `{"ok":true}` job (no `providers` key).

- [ ] **Step 3: Implement `internal/fakeplayer/providers.go`**

```go
package fakeplayer

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Provider fixtures mirror the live shapes probed against cliamp v2.0.1
// (spec §2). Deterministic; the browser smoke reuses these via this package.

var providerFixtures = []map[string]any{
	{"key": "navidrome", "name": "Navidrome", "searchable": true, "browse_albums": true, "browse_artists": true},
	{"key": "radio", "name": "Radio", "searchable": true, "catalog": true},
	{"key": "local", "name": "Local", "searchable": true},
}

func fixturePlaylists() []map[string]any {
	out := make([]map[string]any, 0, 120)
	for i := range 120 {
		out = append(out, map[string]any{
			"id": "pl" + strconv.Itoa(i), "name": "Playlist " + strconv.Itoa(i),
			"provider": "navidrome", "track_count": 10 + i%40,
		})
	}
	return out
}

func fixtureAlbums() []map[string]any {
	return []map[string]any{
		{"id": "al1", "name": "Album One", "artist": "Artist A", "artist_id": "ar1", "year": 1999, "track_count": 7, "genre": "Rock"},
		{"id": "al2", "name": "Album Two", "artist": "Artist A", "artist_id": "ar1", "year": 2003, "track_count": 2, "genre": "Rock"},
		{"id": "al3", "name": "Album Three", "artist": "Artist B", "artist_id": "ar2", "year": 2010, "track_count": 1, "genre": "Jazz"},
	}
}

func fixtureArtists() []map[string]any {
	return []map[string]any{
		{"id": "ar1", "name": "Artist A", "album_count": 2},
		{"id": "ar2", "name": "Artist B", "album_count": 1},
	}
}

func fixtureCatalog() []map[string]any {
	return []map[string]any{
		{"id": "c:0", "name": "Station Zero", "provider": "radio", "favoritable": true},
		{"id": "c:1", "name": "Station One", "provider": "radio"},
		{"id": "l:0", "name": "fake radio", "provider": "radio"},
		{"id": "loc:ask", "name": "Use my location", "provider": "radio"},
	}
}

func fixtureTracks(kind, id string) []map[string]any {
	switch kind + ":" + id {
	case "album:al1":
		out := make([]map[string]any, 0, 7)
		for i := range 7 {
			out = append(out, track(fmt.Sprintf("Album One Track %d", i+1), "Artist A", "/fake/a1-"+strconv.Itoa(i)+".flac", 180+i))
		}
		return out
	case "album:al2":
		return []map[string]any{
			track("Album Two Track 1", "Artist A", "/fake/a2-0.flac", 200),
			track("Album Two Track 2", "Artist A", "/fake/a2-1.flac", 210),
		}
	case "artist:ar1":
		return []map[string]any{
			track("Album One Track 1", "Artist A", "/fake/a1-0.flac", 180),
			track("Album One Track 2", "Artist A", "/fake/a1-1.flac", 181),
			track("Album Two Track 1", "Artist A", "/fake/a2-0.flac", 200),
			track("Best Of", "Artist A", "/fake/best.flac", 100),
		}
	case "artist:ar2":
		return []map[string]any{track("Album Three Track 1", "Artist B", "/fake/a3-0.flac", 300)}
	}
	return nil
}

func track(title, artist, path string, dur int) map[string]any {
	return map[string]any{"title": title, "artist": artist, "path": path, "duration_secs": dur, "stream": false}
}

func fixtureSearch(query string) []map[string]any {
	if query == "zzzznone" {
		return []map[string]any{}
	}
	return []map[string]any{
		map[string]any{"title": query + " mix one", "artist": "Someone", "path": "https://example.invalid/one", "duration_secs": 240, "stream": true},
		map[string]any{"title": query + " mix two", "artist": "Someone", "path": "https://example.invalid/two", "duration_secs": 300, "stream": true},
		map[string]any{"title": query + " radio", "artist": "Stream FM", "path": "https://example.invalid/radio.m3u8", "duration_secs": 0, "stream": true},
	}
}

// providerOps answers provider.* exactly like the live player: params carry
// "provider"/"album"/"id"/"query"; unknown provider and missing id fail as
// internal_error jobs with the probed detail strings.
func (f *Fake) providerOps(c jsonWriter, id, op string, params json.RawMessage) bool {
	if !strings.HasPrefix(op, "provider.") {
		return false
	}
	var p struct {
		Provider string `json:"provider"`
		Album    string `json:"album"`
		ID       string `json:"id"`
		Query    string `json:"query"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Provider != "navidrome" && p.Provider != "radio" && p.Provider != "local" {
		failJob(c, id, op, `unknown provider "`+p.Provider+`"`)
		return true
	}
	ok := func(result string) { c(fmt.Sprintf("{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"jx\",\"operation\":%q,\"state\":\"succeeded\",\"result\":%s}}\n", id, op, result)) }
	switch op {
	case "provider.list":
		b, _ := json.Marshal(map[string]any{"ok": true, "providers": providerFixtures})
		ok(string(b))
	case "provider.playlists":
		b, _ := json.Marshal(paged(fixturePlaylists(), p.Offset, p.Limit, "playlists"))
		ok(string(b))
	case "provider.albums":
		b, _ := json.Marshal(paged(fixtureAlbums(), p.Offset, p.Limit, "albums"))
		ok(string(b))
	case "provider.artists":
		b, _ := json.Marshal(paged(fixtureArtists(), p.Offset, p.Limit, "artists"))
		ok(string(b))
	case "provider.catalog":
		b, _ := json.Marshal(paged(fixtureCatalog(), p.Offset, p.Limit, "playlists"))
		ok(string(b))
	case "provider.album_tracks":
		if p.Album == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'album' (code 10)")
			return true
		}
		b, _ := json.Marshal(paged(fixtureTracks("album", p.Album), p.Offset, p.Limit, "tracks"))
		ok(string(b))
	case "provider.tracks":
		if p.ID == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'id' (code 10)")
			return true
		}
		b, _ := json.Marshal(paged(fixtureTracks("artist", p.ID), p.Offset, p.Limit, "tracks"))
		ok(string(b))
	case "provider.search":
		b, _ := json.Marshal(paged(fixtureSearch(p.Query), p.Offset, p.Limit, "tracks"))
		ok(string(b))
	default:
		return false
	}
	return true
}

type jsonWriter func(format string, args ...any)

func paged(rows []map[string]any, offset, limit int, field string) map[string]any {
	if limit <= 0 {
		limit = 50
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return map[string]any{"ok": true, "total": len(rows), field: rows[offset:end]}
}

func failJob(c jsonWriter, id, op, detail string) {
	b, _ := json.Marshal(map[string]any{
		"version": 2, "id": id, "ok": true,
		"job": map[string]any{"id": "jf", "operation": op, "state": "failed",
			"error": map[string]any{"code": "internal_error", "detail": detail}},
	})
	c(string(b) + "\n")
}
```

Adapt `Fake.submit` to accept the writer: change its signature to `func (f *Fake) submit(w io.Writer, id, op string, params json.RawMessage)` is NOT needed — instead insert at the top of `submit`'s `default:` branch in `fakeplayer.go`:

```go
	default:
		if f.providerOps(writerFunc(func(s string) { fmt.Fprint(c, s) }), id, op, req["params"]) {
			return
		}
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j9\",\"operation\":%q,\"state\":\"succeeded\",\"result\":{\"ok\":true}}}\n", id, op)
```

with, in `fakeplayer.go`:

```go
type writerFunc func(s string)
func (w writerFunc) call(s string) { w(s) }
```

— then use `c(fmt.Sprintf(...))` consistently inside `providers.go` and `fmt.Fprint(c, s)` here, unifying to the `jsonWriter` type: define `jsonWriter` in `fakeplayer.go` as `type jsonWriter interface{ call(string) }`… **Simplification the implementer must apply**: drop the interface entirely — change `providers.go`'s `providerOps` to take `c net.Conn` directly and use `fmt.Fprintf(c, ...)`, and `failJob(c net.Conn, ...)`, and delete `type jsonWriter`. The plan's snippets above use `c(string)` only for compactness; final code uses `net.Conn` + `fmt.Fprint`. (This paragraph is a deliberate instruction, not a placeholder: converge on `net.Conn`.)

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./... -race -count=1`
Expected: PASS (new provider tests + everything from Task 1).

- [ ] **Step 5: Commit**

```bash
git add internal/fakeplayer
git commit -m "feat: fake player answers provider ops with live-probed shapes"
```

---

### Task 3: TTL cache primitive

**Files:**
- Create: `internal/server/ttlcache.go`
- Test: `internal/server/ttlcache_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `newTTLCache(ttl time.Duration, maxEntries int, now func() time.Time) *ttlcache` with `get(key string) (json.RawMessage, bool)` and `put(key string, body json.RawMessage)`. TTL 0 disables caching (every `get` misses).

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTTLCacheHitThenExpire(t *testing.T) {
	now := time.Now()
	c := newTTLCache(60*time.Second, 128, func() time.Time { return now })
	c.put("k", json.RawMessage(`{"n":1}`))
	if _, ok := c.get("k"); !ok {
		t.Fatal("immediate get must hit")
	}
	now = now.Add(59 * time.Second)
	if _, ok := c.get("k"); !ok {
		t.Fatal("within TTL must hit")
	}
	now = now.Add(2 * time.Second)
	if v, ok := c.get("k"); ok || v != nil {
		t.Fatalf("past TTL must miss clean, got %s %v", v, ok)
	}
}

func TestTTLCacheZeroTTLNeverCaches(t *testing.T) {
	c := newTTLCache(0, 128, time.Now)
	c.put("k", json.RawMessage(`{}`))
	if _, ok := c.get("k"); ok {
		t.Fatal("ttl 0 must never hit")
	}
}

func TestTTLCacheEvictsOldestAtCap(t *testing.T) {
	now := time.Now()
	c := newTTLCache(time.Minute, 2, func() time.Time { return now })
	c.put("a", json.RawMessage(`1`))
	c.put("b", json.RawMessage(`2`))
	c.put("c", json.RawMessage(`3`)) // evicts "a" (FIFO)
	if _, ok := c.get("a"); ok {
		t.Fatal("a should have been evicted")
	}
	if _, ok := c.get("c"); !ok {
		t.Fatal("c must be present")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/server -run TestTTLCache` → FAIL (undefined: newTTLCache).

- [ ] **Step 3: Implement `internal/server/ttlcache.go`**

```go
package server

import (
	"encoding/json"
	"sync"
	"time"
)

// ttlcache is the library listing cache: TTL-bounded (no invalidation events
// exist in the player), FIFO-capped so a paging crawl cannot grow it without
// bound. Keys are full request paths; values are raw upstream result JSON.
type ttlcache struct {
	ttl     time.Duration
	max     int
	now     func() time.Time
	mu      sync.Mutex
	values  map[string]cacheEntry
	order   []string
}

type cacheEntry struct {
	body    json.RawMessage
	expires time.Time
}

func newTTLCache(ttl time.Duration, maxEntries int, now func() time.Time) *ttlcache {
	return &ttlcache{ttl: ttl, max: maxEntries, now: now, values: map[string]cacheEntry{}}
}

func (c *ttlcache) get(key string) (json.RawMessage, bool) {
	if c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.values[key]
	if !ok || c.now().After(e.expires) {
		if ok {
			c.remove(key)
		}
		return nil, false
	}
	return e.body, true
}

func (c *ttlcache) put(key string, body json.RawMessage) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; !ok {
		for len(c.values)+1 > c.max {
			oldest := c.order[0]
			c.remove(oldest)
		}
		c.order = append(c.order, key)
	}
	c.values[key] = cacheEntry{body: body, expires: c.now().Add(c.ttl)}
}

func (c *ttlcache) remove(key string) {
	delete(c.values, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
```

- [ ] **Step 4: Run to verify pass** — `go test ./internal/server -run TestTTLCache -count=1` → PASS.
- [ ] **Step 5: Commit** — `git commit -am "feat: TTL cache for library listings"`.

---

### Task 4: Library GET endpoints (`internal/server/library.go`)

**Files:**
- Create: `internal/server/library.go`
- Modify: `internal/server/server.go` (Config fields + routes)
- Test: `internal/server/library_test.go`

**Interfaces:**
- Consumes: `Config.Runner` (`ipc.OpRunner.SubmitJob(ctx, op, params)`), existing `writeJSON`, `fakeRunner` in `handlers_test.go` (package-level test helpers `testConfig`, `authedConfig` if present — otherwise build Config inline like `TestQueueGETServesCache`).
- Produces (HTTP contract for Tasks 6-9):
  - `GET /api/library/providers` → player `provider.list` result verbatim (`{ok, providers:[…]}`), cached 300 s
  - `GET /api/library/{provider}/playlists|albums|artists|catalog?offset&limit` → player result verbatim, cached `LibraryTTL` (default 60 s)
  - `GET /api/library/{provider}/album/{id}/tracks` (op `provider.album_tracks`, param `album`) and `…/artist/{id}/tracks` (op `provider.tracks`, param `id`)
  - `GET /api/search/{provider}?q&offset&limit` → `provider.search` result, never cached
  - Errors: validation → 400 `{error,code:"invalid_request"}`; upstream `IPCError` → **502** `{error,code:"upstream_error",detail}`; player offline → **502** `detail:"player offline"`; job wait past `LibraryTimeout` → **504** `{error:"job_timeout",code:"job_timeout"}`.
  - Config additions: `LibraryTTL time.Duration` (0 ⇒ default 60 s), `LibraryTimeout time.Duration` (0 ⇒ default 30 s).

- [ ] **Step 1: Write the failing tests** (`internal/server/library_test.go`)

```go
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

	"cliamp-web/internal/ipc"
)

func libConfig(t *testing.T, fr *fakeRunner) Config {
	cfg := testConfig(t)
	cfg.Runner = fr
	cfg.Auth = NewAuth("deadbeef")
	cfg.Guard = NewHostGuard(nil)
	return cfg
}

func libGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("X-Token", "deadbeef")
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

func TestLibraryRequiresToken(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{}`)}}
	h := New(libConfig(t, fr))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/library/local/albums", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("library must sit behind protect, got %d", rec.Code)
	}
}

func TestLibraryAlbumTracksParams(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"tracks":[]}`)}}
	h := New(libConfig(t, fr))
	libGet(t, h, "/api/library/navidrome/album/al1/tracks?offset=0&limit=50")
	if fr.lastOp != "provider.album_tracks" || fr.lastParams["album"] != "al1" {
		t.Fatalf("album tracks params: op=%s params=%v", fr.lastOp, fr.lastParams)
	}
	libGet(t, h, "/api/library/navidrome/artist/ar2/tracks")
	if fr.lastOp != "provider.tracks" || fr.lastParams["id"] != "ar2" {
		t.Fatalf("artist tracks params: op=%s params=%v", fr.lastOp, fr.lastParams)
	}
}
```

Also extend `fakeRunner` in `handlers_test.go` (existing struct at lines 57-62 — add fields and keep old behavior; its `SubmitJob` method gains the hooks):

```go
type fakeRunner struct {
	lastOp     string
	lastParams map[string]any
	job        *ipc.Job
	err        error
	errFn      func(ctx context.Context) error
	okFn       func()
}

func (f *fakeRunner) SubmitJob(ctx context.Context, op string, params map[string]any) (*ipc.Job, error) {
	f.lastOp, f.lastParams = op, params
	if f.errFn != nil {
		return nil, f.errFn(ctx)
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.okFn != nil {
		f.okFn()
	}
	return f.job, nil
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/server -run TestLibrary` → FAIL (404s, unknown Config fields).

- [ ] **Step 3: Implement `internal/server/library.go`**

```go
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"cliamp-web/internal/ipc"
)

const (
	defaultLibraryTTL     = 60 * time.Second
	defaultLibraryTimeout = 30 * time.Second
	providersTTL          = 5 * time.Minute
	defaultLimit          = 50
	maxLimit              = 200
)

// libParams validates pagination. The provider key is passed upstream
// unvalidated: the player's error is the authority (spec §5.1).
func libParams(r *http.Request) (map[string]any, bool) {
	q := r.URL.Query()
	parseInt := func(name string, def int) (int, bool) {
		s := q.Get(name)
		if s == "" {
			return def, true
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	offset, ok := parseInt("offset", 0)
	if !ok || offset < 0 {
		return nil, false
	}
	limit, ok := parseInt("limit", defaultLimit)
	if !ok || limit < 1 {
		return nil, false
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return map[string]any{"provider": r.PathValue("provider"), "offset": offset, "limit": limit}, true
}

// libraryGET runs one provider op for a GET, with cache + error mapping.
func (s *serverState) libraryGET(w http.ResponseWriter, r *http.Request, cache *ttlcache, op string, params map[string]any) {
	key := r.URL.Path + "?" + r.URL.RawQuery
	if body, ok := cache.get(key); ok {
		writeJSON(w, http.StatusOK, body)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.libraryTimeout())
	defer cancel()
	job, err := s.cfg.Runner.SubmitJob(ctx, op, params)
	if err != nil {
		s.libraryError(w, err)
		return
	}
	if len(job.Result) == 0 {
		job.Result = json.RawMessage(`{"ok":true}`)
	}
	cache.put(key, job.Result)
	writeJSON(w, http.StatusOK, job.Result)
}

func (s *serverState) libraryError(w http.ResponseWriter, err error) {
	var ie *ipc.IPCError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "job_timeout", "code": "job_timeout"})
	case errors.As(err, &ie):
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": ie.Error(), "code": "upstream_error", "detail": ie.Detail})
	case errors.Is(err, ipc.ErrNotConnected):
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "player offline", "code": "upstream_error", "detail": "player offline"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "code": "internal_error"})
	}
}

func (s *serverState) libraryProviders(w http.ResponseWriter, r *http.Request) {
	s.libraryGET(w, r, s.cfg.providersCache, "provider.list", map[string]any{})
}

func (s *serverState) libraryList(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	s.libraryGET(w, r, s.cfg.listCache, "provider."+r.PathValue("kind"), params)
}

func (s *serverState) libraryAlbumTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["album"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.album_tracks", params)
}

func (s *serverState) libraryArtistTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["id"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.tracks", params)
}

func (s *serverState) librarySearch(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	params["query"] = q
	s.libraryGET(w, r, s.cfg.noCache, "provider.search", params)
}
```

- [ ] **Step 4: Extend `server.go`** — `Config` gains:

```go
	LibraryTTL     time.Duration             // library listing cache TTL; 0 ⇒ 60 s
	LibraryTimeout time.Duration             // provider job wait cap; 0 ⇒ 30 s
```

and `New` wires:

```go
	if cfg.LibraryTTL == 0 {
		cfg.LibraryTTL = defaultLibraryTTL
	}
	if cfg.LibraryTimeout == 0 {
		cfg.LibraryTimeout = defaultLibraryTimeout
	}
	cfg.listCache = newTTLCache(cfg.LibraryTTL, 128, time.Now)
	cfg.providersCache = newTTLCache(providersTTL, 4, time.Now)
	cfg.noCache = newTTLCache(0, 1, time.Now)
```

with unexported fields on Config (`listCache, providersCache, noCache *ttlcache` — Config is only built inside main/tests; adding unexported fields is safe since `server.New(cfg)` copies it) plus:

```go
func (c *Config) libraryTimeout() time.Duration {
	if c.LibraryTimeout == 0 {
		return defaultLibraryTimeout
	}
	return c.LibraryTimeout
}
```

and routes inside `New`, after the existing `/api/command` line:

```go
	mux.Handle("GET /api/library/providers", s.protect(http.HandlerFunc(s.libraryProviders)))
	mux.Handle("GET /api/library/{provider}/{kind}", s.protect(http.HandlerFunc(s.libraryList)))
	mux.Handle("GET /api/library/{provider}/album/{id}/tracks", s.protect(http.HandlerFunc(s.libraryAlbumTracks)))
	mux.Handle("GET /api/library/{provider}/artist/{id}/tracks", s.protect(http.HandlerFunc(s.libraryArtistTracks)))
	mux.Handle("GET /api/search/{provider}", s.protect(http.HandlerFunc(s.librarySearch)))
```

Note the `{kind}` route must not shadow `album`/`artist` sub-paths: Go's `ServeMux` prefers the more specific registered pattern, so `…/album/{id}/tracks` wins over `…/{kind}` — no extra guard needed, but add the test `TestLibraryKindRouteDoesNotShadowAlbumTracks` asserting the album-tracks path reaches `provider.album_tracks` (it is already `TestLibraryAlbumTracksParams`; keep both route registrations and that test).

`server.go` imports gain `"time"`.

- [ ] **Step 5: Run full suite** — `go test ./... -race -count=1` → PASS; `gofmt -l .` and `go vet ./...` clean.

- [ ] **Step 6: Commit** — `git commit -am "feat: /api/library + /api/search provider browse endpoints"`.

---

### Task 5: End-to-end wiring through `run()`

**Files:**
- Modify: `cmd/cliamp-web/wiring_test.go` (new integration test only)

**Interfaces:**
- Consumes: `fakeplayer` provider ops (Task 2), routes (Task 4), `run()` unchanged.
- Produces: proven HTTP contract for the UI tasks.

- [ ] **Step 1: Add the integration test** (append to `wiring_test.go`):

```go
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
	st := readStatus(t, dir)
	tok, _ := st["token"].(string)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	deadline := time.Now().Add(5 * time.Second)
	for {
		r := getJSON(t, base+"/api/library/providers", tok)
		if r["ok"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("providers never came up: %v", r)
		}
		time.Sleep(50 * time.Millisecond)
	}
	r := getJSON(t, base+"/api/library/navidrome/playlists?offset=0&limit=50", tok)
	if total, _ := r["total"].(float64); total != 120 {
		t.Fatalf("want 120 playlists, got %v", r["total"])
	}
	s := getJSON(t, base+"/api/search/ytmusic?q=lofi", tok)
	tracks, _ := s["tracks"].([]any)
	if len(tracks) != 3 || tracks[0].(map[string]any)["stream"] != true {
		t.Fatalf("search passthrough: %v", s)
	}
	if rec := getJSONNoAuth(t, base+"/api/library/local/albums"); rec["error"] == nil {
		t.Fatalf("library must require token: %v", rec)
	}
}
```

(`getJSONNoAuth` already exists in this file; it returns the parsed body.)

- [ ] **Step 2: Run** — `go test ./cmd/cliamp-web -run TestRunServesLibrary -count=1` → PASS (Task 4 already landed the routes; this test is a contract pin, written once, expected green).
- [ ] **Step 3: Full suite + commit** — `go test ./... -race -count=1 && gofmt -l . && go vet ./...` then `git commit -am "test: end-to-end provider browse through the real bridge"`.

---

### Task 6: UI groundwork — api client, store state, tabbed shell, MiniPlayer

**Files:**
- Modify: `webui/src/api.ts`, `webui/src/store.ts`, `webui/src/App.vue`, `webui/src/style.css`
- Create: `webui/src/components/MiniPlayer.vue`
- Build artifact: `webui/dist` (rebuild + commit)

**Interfaces:**
- Consumes: Task 4 HTTP contract; existing `req<T>` auth helper.
- Produces (used by Tasks 7-9):
  - api.ts: `getProviders() → {ok, providers: ProviderInfo[]}`; `libraryList(provider, kind, offset, limit) → ListReply`; `libraryTracks(provider, detail: "album"|"artist", id, offset, limit) → ListReply`; `searchProvider(provider, q, offset, limit) → ListReply`; `interface ProviderInfo { key; name; searchable?; catalog?; browse_albums?; browse_artists? }`; `interface ListReply { ok: boolean; total?: number; playlists?: any[]; albums?: any[]; artists?: any[]; tracks?: any[] }`
  - store.ts: `store.view: "nowplaying" | "queue" | "browse"` (initial `"nowplaying"`); `setView(v)`; `browse` reactive slice (Task 8 fills actions): `{ providers: ProviderInfo[]; provider: string; tab: string; list: any[]; total: number; offset: number; loading: boolean; error: string; query: string; stack: Drill[] }` with `interface Drill { kind: "album" | "artist" | "playlist"; id: string; title: string }`.

- [ ] **Step 1: api.ts additions** (append after existing exports):

```ts
export interface ProviderInfo {
  key: string;
  name: string;
  searchable?: boolean;
  catalog?: boolean;
  browse_albums?: boolean;
  browse_artists?: boolean;
}

export interface ListReply {
  ok: boolean;
  total?: number;
  playlists?: any[];
  albums?: any[];
  artists?: any[];
  tracks?: any[];
}

export const getProviders = () =>
  req<ListReply & { providers: ProviderInfo[] }>("/api/library/providers");

export const libraryList = (
  provider: string,
  kind: "playlists" | "albums" | "artists" | "catalog",
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/library/${encodeURIComponent(provider)}/${kind}?offset=${offset}&limit=${limit}`,
  );

export const libraryTracks = (
  provider: string,
  detail: "album" | "artist",
  id: string,
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/library/${encodeURIComponent(provider)}/${detail}/${encodeURIComponent(id)}/tracks?offset=${offset}&limit=${limit}`,
  );

export const searchProvider = (
  provider: string,
  q: string,
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/search/${encodeURIComponent(provider)}?q=${encodeURIComponent(q)}&offset=${offset}&limit=${limit}`,
  );
```

- [ ] **Step 2: store.ts additions** — add to the `store` object:

```ts
  view: "nowplaying" as "nowplaying" | "queue" | "browse",
  browse: {
    providers: [] as api.ProviderInfo[],
    provider: "",
    tab: "",
    list: [] as any[],
    total: -1,
    offset: 0,
    loading: false,
    error: "",
    query: "",
    stack: [] as Drill[],
  },
```

with `export interface Drill { kind: "album" | "artist" | "playlist"; id: string; title: string }` above `store`, and at the bottom:

```ts
export function setView(v: "nowplaying" | "queue" | "browse") {
  store.view = v;
}
```

- [ ] **Step 3: MiniPlayer + tabbed App.vue**

`webui/src/components/MiniPlayer.vue`:

```vue
<script setup lang="ts">
import { computed } from "vue";
import { store, runCommand, setView } from "../store";
import { fmtPos } from "../lib/format";

const track = computed(() => (store.snapshot.track ?? {}) as Record<string, any>);
const pos = computed(() =>
  fmtPos(Number(store.snapshot.position ?? 0)),
);
</script>

<template>
  <div class="mini" @click="setView('nowplaying')">
    <div class="mini-text">
      <b>{{ track.title ?? "nothing playing" }}</b>
      <span>{{ track.artist ?? "" }} · {{ pos }}</span>
    </div>
    <button
      class="mini-toggle"
      :disabled="!store.online"
      :aria-label="store.snapshot.state === 'playing' ? 'pause' : 'play'"
      @click.stop="runCommand('toggle')"
    >
      {{ store.snapshot.state === "playing" ? "⏸" : "▶" }}
    </button>
  </div>
</template>

<style scoped>
.mini {
  display: flex;
  align-items: center;
  gap: 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 8px 12px;
  background: var(--surface);
}
.mini-text {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  flex: 1;
}
.mini-text span {
  color: var(--muted);
  font-size: 12px;
}
.mini-toggle {
  font-size: 20px;
}
</style>
```

`webui/src/App.vue` full replacement:

```vue
<script setup lang="ts">
import NowPlaying from "./components/NowPlaying.vue";
import PlayNextList from "./components/PlayNextList.vue";
import QueueList from "./components/QueueList.vue";
import StatusPill from "./components/StatusPill.vue";
import Toasts from "./components/Toasts.vue";
import TransportBar from "./components/TransportBar.vue";
import MiniPlayer from "./components/MiniPlayer.vue";
import { store, setView } from "./store";
</script>

<template>
  <main class="wrap">
    <header class="bar">
      <span class="brand">cliamp</span>
      <StatusPill />
    </header>

    <p v-if="!store.online" class="banner">
      player offline — controls paused, state may be stale
    </p>

    <template v-if="store.view === 'nowplaying'">
      <NowPlaying />
      <TransportBar />
    </template>
    <template v-else-if="store.view === 'queue'">
      <PlayNextList />
      <QueueList />
    </template>
    <template v-else>
      <p class="muted">browse lands in a moment…</p>
    </template>

    <MiniPlayer v-if="store.view !== 'nowplaying'" />

    <nav class="tabs">
      <button :class="{ on: store.view === 'nowplaying' }" @click="setView('nowplaying')">Now Playing</button>
      <button :class="{ on: store.view === 'queue' }" @click="setView('queue')">Queue</button>
      <button :class="{ on: store.view === 'browse' }" @click="setView('browse')">Browse</button>
    </nav>

    <Toasts />
  </main>
</template>

<style scoped>
.tabs {
  position: sticky;
  bottom: 0;
  display: flex;
  gap: 4px;
  padding: 6px 0;
  background: var(--surface);
}
.tabs button {
  flex: 1;
  padding: 10px 0;
  border-radius: 10px;
}
.tabs button.on {
  border-color: var(--accent);
  color: var(--accent);
}
.muted {
  color: var(--muted);
}
</style>
```

(`NowPlaying`/`TransportBar` render inside the Now Playing view until Task 7 replaces them with the hero — the app is functional at every task boundary.)

- [ ] **Step 4: Typecheck + build + embed**

Run: `cd webui && npm run build && make build`
Expected: `vue-tsc` clean, dist rebuilt, `dist/cliamp-web` links the new embed.

- [ ] **Step 5: Browser smoke** — start the standalone fake + bridge (scratch, not committed): a `cmd/fakeplayer` main is NOT created; reuse the test binary:

```bash
cat > /tmp/fakeplayer/main.go <<'EOF'
package main

import "cliamp-web/internal/fakeplayer"
import "fmt"; import "os"; import "os/signal"

func main() {
	f, err := fakeplayer.Start(os.Args[1][:len(os.Args[1])-len("/s")], fakeplayer.Options{})
	if err != nil { panic(err) }
	fmt.Println("fake at", f.Path)
	signal.Wait(os.Interrupt)
	f.Close()
}
EOF
```

— simpler and identical: keep the v1 scratch approach — copy `internal/fakeplayer` into a throwaway `/tmp/fakeplayer` module as in v1 Task 9 (`go run /tmp/fakeplayer /tmp/fp.sock`). Then `./dist/cliamp-web serve --port 8731 --data-dir /tmp/cw-v2 --socket /tmp/fp.sock`. Open `http://127.0.0.1:8731/?token=…`; assert: app opens with the Now Playing content visible; tapping **Browse** shows the placeholder; tapping **Queue** shows QueueList; MiniPlayer visible on Queue/Browse and gone on Now Playing; clicking MiniPlayer returns to Now Playing. Zero page errors from `tab.errors()`.

- [ ] **Step 6: Commit** — `git add webui && git commit -m "feat(ui): tabbed shell (Now Playing first), MiniPlayer, library api client"` (includes rebuilt `webui/dist`).

---

### Task 7: NowPlayingHero — full playback page, retire v1 panels

**Files:**
- Create: `webui/src/components/NowPlayingHero.vue`
- Delete: `webui/src/components/NowPlaying.vue`, `webui/src/components/TransportBar.vue` (all their behavior lands in the hero; `QueueList`/`PlayNextList` untouched)
- Modify: `webui/src/App.vue` (import the hero), `webui/src/style.css` (hero styles scoped in the component; drop dead global rules)

**Interfaces:**
- Consumes: everything v1 NowPlaying/TransportBar used — `fmtPos`, `runCommand`, `withRevision`, `store`, Reka `Slider/Switch`.
- Produces: `NowPlayingHero.vue` with no public interface beyond rendering.

- [ ] **Step 1: Write the component** — `webui/src/components/NowPlayingHero.vue`. This is v1 `NowPlaying.vue` (drift timer, drag-vs-commit seek logic, indeterminate bar for streams) plus v1 `TransportBar.vue` (prev/toggle/next/stop, shuffle switch, repeat cycle, volume slider `v-model` + `value-commit`) plus the Up-next list, merged and re-scaled. Carry the existing code forward verbatim where identical (read both files before writing):

```vue
<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import * as Slider from "reka-ui";
import { store, runCommand, withRevision } from "../store";
import { fmtPos } from "../lib/format";

const VOL_MIN = -30, VOL_MAX = 6;

const track = computed(() => (store.snapshot.track ?? {}) as Record<string, any>);
const duration = computed(() => Number(track.value.duration ?? 0));
const seekable = computed(() => store.snapshot.seekable !== false && duration.value > 0);

// scrubber: mirror player position unless dragging; commit only on release
const model = ref<number[]>([0]);
const dragging = ref(false);
const localPos = ref(0);
watch(
  [() => Number(store.snapshot.position ?? 0), dragging],
  ([p, drag]) => {
    if (!drag) {
      localPos.value = p;
      model.value = [p];
    }
  },
  { immediate: true },
);
function seekTo(v: number[]) {
  runCommand("seek.absolute", { value: v[0] });
}
// 1 s drift while playing, exactly as v1 NowPlaying did
const drift = setInterval(() => {
  if (store.snapshot.state === "playing" && seekable.value && !dragging.value) {
    localPos.value += 1;
    model.value = [localPos.value];
  }
}, 1000);
onUnmounted(() => clearInterval(drift));

// volume: V2 never reports dB back — track the last value this page set
const volume = ref<number[]>([0]);
function setVolume(v: number[]) {
  runCommand("volume", { value: v[0] });
}

const repeatNames = ["off", "all", "one"] as const;
const repeatLabel: Record<string, string> = { off: "Repeat Off", all: "Repeat All", one: "Repeat One" };
function cycleRepeat() {
  runCommand("repeat", { name: "cycle" });
}

const upNext = computed(() => {
  const tracks = store.queue.tracks ?? [];
  const i = Number(store.snapshot.index ?? 0);
  return tracks.slice(i + 1, i + 6).map((t, k) => ({ t, index: i + 1 + k }));
});
function playAt(index: number) {
  withRevision("queue.play", { index });
}
</script>

<template>
  <section class="hero" :style="{ background: `linear-gradient(160deg, ${track.title ? hue(track.title) : 'var(--surface)'}, var(--surface) 70%)` }">
    <h1 class="title">{{ track.title ?? "nothing playing" }}</h1>
    <p class="artist">{{ track.artist ?? (track.path ?? "") }}</p>

    <div class="posrow">
      <span>{{ fmtPos(localPos) }}</span>
      <span>{{ seekable ? fmtPos(duration) : "stream" }}</span>
    </div>
    <Slider.Root
      v-if="seekable"
      class="scrub"
      :model-value="model"
      :min="0"
      :max="duration || 1"
      :step="1"
      @update:model-value="(v: number[]) => (model = v)"
      @drag-start="dragging = true"
      @drag-end="dragging = false"
      @value-commit="seekTo"
    >
      <Slider.Track class="track"><Slider.Range class="range" /><Slider.Thumb class="thumb" /></Slider.Track>
    </Slider.Root>
    <div v-else class="scrub stream-indeterminate"><div class="shimmer"></div></div>

    <div class="transport">
      <button :disabled="!store.online" aria-label="previous" @click="runCommand('prev')">⏮</button>
      <button class="big" :disabled="!store.online" :aria-label="store.snapshot.state === 'playing' ? 'pause' : 'play'" @click="runCommand('toggle')">
        {{ store.snapshot.state === "playing" ? "⏸" : "▶" }}
      </button>
      <button :disabled="!store.online" aria-label="next" @click="runCommand('next')">⏭</button>
      <button :disabled="!store.online" aria-label="stop" @click="runCommand('stop')">⏹</button>
    </div>

    <div class="modes">
      <Switch.Root
        class="sw"
        :checked="String(store.snapshot.shuffle ?? 'off').toLowerCase() === 'on'"
        :disabled="!store.online"
        :name="String(store.snapshot.shuffle ?? 'off')"
        @update:checked="() => runCommand('shuffle', { name: 'toggle' })"
      >
        <Switch.Thumb class="sw-thumb" />
      </Switch.Root>
      <button :disabled="!store.online" :name="repeatNames.includes(String(store.snapshot.repeat) as any) ? 'cycle' : 'cycle'" @click="cycleRepeat">
        {{ repeatLabel[String(store.snapshot.repeat ?? "off")] ?? "Repeat" }}
      </button>
      <Slider.Root class="vol" :model-value="volume" :min="VOL_MIN" :max="VOL_MAX" :step="1"
        @update:model-value="(v: number[]) => (volume = v)" @value-commit="setVolume">
        <Slider.Track class="track"><Slider.Range class="range" /><Slider.Thumb class="thumb" /></Slider.Track>
      </Slider.Root>
      <span class="volx">{{ volume[0] }} dB</span>
    </div>

    <h2 class="next-h">up next</h2>
    <ul class="upnext">
      <li v-for="row in upNext" :key="row.index" @click="playAt(row.index)">
        <span class="t">{{ row.t.title }}</span>
        <span class="a">{{ row.t.artist }}</span>
      </li>
    </ul>
  </section>
</template>
```

`hue()` helper (top of the same script block):

```ts
function hue(seed: string): string {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0;
  return `hsl(${h % 360} 35% 18%)`;
}
```

Carry over from v1 components (read them before deleting): the drift interval's exact guard order, the `watch([position,revision])` resync behavior (`revision` changes must resync the scrubber even when `position` repeats — add `() => store.snapshot.revision` to the watch sources), the Reka `Slider` import style (`import * as Slider from "reka-ui"` is WRONG — v1 imports `{ SliderRoot as Slider, … }`-style names; mirror v1's exact import lines), and the `Switch` import likewise. If v1 used `SliderRoot`/`SliderTrack` element names, the hero must use the same.

- [ ] **Step 2: App.vue** — replace the `NowPlaying`/`TransportBar` imports and usage with `<NowPlayingHero />` alone; delete the two v1 component files.
- [ ] **Step 3: Build** — `cd webui && npm run build && make build` → vue-tsc clean.
- [ ] **Step 4: Browser smoke** (same scratch stack as Task 6): hero shows title from the fake; scrubber drags commit `seek.absolute` (assert via bridge log or a `runCommand` spy through `window.__cw = runCommand`? no — assert against the fake: switch the smoke bridge to the REAL socket is out of scope here; assert the slider drag changes the displayed position and no page errors; with the REAL player from v1 Task 10 recipe: click row → position jumps). Up-next renders fake queue rows (the fake has one row; assert ≤5 rows render without error). Transport buttons disabled while fake socket file deleted, enabled after restart.
- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat(ui): Now Playing hero view replaces v1 panels"`.

---

### Task 8: Browse core — providers, tabs, lists, drill stack

**Files:**
- Create: `webui/src/components/BrowseView.vue`
- Modify: `webui/src/App.vue` (replace the placeholder), `webui/src/store.ts` (browse actions)

**Interfaces:**
- Consumes: `api.getProviders/libraryList/libraryTracks`, `store.browse`.
- Produces: store actions `loadProviders()`, `pickProvider(key)`, `pickTab(kind)`, `openDrill(entry)`, `closeDrill()`, `browseMore()` used by the component (actions live in the store so the infinite-scroll sentinel only calls one function).

- [ ] **Step 1: store.ts browse actions** (append):

```ts
const LIST_FIELDS: Record<string, "playlists" | "albums" | "artists" | "tracks"> = {
  playlists: "playlists",
  albums: "albums",
  artists: "artists",
  catalog: "playlists",
  album: "tracks",
  artist: "tracks",
};

function rows(reply: api.ListReply, field: string): any[] {
  return (reply as Record<string, any[]>)[field] ?? [];
}

export async function loadProviders() {
  if (store.browse.providers.length) return;
  try {
    const r = await api.getProviders();
    store.browse.providers = r.providers ?? [];
    if (!store.browse.provider && store.browse.providers.length)
      pickProvider(store.browse.providers[0].key);
  } catch {
    store.browse.error = "provider list unavailable";
  }
}

export function tabsFor(p: api.ProviderInfo): string[] {
  const t: string[] = [];
  if (p.catalog) t.push("catalog");
  if (p.key === "navidrome") t.push("playlists");
  if (p.browse_albums) t.push("albums");
  if (p.browse_artists) t.push("artists");
  if (p.searchable) t.push("search");
  return t;
}

export function pickProvider(key: string) {
  const p = store.browse.providers.find((x) => x.key === key);
  if (!p) return;
  store.browse.provider = key;
  store.browse.stack = [];
  store.browse.query = "";
  pickTab(tabsFor(p)[0] ?? "");
}

export function pickTab(kind: string) {
  store.browse.tab = kind;
  store.browse.error = "";
  if (kind === "search") {
    store.browse.list = [];
    store.browse.total = -1;
    store.browse.offset = 0;
    return;
  }
  void fetchPage(kind, 0, true);
}

export function openDrill(entry: Drill) {
  store.browse.stack.push(entry);
  void fetchPage(entry.kind, 0, true);
}

export function closeDrill() {
  store.browse.stack.pop();
  const top = store.browse.stack[store.browse.stack.length - 1];
  void fetchPage(top ? top.kind : store.browse.tab, 0, true);
}

export function browseMore() {
  const b = store.browse;
  if (b.total >= 0 && b.offset >= b.total) return; // past the last page
  const top = b.stack[b.stack.length - 1];
  const kind = top ? top.kind : b.tab;
  if (kind) void fetchPage(kind, b.offset, false);
}

async function fetchPage(kind: string, offset: number, reset: boolean) {
  const b = store.browse;
  if (!kind || b.loading) return;
  b.loading = true;
  b.error = "";
  try {
    const top = b.stack[b.stack.length - 1];
    let reply: api.ListReply;
    if (kind === "album") reply = await api.libraryTracks(b.provider, "album", top!.id, offset);
    else if (kind === "artist") reply = await api.libraryTracks(b.provider, "artist", top!.id, offset);
    else reply = await api.libraryList(b.provider, kind as any, offset);
    const field = LIST_FIELDS[kind];
    const add = rows(reply, field);
    b.list = reset ? add : b.list.concat(add);
    b.total = typeof reply.total === "number" ? reply.total : b.list.length;
    b.offset = b.list.length;
  } catch (e: any) {
    if (!reset) b.error = String(e?.body?.detail ?? e?.status ?? "provider unreachable");
    else b.error = String(e?.body?.detail ?? e?.status ?? "provider unreachable");
  } finally {
    b.loading = false;
  }
}
```

(Review Focus #3 note: `rows()` returns `[]` when the player omits the array entirely — both empty shapes render "no results". Review Focus #6: `browseMore` returns before any request once `offset >= total`.)

- [ ] **Step 2: BrowseView.vue**

```vue
<script setup lang="ts">
import { onMounted, ref } from "vue";
import { store, loadProviders, pickProvider, pickTab, openDrill, closeDrill, browseMore, tabsFor, notify } from "../store";

onMounted(loadProviders);

const sentinel = ref<HTMLElement | null>(null);
const io = new IntersectionObserver((es) => {
  if (es.some((e) => e.isIntersecting)) browseMore();
});
// observe after each render tick
const stop = setInterval(() => {
  if (sentinel.value) io.observe(sentinel.value);
}, 250);
// (stop + disconnect run in onUnmounted below — the interval exists because
// the sentinel re-mounts whenever the view content changes; 250 ms polling is
// the boring version and fine at 1 element)
</script>

<template>
  <section class="browse" v-if="store.browse.providers.length">
    <div class="chips">
      <button
        v-for="p in store.browse.providers"
        :key="p.key"
        :class="{ on: p.key === store.browse.provider }"
        @click="pickProvider(p.key)"
      >{{ p.name }}</button>
    </div>

    <div class="subtabs" v-if="!store.browse.stack.length">
      <button
        v-for="t in tabsFor(store.browse.providers.find((p) => p.key === store.browse.provider)!)"
        :key="t"
        :class="{ on: t === store.browse.tab }"
        @click="pickTab(t)"
      >{{ t }}</button>
    </div>

    <p v-if="store.browse.error" class="banner">
      {{ store.browse.error }}
      <button @click="store.browse.stack.length ? closeDrill() && pickTab(store.browse.tab) : pickTab(store.browse.tab)">retry</button>
    </p>

    <p v-if="store.browse.stack.length" class="crumbs">
      <button @click="closeDrill()">← back</button>
      {{ store.browse.stack.map((s) => s.title).join(" / ") }}
    </p>

    <ul class="rows">
      <li
        v-for="(row, i) in store.browse.list"
        :key="row.id ?? row.path ?? i"
        class="row"
      >
        <template v-if="store.browse.tab === 'albums' && !store.browse.stack.length">
          <div @click="openDrill({ kind: 'album', id: row.id, title: row.name })">
            <b>{{ row.name }}</b> <span class="muted">{{ row.artist }} · {{ row.year }}</span>
          </div>
        </template>
        <template v-else-if="store.browse.tab === 'artists' && !store.browse.stack.length">
          <div @click="openDrill({ kind: 'artist', id: row.id, title: row.name })"><b>{{ row.name }}</b></div>
        </template>
        <template v-else-if="(store.browse.tab === 'playlists' || store.browse.tab === 'catalog') && !store.browse.stack.length">
          <div @click="openDrill({ kind: 'playlist', id: row.id, title: row.name })">
            <b>{{ row.name }}</b> <span class="muted">{{ row.track_count ?? "" }}</span>
          </div>
        </template>
        <template v-else>
          <span class="t">{{ row.title }}</span>
          <span class="a">{{ row.artist }} · {{ Math.round((row.duration_secs ?? 0) / 60) }} min</span>
        </template>
      </li>
    </ul>
    <p v-if="!store.browse.loading && store.browse.list.length === 0 && !store.browse.error" class="muted">no results</p>
    <div ref="sentinel" class="sentinel"></div>
  </section>
</template>
```

Wait — playlist drill-down: the player has NO "playlist tracks" listing op (`provider.load` replaces the queue directly; `provider.load_album` exists for albums). **Ruling:** tapping a playlist row does NOT drill; it fires the confirm→`provider.load` action (that belongs to Task 9's load-confirm work; in Task 8 a playlist tap opens nothing — render a ▶ button on playlist rows that is wired in Task 9). Adjust: drop `openDrill({kind:'playlist'…})` and instead render `<button class="load" @click="notify('loading playlist…'); $emit(...)">` — the concrete load button arrives with Task 9; for Task 8 playlist rows show name + count only, no click handler. Update the template accordingly (remove the third `template v-else-if` branch's `@click`, keep the styling). `Drill.kind` stays `"album" | "artist"` — the interface in Task 6 listed `"playlist"`; **use the narrower union `"album" | "artist"` in Task 6's interface** (fix there, not duplicated here).

Also add scoped styles: `.chips button`, `.subtabs button`, `.row`, `.sentinel { height: 1px; }`, `.crumbs button` — reuse v1 `.row` conventions from `QueueList.vue`'s scoped styles (read it, mirror colors/sizes; NEVER copy global classes).

Cleanup correctness: `onUnmounted(() => { clearInterval(stop); io.disconnect(); })` — include it.

- [ ] **Step 3: App.vue** — replace the browse placeholder with `<BrowseView />` (import it).
- [ ] **Step 4: Build + browser smoke**: Browse tab shows three provider chips (`Navidrome/Radio/Local` from the fake); Navidrome lands on… first tab = `playlists`; list shows 50 rows, scrolling to the sentinel brings rows 51-100, 101-120, then STOPS (network tab / `tab.requests()` count: exactly 3 playlist pages, no 4th); Albums tab → tap Album One → 7 track rows + back button → Queue tab unaffected; delete fake socket → error banner with retry, list content remains visible.
- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat(ui): provider browse with drill-down and infinite scroll"` (with rebuilt dist).

---

### Task 9: Search, track actions, queue-replacement confirms

**Files:**
- Modify: `webui/src/components/BrowseView.vue` (search box, track action buttons, playlist/album load buttons), `webui/src/store.ts` (`browseSearch(debounced)`, `playTrack`, `queueTrack`, `loadProviderItem`)

**Interfaces:**
- Consumes: `withRevision` (if_revision for track ops), `window.confirm`, `runCommand`.
- Produces: no new cross-task interface; final UI behavior.

- [ ] **Step 1: store.ts actions** (append):

```ts
let searchTimer: ReturnType<typeof setTimeout> | null = null;

export function browseSearch(q: string) {
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    const b = store.browse;
    b.query = q;
    if (b.tab !== "search" || !q) return;
    b.loading = true;
    b.error = "";
    api
      .searchProvider(b.provider, q)
      .then((r) => {
        b.list = rows(r, "tracks");
        b.total = r.total ?? b.list.length;
        b.offset = b.list.length;
      })
      .catch((e: any) => (b.error = String(e?.body?.detail ?? "search failed")))
      .finally(() => (b.loading = false));
  }, 300);
}

export function playTrack(t: Record<string, unknown>) {
  withRevision("track.play", { track: t });
}
export function queueTrack(t: Record<string, unknown>) {
  withRevision("track.queue", { track: t });
}

export function loadPlaylist(provider: string, id: string, name: string) {
  if (!window.confirm(`replace the queue with playlist "${name}"?`)) return;
  runCommand("provider.load", { provider, playlist: id });
}
export function loadAlbum(provider: string, id: string, name: string) {
  if (!window.confirm(`replace the queue with album "${name}"?`)) return;
  runCommand("provider.load_album", { provider, album: id });
}
```

- [ ] **Step 2: BrowseView template additions**:

Search tab (rendered when `store.browse.tab === "search"` above the row list):

```html
<input
  class="searchbox"
  placeholder="search this provider…"
  :value="store.browse.query"
  @input="(e) => browseSearch((e.target as HTMLInputElement).value)"
/>
```

Track rows (the `v-else` branch from Task 8) gain buttons:

```html
<span class="acts">
  <button aria-label="play now" @click.stop="playTrack(row)">▶</button>
  <button aria-label="queue next" @click.stop="queueTrack(row)">+</button>
</span>
```

Playlist rows (playlists/catalog tabs): `<button aria-label="load playlist" @click="loadPlaylist(store.browse.provider, row.id, row.name)">load</button>`.
Album detail header (when `stack` top is an album): a `<button @click="loadAlbum(...)">play album</button>` above the tracks (needs the album id/title: it is `stack[stack.length-1]`).

- [ ] **Step 3: Build + browser acceptance** (fake stack): search tab → type `lofi` → after debounce 3 stream rows appear; queue-next (+) on row 1 → switch to Queue tab → `play_next` list shows the track (the fake's queue doesn't change — pin this against the REAL player instead: run the v1 T10 acceptance stack `cliamp --no-auto-play` TUI + bridge, `track.queue` lands in playnext, `track.play` starts the track, and force a 409 by bumping the revision elsewhere between page-load and tap — e.g. remove one queue row from the TUI first → toast "playlist changed elsewhere" + refetch). Playlist load confirm: cancel → NOTHING sent (fake/bridge log clean); accept → job runs, playlist SSE refreshes Queue tab. Album load: fake → `provider.load_album` fires with `{provider, album:"al1"}`.
- [ ] **Step 4: Commit** — `git add -A && git commit -m "feat(ui): search, play/queue actions, queue-replacement confirms"`.

---

### Task 10: Docs, full-branch verification, live phone acceptance

**Files:**
- Modify: `README.md` (v2 features), `docs/superpowers/specs/…v2…` untouched.

- [ ] **Step 1: README** — extend the features block: provider browse (playlists/albums/artists/radio catalog), per-provider search, play-now/queue-next, load playlist/album, Now Playing page with tab shell. Add a "what the phone can do" paragraph mirroring spec §3 v1+v2. Keep the install steps unchanged (v2 adds no config knobs).
- [ ] **Step 2: Whole-branch checks** — `go test ./... -race -count=1 && gofmt -l . && go vet ./... && cd webui && npm run build && make build`.
- [ ] **Step 3: Live acceptance on the real player** (user's machine, TUI + bridge on :8730 as currently deployed): phone → Browse → Navidrome → Playlists → `load` the small playlist → queue replaces and plays; Albums → drill → play album; ytmusic search → queue a track; kill/restart player mid-browse → banner, lists survive. Verify the Now Playing page reflects TUI actions within ~1 s.
- [ ] **Step 4: Commit + update `~/.local/bin/cliamp-web`** — `make build && cp dist/cliamp-web ~/.local/bin/cliamp-web` (documented in README dev section as the dev-update step).

---

## Plan-level self-review (done at write time)

- Spec §2 wire table → Task 2 (fake mirrors it), Task 4 (params ride inside params, pinned by `TestLibraryPlaylistsPassesParamsThrough`).
- Spec §3 scope → T4/T5 (endpoints), T6-T9 (UI surfaces incl. Now Playing-first shell per the amendment), T10 (docs/live).
- Cuts (artwork, favorites, history/EQ/speed, HTTPS, virtualization) → no task, correct.
- Review Focus → 1: T4 502 detail + T8 banner keep; 2: T9 real-player 409 check; 3: T4 cache/shape tests + T8 "no results"; 4: T4 504 test; 5: T4 offline test + T8; 6: T8 sentinel-stop assertion.
- Type consistency: `ListReply`, `Drill` (narrowed to `"album" | "artist"`), `ProviderInfo` defined once in Tasks 6, consumed 8/9; `browseMore`/`fetchPage` naming consistent; `fakeRunner` extension in Task 4 keeps v1 fields.
