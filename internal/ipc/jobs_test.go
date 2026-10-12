package ipc

import (
	"context"
	"errors"
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

	// The request bodies are the daemon's contract: "operation" on the submit
	// envelope itself (cliamp v2.0.1 rejects a nested one), "job_id" in the
	// poll's params.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.received[0]["operation"] != "play" {
		t.Errorf("operation.submit envelope = %v, want operation=play", f.received[0])
	}
	poll, _ := f.received[1]["params"].(map[string]any)
	if poll["job_id"] != jobID {
		t.Errorf("job.get params = %v, want job_id=%s", poll, jobID)
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

// TestSubmitJobPollsEveryTwoHundredMs: the poll interval is pinned at 200 ms.
// Slower and the browser's 10 s command cap starts timing jobs out; faster and
// the daemon is hammered while a job runs.
func TestSubmitJobPollsEveryTwoHundredMs(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true,"job":{"id":"j1","operation":"play","state":"running"}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 550*time.Millisecond)
	defer cancel()
	job, err := c.SubmitJob(ctx, "play", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the ctx deadline back, got %v", err)
	}
	if job == nil || job.State != "running" {
		t.Fatalf("the last known job must come back with the error, got %+v", job)
	}
	gets := 0
	f.mu.Lock()
	for _, r := range f.received {
		if r["method"] == "job.get" {
			gets++
		}
	}
	f.mu.Unlock()
	if gets < 1 || gets > 3 {
		t.Fatalf("polled job.get %d times in 550ms, want 1..3 (one poll per 200ms)", gets)
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
