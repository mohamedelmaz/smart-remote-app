//go:build windows

package remote

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// firewallRuleName is the display name of the inbound rule this server owns.
const firewallRuleName = "Smart Remote Server"

// AddFirewallRule creates an inbound rule allowing the port over TCP.
//
// Rule creation needs Administrator rights, so failure is expected and is
// reported rather than fatal: the server still works, and the diagnostics
// endpoint tells the user exactly what to do manually.
func AddFirewallRule(port int, logger *log.Logger) error {
	if port <= 0 {
		return fmt.Errorf("remote: invalid port %d", port)
	}
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return fmt.Errorf("remote: powershell.exe not found: %w", err)
	}

	// Remove any stale rule first so re-running does not create duplicates.
	removeCmd := fmt.Sprintf(
		`Remove-NetFirewallRule -DisplayName %q -ErrorAction SilentlyContinue; `+
			`New-NetFirewallRule -DisplayName %q -Direction Inbound -Action Allow `+
			`-Protocol TCP -LocalPort %d -Profile Any | Out-Null`,
		firewallRuleName, firewallRuleName, port)

	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command", removeCmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if logger != nil {
			logger.Printf("firewall: could not add rule (needs Administrator): %v | %s",
				err, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("remote: add firewall rule failed (run as Administrator): %w", err)
	}
	if logger != nil {
		logger.Printf("firewall: inbound TCP %d allowed by rule %q", port, firewallRuleName)
	}
	return nil
}

// CheckFirewallRule reports whether an inbound rule for the port exists.
func CheckFirewallRule(port int) (bool, string) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return false, "cannot verify: powershell.exe not found"
	}
	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			`$r = Get-NetFirewallRule -DisplayName %q -ErrorAction SilentlyContinue; `+
				`if ($r -and $r.Enabled -eq 'True') { 'yes' } else { 'no' }`,
			firewallRuleName))
	out, err := cmd.Output()
	if err != nil {
		return false, "cannot verify: " + err.Error()
	}
	if strings.TrimSpace(string(out)) == "yes" {
		return true, "inbound rule " + firewallRuleName + " is present and enabled"
	}
	return false, "no enabled inbound rule named " + firewallRuleName
}

// RemoveFirewallRule deletes the rule this server created.
func RemoveFirewallRule(logger *log.Logger) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return
	}
	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(`Remove-NetFirewallRule -DisplayName %q -ErrorAction SilentlyContinue`,
			firewallRuleName))
	if err := cmd.Run(); err != nil && logger != nil {
		logger.Printf("firewall: could not remove rule: %v", err)
	}
}

// currentNetworkProfile reports the active Windows network profile.
//
// A Public profile blocks mDNS and some discovery traffic, which is the
// single most common reason auto-discovery fails on a home network.
func currentNetworkProfile() (name string, private bool) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return "", false
	}
	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command",
		`Get-NetConnectionProfile | Select-Object -First 1 -ExpandProperty NetworkCategory`)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	name = strings.TrimSpace(string(out))
	return name, name == "Private"
}

// SetNetworkProfilePrivate switches the current profile to Private.
//
// This is offered explicitly rather than done automatically: silently
// weakening the firewall profile is a security decision the user must make.
func SetNetworkProfilePrivate(logger *log.Logger) error {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return fmt.Errorf("remote: powershell.exe not found: %w", err)
	}
	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command",
		`Set-NetConnectionProfile -NetworkCategory Private`)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remote: set profile failed: %w | %s",
			err, strings.TrimSpace(string(out)))
	}
	if logger != nil {
		logger.Printf("network profile set to Private")
	}
	return nil
}
