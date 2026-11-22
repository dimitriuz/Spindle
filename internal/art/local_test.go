package art

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// id3WithCover builds a minimal MP3 whose ID3v2.3 tag carries one APIC frame.
func id3WithCover(img []byte) []byte {
	var frame bytes.Buffer
	frame.WriteByte(0)                 // text encoding: latin1
	frame.WriteString("image/png\x00") // mime
	frame.WriteByte(3)                 // picture type: front cover
	frame.WriteString("\x00")          // description
	frame.Write(img)
	var body bytes.Buffer
	body.WriteString("APIC")
	n := frame.Len()
	body.Write([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
	body.Write([]byte{0, 0})
	body.Write(frame.Bytes())
	size := body.Len()
	hdr := []byte{'I', 'D', '3', 3, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	return append(append(hdr, body.Bytes()...), make([]byte, 64)...)
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func localFor(known ...string) *Local {
	set := map[string]bool{}
	for _, k := range known {
		set[k] = true
	}
	return &Local{Known: func(p string) bool { return set[p] }}
}

func resolve(t *testing.T, l *Local, kind Kind, track string) ([]byte, string, error) {
	t.Helper()
	key, fetch, err := l.Resolve(kind, track)
	if err != nil {
		return nil, "", err
	}
	b, err := fetch()
	return b, key, err
}

func TestEmbeddedCover(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "Artist", "Album", "01.mp3")
	img := pngBytes(t, 6, 6)
	write(t, track, id3WithCover(img))
	got, key, err := resolve(t, localFor(track), Cover, track)
	if err != nil || !bytes.Equal(got, img) {
		t.Fatalf("embedded cover: %v (%d bytes)", err, len(got))
	}
	if key == "" {
		t.Fatal("empty art key")
	}
}

func TestFolderCoverNamesAndOrder(t *testing.T) {
	dir := t.TempDir()
	album := filepath.Join(dir, "Artist", "Album")
	track := filepath.Join(album, "01.flac")
	write(t, track, []byte("not a tagged file"))
	small, big := pngBytes(t, 2, 2), pngBytes(t, 9, 9)
	write(t, filepath.Join(album, "folder.png"), small)
	write(t, filepath.Join(album, "cover.jpg"), big) // PNG bytes under a .jpg name: content decides, name only orders
	got, _, err := resolve(t, localFor(track), Cover, track)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("cover.jpg must win over folder.png: %v", err)
	}
}

func TestDiscNames(t *testing.T) {
	for _, name := range []string{"disc.png", "cdart.png"} {
		dir := t.TempDir()
		track := filepath.Join(dir, "A", "B", "01.flac")
		write(t, track, []byte("x"))
		img := pngBytes(t, 5, 5)
		write(t, filepath.Join(dir, "A", "B", name), img)
		got, _, err := resolve(t, localFor(track), Disc, track)
		if err != nil || !bytes.Equal(got, img) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestArtistInAlbumDirOrParent(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "Artist", "Album", "01.flac")
	write(t, track, []byte("x"))
	img := pngBytes(t, 7, 7)
	write(t, filepath.Join(dir, "Artist", "artist.png"), img)
	got, _, err := resolve(t, localFor(track), Artist, track)
	if err != nil || !bytes.Equal(got, img) {
		t.Fatalf("parent artist image: %v", err)
	}
	inAlbum := pngBytes(t, 3, 3)
	write(t, filepath.Join(dir, "Artist", "Album", "artist.jpg"), inAlbum)
	got, _, err = resolve(t, localFor(track), Artist, track)
	if err != nil || !bytes.Equal(got, inAlbum) {
		t.Fatalf("album-dir artist image wins: %v", err)
	}
}

func TestUnknownPathIsNoArt(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.mp3")
	write(t, track, id3WithCover(pngBytes(t, 4, 4)))
	write(t, filepath.Join(dir, "cover.png"), pngBytes(t, 4, 4))
	for _, p := range []string{track, "/etc/passwd", dir + "/../../etc/passwd"} {
		if _, _, err := localFor().Resolve(Cover, p); err != ErrNoArt {
			t.Fatalf("%s is not a known track: %v", p, err)
		}
	}
}

func TestRelativeAndDotDotPathsAreNoArt(t *testing.T) {
	for _, p := range []string{"relative/a.mp3", "../x.mp3", "a/../../b.mp3", ""} {
		if _, _, err := localFor(p).Resolve(Cover, p); err != ErrNoArt {
			t.Fatalf("%q: %v", p, err)
		}
	}
}

func TestSymlinkEscapingTheDirIsNoArt(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.png")
	write(t, secret, pngBytes(t, 4, 4))
	album := filepath.Join(dir, "A", "B")
	track := filepath.Join(album, "01.flac")
	write(t, track, []byte("x"))
	if err := os.Symlink(secret, filepath.Join(album, "cover.png")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, _, err := resolve(t, localFor(track), Cover, track); err != ErrNoArt {
		t.Fatalf("symlink out of the album dir must be refused: %v", err)
	}
}

func TestOversizeImageIsNoArt(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.flac")
	write(t, track, []byte("x"))
	write(t, filepath.Join(dir, "cover.png"), append(pngBytes(t, 2, 2), make([]byte, MaxImageBytes)...))
	if _, _, err := resolve(t, localFor(track), Cover, track); err != ErrNoArt {
		t.Fatalf("oversize: %v", err)
	}
}

func TestNoImagesIsNoArt(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.flac")
	write(t, track, []byte("x"))
	for _, k := range []Kind{Cover, Disc, Artist} {
		if _, _, err := resolve(t, localFor(track), k, track); err != ErrNoArt {
			t.Fatalf("%s: %v", k, err)
		}
	}
}

func TestKeyChangesWhenTheFileChanges(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.flac")
	write(t, track, []byte("x"))
	img := filepath.Join(dir, "cover.png")
	write(t, img, pngBytes(t, 2, 2))
	_, k1, _ := resolve(t, localFor(track), Cover, track)
	write(t, img, pngBytes(t, 20, 20))
	_, k2, _ := resolve(t, localFor(track), Cover, track)
	if k1 == k2 {
		t.Fatal("replacing cover.png must invalidate the cached picture")
	}
}
