package remote

// FrameSource produces JPEG frames on demand.
//
// It exists so the streaming pipeline does not care *what* is being
// photographed. The desktop GDI capturer and the webcam capturer both satisfy
// it, which is what lets one stream handler, one MJPEG writer and one framing
// contract serve both feeds. Adding the webcam therefore required no change to
// the framing code at all - only a new FrameSource implementation.
//
// Implementations need only be safe for the single goroutine that the stream
// loop drives them from.
type FrameSource interface {
	// Capture grabs the next frame and returns it encoded as JPEG.
	Capture() ([]byte, error)

	// Size reports the source's current frame size in pixels.
	Size() (w, h int32)

	// Close releases any OS resources. It must be safe to call more than once.
	Close() error
}
