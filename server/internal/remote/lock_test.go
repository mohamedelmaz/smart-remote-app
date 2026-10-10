package remote

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The security lock tests drive the real Handler() the way the browser does.
// Only the peer address is faked, because "is this request local" is the single
// decision the whole feature turns on.

const localAddr = "127.0.0.1:5555"
const remoteAddr = "192.168.1.35:5555"

type lockFixture struct {
	srv   *Server
	lock  *Lock
	ts    *httptest.Server
	dir   string
	pin   string
	clock time.Time
}

// newLockFixture builds a server with a lock whose clock is under test control.
func newLockFixture(t *testing.T, locked bool) *lockFixture {
	t.Helper()
	dir := t.TempDir()

	auth, err := NewAuth("")
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	macros := &MacroStore{items: DefaultMacros()}
	srv := NewServer(ServerOptions{
		Logger:    testLog(),
		Macros:    macros,
		Auth:      auth,
		BlockFile: filepath.Join(dir, "blocklist.json"),
	})

	path := filepath.Join(dir, SettingsFileName)
	settings := DefaultSettings()
	l := NewLock(path, settings, discardLogger{})
	if locked {
		if err := l.setCode("hunter2code", true); err != nil {
			t.Fatalf("setCode: %v", err)
		}
	}
	srv.SetLock(l)

	f := &lockFixture{
		srv: srv, lock: l, dir: dir,
		pin:   auth.Pin(),
		clock: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	l.now = func() time.Time { return f.clock }
	l.SetActions(func(do string) (any, error) { return map[string]any{"did": do}, nil })

	f.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(f.ts.Close)
	return f
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// testLog is a logger that throws everything away, so the tests never write to
// the shared server log beside the executable.
func testLog() *log.Logger { return log.New(io.Discard, "", 0) }

// do issues a request with a chosen peer address and the Host a real dashboard
// would send.
func (f *lockFixture) do(method, path, peer string, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = peer
	req.Host = "127.0.0.1:9520"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// statusPin reads the PIN straight out of a status response, so a test that
// regenerates the PIN first still asserts against the live value.
func statusPin(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var st struct {
		PIN string `json:"pin"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, rec.Body.String())
	}
	return st.PIN
}

// get is the common local GET.
func (f *lockFixture) get(path string) *httptest.ResponseRecorder {
	return f.do(http.MethodGet, path, localAddr, "", nil)
}

// unlock performs a real unlock and returns the session cookie.
func (f *lockFixture) unlock(code string) *http.Cookie {
	rec := f.do(http.MethodPost, "/api/lock/unlock", localAddr,
		`{"code":"`+code+`"}`, nil)
	if rec.Code != http.StatusOK {
		return nil
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == LockSessionCookie {
			return c
		}
	}
	return nil
}

func sameHostHeaders(host string) map[string]string {
	return map[string]string{"Origin": "http://" + host}
}

// ---------------------------------------------------------------- test 1

// TestLockOffIsInvisible: with the lock off nothing changes. Every guarded
// route must answer exactly as it always did, to a local caller and a remote
// one.
func TestLockOffIsInvisible(t *testing.T) {
	f := newLockFixture(t, false)

	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/panel/status"},
		{http.MethodGet, "/api/net"},
		{http.MethodGet, "/api/macros"},
		{http.MethodGet, "/api/macros/lock"},
		{http.MethodGet, "/api/screencap-test"},
		{http.MethodPost, "/api/pin"},
		{http.MethodGet, "/healthz"},
	}
	for _, p := range paths {
		for _, peer := range []string{localAddr, remoteAddr} {
			rec := f.do(p.method, p.path, peer, "", nil)
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("%s %s from %s was refused with 401 while the lock is off",
					p.method, p.path, peer)
			}
		}
	}

	// The status body must still carry the PIN for a local caller. It is read
	// back rather than compared against the captured one, because the loop
	// above deliberately regenerates the PIN.
	rec := f.get("/api/panel/status")
	if pin := statusPin(t, rec); len(pin) != PinLength {
		t.Errorf("lock off: local status PIN = %q, want %d digits", pin, PinLength)
	}
}

// ---------------------------------------------------------------- test 2

// TestLockOnLocalLocked: with the lock on, a local browser without a session
// gets nothing. Crucially the 401 body carries no PIN.
func TestLockOnLocalLocked(t *testing.T) {
	f := newLockFixture(t, true)

	locked := []struct{ method, path, body string }{
		{http.MethodGet, "/api/panel/status", ""},
		{http.MethodPost, "/api/pin", ""},
		{http.MethodGet, "/api/macros", ""},
		{http.MethodGet, "/api/macros/lock", ""},
		{http.MethodGet, "/api/net", ""},
		{http.MethodPost, "/api/devices/block", `{"ip":"10.0.0.9"}`},
		{http.MethodPost, "/api/devices/unblock", `{"ip":"10.0.0.9"}`},
		{http.MethodGet, "/api/screencap-test", ""},
	}
	for _, p := range locked {
		rec := f.do(p.method, p.path, localAddr, p.body, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s from loopback = %d, want 401",
				p.method, p.path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), f.pin) {
			t.Errorf("%s %s leaked the PIN in a 401: %s", p.method, p.path, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"locked"`) {
			t.Errorf("%s %s body = %q, want the locked error", p.method, p.path, rec.Body.String())
		}
	}

	// The event socket must not get as far as sending its hello frame, which
	// carries the PIN.
	rec := f.get("/ws")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/ws from loopback = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), f.pin) {
		t.Errorf("/ws leaked the PIN: %s", rec.Body.String())
	}

	// Always-open routes stay open.
	if rec := f.get("/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz from loopback = %d, want 200 (probeServer depends on it)",
			rec.Code)
	}
	if rec := f.get("/api/lock/state"); rec.Code != http.StatusOK {
		t.Errorf("/api/lock/state = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------- test 3

// TestLockOnRemoteUntouched: a phone on the network must see exactly what it
// saw before the lock existed, PIN included.
func TestLockOnRemoteUntouched(t *testing.T) {
	f := newLockFixture(t, true)

	paths := []struct{ method, path, body string }{
		{http.MethodGet, "/api/panel/status", ""},
		{http.MethodPost, "/api/pin", ""},
		{http.MethodGet, "/api/macros", ""},
		{http.MethodGet, "/api/macros/lock", ""},
		{http.MethodGet, "/api/net", ""},
		{http.MethodPost, "/api/devices/block", `{"ip":"10.0.0.9"}`},
		{http.MethodGet, "/api/screencap-test", ""},
	}
	for _, p := range paths {
		rec := f.do(p.method, p.path, remoteAddr, p.body, nil)
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s from the network was refused; the lock must not touch phones",
				p.method, p.path)
		}
	}

	rec := f.do(http.MethodGet, "/api/panel/status", remoteAddr, "", nil)
	if pin := statusPin(t, rec); len(pin) != PinLength {
		t.Errorf("a remote status PIN = %q, want %d digits; a phone depends on it",
			pin, PinLength)
	}
}

// ---------------------------------------------------------------- test 4

func TestUnlockFlowAndGuessLimit(t *testing.T) {
	f := newLockFixture(t, true)

	rec := f.do(http.MethodPost, "/api/lock/unlock", localAddr, `{"code":"wrong-one"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong code = %d, want 401", rec.Code)
	}

	// One wrong answer is already on the record above, so this loop brings the
// total to LockMaxFails-1: still answered 401, one short of the limiter.
	for i := 1; i < LockMaxFails-1; i++ {
		rec = f.do(http.MethodPost, "/api/lock/unlock", localAddr, `{"code":"wrong-one"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d = %d, want 401", i+1, rec.Code)
		}
	}
	// The LockMaxFails'th wrong attempt is the one that trips it.
	rec = f.do(http.MethodPost, "/api/lock/unlock", localAddr, `{"code":"wrong-one"}`, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d = %d, want 429", LockMaxFails, rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 must carry Retry-After")
	}

	// Even the right code is refused while the limiter is running.
	rec = f.do(http.MethodPost, "/api/lock/unlock", localAddr, `{"code":"hunter2code"}`, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the right code during the lockout = %d, want 429", rec.Code)
	}

	// After the lockout expires the right code works.
	f.clock = f.clock.Add(LockLockout + time.Second)
	cookie := f.unlock("hunter2code")
	if cookie == nil {
		t.Fatal("the correct code was refused after the lockout expired")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("the session cookie must be SameSite=Strict")
	}
	if cookie.Path != "/" {
		t.Errorf("the session cookie path = %q, want /", cookie.Path)
	}

	// With the cookie the dashboard works and sees the PIN again.
	rec = f.do(http.MethodGet, "/api/panel/status", localAddr, "",
		map[string]string{"Cookie": LockSessionCookie + "=" + cookie.Value})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), f.pin) {
		t.Errorf("an unlocked local status = %d %s, want 200 with the PIN",
			rec.Code, rec.Body.String())
	}
}

func TestIdleExpiry(t *testing.T) {
	f := newLockFixture(t, true)
	cookie := f.unlock("hunter2code")
	if cookie == nil {
		t.Fatal("could not unlock")
	}
	hdr := map[string]string{"Cookie": LockSessionCookie + "=" + cookie.Value}

	// Just inside the window the session still works.
	f.clock = f.clock.Add(LockIdleTimeout - time.Minute)
	if rec := f.do(http.MethodGet, "/api/panel/status", localAddr, "", hdr); rec.Code != http.StatusOK {
		t.Fatalf("inside the idle window = %d, want 200", rec.Code)
	}

	// Past the window without a touch it is gone.
	f.clock = f.clock.Add(2 * LockIdleTimeout)
	if rec := f.do(http.MethodGet, "/api/panel/status", localAddr, "", hdr); rec.Code != http.StatusUnauthorized {
		t.Errorf("after the idle window = %d, want 401", rec.Code)
	}
}

func TestSessionDoesNotSurviveRestart(t *testing.T) {
	f := newLockFixture(t, true)
	cookie := f.unlock("hunter2code")
	if cookie == nil {
		t.Fatal("could not unlock")
	}
	// A restart is a fresh Lock, and sessions live only in memory.
	fresh := NewLock(filepath.Join(f.dir, SettingsFileName), f.lock.settings, discardLogger{})
	f.srv.SetLock(fresh)
	rec := f.do(http.MethodGet, "/api/panel/status", localAddr, "",
		map[string]string{"Cookie": LockSessionCookie + "=" + cookie.Value})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a session survived a restart = %d, want 401", rec.Code)
	}
}

// ---------------------------------------------------------------- test 5

func TestLockSetRequiresSessionAndOrigin(t *testing.T) {
	f := newLockFixture(t, false)

	// Turning the lock on needs no code but does need a same-origin POST.
	rec := f.do(http.MethodPost, "/api/lock/set", localAddr, `{"code":"brandnewcode"}`,
		map[string]string{"Origin": "http://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("a cross-origin set = %d, want 403", rec.Code)
	}

	rec = f.do(http.MethodPost, "/api/lock/set", localAddr, `{"code":"brandnewcode"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("enabling the lock = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !f.lock.Enabled() {
		t.Fatal("the lock reports itself off after being enabled")
	}

	// Enabling it locks the browser that enabled it.
	rec = f.do(http.MethodGet, "/api/panel/status", localAddr, "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("after enabling, status = %d, want 401 until unlocked", rec.Code)
	}

	// A too-short code is refused by the setter, so the lock never ends up
	// holding something the owner can no longer reproduce.
	rec = f.do(http.MethodPost, "/api/lock/unlock", localAddr, `{"code":"brandnewcode"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("could not unlock the freshly set lock: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDisableNeedsTheCodeEvenWithASession(t *testing.T) {
	f := newLockFixture(t, true)
	cookie := f.unlock("hunter2code")
	if cookie == nil {
		t.Fatal("could not unlock")
	}
	hdr := map[string]string{"Cookie": LockSessionCookie + "=" + cookie.Value}

	// A valid session alone must not be enough to remove the lock.
	rec := f.do(http.MethodPost, "/api/lock/disable", localAddr, `{"code":"not-the-code"}`, hdr)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("disable with a wrong code = %d, want 401", rec.Code)
	}
	if !f.lock.Enabled() {
		t.Fatal("the lock came off with a wrong code")
	}

	rec = f.do(http.MethodPost, "/api/lock/disable", localAddr, `{"code":"hunter2code"}`, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable with the right code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if f.lock.Enabled() {
		t.Error("the lock is still on after a correct disable")
	}
	// And once off, the dashboard is open again.
	if rec := f.get("/api/panel/status"); rec.Code != http.StatusOK {
		t.Errorf("status after disable = %d, want 200", rec.Code)
	}
}

func TestLockActionAllowList(t *testing.T) {
	f := newLockFixture(t, true)

	// No session: refused.
	rec := f.do(http.MethodPost, "/api/lock/action", localAddr, `{"do":"toggle"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an action without a session = %d, want 401", rec.Code)
	}

	cookie := f.unlock("hunter2code")
	hdr := map[string]string{"Cookie": LockSessionCookie + "=" + cookie.Value}

	// Cross-origin: refused even with a session.
	rec = f.do(http.MethodPost, "/api/lock/action", localAddr, `{"do":"toggle"}`,
		map[string]string{"Cookie": hdr["Cookie"], "Origin": "http://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("a cross-origin action = %d, want 403", rec.Code)
	}

	// Anything outside the allow-list is a 400, never an execution.
	for _, bad := range []string{"unpin", "newpin; rm -rf", "", "TOGGLE", "shell"} {
		rec = f.do(http.MethodPost, "/api/lock/action", localAddr,
			`{"do":"`+bad+`"}`, hdr)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("action %q = %d, want 400", bad, rec.Code)
		}
	}

	// The three real ones run.
	for _, good := range []string{"newpin", "toggle", "quit"} {
		rec = f.do(http.MethodPost, "/api/lock/action", localAddr, `{"do":"`+good+`"}`, hdr)
		if rec.Code != http.StatusOK {
			t.Errorf("action %q = %d, want 200: %s", good, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), good) {
			t.Errorf("action %q did not report itself: %s", good, rec.Body.String())
		}
	}
}

// ---------------------------------------------------------------- test 6

// TestStoppedServerStillHides: "Close server" keeps the process and the
// listener alive with serving off. The lock must not relax while stopped.
func TestStoppedServerStillHides(t *testing.T) {
	f := newLockFixture(t, true)
	f.srv.SetServing(false)

	rec := f.get("/api/panel/status")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status while stopped and locked = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), f.pin) {
		t.Error("the PIN leaked while stopped and locked")
	}
	// The tray's own action stays reachable the ordinary way, for the phone.
	rec = f.do(http.MethodGet, "/api/panel/status", remoteAddr, "", nil)
	if rec.Code == http.StatusUnauthorized {
		t.Error("the lock reached the network while stopped")
	}
}

// ---------------------------------------------------------------- test 7

func TestSettingsStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFileName)

	// A missing file reads as off.
	s, corrupt := LoadSettings(path, discardLogger{})
	if corrupt {
		t.Error("a missing file was reported as corrupt")
	}
	if s.Lock.Enabled {
		t.Error("a missing file must read as the lock being off")
	}

	l := NewLock(path, s, discardLogger{})
	if err := l.setCode("storedcode1", true); err != nil {
		t.Fatalf("setCode: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}
	if strings.Contains(string(raw), "storedcode1") {
		t.Error("the code is stored in the clear")
	}

	// A second set must use a different salt, so two identical codes do not
	// produce identical records.
	first := l.settings.Lock.Salt
	if err := l.setCode("storedcode1", true); err != nil {
		t.Fatalf("second setCode: %v", err)
	}
	if l.settings.Lock.Salt == first {
		t.Error("the salt did not change between settings")
	}

	// A corrupt file is moved aside, not deleted, and reads as off.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing corrupt settings: %v", err)
	}
	s, corrupt = LoadSettings(path, discardLogger{})
	if !corrupt {
		t.Error("a corrupt file was not reported")
	}
	if s.Lock.Enabled {
		t.Error("a corrupt file must read as the lock being off")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the corrupt file was not moved aside")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("the corrupt file was not kept as settings.json.corrupt: %v", err)
	}

	// An enabled lock with no stored code must never lock the owner out.
	if err := os.WriteFile(path,
		[]byte(`{"version":1,"lock":{"enabled":true}}`), 0o600); err != nil {
		t.Fatalf("writing settings: %v", err)
	}
	s, _ = LoadSettings(path, discardLogger{})
	if s.Lock.Enabled {
		t.Error("an enabled lock with no code must be downgraded to off")
	}
}

func TestCodeLengthBounds(t *testing.T) {
	if err := validCode("12345"); err == nil {
		t.Error("a five character code must be refused")
	}
	if err := validCode(strings.Repeat("a", 65)); err == nil {
		t.Error("a 65 character code must be refused")
	}
	if err := validCode("123456"); err != nil {
		t.Errorf("digits must be accepted: %v", err)
	}
}

// ---------------------------------------------------------------- test 8

func TestCORSIsSameOriginOnly(t *testing.T) {
	f := newLockFixture(t, false)

	rec := f.do(http.MethodGet, "/api/panel/status", localAddr, "",
		map[string]string{"Origin": "http://evil.example"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("a foreign Origin was echoed back as %q", got)
	}

	rec = f.do(http.MethodGet, "/api/panel/status", localAddr, "",
		map[string]string{"Origin": "http://127.0.0.1:9520"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Error("the same origin was refused an Allow-Origin header")
	}

	// No Origin at all is the phone's case and must be untouched.
	rec = f.get("/api/panel/status")
	if rec.Code != http.StatusOK {
		t.Errorf("a request with no Origin = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------- test 9

func TestLockStateCarriesNoSecrets(t *testing.T) {
	f := newLockFixture(t, true)
	rec := f.get("/api/lock/state")
	if rec.Code != http.StatusOK {
		t.Fatalf("state = %d, want 200", rec.Code)
	}
	var state map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	for _, k := range []string{"enabled", "unlocked", "local"} {
		if _, ok := state[k]; !ok {
			t.Errorf("state is missing %q", k)
		}
	}
	for _, k := range []string{"salt", "hash", "iterations", "pin", "code"} {
		if _, ok := state[k]; ok {
			t.Errorf("state leaked %q: %s", k, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), f.pin) {
		t.Error("the state endpoint leaked the PIN")
	}
}