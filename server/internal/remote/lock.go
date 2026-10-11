package remote

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The security lock, opt-in and off by default.
//
// What it actually does, stated plainly: when it is on, a browser on this
// machine has to enter a code before it can read the pairing PIN, the device
// list or the diagnostics. That is a shoulder-surfing and walk-up-to-the-PC
// control. It is not a defence against anyone on the network, because the PIN
// still travels in the mDNS TXT record and in the websocket hello frame, both
// of which are unchanged here.
//
// Phones are untouched by design. "Local" means the connection actually
// arrived from this machine, decided from the real RemoteAddr and never from a
// header, so a request off the LAN behaves exactly as it did before.
const (
	// LockSessionCookie is the cookie an unlocked dashboard carries.
	LockSessionCookie = "sr_session"

	// LockIdleTimeout is how long an unlocked dashboard stays unlocked without
	// being used. Sliding: every accepted request pushes it back.
	LockIdleTimeout = 10 * time.Minute

	// LockMaxFails before the unlock form is refused outright.
	LockMaxFails = 5

	// LockLockout is how long the unlock form stays refused.
	LockLockout = 30 * time.Second

	// lockCodeMin and lockCodeMax bound the stored code.
	lockCodeMin = 6
	lockCodeMax = 64

	// lockSaltLen is the random salt length in bytes.
	lockSaltLen = 16

	// lockTokenLen is the session token length in bytes before hex encoding.
	lockTokenLen = 32

	// lockLocalCacheTTL bounds how often the interface list is re-read. It
	// only exists to keep a per-request check off the syscall path.
	lockLocalCacheTTL = 30 * time.Second
)

// LockActions performs the privileged operations a locked dashboard is
// allowed to request. It is supplied by main so the lock never re-implements
// the tray's own logic: "toggle" and "quit" run exactly the code the tray runs.
type LockActions func(do string) (any, error)

// Lock holds the lock settings, the live sessions and the guess limiter.
type Lock struct {
	mu       sync.Mutex
	path     string
	logger   interface{ Printf(string, ...any) }
	settings *Settings
	now      func() time.Time
	actions  LockActions

	// onChange is called after the lock is enabled or disabled so the tray can
	// redraw itself without the server being restarted.
	onChange func(enabled bool)

	// sessions maps a token to the moment it was last used.
	sessions map[string]time.Time

	fails     int
	lockUntil time.Time

	localIPs []net.IP
	localAt  time.Time
}

// NewLock prepares the lock around a loaded settings file.
func NewLock(path string, s *Settings, logger interface{ Printf(string, ...any) }) *Lock {
	if s == nil {
		s = DefaultSettings()
	}
	if s.Lock.Enabled && !s.lockConfigured() {
		s.Lock.Enabled = false
	}
	return &Lock{
		path:     path,
		logger:   logger,
		settings: s,
		now:      time.Now,
		sessions: map[string]time.Time{},
	}
}

// SetActions installs the privileged operations.
func (l *Lock) SetActions(fn LockActions) {
	l.mu.Lock()
	l.actions = fn
	l.mu.Unlock()
}

// SetOnChange installs the tray refresh hook.
func (l *Lock) SetOnChange(fn func(enabled bool)) {
	l.mu.Lock()
	l.onChange = fn
	l.mu.Unlock()
}

// Enabled reports whether the lock is currently on.
func (l *Lock) Enabled() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.settings.Lock.Enabled
}

// blocks reports whether this request must be refused.
//
// It is the single decision the whole feature rests on: on, local, and no
// valid session. Everything else passes straight through to the original
// handler, byte for byte.
func (l *Lock) blocks(r *http.Request) bool {
	if l == nil || !l.Enabled() {
		return false
	}
	if !l.isLocal(r) {
		return false
	}
	return !l.hasSession(r)
}

// isLocal reports whether the peer is this machine.
//
// RemoteAddr is the only input. No forwarded header is consulted anywhere,
// because any header can be written by whoever is asking.
func (l *Lock) isLocal(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	return l.ownAddress(ip)
}

// ownAddress reports whether ip is one of this machine's own addresses, which
// is what makes the dashboard locked even when it was opened as
// http://192.168.x.x:9520 rather than through loopback.
func (l *Lock) ownAddress(ip net.IP) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.localIPs == nil || l.now().Sub(l.localAt) > lockLocalCacheTTL {
		var ips []net.IP
		addrs, err := net.InterfaceAddrs()
		if err == nil {
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok {
					ips = append(ips, ipnet.IP)
				}
			}
		}
		l.localIPs, l.localAt = ips, l.now()
	}
	for _, own := range l.localIPs {
		if own.Equal(ip) {
			return true
		}
	}
	return false
}

// sessionToken reads the cookie and reports whether it names a live session,
// sliding the idle window when it does.
func (l *Lock) sessionToken(r *http.Request) (string, bool) {
	c, err := r.Cookie(LockSessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	seen, ok := l.sessions[c.Value]
	if !ok {
		return "", false
	}
	if l.now().Sub(seen) > LockIdleTimeout {
		delete(l.sessions, c.Value)
		return "", false
	}
	l.sessions[c.Value] = l.now()
	return c.Value, true
}

// hasSession is sessionToken reduced to the answer blocks needs.
func (l *Lock) hasSession(r *http.Request) bool {
	_, ok := l.sessionToken(r)
	return ok
}

// newSession mints a token. Restarting the server drops the whole map, so
// every session is invalid the moment the process goes away.
func (l *Lock) newSession() (string, error) {
	raw := make([]byte, lockTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hexToken(raw)
	l.mu.Lock()
	l.sessions[token] = l.now()
	l.mu.Unlock()
	return token, nil
}

// dropSession forgets one token, or all of them when token is empty.
func (l *Lock) dropSession(token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if token == "" {
		l.sessions = map[string]time.Time{}
		return
	}
	delete(l.sessions, token)
}

// guessLimited reports whether the unlock form is currently refused, and how
// long is left. The counter is global rather than per address on purpose: the
// form only serves this machine, so a per-address limit would let a walk-up
// attacker simply spread attempts across sources - and there is only one
// source anyway.
func (l *Lock) guessLimited() (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lockUntil.IsZero() {
		return false, 0
	}
	left := l.lockUntil.Sub(l.now())
	if left <= 0 {
		l.lockUntil = time.Time{}
		l.fails = 0
		return false, 0
	}
	return true, left
}

// recordFailure counts a wrong code and returns whether the caller is now
// locked out, plus how long.
func (l *Lock) recordFailure() (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails++
	if l.fails < LockMaxFails {
		return false, 0
	}
	l.fails = 0
	l.lockUntil = l.now().Add(LockLockout)
	return true, LockLockout
}

// clearFailures forgets the guess counter after a success.
func (l *Lock) clearFailures() {
	l.mu.Lock()
	l.fails = 0
	l.lockUntil = time.Time{}
	l.mu.Unlock()
}

// verify compares a presented code with the stored hash in constant time.
func (l *Lock) verify(code string) bool {
	l.mu.Lock()
	saltB64, hashB64, iter := l.settings.Lock.Salt, l.settings.Lock.Hash, l.settings.Lock.Iterations
	l.mu.Unlock()

	if saltB64 == "" || hashB64 == "" || iter <= 0 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	got, err := deriveKey(code, salt, iter)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// setCode stores a new code, replacing salt, hash and iteration count.
func (l *Lock) setCode(code string, enable bool) error {
	salt := make([]byte, lockSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	sum, err := deriveKey(code, salt, DefaultLockIterations)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.settings.Lock = LockSettings{
		Enabled:    enable,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Hash:       base64.StdEncoding.EncodeToString(sum),
		Iterations: DefaultLockIterations,
	}
	snapshot := *l.settings
	notify := l.onChange
	l.mu.Unlock()

	if err := SaveSettings(l.path, &snapshot); err != nil {
		return err
	}
	if notify != nil {
		notify(enable)
	}
	return nil
}

// disable turns the lock off. Every session dies with it.
func (l *Lock) disable() error {
	l.mu.Lock()
	l.settings.Lock = LockSettings{}
	snapshot := *l.settings
	l.sessions = map[string]time.Time{}
	notify := l.onChange
	l.mu.Unlock()

	if err := SaveSettings(l.path, &snapshot); err != nil {
		return err
	}
	if notify != nil {
		notify(false)
	}
	return nil
}

// deriveKey is the one place a code is turned into a key. The standard library
// only: no dependency is added for this.
func deriveKey(code string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, code, salt, iter, sha256.Size)
}

func hexToken(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// sameOrigin reports whether a browser sent this request from this site.
//
// It mirrors blockGuard: a request with no Origin is not a cross-site browser
// request and is allowed, and any Origin that does not match Host is refused.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

// writeLocked is the single 401 body. It carries nothing about the code.
func writeLocked(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "locked"})
}

// lockState is what /api/lock/state reports. It deliberately carries no salt,
// no hash and no code.
type lockState struct {
	Enabled  bool `json:"enabled"`
	Unlocked bool `json:"unlocked"`
	Local    bool `json:"local"`
}

func (s *Server) handleLockState(w http.ResponseWriter, r *http.Request) {
	state := lockState{Enabled: false, Unlocked: false, Local: false}
	if s.lock != nil {
		state.Enabled = s.lock.Enabled()
		state.Local = s.lock.isLocal(r)
		state.Unlocked = state.Enabled && s.lock.hasSession(r)
	}
	writeJSON(w, http.StatusOK, state)
}

// requirePost refuses anything that is not a POST, so these endpoints can
// never be triggered by following a link.
func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed,
			map[string]string{"error": "use POST"})
		return false
	}
	return true
}

func (s *Server) handleLockUnlock(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "mismatched origin"})
		return
	}
	if !s.lock.Enabled() {
		// Nothing to unlock. Answering "ok" keeps a stale browser tab from
		// showing a form for a lock that is not on.
		writeJSON(w, http.StatusOK, lockState{Enabled: false, Local: true})
		return
	}
	if limited, left := s.lock.guessLimited(); limited {
		w.Header().Set("Retry-After", strconv.Itoa(int(left.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":   "too many attempts",
			"retryIn": int(left.Seconds()) + 1,
		})
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide {\"code\":\"...\"}"})
		return
	}
	// The code itself is never logged, not even on failure.
	if !s.lock.verify(body.Code) {
		if limited, left := s.lock.recordFailure(); limited {
			w.Header().Set("Retry-After", strconv.Itoa(int(left.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":   "too many attempts",
				"retryIn": int(left.Seconds()) + 1,
			})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect code"})
		return
	}

	s.lock.clearFailures()
	token, err := s.lock.newSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start a session"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     LockSessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, lockState{Enabled: true, Unlocked: true, Local: true})
}

func (s *Server) handleLockLogout(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "mismatched origin"})
		return
	}
	// "Lock now" re-locks this browser only. The code stays set, so the tray
	// and the rest of the machine stay locked.
	if s.lock != nil {
		token, _ := s.lock.sessionToken(r)
		s.lock.dropSession(token)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     LockSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, lockState{Enabled: s.lock.Enabled(), Local: true})
}

func (s *Server) handleLockSet(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "mismatched origin"})
		return
	}
	enabled := s.lock.Enabled()

	// A lock that is already on needs a live session; turning it on for the
	// first time is a local act that needs no code.
	if enabled && !s.lock.hasSession(r) {
		writeLocked(w)
		return
	}

	var body struct {
		Code    string `json:"code"`
		Current string `json:"current"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide {\"code\":\"...\"}"})
		return
	}
	if err := validCode(body.Code); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if enabled && !s.lock.verify(body.Current) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect code"})
		return
	}
	if err := s.lock.setCode(body.Code, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the lock"})
		return
	}
	// Enabling the lock locks the browser that enabled it, like any other.
	s.lock.dropSession("")
	writeJSON(w, http.StatusOK, lockState{Enabled: true, Unlocked: false, Local: true})
}

func (s *Server) handleLockDisable(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "mismatched origin"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide {\"code\":\"...\"}"})
		return
	}
	// The current code is required even when a valid session is presented: a
	// stolen cookie must not be enough to remove the lock.
	if !s.lock.verify(body.Code) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect code"})
		return
	}
	if err := s.lock.disable(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the lock"})
		return
	}
	writeJSON(w, http.StatusOK, lockState{Enabled: false, Unlocked: false, Local: true})
}

// lockActions is the strict allow-list. Nothing else is ever executed.
var lockActions = map[string]bool{"newpin": true, "toggle": true, "quit": true}

func (s *Server) handleLockAction(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "mismatched origin"})
		return
	}
	if !s.lock.hasSession(r) {
		writeLocked(w)
		return
	}
	if !s.lock.Enabled() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the lock is off"})
		return
	}
	var body struct {
		Do string `json:"do"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide {\"do\":\"...\"}"})
		return
	}
	if !lockActions[body.Do] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
		return
	}
	s.lock.mu.Lock()
	actions := s.lock.actions
	s.lock.mu.Unlock()
	if actions == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no actions available"})
		return
	}
	payload, err := actions(body.Do)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func validCode(code string) error {
	if len([]rune(code)) < lockCodeMin || len([]rune(code)) > lockCodeMax {
		return errors.New("the code must be between 6 and 64 characters")
	}
	return nil
}