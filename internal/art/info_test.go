package art

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// flacHeader is "fLaC" plus a STREAMINFO block for the given format.
func flacHeader(rate, channels, bits int) []byte {
	b := []byte("fLaC")
	b = append(b, 0x80, 0, 0, 34) // last block, type 0 (STREAMINFO), length 34
	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:], 4096)
	binary.BigEndian.PutUint16(si[2:], 4096)
	v := uint64(rate)<<44 | uint64(channels-1)<<41 | uint64(bits-1)<<36 | 441000
	binary.BigEndian.PutUint64(si[10:], v)
	return append(b, si...)
}

func TestNavidromeInfoFromGetSong(t *testing.T) {
	f := newFakeND(t)
	n := &Navidrome{HTTP: f.srv.Client()}
	got, err := n.Info(context.Background(), f.stream(""))
	want := Info{Format: "FLAC", BitrateKbps: 1161, BitDepth: 24, SampleRate: 44100, Channels: 2}
	if err != nil || got != want {
		t.Fatalf("got %+v err %v, want %+v", got, err, want)
	}
	// art lookups and info share one getSong per song
	if _, _, err := fetchBytes(t, n, Cover, f.stream("")); err != nil {
		t.Fatal(err)
	}
	if f.songCalls.Load() != 1 {
		t.Fatalf("getSong ran %d times", f.songCalls.Load())
	}
}

func TestNavidromeInfoOfNonSubsonicURLIsNoInfo(t *testing.T) {
	n := &Navidrome{HTTP: http_default()}
	for _, u := range []string{"https://radio.example/stream", "/a/b.flac", ""} {
		if _, err := n.Info(context.Background(), u); !errors.Is(err, ErrNoInfo) {
			t.Fatalf("%q: %v", u, err)
		}
	}
}

func TestLocalInfoFLACReadsStreamInfo(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.flac")
	write(t, track, append(flacHeader(96000, 2, 24), make([]byte, 200)...))
	got, err := localFor(track).Info(track, 0)
	if err != nil || got.Format != "FLAC" || got.SampleRate != 96000 || got.BitDepth != 24 || got.Channels != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestLocalInfoMP3UsesAverageBitrate(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.mp3")
	data := id3WithCover(pngBytes(t, 4, 4))
	data = append(data, make([]byte, 40000)...)
	write(t, track, data)
	fi, _ := os.Stat(track)
	got, err := localFor(track).Info(track, 10)
	want := int(fi.Size() * 8 / 10 / 1000)
	if err != nil || got.Format != "MP3" || got.BitrateKbps != want {
		t.Fatalf("%+v %v want bitrate %d", got, err, want)
	}
	got, _ = localFor(track).Info(track, 0)
	if got.BitrateKbps != 0 {
		t.Fatalf("no duration, no bitrate: %+v", got)
	}
}

func TestLocalInfoRefusesUnknownAndEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.flac")
	write(t, track, flacHeader(44100, 2, 16))
	for _, p := range []string{track, "/etc/passwd", dir + "/../../etc/passwd", "rel/a.flac", ""} {
		if _, err := localFor().Info(p, 0); !errors.Is(err, ErrNoInfo) {
			t.Fatalf("%q not known: %v", p, err)
		}
	}
}

func TestLocalInfoOfUnrecognisedFileIsNoInfo(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "01.flac")
	write(t, track, []byte("not really flac"))
	if _, err := localFor(track).Info(track, 0); !errors.Is(err, ErrNoInfo) {
		t.Fatalf("content decides, not the extension: %v", err)
	}
}

func TestResolverInfoGatesLikeGet(t *testing.T) {
	f := newFakeND(t)
	r := newResolver(t, func(string) bool { return false }, nil)
	r.nd.HTTP = f.srv.Client()
	if _, err := r.Info(context.Background(), f.stream(""), 0); !errors.Is(err, ErrNoInfo) {
		t.Fatalf("unknown URL: %v", err)
	}
	if f.songCalls.Load() != 0 {
		t.Fatal("no request may be made for an unknown URL")
	}
	r2 := newResolver(t, func(p string) bool { return p == f.stream("") }, nil)
	r2.nd.HTTP = f.srv.Client()
	if got, err := r2.Info(context.Background(), f.stream(""), 0); err != nil || got.Format != "FLAC" {
		t.Fatalf("%+v %v", got, err)
	}
}
