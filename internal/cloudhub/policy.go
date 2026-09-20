package cloudhub

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *Service) GetAccountPolicyStatus(ctx context.Context, accountID string) (AccountPolicyStatus, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AccountPolicyStatus{}, fmt.Errorf("account_id is required")
	}
	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return AccountPolicyStatus{}, fmt.Errorf("account was not found: %w", err)
	}
	account, err = s.refreshAccountSubscriptionLocked(ctx, account)
	if err != nil {
		return AccountPolicyStatus{}, err
	}
	return s.accountPolicyStatusLocked(ctx, account)
}

func (s *Service) accountPolicyStatusLocked(ctx context.Context, account Account) (AccountPolicyStatus, error) {
	var err error
	account, err = s.refreshAccountSubscriptionLocked(ctx, account)
	if err != nil {
		return AccountPolicyStatus{}, err
	}
	now := s.nowTime()
	policy := s.policyForAccount(account)
	sessions, err := s.store.ListRelaySessions(ctx, account.ID)
	if err != nil {
		return AccountPolicyStatus{}, err
	}
	relayBytesUsed, err := s.accountRelayBytesUsedLocked(ctx, account.ID, sessions)
	if err != nil {
		return AccountPolicyStatus{}, err
	}
	if account.PlanID != "" {
		plan, err := s.planForAccount(account)
		if err != nil {
			return AccountPolicyStatus{}, err
		}
		decision, err := s.evaluateQuotaLocked(ctx, account.ID, QuotaDimensionOfficialRelayTraffic, plan.Entitlements.OfficialRelayTraffic)
		if err != nil {
			return AccountPolicyStatus{}, withQuotaPlan(err, plan.ID)
		}
		policy.Name = "plan-" + string(plan.ID) + "-relay"
		if decision.Limited {
			policy.RelayBytesQuota = decision.Limit
		} else {
			policy.RelayBytesQuota = 0
		}
		if s.privateLicenseEnabled() && plan.ID == PlanEnterprise {
			current, currentErr := s.privateLicenseManager().Current()
			if currentErr != nil || !current.Payload.Entitlements.Relay {
				return AccountPolicyStatus{}, newQuotaError(QuotaDimensionOfficialRelayTraffic, 0, 0, PlanQuotaContractCustom, plan.ID, "private Relay entitlement is unavailable")
			}
			policy.MaxActiveRelaySessions = int(current.Payload.Entitlements.ActiveRelaySessions)
		}
	}
	var activeSessions int
	var sessionsCreatedToday int
	for _, session := range sessions {
		if sameUTCDay(session.CreatedAt, now) {
			sessionsCreatedToday++
		}
		if session.Status != RelaySessionClosed && (session.ExpiresAt.IsZero() || !now.After(session.ExpiresAt)) {
			activeSessions++
		}
	}
	remaining := int64(0)
	if policy.RelayBytesQuota > 0 {
		remaining = policy.RelayBytesQuota - relayBytesUsed
		if remaining < 0 {
			remaining = 0
		}
	}
	status := AccountPolicyStatus{
		AccountID:                 account.ID,
		AccountStatus:             account.Status,
		Policy:                    policy,
		RelayBytesUsed:            relayBytesUsed,
		RelayBytesRemaining:       remaining,
		ActiveRelaySessions:       activeSessions,
		RelaySessionsCreatedToday: sessionsCreatedToday,
		AsOf:                      now,
	}
	status.RelayUsageReminder = relayUsageReminderForPolicy(status)
	return status, nil
}

func (s *Service) accountRelayBytesUsedLocked(ctx context.Context, accountID string, sessions []RelaySession) (int64, error) {
	var relayBytesUsed int64
	sessionIDs := map[string]struct{}{}
	for _, session := range sessions {
		sessionIDs[session.ID] = struct{}{}
		relayBytesUsed += session.RelayBytesIn + session.RelayBytesOut
	}
	usageRows, err := s.store.ListRelayUsage(ctx, "")
	if err != nil {
		return 0, err
	}
	for _, usage := range usageRows {
		if usage.AccountID != accountID {
			continue
		}
		if usage.SessionID != "" {
			if _, ok := sessionIDs[usage.SessionID]; ok {
				continue
			}
		}
		relayBytesUsed += usage.BytesIn + usage.BytesOut
	}
	return relayBytesUsed, nil
}

func (s *Service) policyForAccount(account Account) AccountPolicy {
	name := strings.TrimSpace(account.PolicyName)
	if name == "" {
		name = s.defaultPolicyName
	}
	if policy, ok := s.policies[name]; ok {
		return policy
	}
	return DefaultAccountPolicy
}

func checkRelaySessionQuota(status AccountPolicyStatus) error {
	policy := status.Policy
	if policy.RelayBytesQuota > 0 && status.RelayBytesUsed >= policy.RelayBytesQuota {
		return newQuotaError(QuotaDimensionOfficialRelayTraffic, status.RelayBytesUsed, policy.RelayBytesQuota, PlanQuotaLimited, "", "relay byte quota exceeded")
	}
	if policy.MaxActiveRelaySessions > 0 && status.ActiveRelaySessions >= policy.MaxActiveRelaySessions {
		return newQuotaError(QuotaDimensionActiveRelaySessions, int64(status.ActiveRelaySessions), int64(policy.MaxActiveRelaySessions), PlanQuotaLimited, "", "relay active session quota exceeded")
	}
	if policy.MaxRelaySessionsPerDay > 0 && status.RelaySessionsCreatedToday >= policy.MaxRelaySessionsPerDay {
		return newQuotaError(QuotaDimensionRelaySessionsPerDay, int64(status.RelaySessionsCreatedToday), int64(policy.MaxRelaySessionsPerDay), PlanQuotaLimited, "", "relay daily session quota exceeded")
	}
	return nil
}

func sameUTCDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}
