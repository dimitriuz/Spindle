package server

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTTLCacheHitThenExpire(t *testing.T) {
	now := time.Now()
	c := newTTLCache(60*time.Second, 128, func() time.Time { return now })
	c.put("k", json.RawMessage(`{"n":1}`))
	if _, ok := c.get("k"); !ok {
		t.Fatal("immediate get must hit")
	}
	now = now.Add(59 * time.Second)
	if _, ok := c.get("k"); !ok {
		t.Fatal("within TTL must hit")
	}
	now = now.Add(2 * time.Second)
	if v, ok := c.get("k"); ok || v != nil {
		t.Fatalf("past TTL must miss clean, got %s %v", v, ok)
	}
}

func TestTTLCacheZeroTTLNeverCaches(t *testing.T) {
	c := newTTLCache(0, 128, time.Now)
	c.put("k", json.RawMessage(`{}`))
	if _, ok := c.get("k"); ok {
		t.Fatal("ttl 0 must never hit")
	}
}

func TestTTLCacheEvictsOldestAtCap(t *testing.T) {
	now := time.Now()
	c := newTTLCache(time.Minute, 2, func() time.Time { return now })
	c.put("a", json.RawMessage(`1`))
	c.put("b", json.RawMessage(`2`))
	c.put("c", json.RawMessage(`3`)) // evicts "a" (FIFO)
	if _, ok := c.get("a"); ok {
		t.Fatal("a should have been evicted")
	}
	if _, ok := c.get("c"); !ok {
		t.Fatal("c must be present")
	}
}
