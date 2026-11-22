# Now Playing Art Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show cover, disc (flip + realistic spin) and artist art on the Now Playing page for Navidrome and local-file tracks.

**Architecture:** A new `internal/art` package resolves `(kind, track path)` to an image from Navidrome (Subsonic `getCoverArt`) or local files, behind a disk cache. `GET /api/art/{kind}` exposes it. The page loads images on track change and animates the disc with a pure, node-tested physics step.

**Tech Stack:** Go 1.23 stdlib + `github.com/dhowden/tag` (embedded pictures); vanilla JS + Web Animations API.

**Spec:** `docs/superpowers/specs/2026-11-22-now-playing-art-design.md`

## Global Constraints

- cliamp is untouched; art comes only from the bridge. Radio and other providers: no art, placeholder stays.
- No outside service is ever contacted; the only network target is the Navidrome host inside the track's own stream URL.
- Credentials (`u`,`s`,`t`) never reach the browser, logs, cache keys or 404 bodies.
- Disk cache under `<data-dir>/art/`, capped at 200 MB (oldest first); misses remembered 10 min in memory; upstream timeout 10 s; images at most 10 MB and must sniff as `image/*`.
- Navidrome ids: cover `al-<albumId>` (`size=600`), disc `dc-<albumId>:1` (original size), artist `ar-<artistId>` (`size=600`); album/artist ids from `getSong`.
- Local lookups: cover = embedded picture, else `cover|folder|front|album` + `.jpg|.jpeg|.png|.webp`; disc = `disc.png`/`cdart.png`; artist = `artist.jpg|.png` in the album dir or its parent. Only paths of tracks the bridge knows; resolved files must stay inside the allowed directory after symlink resolution.
- Disc spins at 33 1/3 rpm = 200 deg/s; must respect `prefers-reduced-motion`.
- Existing test commands: `make test`, `go test -race ./...`.

## Review Focus

- Local `path` that is not a known track (`/etc/passwd`, `../..`): 404, nothing read. (Task 3, 4)
- Symlink inside an album dir pointing outside it: 404. (Task 3)
- Navidrome down / returns HTML or JSON error for a cover or disc: 404 `no_art`, no 5xx, nothing cached as an image. (Task 2, 4)
- Two tracks of one album requested at once: one upstream fetch, one cache entry. (Task 1, 2)
- Track with no art or radio stream: page keeps the placeholder, no console errors, disc flip inert. (Task 5)

---

### Task 1: Disk cache with singleflight and negative cache

**Files:**
- Create: `internal/art/cache.go`, `internal/art/cache_test.go`

**Interfaces:**
- Produces:
  ```go
  type Kind string
  const (Cover Kind = "cover"; Disc Kind = "disc"; Artist Kind = "artist")
  var ErrNoArt = errors.New("art: none")
  type Image struct { Data []byte; ContentType string; Key string } // Key is the ETag source
  type Cache struct{ /* unexported */ }
  func NewCache(dir string, maxBytes int64, now func() time.Time) (*Cache, error)
  // Do returns the cached image for key, else runs fetch once (concurrent callers share it),
  // stores a result, and remembers ErrNoArt for 10 min. Any other error is returned uncached.
  func (c *Cache) Do(key string, fetch func() ([]byte, error)) (*Image, error)
  ```

- [ ] **Step 1: Write failing tests** in `cache_test.go`: `TestDoFetchesOnceAndServesFromDisk` (second call, new `Cache` on same dir, fetch not invoked; `Image.ContentType` `image/png` sniffed from PNG bytes; `Key` stable and contains no part of the input key text); `TestConcurrentCallsShareOneFetch` (20 goroutines, fetch counter == 1); `TestNoArtIsRememberedForTenMinutes` (fetch returns `ErrNoArt`; second call within 10 min not re-fetched, after 11 min via injected `now` it is); `TestOtherErrorsAreNotCached`; `TestRejectsNonImageBytes` (HTML bytes -> `ErrNoArt`, nothing written to disk); `TestRejectsOversize` (>10 MB -> `ErrNoArt`); `TestEvictsOldestOverCap` (cap small, three images, oldest file gone).
- [ ] **Step 2: Run** `go test ./internal/art` — expect FAIL (package missing).
- [ ] **Step 3: Implement** `cache.go`. File name = hex SHA-256 of key under `dir/<first 2 hex>/`; content type from `http.DetectContentType` on read; reads `os.Chtimes` to refresh LRU; eviction scans the dir when the running total exceeds the cap.
- [ ] **Step 4: Run** `go test -race ./internal/art` — expect PASS.
- [ ] **Step 5: Commit** `feat(art): disk cache with singleflight and negative cache`.

### Task 2: Navidrome source

**Files:**
- Create: `internal/art/navidrome.go`, `internal/art/navidrome_test.go`

**Interfaces:**
- Consumes: `Kind`, `ErrNoArt` (Task 1).
- Produces:
  ```go
  // IsNavidrome reports whether streamURL looks like <base>/rest/stream?id=...
  func parseStream(streamURL string) (base string, songID string, auth url.Values, ok bool)
  type Navidrome struct{ HTTP *http.Client }
  // Fetch returns the raw image bytes for kind, or ErrNoArt. artKey identifies the image
  // (album/artist id + kind) for cache keys; it contains no credentials.
  func (n *Navidrome) Resolve(ctx context.Context, kind Kind, streamURL string) (artKey string, fetch func() ([]byte, error), err error)
  ```

- [ ] **Step 1: Write failing tests** against an `httptest.Server` faking `/rest/getSong` (JSON `subsonic-response.song.{albumId,artistId}`) and `/rest/getCoverArt` (checks `id` param, returns PNG): `TestCoverDiscArtistUseTheRightIds` (`al-A1`, `dc-A1:1`, `ar-R1`, cover/artist `size=600`, disc without `size`); `TestAuthParamsAreForwarded` (`u`,`s`,`t`,`v`,`c` reach the server); `TestSongLookupIsCachedPerSongID`; `TestMissingDiscIsNoArt` (server returns 200 `text/html`/JSON error -> `ErrNoArt`); `TestUpstreamDownIsAnErrorNotNoArt` (connection refused -> non-`ErrNoArt` error); `TestNonSubsonicURLIsNoArt` (`https://radio.example/stream` -> `ErrNoArt`); `TestArtKeyHasNoCredentials`.
- [ ] **Step 2: Run** `go test ./internal/art` — expect FAIL.
- [ ] **Step 3: Implement** per the Interfaces. `getSong` uses `f=json`; its result (albumId, artistId) cached in an in-memory map keyed by `base+songID`. Images read with `io.LimitReader(10 MB + 1)`.
- [ ] **Step 4: Run** `go test -race ./internal/art` — expect PASS.
- [ ] **Step 5: Commit** `feat(art): Navidrome cover/disc/artist source`.

### Task 3: Local source

**Files:**
- Create: `internal/art/local.go`, `internal/art/local_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/dhowden/tag`)

**Interfaces:**
- Consumes: `Kind`, `ErrNoArt`.
- Produces:
  ```go
  type Local struct{ Known func(path string) bool }
  // Resolve returns the cache key (file path + mtime + kind) and a fetch func, or ErrNoArt.
  func (l *Local) Resolve(kind Kind, trackPath string) (artKey string, fetch func() ([]byte, error), err error)
  ```

- [ ] **Step 1: Write failing tests** using `t.TempDir()` fixtures (a tiny valid MP3 with an ID3v2 APIC frame written by the test, and a real FLAC/other is not needed): `TestEmbeddedCover`; `TestFolderCoverNamesAndOrder` (`cover.jpg` beats `folder.png`); `TestDiscNames` (`disc.png`, `cdart.png`); `TestArtistInAlbumDirOrParent`; `TestUnknownPathIsNoArt` (`Known` false for `/etc/passwd`, nothing read); `TestSymlinkEscapingTheDirIsNoArt`; `TestRelativeAndDotDotPathsAreNoArt`; `TestOversizeImageIsNoArt`; `TestNoImagesIsNoArt`.
- [ ] **Step 2: Run** `go get github.com/dhowden/tag@latest && go test ./internal/art` — expect FAIL.
- [ ] **Step 3: Implement.** Require `filepath.IsAbs`, `Known(path)`, regular file; candidates must `EvalSymlinks` inside `EvalSymlinks(dir)` (artist: album dir or its parent); `stat` size <= 10 MB; embedded art via `tag.ReadFrom` -> `Picture().Data`.
- [ ] **Step 4: Run** `go test -race ./internal/art` — expect PASS.
- [ ] **Step 5: Commit** `feat(art): local-file source (embedded + folder images)`.

### Task 4: Resolver, route and wiring

**Files:**
- Create: `internal/art/resolver.go`, `internal/art/resolver_test.go`, `internal/server/art.go`, `internal/server/art_test.go`
- Modify: `internal/server/server.go` (route + `Config.Art`), `internal/server/redact.go` (export `RestoreURL`), `internal/snapshot/cache.go` (+`HasTrack`) and its test, `cmd/cliamp-web/main.go`, `cmd/cliamp-web/wiring_test.go`

**Interfaces:**
- Consumes: Tasks 1-3.
- Produces:
  ```go
  type Resolver struct{ /* cache, navidrome, local */ }
  type Options struct { Dir string; MaxBytes int64; HTTP *http.Client; Known func(string) bool; Restore func(string) string }
  func NewResolver(o Options) (*Resolver, error)
  func (r *Resolver) Get(ctx context.Context, kind Kind, trackPath string) (*Image, error) // ErrNoArt when none
  func RestoreURL(scrubbed string) string                 // server package
  func (c *Cache) HasTrack(path string) bool              // snapshot: state track/logical_track, queue, play-next
  ```
  Route: `GET /api/art/{kind}?path=` -> 200 image (`Cache-Control: public, max-age=86400`, `ETag: "<Key>"`, 304 on `If-None-Match`), 404 `{"code":"no_art"}`, 400 on bad kind/missing path.

- [ ] **Step 1: Write failing tests:** resolver (`TestGetRoutesNavidromeAndLocalByPath`, `TestGetRestoresScrubbedURLBeforeLookup`); `HasTrack` (`TestHasTrackCoversStateQueueAndPlayNext`); route (`TestArtRouteServesImageWithETag`, `TestArt304OnMatchingETag`, `TestArt404NoArtBodyHasNoCredentials`, `TestArt400OnBadKindOrMissingPath`, `TestArtRouteIsBehindTheGuard` foreign Host -> 400); wiring (`TestRunServesArtForLocalTrack`: fake player reporting a local track with a `cover.jpg` beside it -> 200).
- [ ] **Step 2: Run** `go test ./...` — expect FAIL.
- [ ] **Step 3: Implement.** `Get`: `Restore(trackPath)` then Navidrome if `parseStream` ok else Local; `cache.Do(artKey+"|"+kind, fetch)`. `HasTrack` backed by a set rebuilt on `SetState`/`AbsorbQueueResult`/`AbsorbPlayNextResult`. `main.go` builds the resolver with `Dir: <data-dir>/art`, `MaxBytes: 200<<20`, `Known: cache.HasTrack`, `Restore: server.RestoreURL`. Never log the path of a Navidrome URL.
- [ ] **Step 4: Run** `make test && go test -race ./...` — expect PASS.
- [ ] **Step 5: Commit** `feat: GET /api/art/{kind} for Navidrome and local tracks`.

### Task 5: Page — cover, artist avatar, disc flip and spin physics

**Files:**
- Modify: `webui/index.html` (cover box images, artist line), `webui/app.css`, `webui/app.js`, `webui/app_test.js`

**Interfaces:**
- Produces (pure, exported for node tests):
  ```js
  artUrl(kind, path)                         // '/api/art/' + kind + '?path=' + encodeURIComponent(path)
  spinStep(m, target, k, dt)                 // m={w,a} deg/s, deg/s^2 -> new {w,a}; critically damped, exact for any dt
  spinPlan(state, reduced)                   // -> {target, k}: playing {200,4}, paused {0,5}, other {0,2.2}; reduced => target 0
  ```
  Critically damped exact solution with `e = w - target`: `e(t) = (e0 + (a0 + k*e0)*t) * exp(-k*t)`, `a(t) = (a0 - k*(a0 + k*e0)*t) * exp(-k*t)`; clamp `w >= 0`.

- [ ] **Step 1: Write failing node tests:** `artUrl` encoding (`/a b?x`); `spinStep` start is smooth (`w < 1` after 10 ms from rest toward 200, monotonic increasing, `> 199` after 3 s with k=4); frame-rate independent (60 steps of 1/60 s equals one step of 1 s within 1e-6); stop coasts (from 200 target 0 k=2.2: `> 60` after 1 s, `< 1` after 5 s, never negative); pause brakes faster than stop; mid-spin-up reversal keeps `a` continuous (no jump > 1e-6 at the switch); `spinPlan` for `playing|paused|stopped`, `reduced=true` always target 0.
- [ ] **Step 2: Run** `node webui/app_test.js` — expect FAIL.
- [ ] **Step 3: Implement pure functions** and export them.
- [ ] **Step 4: Run** `node webui/app_test.js` — expect PASS.
- [ ] **Step 5: Implement the DOM side** in `app.js`/`index.html`/`app.css`:
  - `.cover` holds `<img id="art-cover">`, `<img id="art-disc" class="disc" hidden>` and the existing placeholder; artist line becomes `<p class="artist-line"><img id="artist-img" class="avatar" alt="" hidden><span id="artist"></span></p>`.
  - `loadArt(song)` runs from `renderNow` only when `song.path` changes; a sequence counter drops stale loads; each image loads via `new Image()`, shown on `load`, hidden on `error` (cover -> placeholder visible).
  - Disc available => `.cover` gets class `has-disc` (pointer cursor, `aria-label`); tap flips with Web Animations API: `rotateY(0 -> 90deg)` 170 ms `ease-in`, swap visibility, `-90deg -> 0` 170 ms `ease-out` (`perspective(800px)`); with no disc a tap does nothing; a new track without a disc returns to the cover without animation.
  - Disc rotation: a `requestAnimationFrame` loop owns `{angle, w, a}`; `renderNow` sets `{target,k}` from `spinPlan(st.state, matchMedia('(prefers-reduced-motion: reduce)').matches)`; each frame `dt = min(0.1, elapsed)`, `angle += (w_old + w_new)/2 * dt`, applied as `transform: rotate(...)` on the disc; loop stops when target is 0 and `|w|,|a| < 0.05`, and restarts on the next state change. Disc shown without the box background (transparent corners) and with a soft drop shadow.
- [ ] **Step 6: Verify in a browser** against the Pi (Task 6) — cover, flip, spin-up, pause braking, stop coast, no console errors; then `make test`.
- [ ] **Step 7: Commit** `feat(webui): cover, artist avatar, disc flip with realistic spin`.

### Task 6: Docs, packaging, deploy, live check

**Files:**
- Modify: `README.md`, `PKGBUILD` (`prepare()` runs `go mod download`), `web.lua` version only if behavior changed (no)

- [ ] **Step 1: README** section "Artwork": sources, `disc.png`, cache dir/cap, radio has none, Navidrome `dc-` id caveat (works on 0.64.2).
- [ ] **Step 2: Deploy** (arm64 build, install to `/usr/local/bin`, copy current `web.lua` to the Pi plugins dir and `plugins trust web`, restart `cliamp` user service).
- [ ] **Step 3: Live checks** on the Pi: cover/artist 200 for a Navidrome track; disc 200 for an album with `disc.png`, 404 `no_art` without; cache dir populated and grows no larger than the cap; an arbitrary local path -> 404; no credential in any response body or filename under `art/`.
- [ ] **Step 4: Browser check** of the page (screenshots of cover, disc mid-flip, disc spinning) if a headless browser is available; otherwise ask the user to confirm on the phone.
- [ ] **Step 5: Commit** `docs: artwork`.

---

## Self-review

- **Spec coverage:** route (T4), Navidrome (T2), local (T3), cache/limits (T1), page cover/disc/avatar (T5), non-goals untouched, packaging dependency note (T6). Disc physics requested after the spec is covered by T5 (`spinStep`/`spinPlan`).
- **Types:** `Kind`, `ErrNoArt`, `Image` defined in T1 and used unchanged in T2-T4; `Resolver.Get` signature used by the route; `RestoreURL`/`HasTrack` defined in T4 and consumed in `main.go` in T4.
- **Proportion:** the plan states interfaces and assertions only; implementation bodies are left to the builder except the spin equation, which the tests alone do not determine.
