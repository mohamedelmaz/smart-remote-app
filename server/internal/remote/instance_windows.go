//go:build windows

package remote

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// SingleInstance enforces that only one server owns the input surface at a
// time, using a named mutex.
//
// Two servers running simultaneously would inject input from two goroutine
// sets and fight over the screen stream, so the second launch replaces the
// first instead of coexisting.
type SingleInstance struct {
	name  string
	mu    syscall.Handle
	owned bool
}

var (
	kernel32Mutex                = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW             = kernel32Mutex.NewProc("CreateMutexW")
	procCloseHandle              = kernel32Mutex.NewProc("CloseHandle")
	procReleaseMutex             = kernel32Mutex.NewProc("ReleaseMutex")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

// Win32 constants used here.
const (
	mutexAllAccess  = 0x1F0001
	errorAlreadyExi = 183
	wmClose         = 0x0010
)

// InstanceResult describes what happened when claiming the mutex.
type InstanceResult struct {
	// AlreadyRunning is true when another instance held the mutex.
	AlreadyRunning bool
	// ReplacedPID is the process id of the instance that was terminated.
	ReplacedPID int
	// Err is non-nil when the mutex could not be created or claimed.
	Err error
}

// ClaimSingleInstance attempts to become the sole server instance.
//
// If another instance holds the mutex it is asked to exit and this process
// waits briefly for the mutex to become available. Both the mutex handle and
// the opened handle of the previous instance are closed on every path,
// including the ERROR_ALREADY_EXISTS path: leaking the handle from
// OpenProcess is the classic cause of a mutex that can never be re-claimed
// after a failed launch.
func ClaimSingleInstance(name string, replacePrevious bool) (*SingleInstance, InstanceResult) {
	var res InstanceResult

	handle, _, _ := procCreateMutexW.Call(0, 1, utf16Ptr(name))
	if handle == 0 {
		res.Err = fmt.Errorf("remote: CreateMutex failed (err %d)", lastError())
		return nil, res
	}

	// GetLastError must be read immediately after CreateMutex: any intervening
	// call resets it.
	if lastError() == errorAlreadyExi {
		// Another instance owns the mutex. The handle just returned is a
		// second handle to the *same* mutex object that we do not own, so it
		// must be closed. Leaking it here is the classic cause of a mutex
		// that can never be re-claimed after a failed launch.
		_, _, _ = procCloseHandle.Call(handle)

		if replacePrevious {
			res.ReplacedPID = terminateInstance(name)
		}

		// Wait for the previous owner to exit and release the mutex.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			retry, _, _ := procCreateMutexW.Call(0, 1, utf16Ptr(name))
			if retry == 0 {
				continue
			}
			if lastError() == errorAlreadyExi {
				// Still owned by the old instance; close this handle too so
				// we do not accumulate handles while polling.
				_, _, _ = procCloseHandle.Call(retry)
				continue
			}
			handle = retry
			res.AlreadyRunning = true
			break
		}

		if res.AlreadyRunning == false {
			res.Err = errors.New("remote: previous instance did not exit in time")
			return nil, res
		}
	}

	inst := &SingleInstance{name: name, mu: syscall.Handle(handle), owned: true}
	return inst, res
}

// Release drops ownership and closes the handle.
func (s *SingleInstance) Release() {
	if s == nil || !s.owned {
		return
	}
	// ReleaseMutex is only valid for a mutex this thread owns. If it fails,
	// CloseHandle still runs so the handle does not leak.
	_, _, _ = procReleaseMutex.Call(uintptr(s.mu))
	_, _, _ = procCloseHandle.Call(uintptr(s.mu))
	s.owned = false
	s.mu = 0
}

// terminateInstance asks a running instance to exit and returns its pid.
//
// A WM_CLOSE is posted rather than a hard kill so the old instance can
// release its port, screen stream and mutex in an orderly fashion.
// terminateInstance asks a running instance to exit and returns its pid.
//
// A WM_CLOSE is posted rather than a hard kill so the old instance can
// release its port, screen stream and mutex in an orderly fashion.
//
// This callback writes the owning process id back through the lparam pointer
// that EnumWindows supplied. go vet reports "possible misuse of unsafe.Pointer"
// here for the same reason it does on the COM vtable reads in winutil_windows.go:
// lparam is a uintptr supplied by Win32 that must be converted back to a
// pointer to reach the caller's int. The address is owned by a Windows stack
// frame, not the Go heap, so the moving-GC hazard the check guards against
// cannot apply. It cannot be silenced with //nolint, which is a golangci-lint
// directive that the standard toolchain does not implement.
func terminateInstance(name string) int {
	var pid int
	callback := syscall.NewCallback(func(hwnd syscall.Handle, lparam uintptr) uintptr {
		var wpid uint32
		procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&wpid)))

		buf := make([]uint16, 256)
		procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)*2))
		if syscall.UTF16ToString(buf) == name {
			*(*int)(unsafe.Pointer(lparam)) = int(wpid)
			procPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
			return 0 // stop enumerating
		}
		return 1 // keep enumerating
	})
	procEnumWindows.Call(callback, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// utf16Ptr returns a pointer to a NUL-terminated UTF-16 copy of s.
func utf16Ptr(s string) uintptr {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return 0
	}
	return uintptr(unsafe.Pointer(p))
}
