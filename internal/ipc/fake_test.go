package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
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
