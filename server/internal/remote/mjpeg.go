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
var ErrViewerBusy = errors.New("remote: another viewer is already connected")

// staleStreamLease is how long a stream lock may go without evidence of life
// before the next requester may force it open.
//
// This exists because of the 2026-10-08 incident: a camera driver that had
// stopped answering left its handler blocked inside capture forever. The
// handler never returned, its deferred Release never ran, and every later
// request was answered 409 "since 6:08PM" until the server was killed. A
// holder that has sent no frame for this long is definitionally broken, so
// the lock records progress (Touch on every frame written) and Acquire
// reclaims a lock this stale, reporting the old holder through the returned
// evicted name so the takeover is visible in the log.
//
// The takeover is made safe by leases: when a zombie handler eventually
// returns, its Release presents the lease number it acquired with, which no
// longer matches, so it cannot free the lock its successor now holds.
const staleStreamLease = 30 * time.Second

// ViewerLock grants exclusive stream-viewing rights to one client.
//
// Two concurrent viewers of the same GDI capture would halve the frame rate
// for both, so access is serialised. The lock is held by the handler for the
// lifetime of the stream and released on disconnect, including when the
// client vanishes without closing the connection - and, via staleStreamLease,
// even when the handler itself is wedged.
type ViewerLock struct {
	mu       sync.Mutex
	holder   string
	since    time.Time
	progress time.Time
	lease    uint64
}

// Acquire takes the lock for the named viewer and returns the lease that
// Release must present.
//
// evicted names the previous holder when this call force-reclaimed a lock
// that had gone stale (no frame for staleStreamLease); callers log it so a
// forced takeover is never silent. err wraps ErrViewerBusy and is non-nil
// only when the lock is held by a viewer that is still making progress.
func (l *ViewerLock) Acquire(who string) (lease uint64, evicted string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder != "" {
		if time.Since(l.progress) < staleStreamLease {
			return 0, "", fmt.Errorf("%w (%s since %s)",
				ErrViewerBusy, l.holder, l.since.Format(time.Kitchen))
		}
		evicted = l.holder
	}
	l.lease++
	l.holder = who
	l.since = time.Now()
	l.progress = l.since
	return l.lease, evicted, nil
}

// Touch records evidence of life - normally one call per frame written. It is
// a no-op when the lease no longer owns the lock, because a takeover has
// already superseded this handler and its late progress must not resurrect
// the old ownership.
func (l *ViewerLock) Touch(lease uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lease != 0 && l.lease == lease {
		l.progress = time.Now()
	}
}

// Release frees the lock when lease is still the current one. An out-of-date
// lease (a handler waking up long after its lock was force-reclaimed) is
// ignored rather than stealing the lock from the live owner.
func (l *ViewerLock) Release(lease uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lease == 0 || l.lease != lease {
		return
	}
	l.holder = ""
	l.since = time.Time{}
	l.progress = time.Time{}
}

// Reset clears the lock unconditionally. It is for shutdown, where the goal
// is that a later start is never refused by a leftover holder.
//
// The lease is bumped so any handler still holding the old lease can neither
// Release nor Touch the fresh state: without this a zombie that wakes up
// after Reset would resurrect its progress timestamp under the empty holder.
func (l *ViewerLock) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lease++
	l.holder = ""
	l.since = time.Time{}
	l.progress = time.Time{}
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

// openFrameSource acquires the capture device a stream session will photograph.
//
// Returning an error here is what lets one loop serve a desktop that cannot be
// captured and a PC with no camera, each answering with its own status: the
// caller inspects the error to choose one.
type openFrameSource func(quality int) (FrameSource, error)

// ServeScreenStream streams the desktop until the client disconnects.
//
// It honours single-viewer locking and always releases the lock via defer,
// so a handler that aborts or panics cannot leave the lock held forever and
// block every future viewer.
func ServeScreenStream(w http.ResponseWriter, r *http.Request, settings ScreenStreamConfig,
	lock *ViewerLock, logger *log.Logger) {

	serveMJPEG(w, r, settings, lock, logger, "screen",
		func(quality int) (FrameSource, error) {
			return NewCapturer(CapturerOptions{Quality: quality})
		})
}

// ServeWebcamStream streams a camera until the client disconnects.
//
// It is the same pipeline as the desktop feed - same locking, same failure
// budget, same framing - with only the capture device swapped. The lock it is
// given is a *separate* ViewerLock from the desktop one: the single-viewer
// rule exists because two viewers of one device halve its frame rate, which
// holds just as much for one camera as for one screen, but a camera viewer and
// a screen viewer contend for nothing and so must not lock each other out.
func ServeWebcamStream(w http.ResponseWriter, r *http.Request, settings ScreenStreamConfig,
	lock *ViewerLock, logger *log.Logger) {

	serveMJPEG(w, r, settings, lock, logger, "webcam",
		func(quality int) (FrameSource, error) {
			return NewWebcamCapturer(WebcamOptions{Quality: quality, Logger: logger})
		})
}

// serveMJPEG is the shared streaming loop behind both feeds.
//
// Everything that defines a stream lives here: the viewer lock, the query
// overrides, the failure budget and the write loop. The only thing a caller
// supplies is the capture device, via open. That is deliberate - the desktop
// and the webcam must not drift apart in how they authenticate, how they behave
// when capture fails, or how they terminate.
func serveMJPEG(w http.ResponseWriter, r *http.Request, settings ScreenStreamConfig,
	lock *ViewerLock, logger *log.Logger, label string, open openFrameSource) {

	if logger == nil {
		logger = log.Default()
	}
	who := clientLabel(r)
	lease, evicted, err := lock.Acquire(who)
	if err != nil {
		// 409 Conflict is the honest status: the request is valid but
		// conflicts with the current state of the resource.
		http.Error(w, err.Error(), http.StatusConflict)
		logger.Printf("%s stream rejected for %s: %v", label, who, err)
		return
	}
	if evicted != "" {
		// A forced takeover is a significant event - it means the previous
		// handler was alive enough to take the lock but never sent a frame
		// again - so it is logged with both names for correlation.
		logger.Printf("%s stream: forced release of stale stream lock from %s "+
			"(no frames sent for %s); %s is taking over",
			label, evicted, staleStreamLease, who)
	}
	// A single defer owns both jobs. The lease-based release runs on any exit
	// path - normal return, client disconnect, or a panic, which is logged
	// here with the stream's label instead of vanishing into net/http's
	// generic recovery - and a stale lease released late is a no-op by
	// construction, so even a zombie handler cannot free its successor's lock.
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Printf("%s stream: panic recovered for %s: %v", label, who, recovered)
		}
		lock.Release(lease)
	}()

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

	capturer, err := open(settings.Quality)
	if err != nil {
		// A PC with no camera is an expected, self-describing state rather
		// than a server fault, and the phone has to tell it apart from "your PC
		// is broken" - it says "no camera" and stops asking. Answering before
		// the MJPEG headers go out is what makes that possible: once the 200 is
		// committed the status can never change.
		if errors.Is(err, ErrNoWebcam) {
			logger.Printf("%s stream: no camera available for %s: %v",
				label, who, err)
			http.Error(w, "no camera detected on this PC: "+err.Error(),
				http.StatusNotFound)
			return
		}
		logger.Printf("%s stream: capture unavailable for %s: %v", label, who, err)
		http.Error(w, label+" capture is unavailable on this PC: "+err.Error(),
			http.StatusServiceUnavailable)
		return
	}
	defer capturer.Close()

	stream := NewStream(capturer, settings.FPS)
	defer stream.Stop()

	writer, err := NewMJPEGWriter(w)
	if err != nil {
		logger.Printf("%s stream: %v", label, err)
		return
	}
	logger.Printf("%s stream: headers sent to %s (boundary=%s, flushed)",
		label, who, StreamBoundary)

	openedAt := time.Now()
	logger.Printf("%s stream opened by %s at %d fps, quality %d",
		label, who, settings.FPS, settings.Quality)
	defer func() {
		logger.Printf("%s stream closed for %s after %d frames (open for %s)",
			label, who, writer.Sequence(), time.Since(openedAt).Round(time.Second))
	}()

	// mjpegDebug enables one log line per frame (write size, sequence, flush).
	// Off by default: at 15fps it would flood the log. Enable with
	// ?debug=1 on the /screen or /webcam URL for a single diagnosis session.
	mjpegDebug := r.URL.Query().Get("debug") == "1"
	if mjpegDebug {
		logger.Printf("%s stream: per-frame debug logging enabled for %s",
			label, who)
	}

	// maxCaptureFailures bounds how many consecutive capture errors are tolerated
	// before the stream is abandoned.
	//
	// Retrying forever was the original behaviour, and it is the worst possible
	// choice: a persistent failure (a locked desktop, session 0, a GPU driver
	// that refuses BitBlt, a camera unplugged mid-session) then produces HTTP
	// 200, valid headers, and then infinite silence. The client is left staring
	// at "Connecting..." with no error and no way to tell that the PC is failing
	// to capture at all - the exact symptom that made this look like a broken
	// viewer rather than a broken capture.
	//
	// A transient blip still recovers, because the counter resets the moment a
	// frame is written. Only a failure that never clears ends the session, and
	// ending it is what lets the client say something useful.
	const maxCaptureFailures = 5

	// heartbeat reports that the stream is alive even when nobody is watching
	// the log for frame-by-frame detail: one line per 30 seconds is enough to
	// prove frames are moving without flooding smart-remote-server.log.
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	firstFrameLogged := false
	lastFrameSize := 0

	failures := 0
	for {
		select {
		case <-r.Context().Done():
			// The client went away; the deferred Release lets the next
			// viewer connect immediately.
			return
		case <-heartbeat.C:
			logger.Printf("%s stream: %s still open after %s: %d frames, "+
				"last frame %d bytes",
				label, who, time.Since(openedAt).Round(time.Second),
				writer.Sequence(), lastFrameSize)
		default:
		}

		// Stream.Next is bounded: a capture device that stops answering must
		// count as a failure rather than block this handler forever, because
		// a blocked handler never reaches its deferred Release and the next
		// viewer would be answered 409 until the process is restarted.
		frame, err := nextFrameBounded(stream, frameBudget)
		if err != nil {
			failures++
			logger.Printf("%s stream: capture error %d/%d for %s: %v",
				label, failures, maxCaptureFailures, who, err)

			if failures >= maxCaptureFailures {
				// Give up rather than spin. The response has already been
				// committed with a 200, so the status cannot be changed -
				// closing the connection is the only remaining signal, and the
				// client's watchdog turns that into a visible error.
				logger.Printf("%s stream: giving up for %s after %d "+ 
					"consecutive capture failures; the PC cannot capture "+ 
					"this source (device unplugged, driver busy, or no "+ 
					"interactive session)",
					label, who, failures)
				return
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if err := writer.WriteFrame(frame.JPEG); err != nil {
			// A write error means the client disconnected (or stopped
			// reading long enough for the connection to break). Either way
			// the session is over, and saying so with the byte count is what
			// makes a truncated stream diagnosable from the log.
			logger.Printf("%s stream: write to %s failed after %d frames "+
				"(%d bytes of last frame): %v",
				label, who, writer.Sequence(), len(frame.JPEG), err)
			return
		}

		// A frame got through, so whatever went wrong was transient. Reset so
		// the next hiccup gets the full budget of retries, and tell the lock
		// the stream is alive - this Touch is what keeps a healthy stream
		// from ever being judged stale, and its absence is what lets a wedged
		// one be reclaimed after staleStreamLease.
		failures = 0
		lock.Touch(lease)
		lastFrameSize = len(frame.JPEG)

		if !firstFrameLogged {
			firstFrameLogged = true
			logger.Printf("%s stream: first frame sent to %s (%d bytes)",
				label, who, len(frame.JPEG))
		}

		// Opt-in trace: size, running sequence and explicit flush note per
		// frame. This is what shows whether WriteFrame or flusher.Flush is
		// the point a stall begins.
		if mjpegDebug {
			logger.Printf("%s stream: frame %d to %s (%d bytes, flushed)",
				label, writer.Sequence(), who, len(frame.JPEG))
		}
	}
}

// frameBudget bounds a single Stream.Next call.
//
// It is the outer safety net under every capture device's own timeouts: the
// MJPEG handler must never block indefinitely, because a handler that blocks
// never reaches its deferred lock Release and the stream lock then outlives
// every client - the failure mode that answered 409 to every request for the
// rest of the process's life. Six seconds comfortably covers a normal frame
// interval plus an in-flight capture timeout.
const frameBudget = 6 * time.Second

// nextFrameBounded runs Stream.Next on a helper goroutine and gives up after
// budget.
//
// On timeout the helper goroutine stays behind until its capture call returns
// - it may never - but the handler stops caring: the failure counts against
// the session's retry budget, and when the session ends the lease-based lock
// release runs regardless. The leak is bounded by the number of wedged
// sessions, which is precisely the thing the timeout converts from "all
// future viewers" to "at most this one".
func nextFrameBounded(s *Stream, budget time.Duration) (Frame, error) {
	type outcome struct {
		frame Frame
		err   error
	}
	ch := make(chan outcome, 1)
	go func() {
		f, err := s.Next()
		ch <- outcome{frame: f, err: err}
	}()
	select {
	case o := <-ch:
		return o.frame, o.err
	case <-time.After(budget):
		return Frame{}, fmt.Errorf(
			"remote: the capture device produced no frame within %v", budget)
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
