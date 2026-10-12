package server

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sseFrame struct{ name, data string }

type sseClient struct {
	resp *http.Response
	sc   *bufio.Scanner
}

func sseConnect(t *testing.T, baseURL, tok string) *sseClient {
	t.Helper()
	req, err := http.NewRequest("GET", baseURL+"/api/events?token="+tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("events connect: %d", resp.StatusCode)
	}
	return &sseClient{resp: resp, sc: bufio.NewScanner(resp.Body)}
}

// close must run before the httptest server closes: SSE handlers pin the
// server until their client disconnects.
func (c *sseClient) close() { _ = c.resp.Body.Close() }

// next returns the next event frame, skipping comment heartbeats and blank
// separator lines; fails the test after d.
func (c *sseClient) next(t *testing.T, d time.Duration) sseFrame {
	t.Helper()
	type res struct {
		f   sseFrame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var name string
		for c.sc.Scan() {
			line := c.sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ch <- res{f: sseFrame{name: name, data: strings.TrimPrefix(line, "data: ")}}
				return
			}
		}
		ch <- res{err: c.sc.Err()}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("sse read: %v", r.err)
		}
		return r.f
	case <-time.After(d):
		t.Fatal("no SSE frame within deadline")
		return sseFrame{}
	}
}

func TestSSEFanOutToTwoClients(t *testing.T) {
	cfg := authedConfig(t)
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c1 := sseConnect(t, srv.URL, "deadbeef")
	c2 := sseConnect(t, srv.URL, "deadbeef")
	defer c1.close()
	defer c2.close()
	// consume the per-connect replay frames first
	c1.next(t, 2*time.Second)
	c1.next(t, 2*time.Second)
	c2.next(t, 2*time.Second)
	c2.next(t, 2*time.Second)

	cfg.Hub.Broadcast("state", map[string]any{"state": "playing"})
	for i, c := range []*sseClient{c1, c2} {
		f := c.next(t, 2*time.Second)
		if f.name != "state" || !strings.Contains(f.data, `"playing"`) {
			t.Fatalf("client %d got %q %q", i, f.name, f.data)
		}
	}
}

func TestSSEReplayCurrentPlayerAndStateOnConnect(t *testing.T) { // Review Focus #3
	cfg := authedConfig(t)
	cfg.Cache.SetOnline(true)
	cfg.Cache.SetState([]byte(`{"state":"playing","revision":3}`))
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := sseConnect(t, srv.URL, "deadbeef")
	defer c.close()
	f1 := c.next(t, 2*time.Second)
	f2 := c.next(t, 2*time.Second)
	if f1.name != "player" || !strings.Contains(f1.data, "online") {
		t.Fatalf("first replay: %+v", f1)
	}
	if f2.name != "state" || !strings.Contains(f2.data, `"revision":3`) {
		t.Fatalf("second replay: %+v", f2)
	}
}

func TestSSENamedPing(t *testing.T) {
	cfg := authedConfig(t)
	cfg.HeartbeatInterval = 50 * time.Millisecond
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := sseConnect(t, srv.URL, "deadbeef")
	defer c.close()
	// connect replays player+state first; a ping must follow within a few
	// 50 ms heartbeats:
	for i := 0; i < 4; i++ {
		if f := c.next(t, 2*time.Second); f.name == "ping" {
			return
		}
	}
	t.Fatal("no named ping frame within window")
}

func TestSSEUnsubscribeOnClientDisconnect(t *testing.T) {
	cfg := authedConfig(t)
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := sseConnect(t, srv.URL, "deadbeef")
	defer c.close()
	c.next(t, 2*time.Second)
	c.close() // client gone

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hubClients(cfg.Hub) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("handler did not unsubscribe after client disconnect: %d clients", hubClients(cfg.Hub))
}

func TestSSESlowClientDroppedNotStalled(t *testing.T) {
	cfg := authedConfig(t)
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := sseConnect(t, srv.URL, "deadbeef")
	defer c.close()
	c.next(t, 2*time.Second) // replay player
	c.next(t, 2*time.Second) // replay state
	// never read again; flood past the buffer
	for i := range 200 {
		cfg.Hub.Broadcast("playback", map[string]any{"position": i})
	}
	// a healthy second client must still get fresh frames
	c2 := sseConnect(t, srv.URL, "deadbeef")
	defer c2.close()
	c2.next(t, 2*time.Second)
	c2.next(t, 2*time.Second)
	cfg.Hub.Broadcast("resync", map[string]any{})
	f := c2.next(t, 2*time.Second)
	if f.name != "resync" {
		t.Fatalf("second client starved by slow client: %+v", f)
	}
}

func hubClients(h *Hub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.c)
}
