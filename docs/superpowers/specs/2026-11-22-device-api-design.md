# Device API: live updates, compact browsing, sized art

## Intent

An ESP32-P4 board with a touch screen, running no browser, should work as a remote for Spindle: a Now Playing screen and library browsing. The bridge already serves an HTTP and JSON API (README, "HTTP API (for scripts)") and an SSE stream, and a non-browser client can use it today. This work makes that API comfortable for a small device and documents it as a contract.

## Goals

- Live now-playing updates over a WebSocket with a compact payload.
- Compact list responses for browsing, searching, the file browser and the queue.
- Art in a requested size, always JPEG.
- A reference document, `docs/api.md`, that an app author can build from.

## Non-goals

- Authentication. The bridge stays open on the LAN as it is today. An optional `--token` is a possible later step.
- Commands over the WebSocket. Commands stay on `POST /api/command` and `POST /api/enqueue`.
- Changing or removing anything the web page uses: `/api/events` (SSE) and the existing full responses stay as they are.

## Anti-goals

- A second, divergent code path for state. The compact frames are derived from the same cache and hub the SSE stream uses.
- Anything that lets the board reach operations `allowedOps` already blocks.

## Approaches considered

1. **WebSocket `/api/ws` with compact frames** (chosen). ESP-IDF has `esp_websocket_client` with reconnect and ping/pong. Cost: a WebSocket dependency or a small handshake of our own.
2. SSE with `?compact=1`. No dependency, but streaming HTTP parsing and reconnects are harder on the device.
3. Long-poll or `GET /api/status`. Simplest on the device, not live.

Chosen by the user's partner in conversation: option 1 together with compact payloads.

## Design

### 1. WebSocket `GET /api/ws`

- Same guard as other endpoints (`protect`); the Origin rule applies when an Origin header is present.
- On connect the server sends `status` and `queue` frames, then:
  - `status` on any player change: title, artist, album, position, duration, volume, paused, shuffle, repeat, queue index, `art` (a key for `/api/art/...`), online flag. Position is sent on change and not more often than once a second.
  - `queue` on queue change: compact items (see section 2).
  - `resync` after dropped frames, same rule as the SSE hub; the client refetches `status` and `queue`.
  - `ping` heartbeat; the server answers client pings.
- Frames are JSON text: `{"type":"status", ...}`.
- Fan-out reuses `Hub` frames; a translator maps the existing event names to compact frames. Slow clients drop and resync, never block cliamp.
- Stream URLs are scrubbed the same way as in SSE (`urls.Scrub`).

### 2. Compact lists, `?compact=1`

Applies to the library lists, drill-downs, `/api/search/{provider}`, `/api/files`, and `/api/queue`. Items carry only `id`, `title`, `subtitle`, `kind`, `playable`, `has_art`. Paging fields (`offset`, `limit`, `total` where known) are kept. Without the parameter nothing changes.

### 3. Art `?size=N`

On `/api/art/*`, N is clamped to 32..512, default as today. Output is JPEG. Results are cached per size by the existing art cache.

### 4. Docs

`docs/api.md`: every endpoint with parameters and example responses, the WebSocket protocol and frame shapes, paging, art sizes, error codes, the command operations, an ESP32 section (JSON buffer sizes, reconnect, headers: `Content-Type: application/json` on writes and no `Origin`), and curl examples. README's API section links to it.

### 5. Tests

Go tests in the style of `internal/server/*_test.go`: WebSocket connect, frame sequence, drop and resync, compact shapes against fixtures, `size` clamping and cache keys.

## Constraints

- Go standard library plus at most one small, maintained WebSocket package; no cgo, builds for arm and macOS stay intact.
- `webui` untouched.

## Left to the builder (builder's calls)

- Exact field names inside the compact items, kept consistent with existing JSON names where possible.
- WebSocket library choice (or a hand-written server side), judged by binary size and maintenance.
- How `total` is exposed where a provider does not know it.
