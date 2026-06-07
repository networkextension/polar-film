package film

// phash.go — a lightweight perceptual hash (dHash) for screenshots, used
// later (M4+) for de-dup and 以图搜片. Pure stdlib: decode the image,
// nearest-neighbour downscale to 9x8 grayscale, then emit 64 bits from
// adjacent-pixel brightness comparisons. Unknown/undecodable formats yield
// "" (non-fatal — phash is advisory).

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // register decoders
	_ "image/jpeg" //
	_ "image/png"  //
)

// computePHash returns the 16-hex-char dHash of an image, or "" if the bytes
// can't be decoded as a known image format.
func computePHash(data []byte) string {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	return dHash(img)
}

func dHash(img image.Image) string {
	const w, h = 9, 8 // 8 columns of comparison per row × 8 rows = 64 bits
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return ""
	}
	var gray [h][w]uint32
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*b.Dx()/w
			sy := b.Min.Y + y*b.Dy()/h
			r, g, bl, _ := img.At(sx, sy).RGBA() // 16-bit per channel
			gray[y][x] = (r*299 + g*587 + bl*114) / 1000
		}
	}
	var bits uint64
	var n uint
	for y := 0; y < h; y++ {
		for x := 0; x < w-1; x++ {
			if gray[y][x] < gray[y][x+1] {
				bits |= 1 << n
			}
			n++
		}
	}
	return fmt.Sprintf("%016x", bits)
}
