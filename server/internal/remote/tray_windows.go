//go:build windows

package remote

import (
	"fmt"
	"log"
	"sync/atomic"

	"github.com/getlantern/systray"
)

// TrayConfig describes what the tray menu should show.
type TrayConfig struct {
	// PIN is the current pairing PIN, displayed as read-only text.
	PIN string

	// Address is the LAN address a phone should pair against.
	Address string

	// DashboardURL is opened by the "Open Dashboard" item.
	DashboardURL string

	// OnQuit is called when the user picks Quit.
	OnQuit func()

	// OnRegenerate is called when the user asks for a new PIN. It is
	// responsible for calling UpdatePIN once the new value is known.
	OnRegenerate func() string

	// OnToggleStartup is called when the user ticks or unticks the startup
	// item. It receives the state the user is asking for and returns the state
	// that actually took effect.
	//
	// The indirection exists for the same reason as OnQuit: main owns the
	// logger and the decision about whether the change is allowed, so the tray
	// never touches the registry itself. That also lets the menu label be
	// refreshed from the real outcome rather than from what was clicked.
	OnToggleStartup func(enabled bool) bool

	// StartupEnabled is the current registration state, used to render the
	// initial label and tick.
	StartupEnabled bool

	// Logger receives diagnostics. Must not be nil.
	Logger *log.Logger
}

// trayStarted records whether systray actually created an icon.
//
// systray.Run can return without ever invoking onReady (no Explorer shell, or
// running in session 0 / a service). Without this flag the process cannot tell
// "user closed the tray" from "tray was never available", and would exit
// silently on startup.
var trayStarted atomic.Bool

// RunTray shows the tray icon and blocks until the user quits.
//
// It must be called from the main goroutine: on Windows the tray icon has to be
// created on the thread that owns the message loop.
//
// The server is fully functional before this is called, so a tray failure can
// never prevent the HTTP server from starting. Callers should run it after
// Start() and must be prepared for it to return without the tray having started.
func RunTray(cfg TrayConfig) {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	systray.Run(func() { onTrayReady(cfg) }, func() {
		cfg.Logger.Printf("tray: exited")
	})
}

// onTrayReady builds the icon and menu.
//
// It runs on the goroutine that called systray.Run, which Windows requires to
// be the main thread.
func onTrayReady(cfg TrayConfig) {
	trayStarted.Store(true)

	icon := trayIconData(cfg.Logger)
	systray.SetIcon(icon)
	systray.SetTitle("Smart Remote")
	systray.SetTooltip(fmt.Sprintf("Smart Remote - PIN: %s", cfg.PIN))

	header := systray.AddMenuItem("Smart Remote", "Control this PC from your phone")
	header.Disable()
	systray.AddSeparator()

	// The PIN is the single most important thing to surface: with a GUI
	// subsystem there is no console to print it to, and this binary is built
	// with -H windowsgui precisely so no console appears.
	pinItem := systray.AddMenuItem(fmt.Sprintf("PIN: %s", cfg.PIN), "Your pairing PIN")
	pinItem.Disable()

	addrItem := systray.AddMenuItem(
		fmt.Sprintf("Address: %s", cfg.Address),
		"Pair your phone with this address")
	addrItem.Disable()

	mCopy := systray.AddMenuItem("Copy address", "Copy the pairing address to the clipboard")
	mPanel := systray.AddMenuItem("Open Dashboard", cfg.DashboardURL)

	mRegen := systray.AddMenuItem("New PIN", "Generate a new pairing PIN")

	systray.AddSeparator()

	// "Run on Windows Startup".
	//
	// It is a check item, so the tick reflects the real registry state at the
	// moment the menu is opened rather than what the user last asked for. A
	// mismatch here is worse than no tick at all: the user would tick it off
	// believing autostart was disabled while the app starts anyway.
	startupItem := systray.AddMenuItem(
		startupLabel(cfg.StartupEnabled),
		"Start Smart Remote automatically when Windows starts")
	// systray exposes Check and Uncheck separately rather than Check(bool), so
	// the two states are set through one helper to keep the branch in a single
	// place.
	startupItem.Uncheck()
	setStartupCheck(startupItem, cfg.StartupEnabled)

	mQuit := systray.AddMenuItem("Quit", "Exit Smart Remote")

	go func() {
		for {
			select {
			case <-mCopy.ClickedCh:
				if err := clipboardSet(cfg.Address); err != nil {
					cfg.Logger.Printf("tray: could not copy address: %v", err)
				}
			case <-mPanel.ClickedCh:
				OpenBrowser(cfg.DashboardURL, cfg.Logger)
			case <-mRegen.ClickedCh:
				if cfg.OnRegenerate == nil {
					continue
				}
				// Only update the label once regeneration has actually
				// succeeded, so the menu never advertises a stale PIN.
				if pin := cfg.OnRegenerate(); pin != "" {
					systray.SetTooltip(fmt.Sprintf("Smart Remote - PIN: %s", pin))
					pinItem.SetTitle(fmt.Sprintf("PIN: %s", pin))
					cfg.Logger.Printf("tray: PIN regenerated")
				}
			case <-startupItem.ClickedCh:
				if cfg.OnToggleStartup == nil {
					continue
				}
				// Ask for the opposite of the state we believe we are in,
				// then adopt whatever the registry actually says afterwards.
				// Reporting success without checking would leave the tick
				// lying whenever the write failed - for example on a PC where
				// policy forbids per-user autostart.
				want := !startupItem.Checked()
				actual := cfg.OnToggleStartup(want)

				setStartupCheck(startupItem, actual)
				startupItem.SetTitle(startupLabel(actual))

			case <-mQuit.ClickedCh:
				cfg.Logger.Printf("tray: quit requested")
				if cfg.OnQuit != nil {
					cfg.OnQuit()
				}
				systray.Quit()
				return
			}
		}
	}()
}

// setStartupCheck applies the tick state to the startup menu item.
//
// systray's Check and Uncheck are separate zero-argument calls, so this is the
// one place the branch lives; calling them directly at each site invites the two
// to be swapped by mistake, which would invert the meaning of the menu.
func setStartupCheck(item *systray.MenuItem, enabled bool) {
	if enabled {
		item.Check()
		return
	}
	item.Uncheck()
}

// startupLabel renders the menu item for the current registration state.
//
// The word changes with the state rather than staying "Run on Windows Startup",
// so the item describes what clicking it will do. A fixed label next to a tick
// makes the two contradict each other: a ticked "Run on..." reads as though
// clicking would disable it, which is not what a check item does.
func startupLabel(enabled bool) string {
	if enabled {
		return "Disable: start with Windows"
	}
	return "Run on Windows Startup"
}

// TrayStarted reports whether the tray icon was successfully created.
//
// A false result means the UI is running dashboard-only: the server still
// works and the PIN is still available from the dashboard and the log file, but
// there is no tray entry to interact with.
func TrayStarted() bool { return trayStarted.Load() }
