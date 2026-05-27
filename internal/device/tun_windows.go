//go:build windows

package device

import wgtun "golang.zx2c4.com/wireguard/tun"

func init() {
	wgtun.WintunTunnelType = "Meshlink"
}
