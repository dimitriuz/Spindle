package ipc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestSubmitJobPollsUntilTerminal: a submit that comes back non-final has to be
// polled with job.get until the job is terminal, and the terminal job's
// snapshot is what the HTTP layer hands back to the browser.
func TestSubmitJobPollsUntilTerminal(t *testing.T) {
	var jobID string
	f := newFakeIPC(t, func(req map[string]any) []string {
		switch req["method"] {
		case "operation.submit":
			jobID = "j1"
			return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"job":{"id":"j1","operation":"play","state":"running"}}`}
		case "job.get":
			return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"job":{"id":"j1","operation":"play","state":"succeeded","snapshot":{"revision":19}}}`}
		}
		return nil
	})
	c := NewConn(f.path)
	defer c.Close()
	job, err := c.SubmitJob(context.Background(), "play", nil)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "succeeded" || string(job.Snapshot) != `{"revision":19}` {
		t.Fatalf("bad job: %+v", job)
	}
	if f.count() < 2 {
		t.Fatal("expected submit + at least one job.get")
	}

	// The request bodies are the daemon's contract: "operation" and "job_id"
	// ride the envelope itself (cliamp v2.0.1 rejects either nested in params).
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.received[0]["operation"] != "play" {
		t.Errorf("operation.submit envelope = %v, want operation=play", f.received[0])
	}
	if f.received[1]["job_id"] != jobID {
		t.Errorf("job.get envelope = %v, want job_id=%s", f.received[1], jobID)
	}
}

// TestSubmitJobTerminalFailure: a job that fails carries its IPCError inline,
// and the caller must see it as an *IPCError so /api/command can map conflict
// to 409.
func TestSubmitJobTerminalFailure(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"job":{"id":"j1","operation":"queue.move","state":"failed","error":{"code":"conflict","detail":"shuffle on"}}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	_, err := c.SubmitJob(context.Background(), "queue.move", nil)
	var ie *IPCError
	if !errors.As(err, &ie) || ie.Code != "conflict" {
		t.Fatalf("want conflict, got %v", err)
	}
}

// TestSubmitJobStopsAtCanceled pins the third terminal state: polling ends at
// "canceled" and a canceled job is not an error.
func TestSubmitJobStopsAtCanceled(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		state := "queued"
		if req["method"] == "job.get" {
			state = "canceled"
		}
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"job":{"id":"j9","operation":"play","state":"` + state + `"}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	job, err := c.SubmitJob(context.Background(), "play", map[string]any{"clip_id": "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "canceled" {
		t.Fatalf("job = %+v, want state canceled", job)
	}
	if n := f.count(); n != 2 {
		t.Fatalf("%d requests, want exactly submit + one job.get: a terminal state must end polling", n)
	}
}

// TestSubmitJobFinishesFastJobsQuickly: most operations are done within a few
// milliseconds, so the first polls must not wait a fixed 200 ms. (That wait
// made appending 12,000 tracks one by one take forty minutes.)
func TestSubmitJobFinishesFastJobsQuickly(t *testing.T) {
	polls := 0
	f := newFakeIPC(t, func(req map[string]any) []string {
		id := req["id"].(string)
		if req["method"] == "job.get" {
			polls++
			state := "running"
			if polls >= 3 {
				state = "succeeded"
			}
			return []string{`{"version":2,"id":"` + id + `","ok":true,"job":{"id":"j1","operation":"queue","state":"` + state + `"}}`}
		}
		return []string{`{"version":2,"id":"` + id + `","ok":true,"job":{"id":"j1","operation":"queue","state":"queued"}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	start := time.Now()
	job, err := c.SubmitJob(context.Background(), "queue", nil)
	if err != nil || job.State != "succeeded" {
		t.Fatalf("%v %+v", err, job)
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("a job that finishes on the third poll took %v; early polls must back off from milliseconds, not 200 ms", took)
	}
}

// TestSubmitJobSettlesAtTwoHundredMs: a job that keeps running is polled at
// most every 200 ms once it has had time to finish. Slower and the browser's
// 10 s command cap starts timing jobs out; faster and the daemon is hammered.
func TestSubmitJobSettlesAtTwoHundredMs(t *testing.T) {
	var mu sync.Mutex
	var at []time.Time
	f := newFakeIPC(t, func(req map[string]any) []string {
		if req["method"] == "job.get" {
			mu.Lock()
			at = append(at, time.Now())
			mu.Unlock()
		}
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"job":{"id":"j1","operation":"play","state":"running"}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
	defer cancel()
	job, err := c.SubmitJob(ctx, "play", nil)
	if !errors.Is(err, context.DeadlineExceeded) || job == nil || job.State != "running" {
		t.Fatalf("want the deadline and the last known job, got %v %+v", err, job)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) < 6 || len(at) > 14 {
		t.Fatalf("%d polls in 1.8 s: expected a quick start then one per 200 ms (6..14)", len(at))
	}
	for i := len(at) - 3; i < len(at)-1; i++ { // the settled gaps
		if gap := at[i+1].Sub(at[i]); gap < 150*time.Millisecond || gap > 260*time.Millisecond {
			t.Fatalf("settled poll gap %v, want about 200 ms", gap)
		}
	}
}

// TestSubmitJobRejectsSubmitWithoutJob: an ok:true submit reply with no job
// object leaves the bridge with nothing to poll and nothing to report, so it is
// a protocol violation rather than a finished command.
func TestSubmitJobRejectsSubmitWithoutJob(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"snapshot":{"revision":3}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	job, err := c.SubmitJob(context.Background(), "play", nil)
	if !errors.Is(err, ErrNoJob) {
		t.Fatalf("want ErrNoJob, got %v", err)
	}
	if job != nil {
		t.Errorf("no job may be reported, got %+v", job)
	}
	if n := f.count(); n != 1 {
		t.Errorf("%d requests, want 1: a submit with no job must not start polling", n)
	}
}

// TestSubmitJobStopsWhenPollLosesTheJob: cliamp can forget a job (restart, ring
// pruning) while we poll it. The last known state comes back with the error,
// and the loop must not keep asking for a job that is not there.
func TestSubmitJobStopsWhenPollLosesTheJob(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		if req["method"] == "operation.submit" {
			return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"job":{"id":"j1","operation":"play","state":"running"}}`}
		}
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	// Bounded on purpose: a loop that does not stop when job.get answers with
	// no job would poll forever, so the deadline turns that regression into a
	// clean failure rather than a hung suite.
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	job, err := c.SubmitJob(ctx, "play", nil)
	if !errors.Is(err, ErrNoJob) {
		t.Fatalf("want ErrNoJob, got %v", err)
	}
	if job == nil || job.State != "running" {
		t.Fatalf("the last known job must come back with the error, got %+v", job)
	}
	if n := f.count(); n != 2 {
		t.Errorf("%d requests, want exactly submit + one job.get", n)
	}
}
