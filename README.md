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
- **Now Playing** full-screen page: typographic hero, full-width scrubber,
  large transport/volume/shuffle/repeat controls, "Up next" tap-to-play; the
  `Now Playing · Queue · Browse` tab shell keeps a mini player on the other
  views.
- Tap a playlist to page through its real tracks; ▶ plays any one,
  “play playlist” (confirm) replaces the queue
- Radio stations start on tap (confirm) — no load buttons anywhere
- Lyrics sheet on the Now Playing page, current line follows the track,
  tap a line to jump there
- The tab bar and mini-player stay pinned; rows read “title · artist”
  with ellipsis; the live connection reconnects by itself (lid close,
  bridge restart, iOS sleep)

Listings refresh on a bounded TTL (60 s default); a provider outage shows an
inline retry banner that keeps the rows already on screen.


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

It writes `~/.local/share/cliamp/web/status.json` (`0600`) containing the URL
**with the access token**:

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

- Every request except `/api/health` needs a 32-byte hex token (query `?token=`
  for GET/SSE, `X-Token` header for POST). Token lives in
  `~/.local/share/cliamp/web/token`, mode `0600`.
- Default bind is `127.0.0.1`; `--lan` / `lan = true` is an explicit opt-in.
- Host header is validated against loopback names plus `allowed_hosts`
  (DNS-rebinding guard).
- `/api/command` is rate limited (20 req/s, burst 40).
- On LAN the token travels in cleartext HTTP. For anything beyond a trusted
  network, terminate TLS with a reverse proxy and add the hostname:

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
make ui      # npm ci + vite build → webui/dist (committed, go:embed)
make build   # dist/cliamp-web (embeds webui/dist)
make test    # go vet + go test
```

Update the installed dev binary: `make build && cp dist/cliamp-web ~/.local/bin/cliamp-web`.


Hot-reload the UI during development: `cliamp-web serve` on one terminal,
`cd webui && npm run dev` on another, then open
`http://localhost:5173/?token=<token>` — Vite proxies `/api` to `:8730`.

## License

MIT — see [LICENSE](LICENSE).
