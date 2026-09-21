package onboarding

import (
	"fmt"
	"meshlink/internal/deviceidentity"
)

// SyncDeviceMetadata is called only after mutual TLS admission. Recheck the
// certificate tuple under the enrollment lock before binding legacy records.
// MAC identifies a device; it never authenticates a control connection.
func (m Manager) SyncDeviceMetadata(nodeID, fingerprint, displayName, mac string) (RegisteredNode, error) {
	if displayName != "" {
		if err := validateEnrollmentNodeName(displayName); err != nil {
			return RegisteredNode{}, err
		}
	}
	if mac != "" {
		normalized, err := deviceidentity.NormalizeMAC(mac)
		if err != nil {
			return RegisteredNode{}, err
		}
		mac = normalized
	}
	enrollmentMu.Lock()
	defer enrollmentMu.Unlock()
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return RegisteredNode{}, err
	}
	index := -1
	for i, node := range registry.Nodes {
		if node.NodeID == nodeID {
			if index >= 0 {
				return RegisteredNode{}, ErrDeviceIdentityAmbiguous
			}
			index = i
		}
	}
	if index < 0 {
		return RegisteredNode{}, ErrDeviceNotFound
	}
	node := registry.Nodes[index]
	if node.CertFingerprint != fingerprint || node.Disabled || node.DeletedAt != nil {
		return RegisteredNode{}, fmt.Errorf("device identity is no longer authorized")
	}
	// Do not replace a persisted MAC merely because NIC ordering has changed.
	// A legacy record acquires its first binding using its authenticated identity.
	if node.MACAddress == "" && mac != "" {
		for i, other := range registry.Nodes {
			if i != index && other.MACAddress == mac {
				return RegisteredNode{}, fmt.Errorf("MAC already belongs to another registered device")
			}
		}
		node.MACAddress = mac
	}
	if displayName != "" && displayName != node.ReportedName {
		// An unchanged client setting must not undo an administrator's rename.
		// On first binding retain legacy custom names set by the administrator.
		if node.ReportedName != "" || node.DisplayName == "" || node.DisplayName == node.NodeID {
			node.DisplayName = displayName
		}
		node.ReportedName = displayName
	}
	if node.MACAddress != registry.Nodes[index].MACAddress || node.DisplayName != registry.Nodes[index].DisplayName || node.ReportedName != registry.Nodes[index].ReportedName {
		registry.Nodes[index] = node
		if err := m.saveDeviceRegistry(registry); err != nil {
			return RegisteredNode{}, err
		}
	}
	return node, nil
}
