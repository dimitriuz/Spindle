package art

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRadio is one server playing both parts: the radio directory under /json
// and a stream at /live with ICY headers, plus the logo at /logo.png.
type fakeRadio struct {
	srv                       *httptest.Server
	dirHits, logoHits         int32
	icyLogo, icyBr, ctype     string
	byURLFavicon, nameFavicon string
	codec                     string
	bitrate                   int
}

func newFakeRadio(t *testing.T) *fakeRadio {
	f := &fakeRadio{ctype: "audio/mpeg", icyBr: "128", codec: "MP3", bitrate: 96}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/stations/byurl", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.dirHits, 1)
		w.Write([]byte(`[{"name":"X","favicon":"` + f.byURLFavicon + `","codec":"` + f.codec + `","bitrate":` + strconv.Itoa(f.bitrate) + `}]`))
	})
	mux.HandleFunc("/json/stations/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.dirHits, 1)
		if r.URL.Query().Get("name") != "Classic Vinyl HD" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(`[{"name":"Classic Vinyl HD","favicon":"` + f.nameFavicon + `"}]`))
	})
	mux.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Icy-MetaData") != "1" {
			t.Error("stream asked without Icy-MetaData")
		}
		w.Header().Set("Content-Type", f.ctype)
		if f.icyBr != "" {
			w.Header().Set("icy-br", f.icyBr)
		}
		if f.icyLogo != "" {
			w.Header().Set("icy-logo", f.icyLogo)
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for i := 0; i < 1000 && r.Context().Err() == nil; i++ { // an endless stream: the reader must hang up
			w.Write(make([]byte, 1024))
		}
	})
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.logoHits, 1)
		w.Write(pngBytes(t, 8, 8))
	})
	mux.HandleFunc("/page.html", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>not a picture</html>")) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func radioResolver(t *testing.T, f *fakeRadio) (*Resolver, string) {
	stream := f.srv.URL + "/live"
	r := newResolver(t, func(p string) bool { return p == stream }, nil)
	r.st = &Stations{HTTP: f.srv.Client(), Servers: []string{f.srv.URL}}
	return r, stream
}

func TestStationCoverComesFromTheDirectoryByStreamURL(t *testing.T) {
	f := newFakeRadio(t)
	f.byURLFavicon = f.srv.URL + "/logo.png"
	r, stream := radioResolver(t, f)
	img, err := r.Get(context.Background(), Cover, stream)
	if err != nil || img.ContentType != "image/png" {
		t.Fatalf("%v %+v", err, img)
	}
	if _, err := r.Get(context.Background(), Cover, stream); err != nil || f.logoHits != 1 {
		t.Fatalf("second call must be cached: err %v, logo fetched %d times", err, f.logoHits)
	}
}

func TestStationCoverFallsBackToTheStreamsIcyLogo(t *testing.T) {
	f := newFakeRadio(t)
	f.icyLogo = f.srv.URL + "/logo.png"
	r, stream := radioResolver(t, f)
	if _, err := r.Get(context.Background(), Cover, stream); err != nil {
		t.Fatal(err)
	}
}

func TestStationHasNoDiscOrArtistAndNoSyntheticDisc(t *testing.T) {
	f := newFakeRadio(t)
	f.byURLFavicon = f.srv.URL + "/logo.png"
	r, stream := radioResolver(t, f)
	for _, k := range []Kind{Disc, Artist} {
		if _, err := r.Get(context.Background(), k, stream); !errors.Is(err, ErrNoArt) {
			t.Fatalf("%s: %v", k, err)
		}
	}
}

func TestStationUnknownToTheBridgeIsRefusedWithoutAnyRequest(t *testing.T) {
	f := newFakeRadio(t)
	f.byURLFavicon = f.srv.URL + "/logo.png"
	r, _ := radioResolver(t, f)
	if _, err := r.Get(context.Background(), Cover, f.srv.URL+"/other"); !errors.Is(err, ErrNoArt) {
		t.Fatal(err)
	}
	if _, err := r.Info(context.Background(), f.srv.URL+"/other", 0); err == nil {
		t.Fatal("info for an unknown stream")
	}
	if f.dirHits != 0 {
		t.Fatal("directory was asked about a stream the bridge was never shown")
	}
}

func TestStationLogoThatIsNotAPictureIsNoArt(t *testing.T) {
	f := newFakeRadio(t)
	f.byURLFavicon = f.srv.URL + "/page.html"
	r, stream := radioResolver(t, f)
	if _, err := r.Get(context.Background(), Cover, stream); !errors.Is(err, ErrNoArt) {
		t.Fatal(err)
	}
}

func TestStationArtByNameStripsCatalogDecorations(t *testing.T) {
	f := newFakeRadio(t)
	f.nameFavicon = f.srv.URL + "/logo.png"
	r, _ := radioResolver(t, f)
	for _, n := range []string{"Classic Vinyl HD [320k] · United States Of America", "★ Classic Vinyl HD", "Classic Vinyl HD"} {
		if _, err := r.StationArt(context.Background(), n); err != nil {
			t.Fatalf("%q: %v", n, err)
		}
	}
	if _, err := r.StationArt(context.Background(), "Nobody FM"); !errors.Is(err, ErrNoArt) {
		t.Fatal(err)
	}
	if _, err := r.StationArt(context.Background(), " · "); !errors.Is(err, ErrNoArt) {
		t.Fatal("empty name")
	}
}

func TestCleanStationName(t *testing.T) {
	for in, want := range map[string]string{
		"MANGORADIO [128k] · Germany":  "MANGORADIO",
		"★ 101 SMOOTH JAZZ [192k]":     "101 SMOOTH JAZZ",
		"RFE/RL Radio Farda · Czechia": "RFE/RL Radio Farda",
		"Dance Wave! | MP3 [128k]":     "Dance Wave! | MP3",
		"Plain":                        "Plain",
	} {
		if got := CleanStationName(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestStationInfoReadsStreamHeaders(t *testing.T) {
	f := newFakeRadio(t)
	r, stream := radioResolver(t, f)
	info, err := r.Info(context.Background(), stream, 0)
	if err != nil || info.Format != "MP3" || info.BitrateKbps != 128 {
		t.Fatalf("%v %+v", err, info)
	}
}

func TestStationInfoFillsGapsFromTheDirectory(t *testing.T) {
	f := newFakeRadio(t)
	f.ctype, f.icyBr = "application/octet-stream", ""
	f.codec, f.bitrate = "AAC+", 64
	r, stream := radioResolver(t, f)
	info, err := r.Info(context.Background(), stream, 0)
	if err != nil || info.Format != "AAC+" || info.BitrateKbps != 64 {
		t.Fatalf("%v %+v", err, info)
	}
}

func TestStationErrorsNeverQuoteTheStreamURL(t *testing.T) {
	f := newFakeRadio(t)
	r, _ := radioResolver(t, f)
	secret := "http://127.0.0.1:1/live?token=SECRET"
	r.known = func(string) bool { return true }
	_, err := r.Get(context.Background(), Cover, secret)
	if err != nil && strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks the url: %v", err)
	}
	if _, err := r.Info(context.Background(), secret, 0); err != nil && strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("info error leaks the url: %v", err)
	}
}

func TestStationSearchMergesNameTagAndCountryAndRemembersLogos(t *testing.T) {
	var qmu sync.Mutex
	var queries []string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/json/stations/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for _, f := range []string{"name", "tag", "country"} {
			if q.Get(f) != "" {
				qmu.Lock()
				queries = append(queries, f)
				qmu.Unlock()
			}
		}
		if q.Get("hidebroken") != "true" || q.Get("limit") == "" {
			t.Error("search must hide broken stations and be limited")
		}
		switch {
		case q.Get("name") != "":
			w.Write([]byte(`[{"stationuuid":"a","name":"Jazz FM","url_resolved":"http://a/live","votes":5,"lastcheckok":1,"tags":"jazz,swing","favicon":"` + srv.URL + `/logo.png"}]`))
		case q.Get("tag") != "":
			w.Write([]byte(`[{"stationuuid":"a","name":"Jazz FM","url_resolved":"http://a/live","votes":5},{"stationuuid":"b","name":"Smooth","url":"http://b/live","votes":50,"lastcheckok":1},{"stationuuid":"c","name":"No stream","url_resolved":"ftp://c"}]`))
		default:
			w.Write([]byte(`[]`))
		}
	})
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) { w.Write(pngBytes(t, 4, 4)) })
	r := newResolver(t, nil, nil)
	r.st = &Stations{HTTP: srv.Client(), Servers: []string{srv.URL}}
	hits := r.StationSearch(context.Background(), "jazz")
	if len(hits) != 2 || hits[0].ID != "b" || hits[1].ID != "a" {
		t.Fatalf("want b (more votes) then a, deduplicated, without the non-http stream: %+v", hits)
	}
	if hits[1].URL != "http://a/live" || hits[1].Tags[0] != "jazz" || !hits[1].Online || hits[0].URL != "http://b/live" {
		t.Fatalf("%+v", hits)
	}
	if len(queries) != 3 {
		t.Fatalf("name, tag and country must all be asked: %v", queries)
	}
	if img, err := r.StationArtByID(context.Background(), "a"); err != nil || img.ContentType != "image/png" {
		t.Fatalf("logo by id: %v %+v", err, img)
	}
	if _, err := r.StationArtByID(context.Background(), "zzz"); !errors.Is(err, ErrNoArt) {
		t.Fatalf("an id no search returned must not be fetched: %v", err)
	}
	if got := r.StationSearch(context.Background(), "  "); got != nil {
		t.Fatal("blank query")
	}
}

func TestStationMetaTellsFoundFromUnknown(t *testing.T) {
	f := newFakeRadio(t)
	f.nameFavicon = "x"
	r, _ := radioResolver(t, f)
	got := r.StationMeta(context.Background(), []string{"Classic Vinyl HD [320k] · USA", "Nobody FM"})
	if len(got) != 2 || !got[0].Found || got[1].Found || got[1].Name != "Nobody FM" {
		t.Fatalf("%+v", got)
	}
}
