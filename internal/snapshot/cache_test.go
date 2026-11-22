package snapshot

import (
	"encoding/json"
	"fmt"
	"testing"

	"cliamp-web/internal/ipc"
)

func ev(topic, data string) ipc.Event {
	return ipc.Event{Topic: topic, Data: json.RawMessage(data)}
}

func TestApplyStateUpdatesSnapshot(t *testing.T) {
	c := NewCache()
	kind := c.Apply(ev("runtime.state", `{"revision":18,"state":"playing"}`))
	if kind != "state" {
		t.Fatalf("kind=%q", kind)
	}
	if got := string(c.State()); got != `{"revision":18,"state":"playing"}` {
		t.Fatalf("state=%s", got)
	}
	if c.PlaylistRevision() != 0 {
		t.Fatalf("playlist revision must stay 0 unless reported: %d", c.PlaylistRevision())
	}
}

func TestApplyStateSeedsPlaylistRevision(t *testing.T) {
	c := NewCache()
	c.Apply(ev("runtime.state", `{"revision":18,"playlist_revision":6}`))
	if c.PlaylistRevision() != 6 {
		t.Fatalf("rev=%d, want 6 from snapshot seed", c.PlaylistRevision())
	}
}

func TestApplyPlaybackMergesWithoutClobbering(t *testing.T) {
	c := NewCache()
	c.Apply(ev("runtime.state", `{"revision":18,"state":"playing","track":{"title":"A"}}`))
	kind := c.Apply(ev("runtime.playback", `{"state":"paused"}`))
	if kind != "playback" {
		t.Fatalf("kind=%q", kind)
	}
	var st map[string]json.RawMessage
	if err := json.Unmarshal(c.State(), &st); err != nil {
		t.Fatal(err)
	}
	if string(st["state"]) != `"paused"` {
		t.Fatalf("state not patched: %s", c.State())
	}
	if _, ok := st["track"]; !ok {
		t.Fatalf("playback merge clobbered track: %s", c.State())
	}
}

func TestApplyPlaylistSignalsAndTracksRevision(t *testing.T) {
	c := NewCache()
	kind := c.Apply(ev("runtime.playlist", `{"playlist_revision":7}`))
	if kind != "playlist" {
		t.Fatal("want playlist")
	}
	if c.PlaylistRevision() != 7 {
		t.Fatalf("rev=%d", c.PlaylistRevision())
	}
}

func TestApplyJobAndUnknown(t *testing.T) {
	c := NewCache()
	if k := c.Apply(ev("runtime.job", `{"id":"j1"}`)); k != "job" {
		t.Fatalf("job kind=%q", k)
	}
	if k := c.Apply(ev("runtime.nonsense", `{}`)); k != "" {
		t.Fatalf("unknown kind=%q", k)
	}
}

func TestAbsorbQueueResultShapesAndTruncate(t *testing.T) {
	c := NewCache()
	c.AbsorbQueueResult(json.RawMessage(`{"tracks":[{"path":"a"},{"path":"b"}]}`), 7)
	c.AbsorbPlayNextResult(json.RawMessage(`[{"path":"n1"}]`), 7)
	q := c.Queue()
	if q.Count != 2 || q.PlaylistRevision != 7 || q.Truncated {
		t.Fatalf("queue=%+v", q)
	}
	if string(q.PlayNext) != `[{"path":"n1"}]` {
		t.Fatalf("playnext=%s", q.PlayNext)
	}

	var big = make([]map[string]string, 5001)
	for i := range big {
		big[i] = map[string]string{"path": fmt.Sprintf("p%d", i)}
	}
	raw, _ := json.Marshal(map[string]any{"items": big})
	c.AbsorbQueueResult(raw, 8)
	q = c.Queue()
	var back []json.RawMessage
	if err := json.Unmarshal(q.Tracks, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 5000 || !q.Truncated {
		t.Fatalf("truncation failed: len=%d trunc=%v", len(back), q.Truncated)
	}

	c.AbsorbQueueResult(json.RawMessage(`not json`), 9)
	if q = c.Queue(); q.Count != 5000 || q.PlaylistRevision != 8 {
		t.Fatalf("garbage result must not corrupt queue: %+v", q)
	}
}

// cliamp v2 omits the array field from successful-but-empty list results;
// absorbing one must clear the cached rows, not keep stale ones.
func TestAbsorbEmptyOkResultClears(t *testing.T) {
	c := NewCache()
	c.AbsorbQueueResult(json.RawMessage(`{"tracks":[{"path":"a"}]}`), 3)
	c.AbsorbPlayNextResult(json.RawMessage(`{"items":[{"path":"n1"}]}`), 3)
	c.AbsorbQueueResult(json.RawMessage(`{"ok":true,"index":-1}`), 4)
	c.AbsorbPlayNextResult(json.RawMessage(`{"ok":true}`), 4)
	q := c.Queue()
	if q.Count != 0 || string(q.Tracks) != `[]` || string(q.PlayNext) != `[]` {
		t.Fatalf("empty ok result must clear rows: %+v", q)
	}
}

func TestCapsRoundTrip(t *testing.T) {
	c := NewCache()
	if len(c.Caps()) != 0 {
		t.Fatal("caps start empty")
	}
	c.SetCaps(json.RawMessage(`{"operations":["play","toggle"]}`))
	if got := string(c.Caps()); got != `{"operations":["play","toggle"]}` {
		t.Fatalf("caps=%s", got)
	}
}

func TestOnlineFlagStillWorks(t *testing.T) {
	c := NewCache()
	c.SetOnline(true)
	if !c.Online() {
		t.Fatal("online flag lost")
	}
}

// A slow state.get reply (taken before playback started) must not clobber a
// newer runtime.state event: that left the UI showing "stopped" while the
// player was playing, so the first play press paused it.
func TestSetStateIgnoresOlderRevision(t *testing.T) {
	c := NewCache()
	c.Apply(ev("runtime.state", `{"revision":32,"state":"playing"}`))
	c.SetState(json.RawMessage(`{"revision":31,"state":"stopped"}`))
	if got := string(c.State()); got != `{"revision":32,"state":"playing"}` {
		t.Fatalf("stale snapshot overwrote newer state: %s", got)
	}
	c.SetState(json.RawMessage(`{"revision":33,"state":"paused"}`))
	if got := string(c.State()); got != `{"revision":33,"state":"paused"}` {
		t.Fatalf("newer snapshot must win: %s", got)
	}
}

// After the player restarts its revisions begin again; ResetRevision lets the
// resync snapshot through.
func TestResetRevisionAcceptsRestartedPlayer(t *testing.T) {
	c := NewCache()
	c.SetState(json.RawMessage(`{"revision":900,"state":"playing"}`))
	c.ResetRevision()
	c.SetState(json.RawMessage(`{"revision":2,"state":"stopped"}`))
	if got := string(c.State()); got != `{"revision":2,"state":"stopped"}` {
		t.Fatalf("state after reset: %s", got)
	}
}

func TestStalePlaybackTickCannotRollBackState(t *testing.T) {
	c := NewCache()
	c.SetState(json.RawMessage(`{"revision":10,"state":"playing","position":5}`))
	c.Apply(ev("runtime.playback", `{"revision":9,"state":"stopped","position":0}`))
	var got struct {
		State    string
		Position float64
	}
	_ = json.Unmarshal(c.State(), &got)
	if got.State != "playing" || got.Position != 5 {
		t.Fatalf("stale tick merged: %s", c.State())
	}
	c.Apply(ev("runtime.playback", `{"revision":11,"position":6}`))
	_ = json.Unmarshal(c.State(), &got)
	if got.Position != 6 {
		t.Fatalf("newer tick must merge: %s", c.State())
	}
	// and it advanced the guard: a state.get older than the tick is dropped
	c.SetState(json.RawMessage(`{"revision":10,"state":"stopped"}`))
	_ = json.Unmarshal(c.State(), &got)
	if got.State != "playing" {
		t.Fatalf("snapshot older than the last tick overwrote it: %s", c.State())
	}
}

func TestQueueServesTheRevisionItWasListedAt(t *testing.T) {
	c := NewCache()
	c.AbsorbQueueResult(json.RawMessage(`{"ok":true,"tracks":[{"path":"a"}]}`), 5)
	c.Apply(ev("runtime.playlist", `{"playlist_revision":6}`)) // moved on; rows not refreshed yet
	if got := c.Queue().PlaylistRevision; got != 5 {
		t.Fatalf("rows listed at 5 must be guarded with 5 so a stale edit is refused, got %d", got)
	}
}

func TestQueueTruncatedWhenPlayerHoldsMore(t *testing.T) {
	c := NewCache()
	c.AbsorbQueueResult(json.RawMessage(`{"ok":true,"total":900,"tracks":[{"path":"a"}]}`), 1)
	if q := c.Queue(); !q.Truncated || q.Count != 1 {
		t.Fatalf("total 900 with 1 row must be flagged truncated: %+v", q)
	}
}
