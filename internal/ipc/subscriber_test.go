package ipc

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Event-stream peer: answers one subscribe per connection, then runs a script
// that controls the whole lifetime of that stream.
// ---------------------------------------------------------------------------

type streamWriter struct {
	c   net.Conn
	enc *json.Encoder
}

func (w *streamWriter) Send(v any)   { _ = w.enc.Encode(v) }
func (w *streamWriter) Raw(s string) { _, _ = w.c.Write([]byte(s)) }
func (w *streamWriter) HangUp()      { _ = w.c.Close() }

// streamPeer answers the subscribe handshake on every connection it accepts.
// hold keeps the stream open after the script has run; without it the peer
// hangs up as soon as the script returns.
type streamPeer struct {
	ln     net.Listener
	mu     sync.Mutex
	conns  []net.Conn
	reqs   []map[string]any
	acks   []time.Time
	script func(w *streamWriter, round int)
	hold   bool
}

func newStreamPeer(t *testing.T, path string, script func(w *streamWriter, round int), hold bool) *streamPeer {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	p := &streamPeer{ln: ln, script: script, hold: hold}
	t.Cleanup(p.close)
	go p.serve()
	return p
}

func (p *streamPeer) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		p.conns = append(p.conns, c)
		p.mu.Unlock()
		go func() {
			var req map[string]any
			if json.NewDecoder(c).Decode(&req) != nil {
				return
			}
			p.mu.Lock()
			p.reqs = append(p.reqs, req)
			p.acks = append(p.acks, time.Now())
			round := len(p.reqs)
			p.mu.Unlock()
			w := &streamWriter{c: c, enc: json.NewEncoder(c)}
			w.Send(map[string]any{"version": 2, "id": req["id"], "ok": true})
			if p.script != nil {
				p.script(w, round)
			}
			if p.hold {
				select {} // the stream stays open until the test hangs it up
			}
			c.Close()
		}()
	}
}

func (p *streamPeer) close() {
	p.mu.Lock()
	p.ln.Close()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// hangup stands in for cliamp going away: every live stream closes.
func (p *streamPeer) hangup() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (p *streamPeer) subscribes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

func (p *streamPeer) firstSubscribe() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		return nil
	}
	return p.reqs[0]
}

// ---------------------------------------------------------------------------
// Test plumbing
// ---------------------------------------------------------------------------

type recorder struct {
	mu     sync.Mutex
	events []Event
	online []bool
}

func (r *recorder) event(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) state(on bool) {
	r.mu.Lock()
	r.online = append(r.online, on)
	r.mu.Unlock()
}

func (r *recorder) evs() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *recorder) states() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.online...)
}

func sockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "s") // short: unix socket paths cap at 107 bytes
}

// poll re-evaluates cond until it holds or d elapses.
func poll(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// ---------------------------------------------------------------------------
// Handshake and delivery
// ---------------------------------------------------------------------------

// TestSubscriberHandshakeAndEvents: the subscription rides its own connection,
// so events keep flowing while a command connection is busy. The topics list
// and the ok:true ack are what cliamp requires before it starts pushing.
func TestSubscriberHandshakeAndEvents(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		if req["method"] == "subscribe" {
			return []string{
				`{"version":2,"id":"` + req["id"].(string) + `","ok":true}`,
				`{"event":"runtime.state","seq":1,"retained":true,"data":{"state":"playing"}}`,
			}
		}
		return nil
	})
	rec := &recorder{}
	s := NewSubscriber(f.path, []string{"runtime.state"}, rec.event, rec.state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	if !poll(3*time.Second, func() bool { return len(rec.evs()) == 1 }) {
		t.Fatalf("bad events: %+v", rec.evs())
	}
	got := rec.evs()[0]
	if got.Topic != "runtime.state" || !got.Retained || got.Seq != 1 ||
		string(got.Data) != `{"state":"playing"}` {
		t.Fatalf("bad event: %+v", got)
	}
	online := rec.states()
	if len(online) == 0 || !online[0] {
		t.Fatalf("want online true first, got %v", online)
	}

	if !poll(time.Second, func() bool { return f.count() == 1 }) {
		t.Fatalf("want exactly one subscribe frame, got %d requests", f.count())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.received[0]
	if req["method"] != "subscribe" {
		t.Errorf("subscribe frame = %v, want method=subscribe", req)
	}
	if v, _ := req["version"].(float64); v != 2 {
		t.Errorf("subscribe frame version = %v, want 2", req["version"])
	}
	if req["id"] == "" || req["id"] == nil {
		t.Errorf("subscribe frame needs a request id for the ack, got %v", req["id"])
	}
	tps, _ := req["topics"].([]any)
	if len(tps) != 1 || tps[0] != "runtime.state" {
		t.Errorf("subscribe frame topics = %v, want [runtime.state]", req["topics"])
	}
}

// TestSubscriberOverflowTriggersResyncCycle: cliamp drops events when its ring
// is full. The bridge must not keep a subscription that has already lost data:
// it closes the stream and re-subscribes, and the UI is told the link broke so
// it can throw its stale view away.
func TestSubscriberOverflowTriggersResyncCycle(t *testing.T) {
	var mu sync.Mutex
	subs := 0
	f := newFakeIPC(t, func(req map[string]any) []string {
		if req["method"] != "subscribe" {
			return nil
		}
		mu.Lock()
		subs++
		first := subs == 1
		mu.Unlock()
		ack := `{"version":2,"id":"` + req["id"].(string) + `","ok":true}`
		if first {
			return []string{ack, `{"event":"system.overflow","seq":2,"data":{"resync_required":true}}`}
		}
		return []string{ack}
	})
	rec := &recorder{}
	s := NewSubscriber(f.path, []string{"runtime.state"}, rec.event, rec.state)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go s.Run(ctx)

	if !poll(3*time.Second, func() bool { return f.count() >= 2 }) {
		t.Fatalf("overflow must close+resubscribe, got %d subscribes", f.count())
	}
	if !poll(2*time.Second, func() bool { return len(rec.states()) >= 2 }) {
		t.Fatalf("the UI was never told the stream broke: %v", rec.states())
	}
	online := rec.states()
	if !online[0] || online[1] {
		t.Errorf("online sequence = %v, want true then false", online)
	}
	for _, e := range rec.evs() {
		if e.Topic == "system.overflow" {
			t.Errorf("overflow is a resync signal, not a UI event: %+v", e)
		}
	}
}

// TestSubscriberSkipsGarbageLines (Review Focus #1): a malformed line, a blank
// line, a frame with no topic and a stray reply must all be ignored without
// tearing down the subscription or reaching the UI as an empty card.
func TestSubscriberSkipsGarbageLines(t *testing.T) {
	path := sockPath(t)
	peer := newStreamPeer(t, path, func(w *streamWriter, round int) {
		w.Raw("\n")
		w.Raw("this is not json\n")
		w.Raw("{}\n")
		w.Raw(`{"event":"","seq":7,"data":{"x":1}}` + "\n")
		w.Raw(`{"version":2,"id":"stray","ok":true}` + "\n")
		w.Send(map[string]any{"event": "clip.created", "seq": 8,
			"data": map[string]any{"id": "a"}})
	}, true)
	rec := &recorder{}
	s := newSubscriberLadder(path, []string{"runtime.state"}, rec.event, rec.state,
		20*time.Millisecond, 40*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	if !poll(3*time.Second, func() bool { return len(rec.evs()) == 1 }) {
		t.Fatalf("bad events: %+v", rec.evs())
	}
	got := rec.evs()[0]
	if got.Topic != "clip.created" || got.Seq != 8 {
		t.Fatalf("delivered (%d,%s), want (8,clip.created)", got.Seq, got.Topic)
	}
	if n := peer.subscribes(); n != 1 {
		t.Fatalf("%d subscribes: garbage lines tore the stream down", n)
	}
}

// ---------------------------------------------------------------------------
// Liveness
// ---------------------------------------------------------------------------

// TestSubscriberStaysConnectedWhileIdle is the regression that stops a laptop
// tab on a lock screen from being reported as disconnected: an event stream
// that sends nothing is normal, so the subscription connection has no read
// deadline and silence must never cost us the subscription. Several ladder
// steps pass inside this window, so any redial-on-silence shows up here.
func TestSubscriberStaysConnectedWhileIdle(t *testing.T) {
	path := sockPath(t)
	peer := newStreamPeer(t, path, nil, true)
	rec := &recorder{}
	s := newSubscriberLadder(path, []string{"runtime.state"}, rec.event, rec.state,
		10*time.Millisecond, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	if !poll(3*time.Second, func() bool { return peer.subscribes() == 1 }) {
		t.Fatalf("want one subscribe, got %d", peer.subscribes())
	}
	time.Sleep(400 * time.Millisecond)
	if n := peer.subscribes(); n != 1 {
		t.Fatalf("%d subscribes: an idle event stream must not reconnect", n)
	}
	if online := rec.states(); len(online) != 1 || !online[0] {
		t.Errorf("online sequence = %v, want [true]: silence is not a disconnect", online)
	}
	if evs := rec.evs(); len(evs) != 0 {
		t.Errorf("invented events on an idle stream: %+v", evs)
	}
}

// TestSubscriberResubscribesAfterPeerHangup: with no read deadline, a peer that
// vanishes is detected by the socket closing. The loop must come back,
// re-subscribe, and tell the UI the link went down and up again.
func TestSubscriberResubscribesAfterPeerHangup(t *testing.T) {
	path := sockPath(t)
	peer := newStreamPeer(t, path, func(w *streamWriter, round int) {
		if round == 1 {
			w.HangUp() // cliamp goes away mid-stream
			return
		}
		w.Send(map[string]any{"event": "clip.created", "seq": 3,
			"data": map[string]any{"id": "a"}})
	}, true)
	rec := &recorder{}
	s := newSubscriberLadder(path, []string{"runtime.state"}, rec.event, rec.state,
		20*time.Millisecond, 40*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	if !poll(3*time.Second, func() bool { return len(rec.evs()) == 1 }) {
		t.Fatalf("no event after reconnect: %d subscribes, events %+v",
			peer.subscribes(), rec.evs())
	}
	if n := peer.subscribes(); n < 2 {
		t.Fatalf("%d subscribes: a hung-up stream must be replaced", n)
	}
	if !poll(2*time.Second, func() bool { return len(rec.states()) >= 3 }) {
		t.Fatalf("online sequence = %v, want down then up around the reconnect", rec.states())
	}
	online := rec.states()
	if !online[0] || online[1] || !online[2] {
		t.Errorf("online sequence = %v, want true,false,true", online)
	}
	if got := rec.evs()[0]; got.Topic != "clip.created" || got.Seq != 3 {
		t.Errorf("event after reconnect = %+v, want (3,clip.created)", got)
	}
}

// TestSubscriberRunStopsOnCancel: Run owns the socket, so cancelling ctx has to
// hang the stream up for it and return instead of sitting in a read that no
// deadline will ever end.
func TestSubscriberRunStopsOnCancel(t *testing.T) {
	path := sockPath(t)
	peer := newStreamPeer(t, path, nil, true)
	s := newSubscriberLadder(path, []string{"runtime.state"}, nil, nil,
		20*time.Millisecond, 40*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	if !poll(3*time.Second, func() bool { return peer.subscribes() == 1 }) {
		t.Fatalf("peer saw %d subscribes, want 1", peer.subscribes())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run is still blocked after ctx cancel: the stream must be hung up")
	}
	time.Sleep(100 * time.Millisecond) // two ladder steps
	if n := peer.subscribes(); n != 1 {
		t.Fatalf("%d subscribes: Run kept redialling after ctx cancel", n)
	}
}

// ---------------------------------------------------------------------------
// Backoff ladder
// ---------------------------------------------------------------------------

// TestSubscriberBackoffGrowsWhileDisconnected: while cliamp is down the loop
// steps back instead of hammering the socket path.
func TestSubscriberBackoffGrowsWhileDisconnected(t *testing.T) {
	path := sockPath(t)
	start := time.Now()
	s := newSubscriberLadder(path, []string{"runtime.state"}, nil, nil,
		100*time.Millisecond, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	time.Sleep(350 * time.Millisecond) // failed dials at ~0, ~100, ~300ms
	peer := newStreamPeer(t, path, nil, true)
	if !poll(2*time.Second, func() bool { return peer.subscribes() >= 1 }) {
		t.Fatal("the loop never connected once a socket existed")
	}
	// The ladder's next failed dial is at ~700ms. A loop that redialled every
	// 100ms instead of stepping back would be in here by ~450ms.
	if d := time.Since(start); d < 600*time.Millisecond {
		t.Fatalf("connected after %v: the redial ladder did not step back", d)
	}
}

// TestSubscriberBackoffResetsAfterSubscribe: an acknowledged subscribe proves
// the path is good, so a stream that breaks right after must be rebuilt after
// one minimum step, not after whatever rung the ladder had grown to.
func TestSubscriberBackoffResetsAfterSubscribe(t *testing.T) {
	path := sockPath(t)
	s := newSubscriberLadder(path, []string{"runtime.state"}, nil, nil,
		100*time.Millisecond, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	time.Sleep(350 * time.Millisecond) // grow the ladder with failed dials
	peer := newStreamPeer(t, path, func(w *streamWriter, round int) {
		w.HangUp() // cliamp closes the stream right after the ack
	}, false)
	if !poll(2*time.Second, func() bool { return peer.subscribes() >= 1 }) {
		t.Fatal("the loop never connected once a socket existed")
	}
	// Each cycle is one min step apart (~100ms) once the ack resets the ladder;
	// without the reset the next redial would wait out a multi-second rung.
	if !poll(600*time.Millisecond, func() bool { return peer.subscribes() >= 3 }) {
		t.Fatalf("%d subscribes in 600ms: a successful subscribe must reset the backoff",
			peer.subscribes())
	}
}
