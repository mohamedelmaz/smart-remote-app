//go:build windows

// Package remote implements the Windows side of Smart Remote: input
// injection, screen capture, MJPEG streaming, mDNS discovery and the
// command server.
//
// Input injection deliberately uses SendInput with *virtual key codes* only.
// KEYEVENTF_SCANCODE is never set: doing so without a genuine, correct scan
// code produces wrong keystrokes that are very hard to diagnose.
package remote

import (
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procSendInput    = user32.NewProc("SendInput")
	procGetLastError = kernel32.NewProc("GetLastError")
)

// Input types and keyboard flags.
const (
	inputMouse    = 0
	inputKeyboard = 1

	keyEventExtended = 0x0001
	keyEventKeyUp    = 0x0002
	keyEventUnicode  = 0x0004
)

// Mouse flags.
const (
	mouseMove         = 0x0001
	mouseLeftDown     = 0x0002
	mouseLeftUp       = 0x0004
	mouseRightDown    = 0x0008
	mouseRightUp      = 0x0010
	mouseMiddleDown   = 0x0020
	mouseMiddleUp     = 0x0040
	mouseWheel        = 0x0800
	mouseHWheel       = 0x1000
	mouseAbsolute     = 0x8000
	mouseVirtualDesk  = 0x4000
	errorAccessDenied = 5
)

// Virtual key codes used by the protocol. These are layout-independent
// logical keys; the OS maps them to the active keyboard layout.
// Base codes for the alphanumeric range, used to derive letters and digits.
const (
	vk0 = 0x30 // '0'
	vkA = 0x41 // 'A'
)

const (
	vkBack        = 0x08
	vkTab         = 0x09
	vkEnter       = 0x0D
	vkShift       = 0x10
	vkControl     = 0x11
	vkMenu        = 0x12 // Alt
	vkPause       = 0x13
	vkCapital     = 0x14
	vkEscape      = 0x1B
	vkSpace       = 0x20
	vkPrior       = 0x21 // PageUp
	vkNext        = 0x22 // PageDown
	vkEnd         = 0x23
	vkHome        = 0x24
	vkLeft        = 0x25
	vkUp          = 0x26
	vkRight       = 0x27
	vkDown        = 0x28
	vkPrint       = 0x2A
	vkExecute     = 0x2B
	vkSnapshot    = 0x2C
	vkInsert      = 0x2D
	vkDelete      = 0x2E
	vkHelp        = 0x2F
	vkLWin        = 0x5B
	vkRWin        = 0x5C
	vkApps        = 0x5D
	vkNumpad0     = 0x60
	vkNumpad1     = 0x61
	vkNumpad2     = 0x62
	vkNumpad3     = 0x63
	vkNumpad4     = 0x64
	vkNumpad5     = 0x65
	vkNumpad6     = 0x66
	vkNumpad7     = 0x67
	vkNumpad8     = 0x68
	vkNumpad9     = 0x69
	vkMultiply    = 0x6A
	vkAdd         = 0x6B
	vkSeparator   = 0x6C
	vkSubtract    = 0x6D
	vkDecimal     = 0x6E
	vkDivide      = 0x6F
	vkF1          = 0x70
	vkF2          = 0x71
	vkF3          = 0x72
	vkF4          = 0x73
	vkF5          = 0x74
	vkF6          = 0x75
	vkF7          = 0x76
	vkF8          = 0x77
	vkF9          = 0x78
	vkF10         = 0x79
	vkF11         = 0x7A
	vkF12         = 0x7B
	vkNumLock     = 0x90
	vkScroll      = 0x91
	vkLShift      = 0xA0
	vkRShift      = 0xA1
	vkLControl    = 0xA2
	vkRControl    = 0xA3
	vkLMenu       = 0xA4
	vkRMenu       = 0xA5
	vkBrowserBack = 0xA6
	vkMediaPrev   = 0xB1
	vkMediaNext   = 0xB0
	vkMediaPause  = 0xB3
	vkMediaStop   = 0xB2
	vkVolumeMute  = 0xAD
	vkVolumeDown  = 0xAE
	vkVolumeUp    = 0xAF
)

// VKNames maps protocol key names to virtual key codes.
var VKNames = map[string]uint16{
	"back": vkBack, "backspace": vkBack, "bksp": vkBack,
	"tab": vkTab, "enter": vkEnter, "return": vkEnter,
	"shift": vkShift, "ctrl": vkControl, "control": vkControl, "alt": vkMenu,
	"esc": vkEscape, "escape": vkEscape, "space": vkSpace, "del": vkDelete,
	"delete": vkDelete, "insert": vkInsert, "home": vkHome, "end": vkEnd,
	"pageup": vkPrior, "pgup": vkPrior, "pagedown": vkNext, "pgdn": vkNext,
	"up": vkUp, "down": vkDown, "left": vkLeft, "right": vkRight,
	"win": vkLWin, "lwin": vkLWin, "apps": vkApps, "menu": vkApps,
	"capslock": vkCapital, "numlock": vkNumLock, "scrolllock": vkScroll,
	"print": vkSnapshot, "pause": vkPause,
	"f1": vkF1, "f2": vkF2, "f3": vkF3, "f4": vkF4, "f5": vkF5, "f6": vkF6,
	"f7": vkF7, "f8": vkF8, "f9": vkF9, "f10": vkF10, "f11": vkF11, "f12": vkF12,
	"volup": vkVolumeUp, "voldown": vkVolumeDown, "mute": vkVolumeMute,
	"mediaprev": vkMediaPrev, "medianext": vkMediaNext,
	"mediapause": vkMediaPause, "mediaplay": vkMediaPause, "mediastop": vkMediaStop,
}

// extendedKeys need KEYEVENTF_EXTENDEDKEY or they are misread on a 102-key
// layout (the arrow cluster and the right-hand modifiers share scan codes).
var extendedKeys = map[uint16]bool{
	vkUp: true, vkDown: true, vkLeft: true, vkRight: true,
	vkInsert: true, vkDelete: true, vkHome: true, vkEnd: true,
	vkPrior: true, vkNext: true, vkApps: true, vkLWin: true, vkRWin: true,
	vkNumLock: true, vkDivide: true, vkLControl: true, vkRControl: true,
	vkLMenu: true, vkRMenu: true, vkBrowserBack: true, vkPause: true,
}

// The INPUT / KEYBDINPUT / MOUSEINPUT structures from winuser.h.
//
// Sizes matter enormously here. SendInput validates the cbSize argument
// against the real INPUT size and returns 0 with ERROR_INVALID_PARAMETER
// ("The parameter is incorrect") when it does not match, so these types must
// reproduce the C layout exactly rather than merely being "big enough".
//
// On 64-bit Windows that is INPUT = 40 bytes: a 4-byte type, 4 bytes of
// alignment padding, then a 32-byte union. The union is sized by its largest
// member, MOUSEINPUT at 32; Go already does this automatically, so there is no
// explicit padding anywhere below.
//
// An earlier version added a [32]byte pad inside the union "so one definition
// covers both architectures". That made the union 88 bytes and INPUT 96, so
// every SendInput call was rejected outright for a bad cbSize. Because the
// resulting error text mentions no security and looks like a blocked input,
// the circuit breaker misreported it as UIPI or a missing desktop session.
type inputUnion struct {
	// A zero-length array of a pointer-sized type contributes alignment 8 but
	// no storage, which is exactly what is needed: the real union contains
	// MOUSEINPUT, whose dwExtraInfo field is a pointer, so the C union is
	// 8-byte aligned and INPUT places it at offset 8. Without this a plain
	// byte array would align to 1, land the union at offset 4, and make
	// INPUT 36 bytes instead of 40.
	_ [0]uintptr

	_ [unsafe.Sizeof(mouseInput{})]byte
}

// ki and mi reinterpret the union storage as one of its members.
//
// Go has no union type: a struct lays its fields out sequentially, so naming
// both members would make the union their *sum* (24 + 32 = 56 bytes) instead of
// their maximum. Storing the union as raw bytes and casting is what actually
// reproduces the C layout, where the members overlap at offset 0 and the union
// is exactly as large as its biggest member.
//
// Sizing the array with unsafe.Sizeof keeps this correct on both
// architectures: 32 bytes on x64, 24 on x86.
func (u *inputUnion) ki() *keybdInput { return (*keybdInput)(unsafe.Pointer(u)) }

func (u *inputUnion) mi() *mouseInput { return (*mouseInput)(unsafe.Pointer(u)) }

// newKeyInput builds a keyboard INPUT.
func newKeyInput(vk, scan uint16, flags uint32) input {
	var in input
	in.Type = inputKeyboard
	k := in.U.ki()
	k.Vk = vk
	k.Scan = scan
	k.Flags = flags
	return in
}

// newMouseInput builds a mouse INPUT.
func newMouseInput(dx, dy int32, data, flags uint32) input {
	var in input
	in.Type = inputMouse
	m := in.U.mi()
	m.DX = dx
	m.DY = dy
	m.MouseData = data
	m.Flags = flags
	return in
}

// input mirrors the C tagINPUT. The padding before U is inserted by the
// compiler to satisfy MOUSEINPUT's 8-byte alignment, which is what puts the
// union at offset 8 on x64 and offset 4 on x86 - exactly as Windows expects.
type input struct {
	Type uint32
	U    inputUnion
}

type keybdInput struct {
	Vk    uint16
	Scan  uint16
	Flags uint32
	Time  uint32
	Extra uintptr
}

type mouseInput struct {
	DX        int32
	DY        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	Extra     uintptr
}

func (i *input) keyboard(vk uint16, flags uint32) {
	i.Type = inputKeyboard
	k := i.U.ki()
	k.Vk = vk
	k.Flags = flags
}

// unicodeChar prepares a keystroke that produces exactly one UTF-16 code
// unit. KEYEVENTF_UNICODE carries the character in wScan, which is not a
// scan code, so this path is independent of the active keyboard layout and
// works for characters that have no virtual key at all.
func (i *input) unicodeChar(u uint16, up bool) {
	i.Type = inputKeyboard
	k := i.U.ki()
	k.Scan = u
	k.Flags = keyEventUnicode
	if up {
		k.Flags |= keyEventKeyUp
	}
}

// Injector serialises SendInput calls. Serialising also guarantees that a
// chord (modifier down, key, key up, modifier up) is never interleaved with
// another goroutine's chord, which would otherwise drop keystrokes.
type Injector struct {
	mu sync.Mutex
}

// NewInjector returns a ready input injector.
func NewInjector() *Injector { return &Injector{} }

// send performs one SendInput call. SendInput signals failure by returning
// 0; the returned error then carries the last Win32 error code.
func send(inputs []input) (int, error) {
	if len(inputs) == 0 {
		return 0, nil
	}
	n, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	if n != uintptr(len(inputs)) {
		if err != nil {
			return int(n), err
		}
		return int(n), fmt.Errorf(
			"remote: SendInput inserted %d of %d events", n, len(inputs),
		)
	}
	return int(n), nil
}

// tapVirtualKey presses and releases a virtual key.
func (in *Injector) tapVirtualKey(vk uint16) error {
	in.mu.Lock()
	defer in.mu.Unlock()

	var flags uint32
	if extendedKeys[vk] {
		flags |= keyEventExtended
	}
	ev := []input{
		newKeyInput(vk, 0, flags),
		newKeyInput(vk, 0, flags|keyEventKeyUp),
	}
	_, err := send(ev)
	return err
}

// TapKey presses and releases the named virtual key.
//
// It resolves through [resolveKey] rather than reading VKNames directly. That
// indirection is what makes a bare letter work: "d" is not in VKNames, because
// that table deliberately lists only *named* keys, but resolveKey falls through
// to letterKey and returns the D virtual key.
//
// Reading VKNames directly here - as this used to - meant every single-letter
// key was rejected with `unknown key "d"`. That is not a rare edge case: the
// chord path already accepted letters, so Win+D worked while a plain tap of "d"
// did not, which made the protocol look inconsistent for no visible reason.
func (in *Injector) TapKey(name string) error {
	vk, err := resolveKey(name)
	if err != nil {
		return err
	}
	return in.tapVirtualKey(vk)
}

// TapVK presses and releases an explicit virtual key code.
func (in *Injector) TapVK(vk uint16) error { return in.tapVirtualKey(vk) }

// Chord presses a key while modifiers are held, then releases everything in
// reverse order so the host application never sees a stuck modifier.
func (in *Injector) Chord(mods []uint16, vk uint16) error {
	in.mu.Lock()
	defer in.mu.Unlock()

	ev := chordInputs(mods, vk)
	sent, err := send(ev)
	if err == nil && sent == len(ev) {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("remote: SendInput inserted %d of %d chord events",
			sent, len(ev))
	}

	// SendInput can report a partial batch. Best-effort release every key in
	// the chord so a partial press cannot leave a modifier latched on Windows.
	if _, releaseErr := send(chordReleaseInputs(mods, vk)); releaseErr != nil {
		return fmt.Errorf("%w; chord key release failed: %v", err, releaseErr)
	}
	return err
}

func chordInputs(mods []uint16, vk uint16) []input {
	var flags uint32
	if extendedKeys[vk] {
		flags |= keyEventExtended
	}

	ev := make([]input, 0, len(mods)*2+2)
	for _, m := range mods {
		f := uint32(0)
		if extendedKeys[m] {
			f |= keyEventExtended
		}
		ev = append(ev, newKeyInput(m, 0, f))
	}
	ev = append(ev,
		newKeyInput(vk, 0, flags),
		newKeyInput(vk, 0, flags|keyEventKeyUp),
	)
	// Release modifiers in reverse so the last pressed is the first released.
	for i := len(mods) - 1; i >= 0; i-- {
		f := uint32(keyEventKeyUp)
		if extendedKeys[mods[i]] {
			f |= keyEventExtended
		}
		ev = append(ev, newKeyInput(mods[i], 0, f))
	}
	return ev
}

func chordReleaseInputs(mods []uint16, vk uint16) []input {
	flags := uint32(keyEventKeyUp)
	if extendedKeys[vk] {
		flags |= keyEventExtended
	}
	ev := []input{newKeyInput(vk, 0, flags)}
	for i := len(mods) - 1; i >= 0; i-- {
		flags = keyEventKeyUp
		if extendedKeys[mods[i]] {
			flags |= keyEventExtended
		}
		ev = append(ev, newKeyInput(mods[i], 0, flags))
	}
	return ev
}

// SetLatch drives a modifier key to a latched (held) or released state.
// Latched modifiers persist until SetLatch is called again with the same
// key and down=false, which is what the on-screen modifier buttons do.
func (in *Injector) SetLatch(name string, down bool) error {
	vk, ok := VKNames[low(name)]
	if !ok {
		return fmt.Errorf("remote: unknown modifier %q", name)
	}

	in.mu.Lock()
	defer in.mu.Unlock()

	f := uint32(0)
	if extendedKeys[vk] {
		f |= keyEventExtended
	}
	if !down {
		f |= keyEventKeyUp
	}
	_, err := send([]input{newKeyInput(vk, 0, f)})
	return err
}

// TypeText injects a UTF-8 string one UTF-16 code unit at a time using
// KEYEVENTF_UNICODE, so any character reaches the target app regardless of
// the active keyboard layout.
func (in *Injector) TypeText(s string) error {
	in.mu.Lock()
	defer in.mu.Unlock()

	const batch = 128
	units := utf16Units(s)
	ev := make([]input, 0, batch)
	for _, u := range units {
		var down, up input
		down.unicodeChar(u, false)
		up.unicodeChar(u, true)
		ev = append(ev, down, up)
		if len(ev) >= batch {
			if _, err := send(ev); err != nil {
				return fmt.Errorf("remote: typing failed after partial input: %w", err)
			}
			ev = ev[:0]
		}
	}
	if len(ev) > 0 {
		if _, err := send(ev); err != nil {
			return err
		}
	}
	return nil
}

// utf16Units converts UTF-8 to UTF-16 code units. Characters outside the BMP
// become surrogate pairs, each injected separately, which is exactly what the
// UNICODE keystroke mechanism expects.
func utf16Units(s string) []uint16 {
	var out []uint16
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			out = append(out, uint16(r))
		}
	}
	return out
}

// Scroll injects wheel movement. Deltas are in notches: positive scrolls up.
func (in *Injector) Scroll(dx, dy int32) error {
	in.mu.Lock()
	defer in.mu.Unlock()

	ev := make([]input, 0, 2)
	if dy != 0 {
		ev = append(ev, newMouseInput(0, 0, uint32(dy*120), mouseWheel))
	}
	if dx != 0 {
		ev = append(ev, newMouseInput(0, 0, uint32(dx*120), mouseHWheel))
	}
	_, err := send(ev)
	return err
}

// mouseButton maps a protocol button name to its down/up flags.
func mouseButton(name string) (down, up uint32, ok bool) {
	switch low(name) {
	case "left", "lmb", "1":
		return mouseLeftDown, mouseLeftUp, true
	case "right", "rmb", "2":
		return mouseRightDown, mouseRightUp, true
	case "middle", "mmb", "3":
		return mouseMiddleDown, mouseMiddleUp, true
	}
	return 0, 0, false
}

// Click performs a full press-release of the named mouse button.
func (in *Injector) Click(name string) error {
	dn, up, ok := mouseButton(name)
	if !ok {
		return fmt.Errorf("remote: unknown mouse button %q", name)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	_, err := send([]input{
		newMouseInput(0, 0, 0, dn),
		newMouseInput(0, 0, 0, up),
	})
	return err
}

// ButtonDown presses a mouse button and keeps it held, which the touchpad
// drag gesture needs so a drag-select behaves like a real button hold.
func (in *Injector) ButtonDown(name string) error {
	dn, _, ok := mouseButton(name)
	if !ok {
		return fmt.Errorf("remote: unknown mouse button %q", name)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	_, err := send([]input{newMouseInput(0, 0, 0, dn)})
	return err
}

// ButtonUp releases a held mouse button.
func (in *Injector) ButtonUp(name string) error {
	_, up, ok := mouseButton(name)
	if !ok {
		return fmt.Errorf("remote: unknown mouse button %q", name)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	_, err := send([]input{newMouseInput(0, 0, 0, up)})
	return err
}

// ReleaseAll lifts every mouse button and modifier that might be held.
//
// This exists because a held button is a state machine that lives on the PC,
// not in the app. If the phone backgrounds mid-drag, the Wi-Fi drops, or the
// app is killed, the matching "button up" never arrives and the PC's left
// button stays down indefinitely. The visible symptom is that afterwards *every*
// cursor movement selects text on the PC, with nothing on screen to explain
// why - the user has no way to connect their swipe to a button they never saw
// go down.
//
// So the server releases on its own terms rather than trusting the client to be
// well behaved: on disconnect, and whenever a new client takes over the link.
// It is idempotent and safe to call at any time.
func (in *Injector) ReleaseAll() {
	in.mu.Lock()
	defer in.mu.Unlock()
	_, _ = send([]input{
		newMouseInput(0, 0, 0, mouseLeftUp),
		newMouseInput(0, 0, 0, mouseRightUp),
		newMouseInput(0, 0, 0, mouseMiddleUp),
	})
	// Modifiers are released for the same reason: a latched Ctrl turns the next
	// click into a shortcut the user did not ask for.
	for _, vk := range []uint16{vkShift, vkControl, vkMenu, vkLWin, vkRWin} {
		_, _ = send([]input{newKeyInput(vk, 0, keyEventExtended|keyEventKeyUp)})
	}
}

// MoveRelative moves the cursor by a pixel delta, which is how a touchpad
// drag becomes cursor motion. MOUSEEVENTF_MOVE without ABSOLUTE treats
// DX/DY as offsets from the current position, which works across monitors
// without needing to know the virtual desktop layout.
func (in *Injector) MoveRelative(dx, dy int32) error {
	if dx == 0 && dy == 0 {
		return nil
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	_, err := send([]input{newMouseInput(dx, dy, 0, mouseMove)})
	return err
}

// GDI bitmaps and go vet
// -----------------------
// go vet reports "possible misuse of unsafe.Pointer" on the GetSystemMetrics
// call below and on the DIB pixel slice in capture_windows.go. As with the COM
// vtable reads in winutil_windows.go there is no //nolint equivalent in the
// standard toolchain, so the warning is documented rather than silenced.
//
// Both are correct. CreateDIBSection hands back a pointer to memory the OS
// allocated, and GetSystemMetrics writes through a pointer to a Go array; in
// each case a uintptr must cross the syscall boundary and be converted back to
// a pointer. The unsafeptr check cannot distinguish that from the genuinely
// unsafe case it guards against - a Go heap pointer surviving a stack move -
// because the address belongs to neither the Go heap nor the stack.

// getSystemMetricsFn is a seam for tests: the real one talks to user32, and
// swapping it lets the fallback chain below be exercised without a display.
var getSystemMetricsFn = getSystemMetrics

// getSystemMetrics reads one SM_* metric.
//
// GetSystemMetrics takes an int and RETURNS an int; it never writes through a
// pointer. The previous version here passed a pointer to a local and then
// returned that local, discarding the call's actual return value - so it
// answered 0 for every index, whatever the display was doing. virtualDesktop()
// consequently never saw a real size, fell through to its 1920x1080 guess,
// and the capturer captured 1920x1080 no matter what resolution the desktop
// was actually at: the picture arrived pinned to the top-left corner with
// black padding down the right and bottom.
//
// Note that GetDC-relative metrics are a different API (GetDeviceCaps), which
// is why QueryDeviceInfo can read its return value the same way without ever
// having hit this.
func getSystemMetrics(index int) int32 {
	proc := user32.NewProc("GetSystemMetrics")
	v, _, _ := proc.Call(uintptr(index))
	return int32(v)
}

// virtualDesktopFn is the seam the capture path uses. It points at
// virtualDesktop by default and exists so a test can drive the resize logic
// with a desktop geometry it controls.
var virtualDesktopFn = virtualDesktop

// onMetricsFallback, when set, receives a message whenever virtualDesktop has
// to fall back to a guessed geometry.
var onMetricsFallback = func(string) {}

// SetMetricsWarningLogger routes display-metric fallbacks into the server log.
// Without it a capture running on a guessed size is silent, which is exactly
// the case nobody can diagnose later.
func SetMetricsWarningLogger(fn func(string)) {
	if fn != nil {
		onMetricsFallback = fn
	}
}

// virtualDesktop returns the virtual desktop bounds in pixels.
//
// Some sessions - a headless service, a locked console, a display driver that
// has not finished enumerating - report a zero virtual screen. Guessing 1920x1080
// there produced captures that were silently the wrong size, so the primary
// display is consulted first: it is the real size in nearly every case where
// the virtual screen is missing. Only when that also fails do we guess.
func virtualDesktop() (ox, oy, w, h int32) {
	ox = getSystemMetricsFn(76) // SM_XVIRTUALSCREEN
	oy = getSystemMetricsFn(77) // SM_YVIRTUALSCREEN
	w = getSystemMetricsFn(78)  // SM_CXVIRTUALSCREEN
	h = getSystemMetricsFn(79)  // SM_CYVIRTUALSCREEN
	if w > 0 && h > 0 {
		return ox, oy, w, h
	}

	if sw, sh := getSystemMetricsFn(0), getSystemMetricsFn(1); sw > 0 && sh > 0 { // SM_CXSCREEN, SM_CYSCREEN
		onMetricsFallback(fmt.Sprintf(
			"virtual screen unavailable, falling back to the primary display %dx%d", sw, sh))
		return 0, 0, sw, sh
	}

	onMetricsFallback("no display metrics available, assuming 1920x1080")
	return 0, 0, 1920, 1080
}

// MoveAbsolute moves the cursor to a pixel coordinate in the virtual desktop
// origin space returned by VirtualDesktop.
//
// SendInput requires absolute coordinates normalised to 0..65535 across the
// virtual desktop. The +32767 bias is the rounding documented for
// MOUSEEVENTF_ABSOLUTE so that pixel 0 and the far edge are both reachable.
func (in *Injector) MoveAbsolute(x, y int32) error {
	ox, oy, w, h := virtualDesktop()
	nx := (int64(x-ox)*65535)/int64(w-1) + 32767
	ny := (int64(y-oy)*65535)/int64(h-1) + 32767
	clamp := func(v int64) int32 {
		if v < 0 {
			return 0
		}
		if v > 65535 {
			return 65535
		}
		return int32(v)
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	_, err := send([]input{
		newMouseInput(clamp(nx), clamp(ny), 0,
			mouseMove|mouseAbsolute|mouseVirtualDesk),
	})
	return err
}

// CircuitBreaker trips after repeated SendInput failures.
//
// Without it, a blocked process (UIPI, or a session with no interactive
// desktop) swallows every input silently, and the user sees a remote control
// that appears to work but types nothing.
type CircuitBreaker struct {
	mu        sync.Mutex
	failures  int
	tripped   bool
	threshold int
	cooldown  time.Duration
	lastTrip  time.Time
	diagnosis string
	onTrip    func(diagnosis string)
}

// NewCircuitBreaker returns a breaker that trips after threshold consecutive
// failures and auto-resets after cooldown.
func NewCircuitBreaker(threshold int, cooldown time.Duration, onTrip func(string)) *CircuitBreaker {
	return &CircuitBreaker{threshold: threshold, cooldown: cooldown, onTrip: onTrip}
}

// Record reports the outcome of one injection and reports whether input
// should currently be suppressed. It never trips without logging why.
func (b *CircuitBreaker) Record(err error) (blocked bool, diagnosis string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err == nil {
		b.failures = 0
		if b.tripped && time.Since(b.lastTrip) > b.cooldown {
			b.tripped = false
		}
		return b.tripped, b.diagnosis
	}

	b.failures++
	if b.tripped || b.failures < b.threshold {
		return b.tripped, b.diagnosis
	}

	b.tripped = true
	b.lastTrip = time.Now()
	b.diagnoseLocked(err)
	if b.onTrip != nil {
		go b.onTrip(b.diagnosis)
	}
	return true, b.diagnosis
}

// diagnoseLocked builds a human-readable explanation of why injection is
// blocked. UIPI (User Interface Privilege Isolation) is by far the most
// common cause: the foreground app runs elevated and this server does not,
// so Windows silently discards the lower-integrity input.
func (b *CircuitBreaker) diagnoseLocked(err error) {
	code := lastError()
	switch {
	case code == errorAccessDenied:
		b.diagnosis = fmt.Sprintf(
			"UIPI/elevation block: SendInput failed with ERROR_ACCESS_DENIED (%d) %d times "+
				"in a row. Windows blocks input from a lower-integrity process to a higher-"+
				"integrity window. Close elevated apps, or relaunch the Smart Remote server as "+
				"Administrator so its integrity level matches.", code, b.failures)
	case code != 0:
		b.diagnosis = fmt.Sprintf(
			"SendInput failing repeatedly, last Win32 error %d. Check for another input hook, "+
				"a Remote Desktop session, or a Remote Access Control lock.", code)
	default:
		b.diagnosis = fmt.Sprintf(
			"SendInput reported failure %d times in a row (%v). Usually no interactive desktop "+
				"is attached, or the session is locked or disconnected.", b.failures, err)
	}
}

// Diagnosis returns the current diagnosis string, if any.
func (b *CircuitBreaker) Diagnosis() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.diagnosis
}

// Tripped reports whether the breaker is currently suppressing input.
func (b *CircuitBreaker) Tripped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped
}

func lastError() uint32 {
	r, _, _ := procGetLastError.Call()
	return uint32(r)
}

// low lowercases an ASCII string without allocating a lowercase copy of the
// whole message for keys, which are short and compared constantly.
func low(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
