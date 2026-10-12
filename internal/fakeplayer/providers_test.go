package fakeplayer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cliamp-web/internal/ipc"
)

func startProviders(t *testing.T) *ipc.Conn {
	f, err := Start(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return ipc.NewConn(f.Path)
}

func call(t *testing.T, c *ipc.Conn, op string, params map[string]any) json.RawMessage {
	t.Helper()
	job, err := c.SubmitJob(context.Background(), op, params)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return job.Result
}

func TestProviderListShape(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.list", nil)
	var r struct {
		OK        bool `json:"ok"`
		Providers []struct {
			Key          string `json:"key"`
			Name         string `json:"name"`
			Searchable   bool   `json:"searchable"`
			Catalog      bool   `json:"catalog"`
			BrowseAlbums bool   `json:"browse_albums"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || !r.OK || len(r.Providers) != 3 {
		t.Fatalf("bad providers: %s", raw)
	}
}

func TestProviderPagination(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.playlists", map[string]any{"provider": "navidrome", "offset": 100, "limit": 50})
	var r struct {
		Total     int `json:"total"`
		Playlists []struct {
			Name string `json:"name"`
		} `json:"playlists"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Total != 120 || len(r.Playlists) != 20 {
		t.Fatalf("offset 100 limit 50 of 120 should yield 20 rows: %s", raw)
	}
	if r.Playlists[0].Name != "Playlist 100" {
		t.Fatalf("offset ignored: %s", raw)
	}
}

func TestUnknownProviderFailsLikeLive(t *testing.T) {
	c := startProviders(t)
	_, err := c.SubmitJob(context.Background(), "provider.playlists", map[string]any{"provider": "bogus"})
	if err == nil || !strings.Contains(err.Error(), `unknown provider ""`) {
		t.Fatalf("want internal_error unknown provider, got %v", err)
	}
}

func TestMissingIDParamFailsLikeLive(t *testing.T) {
	c := startProviders(t)
	_, err := c.SubmitJob(context.Background(), "provider.tracks", map[string]any{"provider": "navidrome"})
	if err == nil || !strings.Contains(err.Error(), "missing parameter: 'id'") {
		t.Fatalf("want missing id error, got %v", err)
	}
}

func TestLoadOpsDemandTheirParam(t *testing.T) {
	c := startProviders(t)
	// live cliamp v2.0.1: provider.load wants params.playlist,
	// provider.load_album wants params.album; anything else fails the job.
	if _, err := c.SubmitJob(context.Background(), "provider.load", map[string]any{"provider": "navidrome", "id": "pl1"}); err == nil || !strings.Contains(err.Error(), "missing parameter: 'playlist'") {
		t.Fatalf("load with wrong param name must fail like live: %v", err)
	}
	if _, err := c.SubmitJob(context.Background(), "provider.load_album", map[string]any{"provider": "navidrome", "id": "al3"}); err == nil || !strings.Contains(err.Error(), "missing parameter: 'album'") {
		t.Fatalf("load_album given id instead of album must fail like live: %v", err)
	}
	call(t, c, "provider.load", map[string]any{"provider": "navidrome", "playlist": "pl1"})
	call(t, c, "provider.load_album", map[string]any{"provider": "navidrome", "album": "al3"})
}

func TestSearchEmptyAndNonEmpty(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.search", map[string]any{"provider": "ytmusic", "query": "lofi", "limit": 2})
	if !strings.Contains(string(raw), `"total":3`) || !strings.Contains(string(raw), `"duration_secs"`) {
		t.Fatalf("search shape: %s", raw)
	}
	raw = call(t, c, "provider.search", map[string]any{"provider": "ytmusic", "query": "zzzznone"})
	var r struct {
		Total  int           `json:"total"`
		Tracks []interface{} `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Total != 0 || r.Tracks == nil || len(r.Tracks) != 0 {
		t.Fatalf("empty search must be total:0 with an empty array: %s", raw)
	}
}

func TestProviderTracksPlaylistParam(t *testing.T) {
	c := startProviders(t)
	raw := call(t, c, "provider.tracks", map[string]any{"provider": "navidrome", "playlist": "pl1"})
	if !strings.Contains(string(raw), `"Playlist One Track 1"`) || !strings.Contains(string(raw), `"total":3`) {
		t.Fatalf("playlist rows: %s", raw)
	}
	// id wins when both params present (live precedence, spec §4)
	raw = call(t, c, "provider.tracks", map[string]any{"provider": "navidrome", "id": "ar2", "playlist": "pl1"})
	if strings.Contains(string(raw), "Playlist One") || !strings.Contains(string(raw), "Album Three Track 1") {
		t.Fatalf("id must win over playlist: %s", raw)
	}
	if _, err := c.SubmitJob(context.Background(), "provider.tracks", map[string]any{"provider": "navidrome", "playlist": "nope"}); err == nil || !strings.Contains(err.Error(), "playlist not found (code 70)") {
		t.Fatalf("bogus playlist must fail like live: %v", err)
	}
	if _, err := c.SubmitJob(context.Background(), "provider.tracks", map[string]any{"provider": "navidrome"}); err == nil || !strings.Contains(err.Error(), "missing parameter: 'id'") {
		t.Fatalf("missing both must keep failing 'id': %v", err)
	}
}

func TestLyricsOp(t *testing.T) {
	f, err := Start(t.TempDir(), Options{Lyrics: `[{"start":0.5,"text":"line one"}]`})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw := call(t, ipc.NewConn(f.Path), "lyrics", nil)
	if !strings.Contains(string(raw), `"line one"`) {
		t.Fatalf("lyrics fixture: %s", raw)
	}
	g, err := Start(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := ipc.NewConn(g.Path).SubmitJob(context.Background(), "lyrics", nil); err == nil || !strings.Contains(err.Error(), "no lyrics found") {
		t.Fatalf("empty lyrics must fail like live: %v", err)
	}
}
