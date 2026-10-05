package remote

import (
	"os"
	"strings"
	"testing"
)

// TestUnquoteRunCommand covers the parsing that decides where Windows will
// actually launch the executable from.
//
// The path-with-spaces case is the one that matters: without the quotes a value
// like C:\Program Files\Smart Remote\app.exe is truncated at the first space and
// Windows silently starts nothing. Every installer path that is not fully
// portable hits this.
func TestUnquoteRunCommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "quoted path with spaces",
			in:   `"C:\Program Files\Smart Remote\app.exe"`,
			want: `C:\Program Files\Smart Remote\app.exe`,
		},
		{
			name: "quoted path with trailing arguments",
			in:   `"C:\Apps\sr.exe" --port 1234`,
			want: `C:\Apps\sr.exe`,
		},
		{
			name: "surrounding whitespace",
			in:   `  "C:\Apps\sr.exe"  `,
			want: `C:\Apps\sr.exe`,
		},
		{
			name: "unquoted path passes through",
			in:   `C:\Apps\sr.exe`,
			want: `C:\Apps\sr.exe`,
		},
		{
			name: "unterminated quote yields nothing",
			in:   `"C:\Apps\sr.exe`,
			want: ``,
		},
		{
			name: "empty input",
			in:   ``,
			want: ``,
		},
		{
			name: "whitespace only",
			in:   "   ",
			want: ``,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unquoteRunCommand(tc.in); got != tc.want {
				t.Errorf("unquoteRunCommand(%q) = %q, want %q",
					tc.in, got, tc.want)
			}
		})
	}
}

// TestStartupLabel checks the menu text reflects the action available.
//
// A fixed label beside a tick is self-contradictory - a checked "Run on Windows
// Startup" reads as though clicking would turn it on - so the two states must
// produce different strings.
func TestStartupLabel(t *testing.T) {
	off := startupLabel(false)
	on := startupLabel(true)

	if off != "Run on Windows Startup" {
		t.Errorf("startupLabel(false) = %q, want %q", off, "Run on Windows Startup")
	}
	if on == off {
		t.Errorf("startupLabel(true) = %q, must differ from the disabled label", on)
	}
	if !strings.Contains(on, "Disable") {
		t.Errorf("startupLabel(true) = %q, should say how to turn it off", on)
	}
}

// TestStartupWellFormed checks the staleness detector.
//
// A Run entry pointing at a path that no longer exists makes Windows show an
// error dialog on every sign-in, so recognising that state has to be possible
// without rebooting.
func TestStartupWellFormed(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{
			name: "existing file is well formed",
			cmd:  `"` + testExePath(t) + `"`,
			want: true,
		},
		{
			name: "missing file is rejected",
			cmd:  `"C:\definitely\not\here\smart-remote-9f3a.exe"`,
			want: false,
		},
		{
			name: "empty command is rejected",
			cmd:  ``,
			want: false,
		},
		{
			name: "unterminated quote is rejected",
			cmd:  `"C:\Apps\sr.exe`,
			want: false,
		},
		{
			name: "a directory is not an executable",
			cmd:  `"C:\Windows"`,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupEntryIsWellFormed(tc.cmd); got != tc.want {
				t.Errorf("startupEntryIsWellFormed(%q) = %v, want %v",
					tc.cmd, got, tc.want)
			}
		})
	}
}

// testExePath returns the path of the test binary, which is a real file.
func testExePath(t *testing.T) string {
	t.Helper()
	return os.Args[0]
}
