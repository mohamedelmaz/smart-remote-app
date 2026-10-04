package remote

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// Dispatcher executes validated commands against the injector.
//
// Every execution goes through the circuit breaker, so a blocked system
// (UIPI or a locked desktop) degrades into a clearly-reported notice rather
// than silent input loss.
type Dispatcher struct {
	inj       *Injector
	macros    *MacroStore
	breaker   *CircuitBreaker
	logger    *log.Logger
	audio     AudioSink
	stream    atomic.Pointer[StreamSettings]
	execCount atomic.Uint64
	errCount  atomic.Uint64
}

// StreamSettings are the live MJPEG stream parameters.
type StreamSettings struct {
	FPS     int `json:"fps"`
	Quality int `json:"quality"`
}

// DispatcherOptions configures a Dispatcher.
type DispatcherOptions struct {
	Injector *Injector
	Macros   *MacroStore
	Logger   *log.Logger
	// Audio receives voice PCM when a client opens an audio stream.
	Audio AudioSink
	// BreakerThreshold is how many consecutive failures trip the breaker.
	BreakerThreshold int
}

// NewDispatcher builds a dispatcher with sane defaults.
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	if opts.Injector == nil {
		opts.Injector = NewInjector()
	}
	if opts.Macros == nil {
		store, err := NewMacroStore("")
		if err != nil {
			log.Printf("remote: macro store unavailable: %v", err)
		}
		opts.Macros = store
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if opts.BreakerThreshold <= 0 {
		opts.BreakerThreshold = 5
	}

	logger := opts.Logger
	d := &Dispatcher{
		inj:    opts.Injector,
		macros: opts.Macros,
		logger: logger,
		audio:  opts.Audio,
		breaker: NewCircuitBreaker(opts.BreakerThreshold, 10*time.Second, func(diag string) {
			// Always log *why* the breaker opened. A breaker that trips
			// quietly leaves the user debugging a remote control that
			// silently stopped responding.
			logger.Printf("CIRCUIT BREAKER OPEN: %s", diag)
		}),
	}
	d.stream.Store(&StreamSettings{FPS: 15, Quality: 70})
	return d
}

// Breaker exposes the input circuit breaker for status reporting.
func (d *Dispatcher) Breaker() *CircuitBreaker { return d.breaker }

// StreamSettings returns a copy of the current stream settings.
func (d *Dispatcher) StreamSettings() StreamSettings { return *d.stream.Load() }

// Stats reports counters for the status endpoint.
func (d *Dispatcher) Stats() (executed, failed uint64) {
	return d.execCount.Load(), d.errCount.Load()
}

// setVolumeHooks are indirect calls so the platform-specific volume and
// cursor control can be swapped in without this file importing win32.
var (
	volumeApplier func(pct int) error
	cursorApplier func(visible bool) error
	shellRunner   func(command string) error
)

func init() {
	shellRunner = func(command string) error {
		// hiddenCommand, not exec.Command: this runs on the GUI-subsystem
		// server, and a bare exec would have Windows allocate a console
		// window for every shell macro the user triggers.
		return hiddenCommand("cmd", "/c", command).Start()
	}
}

// SetVolumeApplier installs the platform volume implementation.
func SetVolumeApplier(fn func(pct int) error) { volumeApplier = fn }

// SetCursorApplier installs the platform cursor-visibility implementation.
func SetCursorApplier(fn func(visible bool) error) { cursorApplier = fn }

// runShell launches a shell macro detached from the server process.
func runShell(command string) error { return shellRunner(command) }

// Execute validates and runs one command.
//
// It returns an Ack rather than a bare error so the caller can always reply,
// including for validation failures: the mobile app needs to distinguish
// "your command was malformed" from "the command failed on the PC".
func (d *Dispatcher) Execute(c Command) Ack {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		d.errCount.Add(1)
		return Ack{OK: false, Err: err.Error(), Nonce: c.Nonce}
	}

	// While the breaker is open, reject immediately with the diagnosis
	// instead of hammering SendInput, which cannot succeed.
	if d.breaker.Tripped() {
		d.errCount.Add(1)
		return Ack{OK: false, Err: "input blocked: " + d.breaker.Diagnosis(), Nonce: c.Nonce}
	}

	var err error
	switch c.Type {
	case CmdPing:
		// No-op; reaching here already proves the connection is alive.
	case CmdMouseMove:
		err = d.inj.MoveRelative(c.DX, c.DY)
	case CmdMouseClick:
		err = d.inj.Click(c.Button)
	case CmdMouseDown:
		err = d.inj.ButtonDown(c.Button)
	case CmdMouseUp:
		err = d.inj.ButtonUp(c.Button)
	case CmdInputRelease:
		// The client's panic button: drop anything currently held so a lost
		// "button up" cannot leave the PC selecting text forever.
		d.inj.ReleaseAll()
	case CmdMouseScroll:
		err = d.inj.Scroll(c.WheelDX, c.WheelDY)
	case CmdMousePosition:
		err = d.inj.MoveAbsolute(c.X, c.Y)
	case CmdKeyTap:
		err = d.inj.TapKey(c.Key)
	case CmdKeyChord:
		err = d.chord(c)
	case CmdKeyLatch:
		err = d.inj.SetLatch(c.Key, c.Down)
	case CmdText:
		err = d.inj.TypeText(c.Text)
	case CmdMacroRun:
		err = d.runMacro(c.MacroID)
	case CmdVolumeSet:
		err = d.setVolume(c.Volume)
	case CmdStreamConfig:
		d.applyStream(c)
	case CmdCursorShow:
		if cursorApplier != nil {
			err = cursorApplier(c.Visible)
		}
	case CmdAudioStart:
		if d.audio == nil {
			err = fmt.Errorf("remote: audio output is unavailable")
		} else {
			d.audio.Start()
		}
	case CmdAudioStop:
		if d.audio != nil {
			d.audio.Stop()
		}
	default:
		err = fmt.Errorf("remote: unhandled command %q", c.Type)
	}

	blocked, _ := d.breaker.Record(err)
	if err != nil || blocked {
		d.errCount.Add(1)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if blocked {
			msg = "input blocked: " + d.breaker.Diagnosis()
		}
		return Ack{OK: false, Err: msg, Nonce: c.Nonce}
	}

	d.execCount.Add(1)
	return Ack{OK: true, Nonce: c.Nonce}
}

// chord presses a key while the named modifiers are held.
func (d *Dispatcher) chord(c Command) error {
	mods, err := resolveKeys(c.Mods)
	if err != nil {
		return err
	}
	vk, err := resolveKey(c.Key)
	if err != nil {
		return err
	}
	return d.inj.Chord(mods, vk)
}

// resolveKeys maps protocol key names to virtual key codes.
func resolveKeys(names []string) ([]uint16, error) {
	out := make([]uint16, 0, len(names))
	for _, name := range names {
		vk, err := resolveKey(name)
		if err != nil {
			return nil, err
		}
		out = append(out, vk)
	}
	return out, nil
}

// resolveKey resolves a single protocol key name.
//
// It accepts both the named keys in VKNames and bare letters and digits, which
// is what makes the desktop shortcuts expressible: "win" + "e" is a compound
// keystroke, not a named key.
func resolveKey(name string) (uint16, error) {
	if vk, ok := VKNames[low(name)]; ok {
		return vk, nil
	}
	if vk, ok := letterKey(name); ok {
		return vk, nil
	}
	return 0, fmt.Errorf("remote: unknown key %q", name)
}

// letterKey resolves a bare letter to its virtual key code.
//
// VKNames deliberately lists only *named* keys - Escape, F5, the media keys -
// because those are what a remote sends on their own. The desktop shortcuts are
// the opposite: Win+D and Ctrl+C are a modifier plus a single letter, and a
// protocol with no way to express a letter simply cannot offer them.
//
// Note this is the virtual key, not a character: Windows resolves it through
// the active keyboard layout, so Win+E produces the same key the user's own
// keyboard would, on AZERTY as well as QWERTY.
func letterKey(name string) (uint16, bool) {
	if len(name) != 1 {
		return 0, false
	}
	c := name[0]
	switch {
	case c >= 'a' && c <= 'z':
		return uint16(c-'a') + vkA, true
	case c >= 'A' && c <= 'Z':
		return uint16(c-'A') + vkA, true
	case c >= '0' && c <= '9':
		return uint16(c-'0') + vk0, true
	}
	return 0, false
}

// runMacro executes a stored macro by ID.
func (d *Dispatcher) runMacro(id string) error {
	m, ok := d.macros.Get(id)
	if !ok {
		return fmt.Errorf("remote: no macro with id %q", id)
	}
	switch m.Kind {
	case MacroKindShell:
		return runShell(m.Shell)
	case MacroKindKeys:
		// Mods are held for the whole sequence, then the final key is
		// tapped. A one-key macro is a plain tap.
		if len(m.Mods) == 0 {
			if len(m.Keys) == 1 {
				return d.inj.TapKey(m.Keys[0])
			}
			// Sequential taps for multi-key macros without modifiers.
			for _, k := range m.Keys {
				if err := d.inj.TapKey(k); err != nil {
					return err
				}
			}
			return nil
		}
		mods, err := resolveKeys(m.Mods)
		if err != nil {
			return fmt.Errorf("remote: macro %q: %w", id, err)
		}
		last := m.Keys[len(m.Keys)-1]
		vk, err := resolveKey(last)
		if err != nil {
			return fmt.Errorf("remote: macro %q: %w", id, err)
		}
		return d.inj.Chord(mods, vk)
	default:
		return fmt.Errorf("remote: macro %q has unsupported kind %q", id, m.Kind)
	}
}

// setVolume maps a 0..100 percentage onto the platform volume setter.
func (d *Dispatcher) setVolume(pct int) error {
	if volumeApplier == nil {
		return nil
	}
	return volumeApplier(pct)
}

// applyStream updates the MJPEG stream parameters for future connections.
func (d *Dispatcher) applyStream(c Command) {
	s := d.StreamSettings()
	if c.FPS > 0 {
		s.FPS = c.FPS
	}
	if c.Quality > 0 {
		s.Quality = c.Quality
	}
	d.stream.Store(&s)
	d.logger.Printf("stream settings updated: %d fps, quality %d", s.FPS, s.Quality)
}
