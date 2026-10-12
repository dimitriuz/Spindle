package server

import (
	"sync"
	"time"
)

// Limiter is a per-key token bucket. Allow returns false once the bucket is
// empty; tokens refill at rate per second up to burst.
type Limiter struct {
	mu    sync.Mutex
	rate  float64
	burst int
	now   func() time.Time
	m     map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter(rate float64, burst int, now func() time.Time) *Limiter {
	return &Limiter{rate: rate, burst: burst, now: now, m: map[string]*bucket{}}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.m[key]
	if !ok {
		b = &bucket{tokens: float64(l.burst), last: now}
		l.m[key] = b
	}
	b.tokens += l.rate * now.Sub(b.last).Seconds()
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
