// Package deviceidentity discovers a usable physical network adapter MAC.
package deviceidentity

import (
	"errors"
	"net"
	"strings"
)

// NormalizeMAC returns the canonical lowercase, colon-separated six-byte unicast
// address. Locally administered addresses are valid (including Wi-Fi privacy MACs).
func NormalizeMAC(value string) (string, error) {
	mac, err := net.ParseMAC(strings.TrimSpace(value))
	if err != nil || len(mac) != 6 {
		return "", errors.New("MAC must be a six-byte address")
	}
	if mac[0]&1 != 0 || (mac[0]|mac[1]|mac[2]|mac[3]|mac[4]|mac[5]) == 0 {
		return "", errors.New("MAC must be a nonzero unicast address")
	}
	return mac.String(), nil
}

type adapter struct {
	name, permanent, current string
	hardware, filter         bool
	kind                     uint32
}

// Deliberately ignore connection state, interface index, and enumeration order.
// Prefer the permanent address to avoid Wi-Fi randomization changing identity.
func selectMAC(adapters []adapter) string {
	best := ""
	for _, a := range adapters {
		if !a.hardware || a.filter || (a.kind != 6 && a.kind != 71) || virtualName(a.name) {
			continue
		}
		mac, err := NormalizeMAC(a.permanent)
		if err != nil {
			mac, err = NormalizeMAC(a.current)
		}
		if err == nil && (best == "" || mac < best) {
			best = mac
		}
	}
	return best
}

func virtualName(name string) bool {
	name = strings.ToLower(name)
	for _, marker := range []string{"meshlink", "wintun", "wireguard", "tunnel", "tap-", "virtual", "hyper-v", "vmware", "virtio", "loopback", "proxy", "clash", "sing-box", "tailscale", "zerotier"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
