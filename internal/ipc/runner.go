package ipc

import "context"

// OpRunner is how the HTTP layer talks to cliamp. Task 2's Conn implements
// Call; Task 3 adds SubmitJob. The signature is fixed for T1/T2/T3/T7.
type OpRunner interface {
	Call(ctx context.Context, method string, params any) (*Response, error)
	SubmitJob(ctx context.Context, op string, params any) (*Job, error)
}

// Response and Job are placeholders so OpRunner compiles before Task 2.
// Task 2 (internal/ipc/conn.go) replaces these declarations: delete them
// there, do not redeclare them.
type Response struct{}
type Job struct{}
