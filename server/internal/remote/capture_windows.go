//go:build windows

package remote

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	gdi32                  = syscall.NewLazyDLL("gdi32.dll")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procGetDeviceCaps      = gdi32.NewProc("GetDeviceCaps")
)

// GDI constants.
const (
	srccopy    = 0x00CC0020
	dibRGB     = 0
	bytesPerPx = 4

	// GetDeviceCaps indices used by DeviceInfo.
	capBitsPixel = 12
	// VREFRESH is 116. It was 11, which is not a refresh-rate capability at
	// all (8 is HORZRES, 10 is VERTRES, 12 is BITSPIXEL, 11 is unused), so
	// GetDeviceCaps answered 0 on every machine and the dashboard printed a
	// permanent "0Hz" beside a perfectly good resolution.
	capVRefresh = 116
)

// BITMAPINFOHEADER from wingdi.h.
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// Capturer grabs frames of the virtual desktop.
//
// All GDI handles are created once and reused. Recreating a device context
// and DIB section per frame is the classic cause of steady GDI handle leaks
// that slowly degrade the whole desktop session.
type Capturer struct {
	mu sync.Mutex

	dc     syscall.Handle
	memDC  syscall.Handle
	bmp    syscall.Handle
	oldBmp syscall.Handle
	pixels []byte
	width  int32
	height int32
	stride int32

	quality int
	maxWidth int
	onResize func(oldW, oldH, newW, newH int32)
	jpegBuf *bytes.Buffer

	closed bool
}

// CapturerOptions tunes capture cost versus quality.
type CapturerOptions struct {
	// Quality is the JPEG quality 1..100. Below ~60 the text on screen
	// becomes hard to read, which defeats the purpose of a remote view.
	Quality int
	// MaxWidth downscales wider desktops to keep frame size and encode time
	// reasonable over WiFi. 0 disables downscaling.
	MaxWidth int
	// OnResize, when set, is called after the geometry changed, with the old
	// and new desktop sizes. It runs on the capture goroutine.
	OnResize func(oldW, oldH, newW, newH int32)
}

// NewCapturer allocates GDI resources for the whole virtual desktop.
func NewCapturer(opts CapturerOptions) (*Capturer, error) {
	if opts.Quality == 0 {
		opts.Quality = 70
	}
	if opts.Quality < 10 || opts.Quality > 100 {
		opts.Quality = 70
	}

	ox, oy, w, h := virtualDesktopFn()
	_ = ox
	_ = oy

	c := &Capturer{
		quality:  opts.Quality,
		maxWidth: opts.MaxWidth,
		onResize: opts.OnResize,
		jpegBuf:  new(bytes.Buffer),
	}
	if err := c.allocate(w, h); err != nil {
		return nil, err
	}
	return c, nil
}

// allocate (re)creates the GDI surfaces for a desktop of w x h pixels.
//
// It is separated from NewCapturer so that a mid-stream resolution change can
// rebuild the surfaces through exactly the same path a fresh capturer uses,
// instead of growing a second, subtly different copy of that code.
func (c *Capturer) allocate(w, h int32) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("remote: refusing to capture %dx%d", w, h)
	}
	c.width = w
	c.height = h
	c.stride = w * bytesPerPx

	screenDC, _, err := user32.NewProc("GetDC").Call(0)
	if screenDC == 0 {
		return fmt.Errorf("remote: GetDC failed")
	}
	c.dc = syscall.Handle(screenDC)

	memDC, _, err := procCreateCompatibleDC.Call(uintptr(c.dc))
	if memDC == 0 {

		return fmt.Errorf("remote: CreateCompatibleDC failed")
	}
	c.memDC = syscall.Handle(memDC)

	bi := bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    w,
		Height:   -h, // negative height requests a top-down DIB
		Planes:   1,
		BitCount: 32,
		// A DIB section with a NULL pointer lets the OS allocate the
		// backing store and gives us a direct pointer to the pixels,
		// avoiding a GetDIBits copy.
	}
	bits := uintptr(0)
	bmp, _, err := procCreateDIBSection.Call(
		uintptr(c.memDC),
		uintptr(unsafe.Pointer(&bi)),
		dibRGB,
		uintptr(unsafe.Pointer(&bits)),
		0, 0,
	)
	if bmp == 0 || bits == 0 {
		c.freeSurfaces()
		return fmt.Errorf("remote: CreateDIBSection failed")
	}
	c.bmp = syscall.Handle(bmp)
	c.pixels = unsafe.Slice((*byte)(unsafe.Pointer(bits)), int(c.stride)*int(c.height))

	old, _, err := procSelectObject.Call(uintptr(c.memDC), uintptr(c.bmp))
	if err != syscall.Errno(0) {
		c.freeSurfaces()
		return fmt.Errorf("remote: SelectObject failed: %w", err)
	}
	c.oldBmp = syscall.Handle(old)

	return nil
}

// Capture grabs one frame and returns it as an encoded JPEG.
//
// The returned slice is freshly allocated and owned by the caller. Reusing a
// shared buffer across concurrent MJPEG viewers would corrupt frames, so the
// copy is deliberate.
func (c *Capturer) Capture() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("remote: capturer is closed")
	}

	if err := c.resizeIfChangedLocked(); err != nil {
		return nil, err
	}

	// SRCCOPY of the whole virtual desktop into the top-down DIB.
	//
	// The argument order is BitBlt's, and it is not the order these fields
	// appear in on the Capturer:
	//
	//	BitBlt(hdcDest, xDest, yDest, wDest, hDest, hdcSrc, xSrc, ySrc, rop)
	//
	// An earlier version passed them grouped as "destination, source, size",
	// which shifted every argument from wDest onwards: the *destination width*
	// received the source DC handle, hDest received 0, and hdcSrc received NULL.
	// That is a copy from a null source of zero height, which cannot succeed on
	// any Windows configuration - and because the call fails before setting a
	// last-error, GetLastError returned 0 and the real cause stayed invisible.
	//
	// This looked like a GDI/driver limitation, and would have prompted a
	// pointless rewrite onto DXGI that could not have fixed it.
	ret, _, _ := procBitBlt.Call(
		uintptr(c.memDC), 0, 0,
		uintptr(c.width), uintptr(c.height),
		uintptr(c.dc), 0, 0,
		srccopy,
	)
	if ret == 0 {
		// Read the error immediately: anything between the failing call and
		// here can overwrite it, and a bare 0 is what hid this bug.
		code := lastError()
		return nil, fmt.Errorf("remote: BitBlt failed (%dx%d from dc %d, "+
			"win32 error %d)", c.width, c.height, c.dc, code)
	}

	img := c.convertLocked()
	var buf bytes.Buffer
	buf.Grow(img.Rect.Dx() * img.Rect.Dy() / 8)
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: c.quality}); err != nil {
		return nil, fmt.Errorf("remote: jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}

// resizeIfChangedLocked rebuilds the capture surfaces when the virtual desktop
// changed size, so a resolution change mid-stream shows up in the next frame
// instead of cropping to the old geometry or blacking out.
//
// The metrics are re-read rather than cached because a user who plugs in a
// second monitor or changes display scaling expects the picture to follow
// without reconnecting.
func (c *Capturer) resizeIfChangedLocked() error {
	_, _, w, h := virtualDesktopFn()
	if w == c.width && h == c.height {
		return nil
	}
	if w <= 0 || h <= 0 {
		// A transient zero (a monitor mid-sleep) must not destroy a working
		// capture; the next frame will see the real size again.
		return nil
	}
	prevW, prevH := c.width, c.height
	c.freeSurfaces()
	if err := c.allocate(w, h); err != nil {
		return err
	}
	if c.onResize != nil {
		c.onResize(prevW, prevH, w, h)
	}
	return nil
}

// convertLocked converts the GDI 32-bit BGRA DIB into an image.RGBA.
//
// The conversion is written as an explicit loop over the pixel buffer rather
// than a per-pixel PutRGBA call, because the latter is roughly an order of
// magnitude slower and capture runs continuously while a stream is open.
func (c *Capturer) convertLocked() *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, int(c.width), int(c.height)))
	src := c.pixels
	out := dst.Pix
	rowBytes := int(c.stride)
	outRow := dst.Stride

	for y := 0; y < int(c.height); y++ {
		s := src[y*rowBytes : y*rowBytes+rowBytes]
		o := out[y*outRow : y*outRow+rowBytes]
		for x := 0; x < rowBytes; x += 4 {
			// GDI DIBs are 0x00RRGGBB in little-endian byte order,
			// i.e. B, G, R, A in memory.
			o[x] = s[x+2]
			o[x+1] = s[x+1]
			o[x+2] = s[x]
			o[x+3] = 0xFF
		}
	}
	return scaleToMaxWidth(dst, c.maxWidth)
}

// Size reports the capture dimensions in pixels.
func (c *Capturer) Size() (w, h int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.width, c.height
}

// SetQuality changes the JPEG quality for subsequent frames.
func (c *Capturer) SetQuality(q int) {
	c.mu.Lock()
	c.quality = q
	c.mu.Unlock()
}

// Close releases the GDI resources. It is safe to call more than once.
func (c *Capturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true

	c.freeSurfaces()
	return nil
}

// freeSurfaces releases the GDI handles and the pixel backing without closing
// the capturer, so allocate can immediately build them again at a new size.
func (c *Capturer) freeSurfaces() {
	if c.oldBmp != 0 && c.memDC != 0 {
		procSelectObject.Call(uintptr(uintptr(c.memDC)), uintptr(c.oldBmp))
		c.oldBmp = 0
	}
	if c.bmp != 0 {
		procDeleteObject.Call(uintptr(c.bmp))
		c.bmp = 0
	}
	if c.memDC != 0 {
		procDeleteDC.Call(uintptr(uintptr(c.memDC)))
		c.memDC = 0
	}
	if c.dc != 0 {
		user32.NewProc("ReleaseDC").Call(0, uintptr(c.dc))
		c.dc = 0
	}
	c.pixels = nil
}

func (c *Capturer) release() {
	_ = c.Close()
}

// DeviceInfo describes the display, used by the diagnostics endpoint.
type DeviceInfo struct {
	Width     int32 `json:"width"`
	Height    int32 `json:"height"`
	BPP       int   `json:"bpp"`
	RefreshHz int   `json:"refreshHz"`
}

// QueryDeviceInfo reads the primary display capabilities via GDI.
func QueryDeviceInfo() DeviceInfo {
	dc, _, _ := user32.NewProc("GetDC").Call(0)
	if dc == 0 {
		return DeviceInfo{}
	}
	defer user32.NewProc("ReleaseDC").Call(0, dc)

	get := func(index int) int32 {
		v, _, _ := procGetDeviceCaps.Call(uintptr(dc), uintptr(index))
		return int32(v)
	}
	_, _, w, h := virtualDesktop()
	return DeviceInfo{
		Width:     w,
		Height:    h,
		BPP:       int(get(capBitsPixel)),
		RefreshHz: int(get(capVRefresh)),
	}
}

// Frame is one captured image with its timestamp.
type Frame struct {
	JPEG       []byte
	Width      int32
	Height     int32
	Sequence   uint64
	CapturedAt time.Time
}

// Stream owns a FrameSource and hands frames to a single viewer at a target
// frame rate, doing the rate limiting here rather than in the HTTP handler so
// that a slow client cannot force extra captures.
type Stream struct {
	cap     FrameSource
	fps     int
	lastSeq uint64

	mu   sync.Mutex
	seq  uint64
	done chan struct{}
	once sync.Once
}

// NewStream wraps a frame source for streaming at fps frames per second.
func NewStream(c FrameSource, fps int) *Stream {
	if fps <= 0 || fps > 60 {
		fps = 15
	}
	return &Stream{cap: c, fps: fps, done: make(chan struct{})}
}

// Next captures and encodes the next frame, blocking for at most one frame
// interval to honour the target frame rate.
func (s *Stream) Next() (Frame, error) {
	interval := time.Second / time.Duration(s.fps)

	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	// Sleep outside the capture path is fine here; Next is called
	// sequentially by one MJPEG writer goroutine.
	time.Sleep(interval)

	jpegBytes, err := s.cap.Capture()
	if err != nil {
		return Frame{}, err
	}
	w, h := s.cap.Size()
	return Frame{
		JPEG:       jpegBytes,
		Width:      w,
		Height:     h,
		Sequence:   seq,
		CapturedAt: time.Now(),
	}, nil
}

// Sequence returns the number of frames produced so far.
func (s *Stream) Sequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// Stop releases the stream and its capturer.
func (s *Stream) Stop() {
	s.once.Do(func() {
		close(s.done)
		_ = s.cap.Close()
	})
}

// Stopped reports whether Stop has been called.
func (s *Stream) Stopped() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}
