package fakeplayer

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// Favorites as live cliamp v2.4.0 behaves (probed): both ops only TOGGLE;
// a favorited track appears in the Local provider's "Favorites" playlist; a
// favorited radio station gets an f:<url> row and a star on its catalog row;
// a station can only be favorited from its catalog row.

var fakeStations = map[string]string{"c:0": "MANGORADIO", "c:1": "Dance Wave", "c:2": "Radio Paradise"}

func (f *Fake) trackKey(t map[string]any) string {
	if meta, ok := t["provider_meta"].(map[string]any); ok {
		for k, v := range meta {
			if strings.HasSuffix(k, ".id") {
				return fmt.Sprint(v)
			}
		}
	}
	p, _ := t["path"].(string)
	return p
}

func (f *Fake) bookmark(params json.RawMessage) {
	var p struct {
		Track map[string]any `json:"track"`
	}
	if json.Unmarshal(params, &p) != nil || p.Track == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.trackKey(p.Track)
	for i, t := range f.favTracks {
		if f.trackKey(t) == key {
			f.favTracks = append(f.favTracks[:i], f.favTracks[i+1:]...)
			return
		}
	}
	f.favTracks = append(f.favTracks, p.Track)
}

// favoritesOp answers the favorites-related provider ops; false means "not mine".
func (f *Fake) favoritesOp(c net.Conn, id, op, provider, playlist string, offset, limit int, ok func(string), fail func(string)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case op == "provider.tracks" && provider == "local" && playlist == "Favorites":
		if limit <= 0 {
			limit = 50
		}
		rows := []map[string]any{}
		if offset < len(f.favTracks) {
			rows = f.favTracks[offset:min(offset+limit, len(f.favTracks))]
		}
		b, _ := json.Marshal(map[string]any{"ok": true, "total": len(f.favTracks), "tracks": rows})
		ok(string(b))
		return true
	case op == "provider.playlists" && provider == "radio":
		var rows []map[string]any
		for _, cid := range []string{"c:0", "c:1", "c:2"} {
			name := fakeStations[cid]
			if f.favStations[cid] {
				name = "★ " + name
			}
			rows = append(rows, map[string]any{"id": cid, "name": name, "provider": "radio", "favoritable": true})
		}
		for _, cid := range []string{"c:0", "c:1", "c:2"} {
			if f.favStations[cid] {
				rows = append(rows, map[string]any{"id": "f:http://stream.example/" + cid, "name": "★ " + fakeStations[cid], "provider": "radio", "favorite": true})
			}
		}
		b, _ := json.Marshal(map[string]any{"ok": true, "total": len(rows), "playlists": rows})
		ok(string(b))
		return true
	case op == "provider.favorite" && provider == "radio":
		if f.favStations == nil {
			f.favStations = map[string]bool{}
		}
		if cid, found := strings.CutPrefix(playlist, "f:http://stream.example/"); found {
			f.favStations[cid] = false
			ok(`{"ok":true}`)
			return true
		}
		if _, known := fakeStations[playlist]; !known {
			fail("unknown favorite station")
			return true
		}
		f.favStations[playlist] = !f.favStations[playlist]
		ok(`{"ok":true}`)
		return true
	}
	return false
}
