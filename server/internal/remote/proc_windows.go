//go:build windows

package remote

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// createNoWindow is the Windows CREATE_NO_WINDOW creation flag.
//
// A binary built with -H windowsgui is a GUI-subsystem process and has no
// console of its own. Windows therefore allocates a brand new console for
// every console-subsystem child it starts (powershell.exe, cmd.exe,
// rundll32.exe), painting a black window that stays up for the child's whole
// lifetime. CREATE_NO_WINDOW stops that child from being given one at all.
const createNoWindow = 0x08000000

// hideConsole suppresses the console window a child process would otherwise
// flash on screen.
//
// Both attributes are needed. HideWindow asks for a hidden window outright;
// CreationFlags|CREATE_NO_WINDOW is the belt-and-braces half, and is the flag
// that actually prevents the console allocation on the versions of Windows
// where HideWindow alone is advisory. They are harmless together.
//
// Suppressing this has to happen on the child *before* it starts. There is no
// way to detach a console window retroactively, so every exec.Command in this
// package must go through here.
func hideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// hiddenCommand is the ONLY supported way to start a helper process from this
// GUI-subsystem server.
//
// It exists because a bare exec.Command was being used for the PowerShell
// firewall and network-profile calls, and for the cmd.exe shell macros. Those
// calls include one in the status-polling path that runs every few seconds,
// which is why a black console kept reappearing instead of appearing once.
func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	hideConsole(cmd)
	return cmd
}

// OpenBrowser opens url in the default browser, logging but never failing.
//
// rundll32 is used rather than cmd's `start`: `start` reinterprets a URL
// containing an ampersand as a command, which dashboard URLs can.
func OpenBrowser(url string, logger *log.Logger) {
	if url == "" || os.Getenv("SMART_REMOTE_NO_BROWSER") != "" {
		return
	}
	if err := hiddenCommand("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		if logger != nil {
			logger.Printf("ui: could not open browser: %v (open %s manually)", err, url)
		}
		return
	}
	if logger != nil {
		logger.Printf("ui: opened %s", url)
	}
}

// clipboardSet copies text to the Windows clipboard.
//
// A helper process is unavoidable: PowerShell's Set-Clipboard is the scripting
// route that needs no extra dependency, and hiddenCommand is what keeps it from
// flashing a console window.
func clipboardSet(text string) error {
	script := "Set-Clipboard -Value " + psQuote(text)
	return hiddenCommand("powershell.exe",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script).Run()
}

// psQuote wraps s as a PowerShell single-quoted literal, doubling any embedded
// quote. Without this an address containing a quote would break the script
// instead of being copied.
func psQuote(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '\'')
	for _, r := range s {
		if r == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, r)
	}
	return string(append(out, '\''))
}

// LogFilePath returns the file this server mirrors its log output to.
//
// A GUI-subsystem binary has no usable stderr, so the log file is the only
// place the user can recover the PIN or read a startup failure.
func LogFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "smart-remote-server.log")
}
