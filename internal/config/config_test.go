package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMigratesV1SpokeOnceAndKeepsIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active.json")
	original := []byte(`{
  "node_id": "laptop",
  "mode": "spoke",
  "transport": {
    "protocol": "tcp_tls_v1",
    "connect": "home.example.com:8443",
    "server_name": "home.example.com"
  },
  "connect": "home.example.com:8443",
  "server_name": "home.example.com",
  "ca_file": "../certs/ca.pem",
  "cert_file": "../certs/laptop.pem",
  "key_file": "../certs/laptop-key.pem",
  "virtual_ip": "10.77.0.2",
  "routes": [{"cidr": "192.168.1.0/24"}],
  "mtu": 1420,
  "device": {"type": "tun", "name": "meshlink0"},
  "setup": {"enabled": true, "address": "10.77.0.2/24"}
}
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != ConfigVersion {
		t.Fatalf("Version = %d, want %d", cfg.Version, ConfigVersion)
	}
	if cfg.Transport.Protocol != ControlProtocolV2 {
		t.Fatalf("Transport.Protocol = %q, want %q", cfg.Transport.Protocol, ControlProtocolV2)
	}
	if cfg.P2P != (P2PConfig{Protocol: P2PProtocolQUICUDPv1, Listen: "0.0.0.0:0"}) {
		t.Fatalf("P2P = %+v", cfg.P2P)
	}
	if cfg.NodeID != "laptop" || cfg.VirtualIP != "10.77.0.2" || cfg.CAFile != "../certs/ca.pem" || cfg.CertFile != "../certs/laptop.pem" || cfg.KeyFile != "../certs/laptop-key.pem" {
		t.Fatalf("migrated identity = %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.Routes, []Route{{CIDR: "192.168.1.0/24"}}) {
		t.Fatalf("Routes = %+v", cfg.Routes)
	}
	backupPath := path + ".v1.bak"
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, original) {
		t.Fatalf("backup differs from original\n got: %s\nwant: %s", backup, original)
	}
	activeAfterFirstLoad, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	activeAfterSecondLoad, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backupAfterSecondLoad, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(activeAfterSecondLoad, activeAfterFirstLoad) || !bytes.Equal(backupAfterSecondLoad, original) {
		t.Fatal("second Load rewrote the active config or v1 backup")
	}
}

func TestLoadMigratesV1HubWithoutDataPlaneFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.json")
	original := []byte(`{
  "node_id": "home-hub",
  "mode": "hub",
  "transport": {"protocol": "tcp_tls_v1", "listen": "0.0.0.0:8443"},
  "listen": "0.0.0.0:8443",
  "ca_file": "../certs/ca.pem",
  "cert_file": "../certs/home-hub.pem",
  "key_file": "../certs/home-hub-key.pem",
  "virtual_ip": "10.77.0.1",
  "routes": [{"cidr": "192.168.1.0/24"}],
  "mtu": 1280,
  "device": {"type": "tun", "name": "meshlink0"},
  "setup": {"enabled": true, "address": "10.77.0.1/24", "forwarding": true}
}
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != ConfigVersion || cfg.Mode != "hub" || cfg.NodeID != "home-hub" || cfg.Listen != "0.0.0.0:8443" {
		t.Fatalf("migrated coordinator identity = %+v", cfg)
	}
	if cfg.Transport.Protocol != ControlProtocolV2 || cfg.Transport.Listen != "0.0.0.0:8443" {
		t.Fatalf("migrated coordinator transport = %+v", cfg.Transport)
	}
	if cfg.CAFile != "../certs/ca.pem" || cfg.CertFile != "../certs/home-hub.pem" || cfg.KeyFile != "../certs/home-hub-key.pem" {
		t.Fatalf("migrated coordinator certificates = %+v", cfg)
	}
	if cfg.NetworkCIDR != "10.77.0.0/24" {
		t.Fatalf("NetworkCIDR = %q, want 10.77.0.0/24", cfg.NetworkCIDR)
	}
	if cfg.VirtualIP != "" || cfg.Routes != nil || cfg.MTU != 0 || cfg.Device != (DeviceConfig{}) || !reflect.DeepEqual(cfg.Setup, SetupConfig{}) || cfg.P2P != (P2PConfig{}) {
		t.Fatalf("coordinator retained data-plane fields: %+v", cfg)
	}
}

func TestLoadLeavesV1ActiveConfigUnchangedWhenBackupExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active.json")
	original := []byte(`{"node_id":"laptop","mode":"spoke","connect":"127.0.0.1:8443","ca_file":"ca.pem","cert_file":"node.pem","key_file":"node-key.pem","virtual_ip":"10.77.0.2"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".v1.bak", []byte("existing backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load returned nil error with an existing v1 backup")
	}
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(active, original) {
		t.Fatalf("active config changed after backup failure: %s", active)
	}
}

func TestLoadLeavesV1ActiveConfigUnchangedWhenReplacementFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active.json")
	original := []byte(`{"node_id":"laptop","mode":"spoke","connect":"127.0.0.1:8443","ca_file":"ca.pem","cert_file":"node.pem","key_file":"node-key.pem","virtual_ip":"10.77.0.2"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	replacementErr := errors.New("injected replacement failure")
	_, err := loadWithWriter(path, func(target string, cfg Config) error {
		return writeWithRename(target, cfg, func(oldPath, newPath string) error {
			if newPath != path {
				t.Fatalf("replacement target = %q, want %q", newPath, path)
			}
			return replacementErr
		})
	})
	if !errors.Is(err, replacementErr) {
		t.Fatalf("Load error = %v, want injected replacement failure", err)
	}

	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(active, original) {
		t.Fatalf("active config changed after replacement failure: %s", active)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, original) {
		t.Fatalf("backup differs from original after replacement failure: %s", backup)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".active.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files remain after replacement failure: %v", temps)
	}
}

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
	cfg.NetworkCIDR = "10.77.0.0/24"
	cfg.VirtualIP = "10.77.0.1"
	cfg.Transport = TransportConfig{
		Protocol: ControlProtocolV2,
		Listen:   "0.0.0.0:8443",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:8443" {
		t.Fatalf("Listen = %q, want transport listen", cfg.Listen)
	}
	if cfg.Transport.Protocol != ControlProtocolV2 {
		t.Fatalf("Transport.Protocol = %q, want %s", cfg.Transport.Protocol, ControlProtocolV2)
	}
}

func TestValidateNormalizesSpokeTransportConnect(t *testing.T) {
	cfg := baseConfig()
	cfg.Connect = ""
	cfg.ServerName = ""
	cfg.Transport = TransportConfig{
		Protocol:   ControlProtocolV2,
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
		Version:   ConfigVersion,
		NodeID:    "node-a",
		Mode:      "spoke",
		Connect:   "127.0.0.1:8443",
		CAFile:    "ca.pem",
		CertFile:  "node.pem",
		KeyFile:   "node-key.pem",
		VirtualIP: "10.77.0.2",
	}
}
