package cloudhub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"meshlink/internal/licensing"
)

type PrivateLicenseSummary = licensing.Summary

type ImportPrivateLicenseRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	SignedLicense  []byte `json:"-"`
}

func (s *Service) SetPrivateLicenseManager(manager *licensing.Manager) {
	s.privateLicenseMu.Lock()
	s.privateLicense = manager
	s.privateLicenseMu.Unlock()
}

func (s *Service) ImportPrivateLicense(ctx context.Context, req ImportPrivateLicenseRequest) (PrivateLicenseSummary, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionPrivateLicenseManage, true)
	if err != nil {
		return PrivateLicenseSummary{}, err
	}
	manager := s.privateLicenseManager()
	if manager == nil {
		return PrivateLicenseSummary{}, fmt.Errorf("private licensing is not configured: %w", licensing.ErrUnavailable)
	}
	if manager.Binding().OrganizationID != organization.ID {
		err := licensing.ErrBindingMismatch
		_ = s.recordOrganizationAudit(ctx, AuditPrivateLicenseValidationFailed, actor.ID, organization.ID, "", "import_private_license", "rejected", map[string]any{
			"error_kind": privateLicenseErrorKind(err),
		})
		return PrivateLicenseSummary{}, err
	}
	summary, err := manager.Import(req.SignedLicense)
	if err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditPrivateLicenseValidationFailed, actor.ID, organization.ID, "", "import_private_license", "rejected", map[string]any{
			"error_kind": privateLicenseErrorKind(err),
		})
		return PrivateLicenseSummary{}, err
	}
	if summary.OrganizationID != organization.ID {
		return PrivateLicenseSummary{}, licensing.ErrBindingMismatch
	}
	if err := s.recordOrganizationAudit(ctx, AuditPrivateLicenseImported, actor.ID, organization.ID, "", "import_private_license", "imported", map[string]any{
		"license_id": summary.LicenseID, "key_id": summary.KeyID, "deployment_id": summary.DeploymentID,
		"expires_at": summary.ExpiresAt, "expiry_policy": string(summary.ExpiryPolicy),
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return PrivateLicenseSummary{}, err
	}
	return summary, nil
}

func (s *Service) GetPrivateLicenseSummary(ctx context.Context, actorAccountID, organizationID string) (PrivateLicenseSummary, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionPrivateLicenseManage, true)
	if err != nil {
		return PrivateLicenseSummary{}, err
	}
	manager := s.privateLicenseManager()
	if manager == nil {
		return PrivateLicenseSummary{}, fmt.Errorf("private licensing is not configured: %w", licensing.ErrUnavailable)
	}
	summary, err := manager.Summary()
	if err != nil {
		return PrivateLicenseSummary{}, err
	}
	if summary.OrganizationID != organization.ID {
		return PrivateLicenseSummary{}, licensing.ErrBindingMismatch
	}
	return summary, nil
}

func (s *Service) privateLicenseManager() *licensing.Manager {
	s.privateLicenseMu.RLock()
	defer s.privateLicenseMu.RUnlock()
	return s.privateLicense
}

func (s *Service) privateLicenseEnabled() bool {
	return s.privateLicenseManager() != nil
}

func (s *Service) enforcePrivateLicenseOrganizationOperationLocked(ctx context.Context, organizationID, actorAccountID string, operation licensing.Operation, action string) error {
	if !s.privateLicenseEnabled() {
		return nil
	}
	current, err := s.privateLicenseCurrentForOrganizationLocked(ctx, organizationID)
	if err != nil {
		s.recordPrivateLicenseDeniedLocked(ctx, actorAccountID, organizationID, action, err, nil)
		return err
	}
	return s.authorizePrivateLicenseOperationLocked(ctx, current, actorAccountID, organizationID, operation, action)
}

func (s *Service) enforcePrivateLicenseAccountOperationLocked(ctx context.Context, accountID string, operation licensing.Operation, action string) error {
	if !s.privateLicenseEnabled() {
		return nil
	}
	manager := s.privateLicenseManager()
	current, err := manager.Current()
	if err != nil {
		s.recordPrivateLicenseDeniedLocked(ctx, accountID, "", action, err, nil)
		return err
	}
	organization, err := s.store.GetOrganization(ctx, current.Payload.OrganizationID)
	if err != nil {
		err = fmt.Errorf("private license organization is unavailable: %w", licensing.ErrBindingMismatch)
		s.recordPrivateLicenseDeniedLocked(ctx, accountID, current.Payload.OrganizationID, action, err, &current)
		return err
	}
	if accountID != organization.OwnerAccountID {
		member, memberErr := s.store.GetOrganizationMembership(ctx, organization.ID, strings.TrimSpace(accountID))
		if memberErr != nil || member.Status != MembershipStatusActive {
			err = fmt.Errorf("account is outside the licensed organization: %w", licensing.ErrBindingMismatch)
			s.recordPrivateLicenseDeniedLocked(ctx, accountID, organization.ID, action, err, &current)
			return err
		}
	}
	return s.authorizePrivateLicenseOperationLocked(ctx, current, accountID, organization.ID, operation, action)
}

func (s *Service) privateLicenseCurrentForOrganizationLocked(ctx context.Context, organizationID string) (licensing.Verified, error) {
	manager := s.privateLicenseManager()
	if manager == nil {
		return licensing.Verified{}, licensing.ErrUnavailable
	}
	current, err := manager.Current()
	if err != nil {
		return licensing.Verified{}, err
	}
	if current.Payload.OrganizationID != strings.TrimSpace(organizationID) {
		return licensing.Verified{}, licensing.ErrBindingMismatch
	}
	if _, err := s.store.GetOrganization(ctx, current.Payload.OrganizationID); err != nil {
		return licensing.Verified{}, licensing.ErrBindingMismatch
	}
	return current, nil
}

func (s *Service) authorizePrivateLicenseOperationLocked(ctx context.Context, current licensing.Verified, actorAccountID, organizationID string, operation licensing.Operation, action string) error {
	now := s.nowTime()
	summary := current.Summary(now)
	metadata := map[string]any{
		"license_id": summary.LicenseID, "key_id": summary.KeyID, "deployment_id": summary.DeploymentID,
		"expires_at": summary.ExpiresAt, "expiry_policy": string(summary.ExpiryPolicy), "license_state": summary.State,
	}
	if summary.State == "expired" || summary.State == "grace_period" {
		_ = s.recordOrganizationAudit(ctx, AuditPrivateLicenseExpired, actorAccountID, organizationID, "", action, summary.State, metadata)
	} else if summary.ExpiresAt.Sub(now) <= 30*24*time.Hour {
		_ = s.recordOrganizationAudit(ctx, AuditPrivateLicenseExpiring, actorAccountID, organizationID, "", action, "warning", metadata)
	}
	if err := current.Authorize(operation, now); err != nil {
		s.recordPrivateLicenseDeniedLocked(ctx, actorAccountID, organizationID, action, err, &current)
		return err
	}
	return nil
}

func (s *Service) recordPrivateLicenseDeniedLocked(ctx context.Context, actorAccountID, organizationID, action string, cause error, current *licensing.Verified) {
	metadata := map[string]any{"error_kind": privateLicenseErrorKind(cause)}
	if current != nil {
		summary := current.Summary(s.nowTime())
		metadata["license_id"] = summary.LicenseID
		metadata["key_id"] = summary.KeyID
		metadata["deployment_id"] = summary.DeploymentID
		metadata["expiry_policy"] = string(summary.ExpiryPolicy)
		metadata["license_state"] = summary.State
	}
	_ = s.recordOrganizationAudit(ctx, AuditPrivateLicensePolicyDenied, actorAccountID, organizationID, "", action, "denied", metadata)
}

func privateLicenseErrorKind(err error) string {
	switch {
	case errors.Is(err, licensing.ErrUnknownKey):
		return "unknown_key"
	case errors.Is(err, licensing.ErrSignatureInvalid):
		return "signature_invalid"
	case errors.Is(err, licensing.ErrBindingMismatch):
		return "binding_mismatch"
	case errors.Is(err, licensing.ErrNotEffective):
		return "not_effective"
	case errors.Is(err, licensing.ErrExpired):
		return "expired"
	case errors.Is(err, licensing.ErrPolicyDenied):
		return "policy_denied"
	case errors.Is(err, licensing.ErrUnavailable):
		return "unavailable"
	default:
		return "invalid"
	}
}

func (s *Service) evaluateQuotaLocked(ctx context.Context, accountID string, dimension QuotaDimension, quota PlanQuota) (QuotaDecision, error) {
	if quota.Mode != PlanQuotaContractCustom || !s.privateLicenseEnabled() {
		return s.quotaEvaluator.Evaluate(accountID, dimension, quota)
	}
	current, err := s.privateLicenseManager().Current()
	if err != nil {
		return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, PlanEnterprise, "private license entitlement is unavailable")
	}
	organization, err := s.store.GetOrganization(ctx, current.Payload.OrganizationID)
	if err != nil {
		return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, PlanEnterprise, "private license organization binding is unavailable")
	}
	if accountID != organization.OwnerAccountID {
		member, memberErr := s.store.GetOrganizationMembership(ctx, organization.ID, accountID)
		if memberErr != nil || member.Status != MembershipStatusActive {
			return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, PlanEnterprise, "account is outside the licensed organization")
		}
	}
	var limit int64
	switch dimension {
	case QuotaDimensionDeviceCount:
		limit = current.Payload.Entitlements.DeviceCount
	case QuotaDimensionConcurrentOnlineDevices:
		limit = current.Payload.Entitlements.ConcurrentOnlineDevices
	case QuotaDimensionOfficialRelayTraffic:
		limit = current.Payload.Entitlements.RelayBytesPerMonth
	case QuotaDimensionMemberCount:
		limit = current.Payload.Entitlements.MemberCount
	case QuotaDimensionAuditLogRetention:
		limit = current.Payload.Entitlements.AuditRetentionDays
	case QuotaDimensionActiveRelaySessions:
		limit = current.Payload.Entitlements.ActiveRelaySessions
	case QuotaDimensionDeploymentCount:
		limit = current.Payload.Entitlements.DeploymentCount
	default:
		return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, PlanEnterprise, "private license entitlement is not mapped")
	}
	if limit <= 0 {
		return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, PlanEnterprise, "private license entitlement is missing")
	}
	return QuotaDecision{Mode: PlanQuotaContractCustom, Limited: true, Limit: limit}, nil
}

func (s *Service) enforcePrivateDeploymentCountLocked(ctx context.Context, organization Organization, actorAccountID string) error {
	if !s.privateLicenseEnabled() {
		return nil
	}
	current, err := s.privateLicenseCurrentForOrganizationLocked(ctx, organization.ID)
	if err != nil {
		return err
	}
	bundles, err := s.store.ListDeploymentBundles(ctx, organization.ID)
	if err != nil {
		return err
	}
	used, limit := int64(len(bundles)), current.Payload.Entitlements.DeploymentCount
	if limit <= 0 || used >= limit {
		quotaErr := newQuotaError(QuotaDimensionDeploymentCount, used, limit, PlanQuotaContractCustom, PlanEnterprise, "private deployment quota exceeded")
		s.recordQuotaDeniedLocked(ctx, actorAccountID, "", "", quotaErr)
		return quotaErr
	}
	return nil
}
