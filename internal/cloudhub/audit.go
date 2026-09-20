package cloudhub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"meshlink/internal/p2p"
)

const (
	AuditAccountCreated                   = "account_created"
	AuditAccountFrozen                    = "account_frozen"
	AuditAccountUnfrozen                  = "account_unfrozen"
	AuditAccountBanned                    = "account_banned"
	AuditNetworkCreated                   = "network_created"
	AuditInviteCreated                    = "invite_created"
	AuditInviteVerificationFailed         = "invite_verification_failed"
	AuditInviteInvalidated                = "invite_invalidated"
	AuditDeviceJoined                     = "device_joined"
	AuditDeviceHeartbeat                  = "device_heartbeat"
	AuditDeviceRevoked                    = "device_revoked"
	AuditRelaySessionCreated              = "relay_session_created"
	AuditRelaySessionActivated            = "relay_session_activated"
	AuditRelaySessionUsageRecorded        = "relay_session_usage_recorded"
	AuditRelaySessionClosed               = "relay_session_closed"
	AuditRelaySessionRevoked              = "relay_session_revoked"
	AuditP2PCandidatesRegistered          = "p2p_candidates_registered"
	AuditP2PConnectionNegotiated          = "p2p_connection_negotiated"
	AuditSubscriptionWebhookAccepted      = "subscription_webhook_accepted"
	AuditSubscriptionWebhookRepeated      = "subscription_webhook_repeated"
	AuditSubscriptionWebhookRejected      = "subscription_webhook_rejected"
	AuditSubscriptionWebhookConflict      = "subscription_webhook_conflict"
	AuditSubscriptionStatusChanged        = "subscription_status_changed"
	AuditOrganizationCreated              = "organization_created"
	AuditOrganizationSuspended            = "organization_suspended"
	AuditOrganizationResumed              = "organization_resumed"
	AuditOrganizationInviteCreated        = "organization_invite_created"
	AuditOrganizationInviteAccepted       = "organization_invite_accepted"
	AuditOrganizationInviteRevoked        = "organization_invite_revoked"
	AuditOrganizationInviteRejected       = "organization_invite_rejected"
	AuditOrganizationMemberSuspended      = "organization_member_suspended"
	AuditOrganizationMemberResumed        = "organization_member_resumed"
	AuditOrganizationMemberRemoved        = "organization_member_removed"
	AuditOrganizationMemberRoleChanged    = "organization_member_role_changed"
	AuditOrganizationAuthorizationDenied  = "organization_authorization_denied"
	AuditDeviceGroupCreated               = "device_group_created"
	AuditDeviceGroupRenamed               = "device_group_renamed"
	AuditDeviceGroupDeleted               = "device_group_deleted"
	AuditOrganizationDeviceEnrolled       = "organization_device_enrolled"
	AuditOrganizationDeviceRemoved        = "organization_device_removed"
	AuditOrganizationDeviceGrouped        = "organization_device_grouped"
	AuditOrganizationDeviceUngrouped      = "organization_device_ungrouped"
	AuditConnectionGrantCreated           = "connection_grant_created"
	AuditConnectionGrantRevoked           = "connection_grant_revoked"
	AuditOrganizationConnectionAuthorized = "organization_connection_authorized"
	AuditOrganizationConnectionDenied     = "organization_connection_denied"
	AuditOrganizationAuditCleaned         = "organization_audit_cleaned"
	AuditDeploymentBundleCreated          = "deployment_bundle_created"
	AuditBootstrapCredentialRevoked       = "bootstrap_credential_revoked"
	AuditBootstrapCredentialRedeemed      = "bootstrap_credential_redeemed"
	AuditRolloutCreated                   = "rollout_created"
	AuditRolloutTargetReported            = "rollout_target_reported"
	AuditRolloutTargetRetried             = "rollout_target_retried"
	AuditRolloutCanceled                  = "rollout_canceled"
	AuditPrivateLicenseImported           = "private_license_imported"
	AuditPrivateLicenseValidationFailed   = "private_license_validation_failed"
	AuditPrivateLicenseExpiring           = "private_license_expiring"
	AuditPrivateLicenseExpired            = "private_license_expired"
	AuditPrivateLicensePolicyDenied       = "private_license_policy_denied"
)

func (s *Service) ListAuditEvents(ctx context.Context) ([]AuditEvent, error) {
	return s.store.ListAuditEvents(ctx)
}

func (s *Service) RecordConnectionLog(ctx context.Context, req RecordConnectionLogRequest) (ConnectionLog, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if req.AccountID == "" {
		return ConnectionLog{}, fmt.Errorf("account_id is required")
	}
	if req.NetworkID == "" {
		return ConnectionLog{}, fmt.Errorf("network_id is required")
	}
	if req.RelayBytesIn < 0 || req.RelayBytesOut < 0 {
		return ConnectionLog{}, fmt.Errorf("relay byte counters cannot be negative")
	}
	account, err := s.store.GetAccount(ctx, req.AccountID)
	if err != nil {
		return ConnectionLog{}, fmt.Errorf("account was not found: %w", err)
	}
	network, err := s.store.GetNetwork(ctx, req.NetworkID)
	if err != nil {
		return ConnectionLog{}, fmt.Errorf("network was not found: %w", err)
	}
	if network.AccountID != account.ID {
		return ConnectionLog{}, fmt.Errorf("network does not belong to account: %w", ErrForbidden)
	}
	organizationID, permissionSource, err := s.organizationConnectionLogContextLocked(ctx, account.ID, network.ID, req.SourceDeviceID, req.TargetDeviceID)
	if err != nil {
		return ConnectionLog{}, err
	}
	startedAt := req.StartedAt
	if startedAt.IsZero() {
		startedAt = s.nowTime()
	}
	log := ConnectionLog{
		ID:                 mustID("conn"),
		OrganizationID:     organizationID,
		PermissionSource:   permissionSource,
		SessionID:          strings.TrimSpace(req.SessionID),
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     strings.TrimSpace(req.SourceDeviceID),
		TargetDeviceID:     strings.TrimSpace(req.TargetDeviceID),
		PathType:           strings.TrimSpace(req.PathType),
		PathState:          strings.TrimSpace(req.PathState),
		LatencyMS:          req.LatencyMS,
		PacketLossPermille: req.PacketLossPermille,
		JitterMS:           req.JitterMS,
		SwitchCount:        req.SwitchCount,
		SwitchReasons:      append([]string(nil), req.SwitchReasons...),
		StartedAt:          startedAt,
		EndedAt:            req.EndedAt,
		RelayBytesIn:       req.RelayBytesIn,
		RelayBytesOut:      req.RelayBytesOut,
		SwitchFromPath:     sanitizeSwitchPath(req.SwitchFromPath),
		SwitchToPath:       sanitizeSwitchPath(req.SwitchToPath),
		SwitchScoreDelta:   nonNegativeInt(req.SwitchScoreDelta),
		AutoSwitched:       req.AutoSwitched,
		Error:              strings.TrimSpace(req.Error),
	}
	log = withConnectionLogQuality(log)
	return s.store.CreateConnectionLog(ctx, log)
}

func (s *Service) organizationConnectionLogContextLocked(ctx context.Context, accountID, networkID, sourceDeviceID, targetDeviceID string) (string, ConnectionPermissionSource, error) {
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	if targetDeviceID == "" {
		return "", "", nil
	}
	organizationDevice, err := s.store.FindOrganizationDevice(ctx, targetDeviceID)
	if errors.Is(err, ErrNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if _, err := s.store.GetOrganization(ctx, organizationDevice.OrganizationID); err != nil {
		return "", "", fmt.Errorf("organization for target device was not found: %w", ErrForbidden)
	}
	target, err := s.store.GetDevice(ctx, targetDeviceID)
	if err != nil || target.AccountID != organizationDevice.AccountID {
		return "", "", fmt.Errorf("organization target device ownership is invalid: %w", ErrForbidden)
	}
	sourceDeviceID = strings.TrimSpace(sourceDeviceID)
	if sourceDeviceID != "" {
		source, err := s.store.GetDevice(ctx, sourceDeviceID)
		if err != nil {
			return "", "", fmt.Errorf("source device was not found: %w", err)
		}
		if source.AccountID != accountID || source.NetworkID != networkID {
			return "", "", fmt.Errorf("source device does not belong to connection account and network: %w", ErrForbidden)
		}
	}
	permissionSource := ConnectionPermissionDeny
	membership, err := s.store.GetOrganizationMembership(ctx, organizationDevice.OrganizationID, accountID)
	if err == nil && membership.Status == MembershipStatusActive {
		grants, listErr := s.store.ListConnectionGrants(ctx, organizationDevice.OrganizationID)
		if listErr != nil {
			return "", "", listErr
		}
		for _, grant := range grants {
			if grant.MemberAccountID == accountID && grant.Scope == ConnectionGrantDevice && grant.DeviceID == targetDeviceID {
				permissionSource = ConnectionPermissionDeviceGrant
				break
			}
		}
		if permissionSource == ConnectionPermissionDeny && organizationDevice.GroupID != "" {
			for _, grant := range grants {
				if grant.MemberAccountID == accountID && grant.Scope == ConnectionGrantGroup && grant.GroupID == organizationDevice.GroupID {
					permissionSource = ConnectionPermissionGroupGrant
					break
				}
			}
		}
	}
	return organizationDevice.OrganizationID, permissionSource, nil
}

func (s *Service) ListRelaySessionConnectionLogs(ctx context.Context, sessionID string) ([]ConnectionLog, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if _, err := s.store.GetRelaySession(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("relay session was not found: %w", err)
	}
	return s.store.ListConnectionLogs(ctx, sessionID)
}

func (s *Service) RecordRelayUsage(ctx context.Context, req RecordRelayUsageRequest) (RelayUsage, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if req.AccountID == "" {
		return RelayUsage{}, fmt.Errorf("account_id is required")
	}
	if req.NetworkID == "" {
		return RelayUsage{}, fmt.Errorf("network_id is required")
	}
	account, err := s.store.GetAccount(ctx, req.AccountID)
	if err != nil {
		return RelayUsage{}, fmt.Errorf("account was not found: %w", err)
	}
	network, err := s.store.GetNetwork(ctx, req.NetworkID)
	if err != nil {
		return RelayUsage{}, fmt.Errorf("network was not found: %w", err)
	}
	if network.AccountID != account.ID {
		return RelayUsage{}, fmt.Errorf("network does not belong to account: %w", ErrForbidden)
	}
	usage := RelayUsage{
		ID:         mustID("relay"),
		AccountID:  account.ID,
		NetworkID:  network.ID,
		DeviceID:   strings.TrimSpace(req.DeviceID),
		SessionID:  strings.TrimSpace(req.SessionID),
		BytesIn:    req.BytesIn,
		BytesOut:   req.BytesOut,
		RecordedAt: s.nowTime(),
	}
	return s.store.CreateRelayUsage(ctx, usage)
}

func withConnectionLogQuality(log ConnectionLog) ConnectionLog {
	quality := p2p.ScoreConnectionQuality(p2p.ConnectionQualityInput{
		PathType:           p2p.PathType(strings.TrimSpace(log.PathType)),
		State:              p2p.PathState(strings.TrimSpace(log.PathState)),
		LatencyMS:          log.LatencyMS,
		PacketLossPermille: log.PacketLossPermille,
		JitterMS:           log.JitterMS,
		RelayBytesIn:       log.RelayBytesIn,
		RelayBytesOut:      log.RelayBytesOut,
		SwitchCount:        log.SwitchCount,
		SwitchReasons:      log.SwitchReasons,
	})
	log.PathType = string(quality.PathType)
	log.PathState = string(quality.State)
	log.QualityScore = quality.Score
	log.LatencyMS = quality.LatencyMS
	log.PacketLossPermille = quality.PacketLossPermille
	log.JitterMS = quality.JitterMS
	log.RelayBytesIn = quality.RelayBytesIn
	log.RelayBytesOut = quality.RelayBytesOut
	log.SwitchCount = quality.SwitchCount
	log.SwitchReasons = append([]string(nil), quality.SwitchReasons...)
	log.SwitchFromPath = sanitizeSwitchPath(log.SwitchFromPath)
	log.SwitchToPath = sanitizeSwitchPath(log.SwitchToPath)
	log.SwitchScoreDelta = nonNegativeInt(log.SwitchScoreDelta)
	return log
}

func sanitizeSwitchPath(path string) string {
	switch p2p.PathType(strings.TrimSpace(path)) {
	case p2p.PathTypeLANDirect:
		return string(p2p.PathTypeLANDirect)
	case p2p.PathTypePublicDirect:
		return string(p2p.PathTypePublicDirect)
	case p2p.PathTypeRelay:
		return string(p2p.PathTypeRelay)
	default:
		return ""
	}
}

func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func (s *Service) ListRelaySessionUsage(ctx context.Context, sessionID string) ([]RelayUsage, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if _, err := s.store.GetRelaySession(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("relay session was not found: %w", err)
	}
	return s.store.ListRelayUsage(ctx, sessionID)
}

func (s *Service) recordAudit(ctx context.Context, event, accountID, networkID, deviceID string, metadata map[string]any) error {
	if event == "" {
		return nil
	}
	audit := AuditEvent{
		ID:        mustID("audit"),
		Time:      s.nowTime(),
		Event:     event,
		AccountID: accountID,
		NetworkID: networkID,
		DeviceID:  deviceID,
		Metadata:  sanitizeAuditMetadata(metadata),
	}
	_, err := s.store.AddAuditEvent(ctx, audit)
	return err
}

func sanitizeAuditMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "private") ||
			strings.Contains(lower, "key_pem") ||
			strings.Contains(lower, "csr") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "code") ||
			strings.Contains(lower, "signature") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "payload") ||
			strings.Contains(lower, "digest") ||
			strings.Contains(lower, "provider_subscription") ||
			strings.Contains(lower, "provider_customer") ||
			strings.Contains(lower, "payment") ||
			strings.Contains(lower, "card") ||
			strings.Contains(lower, "checkout") ||
			strings.Contains(lower, "clipboard") ||
			strings.Contains(lower, "rdp_content") ||
			strings.Contains(lower, "file_content") {
			continue
		}
		if strings.Contains(lower, "candidate_address") ||
			strings.Contains(lower, "remote_addr") ||
			strings.Contains(lower, "raw_traffic") {
			continue
		}
		out[key] = value
	}
	return out
}
