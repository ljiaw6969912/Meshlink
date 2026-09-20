package cloudhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"meshlink/internal/licensing"

	"meshlink/internal/p2p"
)

const (
	defaultRelaySessionTTL = 2 * time.Minute
	maxRelaySessionTTL     = 10 * time.Minute
	relayJoinRoleSource    = "source"
	relayJoinRoleTarget    = "target"
)

func (s *Service) CreateRelaySession(ctx context.Context, req CreateRelaySessionRequest) (RelaySessionResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	accountID := strings.TrimSpace(req.AccountID)
	networkID := strings.TrimSpace(req.NetworkID)
	sourceDeviceID := strings.TrimSpace(req.SourceDeviceID)
	targetDeviceID := strings.TrimSpace(req.TargetDeviceID)
	if accountID == "" {
		return RelaySessionResult{}, fmt.Errorf("account_id is required")
	}
	if networkID == "" {
		return RelaySessionResult{}, fmt.Errorf("network_id is required")
	}
	if sourceDeviceID == "" {
		return RelaySessionResult{}, fmt.Errorf("source_device_id is required")
	}
	if targetDeviceID == "" {
		return RelaySessionResult{}, fmt.Errorf("target_device_id is required")
	}

	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return RelaySessionResult{}, fmt.Errorf("account was not found: %w", err)
	}
	account, err = s.refreshAccountSubscriptionLocked(ctx, account)
	if err != nil {
		return RelaySessionResult{}, err
	}
	if err := ensureAccountActive(account); err != nil {
		s.recordRelaySessionDenied(ctx, accountID, networkID, sourceDeviceID, err, map[string]any{
			"account_status": account.Status,
		})
		return RelaySessionResult{}, err
	}
	if err := s.enforcePrivateLicenseAccountOperationLocked(ctx, account.ID, licensing.OperationExistingRelay, "create_relay_session"); err != nil {
		s.recordRelaySessionDenied(ctx, account.ID, networkID, sourceDeviceID, err, map[string]any{"license_policy": "denied"})
		return RelaySessionResult{}, err
	}
	network, err := s.store.GetNetwork(ctx, networkID)
	if err != nil {
		return RelaySessionResult{}, fmt.Errorf("network was not found: %w", err)
	}
	if network.AccountID != account.ID {
		return RelaySessionResult{}, fmt.Errorf("network does not belong to account: %w", ErrForbidden)
	}
	if network.DeletedAt != nil {
		return RelaySessionResult{}, fmt.Errorf("network has been deleted: %w", ErrForbidden)
	}
	source, err := s.relaySessionDevice(ctx, account.ID, network.ID, sourceDeviceID, "source")
	if err != nil {
		s.recordRelaySessionDenied(ctx, account.ID, network.ID, sourceDeviceID, err, nil)
		return RelaySessionResult{}, err
	}
	authorization, err := s.authorizeConnectionLocked(ctx, source.ID, targetDeviceID)
	if err != nil {
		s.recordRelaySessionDenied(ctx, account.ID, network.ID, source.ID, err, map[string]any{
			"target_device_id": targetDeviceID,
		})
		return RelaySessionResult{}, err
	}
	target, err := s.relayConnectionTargetDevice(ctx, account.ID, network.ID, targetDeviceID, authorization)
	if err != nil {
		s.recordRelaySessionDenied(ctx, account.ID, network.ID, source.ID, err, map[string]any{
			"target_device_id": targetDeviceID,
		})
		return RelaySessionResult{}, err
	}
	policyStatus, err := s.accountPolicyStatusLocked(ctx, account)
	if err != nil {
		s.recordQuotaDeniedLocked(ctx, account.ID, network.ID, sourceDeviceID, err)
		s.recordRelaySessionDenied(ctx, account.ID, network.ID, sourceDeviceID, err, map[string]any{
			"plan_id": account.PlanID,
		})
		return RelaySessionResult{}, err
	}
	relayBudget, err := s.checkRelaySessionCreateQuotaLocked(ctx, account, policyStatus)
	if err != nil {
		_ = s.recordRiskEvent(ctx, RiskQuotaExceeded, account.ID, network.ID, sourceDeviceID, err.Error(), map[string]any{
			"policy_name":                  policyStatus.Policy.Name,
			"relay_bytes_used":             policyStatus.RelayBytesUsed,
			"relay_bytes_quota":            policyStatus.Policy.RelayBytesQuota,
			"active_relay_sessions":        policyStatus.ActiveRelaySessions,
			"max_active_relay_sessions":    policyStatus.Policy.MaxActiveRelaySessions,
			"relay_sessions_created_today": policyStatus.RelaySessionsCreatedToday,
			"max_relay_sessions_per_day":   policyStatus.Policy.MaxRelaySessionsPerDay,
		})
		s.recordRelaySessionDenied(ctx, account.ID, network.ID, sourceDeviceID, err, map[string]any{
			"policy_name": policyStatus.Policy.Name,
			"plan_id":     account.PlanID,
		})
		return RelaySessionResult{}, err
	}
	sessionID := mustID("rsess")
	sourceToken, err := randomTokenBytes(32)
	if err != nil {
		return RelaySessionResult{}, err
	}
	targetToken, err := randomTokenBytes(32)
	if err != nil {
		return RelaySessionResult{}, err
	}
	now := s.nowTime()
	ttl := normalizedRelaySessionTTL(req.TTL)
	session := RelaySession{
		ID:                  sessionID,
		OrganizationID:      authorization.OrganizationID,
		PermissionSource:    authorization.PermissionSource,
		AccountID:           account.ID,
		NetworkID:           network.ID,
		SourceDeviceID:      source.ID,
		TargetDeviceID:      target.ID,
		PathType:            RelayPathType,
		Status:              RelaySessionPending,
		CreatedAt:           now,
		StartedAt:           now,
		ExpiresAt:           now.Add(ttl),
		SourceJoinTokenHash: hashRelayJoinToken(sessionID, relayJoinRoleSource, sourceToken),
		TargetJoinTokenHash: hashRelayJoinToken(sessionID, relayJoinRoleTarget, targetToken),
	}
	created, err := s.store.CreateRelaySession(ctx, session)
	if err != nil {
		return RelaySessionResult{}, err
	}
	if err := s.recordAudit(ctx, AuditRelaySessionCreated, account.ID, network.ID, source.ID, map[string]any{
		"relay_session_id": created.ID,
		"target_device_id": target.ID,
		"path_type":        RelayPathType,
		"expires_at":       created.ExpiresAt,
	}); err != nil {
		return RelaySessionResult{}, err
	}
	return RelaySessionResult{
		Session:         publicRelaySession(created),
		SourceJoinToken: sourceToken,
		TargetJoinToken: targetToken,
		RelayByteBudget: relayBudget,
	}, nil
}

func (s *Service) relayConnectionTargetDevice(ctx context.Context, accountID, networkID, targetDeviceID string, authorization ConnectionAuthorization) (Device, error) {
	if authorization.OrganizationID == "" {
		return s.relaySessionDevice(ctx, accountID, networkID, targetDeviceID, "target")
	}
	target, err := s.store.GetDevice(ctx, strings.TrimSpace(targetDeviceID))
	if err != nil {
		return Device{}, fmt.Errorf("target device was not found: %w", err)
	}
	if target.Status == DeviceStatusRevoked || target.RevokedAt != nil {
		return Device{}, fmt.Errorf("target device has been revoked: %w", ErrRevoked)
	}
	return target, nil
}

func (s *Service) GetRelaySession(ctx context.Context, sessionID string) (RelaySession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return RelaySession{}, fmt.Errorf("session_id is required")
	}
	session, err := s.store.GetRelaySession(ctx, sessionID)
	if err != nil {
		return RelaySession{}, fmt.Errorf("relay session was not found: %w", err)
	}
	return publicRelaySession(session), nil
}

func (s *Service) GetRelaySessionByteBudget(ctx context.Context, sessionID string) (RelayByteBudget, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	session, err := s.relaySessionForMutation(ctx, sessionID)
	if err != nil {
		return RelayByteBudget{}, err
	}
	account, err := s.store.GetAccount(ctx, session.AccountID)
	if err != nil {
		return RelayByteBudget{}, fmt.Errorf("account was not found: %w", err)
	}
	status, err := s.accountPolicyStatusLocked(ctx, account)
	if err != nil {
		return RelayByteBudget{}, err
	}
	return s.relayByteBudgetForAccountLocked(ctx, account, status)
}

func (s *Service) ActivateRelaySession(ctx context.Context, req ActivateRelaySessionRequest) (RelaySession, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	session, err := s.relaySessionForMutation(ctx, req.SessionID)
	if err != nil {
		return RelaySession{}, err
	}
	if session.Status == RelaySessionClosed {
		return publicRelaySession(session), nil
	}
	if err := s.ensureRelaySessionAccountActive(ctx, session); err != nil {
		s.recordRelaySessionDenied(ctx, session.AccountID, session.NetworkID, session.SourceDeviceID, err, map[string]any{
			"relay_session_id": session.ID,
		})
		return RelaySession{}, err
	}
	if !session.ExpiresAt.IsZero() && s.nowTime().After(session.ExpiresAt) {
		return RelaySession{}, fmt.Errorf("relay session has expired: %w", ErrExpired)
	}
	if session.Status != RelaySessionActive {
		session.Status = RelaySessionActive
		updated, err := s.store.UpdateRelaySession(ctx, session)
		if err != nil {
			return RelaySession{}, err
		}
		session = updated
		if err := s.recordAudit(ctx, AuditRelaySessionActivated, session.AccountID, session.NetworkID, session.SourceDeviceID, map[string]any{
			"relay_session_id": session.ID,
			"target_device_id": session.TargetDeviceID,
			"path_type":        RelayPathType,
		}); err != nil {
			return RelaySession{}, err
		}
	}
	return publicRelaySession(session), nil
}

func (s *Service) RecordRelaySessionUsage(ctx context.Context, req RecordRelaySessionUsageRequest) (RelaySession, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if req.RelayBytesIn < 0 || req.RelayBytesOut < 0 {
		return RelaySession{}, fmt.Errorf("relay byte counters cannot be negative")
	}
	session, err := s.relaySessionForMutation(ctx, req.SessionID)
	if err != nil {
		return RelaySession{}, err
	}
	if session.Status == RelaySessionClosed {
		return RelaySession{}, fmt.Errorf("relay session is closed: %w", ErrForbidden)
	}
	if err := s.ensureRelaySessionAccountActive(ctx, session); err != nil {
		return RelaySession{}, err
	}
	if !session.ExpiresAt.IsZero() && s.nowTime().After(session.ExpiresAt) {
		return RelaySession{}, fmt.Errorf("relay session has expired: %w", ErrExpired)
	}
	if err := s.ensureRelaySessionUsageWithinBudgetLocked(ctx, session, req.RelayBytesIn+req.RelayBytesOut); err != nil {
		s.recordQuotaDeniedLocked(ctx, session.AccountID, session.NetworkID, session.SourceDeviceID, err)
		return RelaySession{}, err
	}
	session.Status = RelaySessionActive
	session.RelayBytesIn += req.RelayBytesIn
	session.RelayBytesOut += req.RelayBytesOut
	updated, err := s.store.UpdateRelaySession(ctx, session)
	if err != nil {
		return RelaySession{}, err
	}
	if err := s.recordRelayEndpointUsageLocked(ctx, updated, req.RelayBytesIn, req.RelayBytesOut); err != nil {
		return RelaySession{}, err
	}
	if err := s.recordAudit(ctx, AuditRelaySessionUsageRecorded, updated.AccountID, updated.NetworkID, updated.SourceDeviceID, map[string]any{
		"relay_session_id": updated.ID,
		"target_device_id": updated.TargetDeviceID,
		"path_type":        RelayPathType,
		"relay_bytes_in":   req.RelayBytesIn,
		"relay_bytes_out":  req.RelayBytesOut,
	}); err != nil {
		return RelaySession{}, err
	}
	return publicRelaySession(updated), nil
}

func (s *Service) CloseRelaySession(ctx context.Context, req CloseRelaySessionRequest) (RelaySession, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	session, err := s.relaySessionForMutation(ctx, req.SessionID)
	if err != nil {
		return RelaySession{}, err
	}
	return s.closeRelaySessionLocked(ctx, session, req, AuditRelaySessionClosed)
}

func (s *Service) closeRelaySessionLocked(ctx context.Context, session RelaySession, req CloseRelaySessionRequest, auditEvent string) (RelaySession, error) {
	if req.RelayBytesIn < 0 || req.RelayBytesOut < 0 {
		return RelaySession{}, fmt.Errorf("relay byte counters cannot be negative")
	}
	if session.Status == RelaySessionClosed {
		return publicRelaySession(session), nil
	}
	relayBytesIn, relayBytesOut, quotaReached := s.clampedRelaySessionUsageLocked(ctx, session, req.RelayBytesIn, req.RelayBytesOut)
	now := s.nowTime()
	session.Status = RelaySessionClosed
	session.EndedAt = &now
	session.RelayBytesIn += relayBytesIn
	session.RelayBytesOut += relayBytesOut
	session.Error = strings.TrimSpace(req.Error)
	if quotaReached && session.Error == "" {
		session.Error = "relay byte quota reached"
	}
	updated, err := s.store.UpdateRelaySession(ctx, session)
	if err != nil {
		return RelaySession{}, err
	}
	log := ConnectionLog{
		ID:               mustID("conn"),
		OrganizationID:   updated.OrganizationID,
		PermissionSource: updated.PermissionSource,
		SessionID:        updated.ID,
		AccountID:        updated.AccountID,
		NetworkID:        updated.NetworkID,
		SourceDeviceID:   updated.SourceDeviceID,
		TargetDeviceID:   updated.TargetDeviceID,
		PathType:         RelayPathType,
		PathState:        string(p2p.PathStateClosed),
		StartedAt:        updated.StartedAt,
		EndedAt:          updated.EndedAt,
		RelayBytesIn:     updated.RelayBytesIn,
		RelayBytesOut:    updated.RelayBytesOut,
		Error:            updated.Error,
	}
	log = withConnectionLogQuality(log)
	if _, err := s.store.CreateConnectionLog(ctx, log); err != nil {
		return RelaySession{}, err
	}
	if err := s.recordRelayEndpointUsageLocked(ctx, updated, relayBytesIn, relayBytesOut); err != nil {
		return RelaySession{}, err
	}
	if err := s.recordAudit(ctx, auditEvent, updated.AccountID, updated.NetworkID, updated.SourceDeviceID, map[string]any{
		"relay_session_id": updated.ID,
		"target_device_id": updated.TargetDeviceID,
		"path_type":        RelayPathType,
		"relay_bytes_in":   relayBytesIn,
		"relay_bytes_out":  relayBytesOut,
		"error":            updated.Error,
	}); err != nil {
		return RelaySession{}, err
	}
	return publicRelaySession(updated), nil
}

func (s *Service) checkRelaySessionCreateQuotaLocked(ctx context.Context, account Account, status AccountPolicyStatus) (RelayByteBudget, error) {
	budget, err := s.relayByteBudgetForAccountLocked(ctx, account, status)
	if err != nil {
		return RelayByteBudget{}, err
	}
	if budget.Limited && budget.RemainingBytes <= 0 {
		mode := PlanQuotaLimited
		if account.PlanID != "" {
			plan, planErr := s.planForAccount(account)
			if planErr == nil {
				mode = plan.Entitlements.OfficialRelayTraffic.Mode
			}
		}
		return RelayByteBudget{}, newQuotaError(QuotaDimensionOfficialRelayTraffic, status.RelayBytesUsed, budget.LimitBytes, mode, account.PlanID, "relay byte quota exceeded")
	}
	if account.PlanID == "" {
		if err := checkRelaySessionQuota(status); err != nil {
			return RelayByteBudget{}, err
		}
		return budget, nil
	}
	if err := checkRelaySessionGuardrailQuota(status); err != nil {
		return RelayByteBudget{}, withQuotaPlan(err, account.PlanID)
	}
	return budget, nil
}

func (s *Service) relayByteBudgetForAccountLocked(ctx context.Context, account Account, status AccountPolicyStatus) (RelayByteBudget, error) {
	if account.PlanID == "" {
		if status.Policy.RelayBytesQuota <= 0 {
			return RelayByteBudget{UsedBytes: status.RelayBytesUsed}, nil
		}
		remaining := status.Policy.RelayBytesQuota - status.RelayBytesUsed
		if remaining < 0 {
			remaining = 0
		}
		return RelayByteBudget{
			Limited:        true,
			UsedBytes:      status.RelayBytesUsed,
			LimitBytes:     status.Policy.RelayBytesQuota,
			RemainingBytes: remaining,
		}, nil
	}
	plan, err := s.planForAccount(account)
	if err != nil {
		return RelayByteBudget{}, err
	}
	decision, err := s.evaluateQuotaLocked(ctx, account.ID, QuotaDimensionOfficialRelayTraffic, plan.Entitlements.OfficialRelayTraffic)
	if err != nil {
		return RelayByteBudget{}, withQuotaPlan(err, plan.ID)
	}
	if !decision.Limited {
		return RelayByteBudget{UsedBytes: status.RelayBytesUsed}, nil
	}
	remaining := decision.Limit - status.RelayBytesUsed
	if remaining < 0 {
		remaining = 0
	}
	return RelayByteBudget{
		Limited:        true,
		UsedBytes:      status.RelayBytesUsed,
		LimitBytes:     decision.Limit,
		RemainingBytes: remaining,
	}, nil
}

func checkRelaySessionGuardrailQuota(status AccountPolicyStatus) error {
	policy := status.Policy
	if policy.MaxActiveRelaySessions > 0 && status.ActiveRelaySessions >= policy.MaxActiveRelaySessions {
		return newQuotaError(QuotaDimensionActiveRelaySessions, int64(status.ActiveRelaySessions), int64(policy.MaxActiveRelaySessions), PlanQuotaLimited, "", "relay active session quota exceeded")
	}
	if policy.MaxRelaySessionsPerDay > 0 && status.RelaySessionsCreatedToday >= policy.MaxRelaySessionsPerDay {
		return newQuotaError(QuotaDimensionRelaySessionsPerDay, int64(status.RelaySessionsCreatedToday), int64(policy.MaxRelaySessionsPerDay), PlanQuotaLimited, "", "relay daily session quota exceeded")
	}
	return nil
}

func (s *Service) ensureRelaySessionUsageWithinBudgetLocked(ctx context.Context, session RelaySession, delta int64) error {
	if delta <= 0 {
		return nil
	}
	account, err := s.store.GetAccount(ctx, session.AccountID)
	if err != nil {
		return fmt.Errorf("account was not found: %w", err)
	}
	status, err := s.accountPolicyStatusLocked(ctx, account)
	if err != nil {
		return err
	}
	budget, err := s.relayByteBudgetForAccountLocked(ctx, account, status)
	if err != nil {
		return err
	}
	if budget.Limited && delta > budget.RemainingBytes {
		mode := PlanQuotaLimited
		if account.PlanID != "" {
			plan, planErr := s.planForAccount(account)
			if planErr == nil {
				mode = plan.Entitlements.OfficialRelayTraffic.Mode
			}
		}
		return newQuotaError(QuotaDimensionOfficialRelayTraffic, status.RelayBytesUsed, budget.LimitBytes, mode, account.PlanID, "relay byte quota exceeded")
	}
	return nil
}

func (s *Service) clampedRelaySessionUsageLocked(ctx context.Context, session RelaySession, bytesIn, bytesOut int64) (int64, int64, bool) {
	delta := bytesIn + bytesOut
	if delta <= 0 {
		return bytesIn, bytesOut, false
	}
	account, err := s.store.GetAccount(ctx, session.AccountID)
	if err != nil {
		return bytesIn, bytesOut, false
	}
	status, err := s.accountPolicyStatusLocked(ctx, account)
	if err != nil {
		return bytesIn, bytesOut, false
	}
	budget, err := s.relayByteBudgetForAccountLocked(ctx, account, status)
	if err != nil || !budget.Limited || delta <= budget.RemainingBytes {
		return bytesIn, bytesOut, false
	}
	remaining := budget.RemainingBytes
	if remaining <= 0 {
		return 0, 0, true
	}
	allowedIn := bytesIn
	if allowedIn > remaining {
		allowedIn = remaining
	}
	remaining -= allowedIn
	allowedOut := bytesOut
	if allowedOut > remaining {
		allowedOut = remaining
	}
	return allowedIn, allowedOut, true
}

func (s *Service) relaySessionDevice(ctx context.Context, accountID, networkID, deviceID, label string) (Device, error) {
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
	return device, nil
}

func (s *Service) relaySessionForMutation(ctx context.Context, sessionID string) (RelaySession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return RelaySession{}, fmt.Errorf("session_id is required")
	}
	session, err := s.store.GetRelaySession(ctx, sessionID)
	if err != nil {
		return RelaySession{}, fmt.Errorf("relay session was not found: %w", err)
	}
	return session, nil
}

func (s *Service) ensureRelaySessionAccountActive(ctx context.Context, session RelaySession) error {
	account, err := s.store.GetAccount(ctx, session.AccountID)
	if err != nil {
		return fmt.Errorf("account was not found: %w", err)
	}
	return ensureAccountActive(account)
}

func (s *Service) recordRelayEndpointUsageLocked(ctx context.Context, session RelaySession, bytesIn, bytesOut int64) error {
	if bytesIn == 0 && bytesOut == 0 {
		return nil
	}
	now := s.nowTime()
	sourceUsage := RelayUsage{
		ID:         mustID("relay"),
		AccountID:  session.AccountID,
		NetworkID:  session.NetworkID,
		DeviceID:   session.SourceDeviceID,
		SessionID:  session.ID,
		BytesIn:    bytesIn,
		BytesOut:   bytesOut,
		RecordedAt: now,
	}
	if _, err := s.store.CreateRelayUsage(ctx, sourceUsage); err != nil {
		return err
	}
	targetUsage := RelayUsage{
		ID:         mustID("relay"),
		AccountID:  session.AccountID,
		NetworkID:  session.NetworkID,
		DeviceID:   session.TargetDeviceID,
		SessionID:  session.ID,
		BytesIn:    bytesOut,
		BytesOut:   bytesIn,
		RecordedAt: now,
	}
	_, err := s.store.CreateRelayUsage(ctx, targetUsage)
	return err
}

func normalizedRelaySessionTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return defaultRelaySessionTTL
	}
	if ttl > maxRelaySessionTTL {
		return maxRelaySessionTTL
	}
	return ttl
}

func hashRelayJoinToken(sessionID, role, token string) string {
	sum := sha256.Sum256([]byte(sessionID + ":" + role + ":" + token))
	return hex.EncodeToString(sum[:])
}

func publicRelaySession(session RelaySession) RelaySession {
	session.SourceJoinTokenHash = ""
	session.TargetJoinTokenHash = ""
	return session
}
