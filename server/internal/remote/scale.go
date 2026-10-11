package remote

import (
	"image"
)

// DefaultMaxFrameWidth is the widest frame the encoder is allowed to produce.
//
// A 4K desktop is 8.3 million pixels per frame; at 15 fps that is a large
// JPEG to build, to push over Wi-Fi and to decode on the phone, for detail the
// phone screen cannot show anyway. Anything at or below this width is returned
// untouched, so an ordinary desktop keeps exactly the pixels and the quality it
// had before this existed.
const DefaultMaxFrameWidth = 2560

// scaleToMaxWidth returns src unchanged when it already fits, and otherwise an
// area-averaged copy no wider than maxWidth with the aspect ratio preserved.
//
// The filter averages every source pixel that falls in the destination block
// rather than sampling a single one. Nearest-neighbour would be cheaper still,
// but it drops whole scanlines on a non-integer ratio and turns text on the
// desktop into shimmering dashes, which is precisely what this function exists
// to avoid.
func scaleToMaxWidth(src *image.RGBA, maxWidth int) *image.RGBA {
	if src == nil || maxWidth <= 0 {
		return src
	}
	srcW, srcH := src.Rect.Dx(), src.Rect.Dy()
	if srcW <= maxWidth || srcW <= 0 || srcH <= 0 {
		return src
	}

	dstW := maxWidth
	// Round to nearest rather than truncating, so the height lands on the
	// closest integer instead of drifting a pixel low on every frame.
	dstH := (srcH*maxWidth + srcW/2) / srcW
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for dy := 0; dy < dstH; dy++ {
		sy0 := dy * srcH / dstH
		sy1 := (dy + 1) * srcH / dstH
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dstW; dx++ {
			sx0 := dx * srcW / dstW
			sx1 := (dx + 1) * srcW / dstW
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b, a uint32
			var n uint32
			for sy := sy0; sy < sy1; sy++ {
				row := src.Pix[sy*src.Stride:]
				for sx := sx0; sx < sx1; sx++ {
					i := sx * 4
					b += uint32(row[i])
					g += uint32(row[i+1])
					r += uint32(row[i+2])
					a += uint32(row[i+3])
					n++
				}
			}
			di := dy*dst.Stride + dx*4
			dst.Pix[di] = uint8(b / n)
			dst.Pix[di+1] = uint8(g / n)
			dst.Pix[di+2] = uint8(r / n)
			dst.Pix[di+3] = uint8(a / n)
		}
	}
	return dst
}
