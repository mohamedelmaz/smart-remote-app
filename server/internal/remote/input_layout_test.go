package remote

import (
	"testing"
	"unsafe"
)

// TestInputStructSize pins INPUT to the size SendInput actually expects.
//
// Windows declares INPUT as a 4-byte type plus a union whose size is that of
// its largest member (MOUSEINPUT, 32 bytes on x64), giving 40 bytes with the
// union 8-byte aligned. SendInput validates cbSize against this and returns 0
// with ERROR_INVALID_PARAMETER - "The parameter is incorrect" - for anything
// else. That failure is indistinguishable from a blocked input at the UI level,
// which is why it previously read as a UIPI or permissions problem.
//
// The old definition carried an extra [32]byte inside the union "so one
// definition works on both architectures". It inflated INPUT to 96 bytes, so
// every single SendInput call was rejected on its own struct size.
func TestInputStructSize(t *testing.T) {
	const want = 40 // sizeof(INPUT) on 64-bit Windows
	if got := unsafe.Sizeof(input{}); got != want {
		t.Fatalf("sizeof(INPUT) = %d, want %d; SendInput rejects any other "+
			"cbSize with ERROR_INVALID_PARAMETER", got, want)
	}
}

// TestInputUnionSize pins the union to its largest member.
func TestInputUnionSize(t *testing.T) {
	const want = 32 // sizeof(MOUSEINPUT) on x64
	if got := unsafe.Sizeof(inputUnion{}); got != want {
		t.Errorf("sizeof(union) = %d, want %d; Go already sizes a struct to "+
			"its largest member, so an explicit pad is redundant", got, want)
	}
}

// TestInputOffsets checks the field placement Windows relies on.
//
// wType sits at offset 0 and the union at offset 8 because MOUSEINPUT holds a
// pointer-sized field and the whole struct is 8-byte aligned. A field at the
// wrong offset would be read as garbage even with a correct total size.
func TestInputOffsets(t *testing.T) {
	var in input
	base := uintptr(unsafe.Pointer(&in))

	if off := uintptr(unsafe.Pointer(&in.Type)) - base; off != 0 {
		t.Errorf("Type offset = %d, want 0", off)
	}
	if off := uintptr(unsafe.Pointer(&in.U)) - base; off != 8 {
		t.Errorf("union offset = %d, want 8 (8-byte alignment)", off)
	}

	// The keyboard member must expose Vk at union offset 0, i.e. struct
	// offset 8 on x64.
	ki := in.U.ki()
	if off := uintptr(unsafe.Pointer(&ki.Vk)) - base; off != 8 {
		t.Errorf("KEYBDINPUT.wVk offset = %d, want 8", off)
	}
}

// TestMouseInputSize pins MOUSEINPUT, the union's largest member.
func TestMouseInputSize(t *testing.T) {
	const want = 32
	if got := unsafe.Sizeof(mouseInput{}); got != want {
		t.Errorf("sizeof(MOUSEINPUT) = %d, want %d", got, want)
	}
	if got := unsafe.Sizeof(keybdInput{}); got != 24 {
		t.Errorf("sizeof(KEYBDINPUT) = %d, want 24", got)
	}
}