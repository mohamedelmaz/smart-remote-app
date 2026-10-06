//go:build windows && live

package remote

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"log"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// This file is a live diagnostic against the real camera. It is excluded from
// normal builds (build tag `live`) and skipped unless LIVE_WEBCAM=1, so CI and
// `go test ./...` never touch hardware. Run with:
//
//	$env:LIVE_WEBCAM='1'; go test -tags live -run TestLiveWebcamProbe -v ./internal/remote
//
// It answers two questions that the 18:08 incident left open:
//  1. Does Capture() ever return? (If not, that is the exact wedge that left
//     the stream lock held and every later request answered 409.)
//  2. What is actually in the frame buffer, and which VFW messages does this
//     driver honour?

var procSendMessageTimeout = user32.NewProc("SendMessageTimeoutA")

const (
	smtoBlock       = 0x0001
	smtoAbortIfHung = 0x0002

	msgSetCallbackKeyed = 0x0405 // what serve() currently sends as "GETFRAME"
	msgSetCallbackFrame = 0x0406
	msgGrabFrame        = 0x043C
	msgGrabFrameNoStop  = 0x043D
	msgSetPreview       = 0x0432
	msgSetPreviewScale  = 0x0433
	msgGetVideoFormat   = 0x042C
	msgDriverDisconnect = 0x040B
)

// captureOutcome is one Capture() result or its absence.
type captureOutcome struct {
	jpeg []byte
	err  error
}

// captureWithTimeout calls Capture() and reports a wedge instead of hanging
// the test.
func captureWithTimeout(t *testing.T, c *WebcamCapturer, timeout time.Duration) ([]byte, error) {
	t.Helper()
	ch := make(chan captureOutcome, 1)
	go func() {
		j, err := c.Capture()
		ch <- captureOutcome{jpeg: j, err: err}
	}()
	select {
	case o := <-ch:
		return o.jpeg, o.err
	case <-time.After(timeout):
		t.Logf("Capture() DID NOT RETURN within %v -> WEDGE REPRODUCED", timeout)
		return nil, fmt.Errorf("capture wedged after %v", timeout)
	}
}

// jpegStats logs size and brightness so a black frame is unmistakable.
func jpegStats(t *testing.T, tag string, b []byte, err error) {
	t.Helper()
	if err != nil {
		t.Logf("%s: err=%v", tag, err)
		return
	}
	img, derr := jpeg.Decode(bytes.NewReader(b))
	if derr != nil {
		t.Logf("%s: %d bytes but decode failed: %v", tag, len(b), derr)
		return
	}
	bounds := img.Bounds()
	total, lit := 0, 0
	var sum uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			v := int((r>>8)+(g>>8)+(bl>>8)) / 3
			sum += uint64(v)
			total++
			if v > 30 {
				lit++
			}
		}
	}
	avg := 0.0
	if total > 0 {
		avg = float64(sum) / float64(total)
	}
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(lit) / float64(total)
	}
	t.Logf("%s: %d bytes, %dx%d, avg-brightness=%.1f, lit-pixels=%.1f%%",
		tag, len(b), bounds.Dx(), bounds.Dy(), avg, pct)
}

// sendTimeoutMsg probes a window message from the test goroutine. SMTO with
// SMTO_ABORTIFHUNG guarantees a wedged capture thread cannot hang the probe.
func sendTimeoutMsg(t *testing.T, hwnd uintptr, msg, w, l uintptr, ms uint32) uintptr {
	t.Helper()
	var result uintptr
	start := time.Now()
	r, _, _ := procSendMessageTimeout.Call(hwnd, msg, w, l,
		smtoBlock|smtoAbortIfHung, uintptr(ms), uintptr(unsafe.Pointer(&result)))
	elapsed := time.Since(start)
	if r == 0 {
		t.Logf("SMTO msg=0x%04X: no answer after %v (window thread wedged or driver refused)", msg, elapsed)
		return 0
	}
	t.Logf("SMTO msg=0x%04X: returned %d after %v", msg, result, elapsed)
	return result
}

// bitBltFromWindow reads the capture window's own surface into a scratch DIB
// and reports how much of it is non-black - i.e. whether the driver paints
// anything visible without a grab message.
func bitBltFromWindow(t *testing.T, c *WebcamCapturer) {
	t.Helper()
	w, h := int(c.width), int(c.height)
	if w <= 0 || h <= 0 {
		t.Logf("BitBlt probe: bad size %dx%d", w, h)
		return
	}
	srcDC, _, _ := procGetDCHandle.Call(c.capture)
	if srcDC == 0 {
		t.Log("BitBlt probe: GetDC(capture) failed")
		return
	}
	defer procReleaseDCHandle.Call(c.capture, srcDC)

	memDC, _, _ := procCreateCompatibleDC.Call(srcDC)
	if memDC == 0 {
		t.Log("BitBlt probe: CreateCompatibleDC failed")
		return
	}
	defer procDeleteDC.Call(memDC)

	info := bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    int32(w),
		Height:   -int32(h),
		Planes:   1,
		BitCount: 32,
	}
	var bits uintptr
	dib, _, _ := procCreateDIBSection.Call(
		memDC, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == 0 {
		t.Log("BitBlt probe: CreateDIBSection failed")
		return
	}
	old, _, _ := procSelectObject.Call(memDC, dib)
	ret, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h),
		srcDC, 0, 0, srccopy)
	nonzero, total := 0, w*h*4
	if ret != 0 && bits != 0 {
		slice := unsafe.Slice((*byte)(unsafe.Pointer(bits)), total)
		for _, b := range slice {
			if b != 0 {
				nonzero++
			}
		}
	}
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(nonzero) / float64(total)
	}
	t.Logf("BitBlt probe: ret=%d, %d/%d bytes nonzero (%.2f%%)", ret, nonzero, total, pct)
	procSelectObject.Call(memDC, old)
	procDeleteObject.Call(dib)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// bitBltWindowStats BitBlts the capture window's actual client area into a
// scratch DIB and reports brightness, so "the driver paints the window but not
// our DIB" becomes a visible measurement.
func bitBltWindowStats(t *testing.T, c *WebcamCapturer, tag string) {
	t.Helper()
	var rect struct{ Left, Top, Right, Bottom int32 }
	pr, _, _ := procGetClientRect.Call(c.capture, uintptr(unsafe.Pointer(&rect)))
	w, h := int(rect.Right-rect.Left), int(rect.Bottom-rect.Top)
	if pr == 0 || w <= 0 || h <= 0 {
		t.Logf("%s: GetClientRect failed (%dx%d)", tag, w, h)
		return
	}
	srcDC, _, _ := procGetDCHandle.Call(c.capture)
	if srcDC == 0 {
		t.Logf("%s: GetDC(capture) failed", tag)
		return
	}
	defer procReleaseDCHandle.Call(c.capture, srcDC)

	memDC, _, _ := procCreateCompatibleDC.Call(srcDC)
	if memDC == 0 {
		t.Logf("%s: CreateCompatibleDC failed", tag)
		return
	}
	defer procDeleteDC.Call(memDC)

	info := bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    int32(w),
		Height:   -int32(h),
		Planes:   1,
		BitCount: 32,
	}
	var bits uintptr
	dib, _, _ := procCreateDIBSection.Call(
		memDC, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == 0 {
		t.Logf("%s: CreateDIBSection failed", tag)
		return
	}
	old, _, _ := procSelectObject.Call(memDC, dib)
	ret, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h),
		srcDC, 0, 0, srccopy)
	nonzero, total := 0, w*h*4
	if ret != 0 && bits != 0 {
		slice := unsafe.Slice((*byte)(unsafe.Pointer(bits)), total)
		for _, b := range slice {
			if b != 0 {
				nonzero++
			}
		}
	}
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(nonzero) / float64(total)
	}
	t.Logf("%s: %dx%d window, BitBlt ret=%d, %d/%d nonzero (%.2f%%)",
		tag, w, h, ret, nonzero, total, pct)
	procSelectObject.Call(memDC, old)
	procDeleteObject.Call(dib)
}

var procGetClientRect = user32.NewProc("GetClientRect")

// TestLiveWebcamProbe runs the production capture path against the real camera
// and reports exactly where it stops.
func TestLiveWebcamProbe(t *testing.T) {
	if os.Getenv("LIVE_WEBCAM") == "" {
		t.Skip("set LIVE_WEBCAM=1 to run against the real camera")
	}

	start := time.Now()
	logf := func(format string, args ...any) {
		t.Logf("[%6.2fs] "+format, append([]any{time.Since(start).Seconds()}, args...)...)
	}

	c, err := NewWebcamCapturer(WebcamOptions{
		Quality: 70,
		Logger:  log.New(os.Stderr, "probe ", log.Ltime|log.Lmicroseconds),
	})
	if err != nil {
		t.Fatalf("NewWebcamCapturer: %v", err)
	}
	logf("capturer opened (camera light should be on now)")

	// 1) Production path first, in the untouched state the 18:08 handler saw.
	wedged := false
	for i := 1; i <= 3; i++ {
		t0 := time.Now()
		j, err := captureWithTimeout(t, c, 5*time.Second)
		if err != nil && bytes.Contains([]byte(err.Error()), []byte("wedged")) {
			wedged = true
			logf("Capture#%d WEDGED after %v", i, time.Since(t0))
			break
		}
		logf("Capture#%d returned in %v", i, time.Since(t0))
		jpegStats(t, fmt.Sprintf("Capture#%d", i), j, err)
	}

	// 2) DIB contents as the driver left them.
	nonzero := 0
	for _, b := range c.pixels {
		if b != 0 {
			nonzero++
		}
	}
	logf("DIB pixels: %d/%d nonzero (%.2f%%)",
		nonzero, len(c.pixels),
		100*float64(nonzero)/float64(maxInt(len(c.pixels), 1)))

	// 3) Raw message probes. If the thread is wedged these time out instead of
	//    hanging, which itself is the diagnosis.
	hwnd := c.capture
	logf("probing the message serve() sends every frame (0x0405):")
	sendTimeoutMsg(t, hwnd, msgSetCallbackKeyed, 0, 0, 3000)

	if !wedged {
		logf("probing real WM_CAP_GRAB_FRAME (0x043C):")
		sendTimeoutMsg(t, hwnd, msgGrabFrame, 0, 0, 3000)
		j, err := captureWithTimeout(t, c, 5*time.Second)
		jpegStats(t, "after GRAB_FRAME", j, err)

		logf("probing WM_CAP_GRAB_FRAME_NOSTOP (0x043D):")
		sendTimeoutMsg(t, hwnd, msgGrabFrameNoStop, 0, 0, 3000)
		j, err = captureWithTimeout(t, c, 5*time.Second)
		jpegStats(t, "after GRAB_FRAME_NOSTOP", j, err)

		logf("BitBlt of the capture window surface:")
		bitBltFromWindow(t, c)

		logf("probing WM_CAP_SET_PREVIEW on (0x0432 w=1):")
		sendTimeoutMsg(t, hwnd, msgSetPreview, 1, 0, 3000)
		time.Sleep(500 * time.Millisecond)
		j, err = captureWithTimeout(t, c, 5*time.Second)
		jpegStats(t, "after SET_PREVIEW", j, err)
	} else {
		logf("skipping further message probes: capture thread already wedged")
	}

	// 4) Teardown: Close() must not hang even when the thread is wedged.
	closed := make(chan struct{})
	go func() { _ = c.Close(); close(closed) }()
	select {
	case <-closed:
		logf("Close() returned")
	case <-time.After(8 * time.Second):
		logf("Close() STILL BLOCKED after 8s -> Close() has no timeout either")
	}
	if wedged {
		t.Log("RESULT: Capture() wedge reproduced; see SMTO timings above")
	} else {
		t.Log("RESULT: no wedge; see frame stats above for the pixel path")
	}
}

// TestLiveWebcamFramePath experiments with ways of actually getting pixels out
// of the driver, because the production path (serve's SendMessage(0x0405) plus
// reading the app-owned DIB) yields 100% black frames: nothing ever asks the
// driver to grab, and 0x0405 is not a grab message. It deliberately avoids
// 0x0405 - that message wedges this driver, which TestLiveWebcamProbe proves
// separately. Run with:
//
//	$env:LIVE_WEBCAM='1'; go test -tags live -run TestLiveWebcamFramePath -v ./internal/remote
func TestLiveWebcamFramePath(t *testing.T) {
	if os.Getenv("LIVE_WEBCAM") == "" {
		t.Skip("set LIVE_WEBCAM=1 to run against the real camera")
	}

	start := time.Now()
	logf := func(format string, args ...any) {
		t.Logf("[%6.2fs] "+format, append([]any{time.Since(start).Seconds()}, args...)...)
	}

	c, err := NewWebcamCapturer(WebcamOptions{
		Quality: 70,
		Logger:  log.New(os.Stderr, "frametest ", log.Ltime|log.Lmicroseconds),
	})
	if err != nil {
		t.Fatalf("NewWebcamCapturer: %v", err)
	}
	// Never block the test on teardown; a wedged thread just leaks with the
	// process.
	defer func() {
		done := make(chan struct{})
		go func() { _ = c.Close(); close(done) }()
		select {
		case <-done:
			logf("Close() returned")
		case <-time.After(5 * time.Second):
			logf("Close() blocked; abandoning the thread (process exit frees it)")
		}
	}()
	logf("capturer opened")

	// The driver's real format: compression tells us whether lpData is raw
	// BGRA (BI_RGB) or something that would need converting.
	var bi bitmapInfo
	sendTimeoutMsg(t, c.capture, msgGetVideoFormat,
		unsafe.Sizeof(bi), uintptr(unsafe.Pointer(&bi)), 3000)
	logf("format: %dx%d bpp=%d compression=0x%08X sizeImage=%d",
		bi.Width, bi.Height, bi.BitCount, bi.Compression, bi.SizeImage)

	// Stage 1: WM_CAP_GRAB_FRAME, then read the capture window's surface.
	logf("stage 1: GRAB_FRAME (0x043C)")
	sendTimeoutMsg(t, c.capture, msgGrabFrame, 0, 0, 5000)
	bitBltWindowStats(t, c, "after GRAB_FRAME")
	logStats(t, "DIB after GRAB_FRAME", c.pixels)

	// Stage 2: WM_CAP_GRAB_FRAME_NOSTOP, then read the surface again.
	logf("stage 2: GRAB_FRAME_NOSTOP (0x043D)")
	sendTimeoutMsg(t, c.capture, msgGrabFrameNoStop, 0, 0, 5000)
	bitBltWindowStats(t, c, "after GRAB_FRAME_NOSTOP")
	logStats(t, "DIB after GRAB_FRAME_NOSTOP", c.pixels)

	// Stage 3: preview mode, which drives continuous painting into the window.
	logf("stage 3: SET_PREVIEW on (0x0432 w=1) + SET_PREVIEW_SCALE (0x0433 w=1)")
	sendTimeoutMsg(t, c.capture, msgSetPreviewScale, 1, 0, 3000)
	sendTimeoutMsg(t, c.capture, msgSetPreview, 1, 0, 3000)
	time.Sleep(700 * time.Millisecond)
	bitBltWindowStats(t, c, "after SET_PREVIEW")
	logStats(t, "DIB after SET_PREVIEW", c.pixels)

	// Stage 4: WM_CAP_SET_CALLBACK_FRAME. The driver hands each frame to a
	// callback on the capture thread - the one message that would remove the
	// per-frame SendMessage from serve() entirely.
	logf("stage 4: SET_CALLBACK_FRAME (0x0406) with a Go callback")
	frameCh := make(chan []byte, 16)
	cb := syscall.NewCallback(func(hwnd, lpvhdr uintptr) uintptr {
		hdr := (*videoHdr)(unsafe.Pointer(lpvhdr))
		if hdr.lpData != 0 && hdr.dwBufferLength > 0 {
			size := int(hdr.dwBufferLength)
			if size > 4*1024*1024 {
				size = 4 * 1024 * 1024
			}
			raw := unsafe.Slice((*byte)(unsafe.Pointer(hdr.lpData)), size)
			sample := make([]byte, size)
			copy(sample, raw)
			select {
			case frameCh <- sample:
			default:
			}
		}
		return 1
	})
	sendTimeoutMsg(t, c.capture, msgSetCallbackFrame, 0, cb, 3000)
	time.Sleep(1200 * time.Millisecond)
	got := 0
	var last []byte
	for {
		select {
		case f := <-frameCh:
			got++
			last = f
		default:
			goto done
		}
	}
done:
	logf("callback frames received: %d", got)
	if last != nil {
		rawStats(t, "callback frame", last, int(bi.Width)*int(bi.Height)*4)
	} else {
		t.Log("callback delivered no frames")
	}
}

// videoHdr mirrors the Win32 VIDEOHDR the frame callback receives (x64 layout).
type videoHdr struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwTimeCaptured  uint32
	dwIndex         uintptr
	dwReserved      uintptr
}

// logStats reports nonzero bytes of a raw pixel buffer.
func logStats(t *testing.T, tag string, pix []byte) {
	t.Helper()
	nonzero := 0
	for _, b := range pix {
		if b != 0 {
			nonzero++
		}
	}
	pct := 0.0
	if len(pix) > 0 {
		pct = 100 * float64(nonzero) / float64(len(pix))
	}
	t.Logf("%s: %d/%d nonzero (%.2f%%)", tag, nonzero, len(pix), pct)
}

// rawStats reports nonzero bytes of one raw driver frame.
func rawStats(t *testing.T, tag string, raw []byte, expect int) {
	t.Helper()
	nonzero := 0
	for _, b := range raw {
		if b != 0 {
			nonzero++
		}
	}
	pct := 0.0
	if len(raw) > 0 {
		pct = 100 * float64(nonzero) / float64(len(raw))
	}
	t.Logf("%s: %d bytes (expected ~%d), %d nonzero (%.2f%%)",
		tag, len(raw), expect, nonzero, pct)
}

// TestLiveWebcamCallbackPath validates the replacement frame path: register
// WM_CAP_SET_CALLBACK_FRAME (0x0405) ONCE with a real callback, turn preview
// on, and let the driver deliver frames to us - instead of the production
// code's per-frame SendMessage(0x0405, 0, 0), which is actually callback
// *unregistration* and wedges this driver on repeat calls. The format is
// MJPG, so the callback's VIDEOHDR is expected to carry a complete JPEG.
func TestLiveWebcamCallbackPath(t *testing.T) {
	if os.Getenv("LIVE_WEBCAM") == "" {
		t.Skip("set LIVE_WEBCAM=1 to run against the real camera")
	}

	start := time.Now()
	logf := func(format string, args ...any) {
		t.Logf("[%6.2fs] "+format, append([]any{time.Since(start).Seconds()}, args...)...)
	}

	c, err := NewWebcamCapturer(WebcamOptions{
		Quality: 70,
		Logger:  log.New(os.Stderr, "cbtest ", log.Ltime|log.Lmicroseconds),
	})
	if err != nil {
		t.Fatalf("NewWebcamCapturer: %v", err)
	}
	defer func() {
		done := make(chan struct{})
		go func() { _ = c.Close(); close(done) }()
		select {
		case <-done:
			logf("Close() returned")
		case <-time.After(6 * time.Second):
			logf("Close() blocked; abandoning the thread")
		}
	}()
	logf("capturer opened")

	frameCh := make(chan frameSample, 8)
	var keepAlive func(uintptr, uintptr) uintptr
	keepAlive = func(hwnd, lpvhdr uintptr) uintptr {
		if hwnd != 0 && lpvhdr != 0 {
			hdr := (*videoHdr)(unsafe.Pointer(lpvhdr))
			if hdr.lpData != 0 && hdr.dwBufferLength > 0 && hdr.dwBufferLength < 8*1024*1024 {
				size := int(hdr.dwBufferLength)
				raw := unsafe.Slice((*byte)(unsafe.Pointer(hdr.lpData)), size)
				sample := make([]byte, size)
				copy(sample, raw)
				select {
				case frameCh <- frameSample{raw: sample, recorded: hdr.dwBytesRecorded}:
				default:
				}
			}
		}
		return 1
	}
	cb := syscall.NewCallback(keepAlive)

	// THE critical message: register the frame callback once, correctly.
	logf("registering frame callback via WM_CAP_SET_CALLBACK_FRAME (0x0405)")
	sendTimeoutMsg(t, c.capture, 0x0405, 0, cb, 3000)

	logf("turning preview on")
	sendTimeoutMsg(t, c.capture, msgSetPreview, 1, 0, 3000)

	// Collect what the driver delivers within 2.5 seconds.
	deadline := time.After(2500 * time.Millisecond)
	got := 0
	var first frameSample
	for done := false; !done; {
		select {
		case f := <-frameCh:
			got++
			if first.raw == nil {
				first = f
			}
		case <-deadline:
			done = true
		}
	}
	logf("callback frames in 2.5s with preview: %d", got)

	if got == 0 {
		logf("no preview frames; trying one GRAB_FRAME")
		sendTimeoutMsg(t, c.capture, msgGrabFrame, 0, 0, 5000)
		wait := time.After(1500 * time.Millisecond)
	waitloop:
		for {
			select {
			case f := <-frameCh:
				got++
				if first.raw == nil {
					first = f
				}
			case <-wait:
				break waitloop
			}
		}
		logf("callback frames after GRAB_FRAME: %d", got)
	}

	if first.raw == nil {
		t.Fatal("no frame data was delivered by the callback")
	}
	t.Logf("first frame: buffer=%d bytes dwBytesRecorded=%d first32=% X",
		len(first.raw), first.recorded, first.raw[:minInt(32, len(first.raw))])
	payload := first.raw
	if first.recorded >= 4 && int(first.recorded) <= len(first.raw) {
		payload = first.raw[:int(first.recorded)]
	}
	jpegish := isCompleteJPEG(payload)
	t.Logf("payload: %d bytes, complete-JPEG=%v", len(payload), jpegish)
	if jpegish {
		img, derr := jpeg.Decode(bytes.NewReader(payload))
		if derr != nil {
			t.Logf("JPEG decode failed: %v", derr)
		} else {
			b := img.Bounds()
			total, lit := 0, 0
			var sum uint64
			for y := b.Min.Y; y < b.Max.Y; y += 4 {
				for x := b.Min.X; x < b.Max.X; x += 4 {
					r, g, bl, _ := img.At(x, y).RGBA()
					v := int((r>>8)+(g>>8)+(bl>>8)) / 3
					sum += uint64(v)
					total++
					if v > 30 {
						lit++
					}
				}
			}
			pct := 0.0
			if total > 0 {
				pct = 100 * float64(lit) / float64(total)
				t.Logf("frame: %dx%d avg-brightness=%.1f lit-pixels=%.1f%%",
					b.Dx(), b.Dy(), float64(sum)/float64(total), pct)
			}
		}
	} else {
		logStats(t, "raw frame", payload)
	}
	if got == 0 {
		t.Fatal("callback delivered nothing")
	}

	// Consecutive grabs: the first frame after the driver wakes is often a
	// black warm-up frame, so brightness is measured across several.
	for i := 0; i < 5; i++ {
		sendTimeoutMsg(t, c.capture, msgGrabFrame, 0, 0, 5000)
		select {
		case f := <-frameCh:
			p := f.raw
			if f.recorded >= 4 && int(f.recorded) <= len(f.raw) {
				p = p[:int(f.recorded)]
			}
			img, derr := jpeg.Decode(bytes.NewReader(p))
			if derr != nil {
				logf("grab %d: %d bytes, decode failed: %v", i+2, len(p), derr)
				continue
			}
			b := img.Bounds()
			total, lit := 0, 0
			var sum uint64
			for y := b.Min.Y; y < b.Max.Y; y += 6 {
				for x := b.Min.X; x < b.Max.X; x += 6 {
					r, g, bl, _ := img.At(x, y).RGBA()
					v := int((r>>8)+(g>>8)+(bl>>8)) / 3
					sum += uint64(v)
					total++
					if v > 30 {
						lit++
					}
				}
			}
			pct := 0.0
			if total > 0 {
				pct = 100 * float64(lit) / float64(total)
			}
			logf("grab %d: %d bytes avg-brightness=%.1f lit=%.1f%%",
				i+2, len(p), float64(sum)/float64(maxInt(total, 1)), pct)
		case <-time.After(3 * time.Second):
			logf("grab %d: no callback frame within 3s", i+2)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// frameSample is one callback delivery: the raw driver buffer plus the
// driver's own record of how many bytes were filled.
type frameSample struct {
	raw      []byte
	recorded uint32
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
