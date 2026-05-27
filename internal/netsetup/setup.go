package netsetup

import (
	"fmt"
	"log/slog"
	"net/netip"

	"meshlink/internal/config"
	"meshlink/internal/device"
)

type Plan struct {
	Interface  string
	Address    netip.Prefix
	Routes     []netip.Prefix
	Forwarding bool
	NAT        *NATPlan
}

type NATPlan struct {
	Name           string
	InternalPrefix netip.Prefix
}

func Apply(iface string, virtualIP string, deviceType string, setup config.SetupConfig, logger *slog.Logger) error {
	if !setup.Enabled {
		return nil
	}
	if deviceType == "" || deviceType == device.TypeNull {
		logger.Info("skipping network setup for null device")
		return nil
	}

	plan, err := buildPlan(iface, virtualIP, setup)
	if err != nil {
		return err
	}
	logger.Info("applying network setup", "interface", plan.Interface, "address", plan.Address, "routes", plan.Routes, "forwarding", plan.Forwarding, "nat", plan.NAT != nil)
	return applyPlatform(plan, logger)
}

func buildPlan(iface string, virtualIP string, setup config.SetupConfig) (Plan, error) {
	addr, err := netip.ParseAddr(virtualIP)
	if err != nil {
		return Plan{}, err
	}
	if !addr.Is4() {
		return Plan{}, fmt.Errorf("virtual_ip must be IPv4, got %q", virtualIP)
	}
	address := netip.PrefixFrom(addr, 32)
	if setup.Address != "" {
		address, err = netip.ParsePrefix(setup.Address)
		if err != nil {
			return Plan{}, fmt.Errorf("setup.address: %w", err)
		}
		if !address.Addr().Is4() {
			return Plan{}, fmt.Errorf("setup.address must be IPv4, got %q", setup.Address)
		}
	}
	if !address.Contains(addr) {
		return Plan{}, fmt.Errorf("setup.address %q does not contain virtual_ip %q", address, virtualIP)
	}

	routes := make([]netip.Prefix, 0, len(setup.Routes))
	for _, route := range setup.Routes {
		prefix, err := netip.ParsePrefix(route.CIDR)
		if err != nil {
			return Plan{}, fmt.Errorf("setup route %q: %w", route.CIDR, err)
		}
		if !prefix.Addr().Is4() {
			return Plan{}, fmt.Errorf("setup route must be IPv4, got %q", route.CIDR)
		}
		routes = append(routes, prefix)
	}
	var nat *NATPlan
	if setup.NAT.Enabled {
		internalPrefix := address.Masked()
		if setup.NAT.InternalPrefix != "" {
			internalPrefix, err = netip.ParsePrefix(setup.NAT.InternalPrefix)
			if err != nil {
				return Plan{}, fmt.Errorf("setup.nat.internal_prefix: %w", err)
			}
			internalPrefix = internalPrefix.Masked()
		}
		if !internalPrefix.Addr().Is4() {
			return Plan{}, fmt.Errorf("setup.nat.internal_prefix must be IPv4, got %q", internalPrefix)
		}
		name := setup.NAT.Name
		if name == "" {
			name = "MeshlinkNAT"
		}
		nat = &NATPlan{
			Name:           name,
			InternalPrefix: internalPrefix,
		}
	}
	return Plan{
		Interface:  iface,
		Address:    address,
		Routes:     routes,
		Forwarding: setup.Forwarding,
		NAT:        nat,
	}, nil
}
