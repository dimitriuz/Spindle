# File Browser Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A "Files" browser in the web UI over the music folder cliamp uses (`initial_directory`), with Play and Add to queue for folders (recursive) and files.

**Architecture:** cliamp has no operation that lists directories, so the bridge (same machine) lists them itself, confined to one root. A new `internal/files` package lists and walks that root; the enqueue worker gains `folder` and `file` kinds that feed local paths to cliamp (`queue {path}` reads the tags itself); the page gets a Files tile and a folder view.

**Tech Stack:** Go stdlib; vanilla JS/CSS.

**Spec:** conversation of 2026-11-22 ("expose cliamp's file browser to the web"; root = cliamp's `initial_directory` automatically).

## Global Constraints

- Root: `--music-dir` flag if given, else `initial_directory` from `<config-dir>/cliamp/config.toml` (top-level key, `~` expanded), else `~/Music`; must exist and be a directory, else the Files feature is off (routes 404, no tile).
- The page only ever sends a path relative to the root; it is cleaned, resolved with symlinks, and must stay inside the resolved root (a symlink leaving the root is invisible and refused). Absolute paths and `..` are rejected.
- Listing is read-only: directories and audio files only (`.flac .mp3 .m4a .aac .ogg .opus .wav .wma .aiff .aif .ape .wv .mka .alac`), no dotfiles; directories first, then files; natural, case-insensitive order (digit runs compare as numbers).
- Folder Play/Add is recursive in natural order (entries of one directory in natural order, directories descended into where they sort); symlinked directories are followed only while they stay inside the root; each real directory is visited once; depth limit 16.
- Local ops: Add to queue = `queue {path: abs}` then `queue.enqueue {index}` (the new row's live index = total before + position), so tracks land in the up-next list with cliamp's own tags, in order. Play = `queue.clear`, `queue {path}` for the first file, `queue.play {index: 0}`, wait until playing, then `queue {path}` for the rest (live list in order). No cap; progress, Stop and one-at-a-time as for other enqueues.
- Responses never contain absolute paths or credentials.
- Existing gates stay green: `make test`, `go test -race ./...`.

## Review Focus

- `..`, absolute paths, encoded traversal, a symlink inside the root pointing out of it: refused, nothing listed or queued outside. (Task 1, 3)
- A folder with thousands of files: sequential, ordered, progress, Stop. (Task 2)
- Missing/unreadable root or a directory that vanishes mid-walk: clean error, the worker ends and is free again. (Task 1, 2)
- File names with unicode, spaces, `#`, `?`, `%` survive the URL round trip. (Task 3, 4)
- Natural order: `Track 2` before `Track 10`. (Task 1)

---

### Task 1: files package

**Files:**
- Create: `internal/files/files.go`, `internal/files/files_test.go`

**Interfaces:**
- Produces:
  ```go
  type Entry struct { Name string `json:"name"`; Dir bool `json:"dir"` }
  type Listing struct { Path string `json:"path"`; Parent string `json:"parent"`; Name string `json:"name"`; Entries []Entry `json:"entries"` }
  var ErrOutside, ErrNotFound error
  type Browser struct{ /* unexported */ }
  func New(root string) (*Browser, error)                          // resolves symlinks; error if not a directory
  func (b *Browser) RootName() string
  func (b *Browser) List(rel string) (Listing, error)              // rel "" = root
  func (b *Browser) Resolve(rel string) (abs string, dir bool, err error) // for files/folders; ErrOutside / ErrNotFound
  func (b *Browser) Walk(ctx context.Context, rel string) ([]string, error) // absolute audio paths, recursive, natural order
  func RootFromConfig(configDir, home string) string               // initial_directory (~ expanded) or home/Music
  func NaturalLess(a, b string) bool
  ```

- [ ] **Step 1: Write failing tests** (temp-dir fixtures): `TestListSortsDirsFirstThenNaturally` (`Track 2` < `Track 10`, case-insensitive, dirs before files); `TestListHidesDotfilesAndNonAudio`; `TestListParentAndName` (root parent `""`, nested parent correct); `TestRelativeEscapesAreRefused` (`..`, `a/../..`, `/etc`, `%2e%2e`-decoded `../`, backslashes, NUL); `TestSymlinkOutOfRootIsInvisibleAndRefused` (dir link and file link); `TestSymlinkInsideRootIsFollowed`; `TestResolveFileVsDir`; `TestWalkIsRecursiveAndOrdered` (album folders with `01..12`), `TestWalkVisitsEachRealDirOnce` (symlink loop), `TestWalkHonoursContext`, `TestWalkSkipsUnreadableDirs`; `TestNewRejectsFilesAndMissing`; `TestRootFromConfig` (quoted/unquoted, `~/`, comment lines, key only counted before any `[section]`, missing file -> `home/Music`); `TestNaturalLess` table.
- [ ] **Step 2: Run** `go test ./internal/files` — expect FAIL.
- [ ] **Step 3: Implement.** Path check: `filepath.Clean` of `filepath.Join(root, rel)` after rejecting `..` elements, absolute, NUL and backslash; then `EvalSymlinks` and prefix check against the resolved root plus separator.
- [ ] **Step 4: Run** `go test -race ./internal/files` — expect PASS.
- [ ] **Step 5: Commit** `feat(files): confined directory listing and walk`.

### Task 2: enqueue folder and file

**Files:**
- Modify: `internal/enqueue/enqueue.go`, `internal/enqueue/enqueue_test.go`

**Interfaces:**
- Consumes: Task 1 via
  ```go
  type Files interface { Resolve(rel string) (abs string, dir bool, err error); Walk(ctx context.Context, rel string) ([]string, error) }
  ```
- Produces: `func (s *Service) SetFiles(f Files)`; `Request.Kind` accepts `file` and `folder` with `Provider "files"`, `ID` = relative path; mapping errors (`files.ErrOutside` -> `ErrUntrusted`, `files.ErrNotFound` -> `ErrInvalid`).

- [ ] **Step 1: Write failing tests** (fake cliamp gains `queue`, `queue.enqueue`, `queue.play` and a row counter): `TestFileAddQueuesThenEnqueuesTheNewRow`; `TestFolderAddKeepsOrderAndEnqueuesEachRowByItsIndex` (existing queue of 5, folder of 40: `queue.enqueue` indices 5..44 in order); `TestFolderPlayClearsPlaysFirstThenQueuesTheRest` (order: clear, queue first, play index 0, wait for playing, queue rest; nothing queued before playing); `TestFilePlayIsClearQueuePlay`; `TestFolderTotalsAndProgress` (events with total = number of files); `TestEmptyFolderFinishesDoneZero`; `TestOutsidePathIsUntrustedWithoutCalls`; `TestMissingPathIsInvalid`; `TestWalkErrorEndsFailedAndFreesTheService`; `TestCancelStopsFolderAdd`; `TestNoFilesServiceRejectsFileKinds`.
- [ ] **Step 2: Run** — expect FAIL. **Step 3: Implement** in the existing worker: resolve first (synchronously in `Start`, so errors map to statuses), walk in the background, starting row index from one `state.get` `total` read before the loop. **Step 4: Run** `go test -race ./...` — expect PASS.
- [ ] **Step 5: Commit** `feat(enqueue): Play and Add to queue for files and folders`.

### Task 3: API and wiring

**Files:**
- Create: `internal/server/files.go`, `internal/server/files_test.go`
- Modify: `internal/server/server.go`, `cmd/cliamp-web/main.go` (+ `--music-dir`, `runConfig.MusicDir`), `cmd/cliamp-web/wiring_test.go`, README flag list.

**Interfaces:**
- Produces: `type FilesSource interface { RootName() string; List(rel string) (files.Listing, error) }`; `GET /api/files?path=<rel>` -> 200 `files.Listing`, 400 for refused paths, 404 for missing path or when the feature is off; `GET /api/files/root` -> `{name}` or 404.

- [ ] **Step 1: Write failing tests:** list shape; `path` with unicode, spaces, `#`, `%`; `..`/absolute/NUL -> 400 and the source never sees them as escapes; 404 mapping; nil source -> 404 on both routes; guard (foreign Host -> 400); no absolute path in any body (temp-dir prefix absent). Wiring test: bridge with `MusicDir` on a temp tree and the fake player: list, then enqueue a folder over HTTP and see `queue` / `queue.enqueue` calls and the `done N/N` event.
- [ ] **Step 2:** `go test ./...` — expect FAIL. **Step 3:** implement; `main.go` builds the browser from `--music-dir` or `RootFromConfig`, logs (without the path) whether Files is on, passes it to the enqueue service (`SetFiles`) and the server. **Step 4:** `go test -race ./...` — expect PASS.
- [ ] **Step 5: Commit** `feat: GET /api/files over cliamp's music folder`.

### Task 4: pure page functions

**Files:** Modify `webui/app.js`, `webui/app_test.js`.

**Interfaces (exported):**
```js
filesRel(parent, name)      // 'Classic' + 'A B' -> 'Classic/A B'; '' + 'x' -> 'x'
filesHash(rel)              // '#browse/files' for '' else '#browse/files/' + encodeURIComponent(rel)
filesParent(rel)            // 'a/b' -> 'a', 'a' -> '', '' -> null
filterEntries(entries, q)   // case-insensitive substring on name; '' returns all; trims q
```
`rowActions` gains kinds `file` and `folder`: `['play','add']`.

- [ ] **Step 1: Write failing node tests** (unicode, `#?%` in names, empty and nested, null parent for root, filter ignoring case and surrounding spaces, `rowActions` for both kinds, `parseHash`/`splitPath` round trip of `#browse/files/<encoded rel>`).
- [ ] **Step 2:** `node webui/app_test.js` — expect FAIL. **Step 3:** implement. **Step 4:** expect PASS. **Step 5: Commit** `feat(webui): file browser helpers`.

### Task 5: Files view

**Files:** Modify `webui/index.html` (nothing), `webui/app.css`, `webui/app.js`; `README.md`.

- [ ] **Step 1:** Browse home shows a "Files" tile when `GET /api/files/root` answers; route `#browse/files[/<rel>]` renders the folder: header (back arrow to the parent or Browse, folder name), buttons Play and Add to queue for the whole folder (`enq({kind:'folder', provider:'files', id: rel, mode})`), a filter input (`filterEntries`), rows with folder/note icons: folders navigate and have the "..." menu (Play, Add to queue), files tap = Play and have the same menu (`kind:'file'`). Empty and error states; the root's breadcrumb text is the folder path relative to the music root.
- [ ] **Step 2:** `make test`. **Step 3: Browser verification** against the Pi (402x874 and 1280x720): tile, navigate Classic -> Antonio Vivaldi -> album, filter, menus, Play a small folder (queue replaced, plays) and Add a folder (rows with tags in the up-next list), refused path in the URL shows an error, no console errors. Confirm the Pi's queue/state first; restore the throwaway album and pause afterwards.
- [ ] **Step 4:** README "Files" section; deploy (install the binary and `pkill -x cliamp-web`, never restart cliamp); `make test`, `go test -race ./...`; commit `feat(webui): file browser`.

---

## Self-review

- **Spec coverage:** root from cliamp config (T1, T3), listing/folders/files (T1, T3, T5), Play/Add recursive with progress (T2), confinement (T1, T3), UI tile, filter, menus (T4, T5).
- **Types:** `Listing`/`Entry`/`Browser` defined in T1; `Files` interface in T2 satisfied by `*files.Browser`; `FilesSource` in T3; JS helpers named identically in T4 and T5.
- **Proportion:** interfaces, constraints and assertions only.
