package proto

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

type ClientHello struct {
	ProtocolVersion int      `json:"protocol_version"`
	Role            string   `json:"role"`
	NodeID          string   `json:"node_id"`
	VirtualIP       string   `json:"virtual_ip"`
	Routes          []string `json:"routes"`
	MTU             int      `json:"mtu"`
	Capabilities    []string `json:"capabilities"`
}

func (h ClientHello) Validate() error {
	if h.ProtocolVersion != ControlProtocolVersion {
		return fmt.Errorf("unsupported control protocol version %d", h.ProtocolVersion)
	}
	if h.Role != "peer" {
		return fmt.Errorf("client hello role must be peer, got %q", h.Role)
	}
	if strings.TrimSpace(h.NodeID) == "" {
		return fmt.Errorf("client hello node_id is required")
	}
	addr, err := netip.ParseAddr(h.VirtualIP)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("client hello virtual_ip must be IPv4, got %q", h.VirtualIP)
	}
	if h.MTU < 576 || h.MTU > 9000 {
		return fmt.Errorf("client hello mtu out of range: %d", h.MTU)
	}
	for _, capability := range h.Capabilities {
		if capability == "quic_udp_v1" {
			return nil
		}
	}
	return fmt.Errorf("client hello requires quic_udp_v1 capability")
}

type Hello struct {
	NodeID    string   `json:"node_id"`
	VirtualIP string   `json:"virtual_ip"`
	Routes    []string `json:"routes,omitempty"`
	MTU       int      `json:"mtu"`
}

func (h Hello) Marshal() ([]byte, error) {
	return json.Marshal(h)
}

func ParseHello(payload []byte) (Hello, error) {
	var h Hello
	if err := json.Unmarshal(payload, &h); err != nil {
		return Hello{}, err
	}
	addr, err := netip.ParseAddr(h.VirtualIP)
	if err != nil {
		return Hello{}, err
	}
	if !addr.Is4() {
		return Hello{}, fmt.Errorf("IPv6 virtual IP is not supported: %s", h.VirtualIP)
	}
	for _, route := range h.Routes {
		prefix, err := netip.ParsePrefix(route)
		if err != nil {
			return Hello{}, err
		}
		if !prefix.Addr().Is4() {
			return Hello{}, fmt.Errorf("IPv6 route is not supported: %s", route)
		}
	}
	return h, nil
}
