package art

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
}

// Stations finds logos and stream details for internet radio: first in the
// Radio Browser directory (by stream URL or by name), then in what the stream
// itself says (ICY headers). Only the directory and the stream host are ever
// contacted; the stream URL is one the bridge was shown (the caller gates it).
type Stations struct {
	HTTP    *http.Client
	Servers []string

	mu    sync.Mutex
	byKey map[string]stationCached
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
		req.Header.Set("User-Agent", "cliamp-web")
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
	req.Header.Set("User-Agent", "cliamp-web")
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
	req.Header.Set("User-Agent", "cliamp-web")
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
