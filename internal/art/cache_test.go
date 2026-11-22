package art

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newCache(t *testing.T, max int64) (*Cache, string, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	now := time.Unix(1_000_000, 0)
	c, err := NewCache(dir, max, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return c, dir, &now
}

func TestDoFetchesOnceAndServesFromDisk(t *testing.T) {
	c, dir, now := newCache(t, 1<<20)
	img := pngBytes(t, 4, 4)
	var calls int
	fetch := func() ([]byte, error) { calls++; return img, nil }
	got, err := c.Do("al-secret-id|cover", fetch)
	if err != nil || got.ContentType != "image/png" || !bytes.Equal(got.Data, img) {
		t.Fatalf("first: %v %+v", err, got)
	}
	if strings.Contains(got.Key, "secret") {
		t.Fatalf("etag key must not echo the input key: %q", got.Key)
	}
	// a fresh Cache over the same dir is served from disk
	c2, err := NewCache(dir, 1<<20, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	got2, err := c2.Do("al-secret-id|cover", fetch)
	if err != nil || calls != 1 || got2.Key != got.Key {
		t.Fatalf("second: err=%v calls=%d key %q vs %q", err, calls, got2.Key, got.Key)
	}
}

func TestConcurrentCallsShareOneFetch(t *testing.T) {
	c, _, _ := newCache(t, 1<<20)
	img := pngBytes(t, 4, 4)
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Do("k", func() ([]byte, error) { calls.Add(1); <-release; return img, nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetch ran %d times", calls.Load())
	}
}

func TestNoArtIsRememberedForTenMinutes(t *testing.T) {
	c, _, now := newCache(t, 1<<20)
	var calls int
	fetch := func() ([]byte, error) { calls++; return nil, ErrNoArt }
	for i := 0; i < 3; i++ {
		if _, err := c.Do("k", fetch); !errors.Is(err, ErrNoArt) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d within the window", calls)
	}
	*now = now.Add(11 * time.Minute)
	_, _ = c.Do("k", fetch)
	if calls != 2 {
		t.Fatalf("calls=%d after the window", calls)
	}
}

func TestOtherErrorsAreNotCached(t *testing.T) {
	c, _, _ := newCache(t, 1<<20)
	var calls int
	boom := errors.New("connection refused")
	for i := 0; i < 2; i++ {
		if _, err := c.Do("k", func() ([]byte, error) { calls++; return nil, boom }); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("transient errors must be retried, calls=%d", calls)
	}
}

func TestRejectsNonImageBytes(t *testing.T) {
	c, dir, _ := newCache(t, 1<<20)
	_, err := c.Do("k", func() ([]byte, error) { return []byte("<html>login</html>"), nil })
	if !errors.Is(err, ErrNoArt) {
		t.Fatalf("html must be no art, got %v", err)
	}
	if n := countFiles(t, dir); n != 0 {
		t.Fatalf("nothing may be written, found %d files", n)
	}
}

func TestRejectsOversize(t *testing.T) {
	c, _, _ := newCache(t, 64<<20)
	big := append(pngBytes(t, 4, 4), make([]byte, MaxImageBytes)...)
	if _, err := c.Do("k", func() ([]byte, error) { return big, nil }); !errors.Is(err, ErrNoArt) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestEvictsOldestOverCap(t *testing.T) {
	img := pngBytes(t, 40, 40)
	c, dir, now := newCache(t, int64(len(img))*2+10)
	for _, k := range []string{"a", "b", "c"} {
		if _, err := c.Do(k, func() ([]byte, error) { return img, nil }); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Second) // the cache stamps mtimes from its own clock
	}
	if n := countFiles(t, dir); n != 2 {
		t.Fatalf("cap allows 2 images, found %d", n)
	}
	var calls int
	_, _ = c.Do("a", func() ([]byte, error) { calls++; return img, nil })
	if calls != 1 {
		t.Fatal("oldest entry (a) should have been evicted")
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n++
		}
		return nil
	})
	return n
}
