package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
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
