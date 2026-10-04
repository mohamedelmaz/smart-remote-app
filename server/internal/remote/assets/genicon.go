//go:build ignore

// Command genicon writes the multi-resolution Windows icon used by the tray and
// by the embedded PE resource.
//
// The icon is generated rather than shipped as a binary blob so the brand
// colours live in one place (the constants below) and the file can be
// regenerated deterministically after any change to the mark.
//
// Run it from the module root:
//
//	go run ./internal/remote/assets/genicon.go
package main

import (
	"encoding/binary"
	"os"
)

// Brand palette, matching the mobile app's theme.
var (
	deepVoid  = [4]byte{0x0A, 0x0E, 0x1A, 0xFF} // #0A0E1A background
	elecCyan  = [4]byte{0x00, 0xE5, 0xFF, 0xFF} // #00E5FF accent
)

// sizes are the resolutions written into the ICO. 16 is what the tray shows at
// 100% scaling; 256 covers high-DPI and the Explorer shell icon.
var sizes = []int{16, 32, 48, 256}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-stdout" {
		os.Stdout.Write(build())
		return
	}
	if err := os.WriteFile("internal/remote/assets/smartremote.ico", build(), 0o644); err != nil {
		panic(err)
	}
}

// build assembles a multi-image ICO.
//
// Each image is stored as a 32-bit BGRA DIB. The height stored in the
// BITMAPINFOHEADER is doubled because an ICO entry carries the XOR (colour)
// bitmap stacked above the AND (mask) bitmap; the height there is the sum of
// the two.
func build() []byte {
	const (
		dirSize     = 6
		entrySize   = 16
		biSize      = 40 // BITMAPINFOHEADER
		bytesPerPx  = 4  // BGRA
	)

	type image struct {
		size int
		data []byte
	}
	images := make([]image, 0, len(sizes))
	for _, s := range sizes {
		images = append(images, image{size: s, data: render(s)})
	}

	// AND mask: fully opaque, so the icon is a solid rounded square. Each row
	// must be padded to a 4-byte boundary.
	maskRow := ((max(images[0].size, 1) + 31) / 32) * 4

	out := make([]byte, 0, dirSize+entrySize*len(images)+1<<20)

	// ICONDIR: reserved=0, type=1 (icon), count.
	out = append(out, 0, 0, 1, 0, byte(len(images)), 0)

	offset := dirSize + entrySize*len(images)
	for _, img := range images {
		var dim byte
		if img.size >= 256 {
			dim = 0 // 0 encodes 256 in the ICO directory
		} else {
			dim = byte(img.size)
		}

		// Each entry records the XOR bitmap plus its AND mask.
		maskBytes := maskRow * img.size
		imgSize := biSize + len(img.data) + maskBytes

		// An ICONDIRENTRY is exactly 16 bytes:
		//   width(1) height(1) colourCount(1) reserved(1)
		//   planes(2) bitCount(2) bytesInRes(4) imageOffset(4)
		e := make([]byte, entrySize)
		e[0] = dim
		e[1] = dim
		// e[2] colourCount stays 0 ("no palette table"); e[3] is reserved.
		putUint16(e[4:], 1)  // planes
		putUint16(e[6:], 32) // bits per pixel
		putUint32(e[8:], uint32(imgSize))
		putUint32(e[12:], uint32(offset))
		out = append(out, e...)

		offset += imgSize
	}

	for _, img := range images {
		var bi [biSize]byte
		putUint32(bi[0:], biSize)
		putUint32(bi[4:], uint32(img.size))
		// Height is doubled: XOR bitmap + AND mask.
		putUint32(bi[8:], uint32(img.size*2))
		putUint16(bi[12:], 1)  // planes
		putUint16(bi[14:], 32) // bits per pixel
		putUint32(bi[20:], uint32(len(img.data)))

		out = append(out, bi[:]...)
		out = append(out, img.data...)

		maskBytes := make([]byte, maskRow*img.size)
		// The AND mask is inverted (a set bit means transparent), so 0xFF
		// everywhere means nothing is knocked out of the icon.
		for i := range maskBytes {
			maskBytes[i] = 0xFF
		}
		out = append(out, maskBytes...)
	}

	return out
}

// render draws one resolution as bottom-up BGRA rows.
//
// The mark is a rounded square in the accent colour with two concentric dots,
// echoing the app's "remote signal" motif. Supersampling 4x4 per pixel keeps
// the edges clean at 16px, where naive nearest-neighbour sampling turns the
// circle into a jagged blob.
func render(size int) []byte {
	const ss = 4 // supersampling factor per axis

	scale := float64(size) / 32.0
	pad := 5.0 * scale
	side := float64(size) - 2*pad
	radius := 5.0 * scale

	cx := float64(size) / 2
	outerR := 7.0 * scale
	innerR := 4.0 * scale

	px := make([]byte, size*size*4)

	for y := 0; y < size; y++ {
		// DIB rows run bottom-up.
		row := size - 1 - y
		for x := 0; x < size; x++ {
			aSum, rSum, gSum, bSum := 0.0, 0.0, 0.0, 0.0

			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := float64(x) + (float64(sx)+0.5)/ss
					fy := float64(y) + (float64(sy)+0.5)/ss

					c := deepVoid
					if inRoundedRect(fx, fy, pad, pad, side, side, radius) {
						c = elecCyan
						// Knock the two dots back out to the background so
						// they read at small sizes.
						if inCircle(fx, fy, cx, cx, innerR) {
							c = deepVoid
						} else if inCircle(fx, fy, cx, cx, outerR) {
							c = elecCyan
						}
					}

					rSum += float64(c[0])
					gSum += float64(c[1])
					bSum += float64(c[2])
					aSum += float64(c[3])
				}
			}

			n := float64(ss * ss)
			off := (row*size + x) * 4
			px[off+0] = byte(bSum / n) // B
			px[off+1] = byte(gSum / n) // G
			px[off+2] = byte(rSum / n) // R
			px[off+3] = byte(aSum / n) // A
		}
	}

	return px
}

// inCircle reports whether (x,y) is within r of (cx,cy).
func inCircle(x, y, cx, cy, r float64) bool {
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// inRoundedRect reports whether (x,y) lies inside a rounded rectangle.
func inRoundedRect(x, y, rx, ry, w, h, r float64) bool {
	if x < rx || y < ry || x > rx+w || y > ry+h {
		return false
	}
	// Only the four corner quadrants need the distance test.
	cx := x
	switch {
	case x < rx+r:
		cx = rx + r
	case x > rx+w-r:
		cx = rx + w - r
	}
	cy := y
	switch {
	case y < ry+r:
		cy = ry + r
	case y > ry+h-r:
		cy = ry + h - r
	}
	return inCircle(x, y, cx, cy, r)
}

func putUint16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
}

func putUint32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b, v)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}