# cliamp-plugin-web

A LAN web remote for [cliamp](https://github.com/bjarneo/cliamp), the retro
terminal music player. A tiny Lua plugin supervises a self-contained Go bridge
that serves a mobile-friendly web UI over your local network. Control playback,
scrub, set volume, and edit the live playlist and play-next list from any
browser — the TUI and the web UI stay in sync within ~1 s in both directions.

```
browser ──HTTP/SSE──> cliamp-web (Go bridge, port 8730)
                          │  cliamp.sock (V2 NDJSON IPC)
                          ▼
                        cliamp  ◄── web.lua plugin spawns & supervises it
```

## What the phone can do

On top of the v1 controls (transport, scrub, volume, shuffle/repeat, playlist
and play-next editing), v2 adds choosing music itself:

- **Browse** every provider the player reports (`provider.list`): playlists,
  albums, artists, radio catalog — capability-adaptive tabs, paginated
  infinite scroll, drill-down to album/artist track lists with breadcrumb
  back-navigation.
- **Search** each searchable provider (debounced typing; results are tracks).
- **Act on results**: play now (▶) / queue next (+) on any track row; load a
  playlist or an album into the queue behind a confirm — those ops replace the
  queue, so the confirm is the guard.
- **Now Playing**: big round transport, drag-seek bar, volume in dB,
  shuffle/repeat; the `Now Playing · Queue · Browse · Search` tab shell is
  pinned to the bottom; Queue also manages the Play-next list (remove, move).
- Tap a playlist to page through its real tracks; ▶ plays any one,
  “play playlist” (confirm) replaces the queue
- Radio stations start on tap (confirm) — no load buttons anywhere
- Lyrics sheet on the Now Playing page, current line follows the track,
  tap a line to jump there
- The tab bar stays pinned; rows truncate with ellipsis and a muted second
  line; the live connection reconnects by itself (lid close, bridge restart,
  iOS sleep)

Listings refresh on a bounded TTL (60 s default); a provider outage shows an
inline retry banner that keeps the rows already on screen.


## The interface

The page is served at `/` from three embedded vanilla-JS files (`webui/`:
`index.html`, `app.css`, `app.js`) — no build step, no framework — ported
from the MiSTer Subsonic web remote. Its pure core is node-tested:
`node webui/app_test.js` (part of `make test`). The earlier Vue app is
frozen under `archive/webui-vue` and no longer served; new features land in
the vanilla page only.


## Artwork

Now Playing shows the album cover with the disc peeking out from behind it, and
a small artist photo next to the artist name. The disc slides out when playback
starts and back in when it stops. Tap the cover to flip to the disc; the disc
spins at 33 1/3 rpm while playing, brakes when you pause and coasts to a stop
when playback stops.

- **Navidrome** (and other Subsonic servers): cover and artist photo come from
  `getCoverArt`; the disc is the `disc.png` in the album folder, fetched with
  Navidrome's `dc-<albumId>:1` id (not part of the documented Subsonic API;
  works on Navidrome 0.64.2, and where it is missing the page simply has no disc).
- **Local files**: the cover is the picture embedded in the file, else
  `cover|folder|front|album` + `.jpg|.jpeg|.png|.webp` in the album folder;
  the disc is `disc.png` or `cdart.png`; the artist photo is `artist.jpg|.png`
  in the album folder or its parent. Only files of tracks cliamp has reported
  are ever read.
- **No disc image?** The bridge draws one from the cover (a printed CD with a
  clear rim, hub and spindle hole) and caches it, so every album with a cover
  has a disc to peek, flip and spin. Navidrome answers disc requests with the
  cover when there is no `disc.png`; that is detected (a real disc is a PNG with
  transparent corners) and replaced by the generated one.
- **Radio and other providers** have no artwork: the placeholder stays.

Pictures are fetched once and cached under `<data-dir>/art/` (200 MB cap, least
recently used removed first). The bridge only ever contacts the Navidrome host
named in the track's own stream URL; nothing goes to outside services.

## Favorites

A heart on anything: tracks, albums, artists, playlists and radio stations, on
list rows, on the album and artist pages, and on Now Playing. The Favorites tab
lists them all with a chip per kind.

- **Tracks and stations are cliamp's own favorites.** Hearting a track puts it in
  cliamp's Favorites playlist (and cliamp stars it on Navidrome); hearting a
  station adds it to cliamp's favorite stations. The bridge keeps no second copy,
  so the two never disagree; changes made in the cliamp TUI show up on the next
  refresh (opening the tab, any change, or every 60 s).
- **Albums, artists and playlists** have no favorites in cliamp, so the bridge
  keeps them in `<data-dir>/favorites.json` (title and ids only).
- A radio station can be favorited only from the radio catalog (a cliamp limit);
  it can be removed from anywhere, including Now Playing.
- Existing stars on your Navidrome server are not imported, and albums, artists
  and playlists are not starred there.

## Fullscreen

The fullscreen button on Now Playing opens a full-window player: blurred cover
backdrop, artist photo and name, `04 · Title`, album and year, progress, a tech
line (`FLAC • 1161 kbps • 24 bit • 44.1 kHz`) and the next track. Close it with
the button, Escape or a swipe down.

- **Wide windows** (at least 700 px, landscape) show the cover and the spinning
  disc side by side. **Narrow ones** show the cover with the disc peeking out
  from behind it, sliding out when playback starts and back in when it stops;
  tap to flip to the disc.
- **Skipping** next or previous: within one album the platter stumbles to a third
  of its speed and the motor brings it back; moving to another album changes
  cover and disc together: the cover slides out and fades (the disc retracts under
  it when narrow, slides off beside it when wide), the pictures swap while hidden,
  and the new ones come in from the other side, in the direction you moved.
- **Tech info** comes from Navidrome (`getSong`), from the FLAC header for local
  FLAC files, and from the tag type plus average bitrate for other local formats.
  Anything unknown is simply left out.
- **Next track** is the first play-next entry, else the following queue row
  (wrapping on repeat-all). It is hidden with shuffle on, because cliamp's real
  order is not visible, and with repeat-one.

## Install

1. `cliamp plugins install <user>/cliamp-plugin-web`
2. install the `cliamp-web` binary (GitHub release, or AUR/PKGBUILD)
3. `allowed_binaries = "cliamp-web"` + `cliamp plugins trust web`
4. start cliamp → message shows the URL; open it.

Step 3 goes in `~/.config/cliamp/config.toml`:

```toml
[plugins]
allowed_binaries = "cliamp-web"
```

> **Version note:** on cliamp ≤ v2.0.1 the headless `--daemon` mode does **not
> load Lua plugins** — run the TUI, or upgrade cliamp (≥ v2.2 loads plugins in
> daemon mode too).

### Plugin configuration

`~/.config/cliamp/config.toml`:

```toml
[plugins.web]
port = 8730          # HTTP port (default 8730)
lan = false          # true ⇒ bind 0.0.0.0 (LAN-visible)
allowed_hosts = "cliamp.example.lan"  # extra Host values to accept
binary = "cliamp-web"                  # supervised binary
```

## Running the bridge without the plugin

The plugin is only a supervisor; the bridge is a normal binary:

```sh
cliamp-web serve --port 8730 [--lan] [--socket PATH] [--data-dir DIR] \
                 [--allowed-hosts LIST] [--verbose]
```

It writes `~/.local/share/cliamp/web/status.json` (`0600`) with the URL, port
and pid:

```sh
jq -r .url ~/.local/share/cliamp/web/status.json
```

Headless recipe (no plugin, cliamp ≥ v2.0.1):

```sh
cliamp --daemon --auto-play --playlist <name> &
cliamp-web serve --lan &
jq -r .url ~/.local/share/cliamp/web/status.json
```

## Security

There is **no login**: this is built for a trusted home network, and anyone who
can reach the port can control playback. Do not expose it to the internet.
What it does guard against:

- Default bind is `127.0.0.1`; `--lan` / `lan = true` is an explicit opt-in.
- Host header is validated against local addresses plus `allowed_hosts`
  (DNS-rebinding guard).
- State-changing requests must be `application/json` from the same origin, so
  a web page open in a browser on your LAN cannot fire commands at the bridge.
- `/api/command` only forwards the operations the UI uses (transport, volume,
  queue and play-next edits, provider load). Plugin calls, URL loading and
  destructive ops such as `history.clear` are refused.
- Stream URLs that carry provider credentials (Navidrome's `u`/`s`/`t`) are
  scrubbed from everything sent to the browser; the bridge keeps the original
  and swaps it back in when a track is played or queued.
- `/api/command` is rate limited per client IP (20 req/s, burst 40).
- Traffic is plain HTTP. For anything beyond a trusted network, terminate TLS
  (and add auth) in a reverse proxy and list the hostname:

```
# Caddyfile
cliamp.example.lan {
    reverse_proxy 127.0.0.1:8730
}
```

```toml
[plugins.web]
lan = true
allowed_hosts = "cliamp.example.lan"
```

## Development

```sh
make build   # dist/cliamp-web — embeds webui/ (go:embed, no JS toolchain)
make test    # go vet + go test + node webui/app_test.js
```

Editing the UI means editing `webui/*.{html,css,js}` and rebuilding the
binary; hard-reload the phone page afterwards. Update the installed dev
binary: `make build && cp dist/cliamp-web ~/.local/bin/cliamp-web`.

## License

MIT — see [LICENSE](LICENSE).
