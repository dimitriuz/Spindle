package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"spindle/internal/art"
	"spindle/internal/files"
)

type fakeFolderArt struct {
	img *art.Image
	err error
	rel string
}

func (f *fakeFolderArt) FolderArt(_ context.Context, rel string) (*art.Image, error) {
	f.rel = rel
	return f.img, f.err
}

func folderServer(t *testing.T, fa *fakeFolderArt) http.Handler {
	cfg := guardedConfig(t)
	cfg.FolderArt = fa
	return New(cfg)
}

func TestFolderArtServesAThumbnail(t *testing.T) {
	fa := &fakeFolderArt{img: &art.Image{Data: []byte("\xff\xd8\xff\xe0"), ContentType: "image/jpeg", Key: "f1"}}
	h := folderServer(t, fa)
	rec := artGet(h, "/api/files/art?path=Classic%2FA+B", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("ETag") != `"f1"` {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if fa.rel != "Classic/A B" {
		t.Fatalf("got %q", fa.rel)
	}
	if rec := artGet(h, "/api/files/art?path=x", map[string]string{"If-None-Match": `"f1"`}); rec.Code != 304 {
		t.Fatalf("304: %d", rec.Code)
	}
}

func TestFolderArtStatuses(t *testing.T) {
	for err, want := range map[error]int{art.ErrNoArt: 404, files.ErrNotFound: 404, files.ErrOutside: 400, errors.New("boom"): 404} {
		rec := artGet(folderServer(t, &fakeFolderArt{err: err}), "/api/files/art?path=x", nil)
		if rec.Code != want {
			t.Fatalf("%v: want %d, got %d", err, want, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "boom") {
			t.Fatalf("internal error text must not reach the browser: %s", rec.Body)
		}
	}
	if rec := artGet(New(guardedConfig(t)), "/api/files/art?path=x", nil); rec.Code != 404 {
		t.Fatalf("off: %d", rec.Code)
	}
}
