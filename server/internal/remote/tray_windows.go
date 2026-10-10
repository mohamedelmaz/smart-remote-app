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

	// OnToggleServing flips the Close/Activate state. It receives true when
	// the user asks to activate and returns the state that actually took
	// effect, so the menu label can never claim something that did not
	// happen. Nil means the menu item is hidden (older callers).
	OnToggleServing func(activate bool) bool

	// Serving is the initial Close/Activate state, used to render the
	// correct label before the first click.
	Serving bool

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

	// LockEnabled is the security lock state at startup, used to render the
	// lock item's label and to decide whether the PIN and address are shown.
	LockEnabled bool

	// OnOpenLock is called when the security-lock item is clicked. It returns
	// the URL to open: the lock card when the lock is off, the code form when
	// it is on. Returning "" opens nothing.
	OnOpenLock func(enabled bool) string

	// Logger receives diagnostics. Must not be nil.
	Logger *log.Logger
}

// trayApplyLock is the live handle the server uses to redraw the menu when the
// lock is switched on or off from the dashboard, with no restart.
//
// It is a package variable because the menu items only exist inside onTrayReady
// and RunTray blocks on the main thread; this is the same handle the click loop
// already uses when it calls SetTitle on a menu item from its own goroutine.
var trayApplyLock func(enabled bool)

// SetTrayLockState refreshes the menu for a new lock state. It is safe to call
// before the tray exists, in which case it does nothing.
func SetTrayLockState(enabled bool) {
	if trayApplyLock != nil {
		trayApplyLock(enabled)
	}
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
	systray.SetTooltip(trayTooltipText(cfg.PIN, cfg.LockEnabled))

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

	// The security lock sits with the other owner actions, between New PIN and
	// the startup toggle, so it is found where the PIN used to be.
	lockItem := systray.AddMenuItem(lockLabel(cfg.LockEnabled), lockTooltip(cfg.LockEnabled))

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

	// Close/Activate stops the server without quitting the process, so the
	// user never needs to reinstall after pressing Quit by mistake. While
	// stopped the tray stays alive with an "Activate server" label; pressing
	// it re-arms the same listener, so resume cannot fail on a stolen port.
	var mServing *systray.MenuItem
	serving := cfg.Serving
	if cfg.OnToggleServing != nil {
		// Default to serving when the caller did not say: a fresh server is
		// always live, and hiding behind "Activate" on first run would look
		// like a broken start.
		if !serving {
			serving = true
		}
		mServing = systray.AddMenuItem(servingLabel(serving), servingTooltip(serving))
	}

	mQuit := systray.AddMenuItem("Quit", "Exit Smart Remote")

	var servingCh chan struct{}
	if mServing != nil {
		servingCh = mServing.ClickedCh
	}

	// locked is the state the click handlers consult, so every branch reads the
	// same value the menu is currently showing.
	locked := cfg.LockEnabled

	// applyLockState redraws the menu for a lock state. While the lock is on
	// the PIN and the address are hidden outright rather than merely disabled:
	// a disabled row is still readable on screen.
	applyLockState := func(on bool) {
		locked = on
		lockItem.SetTitle(lockLabel(on))
		lockItem.SetTooltip(lockTooltip(on))
		if on {
			pinItem.Hide()
			addrItem.Hide()
			mCopy.Hide()
			startupItem.Hide()
			return
		}
		pinItem.Show()
		addrItem.Show()
		mCopy.Show()
		startupItem.Show()
		pinItem.Disable()
		addrItem.Disable()
	}
	trayApplyLock = func(on bool) { applyLockState(on) }
	applyLockState(cfg.LockEnabled)

	// openLocked sends the browser to a dashboard screen instead of acting.
	// The action is only carried in the URL: nothing happens until the owner
	// types the code and confirms on the page.
	openLocked := func(fragment string) {
		OpenBrowser(cfg.DashboardURL+fragment, cfg.Logger)
	}

	go func() {
		for {
			select {
			case <-mCopy.ClickedCh:
				if locked {
					continue
				}
				if err := clipboardSet(cfg.Address); err != nil {
					cfg.Logger.Printf("tray: could not copy address: %v", err)
				}
			case <-mPanel.ClickedCh:
				if locked {
					openLocked("#unlock")
					continue
				}
				OpenBrowser(cfg.DashboardURL, cfg.Logger)
			case <-lockItem.ClickedCh:
				if cfg.OnOpenLock != nil {
					if url := cfg.OnOpenLock(locked); url != "" {
						OpenBrowser(url, cfg.Logger)
					}
				}
			case <-mRegen.ClickedCh:
				if locked {
					openLocked("#do=newpin")
					continue
				}
				if cfg.OnRegenerate == nil {
					continue
				}
				// Only update the label once regeneration has actually
				// succeeded, so the menu never advertises a stale PIN.
				if pin := cfg.OnRegenerate(); pin != "" {
					if locked {
						systray.SetTooltip("Smart Remote - PIN: hidden (lock on)")
					} else {
						systray.SetTooltip(fmt.Sprintf("Smart Remote - PIN: %s", pin))
					}
					pinItem.SetTitle(fmt.Sprintf("PIN: %s", pin))
					cfg.Logger.Printf("tray: PIN regenerated")
				}
			case <-startupItem.ClickedCh:
				if locked {
					continue
				}
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

			case <-servingCh:
				if mServing == nil || cfg.OnToggleServing == nil {
					continue
				}
				if locked {
					openLocked("#do=toggle")
					continue
				}
				// Ask for the opposite of what the label currently claims,
				// then adopt whatever actually took effect.
				actual := cfg.OnToggleServing(!serving)
				serving = actual
				mServing.SetTitle(servingLabel(actual))
				mServing.SetTooltip(servingTooltip(actual))
				cfg.Logger.Printf("tray: serving=%t", actual)

			case <-mQuit.ClickedCh:
				if locked {
					openLocked("#do=quit")
					continue
				}
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

// trayTooltipText keeps the PIN out of the hover text while the lock is on.
func trayTooltipText(pin string, locked bool) string {
	if locked {
		return "Smart Remote - PIN: hidden (lock on)"
	}
	return fmt.Sprintf("Smart Remote - PIN: %s", pin)
}

// lockLabel names the security lock item after what a click will do, the same
// convention the startup and serving items use.
func lockLabel(on bool) string {
	if on {
		return "Security lock: On"
	}
	return "Security lock: Off - Set code..."
}

func lockTooltip(on bool) string {
	if on {
		return "The dashboard asks for a code before it shows anything"
	}
	return "Ask for a code before the dashboard shows the PIN"
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

// servingLabel renders the Close/Activate toggle. The label always names the
// action the click will take, never the current state, so the user can tell
// what will happen before pressing it.
func servingLabel(serving bool) string {
	if serving {
		return "Close server"
	}
	return "Activate server"
}

// servingTooltip explains the toggle outcome for assistive readers.
func servingTooltip(serving bool) string {
	if serving {
		return "Stop accepting phones without quitting Smart Remote"
	}
	return "Resume accepting phones on the same address"
}

// TrayStarted reports whether the tray icon was successfully created.
//
// A false result means the UI is running dashboard-only: the server still
// works and the PIN is still available from the dashboard and the log file, but
// there is no tray entry to interact with.
func TrayStarted() bool { return trayStarted.Load() }
