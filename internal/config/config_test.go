package config

import "testing"

func TestValidateSetupAddressContainsVirtualIP(t *testing.T) {
	cfg := baseConfig()
	cfg.Setup = SetupConfig{
		Enabled: true,
		Address: "10.77.0.2/24",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSetupAddressRejectsDifferentPrefix(t *testing.T) {
	cfg := baseConfig()
	cfg.Setup = SetupConfig{
		Enabled: true,
		Address: "10.88.0.2/24",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsNATWhenSetupDisabled(t *testing.T) {
	cfg := baseConfig()
	cfg.Setup = SetupConfig{
		NAT: NAT{Enabled: true},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsIPv6NATPrefix(t *testing.T) {
	cfg := baseConfig()
	cfg.Setup = SetupConfig{
		Enabled: true,
		Address: "10.77.0.2/24",
		NAT: NAT{
			Enabled:        true,
			InternalPrefix: "fd77::/64",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsIPv6VirtualIP(t *testing.T) {
	cfg := baseConfig()
	cfg.VirtualIP = "fd77::2"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsIPv6Connect(t *testing.T) {
	cfg := baseConfig()
	cfg.Connect = "[2408::1]:8443"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsIPv6Route(t *testing.T) {
	cfg := baseConfig()
	cfg.Routes = []Route{{CIDR: "fd77::/64"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func baseConfig() Config {
	return Config{
		NodeID:    "node-a",
		Mode:      "spoke",
		Connect:   "127.0.0.1:8443",
		CAFile:    "ca.pem",
		CertFile:  "node.pem",
		KeyFile:   "node-key.pem",
		VirtualIP: "10.77.0.2",
	}
}
