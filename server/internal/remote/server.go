package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"smartremote/server/internal/wsx"
)

// DefaultPort is the TCP port for both the HTTP API and the WebSocket.
const DefaultPort = 9520

// Server wires the HTTP API, WebSocket command channel, MJPEG stream and
// discovery responder together.
type Server struct {
	dispatch *Dispatcher
	auth     *Auth
	macros   *MacroStore
	audio    AudioSink
	viewer   *ViewerLock

	// A separate lock for the camera.
	//
	// The single-viewer rule exists because two viewers of one device halve
	// its frame rate. That holds for a camera exactly as it does for a screen,
	// but a camera and a screen are different devices and contend for nothing,
	// so sharing one lock would mean opening the Webcam tab locked the user
	// out of the Screen tab and vice versa.
	webcamViewer *ViewerLock

	logger  *log.Logger
	started time.Time
	port    int

	clients   map[*wsClient]struct{}
	clientsMu sync.Mutex
	clientSeq atomic.Uint64

	// mdns is the discovery responder, nil when disabled.
	mdns *MDNSResponder

	httpSrv  *http.Server
	shutdown chan struct{}
	once     sync.Once

	// serving gates new commands and streams when the user stops the server
	// from the tray without quitting the process. True by default; the tray
	// "Close server / Activate server" toggle flips it. The HTTP listener
	// stays bound so resume never fails on a stolen port.
	serving atomic.Bool

	// blocked is the owner-managed set of refused device IPs. Consulted
	// after a successful PIN check (never before, so outsiders learn
	// nothing) and enforced on auth, commands and streams alike.
	blocked *BlockList
	// blockCorrupt remembers a corrupt block file so the dashboard can warn
	// instead of silently showing an empty list.
	blockCorrupt bool
}

// SetMDNS attaches a discovery responder to the server.
func (s *Server) SetMDNS(m *MDNSResponder) { s.mdns = m }

// wsClient is one connected control client.
type wsClient struct {
	conn *wsx.Conn
	id   string
	addr string
	// name is the phone label sent in the auth frame (optional). Shown on
	// the dashboard device list so "4 clients" becomes identifiable rows.
	name string
	// connectedAt records when the socket was accepted, shown as-is.
	connectedAt time.Time
	// authenticated gates command execution. An unauthenticated socket may
	// only send the auth message.
	authenticated bool
	sendMu        sync.Mutex
}

// ServerOptions configures a Server.
type ServerOptions struct {
	Port     int
	Logger   *log.Logger
	Audio    AudioSink
	Macros   *MacroStore
	Auth     *Auth
	Dispatch *Dispatcher
	// BlockFile persists the blocked-device list. Empty means in-memory
	// only (used by tests); the real entry point always passes a path.
	BlockFile string
}

// NewServer builds a server around an existing dispatcher.
func NewServer(opts ServerOptions) *Server {
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if opts.Port == 0 {
		opts.Port = DefaultPort
	}
	if opts.Dispatch == nil {
		opts.Dispatch = NewDispatcher(DispatcherOptions{
			Logger: opts.Logger,
			Macros: opts.Macros,
			Audio:  opts.Audio,
		})
	}
	if opts.Auth == nil {
		a, err := NewAuth("")
		if err != nil {
			opts.Logger.Printf("PIN store unavailable: %v", err)
		}
		opts.Auth = a
	}
	srv := &Server{
		dispatch:     opts.Dispatch,
		auth:         opts.Auth,
		macros:       opts.Dispatch.macros,
		audio:        opts.Audio,
		viewer:       &ViewerLock{},
		webcamViewer: &ViewerLock{},
		logger:       opts.Logger,
		started:      time.Now(),
		port:         opts.Port,
		clients:      map[*wsClient]struct{}{},
		shutdown:     make(chan struct{}),
	}
	// A fresh server always starts in serving state. The zero value of
	// atomic.Bool is false, so this explicit store is what keeps a newly
	// started server from refusing every command until the tray toggles it.
	srv.serving.Store(true)
	// A corrupt block file starts empty by design (fail-open with a warning,
	// never a wedged server); the dashboard surfaces blockCorrupt as a flag.
	srv.blocked, srv.blockCorrupt = OpenBlockList(opts.BlockFile, opts.Logger)
	return srv
}

// Handler builds the HTTP mux with every API route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/panel/status", s.handleStatus)
	mux.HandleFunc("/api/net", s.handleNet)
	mux.HandleFunc("/api/macros", s.handleMacros)
	mux.HandleFunc("/api/macros/", s.handleMacroByID)
	mux.HandleFunc("/api/pin", s.handlePin)
	mux.HandleFunc("/api/devices/block", s.handleBlockDevice)
	mux.HandleFunc("/api/devices/unblock", s.handleUnblockDevice)
	mux.HandleFunc("/screen", s.handleScreen)
	mux.HandleFunc("/webcam", s.handleWebcam)
	mux.HandleFunc("/api/cameras", s.handleCameras)
	mux.HandleFunc("/api/screencap-test", s.handleScreencapTest)
	mux.HandleFunc("/ws", s.handleWS)

	// The dashboard is embedded, so the binary stays dependency free and the
	// panel assets can never be missing at runtime. The root serves the
	// index; /assets/... serves the stylesheet and script.
	mux.Handle("/assets/", webHandler())
	mux.Handle("/", DashboardHandler())

	return s.withCORS(s.withLogging(mux))
}

// withCORS allows the dashboard to be loaded from another origin during
// development (for example flutter run -d chrome on a different port).
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withLogging logs each request at a compact level.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// The screen stream is long-lived, so its duration is expected to be
		// large; the stream handler logs it on completion instead.
		if r.URL.Path != "/screen" {
			s.logger.Printf("%s %s from %s in %s",
				r.Method, r.URL.Path, clientLabel(r), time.Since(start).Round(time.Millisecond))
		}
	})
}

// handleHealth is a liveness probe that does not touch the dispatcher, so it
// stays responsive even when input is blocked by UIPI.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": ProtocolVersion,
		"uptime":  time.Since(s.started).Round(time.Second).String(),
	})
}

// PanelStatus is the dashboard payload.
type PanelStatus struct {
	OK       bool   `json:"ok"`
	PIN      string `json:"pin"`
	Port     int    `json:"port"`
	Host     string `json:"host"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	// AppVersion is the release number ("1.0.0"), which is not the same thing as
	// Version: the latter is ProtocolVersion ("1.0"), bumped only on wire-format
	// changes. The dashboard shows both so a bug report can name the release.
	AppVersion string `json:"appVersion"`
	// Developer attributes the software. Shown in the dashboard footer.
	Developer string `json:"developer"`
	Uptime    string `json:"uptime"`
	// DeviceList carries one row per connected control channel so the
	// dashboard can show device names instead of only a bare count.
	DeviceList []ClientDevice `json:"devices"`
	// Clients stays as the count so older dashboards and phone pollers that
	// only read "clients" keep working unchanged. It counts paired
	// (authenticated) channels only, so the dashboard's own event socket -
	// which never authenticates - is never counted as a device.
	Clients      int            `json:"clients"`
	Serving      bool           `json:"serving"`
	StreamBusy   bool           `json:"streamBusy"`
	InputBlocked bool           `json:"inputBlocked"`
	Diagnosis    string         `json:"diagnosis,omitempty"`
	CommandsRun  uint64         `json:"commandsRun"`
	CommandsErr  uint64         `json:"commandsFailed"`
	Display      DeviceInfo     `json:"display"`
	Macros       int            `json:"macroCount"`
	Stream       StreamSettings `json:"stream"`
	// BlockedList carries the owner-managed refused IPs so the dashboard can
	// render Unblock buttons. Additive: older readers ignore unknown fields.
	BlockedList []BlockEntry `json:"blocked"`
	// BlockCorrupt warns that the block file was corrupt and the list above
	// started empty, rather than silently pretending nothing was blocked.
	BlockCorrupt bool `json:"blockCorrupt,omitempty"`
}

// ClientDevice is one dashboard row: whatever the client told us plus the
// network address we observed. Every field is optional except Addr so an
// older phone that sends no name still renders as an "Unknown device" row.
type ClientDevice struct {
	Name          string `json:"name"`
	Addr          string `json:"addr"`
	ConnectedAt   string `json:"connectedAt"`
	Authenticated bool   `json:"authenticated"`
}

// handleStatus returns the dashboard state.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	executed, failed := s.dispatch.Stats()
	breaker := s.dispatch.Breaker()
	host, _ := os.Hostname()

	writeJSON(w, http.StatusOK, PanelStatus{
		OK:           true,
		PIN:          s.auth.Pin(),
		Port:         s.port,
		Host:         s.primaryIPv4(),
		Hostname:     host,
		Version:      ProtocolVersion,
		AppVersion:   AppVersion,
		Developer:    Developer,
		Uptime:       time.Since(s.started).Round(time.Second).String(),
		DeviceList:   s.clientList(),
		Clients:      s.clientCount(),
		Serving:      s.IsServing(),
		StreamBusy:   s.viewer.Holder() != "",
		InputBlocked: breaker.Tripped(),
		Diagnosis:    breaker.Diagnosis(),
		CommandsRun:  executed,
		CommandsErr:  failed,
		Display:      QueryDeviceInfo(),
		Macros:       len(s.macros.List()),
		Stream:       s.dispatch.StreamSettings(),
		BlockedList:  s.blocked.List(),
		BlockCorrupt: s.blockCorrupt,
	})
}

// handlePin regenerates the PIN on POST.
func (s *Server) handlePin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST to regenerate the PIN", http.StatusMethodNotAllowed)
		return
	}
	pin, err := s.auth.Regenerate()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.broadcast(Event{Kind: "pin_changed",
		Msg: "PIN regenerated; every client must pair again"})
	s.logger.Printf("PIN regenerated")
	writeJSON(w, http.StatusOK, map[string]string{"pin": pin})
}

// blockGuard restricts device block/unblock to the PC owner at the console.
//
// Two independent checks, either of which refuses: the caller must arrive on
// loopback (the dashboard is the owner's UI; phones never manage blocks),
// and a present Origin must match the request Host (so a website opened on
// this PC cannot CSRF-block the owner's phones via 127.0.0.1).
func (s *Server) blockGuard(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	host := clientLabel(r)
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		s.logger.Printf("block API refused non-loopback caller %s", host)
		http.Error(w, "device management is only available on this PC", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			s.logger.Printf("block API refused mismatched Origin %q", origin)
			http.Error(w, "mismatched origin", http.StatusForbidden)
			return false
		}
	}
	return true
}

// handleBlockDevice refuses a device IP: it is disconnected at once and
// refused afterwards until unblocked.
func (s *Server) handleBlockDevice(w http.ResponseWriter, r *http.Request) {
	if !s.blockGuard(w, r) {
		return
	}
	var req struct {
		IP   string `json:"ip"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide {\"ip\":\"...\"}"})
		return
	}
	// NOTE: loopback is blockable and it harms nothing: phones never arrive
	// on loopback, the dashboard itself is plain HTTP (unaffected by the
	// block set), and unblocking uses the same endpoint. Refusing it would
	// only trade a real capability for a feeling of safety.
	s.BlockDevice(req.IP, sanitizeClientName(req.Name))
	writeJSON(w, http.StatusOK, map[string]any{"blocked": req.IP})
}

// handleUnblockDevice removes an IP from the refused set.
func (s *Server) handleUnblockDevice(w http.ResponseWriter, r *http.Request) {
	if !s.blockGuard(w, r) {
		return
	}
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide {\"ip\":\"...\"}"})
		return
	}
	s.UnblockDevice(req.IP)
	writeJSON(w, http.StatusOK, map[string]any{"unblocked": req.IP})
}

// handleMacros lists macros on GET and adds one on POST.
func (s *Server) handleMacros(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.macros.List())
	case http.MethodPost:
		var m Macro
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.macros.Add(m); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.broadcast(Event{Kind: "macros_changed"})
		writeJSON(w, http.StatusCreated, m)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMacroByID deletes a macro.
func (s *Server) handleMacroByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/macros/")
	if id == "" {
		s.handleMacros(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.macros.Remove(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.broadcast(Event{Kind: "macros_changed"})
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// handleScreencapTest runs one trial capture and reports why it failed.
//
// "The screen viewer is blank" spans several unrelated causes, and guessing
// between them costs a rebuild each time. This answers it in one request:
// whether a frame can be captured here at all, which Win32 code is responsible
// if not, and whether DXGI - the modern alternative - is even available.
//
// It is read-only and captures a single frame into memory, so it is safe to
// call while a stream is open, and safe to leave in place for support.
func (s *Server) handleScreencapTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed,
			map[string]string{"error": "method not allowed"})
		return
	}

	probe := ProbeScreenCapture()
	s.logger.Printf("screencap-test: success=%t error=%q win32=%d session=%d "+
		"dxgiAvailable=%t", probe.Success, probe.Error, probe.Win32Error,
		probe.SessionID, probe.DXGI.Available)

	// Always 200: a failed *capture* is a successful *diagnosis*, and a non-2xx
	// here would read as the endpoint itself being broken.
	writeJSON(w, http.StatusOK, probe)
}

// handleScreen serves the MJPEG stream.
//
// The stream is gated behind the pairing PIN. This is the entire screen of the
// user's PC - whatever is on it, including a password manager or a banking tab -
// and every other unauthenticated route here is read-only metadata or the
// dashboard. Without this check the stream is the one door in the house that
// anyone who learns the port can walk through, so it is authenticated exactly
// like the WebSocket command channel, reusing the same constant-time
// comparison and lockout.
func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeStream(w, r) {
		return
	}

	stream := s.dispatch.StreamSettings()
	ServeScreenStream(w, r, ScreenStreamConfig{
		FPS:     stream.FPS,
		Quality: stream.Quality,
	}, s.viewer, s.logger)
}

// handleWebcam serves the camera stream.
//
// It gates on exactly the same authorizeStream as the desktop feed, and that
// matters more here, not less: a camera pointed at a desk or a hallway is the
// most private thing this server can emit, so the PIN check is not optional and
// shares Auth.Verify, which means the stream shares its lockout counter too.
// A client cannot brute-force the PIN through the camera while being blocked
// on the command channel.
func (s *Server) handleWebcam(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeStream(w, r) {
		return
	}

	stream := s.dispatch.StreamSettings()
	ServeWebcamStream(w, r, ScreenStreamConfig{
		FPS:     stream.FPS,
		Quality: stream.Quality,
	}, s.webcamViewer, s.logger)
}

// handleCameras lists the cameras this PC has, for the diagnostics panel.
//
// It is behind the same PIN gate as the streams: enumerating capture devices is
// a small leak of information about the machine, and every other route in this
// server is either read-only metadata or the dashboard itself.
func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeStream(w, r) {
		return
	}

	devices, err := ListWebcams()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"cameras": []WebcamDevice{},
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cameras": devices})
}

// authorizeStream gates the desktop feed behind the pairing PIN.
//
// It also gates the webcam feed and the camera list, so it is named for the
// streams it protects rather than for the single endpoint it grew out of.
//
// It shares Auth.Verify with the WebSocket, so a client that hammers this
// endpoint is rate limited by the same counter and cannot brute-force the PIN
// through the stream while being blocked on the command channel.
func (s *Server) authorizeStream(w http.ResponseWriter, r *http.Request) bool {
	if !s.IsServing() {
		// Stopped from the tray: refuse new viewers with 503 (not 401) so the
		// phone shows "server stopped" instead of "wrong PIN".
		s.logger.Printf("stream refused from %s: server stopped from tray", clientLabel(r))
		http.Error(w, "server stopped from tray; press Activate to resume", http.StatusServiceUnavailable)
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}

	pin := r.URL.Query().Get("pin")
	if pin == "" {
		// The PIN is deliberately not echoed in the body: a 401 that repeats
		// nothing is also the response to a missing PIN, so probing cannot
		// distinguish "wrong" from "absent" from "no PIN configured".
		s.logger.Printf("screen stream: rejected request from %s with no PIN",
			clientLabel(r))
		http.Error(w, "invalid PIN", http.StatusUnauthorized)
		return false
	}

	if err := s.auth.Verify(clientLabel(r), pin); err != nil {
		s.logger.Printf("screen stream: rejected request from %s: %v",
			clientLabel(r), err)
		http.Error(w, "invalid PIN", http.StatusUnauthorized)
		return false
	}

	// A blocked device knows the PIN (it is shared and discoverable), so the
	// PIN gate alone cannot keep it out of the streams. Refused with 403 -
	// distinct from 401 - so the phone can show "blocked" instead of
	// misleading the user into retyping the PIN.
	if s.blocked.Blocked(clientLabel(r)) {
		s.logger.Printf("screen stream: refused blocked device %s", clientLabel(r))
		http.Error(w, "blocked: this device is blocked by the PC owner", http.StatusForbidden)
		return false
	}

	return true
}

// writeJSON writes a JSON response with the correct content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// clientCount returns the number of paired (authenticated) control channels.
//
// Unauthenticated sockets - including the dashboard's own live-event socket,
// which never sends an auth message - are not devices and must not inflate
// the count, otherwise the PC counts itself as a connected phone.
func (s *Server) clientCount() int {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	n := 0
	for c := range s.clients {
		if c.authenticated {
			n++
		}
	}
	return n
}

// clientList snapshots one dashboard row per paired channel. Unauthenticated
// sockets are skipped for the same reason as in clientCount: the dashboard's
// event socket would otherwise render as an "Unknown device" row. The lock is
// held only for the copy so a slow dashboard poll cannot stall auth.
func (s *Server) clientList() []ClientDevice {
	s.clientsMu.Lock()
	out := make([]ClientDevice, 0, len(s.clients))
	for c := range s.clients {
		if !c.authenticated {
			continue
		}
		out = append(out, ClientDevice{
			Name:          c.name,
			Addr:          c.addr,
			ConnectedAt:   c.connectedAt.Format("15:04:05"),
			Authenticated: c.authenticated,
		})
	}
	s.clientsMu.Unlock()
	return out
}

// IsServing reports whether the server accepts new commands and streams.
// The tray Close/Activate toggle drives this; the dashboard reflects it.
func (s *Server) IsServing() bool { return s.serving.Load() }

// SetServing flips the tray Close/Activate state. Stopping disconnects every
// client so phones immediately show "reconnecting" instead of hanging on a
// dead socket; starting simply re-arms the gate because the listener never
// closed, so resume cannot fail on a stolen port or a missing firewall rule.
func (s *Server) SetServing(on bool) {
	if on == s.serving.Load() {
		return
	}
	s.serving.Store(on)
	if on {
		s.logger.Printf("server activated from tray")
		s.broadcast(Event{Kind: "server_resumed"})
		return
	}
	s.logger.Printf("server stopped from tray: disconnecting clients")
	s.disconnectAll()
	s.broadcast(Event{Kind: "server_stopped"})
}

// disconnectAll closes every client socket. The read loops then return,
// unregister themselves and release held buttons, so stop never leaves the
// PC mid-drag. Used only by SetServing(false); Shutdown closes the listener
// itself which unblocks the same loops.
func (s *Server) disconnectAll() {
	s.clientsMu.Lock()
	targets := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		targets = append(targets, c)
	}
	s.clientsMu.Unlock()
	for _, c := range targets {
		_ = c.conn.Close()
	}
}

// BlockDevice refuses a device IP: it is recorded (persisted), every live
// channel from that IP is disconnected at once, and dashboards refresh.
//
// The sockets are closed exactly like SetServing(false) does - the same
// proven path, so a blocked phone shows "reconnecting" and stuck buttons are
// released by the existing read-loop cleanup rather than by a new mechanism.
func (s *Server) BlockDevice(ip, name string) {
	if name == "" {
		name = s.nameForIP(ip)
	}
	added := s.blocked.Block(ip, name)
	if added {
		s.logger.Printf("device blocked: %s (%s)", ip, name)
	}
	s.disconnectIP(ip)
	s.broadcast(Event{Kind: "blocked_changed"})
}

// UnblockDevice removes an IP from the refused set. It does not connect
// anything: the phone's own backoff reconnects it within seconds.
func (s *Server) UnblockDevice(ip string) {
	if s.blocked.Unblock(ip) {
		s.logger.Printf("device unblocked: %s", ip)
	}
	s.broadcast(Event{Kind: "blocked_changed"})
}

// nameForIP returns the name a currently connected channel from ip reported,
// so the blocked row is recognisable even when the dashboard sent no name.
func (s *Server) nameForIP(ip string) string {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for c := range s.clients {
		if c.addr == ip && c.name != "" {
			return c.name
		}
	}
	return ""
}

// disconnectIP closes every channel from ip. The lock is held only for the
// copy; closing under the lock could deadlock against a read loop trying to
// unregister at the same moment.
func (s *Server) disconnectIP(ip string) {
	s.clientsMu.Lock()
	targets := make([]*wsClient, 0)
	for c := range s.clients {
		if c.addr == ip {
			targets = append(targets, c)
		}
	}
	s.clientsMu.Unlock()
	for _, c := range targets {
		_ = c.conn.Close()
	}
}

// broadcast sends an event to every authenticated client.
func (s *Server) broadcast(ev Event) {
	payload, err := json.Marshal(ev)
	if err != nil {
		s.logger.Printf("broadcast encode failed: %v", err)
		return
	}
	s.clientsMu.Lock()
	targets := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		if c.authenticated {
			targets = append(targets, c)
		}
	}
	s.clientsMu.Unlock()

	for _, c := range targets {
		if err := c.sendText(payload); err != nil {
			s.logger.Printf("broadcast to %s failed: %v", c.addr, err)
		}
	}
}

// sendText writes a text frame, serialised per client so a slow client
// cannot interleave frames from two goroutines.
func (c *wsClient) sendText(payload []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.conn.SendText(payload)
}

// primaryIPv4 returns the best non-loopback IPv4 address, which is what a
// phone on the same network needs to connect to.
func (s *Server) primaryIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	var fallback string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ipv4 := ipnet.IP.To4()
		if ipv4 == nil {
			continue
		}
		// Prefer RFC 1918 and CGNAT ranges: those are reachable from a phone
		// on the same WiFi, unlike VPN or virtual-adapter addresses.
		if isPrivate(ipv4) {
			return ipv4.String()
		}
		if fallback == "" {
			fallback = ipv4.String()
		}
	}
	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}

// PrimaryIP returns the address a phone on the same network should use.
func (s *Server) PrimaryIP() string { return s.primaryIPv4() }

// isPrivate reports whether ip is in an RFC 1918 or CGNAT range.
func isPrivate(ip net.IP) bool {
	return ip.IsPrivate() ||
		(len(ip) == 4 && ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127)
}

// AuthRequest is the first message a client must send.
type AuthRequest struct {
	Type string `json:"type"`
	PIN  string `json:"pin"`
	Name string `json:"name,omitempty"`
	// Nonce is echoed in the reply so the client can match this response to
	// the request it is awaiting.
	Nonce string `json:"nonce,omitempty"`
}

// handleWS upgrades the connection and runs the command loop.
//
// Authentication is required before any command is honoured. Refusing to act
// on an unauthenticated socket is what stops a random device on the network
// from driving the PC's keyboard and mouse.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsx.Upgrade(w, r)
	if err != nil {
		// Upgrade failure means the connection is no longer usable, so the
		// error is logged rather than written as an HTTP response.
		s.logger.Printf("websocket upgrade from %s failed: %v", clientLabel(r), err)
		return
	}

	client := &wsClient{
		conn:        conn,
		id:          fmt.Sprintf("c%d", s.clientSeq.Add(1)),
		addr:        clientLabel(r),
		connectedAt: time.Now(),
	}
	s.registerClient(client)
	defer s.unregisterClient(client)

	s.logger.Printf("websocket %s connected from %s", client.id, client.addr)
	defer s.logger.Printf("websocket %s disconnected from %s", client.id, client.addr)

	// A button held when the socket dies can never be released by the client,
	// because the message that would release it has nowhere to go. Lifting it
	// here is what stops the PC being left mid-drag, selecting text on every
	// subsequent cursor move until the machine is rebooted.
	s.dispatch.inj.ReleaseAll()

	// Ask the client to authenticate immediately so the UI can prompt.
	hello, _ := json.Marshal(Event{Kind: "hello", Data: map[string]any{
		"version": ProtocolVersion,
		"client":  client.id,
		"pin":     s.auth.Pin(),
	}})
	_ = client.sendText(hello)

	// A ping keeps intermediaries from idling the connection out and detects
	// a half-open socket.
	stopPing := make(chan struct{})
	go s.pingLoop(client, stopPing)
	defer close(stopPing)

	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			var ce *wsx.CloseError
			if !errors.As(err, &ce) {
				s.logger.Printf("websocket %s read error: %v", client.id, err)
			}
			return
		}

		switch msg.Opcode {
		case wsx.OpText:
			s.handleText(client, msg.Data)
		case wsx.OpBinary:
			s.handleBinary(client, msg.Data)
		default:
			s.logger.Printf("websocket %s sent unsupported opcode %#x", client.id, msg.Opcode)
		}

		select {
		case <-s.shutdown:
			return
		default:
		}
	}
}

// Opcode constants re-exported for readability at the call site above.
const (
	wsOpText   = 0x1
	wsOpBinary = 0x2
)

// handleText processes a JSON text frame.
func (s *Server) handleText(client *wsClient, raw []byte) {
	// The auth message is handled before the authenticated gate.
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Type == "auth" {
		s.handleAuth(client, raw)
		return
	}

	if !client.authenticated {
		s.reply(client, Ack{OK: false, Err: "not authenticated: send {\"type\":\"auth\",\"pin\":\"...\"} first"})
		return
	}

	// A device blocked after it authenticated (or racing the block click) is
	// refused here too, so closing the socket is not the only line of
	// defence - a lingering channel executes nothing.
	if s.blocked.Blocked(client.addr) {
		s.reply(client, Ack{OK: false, Err: "blocked: this device is blocked by the PC owner"})
		return
	}

	// The tray Close toggle stops new commands without killing the process.
	// Authenticated sockets stay open so resume is instant; only execution is
	// refused, with a message the phone can show instead of timing out.
	if !s.IsServing() {
		s.reply(client, Ack{OK: false, Err: "server stopped from tray; press Activate to resume"})
		return
	}

	cmd, err := ParseCommand(raw)
	if err != nil {
		s.reply(client, Ack{OK: false, Err: err.Error()})
		return
	}

	ack := s.dispatch.Execute(cmd)
	if !ack.OK {
		s.logger.Printf("command %q from %s failed: %s", cmd.Type, client.id, ack.Err)
	}
	s.reply(client, ack)
}

// handleAuth validates a pairing request.
func (s *Server) handleAuth(client *wsClient, raw []byte) {
	var req AuthRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.reply(client, Ack{OK: false, Err: "malformed auth request"})
		return
	}
	// New pairings are refused while stopped so a phone cannot sneak in
	// during the Close window; existing sockets are already disconnected by
	// SetServing(false) and will re-auth on resume.
	if !s.IsServing() {
		s.logger.Printf("auth refused from %s: server stopped from tray", client.addr)
		s.reply(client, Ack{OK: false, Err: "server stopped from tray; press Activate to resume", Nonce: req.Nonce})
		return
	}
	if err := s.auth.Verify(client.addr, req.PIN); err != nil {
		s.logger.Printf("auth failure from %s: %v", client.addr, err)
		s.reply(client, Ack{OK: false, Err: err.Error(), Nonce: req.Nonce})
		return
	}

	// The block check runs AFTER a successful PIN check, never before: only
	// someone holding the PIN learns anything, and all they learn is their
	// own status. The reply carries a stable "blocked:" prefix (not a new
	// wire field) so older phones simply display it as text.
	if s.blocked.Blocked(client.addr) {
		s.logger.Printf("auth refused from %s: device blocked by owner", client.addr)
		s.reply(client, Ack{OK: false, Err: "blocked: this device is blocked by the PC owner", Nonce: req.Nonce})
		// Close the socket: a refused device must not linger as a silent
		// half-open channel, and the phone's backoff will surface the
		// message again on its next attempt.
		_ = client.conn.Close()
		return
	}

	client.authenticated = true
	client.name = sanitizeClientName(req.Name)
	s.logger.Printf("client %s from %s authenticated (%s)", client.id, client.addr, req.Name)
	s.broadcast(Event{Kind: "clients_changed"})
	s.reply(client, Ack{OK: true, Nonce: req.Nonce})
}

// handleBinary routes a binary frame.
//
// Only two binary kinds exist: a small text header identifying the payload
// as audio, or raw PCM. Audio is only accepted from an authenticated client.
func (s *Server) handleBinary(client *wsClient, data []byte) {
	if !client.authenticated {
		return
	}
	// Same race cover as handleText: a blocked channel streams nothing.
	if s.blocked.Blocked(client.addr) {
		return
	}
	if s.audio == nil {
		return
	}
	// A one-byte prefix distinguishes audio from any future binary type.
	const audioPrefix = 0x01
	if len(data) == 0 || data[0] != audioPrefix {
		s.logger.Printf("client %s sent unrecognised binary frame (%d bytes)", client.id, len(data))
		return
	}
	if !s.audio.Active() {
		s.audio.Start()
	}
	if err := s.audio.Write(data[1:]); err != nil {
		s.logger.Printf("audio write from %s failed: %v", client.id, err)
	}
}

// reply sends an Ack to one client.
func (s *Server) reply(client *wsClient, ack Ack) {
	payload, err := json.Marshal(ack)
	if err != nil {
		return
	}
	if err := client.sendText(payload); err != nil {
		s.logger.Printf("reply to %s failed: %v", client.id, err)
	}
}

// registerClient adds a client to the broadcast set.
func (s *Server) registerClient(c *wsClient) {
	s.clientsMu.Lock()
	s.clients[c] = struct{}{}
	s.clientsMu.Unlock()
}

// unregisterClient removes a client and closes its socket.
func (s *Server) unregisterClient(c *wsClient) {
	s.clientsMu.Lock()
	_, ok := s.clients[c]
	if ok {
		delete(s.clients, c)
	}
	s.clientsMu.Unlock()
	_ = c.conn.Close()
	// A disconnect changes the device list just like a connect does, so the
	// dashboard count and rows stay truthful without waiting for the 4s poll.
	if ok {
		s.broadcast(Event{Kind: "clients_changed"})
	}
}

// sanitizeClientName keeps the dashboard device row safe and readable: one
// line, bounded length, no control characters that could break the layout or
// inject markup (the dashboard renders with textContent, this is defence in
// depth for logs and future renderers).
func sanitizeClientName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\t", " ")
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return strings.TrimSpace(name)
}

// pingLoop sends periodic pings so idle connections stay alive.
func (s *Server) pingLoop(client *wsClient, stop <-chan struct{}) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-s.shutdown:
			return
		case <-ticker.C:
			client.sendMu.Lock()
			err := client.conn.Ping(nil)
			client.sendMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}
