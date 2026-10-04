package remote

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ErrViewerBusy is returned when a second viewer tries to attach while one
// is already streaming.
var ErrViewerBusy = errors.New("remote: a screen viewer is already connected")

// ViewerLock grants exclusive screen-streaming rights to one client.
//
// Two concurrent viewers of the same GDI capture would halve the frame rate
// for both, so access is serialised. The lock is held by the handler for the
// lifetime of the stream and released on disconnect, including when the
// client vanishes without closing the connection.
type ViewerLock struct {
	mu     sync.Mutex
	holder string
	since  time.Time
}

// Acquire takes the lock for the named viewer.
func (l *ViewerLock) Acquire(who string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder != "" {
		return fmt.Errorf("%w (%s since %s)", ErrViewerBusy, l.holder, l.since.Format(time.Kitchen))
	}
	l.holder = who
	l.since = time.Now()
	return nil
}

// Release frees the lock if held by the named viewer. Passing an empty name
// releases unconditionally, which the server uses on shutdown.
func (l *ViewerLock) Release(who string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == "" {
		return
	}
	if who != "" && l.holder != who {
		return // another viewer owns it; do not steal the lock
	}
	l.holder = ""
	l.since = time.Time{}
}

// Holder returns the current viewer's name, or "" when free.
func (l *ViewerLock) Holder() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holder
}

// StreamBoundary is the multipart boundary used for all MJPEG streams.
const StreamBoundary = "frameboundary"

// MJPEGWriter serialises frames as multipart/x-mixed-replace.
//
// Each part carries an explicit Content-Length. That is the primary framing
// mechanism and the only reliable one: searching for the SOI/EOI JPEG
// markers fails whenever those two bytes occur *inside* entropy-coded
// compressed data, which is common in a busy screenshot. Markers are used
// only as a fallback sanity check on a frame this process just encoded.
type MJPEGWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
	seq     uint64
}

// NewMJPEGWriter prepares the response for streaming.
//
// The multipart boundary is sent by the first frame, so a client that
// connects and immediately disconnects still receives valid headers.
func NewMJPEGWriter(w http.ResponseWriter) (*MJPEGWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("remote: response writer does not support flushing")
	}

	h := w.Header()
	// Cache-Control must be no-store or an intermediary may buffer the
	// stream and the viewer will appear frozen.
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	h.Set("Pragma", "no-cache")
	h.Set("Connection", "close")
	h.Set("Content-Type", "multipart/x-mixed-replace; boundary="+StreamBoundary)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	// The headers must be flushed here, not left to ride along with the first
	// frame. Go's net/http buffers them, so without this the client's request
	// does not even complete until enough body bytes have accumulated to fill
	// the buffer. On a slow first capture that is a visible stall, and to a
	// client that is watching for headers it is indistinguishable from the PC
	// refusing to serve the stream at all.
	flusher.Flush()

	return &MJPEGWriter{w: w, flusher: flusher}, nil
}

// WriteFrame emits one JPEG part.
//
// It verifies the JPEG structure before writing so a truncated or empty
// frame never reaches the client, where it would desynchronise the stream
// permanently.
func (m *MJPEGWriter) WriteFrame(jpegBytes []byte) error {
	if !isCompleteJPEG(jpegBytes) {
		return fmt.Errorf("remote: refusing to send malformed JPEG frame (%d bytes)", len(jpegBytes))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++

	if _, err := fmt.Fprintf(m.w, "--%s\r\n", StreamBoundary); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(m.w,
		"Content-Type: image/jpeg\r\nContent-Length: %d\r\nX-Sequence: %d\r\n\r\n",
		len(jpegBytes), m.seq); err != nil {
		return err
	}
	if _, err := m.w.Write(jpegBytes); err != nil {
		return err
	}
	if _, err := m.w.Write([]byte("\r\n")); err != nil {
		return err
	}
	m.flusher.Flush()
	return nil
}

// Sequence returns the number of frames written.
func (m *MJPEGWriter) Sequence() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seq
}

// isCompleteJPEG reports whether the buffer starts with SOI and ends with EOI.
func isCompleteJPEG(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	return b[0] == 0xFF && b[1] == 0xD8 &&
		b[len(b)-2] == 0xFF && b[len(b)-1] == 0xD9
}

// ScreenStreamConfig tunes a stream session.
type ScreenStreamConfig struct {
	FPS     int
	Quality int
}

// ServeScreenStream streams the desktop until the client disconnects.
//
// It honours single-viewer locking and always releases the lock via defer,
// so a handler that aborts or panics cannot leave the lock held forever and
// block every future viewer.
func ServeScreenStream(w http.ResponseWriter, r *http.Request, settings ScreenStreamConfig,
	lock *ViewerLock, logger *log.Logger) {

	if logger == nil {
		logger = log.Default()
	}
	who := clientLabel(r)
	if err := lock.Acquire(who); err != nil {
		// 409 Conflict is the honest status: the request is valid but
		// conflicts with the current state of the resource.
		http.Error(w, err.Error(), http.StatusConflict)
		logger.Printf("screen stream rejected for %s: %v", who, err)
		return
	}
	defer lock.Release(who)

	if settings.FPS <= 0 {
		settings.FPS = 15
	}
	if settings.Quality <= 0 {
		settings.Quality = 70
	}
	// Query overrides let the in-app settings adjust quality without a
	// reconnect.
	if v := r.URL.Query().Get("fps"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 60 {
			settings.FPS = n
		}
	}
	if v := r.URL.Query().Get("quality"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 10 && n <= 100 {
			settings.Quality = n
		}
	}

	capturer, err := NewCapturer(CapturerOptions{Quality: settings.Quality})
	if err != nil {
		logger.Printf("screen stream: capture unavailable for %s: %v", who, err)
		http.Error(w, "screen capture is unavailable on this PC: "+err.Error(),
			http.StatusServiceUnavailable)
		return
	}
	defer capturer.Close()

	stream := NewStream(capturer, settings.FPS)
	defer stream.Stop()

	writer, err := NewMJPEGWriter(w)
	if err != nil {
		logger.Printf("screen stream: %v", err)
		return
	}

	logger.Printf("screen stream opened by %s at %d fps, quality %d",
		who, settings.FPS, settings.Quality)
	defer logger.Printf("screen stream closed for %s after %d frames", who, writer.Sequence())

	// maxCaptureFailures bounds how many consecutive capture errors are tolerated
	// before the stream is abandoned.
	//
	// Retrying forever was the original behaviour, and it is the worst possible
	// choice: a persistent failure (a locked desktop, session 0, a GPU driver
	// that refuses BitBlt) then produces HTTP 200, valid headers, and then
	// infinite silence. The client is left staring at "Connecting to the PC
	// screen stream..." with no error and no way to tell that the PC is failing
	// to capture at all - the exact symptom that made this look like a broken
	// viewer rather than a broken capture.
	//
	// A transient blip still recovers, because the counter resets the moment a
	// frame is written. Only a failure that never clears ends the session, and
	// ending it is what lets the client say something useful.
	const maxCaptureFailures = 5

	failures := 0
	for {
		select {
		case <-r.Context().Done():
			// The client went away; the deferred Release lets the next
			// viewer connect immediately.
			return
		default:
		}

		frame, err := stream.Next()
		if err != nil {
			failures++
			logger.Printf("screen stream: capture error %d/%d for %s: %v",
				failures, maxCaptureFailures, who, err)

			if failures >= maxCaptureFailures {
				// Give up rather than spin. The response has already been
				// committed with a 200, so the status cannot be changed -
				// closing the connection is the only remaining signal, and the
				// client's watchdog turns that into a visible error.
				logger.Printf("screen stream: giving up for %s after %d "+
					"consecutive capture failures; the PC cannot capture its "+
					"desktop (locked session, no interactive desktop, or a "+
					"display driver refusing a desktop capture)",
					who, failures)
				return
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if err := writer.WriteFrame(frame.JPEG); err != nil {
			// A write error means the client disconnected.
			return
		}

		// A frame got through, so whatever went wrong was transient. Reset so
		// the next hiccup gets the full budget of retries.
		failures = 0
	}
}

// clientLabel builds a short identifier for logs and lock ownership.
func clientLabel(r *http.Request) string {
	host, _, err := splitHostPortSafe(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

// splitHostPortSafe splits an address without assuming it is well-formed.
func splitHostPortSafe(addr string) (host, port string, err error) {
	i := len(addr) - 1
	for i >= 0 && addr[i] != ':' {
		i--
	}
	if i < 0 {
		return addr, "", errors.New("no port")
	}
	return addr[:i], addr[i+1:], nil
}
