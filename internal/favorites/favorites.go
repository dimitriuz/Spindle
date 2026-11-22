// Package favorites keeps one list of favorites over three homes: cliamp's
// own Favorites playlist (tracks), cliamp's favorite radio stations, and a
// small file for what cliamp has no favorites for (albums, artists, playlists).
package favorites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cliamp-web/internal/ipc"
)

type Kind string

const (
	Track    Kind = "track"
	Album    Kind = "album"
	Artist   Kind = "artist"
	Playlist Kind = "playlist"
	Radio    Kind = "radio"
)

// ErrPartial accompanies a list that is missing cliamp's part (player offline).
var ErrPartial = errors.New("favorites: player unavailable, list is incomplete")

const (
	cacheTTL   = 30 * time.Second
	pageSize   = 200
	maxRows    = 5000
	maxStored  = 5000
	fileName   = "favorites.json"
	starPrefix = "★ "
)

type Item struct {
	Kind     Kind            `json:"kind"`
	Provider string          `json:"provider"`
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Subtitle string          `json:"subtitle,omitempty"`
	Added    *time.Time      `json:"added,omitempty"`
	Track    json.RawMessage `json:"track,omitempty"`
}

type SetRequest struct {
	Kind            Kind
	Provider, ID    string
	Title, Subtitle string
	Favorite        bool
	Track           map[string]any
}

// TrackKey identifies a track: the provider and id from provider_meta
// ("navidrome.id"), else a local file by its path.
func TrackKey(track map[string]any) (provider, id string) {
	if track == nil {
		return "", ""
	}
	if meta, ok := track["provider_meta"].(map[string]any); ok {
		for k, v := range meta {
			if p, found := strings.CutSuffix(k, ".id"); found {
				if s, ok := v.(string); ok && s != "" {
					return p, s
				}
			}
		}
	}
	if p, ok := track["path"].(string); ok && p != "" {
		return "local", p
	}
	return "", ""
}

type Service struct {
	dir     string
	r       ipc.OpRunner
	restore func(string) string
	now     func() time.Time

	mu      sync.Mutex // serialises every read-modify-write, including cliamp toggles
	stored  []Item     // albums, artists, playlists
	cache   []Item
	cacheOK bool
	cacheAt time.Time
}

func New(dir string, r ipc.OpRunner, restore func(string) string, now func() time.Time) (*Service, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if restore == nil {
		restore = func(s string) string { return s }
	}
	if now == nil {
		now = time.Now
	}
	s := &Service{dir: dir, r: r, restore: restore, now: now}
	s.load()
	return s, nil
}

func (s *Service) path() string { return filepath.Join(s.dir, fileName) }

// load reads the file; one that cannot be read is kept as .bad and the list
// starts empty rather than failing the whole bridge.
func (s *Service) load() {
	b, err := os.ReadFile(s.path())
	if err != nil {
		return
	}
	var items []Item
	if json.Unmarshal(b, &items) != nil {
		_ = os.Rename(s.path(), s.path()+".bad")
		return
	}
	s.stored = items
}

func (s *Service) save() error {
	b, err := json.MarshalIndent(s.stored, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// ---- cliamp reads ----

// pages collects every row of a paged cliamp listing.
func (s *Service) pages(ctx context.Context, op string, params map[string]any, field string) ([]json.RawMessage, error) {
	var rows []json.RawMessage
	for len(rows) < maxRows {
		p := map[string]any{"offset": len(rows), "limit": pageSize}
		for k, v := range params {
			p[k] = v
		}
		job, err := s.r.SubmitJob(ctx, op, p)
		if err != nil {
			return nil, err
		}
		var page map[string]json.RawMessage
		if json.Unmarshal(job.Result, &page) != nil {
			return nil, fmt.Errorf("favorites: unreadable %s reply", op)
		}
		var got []json.RawMessage
		_ = json.Unmarshal(page[field], &got)
		rows = append(rows, got...)
		total := -1
		_ = json.Unmarshal(page["total"], &total)
		if len(got) == 0 || total < 0 || len(rows) >= total {
			break
		}
	}
	return rows, nil
}

type stationRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Favorite bool   `json:"favorite"`
}

func (s *Service) stationRows(ctx context.Context) ([]stationRow, error) {
	raw, err := s.pages(ctx, "provider.playlists", map[string]any{"provider": "radio"}, "playlists")
	if err != nil {
		return nil, err
	}
	out := make([]stationRow, 0, len(raw))
	for _, r := range raw {
		var row stationRow
		if json.Unmarshal(r, &row) == nil && row.ID != "" {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *Service) trackRows(ctx context.Context) ([]json.RawMessage, error) {
	return s.pages(ctx, "provider.tracks", map[string]any{"provider": "local", "playlist": "Favorites"}, "tracks")
}

// ---- list ----

func (s *Service) List(ctx context.Context) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheOK && s.now().Sub(s.cacheAt) < cacheTTL {
		return append([]Item(nil), s.cache...), nil
	}
	items := s.storedSorted()
	var partial bool

	if rows, err := s.stationRows(ctx); err != nil {
		partial = true
	} else {
		for _, r := range rows {
			if strings.HasPrefix(r.ID, "f:") {
				items = append(items, Item{Kind: Radio, Provider: "radio", ID: r.ID, Title: strings.TrimPrefix(r.Name, starPrefix)})
			}
		}
	}
	if raws, err := s.trackRows(ctx); err != nil {
		partial = true
	} else {
		for i := len(raws) - 1; i >= 0; i-- { // newest first
			var t map[string]any
			if json.Unmarshal(raws[i], &t) != nil {
				continue
			}
			prov, id := TrackKey(t)
			if id == "" {
				continue
			}
			title, _ := t["title"].(string)
			artist, _ := t["artist"].(string)
			items = append(items, Item{Kind: Track, Provider: prov, ID: id, Title: title, Subtitle: artist, Track: raws[i]})
		}
	}
	if partial {
		return items, ErrPartial // not cached: the next call tries again
	}
	s.cache, s.cacheOK, s.cacheAt = items, true, s.now()
	return append([]Item(nil), items...), nil
}

// storedSorted lists the file's items grouped by kind, newest first.
func (s *Service) storedSorted() []Item {
	order := map[Kind]int{Album: 0, Artist: 1, Playlist: 2}
	out := append([]Item(nil), s.stored...)
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Kind] != order[out[j].Kind] {
			return order[out[i].Kind] < order[out[j].Kind]
		}
		ai, aj := time.Time{}, time.Time{}
		if out[i].Added != nil {
			ai = *out[i].Added
		}
		if out[j].Added != nil {
			aj = *out[j].Added
		}
		return ai.After(aj)
	})
	return out
}

// ---- set ----

// Set makes an item a favorite (or not) and returns the state afterwards.
// cliamp's own calls only toggle, so the current state is read first and the
// call is made only when it differs; everything runs under one lock so two
// quick taps cannot double-toggle.
func (s *Service) Set(ctx context.Context, q SetRequest) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache, s.cacheOK = nil, false
	switch q.Kind {
	case Album, Artist, Playlist:
		return s.setStored(q)
	case Track:
		return s.setTrack(ctx, q)
	case Radio:
		return s.setStation(ctx, q)
	}
	return false, fmt.Errorf("favorites: unknown kind %q", q.Kind)
}

func (s *Service) setStored(q SetRequest) (bool, error) {
	if q.ID == "" || q.Provider == "" {
		return false, errors.New("favorites: id and provider are required")
	}
	idx := -1
	for i, it := range s.stored {
		if it.Kind == q.Kind && it.Provider == q.Provider && it.ID == q.ID {
			idx = i
		}
	}
	switch {
	case q.Favorite && idx < 0:
		now := s.now()
		s.stored = append(s.stored, Item{Kind: q.Kind, Provider: q.Provider, ID: q.ID, Title: q.Title, Subtitle: q.Subtitle, Added: &now})
		if len(s.stored) > maxStored {
			s.stored = s.stored[len(s.stored)-maxStored:]
		}
	case !q.Favorite && idx >= 0:
		s.stored = append(s.stored[:idx], s.stored[idx+1:]...)
	default:
		return q.Favorite, nil
	}
	return q.Favorite, s.save()
}

func (s *Service) trackOn(ctx context.Context, id string) (bool, error) {
	raws, err := s.trackRows(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range raws {
		var t map[string]any
		if json.Unmarshal(r, &t) == nil {
			if _, got := TrackKey(t); got == id {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) setTrack(ctx context.Context, q SetRequest) (bool, error) {
	prov, id := TrackKey(q.Track)
	if id == "" {
		return false, errors.New("favorites: track has no identity")
	}
	on, err := s.trackOn(ctx, id)
	if err != nil {
		return false, err
	}
	if on == q.Favorite {
		return on, nil
	}
	track := make(map[string]any, len(q.Track))
	for k, v := range q.Track {
		track[k] = v
	}
	if p, ok := track["path"].(string); ok {
		track["path"] = s.restore(p) // cliamp needs the real stream URL
	}
	if _, err := s.r.SubmitJob(ctx, "playlist.bookmark", map[string]any{"provider": prov, "track": track}); err != nil {
		return on, err
	}
	return s.trackOn(ctx, id) // trust what cliamp now says, not what we asked for
}

func stationOn(rows []stationRow, id string) bool {
	for _, r := range rows {
		if r.ID != id {
			continue
		}
		return strings.HasPrefix(id, "f:") || strings.HasPrefix(r.Name, starPrefix)
	}
	return false
}

func (s *Service) setStation(ctx context.Context, q SetRequest) (bool, error) {
	if q.ID == "" {
		return false, errors.New("favorites: station id is required")
	}
	rows, err := s.stationRows(ctx)
	if err != nil {
		return false, err
	}
	on := stationOn(rows, q.ID)
	if on == q.Favorite {
		return on, nil
	}
	if _, err := s.r.SubmitJob(ctx, "provider.favorite", map[string]any{"provider": "radio", "playlist": q.ID}); err != nil {
		return on, err
	}
	rows, err = s.stationRows(ctx)
	if err != nil {
		return on, err
	}
	return stationOn(rows, q.ID), nil
}
