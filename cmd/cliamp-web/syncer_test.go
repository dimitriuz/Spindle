package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncerCoalescesAndNeverOverlaps(t *testing.T) {
	var running, maxRunning, runs atomic.Int32
	release := make(chan struct{})
	s := newSyncer(func() {
		n := running.Add(1)
		if n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		runs.Add(1)
		<-release
		running.Add(-1)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Loop(ctx)

	done := make(chan int, 10)
	s.Request(func() { done <- 1 })
	time.Sleep(50 * time.Millisecond) // first run is now blocked inside run()
	for i := 0; i < 5; i++ {          // five more arrive mid-run
		s.Request(func() { done <- 2 })
	}
	release <- struct{}{} // finish run 1
	if v := <-done; v != 1 {
		t.Fatalf("first callback: %d", v)
	}
	release <- struct{}{} // the single coalesced follow-up
	for i := 0; i < 5; i++ {
		if v := <-done; v != 2 {
			t.Fatalf("follow-up callback: %d", v)
		}
	}
	if maxRunning.Load() != 1 {
		t.Fatalf("refreshes overlapped: %d", maxRunning.Load())
	}
	if runs.Load() != 2 {
		t.Fatalf("5 mid-run requests must coalesce into 1 run, ran %d total", runs.Load())
	}
}
