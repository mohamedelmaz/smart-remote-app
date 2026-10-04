package remote

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// InterfaceInfo describes one network adapter.
type InterfaceInfo struct {
	Name  string `json:"name"`
	Addr  string `json:"address"`
	Net   string `json:"cidr"`
	Up    bool   `json:"up"`
	Multi bool   `json:"multicast"`
	Loop  bool   `json:"loopback"`
	// Private marks RFC 1918 / CGNAT addresses that a phone on the same
	// network can actually reach.
	Private bool `json:"private"`
}

// NetDiagnostics is the /api/net payload.
//
// It exists because "the phone cannot connect" is almost always one of three
// things: the wrong address, a firewall rule that was never created, or a
// network profile that blocks mDNS. Reporting all three explicitly is far
// faster than guessing from the phone side.
type NetDiagnostics struct {
	Hostname      string          `json:"hostname"`
	PrimaryIP     string          `json:"primaryIp"`
	Port          int             `json:"port"`
	ListenAddr    string          `json:"listenAddr"`
	Interfaces    []InterfaceInfo `json:"interfaces"`
	FirewallOK    bool            `json:"firewallOk"`
	FirewallNote  string          `json:"firewallNote"`
	ProfileName   string          `json:"networkProfile,omitempty"`
	ProfileIsPriv bool            `json:"networkProfileIsPrivate"`
	MDNSActive    bool            `json:"mdnsActive"`
	Advice        []string        `json:"advice"`
	Generated     time.Time       `json:"generatedAt"`
}

// handleNet returns network diagnostics and actionable advice.
func (s *Server) handleNet(w http.ResponseWriter, r *http.Request) {
	diag := s.diagnoseNetwork()
	writeJSON(w, http.StatusOK, diag)
}

// diagnoseNetwork gathers the diagnostics payload.
func (s *Server) diagnoseNetwork() NetDiagnostics {
	host, _ := os.Hostname()
	diag := NetDiagnostics{
		Hostname:   host,
		PrimaryIP:  s.primaryIPv4(),
		Port:       s.port,
		ListenAddr: fmt.Sprintf("0.0.0.0:%d", s.port),
		Generated:  time.Now(),
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		diag.Advice = append(diag.Advice,
			"Could not enumerate network interfaces: "+err.Error())
		return diag
	}

	for _, ifc := range ifaces {
		info := InterfaceInfo{
			Name:  ifc.Name,
			Up:    ifc.Flags&net.FlagUp != 0,
			Multi: ifc.Flags&net.FlagMulticast != 0,
			Loop:  ifc.Flags&net.FlagLoopback != 0,
		}
		// Addresses are read from the interface object itself, which is the
		// reliable way to associate an address with its interface name.
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ipv4 := ipnet.IP.To4()
			if ipv4 == nil {
				continue
			}
			// Keep the first IPv4 address reported per interface.
			if info.Addr == "" {
				info.Addr = ipv4.String()
				info.Net = ipnet.String()
				info.Private = isPrivate(ipv4)
			}
		}
		diag.Interfaces = append(diag.Interfaces, info)
	}

	diag.FirewallOK, diag.FirewallNote = CheckFirewallRule(s.port)
	diag.ProfileName, diag.ProfileIsPriv = currentNetworkProfile()
	diag.MDNSActive = s.mdns != nil && s.mdns.Active()

	// Turn the raw findings into concrete next steps.
	switch {
	case diag.FirewallOK:
		diag.Advice = append(diag.Advice,
			"Firewall: an inbound rule already exists for this port.")
	case s.port < 1024:
		diag.Advice = append(diag.Advice,
			"Firewall: no inbound rule found. Ports below 1024 need an explicit "+
				"rule, and usually Administrator rights to add one.")
	default:
		diag.Advice = append(diag.Advice,
			"No inbound firewall rule was found for this port. Run the server "+
				"as Administrator once to create it, or add one manually in "+
				"Windows Defender Firewall.")
	}
	if diag.ProfileName != "" && !diag.ProfileIsPriv {
		diag.Advice = append(diag.Advice,
			"Network profile is Public. Set this network to Private in "+
				"Settings > Network & Internet > Wi-Fi, otherwise discovery "+
				"(mDNS) and local discovery are blocked.")
	}
	if !diag.MDNSActive {
		diag.Advice = append(diag.Advice,
			"mDNS responder is not running, so the phone cannot auto-discover "+
				"this PC. Use manual entry with "+diag.PrimaryIP+":"+
				fmt.Sprint(diag.Port)+" and the PIN from the dashboard.")
	}
	if diag.PrimaryIP == "127.0.0.1" {
		diag.Advice = append(diag.Advice,
			"No non-loopback IPv4 address found. Connect the PC to the same "+
				"WiFi as the phone.")
	}
	return diag
}
