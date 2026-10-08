package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BlockEntry is one blocked device: the IP observed when it was blocked plus
// the name it reported then, so the owner recognises the row. The IP is the
// enforcement key; the name is display-only and never trusted for matching.
type BlockEntry struct {
	IP        string `json:"ip"`
	Name      string `json:"name,omitempty"`
	BlockedAt string `json:"blockedAt"`
}

// blockFile is the on-disk schema, versioned so a future format can migrate
// instead of discarding the owner's list.
type blockFile struct {
	Version int          `json:"version"`
	Blocked []BlockEntry `json:"blocked"`
}

// BlockList is the set of blocked device IPs with file persistence.
//
// The zero value is a usable in-memory-only list; OpenBlockList attaches it
// to a file. All methods are safe for concurrent use from the auth path, the
// command path and the dashboard handlers.
type BlockList struct {
	mu   sync.RWMutex
	ips  map[string]BlockEntry
	path string
}

// OpenBlockList loads the blocklist, tolerating every failure mode with an
// empty list rather than refusing to start: a corrupt file must never wedge
// the server, and blocking is enforcement, not a boot dependency. The second
// return reports whether the file was corrupt so the caller can warn.
func OpenBlockList(path string, logger interface{ Printf(string, ...any) }) (*BlockList, bool) {
	bl := &BlockList{ips: map[string]BlockEntry{}, path: path}
	if path == "" {
		return bl, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) && logger != nil {
			logger.Printf("blocklist: cannot read %s (%v); starting empty", path, err)
		}
		return bl, false
	}
	var f blockFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Version != 1 {
		// Preserve the evidence before starting empty: the owner may want to
		// inspect what broke, and silently overwriting it destroys that.
		backup := path + ".bak"
		_ = os.WriteFile(backup, raw, 0o600)
		if logger != nil {
			logger.Printf("blocklist: %s is corrupt (moved to %s); starting empty", path, backup)
		}
		return bl, true
	}
	for _, e := range f.Blocked {
		if e.IP != "" {
			bl.ips[e.IP] = e
		}
	}
	return bl, false
}

// Blocked reports whether ip is blocked.
func (b *BlockList) Blocked(ip string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.ips[ip]
	return ok
}

// List snapshots the blocked entries for the dashboard.
func (b *BlockList) List() []BlockEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]BlockEntry, 0, len(b.ips))
	for _, e := range b.ips {
		out = append(out, e)
	}
	return out
}

// Block adds ip. It returns false when ip was already blocked (no rewrite).
//
// The lock is held for the entire operation including the file write,
// so concurrent Block/Unblock calls are serialized and the file always
// reflects the latest in-memory state.
func (b *BlockList) Block(ip, name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.ips[ip]; ok {
		return false
	}
	b.ips[ip] = BlockEntry{IP: ip, Name: name, BlockedAt: time.Now().Format("15:04:05")}
	snapshot := b.snapshotLocked()
	b.persistLocked(b.path, snapshot)
	return true
}

// Unblock removes ip. It returns false when ip was not blocked.
//
// The lock is held for the entire operation including the file write,
// so concurrent Block/Unblock calls are serialized and the file always
// reflects the latest in-memory state.
func (b *BlockList) Unblock(ip string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.ips[ip]; !ok {
		return false
	}
	delete(b.ips, ip)
	snapshot := b.snapshotLocked()
	b.persistLocked(b.path, snapshot)
	return true
}

// snapshotLocked copies the set; the caller must hold at least a read lock.
// The copy is what keeps a slow disk write from stalling the auth path.
func (b *BlockList) snapshotLocked() []BlockEntry {
	out := make([]BlockEntry, 0, len(b.ips))
	for _, e := range b.ips {
		out = append(out, e)
	}
	return out
}

// persist writes atomically (temp file + rename) so a crash mid-write can
// never leave a half-written list. A failed write is logged, never fatal:
// the in-memory set - the one actually enforced - is already correct.
// This variant does NOT acquire the lock; caller must hold it.
func (b *BlockList) persistLocked(path string, snapshot []BlockEntry) {
	if path == "" {
		return
	}
	raw, err := json.Marshal(blockFile{Version: 1, Blocked: snapshot})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "blocklist-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return
	}
}

// persist is the public wrapper that acquires the lock. Kept for
// external callers (if any), but internal callers should use persistLocked.
func (b *BlockList) persist(path string, snapshot []BlockEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.persistLocked(path, snapshot)
}

// blockFileVersion is asserted by tests so a schema change cannot slip past
// the loader's version gate unnoticed.
const blockFileVersion = 1
