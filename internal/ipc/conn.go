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
	"log/slog"
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

var (
	ErrNotConnected  = errors.New("cliamp socket not connected")
	ErrFrameTooLarge = errors.New("ipc frame exceeds the frame size cap")
)

const (
	// dialGrace is how long a Call waits for the reconnect loop to bring a
	// connection up before handing the caller ErrNotConnected.
	dialGrace = 3 * time.Second
	// maxLine is the protocol's maximum NDJSON frame, enforced in both
	// directions: inbound violations retire the generation, outbound ones are
	// rejected before anything reaches the peer.
	maxLine = 1 << 20
	// minBackoff/maxBackoff bound the redial ladder. The ceiling stays under
	// dialGrace on purpose: a ladder step longer than the call grace would
	// make callers time out while the loop is asleep, even after the socket is
	// back.
	minBackoff = 500 * time.Millisecond
	maxBackoff = 5 * time.Second
	// writeGrace bounds how long one frame may sit in the socket buffer when
	// the peer stops draining it.
	writeGrace = 5 * time.Second
	// readIdle is the liveness window: a peer that sends nothing and never
	// closes is treated as gone after this long, so a wedged socket cannot look
	// healthy forever. Idle re-dials are cheap and keep the pipe fresh.
	readIdle = 60 * time.Second
)

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

// nextBackoff steps the redial ladder and tops out at max instead of doubling
// past it, so the ladder is 0.5, 1, 2, 4, 5, 5... seconds.
func nextBackoff(d, max time.Duration) time.Duration {
	if d >= max/2 {
		return max
	}
	return 2 * d
}

// connState is one socket generation; it dies with the connection. dev/ino
// record which endpoint the path referred to when we dialled it, so a cliamp
// stop or restart cannot leave us serving requests from a dead generation.
type connState struct {
	nc   net.Conn
	dev  uint64
	ino  uint64
	mu   sync.Mutex    // guards pend
	wmu  chan struct{} // write token: one whole frame at a time, ctx-cancelable
	pend map[string]chan *Response
}

// lockWrite takes the write token, or fails with ctx.Err() rather than queueing
// behind a peer that has stopped draining.
func (st *connState) lockWrite(ctx context.Context) error {
	select {
	case st.wmu <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (st *connState) writeUnlock() { <-st.wmu }

// Conn is an auto-reconnecting NDJSON V2 request/response client.
type Conn struct {
	socket  string
	log     *slog.Logger
	minBack time.Duration
	maxBack time.Duration
	mu      sync.Mutex
	st      *connState
	// changed is replaced by an unclosed channel on every connect/disconnect
	// transition, and the channel it replaces is closed. A waiter that finds st
	// nil goes back to sleep on the newest channel.
	changed chan struct{}
	closed  bool
	quit    chan struct{} // closed by Close to break the backoff sleep
	kick    chan struct{} // cap-1 "dial now" nudge from waiting callers
}

func NewConn(socket string) *Conn {
	return newConnLadder(socket, minBackoff, maxBackoff)
}

// newConnLadder lets tests watch a whole backoff ladder inside a second.
func newConnLadder(socket string, minBack, maxBack time.Duration) *Conn {
	c := &Conn{
		socket:  socket,
		log:     slog.Default(),
		minBack: minBack,
		maxBack: maxBack,
		changed: make(chan struct{}),
		quit:    make(chan struct{}),
		kick:    make(chan struct{}, 1),
	}
	go c.loop()
	return c
}

func (c *Conn) loop() {
	backoff := c.minBack
	for {
		c.mu.Lock()
		done := c.closed
		c.mu.Unlock()
		if done {
			return
		}
		nc, err := net.Dial("unix", c.socket)
		if err != nil {
			ok, kicked := c.wait(backoff)
			if !ok {
				return
			}
			if kicked {
				backoff = c.minBack
			} else {
				backoff = nextBackoff(backoff, c.maxBack)
			}
			continue
		}
		backoff = c.minBack
		st := &connState{nc: nc, pend: map[string]chan *Response{}, wmu: make(chan struct{}, 1)}
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
		readErr := c.readLoop(st)
		c.detach(st)
		nc.Close()
		switch {
		case readErr == nil, errors.Is(readErr, net.ErrClosed): // peer hangup or our own teardown
		case errors.Is(readErr, bufio.ErrTooLong):
			// A frame over the cap is a protocol violation the peer will repeat,
			// so say so and step back instead of redialling at full speed.
			c.log.Warn("ipc peer frame over cap", "socket", c.socket, "cap", maxLine)
			if ok, _ := c.wait(c.minBack); !ok {
				return
			}
		default:
			c.log.Debug("ipc socket read failed", "socket", c.socket, "err", readErr)
		}
	}
}

// wait sleeps up to d unless the Conn is closing. The second result reports
// whether a caller kicked the loop, which restarts the ladder.
func (c *Conn) wait(d time.Duration) (proceed, kicked bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return false, false
	}
	select {
	case <-t.C:
		return true, false
	case <-c.kick:
		return true, true
	case <-c.quit:
		return false, false
	}
}

// kickNow pulls the next dial forward: an arriving caller must not have to wait
// out the ladder step the loop is already sleeping through.
func (c *Conn) kickNow() {
	select {
	case c.kick <- struct{}{}:
	default:
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

// readLoop answers pending requests one NDJSON line at a time and returns the
// reason the socket died. Malformed or uncorrelated lines are skipped, never
// fatal (Review Focus #1): a stray banner or a pushed event must not tear down
// the command socket.
func (c *Conn) readLoop(st *connState) error {
	sc := bufio.NewScanner(st.nc)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for {
		if err := st.nc.SetReadDeadline(time.Now().Add(readIdle)); err != nil {
			break // generation is already gone
		}
		if !sc.Scan() {
			break
		}
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
	return sc.Err()
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
	if len(payload)+1 > maxLine {
		return nil, fmt.Errorf("%w: request is %d bytes, cap is %d", ErrFrameTooLarge, len(payload)+1, maxLine)
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
		c.kickNow()
		st, err = c.current(ctx, st)
		if err != nil {
			return nil, err
		}
	}
	ch := make(chan *Response, 1)
	st.mu.Lock()
	st.pend[id] = ch
	st.mu.Unlock()
	if err := st.lockWrite(ctx); err != nil {
		st.mu.Lock()
		delete(st.pend, id)
		st.mu.Unlock()
		return nil, err
	}
	deadline := time.Now().Add(writeGrace)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	werr := st.nc.SetWriteDeadline(deadline)
	if werr == nil {
		_, werr = st.nc.Write(append(payload, '\n')) // one whole frame, never interleaved
	}
	st.nc.SetWriteDeadline(time.Time{})
	st.writeUnlock()
	if werr != nil {
		// The frame never got out, so this generation is not trustworthy: retire
		// it instead of letting a wedged socket keep looking connected.
		st.nc.Close()
		c.kickNow()
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
	c.kickNow()
	wait, cancel := context.WithTimeout(ctx, dialGrace)
	defer cancel()
	for {
		select {
		case <-changed:
		case <-wait.Done():
			c.kickNow() // this caller gave up: let the next one try the socket now
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
