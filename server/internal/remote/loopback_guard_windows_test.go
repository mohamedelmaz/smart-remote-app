package remote

import (
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ownerOnlyRoutes are the endpoints that let a caller change the machine's
// security posture: the six lock actions and PIN regeneration.
//
// Until now the lock handlers were registered raw (server.go:187-192) and
// decided reachability from the Origin header. sameOrigin returns true when
// Origin is absent (lock.go:377-379), and no browser is required to omit it,
// so any host on the LAN could turn the owner's lock off with a plain HTTP
// client that sends no Origin at all. PIN regeneration had the same shape.
//
// Each of these must be answerable only from the PC itself.
var ownerOnlyRoutes = []struct {
	method string
	path   string
	body   string
}{
	{"GET", "/api/lock/state", ""},
	{"POST", "/api/lock/unlock", `{"code":"hunter2code"}`},
	{"POST", "/api/lock/logout", ""},
	{"POST", "/api/lock/set", `{"code":"hunter2code","enabled":true}`},
	{"POST", "/api/lock/disable", `{"code":"hunter2code"}`},
	{"POST", "/api/lock/action", `{"do":"quit"}`},
	{"POST", "/api/pin", ""},
}

// TestOwnerOnlyRoutesRefuseRemoteCallers drives the real Handler() the way a
// hostile device would: an address on a network nobody here owns, and no
// Origin header.
//
// The refusal is asserted as JSON as well as 403, because the dashboard parses
// these bodies - a text/plain refusal would throw inside the browser's
// res.json() and show the owner a parser message instead of the reason.
//
// Each route gets a fresh fixture. Without that, an earlier route that turns
// the lock on would make the later ones answer "locked" and mask the very
// exposure being asserted.
func TestOwnerOnlyRoutesRefuseRemoteCallers(t *testing.T) {
	for _, peer := range nonOursAddrs {
		for _, rt := range ownerOnlyRoutes {
			t.Run(peer+" "+rt.method+" "+rt.path, func(t *testing.T) {
				f := newLockFixture(t, false)
				rec := f.do(rt.method, rt.path, peer, rt.body, nil)
				assertJSONRefusal(t, rec, rt.method, rt.path, peer)
			})
		}
	}
}

// assertJSONRefusal is the single statement of what a refusal looks like: 403,
// a JSON content type, and the owner-readable reason in "error".
func assertJSONRefusal(t *testing.T, rec *httptest.ResponseRecorder, method, path, peer string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("%s %s from %s returned %d, want 403; body=%q",
			method, path, peer, rec.Code, rec.Body.String())
	}
	mediaType, _, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		t.Fatalf("%s %s from %s: Content-Type = %q, want application/json",
			method, path, peer, rec.Header().Get("Content-Type"))
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s from %s: the refusal is not JSON: %v (%s)",
			method, path, peer, err, rec.Body.String())
	}
	if body.Error != "only available on this PC" {
		t.Fatalf("%s %s from %s: error = %q, want %q",
			method, path, peer, body.Error, "only available on this PC")
	}
}

// nonOursAddrs are addresses on networks nobody here owns. 192.0.2.0/24 is
// TEST-NET-1 and 203.0.113.0/24 is TEST-NET-3, both reserved for
// documentation, so neither can be a real interface on this machine.
var nonOursAddrs = []string{"192.0.2.55:1234", "203.0.113.9:1234"}

// TestOwnerOnlyRoutesAcceptOurOwnAddress pins the behaviour the lock has always
// had: a dashboard opened as http://192.168.x.x:9520 is still the owner at this
// PC. Refusing it would break the lock UI and the New PIN button for no gain,
// because a different device cannot arrive from an address this PC owns.
func TestOwnerOnlyRoutesAcceptOurOwnAddress(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs: %v", err)
	}
	var ours []string
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			ours = append(ours, net.JoinHostPort(ipnet.IP.String(), "9520"))
		}
	}
	if len(ours) == 0 {
		t.Skip("no non-loopback IPv4 interface on this machine")
	}
	t.Logf("this machine's own addresses: %v", ours)

	for _, peer := range ours {
		for _, rt := range ownerOnlyRoutes {
			t.Run(peer+" "+rt.path, func(t *testing.T) {
				f := newLockFixture(t, false)
				rec := f.do(rt.method, rt.path, peer, rt.body, nil)
				if rec.Code == 403 {
					t.Fatalf("%s %s from our own address %s returned 403: %q",
						rt.method, rt.path, peer, rec.Body.String())
				}
			})
		}
	}
}

// TestOwnerOnlyRoutesRefuseAddressesWeDoNotOwn is the other side, with and
// without an Origin header, because a header a caller can choose to omit must
// not change the outcome.
func TestOwnerOnlyRoutesRefuseAddressesWeDoNotOwn(t *testing.T) {
	origins := map[string]map[string]string{
		"no origin":   nil,
		"with origin": {"Origin": "http://127.0.0.1:9520"},
	}
	for name, hdr := range origins {
		for _, peer := range nonOursAddrs {
			for _, rt := range ownerOnlyRoutes {
				t.Run(name+" "+peer+" "+rt.path, func(t *testing.T) {
					f := newLockFixture(t, false)
					rec := f.do(rt.method, rt.path, peer, rt.body, hdr)
					if rec.Code != 403 {
						t.Fatalf("%s %s from %s (%s) returned %d, want 403; body=%q",
							rt.method, rt.path, peer, name, rec.Code, rec.Body.String())
					}
				})
			}
		}
	}
}

// TestOwnerOnlyRoutesAcceptLoopbackForms covers both loopback spellings.
func TestOwnerOnlyRoutesAcceptLoopbackForms(t *testing.T) {
	for _, peer := range []string{"127.0.0.1:5555", "[::1]:5555"} {
		for _, rt := range ownerOnlyRoutes {
			t.Run(peer+" "+rt.path, func(t *testing.T) {
				f := newLockFixture(t, false)
				rec := f.do(rt.method, rt.path, peer, rt.body, nil)
				if rec.Code == 403 {
					t.Fatalf("%s %s from loopback %s returned 403: %q",
						rt.method, rt.path, peer, rec.Body.String())
				}
			})
		}
	}
}

// TestOwnerOnlyRoutesAcceptLoopback is the other half: the owner opening the
// dashboard on this PC must still be able to do all of this. Fresh fixture per
// route for the same reason as above.
//
// The assertion is "not 403" rather than "success": several of these handlers
// refuse for their own reasons that have nothing to do with the guard. /api/
// lock/disable needs a code that a disabled fixture never had, and /api/lock/
// action needs a session cookie. Both predate this change and are not what
// this test is about; what matters is that the loopback guard is not what
// stopped them.
func TestOwnerOnlyRoutesAcceptLoopback(t *testing.T) {
	for _, rt := range ownerOnlyRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			f := newLockFixture(t, false)
			rec := f.do(rt.method, rt.path, localAddr, rt.body, nil)
			if rec.Code == 403 {
				t.Fatalf("%s %s from loopback returned 403; the owner must keep access: %q",
					rt.method, rt.path, rec.Body.String())
			}
			t.Logf("%s %s from loopback -> %d", rt.method, rt.path, rec.Code)
		})
	}
}

// TestLockLifecycleFromLoopback walks the whole feature from the PC, because
// the guard must not be satisfied at the cost of the feature working.
//
// Note what the lock is for: it hides the dashboard from a browser on this PC,
// not from the LAN. Lock.blocks returns false for a non-loopback peer
// (lock.go:138-140), so the 401 below is asserted from loopback, which is the
// caller the lock actually governs.
func TestLockLifecycleFromLoopback(t *testing.T) {
	f := newLockFixture(t, false)

	if rec := f.do("GET", "/api/lock/state", localAddr, "", nil); rec.Code != 200 {
		t.Fatalf("state from loopback = %d, want 200", rec.Code)
	}

	// Turning the lock on is the owner's first step.
	if rec := f.do("POST", "/api/lock/set", localAddr, `{"code":"hunter2code","enabled":true}`, nil); rec.Code >= 400 {
		t.Fatalf("set from loopback = %d: %q", rec.Code, rec.Body.String())
	}
	if !f.lock.Enabled() {
		t.Fatal("lock did not turn on after set from loopback")
	}

	// Locked: this PC's own browser is now shut out until the code is typed.
	if rec := f.do("GET", "/api/panel/status", localAddr, "", nil); rec.Code != 401 {
		t.Fatalf("status from loopback while locked = %d, want 401", rec.Code)
	}

	// The owner unlocks it, which must still be possible from loopback. Unlock
	// hands back a session cookie that later calls must carry, exactly as the
	// real dashboard does.
	cookie := f.unlock("hunter2code")
	if cookie == nil {
		t.Fatal("unlock from loopback did not produce a session")
	}
	withCookie := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	if rec := f.do("GET", "/api/panel/status", localAddr, "", withCookie); rec.Code != 200 {
		t.Fatalf("status from loopback while unlocked = %d, want 200: %q",
			rec.Code, rec.Body.String())
	}

	// And the owner turns it back off again.
	if rec := f.do("POST", "/api/lock/disable", localAddr, `{"code":"hunter2code"}`, withCookie); rec.Code >= 400 {
		t.Fatalf("disable from loopback = %d: %q", rec.Code, rec.Body.String())
	}
	if f.lock.Enabled() {
		t.Fatal("lock still enabled after disable from loopback")
	}
}

// TestHelloFrameCarriesNoPin reads the real first frame the server pushes to a
// connecting socket.
//
// The frame used to carry the PIN (server.go:878). Nothing consumed it: the
// phone has no reader for hello at all (its only "hello" is a comment about
// typing that word), and the dashboard does not open a socket. It was risk
// with no benefit, so it is asserted absent here over a live connection.
func TestHelloFrameCarriesNoPin(t *testing.T) {
	_, ts, _ := testServer(t)
	w := dialTestWS(t, ts)
	defer w.conn.Close()

	frame := w.readText(t, 5*time.Second)
	var event struct {
		Kind string                 `json:"kind"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(frame), &event); err != nil {
		t.Fatalf("hello frame is not JSON: %v (%s)", err, frame)
	}
	if event.Kind != "hello" {
		t.Fatalf("first frame kind = %q, want hello (%s)", event.Kind, frame)
	}
	for _, key := range []string{"pin", "PIN", "Pin"} {
		if _, ok := event.Data[key]; ok {
			t.Fatalf("hello frame still carries %q: %s", key, frame)
		}
	}
	if strings.Contains(strings.ToLower(frame), "pin") {
		t.Fatalf("hello frame still mentions the PIN: %s", frame)
	}
	// The fields the app does use must survive.
	if event.Data["version"] == nil || event.Data["client"] == nil {
		t.Fatalf("hello frame lost version or client: %s", frame)
	}
}
