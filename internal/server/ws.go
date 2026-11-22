package server

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// ws is GET /api/ws: the live channel for small devices. It sends compact JSON
// text frames: status (now playing), queue, resync and ping. Commands do not
// travel here; they stay on POST /api/command.
func (s *serverState) ws(w http.ResponseWriter, r *http.Request) {
	// the library refuses a browser page from another host; a device sends no Origin
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	// CloseRead answers pings and closes, drops what the client sends, and
	// cancels ctx when the client goes away
	ctx := conn.CloseRead(r.Context())

	id, ch := s.cfg.Hub.Subscribe()
	defer s.cfg.Hub.Unsubscribe(id)

	send := func(frame []byte) bool {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, urls.Scrub(frame)) == nil
	}
	var lastStatus time.Time
	sendStatus := func() bool {
		lastStatus = time.Now()
		return send(buildStatus(s.cfg.Cache.State(), s.cfg.Cache.Online()))
	}
	sendQueue := func() bool { return send(buildQueueFrame(s.cfg.Cache.Queue())) }

	if !sendStatus() || !sendQueue() {
		return
	}

	hb := time.NewTicker(s.cfg.heartbeat())
	defer hb.Stop()
	gap := s.cfg.statusGap()
	var pending <-chan time.Time // set while a throttled status is waiting to go out
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if !send([]byte(`{"type":"ping"}`)) {
				return
			}
		case <-pending:
			pending = nil
			if !sendStatus() {
				return
			}
		case f, open := <-ch:
			if !open {
				return
			}
			ok := true
			switch f.Name {
			case "state", "settings", "player":
				pending = nil
				ok = sendStatus()
			case "playback":
				// position ticks are frequent; the latest one goes out at most once per gap
				if since := time.Since(lastStatus); since >= gap {
					pending = nil
					ok = sendStatus()
				} else if pending == nil {
					pending = time.After(gap - since)
				}
			case "playlist":
				ok = sendQueue()
			case "resync":
				ok = send([]byte(`{"type":"resync"}`))
			}
			if ok && s.cfg.Hub.TakeDropped(id) {
				ok = send([]byte(`{"type":"resync"}`))
			}
			if !ok {
				return
			}
		}
	}
}
