package onboarding

import (
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"testing"
)

func TestStoppedServiceCannotReuseSavedConnectedStatus(t *testing.T) {
	saved := DeviceList{NetworkState: networkstate.Connected, CoordinatorState: "serving", Nodes: []DeviceSummary{{NodeID: "A", Kind: "self", Status: "online", ConnectionStatus: p2p.ConnectionStatus{PathType: "public_direct"}}}}
	got := WithServiceRunning(saved, false)
	if got.NetworkState == networkstate.Connected || got.CoordinatorState == "serving" || got.Nodes[0].Status == "online" || got.Nodes[0].PathType != "" {
		t.Fatalf("stale online state: %+v", got)
	}
	if saved.Nodes[0].Status != "online" {
		t.Fatal("modified caller's saved status")
	}
}
