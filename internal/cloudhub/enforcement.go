package cloudhub

import (
	"context"
	"fmt"
)

func (s *Service) accountRelaySessionsForEnforcementLocked(ctx context.Context, account Account, action, reason string) ([]RelaySession, error) {
	sessions, err := s.store.ListRelaySessions(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	targets := activeOrPendingRelaySessions(sessions)
	if len(targets) == 0 {
		return nil, nil
	}
	if err := s.recordRiskEvent(ctx, RiskAccountEnforcementApplied, account.ID, "", "", reason, map[string]any{
		"action":                action,
		"account_status":        account.Status,
		"relay_sessions_count":  len(targets),
		"relay_enforcement_key": "account",
	}); err != nil {
		return nil, err
	}
	for _, session := range targets {
		if err := s.recordRelaySessionRevokedLocked(ctx, session, "account", reason, map[string]any{
			"action":         action,
			"account_status": account.Status,
		}); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func (s *Service) deviceRelaySessionsForEnforcementLocked(ctx context.Context, device Device, reason string) ([]RelaySession, error) {
	sessions, err := s.store.ListRelaySessions(ctx, device.AccountID)
	if err != nil {
		return nil, err
	}
	targets := make([]RelaySession, 0, len(sessions))
	for _, session := range sessions {
		if session.Status == RelaySessionClosed {
			continue
		}
		if session.SourceDeviceID == device.ID || session.TargetDeviceID == device.ID {
			targets = append(targets, session)
		}
	}
	for _, session := range targets {
		if err := s.recordRelaySessionRevokedLocked(ctx, session, "device", reason, map[string]any{
			"revoked_device_id": device.ID,
		}); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func activeOrPendingRelaySessions(sessions []RelaySession) []RelaySession {
	targets := make([]RelaySession, 0, len(sessions))
	for _, session := range sessions {
		if session.Status != RelaySessionClosed {
			targets = append(targets, session)
		}
	}
	return targets
}

func (s *Service) recordRelaySessionRevokedLocked(ctx context.Context, session RelaySession, scope, reason string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["relay_session_id"] = session.ID
	metadata["scope"] = scope
	metadata["session_status"] = session.Status
	metadata["target_device_id"] = session.TargetDeviceID
	return s.recordRiskEvent(ctx, RiskRelaySessionRevoked, session.AccountID, session.NetworkID, session.SourceDeviceID, reason, metadata)
}

func (s *Service) finalizeRelaySessionRevocations(ctx context.Context, sessions []RelaySession, reason string) error {
	if len(sessions) == 0 {
		return nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	for _, session := range sessions {
		current, err := s.store.GetRelaySession(ctx, session.ID)
		if err != nil {
			return fmt.Errorf("relay session was not found: %w", err)
		}
		if current.Status == RelaySessionClosed {
			if current.Error == "" && reason != "" {
				current.Error = reason
				if _, err := s.store.UpdateRelaySession(ctx, current); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := s.closeRelaySessionLocked(ctx, current, CloseRelaySessionRequest{
			SessionID: current.ID,
			Error:     reason,
		}, AuditRelaySessionRevoked); err != nil {
			return err
		}
	}
	return nil
}
