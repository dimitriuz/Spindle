package server

import (
	"encoding/json"
	"sync"
)

// Frame is one named SSE event with a pre-encoded JSON payload.
type Frame struct{ Name, Data string }

// Hub fans one broadcast out to every connected SSE client. A client whose
// buffer is full drops frames instead of stalling the bridge; browsers
// recover through the resync frame the stream sends once they catch up, cliamp itself is never blocked.
type Hub struct {
	mu   sync.Mutex
	next uint64
	c    map[uint64]*client
}

type client struct {
	ch      chan Frame
	dropped bool // a frame was lost; the stream must tell the browser to resync
}

func NewHub() *Hub { return &Hub{c: map[uint64]*client{}} }

// Subscribe returns the client id and a channel closed by Unsubscribe.
func (h *Hub) Subscribe() (uint64, <-chan Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	cl := &client{ch: make(chan Frame, 64)}
	h.c[h.next] = cl
	return h.next, cl.ch
}

func (h *Hub) Unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cl, ok := h.c[id]; ok {
		close(cl.ch)
		delete(h.c, id)
	}
}

// TakeDropped reports (once) that frames were lost for this client since the
// last call. The stream answers with a resync frame so the browser refetches
// state and queue instead of staying stale until the next event.
func (h *Hub) TakeDropped(id uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	cl, ok := h.c[id]
	if !ok || !cl.dropped {
		return false
	}
	cl.dropped = false
	return true
}

func (h *Hub) Broadcast(name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	b = urls.Scrub(b)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, cl := range h.c {
		select {
		case cl.ch <- Frame{Name: name, Data: string(b)}:
		default: // slow client: drop this frame and flag it for a resync
			cl.dropped = true
		}
	}
}
