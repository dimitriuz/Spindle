# cliamp-plugin-web v3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** playlist drill-down, a synced lyrics sheet, and four phone-ergonomics fixes (row layout, SSE auto-reconnect, fixed tab bar, no load labels) for the cliamp-web bridge + Vue UI.

**Architecture:** additive Go routes on the existing `libraryGET` pattern (`provider.tracks` with `playlist` param, `lyrics` passthrough, named SSE ping) + a fake-player extension mirroring live cliamp v2.0.1 probes; UI changes are one CSS token/layer pass, one store state-machine (`fetchPage` kind `"playlist"`, `startEventStream` reconnect machine), and one new Hero sheet. v2's generation-counter paging rules are extended, not duplicated.

**Tech Stack:** Go 1.23 (stdlib + existing internal packages), Vue 3 + TypeScript (`reka-ui`), vite/vue-tsc, bun only for throwaway harnesses.

**Spec:** `docs/superpowers/specs/2026-10-12-cliamp-web-v3-design.md` (ground-truth probe table in §2 is binding).

## Global Constraints

- Provider/album/id/query/playlist params ride **inside `params`**, never the job envelope (live cliamp v2.0.1 rejects envelope placement).
- Queue-replacement ops (`provider.load`, `provider.load_album`) MUST be `window.confirm`-gated before submit; everything else fires immediately.
- Library GETs forward the player `result` verbatim; the bridge never talks to providers directly.
- Search and lyrics are never cached; lists/playlist-tracks cache via `listCache` (60 s TTL).
- Every `/api/*` route except `/api/health` stays behind `s.protect` (token guard).
- TypeScript: NEVER `: any` / `as any` (ts-no-any enforced); use the existing all-optional `BrowseRow`, `HttpError` with typed `status`/`detail`, `errText(e: unknown)`, `in`/`typeof` narrowing.
- Go: `net.JoinHostPort(host, strconv.Itoa(port))` for host:port, `for i := range n` loops, gofmt+vet clean.
- CSS: components may reference only variables defined in `:root` of `webui/src/style.css` (this plan adds the missing `--surface`, `--muted`, `--error` there — the v2 bug that made artists render full-strength).
- `webui/dist` is a committed embed artifact: after ANY `webui/src` change run `cd webui && npm run build && cd .. && make build` and commit `webui/dist` together with sources.
- Throwaway verification scripts go in `/tmp`, NEVER committed; the bun harnesses in Tasks 5–7 are throwaways whose invariants get re-checked by the final reviewer reading code.
- Commits: conventional, one per task; branch `feature/cliamp-web-v3` off `main` @ spec commit `5782748`.

## Review Focus

1. **Reconnect machine**: at most ONE live `EventSource` ever — old instance `close()`d before every reopen; `visibilitychange`/watchdog listeners and timers never duplicated; backoff resets only after a *received event*.
2. **Stale playlist replies**: playlist drill reuses the v2 `reqGen` counter — a slow `/playlist/pl1/tracks` reply MUST NOT paint after provider/tab switch or drill exit.
3. **Lyrics lifecycle**: fetch in flight when the sheet closes or the track changes → reply dropped (generation guard); reopen always refetches.
4. **Playlist route param hygiene**: `/api/library/{provider}/playlist/{id}/tracks` sends ONLY `playlist` (never `id`) upstream; test asserts `lastParams` has no `"id"` key.
5. **Fake `provider.tracks` precedence**: `id` wins when both given; the missing-both failure path stays reachable; `playlist:"nope"` maps the live `playlist not found (code 70)`.
6. **Row redesign keeps paging**: the sentinel element and its IntersectionObserver re-observe survive the flex row markup change (still last child of `.rows`).

---

### Task 1: Fake player — playlist tracks + lyrics

**Files:**
- Modify: `internal/fakeplayer/providers.go` (`provider.tracks` case ~lines 156-162, `fixtureTracks` ~line 65)
- Modify: `internal/fakeplayer/fakeplayer.go` (`Options` lines 15-22, `Fake` struct, `Start`, `submit` ~line 137)
- Test: `internal/fakeplayer/providers_test.go`

**Interfaces:**
- Consumes: existing `startProviders(t)`, `call(t, c, op, params)` helpers in `providers_test.go`.
- Produces: op `provider.tracks` accepts `{provider, playlist}` (`id` wins if both; neither → `navidrome: missing parameter: 'id' (code 10)`; `playlist:"nope"` → `navidrome: playlist not found (code 70)`); fixture rows `playlist:pl1` → 3 tracks `"Playlist One Track 1..3"`, `playlist:pl-empty` → `[]`; op `lyrics` → job result `{"ok":true,"lyrics":<Options.Lyrics>}` or failed job `no lyrics found` when `Options.Lyrics == ""`. New option field: `Options.Lyrics string`.

- [ ] **Step 1: Write the failing tests** — append to `internal/fakeplayer/providers_test.go`:

```go
func TestProviderTracksPlaylistParam(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.tracks", map[string]any{"provider": "navidrome", "playlist": "pl1"})
	if !strings.Contains(string(raw), `"Playlist One Track 1"`) || !strings.Contains(string(raw), `"total":3`) {
		t.Fatalf("playlist rows: %s", raw)
	}
	// id wins when both params present (live precedence, spec §4)
	raw = call(t, c, "provider.tracks", map[string]any{"provider": "navidrome", "id": "ar2", "playlist": "pl1"})
	if strings.Contains(string(raw), "Playlist One") || !strings.Contains(string(raw), "Album Three Track 1") {
		t.Fatalf("id must win over playlist: %s", raw)
	}
	if _, err := c.SubmitJob(context.Background(), "provider.tracks", map[string]any{"provider": "navidrome", "playlist": "nope"}); err == nil || !strings.Contains(err.Error(), "playlist not found (code 70)") {
		t.Fatalf("bogus playlist must fail like live: %v", err)
	}
	if _, err := c.SubmitJob(context.Background(), "provider.tracks", map[string]any{"provider": "navidrome"}); err == nil || !strings.Contains(err.Error(), "missing parameter: 'id'") {
		t.Fatalf("missing both must keep failing 'id': %v", err)
	}
}

func TestLyricsOp(t *testing.T) {
	f, err := Start(t.TempDir(), Options{Lyrics: `[{"start":0.5,"text":"line one"}]`})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw := call(t, ipc.NewConn(f.Path), "lyrics", nil)
	if !strings.Contains(string(raw), `"line one"`) {
		t.Fatalf("lyrics fixture: %s", raw)
	}
	g, err := Start(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := ipc.NewConn(g.Path).SubmitJob(context.Background(), "lyrics", nil); err == nil || !strings.Contains(err.Error(), "no lyrics found") {
		t.Fatalf("empty lyrics must fail like live: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**: `go test ./internal/fakeplayer/ -race -run 'TestProviderTracksPlaylistParam|TestLyricsOp' -v` → FAIL (playlist rows empty / lyrics op answers generic ok from the `default:` branch).

- [ ] **Step 3: Implement.** In `providers.go` replace the `provider.tracks` case body:

```go
	case "provider.tracks":
		// live precedence: id wins when both given; playlist drills playlists.
		if p.Playlist != "" && p.ID == "" {
			if p.Playlist == "nope" {
				failJob(c, id, op, "navidrome: playlist not found (code 70)")
				return true
			}
			b, _ := json.Marshal(paged(fixtureTracks("playlist", p.Playlist), p.Offset, p.Limit, "tracks"))
			ok(string(b))
			return true
		}
		if p.ID == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'id' (code 10)")
			return true
		}
		b, _ := json.Marshal(paged(fixtureTracks("artist", p.ID), p.Offset, p.Limit, "tracks"))
		ok(string(b))
```

(add the `return true` after the last `ok(string(b))` only if the surrounding cases don't already fall through to the shared `return true` at the switch end — match the file's existing case shape.)

add fixture cases inside `fixtureTracks`'s switch before its `}`:

```go
	case "playlist:pl1":
		out := make([]map[string]any, 0, 3)
		for i := range 3 {
			out = append(out, track(fmt.Sprintf("Playlist One Track %d", i+1), "Artist A", "/fake/pl1-"+strconv.Itoa(i)+".flac", 200+i))
		}
		return out
	case "playlist:pl-empty":
		return []map[string]any{}
```

In `fakeplayer.go`: add to `Options`:

```go
	// Lyrics is the raw lyrics result JSON for the lyrics op; "" ⇒ the
	// live "no lyrics found" failure.
	Lyrics string
```

add field `lyrics string` to `Fake`, set it in `Start` (where `rowTitle` is initialized: `f.lyrics = opts.Lyrics`), and in `submit` add a case before `default:`:

```go
	case "lyrics":
		if f.lyrics == "" {
			failJob(c, id, op, "no lyrics found")
			return
		}
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"jl\",\"operation\":\"lyrics\",\"state\":\"succeeded\",\"result\":{\"ok\":true,\"lyrics\":%s}}}\n", id, f.lyrics)
```

- [ ] **Step 4: Run package tests** `go test ./internal/fakeplayer/ -race -count=1` → ok; `gofmt -l . && go vet ./...` clean.
- [ ] **Step 5: Commit** `git add internal/fakeplayer && git commit -m "feat(fake): provider.tracks playlist param + lyrics op mirroring live cliamp"`

---

### Task 2: Bridge playlist-tracks route

**Files:**
- Modify: `internal/server/library.go` (add handler after `libraryArtistTracks`)
- Modify: `internal/server/server.go` (routes block, after the artist-tracks `mux.Handle` line)
- Test: `internal/server/library_test.go`

**Interfaces:**
- Consumes: `libParams`, `libraryGET`, `listCache`, test helpers `libConfig(t, fr)`, `libGet(t, h, path)`, `fakeRunner{job,lastOp,lastParams}` (existing).
- Produces: `GET /api/library/{provider}/playlist/{id}/tracks?offset&limit` → op `provider.tracks`, params `{provider, offset, limit, playlist}` — no `id` key ever. Cached in listCache. Task 5's api client calls this exact URL.

- [ ] **Step 1: Write the failing test** — append to `internal/server/library_test.go`:

```go
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
```

(Add `strings` import if the file lacks it; check `fakeRunner` has an `err` field — `TestLibraryUpstreamErrorMaps502WithDetail` already uses it.)

- [ ] **Step 2: Verify failure**: `go test ./internal/server/ -run 'TestPlaylist' -v` → both 404.
- [ ] **Step 3: Implement.** `library.go` add:

```go
func (s *serverState) libraryPlaylistTracks(w http.ResponseWriter, r *http.Request) {
	params, ok := libParams(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	// playlist-only by construction: this route cannot emit an id param.
	params["playlist"] = r.PathValue("id")
	s.libraryGET(w, r, s.cfg.listCache, "provider.tracks", params)
}
```

`server.go` routes: add next to the album/artist track routes:

```go
	mux.Handle("GET /api/library/{provider}/playlist/{id}/tracks", s.protect(http.HandlerFunc(s.libraryPlaylistTracks)))
```

- [ ] **Step 4: Full Go suite** `go test ./... -race -count=1` → ok.
- [ ] **Step 5: Commit** `git commit -am "feat(api): playlist tracks route -> provider.tracks {playlist}"`.

---

### Task 3: Lyrics route + named SSE ping

**Files:**
- Modify: `internal/server/library.go`, `internal/server/server.go` (route + Config + heartbeat method), `internal/server/handlers.go` (events heartbeat loop)
- Test: `internal/server/library_test.go`, `internal/server/sse_test.go`

**Interfaces:**
- Consumes: `noCache` instance, `fakeRunner`, `authedConfig(t)` + token `"deadbeef"`, `sseConnect(t, baseURL, tok)` + `sseClient.next(t, d)` (existing; `next` skips comment frames — only a NAMED ping satisfies the new test).
- Produces: `GET /api/lyrics` → op `lyrics`, params `{}` → verbatim `{"ok":true,"lyrics":[…]}` or 502 w/ detail `no lyrics found`; NEVER cached. `Config.HeartbeatInterval time.Duration` (0 ⇒ 15 s). SSE emits `event: ping\ndata: {}` every heartbeat (comment `: ping` kept for proxies). Task 6's watchdog depends on the named ping.

- [ ] **Step 1: Failing tests.** `library_test.go`:

```go
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
```

If `fakeRunner` (in `handlers_test.go`) has no `calls int` counter: add the field and `fr.calls++` as the first statement of its `SubmitJob`.

`sse_test.go`:

```go
func TestSSENamedPing(t *testing.T) {
	cfg := authedConfig(t)
	cfg.HeartbeatInterval = 50 * time.Millisecond
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := sseConnect(t, srv.URL, "deadbeef")
	defer c.close()
	// connect replays player+state first; a ping must follow within ~a
	// few 50 ms heartbeats:
	for i := 0; i < 4; i++ {
		if f := c.next(t, 2*time.Second); f.name == "ping" {
			return
		}
	}
	t.Fatal("no named ping frame within window")
}
```

- [ ] **Step 2: Verify failure**: `go test ./internal/server/ -run 'TestLyrics|TestSSENamedPing'` → 404s / no ping frame.
- [ ] **Step 3: Implement.** `library.go` add:

```go
func (s *serverState) libraryLyrics(w http.ResponseWriter, r *http.Request) {
	// never cached: lyrics follow whatever track is loaded right now
	s.libraryGET(w, r, s.cfg.noCache, "lyrics", map[string]any{})
}
```

`server.go`: add route with the other protects `mux.Handle("GET /api/lyrics", s.protect(http.HandlerFunc(s.libraryLyrics)))`; add to `Config`:

```go
	// HeartbeatInterval is the SSE keepalive cadence (0 ⇒ 15 s).
	HeartbeatInterval time.Duration
```

and next to `libraryTimeout()`:

```go
func (c *Config) heartbeat() time.Duration {
	if c.HeartbeatInterval == 0 {
		return 15 * time.Second
	}
	return c.HeartbeatInterval
}
```

`handlers.go` events loop: `hb := time.NewTicker(s.cfg.heartbeat())` and replace the heartbeat case body:

```go
		case <-hb.C:
			fmt.Fprint(w, ": ping\n\n")
			fmt.Fprint(w, "event: ping\ndata: {}\n\n")
			flusher.Flush()
```

- [ ] **Step 4: `go test ./... -race -count=1`** ok.
- [ ] **Step 5: Commit** `"feat(api): lyrics passthrough + named SSE ping"`.

---

### Task 4: CSS tokens, row layout, fixed tab bar + mini player dock

**Files:**
- Modify: `webui/src/style.css` (`:root` + `.wrap`)
- Modify: `webui/src/App.vue` (`.tabs`)
- Modify: `webui/src/components/MiniPlayer.vue` (`.mini`)
- Modify: `webui/src/components/BrowseView.vue` (track-row markup + styles)
- Modify: `webui/src/components/PlayNextList.vue`, `webui/src/components/NowPlayingHero.vue` (artist + ellipsis), `webui/src/api.ts` (`Track.artist` if missing)

**Interfaces:**
- Consumes: nothing new.
- Produces: defined vars `--surface`, `--muted`, `--error`, `--tabs-h: 3.1rem` on `:root`; flex `.row` contract reused by Task 5's new row kinds; fixed bottom chrome that Task 7's lyrics sheet must sit above (z-index 40).

- [ ] **Step 1: `style.css`** — append to `:root`:

```css
  --surface: #14161a;
  --muted: #8b93a0;
  --error: var(--err);
  --tabs-h: 3.1rem;
```

and change `.wrap` to `padding: 0.75rem 1rem calc(var(--tabs-h) + 1rem + env(safe-area-inset-bottom, 0px));`

- [ ] **Step 2: App.vue** — replace the `.tabs` rule:

```css
.tabs {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 30;
  display: flex;
  gap: 4px;
  max-width: 720px;
  margin: 0 auto;
  padding: 6px 1rem calc(6px + env(safe-area-inset-bottom, 0px));
  background: var(--panel);
  border-top: 1px solid var(--border);
}
```

- [ ] **Step 3: MiniPlayer.vue** — replace `.mini`:

```css
.mini {
  position: fixed;
  left: 50%;
  transform: translateX(-50%);
  bottom: calc(var(--tabs-h) + 0.5rem + env(safe-area-inset-bottom, 0px));
  width: min(720px, calc(100% - 2rem));
  z-index: 29;
  display: flex;
  align-items: center;
  gap: 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 8px 12px;
  background: var(--surface);
}
```

- [ ] **Step 4: BrowseView rows.** In the track-row `v-else` template branch replace the artist span with a separator-bearing one:

```html
          <span class="muted">{{ row.artist ? "· " + String(row.artist) : "" }}</span>
```

(repeat the same expression in the album `drill` branch only if it currently lacks the separator pattern; the album branch already renders `artist · year` — leave it). Replace style rules `.row`, `.drill`, `.t`, `.muted`, `.act` with:

```css
.row {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  border-radius: 8px;
  padding: 0.35rem 0.5rem;
}
.row:nth-child(odd) { background: #101216; }
.drill {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  flex: 1;
  min-width: 0;
  cursor: pointer;
  color: var(--fg);
}
.drill b { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.t { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--fg); }
.muted { color: var(--muted); font-size: 0.8rem; flex: none; max-width: 42%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.act { flex: none; font-size: 0.75rem; padding: 0.05rem 0.5rem; border-radius: 5px; color: var(--accent); }
```

The sentinel `<div ref="sentinel" class="sentinel">` stays the LAST child of `.rows` (Review Focus 6) — markup order untouched.
- [ ] **Step 5: PlayNextList + Hero up-next artists.** `PlayNextList.vue`: after the `.ttl` span insert `<span class="art">{{ t.artist ? "· " + String(t.artist) : "" }}</span>`; add scoped `.ttl { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; } .art { color: var(--muted); font-size: 0.8rem; flex: none; max-width: 38%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }`. `NowPlayingHero.vue` up-next `<li>` (the `{{ row.title }}` line ~224) becomes `<span class="t">{{ row.title }}</span><span class="muted">{{ row.artist ? "· " + String(row.artist) : "" }}</span>` with scoped copies of `.t`/`.muted` from Step 4. The `Track` interface in `api.ts` must carry `artist?: string` — add it there if absent; NEVER cast to reach it.
- [ ] **Step 6: Build + smoke**: `cd webui && npm run build && cd .. && make build`; serve; with the browser tool (if alive): Browse rows render one line each with muted `· artist`, tab bar stays pinned while scrolling, sentinel still pages the fake playlist list to 120. Without the browser: CSS inspection + explicit deferral note for Task 8 acceptance.
- [ ] **Step 7: Commit** incl. `webui/dist`: `"fix(ui): row layout with artists, fixed tab bar + mini dock, defined CSS vars"`.

---

### Task 5: Playlist drill-down + station tap-load, load labels deleted

**Files:**
- Modify: `webui/src/api.ts` (`libraryPlaylistTracks`)
- Modify: `webui/src/store.ts` (`Drill` kind, `LIST_FIELDS`, `fetchPage`, new `playDrill`/`tapCatalogRow`; DELETE `loadPlaylist` and `playAlbumDrill`)
- Modify: `webui/src/components/BrowseView.vue` (playlist drill rows, catalog tap rows, drill header, imports)
- Test: throwaway `/tmp/v3-drill.mjs` (bun; path-routed fetch stub — the v2 navrace pattern)

**Interfaces:**
- Consumes: Task 2 route URL verbatim; v2 `reqGen`/`browseMore` machinery unchanged.
- Produces: `libraryPlaylistTracks(provider: string, id: string, offset?: number, limit?: number) → Promise<ListReply>`; `Drill.kind = "album" | "artist" | "playlist"`; `playDrill(): void` (confirm, dispatch by head kind); `tapCatalogRow(row: api.BrowseRow): void` (confirm → `provider.load {provider, playlist: row.id}`); `loadPlaylist`/`playAlbumDrill` no longer exist anywhere (grep-clean).

- [ ] **Step 1: api.ts**:

```ts
export const libraryPlaylistTracks = (
  provider: string,
  id: string,
  offset = 0,
  limit = 50,
) =>
  req<ListReply>(
    `/api/library/${encodeURIComponent(provider)}/playlist/${encodeURIComponent(id)}/tracks?offset=${offset}&limit=${limit}`,
  );
```

- [ ] **Step 2: store.ts.** `Drill.kind` becomes `"album" | "artist" | "playlist"`; add `playlist: "tracks"` to `LIST_FIELDS`; in `fetchPage`'s reply branch chain insert after the album line:

```ts
    else if (kind === "playlist" && top) reply = await api.libraryPlaylistTracks(b.provider, top.id, offset);
```

delete `loadPlaylist` and `playAlbumDrill`, add:

```ts
// drill header action: replace the queue with the drilled collection.
export function playDrill() {
  const b = store.browse;
  const top = b.stack[b.stack.length - 1];
  if (!top) return;
  if (top.kind === "album") {
    if (!window.confirm(`replace the queue with album "${top.title}"?`)) return;
    runCommand("provider.load_album", { provider: b.provider, album: top.id });
  } else if (top.kind === "playlist") {
    if (!window.confirm(`replace the queue with playlist "${top.title}"?`)) return;
    runCommand("provider.load", { provider: b.provider, playlist: top.id });
  }
}

// radio catalog rows carry no path; stations play via provider.load only
// (spec §2) — whole-row tap behind the same confirm.
export function tapCatalogRow(row: api.BrowseRow) {
  if (!row.id) return;
  if (!window.confirm(`play station "${row.name ?? ""}"? this replaces the queue`)) return;
  runCommand("provider.load", { provider: store.browse.provider, playlist: row.id });
}
```

- [ ] **Step 3: BrowseView.** Import `playDrill, tapCatalogRow`; drop `loadPlaylist, playAlbumDrill` imports. Replace the combined playlists/catalog template branch (the one containing the `load` button) with two branches:

```html
        <template v-else-if="store.browse.tab === 'playlists' && !store.browse.stack.length">
          <div class="drill" @click="openDrill({ kind: 'playlist', id: row.id ?? '', title: String(row.name ?? '') })">
            <b>{{ row.name }}</b>
            <span class="muted">{{ row.track_count ?? "" }}</span>
          </div>
        </template>
        <template v-else-if="store.browse.tab === 'catalog' && !store.browse.stack.length">
          <div class="tap" @click="tapCatalogRow(row)">
            <b>{{ row.name }}</b>
            <span class="glyph">▶</span>
          </div>
        </template>
```

replace the album-head block with a kind-generic drill head:

```html
    <p v-if="headKind() && headKind() !== 'artist'" class="albumhead">
      <button class="act" :aria-label="'play ' + headKind()" @click.stop="playDrill()">
        ▶ play {{ headKind() === "album" ? "album" : "playlist" }}
      </button>
    </p>
```

add script helper + styles:

```ts
function headKind(): "" | "album" | "artist" | "playlist" {
  const s = store.browse.stack;
  return s.length ? s[s.length - 1].kind : "";
}
```

```css
.tap { display: flex; align-items: baseline; gap: 0.5rem; flex: 1; min-width: 0; cursor: pointer; color: var(--fg); }
.tap b { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.glyph { color: var(--accent); flex: none; }
```

- [ ] **Step 4: Throwaway harness** `/tmp/v3-drill.mjs` (pattern: stub `fetch` collecting `{path,res}` by path; `answer(path, obj)` resolves; provider fixture `[{key:"navidrome",name:"N",searchable:true,browse_albums:true,browse_artists:true,catalog:false},{key:"radio",name:"R",searchable:true,catalog:true}]`):
  (a) `pickProvider("navidrome")` → `pickTab("playlists")` → `openDrill({kind:"playlist",id:"pl1",title:"P"})` issues `/api/library/navidrome/playlist/pl1/tracks?offset=0&limit=50`; after `answer(..., {tracks:[T1,T2,T3], total:5})` rows paint 3; `browseMore()` → offset=3 request; paint 2 more; `browseMore()` → NO further request (total reached).
  (b) Review Focus 2: open drill pl1, then `pickProvider("radio")` BEFORE answering — answering the stale pl1 request must leave `store.browse.list` == radio catalog rows, never T1..T3.
  (c) confirm matrix: `window.confirm = () => false; playDrill()` (after openDrill playlist) → ZERO `/api/command` POSTs; `= () => true` → POST body contains `"op":"provider.load"` and `"playlist":"pl1"`; `tapCatalogRow({id:"c:1",name:"X"})` true → POST `"playlist":"c:1"`, false → no POST.
  Run from `webui/`: `bun /tmp/v3-drill.mjs`; all assertions print PASS lines.
- [ ] **Step 5:** `cd webui && npm run build` (vue-tsc clean — no `any`) **&& `cd .. && make build`**.
- [ ] **Step 6: Commit** incl. dist: `"feat(ui): playlist drill-down, station tap-load, no load labels"`.

---

### Task 6: SSE reconnect machine

**Files:**
- Modify: `webui/src/store.ts` (replace `connectEvents` with `startEventStream`)
- Modify: `webui/src/main.ts` (rename call)
- Test: throwaway `/tmp/v3-sse.mjs` (bun; fake EventSource, controllable clock)

**Interfaces:**
- Consumes: Task 3's named `ping` (watchdog frames); existing `refreshState`/`refreshQueue`/`notify`.
- Produces: `startEventStream(): void` replacing `connectEvents` (update `main.ts` import+call; grep shows no other callers). Backoff ladder `[1000,2000,5000,10000,30000]` ms; stale-35 s watchdog (5 s tick); visible+stale-5 s immediate reopen; at most one live EventSource; resync pair (`refreshState(); refreshQueue()`) on each fresh connection's first frame.

- [ ] **Step 1: store.ts** — delete `connectEvents`, add:

```ts
// --- SSE resilience (spec §5.4): one live EventSource, backoff reopen on
// error, watchdog reopen when frames go stale (iOS half-open sockets),
// immediate reopen when the tab becomes visible again.
const BACKOFF = [1000, 2000, 5000, 10000, 30000];
let es: EventSource | null = null;
let lastFrameAt = 0;
let backoffIdx = 0;
let reconnectTimer: number | undefined;
let resyncPending = true;

function touch() {
  lastFrameAt = Date.now();
  backoffIdx = 0;
  if (resyncPending) {
    resyncPending = false;
    void refreshState();
    void refreshQueue();
  }
}

function attach(e: EventSource) {
  const on = (name: string, fn: (data: string) => void) =>
    e.addEventListener(name, (ev) => {
      touch();
      fn((ev as MessageEvent).data);
    });
  on("state", (d) => Object.assign(store.snapshot, JSON.parse(d)));
  on("playback", (d) => Object.assign(store.snapshot, JSON.parse(d)));
  on("playlist", () => void refreshQueue());
  on("resync", () => {
    void refreshState();
    void refreshQueue();
    notify("state resynced");
  });
  on("player", (d) => {
    const was = store.online;
    store.online = JSON.parse(d).status === "online";
    if (was !== store.online) notify(store.online ? "player online" : "player offline");
  });
  on("ping", () => {});
  e.onopen = () => touch();
  e.onerror = () => {
    // Review Focus 1: the dying instance is ALWAYS closed, ours or not.
    e.close();
    if (es === e) es = null;
    scheduleReopen();
  };
}

function scheduleReopen() {
  if (reconnectTimer !== undefined || es !== null) return;
  resyncPending = true;
  const d = BACKOFF[Math.min(backoffIdx, BACKOFF.length - 1)];
  backoffIdx++;
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = undefined;
    open();
  }, d);
}

function forceReopen() {
  if (reconnectTimer !== undefined) {
    window.clearTimeout(reconnectTimer);
    reconnectTimer = undefined;
  }
  if (es) es.close();
  es = null;
  resyncPending = true;
  open();
}

function open() {
  const e = new EventSource(`/api/events?token=${encodeURIComponent(api.token)}`);
  es = e;
  attach(e);
}

export function startEventStream() {
  open();
  window.setInterval(() => {
    if (Date.now() - lastFrameAt > 35000 && reconnectTimer === undefined) forceReopen();
  }, 5000);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && Date.now() - lastFrameAt > 5000) forceReopen();
  });
}
```

`main.ts`: `import { ..., startEventStream } from "./store";` and `startEventStream();`.
- [ ] **Step 2: Throwaway harness** `/tmp/v3-sse.mjs`: stub `EventSource` as a class recording instances (`{closed, listeners, onopen, onerror}`, method `dispatch(name, data)`), stub `document.addEventListener`, and control time by replacing `Date.now` and intercepting `setTimeout`/`setInterval` into a manual queue the harness runs. Assert, in order: (1) after `startEventStream()`: exactly 1 instance, not closed; (2) `instances[0].onerror()` → that instance `closed === true`, no new instance yet, advance clock 1000 → instance #2 (Review Focus 1: never two unclosed); (3) `onerror` again → next open at +2000, again → +5000; (4) `dispatch("ping","{}")` on #4 then `onerror` → next delay back to 1000 (received frame resets ladder); (5) advance clock +40 s without errors → interval tick forces close+reopen, and the new instance's first dispatched `state` triggers exactly one `/api/state` and one `/api/queue` fetch each (resync pair). Run: `bun /tmp/v3-sse.mjs` → all PASS.
- [ ] **Step 3:** `cd webui && npm run build && cd .. && make build` clean.
- [ ] **Step 4: Commit** incl. dist: `"fix(ui): SSE backoff reconnect, ping watchdog, visible-reopen"`.

---

### Task 7: Lyrics sheet with active line + tap-seek

**Files:**
- Create: `webui/src/lib/lyrics.ts`
- Modify: `webui/src/api.ts` (`LyricLine`, `LyricsReply`, `getLyrics`)
- Modify: `webui/src/components/NowPlayingHero.vue` (button + sheet)
- Test: throwaway `/tmp/v3-lyrics.mjs`

**Interfaces:**
- Consumes: Task 3 `/api/lyrics` (via typed `req`); `runCommand("seek.absolute", { value })` (v2 op shape); `HttpError.status/detail`; Task 4's chrome (sheet overlays everything: z-index 40 > tabs 30 > mini 29).
- Produces: `lyricIndex(lines: LyricLine[], seek: number): number` (last `start <= seek`, −1 before all). Sheet state fully reset on close → reopen always refetches (spec §5.3).

- [ ] **Step 1: api.ts + lib/lyrics.ts.**

```ts
// api.ts
export interface LyricLine {
  start: number;
  text: string;
}
export interface LyricsReply {
  lyrics?: LyricLine[];
}
export const getLyrics = () => req<LyricsReply>("/api/lyrics");
```

```ts
// webui/src/lib/lyrics.ts
import type { LyricLine } from "../api";

// index of the line active at `seek` (last start <= seek), -1 before all.
export function lyricIndex(lines: LyricLine[], seek: number): number {
  let idx = -1;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].start <= seek) idx = i;
    else break;
  }
  return idx;
}
```

- [ ] **Step 2: Hero.** Script (typed; import `computed, ref, watch` from vue, `HttpError, getLyrics, type LyricLine` from `../api`, `lyricIndex` from `../lib/lyrics`):

```ts
const showLyrics = ref(false);
const lyricLines = ref<LyricLine[]>([]);
const lyricErr = ref("");
let lyricGen = 0;

async function openLyrics() {
  showLyrics.value = true;
  const gen = ++lyricGen; // Review Focus 3: drop replies after close/reopen
  lyricErr.value = "";
  lyricLines.value = [];
  try {
    const r = await getLyrics();
    if (gen !== lyricGen) return;
    lyricLines.value = r.lyrics ?? [];
  } catch (e: unknown) {
    if (gen !== lyricGen) return;
    lyricErr.value = e instanceof HttpError ? e.detail || `http ${e.status}` : "unreachable";
  }
}
function closeLyrics() {
  lyricGen++;
  showLyrics.value = false;
  lyricLines.value = [];
  lyricErr.value = "";
}
const seekPos = computed(() => Number(snap.value.seek_position ?? 0));
const activeIdx = computed(() => lyricIndex(lyricLines.value, seekPos.value));
watch(activeIdx, (i) => {
  if (!showLyrics.value || i < 0) return;
  document
    .querySelector(`.lyrics li[data-i="${i}"]`)
    ?.scrollIntoView({ block: "center", behavior: "smooth" });
});
function seekTo(line: LyricLine) {
  runCommand("seek.absolute", { value: line.start });
}
```

Template — add next to the transport controls `<button class="lyr-btn" :disabled="!snap.track?.path" @click="openLyrics">lyrics</button>` and at the end of the component root:

```html
    <div v-if="showLyrics" class="lyrics-bg" @click="closeLyrics">
      <div class="lyrics" @click.stop>
        <p v-if="lyricErr" class="muted">{{ lyricErr === "no lyrics found" ? "no lyrics for this track" : lyricErr }}</p>
        <p v-else-if="!lyricLines.length" class="muted">no lyrics for this track</p>
        <ul v-else>
          <li v-for="(line, i) in lyricLines" :key="i" :data-i="i" :class="{ on: i === activeIdx }" @click="seekTo(line)">
            {{ line.text }}
          </li>
        </ul>
      </div>
    </div>
```

Styles:

```css
.lyrics-bg { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.65); z-index: 40; display: flex; align-items: flex-end; }
.lyrics { background: var(--panel); border-top: 1px solid var(--border); border-radius: 12px 12px 0 0; width: 100%; max-width: 720px; margin: 0 auto; max-height: 70dvh; overflow-y: auto; padding: 1rem 1rem calc(1rem + env(safe-area-inset-bottom, 0px)); }
.lyrics li { padding: 0.3rem 0.4rem; color: var(--muted); border-radius: 6px; }
.lyrics li.on { color: var(--fg); background: #1c1f25; }
```

(`.muted` scoped copy if Hero lacks it: `color: var(--muted)`.)
- [ ] **Step 3: Throwaway harness** `/tmp/v3-lyrics.mjs`: (i) `lyricIndex`: `[]`→−1; `[{start:5}] @ 4.9`→−1; `@5`→0; mid→previous; past-end→last. (ii) stale-reply guard: replicate the openLyrics/closeLyrics generation logic with an injectable async fetch resolving after 10 ms; call `openLyrics()`, then `closeLyrics()` at 5 ms, then resolve → asserted `lyricLines` stays `[]` (dropped). (iii) reopen-during-flight: open, close, open (second fetch also in flight); resolve first late, second early → final lines come from the SECOND reply only. Run `bun /tmp/v3-lyrics.mjs` → PASS lines.
- [ ] **Step 4:** `cd webui && npm run build && cd .. && make build`; commit incl. dist `"feat(ui): synced lyrics sheet with tap-seek"`.

---

### Task 8: Docs, full verification, live acceptance prep

**Files:**
- Modify: `README.md` (v3 bullets in the feature section)

**Interfaces:** Consumes everything; produces the shippable tree + acceptance checklist for the final report.

- [ ] **Step 1: README** — extend the v2 “What the phone can do” list with:

```markdown
- Tap a playlist to page through its real tracks; ▶ plays any one,
  “play playlist” (confirm) replaces the queue
- Radio stations start on tap (confirm) — no load buttons anywhere
- Lyrics sheet on the Now Playing page, current line follows the track,
  tap a line to jump there
- The tab bar and mini-player stay pinned; rows read “title · artist”
  with ellipsis; the live connection reconnects by itself (lid close,
  bridge restart, iOS sleep)
```

- [ ] **Step 2: Battery**:

```bash
go test ./... -race -count=1 && gofmt -l . && go vet ./...
cd webui && npm run build && cd .. && make build
```

All green; `webui/dist` rebuilt at HEAD.
- [ ] **Step 3: Live acceptance (user, phone — report as pending, never claim):** (1) a 12k-track playlist pages past 200 rows without runaway fetches; (2) lyrics line highlight follows playback; tapping a late line jumps; (3) pause in the TUI updates the phone < 1 s; close laptop lid ~2 min, reopen → updates still arrive (reconnect machine); (4) tab bar never scrolls away; (5) zero “load” labels anywhere.
- [ ] **Step 4: Refresh dev binary** `cp dist/cliamp-web ~/.local/bin/cliamp-web` (carried v2 instruction; note it in the report — plugin autostart needs a cliamp restart to pick it up).
- [ ] **Step 5: Commit** `"docs: v3 feature set"`.

---

## Plan self-review checklist (run at final review, before merge)

1. Every spec §1–§5 deliverable maps to a task: fixes→4/5/6, drill→2/5, lyrics→3/7, fake→1, docs→8.
2. No placeholder steps; every code step carries code.
3. Cross-task names match: `libraryPlaylistTracks`, `playDrill`, `tapCatalogRow`, `startEventStream`, `lyricIndex`, `Options.Lyrics`, `Config.HeartbeatInterval`, `--tabs-h`.
4. Review Focus coverage: 1→T6 harness, 2→T5 harness (b), 3→T7 harness (ii/iii), 4→T2 Go test, 5→T1 test, 6→T4 step 6.
