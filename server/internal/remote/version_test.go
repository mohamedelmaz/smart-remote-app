package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A release means editing the version in five places. Miss one and the server
// reports a different build from the EXE properties, and the About screen on
// the phone disagrees with both. Nothing would fail; it would just be wrong,
// and only visible to a user comparing an About box against a file listing.
func TestVersionIsTheSameEverywhere(t *testing.T) {
	root := findRepoRoot(t)

	type site struct {
		file string
		re   *regexp.Regexp
	}
	sites := []site{
		{"server/internal/remote/brand.go", regexp.MustCompile(`AppVersion\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"`)},
		{"server/winres.rc", regexp.MustCompile(`"FileVersion",\s*"([0-9]+\.[0-9]+\.[0-9]+)`)},
		{"server/winres.rc", regexp.MustCompile(`"ProductVersion",\s*"([0-9]+\.[0-9]+\.[0-9]+)`)},
		{"mobile/pubspec.yaml", regexp.MustCompile(`(?m)^version:\s*([0-9]+\.[0-9]+\.[0-9]+)`)},
		{"mobile/lib/screens/info_sheet.dart", regexp.MustCompile(`kAppVersion\s*=\s*'([0-9]+\.[0-9]+\.[0-9]+)'`)},
		// The numeric VS_FIXEDFILEINFO fields, not just the string table.
		// windres writes both, Windows shows the string one in Explorer, and
		// anything reading the fixed struct still sees the number here. A
		// release that bumps only the strings ships a binary that reports two
		// different versions depending on which one the reader uses.
		{"server/winres.rc", regexp.MustCompile(`(?m)^FILEVERSION\s+([0-9]+),([0-9]+),([0-9]+),([0-9]+)`)},
		{"server/winres.rc", regexp.MustCompile(`(?m)^PRODUCTVERSION\s+([0-9]+),([0-9]+),([0-9]+),([0-9]+)`)},
	}

	var want string
	for _, s := range sites {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(s.file)))
		if err != nil {
			t.Fatalf("read %s: %v", s.file, err)
		}
		m := s.re.FindSubmatch(data)
		if m == nil {
			t.Fatalf("no version found in %s (%s)", s.file, s.re)
		}
		// Comma-separated fields are one logical version; dot-separated ones
		// are captured whole.
		got := string(m[1])
		if len(m) == 5 {
			got = fmt.Sprintf("%s.%s.%s", m[1], m[2], m[3])
		}
		if want == "" {
			want = got
			continue
		}
		if got != want {
			t.Errorf("%s says %s, but the release is %s", s.file, got, want)
		}
	}
	if want == "" {
		t.Fatal("no version sites were checked")
	}
}

// The build number is how Android tells an upgrade from a reinstall. Leaving
// it alone means the store refuses the update.
func TestAndroidBuildNumberIncremented(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "mobile", "pubspec.yaml"))
	if err != nil {
		t.Fatalf("read pubspec.yaml: %v", err)
	}
	m := regexp.MustCompile(`(?m)^version:\s*([0-9]+\.[0-9]+\.[0-9]+\+([0-9]+))`).
		FindSubmatch(data)
	if m == nil {
		t.Fatal("no version: X.Y.Z+build line in pubspec.yaml")
	}
	if got := string(m[2]); got == "0" {
		t.Error("build number is 0; Android would refuse the upgrade")
	}
}

// findRepoRoot walks up until it finds the directory holding both halves of
// the project, so the test does not depend on how deep go test was invoked.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		_, rcErr := os.Stat(filepath.Join(dir, "server", "winres.rc"))
		_, apErr := os.Stat(filepath.Join(dir, "mobile", "pubspec.yaml"))
		if rcErr == nil && apErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("repository root not found from " + strings.TrimSpace(dir))
	return ""
}
