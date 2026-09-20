package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

const (
	ConfigVersion        = 2
	ControlProtocolV2    = "tcp_tls_control_v2"
	P2PProtocolQUICUDPv1 = "quic_udp_v1"

	defaultP2PListen        = "0.0.0.0:0"
	legacyControlProtocolV1 = "tcp_tls_v1"
)

type Config struct {
	Version     int             `json:"version"`
	NodeID      string          `json:"node_id"`
	DisplayName string          `json:"display_name,omitempty"`
	Mode        string          `json:"mode"`
	Transport   TransportConfig `json:"transport,omitempty"`
	Listen      string          `json:"listen,omitempty"`
	Connect     string          `json:"connect,omitempty"`
	ServerName  string          `json:"server_name,omitempty"`
	CAFile      string          `json:"ca_file"`
	CertFile    string          `json:"cert_file"`
	KeyFile     string          `json:"key_file"`
	NetworkCIDR string          `json:"network_cidr,omitempty"`
	VirtualIP   string          `json:"virtual_ip,omitempty"`
	Routes      []Route         `json:"routes,omitempty"`
	MTU         int             `json:"mtu,omitempty"`
	Device      DeviceConfig    `json:"device,omitzero"`
	Setup       SetupConfig     `json:"setup,omitzero"`
	P2P         P2PConfig       `json:"p2p,omitzero"`
	// Optional local mesh participant owned by a hub, sharing its UDP port.
	ServerNodeConfig     string `json:"server_node_config,omitempty"`
	ServerPublicEndpoint string `json:"server_public_endpoint,omitempty"`
}

type TransportConfig struct {
	Protocol   string `json:"protocol,omitempty"`
	Listen     string `json:"listen,omitempty"`
	Connect    string `json:"connect,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

type P2PConfig struct {
	Protocol string `json:"protocol,omitempty"`
	Listen   string `json:"listen,omitempty"`
}

type Route struct {
	CIDR string `json:"cidr"`
}

type DeviceConfig struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type SetupConfig struct {
	Enabled    bool    `json:"enabled,omitempty"`
	Address    string  `json:"address,omitempty"`
	Routes     []Route `json:"routes,omitempty"`
	Forwarding bool    `json:"forwarding,omitempty"`
	NAT        NAT     `json:"nat,omitempty"`
}

type NAT struct {
	Enabled        bool   `json:"enabled,omitempty"`
	Name           string `json:"name,omitempty"`
	InternalPrefix string `json:"internal_prefix,omitempty"`
}

func Load(path string) (*Config, error) {
	return loadWithWriter(path, Write)
}

func loadWithWriter(path string, write func(string, Config) error) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	cfg, migrated, err := migrateV1(cfg)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if migrated {
		if err := writeExclusive(path+".v1.bak", b); err != nil {
			return nil, fmt.Errorf("create v1 config backup: %w", err)
		}
		if err := write(path, cfg); err != nil {
			return nil, fmt.Errorf("write migrated v2 config: %w", err)
		}
	}
	return &cfg, nil
}

func Write(path string, cfg Config) error {
	return writeWithRename(path, cfg, os.Rename)
}

func writeWithRename(path string, cfg Config, rename func(string, string) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := rename(tmpPath, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func migrateV1(cfg Config) (Config, bool, error) {
	if cfg.Version != 0 {
		return cfg, false, nil
	}
	if cfg.Transport.Protocol != "" && cfg.Transport.Protocol != legacyControlProtocolV1 {
		return Config{}, false, fmt.Errorf("unsupported v1 transport protocol %q", cfg.Transport.Protocol)
	}
	cfg.Version = ConfigVersion
	cfg.Transport.Protocol = ControlProtocolV2
	switch cfg.Mode {
	case "hub":
		networkCIDR, err := legacyHubNetworkCIDR(cfg)
		if err != nil {
			return Config{}, false, err
		}
		cfg.NetworkCIDR = networkCIDR
		cfg.VirtualIP = ""
		cfg.Routes = nil
		cfg.MTU = 0
		cfg.Device = DeviceConfig{}
		cfg.Setup = SetupConfig{}
		cfg.P2P = P2PConfig{}
	case "spoke":
		cfg.P2P = P2PConfig{Protocol: P2PProtocolQUICUDPv1, Listen: defaultP2PListen}
	}
	return cfg, true, nil
}

func legacyHubNetworkCIDR(cfg Config) (string, error) {
	if cfg.Setup.Address != "" {
		prefix, err := parseIPv4Prefix("setup.address", cfg.Setup.Address)
		if err != nil {
			return "", err
		}
		return prefix.Masked().String(), nil
	}
	addr, err := parseIPv4Addr("virtual_ip", cfg.VirtualIP)
	if err != nil {
		return "", fmt.Errorf("derive hub network_cidr: %w", err)
	}
	return netip.PrefixFrom(addr, 24).Masked().String(), nil
}

func writeExclusive(path string, b []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	keep = true
	return nil
}

func (c *Config) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.NodeID == "" {
		return errors.New("node_id is required")
	}
	if err := c.normalizeTransport(); err != nil {
		return err
	}
	switch c.Mode {
	case "hub":
		if c.Listen == "" {
			return errors.New("listen is required for hub mode")
		}
		if err := validateIPv4Endpoint("listen", c.Listen, true); err != nil {
			return err
		}
		if c.NetworkCIDR == "" {
			return errors.New("network_cidr is required for hub mode")
		}
		networkCIDR, err := validatePrivateMeshRoute("network_cidr", c.NetworkCIDR)
		if err != nil {
			return err
		}
		c.NetworkCIDR = networkCIDR.String()
		if c.ServerNodeConfig != "" {
			if err := validateIPv4Endpoint("server_public_endpoint", c.ServerPublicEndpoint, false); err != nil {
				return err
			}
		}
	case "spoke":
		if c.ServerNodeConfig != "" || c.ServerPublicEndpoint != "" {
			return errors.New("server node settings require hub mode")
		}
		if c.Connect == "" {
			return errors.New("connect is required for spoke mode")
		}
		if err := validateIPv4Endpoint("connect", c.Connect, false); err != nil {
			return err
		}
		if err := c.normalizeP2P(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("mode must be hub or spoke, got %q", c.Mode)
	}
	if err := validateIPv4ServerName(c.ServerName); err != nil {
		return err
	}
	if c.CAFile == "" || c.CertFile == "" || c.KeyFile == "" {
		return errors.New("ca_file, cert_file and key_file are required")
	}
	if c.Mode == "hub" {
		return nil
	}
	virtualIP, err := parseIPv4Addr("virtual_ip", c.VirtualIP)
	if err != nil {
		return err
	}
	for _, route := range c.Routes {
		if _, err := validatePrivateMeshRoute("route", route.CIDR); err != nil {
			return err
		}
	}
	if c.MTU == 0 {
		c.MTU = 1280
	}
	if c.MTU < 576 || c.MTU > 9000 {
		return fmt.Errorf("mtu out of range: %d", c.MTU)
	}
	if c.Device.Type == "" {
		c.Device.Type = "null"
	}
	if c.Setup.Enabled {
		if c.Setup.Address != "" {
			prefix, err := parseIPv4Prefix("setup.address", c.Setup.Address)
			if err != nil {
				return err
			}
			if !prefix.Contains(virtualIP) {
				return fmt.Errorf("setup.address %q does not contain virtual_ip %q", c.Setup.Address, c.VirtualIP)
			}
		}
		for _, route := range c.Setup.Routes {
			if _, err := validatePrivateMeshRoute("setup route", route.CIDR); err != nil {
				return err
			}
		}
		if c.Setup.NAT.Enabled && c.Setup.NAT.InternalPrefix != "" {
			if _, err := validatePrivateMeshRoute("setup.nat.internal_prefix", c.Setup.NAT.InternalPrefix); err != nil {
				return err
			}
		}
	} else if c.Setup.NAT.Enabled {
		return errors.New("setup.enabled must be true when setup.nat.enabled is true")
	}
	return nil
}

func (c *Config) normalizeTransport() error {
	if c.Transport.Protocol == "" {
		c.Transport.Protocol = ControlProtocolV2
	}
	if c.Transport.Protocol != ControlProtocolV2 {
		return fmt.Errorf("unsupported transport protocol %q", c.Transport.Protocol)
	}
	if c.Transport.ServerName != "" {
		if c.ServerName != "" && c.ServerName != c.Transport.ServerName {
			return fmt.Errorf("server_name %q conflicts with transport.server_name %q", c.ServerName, c.Transport.ServerName)
		}
		c.ServerName = c.Transport.ServerName
	} else if c.ServerName != "" {
		c.Transport.ServerName = c.ServerName
	}
	switch c.Mode {
	case "hub":
		if c.Transport.Listen != "" {
			if c.Listen != "" && c.Listen != c.Transport.Listen {
				return fmt.Errorf("listen %q conflicts with transport.listen %q", c.Listen, c.Transport.Listen)
			}
			c.Listen = c.Transport.Listen
		} else if c.Listen != "" {
			c.Transport.Listen = c.Listen
		}
	case "spoke":
		if c.Transport.Connect != "" {
			if c.Connect != "" && c.Connect != c.Transport.Connect {
				return fmt.Errorf("connect %q conflicts with transport.connect %q", c.Connect, c.Transport.Connect)
			}
			c.Connect = c.Transport.Connect
		} else if c.Connect != "" {
			c.Transport.Connect = c.Connect
		}
	}
	return nil
}

func (c *Config) normalizeP2P() error {
	if c.P2P.Protocol == "" {
		c.P2P.Protocol = P2PProtocolQUICUDPv1
	}
	if c.P2P.Protocol != P2PProtocolQUICUDPv1 {
		return fmt.Errorf("unsupported p2p protocol %q", c.P2P.Protocol)
	}
	if c.P2P.Listen == "" {
		c.P2P.Listen = defaultP2PListen
	}
	return validateIPv4Endpoint("p2p.listen", c.P2P.Listen, true)
}

func parseIPv4Addr(name, raw string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s: %w", name, err)
	}
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("%s must be IPv4, got %q", name, raw)
	}
	return addr, nil
}

func parseIPv4Prefix(name, raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%s %q: %w", name, raw, err)
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%s must be IPv4, got %q", name, raw)
	}
	return prefix, nil
}

func validatePrivateMeshRoute(name, raw string) (netip.Prefix, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "0.0.0.0/0" || trimmed == "::/0" {
		return netip.Prefix{}, routeBoundaryError(name, raw)
	}
	prefix, err := parseIPv4Prefix(name, raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	prefix = prefix.Masked()
	if prefix.Bits() == 0 || !isAllowedPrivateRoute(prefix) {
		return netip.Prefix{}, routeBoundaryError(name, raw)
	}
	return prefix, nil
}

func routeBoundaryError(name, raw string) error {
	return fmt.Errorf("%s %q is not allowed: Meshlink 不做全局代理或公网出口，只允许私有远程桌面组网使用的 RFC1918 内网路由", name, raw)
}

func isAllowedPrivateRoute(prefix netip.Prefix) bool {
	for _, allowed := range []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	} {
		if allowed.Contains(prefix.Addr()) && prefix.Bits() >= allowed.Bits() {
			return true
		}
	}
	return false
}

func validateIPv4Endpoint(name, endpoint string, allowEmptyHost bool) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("%s must be host:port IPv4/TCP endpoint: %w", name, err)
	}
	if port == "" {
		return fmt.Errorf("%s port is required", name)
	}
	if host == "" {
		if allowEmptyHost {
			return nil
		}
		return fmt.Errorf("%s host is required", name)
	}
	if addr, err := netip.ParseAddr(host); err == nil && !addr.Is4() {
		return fmt.Errorf("%s must not use IPv6 address %q", name, host)
	}
	return nil
}

func validateIPv4ServerName(serverName string) error {
	if serverName == "" {
		return nil
	}
	if addr, err := netip.ParseAddr(serverName); err == nil && !addr.Is4() {
		return fmt.Errorf("server_name must not use IPv6 address %q", serverName)
	}
	return nil
}
