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
		return deviceListFromRuntime(status), nil
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

func deviceListFromRuntime(status runtimeStatus) DeviceList {
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
		nodes = append(nodes, DeviceSummary{
			Kind:        "peer",
			NodeID:      peer.NodeID,
			Status:      peerStatusText(peer),
			VirtualIP:   peer.VirtualIP,
			RemoteAddr:  peer.RemoteAddr,
			Fingerprint: peer.Fingerprint,
			CommonName:  peer.CommonName,
			LastSeen:    peer.LastSeen,
		})
	}
	return DeviceList{UpdatedAt: status.UpdatedAt, Nodes: nodes}
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

func statusPath(configPath, serviceName string) string {
	if serviceName == "" {
		serviceName = "mesh-agent"
	}
	return filepath.Join(filepath.Dir(configPath), "logs", serviceName+".status.json")
}
