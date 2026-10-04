//go:build windows

package remote

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	d3d11Lib                 = syscall.NewLazyDLL("d3d11.dll")
	dxgiLib                  = syscall.NewLazyDLL("dxgi.dll")
	procD3D11CreateDevice    = d3d11Lib.NewProc("D3D11CreateDevice")
	procCreateDXGIFactory1   = dxgiLib.NewProc("CreateDXGIFactory1")
	procProcessIdToSessionID = kernel32.NewProc("ProcessIdToSessionId")
	procGetCurrentProcessID  = kernel32.NewProc("GetCurrentProcessId")
)

// ScreenProbe reports what the capture stack can actually see right now.
//
// It exists because "the screen viewer shows nothing" is not a diagnosis. GDI
// fails for several unrelated reasons - no interactive desktop, session 0, an
// integrity-level mismatch, an exhausted handle table - and each calls for a
// different fix. Guessing costs a rebuild per hypothesis, so this answers the
// question directly: can a frame actually be captured here, and is DXGI
// available as an alternative?
type ScreenProbe struct {
	// Success is true only when a real frame was captured and encoded.
	Success bool `json:"success"`

	// Method names the capture path exercised.
	Method string `json:"method"`

	// Error is the failure message, or "" on success.
	Error string `json:"error,omitempty"`

	// Win32Error is the numeric Win32 code behind a failure, or 0.
	Win32Error uint32 `json:"win32Error,omitempty"`

	// SessionID and Interactive distinguish the most common cause of "BitBlt
	// always fails": a server running outside a real user session.
	SessionID   uint32 `json:"sessionId"`
	Interactive bool   `json:"interactiveSession"`

	// GDI reports the existing BitBlt path.
	GDI ProbeGDI `json:"gdi"`

	// DXGI reports whether Desktop Duplication is reachable, which is the
	// modern path and the one worth moving to when GDI fails.
	DXGI ProbeDXGI `json:"dxgi"`

	// Suggestion is the actionable conclusion.
	Suggestion string `json:"suggestion"`
}

// ProbeGDI is the result of one trial desktop capture through GDI.
type ProbeGDI struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Width   int32  `json:"width"`
	Height  int32  `json:"height"`
	Bytes   int    `json:"jpegBytes,omitempty"`
	Elapsed string `json:"elapsed,omitempty"`
}

// ProbeDXGI is the result of trying to create a D3D11 device and a DXGI
// factory.
//
// These are HRESULTs, not Win32 error codes, so HResult is kept separate:
// conflating the two produces plausible but meaningless diagnoses.
type ProbeDXGI struct {
	// DeviceOK means D3D11CreateDevice succeeded, so the GPU is reachable.
	DeviceOK bool `json:"deviceOk"`

	// DeviceError explains why the device or factory could not be created.
	DeviceError string `json:"deviceError,omitempty"`

	// HResult is the raw DXGI/D3D result code.
	HResult int32 `json:"hresult,omitempty"`

	// Available means the GPU path is usable: a device exists AND a DXGI
	// factory can be created.
	Available bool `json:"available"`
}

// ProbeScreenCapture runs a single trial capture and reports the outcome.
//
// The GDI path is exercised for real rather than simulated, so the answer
// reflects this machine, this session and this process's integrity level -
// the only thing that actually distinguishes the competing explanations.
func ProbeScreenCapture() ScreenProbe {
	p := ScreenProbe{Method: "GDI"}
	p.SessionID = currentSessionID()
	p.Interactive = p.SessionID != 0 && p.SessionID != 0xFFFFFFFF

	_, _, w, h := virtualDesktop()
	start := time.Now()

	cap, err := NewCapturer(CapturerOptions{Quality: 70})
	if err != nil {
		return p.failed(err)
	}
	defer cap.Close()

	p.GDI.Width, p.GDI.Height = w, h

	frame, err := cap.Capture()
	if err != nil {
		return p.failed(err)
	}

	p.Success = true
	p.GDI.OK = true
	p.GDI.Bytes = len(frame)
	p.GDI.Elapsed = time.Since(start).Round(time.Millisecond).String()
	p.Suggestion = "GDI capture works here. If the phone still shows nothing, " +
		"the fault is in the MJPEG transport or the client parser, not capture."
	p.DXGI = probeDXGI()
	return p
}

// failed fills in every failure field, including the DXGI fallback probe.
func (p *ScreenProbe) failed(err error) ScreenProbe {
	p.Error = err.Error()
	p.Win32Error = lastError()
	p.GDI.Error = err.Error()
	p.DXGI = probeDXGI()
	p.Suggestion = gdiSuggestion(p)
	return *p
}

// gdiSuggestion turns a failure into a concrete next step.
func gdiSuggestion(p *ScreenProbe) string {
	if !p.Interactive {
		return "This process is NOT in an interactive session (session " +
			itoa32(p.SessionID) + "). A non-interactive session has no visible " +
			"desktop to capture and no user-mode capture API will work - not " +
			"GDI, not DXGI. Run the server inside the logged-on user's session " +
			"(a tray app), not as a Windows service."
	}
	if p.Win32Error == errorAccessDenied {
		return "BitBlt was denied (ERROR_ACCESS_DENIED). The server runs at a " +
			"lower integrity level than the desktop it is capturing. Run it at " +
			"the same or higher integrity level, or as administrator."
	}
	if p.GDI.Error != "" && strings.Contains(p.GDI.Error, "win32 error 0") {
		return "BitBlt failed without setting a Win32 error, which usually " +
			"means the arguments were invalid rather than the desktop being " +
			"unavailable - check the BitBlt call's parameter order against " +
			"the Win32 signature."
	}
	if p.DXGI.Available {
		return "GDI capture failed but a DXGI device IS available. Port the " +
			"capture path to Desktop Duplication."
	}
	if strings.Contains(p.GDI.Error, "CreateDIBSection") ||
		strings.Contains(p.GDI.Error, "CreateCompatibleDC") {
		return "GDI device creation failed (" + p.GDI.Error + "). Usually GDI " +
			"handle exhaustion from a long-running process; restarting the PC " +
			"clears leaked handles."
	}
	if p.DXGI.DeviceError != "" {
		return "Neither GDI nor DXGI can capture here (" + p.DXGI.DeviceError +
			"). A locked, secure or disconnected desktop blocks both paths."
	}
	return "GDI capture failed. See error and win32Error above."
}

// probeDXGI reports whether a D3D11 device and a DXGI factory are obtainable.
//
// A successful D3D11CreateDevice is the prerequisite for Desktop Duplication,
// so this answers "is the DXGI route even open on this machine?" without having
// to port the capture code to find out.
func probeDXGI() ProbeDXGI {
	var d ProbeDXGI

	// A null pAdapter selects the default adapter and a null context is valid
	// when only device support is being probed - the documented way to ask
	// "can this GPU make a D3D11 device?" without keeping one around.
	var device, context uintptr
	hr, _, _ := procD3D11CreateDevice.Call(
		0,      // pAdapter: NULL = default adapter
		1,      // D3D_DRIVER_TYPE_HARDWARE
		0,      // D3D_DRIVER_TYPE_SOFTWARE
		0,      // Flags
		0xB100, // D3D_FEATURE_LEVEL_11_0
		0xB000, // D3D_FEATURE_LEVEL_10_0
		0,      // FeatureLevel (out)
		0,      // SDKVersion (out)
		uintptr(unsafe.Pointer(&device)),
		0, // pContext: NULL
		uintptr(unsafe.Pointer(&context)),
	)
	if int32(hr) != 0 {
		d.DeviceError = fmt.Sprintf("D3D11CreateDevice failed: hr=0x%08X", int32(hr))
		d.HResult = int32(hr)
		return d
	}
	d.DeviceOK = true

	// With a device present, a DXGI factory should be obtainable. If this
	// fails the GPU route is unusable even though the device was created.
	var factory uintptr
	hr, _, _ = procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&factory)))
	if int32(hr) != 0 {
		d.DeviceError = fmt.Sprintf("CreateDXGIFactory1 failed: hr=0x%08X", int32(hr))
		d.HResult = int32(hr)
		return d
	}

	d.Available = true
	return d
}

// currentSessionID returns the Windows session this process runs in.
//
// Session 0 is the isolated services session: a process there can synthesise
// input but has no visible desktop, which is the most common reason a screen
// capture works while developing and fails once installed as a service.
func currentSessionID() uint32 {
	var sid uint32
	ret, _, _ := procProcessIdToSessionID.Call(
		currentProcessID(),
		uintptr(unsafe.Pointer(&sid)),
	)
	if ret == 0 {
		return 0xFFFFFFFF
	}
	return sid
}

func currentProcessID() uintptr {
	pid, _, _ := procGetCurrentProcessID.Call()
	return pid
}

// itoa32 avoids importing strconv for a single diagnostic string.
func itoa32(n uint32) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
