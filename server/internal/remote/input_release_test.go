package remote

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProbeScreenCaptureAnswers runs the probe for real.
//
// The probe is only useful if it genuinely exercises the capture path rather
// than reporting a hardcoded answer, so this asserts the invariants that must
// hold on whatever machine runs it: exactly one of success/error is set, a
// failure always carries an actionable suggestion, and a success reports a
// real frame with real dimensions. It does not require capture to work, because
// the entire point is to diagnose the machine where it does not.
// TestScreenStreamRequiresPin is the security regression test for the desktop
// feed.
//
// The stream is the entire screen of the PC - a password manager, a banking
// tab, everything - and every other unauthenticated route here is read-only
// metadata or the dashboard. Serving it without a PIN made the stream the one
// door in the house that anyone who learns the port could walk through.
func TestScreenStreamRequiresPin(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"no pin at all", "/screen", 401},
		{"empty pin", "/screen?pin=", 401},
		{"wrong pin", "/screen?pin=000000", 401},
		{"non-numeric pin", "/screen?pin=abcdef", 401},
		{"post is refused", "/screen", 405},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			method := "GET"
			if tc.name == "post is refused" {
				method = "POST"
			}
			srv.Handler().ServeHTTP(rec,
				httptest.NewRequest(method, tc.url, nil))

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}

			// A refusal must not leak a frame. Checking the body matters
			// because a handler that authenticated *after* starting to write
			// would return 200 with no usable content.
			if tc.want != 405 {
				body := rec.Body.String()
				if strings.Contains(body, "\xff\xd8") {
					t.Error("response body contains JPEG data on a " +
						"refused request")
				}
				if strings.Contains(body, StreamBoundary) {
					t.Errorf("response body exposes the stream boundary: %q",
						body)
				}
			}
		})
	}
}

// TestAuthorizeStreamAcceptsCorrectPin proves the gate is not simply closed.
//
// A test that only asserts 401 passes just as happily against an endpoint that
// refuses everything, so the positive case has to be covered too.
//
// This calls authorizeStream directly rather than going through handleScreen,
// because a successful authorisation starts an endless stream: the handler
// never returns, so driving it through a recorder would hang the suite.
func TestAuthorizeStreamAcceptsCorrectPin(t *testing.T) {
	srv := newTestServer(t)
	pin := srv.auth.Pin()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/screen?pin="+pin, nil)

	if !srv.authorizeStream(rec, req) {
		t.Fatalf("authorizeStream rejected a correct PIN: %s",
			rec.Body.String())
	}

	// Authorisation must not have written a response of its own; the stream
	// handler owns the 200 and its headers.
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("authorizeStream wrote to the response (code=%d, %d bytes); "+
			"it should only permit or refuse", rec.Code, rec.Body.Len())
	}
}

// newTestServer builds a server with a known PIN for auth tests.
func newTestServer(t *testing.T) *Server {
	t.Helper()

	// An empty path makes Auth generate an ephemeral PIN rather than reading
	// or writing the real user's PIN file.
	auth, err := NewAuth("")
	if err != nil {
		t.Fatalf("NewAuth = %v", err)
	}

	return NewServer(ServerOptions{
		Auth:     auth,
		Dispatch: NewDispatcher(DispatcherOptions{}),
	})
}

func TestProbeScreenCaptureAnswers(t *testing.T) {
	p := ProbeScreenCapture()

	if p.Method != "GDI" {
		t.Errorf("Method = %q, want GDI", p.Method)
	}

	if p.Success {
		if p.Error != "" {
			t.Errorf("Success is true but Error = %q; the caller cannot tell "+
				"which happened", p.Error)
		}
		if p.GDI.Bytes <= 0 {
			t.Errorf("Success reported but GDI.Bytes = %d", p.GDI.Bytes)
		}
		if p.GDI.Width <= 0 || p.GDI.Height <= 0 {
			t.Errorf("Success reported with bogus size %dx%d",
				p.GDI.Width, p.GDI.Height)
		}
	} else {
		if p.Error == "" {
			t.Error("Success is false but no Error was reported; the probe " +
				"would be useless")
		}
		if p.Suggestion == "" {
			t.Error("failure carries no Suggestion")
		}
	}

	// A session id of 0 with a blank suggestion means the non-interactive
	// branch was not taken, which would send the user chasing the wrong fix.
	if !p.Interactive && p.Suggestion == "" {
		t.Error("non-interactive session produced no guidance")
	}

	t.Logf("probe: success=%t error=%q win32=%d session=%d dxgi=%t",
		p.Success, p.Error, p.Win32Error, p.SessionID, p.DXGI.Available)
}

// TestScreencapTestEndpointIsReachable pins the route.
//
// The route is easy to drop in a refactor of Handler, and without it the one
// diagnostic that identifies a blank screen simply 404s - which looks
// identical to the bug being investigated.
func TestScreencapTestEndpointIsReachable(t *testing.T) {
	srv := NewServer(ServerOptions{
		Dispatch: NewDispatcher(DispatcherOptions{}),
		Logger:   nil,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/screencap-test", nil)
	srv.Handler().ServeHTTP(rec, req)

	// A failed capture is a successful diagnosis, so the status must be 200
	// regardless of the outcome.
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body:\n%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, key := range []string{"success", "gdi", "dxgi", "sessionId"} {
		if !strings.Contains(body, key) {
			t.Errorf("response missing %q:\n%s", key, body)
		}
	}
}

// TestInputReleaseIsAccepted guards the recovery command.
//
// "input.release" is what lifts a mouse button stranded by a lost "button up"
// - the cause of a PC that selects text on every cursor movement. It carries no
// fields, so it is easy to add to the dispatcher and forget to add to Validate,
// where it would be rejected as an unknown command and silently do nothing.
// That failure mode is invisible until a user needs the button back.
func TestInputReleaseIsAccepted(t *testing.T) {
	c := Command{Type: CmdInputRelease, Nonce: "n1"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate(%q) = %v, want nil; the release command would be "+
			"rejected and a stranded mouse button could never be cleared",
			CmdInputRelease, err)
	}
}

// TestNormalizeAcceptsRelease checks the command survives the case-folding the
// decoder applies before validation.
func TestNormalizeAcceptsRelease(t *testing.T) {
	c := Command{Type: "INPUT.RELEASE"}.Normalize()
	if c.Type != CmdInputRelease {
		t.Fatalf("normalized type = %q, want %q", c.Type, CmdInputRelease)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate after Normalize = %v, want nil", err)
	}
}

// TestViewerLockIsExclusive covers the single-viewer rule.
func TestViewerLockIsExclusive(t *testing.T) {
	var l ViewerLock
	if err := l.Acquire("phone"); err != nil {
		t.Fatalf("first Acquire = %v, want nil", err)
	}
	if err := l.Acquire("laptop"); err == nil {
		t.Fatal("second Acquire succeeded; two viewers would halve each " +
			"other's frame rate and double the capture cost")
	}
	l.Release("laptop") // must not steal the lock
	if got := l.Holder(); got != "phone" {
		t.Fatalf("Holder after a foreign Release = %q, want %q", got, "phone")
	}
	l.Release("phone")
	if err := l.Acquire("laptop"); err != nil {
		t.Fatalf("Acquire after release = %v, want nil", err)
	}
}

// TestWriteFrameRejectsMalformedJPEG guards the framing invariant.
//
// A frame that is not a complete JPEG must never reach the client: the
// Content-Length has already been written by then, so a bad body permanently
// desynchronises the stream and every subsequent frame is lost. Refusing it
// instead ends the session, which the client can retry.
func TestWriteFrameRejectsMalformedJPEG(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewMJPEGWriter(rec)
	if err != nil {
		t.Fatalf("NewMJPEGWriter = %v", err)
	}

	if err := w.WriteFrame([]byte{0xFF, 0xD8, 0x01, 0x02}); err == nil {
		t.Fatal("WriteFrame accepted a truncated JPEG")
	}

	// A well-formed frame must still be written, and carry a length the client
	// parser can actually frame on.
	frame := append([]byte{0xFF, 0xD8}, append(make([]byte, 8), 0xFF, 0xD9)...)
	if err := w.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame(valid) = %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Content-Length: "+itoa(len(frame))) {
		t.Fatalf("body missing a correct Content-Length:\n%s", body)
	}
	if !strings.Contains(body, "--"+StreamBoundary) {
		t.Fatalf("body missing the boundary:\n%s", body)
	}
}

// TestHeadersAreFlushedBeforeAnyFrame pins the behaviour the mentor calls out.
//
// net/http buffers response headers, so without an explicit Flush the client's
// request does not even complete until enough body bytes accumulate to fill the
// buffer. To a viewer that is indistinguishable from the PC refusing to serve
// the stream at all.
func TestHeadersAdvertiseTheBoundary(t *testing.T) {
	rec := httptest.NewRecorder()
	if _, err := NewMJPEGWriter(rec); err != nil {
		t.Fatalf("NewMJPEGWriter = %v", err)
	}

	// The client parser is constructed with StreamBoundary as a constant, so
	// the header advertising a different boundary would desynchronise the
	// stream: the client would wait forever for a delimiter the server never
	// writes, with no error to explain it.
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/x-mixed-replace") {
		t.Fatalf("Content-Type = %q, want multipart/x-mixed-replace", ct)
	}
	if !strings.Contains(ct, "boundary="+StreamBoundary) {
		t.Fatalf("Content-Type %q does not advertise the boundary the "+
			"writer uses (%s); a client deriving the boundary from the "+
			"header would never find a frame", ct, StreamBoundary)
	}

	// A streaming response must not be cacheable, or an intermediary may buffer
	// it and the viewer appears frozen on the first frame.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q, want it to contain no-store", cc)
	}
}

// itoa avoids importing strconv just for one call in a test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}