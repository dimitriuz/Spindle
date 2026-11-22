package art

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeND struct {
	srv       *httptest.Server
	songCalls atomic.Int32
	lastQuery map[string]url.Values // by id
	noDisc    bool
}

func newFakeND(t *testing.T) *fakeND {
	f := &fakeND{lastQuery: map[string]url.Values{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/getSong", func(w http.ResponseWriter, r *http.Request) {
		f.songCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"subsonic-response":{"status":"ok","song":{"id":"S1","albumId":"A1","artistId":"R1"}}}`))
	})
	mux.HandleFunc("/rest/getCoverArt", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		f.lastQuery[id] = r.URL.Query()
		if f.noDisc && strings.HasPrefix(id, "dc-") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":70,"message":"not found"}}}`))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes(t, 8, 8))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeND) stream(extra string) string {
	return f.srv.URL + "/rest/stream?c=cliamp&f=json&id=S1&s=SALT&t=TOKEN&u=bob&v=1.0.0" + extra
}

func fetchBytes(t *testing.T, n *Navidrome, kind Kind, stream string) ([]byte, string, error) {
	t.Helper()
	key, fetch, err := n.Resolve(context.Background(), kind, stream)
	if err != nil {
		return nil, "", err
	}
	b, err := fetch()
	return b, key, err
}

func TestCoverDiscArtistUseTheRightIds(t *testing.T) {
	f := newFakeND(t)
	n := &Navidrome{HTTP: f.srv.Client()}
	for kind, want := range map[Kind]string{Cover: "al-A1", Disc: "dc-A1:1", Artist: "ar-R1"} {
		if _, _, err := fetchBytes(t, n, kind, f.stream("")); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		q := f.lastQuery[want]
		if q == nil {
			t.Fatalf("%s: server never saw id %q (saw %v)", kind, want, f.lastQuery)
		}
		if kind == Disc && q.Get("size") != "" {
			t.Fatalf("disc must be requested at original size, got size=%q", q.Get("size"))
		}
		if kind != Disc && q.Get("size") != "600" {
			t.Fatalf("%s: size=%q", kind, q.Get("size"))
		}
	}
}

func TestAuthParamsAreForwarded(t *testing.T) {
	f := newFakeND(t)
	n := &Navidrome{HTTP: f.srv.Client()}
	if _, _, err := fetchBytes(t, n, Cover, f.stream("")); err != nil {
		t.Fatal(err)
	}
	q := f.lastQuery["al-A1"]
	for k, v := range map[string]string{"u": "bob", "s": "SALT", "t": "TOKEN", "v": "1.0.0", "c": "cliamp"} {
		if q.Get(k) != v {
			t.Fatalf("param %s=%q want %q", k, q.Get(k), v)
		}
	}
}

func TestSongLookupIsCachedPerSongID(t *testing.T) {
	f := newFakeND(t)
	n := &Navidrome{HTTP: f.srv.Client()}
	for _, k := range []Kind{Cover, Disc, Artist, Cover} {
		if _, _, err := fetchBytes(t, n, k, f.stream("")); err != nil {
			t.Fatal(err)
		}
	}
	if f.songCalls.Load() != 1 {
		t.Fatalf("getSong ran %d times", f.songCalls.Load())
	}
}

func TestMissingDiscIsNoArt(t *testing.T) {
	f := newFakeND(t)
	f.noDisc = true
	n := &Navidrome{HTTP: f.srv.Client()}
	_, _, err := fetchBytes(t, n, Disc, f.stream(""))
	if err != ErrNoArt {
		t.Fatalf("json error reply must be ErrNoArt, got %v", err)
	}
}

func TestUpstreamDownIsAnErrorNotNoArt(t *testing.T) {
	f := newFakeND(t)
	stream := f.stream("")
	f.srv.Close()
	n := &Navidrome{HTTP: &http.Client{}}
	_, _, err := fetchBytes(t, n, Cover, stream)
	if err == nil || err == ErrNoArt {
		t.Fatalf("a down server is a transient error, got %v", err)
	}
	for _, secret := range []string{"SALT", "TOKEN", "u=bob"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error text leaks the login (%q): %v", secret, err)
		}
	}
}

func TestNonSubsonicURLIsNoArt(t *testing.T) {
	n := &Navidrome{HTTP: http.DefaultClient}
	for _, u := range []string{"https://radio.example/stream", "/home/me/a.flac", "https://x/rest/stream", ""} {
		if _, _, err := n.Resolve(context.Background(), Cover, u); err != ErrNoArt {
			t.Fatalf("%q: %v", u, err)
		}
	}
}

func TestArtKeyHasNoCredentials(t *testing.T) {
	f := newFakeND(t)
	n := &Navidrome{HTTP: f.srv.Client()}
	_, key, err := fetchBytes(t, n, Cover, f.stream(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SALT", "TOKEN", "bob", "127.0.0.1"} {
		if strings.Contains(key, secret) {
			t.Fatalf("art key leaks %q: %s", secret, key)
		}
	}
	if !strings.Contains(key, "cover") || !strings.Contains(key, "A1") {
		t.Fatalf("key should name kind and album: %s", key)
	}
}
