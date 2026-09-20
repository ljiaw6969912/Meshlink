package cloudhub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meshlink/internal/licensing"
	"meshlink/internal/p2p"
)

type RegisterP2PCandidatesRequest struct {
	AccountID  string          `json:"account_id"`
	NetworkID  string          `json:"network_id"`
	DeviceID   string          `json:"device_id"`
	Candidates []p2p.Candidate `json:"candidates,omitempty"`
}

type QueryP2PCandidatesRequest struct {
	AccountID          string `json:"account_id"`
	NetworkID          string `json:"network_id"`
	RequestingDeviceID string `json:"requesting_device_id"`
	TargetDeviceID     string `json:"target_device_id"`
}

type NegotiateP2PConnectionRequest struct {
	AccountID       string              `json:"account_id"`
	NetworkID       string              `json:"network_id"`
	SourceDeviceID  string              `json:"source_device_id"`
	TargetDeviceID  string              `json:"target_device_id"`
	TransportPolicy p2p.TransportPolicy `json:"transport_policy,omitempty"`
}

func (s *Service) RegisterP2PCandidates(ctx context.Context, req RegisterP2PCandidatesRequest) ([]p2p.Candidate, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	account, network, err := s.p2pAccountNetwork(ctx, req.AccountID, req.NetworkID)
	if err != nil {
		return nil, err
	}
	device, err := s.p2pControlDevice(ctx, account.ID, network.ID, req.DeviceID, "candidate", true)
	if err != nil {
		return nil, err
	}
	prepared := make([]p2p.Candidate, len(req.Candidates))
	for i, candidate := range req.Candidates {
		if strings.TrimSpace(candidate.DeviceID) != "" && strings.TrimSpace(candidate.DeviceID) != device.ID {
			return nil, fmt.Errorf("candidate device_id does not match registering device: %w", ErrForbidden)
		}
		if strings.TrimSpace(candidate.NetworkID) != "" && strings.TrimSpace(candidate.NetworkID) != network.ID {
			return nil, fmt.Errorf("candidate network_id does not match requested network: %w", ErrForbidden)
		}
		candidate.DeviceID = device.ID
		candidate.NetworkID = network.ID
		prepared[i] = candidate
	}
	normalized, err := p2p.NormalizeCandidates(prepared, s.nowTime())
	if err != nil {
		return nil, err
	}
	stored, err := s.store.ReplaceP2PCandidates(ctx, device.ID, normalized)
	if err != nil {
		return nil, err
	}
	if err := s.recordAudit(ctx, AuditP2PCandidatesRegistered, account.ID, network.ID, device.ID, p2pCandidateAuditMetadata(stored)); err != nil {
		return nil, err
	}
	return freshP2PCandidates(stored, s.nowTime()), nil
}

func (s *Service) QueryP2PCandidates(ctx context.Context, req QueryP2PCandidatesRequest) ([]p2p.Candidate, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	account, network, err := s.p2pAccountNetwork(ctx, req.AccountID, req.NetworkID)
	if err != nil {
		return nil, err
	}
	requesting, err := s.p2pControlDevice(ctx, account.ID, network.ID, req.RequestingDeviceID, "requesting", true)
	if err != nil {
		return nil, err
	}
	authorization, err := s.authorizeConnectionLocked(ctx, requesting.ID, req.TargetDeviceID)
	if err != nil {
		return nil, err
	}
	target, err := s.connectionTargetDevice(ctx, account.ID, network.ID, req.TargetDeviceID, authorization, true)
	if err != nil {
		return nil, err
	}
	candidates, err := s.store.ListP2PCandidates(ctx, target.NetworkID, target.ID)
	if err != nil {
		return nil, err
	}
	return freshP2PCandidates(candidates, s.nowTime()), nil
}

func (s *Service) NegotiateP2PConnection(ctx context.Context, req NegotiateP2PConnectionRequest) (p2p.Negotiation, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	account, network, err := s.p2pAccountNetwork(ctx, req.AccountID, req.NetworkID)
	if err != nil {
		return p2p.Negotiation{}, err
	}
	if err := s.enforcePrivateLicenseAccountOperationLocked(ctx, account.ID, licensing.OperationExistingP2P, "negotiate_p2p_connection"); err != nil {
		return p2p.Negotiation{}, err
	}
	source, err := s.p2pControlDevice(ctx, account.ID, network.ID, req.SourceDeviceID, "source", true)
	if err != nil {
		return p2p.Negotiation{}, err
	}
	authorization, err := s.authorizeConnectionLocked(ctx, source.ID, req.TargetDeviceID)
	if err != nil {
		return p2p.Negotiation{}, err
	}
	target, err := s.connectionTargetDevice(ctx, account.ID, network.ID, req.TargetDeviceID, authorization, true)
	if err != nil {
		return p2p.Negotiation{}, err
	}
	sourceCandidates, err := s.store.ListP2PCandidates(ctx, network.ID, source.ID)
	if err != nil {
		return p2p.Negotiation{}, err
	}
	targetCandidates, err := s.store.ListP2PCandidates(ctx, target.NetworkID, target.ID)
	if err != nil {
		return p2p.Negotiation{}, err
	}
	if authorization.OrganizationID != "" {
		scopeID := "organization:" + authorization.OrganizationID
		sourceCandidates = connectionScopedCandidates(sourceCandidates, scopeID)
		targetCandidates = connectionScopedCandidates(targetCandidates, scopeID)
	}
	now := s.nowTime()
	negotiation := p2p.BuildNegotiation(p2p.NegotiationRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		SourceCandidates:   freshP2PCandidates(sourceCandidates, now),
		TargetCandidates:   freshP2PCandidates(targetCandidates, now),
		SourceNATProbe:     source.NATProbe,
		TargetNATProbe:     target.NATProbe,
		TransportPolicy:    req.TransportPolicy,
		AllowRelayFallback: true,
	})
	negotiation.ID = mustID("p2pneg")
	if err := s.recordP2PNegotiationMetadata(ctx, negotiation, now, authorization); err != nil {
		return p2p.Negotiation{}, err
	}
	return negotiation, nil
}

func (s *Service) connectionTargetDevice(ctx context.Context, accountID, networkID, targetDeviceID string, authorization ConnectionAuthorization, requireOnline bool) (Device, error) {
	if authorization.OrganizationID == "" {
		return s.p2pControlDevice(ctx, accountID, networkID, targetDeviceID, "target", requireOnline)
	}
	target, err := s.store.GetDevice(ctx, strings.TrimSpace(targetDeviceID))
	if err != nil {
		return Device{}, fmt.Errorf("target device was not found: %w", err)
	}
	if target.Status == DeviceStatusRevoked || target.RevokedAt != nil {
		return Device{}, fmt.Errorf("target device has been revoked: %w", ErrRevoked)
	}
	if requireOnline && target.Status != DeviceStatusOnline {
		return Device{}, fmt.Errorf("target device must be online for P2P negotiation: %w", ErrForbidden)
	}
	return target, nil
}

func connectionScopedCandidates(candidates []p2p.Candidate, scopeID string) []p2p.Candidate {
	out := make([]p2p.Candidate, len(candidates))
	copy(out, candidates)
	for i := range out {
		out[i].NetworkID = scopeID
	}
	return out
}

func (s *Service) p2pAccountNetwork(ctx context.Context, accountID, networkID string) (Account, Network, error) {
	accountID = strings.TrimSpace(accountID)
	networkID = strings.TrimSpace(networkID)
	if accountID == "" {
		return Account{}, Network{}, fmt.Errorf("account_id is required")
	}
	if networkID == "" {
		return Account{}, Network{}, fmt.Errorf("network_id is required")
	}
	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return Account{}, Network{}, fmt.Errorf("account was not found: %w", err)
	}
	if err := ensureAccountActive(account); err != nil {
		return Account{}, Network{}, err
	}
	network, err := s.store.GetNetwork(ctx, networkID)
	if err != nil {
		return Account{}, Network{}, fmt.Errorf("network was not found: %w", err)
	}
	if network.AccountID != account.ID {
		return Account{}, Network{}, fmt.Errorf("network does not belong to account: %w", ErrForbidden)
	}
	if network.DeletedAt != nil {
		return Account{}, Network{}, fmt.Errorf("network has been deleted: %w", ErrForbidden)
	}
	return account, network, nil
}

func (s *Service) p2pControlDevice(ctx context.Context, accountID, networkID, deviceID, label string, requireOnline bool) (Device, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return Device{}, fmt.Errorf("%s device_id is required", label)
	}
	device, err := s.store.GetDevice(ctx, deviceID)
	if err != nil {
		return Device{}, fmt.Errorf("%s device was not found: %w", label, err)
	}
	if device.AccountID != accountID || device.NetworkID != networkID {
		return Device{}, fmt.Errorf("%s device does not belong to the requested network: %w", label, ErrForbidden)
	}
	if device.RevokedAt != nil || device.Status == DeviceStatusRevoked {
		return Device{}, fmt.Errorf("%s device has been revoked: %w", label, ErrRevoked)
	}
	if requireOnline && device.Status != DeviceStatusOnline {
		return Device{}, fmt.Errorf("%s device must be online for P2P negotiation: %w", label, ErrForbidden)
	}
	return device, nil
}

func (s *Service) recordP2PNegotiationMetadata(ctx context.Context, negotiation p2p.Negotiation, startedAt time.Time, authorization ConnectionAuthorization) error {
	pathType := negotiation.PreferredPathType
	if pathType == "" && negotiation.State == p2p.PathStateFallbackRelay {
		pathType = p2p.PathTypeRelay
	}
	connectionError := ""
	if negotiation.State == p2p.PathStateFallbackRelay || negotiation.State == p2p.PathStateFailed {
		connectionError = negotiation.FallbackReason
	}
	log := ConnectionLog{
		ID:               mustID("conn"),
		OrganizationID:   authorization.OrganizationID,
		PermissionSource: authorization.PermissionSource,
		AccountID:        negotiation.AccountID,
		NetworkID:        negotiation.NetworkID,
		SourceDeviceID:   negotiation.SourceDeviceID,
		TargetDeviceID:   negotiation.TargetDeviceID,
		PathType:         string(pathType),
		PathState:        string(negotiation.State),
		StartedAt:        startedAt,
		Error:            connectionError,
	}
	log = withConnectionLogQuality(log)
	if _, err := s.store.CreateConnectionLog(ctx, log); err != nil {
		return err
	}
	metadata := map[string]any{
		"p2p_negotiation_id":   negotiation.ID,
		"target_device_id":     negotiation.TargetDeviceID,
		"candidate_pair_count": len(negotiation.CandidatePairs),
		"preferred_path_type":  string(pathType),
		"state":                string(negotiation.State),
		"relay_fallback":       negotiation.RelayFallback.CreateRelaySession,
		"fallback_reason":      negotiation.FallbackReason,
	}
	holePunch := p2p.SummarizeHolePunch(negotiation, p2p.HolePunchPolicy{})
	metadata["hole_punch_ready"] = holePunch.Ready
	metadata["hole_punch_candidate_port_count"] = holePunch.CandidatePortCount
	metadata["hole_punch_mode"] = holePunch.Mode
	metadata["hole_punch_max_attempts"] = holePunch.MaxAttempts
	metadata["hole_punch_reason"] = holePunch.Reason
	if negotiation.TransportSelection != nil {
		metadata["preferred_transport"] = string(negotiation.TransportSelection.Preferred.Kind)
		metadata["experimental_transport"] = negotiation.TransportSelection.Preferred.Experimental
	}
	return s.recordAudit(ctx, AuditP2PConnectionNegotiated, negotiation.AccountID, negotiation.NetworkID, negotiation.SourceDeviceID, metadata)
}

func freshP2PCandidates(candidates []p2p.Candidate, now time.Time) []p2p.Candidate {
	out := make([]p2p.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.IsExpired(now) {
			out = append(out, candidate)
		}
	}
	return out
}

func p2pCandidateAuditMetadata(candidates []p2p.Candidate) map[string]any {
	metadata := map[string]any{
		"candidate_count": len(candidates),
	}
	var lanCount, publicCount, relayCount int
	for _, candidate := range candidates {
		switch candidate.Scope {
		case p2p.CandidateScopeLAN:
			lanCount++
		case p2p.CandidateScopePublic:
			publicCount++
		case p2p.CandidateScopeRelay:
			relayCount++
		}
	}
	metadata["lan_count"] = lanCount
	metadata["public_count"] = publicCount
	metadata["relay_count"] = relayCount
	return metadata
}
