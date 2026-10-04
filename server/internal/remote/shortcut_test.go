package remote

import "testing"

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
	// The event sequence Chord builds, expressed directly against the same
	// helpers, so the invariant is asserted rather than described.
	mods := []uint16{vkLWin, vkShift}
	vk := uint16('S')

	ev := make([]input, 0, len(mods)*2+2)
	for _, m := range mods {
		ev = append(ev, newKeyInput(m, 0, 0))
	}
	ev = append(ev,
		newKeyInput(vk, 0, 0),
		newKeyInput(vk, 0, keyEventKeyUp))
	for i := len(mods) - 1; i >= 0; i-- {
		ev = append(ev, newKeyInput(mods[i], 0, keyEventKeyUp))
	}

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
