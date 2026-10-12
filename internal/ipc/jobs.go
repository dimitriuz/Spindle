package ipc

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNoJob means cliamp answered a job-carrying method with no job object.
// There is nothing to poll and nothing to hand the browser, so the V2 reply is
// treated as the protocol violation it is.
var ErrNoJob = errors.New("reply carried no job")

// jobPoll is how often a non-final job is re-read with job.get. 200 ms keeps a
// command well inside the browser's 10 s budget without leaning on the daemon
// while a job is running.
const jobPoll = 200 * time.Millisecond

// terminal lists the job states after which cliamp will never change its
// answer, so polling has to stop.
var terminal = map[string]bool{"succeeded": true, "failed": true, "canceled": true}

// SubmitJob submits an operation and waits for the job to finish, re-reading it
// with job.get while cliamp says it is still running. A failed job comes back
// with its inline IPCError so the HTTP layer can map conflict to 409. The last
// known job is returned alongside the error when the wait is cut short, so the
// caller can still tell the browser which state the job was in. A reply that
// carries no job comes back as ErrNoJob.
func (c *Conn) SubmitJob(ctx context.Context, op string, params any) (*Job, error) {
	// cliamp's V2 envelope carries "operation" top-level; a nested one is
	// rejected with invalid_request (verified against cliamp v2.0.1).
	req := map[string]any{"version": 2, "method": "operation.submit", "operation": op}
	if params != nil {
		req["params"] = params
	}
	resp, err := c.roundtrip(ctx, req)
	if err != nil {
		return nil, err
	}
	job := resp.Job
	if job == nil {
		return nil, fmt.Errorf("operation.submit %s: %w", op, ErrNoJob)
	}
	for !terminal[job.State] {
		t := time.NewTimer(jobPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return job, ctx.Err()
		case <-t.C:
		}
		// like operation, job_id rides the envelope (cliamp v2.0.1 rejects
		// it inside params with invalid_params).
		jr, err := c.roundtrip(ctx, map[string]any{
			"version": 2, "method": "job.get", "job_id": job.ID,
		})
		if err != nil {
			return job, err
		}
		if jr.Job == nil {
			return job, fmt.Errorf("job.get %s: %w", job.ID, ErrNoJob)
		}
		job = jr.Job
	}
	if job.State == "failed" && job.Error != nil {
		return job, job.Error
	}
	return job, nil
}
