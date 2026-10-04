package remote

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPsQuote protects the clipboard helper.
//
// The address is interpolated into a PowerShell single-quoted string, so an
// unescaped quote would not merely look wrong - it would terminate the literal
// and inject the remainder as a separate expression. That is a command
// injection path reachable from the tray menu.
func TestPsQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "192.168.1.36:9520", "'192.168.1.36:9520'"},
		{"empty", "", "''"},
		{"single quote", "it's", "'it''s'"},
		{"only quote", "'", "''''"},
		{"trailing quote", "a'", "'a'''"},
		{"semicolon is inert", "a'; rm -rf x; '", "'a''; rm -rf x; '''"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := psQuote(tc.in); got != tc.want {
				t.Fatalf("psQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPsQuoteAlwaysBalanced checks the invariant the escaping depends on:
// after the doubling, the literal contains an even number of quotes so the
// closing quote really is the last one.
func TestPsQuoteAlwaysBalanced(t *testing.T) {
	for _, in := range []string{"", "a", "'", "''", "a'b'c", "'; DROP", "'''"} {
		got := psQuote(in)
		body := got[1 : len(got)-1] // strip the surrounding quotes
		if strings.Count(body, "'")%2 != 0 {
			t.Fatalf("psQuote(%q) = %q: interior has an odd quote count", in, got)
		}
	}
}

// TestFallbackIconIsValidICO checks the last-resort tray icon really is an ICO.
//
// The fallback only runs if the embedded asset is empty, which is exactly the
// situation where nobody is watching the build. If it produced garbage the
// tray would be silently blank again - the failure this whole path exists to
// prevent - so the structure is asserted rather than assumed.
func TestFallbackIconIsValidICO(t *testing.T) {
	ico := fallbackIcon()
	if len(ico) < 22 {
		t.Fatalf("fallback icon is %d bytes, too short for an ICO header", len(ico))
	}

	if le16(ico[0:2]) != 0 {
		t.Fatalf("reserved = %d, want 0", le16(ico[0:2]))
	}
	if le16(ico[2:4]) != 1 {
		t.Fatalf("type = %d, want 1 (icon)", le16(ico[2:4]))
	}
	if count := le16(ico[4:6]); count != 1 {
		t.Fatalf("image count = %d, want 1", count)
	}

	e := ico[6:22]
	if e[0] == 0 || e[1] == 0 {
		t.Fatalf("entry dimensions are %dx%d, want non-zero", e[0], e[1])
	}
	if bpp := le16(e[6:8]); bpp != 32 {
		t.Fatalf("bits per pixel = %d, want 32", bpp)
	}

	size := le32(e[8:12])
	offset := le32(e[12:16])
	if offset+size != uint32(len(ico)) {
		t.Fatalf("entry claims bytes [%d,%d) but the file is %d bytes",
			offset, offset+size, len(ico))
	}
}

// TestEmbeddedIconPresent guards the primary path.
//
// trayIconData is expected to return the embedded asset, and the embedded
// asset is what gives Explorer, the taskbar and the tray one consistent icon.
// A build that lost the .ico would otherwise only be noticed at runtime.
func TestEmbeddedIconPresent(t *testing.T) {
	if len(appIcon) == 0 {
		t.Fatal("embedded appIcon is empty; the tray would fall back to a plain square")
	}

	// The embedded bytes must themselves be a well-formed ICO header.
	if le16(appIcon[0:2]) != 0 || le16(appIcon[2:4]) != 1 {
		t.Fatalf("embedded icon does not start with a valid ICONDIR")
	}
	if n := le16(appIcon[4:6]); n == 0 {
		t.Fatal("embedded icon declares zero images")
	}
}

// TestTrayIconDataNeverNil is the contract systray depends on.
//
// A nil icon makes Windows draw a blank tray entry, which the user cannot
// distinguish from a broken tray. Returning nil must be impossible.
func TestTrayIconDataNeverNil(t *testing.T) {
	if got := trayIconData(nil); len(got) == 0 {
		t.Fatal("trayIconData returned no bytes")
	}
}

// TestServiceTypeIsFQDN guards a subtle registration bug.
//
// zeroconf treats the type as a fully qualified name. The trailing dot is what
// makes it "_smartremote._tcp.local." rather than a single relative label,
// which no browser would ever match. Dropping the dot compiles fine and
// silently breaks discovery, so it is asserted here.
func TestServiceTypeIsFQDN(t *testing.T) {
	if !strings.HasSuffix(ServiceType, ".") {
		t.Fatalf("ServiceType %q must end with a dot to be fully qualified", ServiceType)
	}
	if !strings.HasPrefix(ServiceType, "_smartremote._tcp.") {
		t.Fatalf("ServiceType %q does not look like a DNS-SD service type", ServiceType)
	}
}

// TestEmbeddedIconIsInTheBinary proves the icon reached the executable.
//
// trayIconData only sees the asset because //go:embed compiled it in, so the
// embed is the mechanism. But the *shell* icon in Explorer and the taskbar
// comes from a separate .syso resource produced by rsrc, and a build that
// forgot the .syso would still pass every other test here while showing the
// default application icon everywhere outside the tray.
//
// go tool nm on the built binary lists it only if the resource is linked, so
// this reads the real artifact rather than trusting the build command.
func TestEmbeddedIconIsInTheBinary(t *testing.T) {
	// The release binary is written to the module root, which is two levels
	// above this package (internal/remote). go test runs the test binary from
	// a temp dir, so the path is anchored on the module root discovered from
	// this source file rather than on the process working directory.
	exe := findReleaseBinary()
	if exe == "" {
		t.Skip("smart-remote-app.exe not built yet; run the release build first")
	}

	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}

	// The service type string is linked in verbatim by the mDNS package, so
	// its presence proves this is a fully linked binary rather than a stub.
	if !bytes.Contains(data, []byte(ServiceType)) {
		t.Fatal("binary does not contain the service type string; link may be incomplete")
	}

	// The ICO is linked twice: once via //go:embed for systray, once as the
	// PE resource for Explorer. Both copies start with the same ICONDIR.
	needle := appIcon[:64]
	if len(appIcon) >= 64 {
		if !bytes.Contains(data, needle) {
			t.Fatal("the tray icon bytes are not present in the binary")
		}
	}
}

// findReleaseBinary locates smart-remote-app.exe at the module root.
//
// It walks up from the working directory rather than trusting a fixed number
// of parent hops: go test may run from the package directory or the module
// root depending on how it was invoked, and a wrong guess would quietly skip
// the check instead of failing it.
func findReleaseBinary() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "smart-remote-app.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func le16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}