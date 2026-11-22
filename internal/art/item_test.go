package art

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func itemResolver(t *testing.T, f *fakeND, password string) *Resolver {
	t.Helper()
	r := newResolver(t, nil, nil)
	r.nd.HTTP = f.srv.Client()
	r.SetNavidrome(NavidromeConfig{URL: f.srv.URL, User: "bob", Password: password})
	return r
}

func TestItemArtUsesTheRightIdsAndSignsWithAToken(t *testing.T) {
	f := newFakeND(t)
	r := itemResolver(t, f, "s3cret!")
	for kind, id := range map[string]string{"album": "al-A1", "artist": "ar-R1", "playlist": "pl-P1"} {
		suffix := strings.SplitN(id, "-", 2)[1]
		img, err := r.ItemArt(context.Background(), kind, suffix)
		if err != nil || img.ContentType != "image/png" {
			t.Fatalf("%s: %v %+v", kind, err, img)
		}
		q := f.lastQuery[id]
		if q == nil {
			t.Fatalf("%s: server never saw id %q (saw %v)", kind, id, f.lastQuery)
		}
		if q.Get("size") != "300" || q.Get("u") != "bob" || q.Get("v") == "" || q.Get("c") == "" {
			t.Fatalf("%s: query %v", kind, q)
		}
		sum := md5.Sum([]byte("s3cret!" + q.Get("s")))
		if q.Get("s") == "" || q.Get("t") != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: token must be md5(password+salt): %v", kind, q)
		}
		if q.Get("p") != "" || strings.Contains(q.Encode(), "s3cret") {
			t.Fatalf("%s: the password itself must never be sent: %v", kind, q)
		}
	}
}

func TestItemArtIsCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes(t, 8, 8))
	}))
	defer srv.Close()
	r := newResolver(t, nil, nil)
	r.SetNavidrome(NavidromeConfig{URL: srv.URL, User: "u", Password: "p"})
	for i := 0; i < 3; i++ {
		if _, err := r.ItemArt(context.Background(), "album", "A1"); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d requests for one album", hits.Load())
	}
}

func TestItemArtUnconfiguredOrInvalidIsNoArt(t *testing.T) {
	r := newResolver(t, nil, nil)
	if _, err := r.ItemArt(context.Background(), "album", "A1"); !errors.Is(err, ErrNoArt) {
		t.Fatalf("no login configured: %v", err)
	}
	f := newFakeND(t)
	r = itemResolver(t, f, "pw")
	for _, c := range [][2]string{{"track", "A1"}, {"", "A1"}, {"album", ""}, {"album", "a/b"}, {"album", "a?b=1"}, {"album", ".."}, {"album", "a\nb"}, {"album", "a&u=evil"}, {"album", strings.Repeat("x", 81)}} {
		if _, err := r.ItemArt(context.Background(), c[0], c[1]); !errors.Is(err, ErrNoArt) {
			t.Fatalf("%v: want ErrNoArt, got %v", c, err)
		}
	}
	if len(f.lastQuery) != 0 {
		t.Fatalf("an invalid id must never reach the server: %v", f.lastQuery)
	}
}

func TestItemArtServerDownIsATransientErrorWithoutTheLogin(t *testing.T) {
	f := newFakeND(t)
	r := itemResolver(t, f, "topsecret")
	f.srv.Close()
	_, err := r.ItemArt(context.Background(), "album", "A1")
	if err == nil || errors.Is(err, ErrNoArt) {
		t.Fatalf("a down server is transient, got %v", err)
	}
	for _, s := range []string{"topsecret", "u=bob", "t="} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error text leaks %q: %v", s, err)
		}
	}
}

func TestItemArtNonImageReplyIsNoArt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"subsonic-response":{"status":"failed"}}`))
	}))
	defer srv.Close()
	r := newResolver(t, nil, nil)
	r.SetNavidrome(NavidromeConfig{URL: srv.URL, User: "u", Password: "p"})
	if _, err := r.ItemArt(context.Background(), "album", "A1"); !errors.Is(err, ErrNoArt) {
		t.Fatalf("%v", err)
	}
}

func TestItemArtCacheKeyHasNoLogin(t *testing.T) {
	f := newFakeND(t)
	r := itemResolver(t, f, "topsecret")
	img, err := r.ItemArt(context.Background(), "album", "A1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(img.Key, "topsecret") || strings.Contains(img.Key, "bob") {
		t.Fatalf("etag key %q", img.Key)
	}
}
