package art

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// authParams are the Subsonic credential/identity parameters copied from the
// stream URL onto every request we make. They never leave this process.
var authParams = []string{"u", "s", "t", "p", "v", "c", "apiKey"}

// parseStream recognises <base>/rest/stream?id=<song> and returns the server
// base URL, the song id and the auth parameters it carries.
func parseStream(streamURL string) (base, songID string, auth url.Values, ok bool) {
	u, err := url.Parse(streamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", nil, false
	}
	if !strings.HasSuffix(u.Path, "/rest/stream") {
		return "", "", nil, false
	}
	q := u.Query()
	songID = q.Get("id")
	if songID == "" {
		return "", "", nil, false
	}
	auth = url.Values{}
	for _, k := range authParams {
		if v := q.Get(k); v != "" {
			auth.Set(k, v)
		}
	}
	base = u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/stream")
	return base, songID, auth, true
}

// Navidrome fetches art through the Subsonic API of the server a stream URL
// points at. It never contacts any other host.
type Navidrome struct {
	HTTP *http.Client

	mu    sync.Mutex
	songs map[string]songMeta
}

// songMeta is what one getSong call tells us; art needs the ids, the info
// route needs the technical fields.
type songMeta struct {
	album, artist string
	info          Info
}

const maxSongs = 4096

// newClient is the default client: bounded wait, and a redirect may not leave
// the host the request started on.
func newClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func (n *Navidrome) client() *http.Client {
	if n.HTTP != nil {
		return n.HTTP
	}
	return newClient()
}

func (n *Navidrome) endpoint(base, method string, auth url.Values, extra url.Values) string {
	q := url.Values{}
	for k, v := range auth {
		q[k] = v
	}
	for k, v := range extra {
		q[k] = v
	}
	return base + "/" + method + "?" + q.Encode()
}

func (n *Navidrome) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := n.client().Do(req)
	var ue *url.Error
	if errors.As(err, &ue) {
		// net/http errors quote the whole URL, query included: that is the login
		return nil, fmt.Errorf("art: request failed: %w", ue.Err)
	}
	return resp, err
}

// lookup resolves a song id to its album and artist ids (cached).
func (n *Navidrome) lookup(ctx context.Context, base, songID string, auth url.Values) (songMeta, error) {
	ck := base + "|" + songID
	n.mu.Lock()
	if ids, ok := n.songs[ck]; ok {
		n.mu.Unlock()
		return ids, nil
	}
	n.mu.Unlock()
	resp, err := n.get(ctx, n.endpoint(base, "getSong", auth, url.Values{"id": {songID}, "f": {"json"}}))
	if err != nil {
		return songMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return songMeta{}, fmt.Errorf("art: getSong: http %d", resp.StatusCode)
	}
	var body struct {
		R struct {
			Status string `json:"status"`
			Song   struct {
				AlbumID      string `json:"albumId"`
				ArtistID     string `json:"artistId"`
				Suffix       string `json:"suffix"`
				BitRate      int    `json:"bitRate"`
				BitDepth     int    `json:"bitDepth"`
				SamplingRate int    `json:"samplingRate"`
				ChannelCount int    `json:"channelCount"`
			} `json:"song"`
		} `json:"subsonic-response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.R.Status != "ok" {
		return songMeta{}, ErrNoArt
	}
	sg := body.R.Song
	ids := songMeta{album: sg.AlbumID, artist: sg.ArtistID, info: Info{
		Format: strings.ToUpper(sg.Suffix), BitrateKbps: sg.BitRate, BitDepth: sg.BitDepth,
		SampleRate: sg.SamplingRate, Channels: sg.ChannelCount,
	}}
	n.mu.Lock()
	if n.songs == nil || len(n.songs) >= maxSongs {
		n.songs = map[string]songMeta{}
	}
	n.songs[ck] = ids
	n.mu.Unlock()
	return ids, nil
}

// Resolve identifies the picture for kind and returns a stable, credential-free
// key for it plus a function that downloads the bytes.
func (n *Navidrome) Resolve(ctx context.Context, kind Kind, streamURL string) (string, func() ([]byte, error), error) {
	base, songID, auth, ok := parseStream(streamURL)
	if !ok {
		return "", nil, ErrNoArt
	}
	ids, err := n.lookup(ctx, base, songID, auth)
	if err != nil {
		return "", nil, err
	}
	var artID, id string
	q := url.Values{}
	switch kind {
	case Cover:
		id, artID = ids.album, "al-"+ids.album
		q.Set("size", "600")
	case Disc:
		id, artID = ids.album, "dc-"+ids.album+":1"
	case Artist:
		id, artID = ids.artist, "ar-"+ids.artist
		q.Set("size", "600")
	default:
		return "", nil, ErrNoArt
	}
	if id == "" {
		return "", nil, ErrNoArt
	}
	q.Set("id", artID)
	srv := sha256.Sum256([]byte(base))
	kname := string(kind)
	if kind == Disc {
		kname = "disc2" // v2: earlier builds cached the album cover Navidrome sends when there is no disc
	}
	key := "nd:" + hex.EncodeToString(srv[:4]) + ":" + kname + ":" + id
	target := n.endpoint(base, "getCoverArt", auth, q)
	return key, func() ([]byte, error) {
		resp, err := n.get(ctx, target)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
			return nil, ErrNoArt
		case resp.StatusCode >= 500:
			return nil, fmt.Errorf("art: getCoverArt: http %d", resp.StatusCode)
		case resp.StatusCode != http.StatusOK:
			return nil, ErrNoArt
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > MaxImageBytes || !strings.HasPrefix(http.DetectContentType(data), "image/") {
			return nil, ErrNoArt // an error document or an oversize file is not art
		}
		if kind == Disc && !looksLikeDisc(data) {
			return nil, ErrNoArt // Navidrome answers dc- ids with the album cover when there is no disc.png
		}
		return data, nil
	}, nil
}

// Info returns the technical details Navidrome holds for the stream's song.
func (n *Navidrome) Info(ctx context.Context, streamURL string) (Info, error) {
	base, songID, auth, ok := parseStream(streamURL)
	if !ok {
		return Info{}, ErrNoInfo
	}
	m, err := n.lookup(ctx, base, songID, auth)
	if errors.Is(err, ErrNoArt) {
		return Info{}, ErrNoInfo
	}
	if err != nil {
		return Info{}, err
	}
	if m.info == (Info{}) {
		return Info{}, ErrNoInfo
	}
	return m.info, nil
}

// looksLikeDisc tells a disc image from the album cover Navidrome sends in its
// place: a disc is a PNG whose four corners are transparent.
func looksLikeDisc(data []byte) bool {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return false // not a PNG (covers are usually JPEG)
	}
	b := img.Bounds()
	for _, p := range []image.Point{{b.Min.X, b.Min.Y}, {b.Max.X - 1, b.Min.Y}, {b.Min.X, b.Max.Y - 1}, {b.Max.X - 1, b.Max.Y - 1}} {
		if _, _, _, a := img.At(p.X, p.Y).RGBA(); a > 0x0fff { // more than ~6% opaque
			return false
		}
	}
	return true
}
