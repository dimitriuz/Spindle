package server

import (
	"encoding/json"
	"strconv"

	"spindle/internal/files"
	"spindle/internal/snapshot"
)

// CompactItem is the one row shape of every compact list: small enough for a
// microcontroller to parse, with all six keys always present.
type CompactItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Kind     string `json:"kind"`
	Playable bool   `json:"playable"`
	HasArt   bool   `json:"has_art"`
}

// compactRow is the union of the fields the providers put on albums, artists,
// playlists, stations and tracks.
type compactRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Path       string `json:"path"`
	TrackCount int    `json:"track_count"`
	AlbumCount int    `json:"album_count"`
}

// listKeys are the array names a provider result can carry its rows under.
var listKeys = []string{"albums", "artists", "playlists", "tracks"}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func (r compactRow) item(hint string) CompactItem {
	it := CompactItem{ID: r.ID, Title: r.Name, Kind: hint, Playable: true, HasArt: true}
	switch hint {
	case "track":
		it.ID, it.Title, it.Subtitle = r.Path, r.Title, r.Artist
	case "album":
		it.Subtitle = r.Artist
	case "artist":
		if r.AlbumCount > 0 {
			it.Subtitle = plural(r.AlbumCount, "album", "albums")
		}
	case "playlist":
		if r.TrackCount > 0 {
			it.Subtitle = plural(r.TrackCount, "track", "tracks")
		}
	}
	return it
}

// compactList turns a provider listing into {"items","total","offset","limit"}.
// hint is album, artist, playlist, track or station. Providers do not echo
// the paging, so offset and limit are what the caller asked for unless the
// result carries its own.
func compactList(hint string, raw json.RawMessage, offset, limit int) (json.RawMessage, error) {
	var res map[string]json.RawMessage
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	items := []CompactItem{}
	for _, k := range listKeys {
		rows, ok := res[k]
		if !ok {
			continue
		}
		var rs []compactRow
		if err := json.Unmarshal(rows, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			items = append(items, r.item(hint))
		}
		break
	}
	num := func(k string, def int) int {
		var n int
		if v, ok := res[k]; ok && json.Unmarshal(v, &n) == nil {
			return n
		}
		return def
	}
	return json.Marshal(map[string]any{
		"items":  items,
		"total":  num("total", len(items)),
		"offset": num("offset", offset),
		"limit":  num("limit", limit),
	})
}

// compactFiles is a folder listing in the compact shape. An item id is its
// path below the music root, which the files endpoints accept back.
func compactFiles(l files.Listing) json.RawMessage {
	items := make([]CompactItem, 0, len(l.Entries))
	for _, e := range l.Entries {
		id := e.Name
		if l.Path != "" {
			id = l.Path + "/" + e.Name
		}
		kind := "file"
		if e.Dir {
			kind = "folder"
		}
		items = append(items, CompactItem{ID: id, Title: e.Name, Kind: kind, Playable: true, HasArt: e.Dir})
	}
	b, _ := json.Marshal(map[string]any{"path": l.Path, "parent": l.Parent, "name": l.Name, "items": items})
	return b
}

// compactQueue is a window of the live queue in the compact shape.
func compactQueue(q snapshot.Queue, offset, limit int) json.RawMessage {
	var rows []compactRow
	_ = json.Unmarshal(q.Tracks, &rows)
	if offset < 0 {
		offset = 0
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	end := min(offset+max(limit, 0), len(rows))
	items := make([]CompactItem, 0, end-offset)
	for _, r := range rows[offset:end] {
		items = append(items, r.item("track"))
	}
	b, _ := json.Marshal(map[string]any{
		"items": items, "count": q.Count, "offset": offset, "limit": limit,
		"playlist_revision": q.PlaylistRevision,
	})
	return b
}

// queueFrameRows is the most queue rows one compact response or WebSocket
// frame carries; a client pages through the rest.
const queueFrameRows = 200
