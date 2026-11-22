package snapshot

import (
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"

	"cliamp-web/internal/ipc"
)

// QueueCap bounds the cached live playlist; beyond it the UI gets the first
// QueueCap rows plus truncated=true (cliamp frames are capped at 1 MiB
// anyway, and no human scrolls past a few thousand).
const QueueCap = 5000

// Cache is the bridge's single source of browser-visible player state:
// the runtime snapshot, the live playlist, capabilities, and online status.
// The subscriber goroutine writes it via Apply/Absorb*; HTTP handlers read
// it. All fields are guarded by mu except the online flag.
type Cache struct {
	mu    sync.RWMutex
	state json.RawMessage
	queue Queue
	caps  json.RawMessage
	plRev int64
	rev   int64 // revision of the cached runtime snapshot; older ones are dropped

	online atomic.Bool
}

// Queue is the JSON shape served at /api/queue.
type Queue struct {
	Tracks           json.RawMessage `json:"tracks"`
	PlayNext         json.RawMessage `json:"play_next"`
	PlaylistRevision int64           `json:"playlist_revision"`
	Truncated        bool            `json:"truncated"`
	Count            int             `json:"count"`
}

func NewCache() *Cache {
	return &Cache{
		state: json.RawMessage(`{}`),
		queue: Queue{Tracks: json.RawMessage(`[]`), PlayNext: json.RawMessage(`[]`)},
	}
}

func (c *Cache) SetOnline(v bool) { c.online.Store(v) }
func (c *Cache) Online() bool     { return c.online.Load() }

// Apply folds one runtime event into the cache and returns the SSE event
// name the bridge should broadcast ("", for topics it does not know).
func (c *Cache) Apply(ev ipc.Event) string {
	switch ev.Topic {
	case "runtime.state":
		c.SetState(ev.Data)
		return "state"
	case "runtime.settings":
		c.SetState(ev.Data)
		return "settings"
	case "runtime.playback":
		c.mergePlayback(ev.Data)
		return "playback"
	case "runtime.playlist":
		var d struct {
			PlaylistRevision int64 `json:"playlist_revision"`
		}
		json.Unmarshal(ev.Data, &d)
		c.mu.Lock()
		if d.PlaylistRevision != 0 {
			c.plRev = d.PlaylistRevision
		}
		c.mu.Unlock()
		return "playlist"
	case "runtime.job":
		return "job"
	}
	return ""
}

// SetState replaces the runtime snapshot and seeds playlist_revision.
func (c *Cache) SetState(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var s struct {
		Revision         int64 `json:"revision"`
		PlaylistRevision int64 `json:"playlist_revision"`
	}
	_ = json.Unmarshal(raw, &s)
	c.mu.Lock()
	defer c.mu.Unlock()
	// state.get replies race the event stream; never let an older snapshot
	// replace a newer one.
	if s.Revision != 0 && s.Revision < c.rev {
		return
	}
	if s.Revision != 0 {
		c.rev = s.Revision
	}
	c.state = raw
	if s.PlaylistRevision != 0 {
		c.plRev = s.PlaylistRevision
	}
}

// ResetRevision forgets the snapshot revision so a restarted player (whose
// counter starts over) is not mistaken for stale.
func (c *Cache) ResetRevision() {
	c.mu.Lock()
	c.rev = 0
	c.mu.Unlock()
}

// mergePlayback patches playback-tick fields into the cached snapshot so
// /api/state never serves a stale position.
func (c *Cache) mergePlayback(raw json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var patch map[string]json.RawMessage
	if json.Unmarshal(raw, &patch) != nil || len(patch) == 0 {
		return
	}
	var tick struct {
		Revision int64 `json:"revision"`
	}
	_ = json.Unmarshal(raw, &tick)
	if tick.Revision != 0 && tick.Revision < c.rev {
		return // older than what we hold: never let it roll the state back
	}
	var cur map[string]json.RawMessage
	if json.Unmarshal(c.state, &cur) != nil {
		cur = map[string]json.RawMessage{}
	}
	for k, v := range patch {
		cur[k] = v
	}
	if out, err := json.Marshal(cur); err == nil {
		c.state = out
		// a tick that carries a revision is as new as a full snapshot
		if tick.Revision > c.rev {
			c.rev = tick.Revision
		}
	}
}

func (c *Cache) State() json.RawMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Cache) PlaylistRevision() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.plRev
}

// absorbArray finds the track array in a queue.list/playnext.list result
// ({"tracks":[..]}, {"items":[..]}, or a bare array) and applies the cap.
// ok=false means the reply is unrecognizable; callers then keep the previous
// rows rather than pretending the playlist emptied. A successful result with
// the array field omitted (cliamp v2 sends {"ok":true,"index":-1} when empty)
// is an emptied list, not garbage.
func absorbArray(res json.RawMessage) (array json.RawMessage, count int, truncated bool, ok bool) {
	var raw json.RawMessage
	var obj map[string]json.RawMessage
	total := -1
	if json.Unmarshal(res, &obj) == nil {
		if t, ok := obj["total"]; ok {
			if n, err := strconv.Atoi(string(t)); err == nil {
				total = n
			}
		}
		raw = obj["tracks"]
		if len(raw) == 0 {
			raw = obj["items"]
		}
		if len(raw) == 0 && string(obj["ok"]) == "true" {
			return json.RawMessage(`[]`), 0, false, true
		}
	} else {
		raw = res // bare array
	}
	if len(raw) == 0 {
		return json.RawMessage(`[]`), 0, false, false
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		return json.RawMessage(`[]`), 0, false, false
	}
	if len(rows) > QueueCap {
		rows = rows[:QueueCap]
		truncated = true
	}
	if total > len(rows) {
		truncated = true // the player holds more rows than we were given
	}
	out, err := json.Marshal(rows)
	if err != nil {
		return json.RawMessage(`[]`), 0, false, false
	}
	return out, len(rows), truncated, true
}

// AbsorbQueueResult replaces the live-playlist rows from a queue.list result.
func (c *Cache) AbsorbQueueResult(res json.RawMessage, revision int64) {
	array, count, trunc, ok := absorbArray(res)
	if !ok {
		return
	}
	c.mu.Lock()
	c.queue.Tracks = array
	c.queue.Count = count
	c.queue.Truncated = trunc
	c.queue.PlaylistRevision = revision
	if revision != 0 {
		c.plRev = revision
	}
	c.mu.Unlock()
}

// AbsorbPlayNextResult replaces the play-next rows from a playnext.list result.
func (c *Cache) AbsorbPlayNextResult(res json.RawMessage, revision int64) {
	array, _, _, ok := absorbArray(res)
	if !ok {
		return
	}
	c.mu.Lock()
	c.queue.PlayNext = array
	if c.queue.PlaylistRevision == 0 {
		c.queue.PlaylistRevision = revision
	}
	c.mu.Unlock()
}

func (c *Cache) Queue() Queue {
	c.mu.RLock()
	defer c.mu.RUnlock()
	q := c.queue
	// The revision the rows were listed at, not the newest one heard of:
	// edits guarded with it are refused (409) if the playlist moved on,
	// instead of silently hitting the wrong row. Before any list, fall back.
	if q.PlaylistRevision == 0 {
		q.PlaylistRevision = c.plRev
	}
	return q
}

func (c *Cache) SetCaps(raw json.RawMessage) {
	c.mu.Lock()
	c.caps = raw
	c.mu.Unlock()
}

func (c *Cache) Caps() json.RawMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps
}
