package server

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cliamp-web/internal/files"
)

type fakeFiles struct {
	root string
	got  string
	l    files.Listing
	err  error
}

func (f *fakeFiles) RootName() string { return f.root }
func (f *fakeFiles) Groups() map[string]string {
	return map[string]string{"земфира": "Rus"}
}
func (f *fakeFiles) List(rel string) (files.Listing, error) {
	f.got = rel
	return f.l, f.err
}

func filesServer(t *testing.T, src FilesSource) http.Handler {
	cfg := guardedConfig(t)
	cfg.Files = src
	return New(cfg)
}

func TestFilesListShape(t *testing.T) {
	ff := &fakeFiles{root: "music", l: files.Listing{Path: "Classic", Parent: "", Name: "Classic", Entries: []files.Entry{{Name: "Vivaldi", Dir: true}, {Name: "a.flac"}}}}
	rec := artGet(filesServer(t, ff), "/api/files?path=Classic", nil)
	b := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, `"path":"Classic"`) || !strings.Contains(b, `{"name":"Vivaldi","dir":true}`) || !strings.Contains(b, `{"name":"a.flac","dir":false}`) {
		t.Fatalf("%d %s", rec.Code, b)
	}
	if ff.got != "Classic" {
		t.Fatalf("source got %q", ff.got)
	}
	if artGet(filesServer(t, ff), "/api/files", nil); ff.got != "" {
		t.Fatalf("no path means the root, got %q", ff.got)
	}
}

func TestFilesPathSurvivesTheURL(t *testing.T) {
	for _, name := range []string{"Beyoncé/Lemonade", "a b/c#d/e?f/g%h", "日本語/曲", "100% & more=x"} {
		ff := &fakeFiles{l: files.Listing{Entries: []files.Entry{}}}
		q := url.Values{"path": {name}}.Encode()
		if rec := artGet(filesServer(t, ff), "/api/files?"+q, nil); rec.Code != 200 || ff.got != name {
			t.Fatalf("%q: %d got %q", name, rec.Code, ff.got)
		}
	}
}

func TestFilesErrorsMapToStatuses(t *testing.T) {
	for err, want := range map[error]int{files.ErrOutside: 400, files.ErrNotFound: 404, errors.New("boom"): 500} {
		rec := artGet(filesServer(t, &fakeFiles{err: err}), "/api/files?path=x", nil)
		if rec.Code != want {
			t.Fatalf("%v: want %d, got %d", err, want, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "boom") {
			t.Fatalf("internal error text must not reach the browser: %s", rec.Body)
		}
	}
}

func TestFilesRoot(t *testing.T) {
	rec := artGet(filesServer(t, &fakeFiles{root: "music"}), "/api/files/root", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"music"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestFilesOffMeans404Everywhere(t *testing.T) {
	h := New(guardedConfig(t))
	for _, p := range []string{"/api/files", "/api/files?path=x", "/api/files/root"} {
		if rec := artGet(h, p, nil); rec.Code != 404 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
}

// Whatever the real browser lists, the body shows relative names only.
func TestFilesBodyNeverShowsAnAbsolutePath(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "Album"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "Album", "a.flac"), []byte("x"), 0o644)
	b, err := files.New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/files", "/api/files?path=Album", "/api/files/root"} {
		body := artGet(filesServer(t, b), p, nil).Body.String()
		if strings.Contains(body, root) || strings.Contains(body, filepath.Dir(root)) {
			t.Fatalf("%s leaks a location: %s", p, body)
		}
	}
	if rec := artGet(filesServer(t, b), "/api/files?path=..%2F..", nil); rec.Code != 400 {
		t.Fatalf("traversal: %d", rec.Code)
	}
}

func TestFilesGroupsRoute(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.Files = &fakeFiles{root: "music"}
	rec := artGet(New(cfg), "/api/files/groups", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"земфира":"Rus"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := artGet(New(guardedConfig(t)), "/api/files/groups", nil); rec.Code != 404 {
		t.Fatalf("feature off: %d", rec.Code)
	}
}
