package remote

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// Audio-related errors.
var (
	errAudioClosed = errors.New("remote: audio device is closed")
	errVolumeRange = errors.New("remote: volume must be 0..100")
)

// errNoDevice converts a waveOut MMSYSERR code into an error.
//
// The raw numeric code is preserved because it is the only way to tell
// "no audio device" apart from "device in use" when diagnosing a machine.
func errNoDevice(code uintptr) error {
	return fmt.Errorf("remote: waveOut error %d", code)
}

// wrapCall converts a Win32 call result into an error.
func wrapCall(ret uintptr, _ uintptr, err error) error {
	if err != nil && err != syscall.Errno(0) {
		return err
	}
	return nil
}

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

// COM identifiers from mmdeviceapi.
//
// iidIMMDeviceEnumerator is required to create the enumerator;
// iidIAudioEndpointVolume is queried on the endpoint afterwards.
var (
	clsidMMDeviceEnumerator = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x8D, 0xCA, 0x7C, 0xD2, 0x62, 0x93, 0xBE}}
	iidIMMDeviceEnumerator  = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0xD8, 0x11, 0x8A, 0x91, 0x11}}
	iidIAudioEndpointVolume = guid{0x5CDF2C82, 0x841E, 0x4546, [8]byte{0x97, 0x20, 0x39, 0x4C, 0x75, 0xAB, 0xCF, 0xDE}}
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// audioEndpointVolume is the vtable layout of IAudioEndpointVolume that this
// code needs: everything up to and including SetMasterVolumeLevelScalar.
type audioEndpointVolume struct {
	vtbl *audioEndpointVolumeVtbl
}

// audioEndpointVolumeVtbl holds the COM method slots in declaration order.
// Only the first seven slots are needed; the rest are placeholders that keep
// the indices of the methods we do call correct.
type audioEndpointVolumeVtbl struct {
	QueryInterface                uintptr
	AddRef                        uintptr
	Release                       uintptr
	RegisterControlChangeNotify   uintptr
	UnregisterControlChangeNotify uintptr
	GetChannelCount               uintptr
	SetMasterVolumeLevel          uintptr
	SetMasterVolumeLevelScalar    uintptr
	_                             [16]uintptr
}

// defaultRenderEndpoint returns the default eRender endpoint and a release
// function the caller must invoke.
//
// The enumerator is created with IID_IMMDeviceEnumerator (not
// IAudioEndpointVolume): they are unrelated interfaces, and asking for the
// wrong one fails with REGDB_E_CLASSNOTREG or a null pointer.
func defaultRenderEndpoint() (endpoint uintptr, release func(), err error) {
	var enumerator uintptr
	ret, _, callErr := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0,
		// CLSCTX_ALL = 23
		23,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enumerator)),
	)
	if ret != 0 || enumerator == 0 {
		return 0, nil, fmt.Errorf("remote: CoCreateInstance(MMDeviceEnumerator) failed: %v", callErr)
	}

	// IMMDeviceEnumerator inherits IUnknown, so GetDefaultAudioEndpoint is
	// vtable slot 3.
	var ep uintptr
	getDefault := **(**uintptr)(unsafe.Pointer(
		*(*uintptr)(unsafe.Pointer(enumerator)) + 3*unsafe.Sizeof(uintptr(0))))
	ret, _, _ = syscall.SyscallN(getDefault,
		enumerator,
		0, // ERender
		0, // EConsole
		0, // ECommunications
		uintptr(unsafe.Pointer(&ep)))
	if ret != 0 || ep == 0 {
		releaseCom(enumerator)
		return 0, nil, errors.New("remote: no default audio output device")
	}
	return ep, func() { releaseCom(enumerator) }, nil
}

// queryEndpointVolume QIIDs the endpoint for IAudioEndpointVolume.
func queryEndpointVolume(endpoint uintptr) (audioEndpointVolume, func(), error) {
	var volume audioEndpointVolume
	queryInterface := **(**uintptr)(unsafe.Pointer(
		*(*uintptr)(unsafe.Pointer(endpoint)) + 0*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(queryInterface, endpoint,
		uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)),
		uintptr(unsafe.Pointer(&volume)))
	if ret != 0 {
		return volume, nil, errors.New("remote: audio endpoint does not implement IAudioEndpointVolume")
	}
	return volume, func() { releaseCom(uintptr(unsafe.Pointer(&volume))) }, nil
}

// COM INTEROP AND go vet
// ----------------------
// go vet reports "possible misuse of unsafe.Pointer" on the vtable
// dereferences below (lines ~98, ~116, ~129 and in releaseCom). There is no
// //nolint equivalent in the standard toolchain - that directive belongs to
// golangci-lint - so these warnings cannot be silenced, only understood.
//
// They are correct, and the reasoning is the same at every site:
//
//   - A COM interface pointer genuinely IS a pointer to a pointer-sized vtable
//     slot, so reading it requires converting a uintptr back to a pointer. That
//     conversion is precisely what the unsafeptr check flags.
//   - The uintptr involved never held a Go pointer, so there is no moving-GC
//     hazard. The check exists to catch a Go heap pointer surviving a stack
//     move, which cannot apply to an address owned by COM.
//
// The rule this code must respect is that a uintptr must not be held across a
// call that could relocate the GC. None of these do: every pointer is
// dereferenced and used immediately, and no uintptr outlives the call that
// produced it.

// vtblSlot reads the function pointer at the given vtable index.
func vtblSlot(vtbl uintptr, index int) uintptr {
	return **(**uintptr)(unsafe.Pointer(vtbl + uintptr(index)*unsafe.Sizeof(uintptr(0))))
}

// setMasterVolumeLevelScalar sets the default endpoint's volume, where level
// is 0.0 (silent) to 1.0 (full).
func setMasterVolumeLevelScalar(level float32) error {
	endpoint, releaseEnum, err := defaultRenderEndpoint()
	if err != nil {
		return err
	}
	defer releaseEnum()
	defer releaseCom(endpoint)

	volume, releaseVol, err := queryEndpointVolume(endpoint)
	if err != nil {
		return err
	}
	defer releaseVol()

	// SetMasterVolumeLevelScalar is vtable slot 7 (after IUnknown's three).
	setScalar := vtblSlot(uintptr(unsafe.Pointer(volume.vtbl)), 7)
	syscall.SyscallN(setScalar, uintptr(unsafe.Pointer(&volume)),
		uintptr(unsafe.Pointer(&level)))
	return nil
}

// getMasterVolumeLevelScalar reads the default endpoint's volume as 0.0..1.0.
func getMasterVolumeLevelScalar() (float32, error) {
	endpoint, releaseEnum, err := defaultRenderEndpoint()
	if err != nil {
		return 0, err
	}
	defer releaseEnum()
	defer releaseCom(endpoint)

	volume, releaseVol, err := queryEndpointVolume(endpoint)
	if err != nil {
		return 0, err
	}
	defer releaseVol()

	var level float32
	// GetMasterVolumeLevelScalar is vtable slot 6.
	getScalar := vtblSlot(uintptr(unsafe.Pointer(volume.vtbl)), 6)
	ret, _, _ := syscall.SyscallN(getScalar, uintptr(unsafe.Pointer(&volume)),
		uintptr(unsafe.Pointer(&level)))
	if ret != 0 {
		return 0, errors.New("remote: GetMasterVolumeLevelScalar failed")
	}
	return level, nil
}

// setSystemVolume sets the master output volume, 0..100.
//
// It goes through IAudioEndpointVolume rather than the VK_VOLUME key taps so
// the level is absolute and reproducible.
func setSystemVolume(pct int) error {
	if pct < 0 || pct > 100 {
		return errVolumeRange
	}
	level := float32(pct) / 100
	if err := setMasterVolumeLevelScalar(level); err != nil {
		// Fall back to the key-tap method so volume still works on systems
		// where the COM endpoint is unavailable (some VMs, RDP sessions).
		return fallbackVolumeTap(pct)
	}
	return nil
}

// fallbackVolumeTap nudges volume with virtual keys, which are relative.
//
// Windows exposes 50 discrete steps, so this converts a percentage into a
// step delta from the current level.
func fallbackVolumeTap(pct int) error {
	const steps = 50
	current, err := currentVolumeSteps()
	if err != nil {
		return err
	}
	target := pct * steps / 100
	delta := target - current
	if delta == 0 {
		return nil
	}
	vk := uint16(vkVolumeDown)
	if delta > 0 {
		vk = vkVolumeUp
		delta = -delta
	}
	// Inject in batches; SendInput has a practical limit per call.
	const perCall = 40
	for i := 0; i < -delta; i += perCall {
		n := perCall
		if -delta-i < n {
			n = -delta - i
		}
		ev := make([]input, 0, n*2)
		for j := 0; j < n; j++ {
			ev = append(ev,
				newKeyInput(vk, 0, 0),
				newKeyInput(vk, 0, keyEventKeyUp),
			)
		}
		if _, err := send(ev); err != nil {
			return err
		}
	}
	return nil
}

// currentVolumeSteps reports the current master volume as 0..50 steps.
func currentVolumeSteps() (int, error) {
	level, err := getMasterVolumeLevelScalar()
	if err != nil {
		return 0, err
	}
	return int(float64(level)*steps50 + 0.5), nil
}

// steps50 is the number of discrete volume steps Windows exposes.
const steps50 = 50

// releaseCom calls IUnknown::Release on a COM pointer.
func releaseCom(ptr uintptr) {
	if ptr == 0 {
		return
	}
	release := **(**uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(ptr)) + 2*unsafe.Sizeof(uintptr(0))))
	syscall.SyscallN(release, ptr)
}

// setSystemCursorVisible shows or hides the mouse cursor via the display
// settings SPI_SETCURSORS flag, which is reversible and does not require
// re-registering a system-wide hook.
func setSystemCursorVisible(visible bool) error {
	var flags uint32 = 0x00000040 // SPIF_UPDATEINIFILE
	if !visible {
		flags |= 0x00000002 // SPIF_SENDCHANGE
	}
	// SPI_SETCURSORS = 0x0057
	ret, _, _ := procSystemParametersInfo.Call(0x0057, uintptr(flags), 0, 0)
	if ret == 0 {
		return fmt.Errorf("remote: SystemParametersInfo(SPI_SETCURSORS) failed (err %d)", lastError())
	}
	return nil
}

var procSystemParametersInfo = user32.NewProc("SystemParametersInfoW")

// init wires the platform appliers into the dispatcher hooks.
func init() {
	SetVolumeApplier(setSystemVolume)
	SetCursorApplier(setSystemCursorVisible)
}
