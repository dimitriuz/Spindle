// Package fakeplayer is a wire-accurate stand-in for a running cliamp: it
// answers the V2 methods the bridge uses so Go tests and browser smokes run
// without a real player. Shapes mirror live cliamp v2.0.1 probes:
// operation/job_id ride the envelope, empty list results are {"ok":true}.
package fakeplayer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

type Options struct {
	// State is the raw snapshot JSON for state.get.
	State string
	// Caps is the raw capabilities result array JSON.
	Caps string
	// Track is the raw track JSON embedded in the snapshot, "" for none.
	Track string
	// Lyrics is the raw lyrics result JSON for the lyrics op; "" ⇒ the
	// live "no lyrics found" failure.
	Lyrics string
}

type Fake struct {
	Path string

	mu       sync.Mutex
	ln       net.Listener
	rowTitle string
	subConn  net.Conn
	lyrics   string
}

func Start(dir string, opts Options) (*Fake, error) {
	if opts.State == "" {
		opts.State = `{"state":"playing","revision":5,"playlist_revision":7}`
	}
	if opts.Caps == "" {
		opts.Caps = `[{"name":"toggle"},{"name":"queue.list"}]`
	}
	f := &Fake{rowTitle: "Row A", lyrics: opts.Lyrics}
	path := dir + "/s"
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	f.Path = path
	f.ln = ln
	go f.serve(opts)
	return f, nil
}

func (f *Fake) Close() { f.ln.Close() }

func (f *Fake) SetRow(title string) {
	f.mu.Lock()
	f.rowTitle = title
	f.mu.Unlock()
}

func (f *Fake) row() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rowTitle
}

// Push sends one raw event line to the most recent subscriber.
func (f *Fake) Push(eventJSON string) {
	f.mu.Lock()
	c := f.subConn
	f.mu.Unlock()
	if c != nil {
		fmt.Fprintln(c, eventJSON)
	}
}

func (f *Fake) serve(opts Options) {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c, opts)
	}
}

func (f *Fake) handle(c net.Conn, opts Options) {
	defer c.Close()
	sc := bufio.NewScanner(bufio.NewReader(c))
	for sc.Scan() {
		var req map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		var id, method, op string
		_ = json.Unmarshal(req["id"], &id)
		_ = json.Unmarshal(req["method"], &method)
		_ = json.Unmarshal(req["operation"], &op)
		switch method {
		case "subscribe":
			f.mu.Lock()
			f.subConn = c
			f.mu.Unlock()
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true}\n", id)
			fmt.Fprintf(c, "{\"event\":\"runtime.state\",\"seq\":1,\"retained\":true,\"data\":%s}\n", snapshot(opts))
		case "state.get":
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"snapshot\":%s}\n", id, snapshot(opts))
		case "capabilities":
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"result\":%s}\n", id, opts.Caps)
		case "operation.submit":
			// real cliamp validates the envelope: "operation" must be
			// top-level, a nested one is invalid_request.
			if op == "" {
				fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":false,\"error\":{\"code\":\"invalid_request\",\"message\":\"invalid request\"}}\n", id)
				break
			}
			f.submit(c, id, op, req["params"])
		default:
			fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true}\n", id)
		}
	}
}

func snapshot(opts Options) string {
	if opts.Track == "" {
		return opts.State
	}
	return trimBrace(opts.State) + `,"track":` + opts.Track + "}"
}

func trimBrace(s string) string {
	if len(s) > 0 && s[len(s)-1] == '}' {
		return s[:len(s)-1]
	}
	return s
}

func (f *Fake) submit(c net.Conn, id, op string, params json.RawMessage) {
	switch op {
	case "queue.list":
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j1\",\"operation\":\"queue.list\",\"state\":\"succeeded\",\"result\":{\"ok\":true,\"tracks\":[{\"title\":%q}]}}}\n", id, f.row())
	case "playnext.list":
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j2\",\"operation\":\"playnext.list\",\"state\":\"succeeded\",\"result\":{\"ok\":true}}}\n", id)
	case "make-change":
		// rows change BEFORE the event exists on the wire
		f.SetRow("Row B")
		f.Push(`{"event":"runtime.playlist","seq":9,"data":{"revision":8,"playlist_revision":8}}`)
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j3\",\"operation\":\"make-change\",\"state\":\"succeeded\",\"result\":{\"ok\":true}}}\n", id)
	case "lyrics":
		if f.lyrics == "" {
			failJob(c, id, op, "no lyrics found")
			return
		}
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"jl\",\"operation\":\"lyrics\",\"state\":\"succeeded\",\"result\":{\"ok\":true,\"lyrics\":%s}}}\n", id, f.lyrics)
	default:
		if f.providerOps(c, id, op, params) {
			return
		}
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j9\",\"operation\":%q,\"state\":\"succeeded\",\"result\":{\"ok\":true}}}\n", id, op)
	}
}
