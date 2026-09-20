package onboarding

import (
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
)

// A saved status survives a forced process exit. Windows UI callers combine it
// with the Service Control Manager rather than displaying stale connectivity.
func WithServiceRunning(devices DeviceList, running bool) DeviceList {
	if running {
		return devices
	}
	if devices.NetworkState != networkstate.NotJoined {
		devices.NetworkState = networkstate.Disconnected
	}
	devices.CoordinatorState = "disconnected"
	devices.P2PListen = ""
	devices.Nodes = append([]DeviceSummary(nil), devices.Nodes...)
	for i := range devices.Nodes {
		if devices.Nodes[i].Status != "disabled" && devices.Nodes[i].Status != "revoked" {
			devices.Nodes[i].Status = "offline"
		}
		devices.Nodes[i].ConnectionStatus = p2p.ConnectionStatus{}
	}
	return devices
}
