package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

func TestMemberPresencePreservesEveryNonDirectSessionState(t *testing.T) {
	for _, state := range []p2p.PathState{p2p.PathStateRequesting, p2p.PathStateReconnecting, p2p.PathStateWaitingCoordinator, p2p.PathStateFailed, p2p.PathStateClosed} {
		t.Run(string(state), func(t *testing.T) {
			s := newStatusStore("", NodeStatus{NodeID: "B"})
			m := proto.Member{NodeID: "C", Status: "online"}
			s.applyMembers([]proto.Member{m}, nil)
			s.applySession(p2p.SessionSnapshot{PeerNodeID: "C", State: state, ErrorCode: "peer_offline"})
			for _, presence := range []string{"online", "offline_or_unknown"} {
				m.Status = presence
				s.applyMembers([]proto.Member{m}, nil)
				encoded, _ := json.Marshal(s.snapshot())
				var got RuntimeStatus
				if err := json.Unmarshal(encoded, &got); err != nil {
					t.Fatal(err)
				}
				p := findAgentPeerStatus(got, "C")
				if p == nil || p.Session == nil || p.Status != string(state) || p.PathState != state || p.ErrorCode != "peer_offline" {
					t.Fatalf("presence %s changed session projection: %s", presence, encoded)
				}
			}
		})
	}
}

func TestStatusStoreDropsInfrastructureAndMarksAllPeersOffline(t *testing.T) {
	store := newStatusStore("", NodeStatus{NodeID: "desk", Mode: "spoke"})
	store.setState("running")
	store.setNetworkState(networkstate.Connected)
	store.applyRoster(proto.Roster{Nodes: []proto.RosterNode{
		{NodeID: "hub", Mode: "hub", Status: PeerStatusOnline},
		{NodeID: "laptop", Mode: "spoke", Status: PeerStatusOnline},
		{NodeID: "office", Mode: "spoke", Status: PeerStatusOnline},
	}})
	if findAgentPeerStatus(store.snapshot(), "hub") != nil {
		t.Fatal("hub must not enter peer status")
	}
	for _, id := range []string{"laptop", "office"} {
		peer := findAgentPeerStatus(store.snapshot(), id)
		if peer == nil || peer.Mode != "spoke" {
			t.Fatalf("peer %q = %+v, want retained spoke mode", id, peer)
		}
	}

	store.markAllPeersOffline()
	status := store.snapshot()
	if status.NetworkState != networkstate.Reconnecting {
		t.Fatalf("network state = %q, want reconnecting", status.NetworkState)
	}
	for _, id := range []string{"laptop", "office"} {
		peer := findAgentPeerStatus(status, id)
		if peer == nil || peer.Status != PeerStatusOffline || peer.PathState != p2p.PathStateOffline {
			t.Fatalf("peer %q = %+v, want offline", id, peer)
		}
	}

	store.applyRoster(proto.Roster{Nodes: []proto.RosterNode{
		{NodeID: "laptop", Mode: "spoke", Status: PeerStatusOnline},
	}})
	status = store.snapshot()
	if findAgentPeerStatus(status, "office") != nil {
		t.Fatal("stale peer must be removed by a later complete roster")
	}
}

func TestStatusStoreTerminateWritesOneConservativeFinalSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "mesh-agent.status.json")
	store := newStatusStore(path, NodeStatus{NodeID: "desk", Mode: "spoke"})
	store.setState("running")
	store.setNetworkState(networkstate.Connected)
	store.applyRoster(proto.Roster{Nodes: []proto.RosterNode{
		{NodeID: "office", Mode: "spoke", Status: PeerStatusOnline},
	}})

	if err := store.terminate(); err != nil {
		t.Fatal(err)
	}

	var status RuntimeStatus
	readAgentStatusJSON(t, path, &status)
	if status.State != "stopped" || status.NetworkState != networkstate.Disconnected {
		t.Fatalf("terminal status = %+v, want stopped and disconnected", status)
	}
	if status.Self.PathState != p2p.PathStateOffline {
		t.Fatalf("terminal self = %+v, want offline projection", status.Self)
	}
	peer := findAgentPeerStatus(status, "office")
	if peer == nil || peer.Status != PeerStatusOffline || peer.PathState != p2p.PathStateOffline || peer.DisconnectedAt == nil {
		t.Fatalf("terminal peer = %+v, want offline projection", peer)
	}
}

func TestStatusStoreWritesConnectionPathQualityFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "mesh-agent.status.json")
	store := newStatusStore(path, NodeStatus{
		NodeID:    "hub",
		Mode:      "hub",
		VirtualIP: "10.77.0.1",
	})
	store.setState("running")
	store.upsertPeer(PeerStatus{
		NodeID:     "laptop",
		Status:     PeerStatusOnline,
		VirtualIP:  "10.77.0.2",
		RemoteAddr: "192.168.1.24:5858",
		ConnectionStatus: p2p.ConnectionStatus{
			PathType:         p2p.PathTypeRelay,
			PathState:        p2p.PathStateFallbackRelay,
			LatencyMS:        88,
			RelayBytesIn:     64,
			RelayBytesOut:    96,
			SwitchCount:      1,
			SwitchReasons:    []string{"direct_quality_degraded"},
			SwitchFromPath:   p2p.PathTypeLANDirect,
			SwitchToPath:     p2p.PathTypeRelay,
			SwitchScoreDelta: 31,
			AutoSwitched:     true,
		},
	})

	var status RuntimeStatus
	readAgentStatusJSON(t, path, &status)
	if status.Self.PathType != "" || status.Self.PathState != p2p.PathStateIdle {
		t.Fatalf("self path = %+v, running process is not an authenticated peer session", status.Self.ConnectionStatus)
	}
	peer := findAgentPeerStatus(status, "laptop")
	if peer == nil {
		t.Fatalf("peer missing from runtime status: %+v", status.Peers)
	}
	if peer.PathType != p2p.PathTypeRelay || peer.PathState != p2p.PathStateFallbackRelay {
		t.Fatalf("peer path = %+v, want relay fallback", peer.ConnectionStatus)
	}
	if peer.LatencyMS != 88 || peer.RelayBytesIn != 64 || peer.RelayBytesOut != 96 || peer.QualityScore == 0 {
		t.Fatalf("peer quality = %+v, want latency, relay bytes, and computed quality", peer.ConnectionStatus)
	}
	if peer.SwitchFromPath != p2p.PathTypeLANDirect || peer.SwitchToPath != p2p.PathTypeRelay || peer.SwitchScoreDelta != 31 || !peer.AutoSwitched {
		t.Fatalf("peer switch summary = %+v, want Task 7E fields", peer.ConnectionStatus)
	}

	store.removePeer("laptop")
	readAgentStatusJSON(t, path, &status)
	peer = findAgentPeerStatus(status, "laptop")
	if peer == nil {
		t.Fatalf("offline peer missing from runtime status: %+v", status.Peers)
	}
	if peer.Status != PeerStatusOffline || peer.PathType != "" || peer.PathState != p2p.PathStateOffline {
		t.Fatalf("offline peer = %+v, want offline path state and no active path type", *peer)
	}
	if peer.QualityScore != 0 || peer.RelayBytesIn != 0 || peer.RelayBytesOut != 0 {
		t.Fatalf("offline peer quality = %+v, want zeroed quality and relay counters", peer.ConnectionStatus)
	}
}

func readAgentStatusJSON(t *testing.T, path string, dest any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dest); err != nil {
		t.Fatal(err)
	}
}

func findAgentPeerStatus(status RuntimeStatus, nodeID string) *PeerStatus {
	for i := range status.Peers {
		if status.Peers[i].NodeID == nodeID {
			return &status.Peers[i]
		}
	}
	return nil
}
