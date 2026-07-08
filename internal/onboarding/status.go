package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meshlink/internal/config"
)

type DeviceList struct {
	UpdatedAt time.Time       `json:"updated_at,omitempty"`
	Nodes     []DeviceSummary `json:"nodes"`
}

type DeviceSummary struct {
	Kind        string    `json:"kind"`
	NodeID      string    `json:"node_id"`
	DisplayName string    `json:"display_name,omitempty"`
	Status      string    `json:"status"`
	VirtualIP   string    `json:"virtual_ip,omitempty"`
	RemoteAddr  string    `json:"remote_addr,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	CommonName  string    `json:"common_name,omitempty"`
	LastSeen    time.Time `json:"last_seen,omitempty"`
}

func (m Manager) Devices(serviceName string) (DeviceList, error) {
	statusPath := statusPath(m.activeConfigPath(), serviceName)
	if b, err := os.ReadFile(statusPath); err == nil {
		var status runtimeStatus
		if err := json.Unmarshal(b, &status); err != nil {
			return DeviceList{}, err
		}
		return m.deviceListFromRuntime(status)
	}
	return m.devicesFromRegistry()
}

type runtimeStatus struct {
	UpdatedAt time.Time    `json:"updated_at"`
	State     string       `json:"state"`
	Self      nodeStatus   `json:"self"`
	Peers     []peerStatus `json:"peers"`
}

type nodeStatus struct {
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
	NodeID         string     `json:"node_id"`
	Status         string     `json:"status,omitempty"`
	VirtualIP      string     `json:"virtual_ip,omitempty"`
	Routes         []string   `json:"routes,omitempty"`
	RemoteAddr     string     `json:"remote_addr,omitempty"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	CommonName     string     `json:"common_name,omitempty"`
	ConnectedAt    time.Time  `json:"connected_at"`
	LastSeen       time.Time  `json:"last_seen"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
}

func (m Manager) deviceListFromRuntime(status runtimeStatus) (DeviceList, error) {
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return DeviceList{}, err
	}
	policies := registryPolicies(registry)
	nodes := []DeviceSummary{
		{
			Kind:        "self",
			NodeID:      status.Self.NodeID,
			Status:      runtimeSelfStatus(status.State),
			VirtualIP:   status.Self.VirtualIP,
			Fingerprint: status.Self.Fingerprint,
			CommonName:  status.Self.CommonName,
			LastSeen:    status.UpdatedAt,
		},
	}
	for _, peer := range status.Peers {
		policy := policies.find(peer.NodeID, peer.Fingerprint)
		if policy != nil && policy.DeletedAt != nil {
			continue
		}
		peerStatus := peerStatusText(peer)
		displayName := ""
		if policy != nil {
			displayName = policy.DisplayName
			if policy.Disabled {
				peerStatus = "disabled"
			}
		}
		nodes = append(nodes, DeviceSummary{
			Kind:        "peer",
			NodeID:      peer.NodeID,
			DisplayName: displayName,
			Status:      peerStatus,
			VirtualIP:   peer.VirtualIP,
			RemoteAddr:  peer.RemoteAddr,
			Fingerprint: peer.Fingerprint,
			CommonName:  peer.CommonName,
			LastSeen:    peer.LastSeen,
		})
	}
	return DeviceList{UpdatedAt: status.UpdatedAt, Nodes: nodes}, nil
}

func (m Manager) devicesFromRegistry() (DeviceList, error) {
	var nodes []DeviceSummary
	if cfg, err := config.Load(m.activeConfigPath()); err == nil {
		nodes = append(nodes, DeviceSummary{
			Kind:      "self",
			NodeID:    cfg.NodeID,
			Status:    "offline",
			VirtualIP: cfg.VirtualIP,
		})
	}
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return DeviceList{}, err
	}
	for _, node := range registry.Nodes {
		if node.DeletedAt != nil {
			continue
		}
		status := node.Status
		if status == "" {
			status = "offline"
		}
		if node.Disabled {
			status = "disabled"
		}
		nodes = append(nodes, DeviceSummary{
			Kind:        "peer",
			NodeID:      node.NodeID,
			DisplayName: node.DisplayName,
			Status:      status,
			VirtualIP:   node.VirtualIP,
			RemoteAddr:  node.SourceAddr,
			Fingerprint: node.CertFingerprint,
			LastSeen:    node.LastSeen,
		})
	}
	return DeviceList{Nodes: nodes}, nil
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
