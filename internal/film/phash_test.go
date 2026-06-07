package film

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// gradient builds a 16x16 image whose brightness strictly increases left→right.
func gradient() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8(x * 16)})
		}
	}
	return img
}

func TestDHash_DeterministicAndShape(t *testing.T) {
	img := gradient()
	h1, h2 := dHash(img), dHash(img)
	if h1 != h2 {
		t.Fatalf("dHash not deterministic: %q vs %q", h1, h2)
	}
	if len(h1) != 16 {
		t.Fatalf("dHash should be 16 hex chars, got %d (%q)", len(h1), h1)
	}
	// strictly increasing left→right → every adjacent comparison true → all 64 bits set
	if h1 != "ffffffffffffffff" {
		t.Fatalf("monotone gradient should hash to all-ones, got %q", h1)
	}
}

func TestComputePHash_PNGDecodes(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient()); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	got := computePHash(buf.Bytes())
	if got != "ffffffffffffffff" {
		t.Fatalf("computePHash(png) = %q, want all-ones", got)
	}
}

func TestComputePHash_GarbageIsEmpty(t *testing.T) {
	if got := computePHash([]byte("not an image")); got != "" {
		t.Fatalf("garbage should yield empty phash, got %q", got)
	}
}
