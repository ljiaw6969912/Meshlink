package netsetup

import (
	"encoding/json"
	"net/netip"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"meshlink/internal/config"
)

func TestServiceFirewallRestrictsProgramsAndHubPort(t *testing.T) {
	program := `C:\Users\O'Brien\Meshlink $tools\mesh-agent.exe`
	plan, err := serviceFirewallPlan("MeshlinkAgent", program, &config.Config{Mode: "hub", Listen: "0.0.0.0:9443"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rules) != 2 || len(plan.Remove) != 0 {
		t.Fatalf("hub rules = %+v", plan)
	}
	for _, rule := range plan.Rules {
		if rule.Program != program {
			t.Fatalf("rule permits another program: %+v", rule)
		}
		switch rule.Protocol {
		case "UDP":
			if rule.LocalPort != "Any" {
				t.Fatalf("dynamic peer UDP listener is blocked: %+v", rule)
			}
		case "TCP":
			if rule.LocalPort != "9443" {
				t.Fatalf("TCP rule permits an unconfigured port: %+v", rule)
			}
		default:
			t.Fatalf("unexpected inbound protocol: %+v", rule)
		}
	}
	spoke, err := serviceFirewallPlan("meshlinkagent", program, &config.Config{Mode: "spoke"})
	if err != nil {
		t.Fatal(err)
	}
	if len(spoke.Rules) != 1 || spoke.Rules[0].Protocol != "UDP" || len(spoke.Remove) != 1 {
		t.Fatalf("spoke should keep UDP and remove old hub TCP: %+v", spoke)
	}
	if spoke.Rules[0].Name != plan.Rules[0].Name || spoke.Remove[0] != plan.Rules[1].Name {
		t.Fatalf("service names must be stable and case-insensitive: hub=%+v spoke=%+v", plan, spoke)
	}
}

func TestTunnelFirewallRestrictsRDPAndPingToMeshInterface(t *testing.T) {
	plan, err := tunnelFirewallPlan(Plan{Interface: "meshlink0", Address: netip.MustParsePrefix("10.77.0.2/24")})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rules) != 2 {
		t.Fatalf("rules = %+v", plan.Rules)
	}
	for _, rule := range plan.Rules {
		if rule.InterfaceAlias != "meshlink0" || rule.LocalAddress != "10.77.0.2" || rule.RemoteAddress != "10.77.0.0/24" {
			t.Fatalf("rule would permit traffic outside the mesh interface/subnet: %+v", rule)
		}
		switch rule.Protocol {
		case "ICMPv4":
			if rule.IcmpType != "8" || rule.LocalPort != "" {
				t.Fatalf("ping rule should permit only echo requests: %+v", rule)
			}
		case "TCP":
			if rule.LocalPort != "3389" {
				t.Fatalf("RDP rule permits another TCP port: %+v", rule)
			}
		default:
			t.Fatalf("unexpected tunnel rule: %+v", rule)
		}
	}
	other, err := tunnelFirewallPlan(Plan{Interface: "meshlink-smoke-test", Address: netip.MustParsePrefix("10.77.0.2/24")})
	if err != nil {
		t.Fatal(err)
	}
	if other.Rules[0].Name == plan.Rules[0].Name {
		t.Fatal("separate adapters must not overwrite each other's rules")
	}
}

func TestFirewallRejectsScopesThatCouldOpenUnrelatedTraffic(t *testing.T) {
	for _, iface := range []string{"", "Any", "*", "mesh?", "mesh[0]"} {
		if _, err := tunnelFirewallPlan(Plan{Interface: iface, Address: netip.MustParsePrefix("10.77.0.2/24")}); err == nil {
			t.Errorf("accepted ambiguous interface %q", iface)
		}
	}
	for _, address := range []string{"0.0.0.0/0", "203.0.113.2/24", "fd77::2/64"} {
		if _, err := tunnelFirewallPlan(Plan{Interface: "meshlink0", Address: netip.MustParsePrefix(address)}); err == nil {
			t.Errorf("accepted non-private mesh scope %q", address)
		}
	}
	for _, listen := range []string{"0.0.0.0", "0.0.0.0:0", "0.0.0.0:65536", "0.0.0.0:Any"} {
		if _, err := serviceFirewallPlan("MeshlinkAgent", `C:\Meshlink\mesh-agent.exe`, &config.Config{Mode: "hub", Listen: listen}); err == nil {
			t.Errorf("accepted invalid TCP listener %q", listen)
		}
	}
	if _, err := serviceFirewallPlan("MeshlinkAgent", "Any", &config.Config{Mode: "spoke"}); err == nil {
		t.Fatal("accepted an unscoped program")
	}
}

// These functions replace only the OS firewall boundary. They deliberately cannot
// call the Windows firewall provider, even when tests run with administrator rights.
const firewallTestCmdlets = `
$ErrorActionPreference = 'Stop'
$global:firewallRules = @{}
function Get-NetAdapter { param($Name, $ErrorAction); [pscustomobject]@{Name=$Name; ifIndex=77} }
function Get-NetFirewallRule { param($Name, $PolicyStore, $ErrorAction); $global:firewallRules[$Name] }
function New-NetFirewallRule {
  param($Name, $DisplayName, $Group, $PolicyStore, $Enabled, $Profile, $Direction, $Action, $EdgeTraversalPolicy, $Protocol, $Program, $InterfaceAlias, $LocalAddress, $RemoteAddress, $LocalPort, $IcmpType)
  if ($global:firewallRules.ContainsKey($Name)) { throw 'duplicate rule' }
  $global:firewallRules[$Name] = [pscustomobject]@{Name=$Name; Group=$Group; Program=$Program; Protocol=$Protocol; LocalPort=$LocalPort; InterfaceAlias=$InterfaceAlias; LocalAddress=$LocalAddress; RemoteAddress=$RemoteAddress; IcmpType=$IcmpType; Enabled=$Enabled; Profile=$Profile; Direction=$Direction; Action=$Action; EdgeTraversalPolicy=$EdgeTraversalPolicy}
}
function Set-NetFirewallRule {
  param($Name, $NewDisplayName, $PolicyStore, $Enabled, $Profile, $Direction, $Action, $EdgeTraversalPolicy, $Protocol, $Program, $InterfaceAlias, $LocalAddress, $RemoteAddress, $LocalPort, $IcmpType)
  if (-not $global:firewallRules.ContainsKey($Name)) { throw 'missing update target' }
  $oldGroup = $global:firewallRules[$Name].Group
  $global:firewallRules[$Name] = [pscustomobject]@{Name=$Name; Group=$oldGroup; Program=$Program; Protocol=$Protocol; LocalPort=$LocalPort; InterfaceAlias=$InterfaceAlias; LocalAddress=$LocalAddress; RemoteAddress=$RemoteAddress; IcmpType=$IcmpType; Enabled=$Enabled; Profile=$Profile; Direction=$Direction; Action=$Action; EdgeTraversalPolicy=$EdgeTraversalPolicy}
}
function Remove-NetFirewallRule {
  param($Name, $PolicyStore, $ErrorAction)
  $global:firewallRules.Remove($Name)
}
`

func TestFirewallScriptUpdatesRulesAndRemovesOnlyStaleServiceRule(t *testing.T) {
	hub, err := serviceFirewallPlan("Meshlink Agent", `C:\O'Brien\mesh $($global:injected=1).exe`, &config.Config{Mode: "hub", Listen: "0.0.0.0:9443"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := serviceFirewallPlan("Meshlink Agent", `C:\New Location\mesh-agent.exe`, &config.Config{Mode: "hub", Listen: "0.0.0.0:10443"})
	if err != nil {
		t.Fatal(err)
	}
	spoke, err := serviceFirewallPlan("Meshlink Agent", `C:\New Location\mesh-agent.exe`, &config.Config{Mode: "spoke"})
	if err != nil {
		t.Fatal(err)
	}
	script := firewallTestCmdlets + `
$global:firewallRules['unrelated'] = [pscustomobject]@{Name='unrelated'; Group='Other app'}
` + firewallTestCommand(t, hub) + "\n" + firewallTestCommand(t, hub) + `
if ($global:injected) { throw 'argument was interpreted as code' }
$udp = @($global:firewallRules.Values | Where-Object Protocol -eq 'UDP')[0]
if ($udp.Program -cne 'C:\O''Brien\mesh $($global:injected=1).exe') { throw 'program argument changed' }
` + firewallTestCommand(t, changed) + `
if ($global:firewallRules.Count -ne 3) { throw 'update duplicated rules' }
$tcp = @($global:firewallRules.Values | Where-Object Protocol -eq 'TCP')[0]
if ($tcp.LocalPort -ne '10443' -or $tcp.Program -cne 'C:\New Location\mesh-agent.exe') { throw 'old listener is still allowed' }
` + firewallTestCommand(t, spoke) + `
if ($global:firewallRules.Count -ne 2 -or -not $global:firewallRules.ContainsKey('unrelated')) { throw 'removed unrelated rule' }
if (@($global:firewallRules.Values | Where-Object Protocol -eq 'TCP').Count -ne 0) { throw 'stale TCP rule remains' }
`
	runFirewallScriptTest(t, script)
}

func TestFirewallScriptKeepsTunnelScopesOnUpdate(t *testing.T) {
	plan, err := tunnelFirewallPlan(Plan{Interface: "Mesh O'Brien $adapter", Address: netip.MustParsePrefix("10.77.0.2/24")})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := tunnelFirewallPlan(Plan{Interface: "Mesh O'Brien $adapter", Address: netip.MustParsePrefix("10.88.0.3/24")})
	if err != nil {
		t.Fatal(err)
	}
	script := firewallTestCmdlets + firewallTestCommand(t, plan) + "\n" + firewallTestCommand(t, updated) + `
if ($global:firewallRules.Count -ne 2) { throw 'tunnel rules duplicated' }
foreach ($rule in $global:firewallRules.Values) {
  if ($rule.InterfaceAlias -cne 'Mesh O''Brien $adapter' -or $rule.LocalAddress -ne '10.88.0.3' -or $rule.RemoteAddress -ne '10.88.0.0/24') { throw 'tunnel scope broadened or did not update' }
  if ($rule.Enabled -ne 'True' -or $rule.Profile -ne 'Any' -or $rule.Direction -ne 'Inbound' -or $rule.Action -ne 'Allow' -or $rule.EdgeTraversalPolicy -ne 'Block') { throw 'incorrect firewall policy' }
  if ($rule.Protocol -eq 'TCP' -and $rule.LocalPort -ne '3389') { throw 'RDP port broadened' }
  if ($rule.Protocol -eq 'ICMPv4' -and $rule.IcmpType -ne '8') { throw 'ICMP protocol broadened' }
}
`
	runFirewallScriptTest(t, script)
}

func TestFirewallScriptRefusesUnownedRuleAndWrongAdapter(t *testing.T) {
	plan, err := tunnelFirewallPlan(Plan{Interface: "meshlink0", Address: netip.MustParsePrefix("10.77.0.2/24")})
	if err != nil {
		t.Fatal(err)
	}
	command := firewallTestCommand(t, plan)
	runFirewallScriptTest(t, firewallTestCmdlets+command+`
foreach ($rule in $global:firewallRules.Values) { $rule.Group = 'Someone else' }
$caught = $false
try {
`+command+`
} catch {
  if ($_.Exception.Message -notlike '*not owned by Meshlink*') { throw }
  $caught = $true
}
if (-not $caught) { throw 'overwrote another owner firewall rule' }
foreach ($rule in $global:firewallRules.Values) {
  if ($rule.Group -ne 'Someone else') { throw 'changed another owner group' }
}
function Get-NetAdapter { param($Name, $ErrorAction); [pscustomobject]@{Name='Ethernet'; ifIndex=1} }
$caught = $false
try {
`+command+`
} catch {
  if ($_.Exception.Message -notlike '*exact adapter*') { throw }
  $caught = $true
}
if (-not $caught) { throw 'accepted a physical adapter for the mesh firewall' }
`)
}

func TestPowerShellReportsFirewallFailure(t *testing.T) {
	err := runPowerShell(`$ErrorActionPreference = 'Stop'; throw 'firewall provider denied access'`)
	if err == nil || !strings.Contains(err.Error(), "firewall provider denied access") {
		t.Fatalf("missing actionable PowerShell failure: %v", err)
	}
}

func firewallTestCommand(t *testing.T, plan firewallPlan) string {
	t.Helper()
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return powerShellCommand(firewallScript, []string{string(b)})
}

func runFirewallScriptTest(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "& {"+script+"\n}")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("firewall script failed: %v\n%s", err, strings.TrimSpace(string(output)))
	}
}
