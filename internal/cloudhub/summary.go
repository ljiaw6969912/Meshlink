package cloudhub

import (
	"context"
	"fmt"
	"strings"

	"meshlink/internal/p2p"
)

func (s *Service) GetAccountManagementSummary(ctx context.Context, accountID string) (AccountManagementSummary, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AccountManagementSummary{}, fmt.Errorf("account_id is required")
	}
	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return AccountManagementSummary{}, fmt.Errorf("account was not found: %w", err)
	}
	account, err = s.refreshAccountSubscriptionLocked(ctx, account)
	if err != nil {
		return AccountManagementSummary{}, err
	}
	policy, err := s.accountPolicyStatusLocked(ctx, account)
	if err != nil {
		return AccountManagementSummary{}, err
	}
	planQuotas, err := s.accountPlanQuotaSummaryLocked(ctx, account, policy)
	if err != nil {
		return AccountManagementSummary{}, err
	}
	riskEvents, err := s.store.ListRiskEvents(ctx, account.ID)
	if err != nil {
		return AccountManagementSummary{}, err
	}
	auditEvents, err := s.store.ListAuditEvents(ctx)
	if err != nil {
		return AccountManagementSummary{}, err
	}
	auditEvents = filterAuditEventsByAccount(auditEvents, account.ID)

	usageRows, err := s.store.ListRelayUsage(ctx, "")
	if err != nil {
		return AccountManagementSummary{}, err
	}
	usageRows = filterRelayUsageByAccount(usageRows, account.ID)

	logs, err := s.store.ListConnectionLogs(ctx, "")
	if err != nil {
		return AccountManagementSummary{}, err
	}
	logs = filterConnectionLogsByAccount(logs, account.ID)
	connectionQuality := summarizeConnectionQuality(logs)

	sessions, err := s.store.ListRelaySessions(ctx, account.ID)
	if err != nil {
		return AccountManagementSummary{}, err
	}
	sessionSummary := summarizeRelaySessions(sessions)
	return AccountManagementSummary{
		Account:            account,
		Policy:             policy,
		PlanQuotas:         planQuotas,
		RelayUsageReminder: policy.RelayUsageReminder,
		RiskEvents:         riskEvents,
		AuditEvents:        auditEvents,
		RelayUsage:         usageRows,
		ConnectionLogs:     logs,
		ConnectionQuality:  connectionQuality,
		RelaySessions:      sessionSummary,
	}, nil
}

func (s *Service) accountPlanQuotaSummaryLocked(ctx context.Context, account Account, policy AccountPolicyStatus) (*AccountPlanQuotaSummary, error) {
	if strings.TrimSpace(string(account.PlanID)) == "" {
		return nil, nil
	}
	plan, err := s.planForAccount(account)
	if err != nil {
		return nil, err
	}
	deviceUsed, err := s.countAccountDevicesLocked(ctx, account.ID, false)
	if err != nil {
		return nil, err
	}
	onlineUsed, err := s.countAccountDevicesLocked(ctx, account.ID, true)
	if err != nil {
		return nil, err
	}
	deviceQuota, err := s.accountPlanQuotaUsage(ctx, account.ID, QuotaDimensionDeviceCount, plan.Entitlements.DeviceCount, deviceUsed)
	if err != nil {
		return nil, withQuotaPlan(err, plan.ID)
	}
	onlineQuota, err := s.accountPlanQuotaUsage(ctx, account.ID, QuotaDimensionConcurrentOnlineDevices, plan.Entitlements.ConcurrentOnlineDevices, onlineUsed)
	if err != nil {
		return nil, withQuotaPlan(err, plan.ID)
	}
	relayQuota, err := s.accountPlanQuotaUsage(ctx, account.ID, QuotaDimensionOfficialRelayTraffic, plan.Entitlements.OfficialRelayTraffic, policy.RelayBytesUsed)
	if err != nil {
		return nil, withQuotaPlan(err, plan.ID)
	}
	return &AccountPlanQuotaSummary{
		DeviceCount:             deviceQuota,
		ConcurrentOnlineDevices: onlineQuota,
		OfficialRelayTraffic:    relayQuota,
	}, nil
}

func (s *Service) accountPlanQuotaUsage(ctx context.Context, accountID string, dimension QuotaDimension, quota PlanQuota, used int64) (AccountPlanQuotaUsage, error) {
	decision, err := s.evaluateQuotaLocked(ctx, accountID, dimension, quota)
	if err != nil {
		return AccountPlanQuotaUsage{}, err
	}
	usage := AccountPlanQuotaUsage{
		Dimension: dimension,
		Mode:      decision.Mode,
		Unit:      quota.Unit,
		Limited:   decision.Limited,
		Used:      nonNegativeInt64(used),
		Source:    quota.Source,
	}
	if decision.Limited {
		usage.Limit = int64Ptr(nonNegativeInt64(decision.Limit))
		remaining := decision.Limit - used
		if remaining < 0 {
			remaining = 0
		}
		usage.Remaining = int64Ptr(remaining)
	}
	return usage, nil
}

func filterAuditEventsByAccount(events []AuditEvent, accountID string) []AuditEvent {
	out := make([]AuditEvent, 0, len(events))
	for _, event := range events {
		if event.AccountID == accountID {
			out = append(out, event)
		}
	}
	return out
}

func filterRelayUsageByAccount(rows []RelayUsage, accountID string) []RelayUsage {
	out := make([]RelayUsage, 0, len(rows))
	for _, row := range rows {
		if row.AccountID == accountID {
			out = append(out, row)
		}
	}
	return out
}

func filterConnectionLogsByAccount(logs []ConnectionLog, accountID string) []ConnectionLog {
	out := make([]ConnectionLog, 0, len(logs))
	for _, log := range logs {
		if log.AccountID == accountID {
			out = append(out, log)
		}
	}
	return out
}

func summarizeRelaySessions(sessions []RelaySession) RelaySessionsSummary {
	summary := RelaySessionsSummary{
		Total:    len(sessions),
		Sessions: make([]RelaySession, 0, len(sessions)),
	}
	for _, session := range sessions {
		session = publicRelaySession(session)
		switch session.Status {
		case RelaySessionPending:
			summary.Pending++
		case RelaySessionActive:
			summary.Active++
		case RelaySessionClosed:
			summary.Closed++
		}
		summary.Sessions = append(summary.Sessions, session)
	}
	return summary
}

func summarizeConnectionQuality(logs []ConnectionLog) p2p.ConnectionQualitySummary {
	inputs := make([]p2p.ConnectionQualityInput, 0, len(logs))
	for _, log := range logs {
		inputs = append(inputs, p2p.ConnectionQualityInput{
			PathType:           p2p.PathType(log.PathType),
			State:              p2p.PathState(log.PathState),
			LatencyMS:          log.LatencyMS,
			PacketLossPermille: log.PacketLossPermille,
			JitterMS:           log.JitterMS,
			RelayBytesIn:       log.RelayBytesIn,
			RelayBytesOut:      log.RelayBytesOut,
			SwitchCount:        log.SwitchCount,
			SwitchReasons:      log.SwitchReasons,
		})
	}
	return p2p.SummarizeConnectionQuality(inputs)
}
