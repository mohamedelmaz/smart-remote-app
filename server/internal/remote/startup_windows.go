//go:build windows

package remote

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// startupValueName is the value this package writes under Run.
//
// Naming it rather than deleting the whole Run key matters: every installed
// application shares HKCU\...\Run, so removing the key to "turn startup off"
// would silently uninstall every other program that starts with Windows.
const startupValueName = "SmartRemote"

// runKeyPath is the per-user Run key.
//
// HKCU rather than HKLM is deliberate. HKLM needs administrator rights to
// write, and this is a personal tray tool an ordinary user installs; asking for
// elevation to register autostart would be out of proportion. HKCU also follows
// the user, so two people on one PC each get their own choice.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// StartupEnabled reports whether the server is registered to run at sign-in.
//
// It reads the actual registry rather than trusting a saved preference, because
// the entry can be removed by anything - Task Manager's Startup tab, msconfig,
// another cleanup tool - and a UI that disagreed with reality would be worse
// than having no UI at all.
func StartupEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath,
		registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()

	_, _, err = key.GetStringValue(startupValueName)
	return err == nil
}

// StartupCommand returns the command Windows would run at sign-in.
//
// It exists for diagnostics and for tests: a registration that silently points
// at the wrong path is otherwise invisible until the user reboots and finds
// nothing happened.
func StartupCommand() (string, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer key.Close()

	command, _, err := key.GetStringValue(startupValueName)
	if err != nil {
		return "", false
	}
	return command, true
}

// SetStartup registers or unregisters the server at Windows sign-in.
//
// The registered command is a double-quoted absolute path to the executable
// with no arguments. The quoting is essential: a program installed under a path
// containing spaces - "C:\Program Files\..." being the obvious case - otherwise
// gets truncated at the first space and Windows silently runs nothing.
func SetStartup(enabled bool, logger *log.Logger) error {
	if logger == nil {
		logger = log.Default()
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("remote: cannot locate the executable: %w", err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return fmt.Errorf("remote: cannot resolve the executable path: %w", err)
	}

	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath,
		registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("remote: cannot open the startup key: %w", err)
	}
	defer key.Close()

	if !enabled {
		if err := key.DeleteValue(startupValueName); err != nil {
			// A missing value already means "not registered", so this is the
			// desired end state rather than a failure worth reporting.
			if err == registry.ErrNotExist {
				logger.Printf("startup: already disabled")
				_ = removeTaskManagerEntry()
				return nil
			}
			return fmt.Errorf("remote: cannot remove the startup entry: %w", err)
		}
		logger.Printf("startup: disabled")
		_ = removeTaskManagerEntry()
		return nil
	}

	command := fmt.Sprintf("\"%s\"", exe)
	if err := key.SetStringValue(startupValueName, command); err != nil {
		return fmt.Errorf("remote: cannot write the startup entry: %w", err)
	}

	// Best effort: the Run key is what actually launches the app, and this only
	// affects how the entry is presented in Task Manager.
	if err := writeTaskManagerEntry(exe, true); err != nil {
		logger.Printf("startup: WARNING could not write the Task Manager record: %v", err)
	}

	logger.Printf("startup: registered %q", command)
	return nil
}

// startupEntryIsWellFormed reports whether a Run entry points at a real file.
//
// A Run value naming a path which no longer exists is the single most common way
// these entries rot: the app is moved or reinstalled elsewhere and the stale
// string keeps launching a Windows error dialog nobody can get rid of.
func startupEntryIsWellFormed(command string) bool {
	path := unquoteRunCommand(command)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// unquoteRunCommand extracts the executable path from a Run entry.
//
// Run values are conventionally `"C:\path with spaces\app.exe" [args]`. Only the
// quoted executable is wanted, so the arguments - which may themselves contain
// quotes - are dropped. An unquoted value is returned as-is, which is what
// Windows itself does when it can find no quote.
func unquoteRunCommand(command string) string {
	command = trimSpaces(command)
	if command == "" {
		return ""
	}
	if command[0] != '"' {
		// Unquoted: the path ends at the first argument separator, but a bare
		// path containing spaces is common enough that trusting the whole
		// string is the safer failure mode here.
		return command
	}
	end := strings.IndexByte(command[1:], '"')
	if end < 0 {
		return ""
	}
	return command[1 : end+1]
}

func trimSpaces(s string) string {
	return strings.TrimSpace(s)
}

// startupEntryJSON is the shape Task Manager's Startup tab reads on Windows 10
// and 11, which is where most users actually manage autostart.
//
// It is a struct written with encoding/json rather than hand-built JSON, so the
// escaping of the path is the standard library's and cannot be got wrong.
type startupEntryJSON struct {
	// StartupApproved is the value Task Manager itself writes when a user
	// enables or disables an entry in the UI. Writing it keeps Task Manager's
	// Enabled column in step instead of showing the entry greyed out.
	StartupApproved string `json:"StartupApproved"`

	// Executable is the absolute path to the binary.
	Executable string `json:"Executable"`

	// Path is the directory holding the binary.
	Path string `json:"Path"`

	// Args is the argument string, empty because the server takes none.
	Args string `json:"Args"`

	// CommandLine is the fully quoted form Windows executes.
	CommandLine string `json:"CommandLine"`

	// Description is the tooltip shown in the Startup tab.
	Description string `json:"Description"`
}

// The two values Windows uses for a task the user has allowed or blocked.
const (
	startupApprovedEnabled  = "2"
	startupApprovedDisabled = "6"
)

// writeTaskManagerEntry records the entry where Task Manager can see it.
//
// Without this the Startup tab lists the entry with no path, and toggling it
// there writes nothing that affects the Run key - the classic "I disabled it in
// Task Manager and it still starts" complaint.
func writeTaskManagerEntry(exe string, enabled bool) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}

	base := filepath.Join(dir, "Microsoft", "Windows", "Start Menu",
		"Programs", "Startup")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}

	approved := startupApprovedEnabled
	if !enabled {
		approved = startupApprovedDisabled
	}

	entry := startupEntryJSON{
		StartupApproved: approved,
		Executable:      exe,
		Path:            filepath.Dir(exe),
		Args:            "",
		CommandLine:     fmt.Sprintf("\"%s\"", exe),
		Description:     "Smart Remote - control this PC from your phone",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(base, "SmartRemote.json"), data, 0o644)
}

// removeTaskManagerEntry deletes the Task Manager record.
//
// A missing file already means the desired state, so absence is not an error.
func removeTaskManagerEntry() error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	base := filepath.Join(dir, "Microsoft", "Windows", "Start Menu",
		"Programs", "Startup")

	if err := os.Remove(filepath.Join(base, "SmartRemote.json")); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}
