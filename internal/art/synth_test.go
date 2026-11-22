package art

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"testing"
)

func solidJPEG(t testing.TB, c color.RGBA, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func decodePNG(t testing.TB, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	return img
}

func alphaAt(img image.Image, x, y int) uint32 { _, _, _, a := img.At(x, y).RGBA(); return a >> 8 }

func TestSynthDiscIsARoundDiscMadeFromTheCover(t *testing.T) {
	out, err := SynthDisc(solidJPEG(t, color.RGBA{200, 30, 30, 255}, 300, 400)) // non-square cover
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, out)
	if b := img.Bounds(); b.Dx() != discSize || b.Dy() != discSize {
		t.Fatalf("size %v", b)
	}
	c := discSize / 2
	for _, p := range [][2]int{{0, 0}, {discSize - 1, 0}, {0, discSize - 1}, {discSize - 1, discSize - 1}} {
		if alphaAt(img, p[0], p[1]) != 0 {
			t.Fatalf("corner %v must be transparent", p)
		}
	}
	if !looksLikeDisc(out) {
		t.Fatal("the generated image must pass the same disc test as a real disc.png")
	}
	if alphaAt(img, c, c) != 0 {
		t.Fatal("the spindle hole is see-through")
	}
	// printed area carries the cover's colour
	r, g, bl, a := img.At(c+170, c).RGBA()
	if a>>8 < 250 || r>>8 < 150 || g>>8 > 120 || bl>>8 > 120 {
		t.Fatalf("print area should be the red cover, got %d %d %d a=%d", r>>8, g>>8, bl>>8, a>>8)
	}
	// the rim is opaque and light, and just outside the disc is empty
	if _, _, _, a := img.At(c+discSize/2-4, c).RGBA(); a>>8 < 250 {
		t.Fatal("rim must be opaque")
	}
	if alphaAt(img, discSize-1, c) > 40 {
		t.Fatal("the very edge must anti-alias to transparent")
	}
}

func TestSynthDiscIsDeterministic(t *testing.T) {
	cover := solidJPEG(t, color.RGBA{10, 120, 200, 255}, 64, 64)
	a, _ := SynthDisc(cover)
	b, _ := SynthDisc(cover)
	if !bytes.Equal(a, b) {
		t.Fatal("same cover must give the same bytes (cache keys and ETags rely on it)")
	}
}

func TestSynthDiscRejectsWhatItCannotDecode(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("RIFF....WEBPVP8 "), []byte("<html>")} {
		if _, err := SynthDisc(in); err == nil {
			t.Fatalf("%q must fail", in)
		}
	}
}

// No disc.png on the server: the resolver makes one from the cover, once.
func TestResolverSynthesisesADiscWhenTheAlbumHasNone(t *testing.T) {
	f := newFakeND(t)
	f.noDisc = true
	f.coverBytes = func() []byte { return solidJPEG(t, color.RGBA{30, 160, 60, 255}, 200, 200) }
	known := func(p string) bool { return p == f.stream("") }
	r := newResolver(t, known, nil)
	r.nd.HTTP = f.srv.Client()
	img, err := r.Get(context.Background(), Disc, f.stream(""))
	if err != nil || img.ContentType != "image/png" || !looksLikeDisc(img.Data) {
		t.Fatalf("synthesised disc: %v %+v", err, img)
	}
	covers := len(f.lastQuery)
	if _, err := r.Get(context.Background(), Disc, f.stream("")); err != nil {
		t.Fatal(err)
	}
	if len(f.lastQuery) != covers {
		t.Fatal("the second request must come from the cache")
	}
	// a real disc still wins
	f2 := newFakeND(t)
	r2 := newResolver(t, func(p string) bool { return p == f2.stream("") }, nil)
	r2.nd.HTTP = f2.srv.Client()
	if img, err := r2.Get(context.Background(), Disc, f2.stream("")); err != nil || len(f2.lastQuery) != 1 || img == nil {
		t.Fatalf("real disc: %v queries=%v", err, f2.lastQuery)
	}
}

func TestNoCoverMeansNoDiscEither(t *testing.T) {
	r := newResolver(t, func(string) bool { return true }, nil)
	if _, err := r.Get(context.Background(), Disc, "https://radio.example/live"); err != ErrNoArt {
		t.Fatalf("radio: %v", err)
	}
}
