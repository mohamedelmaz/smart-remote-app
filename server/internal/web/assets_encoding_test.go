package web

import (
	"io/fs"
	"strings"
	"testing"
)

// mojibake pairs each intended character with the sequence a UTF-8 file turns
// into when its bytes are decoded as Windows-1252 and then saved as UTF-8
// again.
//
// The dashboard assets are embedded in the binary and are therefore read by the
// browser exactly as they sit in this repository. That is what makes the
// failure mode worth a test: a double encoding committed into the source is
// invisible to the Go compiler, survives every rebuild, and is shipped to the
// user as mojibake ("1920Ã—1080") that no server-side header can correct.
//
// Both columns are written as escapes so this test file cannot itself be the
// thing that gets re-encoded.
var mojibake = []struct {
	want string // the character that was meant
	got  string // what the double encoding leaves behind
}{
	{"\u2014", "\u00e2\u20ac\u201d"}, // em dash
	{"\u00d7", "\u00c3\u2014"},      // multiplication sign
	{"\u00b7", "\u00c2\u00b7"},      // middle dot
}

// TestEmbeddedAssetsCarryNoMojibake fails if any text asset shipped in the
// binary still holds a double-encoded sequence.
func TestEmbeddedAssetsCarryNoMojibake(t *testing.T) {
	err := fs.WalkDir(Assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".html"),
			strings.HasSuffix(path, ".css"),
			strings.HasSuffix(path, ".js"):
		default:
			return nil // favicon.png and friends are binary
		}

		raw, readErr := Assets.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(raw)
		for _, m := range mojibake {
			if strings.Contains(body, m.got) {
				t.Errorf("%s contains the Windows-1252 mojibake %q; the intended "+
					"character is %q and must be written as a \\u escape",
					path, m.got, m.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded assets: %v", err)
	}
}

// TestDashboardEscapesItsNonASCIICharacters guards the fix from silently
// regressing: the characters must be present as JS escapes, not as literal
// bytes, or the next editor that saves in a different code page reintroduces
// the whole problem.
func TestDashboardEscapesItsNonASCIICharacters(t *testing.T) {
	raw, err := Assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the dashboard script: %v", err)
	}
	body := string(raw)

	for _, esc := range []string{`\u2014`, `\u00d7`, `\u00b7`} {
		if !strings.Contains(body, esc) {
			t.Errorf("app.js does not use the %s escape; a literal character here "+
				"is what the mojibake came from", esc)
		}
	}

	// No non-ASCII byte may survive in the script at all: every one of them is
	// a character that can be re-encoded by a careless save.
	for i := 0; i < len(body); i++ {
		if body[i] > 0x7f {
			t.Fatalf("app.js still carries the non-ASCII byte 0x%02x at offset %d; "+
				"write the character as a \\u escape instead", body[i], i)
		}
	}
}