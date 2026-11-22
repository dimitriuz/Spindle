package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type wsRig struct {
	t    *testing.T
	cfg  Config
	srv  *httptest.Server
	conn *websocket.Conn
}

func newWSRig(t *testing.T, mut func(*Config)) *wsRig {
	t.Helper()
	cfg := testConfig(t)
	cfg.HeartbeatInterval = time.Hour
	cfg.StatusMinGap = 200 * time.Millisecond
	if mut != nil {
		mut(&cfg)
	}
	srv := httptest.NewServer(New(cfg))
	t.Cleanup(srv.Close)
	return &wsRig{t: t, cfg: cfg, srv: srv}
}

func (r *wsRig) dial(header map[string][]string) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.srv.URL, "http")+"/api/ws", &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		r.t.Cleanup(func() { c.CloseNow() })
	}
	return c, err
}

func (r *wsRig) connect() *websocket.Conn {
	r.t.Helper()
	c, err := r.dial(nil)
	if err != nil {
		r.t.Fatal(err)
	}
	r.conn = c
	return c
}

// next reads one frame, or reports that none came within d.
func (r *wsRig) next(d time.Duration) (map[string]any, bool) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, b, err := r.conn.Read(ctx)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		r.t.Fatalf("not JSON: %s", b)
	}
	return m, true
}

func (r *wsRig) mustNext(typ string) map[string]any {
	r.t.Helper()
	m, ok := r.next(2 * time.Second)
	if !ok || m["type"] != typ {
		r.t.Fatalf("want %s frame, got %v (ok=%v)", typ, m, ok)
	}
	return m
}

func (r *wsRig) waitClients(n int) {
	r.t.Helper()
	for i := 0; i < 200; i++ {
		if r.cfg.Hub.Count() == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("hub has %d clients, want %d", r.cfg.Hub.Count(), n)
}

func (r *wsRig) setState(raw string) {
	r.cfg.Cache.SetState(json.RawMessage(raw))
}

func TestWSInitialFrames(t *testing.T) {
	r := newWSRig(t, nil)
	r.cfg.Cache.SetOnline(true)
	r.setState(`{"revision":1,"state":"playing","track":{"title":"Song","path":"/a"}}`)
	r.connect()
	st := r.mustNext("status")
	if st["title"] != "Song" || st["online"] != true {
		t.Fatalf("%v", st)
	}
	r.mustNext("queue")
}

func TestWSOfflineStatus(t *testing.T) {
	r := newWSRig(t, nil)
	r.connect()
	st := r.mustNext("status")
	if st["online"] != false || st["title"] != "" || st["state"] != "stopped" {
		t.Fatalf("%v", st)
	}
}

func TestWSStatusOnState(t *testing.T) {
	r := newWSRig(t, nil)
	r.connect()
	r.mustNext("status")
	r.mustNext("queue")
	r.waitClients(1)
	r.setState(`{"revision":2,"state":"playing","track":{"title":"Next one","path":"/b"}}`)
	r.cfg.Hub.Broadcast("state", map[string]any{})
	if st := r.mustNext("status"); st["title"] != "Next one" {
		t.Fatalf("%v", st)
	}
}

func TestWSPlaybackThrottled(t *testing.T) {
	r := newWSRig(t, nil)
	r.connect()
	r.mustNext("status")
	r.mustNext("queue")
	r.waitClients(1)
	for i := 0; i < 20; i++ {
		r.setState(`{"revision":` + string(rune('3'+0)) + `,"state":"playing","position":` + strings.Repeat("1", 1+i%3) + `}`)
		r.cfg.Hub.Broadcast("playback", map[string]any{"position": i})
	}
	n := 0
	for {
		m, ok := r.next(700 * time.Millisecond)
		if !ok {
			break
		}
		if m["type"] == "status" {
			n++
		}
	}
	if n < 1 || n > 2 {
		t.Fatalf("20 playback ticks must collapse to 1-2 status frames, got %d", n)
	}
}

func TestWSQueueOnPlaylist(t *testing.T) {
	r := newWSRig(t, nil)
	r.connect()
	r.mustNext("status")
	r.mustNext("queue")
	r.waitClients(1)
	r.cfg.Cache.AbsorbQueueResult(json.RawMessage(`{"tracks":[{"title":"x","path":"/x"}]}`), 6)
	r.cfg.Hub.Broadcast("playlist", map[string]any{})
	q := r.mustNext("queue")
	if q["count"] != 1.0 || q["playlist_revision"] != 6.0 {
		t.Fatalf("%v", q)
	}
}

func TestWSResyncOnDrop(t *testing.T) {
	r := newWSRig(t, nil)
	r.connect()
	r.mustNext("status")
	r.mustNext("queue")
	r.waitClients(1)
	r.cfg.Hub.mu.Lock()
	for _, cl := range r.cfg.Hub.c {
		cl.dropped = true
	}
	r.cfg.Hub.mu.Unlock()
	r.cfg.Hub.Broadcast("job", map[string]any{}) // wakes the handler; job itself is not sent
	r.mustNext("resync")
}

func TestWSIgnoresJobFrames(t *testing.T) {
	r := newWSRig(t, nil)
	r.connect()
	r.mustNext("status")
	r.mustNext("queue")
	r.waitClients(1)
	for _, n := range []string{"job", "enqueue", "favorites"} {
		r.cfg.Hub.Broadcast(n, map[string]any{"x": 1})
	}
	if m, ok := r.next(300 * time.Millisecond); ok {
		t.Fatalf("unexpected frame %v", m)
	}
}

func TestWSPing(t *testing.T) {
	r := newWSRig(t, func(c *Config) { c.HeartbeatInterval = 50 * time.Millisecond })
	r.connect()
	r.mustNext("status")
	r.mustNext("queue")
	r.mustNext("ping")
}

func TestWSNoOriginAccepted(t *testing.T) {
	r := newWSRig(t, nil)
	if _, err := r.dial(nil); err != nil {
		t.Fatalf("a device sends no Origin: %v", err)
	}
}

func TestWSForeignOriginRejected(t *testing.T) {
	r := newWSRig(t, nil)
	if _, err := r.dial(map[string][]string{"Origin": {"http://evil.example"}}); err == nil {
		t.Fatal("a foreign Origin must be refused")
	}
}

func TestWSUnsubscribesOnClose(t *testing.T) {
	r := newWSRig(t, nil)
	c := r.connect()
	r.mustNext("status")
	r.waitClients(1)
	c.Close(websocket.StatusNormalClosure, "bye")
	r.waitClients(0)
}

func TestWSScrubsStreamURLs(t *testing.T) {
	r := newWSRig(t, nil)
	r.setState(`{"state":"playing","track":{"title":"Radio","path":"http://radio.example/stream?token=abc\u0026u=me"}}`)
	r.connect()
	st := r.mustNext("status")
	if a, _ := st["art"].(string); a == "" || strings.Contains(a, "token=abc") || strings.Contains(a, "u=me") {
		t.Fatalf("stream URL must be scrubbed: %q", a)
	}
}
