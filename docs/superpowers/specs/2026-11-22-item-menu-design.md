# Item menu ("...") redesign

Date: 2026-11-22. Status: approved; built with the amendments at the end. Requirements agreed in conversation (the "..." on
every item, Play = replace queue, Add to queue, a real submenu); "My calls" are proposals.

## Intent

Today each row's "..." expands a row of buttons under the item, artists have none, and "Play" on
a track silently keeps the queue. Replace it with one menu component that every item uses, with
the same two core actions everywhere and a presentation that suits touch, mouse and keyboard.

## Goals

- A "..." on every list item: tracks, albums, artists, playlists, radio stations, queue rows,
  play-next rows, favorites.
- **Play**: clear the queue, add the item, start playing. One action, one name (no separate
  "Play now" / "Replace queue").
- **Add to queue**: append to the end of the queue.
- Opens as a submenu, not under the row: a bottom sheet on narrow/touch screens, a popover
  anchored to the "..." on wide pointer screens. Usable with touch, mouse and keyboard.

## Non-goals

- Changing what tapping a row does (tracks keep today's tap; albums, artists, playlists open).
- "Play next" for albums, artists, playlists; multi-select; drag to reorder.
- Playlist editing (add to a saved playlist).

## Findings (probed on the Pi)

- `track.play` does not replace the queue: it appends the track and plays it (queue 13 -> 14).
  A true Play is `queue.clear` then `track.play` (queue 1, playing).
- `queue {path}` appends one path to the end; `track.queue` queues next; `provider.load_album`
  and `provider.load` replace the queue and play; there is no append operation for albums,
  playlists or artists, so the bridge appends track by track.
- Radio catalog rows have no stream URL, so a station can only be Played.

## Approaches

1. **One shared menu component, one bridge endpoint for queue actions (chosen).** Every row
   passes a list of actions; the component renders them as sheet or popover. A new
   `POST /api/enqueue` does the multi-step work (album/playlist/artist appends, clear-then-play)
   on the bridge with progress events.
2. Keep per-row inline menus and just restyle. Rejected: artists and stations still lack it, and
   inline expansion is what looks wrong.
3. Page-side loops calling `/api/command` per track. Rejected: the command rate limit (20/s)
   and long browser-held loops make large playlists unreliable.

## Design

**Actions per item**

| Item | Menu |
|---|---|
| Track | Play, Play next, Add to queue, Favorite |
| Album, playlist | Play, Add to queue, Favorite |
| Artist | Play, Add to queue, Favorite (all of the artist's tracks) |
| Radio station | Play, Favorite |
| Queue row | Play from here, Play next, Move up, Move down, Remove |
| Play-next row | Move up, Move down, Remove |

Favorite reads "Remove from favorites" when it is one. Actions that do not apply are left out
(Move up on the first row is shown disabled). Remove is styled as dangerous.

**Presentation.** Below 600 px or on a coarse pointer: a bottom sheet with the item's title and
subtitle at the top, 56 px action rows with icons, a Cancel row, a scrim, safe-area padding;
tapping the scrim or swiping down closes it. Otherwise: a popover (about 240 px) under the
"...", flipped above when there is no room, closed by an outside click, scroll or resize.
Right-click on a row opens the same menu. Accessibility: `role="menu"`/`menuitem`, the trigger has
`aria-haspopup` and `aria-expanded`, focus moves to the first item, Arrow Up/Down, Home/End,
Enter/Space activate, Escape closes and returns focus to the trigger.

**Bridge: `POST /api/enqueue`** `{kind: track|album|playlist|artist, provider, id, mode: play|append,
track?}`.
- Track: `play` = `queue.clear` + `track.play`; `append` = `queue {path}`. The track object's path
  must be a stream URL the bridge handed out or a track it knows (same gate as art), restored to
  the real URL.
- Album / playlist: `play` = `provider.load_album` / `provider.load`; `append` = list the tracks
  (`provider.album_tracks` / `provider.tracks`, paged) and `queue` each.
- Artist: list the albums (`provider.artist_albums`), then each album's tracks; `play` = clear,
  `track.play` the first, `queue` the rest; `append` = `queue` each.
- Anything bigger than 1000 tracks is cut at 1000; the response says how many exist so the page
  can say "added the first 1000 of 12,323".
- Runs in the background: the request returns `{ok, total}` at once, progress and completion
  arrive as SSE `enqueue` events `{done, total, state: running|done|failed, added, of, error?}`.
  One enqueue at a time per bridge (a second request gets 409 "busy"). Behind the usual
  request guard and rate limit.
- Appending uses no `if_revision` (it changes with every append); the queue refetch on the
  `playlist` event keeps the page in step.

**Page.** Toast "Adding 340 / 1000..." while running, "Added 12 tracks" or "Added the first 1000 of
12,323" when done, an error toast on failure. Play shows "Playing" after the queue is replaced.

## My calls (please check)

- No confirmation before Play: the menu is a deliberate two-step action and Play is defined as
  replacing the queue. (The old confirm guarded one-tap buttons.)
- Cap of 1000 tracks per "Add to queue" with a notice, not a confirm.
- Row tap behaviour unchanged, so tapping a track in a list still keeps the queue and plays it.
- Play next only for tracks; Favorite included in every menu as a convenience.
- Right-click opens the menu; long-press does not (it fights scrolling).

## Left to the builder

File layout, icons, animation timing, exact colours, the enqueue worker's internals, test fixtures.
Tests must include: enqueue per kind and mode with the fake player (order of calls, clear before
play, cap at 1000 with the right totals, busy -> 409, bad kind, untrusted path refused,
credentials never in events or errors), SSE events in order, menu keyboard behaviour and
focus return (browser check), sheet vs popover by viewport, and that every row type has the "...".

## Amendments made while building

- **No 1000-track cap** (requested): everything is added, with progress and a Stop button.
- **Add to queue uses cliamp's up-next list (`track.queue`), not `queue {path}`.** Probed on the
  Pi: `queue {path}` adds a bare URL (title = server hostname, no artist/duration/provider
  metadata, even while playing), `queue {track}` fails and `url.load` is equally bare.
  `track.queue` keeps the track and is first-in-first-out. Consequence: "Play next" and "Add to
  queue" would be the same action for a track, so tracks have only Add to queue (queue rows keep
  Play next, which moves an existing row up next).
- Plain `http(s)` stream URLs (radio, podcasts) need no gate; only file paths do.
- The job poll now backs off from 2 ms to 200 ms; the fixed 200 ms made every command, and
  especially a 12,000-track add, slow.
