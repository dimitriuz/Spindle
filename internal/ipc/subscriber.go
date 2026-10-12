package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"time"
)

// Subscriber owns the second IPC connection, the one that carries pushed V2
// events. V2 allows one subscription per connection and says to use a separate
// connection for normal commands, so this type never sends a command: it
// subscribes, reads events until the stream ends, then re-subscribes on a fresh
// connection.
type Subscriber struct {
	socket   string
	topics   []string
	log      *slog.Logger
	onEvent  func(Event)
	onOnline func(bool)
	minBack  time.Duration
	maxBack  time.Duration
}

// NewSubscriber builds the event connection. onOnline reports subscription
// transitions so the bridge can tell its SSE clients their view went stale;
// either callback may be nil.
func NewSubscriber(socket string, topics []string, onEvent func(Event), onOnline func(bool)) *Subscriber {
	return newSubscriberLadder(socket, topics, onEvent, onOnline, minBackoff, maxBackoff)
}

// newSubscriberLadder lets tests watch a whole backoff ladder inside a second.
func newSubscriberLadder(socket string, topics []string, onEvent func(Event), onOnline func(bool), minBack, maxBack time.Duration) *Subscriber {
	return &Subscriber{
		socket:   socket,
		topics:   topics,
		log:      slog.Default(),
		onEvent:  onEvent,
		onOnline: onOnline,
		minBack:  minBack,
		maxBack:  maxBack,
	}
}

// Run keeps the subscription alive until ctx is done. It blocks, so callers put
// it on its own goroutine. Liveness comes from cliamp closing the socket or ctx
// being cancelled; nothing here times out on silence.
func (s *Subscriber) Run(ctx context.Context) {
	backoff := s.minBack
	for ctx.Err() == nil {
		nc, err := net.Dial("unix", s.socket)
		if err != nil {
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, s.maxBack)
			continue
		}
		// The read loop has no deadline on purpose, so cancelling ctx has to
		// hang the socket up for it: otherwise Run would sit in Scan until
		// cliamp noticed us.
		stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
		online := s.read(ctx, nc)
		stop()
		_ = nc.Close()
		if online {
			// An acknowledged subscribe proves the path is good, so the ladder
			// starts over: a stream that broke gets replaced after one short
			// step, not after whatever rung the ladder had grown to.
			backoff = s.minBack
			if ctx.Err() == nil && s.onOnline != nil {
				s.onOnline(false)
			}
		} else {
			backoff = nextBackoff(backoff, s.maxBack)
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

// read subscribes on nc and delivers events until the stream ends. There is
// deliberately no read deadline: an idle event stream is the normal state of a
// paused player, and treating silence as a disconnect would drop the
// subscription every time nothing happens.
//
// The result reports whether the subscription was ever established, which is
// what decides whether the caller owes listeners an offline callback.
func (s *Subscriber) read(ctx context.Context, nc net.Conn) bool {
	sub, err := json.Marshal(map[string]any{
		"version": 2, "id": newID(), "method": "subscribe", "topics": s.topics,
	})
	if err != nil {
		return false
	}
	if _, err := nc.Write(append(sub, '\n')); err != nil {
		return false
	}
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	if !sc.Scan() {
		return false
	}
	var ack struct {
		OK  bool      `json:"ok"`
		Err *IPCError `json:"error,omitempty"`
	}
	if json.Unmarshal(sc.Bytes(), &ack) != nil || !ack.OK {
		if ack.Err != nil {
			s.log.Warn("ipc subscribe rejected", "socket", s.socket, "err", ack.Err)
		}
		return false
	}
	if s.onOnline != nil {
		s.onOnline(true)
	}
	for ctx.Err() == nil {
		if !sc.Scan() {
			if errors.Is(sc.Err(), bufio.ErrTooLong) {
				// A frame over the cap is a protocol violation the peer will
				// repeat; say so instead of redialling without explanation.
				s.log.Warn("ipc peer frame over cap", "socket", s.socket, "cap", maxLine)
			}
			return true
		}
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev Event
		if json.Unmarshal(line, &ev) != nil || ev.Topic == "" {
			continue // garbage line or a reply that is not ours: skip, never crash
		}
		if ev.Topic == "system.overflow" {
			// cliamp dropped events out of its ring and closes this stream
			// itself. Leave now: the loop re-subscribes and the bridge rebuilds
			// the browser's view from state.get.
			return true
		}
		if s.onEvent != nil {
			s.onEvent(ev)
		}
	}
	return true
}

// sleepCtx waits d, or until ctx is done. A false result means ctx is done and
// the caller should stop.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
