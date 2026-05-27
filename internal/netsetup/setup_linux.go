package netsetup

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
)

func applyPlatform(plan Plan, logger *slog.Logger) error {
	if err := run("ip", "addr", "replace", plan.Address.String(), "dev", plan.Interface); err != nil {
		return fmt.Errorf("configure address: %w", err)
	}
	if err := run("ip", "link", "set", "dev", plan.Interface, "up"); err != nil {
		return fmt.Errorf("bring interface up: %w", err)
	}
	for _, route := range plan.Routes {
		if err := run("ip", "route", "replace", route.String(), "dev", plan.Interface); err != nil {
			return fmt.Errorf("configure route %s: %w", route, err)
		}
	}
	if plan.Forwarding {
		if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
			return fmt.Errorf("enable IPv4 forwarding: %w", err)
		}
	}
	if plan.NAT != nil {
		if err := applyNAT(plan); err != nil {
			return fmt.Errorf("configure NAT: %w", err)
		}
	}
	logger.Info("network setup applied")
	return nil
}

func applyNAT(plan Plan) error {
	if _, err := exec.LookPath("nft"); err == nil {
		return applyNFTNAT(plan)
	}
	return applyIPTablesNAT(plan)
}

func applyNFTNAT(plan Plan) error {
	prefix := plan.NAT.InternalPrefix.String()
	script := fmt.Sprintf(`
table ip meshlink {
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr %s oifname != "%s" masquerade
  }
}
`, prefix, plan.Interface)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func applyIPTablesNAT(plan Plan) error {
	prefix := plan.NAT.InternalPrefix.String()
	if err := run("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", prefix, "!", "-o", plan.Interface, "-j", "MASQUERADE"); err == nil {
		return nil
	}
	return run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", prefix, "!", "-o", plan.Interface, "-j", "MASQUERADE")
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}
