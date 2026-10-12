package server

import (
	"encoding/json"
	"sync"
)

// Frame is one named SSE event with a pre-encoded JSON payload.
type Frame struct{ Name, Data string }

// Hub fans one broadcast out to every connected SSE client. A client whose
// buffer is full drops frames instead of stalling the bridge; browsers
// recover through the resync protocol, cliamp itself is never blocked.
type Hub struct {
	mu   sync.Mutex
	next uint64
	c    map[uint64]chan Frame
}

func NewHub() *Hub { return &Hub{c: map[uint64]chan Frame{}} }

// Subscribe returns the client id and a channel closed by Unsubscribe.
func (h *Hub) Subscribe() (uint64, <-chan Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	ch := make(chan Frame, 64)
	h.c[h.next] = ch
	return h.next, ch
}

func (h *Hub) Unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.c[id]; ok {
		close(ch)
		delete(h.c, id)
	}
}

func (h *Hub) Broadcast(name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.c {
		select {
		case ch <- Frame{Name: name, Data: string(b)}:
		default: // slow client: drop this frame; resync repairs it
		}
	}
}
