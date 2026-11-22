package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"spindle/internal/files"
	"spindle/internal/ipc"
)

func TestLibraryAlbumsCompact(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"total":1,"albums":[{"id":"al1","name":"Album One","artist":"Artist A"}]}`)}}
	h := New(libConfig(t, fr))
	rec := libGet(t, h, "/api/library/navidrome/albums?compact=1")
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	o := decodeCompact(t, json.RawMessage(rec.Body.Bytes()))
	if len(o.Items) != 1 || o.Items[0]["kind"] != "album" || o.Items[0]["subtitle"] != "Artist A" {
		t.Fatalf("compact: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"albums"`) {
		t.Fatalf("raw key leaked: %s", rec.Body)
	}
	// without the parameter the response is the provider's, as before
	if full := libGet(t, h, "/api/library/navidrome/albums"); !strings.Contains(full.Body.String(), `"albums"`) {
		t.Fatalf("full response changed: %s", full.Body)
	}
}

func TestLibraryKindsCompactHints(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"total":1,"playlists":[{"id":"x","name":"N"}]}`)}}
	h := New(libConfig(t, fr))
	for path, kind := range map[string]string{
		"/api/library/radio/catalog?compact=1":       "station",
		"/api/library/navidrome/playlists?compact=1": "playlist",
	} {
		o := decodeCompact(t, json.RawMessage(libGet(t, h, path).Body.Bytes()))
		if len(o.Items) != 1 || o.Items[0]["kind"] != kind {
			t.Fatalf("%s: want kind %s, got %v", path, kind, o.Items)
		}
	}
}

func TestSearchCompactTracks(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"total":1,"tracks":[{"title":"T","artist":"A","path":"/p"}]}`)}}
	h := New(libConfig(t, fr))
	o := decodeCompact(t, json.RawMessage(libGet(t, h, "/api/search/navidrome?q=x&compact=1").Body.Bytes()))
	if len(o.Items) != 1 || o.Items[0]["id"] != "/p" || o.Items[0]["kind"] != "track" {
		t.Fatalf("search: %v", o.Items)
	}
	// the drill-down routes give tracks too, an artist's albums give albums
	if o := decodeCompact(t, json.RawMessage(libGet(t, h, "/api/library/navidrome/album/al1/tracks?compact=1").Body.Bytes())); o.Items[0]["kind"] != "track" {
		t.Fatalf("album tracks: %v", o.Items)
	}
}

func TestArtistAlbumsCompactHint(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"total":1,"albums":[{"id":"al1","name":"N"}]}`)}}
	h := New(libConfig(t, fr))
	o := decodeCompact(t, json.RawMessage(libGet(t, h, "/api/library/navidrome/artist/ar1/albums?compact=1").Body.Bytes()))
	if o.Items[0]["kind"] != "album" {
		t.Fatalf("%v", o.Items)
	}
}

func TestFilesCompact(t *testing.T) {
	ff := &fakeFiles{root: "music", l: files.Listing{Path: "Classic", Name: "Classic", Entries: []files.Entry{{Name: "Vivaldi", Dir: true}}}}
	rec := artGet(filesServer(t, ff), "/api/files?path=Classic&compact=1", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"Classic/Vivaldi"`) || strings.Contains(rec.Body.String(), `"entries"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func queueServer(t *testing.T, n int) *Config {
	cfg := guardedConfig(t)
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"title":"t%d","path":"/p%d"}`, i, i)
	}
	cfg.Cache.AbsorbQueueResult(json.RawMessage(`{"tracks":[`+strings.Join(rows, ",")+`]}`), 5)
	return &cfg
}

func TestQueueCompactWindow(t *testing.T) {
	h := New(*queueServer(t, 5))
	rec := libGet(t, h, "/api/queue?compact=1&offset=1&limit=2")
	var o struct {
		Items         []map[string]any
		Count, Offset int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if len(o.Items) != 2 || o.Items[0]["id"] != "/p1" || o.Count != 5 || o.Offset != 1 {
		t.Fatalf("%+v", o)
	}
	// defaults: first 200 rows
	if rec := libGet(t, New(*queueServer(t, 250)), "/api/queue?compact=1"); strings.Count(rec.Body.String(), `"kind":"track"`) != 200 {
		t.Fatalf("default window must be 200 rows")
	}
	// without compact the queue is unchanged
	if rec := libGet(t, h, "/api/queue"); !strings.Contains(rec.Body.String(), `"play_next"`) {
		t.Fatalf("full queue changed: %s", rec.Body)
	}
}

func TestQueueCompactBadParams(t *testing.T) {
	h := New(*queueServer(t, 2))
	for _, q := range []string{"limit=abc", "limit=0", "offset=-1", "offset=x"} {
		if rec := libGet(t, h, "/api/queue?compact=1&"+q); rec.Code != 400 {
			t.Fatalf("%s: want 400 got %d", q, rec.Code)
		}
	}
	if rec := libGet(t, h, "/api/queue?compact=1&limit=99999"); rec.Code != 200 {
		t.Fatalf("over-large limit clamps, got %d", rec.Code)
	}
}

func TestProvidersNeverCompact(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"providers":[{"key":"navidrome"}]}`)}}
	rec := libGet(t, New(libConfig(t, fr)), "/api/library/providers?compact=1")
	if !strings.Contains(rec.Body.String(), `"providers"`) {
		t.Fatalf("providers must stay as is: %s", rec.Body)
	}
}

// The provider does not echo paging; a device pages by what it asked for.
func TestLibraryCompactEchoesRequestedPaging(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true,"total":9,"albums":[{"id":"a","name":"A"}]}`)}}
	h := New(libConfig(t, fr))
	o := decodeCompact(t, json.RawMessage(libGet(t, h, "/api/library/navidrome/albums?compact=1&offset=4&limit=2").Body.Bytes()))
	if o.Offset != 4 || o.Limit != 2 || o.Total != 9 {
		t.Fatalf("paging: %+v", o)
	}
	// defaults when the caller says nothing
	o = decodeCompact(t, json.RawMessage(libGet(t, h, "/api/library/navidrome/playlists?compact=1").Body.Bytes()))
	if o.Offset != 0 || o.Limit != defaultLimit {
		t.Fatalf("defaults: %+v", o)
	}
}
