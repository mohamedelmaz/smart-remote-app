// Command smart-remote-server runs the PC-side remote control server.
//
// The executable is a tray application, not a console program: it has no window,
// no title bar and nothing to type into. Everything the user needs is on the
// tray icon or on the web dashboard. See build.ps1 for the release build.
//
// This is declared with the windowsgui build tag so `go build` alone produces
// the correct subsystem. Without it the default is console, and Windows
// allocates a black console window for the server that sits on top of
// everything else until the user closes it - including a window that follows
// the mouse when it is dragged, which is both ugly and alarming to a user who
// did not ask for a window at all.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"smartremote/server/internal/remote"
)

// windowTitle is both the mDNS instance label and the window title that the
// single-instance replacement logic looks for.
const windowTitle = "Smart Remote"

func main() {
	var (
		port          = flag.Int("port", remote.DefaultPort, "TCP port for the API, websocket and screen stream")
		noMDNS        = flag.Bool("no-mdns", false, "disable mDNS discovery advertising")
		replace       = flag.Bool("replace", true, "ask a running instance to exit and take over")
		noFirewall    = flag.Bool("no-firewall", false, "do not attempt to add a firewall rule on start")
		openDashboard = flag.Bool("dashboard", true, "print the dashboard URL on startup")
		quality       = flag.Int("quality", 70, "JPEG quality for the screen stream, 10..100")
		fps           = flag.Int("fps", 15, "target frames per second for the screen stream, 1..60")
		deviceName    = flag.String("name", windowTitle, "device name shown in discovery")
	)
	flag.Parse()

	// ---- Logging --------------------------------------------------------
	// This binary is GUI-subsystem (-H windowsgui), so it has no console and
	// stdout is not a usable sink. Every line is mirrored to a log file beside
	// the executable, which is the only place the PIN and any startup failure
	// can be recovered from now that the console is gone.
	logger := newFileLogger(remote.LogFilePath())

	// ---- Single instance -------------------------------------------------
	// Two servers would inject input from two sources and fight over the
	// screen stream, so a second launch replaces the first.
	instance, result := remote.ClaimSingleInstance(windowTitle, *replace)
	if result.Err != nil {
		logger.Printf("FATAL: %v", result.Err)
		os.Exit(1)
	}
	defer instance.Release()

	if result.AlreadyRunning {
		if result.ReplacedPID > 0 {
			logger.Printf("replaced the running instance (pid %d)", result.ReplacedPID)
		} else {
			logger.Printf("took over from the instance that was already running")
		}
	}

	// ---- Config paths ----------------------------------------------------
	configDir, err := os.UserConfigDir()
	if err != nil {
		// UserConfigDir can fail in a service context; fall back next to
		// the executable rather than refusing to start.
		configDir = filepath.Dir(os.Args[0])
	}
	configDir = filepath.Join(configDir, "SmartRemote")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		logger.Printf("FATAL: cannot create config dir %s: %v", configDir, err)
		os.Exit(1)
	}

	// ---- PIN -------------------------------------------------------------
	auth, err := remote.NewAuth(filepath.Join(configDir, "pin"))
	if err != nil {
		logger.Printf("FATAL: %v", err)
		os.Exit(1)
	}

	// ---- Security lock (optional, off unless a code has been set) --------
	settingsPath := filepath.Join(configDir, "settings.json")
	settings, settingsCorrupt := remote.LoadSettings(settingsPath, logger)
	_ = settingsCorrupt // LoadSettings already logged and moved the file aside
	lock := remote.NewLock(settingsPath, settings, logger)

	// ---- Macros ----------------------------------------------------------
	macros, err := remote.NewMacroStore(filepath.Join(configDir, "macros.json"))
	if err != nil {
		// A corrupt macro file is not fatal: the defaults are used and the
		// user is told, rather than being locked out of the machine.
		logger.Printf("WARNING: %v", err)
	}

	// ---- Audio -----------------------------------------------------------
	// Voice output is optional: a PC with no playback device still runs the
	// server, only voice routing is unavailable.
	var sink remote.AudioSink
	wave, err := remote.NewWaveOutSink()
	if err != nil {
		logger.Printf("WARNING: audio output unavailable (%v); voice routing disabled", err)
	} else {
		sink = wave
		defer sink.Close()
	}

	// ---- Dispatcher ------------------------------------------------------
	dispatcher := remote.NewDispatcher(remote.DispatcherOptions{
		Macros: macros,
		Logger: logger,
		Audio:  sink,
	})

	// ---- Server ----------------------------------------------------------
	srv := remote.NewServer(remote.ServerOptions{
		Port:      *port,
		Logger:    logger,
		Auth:      auth,
		Dispatch:  dispatcher,
		Audio:     sink,
		BlockFile: filepath.Join(configDir, "blocklist.json"),
	})

	srv.SetLock(lock)

	responder := remote.NewMDNSResponder(
		remote.SanitizeLabel(*deviceName),
		remote.HostLabel(),
		srv.Port(),
		auth.Pin(),
		logger,
	)
	if !*noMDNS {
		if err := responder.Start(); err != nil {
			// Discovery failing must not stop the server: manual pairing
			// remains available, and /api/net explains the cause.
			logger.Printf("WARNING: mDNS discovery unavailable: %v", err)
		}
	}
	srv.SetMDNS(responder)

	// ---- Firewall --------------------------------------------------------
	if !*noFirewall {
		if err := remote.AddFirewallRule(srv.Port(), logger); err != nil {
			logger.Printf("NOTE: %v", err)
			logger.Printf("NOTE: add the rule manually, or run once as Administrator")
		}
	}

	if err := srv.Start(); err != nil {
		logger.Printf("FATAL: %v", err)
		os.Exit(1)
	}

	// ---- Banner ----------------------------------------------------------
	host := srv.PrimaryIP()
	bar := strings.Repeat("=", 58)
	logger.Printf("%s", bar)
	logger.Printf(" Smart Remote is running")
	// While the security lock is on the PIN is not written to the log, which
	// is world-readable next to the executable. The tray hides it too. Old
	// lines from earlier runs are left exactly as they were written.
	if lock.Enabled() {
		logger.Printf("   PIN        : hidden (lock on)")
	} else {
		logger.Printf("   PIN        : %s   (also shown on the dashboard)", auth.Pin())
	}
	logger.Printf("   Address    : %s:%d", host, srv.Port())
	logger.Printf("   Dashboard  : http://%s:%d", host, srv.Port())
	logger.Printf("   Macros     : %d loaded", len(macros.List()))
	logger.Printf("   Input      : SendInput, virtual keys only")
	logger.Printf("   Stream     : %d fps, quality %d", *fps, *quality)
	if responder.Active() {
		logger.Printf("   Discovery  : mDNS advertising as %q", *deviceName)
	} else {
		logger.Printf("   Discovery  : off - pair manually with the address above")
	}
	logger.Printf("%s", bar)

	if *openDashboard {
		// Opening the browser is best-effort; a headless or service session
		// simply skips it. It is deferred briefly so the listener is definitely
		// accepting connections by the time the browser requests the page.
		go func() {
			time.Sleep(600 * time.Millisecond)
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", srv.Port()), logger)
		}()
	}

	// ---- Quit signal ----------------------------------------------------
	// Both the tray and Ctrl+C / SIGTERM funnel into this channel so shutdown
	// runs exactly once, whichever path the user takes.
	quit := make(chan struct{})
	var quitOnce sync.Once
	requestQuit := func(reason string) {
		quitOnce.Do(func() {
			logger.Printf("shutting down (%s)", reason)
			close(quit)
		})
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	// ---- Tray -----------------------------------------------------------
	// The tray runs last and blocks, because on Windows it must own the main
	// thread's message loop. Everything above it is already live, so a tray
	// that never initialises cannot take the server down with it.
	dashboardURL := fmt.Sprintf("http://127.0.0.1:%d", srv.Port())

	go func() {
		select {
		case received := <-sig:
			requestQuit(received.String())
		case <-quit:
		}
	}()

// ---- Shared owner actions ---------------------------------------------
	// These are defined once and referenced from both the tray and the locked
	// dashboard, so the two can never drift apart.
	applyServing := func(activate bool) bool {
		srv.SetServing(activate)
		return srv.IsServing()
	}

	regeneratePIN := func() string {
		newPin, err := auth.Regenerate()
		if err != nil {
			logger.Printf("WARNING: could not regenerate PIN: %v", err)
			return ""
		}
		return newPin
	}

	// The dashboard may ask for these only with a valid session and only with
	// a name from the server's allow-list. The implementation is the same code
	// the tray runs, not a second copy of it.
	lock.SetActions(func(do string) (any, error) {
		switch do {
		case "newpin":
			pin := regeneratePIN()
			if pin == "" {
				return nil, errors.New("could not regenerate the PIN")
			}
			return map[string]any{"pin": pin}, nil
		case "toggle":
			return map[string]any{"serving": applyServing(!srv.IsServing())}, nil
		case "quit":
			requestQuit("dashboard")
			return map[string]any{"closing": true}, nil
		}
		return nil, errors.New("unknown action")
	})
	lock.SetOnChange(remote.SetTrayLockState)

	remote.RunTray(remote.TrayConfig{
		PIN:          auth.Pin(),
		Address:      fmt.Sprintf("%s:%d", host, srv.Port()),
		DashboardURL: dashboardURL,
		Logger:       logger,
		Serving:      true,
		LockEnabled:  lock.Enabled(),
		OnOpenLock: func(enabled bool) string {
			if enabled {
				return dashboardURL + "#unlock"
			}
			return dashboardURL + "#lock"
		},
		OnToggleServing: applyServing,
		OnQuit:          func() { requestQuit("tray") },
		OnRegenerate:    regeneratePIN,
		StartupEnabled:  remote.StartupEnabled(),
		OnToggleStartup: func(enabled bool) bool {
			if err := remote.SetStartup(enabled, logger); err != nil {
				logger.Printf("WARNING: startup change failed: %v", err)
				// Re-read rather than assuming the previous state: the write
				// may have partly succeeded, and guessing would leave the tick
				// disagreeing with the registry.
				return remote.StartupEnabled()
			}
			return remote.StartupEnabled()
		},
	})

	// RunTray returns when the user picks Quit, or when the tray could not be
	// created at all. The latter must not silently exit: the dashboard is
	// still serving, so the process stays up and says so.
	if !remote.TrayStarted() {
		logger.Printf("NOTE: the tray icon could not be created.")
		logger.Printf("NOTE: this server is still running; the dashboard at %s is your UI", dashboardURL)
		select {
		case received := <-sig:
			requestQuit(received.String())
		case <-quit:
		}
	} else {
		requestQuit("tray closed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	// The firewall rules are deliberately left in place on exit: they are
	// named, scoped, and verified idempotently on every start. Deleting them
	// here would break the next start without Administrator rights - rules
	// can only be created with elevation, which is exactly the failure mode
	// that used to leave phones unable to connect after a restart as a
	// normal user.
	logger.Printf("bye")
}

// newFileLogger returns a logger writing to path, falling back to stdout.
//
// A GUI-subsystem process has no valid standard handles, so a logger aimed at
// stderr would silently discard everything - including the PIN. Writing to a
// real file is what makes the server debuggable once the console is gone.
//
// The stderr write is best-effort and its failure is ignored on purpose: with
// io.MultiWriter a stderr error would abort the write *before* the file was
// reached, leaving the log empty, which is the opposite of what is wanted here.
func newFileLogger(path string) *log.Logger {
	if path == "" {
		return log.New(os.Stdout, "", log.Ltime)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// The server still has to start and still has the dashboard and tray.
		fallback := log.New(os.Stdout, "", log.Ltime)
		fallback.Printf("could not open log file %s: %v", path, err)
		return fallback
	}
	return log.New(&fileLogger{file: f}, "", log.Ltime)
}

// fileLogger writes each line to the log file, best-effort to stdout.
type fileLogger struct {
	file *os.File
}

func (w *fileLogger) Write(p []byte) (int, error) {
	if w.file != nil {
		// The file is the source of truth. A failed write is not reported,
		// because reporting it would recurse into this same writer.
		_, _ = w.file.Write(p)
	}
	_, _ = os.Stdout.Write(p)
	// Report a full write regardless: callers only use this to decide whether
	// to retry, and retrying a log line helps nobody.
	return len(p), nil
}

// openBrowser launches the default browser, ignoring failures.
//
// It delegates to the internal helper so the console-suppression logic lives in
// exactly one place rather than being duplicated per package.
func openBrowser(url string, logger *log.Logger) {
	remote.OpenBrowser(url, logger)
}
