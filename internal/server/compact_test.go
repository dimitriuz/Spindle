package server

import (
	"encoding/json"
	"strings"
	"testing"

	"spindle/internal/files"
	"spindle/internal/snapshot"
)

type compactOut struct {
	Items  []map[string]any `json:"items"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

func decodeCompact(t *testing.T, raw json.RawMessage) compactOut {
	t.Helper()
	var o compactOut
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return o
}

func TestCompactAlbums(t *testing.T) {
	in := json.RawMessage(`{"ok":true,"total":3,"offset":0,"limit":50,"albums":[{"id":"al1","name":"Album One","artist":"Artist A","year":1999,"track_count":7}]}`)
	out, err := compactList("album", in, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	o := decodeCompact(t, out)
	if o.Total != 3 || len(o.Items) != 1 || o.Limit != 50 {
		t.Fatalf("got %s", out)
	}
	it := o.Items[0]
	if it["id"] != "al1" || it["title"] != "Album One" || it["subtitle"] != "Artist A" || it["kind"] != "album" || it["playable"] != true || it["has_art"] != true {
		t.Fatalf("item: %v", it)
	}
	if strings.Contains(string(out), `"albums"`) {
		t.Fatalf("raw key leaked: %s", out)
	}
}

func TestCompactArtists(t *testing.T) {
	in := json.RawMessage(`{"ok":true,"total":2,"artists":[{"id":"ar1","name":"A","album_count":2},{"id":"ar2","name":"B","album_count":1}]}`)
	out, err := compactList("artist", in, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	o := decodeCompact(t, out)
	if o.Items[0]["subtitle"] != "2 albums" || o.Items[1]["subtitle"] != "1 album" {
		t.Fatalf("subtitles: %v", o.Items)
	}
}

func TestCompactPlaylistsAndStation(t *testing.T) {
	in := json.RawMessage(`{"ok":true,"total":1,"playlists":[{"id":"c:0","name":"Station Zero","provider":"radio"}]}`)
	out, err := compactList("station", in, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	it := decodeCompact(t, out).Items[0]
	if it["kind"] != "station" || it["subtitle"] != "" || it["title"] != "Station Zero" {
		t.Fatalf("station: %v", it)
	}
	out, _ = compactList("playlist", json.RawMessage(`{"playlists":[{"id":"pl1","name":"P","track_count":12}]}`), 0, 0)
	it = decodeCompact(t, out).Items[0]
	if it["kind"] != "playlist" || it["subtitle"] != "12 tracks" {
		t.Fatalf("playlist: %v", it)
	}
}

func TestCompactTracks(t *testing.T) {
	in := json.RawMessage(`{"ok":true,"total":1,"tracks":[{"title":"T","artist":"Artist A","path":"/fake/a1-0.flac","duration_secs":180}]}`)
	out, err := compactList("track", in, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	it := decodeCompact(t, out).Items[0]
	if it["id"] != "/fake/a1-0.flac" || it["subtitle"] != "Artist A" || it["kind"] != "track" || it["playable"] != true {
		t.Fatalf("track: %v", it)
	}
}

func TestCompactMissingFields(t *testing.T) {
	out, err := compactList("album", json.RawMessage(`{"albums":[{"id":"x"}]}`), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	it := decodeCompact(t, out).Items[0]
	for _, k := range []string{"id", "title", "subtitle", "kind", "playable", "has_art"} {
		v, ok := it[k]
		if !ok || v == nil {
			t.Fatalf("key %s missing or null in %v", k, it)
		}
	}
	if it["subtitle"] != "" || it["title"] != "" {
		t.Fatalf("want empty strings: %v", it)
	}
}

func TestCompactEmptyIsEmptyArray(t *testing.T) {
	out, err := compactList("album", json.RawMessage(`{"ok":true,"total":0}`), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"items":[]`) {
		t.Fatalf("items must be [] not null: %s", out)
	}
}

func TestCompactFiles(t *testing.T) {
	l := files.Listing{Path: "Rock", Parent: "", Name: "Rock", Entries: []files.Entry{{Name: "Band", Dir: true}, {Name: "a.flac"}}}
	var o struct {
		Path, Parent, Name string
		Items              []map[string]any
	}
	if err := json.Unmarshal(compactFiles(l), &o); err != nil {
		t.Fatal(err)
	}
	if o.Path != "Rock" || len(o.Items) != 2 {
		t.Fatalf("%+v", o)
	}
	d, f := o.Items[0], o.Items[1]
	if d["id"] != "Rock/Band" || d["kind"] != "folder" || d["has_art"] != true || d["playable"] != true {
		t.Fatalf("folder: %v", d)
	}
	if f["id"] != "Rock/a.flac" || f["kind"] != "file" || f["has_art"] != false {
		t.Fatalf("file: %v", f)
	}
	root := files.Listing{Name: "Music", Entries: []files.Entry{{Name: "Rock", Dir: true}}}
	if !strings.Contains(string(compactFiles(root)), `"id":"Rock"`) {
		t.Fatalf("root id must have no leading slash: %s", compactFiles(root))
	}
}

func TestCompactQueueWindow(t *testing.T) {
	q := snapshot.Queue{
		Tracks: json.RawMessage(`[{"title":"1","artist":"a","path":"/1"},{"title":"2","artist":"a","path":"/2"},{"title":"3","path":"/3"},{"title":"4","path":"/4"},{"title":"5","path":"/5"}]`),
		Count:  5, PlaylistRevision: 9,
	}
	var o struct {
		Items            []map[string]any
		Count, Offset    int
		Limit            int
		PlaylistRevision int `json:"playlist_revision"`
	}
	if err := json.Unmarshal(compactQueue(q, 1, 2), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Items) != 2 || o.Items[0]["id"] != "/2" || o.Items[1]["id"] != "/3" || o.Count != 5 || o.Offset != 1 || o.Limit != 2 || o.PlaylistRevision != 9 {
		t.Fatalf("%+v", o)
	}
	if o.Items[0]["kind"] != "track" {
		t.Fatalf("kind: %v", o.Items[0])
	}
	// window past the end is empty, not an error
	if err := json.Unmarshal(compactQueue(q, 50, 2), &o); err != nil || len(o.Items) != 0 {
		t.Fatalf("past end: %v %+v", err, o)
	}
}
