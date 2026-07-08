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

func TestValidateNormalizesHubTransportListen(t *testing.T) {
	cfg := baseConfig()
	cfg.Mode = "hub"
	cfg.Connect = ""
	cfg.Listen = ""
	cfg.VirtualIP = "10.77.0.1"
	cfg.Transport = TransportConfig{
		Protocol: "tcp_tls_v1",
		Listen:   "0.0.0.0:8443",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:8443" {
		t.Fatalf("Listen = %q, want transport listen", cfg.Listen)
	}
	if cfg.Transport.Protocol != "tcp_tls_v1" {
		t.Fatalf("Transport.Protocol = %q, want tcp_tls_v1", cfg.Transport.Protocol)
	}
}

func TestValidateNormalizesSpokeTransportConnect(t *testing.T) {
	cfg := baseConfig()
	cfg.Connect = ""
	cfg.ServerName = ""
	cfg.Transport = TransportConfig{
		Protocol:   "tcp_tls_v1",
		Connect:    "hub.example.com:8443",
		ServerName: "hub.example.com",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Connect != "hub.example.com:8443" {
		t.Fatalf("Connect = %q, want transport connect", cfg.Connect)
	}
	if cfg.ServerName != "hub.example.com" {
		t.Fatalf("ServerName = %q, want transport server name", cfg.ServerName)
	}
}

func TestValidateRejectsUnsupportedTransportProtocol(t *testing.T) {
	cfg := baseConfig()
	cfg.Transport = TransportConfig{
		Protocol: "https_stream_v1",
		Connect:  "127.0.0.1:8443",
	}

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

func TestValidateRejectsFullTunnelRoutesWithProductBoundary(t *testing.T) {
	tests := []struct {
		name   string
		routes []Route
		setup  []Route
	}{
		{name: "top-level IPv4 full tunnel", routes: []Route{{CIDR: "0.0.0.0/0"}}},
		{name: "setup IPv4 full tunnel", setup: []Route{{CIDR: "0.0.0.0/0"}}},
		{name: "top-level IPv6 full tunnel", routes: []Route{{CIDR: "::/0"}}},
		{name: "setup IPv6 full tunnel", setup: []Route{{CIDR: "::/0"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Routes = tt.routes
			cfg.Setup = SetupConfig{
				Enabled: true,
				Address: "10.77.0.2/24",
				Routes:  tt.setup,
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected full tunnel route to be rejected")
			}
			if got := err.Error(); !containsAll(got, []string{"Meshlink", "全局代理", "公网出口"}) {
				t.Fatalf("error = %q, want product boundary explanation", got)
			}
		})
	}
}

func TestValidateRejectsPublicEgressRoutes(t *testing.T) {
	tests := []struct {
		name   string
		routes []Route
		setup  []Route
	}{
		{name: "top-level public route", routes: []Route{{CIDR: "8.8.8.0/24"}}},
		{name: "setup public route", setup: []Route{{CIDR: "1.1.1.0/24"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Routes = tt.routes
			cfg.Setup = SetupConfig{
				Enabled: true,
				Address: "10.77.0.2/24",
				Routes:  tt.setup,
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected public egress route to be rejected")
			}
			if got := err.Error(); !containsAll(got, []string{"Meshlink", "公网出口"}) {
				t.Fatalf("error = %q, want public egress explanation", got)
			}
		})
	}
}

func TestValidateAllowsMeshAndRFC1918Routes(t *testing.T) {
	for _, cidr := range []string{"10.77.0.0/24", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		t.Run(cidr, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Routes = []Route{{CIDR: cidr}}
			cfg.Setup = SetupConfig{
				Enabled: true,
				Address: "10.77.0.2/24",
				Routes:  []Route{{CIDR: cidr}},
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() rejected private route %s: %v", cidr, err)
			}
		})
	}
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !contains(s, part) {
			return false
		}
	}
	return true
}

func contains(s, part string) bool {
	return len(part) == 0 || (len(s) >= len(part) && (s == part || contains(s[1:], part) || s[:len(part)] == part))
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
