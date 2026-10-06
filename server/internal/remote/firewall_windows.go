//go:build windows

package remote

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// firewallRuleName is the display name of the inbound control-port rule.
const firewallRuleName = "Smart Remote Server"

// firewallMDNSRuleName is the display name of the mDNS discovery rule.
//
// Windows firewall rules are protocol-specific, and mDNS discovery is UDP
// 5353 - a rule that opens only the TCP control port leaves phone
// auto-discovery dependent on whatever the profile's default rules happen to
// allow. The rule is separate so each protocol can be verified and reported
// on its own.
const firewallMDNSRuleName = "Smart Remote Server mDNS"

// mDNSPort is the port mDNS discovery traffic (RFC 6762) arrives on.
const mDNSPort = 5353

// AddFirewallRule makes sure the inbound rules this server needs exist: the
// control port over TCP and mDNS discovery over UDP 5353.
//
// The operation is deliberately idempotent and non-destructive:
//
//   - An existing correct rule is left untouched - no delete/recreate churn,
//     so re-running the server (which happens on every start) is a no-op.
//   - A disabled rule is simply re-enabled.
//   - Only a missing or mismatched rule is ever created, and even then the
//     replacement is remove-then-create inside PowerShell: when the caller
//     is not elevated the removal fails first, so a working rule can never
//     be deleted by an attempt that cannot recreate it.
//
// Rule changes need Administrator rights, so failure is expected and is
// reported rather than fatal: the server keeps working and the log carries
// a clear instruction for the user.
func AddFirewallRule(port int, logger *log.Logger) error {
	if port <= 0 {
		return fmt.Errorf("remote: invalid port %d", port)
	}
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return fmt.Errorf("remote: powershell.exe not found: %w", err)
	}

	// Ensure-Rule is the whole idempotency story: check first, create only
	// when needed, never start with a delete of a healthy rule.
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
function Ensure-Rule([string]$Name, [string]$Proto, [string]$Port) {
	$r = Get-NetFirewallRule -DisplayName $Name -ErrorAction SilentlyContinue | Select-Object -First 1
	if ($null -eq $r) {
		New-NetFirewallRule -DisplayName $Name -Direction Inbound -Action Allow -Protocol $Proto -LocalPort $Port -Profile Any | Out-Null
		Write-Output ($Name + ': created')
		return
	}
	$f = $r | Get-NetFirewallPortFilter | Select-Object -First 1
	$samePort = (@($f.LocalPort) -contains $Port) -or ($f.LocalPort -eq 'Any')
	$sameProto = ($f.Protocol -eq 'Any') -or ($f.Protocol -eq $Proto)
	if ($samePort -and $sameProto) {
		if ($r.Enabled -ne 'True') {
			Set-NetFirewallRule -Name $r.Name -Enabled True
			Write-Output ($Name + ': re-enabled')
		} else {
			Write-Output ($Name + ': present')
		}
		return
	}
	Remove-NetFirewallRule -Name $r.Name
	New-NetFirewallRule -DisplayName $Name -Direction Inbound -Action Allow -Protocol $Proto -LocalPort $Port -Profile Any | Out-Null
	Write-Output ($Name + ': replaced')
}
Ensure-Rule %q 'TCP' %d
Ensure-Rule %q 'UDP' %d
`, firewallRuleName, port, firewallMDNSRuleName, mDNSPort)

	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if logger != nil {
			logger.Printf("WARNING: firewall rule update failed: %v | %s",
				err, strings.TrimSpace(string(out)))
			logger.Printf("WARNING: Firewall rule requires Administrator. " +
				"Please restart the app as Admin for full functionality.")
		}
		return fmt.Errorf("remote: firewall rules incomplete (Administrator required to change them): %w", err)
	}
	if logger != nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				logger.Printf("firewall: %s", line)
			}
		}
	}
	return nil
}

// CheckFirewallRule reports whether the inbound rules for the port exist.
//
// Both the TCP control-port rule and the UDP 5353 mDNS rule are checked: a
// present control rule with a missing mDNS rule is exactly the state where
// the phone can connect by address but never discovers the PC, and the note
// says so instead of reporting a plain "present".
func CheckFirewallRule(port int) (bool, string) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return false, "cannot verify: powershell.exe not found"
	}
	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			`$ErrorActionPreference = 'SilentlyContinue'; `+
				`$tcp = Get-NetFirewallRule -DisplayName %q | Select-Object -First 1; `+
				`$udp = Get-NetFirewallRule -DisplayName %q | Select-Object -First 1; `+
				`$t = $null -ne $tcp -and $tcp.Enabled -eq 'True'; `+
				`$u = $null -ne $udp -and $udp.Enabled -eq 'True'; `+
				`if ($t -and $u) { 'both' } elseif ($t) { 'tcp' } else { 'none' }`,
			firewallRuleName, firewallMDNSRuleName))
	out, err := cmd.Output()
	if err != nil {
		return false, "cannot verify: " + err.Error()
	}
	switch strings.TrimSpace(string(out)) {
	case "both":
		return true, fmt.Sprintf("rules present and enabled: TCP %d, UDP %d (mDNS)",
			port, mDNSPort)
	case "tcp":
		return true, fmt.Sprintf(
			"rule %s present (TCP %d), but the mDNS rule (UDP %d) is missing - phone auto-discovery may fail",
			firewallRuleName, port, mDNSPort)
	default:
		return false, "no enabled inbound rule named " + firewallRuleName
	}
}

// RemoveFirewallRule deletes the rules this server created.
//
// It is deliberately NOT called on shutdown any more: the rules are kept so
// that a later start without Administrator rights still finds them in
// place. Deleting them on exit was the loop behind "worked once as Admin,
// broken after that" - creation needs elevation, so quitting an elevated
// session and restarting normally would lose the rules every time.
func RemoveFirewallRule(logger *log.Logger) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		return
	}
	cmd := hiddenCommand(ps, "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			`Remove-NetFirewallRule -DisplayName %q -ErrorAction SilentlyContinue; `+
				`Remove-NetFirewallRule -DisplayName %q -ErrorAction SilentlyContinue`,
			firewallRuleName, firewallMDNSRuleName))
	if err := cmd.Run(); err != nil && logger != nil {
		logger.Printf("firewall: could not remove rules: %v", err)
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
