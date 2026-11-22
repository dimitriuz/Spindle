package art

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
)

func newResolver(t *testing.T, known func(string) bool, restore func(string) string) *Resolver {
	t.Helper()
	r, err := NewResolver(Options{Dir: t.TempDir(), MaxBytes: 1 << 20, Known: known, Restore: restore})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGetRoutesNavidromeAndLocalByPath(t *testing.T) {
	f := newFakeND(t)
	dir := t.TempDir()
	track := filepath.Join(dir, "01.flac")
	write(t, track, []byte("x"))
	local := pngBytes(t, 3, 3)
	write(t, filepath.Join(dir, "cover.png"), local)

	r := newResolver(t, func(p string) bool { return p == track || p == f.stream("") }, nil)
	r.nd.HTTP = f.srv.Client()

	img, err := r.Get(context.Background(), Cover, f.stream(""))
	if err != nil || img.ContentType != "image/png" {
		t.Fatalf("navidrome: %v %+v", err, img)
	}
	img, err = r.Get(context.Background(), Cover, track)
	if err != nil || !bytes.Equal(img.Data, local) {
		t.Fatalf("local: %v", err)
	}
	if _, err := r.Get(context.Background(), Cover, "https://radio.example/live"); !errors.Is(err, ErrNoArt) {
		t.Fatalf("radio stream: %v", err)
	}
}

func TestGetRestoresScrubbedURLBeforeLookup(t *testing.T) {
	f := newFakeND(t)
	scrubbed := f.srv.URL + "/rest/stream?c=cliamp&id=S1"
	r := newResolver(t, nil, func(s string) string {
		if s == scrubbed {
			return f.stream("")
		}
		return s
	})
	r.nd.HTTP = f.srv.Client()
	if _, err := r.Get(context.Background(), Cover, scrubbed); err != nil {
		t.Fatalf("scrubbed url must be restored to the original: %v", err)
	}
	if f.lastQuery["al-A1"].Get("t") != "TOKEN" {
		t.Fatal("the restored credentials must reach Navidrome")
	}
}

func TestSecondTrackOfAnAlbumIsServedFromCache(t *testing.T) {
	f := newFakeND(t)
	r := newResolver(t, func(p string) bool { return p == f.stream("") }, nil)
	r.nd.HTTP = f.srv.Client()
	for i := 0; i < 3; i++ {
		if _, err := r.Get(context.Background(), Cover, f.stream("")); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.lastQuery) != 1 {
		t.Fatalf("one cover request expected: %v", f.lastQuery)
	}
}

// The route must never become a way to make the bridge call arbitrary hosts:
// only stream URLs the bridge itself showed the browser (scrubbed ones it
// remembers, or tracks it knows) are looked up.
func TestStreamURLOfAnUnknownHostIsRefusedWithoutAnyRequest(t *testing.T) {
	f := newFakeND(t)
	r := newResolver(t, func(string) bool { return false }, nil)
	r.nd.HTTP = f.srv.Client()
	if _, err := r.Get(context.Background(), Cover, f.stream("")); !errors.Is(err, ErrNoArt) {
		t.Fatalf("unknown URL: %v", err)
	}
	if f.songCalls.Load() != 0 || len(f.lastQuery) != 0 {
		t.Fatal("the bridge must not contact a host it was not told about")
	}
}

func http_default() *http.Client { return http.DefaultClient }
