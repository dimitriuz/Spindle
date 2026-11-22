package art

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"spindle/internal/files"
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

type fakeCovers struct {
	abs   string
	err   error
	calls int
}

func (f *fakeCovers) CoverFile(string) (string, error) { f.calls++; return f.abs, f.err }

func TestFolderArtMakesACachedThumbnailAndFollowsTheFile(t *testing.T) {
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.png")
	write(t, cover, gradientPNG(t, 900, 900))
	fc := &fakeCovers{abs: cover}
	r := newResolver(t, nil, nil)
	r.SetFolders(fc)
	img, err := r.FolderArt(context.Background(), "A")
	if err != nil || img.ContentType != "image/jpeg" {
		t.Fatalf("%v %+v", err, img)
	}
	if w, h := jpegSize(t, img.Data); w != 300 || h != 300 {
		t.Fatalf("%dx%d", w, h)
	}
	// served from the cache while the file is unchanged, re-made when it changes
	k1 := img.Key
	img2, _ := r.FolderArt(context.Background(), "A")
	if img2.Key != k1 {
		t.Fatal("same file, same picture")
	}
	write(t, cover, gradientPNG(t, 450, 450))
	img3, err := r.FolderArt(context.Background(), "A")
	if err != nil || img3.Key == k1 {
		t.Fatalf("a replaced cover must be re-thumbnailed: %v", err)
	}
	if w, _ := jpegSize(t, img3.Data); w != 300 {
		t.Fatalf("width %d", w)
	}
}

func TestFolderArtErrors(t *testing.T) {
	r := newResolver(t, nil, nil)
	if _, err := r.FolderArt(context.Background(), "A"); !errors.Is(err, ErrNoArt) {
		t.Fatalf("not configured: %v", err)
	}
	r.SetFolders(&fakeCovers{err: files.ErrOutside})
	if _, err := r.FolderArt(context.Background(), "../x"); !errors.Is(err, files.ErrOutside) {
		t.Fatalf("an escape stays an escape: %v", err)
	}
	r.SetFolders(&fakeCovers{err: files.ErrNotFound})
	if _, err := r.FolderArt(context.Background(), "A"); !errors.Is(err, ErrNoArt) {
		t.Fatalf("no cover: %v", err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "cover.jpg")
	write(t, bad, []byte("not an image"))
	r.SetFolders(&fakeCovers{abs: bad})
	if _, err := r.FolderArt(context.Background(), "A"); !errors.Is(err, ErrNoArt) {
		t.Fatalf("an undecodable cover is no art: %v", err)
	}
}
