package remote

import (
	"strings"
	"testing"
)

// swapMetrics installs a fake GetSystemMetrics driven by a table of answers.
func swapMetrics(t *testing.T, table map[int]int32) *[]string {
	t.Helper()
	var warned []string
	prevMetrics := getSystemMetricsFn
	prevWarn := onMetricsFallback
	getSystemMetricsFn = func(index int) int32 { return table[index] }
	onMetricsFallback = func(msg string) { warned = append(warned, msg) }
	t.Cleanup(func() {
		getSystemMetricsFn = prevMetrics
		onMetricsFallback = prevWarn
	})
	return &warned
}

// The ordinary path must stay untouched: a real multi-monitor desktop returns
// the virtual bounds and nothing else.
func TestVirtualDesktopUsesVirtualScreen(t *testing.T) {
	warned := swapMetrics(t, map[int]int32{76: -1920, 77: 0, 78: 3840, 79: 1080, 0: 2560, 1: 1440})

	ox, oy, w, h := virtualDesktop()
	if ox != -1920 || oy != 0 || w != 3840 || h != 1080 {
		t.Fatalf("got (%d,%d,%d,%d), want (-1920,0,3840,1080)", ox, oy, w, h)
	}
	if len(*warned) != 0 {
		t.Fatalf("warned on a healthy desktop: %v", *warned)
	}
}

// The fix: a missing virtual screen falls back to the real primary display
// size, not to a blind 1920x1080 guess.
func TestVirtualDesktopFallsBackToPrimaryDisplay(t *testing.T) {
	warned := swapMetrics(t, map[int]int32{76: 0, 77: 0, 78: 0, 79: 0, 0: 2560, 1: 1440})

	ox, oy, w, h := virtualDesktop()
	if ox != 0 || oy != 0 {
		t.Fatalf("origin = (%d,%d), want (0,0) for a single display", ox, oy)
	}
	if w != 2560 || h != 1440 {
		t.Fatalf("size = %dx%d, want the primary display 2560x1440", w, h)
	}
	if len(*warned) != 1 {
		t.Fatalf("want exactly one warning, got %v", *warned)
	}
	if !strings.Contains((*warned)[0], "2560x1440") {
		t.Fatalf("warning does not name the size actually used: %q", (*warned)[0])
	}
}

// With no metrics at all the 1920x1080 guess remains, but it must say so:
// silently capturing the wrong size is what made this confusing to diagnose.
func TestVirtualDesktopLastResortStillGuesses(t *testing.T) {
	warned := swapMetrics(t, map[int]int32{})

	_, _, w, h := virtualDesktop()
	if w != 1920 || h != 1080 {
		t.Fatalf("size = %dx%d, want the 1920x1080 last resort", w, h)
	}
	if len(*warned) != 1 {
		t.Fatalf("want exactly one warning, got %v", *warned)
	}
	if !strings.Contains((*warned)[0], "1920x1080") {
		t.Fatalf("last-resort warning should name the guessed size: %q", (*warned)[0])
	}
}

// A half-zero answer is still broken and must not be taken as a real size.
func TestVirtualDesktopRejectsHalfZeroVirtualScreen(t *testing.T) {
	swapMetrics(t, map[int]int32{78: 3840, 79: 0, 0: 1280, 1: 1024})

	_, _, w, h := virtualDesktop()
	if w != 1280 || h != 1024 {
		t.Fatalf("size = %dx%d, want the primary display 1280x1024", w, h)
	}
}
