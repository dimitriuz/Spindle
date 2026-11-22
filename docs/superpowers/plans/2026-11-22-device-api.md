# Device API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a small device (ESP32-P4 touch remote, no browser) show Now Playing with live updates and browse the library, through a WebSocket, compact list responses and sized JPEG art, all documented in `docs/api.md`.

**Architecture:** The WebSocket handler subscribes to the existing `Hub` like the SSE stream and translates its frames into compact `status` / `queue` / `resync` frames built from `snapshot.Cache`. `?compact=1` is applied at write time, after the existing provider cache, by pure converter functions. `?size=N` re-encodes the image the art handlers already found, with `art.Thumb`.

**Tech Stack:** Go 1.23 stdlib, `github.com/coder/websocket` (the only new dependency), `net/http/httptest` for tests.

**Spec:** `docs/superpowers/specs/2026-11-22-device-api-design.md`

## Global Constraints

- Nothing the web page uses changes: `/api/events`, the full (non-compact) responses and `webui/` stay byte-for-byte as they are.
- No cgo; `make release` targets (linux amd64/arm64/armv7, macOS) must still build.
- Compact list item fields are exactly `id`, `title`, `subtitle`, `kind`, `playable`, `has_art`; paging fields `offset`, `limit`, `total` are kept.
- `?size=N` is clamped to 32..512; output is always JPEG.
- All new endpoints go through `s.protect(...)`; the `allowedOps` list is not extended.
- Stream URLs stay scrubbed (`urls.Scrub`, already applied by `writeJSON` and `Hub.Broadcast`); WebSocket frames must be scrubbed the same way.
- Tests follow the existing style in `internal/server/*_test.go` (fake player from `internal/fakeplayer`).

## Review Focus

- Player offline when a WebSocket client connects: it gets `status` with `online:false` and an empty track, not an error or a hang.
- A queue of 5000 rows: the `queue` frame carries at most 200 rows plus `count`; the client pages with `GET /api/queue?compact=1&offset=&limit=`.
- Provider returns an item with missing optional fields (no artist, no `track_count`): compact item has empty `subtitle`, never `null` or a missing key.
- `?size=` given as `0`, `-5`, `abc` or `99999`: 400 for non-numbers, clamped for out-of-range numbers, never a panic or a huge allocation.
- A slow or vanished WebSocket client: the handler returns after the write deadline and frees its Hub slot, and cliamp's event loop is never blocked.

---

## File map

- Create `internal/server/compact.go` (+ `compact_test.go`): pure converters to the compact item shape.
- Create `internal/server/status.go` (+ `status_test.go`): builds `status` and `queue` frames from cache state.
- Create `internal/server/ws.go` (+ `ws_test.go`): `GET /api/ws`.
- Create `internal/server/sizedart.go` (+ `sizedart_test.go`): `?size=` parsing and resize with a small cache.
- Modify `internal/server/library.go`, `files.go`, `handlers.go` (queue), `art.go`, `folderart.go`, `server.go`: wiring.
- Create `docs/api.md`; modify `README.md` (link only).

### Task 1: Compact item converters

**Files:**
- Create: `internal/server/compact.go`
- Test: `internal/server/compact_test.go`

**Interfaces:**
- Produces:
  - `type CompactItem struct { ID string "json:\"id\""; Title string "json:\"title\""; Subtitle string "json:\"subtitle\""; Kind string "json:\"kind\""; Playable bool "json:\"playable\""; HasArt bool "json:\"has_art\"" }`
  - `func compactList(hint string, raw json.RawMessage) (json.RawMessage, error)`: `hint` is one of `album|artist|playlist|track|station`. It finds the first of the array keys `albums`, `artists`, `playlists`, `tracks` in the provider result, replaces the whole result with `{"items":[CompactItem...],"total":N,"offset":O,"limit":L}` (`offset`/`limit` copied from the result if present, else 0). `total` falls back to `len(items)`.
  - `func compactFiles(l files.Listing) json.RawMessage`: `{"path","parent","name","items":[...]}`; `id` is `path + "/" + name` (no leading slash when path is empty), `kind` is `folder` or `file`, `playable` true, `has_art` true for folders only.
  - `func compactQueue(q snapshot.Queue, offset, limit int) json.RawMessage`: `{"items":[...],"count":q.Count,"offset":..,"limit":..,"playlist_revision":..}`; `kind` is `track`, `id` is the track `path`.

- [ ] **Step 1: Write failing tests** in `compact_test.go`, using the fixtures' shapes (`internal/fakeplayer/providers.go`): `TestCompactAlbums` (`{"ok":true,"total":3,"albums":[{"id":"al1","name":"Album One","artist":"Artist A","year":1999,"track_count":7}]}` gives one item `{id:"al1",title:"Album One",subtitle:"Artist A",kind:"album",playable:true,has_art:true}`, `total:3`); `TestCompactArtists` (subtitle `"2 albums"` for `album_count` 2, `"1 album"` for 1); `TestCompactPlaylistsAndStation` (hint `station` gives kind `station`, subtitle empty); `TestCompactTracks` (id = `path`, subtitle = artist); `TestCompactMissingFields` (item with only `id` gives `"subtitle":""` and all six keys present when marshalled); `TestCompactFiles` (folder and file entries as described); `TestCompactQueueWindow` (5 tracks, offset 1, limit 2 gives 2 items, `count` 5).
- [ ] **Step 2: Run** `go test ./internal/server -run Compact -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the three functions and `CompactItem` in `compact.go`. Decode with `map[string]json.RawMessage` plus small row structs; never emit `null` for items (use an empty slice).
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat: compact list item converters`.

### Task 2: `?compact=1` on library, search, files and queue

**Files:**
- Modify: `internal/server/library.go` (`libraryGET` gains a `hint string` parameter; each caller passes its hint: `libraryList` uses `album`/`artist`/`playlist`/`station` by `kind` path value, album/artist/playlist track routes and search pass `track`, `libraryArtistAlbums` passes `album`; providers and lyrics pass `""` and are never compacted), `internal/server/files.go` (`filesList`), `internal/server/handlers.go` (`queue`)
- Test: `internal/server/compact_http_test.go`

**Interfaces:**
- Consumes: `compactList`, `compactFiles`, `compactQueue` from Task 1.
- Produces: `func wantCompact(r *http.Request) bool` (true only for `compact=1`); `GET /api/queue` also accepts `offset` and `limit` (defaults 0 and 200, limit clamped to 1..200, bad values give 400 `invalid_request`) when compact.

- [ ] **Step 1: Write failing tests** using the existing test server helpers (see `library_test.go`): `TestLibraryAlbumsCompact` (`/api/library/navidrome/albums?compact=1` returns `items[0].kind=="album"`, no `albums` key; without the parameter the response still has `albums`); `TestSearchCompactTracks`; `TestFilesCompact`; `TestQueueCompactWindow` (`/api/queue?compact=1&offset=1&limit=2`); `TestQueueCompactBadLimit` (`limit=abc` gives 400); `TestProvidersNeverCompact` (`/api/library/providers?compact=1` unchanged).
- [ ] **Step 2: Run** `go test ./internal/server -run 'Compact|Providers' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the wiring. Compacting happens after the provider cache lookup so the cached value stays the raw result; a conversion error is a 500 `internal_error`.
- [ ] **Step 4: Run** `go test ./internal/server -v`. Expected: all PASS, including the untouched existing tests.
- [ ] **Step 5: Commit** `feat: ?compact=1 on browse, search, files and queue`.

### Task 3: `?size=N` on art endpoints

**Files:**
- Create: `internal/server/sizedart.go`
- Modify: `internal/server/art.go` (`art`, `itemArt`, `stationArt`), `internal/server/folderart.go`
- Test: `internal/server/sizedart_test.go`

**Interfaces:**
- Produces:
  - `func parseSize(r *http.Request) (n int, set bool, ok bool)`: absent gives `(0,false,true)`; non-integer gives `ok=false`; integers are clamped to 32..512.
  - `func (s *serverState) sizedImage(img *art.Image, n int) (*art.Image, error)`: returns `art.Thumb(img.Data, n)` as a new `art.Image` with `ContentType:"image/jpeg"` and `Key: img.Key + "@" + strconv.Itoa(n)`, memoized in a bounded map (64 entries, evict oldest) on `serverState`.
  - The four art handlers call both before `writeImage`; `ok=false` is 400 `invalid_request`; a resize failure falls back to 404 `no_art`.

- [ ] **Step 1: Write failing tests:** `TestParseSize` (table: `""`, `"abc"`, `"0"` clamps to 32, `"-5"` clamps to 32, `"99999"` clamps to 512, `"128"`); `TestArtSized` (cover endpoint with `size=64` returns JPEG whose long side is at most 64, with a different ETag from the unsized one); `TestArtSizeInvalid` (`size=abc` gives 400); `TestSizedImageMemoized` (second call returns the same bytes without calling `Thumb` again: assert by pointer equality of `Data`).
- [ ] **Step 2: Run** `go test ./internal/server -run 'Size|Sized' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** as described.
- [ ] **Step 4: Run** `go test ./internal/server ./internal/art -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: ?size= on art endpoints`.

### Task 4: Status and queue frame builders

**Files:**
- Create: `internal/server/status.go`
- Test: `internal/server/status_test.go`

**Interfaces:**
- Consumes: `snapshot.Cache` (`State()`, `Queue()`, `Online()`), `compactQueue` from Task 1.
- Produces:
  - `func buildStatus(state json.RawMessage, online bool) []byte` returns `{"type":"status","online":bool,"state":"playing|paused|stopped","title","artist","album","position","duration","volume","shuffle","repeat","index","seekable","art"}`. `art` is the current track `path` (clients request `/api/art/cover?path=<art>&size=N`), empty when there is no track. Missing numeric fields are `0`, missing strings `""`. Field names of the source state are the ones the web page reads: `state`, `track{title,artist,album,path}`, `position`, `duration`, `volume`, `shuffle`, `repeat`, `index`, `seekable`; confirm against `GET /api/state` of a running cliamp before finalizing and record any difference in a code comment.
  - `func buildQueueFrame(q snapshot.Queue) []byte` returns `{"type":"queue",...compactQueue(q,0,200)...}`.
  - `const queueFrameRows = 200`.

- [ ] **Step 1: Write failing tests:** `TestBuildStatusPlaying` (state JSON from the fake player with a track gives the exact expected JSON); `TestBuildStatusOffline` (`online=false`, `state` `{}` gives `online:false`, `art:""`, zeros, no error); `TestBuildStatusNoNulls` (partial state never produces `null`); `TestBuildQueueFrameCap` (5000-row queue gives 200 items, `count` 5000).
- [ ] **Step 2: Run** `go test ./internal/server -run 'BuildStatus|BuildQueue' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with a small struct decode of the state (unknown fields ignored).
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat: compact status and queue frames`.

### Task 5: WebSocket `GET /api/ws`

**Files:**
- Create: `internal/server/ws.go`
- Modify: `internal/server/server.go` (route `mux.Handle("GET /api/ws", s.protect(http.HandlerFunc(s.ws)))`), `go.mod`/`go.sum` (`go get github.com/coder/websocket`)
- Test: `internal/server/ws_test.go`

**Interfaces:**
- Consumes: `Hub.Subscribe/Unsubscribe/TakeDropped`, `Frame{Name,Data}`, `buildStatus`, `buildQueueFrame`, `writeTimeout`, `Config.heartbeat()`.
- Produces: `func (s *serverState) ws(w http.ResponseWriter, r *http.Request)`.
  - Accept with `websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: false, OriginPatterns: nil})` so the library's same-host Origin check applies; clients with no Origin (ESP32) are accepted.
  - On connect send `status` then `queue`.
  - Hub frame mapping: `state`, `settings`, `player` send `status`; `playback` sends `status` at most once per second (drop-extra, the latest wins); `playlist` sends `queue`; `resync` and a `TakeDropped` hit send `{"type":"resync"}`; other names (`job`, `enqueue`, `favorites`) are ignored.
  - Heartbeat `{"type":"ping"}` at `Config.heartbeat()`; each write uses a `writeTimeout` context; the read loop discards client messages and answers the library's ping/pong; any error returns and unsubscribes.

- [ ] **Step 1: Write failing tests** (dial with `websocket.Dial` against `httptest.NewServer(New(cfg))`): `TestWSInitialFrames` (first two frames are `status` then `queue`); `TestWSStatusOnState` (`hub.Broadcast("state", ...)` yields a `status` with the new title); `TestWSPlaybackThrottled` (20 `playback` broadcasts within 100 ms yield at most 2 `status` frames); `TestWSQueueOnPlaylist`; `TestWSOfflineStatus` (cache offline gives `online:false`); `TestWSResyncOnDrop` (fill the client buffer without reading, then read: a `resync` frame arrives); `TestWSIgnoresJobFrames`; `TestWSNoOriginAccepted` and `TestWSForeignOriginRejected`; `TestWSUnsubscribesOnClose` (`Hub.Count()` returns to 0 after the client closes).
- [ ] **Step 2: Run** `go test ./internal/server -run WS -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `ws`, plus the one-second throttle as a local `time.Timer` pattern inside the handler (no shared state).
- [ ] **Step 4: Run** `go test ./internal/server -race -run WS -v`. Expected: PASS with no race reports. Then `go test ./... ` and `make release` (or `GOOS=linux GOARCH=arm GOARM=7 go build ./cmd/spindle` and `GOOS=darwin GOARCH=arm64 go build ./cmd/spindle`) to confirm the cross builds.
- [ ] **Step 5: Commit** `feat: /api/ws WebSocket with compact status and queue frames`.

### Task 6: API reference and README link

**Files:**
- Create: `docs/api.md`
- Modify: `README.md` (the "HTTP API (for scripts)" section: one line linking to `docs/api.md`, plus rows for `GET /api/ws`, `?compact=1` and `?size=` in the table)

- [ ] **Step 1: Write `docs/api.md`** with these sections: Overview (base URL, no login, `Content-Type: application/json` on writes, no `Origin`), Errors (`code` values: `invalid_request`, `forbidden_operation`, `rate_limited`, `upstream_error`, `job_timeout`, `unavailable`, `not_found`, `no_art`), Now playing (`GET /api/state`), WebSocket protocol (frame shapes from Tasks 4 and 5 with a sample of each, throttling, resync rule, reconnect advice), Commands (`POST /api/command` with the allowed ops list taken from `allowedOps`), Browsing (providers, lists, drill-down, search, files; paging; `?compact=1` shape with a sample), Queue (`GET /api/queue`, compact window), Art (`/api/art/{cover|disc|artist}`, item, station, files art; `size` clamp 32..512, JPEG), Enqueue and favorites, SSE (for browsers), ESP32 notes (JPEG decode on the P4 hardware decoder, recommended sizes 64 for list rows and 320 to 480 for Now Playing, cJSON buffer sizing from the compact shapes, `esp_websocket_client` reconnect and ping interval, only one WebSocket needed), curl and websocat examples. Every sample response is copied from a real run against the fake player or a live bridge, not invented.
- [ ] **Step 2: Verify the samples:** run `go run ./cmd/spindle serve` against the fake player if available (see `internal/fakeplayer`), or a live bridge, and diff each documented sample against the actual response. Expected: matching field names.
- [ ] **Step 3: Edit `README.md`** as described.
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `docs: device API reference`.
