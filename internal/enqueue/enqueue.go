// Package enqueue does the multi-step queue work behind the item menu's
// "Play" and "Add to queue": clearing the queue and playing, or adding a
// whole album, playlist or artist track by track, in the background and with
// progress.
//
// "Add to queue" goes into cliamp's up-next list (track.queue), in order. The
// obvious alternative, appending to the end of the live playlist with
// queue{path}, adds a bare URL: no title, artist, duration or provider
// metadata, so the entry shows as the server's hostname everywhere and loses
// its art, favorite and tech info. track.queue keeps the whole track object.
package enqueue

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"cliamp-web/internal/cliamp"
	"cliamp-web/internal/files"
	"cliamp-web/internal/ipc"
)

var (
	ErrBusy      = errors.New("enqueue: another request is running")
	ErrInvalid   = errors.New("enqueue: invalid request")
	ErrUntrusted = errors.New("enqueue: unknown track path")
)

// Request is one menu action. Kind: track|album|playlist|artist, or file|folder
// for the music folder (Provider "files", ID the path relative to its root).
// Mode: play (clear the queue, add, start playing) or append (add to the end).
type Request struct {
	Kind, Provider, ID, Mode string
	Track                    map[string]any
	abs                      string // file/folder: the resolved absolute path
}

// Files is the music folder the file kinds read from (*files.Browser).
type Files interface {
	Resolve(rel string) (abs string, dir bool, err error)
	Walk(ctx context.Context, rel string) ([]string, error)
}

// Event reports progress. State: running|done|failed|cancelled.
type Event struct {
	State string `json:"state"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Error string `json:"error,omitempty"`
}

const eventEvery = 200 * time.Millisecond

type Service struct {
	r       ipc.OpRunner
	restore func(string) string
	known   func(string) bool
	files   Files
	emit    func(Event)
	now     func() time.Time

	// how often, and for how long, to wait for a started track to be playing
	pollEvery, playTimeout time.Duration

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	lastEv  time.Time
}

func New(r ipc.OpRunner, restore func(string) string, known func(string) bool, emit func(Event), now func() time.Time) *Service {
	if restore == nil {
		restore = func(s string) string { return s }
	}
	if emit == nil {
		emit = func(Event) {}
	}
	if now == nil {
		now = time.Now
	}
	return &Service{r: r, restore: restore, known: known, emit: emit, now: now, pollEvery: 50 * time.Millisecond, playTimeout: 15 * time.Second}
}

// SetFiles enables the file and folder kinds.
func (s *Service) SetFiles(f Files) { s.files = f }

// Start validates the request and runs it in the background.
func (s *Service) Start(req Request) error {
	switch req.Mode {
	case "play", "append":
	default:
		return ErrInvalid
	}
	switch req.Kind {
	case "track":
		path, _ := req.Track["path"].(string)
		if req.Track == nil || path == "" {
			return ErrInvalid
		}
		// Files on this machine only if the bridge was told about them (art and
		// info use the same rule). Stream URLs are the player's to fetch, as
		// they always were through /api/command, and radio or podcast URLs
		// carry no login that could have been scrubbed.
		orig := s.restore(path)
		if orig == path && !isStreamURL(path) && (s.known == nil || !s.known(path)) {
			return ErrUntrusted
		}
		tr := make(map[string]any, len(req.Track))
		for k, v := range req.Track {
			tr[k] = v
		}
		tr["path"] = orig
		req.Track = tr
	case "album", "playlist", "artist":
		if req.Provider == "" || req.ID == "" {
			return ErrInvalid
		}
	case "file", "folder":
		if s.files == nil {
			return ErrInvalid
		}
		abs, dir, err := s.files.Resolve(req.ID)
		switch {
		case errors.Is(err, files.ErrOutside):
			return ErrUntrusted
		case err != nil:
			return ErrInvalid
		case dir != (req.Kind == "folder"):
			return ErrInvalid // a folder is not a file and the other way round
		}
		req.abs = abs
	default:
		return ErrInvalid
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running, s.cancel = true, cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go s.work(ctx, req)
	return nil
}

// Cancel stops the running request after the item in flight.
func (s *Service) Cancel() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// Wait blocks until the running request (if any) has ended.
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) finish() {
	s.mu.Lock()
	s.running = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
	s.wg.Done()
}

func isStreamURL(p string) bool {
	return strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://")
}

var urlRe = regexp.MustCompile(`https?://\S+`)

// safeError is what may be shown to the browser: cliamp's own message with
// any URL removed (stream URLs carry the account login).
func safeError(err error) string {
	var ie *ipc.IPCError
	switch {
	case errors.As(err, &ie) && ie.Detail != "":
		return urlRe.ReplaceAllString(ie.Detail, "<url>")
	case errors.Is(err, ipc.ErrNotConnected):
		return "player offline"
	}
	return "request failed"
}

// progress sends an event at most every 200 ms; force always sends.
func (s *Service) progress(state string, done, total int, force bool) {
	now := s.now()
	if !force && now.Sub(s.lastEv) < eventEvery {
		return
	}
	s.lastEv = now
	s.emit(Event{State: state, Done: done, Total: total})
}

func (s *Service) work(ctx context.Context, req Request) {
	defer s.finish()
	done, total := 0, 0
	fail := func(err error) {
		if ctx.Err() != nil {
			s.emit(Event{State: "cancelled", Done: done, Total: total})
			return
		}
		s.emit(Event{State: "failed", Done: done, Total: total, Error: safeError(err)})
	}
	op := func(name string, params map[string]any) error {
		_, err := s.r.SubmitJob(ctx, name, params)
		return err
	}

	switch {
	case req.Kind == "track" && req.Mode == "play":
		total = 1
		if err := op("queue.clear", map[string]any{}); err != nil {
			fail(err)
			return
		}
		if err := op("track.play", map[string]any{"track": req.Track}); err != nil {
			fail(err)
			return
		}
		s.emit(Event{State: "done", Done: 1, Total: 1})
		return
	case req.Kind == "track":
		total = 1
		if err := op("track.queue", map[string]any{"track": req.Track}); err != nil {
			fail(err)
			return
		}
		s.emit(Event{State: "done", Done: 1, Total: 1})
		return
	case req.Mode == "play" && req.Kind == "album":
		total = 1
		if err := op("provider.load_album", map[string]any{"provider": req.Provider, "album": req.ID}); err != nil {
			fail(err)
			return
		}
		s.emit(Event{State: "done", Done: 1, Total: 1})
		return
	case req.Mode == "play" && req.Kind == "playlist":
		total = 1
		if err := op("provider.load", map[string]any{"provider": req.Provider, "playlist": req.ID}); err != nil {
			fail(err)
			return
		}
		s.emit(Event{State: "done", Done: 1, Total: 1})
		return
	}

	if req.Kind == "file" || req.Kind == "folder" {
		s.workFiles(ctx, req, op)
		return
	}

	// everything else walks a track list: append for album/playlist/artist, and Play for an artist
	tracks, err := s.listTracks(ctx, req)
	if err != nil {
		fail(err)
		return
	}
	total = len(tracks)
	s.progress("running", 0, total, true)
	rest := tracks
	if req.Mode == "play" { // artist: clear, start on the first track, queue the others
		if err := op("queue.clear", map[string]any{}); err != nil {
			fail(err)
			return
		}
		if total > 0 {
			if err := op("track.play", map[string]any{"track": tracks[0]}); err != nil {
				fail(err)
				return
			}
			done = 1
			rest = tracks[1:]
			// cliamp needs a few seconds to start the stream. While it still reports
			// "stopped", each track.queue counts as an append that moves the current
			// track, so the page flickers through every track and playback ends up on
			// the last one. Wait for the first track to really play.
			s.waitPlaying(ctx)
		}
	}
	for _, t := range rest {
		if ctx.Err() != nil {
			fail(ctx.Err())
			return
		}
		if path, _ := t["path"].(string); path != "" {
			if err := op("track.queue", map[string]any{"track": t}); err != nil {
				fail(err)
				return
			}
		}
		done++
		s.progress("running", done, total, false)
	}
	s.emit(Event{State: "done", Done: done, Total: total})
}

func decode(rows []json.RawMessage) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var m map[string]any
		if json.Unmarshal(r, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) albumTracks(ctx context.Context, provider, album string) ([]map[string]any, error) {
	rows, err := cliamp.Pages(ctx, s.r, "provider.album_tracks", map[string]any{"provider": provider, "album": album}, "tracks", 0)
	return decode(rows), err
}

func (s *Service) listTracks(ctx context.Context, req Request) ([]map[string]any, error) {
	switch req.Kind {
	case "album":
		return s.albumTracks(ctx, req.Provider, req.ID)
	case "playlist":
		rows, err := cliamp.Pages(ctx, s.r, "provider.tracks", map[string]any{"provider": req.Provider, "playlist": req.ID}, "tracks", 0)
		return decode(rows), err
	}
	// artist: every album, in the order the server lists them
	albums, err := cliamp.Pages(ctx, s.r, "provider.artist_albums", map[string]any{"provider": req.Provider, "artist": req.ID}, "albums", 0)
	if err != nil {
		return nil, err
	}
	var all []map[string]any
	for _, a := range decode(albums) {
		id, _ := a["id"].(string)
		if id == "" {
			continue
		}
		ts, err := s.albumTracks(ctx, req.Provider, id)
		if err != nil {
			return nil, err
		}
		all = append(all, ts...)
	}
	return all, nil
}

// waitPlaying blocks until the player reports "playing", the wait times out
// (the rest is queued anyway) or ctx ends.
func (s *Service) waitPlaying(ctx context.Context) {
	for tries := int(s.playTimeout / s.pollEvery); tries > 0; tries-- {
		if resp, err := s.r.Call(ctx, "state.get", nil); err == nil {
			var sn struct {
				State string `json:"state"`
			}
			if json.Unmarshal(resp.Snapshot, &sn) == nil && sn.State == "playing" {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.pollEvery):
		}
	}
}

// workFiles adds local files. cliamp reads their tags itself when given a
// path (queue{path}), so unlike a Navidrome URL the entry is complete. To keep
// Add to queue in the same up-next list as every other source, each appended
// row is then moved there with queue.enqueue, by its live index.
func (s *Service) workFiles(ctx context.Context, req Request, op func(string, map[string]any) error) {
	var paths []string
	if req.Kind == "file" {
		paths = []string{req.abs}
	} else {
		var err error
		if paths, err = s.files.Walk(ctx, req.ID); err != nil {
			s.emit(Event{State: "failed", Error: safeError(err)})
			return
		}
	}
	total, done := len(paths), 0
	finish := func(err error) {
		if err == nil {
			s.emit(Event{State: "done", Done: done, Total: total})
			return
		}
		if ctx.Err() != nil {
			s.emit(Event{State: "cancelled", Done: done, Total: total})
			return
		}
		s.emit(Event{State: "failed", Done: done, Total: total, Error: safeError(err)})
	}
	if total == 0 {
		finish(nil) // nothing to add: leave the queue alone
		return
	}
	s.progress("running", 0, total, true)
	rest := paths
	base := 0
	if req.Mode == "play" {
		for _, step := range []struct {
			name   string
			params map[string]any
		}{{"queue.clear", map[string]any{}}, {"queue", map[string]any{"path": paths[0]}}, {"queue.play", map[string]any{"index": 0}}} {
			if err := op(step.name, step.params); err != nil {
				finish(err)
				return
			}
		}
		done, rest = 1, paths[1:]
		s.waitPlaying(ctx) // see the artist case: queuing while the stream starts moves the cursor
	} else if resp, err := s.r.Call(ctx, "state.get", nil); err == nil {
		var sn struct {
			Total int `json:"total"`
		}
		_ = json.Unmarshal(resp.Snapshot, &sn)
		base = sn.Total // the queue's length: the first row we add gets this index
	}
	for k, p := range rest {
		if ctx.Err() != nil {
			finish(ctx.Err())
			return
		}
		if err := op("queue", map[string]any{"path": p}); err != nil {
			finish(err)
			return
		}
		if req.Mode == "append" {
			if err := op("queue.enqueue", map[string]any{"index": base + k}); err != nil {
				finish(err)
				return
			}
		}
		done++
		s.progress("running", done, total, false)
	}
	finish(nil)
}
