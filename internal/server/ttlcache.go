package server

import (
	"encoding/json"
	"sync"
	"time"
)

// ttlcache is the library listing cache: TTL-bounded (no invalidation events
// exist in the player), FIFO-capped so a paging crawl cannot grow it without
// bound. Keys are full request paths; values are raw upstream result JSON.
type ttlcache struct {
	ttl    time.Duration
	max    int
	now    func() time.Time
	mu     sync.Mutex
	values map[string]cacheEntry
	order  []string
}

type cacheEntry struct {
	body    json.RawMessage
	expires time.Time
}

func newTTLCache(ttl time.Duration, maxEntries int, now func() time.Time) *ttlcache {
	return &ttlcache{ttl: ttl, max: maxEntries, now: now, values: map[string]cacheEntry{}}
}

func (c *ttlcache) get(key string) (json.RawMessage, bool) {
	if c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.values[key]
	if !ok || c.now().After(e.expires) {
		if ok {
			c.remove(key)
		}
		return nil, false
	}
	return e.body, true
}

func (c *ttlcache) put(key string, body json.RawMessage) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; !ok {
		for len(c.values)+1 > c.max {
			oldest := c.order[0]
			c.remove(oldest)
		}
		c.order = append(c.order, key)
	}
	c.values[key] = cacheEntry{body: body, expires: c.now().Add(c.ttl)}
}

func (c *ttlcache) remove(key string) {
	delete(c.values, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
