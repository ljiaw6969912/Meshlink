package onboarding

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

var (
	ErrDeviceRegistryUnavailable = errors.New("device registry unavailable")
	ErrDeviceNotFound            = errors.New("device not found in registry")
	ErrDeviceIdentityAmbiguous   = errors.New("ambiguous device registry identity")
)

type DeviceRejection struct {
	NodeID      string `json:"node_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Reason      string `json:"reason"`
}

// LoadRegisteredNodes returns one consistent persisted-registry snapshot.
// Unlike the enrollment loader, a missing or malformed registry is an outage:
// admission callers must not reinterpret it as an empty authoritative set.
func (m Manager) LoadRegisteredNodes() ([]RegisteredNode, error) {
	// The enrollment/admin loader intentionally treats a missing file as a new
	// empty registry. Admission must distinguish that outage from member removal.
	data, err := os.ReadFile(m.deviceRegistryPath())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDeviceRegistryUnavailable, err)
	}
	var registry DeviceRegistry
	if err := jsonUnmarshalStrict(data, &registry); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDeviceRegistryUnavailable, err)
	}
	return registry.Nodes, nil
}

// LookupDevice reads the persisted registry on every call. Control admission
// requires exact identity; the permissive legacy admin matching is not used.
// Revoked entries are returned so callers can distinguish and reconcile them.
func (m Manager) LookupDevice(nodeID string) (RegisteredNode, error) {
	nodes, err := m.LoadRegisteredNodes()
	if err != nil {
		return RegisteredNode{}, err
	}
	var found *RegisteredNode
	for i := range nodes {
		if nodeID != "" && nodes[i].NodeID == nodeID {
			if found != nil {
				return RegisteredNode{}, fmt.Errorf("%w: %q", ErrDeviceIdentityAmbiguous, nodeID)
			}
			found = &nodes[i]
		}
	}
	if found == nil {
		return RegisteredNode{}, fmt.Errorf("%w: %q", ErrDeviceNotFound, nodeID)
	}
	return *found, nil
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
	enrollmentMu.Lock()
	defer enrollmentMu.Unlock()
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
