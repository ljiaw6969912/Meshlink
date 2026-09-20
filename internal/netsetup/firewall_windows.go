package netsetup

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"

	"meshlink/internal/config"
)

type firewallRule struct {
	Name           string
	DisplayName    string
	Protocol       string
	Program        string
	InterfaceAlias string
	LocalAddress   string
	RemoteAddress  string
	LocalPort      string
	IcmpType       string
}

type firewallPlan struct {
	Interface string
	Rules     []firewallRule
	Remove    []string
}

// ConfigureServiceFirewall permits the agent's UDP listeners and, for a hub,
// its configured control port. Call this from elevated service installation,
// rather than agent construction, so in-memory agents never change the OS.
func ConfigureServiceFirewall(serviceName, executablePath string, cfg *config.Config) error {
	plan, err := serviceFirewallPlan(serviceName, executablePath, cfg)
	if err != nil {
		return err
	}
	if err := applyFirewallPlan(plan); err != nil {
		return fmt.Errorf("configure Windows firewall for service %q (administrator rights required): %w", serviceName, err)
	}
	return nil
}

func serviceFirewallPlan(serviceName, executablePath string, cfg *config.Config) (firewallPlan, error) {
	if strings.TrimSpace(serviceName) == "" {
		return firewallPlan{}, fmt.Errorf("firewall service name is required")
	}
	if !filepath.IsAbs(executablePath) {
		return firewallPlan{}, fmt.Errorf("firewall program must be an absolute executable path, got %q", executablePath)
	}
	if cfg == nil || (cfg.Mode != "hub" && cfg.Mode != "spoke") {
		return firewallPlan{}, fmt.Errorf("firewall requires a hub or spoke configuration")
	}
	base := firewallRuleBase("Service", serviceName)
	udp := firewallRule{
		Name: base + "-UDP", DisplayName: "Meshlink agent UDP (" + serviceName + ")",
		Protocol: "UDP", Program: executablePath, LocalPort: "Any",
		InterfaceAlias: "Any", LocalAddress: "Any", RemoteAddress: "Any",
	}
	plan := firewallPlan{Rules: []firewallRule{udp}}
	if cfg.Mode == "spoke" {
		// A service can be switched from hosting to joining. Remove only the
		// TCP rule that belonged to this service, keeping other services intact.
		plan.Remove = []string{base + "-TCP"}
		return plan, nil
	}
	listen := cfg.Listen
	if listen == "" {
		listen = cfg.Transport.Listen
	}
	_, rawPort, err := net.SplitHostPort(listen)
	if err != nil {
		return firewallPlan{}, fmt.Errorf("firewall hub listen address: %w", err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return firewallPlan{}, fmt.Errorf("firewall hub listen port must be between 1 and 65535, got %q", rawPort)
	}
	plan.Rules = append(plan.Rules, firewallRule{
		Name: base + "-TCP", DisplayName: "Meshlink hub TCP (" + serviceName + ")",
		Protocol: "TCP", Program: executablePath, LocalPort: strconv.Itoa(port),
		InterfaceAlias: "Any", LocalAddress: "Any", RemoteAddress: "Any",
	})
	return plan, nil
}

func tunnelFirewallPlan(plan Plan) (firewallPlan, error) {
	if strings.TrimSpace(plan.Interface) == "" || strings.EqualFold(plan.Interface, "Any") || strings.ContainsAny(plan.Interface, "*?[]") {
		return firewallPlan{}, fmt.Errorf("firewall requires an exact, non-wildcard Meshlink interface name, got %q", plan.Interface)
	}
	prefix := plan.Address.Masked()
	private := false
	for _, allowed := range []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	} {
		if prefix.IsValid() && prefix.Addr().Is4() && prefix.Bits() >= allowed.Bits() && allowed.Contains(prefix.Addr()) {
			private = true
			break
		}
	}
	if !private {
		return firewallPlan{}, fmt.Errorf("firewall tunnel address must be within an RFC1918 IPv4 subnet, got %q", plan.Address)
	}
	base := firewallRuleBase("Tunnel", plan.Interface)
	common := firewallRule{
		Program: "Any", InterfaceAlias: plan.Interface,
		LocalAddress: plan.Address.Addr().String(), RemoteAddress: prefix.String(),
	}
	ping := common
	ping.Name = base + "-ICMPv4"
	ping.DisplayName = "Meshlink tunnel ping (" + plan.Interface + ")"
	ping.Protocol = "ICMPv4"
	ping.IcmpType = "8"
	rdp := common
	rdp.Name = base + "-RDP"
	rdp.DisplayName = "Meshlink tunnel RDP (" + plan.Interface + ")"
	rdp.Protocol = "TCP"
	rdp.LocalPort = "3389"
	return firewallPlan{Interface: plan.Interface, Rules: []firewallRule{ping, rdp}}, nil
}

func firewallRuleBase(kind, owner string) string {
	// Windows service and adapter names are case-insensitive. A hash also keeps
	// user-provided names from being interpreted as wildcard firewall selectors.
	sum := sha256.Sum256([]byte(strings.ToLower(owner)))
	return fmt.Sprintf("Meshlink-%s-%x", kind, sum[:16])
}

func applyFirewallPlan(plan firewallPlan) error {
	payload, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return runPowerShell(firewallScript, string(payload))
}

const firewallScript = `
$ErrorActionPreference = 'Stop'
$request = ConvertFrom-Json $args[0]
$group = 'Meshlink managed rules'
if ($request.Interface) {
  $adapter = @(Get-NetAdapter -Name $request.Interface -ErrorAction Stop)
  if ($adapter.Count -ne 1 -or $adapter[0].Name -ne $request.Interface) {
    throw "Meshlink firewall requires the exact adapter '$($request.Interface)'"
  }
}
foreach ($rule in $request.Rules) {
  $existing = Get-NetFirewallRule -Name $rule.Name -PolicyStore PersistentStore -ErrorAction SilentlyContinue
  if ($existing -and $existing.Group -ne $group) {
    throw "Firewall rule '$($rule.Name)' is not owned by Meshlink"
  }
  $parameters = @{
    Name = $rule.Name
    PolicyStore = 'PersistentStore'
    Enabled = 'True'
    Profile = 'Any'
    Direction = 'Inbound'
    Action = 'Allow'
    EdgeTraversalPolicy = 'Block'
    Protocol = $rule.Protocol
    Program = $rule.Program
    InterfaceAlias = $rule.InterfaceAlias
    LocalAddress = $rule.LocalAddress
    RemoteAddress = $rule.RemoteAddress
  }
  if ($rule.LocalPort) { $parameters.LocalPort = $rule.LocalPort }
  if ($rule.IcmpType) { $parameters.IcmpType = $rule.IcmpType }
  if ($existing) {
    Set-NetFirewallRule @parameters -NewDisplayName $rule.DisplayName | Out-Null
  } else {
    New-NetFirewallRule @parameters -DisplayName $rule.DisplayName -Group $group | Out-Null
  }
}
foreach ($name in $request.Remove) {
  $existing = Get-NetFirewallRule -Name $name -PolicyStore PersistentStore -ErrorAction SilentlyContinue
  if ($existing) {
    if ($existing.Group -ne $group) { throw "Firewall rule '$name' is not owned by Meshlink" }
    Remove-NetFirewallRule -Name $name -PolicyStore PersistentStore -ErrorAction Stop
  }
}
`
