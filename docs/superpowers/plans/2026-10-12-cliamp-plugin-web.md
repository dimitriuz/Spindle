# cliamp-plugin-web Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A cliamp plugin + Go bridge + embedded web UI that lets any LAN browser control and mirror cliamp (transport, now-playing, live playlist).

**Architecture:** The Lua plugin (`web.lua`, permission `exec` only) spawns and supervises a Go binary `cliamp-web` that keeps two connections to `~/.config/cliamp/cliamp.sock` — one NDJSON V2 event subscriber, one command connection — caches state, and serves a token-authenticated REST+SSE API to a Vue 3 SPA embedded via `go:embed`.

**Tech Stack:** Go 1.23 (stdlib only in the bridge), Vue 3 + Vite + TypeScript + Reka UI (webui, build-time only), Lua 5.1 plugin API.

**Spec:** `docs/superpowers/specs/2026-10-12-cliamp-web-design.md` (read it alongside this plan)

## Global Constraints

- IPC protocol: version 2 only, NDJSON, 1 MiB max frame, `~/.config/cliamp/cliamp.sock` (override with `--socket`).
- Bridge binary name `cliamp-web`; plugin name `web`; repo name `cliamp-plugin-web`.
- Default port **8730**, default bind **127.0.0.1**; `--lan` ⇒ `0.0.0.0`.
- Token: 32 random bytes hex (256-bit), file `<data-dir>/token` mode 0600; data dir default `~/.local/share/cliamp/web`.
- Queue cache cap: **5000** rows (`truncated: true` beyond); SSE heartbeat every **15 s**; rate limit on `/api/command` **20/s, burst 40**; bridge restart policy: max **3** restarts, **2 s** apart.
- Go module path is `cliamp-web` (local module; no publishing).
- Lua: 5.1-compatible (no `goto`, no `#`-on-table tricks beyond basics); plugin API exactly as in cliamp `docs/plugins.md`.
- All `/api/*` except `/api/health` require the token; POST requires the `X-Token` header.
- Bridge stdout must stay quiet by default (exec output cap 4 MiB); `--verbose` opts in.

## Review Focus

Failure modes the spec implies but no single task title names — each is pinned by a test in the task that owns the code:

1. Garbage on the wire: a non-JSON or non-V2 line from cliamp.sock must be logged and skipped, never crash the client (Task 2).
2. Position/duration are 0 for live streams: UI must show an indeterminate bar and never send `seek.absolute` (Task 10).
3. A browser's SSE connection must become consistent without polling: on every (re)subscribe the bridge replays current `player` + `state` (Task 6/8).
4. `queue.move` during shuffle: cliamp answers `conflict` → HTTP 409 → UI refetches queue, row order snaps back (Task 7/11).
5. Token edge cases: empty `?token=`, wrong token on POST with a valid GET token — POST still fails (Task 5).

---

## File Structure

```
cliamp-plugin-web/
  web.lua                      plugin entry (Task 13)
  go.mod                       module cliamp-web, go 1.23 (Task 1)
  cmd/cliamp-web/main.go       flags, wiring, status.json, shutdown (Tasks 1, 8)
  internal/ipc/conn.go         request/response NDJSON client (Task 2)
  internal/ipc/fake_test.go    fake cliamp.sock server used by all ipc tests (Task 2)
  internal/ipc/jobs.go         SubmitJob + job polling (Task 3)
  internal/ipc/subscriber.go   persistent event connection (Task 3)
  internal/snapshot/cache.go   state/queue/caps cache + event reducer (Task 4)
  internal/server/auth.go      token store, auth, host guard (Task 5)
  internal/server/limiter.go   per-IP token bucket (Task 7)
  internal/server/hub.go       SSE fan-out (Task 6)
  internal/server/handlers.go  REST + SSE endpoints, error mapping (Tasks 6, 7)
  internal/server/server.go    http.Handler assembly, static embed (Task 1 skeleton, 6/7 final)
  webui/                       Vue 3 + Vite + TS + Reka UI (Tasks 9-12)
    dist/index.html            checked-in placeholder so `go build` works without Node
  Makefile PKGBUILD README.md  (Task 14)
  .github/workflows/ci.yml release.yml (Task 14)
```

---

### Task 1: Scaffold + `serve` skeleton + health endpoint

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`, `cmd/cliamp-web/main.go`, `internal/server/server.go`, `internal/server/handlers.go`, `webui/dist/index.html`, `webui/.gitkeep-placeholder` not needed
- Test: `internal/server/handlers_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `server.New(cfg Config) http.Handler` with `Config{ Cache *snapshot.Cache; Runner ipc.OpRunner; Hub *Hub; Guard *HostGuard; Auth *Auth; Limiter *Limiter; Static fs.FS; Logger *slog.Logger }`; `snapshot.NewCache() *Cache`; `ipc.OpRunner` interface (defined now, implemented Task 2/3): `interface { Call(ctx context.Context, method string, params any) (*Response, error); SubmitJob(ctx context.Context, op string, params any) (*Job, error) }`; `GET /api/health` → `{"ok":true,"player":"offline"}`.

- [ ] **Step 1: Create module and skeleton**

`go.mod`:
```
module cliamp-web

go 1.23
```

`.gitignore`:
```
dist/
node_modules/
webui/dist/*
!webui/dist/index.html
```

`webui/dist/index.html` (placeholder so `go:embed` always compiles):
```html
<!doctype html><title>cliamp-web</title><p>Run <code>make ui</code> to build the real UI.</p>
```

`internal/snapshot/cache.go` (minimal until Task 4 grows it):
```go
package snapshot

import "sync/atomic"

type Cache struct {
	online atomic.Bool
}

func NewCache() *Cache { return &Cache{} }
func (c *Cache) SetOnline(v bool) { c.online.Store(v) }
func (c *Cache) Online() bool     { return c.online.Load() }
```

`internal/server/handlers.go`:
```go
package server

import (
	"encoding/json"
	"net/http"
)

func (s *serverState) health(w http.ResponseWriter, r *http.Request) {
	status := "offline"
	if s.cfg.Cache.Online() {
		status = "online"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "true", "player": status})
}
```

`internal/server/server.go`:
```go
package server

import (
	"io/fs"
	"log/slog"

	"cliamp-web/internal/ipc"
	"cliamp-web/internal/snapshot"
)

type Config struct {
	Cache   *snapshot.Cache
	Runner  ipc.OpRunner
	Hub     *Hub
	Auth    *Auth
	Guard   *HostGuard
	Limiter *Limiter
	Static  fs.FS
	Logger  *slog.Logger
}

type serverState struct{ cfg Config }

// New assembles the HTTP handler. Auth/Host/Limit middleware wrap /api/*;
// the token-free health route is exempt.
func New(cfg Config) http.Handler {
	s := &serverState{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	return mux
}
```

`cmd/cliamp-web/main.go`:
```go
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"cliamp-web/internal/snapshot"
	"cliamp-web/internal/server"
)

//go:embed webui/dist
var webui embed.FS

func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d + "/cliamp/web"
	}
	return os.Getenv("HOME") + "/.local/share/cliamp/web"
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: cliamp-web serve [--port N] [--lan] [--data-dir DIR] [--verbose] [--allowed-hosts LIST] [--socket PATH] [--bind ADDR]")
		os.Exit(2)
	}
	// flag parsing + full wiring arrives in Task 8; this skeleton only
	// starts an HTTP server answering /api/health.
	port := os.Getenv("CLIAMP_WEB_PORT") // Task 8 replaces with real flags
	if port == "" {
		port = "8730"
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cache := snapshot.NewCache()
	static, _ := fs.Sub(webui, "webui/dist")
	h := server.New(server.Config{Cache: cache, Hub: server.NewHub(), Static: static, Logger: logger})
	addr := "127.0.0.1:" + port
	logger.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, h); err != nil {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}
```

`Makefile`:
```make
build:
	go build -o dist/cliamp-web ./cmd/cliamp-web
test:
	go vet ./... && go test ./...
```

`internal/server/hub.go` — needed now by main (full implementation Task 6):
```go
package server

type Hub struct{ clients map[uint64]chan Frame }

type Frame struct{ Name, Data string }

func NewHub() *Hub { return &Hub{clients: map[uint64]chan Frame{}} }
```

- [ ] **Step 2: Write the failing test**

`internal/server/handlers_test.go`:
```go
package server

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"testing"

	"cliamp-web/internal/snapshot"
)

func testConfig(t *testing.T) Config {
	return Config{Cache: snapshot.NewCache(), Hub: NewHub(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestHealthReportsOffline(t *testing.T) {
	h := New(testConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["player"] != "offline" {
		t.Fatalf("want player=offline, got %q", body["player"])
	}
}
```
(Add `import ("io"; "net/http")` as required by the code above — final file imports: encoding/json, io, io/fs, log/slog, net/http, net/http/httptest, testing.)

- [ ] **Step 3: Run, watch it fail** — `go test ./internal/server/` — expect compile error (missing `http.Handler` return / missing `Hub`), fix skeleton until the test passes.
- [ ] **Step 4: `make build && make test` green. Manual check:** `./dist/cliamp-web serve &` then `curl -s localhost:8730/api/health` → `{"ok":"true","player":"offline"}`. Kill it.
- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat: scaffold bridge skeleton with health endpoint"`

---

### Task 2: `ipc.Conn` — NDJSON V2 request/response client

**Files:**
- Create: `internal/ipc/conn.go`
- Test: `internal/ipc/fake_test.go`, `internal/ipc/conn_test.go`

**Interfaces:**
- Consumes: nothing (raw socket).
- Produces: `ipc.Event{Topic string; Seq int64; Retained bool; Data json.RawMessage}`; `ipc.IPCError{Code, Detail string}` (implements `error`); `ipc.Response{ID string; OK bool; Err *IPCError; Job *Job; Snapshot json.RawMessage; Raw json.RawMessage}`; `ipc.NewConn(socketPath string) *Conn`; `(c *Conn) Call(ctx context.Context, method string, params any) (*Response, error)`; implements `OpRunner` partially (`Call`; `SubmitJob` in Task 3).

- [ ] **Step 1: Fake cliamp.sock server (used by every later IPC test too)**

`internal/ipc/fake_test.go`:
```go
package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeIPC listens on a temp unix socket and replies per a handler func.
type fakeIPC struct {
	t        *testing.T
	dir      string
	path     string
	ln       net.Listener
	mu       sync.Mutex
	received []map[string]any
	handler  func(req map[string]any) []string // returns NDJSON lines to write
}

func newFakeIPC(t *testing.T, handler func(map[string]any) []string) *fakeIPC {
	dir := t.TempDir()
	path := filepath.Join(dir, "s") // keep path short: unix socket 107-byte limit
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIPC{t: t, dir: dir, path: path, ln: ln, handler: handler}
	go f.serve()
	t.Cleanup(func() { f.ln.Close() })
	return f
}

func (f *fakeIPC) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			dec := json.NewDecoder(c)
			for {
				var req map[string]any
				if dec.Decode(&req) != nil {
					c.Close()
					return
				}
				f.mu.Lock()
				f.received = append(f.received, req)
				f.mu.Unlock()
				for _, line := range f.handler(req) {
					fmt.Fprintln(c, line)
				}
			}
		}()
	}
}

func (f *fakeIPC) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}
```

- [ ] **Step 2: Write failing tests**

`internal/ipc/conn_test.go`:
```go
package ipc

import (
	"context"
	"errors"
	"testing"
	"time"
)

func okReply(id string, extra string) string {
	if extra == "" {
		return `{"version":2,"id":"` + id + `","ok":true}`
	}
	return `{"version":2,"id":"` + id + `","ok":true,` + extra + `}`
}

func TestCallCorrelatesByID(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{okReply(req["id"].(string), `"snapshot":{"state":"playing"}`)}
	})
	c := NewConn(f.path)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c.Call(ctx, "state.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || string(resp.Snapshot) != `{"state":"playing"}` {
		t.Fatalf("bad resp: %+v", resp)
	}
}

func TestCallReturnsIPCError(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":false,"error":{"code":"conflict","detail":"stale"}}`}
	})
	c := NewConn(f.path)
	defer c.Close()
	_, err := c.Call(context.Background(), "state.get", nil)
	var ie *IPCError
	if !errors.As(err, &ie) || ie.Code != "conflict" {
		t.Fatalf("want conflict IPCError, got %v", err)
	}
}

func TestGarbageLinesSkipped(t *testing.T) { // Review Focus #1
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{"not json", `{"id":"no-version"}`, okReply(req["id"].(string), "")}
	})
	c := NewConn(f.path)
	defer c.Close()
	resp, err := c.Call(context.Background(), "state.get", nil)
	if err != nil || !resp.OK {
		t.Fatalf("call should survive garbage: %v", err)
	}
}

func TestReconnectsAndStillServes(t *testing.T) {
	var path string
	f := newFakeIPC(t, func(req map[string]any) []string {
		return []string{okReply(req["id"].(string), "")}
	})
	path = f.path
	c := NewConn(path)
	defer c.Close()
	if _, err := c.Call(context.Background(), "state.get", nil); err != nil {
		t.Fatal(err)
	}
	f.ln.Close() // kill listener: conn must back off, and Calls during downtime fail fast
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "state.get", nil); err == nil {
		t.Fatal("want error while socket is down")
	}
}
```

- [ ] **Step 3: Run — expect "undefined: NewConn".**
- [ ] **Step 4: Implement `internal/ipc/conn.go`**

```go
package ipc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"time"
)

type IPCError struct{ Code, Detail string }

func (e *IPCError) Error() string { return e.Code + ": " + e.Detail }

type Event struct {
	Topic    string          `json:"event"`
	Seq      int64           `json:"seq"`
	Retained bool            `json:"retained,omitempty"`
	Data     json.RawMessage `json:"data"`
}

type Job struct {
	ID        string          `json:"id"`
	Operation string          `json:"operation"`
	State     string          `json:"state"`
	Error     *IPCError       `json:"error,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Snapshot  json.RawMessage `json:"snapshot,omitempty"`
}

type Response struct {
	ID       string          `json:"id"`
	OK       bool            `json:"ok"`
	Err      *IPCError       `json:"error,omitempty"`
	Job      *Job            `json:"job,omitempty"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

type OpRunner interface {
	Call(ctx context.Context, method string, params any) (*Response, error)
	SubmitJob(ctx context.Context, op string, params any) (*Job, error)
}

var ErrNotConnected = errors.New("cliamp socket not connected")

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

type connState struct {
	nc   net.Conn
	dec  *json.Decoder
	pend map[string]chan *Response
}

// Conn is an auto-reconnecting NDJSON V2 request/response client.
type Conn struct {
	socket string
	mu     sync.Mutex
	st     *connState
	ready  chan struct{} // closed while connected... recreated on disconnect
	closed bool
}

func NewConn(socket string) *Conn {
	c := &Conn{socket: socket, ready: make(chan struct{})}
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
		st := &connState{nc: nc, dec: json.NewDecoder(bufio.NewReader(nc)), pend: map[string]chan *Response{}}
		c.mu.Lock()
		c.st = st
		c.ready = make(chan struct{})
		st2 := st
		rdy := c.ready
		c.mu.Unlock()
		close(rdy)
		c.readLoop(st2)
		c.mu.Lock()
		if c.st == st2 {
			c.st = nil
			for _, ch := range st2.pend {
				close(ch)
			}
		}
		c.mu.Unlock()
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
	time.Sleep(d)
	return true
}

func (c *Conn) readLoop(st *connState) {
	for {
		var raw map[string]json.RawMessage
		if err := st.dec.Decode(&raw); err != nil {
			return
		}
		idv, hasID := raw["id"]
		if !hasID { // event or garbage on a command conn: events belong to Subscriber; skip
			continue
		}
		var id string
		if json.Unmarshal(idv, &id) != nil {
			continue // garbage: Review Focus #1 — skip, never crash
		}
		ch, ok := st.pend[id]
		if !ok {
			continue
		}
		full, _ := json.Marshal(raw)
		resp := &Response{Raw: full}
		json.Unmarshal(full, resp)
		if e, isErr := raw["error"]; isErr {
			resp.Err = decodeErr(e)
		}
		ch <- resp
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
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("closed")
	}
	st := c.st
	rdy := c.ready
	c.mu.Unlock()
	if st == nil {
		wait, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		select {
		case <-rdy:
		case <-wait.Done():
			return nil, ErrNotConnected
		}
		c.mu.Lock()
		st = c.st
		c.mu.Unlock()
		if st == nil {
			return nil, ErrNotConnected
		}
	}
	ch := make(chan *Response, 1)
	st.pend[req["id"].(string)] = ch
	if _, err := st.nc.Write(append(payload, '\n')); err != nil {
		delete(st.pend, req["id"].(string))
		return nil, err
	}
	select {
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, ErrNotConnected
		}
		if resp.Err != nil {
			return resp, resp.Err
		}
		return resp, nil
	case <-ctx.Done():
		delete(st.pend, req["id"].(string))
		return nil, ctx.Err()
	}
}

func (c *Conn) Close() {
	c.mu.Lock()
	c.closed = true
	if c.st != nil {
		c.st.nc.Close()
	}
	c.mu.Unlock()
}
```

- [ ] **Step 5: `go test ./internal/ipc/ -race` green.**
- [ ] **Step 6: Commit** — `git commit -am "feat: ipc V2 request/response client with reconnect"`

---

### Task 3: Jobs + `Subscriber` (event connection)

**Files:**
- Create: `internal/ipc/jobs.go`, `internal/ipc/subscriber.go`
- Test: `internal/ipc/jobs_test.go`, `internal/ipc/subscriber_test.go`

**Interfaces:**
- Consumes: `Conn`, `newFakeIPC`, `Job`, `Event` (Task 2).
- Produces: `(c *Conn) SubmitJob(ctx, op string, params any) (*Job, error)` — polls `job.get` every 200 ms until `succeeded|failed|canceled`; `ipc.NewSubscriber(socket string, topics []string, onEvent func(Event), onOnline func(bool)) *Subscriber`; `(s *Subscriber) Run(ctx context.Context)` (blocking reconnect loop; one subscription per connection, per V2 rule "use another IPC connection for normal commands").

- [ ] **Step 1: Failing job tests**

`internal/ipc/jobs_test.go`:
```go
package ipc

import (
	"context"
	"errors"
	"testing"
)

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
}

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
```

- [ ] **Step 2: Failing subscriber tests**

`internal/ipc/subscriber_test.go`:
```go
package ipc

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSubscriberHandshakeAndEvents(t *testing.T) {
	f := newFakeIPC(t, func(req map[string]any) []string {
		if req["method"] == "subscribe" {
			return []string{
				`{"version":2,"id":"` + req["id"].(string) + `","ok":true}`,
				`{"event":"runtime.state","seq":1,"retained":true,"data":{"state":"playing"}}`,
			}
		}
		return nil
	})
	var mu sync.Mutex
	var got []Event
	var online []bool
	s := NewSubscriber(f.path, []string{"runtime.state"},
		func(e Event) { mu.Lock(); got = append(got, e); mu.Unlock() },
		func(b bool) { mu.Lock(); online = append(online, b); mu.Unlock() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Topic != "runtime.state" || !got[0].Retained {
		t.Fatalf("bad events: %+v", got)
	}
	if len(online) == 0 || !online[0] {
		t.Fatalf("want online true first, got %v", online)
	}
}

func TestSubscriberOverflowTriggersResyncCycle(t *testing.T) {
	cycles := 0
	var mu sync.Mutex
	f := newFakeIPC(t, func(req map[string]any) []string {
		if req["method"] == "subscribe" {
			if cycles == 0 {
				return []string{
					`{"version":2,"id":"` + req["id"].(string) + `","ok":true}`,
					`{"event":"system.overflow","seq":2,"data":{"resync_required":true}}`,
				}
			}
			return []string{`{"version":2,"id":"` + req["id"].(string) + `","ok":true}`}
		}
		return nil
	})
	// fake handler can't see reconnects directly; count subscribe requests instead
	go func() { _ = f }()
	s := NewSubscriber(f.path, []string{"runtime.state"}, nil, func(bool) {})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go s.Run(ctx)
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	subs := 0
	for _, r := range f.received {
		if r["method"] == "subscribe" {
			subs++
		}
	}
	if subs < 2 {
		t.Fatalf("overflow must close+resubscribe, got %d subscribes", subs)
	}
}
```

- [ ] **Step 3: Run — failing (undefined).**
- [ ] **Step 4: Implement `internal/ipc/jobs.go`**

```go
package ipc

import (
	"context"
	"time"
)

var terminal = map[string]bool{"succeeded": true, "failed": true, "canceled": true}

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
		t := time.NewTimer(200 * time.Millisecond)
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
```

- [ ] **Step 5: Implement `internal/ipc/subscriber.go`**

```go
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"
)

type Subscriber struct {
	socket   string
	topics   []string
	onEvent  func(Event)
	onOnline func(bool)
}

func NewSubscriber(socket string, topics []string, onEvent func(Event), onOnline func(bool)) *Subscriber {
	return &Subscriber{socket: socket, topics: topics, onEvent: onEvent, onOnline: onOnline}
}

func (s *Subscriber) Run(ctx context.Context) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		nc, err := net.Dial("unix", s.socket)
		if err != nil {
			if !sleepCtx(ctx, backoff) {
				return
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 500 * time.Millisecond
		online := s.read(ctx, nc)
		if online && s.onOnline != nil {
			s.onOnline(false)
		}
		nc.Close()
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

func (s *Subscriber) read(ctx context.Context, nc net.Conn) bool {
	sub, _ := json.Marshal(map[string]any{"version": 2, "id": strconv.Itoa(int(time.Now().UnixNano())), "method": "subscribe", "topics": s.topics})
	if _, err := nc.Write(append(sub, '\n')); err != nil {
		return false
	}
	dec := json.NewDecoder(bufio.NewReader(nc))
	var ack map[string]json.RawMessage
	if dec.Decode(&ack) != nil {
		return false
	}
	var ok bool
	if json.Unmarshal(ack["ok"], &ok) != nil || !ok {
		return false
	}
	if s.onOnline != nil {
		s.onOnline(true)
	}
	online := true
	for ctx.Err() == nil {
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return online
		}
		var ev Event
		if json.Unmarshal(raw, &ev) != nil || ev.Topic == "" {
			continue // garbage: skip (Review Focus #1)
		}
		if ev.Topic == "system.overflow" {
			return online // resync by reconnect: cliamp closes the stream; we resubscribe
		}
		if s.onEvent != nil {
			s.onEvent(ev)
		}
	}
	return online
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
```

- [ ] **Step 6: `go test ./internal/ipc/ -race` green. Commit** — `"feat: job polling and dedicated V2 event subscriber"`

---

### Task 4: `snapshot.Cache` — reducer, queue cap, revisions

**Files:**
- Modify: `internal/snapshot/cache.go`
- Test: `internal/snapshot/cache_test.go`

**Interfaces:**
- Consumes: `ipc.Event`.
- Produces: `(c *Cache) Apply(ev ipc.Event) string` → `"state"|"playback"|"playlist"|"settings"|"job"|""`; `(c *Cache) SetState(raw json.RawMessage)`; `(c *Cache) State() json.RawMessage`; `type Queue{Tracks json.RawMessage; PlayNext json.RawMessage; PlaylistRevision int64; Truncated bool; Count int}` (JSON tags `tracks,play_next,playlist_revision,truncated,count`); `(c *Cache) AbsorbQueueResult(res json.RawMessage, revision int64)` — parses `queue.list` result shapes `{tracks:[...]}`, `{items:[...]}`, or bare `[...]` and `playnext.list` likewise, applies 5000-row cap; `(c *Cache) Queue() Queue`; `(c *Cache) PlaylistRevision() int64`; `(c *Cache) SetCaps(raw json.RawMessage)`; `(c *Cache) Caps() json.RawMessage`.

- [ ] **Step 1: Failing tests**

`internal/snapshot/cache_test.go`:
```go
package snapshot

import (
	"encoding/json"
	"fmt"
	"testing"

	"cliamp-web/internal/ipc"
)

func ev(topic, data string) ipc.Event {
	return ipc.Event{Topic: topic, Data: json.RawMessage(data)}
}

func TestApplyStateUpdatesSnapshotAndRevision(t *testing.T) {
	c := NewCache()
	kind := c.Apply(ev("runtime.state", `{"revision":18,"state":"playing"}`))
	if kind != "state" {
		t.Fatalf("kind=%q", kind)
	}
	if got := string(c.State()); got != `{"revision":18,"state":"playing"}` {
		t.Fatalf("state=%s", got)
	}
	if c.PlaylistRevision() != 0 {
		t.Fatal("playlist revision must come from playlist events")
	}
}

func TestApplyPlaylistSignalsAndTracksRevision(t *testing.T) {
	c := NewCache()
	kind := c.Apply(ev("runtime.playlist", `{"playlist_revision":7}`))
	if kind != "playlist" {
		t.Fatal("want playlist")
	}
	if c.PlaylistRevision() != 7 {
		t.Fatalf("rev=%d", c.PlaylistRevision())
	}
}

func TestAbsorbQueueResultShapesAndTruncate(t *testing.T) {
	c := NewCache()
	c.AbsorbQueueResult(json.RawMessage(`{"tracks":[{"path":"a"},{"path":"b"}]}`), 7)
	c.AbsorbPlayNextResult(json.RawMessage(`[{"path":"n1"}]`), 7)
	q := c.Queue()
	if q.Count != 2 || q.PlaylistRevision != 7 || q.Truncated {
		t.Fatalf("queue=%+v", q)
	}
	// 5001-row input → 5000 kept, truncated=true
	var big = make([]map[string]string, 5001)
	for i := range big {
		big[i] = map[string]string{"path": fmt.Sprintf("p%d", i)}
	}
	raw, _ := json.Marshal(map[string]any{"items": big})
	c.AbsorbQueueResult(raw, 8)
	q = c.Queue()
	var back []json.RawMessage
	json.Unmarshal(q.Tracks, &back)
	if len(back) != 5000 || !q.Truncated {
		t.Fatalf("truncation failed: len=%d trunc=%v", len(back), q.Truncated)
	}
}
```
(Add `AbsorbPlayNextResult(raw json.RawMessage, revision int64)` to the Produces contract and mirror the `AbsorbQueueResult` parse; write its failing line into the test above — it is already exercised there.)

- [ ] **Step 2: Run — fail.**
- [ ] **Step 3: Implement** (replace cache.go; keep online flag from Task 1)

```go
package snapshot

import (
	"encoding/json"
	"sync"

	"cliamp-web/internal/ipc"
)

const QueueCap = 5000

type Cache struct {
	mu       sync.RWMutex
	state    json.RawMessage
	queue    Queue
	caps     json.RawMessage
	plRev    int64
	onlineCh chan struct{} // closed+replaced never; online flag:
	online   *atomicBool
}

type Queue struct {
	Tracks           json.RawMessage `json:"tracks"`
	PlayNext         json.RawMessage `json:"play_next"`
	PlaylistRevision int64           `json:"playlist_revision"`
	Truncated        bool            `json:"truncated"`
	Count            int             `json:"count"`
}

func NewCache() *Cache { return &Cache{state: json.RawMessage(`{}`), online: &atomicBool{}} }

func (c *Cache) SetOnline(v bool) { c.online.Store(v) }
func (c *Cache) Online() bool     { return c.online.Load() }

func (c *Cache) Apply(ev ipc.Event) string {
	switch ev.Topic {
	case "runtime.state":
		c.SetState(ev.Data)
		return "state"
	case "runtime.settings":
		c.SetState(ev.Data)
		return "settings"
	case "runtime.playback":
		c.mergePlayback(ev.Data)
		return "playback"
	case "runtime.playlist":
		var d struct {
			PlaylistRevision int64 `json:"playlist_revision"`
		}
		json.Unmarshal(ev.Data, &d)
		c.mu.Lock()
		c.plRev = d.PlaylistRevision
		c.mu.Unlock()
		return "playlist"
	case "runtime.job":
		return "job"
	}
	return ""
}

func (c *Cache) SetState(raw json.RawMessage) {
	c.mu.Lock()
	if len(raw) > 0 {
		c.state = raw
		var s struct {
			PlaylistRevision int64 `json:"playlist_revision"`
		}
		if json.Unmarshal(raw, &s) == nil && s.PlaylistRevision != 0 {
			c.plRev = s.PlaylistRevision
		}
	}
	c.mu.Unlock()
}

// mergePlayback patches position/state fields into the cached snapshot so
// /api/state never serves a stale position.
func (c *Cache) mergePlayback(raw json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var patch map[string]json.RawMessage
	if json.Unmarshal(raw, &patch) != nil {
		return
	}
	var cur map[string]json.RawMessage
	if json.Unmarshal(c.state, &cur) != nil {
		cur = map[string]json.RawMessage{}
	}
	for k, v := range patch {
		cur[k] = v
	}
	if out, err := json.Marshal(cur); err == nil {
		c.state = out
	}
}

func (c *Cache) State() json.RawMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Cache) PlaylistRevision() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.plRev
}

// absorbArray finds the track array in result ({tracks:[]}|{items:[]}|[]) and
// applies the 5000-row cap.
func absorbArray(res json.RawMessage) (array json.RawMessage, count int, truncated bool) {
	var raw json.RawMessage
	var obj map[string]json.RawMessage
	if json.Unmarshal(res, &obj) == nil {
		raw = obj["tracks"]
		if len(raw) == 0 {
			raw = obj["items"]
		}
	} else {
		raw = res // bare array
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		return json.RawMessage(`[]`), 0, false
	}
	if len(rows) > QueueCap {
		rows = rows[:QueueCap]
		truncated = true
	}
	out, _ := json.Marshal(rows)
	return out, len(rows), truncated
}
```
Then `AbsorbQueueResult` / `AbsorbPlayNextResult` set the corresponding `Queue` fields under `c.mu`, stamping `PlaylistRevision`; `Queue()` returns a copy. Add a tiny `atomicBool` (mutex-wrapped `bool`) in this package rather than importing `sync/atomic` oddly.
- [ ] **Step 4: `go test ./internal/snapshot/ -race` green. Commit** — `"feat: snapshot cache with event reducer and queue cap"`

---

### Task 5: Token store, auth middleware, host guard

**Files:**
- Create: `internal/server/auth.go`
- Test: `internal/server/auth_test.go`

**Interfaces:**
- Consumes: `Config` (Task 1).
- Produces: `LoadOrCreateToken(dataDir string) (string, error)` (32 bytes hex, `<dataDir>/token`, 0600, create parent dirs); `NewAuth(token string) *Auth`; `NewHostGuard(extra []string) *HostGuard`; middleware wiring inside `New`: every `/api/*` except `/api/health` passes Host guard + token check; **POST requires the `X-Token` header** (query token never authorizes POST — Review Focus #5).

- [ ] **Step 1: Failing tests**

`internal/server/auth_test.go`:
```go
package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func authedConfig(t *testing.T) Config {
	cfg := testConfig(t) // Task 1 helper
	tok := "deadbeef"
	cfg.Auth = NewAuth(tok)
	cfg.Guard = NewHostGuard(nil)
	// exercise a protected endpoint: /api/state answers from cache
	return cfg
}

func TestTokenRequired(t *testing.T) {
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != 401 {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestGETAcceptsHeaderOrQuery(t *testing.T) {
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Header.Set("X-Token", "deadbeef")
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("header token: got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state?token=deadbeef", nil))
	if rec.Code != 200 {
		t.Fatalf("query token: got %d", rec.Code)
	}
}

func TestPOSTNeverAcceptsQueryToken(t *testing.T) { // Review Focus #5
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/command?token=deadbeef", nil))
	if rec.Code != 401 {
		t.Fatalf("POST with query token only: want 401, got %d", rec.Code)
	}
}

func TestEmptyQueryTokenFails(t *testing.T) {
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state?token=", nil))
	if rec.Code != 401 {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestHostGuard(t *testing.T) {
	cfg := authedConfig(t)
	cfg.Guard = NewHostGuard([]string{"cliamp.home.lan"})
	h := New(cfg)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/state?token=deadbeef", nil)
	r.Host = "evil.example.com"
	h.ServeHTTP(rec, r)
	if rec.Code != 400 {
		t.Fatalf("want 400 on foreign Host, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/api/state?token=deadbeef", nil)
	r2.Host = "cliamp.home.lan"
	h.ServeHTTP(rec, r2)
	if rec.Code != 200 {
		t.Fatalf("allowed host: got %d", rec.Code)
	}
}

func TestTokenFileCreated0600(t *testing.T) {
	dir := t.TempDir()
	tok, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(tok))
	}
	b, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("token file wrong: %q %v", b, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "token"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %v", fi.Mode().Perm())
	}
	again, _ := LoadOrCreateToken(dir)
	if again != tok {
		t.Fatal("second load must return existing token")
	}
}
```

- [ ] **Step 2: Run — fail.**
- [ ] **Step 3: Implement `internal/server/auth.go`**

```go
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func LoadOrCreateToken(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "token")
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

type Auth struct{ token string }

func NewAuth(tok string) *Auth { return &Auth{token: tok} }

func (a *Auth) ok(got string) bool {
	return subtle.ConstantTimeCompare([]byte(a.token), []byte(got)) == 1
}

// Guard + Auth middleware applied inside New(). POSTs require the header;
// GETs accept header or query (EventSource cannot set headers).
func (s *serverState) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Guard != nil && !s.cfg.Guard.Allow(r) {
			http.Error(w, `{"code":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		if s.cfg.Auth != nil {
			hdr := r.Header.Get("X-Token")
			if r.Method == http.MethodPost {
				if !s.cfg.Auth.ok(hdr) {
					http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
					return
				}
			} else {
				q := r.URL.Query().Get("token")
				if !s.cfg.Auth.ok(hdr) && !s.cfg.Auth.ok(q) {
					http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

type HostGuard struct {
	allowed map[string]bool // interface IPs + localhost + configured extras
}

func NewHostGuard(extra []string) *HostGuard {
	g := &HostGuard{allowed: map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}}
	for _, e := range extra {
		if e = strings.TrimSpace(e); e != "" {
			g.allowed[e] = true
		}
	}
	if ifas, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifas {
			if ipn, ok := a.(*net.IPNet); ok {
				g.allowed[ipn.IP.String()] = true
			}
		}
	}
	return g
}

func (g *HostGuard) Allow(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return g.allowed[host]
}
```

`http.Error` sends text/plain — acceptable for 4xx; JSON body for 5xx errors comes from handlers (Task 7). In `server.New`, wrap the protected mux: `mux.Handle("GET /api/state", s.protect(http.HandlerFunc(s.state)))` — register `/api/state` now returning `Cache.State()` with `Content-Type: application/json` (add to handlers.go; test TestGETAcceptsHeaderOrQuery needs it).
- [ ] **Step 4: tests green (`-race`). Commit** — `"feat: token auth, host guard"`

---

### Task 6: SSE hub + `/api/events`

**Files:**
- Modify: `internal/server/hub.go` (replace skeleton), `internal/server/handlers.go`
- Test: `internal/server/sse_test.go`

**Interfaces:**
- Consumes: `Frame` (Task 1), `Cache.State()`.
- Produces: `Hub.Subscribe() (id uint64, ch <-chan Frame)`; `Hub.Unsubscribe(id uint64)`; `Hub.Broadcast(name string, v any)`; `GET /api/events` writes SSE (`event: <name>\ndata: <json>\n\n`), comment heartbeat every 15 s, and on every connect replays current `player` then `state` (Review Focus #3).

- [ ] **Step 1: Failing tests**

`internal/server/sse_test.go`:
```go
package server

import (
	"bufio"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cliamp-web/internal/snapshot"
)

func TestSSEFanOutToTwoClients(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth = NewAuth("t")
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	read := func(connURL string) *httptest.ResponseRecorder { return nil } // unused; see manual clients below
	_ = read

	c1 := sseConnect(t, srv.URL, "t")
	c2 := sseConnect(t, srv.URL, "t")
	cfg.Hub.Broadcast("state", map[string]any{"state": "playing"})
	for _, c := range []*sseClient{c1, c2} {
		f := c.next(2 * time.Second)
		if f.name != "state" || !strings.Contains(f.data, `"playing"`) {
			t.Fatalf("client got %q %q", f.name, f.data)
		}
	}
}

func TestSSECachesReplayCurrentPlayerAndStateOnConnect(t *testing.T) { // Review Focus #3
	cfg := testConfig(t)
	cfg.Auth = NewAuth("t")
	cfg.Cache.SetOnline(true)
	cfg.Cache.SetState([]byte(`{"state":"playing","revision":3}`))
	h := New(cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := sseConnect(t, srv.URL, "t")
	f1 := c.next(2 * time.Second)
	f2 := c.next(2 * time.Second)
	if f1.name != "player" || !strings.Contains(f1.data, "online") {
		t.Fatalf("first replay: %+v", f1)
	}
	if f2.name != "state" || !strings.Contains(f2.data, `"revision":3`) {
		t.Fatalf("second replay: %+v", f2)
	}
}
```
Plus a small test helper `sseConnect`/`sseClient` (in the same file) that does a plain `http.Get` with `Accept: text/event-stream`, streams with `bufio.Scanner`, parses `event:`/`data:` pairs, and skips comment heartbeats; `next(d)` fails the test on timeout.

- [ ] **Step 2: Run — fail.**
- [ ] **Step 3: Implement hub + events handler**

`internal/server/hub.go` (replace):
```go
package server

import (
	"encoding/json"
	"sync"
)

type Frame struct{ Name, Data string }

type Hub struct {
	mu   sync.Mutex
	next uint64
	c    map[uint64]chan Frame
}

func NewHub() *Hub { return &Hub{c: map[uint64]chan Frame{}} }

func (h *Hub) Subscribe() (uint64, <-chan Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	ch := make(chan Frame, 64)
	h.c[h.next] = ch
	return h.next, ch
}

func (h *Hub) Unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.c[id]; ok {
		close(ch)
		delete(h.c, id)
	}
}

func (h *Hub) Broadcast(name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.c {
		select {
		case ch <- Frame{Name: name, Data: string(b)}:
		default: // slow client: drop frame; browser recovers via resync
		}
	}
}
```

`internal/server/handlers.go` additions:
```go
func (s *serverState) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	id, ch := s.cfg.Hub.Subscribe()
	defer s.cfg.Hub.Unsubscribe(id)

	// replay current status so a fresh (re)connection is consistent
	status := "offline"
	if s.cfg.Cache.Online() {
		status = "online"
	}
	fmt.Fprintf(w, "event: player\ndata: %s\n\n", `{"status":"`+status+`"}`)
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", s.cfg.Cache.State())
	flusher.Flush()

	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case f, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", f.Name, f.Data)
			flusher.Flush()
		}
	}
}
```
Register in `New`: `mux.Handle("GET /api/events", s.protect(http.HandlerFunc(s.events)))` (auth via query token — EventSource cannot send headers).
- [ ] **Step 4: tests green. Commit** — `"feat: SSE hub and events endpoint with connect replay"`

---

### Task 7: `/api/command`, error mapping, rate limiter, state/queue/capabilities GETs

**Files:**
- Create: `internal/server/limiter.go`
- Modify: `internal/server/handlers.go`, `internal/server/server.go`
- Test: `internal/server/handlers_test.go` (extend)

**Interfaces:**
- Consumes: `ipc.OpRunner`, `ipc.IPCError`, `Cache.Queue()/Caps()`.
- Produces: `POST /api/command {op,params}` → `{job:{state, result?, snapshot?, error?}}`, HTTP via `statusForCode`: invalid_params→400, conflict→409, not_found/unknown_operation→404, unavailable→503, internal_error→500; `GET /api/state`, `GET /api/queue` (Cache.Queue JSON), `GET /api/capabilities`; `NewLimiter(rate float64, burst int, now func() time.Time) *Limiter`, `(*Limiter).Allow(key string) bool`.

- [ ] **Step 1: Failing tests** — extend `handlers_test.go`:

```go
type fakeRunner struct {
	lastOp     string
	lastParams map[string]any
	job        *ipc.Job
	err        error
}

func (f *fakeRunner) Call(ctx context.Context, m string, p any) (*ipc.Response, error) {
	return &ipc.Response{OK: true}, nil
}
func (f *fakeRunner) SubmitJob(ctx context.Context, op string, p any) (*ipc.Job, error) {
	f.lastOp, f.lastParams = op, p.(map[string]any)
	if f.err != nil {
		return f.job, f.err
	}
	return f.job, nil
}

func commandConfig(t *testing.T, fr *fakeRunner) Config {
	cfg := testConfig(t)
	cfg.Auth = NewAuth("t")
	cfg.Runner = fr
	cfg.Limiter = NewLimiter(20, 40, time.Now)
	return cfg
}

func postCommand(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/command", strings.NewReader(body))
	r.Header.Set("X-Token", "t")
	h.ServeHTTP(rec, r)
	return rec
}

func TestCommandSuccessReturnsJob(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{ID: "j", State: "succeeded", Snapshot: json.RawMessage(`{"revision":9}`)}}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"op":"queue.jump","params":{"index":4,"if_revision":7}}`)
	if rec.Code != 200 {
		t.Fatalf("got %d body %s", rec.Code, rec.Body)
	}
	if fr.lastOp != "queue.jump" || fr.lastParams["if_revision"] != float64(7) {
		t.Fatalf("params passthrough broken: %+v", fr.lastParams)
	}
}

func TestCommandConflictMapsTo409(t *testing.T) { // Review Focus #4
	fr := &fakeRunner{job: &ipc.Job{State: "failed"}, err: &ipc.IPCError{Code: "conflict", Detail: "stale"}}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"op":"queue.move","params":{"index":1,"to":0,"if_revision":3}}`)
	if rec.Code != 409 {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "conflict") {
		t.Fatalf("body must name code: %s", rec.Body)
	}
}

func TestCommandUnavailableMapsTo503(t *testing.T) {
	fr := &fakeRunner{err: ipc.ErrNotConnected}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"op":"toggle"}`)
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

func TestRateLimit(t *testing.T) {
	now := time.Now
	l := NewLimiter(2, 3, func() time.Time { return now() })
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("burst should allow 3, rejected at %d", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("4th immediate must be rejected")
	}
	now = func() time.Time { return now().Add(time.Second) } // 2 more allowed
	if !l.Allow("1.2.3.4") {
		t.Fatal("after 1s refill want allow")
	}
}

func TestQueueGETServesCache(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth = NewAuth("t")
	cfg.Cache.AbsorbQueueResult(json.RawMessage(`{"tracks":[{"path":"a"}]}`), 5)
	h := New(cfg)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/queue", nil)
	r.Header.Set("X-Token", "t")
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"playlist_revision":5`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
```

- [ ] **Step 2: Run — fail.**
- [ ] **Step 3: Implement**

`internal/server/limiter.go`:
```go
package server

import (
	"sync"
	"time"
)

type bucket struct{ tokens float64; last time.Time }

type Limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  int
	now    func() time.Time
	m      map[string]*bucket
}

func NewLimiter(rate float64, burst int, now func() time.Time) *Limiter {
	return &Limiter{rate: rate, burst: burst, now: now, m: map[string]*bucket{}}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.m[key]
	if !ok {
		b = &bucket{tokens: float64(l.burst), last: l.now()}
		l.m[key] = b
	}
	b.tokens += l.rate * l.now().Sub(b.last).Seconds()
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.last = l.now()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
```

`handlers.go` additions:
```go
func statusForCode(code string) int {
	switch code {
	case "invalid_params":
		return 400
	case "conflict":
		return 409
	case "not_found", "unknown_operation":
		return 404
	case "unavailable":
		return 503
	default:
		return 500
	}
}

func (s *serverState) command(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Limiter.Allow(r.RemoteAddr) {
		writeJSON(w, 429, map[string]any{"error": "rate_limited", "code": "rate_limited"})
		return
	}
	var req struct {
		Op     string         `json:"op"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Op == "" {
		writeJSON(w, 400, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	job, err := s.cfg.Runner.SubmitJob(ctx, req.Op, req.Params)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		code := "internal_error"
		var ie *ipc.IPCError
		if errors.As(err, &ie) {
			code = ie.Code
		} else if errors.Is(err, ipc.ErrNotConnected) {
			code = "unavailable"
		}
		writeJSON(w, statusForCode(code), map[string]any{"error": err.Error(), "code": code})
		return
	}
	writeJSON(w, 200, map[string]any{"job": job})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *serverState) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.Cache.State())
}

func (s *serverState) queue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.Cache.Queue())
}

func (s *serverState) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.Cache.Caps())
}
```

`New()` final route table (replace Task 1/5/6 registrations):
```go
mux.HandleFunc("GET /api/health", s.health)
mux.Handle("GET /api/state", s.protect(http.HandlerFunc(s.state)))
mux.Handle("GET /api/queue", s.protect(http.HandlerFunc(s.queue)))
mux.Handle("GET /api/capabilities", s.protect(http.HandlerFunc(s.capabilities)))
mux.Handle("GET /api/events", s.protect(http.HandlerFunc(s.events)))
mux.Handle("POST /api/command", s.protect(http.HandlerFunc(s.command)))
mux.Handle("/", s.protectStatic(s.cfg.Static)) // SPA: index.html fallback
```
`protectStatic` serves embedded files **without** token (assets carry no state; the token only guards data endpoints).
- [ ] **Step 4: tests green. Commit** — `"feat: command endpoint, error mapping, rate limit, data GETs"`

---

### Task 8: `main.go` wiring — subscriber→cache→hub, queue prefetch, status.json, flags

**Files:**
- Modify: `cmd/cliamp-web/main.go`
- Test: `cmd/cliamp-web/wiring_test.go` (export `run(cfg runConfig) error` so it is testable)

**Interfaces:**
- Consumes: everything above.
- Produces: flags `--port 8730 --bind 127.0.0.1 --lan --data-dir ~/.local/share/cliamp/web --socket ~/.config/cliamp/cliamp.sock --allowed-hosts "" --verbose`; `<data-dir>/status.json` `{url,token,port,pid,started_at}` mode 0600; `run(ctx, runConfig) error`; queue prefetch on connect and on every `playlist` signal; SSE `resync` broadcast after each (re)connect.

- [ ] **Step 1: Failing wiring test**

`cmd/cliamp-web/wiring_test.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestRunWiresFakePlayer(t *testing.T) {
	f := startFakePlayer(t) // helper below: unix socket answering V2 subscribe/state.get/capabilities/queue.list/playnext.list
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	go func() {
		if err := run(ctx, runConfig{Socket: f.path, Port: freePort(t), DataDir: dir, Bind: "127.0.0.1"}); err != nil {
			t.Error(err)
		}
	}()
	base := "http://127.0.0.1:" + portOf(dir)
	tok := waitForToken(t, dir)
	resp := getJSON(t, base+"/api/state", tok)
	if resp["state"] != "playing" {
		t.Fatalf("state not prefetched: %v", resp)
	}
	q := getJSON(t, base+"/api/queue", tok)
	if _, ok := q["tracks"]; !ok {
		t.Fatalf("queue not prefetched: %v", q)
	}
	st := readStatus(t, dir)
	if st["port"] == nil || st["token"] != tok {
		t.Fatalf("status.json wrong: %v", st)
	}
	cancel()
	time.Sleep(200 * time.Millisecond)
	_ = http.DefaultClient // (kept so import is used; drop if unused)
}
```
Helpers `startFakePlayer` (reuse the `newFakeIPC` handler style over a unix socket with canned replies for `subscribe`, `state.get`, `capabilities`, `operation.submit` for `queue.list`/`playnext.list` returning terminal jobs), `freePort` (listen :0, close, return port), `waitForToken` (poll `LoadOrCreateToken`-style file read), `readStatus`, `getJSON` (with `X-Token`) live in the same file — write them concretely, each ≤20 lines.

- [ ] **Step 2: Run — fail (undefined `run`).**
- [ ] **Step 3: Implement** — rewrite `main.go`:

```go
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cliamp-web/internal/ipc"
	"cliamp-web/internal/snapshot"
	"cliamp-web/internal/server"
)

//go:embed webui/dist
var webui embed.FS

type runConfig struct {
	Socket       string
	Port         int
	Bind         string
	DataDir      string
	AllowedHosts []string
	Verbose      bool
}

func run(ctx context.Context, rc runConfig) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if !rc.Verbose {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	tok, err := server.LoadOrCreateToken(rc.DataDir)
	if err != nil {
		return err
	}
	cache := snapshot.NewCache()
	hub := server.NewHub()
	conn := ipc.NewConn(rc.Socket)
	defer conn.Close()

	refresh := func() {
		if resp, err := conn.Call(ctx, "state.get", nil); err == nil && len(resp.Snapshot) > 0 {
			cache.SetState(resp.Snapshot)
		} else if err != nil {
			logger.Warn("state.get", "err", err)
		}
		if caps, err := conn.Call(ctx, "capabilities", nil); err == nil {
			cache.SetCaps(caps.Raw)
		}
		for _, op := range []string{"queue.list", "playnext.list"} {
			job, err := conn.SubmitJob(ctx, op, nil)
			if err != nil {
				logger.Warn(op, "err", err)
				continue
			}
			if op == "queue.list" {
				cache.AbsorbQueueResult(job.Result, cache.PlaylistRevision())
			} else {
				cache.AbsorbPlayNextResult(job.Result, cache.PlaylistRevision())
			}
		}
	}

	sub := ipc.NewSubscriber(rc.Socket,
		[]string{"runtime.state", "runtime.playback", "runtime.playlist", "runtime.settings", "runtime.job"},
		func(ev ipc.Event) {
			switch cache.Apply(ev) {
			case "state", "settings":
				hub.Broadcast("state", cache.State())
			case "playback":
				hub.Broadcast("playback", ev.Data)
			case "playlist":
				hub.Broadcast("playlist", ev.Data)
				go refresh()
			case "job":
				hub.Broadcast("job", ev.Data)
			}
		},
		func(on bool) {
			cache.SetOnline(on)
			status := "offline"
			if on {
				status = "online"
			}
			hub.Broadcast("player", map[string]string{"status": status})
			if on {
				refresh()
				hub.Broadcast("resync", map[string]string{})
			}
		})
	go sub.Run(ctx)

	static, err := fs.Sub(webui, "webui/dist")
	if err != nil {
		return err
	}
	h := server.New(server.Config{
		Cache: cache, Runner: conn, Hub: hub,
		Auth: server.NewAuth(tok), Guard: server.NewHostGuard(rc.AllowedHosts),
		Limiter: server.NewLimiter(20, 40, time.Now), Static: static, Logger: logger,
	})
	addr := fmt.Sprintf("%s:%d", rc.Bind, rc.Port)
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}

	if err := writeStatus(rc.DataDir, tok, rc.Port, addr); err != nil {
		logger.Warn("status.json", "err", err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
		return nil
	case err := <-errCh:
		return err
	}
}

func writeStatus(dir, tok string, port int, addr string) error {
	b, _ := json.Marshal(map[string]any{
		"url":         "http://" + addr + "/?token=" + tok,
		"token":       tok,
		"port":        port,
		"pid":         os.Getpid(),
		"started_at":  time.Now().UTC().Format(time.RFC3339),
	})
	tmp := dir + "/status.json.tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dir+"/status.json")
}

func main() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	rc := runConfig{}
	fs.IntVar(&rc.Port, "port", 8730, "HTTP listen port")
	fs.StringVar(&rc.Bind, "bind", "127.0.0.1", "bind address")
	lan := fs.Bool("lan", false, "bind 0.0.0.0 (LAN-visible)")
	fs.StringVar(&rc.Socket, "socket", defaultSocket(), "cliamp IPC socket path")
	fs.StringVar(&rc.DataDir, "data-dir", defaultDataDir(), "bridge data directory")
	hosts := fs.String("allowed-hosts", "", "comma-separated extra allowed Host values")
	fs.BoolVar(&rc.Verbose, "verbose", false, "verbose logging")
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: cliamp-web serve [flags]")
		os.Exit(2)
	}
	fs.Parse(os.Args[2:])
	if *lan {
		rc.Bind = "0.0.0.0"
	}
	rc.AllowedHosts = strings.Split(*hosts, ",")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, rc); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func defaultSocket() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d + "/cliamp/cliamp.sock"
	}
	return os.Getenv("HOME") + "/.config/cliamp/cliamp.sock"
}
```

- [ ] **Step 4: `make test` green incl. wiring test. Manual smoke:** `./dist/cliamp-web serve --port 8731 --verbose &` → `curl localhost:8731/api/health` → `player: offline`; open `http://localhost:8731/` → placeholder page; status.json exists with 0600. Kill.
- [ ] **Step 5: Commit** — `"feat: bridge wiring, flags, status.json"`

---

### Task 9: Web UI scaffold (Vite + Vue + TS + Reka UI), embedded, live mirror

**Files:**
- Create: `webui/package.json`, `webui/vite.config.ts`, `webui/tsconfig.json`, `webui/index.html`, `webui/src/main.ts`, `webui/src/App.vue`, `webui/src/api.ts`, `webui/src/store.ts`, `webui/src/style.css`
- Modify: Makefile (`ui` target), replace `webui/dist/index.html` placeholder with the real build output committed.

**Interfaces:**
- Consumes: REST/SSE contract (Tasks 5-8).
- Produces: `store` reactive singleton `{online, snapshot, queue, caps}`; `refreshState()`, `refreshQueue()`, `command(op, params)` (adds `if_revision` automatically for queue/playnext mutations), `connectEvents(onResync)`, `token` (from `?token=` once, then `sessionStorage`).

- [ ] **Step 1: Scaffold + pin deps**

`webui/package.json`:
```json
{
  "name": "cliamp-webui",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vue-tsc --noEmit && vite build",
    "typecheck": "vue-tsc --noEmit"
  },
  "dependencies": {
    "reka-ui": "^2.6.0",
    "vue": "^3.5.13"
  },
  "devDependencies": {
    "@vitejs/plugin-vue": "^5.2.1",
    "typescript": "~5.7.2",
    "vite": "^6.0.5",
    "vue-tsc": "^2.2.0"
  }
}
```
`npm install` in `webui/`, commit `package-lock.json` (CI uses `npm ci`).

`webui/vite.config.ts`:
```ts
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  server: { proxy: { "/api": "http://localhost:8730" } },
  build: { target: "es2022" },
});
```
`tsconfig.json`: standard `vite/vue-ts` app config (`"strict": true`, `"moduleResolution": "bundler"`, include `src`).

- [ ] **Step 2: Store + API (full code)**

`webui/src/api.ts`:
```ts
export const token =
  sessionStorage.getItem("cliamp-token") ||
  new URLSearchParams(location.search).get("token") ||
  "";
if (token) sessionStorage.setItem("cliamp-token", token);
if (location.search) history.replaceState(null, "", location.pathname);

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const r = await fetch(path, {
    ...init,
    headers: { ...(init.headers || {}), "X-Token": token },
  });
  if (!r.ok) {
    throw Object.assign(new Error("http " + r.status), {
      status: r.status,
      body: await r.json().catch(() => ({})),
    });
  }
  return r.json();
}

export const getState = () => req<Record<string, any>>("/api/state");
export const getQueue = () => req<Queue>("/api/queue");
export const getCaps = () => req<{ operations?: string[] }>("/api/capabilities");

export interface Track {
  path: string;
  title?: string;
  artist?: string;
  album?: string;
  duration?: number;
  stream?: boolean;
  queued?: boolean;
  index?: number;
}
export interface Queue {
  tracks: Track[];
  play_next: Track[];
  playlist_revision: number;
  truncated: boolean;
  count: number;
}
const MUTATES = new Set([
  "queue.jump", "queue.remove", "queue.move", "queue.clear",
  "playnext.remove", "playnext.move", "playnext.clear",
]);

export function command(op: string, params: Record<string, unknown> = {}) {
  return req<{ job: { state: string; snapshot?: Record<string, unknown> } }>(
    "/api/command",
    { method: "POST", body: JSON.stringify({ op, params }) }
  );
}
```
NOTE for implementer: after `command` resolves, `store.applyCommandResult` refetches queue when the op mutates (`MUTATES`) and 409s.

`webui/src/store.ts`:
```ts
import { reactive } from "vue";
import * as api from "./api";

export const store = reactive({
  online: true,
  snapshot: {} as Record<string, any>,
  queue: { tracks: [], play_next: [], playlist_revision: 0, truncated: false, count: 0 } as api.Queue,
  caps: new Set<string>(),
});

export async function refreshState() { store.snapshot = await api.getState(); }
export async function refreshQueue() { store.queue = await api.getQueue(); }
export async function refreshCaps() {
  const c = await api.getCaps();
  store.caps = new Set(c.operations ?? []);
}

export async function runCommand(op: string, params: Record<string, unknown> = {}) {
  try {
    const r = await api.command(op, params);
    if (r.job?.snapshot) Object.assign(store.snapshot, r.job.snapshot);
  } catch (e: any) {
    if (e.status === 409) await refreshQueue(); // Review Focus #4: snap rows back
    else console.warn("command failed", op, e);
  }
}

export function withRevision(op: string, params: Record<string, unknown> = {}) {
  return runCommand(op, { ...params, if_revision: store.queue.playlist_revision });
}

export function connectEvents(): EventSource {
  const es = new EventSource(`/api/events?token=${encodeURIComponent(api.token)}`);
  es.addEventListener("state", (e) => (store.snapshot = JSON.parse(e.data)));
  es.addEventListener("playback", (e) => Object.assign(store.snapshot, JSON.parse(e.data)));
  es.addEventListener("playlist", () => refreshQueue());
  es.addEventListener("resync", () => {
    refreshState();
    refreshQueue();
  });
  es.addEventListener("player", (e) => (store.online = JSON.parse(e.data).status === "online"));
  return es;
}
```

- [ ] **Step 3: Minimal App shell** — `App.vue` shows online pill + snapshot title (real panels land Tasks 10-12); `main.ts` bootstraps: `refreshState(); refreshQueue(); refreshCaps(); connectEvents();`
- [ ] **Step 4: Build + embed:** `npm run build` in `webui/` (output must land in `webui/dist` — set `outDir` accordingly in vite config; override default), commit `webui/dist/*` (build artifacts under the structure note), `make build`, then run `./dist/cliamp-web serve --port 8731 --verbose` and open `http://127.0.0.1:8731/` with a fake-player stub running (`go run ./cmd/... ` against the wiring test's fake socket is enough, or run against real cliamp if installed). Expected: page loads, health online, title reflects fake `state.get` snapshot.
- [ ] **Step 5: Commit** — `"feat: webui scaffold with live state mirror"`

---

### Task 10: Now-playing + transport panel

**Files:**
- Create: `webui/src/components/NowPlaying.vue`, `webui/src/components/TransportBar.vue`, `webui/src/lib/format.ts`
- Modify: `App.vue`

**Interfaces:** Consumes `store`, `runCommand`, `withRevision`. Produces nothing later tasks depend on.

- [ ] **Step 1: Implement.** Full components: `NowPlaying` renders title/artist/album from `store.snapshot.track`, Reka `Slider` scrubber: `model-value = position`, `max = duration`, drag sets local `dragPos` and **commits only on `@value-commit`** → `runCommand("seek.absolute", {position})`. When `duration === 0`: scrubber `disabled`, bar shown indeterminate, **seek never sent (Review Focus #2)**. `TransportBar`: buttons prev/next/stop (`runCommand "prev" "next" "stop"`), play/pause (`"toggle"`), Reka `Switch` for shuffle (`{name: on/off}` from `store.snapshot.shuffle`), cycle button for repeat showing `store.snapshot.repeat_mode` (`runCommand("repeat",{name:"cycle"})` — Switch can't model 3 states; cycle button replaces the spec's "switch pair"), Reka `Slider` volume: `min = store.snapshot.volume_min ?? -40`, `max = 6`, commit → `runCommand("volume",{db})`.
- [ ] **Step 2: Live drift:** a `setInterval(1s)` in `NowPlaying` advances `position` locally between SSE `playback` events while `state === "playing"` (smooth bar; SSE corrects).
- [ ] **Step 3: `npm run build` passes; manual smoke with fake player:** toggle play → fake `capabilities` list must contain ops (fake from Task 8 wiring test — reuse it via `go test` binary, or run against real cliamp). Buttons disable while `!store.online`.
- [ ] **Step 4: Commit** — `"feat: now-playing and transport UI"`

---

### Task 11: Queue + play-next panel

**Files:**
- Create: `webui/src/components/QueueList.vue`, `webui/src/components/PlayNextList.vue`
- Modify: `App.vue`

**Interfaces:** Consumes `store`, `withRevision`, `runCommand`.

- [ ] **Step 1: Implement.** QueueList: `v-for` over `store.queue.tracks` (≤5000 rows, plain render per spec); row click → `withRevision("queue.jump",{index})`; current row (`store.snapshot.index === i`) highlighted; inline buttons remove (`queue.remove`), up/down (`queue.move` with `{index,to}`); when `store.snapshot.shuffle` truthy, move buttons `disabled` with Reka `Tooltip` "Reorder is disabled while shuffle is on"; `store.queue.truncated` → footer "… 5000 of {count} shown"; `queue.clear` button in panel header (confirm via `window.confirm`). PlayNextList: Reka `Collapsible`, rows with remove (`playnext.remove {index}` — index into `play_next`, documented in component comment), header clear (`playnext.clear`).
- [ ] **Step 2: Conflict behavior:** after any `withRevision` 409, `runCommand` refetches queue → rows snap back (already in store; verify by stubbing the fake player to answer `conflict` once in the manual smoke).
- [ ] **Step 3: `npm run build` + manual smoke. Commit** — `"feat: queue and play-next UI"`

---

### Task 12: Status/reconnect UX

**Files:**
- Create: `webui/src/components/StatusPill.vue`, `webui/src/components/Toasts.vue`
- Modify: `App.vue`, `store.ts` (toast queue — plain array, no lib)

**Interfaces:** Consumes SSE events; produces visible offline banner + toasts.

- [ ] **Step 1: Implement.** `StatusPill`: green/gray dot from `store.online`; banner strip when `!store.online` ("player offline"). Toasts: `store.notices: string[]`; pushed on `resync` ("state resynced"), `player` offline/online, 409 ("playlist changed elsewhere"). Render via Reka `Toast` primitives, auto-dismiss 4 s.
- [ ] **Step 2: Manual smoke:** kill the bridge's fake player socket → pill grays + toast; restart it → pill greens + `resync` refetch updates title without reload. Also: fresh browser tab with full `?token=` URL works; reload after `history.replaceState` keeps session via `sessionStorage`.
- [ ] **Step 3: Build green. Commit** — `"feat: connection status, toasts"`

---

### Task 13: `web.lua` plugin

**Files:**
- Create: `web.lua`

**Interfaces:** Consumes: `cliamp.exec.run` (binary `cliamp-web`), `status.json` from Task 8; produces: `cliamp plugins call web url|status`.

- [ ] **Step 1: Implement `web.lua`** (full file):

```lua
-- cliamp-plugin-web: LAN web remote. Spawns and supervises cliamp-web.
local p = plugin.register({
    name        = "web",
    type        = "hook",
    version     = "1.0.0",
    description = "LAN web interface (spawns the cliamp-web bridge)",
    permissions = {"exec"},
})

local port  = tostring(p:config("port") or "8730")
local lan   = p:config("lan")
local hosts = p:config("allowed_hosts")
local bin   = p:config("binary") or "cliamp-web"

local datadir = os.getenv("HOME") .. "/.local/share/cliamp/web"
local restarts, handle, quitting = 0, nil, false

local function arglist()
    local args = {"serve", "--port", port}
    if lan then args[#args + 1] = "--lan" end
    if hosts then
        args[#args + 1] = "--allowed-hosts"
        args[#args + 1] = tostring(hosts)
    end
    return args
end

local function read_status()
    local raw = cliamp.fs.read(datadir .. "/status.json")
    if not raw then return nil end
    return cliamp.json.decode(raw)
end

local function start()
    if quitting then return end
    local h, err = cliamp.exec.run(bin, arglist(), {
        on_stdout = function(line) cliamp.log.info("bridge: " .. line) end,
        on_stderr = function(line) cliamp.log.info("bridge: " .. line) end,
        on_exit = function(code)
            handle = nil
            if quitting or code == -1 then return end
            if restarts < 3 then
                restarts = restarts + 1
                cliamp.log.warn(string.format(
                    "bridge exited (%s); restart %d/3", tostring(code), restarts))
                cliamp.timer.after(2, start)
            else
                cliamp.log.error("bridge keeps failing; giving up this session")
                cliamp.message("web: bridge died, giving up")
            end
        end,
    })
    if not h then
        cliamp.log.error("cannot start '" .. bin .. "'; install it and add it to [plugins] allowed_binaries")
    else
        handle = h
    end
end

p:on("app.start", function()
    start()
    cliamp.timer.after(1.5, function()
        local st = read_status()
        if st and st.url then
            -- token deliberately NOT shown in the TUI: get it via plugins call
            cliamp.message("web remote: " .. st.url:gsub("%?token=[^&]*", "?token=…"), 10)
        end
    end)
end)

p:on("app.quit", function()
    quitting = true
end)

p:command("url", function()
    local st = read_status()
    if not st then return "bridge not up (check plugins.log)" end
    return st.url
end)

p:command("status", function()
    local st = read_status()
    if not st then return "offline" end
    return string.format("up :%s pid=%s restarts=%d", st.port, tostring(st.pid), restarts)
end)
```

- [ ] **Step 2: Syntax check** (skip if no Lua interpreter locally): `luajit -bl web.lua /dev/null || lua -e 'assert(loadfile("web.lua"))'`.
- [ ] **Step 3: Manual smoke against real cliamp** (documented in README too):
  1. `make build && sudo cp dist/cliamp-web /usr/local/bin/` (or PKGBUILD from Task 14)
  2. `cliamp plugins install <user>/cliamp-plugin-web`
  3. `config.toml`: `[plugins] allowed_binaries = "cliamp-web"`
  4. `cliamp plugins trust web`, start cliamp with a playlist; expect status-bar message with URL.
  5. `cliamp plugins call web url` → full URL with token.
  6. From a second host: open URL; play/queue ops reflect in TUI within ~1 s.
  7. `kill $(jq -r .pid ~/.local/share/cliamp/web/status.json)` → cliamp message on giving-up after 3 restarts; kill cliamp → no orphan `cliamp-web` process (`pgrep -a cliamp-web` empty).
- [ ] **Step 4: Commit** — `"feat: web.lua plugin launcher and supervisor"`

---

### Task 14: Docs, packaging, CI

**Files:**
- Create: `README.md`, `PKGBUILD`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- Modify: `Makefile`

**Interfaces:** Consumes everything; produces user-facing install path (spec §10's four steps) and reproducible releases.

- [ ] **Step 1: README** — sections: what it is; the four install steps verbatim from spec §10; `[plugins.web]` config table (`port`, `lan`, `allowed_hosts`, `binary`); security notes (token, LAN opt-in, cleartext-on-LAN risk); **Caddy HTTPS recipe**:
```
cliamp.example.lan {
    reverse_proxy 127.0.0.1:8730
}
```
plus `--allowed-hosts cliamp.example.lan`; headless recipe `cliamp --daemon --auto-play --playlist <name>` + the plugin; dev recipe (`make ui`, `vite` dev proxy).
- [ ] **Step 2: CI** `.github/workflows/ci.yml`:
```yaml
name: ci
on: [push, pull_request]
jobs:
  go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.23" }
      - run: go vet ./... && go test ./...
  webui:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: 22 }
      - run: npm ci && npm run build
        working-directory: webui
```
- [ ] **Step 3: Release** `.github/workflows/release.yml` (tag `v*` → build `linux/amd64` + `linux/arm64`, sha256sums, upload to release; `CGO_ENABLED=0`).
- [ ] **Step 4: PKGBUILD** (source build; deps go, nodejs, npm):
```bash
# Maintainer: <you>
pkgname=cliamp-web
pkgver=1.0.0
pkgrel=1
pkgdesc="LAN web remote bridge for cliamp"
arch=(x86_64 aarch64)
license=(MIT)
makedepends=(go nodejs npm)
source=("git+https://github.com/<user>/cliamp-plugin-web#tag=v$pkgver")
sha256sums=("SKIP")
build() {
  cd cliamp-plugin-web
  make ui
  make build
}
package() {
  install -Dm755 cliamp-plugin-web/dist/cliamp-web "$pkgdir/usr/bin/cliamp-web"
  install -Dm644 cliamp-plugin-web/LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
}
```
Add `LICENSE` (MIT, your name) and `Makefile` `ui:` target (`cd webui && npm ci && npm run build`).
- [ ] **Step 5: Push; CI green on `main`. Commit** — `"feat: docs, CI, packaging"`

---

## Self-Review Record (author, at plan-writing time)

1. **Spec coverage:** sandbox constraint (§2.1) → plugin launches external process (T13); IPC V2 usage (§2.2) → T2/T3; snapshot+revisions → T4; all v1 features (§3) → T10/T11; security (§6) → T5/T7; error handling (§7) → T3 (reconnect), T4/T6 (resync replay), T7 (code mapping); config (§8) → T8 flags + T13 plugin passthrough; testing (§9) → every Go task TDD, plugin/UI manual checklists; packaging/install (§10) → T14. `/api/capabilities` endpoint: T5+T7. No gaps.
2. **Placeholders:** none. Two defects found and fixed during this review: an editorial artifact in Task 1's test-file preamble, and a malformed wait block in Task 2's `Call` (now bounded by a 3 s child context).
3. **Types:** `OpRunner{Call,SubmitJob}` identical in T1/T2/T3/T7; `Queue` JSON tags identical T4/T7/T9; `Frame` identical T1/T6; `AbsorbQueueResult/AbsorbPlayNextResult` names identical T4/T7/T8; `withRevision` only from T9 onward.
4. **Review Focus:** all five pinned — #1 T2 `TestGarbageLinesSkipped` + T3 subscriber skip; #2 T10 disabled scrubber; #3 T6 `TestSSECachesReplayCurrentPlayerAndStateOnConnect`; #4 T7 `TestCommandConflictMapsTo409` + T11 snap-back; #5 T5 `TestPOSTNeverAcceptsQueryToken`/`TestEmptyQueryTokenFails`.
