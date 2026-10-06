//go:build windows

package remote

// WHY MEDIA FOUNDATION AND NOT VFW
// ----------------------------------
// Video for Windows (avicap32.dll) never delivers frames on Intel UHD 620/630
// built-in cameras: the driver exposes itself exclusively through Windows Media
// Foundation (MSMF). Every VFW path produced solid-black 14 KB dummy frames or
// wedged the capture thread inside SendMessage for 30–95 s.
//
// The fix is IMFSourceReader, called through syscall against the three DLLs
// Windows ships with (mfplat.dll, mfreadwrite.dll, mf.dll). No CGO required.
//
// OUTPUT FORMAT
// -------------
// The sensor rarely hands over JPEG. Plenty of UVC webcams — including the HP
// and Intel units this app targets — expose YUY2 only, so a pipeline that
// assumes MJPEG gets nothing at all. The negotiator therefore asks for MJPEG
// first and falls back to YUY2, converting the frame in-process.
//
// COM VTABLE SLOT RULES — READ BEFORE EDITING
// --------------------------------------------
// Every COM interface appends its own methods after those it inherits, in
// declaration order. Two consequences:
//
//   - A wrong slot index does not return an error HRESULT. It calls a
//     *different* method with the wrong signature, which is how this file
//     used to die with 0xc0000005. Always count the slots from the header.
//   - IMFAttributes has 30 own methods after IUnknown's 3, so any interface
//     inheriting it starts its own methods at slot 33.
//
//   IUnknown (0–2):      QueryInterface, AddRef, Release
//   IMFAttributes (3–32): GetItem…CopyAllItems (see the constants below)
//   IMFActivate (33–35):  ActivateObject, ShutdownObject, DetachObject
//   IMFSample (33–…):     GetSampleFlags…ConvertToContiguousBuffer(=41)
//
// IMFSourceReader inherits only IUnknown, so its methods start at slot 3, but
// the first one is not the same on every SDK — so those slots are resolved at
// runtime from a live reader in sourceReaderSlotsFor.
//
// Frame path:
//   MFStartup (once per process) → CoInitializeEx(MTA) per OS thread
//   → MFEnumDeviceSources → IMFActivate::ActivateObject(slot 33)
//   → IMFMediaSource → MFCreateSourceReaderFromMediaSource → IMFSourceReader
//   → ShutdownObject on the activate object (only now that the reader exists)
//   → SetCurrentMediaType(MJPEG or YUY2, 1280×720, fallbacks)
//   → ReadSample → IMFSample → ConvertToContiguousBuffer(slot 41)
//   → IMFMediaBuffer::Lock → bytes copied out → Unlock

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ErrNoWebcam reports that no usable camera could be found or opened.
var ErrNoWebcam = errors.New("no camera found")

// ── DLL / proc table ─────────────────────────────────────────────────────────

var (
	mfplat      = syscall.NewLazyDLL("mfplat.dll")
	mfreadwrite = syscall.NewLazyDLL("mfreadwrite.dll")
	mf          = syscall.NewLazyDLL("mf.dll")

	procMFStartup                           = mfplat.NewProc("MFStartup")
	procMFCreateAttributes                  = mfplat.NewProc("MFCreateAttributes")
	procMFEnumDeviceSources                 = mf.NewProc("MFEnumDeviceSources")
	procMFCreateSourceReaderFromMediaSource = mfreadwrite.NewProc("MFCreateSourceReaderFromMediaSource")
	procMFCreateMediaType                   = mfplat.NewProc("MFCreateMediaType")
	procCoInitializeEx                      = ole32.NewProc("CoInitializeEx")
	procCoUninitialize                      = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree                       = ole32.NewProc("CoTaskMemFree")
)

// MFStartup flags.
const (
	mfVersion         = 0x0002_0070 // MF_VERSION for Vista/Win7+
	mfStartupNoSocket = 0x1
	coInitMTA         = 0x0 // COINIT_MULTITHREADED (MF requires MTA)
)

// ── MSMF / COM GUIDs ─────────────────────────────────────────────────────────
// (guid struct and ole32 DLL are declared in winutil_windows.go)

var (
	// MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE
	mfDevSourceAttrSourceType = guid{0xC60AC5FE, 0x252A, 0x478F, [8]byte{0xA0, 0xEF, 0xBC, 0x8F, 0xA5, 0xF7, 0xCA, 0xD3}}
	// MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID
	mfDevSourceAttrSourceTypeVidcap = guid{0x8AC3587A, 0x4AE7, 0x42D8, [8]byte{0x99, 0xE0, 0x0A, 0x60, 0x13, 0xEE, 0xF9, 0x0F}}
	// MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME
	mfDevSourceAttrFriendlyName = guid{0x60D0E559, 0x52F8, 0x4FA2, [8]byte{0xBB, 0xCE, 0xAC, 0xDB, 0x34, 0xA8, 0xEC, 0x01}}

	// MFMediaType_Video
	mfMediaTypeVideo = guid{0x73646976, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	// MFVideoFormat_MJPG (FourCC 'MJPG' → 0x47504A4D in little-endian Data1)
	mfVideoFormatMJPG = guid{0x47504A4D, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	// MFVideoFormat_YUY2 (FourCC 'YUY2' → 0x32595559 in little-endian Data1)
	mfVideoFormatYUY2 = guid{0x32595559, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}

	// MF_MT_MAJOR_TYPE
	mfMtMajorType = guid{0x48eba18e, 0xf8c9, 0x4687, [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	// MF_MT_SUBTYPE
	mfMtSubtype = guid{0xf7e34c9a, 0x42e8, 0x4714, [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	// MF_MT_FRAME_SIZE: upper 32 bits = width, lower 32 bits = height
	mfMtFrameSize = guid{0x1652c33d, 0xd6b2, 0x4012, [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d}}

	// IID_IMFMediaSource – needed for IMFActivate::ActivateObject
	iidIMFMediaSource = guid{0x279A808D, 0xAEC7, 0x40C8, [8]byte{0x9C, 0x6B, 0xA6, 0xB4, 0x92, 0xC7, 0x8A, 0x66}}
)

// ── COM vtable slot indices ───────────────────────────────────────────────────
//
// Counted from the declaration order of mfobjects.h / mfreadwrite.h. A wrong
// index here silently calls the wrong method with the wrong signature; it does
// not produce an error HRESULT.
const (
	// IMFAttributes (inherits IUnknown: slots 0–2), own methods start at 3:
	//  3 GetItem           19 DeleteItem
	//  4 GetItemType       20 DeleteAllItems
	//  5 CompareItem       21 SetUINT32
	//  6 Compare           22 SetUINT64
	//  7 GetUINT32         23 SetDouble
	//  8 GetUINT64         24 SetGUID
	//  9 GetDouble         25 SetString
	// 10 GetGUID           26 SetBlob
	// 11 GetStringLength   27 SetUnknown
	// 12 GetString         28 LockStore
	// 13 GetAllocatedString
	// 14 GetBlobSize
	// 15 GetBlob
	// 16 GetAllocatedBlob
	// 17 GetUnknown
	// 18 SetItem
	vGetUINT32       uintptr = 7
	vGetUINT64       uintptr = 8
	vGetGUID         uintptr = 10
	vGetStringLength uintptr = 11
	vGetString       uintptr = 12
	vSetUINT32       uintptr = 21
	vSetUINT64       uintptr = 22
	vSetGUID         uintptr = 24

	// vAttrSlotCount is IMFAttributes' total vtable length (IUnknown's 3 plus
	// its own 30). Every interface inheriting IMFAttributes starts its own
	// methods here, so it is the single source of truth for those offsets.
	vAttrSlotCount uintptr = 33

	// IMFActivate (inherits full IMFAttributes), own methods at 33.
	vActivateObject uintptr = vAttrSlotCount
	vShutdownObject uintptr = vAttrSlotCount + 1

	// IMFSample (inherits full IMFAttributes), own methods start at 33:
	// 33=GetSampleFlags, 34=SetSampleFlags, 35=GetSampleTime, 36=SetSampleTime,
	// 37=GetSampleDuration, 38=SetSampleDuration, 39=GetBufferCount,
	// 40=GetBufferByIndex, 41=ConvertToContiguousBuffer
	vSampleConvertToContiguousBuffer uintptr = vAttrSlotCount + 8

	// IMFMediaBuffer (inherits only IUnknown 0–2), own methods start at 3:
	vBufLock             uintptr = 3
	vBufUnlock           uintptr = 4
	vBufGetCurrentLength uintptr = 5
)

// IMFSourceReader slot offsets, relative to the layout this build exposes.
//
// IMFSourceReader inherits IUnknown only, so its own methods start at 3 — but
// the *first* one is not identical on every SDK: GetSource is absent from the
// Vista-era layout, which shifts every following method down by one. Rather
// than hard-coding a guess that breaks on one of the two, the layout is probed
// once from a live reader in sourceReaderSlotsFor.
const (
	srVistaSetStreamSel   uintptr = 4 // Vista layout: SetStreamSelection
	srVistaGetCurrentType uintptr = 6 // Vista layout: GetCurrentMediaType
	srVistaSetCurrentType uintptr = 7 // Vista layout: SetCurrentMediaType
	srVistaReadSample     uintptr = 9 // Vista layout: ReadSample
	srGetSourceSlot       uintptr = 3 // GetSource, present only on newer SDKs
)

// srProbeSentinel marks the probe's out-parameter as "not written yet".
const srProbeSentinel uintptr = 0xDEADBEEF

// sourceReaderSlots holds the resolved IMFSourceReader vtable offsets.
type sourceReaderSlots struct {
	setStreamSelection  uintptr
	getNativeMediaType  uintptr
	getCurrentMediaType uintptr
	setCurrentMediaType uintptr
	readSample          uintptr
}

var (
	srSlotOnce  sync.Once
	srSlotCache sourceReaderSlots
)

// sourceReaderSlotsFor resolves and caches the IMFSourceReader layout.
//
// The probe calls slot 3 with (IID_IUnknown, &out). If slot 3 is GetSource,
// QueryInterface succeeds and writes a genuine COM pointer into out. If slot 3
// is the Vista-era GetStreamSelection, the bogus "stream index" argument makes
// the call fail with MF_E_INVALIDREQUEST and out is never touched. Either way
// nothing is dereferenced that this code did not hand the callee, so the probe
// cannot fault.
func sourceReaderSlotsFor(reader comObj) sourceReaderSlots {
	srSlotOnce.Do(func() {
		vista := sourceReaderSlots{
			setStreamSelection:  srVistaSetStreamSel,
			getCurrentMediaType: srVistaGetCurrentType,
			setCurrentMediaType: srVistaSetCurrentType,
			readSample:          srVistaReadSample,
		}
		vista.getNativeMediaType = srVistaGetCurrentType - 1
		srSlotCache = vista

		var out uintptr = srProbeSentinel
		iid := guid{Data1: 0x00000000, Data2: 0x0000, Data3: 0x0000,
			Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
		hr, _, _ := reader.vtableCall(srGetSourceSlot,
			uintptr(unsafe.Pointer(&iid)),
			uintptr(unsafe.Pointer(&out)))

		if hresult(hr) == nil && out != 0 && out != 1 && out != srProbeSentinel {
			// A real interface pointer came back, so slot 3 is GetSource and
			// every method after it sits one slot further along.
			comObj(out).Release()
			withGetSource := sourceReaderSlots{
				setStreamSelection:  vista.setStreamSelection + 1,
				getCurrentMediaType: vista.getCurrentMediaType + 1,
				setCurrentMediaType: vista.setCurrentMediaType + 1,
				readSample:          vista.readSample + 1,
			}
			withGetSource.getNativeMediaType = withGetSource.getCurrentMediaType - 1
			srSlotCache = withGetSource
		}
	})
	return srSlotCache
}

// ── comObj: a typed COM interface pointer ─────────────────────────────────────

// comObj is a COM interface pointer (the value you'd hold as IMFxxx* in C++).
// In memory, a COM object starts with a pointer to its vtable.
type comObj uintptr

// vtableCall invokes the COM method at vtable slot index.
// Returns an error if the receiver is nil (prevents access violations).
func (o comObj) vtableCall(index uintptr, args ...uintptr) (r1, r2 uintptr, err error) {
	if o == 0 {
		return 0, 0, fmt.Errorf("COM vtable call on nil interface (slot %d)", index)
	}
	// o points to the COM object; first field is the vtable pointer.
	vtable := *(*uintptr)(unsafe.Pointer(o))
	if vtable == 0 {
		return 0, 0, fmt.Errorf("COM object has null vtable (slot %d)", index)
	}
	// Each vtable entry is one pointer wide (8 bytes on amd64).
	fn := *(*uintptr)(unsafe.Pointer(vtable + index*8))
	if fn == 0 {
		return 0, 0, fmt.Errorf("COM vtable slot %d is null", index)
	}
	all := make([]uintptr, 0, 1+len(args))
	all = append(all, uintptr(o)) // "this" pointer is always first
	all = append(all, args...)
	r1, r2, _ = syscall.SyscallN(fn, all...)
	return r1, r2, nil
}

// Release calls IUnknown::Release (slot 2). Safe to call on a zero comObj.
func (o comObj) Release() {
	if o != 0 {
		_, _, _ = o.vtableCall(2)
	}
}

// ── HRESULT helper ────────────────────────────────────────────────────────────

// hresult converts a Windows HRESULT to a Go error.
// HRESULT is signed; negative values indicate failure.
func hresult(r uintptr) error {
	if int32(r) >= 0 {
		return nil // S_OK or S_FALSE
	}
	return fmt.Errorf("HRESULT 0x%08X", uint32(r))
}

// ── COM / MF initialisation ───────────────────────────────────────────────────

var (
	mfOnce     sync.Once
	mfStartErr error
)

// ensureMF initialises COM (MTA) and Media Foundation exactly once per process.
//
// It has to be process-wide rather than per call site: MFStartup/MFShutdown are
// a global pair, so tearing Media Foundation down when ListWebcams() returns
// pulls the platform out from under a capture goroutine that is still holding
// an IMFSourceReader.
func ensureMF() error {
	mfOnce.Do(func() {
		hr, _, _ := procCoInitializeEx.Call(0, coInitMTA)
		if int32(hr) < 0 {
			mfStartErr = fmt.Errorf("CoInitializeEx(MTA): %w", hresult(hr))
			return
		}
		if hr, _, _ = procMFStartup.Call(mfVersion, mfStartupNoSocket); hresult(hr) != nil {
			mfStartErr = fmt.Errorf("MFStartup: %w", hresult(hr))
		}
	})
	return mfStartErr
}

// mfInitThread prepares the calling OS thread to use Media Foundation.
//
// Two things here are not optional:
//
//   - runtime.LockOSThread. COM apartments and Media Foundation platform state
//     belong to an OS thread, but a Go goroutine may migrate between them at any
//     preemption point. Without the lock the reader gets created on a thread
//     that never called CoInitializeEx, and every MF call then fails with
//     MF_E_PLATFORM_NOT_INITIALIZED (0xC00D3E85).
//   - CoInitializeEx(MTA) on this thread, balanced by CoUninitialize in the
//     returned cleanup. MF refuses to work from a single-threaded apartment.
//
// Media Foundation itself is initialised process-wide by ensureMF and is left
// running for the life of the process.
func mfInitThread() (cleanup func(), err error) {
	runtime.LockOSThread()
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			runtime.UnlockOSThread()
		}
	}

	if err = ensureMF(); err != nil {
		unlock()
		return nil, err
	}

	hr, _, _ := procCoInitializeEx.Call(0, coInitMTA)
	// 0 = S_OK (this thread now owns one initialisation), 1 = S_FALSE (one was
	// already pending, so CoUninitialize is still owed), negative = failure.
	if int32(hr) < 0 {
		// RPC_E_CHANGED_MODE means the thread is already an STA. MF copes with
		// that in practice and failing hard here would take the whole server
		// down over a camera, so only genuine errors are surfaced.
		unlock()
		return nil, fmt.Errorf("CoInitializeEx(MTA): %w", hresult(hr))
	}
	needUninit := hr == 1

	// Media Foundation keeps a per-thread platform context, so a thread that
	// has never called MFStartup cannot use MF even though the process has:
	// every call returns MF_E_PLATFORM_NOT_INITIALIZED (0xC00D3E85). MFStartup
	// returns S_FALSE here because ensureMF already started the platform.
	//
	// The matching MFShutdown is deliberately *not* issued: it would decrement
	// a process-wide counter and tear the platform down underneath every other
	// capture thread. MFShutdown is optional at process exit.
	if hr, _, _ = procMFStartup.Call(mfVersion, mfStartupNoSocket); hresult(hr) != nil {
		if needUninit {
			procCoUninitialize.Call()
		}
		unlock()
		return nil, fmt.Errorf("MFStartup (thread): %w", hresult(hr))
	}

	return func() {
		if needUninit {
			procCoUninitialize.Call()
		}
		unlock()
	}, nil
}

// MFSourceReader stream selector constants (sent as DWORD, use uintptr).
const (
	mfSourceReaderFirstVideoStream uintptr = 0xFFFFFFFC // (DWORD)-4
	mfSourceReaderAnyStream        uintptr = 0xFFFFFFFE // (DWORD)-2
)

// ── Public types ──────────────────────────────────────────────────────────────

// WebcamOptions tunes the webcam capture.
type WebcamOptions struct {
	// Quality is the JPEG quality used when a frame has to be re-encoded
	// (YUY2 input). MJPEG input is forwarded untouched and ignores this.
	Quality int

	// DeviceIndex selects a camera; 0 is the first.
	DeviceIndex int

	// Logger receives diagnostic messages. Nil → standard logger.
	Logger *log.Logger
}

// WebcamDevice describes one camera the machine exposes.
type WebcamDevice struct {
	Index uintptr
	Name  string
	Ver   string
}

// camRequest / camResult are the channel types used between Capture() and run().
type camRequest struct {
	reply chan camResult
}

type camResult struct {
	jpeg []byte
	w, h int32
	err  error
}

// WebcamCapturer captures frames from a camera via IMFSourceReader.
// It satisfies [FrameSource], so the existing MJPEG pipeline consumes it
// unchanged.
type WebcamCapturer struct {
	logger *log.Logger

	req  chan camRequest
	quit chan struct{}
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	width   int32
	height  int32
	format  pixelFormat
	quality int
	closed  bool
}

// NewWebcamCapturer opens the selected camera and starts capturing.
func NewWebcamCapturer(opts WebcamOptions) (cap *WebcamCapturer, retErr error) {
	// Recover from any panic inside the construction path so a bad camera
	// driver never takes down the whole server.
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("%w: NewWebcamCapturer panic: %v", ErrNoWebcam, r)
		}
	}()

	if opts.Quality < 10 || opts.Quality > 100 {
		opts.Quality = 70
	}

	devices, err := ListWebcams()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoWebcam, err)
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("%w: no video capture devices found", ErrNoWebcam)
	}
	if opts.DeviceIndex < 0 || opts.DeviceIndex >= len(devices) {
		opts.DeviceIndex = 0
	}

	c := &WebcamCapturer{
		logger: opts.Logger,
		req:    make(chan camRequest),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if c.logger == nil {
		c.logger = log.Default()
	}
	c.quality = opts.Quality

	ready := make(chan error, 1)
	go c.run(opts.DeviceIndex, ready)

	select {
	case err := <-ready:
		if err != nil {
			<-c.done
			return nil, err
		}
		return c, nil
	case <-time.After(15 * time.Second):
		c.logger.Printf("webcam: MSMF open timed out after 15 s")
		_ = c.Close()
		return nil, fmt.Errorf("%w: camera did not answer within 15 s", ErrNoWebcam)
	}
}

// ListWebcams enumerates cameras via MFEnumDeviceSources.
// Returns an empty slice (not an error) when no cameras are installed.
func ListWebcams() (out []WebcamDevice, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("ListWebcams panic: %v", r)
		}
	}()

	cleanup, err := mfInitThread()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// Build attribute store: source type = video capture.
	var attrRaw uintptr
	if hr, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrRaw)), 1); hresult(hr) != nil {
		return nil, fmt.Errorf("MFCreateAttributes: %w", hresult(hr))
	}
	if attrRaw == 0 {
		return nil, fmt.Errorf("MFCreateAttributes returned null")
	}
	attr := comObj(attrRaw)
	defer attr.Release()

	// IMFAttributes::SetGUID(MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE, VIDCAP)
	if r, _, err2 := attr.vtableCall(vSetGUID,
		uintptr(unsafe.Pointer(&mfDevSourceAttrSourceType)),
		uintptr(unsafe.Pointer(&mfDevSourceAttrSourceTypeVidcap))); err2 != nil || hresult(r) != nil {
		return nil, fmt.Errorf("SetGUID(SOURCE_TYPE): hr=%v err=%v", hresult(r), err2)
	}

	// MFEnumDeviceSources → array of IMFActivate*
	var ppDevices uintptr // IMFActivate** (CoTaskMem-allocated)
	var count uint32
	if hr, _, _ := procMFEnumDeviceSources.Call(
		uintptr(attr),
		uintptr(unsafe.Pointer(&ppDevices)),
		uintptr(unsafe.Pointer(&count)),
	); hresult(hr) != nil {
		return nil, fmt.Errorf("MFEnumDeviceSources: %w", hresult(hr))
	}
	if count == 0 || ppDevices == 0 {
		return nil, nil // no devices, not an error
	}
	defer releaseDeviceArray(ppDevices, count)

	for i := uint32(0); i < count; i++ {
		activate := comObj(*(*uintptr)(unsafe.Pointer(ppDevices + uintptr(i)*8)))
		out = append(out, WebcamDevice{
			Index: uintptr(i),
			Name:  activateFriendlyName(activate),
			Ver:   "MSMF",
		})
	}
	return out, nil
}

// releaseDeviceArray releases each IMFActivate and frees the CoTaskMem block.
func releaseDeviceArray(ppDevices uintptr, count uint32) {
	for i := uint32(0); i < count; i++ {
		p := *(*uintptr)(unsafe.Pointer(ppDevices + uintptr(i)*8))
		comObj(p).Release()
	}
	procCoTaskMemFree.Call(ppDevices)
}

// activateFriendlyName reads MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME from an
// IMFActivate (which inherits IMFAttributes).
func activateFriendlyName(a comObj) string {
	if a == 0 {
		return "<null>"
	}
	// IMFAttributes::GetStringLength — slot 11
	var charCount uint32
	r, _, err := a.vtableCall(vGetStringLength,
		uintptr(unsafe.Pointer(&mfDevSourceAttrFriendlyName)),
		uintptr(unsafe.Pointer(&charCount)))
	if err != nil || hresult(r) != nil || charCount == 0 {
		return "<unknown>"
	}
	buf := make([]uint16, charCount+1)
	// IMFAttributes::GetString — slot 12
	r, _, err = a.vtableCall(vGetString,
		uintptr(unsafe.Pointer(&mfDevSourceAttrFriendlyName)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(charCount+1),
		0)
	if err != nil || hresult(r) != nil {
		return "<unknown>"
	}
	return syscall.UTF16ToString(buf)
}

// ── Capture goroutine ─────────────────────────────────────────────────────────

// run owns the MSMF reader for its entire lifetime on a dedicated OS thread.
func (c *WebcamCapturer) run(deviceIndex int, ready chan<- error) {
	defer close(c.done)

	// Recover from panics so a bad camera driver never kills the server.
	defer func() {
		if r := recover(); r != nil {
			c.logger.Printf("webcam: run() panic recovered: %v", r)
			select {
			case ready <- fmt.Errorf("%w: webcam goroutine panic: %v", ErrNoWebcam, r):
			default:
			}
		}
	}()

	// Cleanup also unlocks the OS thread, so this goroutine stays pinned to the
	// apartment it initialised COM in for its whole lifetime.
	cleanup, err := mfInitThread()
	if err != nil {
		ready <- fmt.Errorf("%w: %v", ErrNoWebcam, err)
		return
	}
	defer cleanup()

	reader, w, h, format, err := openSourceReader(deviceIndex)
	if err != nil {
		ready <- fmt.Errorf("%w: %v", ErrNoWebcam, err)
		return
	}
	defer reader.Release()

	slots := sourceReaderSlotsFor(reader)

	c.mu.Lock()
	c.width, c.height, c.format = w, h, format
	c.mu.Unlock()

	c.logger.Printf("webcam: MSMF reader opened for device %d at %dx%d (%s)",
		deviceIndex, w, h, format)
	ready <- nil

	for {
		select {
		case <-c.quit:
			return
		case req := <-c.req:
			jpegBytes, serveErr := c.readFrame(reader, slots, format, w, h)
			req.reply <- camResult{jpeg: jpegBytes, w: w, h: h, err: serveErr}
		}
	}
}

// readFrame pulls one frame with a per-call panic barrier.
//
// A misbehaving camera driver can fault inside ReadSample at any time. The
// barrier keeps that failure scoped to the single request that triggered it:
// the capture loop survives and the caller gets a clean error instead of the
// whole server going down.
func (c *WebcamCapturer) readFrame(reader comObj, slots sourceReaderSlots, format pixelFormat, w, h int32) (frame []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			frame = nil
			err = fmt.Errorf("webcam: frame read panic: %v", r)
			c.logger.Printf("webcam: %v", err)
		}
	}()
	return readOneFrame(reader, slots, format, w, h, c.quality)
}

// ── Reader setup ──────────────────────────────────────────────────────────────

// pixelFormat identifies the sub-type the source reader was configured for.
type pixelFormat uint8

const (
	formatMJPEG pixelFormat = iota // MFVideoFormat_MJPG: forward bytes as-is
	formatYUY2                     // MFVideoFormat_YUY2: convert in-process
)

func (f pixelFormat) String() string {
	if f == formatYUY2 {
		return "YUY2"
	}
	return "MJPEG"
}

// frameSizeSpec is one media type the negotiator may ask for. A zero dimension
// means "any size the driver likes".
type frameSizeSpec struct {
	format pixelFormat
	w, h   int32
}

// negotiationCandidates is tried in order. MJPEG is listed first at every size
// before falling back to raw YUY2, because skipping a per-frame re-encode is
// worth far more than the difference in resolution.
func negotiationCandidates() []frameSizeSpec {
	return []frameSizeSpec{
		{formatMJPEG, 1280, 720},
		{formatMJPEG, 640, 480},
		{formatMJPEG, 0, 0},
		{formatYUY2, 1280, 720},
		{formatYUY2, 640, 480},
		{formatYUY2, 0, 0},
	}
}

// subtypeGUID returns the MFVideoFormat_* GUID for a pixel format.
func subtypeGUID(f pixelFormat) *guid {
	if f == formatYUY2 {
		return &mfVideoFormatYUY2
	}
	return &mfVideoFormatMJPG
}

// defaultFrameW / defaultFrameH describe a frame when the driver accepts an
// unconstrained media type but will not report the size back.
const (
	defaultFrameW int32 = 640
	defaultFrameH int32 = 480
)

// openSourceReader enumerates devices, activates the chosen one, creates an
// IMFSourceReader and negotiates the best output format the driver will give
// us: MJPEG where available, YUY2 otherwise. It also reports the pixel format so
// the caller knows whether frames need converting.
func openSourceReader(deviceIndex int) (reader comObj, w, h int32, format pixelFormat, err error) {
	// Build the filter attribute store.
	var attrRaw uintptr
	if hr, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrRaw)), 1); hresult(hr) != nil {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("MFCreateAttributes: %w", hresult(hr))
	}
	if attrRaw == 0 {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("MFCreateAttributes returned null")
	}
	attr := comObj(attrRaw)
	defer attr.Release()

	if r, _, e := attr.vtableCall(vSetGUID,
		uintptr(unsafe.Pointer(&mfDevSourceAttrSourceType)),
		uintptr(unsafe.Pointer(&mfDevSourceAttrSourceTypeVidcap))); e != nil || hresult(r) != nil {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("SetGUID: hr=%v err=%v", hresult(r), e)
	}

	var ppDevices uintptr
	var count uint32
	if hr, _, _ := procMFEnumDeviceSources.Call(
		uintptr(attr),
		uintptr(unsafe.Pointer(&ppDevices)),
		uintptr(unsafe.Pointer(&count)),
	); hresult(hr) != nil {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("MFEnumDeviceSources: %w", hresult(hr))
	}
	if count == 0 || ppDevices == 0 {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("no video capture devices found")
	}
	defer releaseDeviceArray(ppDevices, count)

	if deviceIndex < 0 || uint32(deviceIndex) >= count {
		deviceIndex = 0
	}
	activatePtr := *(*uintptr)(unsafe.Pointer(ppDevices + uintptr(deviceIndex)*8))
	if activatePtr == 0 {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("device %d IMFActivate pointer is null", deviceIndex)
	}
	activate := comObj(activatePtr)

	// IMFActivate::ActivateObject — slot 33 (after full IMFAttributes 0–32).
	// Signature: ActivateObject(REFIID riid, void** ppv) → HRESULT
	var sourceRaw uintptr
	hr, _, e := activate.vtableCall(vActivateObject,
		uintptr(unsafe.Pointer(&iidIMFMediaSource)),
		uintptr(unsafe.Pointer(&sourceRaw)))
	if e != nil || hresult(hr) != nil || sourceRaw == 0 {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("ActivateObject: hr=%v err=%v", hresult(hr), e)
	}
	source := comObj(sourceRaw)
	defer source.Release()

	// MFCreateSourceReaderFromMediaSource → IMFSourceReader
	var readerRaw uintptr
	if hr, _, _ = procMFCreateSourceReaderFromMediaSource.Call(
		uintptr(source),
		0, // no reader attributes
		uintptr(unsafe.Pointer(&readerRaw)),
	); hresult(hr) != nil {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("MFCreateSourceReaderFromMediaSource: %w", hresult(hr))
	}
	if readerRaw == 0 {
		return 0, 0, 0, formatMJPEG, fmt.Errorf("MFCreateSourceReaderFromMediaSource returned null")
	}
	reader = comObj(readerRaw)

	// NOTE: IMFActivate::ShutdownObject is deliberately NOT called here.
	// Shutting the activate object down tears the media source down while the
	// source reader above is still using it, and every subsequent call then
	// fails with MF_E_PLATFORM_NOT_INITIALIZED (0xC00D3E85). Releasing the
	// activate objects below is enough: the camera is freed when the reader is
	// released in the capture goroutine.

	slots := sourceReaderSlotsFor(reader)

	// Deselect all streams, then enable only the first video stream.
	reader.vtableCall(slots.setStreamSelection, mfSourceReaderAnyStream, 0)
	reader.vtableCall(slots.setStreamSelection, mfSourceReaderFirstVideoStream, 1)

	for _, cand := range negotiationCandidates() {
		w, h = trySetMediaType(reader, cand, slots)
		if w > 0 && h > 0 {
			return reader, w, h, cand.format, nil
		}
	}

	reader.Release()
	return 0, 0, 0, formatMJPEG, fmt.Errorf("camera offers neither MJPEG nor YUY2 output")
}

// trySetMediaType configures the reader for one candidate media type.
// Returns the negotiated size, or 0×0 when the driver rejected the request.
func trySetMediaType(reader comObj, want frameSizeSpec, slots sourceReaderSlots) (int32, int32) {
	if reader == 0 {
		return 0, 0
	}
	var mtRaw uintptr
	if hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&mtRaw))); hresult(hr) != nil || mtRaw == 0 {
		return 0, 0
	}
	mt := comObj(mtRaw)
	defer mt.Release()

	sub := subtypeGUID(want.format)

	// MF_MT_MAJOR_TYPE = MFMediaType_Video
	if r, _, _ := mt.vtableCall(vSetGUID,
		uintptr(unsafe.Pointer(&mfMtMajorType)),
		uintptr(unsafe.Pointer(&mfMediaTypeVideo))); hresult(r) != nil {
		return 0, 0
	}
	// MF_MT_SUBTYPE = the requested video format
	if r, _, _ := mt.vtableCall(vSetGUID,
		uintptr(unsafe.Pointer(&mfMtSubtype)),
		uintptr(unsafe.Pointer(sub))); hresult(r) != nil {
		return 0, 0
	}
	// MF_MT_FRAME_SIZE: packed uint64, high 32 = width, low 32 = height.
	if want.w > 0 && want.h > 0 {
		frameSize := (uint64(uint32(want.w)) << 32) | uint64(uint32(want.h))
		if r, _, _ := mt.vtableCall(vSetUINT64,
			uintptr(unsafe.Pointer(&mfMtFrameSize)),
			uintptr(frameSize)); hresult(r) != nil {
			return 0, 0
		}
	}

	// IMFSourceReader::SetCurrentMediaType(streamIndex, reserved, pMediaType)
	hr, _, _ := reader.vtableCall(slots.setCurrentMediaType,
		mfSourceReaderFirstVideoStream, 0, uintptr(mt))
	if hresult(hr) != nil {
		return 0, 0
	}
	// The driver accepted the request. Read back what it actually negotiated;
	// if the read-back is unavailable, fall back to what was asked for rather
	// than discarding a perfectly good configuration.
	if w, h := readNegotiatedFrameSize(reader, slots); w > 0 && h > 0 {
		return w, h
	}
	if want.w > 0 && want.h > 0 {
		return want.w, want.h
	}
	return defaultFrameW, defaultFrameH
}

// readNegotiatedFrameSize queries MF_MT_FRAME_SIZE from the reader's current
// media type after SetCurrentMediaType has been accepted.
func readNegotiatedFrameSize(reader comObj, slots sourceReaderSlots) (int32, int32) {
	if reader == 0 {
		return 0, 0
	}
	var mtRaw uintptr
	hr, _, _ := reader.vtableCall(slots.getCurrentMediaType,
		mfSourceReaderFirstVideoStream,
		uintptr(unsafe.Pointer(&mtRaw)))
	if hresult(hr) != nil || mtRaw == 0 {
		return 0, 0
	}
	mt := comObj(mtRaw)
	defer mt.Release()

	var frameSize uint64
	r, _, _ := mt.vtableCall(vGetUINT64,
		uintptr(unsafe.Pointer(&mfMtFrameSize)),
		uintptr(unsafe.Pointer(&frameSize)))
	if hresult(r) != nil {
		return 0, 0
	}
	return int32(frameSize >> 32), int32(frameSize & 0xFFFFFFFF)
}

// ── Frame reading ─────────────────────────────────────────────────────────────

// readOneFrame returns one frame as JPEG bytes, whatever the driver produced.
func readOneFrame(reader comObj, slots sourceReaderSlots, format pixelFormat, w, h int32, quality int) ([]byte, error) {
	raw, err := readOneRawFrame(reader, slots)
	if err != nil {
		return nil, err
	}
	if format == formatMJPEG {
		return raw, nil
	}
	return yuy2ToJPEG(raw, w, h, quality)
}

// mfSourceReaderFlagStreamTick reports that ReadSample completed without
// producing data — the source is caught up and has nothing ready yet. This is
// normal right after the reader opens, so it must not be surfaced as an error.
const mfSourceReaderFlagStreamTick uint32 = 0x00000100

// streamTickRetryLimit bounds how many consecutive empty ticks are tolerated
// before giving up, so a camera that never delivers data still fails cleanly
// instead of spinning forever.
const streamTickRetryLimit = 50

// readOneRawFrame calls IMFSourceReader::ReadSample and copies the contiguous
// bytes out of the returned IMFSample.
//
// An empty result carrying only MF_SOURCE_READERF_STREAMTICK means "nothing
// ready yet, ask again", so the call is retried: the very first ReadSample
// after opening a camera almost always ticks instead of delivering a frame,
// and returning that to the caller would make the first viewer request fail.
func readOneRawFrame(reader comObj, slots sourceReaderSlots) ([]byte, error) {
	if reader == 0 {
		return nil, fmt.Errorf("source reader is nil")
	}

	for attempt := 0; attempt < streamTickRetryLimit; attempt++ {
		// IMFSourceReader::ReadSample(streamIndex, controlFlags,
		//   &actualStreamIndex, &streamFlags, &timestamp, &pSample) → HRESULT
		var actualStream uint32
		var flags uint32
		var ts int64
		var sampleRaw uintptr

		hr, _, e := reader.vtableCall(slots.readSample,
			mfSourceReaderFirstVideoStream,
			0, // MF_SOURCE_READER_CONTROL_FLAG_NONE
			uintptr(unsafe.Pointer(&actualStream)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&ts)),
			uintptr(unsafe.Pointer(&sampleRaw)),
		)
		if e != nil {
			return nil, fmt.Errorf("ReadSample (vtable): %w", e)
		}
		if hresult(hr) != nil {
			return nil, fmt.Errorf("ReadSample: %w", hresult(hr))
		}

		if sampleRaw == 0 {
			if flags&mfSourceReaderFlagStreamTick != 0 && flags&^mfSourceReaderFlagStreamTick == 0 {
				continue // nothing ready yet: ask again
			}
			return nil, fmt.Errorf("ReadSample returned nil sample (streamFlags=0x%08X)", flags)
		}

		return copySampleBytes(comObj(sampleRaw))
	}
	return nil, fmt.Errorf("ReadSample produced no frame after %d ticks", streamTickRetryLimit)
}

// copySampleBytes extracts the contiguous payload of an IMFSample.
func copySampleBytes(sample comObj) ([]byte, error) {
	defer sample.Release()

	// IMFSample::ConvertToContiguousBuffer — slot 41
	// (IMFSample inherits 33 IMFAttributes slots 0–32, own methods start at 33)
	var bufRaw uintptr
	if hr, _, e := sample.vtableCall(vSampleConvertToContiguousBuffer,
		uintptr(unsafe.Pointer(&bufRaw))); e != nil || hresult(hr) != nil || bufRaw == 0 {
		return nil, fmt.Errorf("ConvertToContiguousBuffer: hr=%v err=%v", hresult(hr), e)
	}
	buf := comObj(bufRaw)
	defer buf.Release()

	// IMFMediaBuffer::Lock(&ppbBuffer, &pcbMaxLength, &pcbCurrentLength)
	var pbBuffer uintptr
	var maxLen, curLen uint32
	if hr, _, e := buf.vtableCall(vBufLock,
		uintptr(unsafe.Pointer(&pbBuffer)),
		uintptr(unsafe.Pointer(&maxLen)),
		uintptr(unsafe.Pointer(&curLen))); e != nil || hresult(hr) != nil {
		return nil, fmt.Errorf("IMFMediaBuffer::Lock: hr=%v err=%v", hresult(hr), e)
	}
	if pbBuffer == 0 || curLen == 0 {
		_, _, _ = buf.vtableCall(vBufUnlock)
		return nil, fmt.Errorf("IMFMediaBuffer::Lock returned empty/null buffer")
	}

	// Copy the bytes before Unlock; the pointer is only valid until Unlock.
	n := int(curLen)
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(pbBuffer)), n))
	_, _, _ = buf.vtableCall(vBufUnlock)

	return out, nil
}

// yuy2ToJPEG converts one packed YUY2 (4:2:2) frame into a JPEG image.
//
// YUY2 interleaves luma and chroma per *pixel pair*, which lines up exactly with
// image.YCbCr at 4:4:4 sampling — every pixel carries its own Cb/Cr once the
// pair is duplicated. Going through YCbCr rather than RGB keeps the chroma
// values the sensor produced instead of round-tripping them through a colour
// matrix, and lets image/jpeg do the conversion in its own DCT path.
func yuy2ToJPEG(src []byte, w, h int32, quality int) ([]byte, error) {
	if w <= 0 || h <= 0 || w%2 != 0 {
		return nil, fmt.Errorf("yuy2: cannot convert %dx%d frame", w, h)
	}
	need := int(w) * int(h) * 2
	if len(src) < need {
		return nil, fmt.Errorf("yuy2: frame is %d bytes, need %d for %dx%d", len(src), need, w, h)
	}

	img := image.NewYCbCr(image.Rect(0, 0, int(w), int(h)), image.YCbCrSubsampleRatio444)
	luma, cb, cr := img.Y, img.Cb, img.Cr

	si := 0
	for row := 0; row < int(h); row++ {
		yi := row * int(w)
		for col := 0; col < int(w); col += 2 {
			y0 := src[si]
			u := src[si+1]
			y1 := src[si+2]
			v := src[si+3]
			si += 4

			luma[yi] = y0
			luma[yi+1] = y1
			cb[yi] = u
			cb[yi+1] = u
			cr[yi] = v
			cr[yi+1] = v
			yi += 2
		}
	}

	if quality < 1 {
		quality = 70
	} else if quality > 100 {
		quality = 100
	}

	var out bytes.Buffer
	// JPEG from 4:2:2 source typically lands well under half the raw size.
	out.Grow(need / 2)
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("yuy2: jpeg encode: %w", err)
	}
	return out.Bytes(), nil
}

// ── FrameSource methods ───────────────────────────────────────────────────────

const (
	webcamReplyTimeout = 8 * time.Second
	webcamCloseTimeout = 5 * time.Second
)

// Capture grabs one frame, blocking until the capture goroutine replies.
func (c *WebcamCapturer) Capture() ([]byte, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errors.New("remote: webcam capturer is closed")
	}

	reply := make(chan camResult, 1)
	select {
	case c.req <- camRequest{reply: reply}:
	case <-c.done:
		return nil, errors.New("remote: webcam capture thread stopped")
	case <-time.After(webcamReplyTimeout):
		return nil, errors.New("remote: camera did not answer in time")
	}

	select {
	case res := <-reply:
		return res.jpeg, res.err
	case <-c.done:
		return nil, errors.New("remote: webcam capture thread stopped")
	case <-time.After(webcamReplyTimeout):
		return nil, errors.New("remote: camera did not answer in time")
	}
}

// Size reports the camera's frame size.
func (c *WebcamCapturer) Size() (w, h int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.width, c.height
}

// Close stops the capture goroutine. Safe to call more than once.
func (c *WebcamCapturer) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.once.Do(func() {
		close(c.quit)
		select {
		case <-c.done:
		case <-time.After(webcamCloseTimeout):
			c.logger.Printf("webcam: capture goroutine did not exit within %v", webcamCloseTimeout)
		}
	})
	return nil
}
