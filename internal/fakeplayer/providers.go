package fakeplayer

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Provider fixtures mirror the live shapes probed against cliamp v2.0.1
// (spec §2). Deterministic; the browser smoke reuses these via this package.

var providerFixtures = []map[string]any{
	{"key": "navidrome", "name": "Navidrome", "searchable": true, "browse_albums": true, "browse_artists": true},
	{"key": "radio", "name": "Radio", "searchable": true, "catalog": true},
	{"key": "local", "name": "Local", "searchable": true},
}

// knownProviders is wider than the list fixture on purpose: the live player
// answers searches for providers (ytmusic) whose listing set differs.
func knownProviders(key string) bool {
	switch key {
	case "navidrome", "radio", "local", "ytmusic":
		return true
	}
	return false
}

func fixturePlaylists() []map[string]any {
	out := make([]map[string]any, 0, 120)
	for i := range 120 {
		out = append(out, map[string]any{
			"id": "pl" + strconv.Itoa(i), "name": "Playlist " + strconv.Itoa(i),
			"provider": "navidrome", "track_count": 10 + i%40,
		})
	}
	return out
}

func fixtureAlbums() []map[string]any {
	return []map[string]any{
		{"id": "al1", "name": "Album One", "artist": "Artist A", "artist_id": "ar1", "year": 1999, "track_count": 7, "genre": "Rock"},
		{"id": "al2", "name": "Album Two", "artist": "Artist A", "artist_id": "ar1", "year": 2003, "track_count": 2, "genre": "Rock"},
		{"id": "al3", "name": "Album Three", "artist": "Artist B", "artist_id": "ar2", "year": 2010, "track_count": 1, "genre": "Jazz"},
	}
}

func fixtureArtists() []map[string]any {
	return []map[string]any{
		{"id": "ar1", "name": "Artist A", "album_count": 2},
		{"id": "ar2", "name": "Artist B", "album_count": 1},
	}
}

func fixtureCatalog() []map[string]any {
	return []map[string]any{
		{"id": "c:0", "name": "Station Zero", "provider": "radio", "favoritable": true},
		{"id": "c:1", "name": "Station One", "provider": "radio"},
		{"id": "l:0", "name": "fake radio", "provider": "radio"},
		{"id": "loc:ask", "name": "Use my location", "provider": "radio"},
	}
}

func fixtureTracks(kind, id string) []map[string]any {
	switch kind + ":" + id {
	case "album:al1":
		out := make([]map[string]any, 0, 7)
		for i := range 7 {
			out = append(out, track(fmt.Sprintf("Album One Track %d", i+1), "Artist A", "/fake/a1-"+strconv.Itoa(i)+".flac", 180+i))
		}
		return out
	case "album:al2":
		return []map[string]any{
			track("Album Two Track 1", "Artist A", "/fake/a2-0.flac", 200),
			track("Album Two Track 2", "Artist A", "/fake/a2-1.flac", 210),
		}
	case "album:al3":
		return []map[string]any{track("Album Three Track 1", "Artist B", "/fake/a3-0.flac", 300)}
	case "artist:ar1":
		return []map[string]any{
			track("Album One Track 1", "Artist A", "/fake/a1-0.flac", 180),
			track("Album One Track 2", "Artist A", "/fake/a1-1.flac", 181),
			track("Album Two Track 1", "Artist A", "/fake/a2-0.flac", 200),
			track("Best Of", "Artist A", "/fake/best.flac", 100),
		}
	case "artist:ar2":
		return []map[string]any{track("Album Three Track 1", "Artist B", "/fake/a3-0.flac", 300)}
	case "playlist:pl1":
		out := make([]map[string]any, 0, 3)
		for i := range 3 {
			out = append(out, track(fmt.Sprintf("Playlist One Track %d", i+1), "Artist A", "/fake/pl1-"+strconv.Itoa(i)+".flac", 200+i))
		}
		return out
	case "playlist:pl-empty":
		return []map[string]any{}
	}
	return []map[string]any{}
}

func track(title, artist, path string, dur int) map[string]any {
	return map[string]any{"title": title, "artist": artist, "path": path, "duration_secs": dur, "stream": false}
}

func fixtureSearch(query string) []map[string]any {
	if query == "zzzznone" {
		return []map[string]any{}
	}
	return []map[string]any{
		map[string]any{"title": query + " mix one", "artist": "Someone", "path": "https://example.invalid/one", "duration_secs": 240, "stream": true},
		map[string]any{"title": query + " mix two", "artist": "Someone", "path": "https://example.invalid/two", "duration_secs": 300, "stream": true},
		map[string]any{"title": query + " radio", "artist": "Stream FM", "path": "https://example.invalid/radio.m3u8", "duration_secs": 0, "stream": true},
	}
}

// providerOps answers provider.* exactly like the live player: params carry
// "provider"/"album"/"id"/"query"; unknown provider and missing id fail as
// internal_error jobs with the probed detail strings.
func (f *Fake) providerOps(c net.Conn, id, op string, params json.RawMessage) bool {
	if !strings.HasPrefix(op, "provider.") {
		return false
	}
	var p struct {
		Provider string `json:"provider"`
		Playlist string `json:"playlist"`
		Album    string `json:"album"`
		ID       string `json:"id"`
		Artist   string `json:"artist"`
		Query    string `json:"query"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	_ = json.Unmarshal(params, &p)
	if op != "provider.list" && !knownProviders(p.Provider) {
		// live cliamp echoes the empty string: it looks the provider up
		// before validating the param (probed v2.0.1).
		failJob(c, id, op, `unknown provider ""`)
		return true
	}
	ok := func(result string) {
		fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"jx\",\"operation\":%q,\"state\":\"succeeded\",\"result\":%s}}\n", id, op, result)
	}
	switch op {
	case "provider.list":
		b, _ := json.Marshal(map[string]any{"ok": true, "providers": providerFixtures})
		ok(string(b))
	case "provider.playlists":
		b, _ := json.Marshal(paged(fixturePlaylists(), p.Offset, p.Limit, "playlists"))
		ok(string(b))
	case "provider.albums":
		b, _ := json.Marshal(paged(fixtureAlbums(), p.Offset, p.Limit, "albums"))
		ok(string(b))
	case "provider.artists":
		b, _ := json.Marshal(paged(fixtureArtists(), p.Offset, p.Limit, "artists"))
		ok(string(b))
	case "provider.catalog":
		b, _ := json.Marshal(paged(fixtureCatalog(), p.Offset, p.Limit, "playlists"))
		ok(string(b))
	case "provider.album_tracks":
		if p.Album == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'album' (code 10)")
			return true
		}
		b, _ := json.Marshal(paged(fixtureTracks("album", p.Album), p.Offset, p.Limit, "tracks"))
		ok(string(b))
	case "provider.tracks":
		// live precedence: id wins when both given; playlist drills playlists.
		if p.Playlist != "" && p.ID == "" {
			if p.Playlist == "nope" {
				failJob(c, id, op, "navidrome: playlist not found (code 70)")
				return true
			}
			b, _ := json.Marshal(paged(fixtureTracks("playlist", p.Playlist), p.Offset, p.Limit, "tracks"))
			ok(string(b))
			return true
		}
		if p.ID == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'id' (code 10)")
			return true
		}
		b, _ := json.Marshal(paged(fixtureTracks("artist", p.ID), p.Offset, p.Limit, "tracks"))
		ok(string(b))
	case "provider.artist_albums":
		// live v2.4.0: the artist id rides params.artist (params.id → "Artist not found").
		if p.Artist == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'artist' (code 10)")
			return true
		}
		var rows []map[string]any
		for _, a := range fixtureAlbums() {
			if a["artist_id"] == p.Artist {
				rows = append(rows, a)
			}
		}
		b, _ := json.Marshal(paged(rows, p.Offset, p.Limit, "albums"))
		ok(string(b))
	case "provider.search":
		b, _ := json.Marshal(paged(fixtureSearch(p.Query), p.Offset, p.Limit, "tracks"))
		ok(string(b))
	case "provider.load":
		if p.Playlist == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'playlist' (code 10)")
			return true
		}
		ok(`{"ok":true}`)
	case "provider.load_album":
		if p.Album == "" {
			failJob(c, id, op, "navidrome: missing parameter: 'album' (code 10)")
			return true
		}
		ok(`{"ok":true}`)
	default:
		return false
	}
	return true
}

func paged(rows []map[string]any, offset, limit int, field string) map[string]any {
	if limit <= 0 {
		limit = 50
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return map[string]any{"ok": true, "total": len(rows), field: rows[offset:end]}
}

func failJob(c net.Conn, id, op, detail string) {
	b, _ := json.Marshal(map[string]any{
		"version": 2, "id": id, "ok": true,
		"job": map[string]any{"id": "jf", "operation": op, "state": "failed",
			"error": map[string]any{"code": "internal_error", "detail": detail}},
	})
	fmt.Fprintln(c, string(b))
}
