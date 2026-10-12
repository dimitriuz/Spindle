package ipc

import (
	"context"
	"time"
)

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
// caller can still tell the browser which state the job was in.
func (c *Conn) SubmitJob(ctx context.Context, op string, params any) (*Job, error) {
	req := map[string]any{"operation": op}
	if params != nil {
		req["params"] = params
	}
	resp, err := c.Call(ctx, "operation.submit", req)
	if err != nil {
		return nil, err
	}
	job := resp.Job
	for job != nil && !terminal[job.State] {
		t := time.NewTimer(jobPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return job, ctx.Err()
		case <-t.C:
		}
		jr, err := c.Call(ctx, "job.get", map[string]any{"job_id": job.ID})
		if err != nil {
			return job, err
		}
		if jr.Job != nil {
			job = jr.Job
		}
	}
	if job != nil && job.State == "failed" && job.Error != nil {
		return job, job.Error
	}
	return job, nil
}
