package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// decodesWebP reports whether the WebP decoder is registered. The bytes
// are a minimal lossy WebP of a solid block — enough to reach the decoder,
// which is the thing under test.
func decodesWebP(t *testing.T) bool {
	t.Helper()
	webpBytes := []byte{
		'R', 'I', 'F', 'F', 0x1a, 0, 0, 0, 'W', 'E', 'B', 'P',
		'V', 'P', '8', 'L', 0x0d, 0, 0, 0,
		0x2f, 0x00, 0x00, 0x00, 0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07, 0x00,
	}
	_, format, err := image.Decode(bytes.NewReader(webpBytes))
	return err == nil && format == "webp"
}

// crestPNG draws a mark of one colour on a transparent field, which is how
// essentially every team crest is published.
func crestPNG(t *testing.T, mark color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	// A mark covering the middle third — the rest stays transparent, and
	// that emptiness must not drag the measurement towards the middle.
	for y := 10; y < 22; y++ {
		for x := 10; x < 22; x++ {
			img.SetNRGBA(x, y, mark)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// No single background shows every logo: a white wordmark disappears on a
// light chip and a black one disappears on a dark chip. The measurement is
// what lets the app pick per team, so it has to be right about which is
// which — and honest when it cannot tell.
func TestLogoIsLight(t *testing.T) {
	white, ok := logoIsLight(crestPNG(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}))
	if !ok || !white {
		t.Fatalf("a white mark measured as light=%v, ok=%v — it needs a dark chip", white, ok)
	}

	black, ok := logoIsLight(crestPNG(t, color.NRGBA{A: 255}))
	if !ok || black {
		t.Fatalf("a black mark measured as light=%v, ok=%v — it needs a light chip", black, ok)
	}

	// A saturated brand colour is judged by its own brightness, not by
	// being "coloured": a yellow mark still vanishes on white.
	yellow, ok := logoIsLight(crestPNG(t, color.NRGBA{R: 255, G: 220, B: 0, A: 255}))
	if !ok || !yellow {
		t.Fatalf("a bright yellow mark measured as light=%v, ok=%v", yellow, ok)
	}

	// Transparency is not darkness. An image that is mostly empty must be
	// judged on the pixels that are actually drawn.
	if light, ok := logoIsLight(crestPNG(t, color.NRGBA{R: 240, G: 240, B: 240, A: 255})); !ok || !light {
		t.Fatalf("a near-white mark on a transparent field measured as light=%v, ok=%v", light, ok)
	}

	// WebP is not in the standard library and a provider can serve it, so
	// it has to be registered explicitly — otherwise these crests measure
	// as "unknown" and get the neutral chip this exists to avoid.
	if !decodesWebP(t) {
		t.Error("WebP does not decode; a crest served in it would never get a chip")
	}

	// Nothing to measure is said out loud rather than guessed: an SVG, or
	// a fully transparent image.
	if _, ok := logoIsLight([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>")); ok {
		t.Error("an undecodable format must report that it could not be measured")
	}
	blank := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := png.Encode(&buf, blank); err != nil {
		t.Fatal(err)
	}
	if _, ok := logoIsLight(buf.Bytes()); ok {
		t.Error("a fully transparent image has no mark to measure")
	}
}
