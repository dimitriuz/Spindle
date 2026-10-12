package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func okReply(id string, extra string) string {
	if extra == "" {
		return `{"version":2,"id":"` + id + `","ok":true}`
	}
	return `{"version":2,"id":"` + id + `","ok":true,` + extra + `}`
}

func TestCallCorrelatesByID(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{okReply(req["id"].(string), `"snapshot":{"state":"playing"}`)}
	})
	c := NewConn(f.path)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c.Call(ctx, "state.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || string(resp.Snapshot) != `{"state":"playing"}` {
		t.Fatalf("bad resp: %+v", resp)
	}
}

func TestCallReturnsIPCError(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":false,"error":{"code":"conflict","detail":"stale"}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	_, err := c.Call(context.Background(), "state.get", nil)
	var ie *IPCError
	if !errors.As(err, &ie) || ie.Code != "conflict" {
		t.Fatalf("want conflict IPCError, got %v", err)
	}
}

func TestGarbageLinesSkipped(t *testing.T) { // Review Focus #1
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{"not json", `{"id":"no-version"}`, okReply(req["id"].(string), "")}
	})
	c := NewConn(f.path)
	defer c.Close()
	resp, err := c.Call(context.Background(), "state.get", nil)
	if err != nil || !resp.OK {
		t.Fatalf("call should survive garbage: %v", err)
	}
}

func TestReconnectsAndStillServes(t *testing.T) {
	var path string
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{okReply(req["id"].(string), "")}
	})
	path = f.path
	c := NewConn(path)
	defer c.Close()
	if _, err := c.Call(context.Background(), "state.get", nil); err != nil {
		t.Fatal(err)
	}
	f.ln.Close() // kill listener: conn must back off, and Calls during downtime fail fast
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "state.get", nil); err == nil {
		t.Fatal("want error while socket is down")
	}
}

// serveReplyAt answers every request on path with a snapshot naming gen, so a
// test can tell which socket generation replied.
func serveReplyAt(t *testing.T, path, gen string) net.Listener {
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req struct {
						ID string `json:"id"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == "" {
						continue
					}
					fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"snapshot\":{\"gen\":%q}}\n", req.ID, gen)
				}
			}()
		}
	}()
	return ln
}

// TestServesFromReplacedSocket covers the cliamp-restart case: the path is
// recreated with a new inode, and the bridge must not keep talking to the old
// generation.
func TestServesFromReplacedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s")
	ln := serveReplyAt(t, path, "old")
	c := NewConn(path)
	defer c.Close()
	resp, err := c.Call(context.Background(), "state.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Snapshot) != `{"gen":"old"}` {
		t.Fatalf("bad first resp: %s", resp.Snapshot)
	}
	ln.Close() // cliamp exited: the socket path is unlinked...
	ln2 := serveReplyAt(t, path, "new")
	defer ln2.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err = c.Call(ctx, "state.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Snapshot) != `{"gen":"new"}` {
		t.Fatalf("stale generation still serving: %s", resp.Snapshot)
	}
}

// TestConcurrentCallsCorrelate pins the multiplexing invariant: many requests
// may be in flight on one socket, each answer delivered to its own caller.
func TestConcurrentCallsCorrelate(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{okReply(req["id"].(string), `"snapshot":{"id":"`+req["id"].(string)+`"}`)}
	})
	c := NewConn(f.path)
	defer c.Close()
	const n = 8
	var wg sync.WaitGroup
	ids := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Call(context.Background(), "state.get", nil)
			if err != nil {
				t.Error(err)
				return
			}
			if string(resp.Snapshot) != `{"id":"`+resp.ID+`"}` {
				t.Errorf("reply %s carried mismatched snapshot %s", resp.ID, resp.Snapshot)
			}
			ids[i] = resp.ID
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("duplicate or empty id in %v", ids)
		}
		seen[id] = true
	}
	if f.count() != n {
		t.Fatalf("fake saw %d requests, want %d", f.count(), n)
	}
}

func TestBackoffCeiling(t *testing.T) {
	for _, step := range []struct{ in, want time.Duration }{
		{minBackoff, time.Second},
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{4 * time.Second, maxBackoff}, // the old code doubled this to 8s
		{maxBackoff, maxBackoff},
	} {
		if got := nextBackoff(step.in, maxBackoff); got != step.want {
			t.Fatalf("a %v step produced %v, want %v", step.in, got, step.want)
		}
	}
	d := minBackoff
	for range 12 {
		d = nextBackoff(d, maxBackoff)
	}
	if d != maxBackoff {
		t.Fatalf("ladder settles at %v, want %v", d, maxBackoff)
	}
}

// TestCallerCutsReconnectBackoff: the reconnect loop is asleep inside a 2 s
// ladder step when the daemon comes back; an arriving caller must pull the next
// dial forward instead of timing out. The step dwarfs the caller's budget, so a
// client without the kick fails this test.
func TestCallerCutsReconnectBackoff(t *testing.T) {
	const step = 2 * time.Second
	budget := 700 * time.Millisecond
	if budget*2 >= step {
		t.Fatalf("budget %v must stay well under one ladder step %v", budget, step)
	}
	path := filepath.Join(t.TempDir(), "s")
	c := newConnLadder(path, step, step) // every step is a ceiling step
	defer c.Close()
	// Fail once with no socket: that is what puts the loop to sleep for a full
	// step, which is the window the next caller has to cut into.
	probe, cancelProbe := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelProbe()
	if _, err := c.Call(probe, "state.get", nil); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want ErrNotConnected while no socket exists, got %v", err)
	}
	time.Sleep(150 * time.Millisecond) // loop is inside its step by now
	ln := serveReplyAt(t, path, "back")
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if _, err := c.Call(ctx, "state.get", nil); err != nil {
		t.Fatalf("caller should have pulled the redial out of the %v step: %v", step, err)
	}
}

func TestOversizedRequestRejected(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{okReply(req["id"].(string), "")}
	})
	c := NewConn(f.path)
	defer c.Close()
	_, err := c.Call(context.Background(), "prompt.send", map[string]any{"text": strings.Repeat("y", maxLine)})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
	if f.count() != 0 {
		t.Fatalf("oversized request reached the peer %d times", f.count())
	}
}

// TestOversizedReplyIsDiagnosed: a peer frame over the cap must surface as a
// retired generation plus a log line, not a silent reconnect cycle.
func TestOversizedReplyIsDiagnosed(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		if p, ok := req["params"].(map[string]any); ok && p["big"] == true {
			return []string{okReply(req["id"].(string), `"snapshot":{"pad":"`+strings.Repeat("z", 2*maxLine)+`"}`)}
		}
		return []string{okReply(req["id"].(string), "")}
	})
	c := NewConn(f.path)
	defer c.Close()
	first, cancelFirst := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelFirst()
	if _, err := c.Call(first, "state.get", map[string]any{"big": true}); err == nil {
		t.Fatal("oversized reply should fail the call")
	}
	second, cancelSecond := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSecond()
	resp, err := c.Call(second, "state.get", nil)
	if err != nil || !resp.OK {
		t.Fatalf("connection should recover after the violation: %v", err)
	}
	if f.count() < 2 {
		t.Fatalf("client never redialled after the violation (saw %d requests)", f.count())
	}
}

// TestWriteLockIsCancelable: a caller must never be trapped behind a write the
// peer is not draining, and giving up must leave the token intact.
func TestWriteLockIsCancelable(t *testing.T) {
	st := &connState{wmu: make(chan struct{}, 1)}
	st.wmu <- struct{}{} // another frame owns the socket right now
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := st.lockWrite(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued caller got %v; it must give up with its own ctx error", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("queued caller waited %v before giving up", d)
	}
	select {
	case <-st.wmu: // the owner's token is still there
	default:
		t.Fatal("the abandoned caller somehow took the write token")
	}
	if err := st.lockWrite(context.Background()); err != nil {
		t.Fatalf("write lock unusable after a cancelled caller: %v", err)
	}
	st.writeUnlock()
}

// TestStalledPeerCallIsBounded: against a peer that accepts and never reads, a
// large frame must come back bounded by the caller's ctx instead of pinning the
// Call for as long as the kernel buffer keeps absorbing it.
func TestStalledPeerCallIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	unhold := make(chan struct{})
	defer close(unhold)
	go func() {
		uc, err := ln.Accept()
		if err != nil {
			return
		}
		defer uc.Close()
		<-unhold // accept, then never read
	}()
	c := NewConn(path)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Call(ctx, "prompt.send", map[string]any{"text": strings.Repeat("y", 900*1024)}); err == nil {
		t.Fatal("want failure against a stalled peer")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("write to a stalled peer was unbounded: %v", d)
	}
}
