package snapshot

import "sync/atomic"

// Cache holds the latest player view. Task 4 grows it into the full
// state/queue/caps cache + event reducer; only the online flag exists now.
type Cache struct {
	online atomic.Bool
}

func NewCache() *Cache            { return &Cache{} }
func (c *Cache) SetOnline(v bool) { c.online.Store(v) }
func (c *Cache) Online() bool     { return c.online.Load() }
