package cloudhub

import (
	"context"
	"fmt"
	"strings"

	"meshlink/internal/licensing"
	"meshlink/internal/p2p"
)

func (s *Service) JoinDevice(ctx context.Context, req JoinDeviceRequest) (Device, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if req.Token == "" || req.Code == "" {
		return Device{}, fmt.Errorf("token and code are required")
	}
	deviceName := strings.TrimSpace(req.DeviceName)
	if deviceName == "" {
		return Device{}, fmt.Errorf("device_name is required")
	}
	invite, err := s.store.GetInviteByToken(ctx, req.Token)
	if err != nil {
		return Device{}, fmt.Errorf("invite token was not found: %w", err)
	}
	now := s.nowTime()
	if invite.MaxFailures == 0 {
		invite.MaxFailures = defaultInviteMaxFailures
	}
	if invite.Failures >= invite.MaxFailures {
		return Device{}, fmt.Errorf("invite has been invalidated")
	}
	if invite.RevokedAt != nil {
		return Device{}, fmt.Errorf("invite has been revoked: %w", ErrRevoked)
	}
	if invite.OneTime && (invite.UsedAt != nil || invite.Uses > 0) {
		return Device{}, fmt.Errorf("invite has already been used")
	}
	if !invite.OneTime && invite.MaxUses > 0 && invite.Uses >= invite.MaxUses {
		return Device{}, fmt.Errorf("invite device limit has been reached")
	}
	if !invite.ExpiresAt.IsZero() && now.After(invite.ExpiresAt) {
		return Device{}, fmt.Errorf("invite has expired")
	}
	account, err := s.store.GetAccount(ctx, invite.AccountID)
	if err != nil {
		return Device{}, fmt.Errorf("account was not found: %w", err)
	}
	account, err = s.refreshAccountSubscriptionLocked(ctx, account)
	if err != nil {
		return Device{}, err
	}
	if err := ensureAccountActive(account); err != nil {
		return Device{}, err
	}
	if err := s.enforcePrivateLicenseAccountOperationLocked(ctx, account.ID, licensing.OperationNewDevice, "join_device"); err != nil {
		return Device{}, err
	}
	if _, err := s.store.GetNetwork(ctx, invite.NetworkID); err != nil {
		return Device{}, fmt.Errorf("network was not found: %w", err)
	}
	if !verifyInviteCode(invite, req.Code) {
		invite.Failures++
		if _, err := s.store.UpdateInvite(ctx, invite); err != nil {
			return Device{}, err
		}
		_ = s.recordAudit(ctx, AuditInviteVerificationFailed, invite.AccountID, invite.NetworkID, "", map[string]any{
			"invite_id":    invite.ID,
			"failures":     invite.Failures,
			"max_failures": invite.MaxFailures,
			"remote_addr":  req.RemoteAddr,
		})
		if invite.Failures >= invite.MaxFailures {
			_ = s.recordAudit(ctx, AuditInviteInvalidated, invite.AccountID, invite.NetworkID, "", map[string]any{
				"invite_id":    invite.ID,
				"failures":     invite.Failures,
				"max_failures": invite.MaxFailures,
				"remote_addr":  req.RemoteAddr,
			})
			_ = s.recordRiskEvent(ctx, RiskInviteAbuseSuspected, invite.AccountID, invite.NetworkID, "", "invite failed verification limit reached", map[string]any{
				"invite_id":    invite.ID,
				"failures":     invite.Failures,
				"max_failures": invite.MaxFailures,
				"remote_addr":  req.RemoteAddr,
			})
		}
		return Device{}, fmt.Errorf("verification code is incorrect")
	}
	if err := s.enforceDeviceCountQuotaLocked(ctx, account, invite.NetworkID); err != nil {
		return Device{}, err
	}

	invite.Uses++
	invite.UsedAt = &now
	if _, err := s.store.UpdateInvite(ctx, invite); err != nil {
		return Device{}, err
	}
	device := Device{
		ConnectionStatus: p2p.NormalizeConnectionStatus(p2p.ConnectionStatus{}, p2p.ConnectionStatusDefaults{}),
		ID:               mustID("dev"),
		AccountID:        invite.AccountID,
		NetworkID:        invite.NetworkID,
		Name:             deviceName,
		Fingerprint:      strings.TrimSpace(req.Fingerprint),
		Status:           DeviceStatusOffline,
		RemoteAddr:       strings.TrimSpace(req.RemoteAddr),
		JoinedAt:         now,
	}
	created, err := s.store.CreateDevice(ctx, device)
	if err != nil {
		return Device{}, err
	}
	if err := s.recordAudit(ctx, AuditDeviceJoined, created.AccountID, created.NetworkID, created.ID, map[string]any{
		"device_name": created.Name,
		"fingerprint": created.Fingerprint,
		"remote_addr": created.RemoteAddr,
		"invite_id":   invite.ID,
	}); err != nil {
		return Device{}, err
	}
	return created, nil
}

func (s *Service) HeartbeatDevice(ctx context.Context, req HeartbeatDeviceRequest) (Device, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if req.DeviceID == "" {
		return Device{}, fmt.Errorf("device_id is required")
	}
	status := normalizeHeartbeatStatus(req.Status)
	device, err := s.store.GetDevice(ctx, req.DeviceID)
	if err != nil {
		return Device{}, fmt.Errorf("device was not found: %w", err)
	}
	if device.RevokedAt != nil || device.Status == DeviceStatusRevoked {
		return Device{}, fmt.Errorf("device has been revoked: %w", ErrRevoked)
	}
	if device.RolloutID != "" {
		fingerprint := strings.TrimSpace(req.Fingerprint)
		if fingerprint == "" || !constantTimeStringEqual(fingerprint, device.Fingerprint) {
			return Device{}, fmt.Errorf("rollout device identity mismatch: %w", ErrForbidden)
		}
		if rolloutID := strings.TrimSpace(req.RolloutID); rolloutID != "" && rolloutID != device.RolloutID {
			return Device{}, fmt.Errorf("heartbeat rollout assignment mismatch: %w", ErrForbidden)
		}
	}
	account, err := s.store.GetAccount(ctx, device.AccountID)
	if err != nil {
		return Device{}, fmt.Errorf("account was not found: %w", err)
	}
	account, err = s.refreshAccountSubscriptionLocked(ctx, account)
	if err != nil {
		return Device{}, err
	}
	if err := ensureAccountActive(account); err != nil {
		return Device{}, err
	}
	if err := s.enforcePrivateLicenseAccountOperationLocked(ctx, account.ID, licensing.OperationExistingHeartbeat, "device_heartbeat"); err != nil {
		return Device{}, err
	}
	if req.NATProbe != nil {
		if strings.TrimSpace(req.AccountID) != device.AccountID || strings.TrimSpace(req.NetworkID) != device.NetworkID {
			return Device{}, fmt.Errorf("NAT probe device ownership does not match heartbeat request: %w", ErrForbidden)
		}
		if status != DeviceStatusOnline {
			return Device{}, fmt.Errorf("device must be online to publish NAT probe summary: %w", ErrForbidden)
		}
		device.NATProbe = p2p.CloneNATProbeSummary(req.NATProbe)
	}
	if err := s.enforceOnlineDeviceQuotaLocked(ctx, account, device, status); err != nil {
		return Device{}, err
	}
	if req.RolloutID == "" && device.RolloutID == "" && strings.TrimSpace(req.CurrentVersion) != "" {
		if !deploymentVersionPattern.MatchString(strings.TrimSpace(req.CurrentVersion)) {
			return Device{}, fmt.Errorf("current_version is invalid")
		}
		device.CurrentVersion = strings.TrimSpace(req.CurrentVersion)
	}
	now := s.nowTime()
	device.Status = status
	device.ConnectionStatus = p2p.NormalizeConnectionStatus(connectionStatusFromHeartbeat(req), p2p.ConnectionStatusDefaults{
		Online:          status == DeviceStatusOnline,
		DefaultPathType: p2p.PathTypeLANDirect,
	})
	device.LastSeen = &now
	updated, err := s.store.UpdateDevice(ctx, device)
	if err != nil {
		return Device{}, err
	}
	if updated.RelayBytesIn > 0 || updated.RelayBytesOut > 0 {
		usage := RelayUsage{
			ID:         mustID("relay"),
			AccountID:  updated.AccountID,
			NetworkID:  updated.NetworkID,
			DeviceID:   updated.ID,
			BytesIn:    updated.RelayBytesIn,
			BytesOut:   updated.RelayBytesOut,
			RecordedAt: now,
		}
		if _, err := s.store.CreateRelayUsage(ctx, usage); err != nil {
			return Device{}, err
		}
	}
	if strings.TrimSpace(req.RolloutID) != "" {
		if _, err := s.reportRolloutTargetLocked(ctx, ReportRolloutTargetRequest{
			RolloutID: req.RolloutID, DeviceID: updated.ID, Sequence: req.VersionSequence,
			Status: req.VersionStatus, CurrentVersion: req.CurrentVersion,
			TargetVersion: req.TargetVersion, ErrorCode: req.VersionErrorCode, Fingerprint: req.Fingerprint,
		}); err != nil {
			return Device{}, err
		}
		updated, err = s.store.GetDevice(ctx, updated.ID)
		if err != nil {
			return Device{}, err
		}
	}
	metadata := map[string]any{
		"status":          updated.Status,
		"path_type":       string(updated.PathType),
		"path_state":      string(updated.PathState),
		"quality_score":   updated.QualityScore,
		"latency_ms":      updated.LatencyMS,
		"relay_bytes_in":  updated.RelayBytesIn,
		"relay_bytes_out": updated.RelayBytesOut,
		"last_error":      updated.LastError,
		"current_version": updated.CurrentVersion,
		"target_version":  updated.TargetVersion,
	}
	if req.NATProbe != nil && updated.NATProbe != nil {
		metadata["nat_probe_type"] = string(updated.NATProbe.Type)
		metadata["nat_probe_relay_recommended"] = updated.NATProbe.RelayRecommended
	}
	if err := s.recordAudit(ctx, AuditDeviceHeartbeat, updated.AccountID, updated.NetworkID, updated.ID, metadata); err != nil {
		return Device{}, err
	}
	return updated, nil
}

func connectionStatusFromHeartbeat(req HeartbeatDeviceRequest) p2p.ConnectionStatus {
	return p2p.ConnectionStatus{
		PathType:           req.PathType,
		PathState:          req.PathState,
		LatencyMS:          req.LatencyMS,
		PacketLossPermille: req.PacketLossPermille,
		JitterMS:           req.JitterMS,
		RelayBytesIn:       req.RelayBytesIn,
		RelayBytesOut:      req.RelayBytesOut,
		SwitchCount:        req.SwitchCount,
		SwitchReasons:      append([]string(nil), req.SwitchReasons...),
		SwitchFromPath:     req.SwitchFromPath,
		SwitchToPath:       req.SwitchToPath,
		SwitchScoreDelta:   req.SwitchScoreDelta,
		AutoSwitched:       req.AutoSwitched,
		LastError:          req.LastError,
	}
}

func (s *Service) RevokeDevice(ctx context.Context, req RevokeDeviceRequest) (Device, error) {
	s.opMu.Lock()

	if req.DeviceID == "" {
		s.opMu.Unlock()
		return Device{}, fmt.Errorf("device_id is required")
	}
	device, err := s.store.GetDevice(ctx, req.DeviceID)
	if err != nil {
		s.opMu.Unlock()
		return Device{}, fmt.Errorf("device was not found: %w", err)
	}
	now := s.nowTime()
	device.Status = DeviceStatusRevoked
	device.RevokedAt = &now
	device.RevokedReason = strings.TrimSpace(req.Reason)
	enforcementReason := deviceEnforcementReason(device.RevokedReason)
	updated, err := s.store.UpdateDevice(ctx, device)
	if err != nil {
		s.opMu.Unlock()
		return Device{}, err
	}
	revocation := Revocation{
		ID:        mustID("rev"),
		Scope:     RevocationScopeDevice,
		TargetID:  updated.ID,
		Reason:    updated.RevokedReason,
		CreatedAt: now,
	}
	if _, err := s.store.CreateRevocation(ctx, revocation); err != nil {
		s.opMu.Unlock()
		return Device{}, err
	}
	if err := s.recordAudit(ctx, AuditDeviceRevoked, updated.AccountID, updated.NetworkID, updated.ID, map[string]any{
		"reason": updated.RevokedReason,
	}); err != nil {
		s.opMu.Unlock()
		return Device{}, err
	}
	revokedSessions, err := s.deviceRelaySessionsForEnforcementLocked(ctx, updated, enforcementReason)
	if err != nil {
		s.opMu.Unlock()
		return Device{}, err
	}
	s.opMu.Unlock()

	notifyErr := s.notifyRelaySessionsRevoked(ctx, revokedSessions)
	finalizeErr := s.finalizeRelaySessionRevocations(ctx, revokedSessions, enforcementReason)
	if notifyErr != nil {
		return updated, notifyErr
	}
	if finalizeErr != nil {
		return updated, finalizeErr
	}
	return updated, nil
}

func (s *Service) ListNetworkDevices(ctx context.Context, networkID string) ([]Device, error) {
	if networkID == "" {
		return nil, fmt.Errorf("network_id is required")
	}
	if _, err := s.store.GetNetwork(ctx, networkID); err != nil {
		return nil, fmt.Errorf("network was not found: %w", err)
	}
	return s.store.ListNetworkDevices(ctx, networkID)
}

func deviceEnforcementReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "device revoked"
	}
	return "device revoked: " + strings.TrimSpace(reason)
}
