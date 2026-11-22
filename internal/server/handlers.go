package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"spindle/internal/ipc"
)

// writeTimeout bounds one SSE write.
const writeTimeout = 10 * time.Second

// queue serves the cached live playlist.
func (s *serverState) queue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.Cache.Queue())
}

// capabilities serves the cached cliamp capabilities document.
func (s *serverState) capabilities(w http.ResponseWriter, r *http.Request) {
	caps := s.cfg.Cache.Caps()
	if len(caps) == 0 {
		caps = json.RawMessage(`{}`)
	}
	writeJSON(w, 200, caps)
}

func (s *serverState) health(w http.ResponseWriter, r *http.Request) {
	status := "offline"
	if s.cfg.Cache.Online() {
		status = "online"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "true", "player": status})
}

// writeJSON is the single JSON response path for every data endpoint.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, status = []byte(`{"error":"encode failed","code":"internal_error"}`), http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(urls.Scrub(b), '\n'))
}

// state serves the cached runtime snapshot (never proxies to cliamp).
func (s *serverState) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg.Cache.State())
}

// command forwards one operation to cliamp and waits for the job.
func (s *serverState) command(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(clientKey(r.RemoteAddr)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate_limited", "code": "rate_limited"})
		return
	}
	var req struct {
		Op     string         `json:"op"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Op == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	if !allowedOps[req.Op] {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "operation not allowed: " + req.Op, "code": "forbidden_operation"})
		return
	}
	if !settingsParamsOK(req.Op, req.Params) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	// the browser only ever sees scrubbed stream URLs; cliamp needs the real one
	switch t := req.Params["track"].(type) {
	case map[string]any: // the shape cliamp wants: a track object
		if p, ok := t["path"].(string); ok {
			t["path"] = urls.Restore(p)
		}
	case string:
		req.Params["track"] = urls.Restore(t)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	job, err := s.cfg.Runner.SubmitJob(ctx, req.Op, req.Params)
	if err != nil {
		code := "internal_error"
		var ie *ipc.IPCError
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			// the op may still be running inside cliamp: say so, with the job
			// when we have one, instead of reporting a plain failure
			out := map[string]any{"error": "job_timeout", "code": "job_timeout"}
			if job != nil {
				out["job"] = job
			}
			writeJSON(w, http.StatusGatewayTimeout, out)
			return
		case errors.As(err, &ie):
			code = ie.Code
		case errors.Is(err, ipc.ErrNotConnected):
			code = "unavailable"
		}
		writeJSON(w, statusForCode(code), map[string]any{"error": err.Error(), "code": code})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

// allowedOps is every operation the web UI issues. cliamp's capability list
// holds far more (plugin.call runs plugin Lua, url.load fetches arbitrary
// URLs, history.clear and playlist.delete destroy data); with no login in
// front of the bridge those stay unreachable from the network.
var allowedOps = map[string]bool{
	"play": true, "pause": true, "stop": true, "toggle": true, "next": true, "prev": true,
	"seek.absolute": true, "volume": true, "shuffle": true, "repeat": true,
	"track.play": true, "track.queue": true,
	"queue.play": true, "queue.remove": true, "queue.clear": true, "queue.move": true,
	"playnext.remove": true, "playnext.move": true,
	"provider.load": true, "provider.load_album": true,
	// Settings page: output device, EQ, mono, speed (theme and vis are not offered by cliamp's v2 API)
	"device": true, "eq": true, "mono": true, "speed": true,
}

// settingsParamsOK checks the parameters of the settings operations: a short
// name, an EQ band 0-9, and numbers within what cliamp accepts. Anything else
// is refused before it reaches the player.
func settingsParamsOK(op string, p map[string]any) bool {
	switch op {
	case "device", "eq", "mono", "speed":
	default:
		return true
	}
	for k, v := range p {
		switch k {
		case "name":
			n, ok := v.(string)
			if !ok || len(n) > 200 || strings.ContainsAny(n, "\x00\n\r") {
				return false
			}
		case "band":
			f, ok := v.(float64)
			if !ok || f < 0 || f > 9 || f != float64(int(f)) {
				return false
			}
		case "value":
			f, ok := v.(float64)
			if !ok || (op == "eq" && (f < -12 || f > 12)) || (op == "speed" && (f < 0.25 || f > 2)) || (op != "eq" && op != "speed") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func statusForCode(code string) int {
	switch code {
	case "invalid_params":
		return http.StatusBadRequest
	case "conflict":
		return http.StatusConflict
	case "not_found", "unknown_operation":
		return http.StatusNotFound
	case "unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// events is the SSE stream: replay of current player+state on connect,
// then hub frames, with comment heartbeats every 15 s.
func (s *serverState) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]any{"error": "streaming unsupported", "code": "internal_error"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // reverse proxies (nginx/openresty) must not buffer the stream
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	id, ch := s.cfg.Hub.Subscribe()
	defer s.cfg.Hub.Unsubscribe(id)

	status := "offline"
	if s.cfg.Cache.Online() {
		status = "online"
	}
	fmt.Fprintf(w, "event: player\ndata: {\"status\":%q}\n\n", status)
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", urls.Scrub(s.cfg.Cache.State()))
	flusher.Flush()

	hb := time.NewTicker(s.cfg.heartbeat())
	defer hb.Stop()
	rc := http.NewResponseController(w)
	// every write must finish promptly: a phone that vanished without a FIN
	// would otherwise pin this goroutine until TCP gives up
	arm := func() { _ = rc.SetWriteDeadline(time.Now().Add(writeTimeout)) }
	flush := func() bool {
		arm()
		if s.cfg.Hub.TakeDropped(id) {
			fmt.Fprint(w, "event: resync\ndata: {}\n\n")
		}
		return rc.Flush() == nil
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			arm()
			fmt.Fprint(w, "event: ping\ndata: {}\n\n")
			if !flush() {
				return
			}
		case f, open := <-ch:
			if !open {
				return
			}
			arm()
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", f.Name, f.Data)
			if !flush() {
				return
			}
		}
	}
}
