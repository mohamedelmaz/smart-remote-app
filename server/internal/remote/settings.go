package remote

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsFileName is the file the lock settings live in, inside the same
// config directory as the PIN and the block list.
const SettingsFileName = "settings.json"

// DefaultLockIterations is the PBKDF2 work factor used for a lock code.
const DefaultLockIterations = 200000

// Settings is the persisted, versioned configuration file.
//
// The lock lives here rather than beside the PIN so that the two are
// completely independent: the lock code is never derived from the pairing PIN,
// and changing one never touches the other.
type Settings struct {
	Version int          `json:"version"`
	Lock    LockSettings `json:"lock"`
}

// LockSettings is the persisted lock state. An empty Salt or Hash means no
// code has been set, which is how a fresh install reads as "off".
type LockSettings struct {
	Enabled    bool   `json:"enabled"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

// DefaultSettings returns the state a machine with no settings file is in:
// every feature behaves exactly as it did before the lock existed.
func DefaultSettings() *Settings {
	return &Settings{Version: 1}
}

// lockConfigured reports whether this settings file holds a usable code.
func (s *Settings) lockConfigured() bool {
	return s.Lock.Salt != "" && s.Lock.Hash != "" && s.Lock.Iterations > 0
}

// LoadSettings reads path, tolerating every failure the way the block list
// does: a corrupt file must never keep the app from starting.
//
// A corrupt file is renamed rather than deleted, so the owner can still look
// at what was in it. The returned flag reports that this happened.
func LoadSettings(path string, logger interface{ Printf(string, ...any) }) (*Settings, bool) {
	s := DefaultSettings()
	if path == "" {
		return s, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) && logger != nil {
			logger.Printf("settings: cannot read %s (%v); using defaults", path, err)
		}
		return s, false
	}
	var loaded Settings
	if err := json.Unmarshal(raw, &loaded); err != nil || loaded.Version != 1 {
		backup := path + ".corrupt"
		if err := os.Rename(path, backup); err == nil && logger != nil {
			logger.Printf("settings: %s is corrupt; moved to %s and the lock is off",
				path, backup)
		} else if logger != nil {
			logger.Printf("settings: %s is corrupt and could not be moved aside (%v); "+
				"the lock is off", path, err)
		}
		return DefaultSettings(), true
	}
	if loaded.Lock.Enabled && !loaded.lockConfigured() {
		// An enabled lock with no stored code could never be opened again, so
		// it is downgraded rather than locking the owner out of their machine.
		if logger != nil {
			logger.Printf("settings: the lock was enabled without a stored code; " +
				"treating it as off")
		}
		return DefaultSettings(), false
	}
	if loaded.Lock.Iterations == 0 {
		loaded.Lock.Iterations = DefaultLockIterations
	}
	return &loaded, false
}

// SaveSettings writes the file atomically: a temporary file plus a rename, so
// a crash mid-write can never leave a truncated settings file behind.
func SaveSettings(path string, s *Settings) error {
	if path == "" {
		return fmt.Errorf("remote: no settings path")
	}
	s.Version = 1
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("remote: create settings dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("remote: encode settings: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("remote: write settings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("remote: replace settings: %w", err)
	}
	return nil
}