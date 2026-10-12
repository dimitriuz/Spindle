package ipc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// IPCError is a V2 error object as carried on the wire.
type IPCError struct{ Code, Detail string }

func (e *IPCError) Error() string { return e.Code + ": " + e.Detail }

// Event is a pushed V2 event. Conn's command socket ignores them; Subscriber
// (Task 4) owns the event stream.
type Event struct {
	Topic    string          `json:"event"`
	Seq      int64           `json:"seq"`
	Retained bool            `json:"retained,omitempty"`
	Data     json.RawMessage `json:"data"`
}

// Job is a long-running operation handle. SubmitJob lands in Task 3.
type Job struct {
	ID        string          `json:"id"`
	Operation string          `json:"operation"`
	State     string          `json:"state"`
	Error     *IPCError       `json:"error,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Snapshot  json.RawMessage `json:"snapshot,omitempty"`
}

// Response is one V2 reply, with the untouched line kept in Raw for handlers
// that need fields this struct does not model yet.
type Response struct {
	ID       string          `json:"id"`
	OK       bool            `json:"ok"`
	Err      *IPCError       `json:"error,omitempty"`
	Job      *Job            `json:"job,omitempty"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// OpRunner is how the HTTP layer talks to cliamp. Conn implements Call here;
// Task 3 adds SubmitJob. The signature is fixed for T1/T2/T3/T7.
type OpRunner interface {
	Call(ctx context.Context, method string, params any) (*Response, error)
	SubmitJob(ctx context.Context, op string, params any) (*Job, error)
}

var ErrNotConnected = errors.New("cliamp socket not connected")

// dialGrace is how long a Call waits for the reconnect loop to bring a
// connection up before handing the caller ErrNotConnected.
const dialGrace = 3 * time.Second

// maxLine is the protocol's maximum NDJSON frame.
const maxLine = 1 << 20

func decodeErr(raw json.RawMessage) *IPCError {
	var e struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
		Msg    string `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return &IPCError{Code: "internal_error"}
	}
	d := e.Detail
	if d == "" {
		d = e.Msg
	}
	return &IPCError{Code: e.Code, Detail: d}
}

// connState is one socket generation; it dies with the connection. dev/ino
// record which endpoint the path referred to when we dialled it, so a cliamp
// stop or restart cannot leave us serving requests from a dead generation.
type connState struct {
	nc   net.Conn
	dev  uint64
	ino  uint64
	mu   sync.Mutex // guards pend
	wmu  sync.Mutex // serialises request writes
	pend map[string]chan *Response
}

// Conn is an auto-reconnecting NDJSON V2 request/response client.
type Conn struct {
	socket string
	mu     sync.Mutex
	st     *connState
	// changed is replaced by an unclosed channel on every connect/disconnect
	// transition, and the channel it replaces is closed. A waiter that finds st
	// nil goes back to sleep on the newest channel.
	changed chan struct{}
	closed  bool
	quit    chan struct{} // closed by Close to break the backoff sleep
}

func NewConn(socket string) *Conn {
	c := &Conn{socket: socket, changed: make(chan struct{}), quit: make(chan struct{})}
	go c.loop()
	return c
}

func (c *Conn) loop() {
	backoff := 500 * time.Millisecond
	for {
		c.mu.Lock()
		done := c.closed
		c.mu.Unlock()
		if done {
			return
		}
		nc, err := net.Dial("unix", c.socket)
		if err != nil {
			if !c.wait(backoff) {
				return
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 500 * time.Millisecond
		st := &connState{nc: nc, pend: map[string]chan *Response{}}
		if fi, err := os.Stat(c.socket); err == nil {
			if sa, ok := fi.Sys().(*syscall.Stat_t); ok {
				st.dev, st.ino = sa.Dev, sa.Ino
			}
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			nc.Close()
			return
		}
		c.st = st
		old := c.changed
		c.changed = make(chan struct{})
		c.mu.Unlock()
		close(old)
		c.readLoop(st)
		c.detach(st)
		nc.Close()
	}
}

func (c *Conn) wait(d time.Duration) bool { // returns false when Closed during wait
	t := time.NewTimer(d)
	defer t.Stop()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return false
	}
	select {
	case <-t.C:
		return true
	case <-c.quit:
		return false
	}
}

// detach retires generation st and wakes its pending waiters with nil.
func (c *Conn) detach(st *connState) {
	c.mu.Lock()
	if c.st == st {
		c.st = nil
		old := c.changed
		c.changed = make(chan struct{})
		c.mu.Unlock()
		close(old)
	} else {
		c.mu.Unlock()
	}
	st.mu.Lock()
	for id, ch := range st.pend {
		select {
		case ch <- nil:
		default: // already answered; that caller deletes its own entry
		}
		delete(st.pend, id)
	}
	st.mu.Unlock()
}

// readLoop answers pending requests one NDJSON line at a time. Malformed or
// uncorrelated lines are skipped, never fatal (Review Focus #1): a stray
// banner or a pushed event must not tear down the command socket.
func (c *Conn) readLoop(st *connState) {
	sc := bufio.NewScanner(st.nc)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(line, &raw) != nil {
			continue // garbage line: skip, never crash
		}
		idv, hasID := raw["id"]
		if !hasID { // events belong to Subscriber (Task 4)
			continue
		}
		var id string
		if json.Unmarshal(idv, &id) != nil {
			continue // garbage id
		}
		st.mu.Lock()
		ch, ok := st.pend[id]
		st.mu.Unlock()
		if !ok {
			continue
		}
		full, _ := json.Marshal(raw)
		resp := &Response{Raw: full}
		json.Unmarshal(full, resp)
		if e, isErr := raw["error"]; isErr {
			resp.Err = decodeErr(e)
		}
		ch <- resp // buffered (cap 1); detach only runs once this loop is done
	}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Conn) Call(ctx context.Context, method string, params any) (*Response, error) {
	req := map[string]any{"version": 2, "id": newID(), "method": method}
	if params != nil {
		req["params"] = params
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	id := req["id"].(string)
	st, err := c.current(ctx, nil)
	if err != nil {
		return nil, err
	}
	if c.stale(st) {
		// cliamp stopped or restarted since we dialled: this pipe is a zombie,
		// so retire it and wait for the live generation.
		st.nc.Close()
		st, err = c.current(ctx, st)
		if err != nil {
			return nil, err
		}
	}
	ch := make(chan *Response, 1)
	st.mu.Lock()
	st.pend[id] = ch
	st.mu.Unlock()
	st.wmu.Lock() // one whole NDJSON frame per write: never interleave requests
	_, werr := st.nc.Write(append(payload, '\n'))
	st.wmu.Unlock()
	if werr != nil {
		st.mu.Lock()
		delete(st.pend, id)
		st.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrNotConnected, werr)
	}
	select {
	case resp := <-ch:
		st.mu.Lock()
		delete(st.pend, id)
		st.mu.Unlock()
		if resp == nil {
			return nil, ErrNotConnected
		}
		if resp.Err != nil {
			return resp, resp.Err
		}
		return resp, nil
	case <-ctx.Done():
		st.mu.Lock()
		delete(st.pend, id)
		st.mu.Unlock()
		return nil, ctx.Err()
	}
}

// current returns the live connection, waiting at most dialGrace (and never
// past ctx) for the reconnect loop to bring one up. A non-nil retire means
// "any generation but that dead one".
func (c *Conn) current(ctx context.Context, retire *connState) (*connState, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("closed")
	}
	st := c.st
	changed := c.changed
	c.mu.Unlock()
	if st != nil && st != retire {
		return st, nil
	}
	wait, cancel := context.WithTimeout(ctx, dialGrace)
	defer cancel()
	for {
		select {
		case <-changed:
		case <-wait.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			return nil, ErrNotConnected
		case <-c.quit:
			return nil, errors.New("closed")
		}
		c.mu.Lock()
		st = c.st
		changed = c.changed
		c.mu.Unlock()
		if st != nil && st != retire {
			return st, nil
		}
	}
}

// stale reports whether the socket path no longer refers to the endpoint st
// was dialled against. A zero identity means we could not stat the path at
// dial time, so we never claim staleness on that generation.
func (c *Conn) stale(st *connState) bool {
	if st.ino == 0 {
		return false
	}
	fi, err := os.Stat(c.socket)
	if err != nil {
		return true
	}
	sa, ok := fi.Sys().(*syscall.Stat_t)
	return ok && (sa.Dev != st.dev || sa.Ino != st.ino)
}

func (c *Conn) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.st != nil {
		c.st.nc.Close()
	}
	c.mu.Unlock()
	close(c.quit)
}
