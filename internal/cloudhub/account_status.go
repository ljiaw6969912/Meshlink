package cloudhub

import (
	"context"
	"fmt"
	"strings"
)

func (s *Service) FreezeAccount(ctx context.Context, req AccountStatusChangeRequest) (Account, error) {
	s.opMu.Lock()

	account, err := s.accountForStatusChange(ctx, req.AccountID)
	if err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	if account.Status == AccountStatusBanned {
		s.opMu.Unlock()
		return Account{}, fmt.Errorf("banned account cannot be frozen: %w", ErrForbidden)
	}
	now := s.nowTime()
	reason := strings.TrimSpace(req.Reason)
	enforcementReason := accountEnforcementReason(AccountStatusFrozen, reason)
	account.Status = AccountStatusFrozen
	account.FrozenAt = &now
	account.FrozenReason = reason
	updated, err := s.store.UpdateAccount(ctx, account)
	if err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	if err := s.recordAudit(ctx, AuditAccountFrozen, updated.ID, "", "", map[string]any{
		"reason": reason,
	}); err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	if err := s.recordRiskEvent(ctx, RiskAccountFrozen, updated.ID, "", "", riskReasonForAccountStatus(updated), map[string]any{
		"reason": reason,
	}); err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	revokedSessions, err := s.accountRelaySessionsForEnforcementLocked(ctx, updated, "freeze", enforcementReason)
	if err != nil {
		s.opMu.Unlock()
		return Account{}, err
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

func (s *Service) UnfreezeAccount(ctx context.Context, req AccountStatusChangeRequest) (Account, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	account, err := s.accountForStatusChange(ctx, req.AccountID)
	if err != nil {
		return Account{}, err
	}
	if account.Status == AccountStatusBanned {
		return Account{}, fmt.Errorf("banned account cannot be unfrozen: %w", ErrForbidden)
	}
	reason := strings.TrimSpace(req.Reason)
	account.Status = AccountStatusActive
	account.FrozenAt = nil
	account.FrozenReason = ""
	updated, err := s.store.UpdateAccount(ctx, account)
	if err != nil {
		return Account{}, err
	}
	if err := s.recordAudit(ctx, AuditAccountUnfrozen, updated.ID, "", "", map[string]any{
		"reason": reason,
	}); err != nil {
		return Account{}, err
	}
	return updated, nil
}

func (s *Service) BanAccount(ctx context.Context, req AccountStatusChangeRequest) (Account, error) {
	s.opMu.Lock()

	account, err := s.accountForStatusChange(ctx, req.AccountID)
	if err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	now := s.nowTime()
	reason := strings.TrimSpace(req.Reason)
	enforcementReason := accountEnforcementReason(AccountStatusBanned, reason)
	account.Status = AccountStatusBanned
	account.FrozenAt = nil
	account.FrozenReason = ""
	account.BannedAt = &now
	account.BanReason = reason
	updated, err := s.store.UpdateAccount(ctx, account)
	if err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	ban := Ban{
		ID:        mustID("ban"),
		Scope:     BanScopeAccount,
		TargetID:  updated.ID,
		Reason:    reason,
		CreatedAt: now,
	}
	if _, err := s.store.CreateBan(ctx, ban); err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	if err := s.recordAudit(ctx, AuditAccountBanned, updated.ID, "", "", map[string]any{
		"reason": reason,
	}); err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	if err := s.recordRiskEvent(ctx, RiskAccountBanned, updated.ID, "", "", riskReasonForAccountStatus(updated), map[string]any{
		"reason": reason,
	}); err != nil {
		s.opMu.Unlock()
		return Account{}, err
	}
	revokedSessions, err := s.accountRelaySessionsForEnforcementLocked(ctx, updated, "ban", enforcementReason)
	if err != nil {
		s.opMu.Unlock()
		return Account{}, err
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

func (s *Service) accountForStatusChange(ctx context.Context, accountID string) (Account, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Account{}, fmt.Errorf("account_id is required")
	}
	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return Account{}, fmt.Errorf("account was not found: %w", err)
	}
	return account, nil
}

func ensureAccountActive(account Account) error {
	switch account.Status {
	case "", AccountStatusActive:
		return nil
	case AccountStatusFrozen:
		return fmt.Errorf("account is frozen: %w", ErrForbidden)
	case AccountStatusBanned:
		return fmt.Errorf("account is banned: %w", ErrForbidden)
	default:
		return fmt.Errorf("account status %q is not allowed: %w", account.Status, ErrForbidden)
	}
}

func accountEnforcementReason(status AccountStatus, reason string) string {
	base := "account enforcement applied"
	switch status {
	case AccountStatusFrozen:
		base = "account frozen"
	case AccountStatusBanned:
		base = "account banned"
	}
	if strings.TrimSpace(reason) == "" {
		return base
	}
	return base + ": " + strings.TrimSpace(reason)
}
