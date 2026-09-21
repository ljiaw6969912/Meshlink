package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meshlink/internal/config"
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

type DeviceList struct {
	UpdatedAt        time.Time          `json:"updated_at,omitempty"`
	NetworkState     networkstate.State `json:"network_state"`
	CoordinatorState string             `json:"coordinator_state,omitempty"`
	P2PListen        string             `json:"p2p_listen,omitempty"`
	Nodes            []DeviceSummary    `json:"nodes"`
}

type DeviceSummary struct {
	p2p.ConnectionStatus
	Kind          string    `json:"kind"`
	NodeID        string    `json:"node_id"`
	DisplayName   string    `json:"display_name,omitempty"`
	Status        string    `json:"status"`
	VirtualIP     string    `json:"virtual_ip,omitempty"`
	RemoteAddr    string    `json:"remote_addr,omitempty"`
	Fingerprint   string    `json:"fingerprint,omitempty"`
	CommonName    string    `json:"common_name,omitempty"`
	LastSeen      time.Time `json:"last_seen,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
}

func (m Manager) Devices(serviceName string) (DeviceList, error) {
	if _, err := os.Stat(m.activeConfigPath()); err != nil {
		if os.IsNotExist(err) {
			return DeviceList{NetworkState: networkstate.NotJoined, CoordinatorState: "disconnected", Nodes: []DeviceSummary{}}, nil
		}
		return DeviceList{}, err
	}
	statusPath := statusPath(m.activeConfigPath(), serviceName)
	if cfg, err := config.Load(m.activeConfigPath()); err == nil && cfg.Mode == "hub" && cfg.ServerNodeConfig != "" {
		statusPath += ".server-node.json"
	}
	if b, err := os.ReadFile(statusPath); err == nil {
		var status runtimeStatus
		if err := json.Unmarshal(b, &status); err != nil {
			return DeviceList{}, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			return DeviceList{}, err
		}
		_, hasCoordinatorState := fields["coordinator_state"]
		_, hasP2PListen := fields["p2p_listen"]
		status.TruthfulP2P = hasCoordinatorState || hasP2PListen
		return m.deviceListFromRuntime(status)
	}
	return m.devicesFromRegistry()
}

type runtimeStatus struct {
	UpdatedAt        time.Time          `json:"updated_at"`
	State            string             `json:"state"`
	NetworkState     networkstate.State `json:"network_state,omitempty"`
	CoordinatorState string             `json:"coordinator_state,omitempty"`
	P2PListen        string             `json:"p2p_listen,omitempty"`
	TruthfulP2P      bool               `json:"-"`
	Self             nodeStatus         `json:"self"`
	Peers            []peerStatus       `json:"peers"`
}

type nodeStatus struct {
	DisplayName string `json:"display_name,omitempty"`
	p2p.ConnectionStatus
	NodeID      string   `json:"node_id"`
	Mode        string   `json:"mode"`
	VirtualIP   string   `json:"virtual_ip"`
	Listen      string   `json:"listen,omitempty"`
	Connect     string   `json:"connect,omitempty"`
	Routes      []string `json:"routes,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	CommonName  string   `json:"common_name,omitempty"`
}

type peerStatus struct {
	DisplayName string `json:"display_name,omitempty"`
	p2p.ConnectionStatus
	NodeID         string     `json:"node_id"`
	Mode           string     `json:"mode,omitempty"`
	Status         string     `json:"status,omitempty"`
	VirtualIP      string     `json:"virtual_ip,omitempty"`
	Routes         []string   `json:"routes,omitempty"`
	RemoteAddr     string     `json:"remote_addr,omitempty"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	CommonName     string     `json:"common_name,omitempty"`
	ConnectedAt    time.Time  `json:"connected_at"`
	LastSeen       time.Time  `json:"last_seen"`
	LastHeartbeat  time.Time  `json:"last_heartbeat,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
}

func (m Manager) deviceListFromRuntime(status runtimeStatus) (DeviceList, error) {
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return DeviceList{}, err
	}
	policies := registryPolicies(registry)
	effective := networkstate.Effective(status.NetworkState, status.State)
	onlineAllowed := networkstate.IsOnline(effective)
	truthfulP2P := status.TruthfulP2P || status.CoordinatorState != "" || status.P2PListen != ""
	coordinatorState := runtimeCoordinatorState(status.CoordinatorState, effective, status.State)
	membershipState := coordinatorState
	if proto.IsInfrastructureMode(status.Self.Mode) && strings.EqualFold(status.State, "running") && onlineAllowed {
		// The coordinator owns membership and has no upstream control connection.
		coordinatorState = "serving"
		membershipState = "connected"
	}
	readAt := time.Now().UTC()
	selfStatus := runtimeSelfStatus(status.State)
	if !truthfulP2P && !onlineAllowed && selfStatus == "online" {
		selfStatus = "offline"
	}
	nodes := make([]DeviceSummary, 0, len(status.Peers)+1)
	if !proto.IsInfrastructureMode(status.Self.Mode) {
		connectionStatus := runtimeNodeConnectionStatus(status.Self.ConnectionStatus, selfStatus)
		if truthfulP2P {
			connectionStatus, _ = runtimeP2PConnectionStatus(status.Self.ConnectionStatus, selfStatus, membershipState, time.Time{}, readAt)
		}
		nodes = append(nodes, DeviceSummary{
			ConnectionStatus: connectionStatus,
			Kind:             "self",
			NodeID:           status.Self.NodeID,
			DisplayName:      status.Self.DisplayName,
			Status:           selfStatus,
			VirtualIP:        status.Self.VirtualIP,
			Fingerprint:      status.Self.Fingerprint,
			CommonName:       status.Self.CommonName,
			LastSeen:         status.UpdatedAt,
		})
	}
	for _, peer := range status.Peers {
		if proto.IsInfrastructureMode(peer.Mode) {
			continue
		}
		policy := policies.find(peer.NodeID, peer.Fingerprint)
		if policy != nil && policy.DeletedAt != nil {
			continue
		}
		peerStatus := peerStatusText(peer)
		connectionStatus := runtimeNodeConnectionStatus(peer.ConnectionStatus, peerStatus)
		directHealthy := false
		if truthfulP2P {
			connectionStatus, directHealthy = runtimeP2PConnectionStatus(peer.ConnectionStatus, peerStatus, membershipState, peer.LastHeartbeat, readAt)
			if directHealthy {
				peerStatus = "online"
			} else if membershipState != "connected" && strings.EqualFold(peerStatus, "online") {
				peerStatus = "offline"
			}
		}
		displayName := peer.DisplayName
		if policy != nil {
			displayName = policy.DisplayName
			if policy.Disabled {
				peerStatus = "disabled"
				if truthfulP2P {
					connectionStatus, _ = runtimeP2PConnectionStatus(p2p.ConnectionStatus{}, peerStatus, membershipState, time.Time{}, readAt)
				}
			}
		}
		if !truthfulP2P && !onlineAllowed && peerStatus == "online" {
			peerStatus = "offline"
		}
		if !truthfulP2P {
			connectionStatus = runtimeNodeConnectionStatus(peer.ConnectionStatus, peerStatus)
		}
		nodes = append(nodes, DeviceSummary{
			ConnectionStatus: connectionStatus,
			Kind:             "peer",
			NodeID:           peer.NodeID,
			DisplayName:      displayName,
			Status:           peerStatus,
			VirtualIP:        peer.VirtualIP,
			RemoteAddr:       peer.RemoteAddr,
			Fingerprint:      peer.Fingerprint,
			CommonName:       peer.CommonName,
			LastSeen:         peer.LastSeen,
			LastHeartbeat:    peer.LastHeartbeat,
			ErrorCode:        runtimeP2PErrorCode(peer.ErrorCode, connectionStatus.LastError),
		})
	}
	return DeviceList{
		UpdatedAt:        status.UpdatedAt,
		NetworkState:     effective,
		CoordinatorState: coordinatorState,
		P2PListen:        strings.TrimSpace(status.P2PListen),
		Nodes:            nodes,
	}, nil
}

func (m Manager) devicesFromRegistry() (DeviceList, error) {
	var nodes []DeviceSummary
	readAt := time.Now().UTC()
	selfID := ""
	if cfg, err := config.Load(m.activeConfigPath()); err == nil {
		if cfg.Mode == "hub" && cfg.ServerNodeConfig != "" {
			child, err := config.Load(resolveConfigPath(m.configsDir(), cfg.ServerNodeConfig))
			if err != nil {
				return DeviceList{}, err
			}
			cfg = child
		}
		selfID = cfg.NodeID
		connectionStatus, _ := runtimeP2PConnectionStatus(p2p.ConnectionStatus{}, "offline", "disconnected", time.Time{}, readAt)
		nodes = append(nodes, DeviceSummary{
			ConnectionStatus: connectionStatus,
			Kind:             "self",
			NodeID:           cfg.NodeID,
			DisplayName:      cfg.DisplayName,
			Status:           "offline",
			VirtualIP:        cfg.VirtualIP,
		})
	}
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return DeviceList{}, err
	}
	for _, node := range registry.Nodes {
		if node.DeletedAt != nil || node.NodeID == selfID {
			continue
		}
		status := "offline"
		if node.Disabled {
			status = "disabled"
		} else if strings.EqualFold(node.Status, "revoked") {
			status = "revoked"
		}
		connectionStatus, _ := runtimeP2PConnectionStatus(p2p.ConnectionStatus{}, status, "disconnected", time.Time{}, readAt)
		nodes = append(nodes, DeviceSummary{
			ConnectionStatus: connectionStatus,
			Kind:             "peer",
			NodeID:           node.NodeID,
			DisplayName:      node.DisplayName,
			Status:           status,
			VirtualIP:        node.VirtualIP,
			RemoteAddr:       node.SourceAddr,
			Fingerprint:      node.CertFingerprint,
			LastSeen:         node.LastSeen,
		})
	}
	return DeviceList{NetworkState: networkstate.Disconnected, CoordinatorState: "disconnected", Nodes: nodes}, nil
}

func runtimeSelfStatus(state string) string {
	if strings.EqualFold(state, "running") {
		return "online"
	}
	return "offline"
}

func peerStatusText(peer peerStatus) string {
	if peer.Status == "" {
		return "online"
	}
	return peer.Status
}

func runtimeNodeConnectionStatus(status p2p.ConnectionStatus, nodeStatus string) p2p.ConnectionStatus {
	if status.PathType == "" {
		status.PathState = p2p.PathState("offline_or_unknown")
		if strings.EqualFold(nodeStatus, "online") {
			status.PathState = p2p.PathStateIdle
		}
		status.QualityScore = 0
		return status
	}
	return p2p.NormalizeConnectionStatus(status, p2p.ConnectionStatusDefaults{
		Online: strings.EqualFold(nodeStatus, "online"),
	})
}

const directHeartbeatFreshness = 35 * time.Second

func runtimeCoordinatorState(explicit string, networkState networkstate.State, processState string) string {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "connected":
		return "connected"
	case "connecting", "reconnecting":
		return "reconnecting"
	case "disconnected", "stopped", "failed":
		return "disconnected"
	}
	if strings.EqualFold(processState, "running") && networkstate.IsOnline(networkState) {
		return "connected"
	}
	if networkState == networkstate.Connecting || networkState == networkstate.Reconnecting {
		return "reconnecting"
	}
	return "disconnected"
}

func runtimeP2PConnectionStatus(status p2p.ConnectionStatus, nodeStatus, coordinatorState string, lastHeartbeat, observedAt time.Time) (p2p.ConnectionStatus, bool) {
	pathType := strings.ToLower(strings.TrimSpace(string(status.PathType)))
	switch pathType {
	case "lan_direct", "public_direct":
		status.PathType = p2p.PathType(pathType)
	default:
		status.PathType = ""
	}

	pathState := strings.ToLower(strings.TrimSpace(string(status.PathState)))
	switch pathState {
	case "lan_direct_connected":
		pathState = "lan_direct"
	case "public_direct_connected":
		pathState = "public_direct"
	case "connecting", "trying_lan_direct", "trying_public_direct":
		pathState = "requesting"
	case "offline":
		pathState = "offline_or_unknown"
	case "fallback_relay":
		pathState = "failed"
		status.LastError = "direct_unreachable_no_relay"
	}
	switch pathState {
	case "idle", "requesting", "preparing", "punching", "authenticating", "lan_direct", "public_direct", "reconnecting", "waiting_coordinator", "failed", "closed", "offline_or_unknown", "rdp-unreachable":
		status.PathState = p2p.PathState(pathState)
	default:
		status.PathState = ""
	}

	if pathState == "lan_direct" {
		status.PathType = p2p.PathType("lan_direct")
	} else if pathState == "public_direct" {
		status.PathType = p2p.PathType("public_direct")
	} else {
		status.PathType = ""
	}

	directHealthy := (pathState == "lan_direct" || pathState == "public_direct") && heartbeatIsCurrent(lastHeartbeat, observedAt)
	if !directHealthy && (pathState == "lan_direct" || pathState == "public_direct") {
		status.PathType = ""
		status.PathState = p2p.PathState("offline_or_unknown")
		pathState = "offline_or_unknown"
	}
	if status.PathState == "" {
		if strings.EqualFold(nodeStatus, "online") && coordinatorState == "connected" {
			status.PathState = p2p.PathState("idle")
		} else {
			status.PathState = p2p.PathState("offline_or_unknown")
		}
	}
	if coordinatorState != "connected" && status.PathState == p2p.PathState("idle") {
		status.PathState = p2p.PathState("offline_or_unknown")
	}
	if strings.EqualFold(nodeStatus, "disabled") || strings.EqualFold(nodeStatus, "revoked") {
		status.PathType = ""
		status.PathState = p2p.PathState("closed")
		directHealthy = false
	}

	status.RelayBytesIn = 0
	status.RelayBytesOut = 0
	if status.SwitchFromPath == p2p.PathTypeRelay {
		status.SwitchFromPath = ""
	}
	if status.SwitchToPath == p2p.PathTypeRelay {
		status.SwitchToPath = ""
	}
	if !directHealthy {
		status.QualityScore = 0
		status.LatencyMS = 0
		status.PacketLossPermille = 0
		status.JitterMS = 0
	}
	status.LastError = runtimeP2PErrorCode("", status.LastError)
	return status, directHealthy
}

func heartbeatIsCurrent(lastHeartbeat, observedAt time.Time) bool {
	if lastHeartbeat.IsZero() {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	age := observedAt.UTC().Sub(lastHeartbeat.UTC())
	return age >= 0 && age <= directHeartbeatFreshness
}

func runtimeP2PErrorCode(errorCode, fallback string) string {
	code := strings.ToLower(strings.TrimSpace(errorCode))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(fallback))
	}
	switch code {
	case "control_upgrade_required", "control_unavailable", "peer_offline", "peer_revoked", "route_conflict", "candidate_unavailable", "udp_probe_failed", "hole_punch_timeout", "quic_handshake_failed", "peer_identity_mismatch", "session_authorization_failed", "direct_heartbeat_timeout", "direct_unreachable_no_relay":
		return code
	default:
		return ""
	}
}

type registryPolicyIndex struct {
	byNode        map[string]*RegisteredNode
	byFingerprint map[string]*RegisteredNode
}

func registryPolicies(registry DeviceRegistry) registryPolicyIndex {
	index := registryPolicyIndex{
		byNode:        make(map[string]*RegisteredNode),
		byFingerprint: make(map[string]*RegisteredNode),
	}
	for i := range registry.Nodes {
		node := &registry.Nodes[i]
		if node.NodeID != "" {
			index.byNode[strings.ToLower(node.NodeID)] = node
		}
		if node.CertFingerprint != "" {
			index.byFingerprint[strings.ToLower(node.CertFingerprint)] = node
		}
	}
	return index
}

func (i registryPolicyIndex) find(nodeID, fingerprint string) *RegisteredNode {
	if nodeID != "" {
		if node := i.byNode[strings.ToLower(nodeID)]; node != nil {
			return node
		}
	}
	if fingerprint != "" {
		return i.byFingerprint[strings.ToLower(fingerprint)]
	}
	return nil
}

func statusPath(configPath, serviceName string) string {
	if serviceName == "" {
		serviceName = "mesh-agent"
	}
	return filepath.Join(filepath.Dir(configPath), "logs", serviceName+".status.json")
}
