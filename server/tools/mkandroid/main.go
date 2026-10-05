// Command mkandroid generates the Android launcher icons from the source logo.
//
// WHY A TOOL RATHER THAN CHECKED-IN PNGs
// --------------------------------------
// The launcher icon is what the user sees first on their home screen, so it must
// come from the same logo as the tray and the taskbar. Committing the PNGs makes
// that relationship invisible: changing the logo would update the .ico and the
// in-app header but silently leave the launcher showing the old mark.
//
// Android also needs two different shapes. Legacy launchers take a full-bleed
// square and mask it themselves; adaptive icons take a foreground layer that the
// system crops to a squircle covering the middle ~72%, so the mark has to be
// inset with padding or its edges are cut off. Neither is reliable by hand.
//
// Usage, from the module root:
//
//	go run ./tools/mkandroid -in "path/to/logo.jpg" -out ../mobile/android/app/src/main/res
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

func init() { _ = jpeg.Decode }

// densities are the legacy launcher buckets: the mipmap folder and the pixel
// size Android expects for a full-bleed launcher icon.
var densities = []struct {
	dir  string
	size int
}{
	{"mipmap-mdpi", 48},
	{"mipmap-hdpi", 72},
	{"mipmap-xhdpi", 96},
	{"mipmap-xxhdpi", 144},
	{"mipmap-xxxhdpi", 192},
}

// foregroundScales are the adaptive-icon foreground sizes.
var foregroundScales = []struct {
	dir  string
	size int
}{
	{"mipmap-mdpi", 108},
	{"mipmap-hdpi", 162},
	{"mipmap-xhdpi", 216},
	{"mipmap-xxhdpi", 324},
	{"mipmap-xxxhdpi", 432},
}

// backgroundColor sits behind the adaptive foreground.
//
// The logo is a JPEG and so has no alpha channel. Without a solid background the
// device wallpaper would show through the transparent safe zone around the mark,
// and a dark logo would vanish into a dark wallpaper.
var backgroundColor = color.RGBA{0x0A, 0x0E, 0x1A, 0xFF}

// foregroundFill is the fraction of the canvas the mark occupies.
//
// 0.60 leaves room for the system's crop on every launcher mask, since the
// guaranteed-visible area of an adaptive icon is the middle 66dp of 108dp.
const foregroundFill = 0.60

func main() {
	in := flag.String("in", "", "source logo (JPEG or PNG)")
	out := flag.String("out", "", "Android res directory")
	showRows := flag.Bool("debug-rows", false,
		"print the per-row mark fraction and exit (calibration aid)")
	showComponents := flag.Bool("debug-components", false,
		"print every connected component found, with its bounding box")
	flag.Parse()

	if *in == "" {
		log.Fatal("usage: mkandroid -in <logo> -out <res dir>")
	}

	src, err := loadLogo(*in)
	if err != nil {
		log.Fatalf("load %s: %v", *in, err)
	}

	if *showRows {
		debugRows(src)
		return
	}
	if *out == "" {
		log.Fatal("-out is required unless -debug-rows is set")
	}

	mark := cropToMark(src, *showComponents)
	if mark == nil {
		// No brand-blue region was found, so the source is already a plain
		// logo; using it whole is better than refusing to build.
		fmt.Fprintln(os.Stderr,
			"WARNING: no mark detected in the source; using the whole image")
		mark = src
	}

	for _, d := range densities {
		icon := fit(mark, d.size)
		write(filepath.Join(*out, d.dir, "ic_launcher.png"), icon)
		write(filepath.Join(*out, d.dir, "ic_launcher_round.png"), circleMask(icon))
	}

	for _, f := range foregroundScales {
		markSize := int(float64(f.size) * foregroundFill)
		fg := image.NewRGBA(image.Rect(0, 0, f.size, f.size))
		drawCentered(fg, knockOutShadow(fit(mark, markSize)))
		write(filepath.Join(*out, f.dir, "ic_launcher_foreground.png"), fg)
	}

	writeBackground(*out)
	fmt.Printf("wrote %d legacy buckets and %d adaptive buckets to %s\n",
		len(densities), len(foregroundScales), *out)
}

// loadLogo decodes the source and flattens it onto an opaque background.
func loadLogo(path string) (image.Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	b := img.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(flat, flat.Bounds(), &image.Uniform{backgroundColor}, image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, b.Min, draw.Over)
	return flat, nil
}

// fit scales src so it FILLS a size x size square, then crops the overflow.
//
// Scaling by the larger ratio and centre-cropping keeps the mark the same size on
// every device. Fitting the whole logo inside the square instead would make it
// visibly smaller for a tall source image than for a square one.
//
// Nearest-neighbour is used through a manual loop: the standard library exports
// no resampler, and bilinear sampling of thin logo strokes turns to mush at
// 48px. Centring on the image's own middle rather than on the destination's
// centre matters for a source whose content is not exactly centred - otherwise
// the crop is biased and part of the mark is lost.
func fit(src image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{backgroundColor}, image.Point{}, draw.Src)

	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return dst
	}

	scale := math.Max(float64(size)/float64(sw), float64(size)/float64(sh))
	offX := (float64(size) - float64(sw)*scale) / 2
	offY := (float64(size) - float64(sh)*scale) / 2

	for y := 0; y < size; y++ {
		sy := b.Min.Y + int((float64(y)-offY)/scale)
		if sy < b.Min.Y || sy >= b.Max.Y {
			continue
		}
		for x := 0; x < size; x++ {
			sx := b.Min.X + int((float64(x)-offX)/scale)
			if sx < b.Min.X || sx >= b.Max.X {
				continue
			}
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// debugRows prints the per-row mark fraction, to calibrate the threshold
// against the real file rather than a guess.
//
// Run as:  go run ./tools/mkandroid -in <logo> -debug-rows
func debugRows(src image.Image) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	fmt.Printf("source %dx%d, threshold %.2f\n", w, h, rowDensityThreshold)
	for y := 0; y < h; y++ {
		hits := 0
		for x := 0; x < w; x++ {
			if isMarkPixel(src.At(b.Min.X+x, b.Min.Y+y)) {
				hits++
			}
		}
		frac := float64(hits) / float64(w)
		bar := ""
		n := int(frac * 40)
		for i := 0; i < n; i++ {
			bar += "#"
		}
		if frac > 0.005 {
			fmt.Printf("y=%3d %5.3f %s\n", y, frac, bar)
		}
	}
}

// rowDensityThreshold is retained only for -debug-rows, which is what
// established that density cannot separate the two parts of this source.
//
// Measured with -debug-rows on the shipped 1408x768 mockup:
//
//	y=164..536  the icon tile   0.21 - 0.29
//	y=542..598 the "smart-remote" caption   up to 0.288
//
// The caption is *denser* than the icon: it is a heavy bold face on one tight
// line, whereas the icon has transparent gaps between its glyphs. No threshold
// on row density can separate them, and every value tried here either kept the
// caption or cut the icon in half. The separation that does work is vertical
// extent - the icon is a ~380px square, the caption a ~55px line - so the crop
// is found by shape instead. See cropToMark.
const rowDensityThreshold = 0.22

// cropToMark finds the logo tile inside the source and crops to it.
//
// The source shipped with this project is a presentation mockup rather than a
// bare logo: a wide 1408x768 sheet with the app icon centred and the wordmark
// "smart-remote" beneath it. Centre-cropping that to a square gives an icon with
// the caption sliced through it, which is what a plain resize produces.
//
// THRESHOLDS DO NOT WORK HERE
// ---------------------------
// Every attempt to separate the two by colour or by density failed, and -debug-
// rows is what proved it. Measured on the 1408x768 source:
//
//	y=164..536  the icon tile            0.21 - 0.29 mark fraction per row
//	y=542..598 the "smart-remote" line   up to 0.288
//
// The caption is *denser* than the icon. It is a heavy bold face on one tight
// line, while the icon has light gaps between its glyphs, so every threshold
// that keeps the caption also keeps most of the icon, and every threshold that
// drops the caption cuts the icon in half.
//
// CONNECTED COMPONENTS DO WORK
// ----------------------------
// The two differ in a way that is not about pixels at all: the tile is a single
// large connected blob, whereas the caption is a dozen small disconnected ones.
// So the image is flood-filled into connected components of non-background
// pixels and the largest by area is taken. This needs no colour threshold tuned
// to this particular file - only the observation that a logo is one big shape.
func cropToMark(src image.Image, verbose bool) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}

	// A component smaller than this fraction of the image is text or noise, not
	// the logo. On a 1408x768 source that is ~65k pixels, which the ~380px tile
	// clears by a wide margin and which no single letter comes close to.
	minComponentArea := w * h / 200

	// Visited mask over the mark pixels.
	seen := make([]bool, w*h)
	stack := make([]int, 0, 4096)

	// EROSION: a pixel only seeds the fill if it and its four neighbours are
	// all mark pixels.
	//
	// Without this the tile's drop shadow forms a one-pixel bridge down to the
	// caption's lettering, so the flood fill walks straight from the icon into
	// the wordmark and the resulting bounding box swallows it - which is
	// exactly the defect this function exists to prevent.
	//
	// Eroding by one pixel is safe here because the tile is a solid block: its
	// interior is far wider than the one pixel given up at each edge, while the
	// shadow bridge is only a pixel or two thick and disappears entirely.
	solid := func(x, y int) bool {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if nx < 0 || ny < 0 || nx >= w || ny >= h {
					return false
				}
				if !isMarkPixel(src.At(b.Min.X+nx, b.Min.Y+ny)) {
					return false
				}
			}
		}
		return true
	}

	best := image.Rectangle{}
	bestArea := 0

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			if seen[idx] {
				continue
			}
			if !solid(x, y) {
				// Marked but not solid: still mark it seen so the scan moves on,
				// but do not grow a component from it.
				seen[idx] = true
				continue
			}

			// Flood fill this component, 4-connected.
			minX, maxX, minY, maxY := x, x, y, y
			area := 0

			stack = append(stack[:0], idx)
			seen[idx] = true

			for len(stack) > 0 {
				cur := stack[len(stack)-1]
				stack = stack[:len(stack)-1]

				curX := cur % w
				curY := cur / w
				area++

				if curX < minX {
					minX = curX
				}
				if curX > maxX {
					maxX = curX
				}
				if curY < minY {
					minY = curY
				}
				if curY > maxY {
					maxY = curY
				}

				// Neighbours, bounds-checked. These test solid rather than
				// isMarkPixel, so the fill cannot creep back across the shadow
				// bridge it was seeded away from.
				if curX > 0 {
					pushPixel(&stack, seen, solid, w, cur-1)
				}
				if curX < w-1 {
					pushPixel(&stack, seen, solid, w, cur+1)
				}
				if curY > 0 {
					pushPixel(&stack, seen, solid, w, cur-w)
				}
				if curY < h-1 {
					pushPixel(&stack, seen, solid, w, cur+w)
				}
			}

			if area > bestArea && area >= minComponentArea {
				bestArea = area
				best = image.Rect(minX, minY, maxX+1, maxY+1)
			}
			// The bounding box a component reported, not the best so far.
			if verbose {
				fmt.Printf("component rect=%v area=%d\n",
					image.Rect(minX, minY, maxX+1, maxY+1), area)
			}
		}
	}

	if bestArea == 0 {
		return nil
	}

	return cropSquare(src, b, best)
}

// pushPixel adds an eroded-mark pixel to the flood-fill stack.
//
// Marking on push rather than on pop is what makes the fill terminate: a pixel
// is queued at most once, so an image with a large blob cannot loop forever.
func pushPixel(stack *[]int, seen []bool, solid func(int, int) bool,
	w, idx int) {
	if seen[idx] {
		return
	}
	seen[idx] = true
	if !solid(idx%w, idx/w) {
		return
	}
	*stack = append(*stack, idx)
}

// cropSquare extracts a square region centred on rect.
//
// The side is the SHORTER of the component's two dimensions, not the longer.
//
// This matters on the shipped source. The tile's drop shadow drags its bounding
// box down to y=561 while its visible body ends near y=535, making the box
// 342x393 - clearly not a square. Sizing on the longer side then produces a
// 393px square spanning y=168..561, which reaches back down into the wordmark
// starting at y=543 and prints it across the bottom of the icon. Sizing on the
// shorter side yields a 342px square spanning y=193..535, which stops short of
// the caption while keeping the mark centred.
func cropSquare(src image.Image, b image.Rectangle, rect image.Rectangle) image.Image {
	wRect, hRect := rect.Dx(), rect.Dy()
	if wRect <= 0 || hRect <= 0 {
		return nil
	}

	side := wRect
	if hRect < side {
		side = hRect
	}

	cx := rect.Min.X + wRect/2
	cy := rect.Min.Y + hRect/2

	x0 := cx - side/2
	y0 := cy - side/2

	out := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			sx, sy := x0+x, y0+y
			if sx < 0 || sy < 0 || sx >= b.Dx() || sy >= b.Dy() {
				out.Set(x, y, backgroundColor)
				continue
			}
			out.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return out
}

// knockOutShadow makes the pale halo around the tile transparent.
//
// The source tile carries a soft light drop shadow, and the crop taken around it
// includes that halo. It is invisible on the legacy icons, where the whole
// square is used as-is, but the adaptive foreground is composited over the dark
// background layer: there the halo reads as a bright rectangular fringe around
// the mark and is clearly wrong.
//
// Clearing anything brighter than the mark itself to fully transparent is safe
// because the tile's own body is uniformly dark; only the shadow and the
// antialiased outer edge are light, and neither belongs in the foreground.
func knockOutShadow(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(b)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			lum := (int(r>>8)*299 + int(g>>8)*587 + int(bl>>8)*114) / 1000
			if lum >= markLuminanceMax {
				out.SetRGBA(x, y, color.RGBA{})
				continue
			}
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8),
				A: uint8(a >> 8),
			})
		}
	}
	return out
}

// isMarkPixel reports whether a pixel belongs to the logo tile.
//
// DARKNESS, NOT COLOUR
// --------------------
// An earlier version classified a pixel by being blue-tinted, on the assumption
// that the mockup sat on a neutral grey sheet. It does not: the sheet is a pale
// *blue* gradient, so every background pixel passed the test, the flood fill
// merged the tile, the caption and the whole sheet into a single component, and
// the crop came out as a near-copy of the entire image with the wordmark
// printed across it.
//
// What actually distinguishes the mark from its backdrop is lightness. The tile
// is a dark navy rounded square; both the sheet and the caption lettering are
// light. Testing luminance alone therefore needs no knowledge of the brand hue
// and works regardless of what colour the presentation backdrop was made.
func isMarkPixel(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	lum := (int(r>>8)*299 + int(g>>8)*587 + int(b>>8)*114) / 1000
	return lum < markLuminanceMax
}

// markLuminanceMax is the brightest a logo pixel may be and still count.
//
// The tile's own shading runs from roughly 20 to 120; the backdrop runs from
// about 200 upward. 160 sits in the empty gap between them.
const markLuminanceMax = 160

// circleMask cuts src to a circle, for the round launcher variant.
//
// The edge is softened by a pixel: a hard cut leaves jaggies that are clearly
// visible at 192px on a modern display.
func circleMask(src *image.RGBA) *image.RGBA {
	size := src.Bounds().Dx()
	out := image.NewRGBA(image.Rect(0, 0, size, size))

	cx, cy := float64(size)/2, float64(size)/2
	r := float64(size) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			dist := math.Sqrt(dx*dx + dy*dy)
			switch {
			case dist <= r-1:
				out.Set(x, y, src.At(x, y))
			case dist >= r:
				// Left transparent.
			default:
				a := r - dist
				cr, cg, cb, ca := src.At(x, y).RGBA()
				out.SetRGBA(x, y, color.RGBA{
					R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8),
					A: uint8(float64(ca>>8) * a),
				})
			}
		}
	}
	return out
}

// drawCentered composites src at the centre of dst.
func drawCentered(dst, src *image.RGBA) {
	offX := (dst.Bounds().Dx() - src.Bounds().Dx()) / 2
	offY := (dst.Bounds().Dy() - src.Bounds().Dy()) / 2
	for y := 0; y < src.Bounds().Dy(); y++ {
		for x := 0; x < src.Bounds().Dx(); x++ {
			dst.Set(offX+x, offY+y, src.At(x, y))
		}
	}
}

// writeBackground emits the adaptive-icon background layer.
//
// A vector drawable would pull in AndroidX for a flat colour, so this is a
// colour resource instead: one file, no new dependency, and res/ is already on
// the generated path so the build picks it up with no Gradle change.
func writeBackground(res string) {
	dir := filepath.Join(res, "values")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("create %s: %v", dir, err)
	}
	body := `<?xml version="1.0" encoding="utf-8"?>
<!-- Adaptive-icon background. Generated by tools/mkandroid; do not edit. -->
<resources>
    <color name="ic_launcher_background">#0A0E1A</color>
</resources>
`
	if err := os.WriteFile(filepath.Join(dir, "ic_launcher_background.xml"),
		[]byte(body), 0o644); err != nil {
		log.Fatalf("write background colour: %v", err)
	}
}

// write encodes img as PNG, creating the directory if needed.
func write(path string, img image.Image) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		log.Fatalf("write %s: %v", path, err)
	}
}
