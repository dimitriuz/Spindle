package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"spindle/internal/fakeplayer"
)

// TestWebSocketFollowsPlayer pins the device channel end to end: a client of
// /api/ws gets status and queue frames from the real wiring, and a change in
// the player's queue reaches it as a fresh queue frame.
func TestWebSocketFollowsPlayer(t *testing.T) {
	dir := t.TempDir()
	f, err := fakeplayer.Start(dir, fakeplayer.Options{
		State: `{"state":"playing","revision":5,"playlist_revision":7,"volume":-2,"repeat":"All","track":{"title":"Fake Song","artist":"Fake Band","album":"Fake LP","path":"/fake/a1-0.flac"},"duration":181}`,
		Caps:  `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	go func() { _ = run(ctx, runConfig{Socket: f.Path, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()
	readStatus(t, dir)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// wait until the bridge holds the player's queue
	deadline := time.Now().Add(5 * time.Second)
	for len(getJSON(t, base+"/api/queue")["tracks"].([]any)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("initial queue never arrived")
		}
		time.Sleep(50 * time.Millisecond)
	}

	c, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/api/ws", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read := func() string {
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		defer rcancel()
		_, b, err := c.Read(rctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("frame: %s", b)
		return string(b)
	}
	if s := read(); !contains(s, `"type":"status"`, `"title":"Fake Song"`, `"art":"/fake/a1-0.flac"`, `"repeat":"All"`) {
		t.Fatalf("first frame: %s", s)
	}
	if s := read(); !contains(s, `"type":"queue"`, `"count":1`, `"title":"Row A"`) {
		t.Fatalf("second frame: %s", s)
	}

	if r := postCommand(t, base, `{"op":"queue.clear"}`); r["job"] == nil {
		t.Fatalf("queue.clear rejected: %v", r)
	}
	for end := time.Now().Add(5 * time.Second); ; {
		if time.Now().After(end) {
			t.Fatal("no queue frame after queue.clear")
		}
		if s := read(); contains(s, `"type":"queue"`) && !contains(s, `"Row A"`) {
			break
		}
	}
}

func contains(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
