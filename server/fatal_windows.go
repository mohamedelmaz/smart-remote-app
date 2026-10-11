package main

import (
	"log"
	"strings"
	"syscall"
	"unsafe"

	"smartremote/server/internal/remote"
)

var (
	user32ProcMessageBoxW = syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
)

// MessageBox flags. The error icon plus a single OK button: the user has one
// decision here, which is to read the message and dismiss it.
const (
	mbOK        = 0x00000000
	mbIconError = 0x00000010
)

// showFatalDialog is a variable so tests can replace the dialog with a
// recorder. Setting it to nil disables the dialog entirely, which is what a
// headless or automated run should do.
var showFatalDialog = messageBoxFatal

// messageBoxFatal puts a modal error box on screen.
//
// The server is built as a GUI-subsystem process, so when it dies during
// startup it vanishes without a trace: no console, no taskbar entry, nothing
// on screen. The two failures that need explaining - the port is taken, and
// the settings folder cannot be created - are the ones where the user's next
// action depends entirely on knowing which of them happened.
func messageBoxFatal(title, text string) {
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	m, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = user32ProcMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		uintptr(mbOK|mbIconError),
	)
}

// fatalMessageText builds the body of the startup error dialog.
//
// It is deliberately plain English with no jargon and no error codes: the
// person reading it is trying to work out whether they should close another
// program or fix a permissions problem. The log path is included because the
// dialog cannot carry the detail and the log is where the detail is.
func fatalMessageText(reason string) string {
	logPath := remote.LogFilePath()
	var b strings.Builder
	b.WriteString("Smart Remote could not start.\r\n\r\n")
	b.WriteString(reason)
	b.WriteString("\r\n\r\nThe full details, including the exact address of the ")
	b.WriteString("file below, are in this log:\r\n")
	b.WriteString(logPath)
	return b.String()
}

// reportFatal records a fatal startup failure, shows the dialog, and returns.
// The caller exits; keeping the exit outside means this function stays
// testable, which matters because the whole point is that a silent death is
// the bug being fixed.
func reportFatal(logger *log.Logger, reason string) {
	logger.Printf("FATAL: %s", reason)
	if showFatalDialog == nil {
		return
	}
	showFatalDialog(windowTitle, fatalMessageText(reason))
}
