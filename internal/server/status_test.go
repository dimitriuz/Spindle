package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"spindle/internal/snapshot"
)

func decodeFrame(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %s: %v", b, err)
	}
	return m
}

func TestBuildStatusPlaying(t *testing.T) {
	st := json.RawMessage(`{"revision":5,"state":"playing","track":{"title":"Song","artist":"Band","album":"LP","path":"/m/a.flac"},"position":12.5,"duration":200,"volume":-3.5,"shuffle":true,"repeat":"All","index":4,"seekable":true}`)
	got := decodeFrame(t, buildStatus(st, true))
	want := map[string]any{
		"type": "status", "online": true, "state": "playing", "title": "Song", "artist": "Band", "album": "LP",
		"position": 12.5, "duration": 200.0, "volume": -3.5, "shuffle": true, "repeat": "All", "index": 4.0,
		"seekable": true, "art": "/m/a.flac",
	}
	if len(got) != len(want) {
		t.Fatalf("keys: got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %v (%T) want %v", k, got[k], got[k], v)
		}
	}
}

// cliamp leaves zero values out of its snapshot: absent index on a loaded
// track means 0, absent volume means 0 dB, absent repeat means Off.
func TestBuildStatusZeroValuesOmitted(t *testing.T) {
	got := decodeFrame(t, buildStatus(json.RawMessage(`{"state":"paused","track":{"title":"A","path":"/a"}}`), true))
	if got["index"] != 0.0 || got["volume"] != 0.0 || got["repeat"] != "Off" || got["shuffle"] != false || got["state"] != "paused" {
		t.Fatalf("%v", got)
	}
}

func TestBuildStatusOffline(t *testing.T) {
	got := decodeFrame(t, buildStatus(json.RawMessage(`{}`), false))
	if got["online"] != false || got["art"] != "" || got["title"] != "" || got["state"] != "stopped" || got["index"] != -1.0 || got["position"] != 0.0 {
		t.Fatalf("%v", got)
	}
}

func TestBuildStatusNoNulls(t *testing.T) {
	for _, in := range []string{``, `null`, `[]`, `{"track":null}`, `{"track":{"title":null}}`, `{"position":"x"}`} {
		b := buildStatus(json.RawMessage(in), true)
		if strings.Contains(string(b), "null") {
			t.Errorf("input %q gave %s", in, b)
		}
		decodeFrame(t, b)
	}
}

func TestBuildQueueFrameCap(t *testing.T) {
	rows := make([]string, 5000)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"title":"t%d","path":"/p%d"}`, i, i)
	}
	q := snapshot.Queue{Tracks: json.RawMessage("[" + strings.Join(rows, ",") + "]"), Count: 5000, PlaylistRevision: 3}
	got := decodeFrame(t, buildQueueFrame(q))
	if got["type"] != "queue" || got["count"] != 5000.0 || len(got["items"].([]any)) != queueFrameRows || got["playlist_revision"] != 3.0 {
		t.Fatalf("type=%v count=%v items=%d", got["type"], got["count"], len(got["items"].([]any)))
	}
}
