package art

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func gradientPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegSize(t testing.TB, data []byte) (int, int) {
	t.Helper()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestThumbShrinksKeepingTheAspect(t *testing.T) {
	out, err := Thumb(gradientPNG(t, 1200, 800), 300)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, out); w != 300 || h != 200 {
		t.Fatalf("%dx%d", w, h)
	}
	out, _ = Thumb(gradientPNG(t, 500, 1000), 300)
	if w, h := jpegSize(t, out); w != 150 || h != 300 {
		t.Fatalf("portrait: %dx%d", w, h)
	}
}

func TestThumbNeverEnlarges(t *testing.T) {
	out, err := Thumb(gradientPNG(t, 100, 100), 300)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, out); w != 100 || h != 100 {
		t.Fatalf("%dx%d", w, h)
	}
}

func TestThumbKeepsTheColours(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := 0; y < 600; y++ {
		for x := 0; x < 300; x++ {
			img.Set(x, y, color.RGBA{200, 20, 20, 255}) // left half red
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	out, err := Thumb(b.Bytes(), 300)
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := jpeg.Decode(bytes.NewReader(out))
	r, g, _, _ := dec.At(40, 150).RGBA()
	if r>>8 < 170 || g>>8 > 80 {
		t.Fatalf("left should still be red, got r=%d g=%d", r>>8, g>>8)
	}
	r, g, _, _ = dec.At(260, 150).RGBA()
	if r>>8 < 220 || g>>8 < 220 {
		t.Fatalf("right should still be white, got r=%d g=%d", r>>8, g>>8)
	}
}

func TestThumbIsDeterministic(t *testing.T) {
	src := gradientPNG(t, 640, 480)
	a, _ := Thumb(src, 300)
	b, _ := Thumb(src, 300)
	if !bytes.Equal(a, b) {
		t.Fatal("same input, same bytes (ETags rely on it)")
	}
}

func TestThumbRejectsGarbageAndEmpty(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("nope"), []byte("<html>")} {
		if _, err := Thumb(in, 300); err == nil {
			t.Fatalf("%q must fail", in)
		}
	}
}

// A tiny file can claim enormous dimensions; the size is read from the header
// and refused before any pixel is allocated.
func TestThumbRefusesADecompressionBomb(t *testing.T) {
	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
		return b.Bytes()
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 30000)
	binary.BigEndian.PutUint32(ihdr[4:], 30000)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	bomb := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", ihdr)...)
	bomb = append(bomb, chunk("IEND", nil)...)
	if len(bomb) > 100 {
		t.Fatalf("test setup: %d bytes", len(bomb))
	}
	if _, err := Thumb(bomb, 300); err == nil {
		t.Fatal("900 megapixels in a 70 byte file must be refused")
	}
}
