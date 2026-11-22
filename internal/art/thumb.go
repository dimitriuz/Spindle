package art

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"

	"spindle/internal/files"
)

// maxPixels bounds what Thumb will decode: a tiny file can claim enormous
// dimensions, so the size is read from the header first.
const maxPixels = 40_000_000

// Thumb shrinks a picture to at most max pixels on its long side (never
// enlarging) and returns it as JPEG. The result depends only on the input.
func Thumb(data []byte, max int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, errors.New("art: image too large")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	// flatten onto white: a transparent cover must not turn black in a JPEG
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Over)

	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if long := max2(w, h); long > max {
		dw, dh = max1(w*max/long), max1(h*max/long)
	}
	dst := rgba
	if dw != w || dh != h {
		dst = boxScale(rgba, dw, dh)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func max1(v int) int    { return max(v, 1) }
func max2(a, b int) int { return max(a, b) }

// boxScale shrinks by averaging every source pixel that falls in a target pixel.
func boxScale(src *image.RGBA, dw, dh int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, b, n int
			for yy := y0; yy < y1 && yy < sh; yy++ {
				row := src.Pix[yy*src.Stride:]
				for xx := x0; xx < x1 && xx < sw; xx++ {
					r += int(row[xx*4])
					g += int(row[xx*4+1])
					b += int(row[xx*4+2])
					n++
				}
			}
			o := y*dst.Stride + x*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(b/n), 255
		}
	}
	return dst
}

// CoverFinder locates the cover picture of a music folder (*files.Browser).
type CoverFinder interface {
	CoverFile(rel string) (string, error)
}

// SetFolders enables folder covers.
func (r *Resolver) SetFolders(f CoverFinder) { r.folders = f }

const thumbSize = 300

// FolderArt returns a small thumbnail of a folder's cover. A path that leaves
// the music folder is files.ErrOutside; any other failure to produce a picture
// is ErrNoArt.
func (r *Resolver) FolderArt(_ context.Context, rel string) (*Image, error) {
	if r.folders == nil {
		return nil, ErrNoArt
	}
	abs, err := r.folders.CoverFile(rel)
	if errors.Is(err, files.ErrOutside) {
		return nil, err
	}
	if err != nil {
		return nil, ErrNoArt
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.Size() > MaxImageBytes {
		return nil, ErrNoArt
	}
	// the key follows the file (path, size, mtime), so a replaced cover is made again
	key := "folder:" + filepath.ToSlash(abs) + ":" + itoa(fi.Size()) + ":" + itoa(fi.ModTime().UnixNano())
	return r.cache.Do(key, func() ([]byte, error) {
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, ErrNoArt
		}
		out, err := Thumb(data, thumbSize)
		if err != nil {
			return nil, ErrNoArt // not a picture we can read (webp, damaged, huge)
		}
		return out, nil
	})
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
