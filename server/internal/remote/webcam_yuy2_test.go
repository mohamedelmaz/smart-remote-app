//go:build windows

package remote

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
)

// The YUY2 → JPEG path is exercised with synthetic frames whose expected
// output is known exactly. That matters because the alternative — judging the
// converter by looking at real webcam frames — cannot tell a conversion bug
// apart from a camera that is returning black frames.

func TestYUY2ToJPEGNeutralStaysNeutral(t *testing.T) {
	const w, h int32 = 8, 4

	// Y=128 with neutral chroma (U=V=128) must decode back to mid grey.
	src := bytes.Repeat([]byte{128, 128, 128, 128}, int(w)*int(h))
	enc, err := yuy2ToJPEG(src, w, h, 95)
	if err != nil {
		t.Fatalf("yuy2ToJPEG: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(enc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := meanLuma(img); got < 120 || got > 136 {
		t.Errorf("neutral frame mean luma = %.1f, want ~128", got)
	}
}

func TestYUY2ToJPEGWhiteStaysWhite(t *testing.T) {
	const w, h int32 = 8, 4

	src := bytes.Repeat([]byte{255, 128, 255, 128}, int(w)*int(h))
	enc, err := yuy2ToJPEG(src, w, h, 95)
	if err != nil {
		t.Fatalf("yuy2ToJPEG: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(enc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := meanLuma(img); got < 245 {
		t.Errorf("white frame mean luma = %.1f, want ~255", got)
	}
}

// TestYUY2ToJPEGPreservesPixelOrder guards against swapping the two luma bytes
// of each YUY2 quad, which would mirror the image and is invisible in any
// uniform test frame.
func TestYUY2ToJPEGPreservesPixelOrder(t *testing.T) {
	const w, h int32 = 8, 4

	var src []byte
	for row := 0; row < int(h); row++ {
		for col := 0; col < int(w); col += 2 {
			src = append(src,
				uint8(col*255/int(w)), 128,
				uint8((col+1)*255/int(w)), 128)
		}
	}
	enc, err := yuy2ToJPEG(src, w, h, 100)
	if err != nil {
		t.Fatalf("yuy2ToJPEG: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(enc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	b := img.Bounds()
	var left, right float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		left += lumaAt(img, b.Min.X, y)
		right += lumaAt(img, b.Max.X-1, y)
	}
	left /= float64(b.Dy())
	right /= float64(b.Dy())
	if right-left < 100 {
		t.Errorf("luma ramp flattened: left %.1f, right %.1f (want a wide spread)", left, right)
	}
}

func TestYUY2ToJPEGRejectsBadInput(t *testing.T) {
	const w, h int32 = 8, 4
	need := int(w) * int(h) * 2

	if _, err := yuy2ToJPEG(make([]byte, need-1), w, h, 70); err == nil {
		t.Error("short frame was accepted, want an error")
	}
	if _, err := yuy2ToJPEG(make([]byte, need), 7, h, 70); err == nil {
		t.Error("odd width was accepted, want an error")
	}
	if _, err := yuy2ToJPEG(make([]byte, need), 0, h, 70); err == nil {
		t.Error("zero width was accepted, want an error")
	}
}

// TestPixelFormatString pins the log-facing names; they appear in the server log
// when diagnosing which camera path is in use.
func TestPixelFormatString(t *testing.T) {
	if got := formatMJPEG.String(); got != "MJPEG" {
		t.Errorf("formatMJPEG.String() = %q, want \"MJPEG\"", got)
	}
	if got := formatYUY2.String(); got != "YUY2" {
		t.Errorf("formatYUY2.String() = %q, want \"YUY2\"", got)
	}
}

// TestNegotiationCandidatesPrefersMJPEG guards the ordering that keeps cameras
// which can emit JPEG on the zero-conversion path: every MJPEG size is tried
// before the first YUY2 one.
func TestNegotiationCandidatesPrefersMJPEG(t *testing.T) {
	cands := negotiationCandidates()
	if len(cands) == 0 {
		t.Fatal("no negotiation candidates")
	}

	lastMJPEG, firstYUY2 := -1, -1
	for i, c := range cands {
		switch c.format {
		case formatMJPEG:
			if firstYUY2 >= 0 {
				t.Errorf("candidate %d is MJPEG but candidate %d was already YUY2", i, firstYUY2)
			}
			lastMJPEG = i
		case formatYUY2:
			if firstYUY2 < 0 {
				firstYUY2 = i
			}
		default:
			t.Errorf("candidate %d has unknown format %d", i, c.format)
		}
	}

	if lastMJPEG < 0 {
		t.Error("no MJPEG candidate: cameras that can emit JPEG lose the fast path")
	}
	if firstYUY2 < 0 {
		t.Error("no YUY2 candidate: cameras without MJPEG output could never open")
	}
	if firstYUY2 >= 0 && lastMJPEG > firstYUY2 {
		t.Errorf("MJPEG candidate %d comes after YUY2 candidate %d", lastMJPEG, firstYUY2)
	}
}

func meanLuma(img image.Image) float64 {
	b := img.Bounds()
	total := 0.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			total += lumaAt(img, x, y)
		}
	}
	return total / float64(b.Dx()*b.Dy())
}

func lumaAt(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return float64((r + g + b) / 3 >> 8)
}
