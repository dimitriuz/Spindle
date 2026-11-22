# Chiptune and tracker formats with cliamp: findings

Date: 2026-11-22. Status: **research only, nothing built.** The question was whether cliamp can be extended
to play tracker and chip music (MOD, SID, PT3, VGM and similar), for example through a plugin or a provider.

## Short answer

- cliamp's list of formats is fixed in its Go code. **Lua plugins cannot add decoders.**
- cliamp can only play more formats if its own Go code changes (a fork or an upstream pull request).
- ZXTune (`zxtune123`) plays all the formats we tried and is fast enough, but the only way we found to use it
  from a provider is to render each tune to a WAV file first. That was **rejected** (too slow to start, and it
  writes a lot to the SD card). Revisit if a streaming route turns up.

## How cliamp picks a decoder (cliamp v2.4.0, `player/decode.go`)

- `SupportedExts` is a hard-coded set: `mp3 wav flac ogg m4a aac aacp m4b alac wma opus webm`. It decides which
  files the player and its folder scans accept at all (`external/local/dirs.go`, `resolve/resolve.go`), so
  `.mod`, `.sid` and friends are skipped even before decoding.
- Four formats (`mp3 wav flac ogg`) have built-in Go decoders. Everything else in the list goes through `ffmpeg`
  (`needsFFmpeg`). An unknown extension falls through to the MP3 decoder and fails.
- Custom sources exist only for Go code compiled into cliamp: `RegisterStreamerFactory("spotify:", ...)` returns
  decoded audio and a duration; `RegisterSourceResolver("tidal://", ...)` returns a URL for the ffmpeg pipeline.
  Neither is exposed to Lua (`luaplugin/` has no hook for them).
- Lua plugins (`docs/plugins.md`) can run allowed programs (`cliamp.exec`, default allowlist `yt-dlp`, `ffmpeg`),
  add files to the queue (`cliamp.queue.add`) and react to events, but cannot register a decoder, provider or scheme.
- A new **provider** is a Go package under `external/` compiled into cliamp, so it means a fork or a pull request.
  Any provider cliamp reports appears in Spindle's Browse tab with no Spindle changes.

## Routes considered

| Route | Covers | Needs | Verdict |
|---|---|---|---|
| Add extensions to `SupportedExts` and `needsFFmpeg` (a few lines in cliamp) | whatever the Pi's ffmpeg demuxes | cliamp patch | Plausible for trackers and VGM, not tried. See below. |
| ZXTune provider in cliamp (Go package, shells out to `zxtune123`) | everything below | cliamp fork or PR | Works technically, but needs a WAV render per tune: **rejected for now** |
| Render in Spindle or a Lua plugin, then queue the file | everything below | external renderers | Same WAV render problem |
| Ask upstream cliamp | depends | the maintainer | Not tried |

### ffmpeg on the Pi (Debian 13, ffmpeg 7.1.5)

`ffmpeg -demuxers` lists **libopenmpt** (tracker formats: mod, xm, it, s3m, ...) and **libgme** (Game Music Emu:
vgm/vgz, nsf, spc, gbs, hes, kss, ay, sap, gym). So a patch that adds those extensions to cliamp's ffmpeg list
might be enough for those formats. Not tested: seeking, track length and metadata behaviour. ffmpeg has no
decoder for **SID** or **PT3/PT2/STC** (ZX Spectrum trackers).

## ZXTune test

- **Binary:** the official release `zxtune_r5112_linux_arm64.tar.gz` from
  `github.com/vitamin-caig/zxtune/releases` (24 MB). Runs on the Pi unmodified; needs only libz. License:
  LGPL-3.0 (as reported by GitHub; not independently checked). Unpacked to `~/zxtune-test` on the Pi.
- **Formats:** `zxtune123 --list-plugins` shows 382 plugins. Among them tracker formats (MOD, XM, IT, S3M, AHX,
  HVL, MED, ...), ZX Spectrum and AY formats (PT1/2/3, STC, ASC, STP, PSG, TS, AY, YM, VTX, ...), SID, NSF, GBS,
  HES, SAP, KSS, SPC, VGM, S98, PlayStation/N64/GBA/DS sound formats, V2M, and containers (ZIP, RAR, 7-Zip, LHA).
  **MIDI is not supported.**
- **Render test** on the Pi (aarch64, 4 cores, 8 GB), real files from the NAS collections, rendered to
  44.1 kHz 16-bit stereo WAV:

| Format | Audio length | Render time |
|---|---|---|
| mod | 323 s | 1.7 s |
| xm | 395 s | 5.5 s |
| it | 237 s | 11.1 s |
| ahx | 54 s | 0.3 s |
| sid (single song) | 43 s | 3.7 s |
| pt3 | 62 s | 1.1 s |
| pt2 | 154 s | 2.2 s |
| stc | 92 s | 1.3 s |
| vgz (PC Engine, unpacked from a ZIP) | 87 s | 2.3 s |
| sid, Arkanoid sub-tune 1 | 142 s | 12.0 s |

Typical rendering runs 12 to 200 times faster than real time.

- **Other things found**
  - Metadata: `zxtune123 --file F --index "filename=out.csv,csv=[Subpath]|[Title]|[Author]|[Program]|[Type]"`
    takes about 0.2 s and lists one line per sub-song (Arkanoid.sid: 20 lines, `#1` to `#20`).
  - Sub-songs: select with `--file "path?#3"`.
  - Duration: the normal run prints `Time:` and `Loop duration:`. Each tune plays through once including one
    loop pass, so endless loops need no special handling.
  - Output: `--wav filename=PATH` works. `--wav filename=-` (stdout) **fails** with "Failed to initialize
    playback". Other backends: null, alsa, paudio, oss, mp3, ogg.
  - Not tested: opening files inside ZIP/7z directly, the `--convert` modes, sample rate options, running two
    instances at once, memory use.

## Why the WAV-render provider was rejected

- A WAV is about 10 MB per minute of audio, so a collection of tens of thousands of tunes needs a managed cache.
- First play waits for the whole render: about 1 to 3 s typically and up to about 12 s for long tunes, with
  nothing to hear meanwhile.
- A cache on the SD card means constant large writes, which wears the card.

## Ideas not tried (for later)

- **Stream instead of render:** a ZXTune build or option that writes PCM to stdout, or a small wrapper around
  its library, so cliamp could start playing at once and write nothing to disk. Needs checking ZXTune's
  source and its other backends (raw output, `--convert`).
- **Render to RAM (`/dev/shm`)** instead of the SD card, deleting each file after use. Removes the SD wear but not
  the wait.
- **Render ahead:** start the next tune's render while the current one plays, hiding the wait in a playlist.
- **The small cliamp patch** (extensions for ffmpeg's libopenmpt and libgme) as a first step for trackers and
  VGM, leaving SID, PT3, AY and YM for another tool.
- **Another player beside cliamp** for chip music, with Spindle switching between them. Heavy and awkward.
- **Ask upstream cliamp** whether a generic "external decoder command" (read a file, write PCM to stdout) would
  be acceptable, which would cover ZXTune, `sidplayfp` and `openmpt123` without any renderer being built in.

## Reproducing the test

```sh
# on the Pi
mkdir -p ~/zxtune-test && cd ~/zxtune-test
curl -sL -o z.tgz https://github.com/vitamin-caig/zxtune/releases/download/r5112/zxtune_r5112_linux_arm64.tar.gz
tar xzf z.tgz
Z=~/zxtune-test/usr/bin/zxtune123
$Z --list-plugins | grep ^Plugin: | wc -l                  # 382
time $Z --file "song.pt3" --wav filename=$HOME/out.wav --silent
$Z --file "song.sid" --index "filename=/tmp/i.csv,csv=[Subpath]|[Title]|[Author]|[Type]"
$Z --file "song.sid?#3" --null                              # prints Title, Author, Time, Loop duration
```
