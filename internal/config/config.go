package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
)

type Config struct {
	NodeID     string       `json:"node_id"`
	Mode       string       `json:"mode"`
	Listen     string       `json:"listen,omitempty"`
	Connect    string       `json:"connect,omitempty"`
	ServerName string       `json:"server_name,omitempty"`
	CAFile     string       `json:"ca_file"`
	CertFile   string       `json:"cert_file"`
	KeyFile    string       `json:"key_file"`
	VirtualIP  string       `json:"virtual_ip"`
	Routes     []Route      `json:"routes,omitempty"`
	MTU        int          `json:"mtu,omitempty"`
	Device     DeviceConfig `json:"device"`
	Setup      SetupConfig  `json:"setup,omitempty"`
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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.NodeID == "" {
		return errors.New("node_id is required")
	}
	switch c.Mode {
	case "hub":
		if c.Listen == "" {
			return errors.New("listen is required for hub mode")
		}
		if err := validateIPv4Endpoint("listen", c.Listen, true); err != nil {
			return err
		}
	case "spoke":
		if c.Connect == "" {
			return errors.New("connect is required for spoke mode")
		}
		if err := validateIPv4Endpoint("connect", c.Connect, false); err != nil {
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
	virtualIP, err := parseIPv4Addr("virtual_ip", c.VirtualIP)
	if err != nil {
		return err
	}
	for _, route := range c.Routes {
		if _, err := parseIPv4Prefix("route", route.CIDR); err != nil {
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
			if _, err := parseIPv4Prefix("setup route", route.CIDR); err != nil {
				return err
			}
		}
		if c.Setup.NAT.Enabled && c.Setup.NAT.InternalPrefix != "" {
			if _, err := parseIPv4Prefix("setup.nat.internal_prefix", c.Setup.NAT.InternalPrefix); err != nil {
				return err
			}
		}
	} else if c.Setup.NAT.Enabled {
		return errors.New("setup.enabled must be true when setup.nat.enabled is true")
	}
	return nil
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
