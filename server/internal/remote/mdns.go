package remote

import (
	"fmt"
	"log"
	"sync"

	"github.com/grandcat/zeroconf"
)

// ServiceType is the DNS-SD service the phone browses for.
//
// The trailing dot is required: zeroconf treats the string as a fully
// qualified domain name, and without it the name is registered as a single
// relative label that no browser will ever match.
const ServiceType = "_smartremote._tcp.local."

// MDNSResponder advertises the server over multicast DNS so the phone can find
// it without the user typing an IP address.
//
// This used to be a hand-rolled responder that encoded the PTR/SRV/TXT/A
// records by hand. It could never work on a normal Windows machine: UDP 5353
// is already bound by svchost (the system mDNS/DNS-SD service), so every
// net.ListenUDP attempt failed with WSAEACCES and discovery silently degraded
// to "no private IPv4 interface available" - a message that blamed the
// network when the port was the real cause.
//
// zeroconf solves this by setting SO_REUSEADDR/SO_REUSEPORT so its socket
// coexists with the system responder rather than fighting it for the port.
type MDNSResponder struct {
	mu      sync.Mutex
	port    int
	pin     string
	name    string
	host    string
	logger  *log.Logger
	running bool
	server  *zeroconf.Server
}

// NewMDNSResponder prepares a responder. Start registers the service.
func NewMDNSResponder(name, host string, port int, pin string, logger *log.Logger) *MDNSResponder {
	if logger == nil {
		logger = log.Default()
	}
	return &MDNSResponder{name: name, host: host, port: port, pin: pin, logger: logger}
}

// Active reports whether the service is registered and advertising.
func (m *MDNSResponder) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// serviceInstance returns the DNS-SD instance name.
func (m *MDNSResponder) serviceInstance() string {
	return fmt.Sprintf("%s.%s", m.name, ServiceType)
}

// Start registers the service with the multicast DNS responder.
func (m *MDNSResponder) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil
	}

	// The PIN travels in the TXT record so a discovered device can be paired
	// in one step. This is deliberate: mDNS only reaches the local network,
	// which is the same trust boundary as the PIN-protected API itself.
	txt := []string{
		"pin=" + m.pin,
		"port=" + fmt.Sprint(m.port),
		"proto=" + ProtocolVersion,
		"name=" + m.name,
	}

	server, err := zeroconf.Register(
		m.name,
		ServiceType,
		"local.",
		m.port,
		txt,
		// A nil interface list makes zeroconf bind on every usable adapter,
		// which is what is wanted: the phone is on Wi-Fi while the PC may also
		// have a VPN or virtual adapter up.
		nil,
	)
	if err != nil {
		return fmt.Errorf("remote: mDNS registration failed: %w", err)
	}

	m.server = server
	m.running = true
	m.logger.Printf("mDNS: advertising %q on port %d as %q",
		m.name, m.port, ServiceType)
	return nil
}

// Stop unregisters the service.
func (m *MDNSResponder) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	server := m.server
	m.server = nil
	m.mu.Unlock()

	if server != nil {
		server.Shutdown()
	}
	m.logger.Printf("mDNS: stopped")
}

// sanitizeLabel strips characters that are not valid in a DNS label.

// sanitizeLabel strips characters that are not valid in a DNS label.
func sanitizeLabel(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9', ch == '-', ch == '_':
			b = append(b, ch)
		case ch == ' ':
			b = append(b, '-')
		}
	}
	if len(b) == 0 {
		return "device"
	}
	if len(b) > 63 {
		b = b[:63]
	}
	return string(b)
}
