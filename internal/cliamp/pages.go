// Package cliamp holds small helpers for talking to the player.
package cliamp

import (
	"context"
	"encoding/json"
	"fmt"

	"spindle/internal/ipc"
)

// PageSize is how many rows are asked for at a time.
const PageSize = 200

// Pages collects every row of a paged cliamp listing: op is the listing
// operation, field names the array in its reply, and params are sent with
// each page next to offset/limit. maxRows <= 0 means no limit.
func Pages(ctx context.Context, r ipc.OpRunner, op string, params map[string]any, field string, maxRows int) ([]json.RawMessage, error) {
	var rows []json.RawMessage
	for maxRows <= 0 || len(rows) < maxRows {
		p := map[string]any{"offset": len(rows), "limit": PageSize}
		for k, v := range params {
			p[k] = v
		}
		job, err := r.SubmitJob(ctx, op, p)
		if err != nil {
			return nil, err
		}
		var page map[string]json.RawMessage
		if json.Unmarshal(job.Result, &page) != nil {
			return nil, fmt.Errorf("cliamp: unreadable %s reply", op)
		}
		var got []json.RawMessage
		_ = json.Unmarshal(page[field], &got)
		rows = append(rows, got...)
		total := -1
		_ = json.Unmarshal(page["total"], &total)
		if len(got) == 0 || total < 0 || len(rows) >= total {
			break
		}
	}
	return rows, nil
}
