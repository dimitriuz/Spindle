package server

import "testing"

// A client that cannot keep up loses frames; it must be told to resync
// instead of staying stale until the next event.
func TestHubFlagsClientsThatLostFrames(t *testing.T) {
	h := NewHub()
	id, ch := h.Subscribe()
	if h.TakeDropped(id) {
		t.Fatal("nothing dropped yet")
	}
	for i := 0; i < 64+5; i++ { // buffer is 64; the rest are dropped
		h.Broadcast("state", map[string]int{"i": i})
	}
	if !h.TakeDropped(id) {
		t.Fatal("client lost frames and must be flagged")
	}
	if h.TakeDropped(id) {
		t.Fatal("the flag is consumed once")
	}
	if len(ch) != 64 {
		t.Fatalf("buffered %d", len(ch))
	}
	other, _ := h.Subscribe()
	h.Broadcast("state", 1)
	if h.TakeDropped(other) {
		t.Fatal("a healthy client must not be flagged")
	}
}
