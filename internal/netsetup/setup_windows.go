package netsetup

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

func applyPlatform(plan Plan, logger *slog.Logger) error {
	if err := runPowerShell(addressScript, plan.Interface, plan.Address.Addr().String(), strconv.Itoa(plan.Address.Bits())); err != nil {
		return fmt.Errorf("configure address: %w", err)
	}
	for _, route := range plan.Routes {
		nextHop := "0.0.0.0"
		if err := runPowerShell(routeScript, plan.Interface, route.String(), nextHop); err != nil {
			return fmt.Errorf("configure route %s: %w", route, err)
		}
	}
	if plan.Forwarding {
		if err := runPowerShell(forwardingScript, plan.Interface); err != nil {
			return fmt.Errorf("enable forwarding: %w", err)
		}
	}
	if plan.NAT != nil {
		if err := runPowerShell(natScript, plan.NAT.Name, plan.NAT.InternalPrefix.String()); err != nil {
			return fmt.Errorf("configure NAT: %w", err)
		}
	}
	logger.Info("network setup applied")
	return nil
}

func runPowerShell(script string, args ...string) error {
	cmdArgs := []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-Command",
		powerShellCommand(script, args),
	}
	cmd := exec.Command("powershell.exe", cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func powerShellCommand(script string, args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "''")+"'")
	}
	if len(quoted) == 0 {
		return "& {" + script + "\n}"
	}
	return "& {" + script + "\n} " + strings.Join(quoted, " ")
}

const addressScript = `
$ErrorActionPreference = 'Stop'
$alias = $args[0]
$ip = $args[1]
$prefix = [int]$args[2]
$adapter = Get-NetAdapter -Name $alias -ErrorAction Stop
$existing = Get-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress $ip -ErrorAction SilentlyContinue
if (-not $existing) {
  New-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress $ip -PrefixLength $prefix -AddressFamily IPv4 -PolicyStore ActiveStore | Out-Null
}
`

const routeScript = `
$ErrorActionPreference = 'Stop'
$alias = $args[0]
$dest = $args[1]
$nextHop = $args[2]
$adapter = Get-NetAdapter -Name $alias -ErrorAction Stop
$existing = Get-NetRoute -InterfaceIndex $adapter.ifIndex -DestinationPrefix $dest -ErrorAction SilentlyContinue
if (-not $existing) {
  New-NetRoute -InterfaceIndex $adapter.ifIndex -DestinationPrefix $dest -NextHop $nextHop -PolicyStore ActiveStore | Out-Null
}
`

const forwardingScript = `
$ErrorActionPreference = 'Stop'
$alias = $args[0]
$adapter = Get-NetAdapter -Name $alias -ErrorAction Stop
Set-NetIPInterface -InterfaceIndex $adapter.ifIndex -Forwarding Enabled
`

const natScript = `
$ErrorActionPreference = 'Stop'
$name = $args[0]
$prefix = $args[1]
$existing = Get-NetNat -Name $name -ErrorAction SilentlyContinue
if ($existing) {
  if ($existing.InternalIPInterfaceAddressPrefix -ne $prefix) {
    throw "NAT $name already exists with prefix $($existing.InternalIPInterfaceAddressPrefix), expected $prefix"
  }
} else {
  New-NetNat -Name $name -InternalIPInterfaceAddressPrefix $prefix | Out-Null
}
`
