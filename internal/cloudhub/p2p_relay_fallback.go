package cloudhub

import (
	"context"
	"fmt"
	"strings"

	"meshlink/internal/p2p"
)

type P2PRelaySessionManager struct {
	Service  *Service
	Endpoint string
}

func (m P2PRelaySessionManager) CreateRelaySession(ctx context.Context, req p2p.RelaySessionRequest) (p2p.RelaySessionGrant, error) {
	if m.Service == nil {
		return p2p.RelaySessionGrant{}, fmt.Errorf("cloud hub service is required")
	}
	if err := m.ensureFallbackDevicesOnline(ctx, req); err != nil {
		return p2p.RelaySessionGrant{}, err
	}
	result, err := m.Service.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      req.AccountID,
		NetworkID:      req.NetworkID,
		SourceDeviceID: req.SourceDeviceID,
		TargetDeviceID: req.TargetDeviceID,
		TTL:            req.TTL,
	})
	if err != nil {
		return p2p.RelaySessionGrant{}, err
	}
	grant := P2PRelaySessionGrantFromResult(result)
	if strings.TrimSpace(m.Endpoint) != "" {
		grant.Endpoint = strings.TrimSpace(m.Endpoint)
	}
	return grant, nil
}

func (m P2PRelaySessionManager) CloseRelaySession(ctx context.Context, req p2p.CloseRelaySessionRequest) error {
	if m.Service == nil {
		return fmt.Errorf("cloud hub service is required")
	}
	_, err := m.Service.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID: req.SessionID,
		Error:     req.Error,
	})
	return err
}

func (m P2PRelaySessionManager) ensureFallbackDevicesOnline(ctx context.Context, req p2p.RelaySessionRequest) error {
	devices, err := m.Service.ListNetworkDevices(ctx, strings.TrimSpace(req.NetworkID))
	if err != nil {
		return err
	}
	if err := ensureP2PFallbackDeviceOnline(devices, req.AccountID, req.SourceDeviceID, "source"); err != nil {
		return err
	}
	return ensureP2PFallbackDeviceOnline(devices, req.AccountID, req.TargetDeviceID, "target")
}

func ensureP2PFallbackDeviceOnline(devices []Device, accountID, deviceID, label string) error {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return fmt.Errorf("%s device_id is required", label)
	}
	for _, device := range devices {
		if device.ID != deviceID {
			continue
		}
		if device.AccountID != accountID {
			return fmt.Errorf("%s device does not belong to the requested account: %w", label, ErrForbidden)
		}
		if device.RevokedAt != nil || device.Status == DeviceStatusRevoked {
			return fmt.Errorf("%s device has been revoked: %w", label, ErrRevoked)
		}
		if device.Status != DeviceStatusOnline {
			return fmt.Errorf("%s device must be online for P2P relay fallback: %w", label, ErrForbidden)
		}
		return nil
	}
	return fmt.Errorf("%s device was not found: %w", label, ErrNotFound)
}

func P2PRelaySessionGrantFromResult(result RelaySessionResult) p2p.RelaySessionGrant {
	pathType := p2p.PathType(result.Session.PathType)
	if pathType == "" {
		pathType = p2p.PathTypeRelay
	}
	return p2p.RelaySessionGrant{
		ID:              result.Session.ID,
		AccountID:       result.Session.AccountID,
		NetworkID:       result.Session.NetworkID,
		SourceDeviceID:  result.Session.SourceDeviceID,
		TargetDeviceID:  result.Session.TargetDeviceID,
		PathType:        pathType,
		Endpoint:        result.RelayEndpoint,
		ExpiresAt:       result.Session.ExpiresAt,
		SourceJoinToken: result.SourceJoinToken,
		TargetJoinToken: result.TargetJoinToken,
	}
}

func RelaySessionResultFromP2PGrant(grant p2p.RelaySessionGrant) RelaySessionResult {
	return RelaySessionResult{
		Session: RelaySession{
			ID:             grant.ID,
			AccountID:      grant.AccountID,
			NetworkID:      grant.NetworkID,
			SourceDeviceID: grant.SourceDeviceID,
			TargetDeviceID: grant.TargetDeviceID,
			PathType:       string(grant.PathType),
			Status:         RelaySessionPending,
			ExpiresAt:      grant.ExpiresAt,
		},
		SourceJoinToken: grant.SourceJoinToken,
		TargetJoinToken: grant.TargetJoinToken,
		RelayEndpoint:   grant.Endpoint,
	}
}
