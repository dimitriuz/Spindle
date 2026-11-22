# Favorites

Date: 2026-11-22. Status: awaiting approval. Scope and the sync choice (option A) agreed in
conversation; "My calls" are proposals.

## Intent

A heart on anything worth coming back to: tracks, albums, artists, playlists and radio
stations, and one place to find them all again. Tracks and stations are also favorited in
cliamp itself, so the heart shows in its TUI too (and cliamp stars Navidrome tracks on the
server by itself).

## Goals

- Toggle a favorite from every list row, from the album/artist/playlist pages, and from Now
  Playing (and fullscreen) for the playing track.
- A Favorites tab listing everything, filterable by kind; tapping an item plays or opens it.
- Tracks and stations are written into cliamp so both sides agree.
- Several open pages stay in step.

## Non-goals

- Navidrome stars for albums, artists or playlists, and importing stars made elsewhere (the
  server already has one starred artist; it is not imported).
- Ratings, sorting beyond "newest first", reordering, folders.
- Favoriting a radio station that is not in the radio catalog (see Findings).

## Anti-goals

- A double toggle leaving the heart inverted: cliamp's calls toggle, they do not set.
- Credentials in any stored file, response or log.
- The page showing a heart state that the server does not hold.

## Findings (probed on the Pi, all changes undone)

- cliamp favorites exist for tracks and radio stations only. Toggle-only calls:
  `playlist.bookmark {provider, track}` and `provider.favorite {provider, playlist}`.
- A favorited track appears in the Local provider's playlist `Favorites` (readable with
  `provider.tracks`) and is starred on Navidrome; toggling again undoes both.
- A favorited station becomes a catalog row `f:<stream url>` with `favorite: true`. It can be
  favorited only from its catalog row (`c:N`, an index that changes between sessions); an
  unknown URL fails with "unknown favorite station". Unfavoriting works from the `f:` row or
  the catalog row.
- cliamp has no favorites for artists, albums or playlists.
- `/api/command` must not gain these two ops: the bridge calls them itself.

## Approaches

1. **Bridge-owned list for artists, albums, playlists; cliamp stays the source of truth for
   tracks and stations (chosen).** No duplicated state for the things cliamp already stores.
2. A full copy of every favorite in the bridge, mirrored into cliamp. Rejected: two copies
   drift whenever something is changed in the cliamp TUI.
3. cliamp only. Rejected: it cannot hold three of the five kinds.

## Design

**Where favorites live.** Tracks: cliamp's `Favorites` playlist. Stations: cliamp's `f:` rows.
Albums, artists, playlists: `<data-dir>/favorites.json` (written atomically), each entry
`{kind, provider, id, title, subtitle, added}`, nothing else, so no credentials.

**API (server only; the browser never sends cliamp ops).**
- `GET /api/favorites` -> `{items: [...]}` newest first, each item
  `{kind, provider, id, title, subtitle, added?, track?}`. Tracks carry their track object (so
  they can be played), stations their `f:` id. Cached for 30 s, dropped on every change.
- `PUT /api/favorites` with `{kind, provider, id, favorite: true|false, title?, subtitle?,
  track?}` sets the state idempotently and returns `{ok, favorite}`. For tracks and stations the
  bridge reads the current state, toggles only if it differs, then re-reads to confirm. One
  lock serialises changes so two taps cannot double-toggle. Behind the existing request guard
  (JSON, same origin).
- Every change broadcasts an SSE `favorites` event; the page refetches.

**Identity.** Track key: `<provider>:<id>` from `provider_meta` (`navidrome.id`), else the file
path. Album, artist, playlist, station: `<provider>:<id>` (stations by their `f:` id once
favorited, by `c:N` while not).

**Page.**
- A heart button on: browse rows (playlists, albums, artists, radio catalog), track rows
  (search results, album pages, artist albums), page headers of an album / artist / playlist, and
  Now Playing and fullscreen for the playing track.
- A fifth tab, Favorites (heart icon), with kind chips All / Tracks / Albums / Artists /
  Playlists / Radio. Track: plays; album, artist, playlist: opens its page; station: plays
  (with the same confirm as the catalog).
- Hearts render from the set of keys in `GET /api/favorites`; a tap updates optimistically and is
  rolled back with a toast if the server refuses.
- Radio on Now Playing: the heart appears only when the playing station is already a favorite
  (its stream URL matches an `f:` row), so it can be removed there; adding is done from the
  catalog row.

**Failure behaviour.** cliamp offline: track and station hearts fail with a toast, the bridge
list still works. A Navidrome star that fails stays a cliamp favorite (cliamp's own rule).
Changes made in the cliamp TUI show up on the next refresh (tab open, any change, resync, or
every 60 s while the tab is visible).

## My calls (please check)

- Fifth tab instead of a Browse tile; chips for kinds; newest first.
- Radio can be favorited only from the catalog (cliamp's limitation), removed from anywhere.
- Existing Navidrome stars are not imported; albums/artists/playlists are not starred on the
  server.
- A 60 s refresh while visible to pick up TUI changes (no event from cliamp exists for them).

## Left to the builder

File layout (suggest `internal/favorites`), JSON field details, icon, chip styling, test
fixtures. Tests must include: the read-then-toggle logic (already in the wanted state: no
toggle; the double-tap race; cliamp error; confirm-after mismatch), `favorites.json` round trip
and corruption, key derivation, merged listing order and paging of cliamp's Favorites, the two
ops are rejected on `/api/command`, no credentials anywhere, SSE broadcast, the page's heart
state and rollback, and a live check on the Pi that leaves favorites as found.
