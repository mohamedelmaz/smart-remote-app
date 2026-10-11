package remote

import (
	"image"
	"testing"
)

// solidFrame builds a w x h RGBA image filled with one colour.
func solidFrame(w, h int, r, g, b uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		o := i * 4
		img.Pix[o] = r
		img.Pix[o+1] = g
		img.Pix[o+2] = b
		img.Pix[o+3] = 0xFF
	}
	return img
}

// A desktop that already fits must come back as the very same image, not a
// copy: the common case has to stay byte-for-byte what it was.
func TestScaleToMaxWidthLeavesFittingFramesUntouched(t *testing.T) {
	for _, w := range []int{1920, 2560} {
		src := solidFrame(w, 1080, 10, 20, 30)
		got := scaleToMaxWidth(src, DefaultMaxFrameWidth)
		if got != src {
			t.Fatalf("width %d: frame was copied/re-encoded, want the original pointer", w)
		}
		if got.Rect.Dx() != w {
			t.Fatalf("width %d: got %d", w, got.Rect.Dx())
		}
	}
}

// Above the cap the frame is clamped to exactly the cap width.
func TestScaleToMaxWidthClampsWideDesktops(t *testing.T) {
	got := scaleToMaxWidth(solidFrame(3840, 2160, 1, 2, 3), DefaultMaxFrameWidth)
	if got.Rect.Dx() != DefaultMaxFrameWidth {
		t.Fatalf("width = %d, want %d", got.Rect.Dx(), DefaultMaxFrameWidth)
	}
	// 2160 * 2560 / 3840 == 1440.
	if got.Rect.Dy() != 1440 {
		t.Fatalf("height = %d, want 1440", got.Rect.Dy())
	}
}

// A non-integer ratio (ultrawide 3440x1440 -> 2560) must keep the proportion
// instead of truncating the height a pixel low on every frame.
func TestScaleToMaxWidthPreservesNonIntegerRatio(t *testing.T) {
	got := scaleToMaxWidth(solidFrame(3440, 1440, 1, 2, 3), DefaultMaxFrameWidth)
	if got.Rect.Dy() != 1072 {
		t.Fatalf("height = %d, want 1072 (1440*2560/3440 rounded)", got.Rect.Dy())
	}
}

// Averaging, not sampling. The black/white edge sits on an odd column so it
// falls *inside* a destination block.
func TestScaleToMaxWidthAveragesSourcePixels(t *testing.T) {
	const edge = 3 // columns 0..2 black, 3..7 white
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			o := y*src.Stride + x*4
			v := uint8(0)
			if x >= edge {
				v = 255
			}
			src.Pix[o], src.Pix[o+1], src.Pix[o+2], src.Pix[o+3] = v, v, v, 0xFF
		}
	}
	got := scaleToMaxWidth(src, 4)
	if got.Rect.Dx() != 4 || got.Rect.Dy() != 4 {
		t.Fatalf("dims = %dx%d, want 4x4", got.Rect.Dx(), got.Rect.Dy())
	}
	// Output column 1 covers source columns 2 and 3, straddling the edge, so it
	// must land on the midpoint. Nearest-neighbour copies 0 or 255 and fails.
	want := []uint8{0, 127, 255, 255}
	for x, w := range want {
		if g := got.Pix[x*4]; g != w {
			t.Fatalf("column %d = %d, want %d (mid block must average across the edge)", x, g, w)
		}
	}
}

// A cap that is absent or nonsensical must not be mistaken for "shrink to 1".
func TestScaleToMaxWidthIgnoresUnsetCap(t *testing.T) {
	src := solidFrame(3840, 2160, 1, 2, 3)
	if got := scaleToMaxWidth(src, 0); got != src {
		t.Fatal("maxWidth 0 must leave the frame alone")
	}
	if got := scaleToMaxWidth(src, -5); got != src {
		t.Fatal("negative maxWidth must leave the frame alone")
	}
	// A cap wider than the frame is not a reason to upscale.
	if got := scaleToMaxWidth(src, 5000); got != src {
		t.Fatal("maxWidth wider than the frame must leave it alone")
	}
}
