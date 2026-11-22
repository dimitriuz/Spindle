# Fullscreen Now Playing

Date: 2026-11-22. Status: awaiting approval. Scope agreed in conversation; "My calls" are proposals.

## Intent

A fullscreen player for the phone, tablet or a wall screen: artist (with photo), album and
year, track number and title, progress, technical info (codec, bitrate, ...), the next
track, and the cover and disc art already built. BerryAudio's fullscreen player (blurred
cover backdrop, centred cover, tech line between elapsed and total time) is the reference.

## Goals

- Show everything above in one screen, readable from a distance.
- Wide screens show cover and disc side by side; narrow ones show one and switch between them.
- With the cover selected, part of the spinning disc peeks out from behind it (a record half
  out of its sleeve); the disc slides out when playback starts and back in when it stops.
- Tech info for Navidrome and local tracks; missing pieces are hidden, never shown as blanks.

## Non-goals

- Radio metadata, lyrics, volume, queue editing inside the fullscreen view.
- Real-time output format of the DAC (only the format of the file being played).
- Outside services.

## Findings

- cliamp exposes no codec or bitrate. Navidrome's `getSong` has `suffix`, `bitRate`,
  `bitDepth`, `samplingRate`, `channelCount` (verified on the Pi: flac, 1161 kbps, 24 bit,
  44100 Hz, 2 ch). The art code already calls `getSong`.
- The track object carries `album`, `year`, `track_number`, `artist`, `title`, `duration_secs`.
- The queue (`/api/queue`) has rows and play-next; the state has the current `index`,
  `shuffle` and `repeat`. With shuffle on, cliamp's real next track is not visible.

## Approaches

1. **Restyle the existing Now Playing as a fullscreen mode (chosen).** The DOM already has
   cover, disc, title, artist, seek bar and transport, all wired to state. Fullscreen adds a
   class, a backdrop and a few extra info elements. No duplicated logic.
2. A separate overlay with its own copies of the bar, buttons and art. Rejected: every
   state fix would have to be made twice.
3. The browser Fullscreen API alone. Rejected as the only mechanism: iOS Safari does not
   offer it for pages. It is used where available, on top of the fixed in-page layout.

## Design

**Entering.** A fullscreen button on Now Playing opens the mode: the Now Playing view becomes a
fixed layer covering the viewport (and calls `requestFullscreen()` where the browser allows).
Close with the button, Escape, or a swipe down. The choice (cover/disc) is remembered for the
session.

**Layout.** Blurred, darkened cover as the backdrop. Top: artist photo and name. Middle: the
art. Below: `04 · Cadenza` (track number and title), `Album · 2018`, the progress bar with
elapsed and total, and the tech line centred between them, e.g. `FLAC • 1161 kbps • 24 bit •
44.1 kHz`. Then `Next: <title> — <artist>`, then previous / play-pause / next.

**Art, enough room vs not.** "Both" when the area is landscape (aspect >= 5/4) and at least
700 px wide: cover left, full disc right, disc spinning with the same motor model as now.
Otherwise one at a time: the cover with the disc peeking out (see below); tapping flips to the
disc and back with the existing flip animation.

**Disc peek.** The disc sits behind the cover, centred at 85% of the cover width from its
left edge, so about 35% of its diameter shows beyond the right edge. It is the same element and
the same physics as today. It slides in under the cover when playback stops and out when it
starts, as a 700 ms eased transition. With no disc for the album, nothing peeks.

**Tech info.** New route `GET /api/trackinfo?path=<track path>[&duration=<seconds>]` returning
`{format, bitrate_kbps, bit_depth, sample_rate, channels}` with absent fields omitted; 404 when
nothing is known. Same gating as art (only tracks the bridge knows, scrubbed URLs restored), so
it cannot be aimed at other hosts or files. Navidrome: from `getSong` (cached per song).
Local: format from the file's tag type; FLAC also gives sample rate, bit depth and channels
from its STREAMINFO header; for other formats an average bitrate from file size and the
supplied duration. Implemented in `internal/art` because it shares the resolver, its gating
and the Navidrome client.

**Next track.** Pure function `nextTrack(state, queue)`: first play-next entry; else the queue
row after the current index (wrapping to the first when repeat is All); nothing when shuffle
is on or repeat is One. Hidden when there is none.

**Failure behaviour.** Any missing piece (no art, no tech info, no next track, no year) leaves
its element out and the rest of the layout intact.

## My calls (please check)

- Transport buttons (prev, play/pause, next) are included although not requested; a
  fullscreen player without them is awkward. No volume, queue or lyrics.
- Swipe-down and Escape close it; a visible close button too.
- Layout thresholds (aspect 5/4, 700 px) and the 35% peek are starting values, tuned on a phone
  and a desktop window.
- Local-file tech info is best-effort: full for FLAC, format plus average bitrate for others.
- The browser Fullscreen API is used where available; the page still works without it.

## Left to the builder

CSS details, the slide timing, how the info elements are named, test fixtures. Tests must
include: `nextTrack` for play-next, last track, repeat All/One, shuffle, empty queue;
`trackinfo` for Navidrome (fake server) and a FLAC fixture, an unknown path and an unknown
host (refused, no request); the tech-line formatter (missing fields, 44.1 vs 96 kHz, kbps);
and a headless-browser check of both layouts and the peek.
