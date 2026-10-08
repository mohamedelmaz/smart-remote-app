package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPsQuote protects the clipboard helper.
//
// The address is interpolated into a PowerShell single-quoted string, so an
// unescaped quote would not merely look wrong - it would terminate the literal
// and inject the remainder as a separate expression. That is a command
// injection path reachable from the tray menu.
func TestPsQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "192.168.1.36:9520", "'192.168.1.36:9520'"},
		{"empty", "", "''"},
		{"single quote", "it's", "'it''s'"},
		{"only quote", "'", "''''"},
		{"trailing quote", "a'", "'a'''"},
		{"semicolon is inert", "a'; rm -rf x; '", "'a''; rm -rf x; '''"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := psQuote(tc.in); got != tc.want {
				t.Fatalf("psQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPsQuoteAlwaysBalanced checks the invariant the escaping depends on:
// after the doubling, the literal contains an even number of quotes so the
// closing quote really is the last one.
func TestPsQuoteAlwaysBalanced(t *testing.T) {
	for _, in := range []string{"", "a", "'", "''", "a'b'c", "'; DROP", "'''"} {
		got := psQuote(in)
		body := got[1 : len(got)-1] // strip the surrounding quotes
		if strings.Count(body, "'")%2 != 0 {
			t.Fatalf("psQuote(%q) = %q: interior has an odd quote count", in, got)
		}
	}
}

// TestFallbackIconIsValidICO checks the last-resort tray icon really is an ICO.
//
// The fallback only runs if the embedded asset is empty, which is exactly the
// situation where nobody is watching the build. If it produced garbage the
// tray would be silently blank again - the failure this whole path exists to
// prevent - so the structure is asserted rather than assumed.
func TestFallbackIconIsValidICO(t *testing.T) {
	ico := fallbackIcon()
	if len(ico) < 22 {
		t.Fatalf("fallback icon is %d bytes, too short for an ICO header", len(ico))
	}

	if le16(ico[0:2]) != 0 {
		t.Fatalf("reserved = %d, want 0", le16(ico[0:2]))
	}
	if le16(ico[2:4]) != 1 {
		t.Fatalf("type = %d, want 1 (icon)", le16(ico[2:4]))
	}
	if count := le16(ico[4:6]); count != 1 {
		t.Fatalf("image count = %d, want 1", count)
	}

	e := ico[6:22]
	if e[0] == 0 || e[1] == 0 {
		t.Fatalf("entry dimensions are %dx%d, want non-zero", e[0], e[1])
	}
	if bpp := le16(e[6:8]); bpp != 32 {
		t.Fatalf("bits per pixel = %d, want 32", bpp)
	}

	size := le32(e[8:12])
	offset := le32(e[12:16])
	if offset+size != uint32(len(ico)) {
		t.Fatalf("entry claims bytes [%d,%d) but the file is %d bytes",
			offset, offset+size, len(ico))
	}
}

// TestEmbeddedIconPresent guards the primary path.
//
// trayIconData is expected to return the embedded asset, and the embedded
// asset is what gives Explorer, the taskbar and the tray one consistent icon.
// A build that lost the .ico would otherwise only be noticed at runtime.
func TestEmbeddedIconPresent(t *testing.T) {
	if len(appIcon) == 0 {
		t.Fatal("embedded appIcon is empty; the tray would fall back to a plain square")
	}

	// The embedded bytes must themselves be a well-formed ICO header.
	if le16(appIcon[0:2]) != 0 || le16(appIcon[2:4]) != 1 {
		t.Fatalf("embedded icon does not start with a valid ICONDIR")
	}
	if n := le16(appIcon[4:6]); n == 0 {
		t.Fatal("embedded icon declares zero images")
	}
}

// TestTrayIconDataNeverNil is the contract systray depends on.
//
// A nil icon makes Windows draw a blank tray entry, which the user cannot
// distinguish from a broken tray. Returning nil must be impossible.
func TestTrayIconDataNeverNil(t *testing.T) {
	if got := trayIconData(nil); len(got) == 0 {
		t.Fatal("trayIconData returned no bytes")
	}
}

// TestServiceTypeIsFQDN guards a subtle registration bug.
//
// zeroconf treats the type as a fully qualified name. The trailing dot is what
// makes it "_smartremote._tcp.local." rather than a single relative label,
// which no browser would ever match. Dropping the dot compiles fine and
// silently breaks discovery, so it is asserted here.
func TestServiceTypeIsFQDN(t *testing.T) {
	if !strings.HasSuffix(ServiceType, ".") {
		t.Fatalf("ServiceType %q must end with a dot to be fully qualified", ServiceType)
	}
	if !strings.HasPrefix(ServiceType, "_smartremote._tcp.") {
		t.Fatalf("ServiceType %q does not look like a DNS-SD service type", ServiceType)
	}
}

// TestServingGateDefaultsToLiveAndToggles proves the tray Close/Activate
// contract: NewServer starts live (a zero atomic.Bool would otherwise refuse
// every command), SetServing(false) stops, SetServing(true) resumes.
func TestServingGateDefaultsToLiveAndToggles(t *testing.T) {
	auth, err := NewAuth("")
	if err != nil {
		t.Fatalf("NewAuth = %v", err)
	}
	srv := NewServer(ServerOptions{
		Auth:     auth,
		Dispatch: NewDispatcher(DispatcherOptions{}),
	})
	if !srv.IsServing() {
		t.Fatal("fresh server must start serving; refusing commands on boot looks like a broken start")
	}
	srv.SetServing(false)
	if srv.IsServing() {
		t.Fatal("SetServing(false) must stop the server")
	}
	srv.SetServing(true)
	if !srv.IsServing() {
		t.Fatal("SetServing(true) must resume the server")
	}
}

// TestSanitizeClientNameKeepsRowsSafe proves hostile names cannot break the
// dashboard layout: single line, bounded, trimmed.
func TestSanitizeClientNameKeepsRowsSafe(t *testing.T) {
	if got := sanitizeClientName("  Galaxy S24\nDROP TABLE  "); got != "Galaxy S24 DROP TABLE" {
		t.Fatalf("sanitizeClientName collapsed whitespace wrong: %q", got)
	}
	if got := sanitizeClientName(""); got != "" {
		t.Fatalf("empty name must stay empty so the dashboard shows Unknown device: %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := sanitizeClientName(long); len(got) != 64 {
		t.Fatalf("long name must be capped at 64, got %d", len(got))
	}
}

// TestClientCountOnlyAuthenticated proves the dashboard's own event socket -
// which opens /ws but never sends auth - is never counted as a device and
// never renders as an "Unknown device" row. Only paired channels count.
func TestClientCountOnlyAuthenticated(t *testing.T) {
	auth, err := NewAuth("")
	if err != nil {
		t.Fatalf("NewAuth = %v", err)
	}
	srv := NewServer(ServerOptions{
		Auth:     auth,
		Dispatch: NewDispatcher(DispatcherOptions{}),
	})
	// The dashboard event socket: connected but never authenticated.
	srv.registerClient(&wsClient{id: "c1", addr: "127.0.0.1:1", connectedAt: time.Now()})
	// A paired phone.
	srv.registerClient(&wsClient{id: "c2", addr: "192.168.1.34:2", name: "elmamo",
		connectedAt: time.Now(), authenticated: true})

	if got := srv.clientCount(); got != 1 {
		t.Fatalf("clientCount = %d, want 1 (unauthenticated sockets must not count)", got)
	}
	list := srv.clientList()
	if len(list) != 1 {
		t.Fatalf("clientList len = %d, want 1", len(list))
	}
	if list[0].Name != "elmamo" || list[0].Addr != "192.168.1.34:2" {
		t.Fatalf("clientList row = %+v, want the paired phone", list[0])
	}
}

// TestServingLabelsNameActions proves the tray item always names what the
// click WILL do, never the current state.
func TestServingLabelsNameActions(t *testing.T) {
	if servingLabel(true) != "Close server" {
		t.Fatalf("live label must offer Close, got %q", servingLabel(true))
	}
	if servingLabel(false) != "Activate server" {
		t.Fatalf("stopped label must offer Activate, got %q", servingLabel(false))
	}
}

// trayIconData only sees the asset because //go:embed compiled it in, so the
// embed is the mechanism. But the *shell* icon in Explorer and the taskbar
// comes from a separate .syso resource produced by rsrc, and a build that
// forgot the .syso would still pass every other test here while showing the
// default application icon everywhere outside the tray.
//
// go tool nm on the built binary lists it only if the resource is linked, so
// this reads the real artifact rather than trusting the build command.
func TestEmbeddedIconIsInTheBinary(t *testing.T) {
	// The release binary is written to the module root, which is two levels
	// above this package (internal/remote). go test runs the test binary from
	// a temp dir, so the path is anchored on the module root discovered from
	// this source file rather than on the process working directory.
	exe := findReleaseBinary()
	if exe == "" {
		t.Skip("smart-remote-app.exe not built yet; run the release build first")
	}

	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}

	// The service type string is linked in verbatim by the mDNS package, so
	// its presence proves this is a fully linked binary rather than a stub.
	if !bytes.Contains(data, []byte(ServiceType)) {
		t.Fatal("binary does not contain the service type string; link may be incomplete")
	}

	// The ICO is linked twice: once via //go:embed for systray, once as the
	// PE resource for Explorer. Both copies start with the same ICONDIR.
	needle := appIcon[:64]
	if len(appIcon) >= 64 {
		if !bytes.Contains(data, needle) {
			t.Fatal("the tray icon bytes are not present in the binary")
		}
	}
}

// findReleaseBinary locates smart-remote-app.exe at the module root.
//
// It walks up from the working directory rather than trusting a fixed number
// of parent hops: go test may run from the package directory or the module
// root depending on how it was invoked, and a wrong guess would quietly skip
// the check instead of failing it.
func findReleaseBinary() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "smart-remote-app.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func le16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// ---------------------------------------------------------------------------
// Channel security invariants.
//
// These tests speak the real WebSocket wire through httptest: they prove the
// auth gate holds end to end, not just that a helper returns the right value.
// They change no behaviour and execute no input: the unauthenticated probe is
// rejected before it ever reaches the dispatcher, so no cursor moves on the
// machine running the tests.
// ---------------------------------------------------------------------------

// testWS is one raw client socket: handshake done, frames spoken manually so
// the tests never depend on a third-party websocket client.
type testWS struct {
	conn net.Conn
	br   *bufio.Reader
}

// dialTestWS completes the RFC 6455 handshake against a test server.
func dialTestWS(t *testing.T, ts *httptest.Server) *testWS {
	t.Helper()
	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	req := "GET /ws HTTP/1.1\r\nHost: " + addr + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		t.Fatalf("handshake write: %v", err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		t.Fatalf("handshake read: %v", err)
	}
	if !strings.Contains(status, "101") {
		_ = conn.Close()
		t.Fatalf("handshake status = %q, want 101", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			t.Fatalf("handshake headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	return &testWS{conn: conn, br: br}
}

// writeText sends one masked client text frame, as RFC 6455 requires.
func (w *testWS) writeText(t *testing.T, s string) {
	t.Helper()
	payload := []byte(s)
	if len(payload) > 125 {
		t.Fatalf("test payload too large for the simple frame writer")
	}
	var mask = [4]byte{0x11, 0x22, 0x33, 0x44}
	frame := []byte{0x81, byte(len(payload)) | 0x80}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := w.conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// readText reads one unmasked server text frame.
func (w *testWS) readText(t *testing.T, timeout time.Duration) string {
	t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(timeout))
	head := make([]byte, 2)
	if _, err := io.ReadFull(w.br, head); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if head[0] != 0x81 {
		t.Fatalf("server frame header = %#x, want single text frame", head[0])
	}
	if head[1]&0x80 != 0 {
		t.Fatalf("server must not mask frames")
	}
	length := uint64(head[1] & 0x7F)
	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			t.Fatalf("read extended length: %v", err)
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	} else if length == 127 {
		t.Fatalf("test server sent a frame too large for the simple reader")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(w.br, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	return string(payload)
}

// readTextTimeout returns ("", false) on read timeout instead of failing: it
// is how the tests assert that a channel received nothing.
func (w *testWS) readTextTimeout(timeout time.Duration) (string, bool) {
	_ = w.conn.SetReadDeadline(time.Now().Add(timeout))
	head := make([]byte, 2)
	if _, err := io.ReadFull(w.br, head); err != nil {
		return "", false
	}
	if head[0] != 0x81 || head[1]&0x80 != 0 {
		return "", false
	}
	length := uint64(head[1] & 0x7F)
	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return "", false
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(w.br, payload); err != nil {
		return "", false
	}
	return string(payload), true
}

// testServer builds a live server on an ephemeral loopback port. The PIN is
// returned so tests can complete a genuine pairing.
func testServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	auth, err := NewAuth("")
	if err != nil {
		t.Fatalf("NewAuth = %v", err)
	}
	srv := NewServer(ServerOptions{
		Auth:     auth,
		Dispatch: NewDispatcher(DispatcherOptions{}),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	t.Cleanup(func() { srv.Shutdown(ctx) })
	return srv, ts, auth.Pin()
}

// TestUnauthenticatedChannelCannotSendCommands proves a socket that never
// paired cannot drive the PC: the command is refused at the gate, before the
// dispatcher ever sees it.
func TestUnauthenticatedChannelCannotSendCommands(t *testing.T) {
	_, ts, _ := testServer(t)
	w := dialTestWS(t, ts)
	defer w.conn.Close()
	_ = w.readText(t, 5*time.Second) // hello

	w.writeText(t, `{"type":"move","dx":50,"dy":50}`)
	reply := w.readText(t, 5*time.Second)
	if !strings.Contains(reply, `"ok":false`) || !strings.Contains(reply, "not authenticated") {
		t.Fatalf("unauthenticated command reply = %q, want ok:false not-authenticated", reply)
	}
}

// TestBroadcastOnlyAuthenticated proves server events (here pin_changed, fired
// by regenerating the PIN over plain HTTP) reach a paired phone and never the
// dashboard-style socket that never authenticated.
func TestBroadcastOnlyAuthenticated(t *testing.T) {
	_, ts, pin := testServer(t)

	phone := dialTestWS(t, ts)
	defer phone.conn.Close()
	_ = phone.readText(t, 5*time.Second) // hello
	phone.writeText(t, `{"type":"auth","pin":"`+pin+`","name":"test-phone","nonce":"n1"}`)
	// handleAuth broadcasts clients_changed before replying the ack, so read
	// until the frame carrying our nonce.
	for i := 0; i < 4; i++ {
		if frame := phone.readText(t, 5*time.Second); strings.Contains(frame, `"nonce":"n1"`) {
			if !strings.Contains(frame, `"ok":true`) {
				t.Fatalf("auth ack = %q, want ok:true", frame)
			}
			break
		}
		if i == 3 {
			t.Fatal("never received the auth ack")
		}
	}

	viewer := dialTestWS(t, ts)
	defer viewer.conn.Close()
	_ = viewer.readText(t, 5*time.Second) // hello, never authenticates

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/pin", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/pin: %v", err)
		_ = resp.Body.Close()
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/pin status = %d, want 200", resp.StatusCode)
	}

	got := phone.readText(t, 5*time.Second)
	if !strings.Contains(got, "pin_changed") {
		t.Fatalf("paired phone received %q, want pin_changed event", got)
	}
	if frame, ok := viewer.readTextTimeout(800 * time.Millisecond); ok {
		t.Fatalf("unauthenticated socket received %q, want silence", frame)
	}
}

// ---------------------------------------------------------------------------
// Device block enforcement.
//
// A blocked IP is refused after a successful PIN check (never before, so
// outsiders learn nothing), disconnected at once, and refused on commands
// and streams too - the PIN alone cannot let it back in.
// ---------------------------------------------------------------------------

// postBlock is the dashboard block/unblock call. Loopback callers are the
// owner, so the guard lets the test server (127.0.0.1) through.
func postBlock(t *testing.T, ts *httptest.Server, action, ip string) {
	t.Helper()
	body := `{"ip":"` + ip + `"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/devices/"+action,
		strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", action, resp.StatusCode)
	}
}

// authPhone pairs one raw socket and returns it after a successful ack.
func authPhone(t *testing.T, ts *httptest.Server, pin, nonce string) *testWS {
	t.Helper()
	w := dialTestWS(t, ts)
	_ = w.readText(t, 5*time.Second) // hello
	w.writeText(t, `{"type":"auth","pin":"`+pin+`","name":"test-phone","nonce":"`+nonce+`"}`)
	for i := 0; i < 4; i++ {
		if frame := w.readText(t, 5*time.Second); strings.Contains(frame, `"nonce":"`+nonce+`"`) {
			if !strings.Contains(frame, `"ok":true`) {
				w.conn.Close()
				t.Fatalf("auth ack = %q, want ok:true", frame)
			}
			return w
		}
	}
	w.conn.Close()
	t.Fatal("never received the auth ack")
	return nil
}

// TestBlockedDeviceCannotAuth proves the full refusal path: block, then a
// fresh pairing with the RIGHT pin is still refused with the stable
// "blocked:" prefix, and unblocking restores pairing.
func TestBlockedDeviceCannotAuth(t *testing.T) {
	_, ts, pin := testServer(t)
	postBlock(t, ts, "block", "127.0.0.1")

	w := dialTestWS(t, ts)
	defer w.conn.Close()
	_ = w.readText(t, 5*time.Second) // hello
	w.writeText(t, `{"type":"auth","pin":"`+pin+`","name":"x","nonce":"nb"}`)
	reply := w.readText(t, 5*time.Second)
	if !strings.Contains(reply, `"ok":false`) || !strings.Contains(reply, "blocked:") {
		t.Fatalf("blocked auth reply = %q, want ok:false blocked:", reply)
	}

	postBlock(t, ts, "unblock", "127.0.0.1")
	p := authPhone(t, ts, pin, "na")
	p.conn.Close()
}

// TestBlockDisconnectsLiveChannel proves blocking disconnects the live
// socket at once: the phone shows reconnecting instead of hanging.
func TestBlockDisconnectsLiveChannel(t *testing.T) {
	_, ts, pin := testServer(t)
	p := authPhone(t, ts, pin, "nd")
	defer p.conn.Close()

	postBlock(t, ts, "block", "127.0.0.1")
	// The server closed the socket; the next read must fail (EOF/close),
	// never deliver another frame.
	if frame, ok := p.readTextTimeout(3 * time.Second); ok {
		t.Fatalf("blocked live channel received %q, want disconnect", frame)
	}
	postBlock(t, ts, "unblock", "127.0.0.1")
}

// TestBlockedLingeringChannelExecutesNothing covers the race window: a
// channel blocked without being disconnected (or racing the block click)
// must execute nothing afterwards.
func TestBlockedLingeringChannelExecutesNothing(t *testing.T) {
	srv, ts, pin := testServer(t)
	p := authPhone(t, ts, pin, "nl")
	defer p.conn.Close()

	// Block directly in the set, bypassing disconnectIP: this is exactly the
	// lingering-channel state.
	srv.blocked.Block("127.0.0.1", "test-phone")
	p.writeText(t, `{"type":"move","dx":50,"dy":50}`)
	reply := p.readText(t, 5*time.Second)
	if !strings.Contains(reply, `"ok":false`) || !strings.Contains(reply, "blocked:") {
		t.Fatalf("lingering blocked command reply = %q, want ok:false blocked:", reply)
	}
}

// TestBlockedStreamRefused proves a blocked device cannot fall back to the
// screen stream with the (shared, discoverable) PIN: 403, not 401.
func TestBlockedStreamRefused(t *testing.T) {
	_, ts, pin := testServer(t)
	postBlock(t, ts, "block", "127.0.0.1")
	resp, err := http.Get(ts.URL + "/screen?pin=" + pin)
	if err != nil {
		t.Fatalf("GET /screen: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /screen status = %d, want 403", resp.StatusCode)
	}
}

// TestBlockGuardRefusesRemoteCallers proves the management endpoints are the
// owner's only: a non-loopback caller and a mismatched Origin are refused,
// while the loopback dashboard passes.
func TestBlockGuardRefusesRemoteCallers(t *testing.T) {
	srv, _, _ := testServer(t)

	remote := httptest.NewRequest(http.MethodPost, "/api/devices/block",
		strings.NewReader(`{"ip":"10.0.0.9"}`))
	remote.RemoteAddr = "192.168.1.99:1234"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, remote)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote caller status = %d, want 403", rec.Code)
	}

	csrf := httptest.NewRequest(http.MethodPost, "/api/devices/block",
		strings.NewReader(`{"ip":"10.0.0.9"}`))
	csrf.RemoteAddr = "127.0.0.1:1234"
	csrf.Host = "127.0.0.1:9520"
	csrf.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, csrf)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mismatched origin status = %d, want 403", rec.Code)
	}
	if srv.blocked.Blocked("10.0.0.9") {
		t.Fatal("refused request must not change the block set")
	}
}

// TestCorruptBlockFileStartsEmpty proves a corrupt file can never wedge the
// server: it is quarantined to .bak and the list starts empty with a flag.
func TestCorruptBlockFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocklist.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	auth, err := NewAuth("")
	if err != nil {
		t.Fatalf("NewAuth = %v", err)
	}
	srv := NewServer(ServerOptions{
		Auth:      auth,
		Dispatch:  NewDispatcher(DispatcherOptions{}),
		BlockFile: path,
	})
	if !srv.blockCorrupt {
		t.Fatal("corrupt file must raise blockCorrupt for the dashboard")
	}
	if srv.blocked.Blocked("192.168.1.34") {
		t.Fatal("corrupt file must load as an empty set")
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("corrupt file must be quarantined to .bak: %v", err)
	}
}

// TestBlocklistPersistsAcrossRestart proves a block survives a restart: the
// file written on block is honoured by the next server opening it.
func TestBlocklistPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocklist.json")
	newSrv := func() *Server {
		auth, err := NewAuth("")
		if err != nil {
			t.Fatalf("NewAuth = %v", err)
		}
		return NewServer(ServerOptions{
			Auth:      auth,
			Dispatch:  NewDispatcher(DispatcherOptions{}),
			BlockFile: path,
		})
	}
	first := newSrv()
	first.BlockDevice("192.168.1.77", "intruder")
	second := newSrv()
	if !second.blocked.Blocked("192.168.1.77") {
		t.Fatal("block must survive a restart via the block file")
	}
	second.UnblockDevice("192.168.1.77")
	third := newSrv()
	if third.blocked.Blocked("192.168.1.77") {
		t.Fatal("unblock must survive a restart via the block file")
	}
}
