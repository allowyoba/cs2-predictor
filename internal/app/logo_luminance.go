package app

import (
	"bytes"
	"image"
	_ "image/gif"  // registered for decoding only
	_ "image/jpeg" // registered for decoding only
	_ "image/png"  // registered for decoding only

	// WebP is not in the standard library, and a third of a provider's
	// crests can arrive in it. Without this they measure as "unknown" and
	// fall back to a neutral chip, which is the one outcome this whole
	// measurement exists to avoid.
	_ "golang.org/x/image/webp"
)

// Measuring a crest so the app can put the right chip behind it.
//
// Team logos come in two kinds: light marks drawn to sit on a dark
// background, and dark marks drawn to sit on a light one. There is no
// single chip colour that shows both — a light chip loses the white
// wordmarks, a dark chip loses the black ones, and a middle grey loses
// both a little. So the bot measures each crest once, when it mirrors it,
// and the app picks the chip per team.

const (
	// logoAlphaFloor ignores pixels that are mostly transparent. Nearly
	// every crest is a mark on a transparent field, and averaging the
	// empty space in would make every logo look middling.
	logoAlphaFloor = 0x4000
	// logoLightThreshold is the mean luminance above which a mark counts
	// as light. Deliberately below the midpoint: a mark only has to be
	// brighter than a dark chip to need one, and the cost of getting this
	// wrong is a low-contrast logo either way, so it leans towards the
	// dark chip, which suits this app's own palette.
	logoLightThreshold = 0.45
)

// logoIsLight reports whether a crest's visible pixels are light overall,
// and whether it could be measured at all. An undecodable format — SVG,
// most obviously — returns ok=false, and the caller leaves the answer
// unrecorded rather than guessing.
func logoIsLight(data []byte) (light bool, ok bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false, false
	}
	bounds := img.Bounds()
	var sum float64
	var counted int
	// Sampled rather than exhaustive: these are 50-pixel crests, but the
	// cost of this should not depend on somebody else's image dimensions.
	step := 1
	if w := bounds.Dx(); w > 64 {
		step = w / 64
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, a := img.At(x, y).RGBA()
			if a < logoAlphaFloor {
				continue
			}
			// Un-premultiply so a semi-transparent pixel is judged by its
			// own colour rather than by how faint it is.
			scale := float64(a)
			lum := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / scale
			sum += lum
			counted++
		}
	}
	if counted == 0 {
		return false, false
	}
	return sum/float64(counted) > logoLightThreshold, true
}
