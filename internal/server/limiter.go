package server

import (
	"net"
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

const (
	sweepAbove = 256
	idleTTL    = time.Minute
)

// clientKey is the limiter key for a request: the peer IP, not ip:port, or
// every new TCP connection would arrive with a fresh, full bucket.
func clientKey(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return remoteAddr
}

func NewLimiter(rate float64, burst int, now func() time.Time) *Limiter {
	return &Limiter{rate: rate, burst: burst, now: now, m: map[string]*bucket{}}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) > sweepAbove {
		// drop buckets that have been idle long enough to be full again
		for k, v := range l.m {
			if now.Sub(v.last) > idleTTL {
				delete(l.m, k)
			}
		}
	}
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
