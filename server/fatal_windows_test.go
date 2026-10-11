package main

import (
	"bytes"
	"log"
	"reflect"
	"strings"
	"testing"
)

// A GUI-subsystem process has no console, so this is the only place the user
// finds out why nothing happened.
func TestFatalDialogShowsReasonAndLogPath(t *testing.T) {
	prev := showFatalDialog
	defer func() { showFatalDialog = prev }()

	var gotTitle, gotText string
	shown := 0
	showFatalDialog = func(title, text string) {
		shown++
		gotTitle, gotText = title, text
	}

	var logBuf bytes.Buffer
	reportFatal(log.New(&logBuf, "", 0), "the port 8765 is already in use")

	if shown != 1 {
		t.Fatalf("dialog shown %d times, want 1", shown)
	}
	if !strings.Contains(gotText, "8765") {
		t.Fatalf("dialog does not name the port: %q", gotText)
	}
	if !strings.Contains(gotText, "could not start") {
		t.Fatalf("dialog does not say what happened: %q", gotText)
	}
	if !strings.Contains(strings.ToLower(gotText), "log") {
		t.Fatalf("dialog does not point at the log: %q", gotText)
	}
	if gotTitle == "" {
		t.Fatal("dialog has no title")
	}
	// The log line must still happen: the dialog is in addition, never instead.
	if !strings.Contains(logBuf.String(), "FATAL") {
		t.Fatalf("log = %q, want a FATAL line", logBuf.String())
	}
}

// The settings folder failure is the other case worth interrupting for.
func TestFatalDialogCoversSettingsDirFailure(t *testing.T) {
	prev := showFatalDialog
	defer func() { showFatalDialog = prev }()

	var gotText string
	showFatalDialog = func(_, text string) { gotText = text }

	reportFatal(log.New(&bytes.Buffer{}, "", 0),
		"cannot create the settings folder C:\\Users\\x\\AppData\\Roaming\\SmartRemote (access denied)")

	if !strings.Contains(gotText, "settings folder") {
		t.Fatalf("dialog does not identify the settings folder: %q", gotText)
	}
	if !strings.Contains(gotText, "access denied") {
		t.Fatalf("dialog lost the underlying reason: %q", gotText)
	}
}

// Automated and headless runs must be able to turn the dialog off without
// patching the code.
func TestFatalDialogCanBeDisabled(t *testing.T) {
	prev := showFatalDialog
	defer func() { showFatalDialog = prev }()
	showFatalDialog = nil

	var logBuf bytes.Buffer
	reportFatal(log.New(&logBuf, "", 0), "boom") // must not panic

	if !strings.Contains(logBuf.String(), "boom") {
		t.Fatalf("log = %q, want the reason even with the dialog disabled", logBuf.String())
	}
}

// The other tests install their own recorder, so without this one the default
// wiring is never checked: the dialog could be left disabled and the suite
// would still be green while the server dies silently in production.
func TestFatalDialogIsWiredUpByDefault(t *testing.T) {
	if showFatalDialog == nil {
		t.Fatal("fatal dialog is disabled by default; startup failures would be silent again")
	}
	got := reflect.ValueOf(showFatalDialog).Pointer()
	want := reflect.ValueOf(messageBoxFatal).Pointer()
	if got != want {
		t.Fatal("showFatalDialog does not point at messageBoxFatal by default")
	}
}

// The message is for someone who did not read the source, so it must not leak
// jargon.
func TestFatalMessageTextIsPlain(t *testing.T) {
	text := fatalMessageText("the server could not start (address already in use)")
	for _, bad := range []string{"0x", "errno", "syscall", "nil", "goroutine"} {
		if strings.Contains(text, bad) {
			t.Fatalf("message contains jargon %q: %q", bad, text)
		}
	}
	if !strings.Contains(text, "address already in use") {
		t.Fatalf("message dropped the reason: %q", text)
	}
}
