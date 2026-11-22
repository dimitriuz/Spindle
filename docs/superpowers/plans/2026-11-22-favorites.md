# Favorites Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Heart any track, album, artist, playlist or radio station; one Favorites tab lists them; tracks and stations are also favorited in cliamp.

**Architecture:** A new `internal/favorites` service merges three sources: cliamp's Favorites playlist (tracks), cliamp's `f:` radio rows (stations), and `favorites.json` (albums, artists, playlists). Writes to cliamp are read-then-toggle under one lock. The server exposes `GET/PUT /api/favorites` plus an SSE `favorites` event; the page renders hearts from the listing and has a fifth tab.

**Tech Stack:** Go stdlib; vanilla JS/CSS.

**Spec:** `docs/superpowers/specs/2026-11-22-favorites-design.md`

## Global Constraints

- cliamp ops `playlist.bookmark` and `provider.favorite` are called only by the bridge; they are NOT added to the `/api/command` allowlist.
- Tracks: `playlist.bookmark {provider, track}` where provider is the prefix of the `provider_meta` key (`navidrome.id` -> `navidrome`), else `local`; state read from `provider.tracks {provider:"local", playlist:"Favorites"}` (paged, `limit` 200).
- Stations: `provider.favorite {provider:"radio", playlist:id}`; list from `provider.playlists {provider:"radio"}` rows with `favorite:true` (id `f:<stream url>`); a catalog row `c:N` is favorited iff its name starts with `"★ "`; unknown station -> error, never retried.
- `favorites.json` holds only `{kind, provider, id, title, subtitle, added}`; no credentials, atomic write, tolerant of a missing or corrupt file (starts empty, keeps a `.bad` copy).
- Listing cached 30 s and dropped on every change; one mutex serialises all changes.
- Track paths in responses go through the existing credential scrubbing; incoming track objects get `urls.Restore` on their `path`.
- Existing gates stay green: `make test`, `go test -race ./...`.

## Review Focus

- Two taps in quick succession must not leave the heart inverted (read-then-toggle under a lock, confirm after). (Task 1)
- Already in the wanted state: no cliamp call at all. (Task 1)
- cliamp offline/erroring: the call fails with an error, the bridge list is untouched. (Task 1, 2)
- Corrupt `favorites.json`: bridge starts, list works. (Task 1)
- Heart state after a failed PUT is rolled back and a toast shown. (Task 4)

---

### Task 1: favorites service

**Files:**
- Create: `internal/favorites/favorites.go`, `internal/favorites/favorites_test.go`

**Interfaces:**
- Consumes: `ipc.OpRunner` (`SubmitJob(ctx, op, params) (*ipc.Job, error)`).
- Produces:
  ```go
  type Kind string // "track","album","artist","playlist","radio"
  type Item struct { Kind Kind `json:"kind"`; Provider string `json:"provider"`; ID string `json:"id"`; Title string `json:"title"`
      Subtitle string `json:"subtitle,omitempty"`; Added time.Time `json:"added,omitzero"`; Track json.RawMessage `json:"track,omitempty"` }
  type SetRequest struct { Kind Kind; Provider, ID, Title, Subtitle string; Favorite bool; Track map[string]any }
  func TrackKey(track map[string]any) (provider, id string) // provider_meta "<prov>.id" else ("local", path)
  type Service struct{ /* unexported */ }
  func New(dir string, r ipc.OpRunner, restore func(string) string, now func() time.Time) (*Service, error)
  func (s *Service) List(ctx context.Context) ([]Item, error)
  func (s *Service) Set(ctx context.Context, q SetRequest) (bool, error) // returns the state after the call
  ```
  List order: albums, artists, playlists (each newest first), then stations, then tracks (cliamp order reversed).

- [ ] **Step 1: Write failing tests** with a fake runner that models cliamp (a favorites playlist and radio rows, both toggle-only, plus counters per op and an optional error): `TestTrackFavoriteReadsThenToggles` (not favorited -> one `playlist.bookmark`; asking again for true -> zero calls; false -> one call); `TestConcurrentSetsDoNotDoubleToggle` (20 goroutines asking for true -> exactly one toggle); `TestSetReportsStateAfterConfirm` (cliamp ignores the toggle -> returns the real state, no loop); `TestStationFavoriteByCatalogRow` (`c:2` favored iff name has `★ `; toggles `provider.favorite` with that id; `f:` id with Favorite=false toggles, true is a no-op); `TestUnknownStationErrorSurfaces`; `TestAlbumArtistPlaylistFileRoundTrip` (set true twice = one entry, false removes; reload from disk); `TestCorruptFileStartsEmptyAndKeepsBad`; `TestListMergesSourcesInOrder`; `TestListPagesCliampFavorites` (450 tracks -> all returned); `TestTrackKey` (navidrome id, local path, missing meta); `TestTrackPathIsRestoredBeforeBookmark`; `TestCliampErrorIsReturnedAndNothingCached`; `TestListCachedFor30sAndDroppedOnSet`.
- [ ] **Step 2: Run** `go test ./internal/favorites` — expect FAIL (package missing).
- [ ] **Step 3: Implement** per the constraints; fresh read (no cache) inside `Set` for tracks and stations.
- [ ] **Step 4: Run** `go test -race ./internal/favorites` — expect PASS.
- [ ] **Step 5: Commit** `feat(favorites): service over cliamp favorites and a bridge file`.

### Task 2: API, SSE, wiring

**Files:**
- Create: `internal/server/favorites.go`, `internal/server/favorites_test.go`
- Modify: `internal/server/server.go` (routes, `Config.Favorites`), `cmd/cliamp-web/main.go`, `cmd/cliamp-web/wiring_test.go`, `internal/fakeplayer/providers.go` (+ test) to model cliamp favorites.

**Interfaces:**
- Consumes: Task 1.
- Produces: `type FavoritesSource interface { List(ctx) ([]favorites.Item, error); Set(ctx, favorites.SetRequest) (bool, error) }`; `GET /api/favorites` -> `{items:[...]}` (500-free: errors give `{items:[], error:"unavailable"}` with 503 only when cliamp is unreachable and nothing is cached); `PUT /api/favorites` body `{kind, provider, id, favorite, title?, subtitle?, track?}` -> `{ok:true, favorite:bool}`; 400 on unknown kind or missing id; after a successful PUT `Hub.Broadcast("favorites", {})`.

- [ ] **Step 1: Write failing tests:** list JSON shape and scrubbing of a Navidrome `track.path`; PUT sets and broadcasts exactly one `favorites` frame; PUT with a `path` containing a scrubbed URL restores it before the service call; 400s; non-JSON content type -> 415; foreign Origin -> 403; `playlist.bookmark` and `provider.favorite` posted to `/api/command` -> 403 and no runner call; nil service -> 404. Wiring test: fake player models favorites; PUT then GET round trip for a track, a station and an album.
- [ ] **Step 2: Run** `go test ./...` — expect FAIL.
- [ ] **Step 3: Implement.** `main.go` builds the service with `DataDir`, the IPC conn, `server.RestoreURL`.
- [ ] **Step 4: Run** `go test -race ./...` — expect PASS.
- [ ] **Step 5: Commit** `feat: /api/favorites with SSE change event`.

### Task 3: pure page functions

**Files:** Modify `webui/app.js`, `webui/app_test.js`.

**Interfaces (exported for node tests):**
```js
favKey(kind, provider, id)        // 'album:navidrome:al1'
trackFavKey(track)                // 'track:navidrome:<id>' from provider_meta, else 'track:local:<path>'
favKeys(items)                    // Set of keys for a /api/favorites item list (stations: 'radio:radio:<f: id>')
favFilter(items, kind)            // kind 'all' returns items; otherwise those with that kind
favCounts(items)                  // {all, track, album, artist, playlist, radio}
isPlayingFavStation(items, track) // true when a radio item's id is 'f:' + track.path
```
- [ ] **Step 1: Write failing node tests** for each (keys for all five kinds, local path track, meta-less track, filter/counts with empty list, station match true/false, tolerant of null).
- [ ] **Step 2:** `node webui/app_test.js` — expect FAIL. **Step 3:** implement and export. **Step 4:** expect PASS.
- [ ] **Step 5: Commit** `feat(webui): favorites helpers`.

### Task 4: hearts, Favorites tab, sync

**Files:** Modify `webui/index.html`, `webui/app.css`, `webui/app.js`.

- [ ] **Step 1: Markup/CSS/icons.** Heart and filled-heart icons; `heart` button styles (44 px target, accent when on); fifth tab `<a href="#favorites" data-tab="favorites">` and `<section id="favorites" class="view">`; `TABS` gains `'favorites'`; kind chips reuse `.seg`.
- [ ] **Step 2: State.** `favs` (items) and `favOn` (Set from `favKeys`) loaded at start, on the `favorites` SSE event, on resync, when the tab opens, and every 60 s while visible; `toggleFav(req)` updates `favOn` optimistically, repaints every `[data-fav-key]`, PUTs, rolls back with a toast on failure and reloads on success. Catalog station rows keep their own local state (initial from `row.favorite` or a `★ ` name) because their `c:N` ids do not appear in the listing.
- [ ] **Step 3: Hearts.** `item()` accepts `o.heart` (button before the more button); used for browse rows (playlists, albums, artists, catalog stations), track rows (search results, album tracks, artist albums, favorites tab), and `head(...)` extras for album / artist / playlist pages; a heart beside the title on Now Playing and in fullscreen for the playing track (radio: only when `isPlayingFavStation`).
- [ ] **Step 4: Favorites tab.** Chips from `favCounts` (hide empty kinds except All), rows: track plays, album/artist/playlist opens `#browse/<provider>/<kind>s/<kind>/<id>`, station plays with the catalog confirm (`provider.load {provider:'radio', playlist:id}`); empty state text.
- [ ] **Step 5: Run** `make test`. **Step 6: Browser verification** against the Pi (iPhone 402x874 and 1280x720): hearts on each row type, toggle round trip, rollback on a forced failure, Favorites tab chips and actions, Now Playing/fullscreen heart, two pages syncing through SSE, no console errors. Screenshots read and checked.
- [ ] **Step 7: Commit** `feat(webui): favorites hearts and tab`.

### Task 5: docs, deploy, live check

**Files:** Modify `README.md`.

- [ ] **Step 1:** README "Favorites" section (what is stored where, radio limit, refresh behaviour).
- [ ] **Step 2:** Deploy to the Pi. **Step 3:** Live round trip for a Navidrome track (cliamp Favorites and the server star both appear and are removed again), a station, an album, an artist, a playlist; leave cliamp favorites and Navidrome stars exactly as found; `favorites.json` contains no credentials.
- [ ] **Step 4:** `make test`, `go test -race ./...`; commit `docs: favorites`.

---

## Self-review

- **Spec coverage:** five kinds (T1), sync into cliamp (T1), merged list/API/SSE (T2), hearts/tab/Now Playing/radio rule (T3-T4), 60 s refresh (T4), no credentials and op allowlist (T2, constraints), live check that restores state (T5).
- **Types:** `Item`, `SetRequest`, `TrackKey` defined in T1, used by T2; JS key helpers defined in T3, used in T4; endpoint shapes identical in T2 and T4.
- **Proportion:** signatures, constraints and assertions; no bodies.
