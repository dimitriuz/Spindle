package art

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg" // cover formats SynthDisc can read
	"image/png"
	"math"
)

// discSize is the side of a generated disc in pixels (a real disc.png is
// typically larger; this is plenty for a phone and cheap to send).
const discSize = 600

// Radii as fractions of the disc radius: clear outer rim, printed area,
// the clear hub around the spindle hole, and the hole itself.
const (
	rimFrac   = 0.94
	hubFrac   = 0.34
	holeFrac  = 0.13
	sheenGain = 0.20
)

// SynthDisc draws a disc from an album cover for albums that have no disc
// image: the cover printed on a round disc with a clear rim, a see-through
// hub and spindle hole, and a faint light sheen. The corners are transparent,
// so it passes for (and is used as) a real disc image. Deterministic.
func SynthDisc(cover []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(cover))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	if b.Dx() < 2 || b.Dy() < 2 {
		return nil, errors.New("art: cover too small")
	}
	// flatten to NRGBA once so sampling is cheap
	px := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(px, px.Bounds(), src, b.Min, draw.Src)
	side := float64(min(px.Bounds().Dx(), px.Bounds().Dy()))
	ox := float64(px.Bounds().Dx()-int(side)) / 2 // centre crop
	oy := float64(px.Bounds().Dy()-int(side)) / 2

	out := image.NewNRGBA(image.Rect(0, 0, discSize, discSize))
	c := float64(discSize) / 2
	rOut := c - 1
	rRim, rHub, rHole := rOut*rimFrac, rOut*hubFrac, rOut*holeFrac
	for y := 0; y < discSize; y++ {
		for x := 0; x < discSize; x++ {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			d := math.Hypot(dx, dy)
			edge := clamp01(rOut + 0.5 - d) // anti-aliased outer edge
			if edge == 0 {
				continue
			}
			hole := clamp01(d - rHole + 0.5) // 0 inside the spindle hole
			if hole == 0 {
				continue
			}
			var r, g, bl, a float64 = 0, 0, 0, 255
			switch {
			case d >= rRim: // clear rim: light, slightly darker toward the edge
				shade := 1 - 0.14*(d-rRim)/(rOut-rRim)
				r, g, bl = 232*shade, 234*shade, 238*shade
			case d >= rHub: // printed area: the cover, centre-cropped to a square
				u := ((dx/rRim)/2 + 0.5) * side
				v := ((dy/rRim)/2 + 0.5) * side
				r, g, bl = sample(px, ox+u, oy+v)
			default: // clear hub: translucent silver
				r, g, bl, a = 205, 209, 215, 150
			}
			if d < rRim && d >= rHole {
				// two opposite light wedges, like a window reflecting on a CD
				th := math.Atan2(dy, dx)
				s := math.Pow(math.Max(0, math.Cos(2*th-0.9)), 6) * sheenGain
				r, g, bl = r+(255-r)*s, g+(255-g)*s, bl+(255-bl)*s
			}
			// fine dark lines where the zones meet
			if math.Abs(d-rRim) < 0.8 || math.Abs(d-rHub) < 0.8 || math.Abs(d-rHole) < 1.2 {
				r, g, bl = r*0.7, g*0.7, bl*0.7
			}
			out.SetNRGBA(x, y, color.NRGBA{uint8(clamp255(r)), uint8(clamp255(g)), uint8(clamp255(bl)), uint8(a * edge * hole)})
		}
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sample reads the picture at fractional coordinates with bilinear filtering.
func sample(p *image.NRGBA, fx, fy float64) (r, g, b float64) {
	w, h := p.Bounds().Dx(), p.Bounds().Dy()
	x0, y0 := int(math.Floor(fx-0.5)), int(math.Floor(fy-0.5))
	tx, ty := fx-0.5-float64(x0), fy-0.5-float64(y0)
	at := func(x, y int) (float64, float64, float64) {
		x, y = max(0, min(w-1, x)), max(0, min(h-1, y))
		c := p.NRGBAAt(x, y)
		return float64(c.R), float64(c.G), float64(c.B)
	}
	r00, g00, b00 := at(x0, y0)
	r10, g10, b10 := at(x0+1, y0)
	r01, g01, b01 := at(x0, y0+1)
	r11, g11, b11 := at(x0+1, y0+1)
	lerp := func(a, b, t float64) float64 { return a + (b-a)*t }
	return lerp(lerp(r00, r10, tx), lerp(r01, r11, tx), ty),
		lerp(lerp(g00, g10, tx), lerp(g01, g11, tx), ty),
		lerp(lerp(b00, b10, tx), lerp(b01, b11, tx), ty)
}

func clamp01(v float64) float64  { return math.Max(0, math.Min(1, v)) }
func clamp255(v float64) float64 { return math.Max(0, math.Min(255, math.Round(v))) }
