//go:build !windows

package deviceidentity

// LocalMAC returns empty on platforms without physical-adapter discovery.
// Do not guess from net.Interfaces: virtual interfaces can carry ordinary MACs.
func LocalMAC() string { return "" }
