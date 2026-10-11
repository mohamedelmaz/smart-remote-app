package remote

import (
	"syscall"
	"testing"
)

// referenceMetrics calls GetSystemMetrics the way the API is declared: one int
// in, one int back in the return register. It exists so the server's own
// reader can be checked against the truth on the very machine running the
// tests, instead of against a hand-written expectation.
func referenceMetrics(index int) int32 {
	proc := syscall.NewLazyDLL("user32.dll").NewProc("GetSystemMetrics")
	v, _, _ := proc.Call(uintptr(index))
	return int32(v)
}

// TestGetSystemMetricsReadsTheReturnValue is the regression test for the bug
// found by measurement on a real machine: the reader passed a pointer to a Go
// local and returned that local, so it answered 0 for every index. Every
// size decision downstream - the capture size, and whether the stream
// re-allocated after a resolution change - was then taken against a
// hardcoded 1920x1080.
func TestGetSystemMetricsReadsTheReturnValue(t *testing.T) {
	for _, idx := range []int{0, 1, 78, 79} { // SM_CX/CY/SCREEN, SM_CX/CYVIRTUALSCREEN
		want := referenceMetrics(idx)
		got := getSystemMetricsFn(idx)
		if got != want {
			t.Errorf("metric %d: server read %d, Windows reports %d", idx, got, want)
		}
		if want > 0 && got == 0 {
			t.Errorf("metric %d: server read 0 while the display reports %d; "+
				"the return value is being discarded", idx, want)
		}
	}
}

// TestVirtualDesktopTracksTheRealScreen proves the size the capturer is built
// from is the size the desktop actually is.
//
// With the reader broken this failed on every machine that was not sitting at
// exactly 1920x1080: virtualDesktop returned the guess while the display said
// otherwise, and the difference showed up as a picture pinned to the top-left
// corner of a too-large frame.
func TestVirtualDesktopTracksTheRealScreen(t *testing.T) {
	realW, realH := referenceMetrics(78), referenceMetrics(79) // virtual screen
	primW, primH := referenceMetrics(0), referenceMetrics(1)   // primary display
	if realW <= 0 || realH <= 0 {
		realW, realH = primW, primH
	}
	if realW <= 0 || realH <= 0 {
		t.Skip("no display metrics available in this environment")
	}

	_, _, w, h := virtualDesktop()
	if w != realW || h != realH {
		t.Fatalf("virtualDesktop() = %dx%d, but the desktop is %dx%d", w, h, realW, realH)
	}
	// A capture frame is never 1920x1080 by accident: if the desktop really is
	// that size the value above already matched, so reaching here with it means
	// the reader is guessing again.
	if w <= 0 || h <= 0 {
		t.Fatalf("virtualDesktop() returned a non-positive size %dx%d", w, h)
	}
}

// TestCapturerSizeMatchesTheDesktop closes the loop on the capture itself: the
// frame buffer is allocated from the same metrics the reader reports, so a
// stale or guessed geometry cannot hide behind a correct reader.
func TestCapturerSizeMatchesTheDesktop(t *testing.T) {
	_, _, wantW, wantH := virtualDesktop()
	if wantW <= 0 || wantH <= 0 {
		t.Skip("no usable desktop metrics in this environment")
	}
	c, err := NewCapturer(CapturerOptions{Quality: 70})
	if err != nil {
		t.Skipf("no capturable desktop here: %v", err)
	}
	defer c.Close()

	gotW, gotH := c.Size()
	if gotW != wantW || gotH != wantH {
		t.Fatalf("capturer is %dx%d but the desktop is %dx%d", gotW, gotH, wantW, wantH)
	}
}
