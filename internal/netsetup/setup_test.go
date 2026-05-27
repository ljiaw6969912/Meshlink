package netsetup

import (
	"testing"

	"meshlink/internal/config"
)

func TestBuildPlanDefaultsHostPrefix(t *testing.T) {
	plan, err := buildPlan("mesh0", "10.77.0.2", config.SetupConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Address.String(), "10.77.0.2/32"; got != want {
		t.Fatalf("address = %s, want %s", got, want)
	}
}

func TestBuildPlanUsesConfiguredAddressAndRoutes(t *testing.T) {
	plan, err := buildPlan("mesh0", "10.77.0.2", config.SetupConfig{
		Enabled: true,
		Address: "10.77.0.2/24",
		Routes: []config.Route{
			{CIDR: "192.168.1.0/24"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Address.String(), "10.77.0.2/24"; got != want {
		t.Fatalf("address = %s, want %s", got, want)
	}
	if got, want := len(plan.Routes), 1; got != want {
		t.Fatalf("routes = %d, want %d", got, want)
	}
}

func TestBuildPlanNATDefaultsToInterfacePrefix(t *testing.T) {
	plan, err := buildPlan("mesh0", "10.77.0.1", config.SetupConfig{
		Enabled: true,
		Address: "10.77.0.1/24",
		NAT: config.NAT{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.NAT == nil {
		t.Fatal("expected NAT plan")
	}
	if got, want := plan.NAT.Name, "MeshlinkNAT"; got != want {
		t.Fatalf("nat name = %s, want %s", got, want)
	}
	if got, want := plan.NAT.InternalPrefix.String(), "10.77.0.0/24"; got != want {
		t.Fatalf("nat prefix = %s, want %s", got, want)
	}
}

func TestBuildPlanRejectsIPv6NAT(t *testing.T) {
	_, err := buildPlan("mesh0", "fd77::1", config.SetupConfig{
		Enabled: true,
		Address: "fd77::1/64",
		NAT: config.NAT{
			Enabled: true,
		},
	})
	if err == nil {
		t.Fatal("expected IPv6 NAT validation error")
	}
}

func TestBuildPlanRejectsIPv6VirtualIP(t *testing.T) {
	_, err := buildPlan("mesh0", "fd77::1", config.SetupConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected IPv6 virtual IP validation error")
	}
}

func TestBuildPlanRejectsIPv6Route(t *testing.T) {
	_, err := buildPlan("mesh0", "10.77.0.1", config.SetupConfig{
		Enabled: true,
		Address: "10.77.0.1/24",
		Routes: []config.Route{
			{CIDR: "fd77::/64"},
		},
	})
	if err == nil {
		t.Fatal("expected IPv6 route validation error")
	}
}
