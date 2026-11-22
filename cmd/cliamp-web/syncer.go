package main

import (
	"context"
	"sync"
)

// syncer runs the state/queue refresh on one goroutine. Playlist events used
// to spawn a refresh each, so two overlapping refreshes could land out of
// order and an older queue replaced a newer one. Requests that arrive while a
// refresh runs coalesce into a single follow-up run; every callback runs
// after a refresh that STARTED after it was requested, which is what "the
// cache already holds the new rows" needs.
type syncer struct {
	run     func()
	mu      sync.Mutex
	waiters []func()
	wake    chan struct{}
}

func newSyncer(run func()) *syncer {
	return &syncer{run: run, wake: make(chan struct{}, 1)}
}

// Request schedules a refresh and calls after (may be nil) once it is done.
func (s *syncer) Request(after func()) {
	s.mu.Lock()
	if after != nil {
		s.waiters = append(s.waiters, after)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default: // a run is already pending and will pick this request up
	}
}

func (s *syncer) Loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		s.mu.Lock()
		w := s.waiters
		s.waiters = nil
		s.mu.Unlock()
		s.run()
		for _, f := range w {
			f()
		}
	}
}
