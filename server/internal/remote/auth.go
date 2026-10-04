//go:build windows

package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PinLength is the number of digits in a pairing PIN.
const PinLength = 6

// Auth manages the pairing PIN and the authenticated client set.
//
// The PIN is the only thing standing between an unauthenticated device on
// the local network and full control of the PC's keyboard and mouse, so
// comparisons are constant-time and failures are rate limited.
type Auth struct {
	mu       sync.RWMutex
	pin      string
	path     string
	failures map[string]*failureRecord
	maxFails int
	lockout  time.Duration
}

// failureRecord tracks consecutive auth failures from one address.
type failureRecord struct {
	count int
	until time.Time
}

// NewAuth loads the PIN from path, generating and persisting one if absent.
func NewAuth(path string) (*Auth, error) {
	a := &Auth{
		path:     path,
		failures: map[string]*failureRecord{},
		maxFails: 5,
		lockout:  30 * time.Second,
	}

	pin, err := loadPIN(path)
	if err != nil {
		return nil, err
	}
	if pin == "" {
		pin, err = GeneratePIN()
		if err != nil {
			return nil, err
		}
		if err := savePIN(path, pin); err != nil {
			return nil, err
		}
	}
	a.pin = pin
	return a, nil
}

// GeneratePIN returns a cryptographically random numeric PIN.
//
// crypto/rand is used rather than math/rand: the PIN is the only barrier to
// unauthorised input injection, so a predictable one is not acceptable.
func GeneratePIN() (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(PinLength), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("remote: generate PIN: %w", err)
	}
	// Left-pad so the PIN always has exactly PinLength digits, which the
	// dashboard displays with a fixed-width font.
	s := n.String()
	for len(s) < PinLength {
		s = "0" + s
	}
	return s, nil
}

// Pin returns the current PIN.
func (a *Auth) Pin() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pin
}

// Regenerate creates and persists a new PIN, invalidating every existing
// session.
func (a *Auth) Regenerate() (string, error) {
	pin, err := GeneratePIN()
	if err != nil {
		return "", err
	}
	if err := savePIN(a.path, pin); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.pin = pin
	a.mu.Unlock()
	return pin, nil
}

// Verify checks a presented PIN with a constant-time comparison and applies
// lockout after repeated failures.
func (a *Auth) Verify(addr, presented string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rec, ok := a.failures[addr]; ok && time.Now().Before(rec.until) {
		return fmt.Errorf("remote: too many failed attempts, retry in %s",
			time.Until(rec.until).Round(time.Second))
	}

	// Constant-time compare avoids leaking the PIN one digit at a time.
	if subtle.ConstantTimeCompare([]byte(a.pin), []byte(presented)) != 1 {
		rec := a.failures[addr]
		if rec == nil {
			rec = &failureRecord{}
			a.failures[addr] = rec
		}
		rec.count++
		if rec.count >= a.maxFails {
			rec.until = time.Now().Add(a.lockout)
			rec.count = 0
			return fmt.Errorf("remote: too many failed attempts, locked for %s", a.lockout)
		}
		return errors.New("remote: incorrect PIN")
	}

	delete(a.failures, addr)
	return nil
}

// loadPIN reads a persisted PIN, returning "" when none exists.
func loadPIN(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("remote: read PIN: %w", err)
	}
	pin := strings.TrimSpace(string(data))
	if len(pin) != PinLength || !isAllDigits(pin) {
		return "", fmt.Errorf("remote: PIN file %s is malformed", path)
	}
	return pin, nil
}

// savePIN writes the PIN with owner-only permissions.
func savePIN(path, pin string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("remote: create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(pin), 0o600); err != nil {
		return fmt.Errorf("remote: write PIN: %w", err)
	}
	return nil
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
