package remote

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// MacroKind selects how a macro body is executed.
type MacroKind string

const (
	// MacroKindKeys replays a sequence of keystrokes, optionally with
	// modifiers held across the whole sequence.
	MacroKindKeys MacroKind = "keys"
	// MacroKindShell runs a command through the system shell.
	MacroKindShell MacroKind = "shell"
)

// Macro is a named, reusable action shown on the Macro Deck screen.
//
// Every macro carries both Icon and Label. Icons alone are not a reliable
// signal: the icon font can fail to render on a device, and a control with
// no readable text is unusable for anyone who cannot interpret the glyph.
// The label is therefore the authoritative identifier and the icon is
// decorative.
type Macro struct {
	ID      string    `json:"id"`
	Label   string    `json:"label"`
	Icon    string    `json:"icon"`
	Kind    MacroKind `json:"kind"`
	Keys    []string  `json:"keys,omitempty"`
	Mods    []string  `json:"mods,omitempty"`
	Shell   string    `json:"shell,omitempty"`
	BuiltIn bool      `json:"builtIn"`
}

// MacroStore holds the macro list, persisted as JSON next to the config file
// so user edits survive restarts.
type MacroStore struct {
	mu    sync.RWMutex
	items []Macro
	path  string
}

// DefaultMacros returns the built-in deck shipped with the app.
func DefaultMacros() []Macro {
	return []Macro{
		{ID: "lock", Label: "Lock", Icon: "lock", Kind: MacroKindKeys, Keys: []string{"win", "l"}, BuiltIn: true},
		{ID: "sleep", Label: "Sleep", Icon: "moon", Kind: MacroKindKeys, Keys: []string{"win", "x"}, BuiltIn: true},
		{ID: "spotify", Label: "Spotify", Icon: "music", Kind: MacroKindKeys, Keys: []string{"ctrl", "alt", "s"}, BuiltIn: true},
		{ID: "mute", Label: "Mute", Icon: "volume_off", Kind: MacroKindKeys, Keys: []string{"mute"}, BuiltIn: true},
		{ID: "copy", Label: "Copy", Icon: "content_copy", Kind: MacroKindKeys, Keys: []string{"ctrl", "c"}, BuiltIn: true},
		{ID: "paste", Label: "Paste", Icon: "content_paste", Kind: MacroKindKeys, Keys: []string{"ctrl", "v"}, BuiltIn: true},
		{ID: "explorer", Label: "Files", Icon: "folder", Kind: MacroKindKeys, Keys: []string{"win", "e"}, BuiltIn: true},
		{ID: "desktop", Label: "Desktop", Icon: "desktop", Kind: MacroKindKeys, Keys: []string{"win", "d"}, BuiltIn: true},
		{ID: "taskmgr", Label: "Task Mgr", Icon: "monitor", Kind: MacroKindKeys, Keys: []string{"ctrl", "shift", "esc"}, BuiltIn: true},
		{ID: "open_files", Label: "Open Files", Icon: "terminal", Kind: MacroKindShell, Shell: "explorer.exe", BuiltIn: true},
	}
}

// NewMacroStore creates a store seeded with the defaults and backed by path.
func NewMacroStore(path string) (*MacroStore, error) {
	s := &MacroStore{path: path, items: DefaultMacros()}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // defaults only, nothing persisted yet
		}
		return nil, fmt.Errorf("remote: read macros: %w", err)
	}
	var saved []Macro
	if err := json.Unmarshal(data, &saved); err != nil {
		// A corrupt file must not stop the server from starting: fall back
		// to defaults so the user is not locked out of their own machine.
		return s, fmt.Errorf("remote: macros file is corrupt, using defaults: %w", err)
	}
	if len(saved) > 0 {
		s.items = mergeMacros(s.items, saved)
	}
	return s, nil
}

// mergeMacros keeps built-in macros in their canonical order and appends the
// user's own macros after them.
func mergeMacros(base, saved []Macro) []Macro {
	byID := make(map[string]Macro, len(saved))
	for _, m := range saved {
		byID[m.ID] = m
	}
	out := make([]Macro, 0, len(base)+len(saved))
	seen := map[string]bool{}
	for _, m := range base {
		if custom, ok := byID[m.ID]; ok && !custom.BuiltIn {
			out = append(out, custom)
		} else {
			out = append(out, m)
		}
		seen[m.ID] = true
	}
	extra := make([]Macro, 0, len(saved))
	for _, m := range saved {
		if !seen[m.ID] && !m.BuiltIn {
			extra = append(extra, m)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Label < extra[j].Label })
	return append(out, extra...)
}

// List returns a copy of the macro list.
func (s *MacroStore) List() []Macro {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Macro, len(s.items))
	copy(out, s.items)
	return out
}

// Get returns a macro by ID.
func (s *MacroStore) Get(id string) (Macro, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.items {
		if m.ID == id {
			return m, true
		}
	}
	return Macro{}, false
}

// Add appends a user macro and persists the store.
func (s *MacroStore) Add(m Macro) error {
	if m.Label == "" {
		return fmt.Errorf("remote: macro requires a label")
	}
	if m.Kind == "" {
		m.Kind = MacroKindKeys
	}
	if m.Kind == MacroKindShell && m.Shell == "" {
		return fmt.Errorf("remote: shell macro requires a command")
	}
	if m.Kind == MacroKindKeys && len(m.Keys) == 0 {
		return fmt.Errorf("remote: key macro requires at least one key")
	}
	if m.ID == "" {
		m.ID = "user_" + sanitizeID(m.Label)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.items {
		if existing.ID == m.ID {
			return fmt.Errorf("remote: macro %q already exists", m.ID)
		}
	}
	s.items = append(s.items, m)
	return s.persistLocked()
}

// Remove deletes a user macro. Built-in macros cannot be removed, only
// overridden by redefining them with the same ID.
func (s *MacroStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.items {
		if m.ID == id {
			if m.BuiltIn {
				return fmt.Errorf("remote: cannot remove built-in macro %q", id)
			}
			s.items = append(s.items[:i], s.items[i+1:]...)
			return s.persistLocked()
		}
	}
	return fmt.Errorf("remote: no macro with id %q", id)
}

// persistLocked writes the macro list to disk. Callers must hold the lock.
//
// Only user macros are persisted; built-ins are code, not configuration.
func (s *MacroStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("remote: create config dir: %w", err)
	}
	var user []Macro
	for _, m := range s.items {
		if !m.BuiltIn {
			user = append(user, m)
		}
	}
	data, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		return fmt.Errorf("remote: encode macros: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("remote: write macros: %w", err)
	}
	// Write to a temp file and rename, so a crash mid-write cannot leave a
	// truncated macros file behind.
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("remote: replace macros: %w", err)
	}
	return nil
}

// sanitizeID turns a label into a safe identifier fragment.
func sanitizeID(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			b = append(b, ch)
		case ch >= 'A' && ch <= 'Z':
			b = append(b, ch+('a'-'A'))
		case ch == ' ' || ch == '-' || ch == '_':
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "macro"
	}
	if len(b) > 32 {
		b = b[:32]
	}
	return string(b)
}
