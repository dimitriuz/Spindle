package art

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultRadioServers are public Radio Browser (radio-browser.info) mirrors;
// the community directory most internet-radio players, cliamp's catalog
// included, take their stations from.
var defaultRadioServers = []string{
	"https://de1.api.radio-browser.info",
	"https://fi1.api.radio-browser.info",
	"https://nl1.api.radio-browser.info",
}

const stationTTL = 10 * time.Minute

// stationRec is what the directory knows about one station.
type stationRec struct {
	Name    string `json:"name"`
	Favicon string `json:"favicon"`
	Codec   string `json:"codec"`
	Bitrate int    `json:"bitrate"`
	Tags    string `json:"tags"`
	Country string `json:"country"`
	Lang    string `json:"language"`
	Votes   int    `json:"votes"`
	Clicks  int    `json:"clickcount"`
	Check   int    `json:"lastcheckok"`
	Home    string `json:"homepage"`
	UUID    string `json:"stationuuid"`
	Stream  string `json:"url_resolved"`
	Raw     string `json:"url"`
}

// StationMeta is what the page shows on a radio tile and filters by.
type StationMeta struct {
	Name     string   `json:"name"` // as asked
	Found    bool     `json:"found"`
	Codec    string   `json:"codec,omitempty"`
	Bitrate  int      `json:"bitrate,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Country  string   `json:"country,omitempty"`
	Language string   `json:"language,omitempty"`
	Votes    int      `json:"votes,omitempty"`
	Clicks   int      `json:"clicks,omitempty"`
	Online   bool     `json:"online"` // the directory's last check of the stream passed
	Homepage string   `json:"homepage,omitempty"`
	Logo     bool     `json:"logo,omitempty"` // the directory has a picture for it
}

func metaOf(name string, rec stationRec, ok bool) StationMeta {
	m := StationMeta{Name: name, Found: ok}
	if !ok {
		return m
	}
	m.Codec, m.Bitrate, m.Country, m.Language = strings.ToUpper(rec.Codec), rec.Bitrate, rec.Country, strings.Title(rec.Lang) //nolint:staticcheck
	m.Votes, m.Clicks, m.Online = rec.Votes, rec.Clicks, rec.Check == 1
	m.Logo = strings.HasPrefix(rec.Favicon, "http")
	if strings.HasPrefix(rec.Home, "http") {
		m.Homepage = rec.Home
	}
	for _, t := range strings.Split(rec.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" && len(t) <= 30 && len(m.Tags) < 8 {
			m.Tags = append(m.Tags, t)
		}
	}
	return m
}

// Stations finds logos and stream details for internet radio: first in the
// Radio Browser directory (by stream URL or by name), then in what the stream
// itself says (ICY headers). Only the directory and the stream host are ever
// contacted; the stream URL is one the bridge was shown (the caller gates it).
type Stations struct {
	HTTP    *http.Client
	Servers []string

	mu     sync.Mutex
	byKey  map[string]stationCached
	favico map[string]string // station uuid -> logo, for the stations a search showed
}

type stationCached struct {
	rec stationRec
	ok  bool
	at  time.Time
}

func (s *Stations) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 8 * time.Second}
}

func (s *Stations) servers() []string {
	if len(s.Servers) > 0 {
		return s.Servers
	}
	return defaultRadioServers
}

func (s *Stations) cached(key string) (stationCached, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byKey[key]
	if !ok || time.Since(c.at) > stationTTL {
		return stationCached{}, false
	}
	return c, true
}

func (s *Stations) remember(key string, rec stationRec, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byKey == nil {
		s.byKey = map[string]stationCached{}
	}
	s.byKey[key] = stationCached{rec, ok, time.Now()}
}

// directory asks the mirrors in turn until one answers.
func (s *Stations) directory(ctx context.Context, path string, q url.Values) ([]stationRec, error) {
	var last error
	for _, base := range s.servers() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "spindle")
		resp, err := s.client().Do(req)
		if err != nil {
			last = errors.New("art: radio directory unreachable")
			continue
		}
		var recs []stationRec
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&recs)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			last = errors.New("art: radio directory error")
			continue
		}
		return recs, nil
	}
	return nil, last
}

func best(recs []stationRec) (stationRec, bool) {
	for _, r := range recs {
		if r.Favicon != "" {
			return r, true
		}
	}
	if len(recs) > 0 {
		return recs[0], true
	}
	return stationRec{}, false
}

// ByURL looks a station up by its stream address.
func (s *Stations) ByURL(ctx context.Context, streamURL string) (stationRec, bool, error) {
	key := "u|" + streamURL
	if c, ok := s.cached(key); ok {
		return c.rec, c.ok, nil
	}
	recs, err := s.directory(ctx, "/json/stations/byurl", url.Values{"url": {streamURL}})
	if err != nil {
		return stationRec{}, false, err
	}
	rec, ok := best(recs)
	s.remember(key, rec, ok)
	return rec, ok, nil
}

var (
	reBitrate = regexp.MustCompile(`\s*\[\d+k\]`)
	reCountry = regexp.MustCompile(`\s+·\s+[^·]+$`)
)

// CleanStationName strips what cliamp's catalog adds to a station name: the
// favorite star, the "[128k]" bitrate and the " · Country" suffix.
func CleanStationName(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "★"))
	s = reCountry.ReplaceAllString(s, "")
	s = reBitrate.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// ByName looks a station up by its (cleaned) name, the most voted exact match first.
func (s *Stations) ByName(ctx context.Context, name string) (stationRec, bool, error) {
	name = CleanStationName(name)
	if name == "" || len(name) > 120 {
		return stationRec{}, false, nil
	}
	key := "n|" + strings.ToLower(name)
	if c, ok := s.cached(key); ok {
		return c.rec, c.ok, nil
	}
	q := url.Values{"name": {name}, "nameExact": {"true"}, "order": {"votes"}, "reverse": {"true"}, "hidebroken": {"true"}, "limit": {"5"}}
	recs, err := s.directory(ctx, "/json/stations/search", q)
	if err != nil {
		return stationRec{}, false, err
	}
	rec, ok := best(recs)
	s.remember(key, rec, ok)
	return rec, ok, nil
}

// streamHeaders opens the stream just far enough to read its response headers.
func (s *Stations) streamHeaders(ctx context.Context, streamURL string) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("User-Agent", "spindle")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, errors.New("art: stream unreachable") // not the url error: it quotes the address
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errors.New("art: stream refused")
	}
	return resp.Header.Clone(), nil
}

// Logo finds the picture of the station behind a stream URL.
func (s *Stations) Logo(ctx context.Context, streamURL, name string) (string, error) {
	rec, ok, err := s.ByURL(ctx, streamURL)
	if err == nil && ok && rec.Favicon != "" {
		return rec.Favicon, nil
	}
	if h, herr := s.streamHeaders(ctx, streamURL); herr == nil {
		if l := h.Get("icy-logo"); strings.HasPrefix(l, "http") {
			return l, nil
		}
	}
	if name != "" {
		if rec, ok, nerr := s.ByName(ctx, name); nerr == nil && ok && rec.Favicon != "" {
			return rec.Favicon, nil
		}
	}
	if err != nil && !errors.Is(err, ErrNoArt) {
		return "", err
	}
	return "", ErrNoArt
}

// Image downloads a logo: http(s) only, size capped, must really be a picture.
func (s *Stations) Image(ctx context.Context, logoURL string) ([]byte, error) {
	u, err := url.Parse(logoURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrNoArt
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "spindle")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, errors.New("art: logo unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, ErrNoArt
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("art: logo http " + strconv.Itoa(resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxImageBytes || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return nil, ErrNoArt
	}
	return data, nil
}

var formatByType = map[string]string{
	"audio/mpeg": "MP3", "audio/mp3": "MP3", "audio/aac": "AAC", "audio/aacp": "AAC+", "audio/mp4": "AAC",
	"audio/ogg": "OGG", "application/ogg": "OGG", "audio/opus": "OPUS", "audio/flac": "FLAC", "audio/x-flac": "FLAC",
}

// Info reports what a stream says about itself: format from the content type,
// bitrate from icy-br, and either of them from the directory when missing.
func (s *Stations) Info(ctx context.Context, streamURL string) (Info, error) {
	var info Info
	if h, err := s.streamHeaders(ctx, streamURL); err == nil {
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(h.Get("Content-Type"), ";", 2)[0]))
		info.Format = formatByType[ct]
		br := strings.TrimSpace(strings.SplitN(h.Get("icy-br"), ",", 2)[0])
		if n, err := strconv.Atoi(br); err == nil && n > 0 && n < 10000 {
			info.BitrateKbps = n
		}
		if sr, err := strconv.Atoi(strings.TrimSpace(h.Get("icy-sr"))); err == nil && sr > 0 {
			info.SampleRate = sr
		}
	}
	if info.Format == "" || info.BitrateKbps == 0 {
		if rec, ok, _ := s.ByURL(ctx, streamURL); ok {
			if info.Format == "" {
				info.Format = strings.ToUpper(rec.Codec)
			}
			if info.BitrateKbps == 0 && rec.Bitrate > 0 {
				info.BitrateKbps = rec.Bitrate
			}
		}
	}
	if info == (Info{}) {
		return Info{}, ErrNoInfo
	}
	return info, nil
}

// Meta looks up many stations by name, a few at a time. Names the directory
// does not know come back with Found false; one failure never fails the rest.
func (s *Stations) Meta(ctx context.Context, names []string) []StationMeta {
	out := make([]StationMeta, len(names))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rec, ok, err := s.ByName(ctx, n)
			out[i] = metaOf(n, rec, ok && err == nil)
		}(i, n)
	}
	wg.Wait()
	return out
}

// StationHit is one station found by a search: enough to show and to play.
type StationHit struct {
	ID  string `json:"id"` // directory id; the logo route takes it
	URL string `json:"url"`
	StationMeta
}

// Search looks stations up by name, tag and country at once and merges them,
// the most voted first. It remembers each hit's logo address so the logo route
// can serve it by id without taking an address from the browser.
func (s *Stations) Search(ctx context.Context, q string) []StationHit {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 100 {
		return nil
	}
	base := url.Values{"hidebroken": {"true"}, "order": {"votes"}, "reverse": {"true"}, "limit": {"40"}}
	results := make([][]stationRec, 3)
	var wg sync.WaitGroup
	for i, field := range []string{"name", "tag", "country"} {
		wg.Add(1)
		go func(i int, field string) {
			defer wg.Done()
			v := url.Values{field: {q}}
			for k, vv := range base {
				v[k] = vv
			}
			results[i], _ = s.directory(ctx, "/json/stations/search", v)
		}(i, field)
	}
	wg.Wait()
	seen := map[string]bool{}
	var hits []StationHit
	for _, recs := range results {
		for _, r := range recs {
			u := r.Stream
			if u == "" {
				u = r.Raw
			}
			if r.UUID == "" || seen[r.UUID] || !strings.HasPrefix(u, "http") {
				continue
			}
			seen[r.UUID] = true
			hits = append(hits, StationHit{ID: r.UUID, URL: u, StationMeta: metaOf(r.Name, r, true)})
			if strings.HasPrefix(r.Favicon, "http") {
				s.mu.Lock()
				if s.favico == nil {
					s.favico = map[string]string{}
				}
				if len(s.favico) > 5000 {
					s.favico = map[string]string{}
				}
				s.favico[r.UUID] = r.Favicon
				s.mu.Unlock()
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Votes > hits[j].Votes })
	if len(hits) > 60 {
		hits = hits[:60]
	}
	return hits
}

// FaviconOf is the logo address remembered for a station a search returned.
func (s *Stations) FaviconOf(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.favico[id]
	return u, ok
}
