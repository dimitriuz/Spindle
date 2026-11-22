# Spindle HTTP API

The bridge serves a JSON API next to the web page. The page uses it, and so can a script or a small device such as an ESP32 touch remote that has no browser. This is the reference.

- [Basics](#basics)
- [Now playing](#now-playing)
- [Live updates: WebSocket](#live-updates-websocket)
- [Commands](#commands)
- [Browsing](#browsing)
- [Queue](#queue)
- [Play and add to the queue](#play-and-add-to-the-queue)
- [Pictures](#pictures)
- [Other endpoints](#other-endpoints)
- [Errors](#errors)
- [Notes for an ESP32 app](#notes-for-an-esp32-app)

## Basics

- **Base URL:** `http://<host>:8730` (`--port` changes it; the bridge must run with `--lan` or `lan = true` to be reachable from other machines). The address is in `~/.local/share/cliamp/spindle/status.json`.
- **No login.** Anyone who can reach the port controls playback. Keep it on a trusted network.
- **Writes** (`POST`, `PUT`) need the header `Content-Type: application/json`. A request that carries an `Origin` header must carry the bridge's own address in it. A device that sends no `Origin` is fine.
- **Responses** are JSON (`Content-Type: application/json`) except pictures.
- **Rate limit:** `POST /api/command`, `/api/enqueue` and `PUT /api/favorites` are limited per client; the answer is `429` with `{"code":"rate_limited"}`.
- **Stream URLs** that carry a login (`u=`, `token=` and similar query parameters) are cleaned before they reach you. Send a path back exactly as you received it.

Two shortcuts keep answers small for devices. Both are optional, and without them every response is exactly what the web page gets:

| Parameter | Where | Effect |
|---|---|---|
| `compact=1` | library lists, search, `/api/files`, `/api/queue` | rows in one small shape, see [Browsing](#browsing) |
| `size=N` | all picture endpoints | JPEG with the long side at most N pixels, see [Pictures](#pictures) |

## Now playing

`GET /api/state` returns the player's snapshot as cliamp reports it. cliamp leaves zero values out, so an absent `index` on a loaded track means 0, an absent `volume` means 0 dB and an absent `repeat` means `Off`.

```json
{"state":"playing","revision":5,"playlist_revision":7,"track":{"title":"Fake Song","artist":"Fake Band","path":"/fake/a1-0.flac"}}
```

Fields a remote usually needs: `state` (`playing`, `paused`, `stopped`), `track` (`title`, `artist`, `album`, `path`), `position` and `duration` in seconds, `volume` in dB, `shuffle` (bool), `repeat` (`Off`, `All`, `One`), `index` (row of the playing track in the queue), `seekable`.

For a device, the [WebSocket](#live-updates-websocket) delivers the same facts already trimmed, so you rarely need this endpoint.

`GET /api/health` answers `{"ok":"true","player":"online"}` (or `"offline"`) and is the only endpoint that is always open.

## Live updates: WebSocket

`GET /api/ws` upgrades to a WebSocket. Every message is a JSON text frame with a `type`. The server only sends; whatever the client sends is ignored (control frames, ping and pong, work as usual). Commands go over [`POST /api/command`](#commands).

On connect you get a `status` frame and then a `queue` frame. After that:

| Frame | Sent when |
|---|---|
| `status` | the player's state changes (track, pause, volume, shuffle, repeat, online or offline). Position-only updates are sent at most once per second, always the latest. |
| `queue` | the queue changes |
| `resync` | the bridge could not keep up with this client and dropped frames. Fetch what you need again (`status` and `queue` arrive anyway; refetch any lists you cached). |
| `ping` | every 15 seconds, so you can tell the link is alive |

### `status`

```json
{"album":"Fake LP","art":"/fake/a1-0.flac","artist":"Fake Band","duration":181,"index":0,"online":true,"position":0,"repeat":"All","seekable":false,"shuffle":false,"state":"playing","title":"Fake Song","type":"status","volume":-2}
```

| Field | Meaning |
|---|---|
| `online` | the bridge is connected to cliamp. When `false` the other fields are the last known values (or empty). |
| `state` | `playing`, `paused` or `stopped` |
| `title`, `artist`, `album` | strings, `""` when unknown |
| `position`, `duration` | seconds (number) |
| `volume` | dB (number, 0 = unity) |
| `shuffle` | bool |
| `repeat` | `Off`, `All` or `One` |
| `index` | row of the playing track in the queue, `-1` when nothing is loaded |
| `seekable` | `seek.absolute` works for this track |
| `art` | the track's path. Use it as `/api/art/cover?path=<art>&size=N`. Empty when nothing is loaded. |

No field is ever `null`. Frames have no fixed key order.

### `queue`

```json
{"count":1,"items":[{"id":"","title":"Row A","subtitle":"","kind":"track","playable":true,"has_art":true}],"limit":200,"offset":0,"playlist_revision":7,"type":"queue"}
```

It carries the first 200 rows and `count`, the total. For more, page with `GET /api/queue?compact=1&offset=200&limit=200`. `playlist_revision` identifies the queue's version; send it as `if_revision` with queue edits to be refused (`409`) when the queue changed under you.

### Connecting

- Same-origin rule: a client without an `Origin` header (an ESP32, `websocat`, a script) is accepted. A browser page from another address is refused.
- Reconnect after any error with a short back-off. The first frames after connecting give you the full picture again.
- The connection is plain `ws://`. There is no TLS in the bridge; put a proxy in front if you need `wss://`.

```sh
websocat ws://192.168.1.20:8730/api/ws
```

## Commands

`POST /api/command` with `{"op": "<name>", "params": {...}}`. It answers `200` with `{"job": {...}}` once cliamp accepted the operation. The player's own state changes arrive on the WebSocket.

```sh
curl -X POST http://192.168.1.20:8730/api/command \
  -H 'Content-Type: application/json' \
  -d '{"op":"toggle"}'
```

Allowed operations (anything else is `403 forbidden_operation`):

| Operation | Params |
|---|---|
| `play`, `pause`, `stop`, `toggle`, `next`, `prev` | none |
| `seek.absolute` | `{"value": seconds}` |
| `volume` | `{"value": dB}` |
| `shuffle` | `{"name": "on"}` or `{"name": "off"}` |
| `repeat` | `{"name": "cycle"}` |
| `track.play`, `track.queue` | `{"track": {...}}`, a track object with at least `path` |
| `queue.play`, `queue.remove` | `{"index": n}`, optionally `"if_revision": playlist_revision` |
| `queue.move` | `{"index": from, "to": to}`, optionally `"if_revision"` |
| `queue.clear` | none |
| `playnext.remove`, `playnext.move` | as the queue operations |
| `provider.load` | `{"provider": key, "playlist": id}`: plays a playlist **or a radio station** (a `station` row's `id`) |
| `provider.load_album` | `{"provider": key, "album": id}` |
| `device`, `eq`, `mono`, `speed` | settings; values are checked (`eq` band 0 to 9 and value −12 to 12, `speed` 0.25 to 2) |

For an album, playlist, artist, folder or file use [`POST /api/enqueue`](#play-and-add-to-the-queue); it does the multi-step work for you.

## Browsing

### Providers

`GET /api/library/providers` lists what cliamp offers (always the full shape, never compact):

```json
{"ok":true,"providers":[{"browse_albums":true,"browse_artists":true,"key":"navidrome","name":"Navidrome","searchable":true},{"catalog":true,"key":"radio","name":"Radio","searchable":true},{"key":"local","name":"Local","searchable":true}]}
```

`key` is what the other endpoints take as `{provider}`. The flags say what the provider can do: `browse_albums`, `browse_artists`, `catalog` (radio stations), `searchable`.

### Lists

| Endpoint | Rows |
|---|---|
| `GET /api/library/{provider}/albums` | albums (`sort` is the provider's own order, such as `newest`) |
| `GET /api/library/{provider}/artists` | artists |
| `GET /api/library/{provider}/playlists` | playlists |
| `GET /api/library/{provider}/catalog` | radio stations |
| `GET /api/library/{provider}/album/{id}/tracks` | tracks of an album |
| `GET /api/library/{provider}/artist/{id}/albums` | albums of an artist |
| `GET /api/library/{provider}/artist/{id}/tracks` | tracks of an artist |
| `GET /api/library/{provider}/playlist/{id}/tracks` | tracks of a playlist |
| `GET /api/search/{provider}?q=text` | tracks matching the text |

Paging: `offset` (default 0) and `limit` (default 50, at most 200). Lists are cached by the bridge for a minute.

### Compact rows

Add `compact=1` and the answer is the same small shape for every kind of list:

```sh
curl 'http://192.168.1.20:8730/api/library/navidrome/albums?compact=1&limit=2'
```
```json
{"items":[{"id":"al1","title":"Album One","subtitle":"Artist A","kind":"album","playable":true,"has_art":true},{"id":"al2","title":"Album Two","subtitle":"Artist A","kind":"album","playable":true,"has_art":true}],"limit":2,"offset":0,"total":3}
```

| Field | Meaning |
|---|---|
| `id` | what to put in the next request: an album or artist id, or a track's path |
| `title` | the name to show |
| `subtitle` | the line under it: an album's artist, `2 albums` for an artist, `12 tracks` for a playlist, a track's artist; `""` when there is none |
| `kind` | `album`, `artist`, `playlist`, `station`, `track`, `folder` or `file` |
| `playable` | can be played or added (always `true` today) |
| `has_art` | a picture may exist (see [Pictures](#pictures)) |

Around the rows: `total` (all rows, or the row count when the provider does not say), `offset` and `limit` (what you asked for). To page, request `offset + limit` next until it reaches `total`. All six row keys are always present, never `null`. An empty list is `"items":[]`.

Tracks:

```sh
curl 'http://192.168.1.20:8730/api/library/navidrome/album/al2/tracks?compact=1'
```
```json
{"items":[{"id":"/fake/a2-0.flac","title":"Album Two Track 1","subtitle":"Artist A","kind":"track","playable":true,"has_art":true},{"id":"/fake/a2-1.flac","title":"Album Two Track 2","subtitle":"Artist A","kind":"track","playable":true,"has_art":true}],"limit":50,"offset":0,"total":2}
```

Radio stations (`catalog`) have `kind` `station` and no subtitle.

### Music folder

`GET /api/files/root` answers `{"name": "Music"}` when the file browser is on (404 `no_files` otherwise). `GET /api/files?path=Rock/Band` lists a folder; with `compact=1`:

```json
{"path":"Classic","parent":"","name":"Classic","items":[{"id":"Classic/Vivaldi","title":"Vivaldi","subtitle":"","kind":"folder","playable":true,"has_art":true}]}
```

Folders have `has_art: true` (use `/api/files/art?path=<id>`), files `false`. An `id` goes back into `path` to open a folder, or into [`/api/enqueue`](#play-and-add-to-the-queue) to play it. Paths are relative to the music folder, never absolute.

## Queue

`GET /api/queue` returns the queue as the web page reads it: `tracks`, `play_next`, `played`, `playlist_revision`, `count`, `truncated`. For a device:

```sh
curl 'http://192.168.1.20:8730/api/queue?compact=1&offset=0&limit=2'
```
```json
{"count":1,"items":[{"id":"","title":"Row A","subtitle":"","kind":"track","playable":true,"has_art":true}],"limit":2,"offset":0,"playlist_revision":7}
```

`offset` defaults to 0, `limit` to 200 and is capped at 200. A `limit` below 1 or an `offset` below 0 is `400`. The playing row is the one whose position equals `status.index`. Edit the queue with `queue.play`, `queue.remove`, `queue.move` and `queue.clear` ([Commands](#commands)).

## Play and add to the queue

`POST /api/enqueue`:

```json
{"kind":"album","provider":"navidrome","id":"al1","mode":"play"}
```

| Field | Values |
|---|---|
| `kind` | `track`, `album`, `playlist`, `artist`, `file`, `folder` |
| `provider` | the provider `key`; needed for `album`, `playlist` and `artist`, not for `file`, `folder` or `track` |
| `id` | the row's `id`; for `file` and `folder` the folder path |
| `mode` | `play` (clear the queue and start) or `append` (add to the end) |
| `track` | for `kind` `track`: a track object with at least `path` (a row's `id`), plus `title` and `artist` |

It answers `202 {"ok":true}` right away; large jobs (an artist, a 12,000-track playlist) run in the bridge and report progress as `enqueue` events on `GET /api/events`. A second request while one is running is `409 busy`; `POST /api/enqueue/cancel` stops the running one. A local file path the bridge never handed out is refused with `403`. Stream URLs are accepted.

## Pictures

| Endpoint | Picture |
|---|---|
| `GET /api/art/cover?path=<track path>` | album cover of a track (use `status.art`) |
| `GET /api/art/artist?path=<track path>` | artist photo |
| `GET /api/art/disc?path=<track path>` | disc image (drawn from the cover when there is none) |
| `GET /api/art/item?provider=navidrome&kind=album\|artist\|playlist&id=<id>` | cover of a listed album, artist or playlist |
| `GET /api/art/station?id=<station id>` or `?name=<name>` | radio logo |
| `GET /api/files/art?path=<folder>` | folder cover |

Every one answers `200` with the picture, or `404 {"code":"no_art"}` when there is none. They carry an `ETag`; send `If-None-Match` to get `304`.

**`size=N`** returns a JPEG whose long side is at most N pixels (the picture is never enlarged). N is clamped to 32 to 512; a value that is not a whole number is `400`. Without `size` you get the picture as the bridge holds it (JPEG or PNG, up to about 300 pixels for folders). Resized pictures are kept in memory by the bridge, so asking again is cheap. Good sizes: 64 for list rows, 240 to 480 for Now Playing.

```sh
curl -o cover.jpg 'http://192.168.1.20:8730/api/art/cover?path=/fake/a1-0.flac&size=240'
```

A picture that cannot be decoded (some WebP files) is a `404` when `size` is asked for.

## Other endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/events` | the browser's Server-Sent Events stream: `state`, `playback`, `playlist`, `job`, `player`, `favorites`, `enqueue`, `resync`, `ping`. Full payloads; a device should use the WebSocket. |
| `GET /api/capabilities` | cliamp's operations |
| `GET /api/about` | bridge information |
| `GET /api/lyrics` | lyrics of the playing track |
| `GET /api/trackinfo?path=&duration=` | codec, bitrate, sample rate |
| `GET /api/favorites`, `PUT /api/favorites` | list; set with `{"kind","provider","id","favorite":true}` |
| `GET /api/radio/search?q=`, `POST /api/radio/meta` | radio directory |

## Errors

Errors are JSON with a `code` (and often an `error` text). A picture failure is a plain `404 {"code":"no_art"}`.

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_request` | a parameter is missing or malformed (`offset`, `limit`, `size`, a body) |
| 403 | `forbidden_operation` | `/api/command` refuses this operation |
| 403 | `forbidden` | an unknown track, or a foreign `Origin` |
| 404 | `not_found`, `no_art`, `no_files`, `no_favorites` | nothing there, or the feature is off |
| 409 | `conflict`, `busy` | the queue changed (`if_revision`), or an enqueue is running |
| 415 | `invalid_request` | a write without `Content-Type: application/json` |
| 429 | `rate_limited` | too many commands |
| 502 | `upstream_error` | the provider (Navidrome and so on) failed; `detail` says how |
| 503 | `unavailable` | the bridge is not connected to cliamp |
| 504 | `job_timeout` | the operation was still running when the bridge stopped waiting |

## Notes for an ESP32 app

- **One connection is enough.** Open the WebSocket for Now Playing, use `POST /api/command` for buttons and plain `GET`s for browsing. `esp_websocket_client` handles reconnecting; set its ping interval to 15 seconds or more and treat a missing `ping` frame for 40 seconds as a dead link.
- **Parse small things.** With `compact=1` a row is under about 150 bytes plus the text, so a page of 20 rows fits in about 4 KB; ask for `limit=20` to match the screen. A `status` frame is about 300 bytes. Keep the receive buffer at 2 KB or more for the WebSocket and 8 KB for list responses.
- **Pictures.** Ask for `size=` exactly as large as the widget (64 for rows, the cover box for Now Playing); the P4's hardware JPEG decoder takes the bridge's baseline JPEG as it is. Cache by the `art` value and by `ETag`.
- **Always send** `Content-Type: application/json` on `POST` and `PUT`; do not send an `Origin` header.
- **Seek and volume feel better when sent once** on release, not on every slider step (`POST /api/command` is rate limited).
- **Playing things.** An album, playlist or artist needs only its `id` (`POST /api/enqueue`). A radio station is `provider.load` with the row's `id` as `playlist`. A single track needs a track object: build it from the compact row (`{"path": id, "title": title, "artist": subtitle}`); if your cliamp wants more fields for some track, take that track's object from the full list (without `compact=1`). The compact route was checked against the bridge's fake player, not against every provider.
- **Time:** the player's clock is not yours. Interpolate `position` between frames while `state` is `playing`, and replace it with each new `status`.
