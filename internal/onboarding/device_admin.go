package onboarding

import (
	"fmt"
	"strings"
	"time"
)

type DeviceRejection struct {
	NodeID      string `json:"node_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Reason      string `json:"reason"`
}

func (m Manager) RenameDevice(nodeID, displayName string) (RegisteredNode, error) {
	nodeID = strings.TrimSpace(nodeID)
	displayName = strings.TrimSpace(displayName)
	if nodeID == "" {
		return RegisteredNode{}, fmt.Errorf("node_id is required")
	}
	if displayName == "" {
		return RegisteredNode{}, fmt.Errorf("display_name is required")
	}
	return m.updateDevice(nodeID, "device_renamed", func(node *RegisteredNode, now time.Time) {
		node.DisplayName = displayName
	})
}

func (m Manager) DisableDevice(nodeID string) (RegisteredNode, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return RegisteredNode{}, fmt.Errorf("node_id is required")
	}
	return m.updateDevice(nodeID, "device_disabled", func(node *RegisteredNode, now time.Time) {
		node.Disabled = true
		node.Status = "disabled"
	})
}

func (m Manager) RemoveDevice(nodeID string) (RegisteredNode, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return RegisteredNode{}, fmt.Errorf("node_id is required")
	}
	return m.updateDevice(nodeID, "device_removed", func(node *RegisteredNode, now time.Time) {
		node.Disabled = true
		node.Status = "removed"
		node.DeletedAt = &now
	})
}

func (m Manager) updateDevice(nodeID, auditEvent string, update func(*RegisteredNode, time.Time)) (RegisteredNode, error) {
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return RegisteredNode{}, err
	}
	now := m.now()
	for i := range registry.Nodes {
		if !sameNodeID(registry.Nodes[i].NodeID, nodeID) {
			continue
		}
		update(&registry.Nodes[i], now)
		if err := m.saveDeviceRegistry(registry); err != nil {
			return RegisteredNode{}, err
		}
		node := registry.Nodes[i]
		if err := m.writeAudit(auditEvent, map[string]any{
			"node_id":      node.NodeID,
			"display_name": node.DisplayName,
			"fingerprint":  node.CertFingerprint,
			"virtual_ip":   node.VirtualIP,
		}); err != nil {
			return RegisteredNode{}, err
		}
		return node, nil
	}
	return RegisteredNode{}, fmt.Errorf("device %q was not found", nodeID)
}

func (m Manager) DeviceRejection(nodeID, fingerprint string) (DeviceRejection, bool, error) {
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return DeviceRejection{}, false, err
	}
	nodeID = strings.TrimSpace(nodeID)
	fingerprint = strings.TrimSpace(fingerprint)
	for _, node := range registry.Nodes {
		if !deviceMatches(node, nodeID, fingerprint) {
			continue
		}
		if node.DeletedAt != nil {
			return DeviceRejection{NodeID: node.NodeID, Fingerprint: node.CertFingerprint, Reason: "device removed"}, true, nil
		}
		if node.Disabled {
			return DeviceRejection{NodeID: node.NodeID, Fingerprint: node.CertFingerprint, Reason: "device disabled"}, true, nil
		}
	}
	return DeviceRejection{}, false, nil
}

func deviceMatches(node RegisteredNode, nodeID, fingerprint string) bool {
	if nodeID != "" && sameNodeID(node.NodeID, nodeID) {
		return true
	}
	if fingerprint != "" && node.CertFingerprint != "" && strings.EqualFold(node.CertFingerprint, fingerprint) {
		return true
	}
	return false
}

func sameNodeID(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
