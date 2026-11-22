package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		p := filepath.Join(root, filepath.FromSlash(r))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newBrowser(t *testing.T, rels ...string) (*Browser, string) {
	t.Helper()
	root := t.TempDir()
	touch(t, root, rels...)
	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return b, root
}

func names(l Listing) string {
	var out []string
	for _, e := range l.Entries {
		n := e.Name
		if e.Dir {
			n += "/"
		}
		out = append(out, n)
	}
	return strings.Join(out, " ")
}

func TestListSortsDirsFirstThenNaturally(t *testing.T) {
	b, _ := newBrowser(t, "Track 10.flac", "track 2.flac", "Track 1.flac", "Zebra/a.flac", "apple/a.flac", "Mango/a.flac")
	l, err := b.List("")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(l), "apple/ Mango/ Zebra/ Track 1.flac track 2.flac Track 10.flac"; got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestListHidesDotfilesAndNonAudio(t *testing.T) {
	b, _ := newBrowser(t, "a.flac", "cover.jpg", "notes.txt", ".hidden.flac", ".git/config", "b.MP3", "sub/c.ogg")
	l, _ := b.List("")
	if got, want := names(l), "sub/ a.flac b.MP3"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestListParentAndName(t *testing.T) {
	b, _ := newBrowser(t, "Classic/Vivaldi/Seasons/01.flac")
	for rel, want := range map[string][2]string{
		"":                          {"", b.RootName()},
		"Classic":                   {"", "Classic"},
		"Classic/Vivaldi":           {"Classic", "Vivaldi"},
		"Classic/Vivaldi/Seasons":   {"Classic/Vivaldi", "Seasons"},
		"Classic/Vivaldi/Seasons/.": {"Classic/Vivaldi", "Seasons"},
	} {
		l, err := b.List(rel)
		if err != nil {
			t.Fatalf("%q: %v", rel, err)
		}
		if l.Parent != want[0] || l.Name != want[1] || strings.Contains(l.Path, "..") {
			t.Fatalf("%q: parent %q name %q path %q", rel, l.Parent, l.Name, l.Path)
		}
	}
}

func TestRelativeEscapesAreRefused(t *testing.T) {
	b, root := newBrowser(t, "a/b.flac")
	secret := filepath.Join(filepath.Dir(root), "secret")
	_ = os.MkdirAll(secret, 0o755)
	for _, rel := range []string{"..", "../", "a/../..", "../" + filepath.Base(secret), "/etc", "/", "a/../../x", "a\\..\\..", "a\x00b", "..\\x"} {
		if _, err := b.List(rel); err == nil || !(errors.Is(err, ErrOutside) || errors.Is(err, ErrNotFound)) {
			t.Fatalf("%q must be refused, got %v", rel, err)
		}
		if _, _, err := b.Resolve(rel); err == nil {
			t.Fatalf("Resolve(%q) must be refused", rel)
		}
	}
	if _, err := b.List("../" + filepath.Base(secret)); !errors.Is(err, ErrOutside) {
		t.Fatalf("a real directory outside must be ErrOutside: %v", err)
	}
}

func TestSymlinkOutOfRootIsInvisibleAndRefused(t *testing.T) {
	b, root := newBrowser(t, "in/a.flac")
	outside := t.TempDir()
	touch(t, outside, "secret/s.flac", "top.flac")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "leak")); err != nil {
		t.Skip("symlinks unavailable")
	}
	_ = os.Symlink(filepath.Join(outside, "top.flac"), filepath.Join(root, "leak.flac"))
	l, _ := b.List("")
	if got := names(l); got != "in/" {
		t.Fatalf("links out of the root must not be listed: %s", got)
	}
	if _, err := b.List("leak"); !errors.Is(err, ErrOutside) {
		t.Fatalf("listing through the link: %v", err)
	}
	if _, _, err := b.Resolve("leak.flac"); !errors.Is(err, ErrOutside) {
		t.Fatalf("resolving the file link: %v", err)
	}
	paths, err := b.Walk(context.Background(), "")
	if err != nil || len(paths) != 1 || !strings.HasSuffix(paths[0], "a.flac") {
		t.Fatalf("walk must not follow links out: %v %v", paths, err)
	}
}

func TestSymlinkInsideRootIsFollowed(t *testing.T) {
	b, root := newBrowser(t, "real/a.flac")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Skip("symlinks unavailable")
	}
	l, err := b.List("alias")
	if err != nil || names(l) != "a.flac" {
		t.Fatalf("%v %v", names(l), err)
	}
}

func TestResolveFileVsDir(t *testing.T) {
	b, root := newBrowser(t, "d/a.flac")
	abs, dir, err := b.Resolve("d")
	if err != nil || !dir || abs != filepath.Join(mustReal(t, root), "d") {
		t.Fatalf("%q %v %v", abs, dir, err)
	}
	abs, dir, err = b.Resolve("d/a.flac")
	if err != nil || dir || !strings.HasSuffix(abs, "a.flac") {
		t.Fatalf("%q %v %v", abs, dir, err)
	}
	if _, _, err := b.Resolve("d/missing.flac"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, _, err := b.Resolve("d/notes.txt"); err == nil {
		t.Fatal("non-audio files are not playable")
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWalkIsRecursiveAndOrdered(t *testing.T) {
	var rels []string
	for _, a := range []string{"Album 10", "Album 2"} {
		for _, n := range []string{"10", "02", "01"} {
			rels = append(rels, "Artist/"+a+"/"+n+" song.flac")
		}
	}
	rels = append(rels, "Artist/cover.jpg", "Other/x.mp3")
	b, root := newBrowser(t, rels...)
	paths, err := b.Walk(context.Background(), "Artist")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range paths {
		r, _ := filepath.Rel(mustReal(t, root), p)
		got = append(got, filepath.ToSlash(r))
	}
	want := "Artist/Album 2/01 song.flac Artist/Album 2/02 song.flac Artist/Album 2/10 song.flac Artist/Album 10/01 song.flac Artist/Album 10/02 song.flac Artist/Album 10/10 song.flac"
	if strings.Join(got, " ") != want {
		t.Fatalf("\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	one, err := b.Walk(context.Background(), "Other/x.mp3")
	if err != nil || len(one) != 1 {
		t.Fatalf("walking a file yields the file: %v %v", one, err)
	}
}

func TestWalkVisitsEachRealDirOnce(t *testing.T) {
	b, root := newBrowser(t, "a/one.flac")
	if err := os.Symlink(root, filepath.Join(root, "a", "loop")); err != nil {
		t.Skip("symlinks unavailable")
	}
	paths, err := b.Walk(context.Background(), "")
	if err != nil || len(paths) != 1 {
		t.Fatalf("a symlink loop must not repeat or hang: %v %v", paths, err)
	}
}

func TestWalkHonoursContext(t *testing.T) {
	var rels []string
	for i := 0; i < 200; i++ {
		rels = append(rels, "d"+string(rune('a'+i%26))+strings.Repeat("x", i/26)+"/f.flac")
	}
	b, _ := newBrowser(t, rels...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Walk(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestWalkSkipsUnreadableDirs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	b, root := newBrowser(t, "ok/a.flac", "locked/b.flac")
	_ = os.Chmod(filepath.Join(root, "locked"), 0)
	defer os.Chmod(filepath.Join(root, "locked"), 0o755)
	paths, err := b.Walk(context.Background(), "")
	if err != nil || len(paths) != 1 {
		t.Fatalf("%v %v", paths, err)
	}
}

func TestNewRejectsFilesAndMissing(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(f, nil, 0o644)
	if _, err := New(f); err == nil {
		t.Fatal("a file is not a music folder")
	}
	if _, err := New(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing")
	}
}

func TestRootFromConfig(t *testing.T) {
	home := "/home/u"
	cfg := t.TempDir()
	write := func(body string) {
		_ = os.MkdirAll(filepath.Join(cfg, "cliamp"), 0o755)
		_ = os.WriteFile(filepath.Join(cfg, "cliamp", "config.toml"), []byte(body), 0o600)
	}
	cases := map[string]string{
		"theme = \"\"\ninitial_directory = \"/mnt/music/\"\n":                   "/mnt/music",
		"initial_directory = '/mnt/m'   # my library\n":                         "/mnt/m",
		"initial_directory = \"~/Tunes\"\n":                                     "/home/u/Tunes",
		"initial_directory = \"~\"\n":                                           "/home/u",
		"# initial_directory = \"/no\"\ntheme = \"x\"\n":                        "/home/u/Music",
		"[navidrome]\ninitial_directory = \"/wrong/section\"\n":                 "/home/u/Music",
		"initial_directory = \"\"\n":                                            "/home/u/Music",
		"  initial_directory   =   \"/spaced path/with  gaps\"  \n[x]\na = 1\n": "/spaced path/with  gaps",
	}
	for body, want := range cases {
		write(body)
		if got := RootFromConfig(cfg, home); got != want {
			t.Fatalf("%q: got %q want %q", body, got, want)
		}
	}
	_ = os.Remove(filepath.Join(cfg, "cliamp", "config.toml"))
	if got := RootFromConfig(cfg, home); got != "/home/u/Music" {
		t.Fatalf("no config: %q", got)
	}
}

func TestNaturalLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Track 2", "Track 10", true}, {"Track 10", "Track 2", false}, {"a", "B", true}, {"B", "a", false},
		{"01 a", "1 a", true}, {"x", "x", false}, {"file2", "file2b", true}, {"Éa", "Eb", false}, {"", "a", true},
		{"a1b2", "a1b10", true}, {"007", "7", true},
	} {
		if got := NaturalLess(c.a, c.b); got != c.want {
			t.Fatalf("NaturalLess(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCoverFilePriorityAndFallback(t *testing.T) {
	b, root := newBrowser(t, "A/folder.png", "A/cover.jpg", "A/x.flac", "B/sub 10/cover.jpg", "B/sub 2/cover.png", "C/x.flac", "D/Cover.JPG")
	rel := func(abs string, err error) string {
		if err != nil {
			return "ERR:" + err.Error()
		}
		r, _ := filepath.Rel(mustReal(t, root), abs)
		return filepath.ToSlash(r)
	}
	for in, want := range map[string]string{
		"A": "A/cover.jpg",          // cover beats folder
		"B": "B/sub 2/cover.png",    // none here: the first sub-folder in natural order
		"D": "D/Cover.JPG",          // case-insensitive
		"C": "ERR:files: not found", // nothing anywhere
	} {
		if got := rel(b.CoverFile(in)); got != want {
			t.Fatalf("%s: got %q want %q", in, got, want)
		}
	}
	if _, err := b.CoverFile("A/x.flac"); err == nil {
		t.Fatal("a file has no folder cover")
	}
}

func TestCoverFileRefusesEscapesAndSymlinksOut(t *testing.T) {
	b, root := newBrowser(t, "in/x.flac")
	outside := t.TempDir()
	touch(t, outside, "pic.jpg", "dir/cover.jpg")
	if err := os.Symlink(filepath.Join(outside, "pic.jpg"), filepath.Join(root, "in", "cover.jpg")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := b.CoverFile("in"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a cover that is a link out of the root must not be used: %v", err)
	}
	_ = os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "in", "linked"))
	if _, err := b.CoverFile("in"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nor one reached through a folder link out: %v", err)
	}
	for _, rel := range []string{"..", "../x", "/etc", "in/../.."} {
		if _, err := b.CoverFile(rel); !errors.Is(err, ErrOutside) {
			t.Fatalf("%q: %v", rel, err)
		}
	}
}

func TestCoverFileSkipsHugeImages(t *testing.T) {
	b, root := newBrowser(t, "A/x.flac")
	big := filepath.Join(root, "A", "cover.jpg")
	f, _ := os.Create(big)
	_ = f.Truncate(11 << 20)
	f.Close()
	if _, err := b.CoverFile("A"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an 11 MB cover is not a thumbnail candidate: %v", err)
	}
}

func TestGroupsMapsSecondLevelFoldersToTheirTopFolder(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Rus/Земфира/10 мальчиков", "Rus/Би-2", "French/Chante France", "Rock/Deep Purple", "Rock/Various Artists", "Other/Various Artists", ".hidden/Ghost"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Rus", "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	g := b.Groups()
	want := map[string]string{"земфира": "Rus", "би-2": "Rus", "chante france": "French", "deep purple": "Rock"}
	for k, v := range want {
		if g[k] != v {
			t.Errorf("%q: got %q want %q", k, g[k], v)
		}
	}
	if _, ok := g["various artists"]; ok {
		t.Error("a name under two top folders is ambiguous and must be left out")
	}
	if _, ok := g["ghost"]; ok || g["10 мальчиков"] != "" {
		t.Error("hidden folders and third-level folders are not listed")
	}
}
