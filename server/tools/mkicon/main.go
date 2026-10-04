// Command mkicon converts a source image into a multi-resolution Windows .ico.
//
// WHY A TOOL RATHER THAN A CHECKED-IN BLOB
// -----------------------------------------
// The icon must stay in step with the logo, and a .ico committed as binary is
// something nobody can review or regenerate. Keeping the conversion as code
// means the icon can always be rebuilt from the logo with one command.
//
// It also avoids depending on ImageMagick being installed, which a contributor
// or release machine may not have. Go's standard library decodes the JPEG and
// writes the ICO container, so this needs nothing beyond the toolchain that
// already builds the server.
//
// Windows selects an icon resource by size, so several are embedded: Explorer
// shows 16px, the taskbar 24-32, Alt-Tab 32, large displays 256. Embedding only
// one size yields a blurry or mis-scaled icon depending on where it appears.
//
// Usage:
//
//	go run ./tools/mkicon -in logo.jpg -out smart-remote.ico
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

// init registers the JPEG decoder.
//
// It is normally done by a blank import of image/jpeg, but the import is
// referenced here so it is both registered and visibly intentional.
func init() {
	_ = jpeg.Decode
}

// 256 is the largest Windows reads; the rest cover the shell, taskbar, Alt-Tab
// and notification-area cases.
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	in := flag.String("in", "", "source image (JPEG or PNG)")
	out := flag.String("out", "", "output .ico path")
	flag.Parse()

	if *in == "" || *out == "" {
		log.Fatal("usage: mkicon -in <source> -out <icon.ico>")
	}

	src, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("read %s: %v", *in, err)
	}
	base, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		log.Fatalf("decode %s: %v", *in, err)
	}

	ico, err := buildICO(base)
	if err != nil {
		log.Fatalf("build icon: %v", err)
	}
	if err := os.WriteFile(*out, ico, 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	fmt.Printf("wrote %s (%d bytes, %d sizes from %dx%d source)\n",
		filepath.Base(*out), len(ico), len(sizes),
		base.Bounds().Dx(), base.Bounds().Dy())
}

// buildICO assembles the icon container from several resized copies.
func buildICO(src image.Image) ([]byte, error) {
	type entry struct {
		size int
		data []byte
	}

	entries := make([]entry, 0, len(sizes))
	for _, s := range sizes {
		var buf bytes.Buffer
		if s == 256 {
			// PNG-compressed at 256, the documented convention and the only
			// size where raw pixels would bloat the container.
			if err := png.Encode(&buf, resize(src, s)); err != nil {
				return nil, fmt.Errorf("encode %dpx: %w", s, err)
			}
		} else {
			dib, err := encodeDIB(resize(src, s))
			if err != nil {
				return nil, fmt.Errorf("encode %dpx: %w", s, err)
			}
			buf.Write(dib)
		}
		entries = append(entries, entry{size: s, data: buf.Bytes()})
	}

	var out bytes.Buffer
	header := make([]byte, 6)
	binary.LittleEndian.PutUint16(header[0:2], 0) // reserved
	binary.LittleEndian.PutUint16(header[2:4], 1) // type: icon
	binary.LittleEndian.PutUint16(header[4:6], uint16(len(entries)))
	out.Write(header)

	// One ICONDIRENTRY per image. 256 is encoded as 0 because the dimension
	// field is a single byte.
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		dir := make([]byte, 16)
		dir[0] = byte(e.size % 256)
		dir[1] = byte(e.size / 256)
		dir[2] = 0                                  // palette size: 0 for truecolour
		dir[3] = 0                                  // reserved
		binary.LittleEndian.PutUint16(dir[4:6], 1)  // colour planes
		binary.LittleEndian.PutUint16(dir[6:8], 32) // bits per pixel
		binary.LittleEndian.PutUint32(dir[8:12], uint32(len(e.data)))
		binary.LittleEndian.PutUint32(dir[12:16], uint32(offset))
		out.Write(dir)
		offset += len(e.data)
	}

	for _, e := range entries {
		out.Write(e.data)
	}
	return out.Bytes(), nil
}

// encodeDIB renders one size as the BITMAPINFOHEADER + BGRA + AND-mask form
// that .ico entries below 256px use.
func encodeDIB(img image.Image) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	var out bytes.Buffer

	// The height is doubled because the colour bitmap and the AND mask share
	// one block, both stored bottom-up.
	hdr := make([]byte, 40)
	binary.LittleEndian.PutUint32(hdr[0:4], 40)           // biSize
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(w))    // biWidth
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(h*2)) // biHeight
	binary.LittleEndian.PutUint16(hdr[12:14], 1)          // biPlanes
	binary.LittleEndian.PutUint16(hdr[14:16], 32)         // biBitCount
	binary.LittleEndian.PutUint32(hdr[16:20], 0)          // BI_RGB
	out.Write(hdr)

	row := make([]byte, 0, w*4)
	for y := h - 1; y >= 0; y-- {
		row = row[:0]
		for x := 0; x < w; x++ {
			r, g, bb, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			row = append(row, byte(bb>>8), byte(g>>8), byte(r>>8), byte(a>>8))
		}
		out.Write(row)
	}

	// The AND mask is 1bpp with rows padded to 4 bytes. All zeros, because the
	// icon is opaque and transparency already lives in the alpha channel.
	maskStride := ((w + 31) / 32) * 4
	out.Write(make([]byte, maskStride*h))

	return out.Bytes(), nil
}

// resize scales to a square, fitting by aspect ratio and centring the result.
//
// Stretching would visibly distort the logo, and anchoring to a corner would
// shift it off-centre in the taskbar.
func resize(src image.Image, size int) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))

	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return dst
	}

	scale := float64(size) / float64(sw)
	if h := float64(sh) * scale; h > float64(size) {
		scale = float64(size) / float64(sh)
	}
	dw := int(float64(sw) * scale)
	dh := int(float64(sh) * scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	offsetX := (size - dw) / 2
	offsetY := (size - dh) / 2

	// Nearest-neighbour keeps edges crisp at 16px, where smoothing would smear
	// a thin logo into nothing.
	for y := 0; y < dh; y++ {
		sy := b.Min.Y + y*sh/dh
		for x := 0; x < dw; x++ {
			dst.Set(offsetX+x, offsetY+y, src.At(b.Min.X+x*sw/dw, sy))
		}
	}
	return dst
}
