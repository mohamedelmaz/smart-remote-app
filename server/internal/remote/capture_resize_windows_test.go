package remote

import (
	"bytes"
	"image/jpeg"
	"testing"
)

// stubMetrics installs a controllable desktop geometry and returns a func that
// changes it, plus a restore func.
func stubMetrics(t *testing.T, w, h int32) (set func(w, h int32), restore func()) {
	t.Helper()
	prev := virtualDesktopFn
	virtualDesktopFn = func() (int32, int32, int32, int32) { return 0, 0, w, h }
	return func(nw, nh int32) { w, h = nw, nh }, func() { virtualDesktopFn = prev }
}

// The core promise of the resize fix: after the desktop changes size, the next
// capture is built at the NEW size instead of the one it was created with.
func TestCapturerFollowsResolutionChange(t *testing.T) {
	set, restore := stubMetrics(t, 1280, 720)
	defer restore()

	var gotOldW, gotOldH, gotNewW, gotNewH int32
	c, err := NewCapturer(CapturerOptions{
		Quality: 70,
		OnResize: func(ow, oh, nw, nh int32) {
			gotOldW, gotOldH, gotNewW, gotNewH = ow, oh, nw, nh
		},
	})
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	defer c.Close()

	if w, h := c.Size(); w != 1280 || h != 720 {
		t.Fatalf("initial size = %dx%d, want 1280x720", w, h)
	}

	set(1024, 768)
	if err := c.resizeIfChangedLocked(); err != nil {
		t.Fatalf("resizeIfChangedLocked: %v", err)
	}

	if w, h := c.Size(); w != 1024 || h != 768 {
		t.Fatalf("size after change = %dx%d, want 1024x768", w, h)
	}
	if gotOldW != 1280 || gotOldH != 720 || gotNewW != 1024 || gotNewH != 768 {
		t.Fatalf("OnResize got %dx%d -> %dx%d, want 1280x720 -> 1024x768",
			gotOldW, gotOldH, gotNewW, gotNewH)
	}
	// The DIB backing store must match the new geometry, not the old one.
	if want := 1024 * 768 * 4; len(c.pixels) != want {
		t.Fatalf("pixel buffer = %d bytes, want %d", len(c.pixels), want)
	}
}

// An unchanged desktop must not churn GDI handles every single frame.
func TestCapturerDoesNotResizeWhenSizeIsStable(t *testing.T) {
	_, restore := stubMetrics(t, 1280, 720)
	defer restore()

	calls := 0
	c, err := NewCapturer(CapturerOptions{
		Quality:  70,
		OnResize: func(int32, int32, int32, int32) { calls++ },
	})
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	defer c.Close()

	for i := 0; i < 5; i++ {
		if err := c.resizeIfChangedLocked(); err != nil {
			t.Fatalf("resizeIfChangedLocked: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("OnResize fired %d times for an unchanging desktop, want 0", calls)
	}
}

// A monitor that reports zero mid-sleep must not tear down a working capture.
func TestCapturerIgnoresTransientZeroMetrics(t *testing.T) {
	set, restore := stubMetrics(t, 1280, 720)
	defer restore()

	c, err := NewCapturer(CapturerOptions{Quality: 70})
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	defer c.Close()

	set(0, 0)
	if err := c.resizeIfChangedLocked(); err != nil {
		t.Fatalf("resizeIfChangedLocked: %v", err)
	}
	if w, h := c.Size(); w != 1280 || h != 720 {
		t.Fatalf("size after zero metrics = %dx%d, want the capture kept at 1280x720", w, h)
	}
}

// The wiring test: the resize must happen on the ordinary Capture path, not
// only when someone remembers to call the helper by hand.
func TestCaptureAppliesTheResolutionChange(t *testing.T) {
	set, restore := stubMetrics(t, 1280, 720)
	defer restore()

	fired := false
	c, err := NewCapturer(CapturerOptions{
		Quality:  70,
		OnResize: func(int32, int32, int32, int32) { fired = true },
	})
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	defer c.Close()

	set(1024, 768)
	// A BitBlt failure in a headless environment is not what this test is
	// about; the resize is applied before the copy either way.
	_, _ = c.Capture()

	if !fired {
		t.Fatal("Capture did not react to the resolution change")
	}
	if w, h := c.Size(); w != 1024 || h != 768 {
		t.Fatalf("size after Capture = %dx%d, want 1024x768", w, h)
	}
}

// convertLocked is where the downscale must be applied. This drives it with a
// plain pixel buffer, so no desktop, no GDI and no timing are involved.
func TestConvertLockedAppliesTheWidthCap(t *testing.T) {
	const w, h = 3840, 2160
	c := &Capturer{
		width: w, height: h, stride: w * 4,
		pixels:   make([]byte, w*h*4),
		maxWidth: DefaultMaxFrameWidth,
	}
	for i := range c.pixels {
		c.pixels[i] = 0x7F
	}

	img := c.convertLocked()
	if img.Rect.Dx() != DefaultMaxFrameWidth {
		t.Fatalf("converted width = %d, want %d", img.Rect.Dx(), DefaultMaxFrameWidth)
	}
	if img.Rect.Dy() != 1440 {
		t.Fatalf("converted height = %d, want 1440", img.Rect.Dy())
	}
}

// At or below the cap nothing may change, or ordinary desktops would get a
// different-looking picture than they always had.
func TestConvertLockedLeavesNormalDesktopsAlone(t *testing.T) {
	const w, h = 1920, 1080
	c := &Capturer{
		width: w, height: h, stride: w * 4,
		pixels:   make([]byte, w*h*4),
		maxWidth: DefaultMaxFrameWidth,
	}
	// Distinct per-channel values, laid out as GDI stores them (B,G,R,A).
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := int(y)*int(c.stride) + x*4
			c.pixels[o], c.pixels[o+1], c.pixels[o+2] = 0x11, 0x22, 0x33
		}
	}

	img := c.convertLocked()
	if img.Rect.Dx() != w || img.Rect.Dy() != h {
		t.Fatalf("converted to %dx%d, want the untouched %dx%d", img.Rect.Dx(), img.Rect.Dy(), w, h)
	}
	// The BGRA->RGBA swap must still have happened exactly as before.
	want := []byte{0x33, 0x22, 0x11, 0xFF}
	for i, wv := range want {
		if img.Pix[i] != wv {
			t.Fatalf("pixel 0 = %v, want %v", img.Pix[:4], want)
		}
	}
}

// End to end: the JPEG a caller receives really is capped.
func TestCapturedFrameRespectsTheWidthCap(t *testing.T) {
	_, restore := stubMetrics(t, 1280, 720)
	defer restore()

	c, err := NewCapturer(CapturerOptions{Quality: 70, MaxWidth: 320})
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	defer c.Close()

	frame, err := c.Capture()
	if err != nil {
		t.Skipf("no capturable desktop in this environment: %v", err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Width > 320 {
		t.Fatalf("encoded frame is %d wide, want at most 320", cfg.Width)
	}
	if cfg.Height <= 0 {
		t.Fatalf("encoded frame height = %d", cfg.Height)
	}
}
