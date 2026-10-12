# cliamp-plugin-web v3 — depth + phone ergonomics

Date: 2026-10-12
Status: approved design (chat brainstorm 2026-10-12), pending implementation plan
Depends on: v2 (`docs/superpowers/specs/2026-10-12-cliamp-web-v2-design.md`), merged to `main` (merge `3635b26`, review fixes through `c48c866`).

## 1. Goal

v2 made the phone able to choose music. v3 makes it *pleasant and trustworthy*
on the phone, and digs one level deeper into the library:

1. four confirmed field bugs: track rows render as cramped "labels" without a
   visible artist; playback state freezes when the bridge process the phone was
   talking to dies (client EventSource never recovers); bottom tabs scroll away;
   per-row "load" buttons clutter every playlist row.
2. **playlist drill-down**: tap a playlist, page through its real tracks, play
   or queue any one of them.
3. **lyrics sheet**: timestamped lyric lines for the playing track, active line
   highlighted, tap-to-seek.

Non-goals for v3 (probed and rejected, or user-declined): favorites (only radio
supports `provider.favorite`; no read op exists anywhere, so state can't be
rendered), play history (no player op; bridge-side recorder declined for now),
HTTPS / mDNS / PWA / multi-user, virtualized lists.

Success looks like: on the phone, playlists open like albums do today, lyrics
follow Elvis Costello in real time, the tab bar never moves, rows read
title — artist with room to breathe, and closing the laptop lid mid-playback
no longer permanently freezes the phone UI.

## 2. Ground truth (live probes against cliamp v2.0.1, 2026-10-12, evening)

Everything below was observed against the user's running player via the bridge;
where docs or earlier rulings disagree, this wins.

| probe | result | consequence |
|---|---|---|
| `provider.tracks {provider, playlist}` | **succeeds**: `{ok, total: 12883, tracks:[{title,artist,album,genre,path,year,track_number,duration_secs,stream}]}`, honors `offset`/`limit` | playlist drill needs no new player capability. v2's "no playlist-tracks op" ruling was wrong — it only ever tried `id`. |
| `provider.tracks {provider}` (neither id nor playlist) | fails `navidrome: playlist not found (code 70)` when playlist bogus | bridge must pass `playlist` verbatim; player errors are the authority. |
| `provider.album_tracks` rows | carry `artist` | row redesign is pure CSS; data was never missing. |
| `provider.favorite` | exists; `radio` validates station id; navidrome/local/ytmusic/youtube/yt → "provider does not support favorites" | favorites out of scope (see §1). |
| history ops | none exist | history out of scope. |
| radio `catalog` rows | `{id,name,provider,favoritable?}` — **no path**; radio *search* rows carry `path` (stream URL) | stations play only via `provider.load {provider, playlist: <station id>}`; no `provider.play`/`provider.queue` op exists. |
| `lyrics` (track loaded) | succeeds: `{ok, lyrics:[{start: 0.29, text: "I see trees that are green"}, …]}` (LRC-style seconds) | lyrics sheet + sync + tap-seek feasible with zero deps. |
| `lyrics` (nothing loaded / none found) | failed job, detail `no lyrics found` | UI maps to empty-state, not error banner. |
| history ops | none exist | history out of scope. |
| player → bridge SSE | verified healthy: `toggle` produced live `playback` events < 1 s; event names `state` `playback` `playlist` `job` `player` all flow | freeze bug is client-side, not bridge-side. |
| field incident (plugins.log) | plugin autostart crash-looped 15:36–15:40 on `bind 0.0.0.0:8730: address already in use`; phone EventSource died with its bridge and never reconnected | motivates §6 reconnect design. |

## 3. Bridge additions (Go)

All three follow the v2 library patterns exactly (`libParams`, `libraryError`,
`protect`, raw-JSON passthrough).

1. `GET /api/library/{provider}/playlist/{id}/tracks?offset&limit` →
   op `provider.tracks`, `params.playlist = {id}` (plus provider/offset/limit).
   Cached in `listCache`, key = full path+query. Mux pattern is 5 segments,
   no conflict with existing routes (same shape as album/artist routes).
2. `GET /api/lyrics` → op `lyrics`, params `{}`. Never cached (`noCache`).
   200 with `{"ok":true,"lyrics":[…]}` or `{"ok":true,"lyrics":[]}` when the
   job *succeeds*; failed job "no lyrics found" still maps through
   `libraryError` → 502 + detail, and the UI (§5) renders *that* as the empty
   state. Timeout/offline identical to library endpoints.
3. SSE heartbeat: `events` handler sends `event: ping` + `data: {}\n\n` every
   15 s alongside the existing `:` comment. Named event, because the
   `EventSource` API does not surface comment lines. Existing subscribers
   ignore unknown event names; Go test asserts ping frames exist.

`kind` whitelist unchanged (playlist tracks is a separate route, not a kind).

## 4. Fake player (Go, test-only)

- `provider.tracks` mirrors live: accepts `playlist` as an alternative to `id`
  (`fixtureTracks("playlist", playlist)` when `playlist` set, `id` wins if both);
  when neither → `navidrome: missing parameter: 'id' (code 10)` (live behavior).
  With `playlist: "nope"` → `navidrome: playlist not found (code 70)` so drill
  error paths are smokeable.
- `lyrics` op → fixed fixture: ~8 `{start,text}` lines for the fake track, plus
  option `Lyrics` (`""` ⇒ failed job `no lyrics found`).

## 5. UI

### 5.1 Row redesign (BrowseView, PlayNextList, hero up-next)

`.row` becomes a real line: `display:flex; align-items:baseline; gap:.5rem`.
Title `flex:1; min-width:0; white-space:nowrap; overflow:hidden;
text-overflow:ellipsis`; artist/secondary `flex:none; max-width:40%;
ellipsis` prefixed with `·` only when non-empty; action buttons `flex:none`.
PlayNextList gains the artist column (it renders title-only today — a second
place the user saw "no artist"). Hero up-next rows get the same truncation.
Album rows keep name + `artist · year` but with the same flex/truncation.
Track metadata order everywhere: **title first, artist secondary/muted** —
matching what the user asked for.

### 5.2 Playlist drill-down

`Drill.kind` becomes `"album" | "artist" | "playlist"`. Playlist rows become
drill rows (`openDrill {kind:"playlist", id, name}`); `fetchPage` gains the
kind → `api.libraryPlaylistTracks(provider, id, offset)` →
`/playlist/{id}/tracks`; drilled tracks render with play/queue like album
tracks already do. The drill header gets one confirm-gated button
"▶ play playlist" → `provider.load {provider, playlist}` (same confirm text
as today's load). **The per-row `load` button is deleted.**

Catalog rows (radio stations) cannot play by path — no path field, no station
play op (§2) — so the whole row is a tap target firing the same
confirm → `provider.load {provider, playlist: <station id>}` that the load
button fired, marked with a trailing ▶ glyph. No "load" text anywhere in the
app; playlist rows never tap-load, they drill.

### 5.3 Lyrics sheet

Hero shows a "lyrics" button when `snapshot.track?.path` is set. Tapping opens
a bottom sheet (fixed overlay, closes on backdrop/Esc): fetch `/api/lyrics`
once per open, render one line per entry. Active line = last entry with
`start <= snapshot.seek_position`; auto-scroll via `scrollIntoView({block:
"center", behavior:"smooth"})` on change; line tap →
`runCommand("seek.absolute", {value: start})` and sheet stays open. 502/detail
`no lyrics found` or empty array → centered "no lyrics for this track".
Re-fetch only on manual retry button.

### 5.4 SSE resilience (store.ts)

State machine, replacing the fire-and-forget `connectEvents`:

- `es.onerror` → `close()`, schedule reopen with backoff 1 s, 2 s, 5 s, 10 s
  (cap 30 s). Successful open (any event received) resets backoff.
- watchdog `setInterval` 5 s: no event of any kind for 35 s → force
  close+reopen now (covers half-open TCP on iOS wake).
- `visibilitychange` → when page becomes visible and last event is older than
  5 s, force immediate reopen.
- on every successful reopen: `refreshState(); refreshQueue()` (resync).
- `document.title` untouched; StatusPill keeps showing online/offline from
  `player` events.

### 5.5 Fixed tab bar

`nav.tabs` → `position:fixed; left:0; right:0; bottom:0` with
`padding-bottom: env(safe-area-inset-bottom)`, `z-index`, and a spacer rule
`main { padding-bottom: calc(tab-height + safe-area) }` so no content hides
under it. MiniPlayer docks above the tabs (`bottom: tabs-height`), both fixed.

## 6. Data-flow invariants (unchanged, restated for the reviewer)

- Provider/playlist/album/id/query params ride inside `params`, never the
  envelope.
- Queue-replacement actions (`provider.load`, `provider.load_album`) are
  confirm-gated before submit; everything else fires immediately.
- Library GETs forward the player `result` verbatim; the bridge never talks to
  providers itself.
- Search and lyrics are never cached; list/playlist tracks cache 60 s TTL.
- All new endpoints are behind `protect` (token) like every `/api/*` route.

## 7. Testing

Go (permanent tests):
- playlist-tracks route: params placement (`playlist` inside params), 400 on
  bad pagination, 502 detail passthrough for `playlist not found (code 70)`.
- lyrics route: passthrough shape, 502 on `no lyrics found`, never cached
  (second call hits runner twice).
- SSE: `ping` frame observed within ~heartbeat window; old comment ping intact.
- fake: `provider.tracks` playlist/id routing + both live error strings;
  `lyrics` fixture + no-lyrics failure.

UI:
- throwaway bun harness (like v3 review's navrace): playlist drill paging,
  reconnect state machine driven with fake timers/backoff assertions.
- browser smoke (if harness alive) + user live acceptance on phone:
  playlist 12k paging, lyrics sync on *What a Wonderful World*, pause-in-TUI
  updates after lid-close wake, tabs fixed while scrolling 222 playlists,
  no "load" labels visible.

## 8. Review focus

1. reconnect machine: no double EventSources (old one always closed), no
   unbounded timers on unmount.
2. playlist drill shares the v2 generation-counter rules — stale playlist
   replies must not paint over a provider switch (reuse, don't re-implement).
3. lyrics sheet must not leak the fetch on close; reopen of a different track
   refetches.
4. `provider.tracks` route builds **only** the `playlist` param (whitelist
   spirit from the v2 review): no way to smuggle `id` through this route.
5. fake `provider.tracks` precedence (id vs playlist) can't shadow the
   missing-parameter failure path.
6. row redesign doesn't break the sentinel/IntersectionObserver paging.
