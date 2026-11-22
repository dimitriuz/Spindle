# Now Playing art: cover, disc, artist

Date: 2026-11-22. Status: awaiting approval. Approach and scope agreed in conversation;
everything under "My calls" is a proposal, not something agreed.

## Intent

Now Playing shows an empty placeholder where the cover should be. The owner listens to a
Navidrome library and local files on a Raspberry Pi and wants real artwork on the phone:
the album cover, the disc image (the `disc.png` in the album folder, as already done on the
ESP32-P4 player in `esp32-p4-hifi`), and an artist photo.

## Goals

- Cover, disc and artist images for tracks from **Navidrome** and from the **local** provider.
- Works through the bridge only. cliamp itself is untouched.
- Images are fetched once and cached on disk; the phone never sees provider credentials.
- Anything missing degrades quietly to today's placeholder.

## Non-goals

- Radio, podcasts and every other provider: they keep the placeholder.
- Any lookup on an outside service (MusicBrainz, iTunes, Deezer, radio-browser). Nothing
  leaves the home network except requests to the user's own Navidrome.
- Art in the queue, browse and search rows (cheap follow-up on the same route, not part of this).
- Showing art in cliamp's TUI, a cliamp plugin, or a cliamp fork.

## Anti-goals

- A route that can read arbitrary files or fetch arbitrary URLs.
- Credentials (`u`/`s`/`t`) reaching the browser, logs, or the disk cache keys.
- A slow or missing image blocking the page or the playback controls.

## Findings that shape the design

- cliamp's IPC track object has no art URL, and the plugin event `album_art_url` is empty for
  Navidrome and radio on cliamp v2.4.0 (probed on the Pi). A plugin cannot help, and it could
  not return image bytes to the page anyway. A fork is not needed for this.
- Navidrome's Subsonic `getCoverArt` accepts prefixed ids: `al-<albumId>` cover,
  `dc-<albumId>:1` disc (the `disc.png` in the album folder; not documented Subsonic, works on
  Navidrome 0.64.2 per the ESP32 player), `ar-<artistId>` artist image. A missing disc comes
  back as something that is not an image, which means "no disc".
- A track gives us only its song id (`provider_meta["navidrome.id"]`, also the `id=` of the
  stream URL). `getSong` returns its `albumId` and `artistId`.
- The bridge already holds the original stream URL (credentials included) for every track it
  has shown the browser (the redactor from the previous change), so it can talk to Navidrome
  without any new configuration.

## Approaches

1. **One resolver in the bridge (chosen).** A `GET /api/art/{kind}` route; per-source
   resolvers behind it (Navidrome, local files); disk cache.
2. A cliamp Lua plugin that forwards art. Rejected: nothing to forward (probe), no way to send
   binary data to the page.
3. Fork cliamp to expose art over IPC. Rejected: ongoing maintenance for something the bridge
   can do alone. Could return as an upstream PR later if TUI art is wanted.

## Design

### Route

`GET /api/art/{kind}?path=<track.path as the browser sees it>` with `kind` in
`cover | disc | artist`.

- 200 with the image, `Cache-Control: public, max-age=86400`, and an `ETag` from the cache key
  (so different tracks of one album revalidate to 304 instead of re-downloading).
- 404 `{"code":"no_art"}` when the source has none. Never 5xx for "no art".
- 400 for a bad `kind` or a missing `path`; the route sits behind the existing request guard.

### Sources

**Navidrome.** Recognised by a stream URL whose path ends in `/rest/stream` with an `id`
parameter, so any Subsonic-compatible server resolves cover and artist; disc only works where
the server supports `dc-` ids. Steps: restore the original URL (redactor) -> `getSong` ->
`albumId`, `artistId` (cached in memory per song id) -> `getCoverArt` with `al-`, `dc-<id>:1`,
`ar-`. The host and credentials come only from that original URL, so the route can fetch from
no other host. `size=600` for cover and artist, original size for disc.

**Local.** A track whose path is an absolute file path. The path is accepted only if it
belongs to a track the bridge currently knows (now playing, queue, play-next); anything else
is 404. Lookup order, all inside the track's own directory:
- cover: embedded picture in the file (ID3 APIC, FLAC PICTURE, MP4 `covr`, Ogg), else
  `cover|folder|front|album` + `.jpg|.jpeg|.png|.webp`
- disc: `disc.png` or `cdart.png` (the names the ESP32 tool uses)
- artist: `artist.jpg|.png` in the album directory or its parent
Resolved files must stay inside the track's directory tree after symlinks are resolved, and
only image files of at most 10 MB are read.

### Cache and limits

- Disk cache under `<data-dir>/art/`, key = hash of (source, art id, kind), never the URL or
  any credential. Total size capped at 200 MB, oldest-first eviction.
- Misses are remembered in memory for 10 minutes so a missing disc is not re-requested on every
  track change.
- One in-flight fetch per key (concurrent requests share it). Upstream timeout 10 s, response
  cap 10 MB, content type must be `image/*` (checked on bytes, not the header alone).
- Navidrome being down yields 404 for that request and does not poison the cache.

### Page (Now Playing)

- The existing `.cover` box shows the cover image, falling back to the placeholder on 404 or
  error; fades in when loaded; requested only when the track changes.
- Tap the cover to flip to the disc when one exists: a short quarter-turn animation, then the
  disc turns at 33 1/3 rpm while playing and holds still while paused, as on the ESP32 player.
  Tap again to flip back. With no disc nothing changes.
- A small round artist photo next to the artist name; hidden when there is none.
- Tracks without art (radio) look exactly as they do now.

## My calls (please check)

- Disc UX copies the ESP32 player (tap to flip, spins while playing).
- Artist photo is a small avatar by the artist name, not a full-size view.
- One new dependency: `github.com/dhowden/tag` (MIT, pure Go) for embedded pictures. The
  project has had none so far. The alternative is about 150 lines of hand-written ID3/FLAC/MP4
  parsing; the library is less code to own. `PKGBUILD` then needs a `go mod download` step.
- Folder-image names and the artist-image location for local files are my guess.
- The art cache lives in the existing data dir and is capped at 200 MB.

## Left to the builder

File layout inside `internal/` (suggest `internal/art`), cache eviction details, image
sniffing helper, test fixtures. Tests must include: a fake Subsonic server (success, no disc,
server down, non-image reply), an embedded-picture fixture and a folder-image fixture, a
path-escape attempt (`../`, symlink, unknown path), credential-free cache keys and 404 bodies,
and a live check on the Pi with a real Navidrome album that has a `disc.png`.
