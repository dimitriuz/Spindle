# Fullscreen Now Playing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A fullscreen mode of Now Playing showing artist photo, album/year, track number/title, progress, tech info, next track, and cover + disc art (side by side when wide, switchable with a peeking disc when narrow).

**Architecture:** The existing `#now` view becomes the fullscreen layer via a class; all state wiring is reused. A new `GET /api/trackinfo` (in `internal/art`, sharing the resolver's gating and Navidrome client) supplies tech info. Pure, node-tested functions pick the next track, format the tech line and choose the layout.

**Tech Stack:** Go (existing + `dhowden/tag`), vanilla JS/CSS, Web Animations, Fullscreen API where available.

**Spec:** `docs/superpowers/specs/2026-11-22-fullscreen-now-playing-design.md`

## Global Constraints

- No new dependency; no outside services; the bridge contacts only the Navidrome host of a track's own stream URL.
- `/api/trackinfo` has the same gating as art: only tracks the bridge knows or scrubbed URLs it remembers; credentials never in responses, errors or logs.
- Missing data hides its element; it never shows blanks or "undefined".
- Layout: "both" iff width >= 700 px and width/height >= 1.25; otherwise one art face at a time.
- Disc peek: disc centred at 85% of the cover width from its left edge (about 35% of its diameter shows); slides out on `playing`, back under the cover otherwise, 700 ms eased.
- Respect `prefers-reduced-motion`. Existing tests stay green: `make test`, `go test -race ./...`.

## Review Focus

- `trackinfo` for an unknown host or unknown local path: 404, no outbound request, nothing read. (Task 1)
- Shuffle on, repeat one, last track, empty queue, play-next non-empty: `nextTrack` answers correctly. (Task 2)
- Radio / no art / no info: fullscreen still lays out cleanly with those elements absent. (Task 3)
- Browser leaves fullscreen via its own Esc: the page leaves fullscreen mode too. (Task 3)
- Narrow window resized to wide while open: layout switches without losing the disc spin. (Task 3)

---

### Task 1: Track info (Navidrome + local) and route

**Files:**
- Create: `internal/art/info.go`, `internal/art/info_test.go`
- Modify: `internal/art/navidrome.go` (keep song metadata from `getSong`), `internal/art/resolver.go` (`Info`), `internal/server/art.go` (route + `InfoSource`), `internal/server/server.go` (route, `Config.Info`), `internal/server/art_test.go`, `cmd/cliamp-web/main.go`, `cmd/cliamp-web/wiring_test.go`

**Interfaces:**
- Produces:
  ```go
  type Info struct { Format string `json:"format,omitempty"`; BitrateKbps int `json:"bitrate_kbps,omitempty"`
      BitDepth int `json:"bit_depth,omitempty"`; SampleRate int `json:"sample_rate,omitempty"`; Channels int `json:"channels,omitempty"` }
  var ErrNoInfo = errors.New("art: no info")
  func (n *Navidrome) Info(ctx context.Context, streamURL string) (Info, error)  // from getSong: suffix, bitRate, bitDepth, samplingRate, channelCount
  func (l *Local) Info(trackPath string, durationSecs float64) (Info, error)     // tag type; FLAC STREAMINFO; else avg bitrate = size*8/duration
  func (r *Resolver) Info(ctx context.Context, trackPath string, durationSecs float64) (Info, error) // same gating as Get
  type InfoSource interface { Info(ctx context.Context, trackPath string, durationSecs float64) (art.Info, error) } // server
  ```
  Route: `GET /api/trackinfo?path=&duration=` -> 200 JSON `Info` (`Cache-Control: public, max-age=3600`), 404 `{"code":"no_info"}`, 400 missing path.

- [ ] **Step 1: Write failing tests:** Navidrome `Info` against the fake server (extend its `getSong` reply with `suffix:"flac",bitRate:1161,bitDepth:24,samplingRate:44100,channelCount:2` -> exact `Info`; `getSong` still called once per song across `Info`+art calls; unknown/non-Subsonic URL -> `ErrNoInfo`); local `Info` (a synthetic FLAC header fixture with STREAMINFO 96000 Hz / 24 bit / 2 ch -> format `FLAC` and those values; an ID3 file + `duration` -> `MP3` and bitrate `size*8/duration/1000`; unknown path / `../` / unlisted -> `ErrNoInfo` with nothing read); resolver `Info` refuses an unknown stream URL with zero requests; route tests (200 JSON, 404 body has no credentials, 400, behind the guard, nil source -> 404); wiring test with the fake player and a FLAC fixture.
- [ ] **Step 2: Run** `go test ./...` — expect FAIL.
- [ ] **Step 3: Implement.** Replace `songIDs` with a `songMeta{album, artist string; info Info}` cached per song; FLAC STREAMINFO parsed by hand (34-byte block after `fLaC` + block header: 20-bit rate, 5-bit channels-1, 5-bit bits-1 +1); format names uppercased from `suffix` / `tag.FileType`. `main.go` passes the resolver as `Config.Info`.
- [ ] **Step 4: Run** `go test -race ./...` — expect PASS.
- [ ] **Step 5: Commit** `feat: GET /api/trackinfo (codec, bitrate, depth, rate)`.

### Task 2: Pure functions

**Files:**
- Modify: `webui/app.js`, `webui/app_test.js`

**Interfaces:**
- Produces (exported for node tests):
  ```js
  nextTrack(state, queue)  // -> track row | null: play_next[0]; else row after curIndex; wraps when repeat 'All'; null for shuffle, repeat 'One', empty/unknown
  techLine(info)           // -> 'FLAC • 1161 kbps • 24 bit • 44.1 kHz' ; '' for empty/undefined; channels shown only when != 2 ('mono', '5.1' style -> `${n} ch`)
  fsLayout(w, h)           // -> 'both' | 'single'
  ```

- [ ] **Step 1: Write failing node tests:** `nextTrack`: play-next wins over queue; middle row; last row without repeat -> null; last row with repeat All -> first row; shuffle on -> null (even with play-next empty); repeat One -> null; empty queue / null state -> null; play-next non-empty beats shuffle. `techLine`: full FLAC; 96000 -> `96 kHz`; 88200 -> `88.2 kHz`; 48000 -> `48 kHz`; missing fields skipped; `{}`/`null` -> `''`; channels 1 -> `mono`; 6 -> `6 ch`. `fsLayout`: (1280,720) both; (390,844) single; (800,900) single; (700,500) both; (699,400) single.
- [ ] **Step 2: Run** `node webui/app_test.js` — expect FAIL.
- [ ] **Step 3: Implement** the three functions and export them.
- [ ] **Step 4: Run** `node webui/app_test.js` — expect PASS.
- [ ] **Step 5: Commit** `feat(webui): nextTrack, techLine, fsLayout`.

### Task 3: Fullscreen mode (markup, CSS, behaviour)

**Files:**
- Modify: `webui/index.html`, `webui/app.css`, `webui/app.js`

**Interfaces:** consumes Task 1 route and Task 2 functions.

- [ ] **Step 1: Markup.** `.meta` becomes `display: contents`; add `<span id="trackno" class="fs-only">` and `<span id="titletext">` inside `#title`; `#album` text becomes `Album · Year`; add `<div id="tech" class="tech fs-only">`, `<p id="next" class="next fs-only" hidden>`, `<div id="fs-bg" class="fs-bg">` in `#now`, `<button id="b-fs" class="ic" data-icon="expand">` in `.extras`; the disc `<img>` gets a `<div class="disc-slide">` wrapper; add `expand`/`collapse` icons.
- [ ] **Step 2: Behaviour.**
  - Enter/exit: toggles `#now.fs`, calls `document.documentElement.requestFullscreen()` when available (ignore rejection), exits on button, `Escape`, swipe down (`dy > 90`, `|dx| < 60`, not starting on `#bar`), and on `fullscreenchange` when the browser left fullscreen.
  - Layout: on enter and on `resize`, `#now.dataset.art = fsLayout(innerWidth, innerHeight)`; `paintArt` decides faces from `{fs, layout, showDisc}`: normal as before; fs+both: cover and disc both visible; fs+single: cover with disc peeking (disc behind, `translateX(35%)` when playing, `0` otherwise, 700 ms ease; reduced motion: no transition) or, after a tap, the disc alone; tap flips only in single layout.
  - Info: on track change fetch `/api/trackinfo?path=&duration=` (sequence-guarded) -> `#tech` via `techLine`; `renderNow` sets `#trackno` (`04 · `), `#titletext`, `#album` (`album · year`), `#next` (`Next: title — artist`, hidden when `nextTrack` is null); backdrop `#fs-bg` uses the cover URL, blurred and darkened.
  - Remember cover/disc choice in `sessionStorage` (try/catch).
- [ ] **Step 3: CSS.** Fixed layer (`inset: 0`, above tabs, below toasts), safe-area padding, `--s` art size (`min(44vh, 62vw)` single, `min(52vh, (100vw - 120px)/2)` both), fs-only elements hidden outside fs, `.extras`/`.vol` hidden in fs, close button top right, backdrop `filter: blur(40px) brightness(.45)`; disc peek as `translate: -17.5% 0` on the cover group so the pair is centred.
- [ ] **Step 4: Run** `make test` — expect PASS.
- [ ] **Step 5: Browser verification** (headless Chrome over the DevTools protocol against the Pi): portrait 390x844 (cover + peeking disc, flip to disc, close), landscape 1280x720 (both), resize narrow<->wide while open, no console errors, radio track (no art/tech/next-hidden), Escape and swipe close. Screenshots read and checked.
- [ ] **Step 6: Commit** `feat(webui): fullscreen Now Playing`.

### Task 4: Docs, deploy, final check

**Files:** Modify `README.md`.

- [ ] **Step 1:** README "Fullscreen" section (what it shows, layouts, tech-info sources, next-track rules).
- [ ] **Step 2:** Deploy to the Pi (arm64 build, install, restart); live checks: `/api/trackinfo` for a Navidrome track returns the expected fields, an unknown host/path is 404 with no outbound request, no credentials in any body.
- [ ] **Step 3:** `make test`, `go test -race ./...`; commit `docs: fullscreen`.

---

## Self-review

- **Spec coverage:** artist+photo, album/year, track number/title, progress (reused bar), tech line (T1+T2+T3), next track (T2+T3), both vs single + peek + slide (T3), tap-to-flip (T3), close/Esc/swipe/Fullscreen API (T3), local tech info (T1), failure behaviour (Review Focus + T3 step 5).
- **Types:** `Info`/`ErrNoInfo`/`InfoSource` defined in T1 and used in T3 via the route; JS function names identical in T2 and T3.
- **Proportion:** interfaces, assertions and decisions only; CSS and DOM wiring left to the builder except the pinned numbers.
