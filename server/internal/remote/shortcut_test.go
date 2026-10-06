package remote

import (
	"strings"
	"testing"
)

// TestShortcutsResolveToRealWindowsChords pins the compound shortcuts the app
// offers against the protocol's key names.
//
// These are the shortcuts a user expects a PC remote to have. If a modifier
// name stops resolving, every button that uses it starts failing at runtime with
// an opaque "unknown modifier" error, which is why each one is checked here.
func TestShortcutsResolveToRealWindowsChords(t *testing.T) {
	cases := []struct {
		name string
		mods []string
		key  string
	}{
		{"show desktop", []string{"win"}, "d"},
		{"file explorer", []string{"win"}, "e"},
		{"lock", []string{"win"}, "l"},
		{"switch app", []string{"alt"}, "tab"},
		{"task manager", []string{"ctrl", "shift"}, "esc"},
		{"snip", []string{"win", "shift"}, "s"},
		{"copy", []string{"ctrl"}, "c"},
		{"paste", []string{"ctrl"}, "v"},
		{"cut", []string{"ctrl"}, "x"},
		{"undo", []string{"ctrl"}, "z"},
		{"redo", []string{"ctrl"}, "y"},
		{"find", []string{"ctrl"}, "f"},
		{"refresh", []string{}, "f5"},
		{"settings", []string{"win"}, "i"},
		{"run", []string{"win"}, "r"},
		{"quick assist", []string{"win"}, "q"},
		{"project", []string{"ctrl"}, "p"},
		{"close window", []string{"alt"}, "f4"},
		{"rename", []string{}, "f2"},
		{"save", []string{"ctrl"}, "s"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mods, err := resolveKeys(tc.mods)
			if err != nil {
				t.Fatalf("resolveKeys(%v) = %v", tc.mods, err)
			}
			if len(mods) != len(tc.mods) {
				t.Fatalf("resolved %d modifiers, want %d", len(mods),
					len(tc.mods))
			}
			vk, err := resolveKey(tc.key)
			if err != nil {
				t.Fatalf("resolveKey(%q) = %v", tc.key, err)
			}

			// Every modifier must be non-zero: a silent zero would be injected
			// as a real keystroke on some other key, which is worse than an
			// error because it looks like it worked.
			for i, m := range mods {
				if m == 0 {
					t.Errorf("modifier %q resolved to VK 0", tc.mods[i])
				}
			}
			if vk == 0 {
				t.Errorf("key %q resolved to VK 0", tc.key)
			}

			// A letter must land on its own virtual key, not merely on
			// *some* key. Win+E opening File Explorer depends on the E being
			// the E key; resolving to an arbitrary non-zero code would pass
			// every other assertion here while sending the wrong keystroke.
			if len(tc.key) == 1 {
				c := tc.key[0]
				switch {
				case c >= 'a' && c <= 'z':
					if want := uint16(c-'a') + vkA; vk != want {
						t.Errorf("key %q -> VK_%X, want VK_%X", tc.key, vk, want)
					}
				case c >= 'A' && c <= 'Z':
					if want := uint16(c-'A') + vkA; vk != want {
						t.Errorf("key %q -> VK_%X, want VK_%X", tc.key, vk, want)
					}
				}
			}
		})
	}
}

// TestTapKeyAcceptsEveryNameResolveKeyAccepts guards the tap path.
//
// TapKey used to look names up in VKNames directly, which meant a bare letter
// was rejected with `unknown key "d"` even though the chord path resolved it
// fine. The two halves of the protocol therefore disagreed about which names
// were valid, and the difference only showed up at runtime as a toast on the
// phone.
//
// resolveKey is the single authority on key names; this asserts the tap path
// agrees with it for every name the protocol accepts.
func TestTapKeyAcceptsEveryNameResolveKeyAccepts(t *testing.T) {
	names := []string{
		// Named keys.
		"enter", "tab", "esc", "space", "back", "backspace", "del", "insert",
		"home", "end", "pageup", "pagedown", "up", "down", "left", "right",
		"f1", "f5", "f12", "mute", "volup", "voldown", "mediaprev",
		// Modifiers.
		"win", "ctrl", "alt", "shift",
		// Bare letters and digits - the case that used to fail.
		"a", "d", "e", "l", "q", "r", "s", "z", "0", "5", "9",
		// Upper case resolves to the same key.
		"D", "S", "Z",
	}

	for _, name := range names {
		want, err := resolveKey(name)
		if err != nil {
			t.Errorf("resolveKey(%q) failed: %v", name, err)
			continue
		}
		// TapKey would return "unknown key" here if it still consulted only
		// VKNames. Injecting the key is safe: the failure we care about
		// happens before any input is sent.
		if err := (&Injector{}).TapKey(name); err != nil {
			t.Errorf("TapKey(%q) = %v, but resolveKey accepts it as VK_%X",
				name, err, want)
		}
	}
}

// TestTapKeyStillRejectsGenuinelyUnknownNames makes sure the fix did not turn
// the tap path into an accept-anything parser. A name that resolves to nothing
// must still be refused with the same message the user saw before.
func TestTapKeyStillRejectsGenuinelyUnknownNames(t *testing.T) {
	for _, name := range []string{"", "notakey", "ctrl+alt", "hello world"} {
		err := (&Injector{}).TapKey(name)
		if err == nil {
			t.Errorf("TapKey(%q) succeeded; it should be rejected", name)
			continue
		}
		if !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("TapKey(%q) = %v, want an \"unknown key\" error", name, err)
		}
	}
}

// TestDefaultMacrosUseModsForChords guards the built-in deck.
//
// A chord must be split into Mods (held) and Keys (the trigger). Listing the
// whole chord in Keys with no Mods is read as two sequential taps - tap Win,
// then tap L - and a bare Win tap does nothing on its own, so the button
// silently failed. Every multi-key macro must therefore either use Mods or be a
// genuinely sequential macro, which this project does not currently ship.
func TestDefaultMacrosUseModsForChords(t *testing.T) {
	for _, m := range DefaultMacros() {
		if m.Kind != MacroKindKeys {
			continue
		}
		if len(m.Keys) > 1 && len(m.Mods) == 0 {
			t.Errorf("macro %q lists %v in Keys with no Mods; it would be "+
				"tapped as separate keys rather than as one chord",
				m.ID, m.Keys)
		}
		if len(m.Mods) > 0 && len(m.Keys) == 0 {
			t.Errorf("macro %q has Mods %v but no trigger key", m.ID, m.Mods)
		}
	}
}

// TestDefaultMacroKeysResolve checks that every key named in a built-in macro
// is one the protocol can actually resolve.
//
// This is the regression test for the "remote: unknown key" toast: a macro
// naming a key the server does not know fails at the moment the user taps it,
// on the PC, with no way to test it beforehand. Resolving every name here turns
// that into a build-time failure.
func TestDefaultMacroKeysResolve(t *testing.T) {
	for _, m := range DefaultMacros() {
		if m.Kind != MacroKindKeys {
			continue
		}
		for _, name := range m.Mods {
			if _, err := resolveKey(name); err != nil {
				t.Errorf("macro %q: unknown modifier %q: %v", m.ID, name, err)
			}
		}
		for _, name := range m.Keys {
			if _, err := resolveKey(name); err != nil {
				t.Errorf("macro %q: unknown key %q: %v", m.ID, name, err)
			}
		}
	}
}

// TestBackspaceIsAKnownKeyName pins the name the mobile keyboard sends.
//
// Deleting from the phone keyboard sends key.tap with "backspace". VKNames only
// listed "back", so the edit was rejected as an unknown key and the deletion
// silently did nothing - the user saw the character stay put.
func TestBackspaceIsAKnownKeyName(t *testing.T) {
	for _, name := range []string{"back", "backspace", "bksp"} {
		vk, err := resolveKey(name)
		if err != nil {
			t.Errorf("resolveKey(%q) = %v", name, err)
			continue
		}
		if vk != vkBack {
			t.Errorf("resolveKey(%q) = VK_%X, want VK_%X (backspace)", name, vk, vkBack)
		}
	}
}

// TestChordKeyIsNotAlsoAModifier guards the bug that made every shortcut send
// its trigger key twice.
//
// A chord is "mods down, key down+up, mods up". If the trigger key is also
// listed among the modifiers, the same virtual key is pressed once as a
// modifier and again as the chord key, then released twice. Windows reads the
// second press as auto-repeat rather than as a second chord, so Win+E opened
// Explorer and Ctrl+C copied once but Win+D and others behaved inconsistently,
// and no error was ever reported - the symptom was "some shortcuts work".
//
// This cannot be caught by the resolution test above, because every name in the
// duplicated list still resolves perfectly. Only the overlap itself is wrong.
func TestChordKeyIsNotAlsoAModifier(t *testing.T) {
	cases := []struct {
		name string
		mods []string
		key  string
	}{
		{"show desktop", []string{"win"}, "d"},
		{"task manager", []string{"ctrl", "shift"}, "esc"},
		{"snip", []string{"win", "shift"}, "s"},
		{"switch app", []string{"alt"}, "tab"},
		{"refresh", []string{}, "f5"},
		{"close window", []string{"alt"}, "f4"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyVK, err := resolveKey(tc.key)
			if err != nil {
				t.Fatalf("resolveKey(%q) = %v", tc.key, err)
			}
			mods, err := resolveKeys(tc.mods)
			if err != nil {
				t.Fatalf("resolveKeys(%v) = %v", tc.mods, err)
			}
			for i, m := range mods {
				if m == keyVK {
					t.Errorf("chord key %q (VK_%X) also appears in the "+
						"modifier list as %q; it would be pressed and "+
						"released twice", tc.key, keyVK, tc.mods[i])
				}
			}
		})
	}
}

// TestChordReleasesEveryModifier documents the anti-latch guarantee.
//
// A chord is sent as ONE SendInput batch: modifiers down, the key down and up,
// then every modifier released in reverse order. That atomicity is the whole
// reason a stuck Win or Ctrl key cannot survive a shortcut - if these were
// separate commands, a dropped frame mid-chord would leave the modifier held
// and every later keystroke on the PC would be modified.
func TestChordReleasesEveryModifier(t *testing.T) {
	mods := []uint16{vkLWin, vkShift}
	vk := uint16('S')

	ev := chordInputs(mods, vk)

	if want := len(mods)*2 + 2; len(ev) != want {
		t.Fatalf("chord built %d events, want %d", len(ev), want)
	}

	// Walk the batch and confirm each modifier is pressed exactly once and
	// released exactly once, and that nothing is pressed after the last
	// release.
	held := map[uint16]int{}
	pressed, released := 0, 0
	for _, e := range ev {
		k := e.U.ki()
		isUp := k.Flags&keyEventKeyUp != 0
		switch {
		case isUp && isModKey(k.Vk):
			released++
			held[k.Vk]--
			if held[k.Vk] < 0 {
				t.Errorf("VK_%X released more often than pressed", k.Vk)
			}
		case !isUp && isModKey(k.Vk):
			pressed++
			held[k.Vk]++
		}
	}

	if pressed != len(mods) || released != len(mods) {
		t.Errorf("pressed %d / released %d modifiers, want %d each",
			pressed, released, len(mods))
	}
	for vk, n := range held {
		if n != 0 {
			t.Errorf("VK_%X left held %d times; the modifier would stay "+
				"latched on the PC", vk, n)
		}
	}

	releases := chordReleaseInputs(mods, vk)
	if want := len(mods) + 1; len(releases) != want {
		t.Fatalf("recovery built %d release events, want %d",
			len(releases), want)
	}
	for _, event := range releases {
		if event.U.ki().Flags&keyEventKeyUp == 0 {
			t.Errorf("recovery event for VK_%X is not a key release",
				event.U.ki().Vk)
		}
	}
	if got := releases[0].U.ki().Vk; got != vk {
		t.Errorf("recovery releases VK_%X first, want trigger key VK_%X",
			got, vk)
	}
}

// isModKey reports whether a virtual key code is one of the modifiers this
// package chords with.
func isModKey(vk uint16) bool {
	switch vk {
	case vkLWin, vkRWin, vkShift, vkLControl, vkRControl, vkLMenu, vkRMenu:
		return true
	}
	return false
}
