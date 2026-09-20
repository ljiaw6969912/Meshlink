package cloudhub

import (
	"context"
	"fmt"
	"strings"
)

func (s *Service) ListRiskEvents(ctx context.Context, accountID string) ([]RiskEvent, error) {
	return s.store.ListRiskEvents(ctx, strings.TrimSpace(accountID))
}

func (s *Service) recordRiskEvent(ctx context.Context, kind RiskEventKind, accountID, networkID, deviceID, reason string, metadata map[string]any) error {
	if kind == "" {
		return nil
	}
	event := RiskEvent{
		ID:        mustID("risk"),
		Time:      s.nowTime(),
		Kind:      kind,
		AccountID: strings.TrimSpace(accountID),
		NetworkID: strings.TrimSpace(networkID),
		DeviceID:  strings.TrimSpace(deviceID),
		Reason:    strings.TrimSpace(reason),
		Metadata:  sanitizeAuditMetadata(metadata),
	}
	_, err := s.store.AddRiskEvent(ctx, event)
	return err
}

func (s *Service) recordRelaySessionDenied(ctx context.Context, accountID, networkID, sourceDeviceID string, err error, metadata map[string]any) {
	reason := "relay session denied"
	if err != nil {
		reason = err.Error()
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	if err != nil {
		metadata["error"] = err.Error()
	}
	_ = s.recordRiskEvent(ctx, RiskRelaySessionDenied, accountID, networkID, sourceDeviceID, reason, metadata)
}

func riskReasonForAccountStatus(account Account) string {
	switch account.Status {
	case AccountStatusFrozen:
		if strings.TrimSpace(account.FrozenReason) != "" {
			return account.FrozenReason
		}
		return "account frozen"
	case AccountStatusBanned:
		if strings.TrimSpace(account.BanReason) != "" {
			return account.BanReason
		}
		return "account banned"
	default:
		return fmt.Sprintf("account status is %s", account.Status)
	}
}
