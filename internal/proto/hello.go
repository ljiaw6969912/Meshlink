package proto

import (
	"encoding/json"
	"fmt"
	"net/netip"
)

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
