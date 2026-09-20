package cloudhub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"meshlink/internal/licensing"
)

const (
	defaultOrganizationInviteTTL = 10 * time.Minute
	maxOrganizationInviteTTL     = 24 * time.Hour
)

func (s *Service) CreateOrganization(ctx context.Context, req CreateOrganizationRequest) (Organization, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	ownerID := strings.TrimSpace(req.OwnerAccountID)
	if ownerID == "" {
		return Organization{}, fmt.Errorf("owner_account_id is required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Organization{}, fmt.Errorf("organization name is required")
	}
	owner, err := s.store.GetAccount(ctx, ownerID)
	if err != nil {
		return Organization{}, fmt.Errorf("owner account was not found: %w", err)
	}
	owner, err = s.refreshAccountSubscriptionLocked(ctx, owner)
	if err != nil {
		return Organization{}, err
	}
	if err := ensureAccountActive(owner); err != nil {
		return Organization{}, err
	}
	var plan Plan
	var decision QuotaDecision
	organizationID := mustID("org")
	if s.privateLicenseEnabled() {
		plan, err = s.planForAccount(owner)
		if err != nil {
			return Organization{}, err
		}
		if plan.ID != PlanEnterprise {
			return Organization{}, fmt.Errorf("private organization owner requires enterprise plan: %w", ErrForbidden)
		}
		manager := s.privateLicenseManager()
		binding := manager.Binding()
		organizationID = binding.OrganizationID
		limit := int64(1)
		if current, currentErr := manager.Current(); currentErr == nil {
			limit = current.Payload.Entitlements.MemberCount
		} else if !errors.Is(currentErr, licensing.ErrUnavailable) {
			return Organization{}, currentErr
		}
		decision = QuotaDecision{Mode: PlanQuotaContractCustom, Limited: true, Limit: limit}
	} else {
		plan, decision, err = s.memberQuotaDecisionLocked(ctx, owner)
		if err != nil {
			return Organization{}, err
		}
	}
	if decision.Limited && decision.Limit <= 0 {
		quotaErr := newQuotaError(QuotaDimensionMemberCount, 0, decision.Limit, decision.Mode, plan.ID, "member count quota exceeded")
		s.recordQuotaDeniedLocked(ctx, owner.ID, "", "", quotaErr)
		return Organization{}, quotaErr
	}
	now := s.nowTime()
	organization := Organization{
		ID:             organizationID,
		Name:           name,
		Status:         OrganizationStatusActive,
		OwnerAccountID: owner.ID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	created, err := s.store.CreateOrganization(ctx, organization)
	if err != nil {
		return Organization{}, err
	}
	ownerMembership := Membership{
		ID:             mustID("mem"),
		OrganizationID: created.ID,
		AccountID:      owner.ID,
		Role:           MembershipRoleOwner,
		Status:         MembershipStatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := s.store.CreateMembership(ctx, ownerMembership); err != nil {
		return Organization{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationCreated, owner.ID, created.ID, owner.ID, "create_organization", "created", map[string]any{
		"name":    created.Name,
		"plan_id": string(plan.ID),
	}); err != nil {
		return Organization{}, err
	}
	return created, nil
}

func (s *Service) GetOrganization(ctx context.Context, organizationID, actorAccountID string) (Organization, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionOrganizationRead, true)
	return organization, err
}

func (s *Service) SuspendOrganization(ctx context.Context, req OrganizationStatusChangeRequest) (Organization, error) {
	return s.changeOrganizationStatus(ctx, req, OrganizationStatusSuspended)
}

func (s *Service) ResumeOrganization(ctx context.Context, req OrganizationStatusChangeRequest) (Organization, error) {
	return s.changeOrganizationStatus(ctx, req, OrganizationStatusActive)
}

func (s *Service) changeOrganizationStatus(ctx context.Context, req OrganizationStatusChangeRequest, status OrganizationStatus) (Organization, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionOrganizationManage, false)
	if err != nil {
		return Organization{}, err
	}
	now := s.nowTime()
	event := AuditOrganizationResumed
	action := "resume_organization"
	result := "resumed"
	if status == OrganizationStatusSuspended {
		organization.Status = OrganizationStatusSuspended
		organization.SuspendedAt = &now
		organization.SuspendedReason = strings.TrimSpace(req.Reason)
		event = AuditOrganizationSuspended
		action = "suspend_organization"
		result = "suspended"
	} else {
		organization.Status = OrganizationStatusActive
		organization.SuspendedAt = nil
		organization.SuspendedReason = ""
	}
	organization.UpdatedAt = now
	updated, err := s.store.UpdateOrganization(ctx, organization)
	if err != nil {
		return Organization{}, err
	}
	if err := s.recordOrganizationAudit(ctx, event, req.ActorAccountID, updated.ID, "", action, result, map[string]any{
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return Organization{}, err
	}
	return updated, nil
}

func (s *Service) ListOrganizationMembers(ctx context.Context, organizationID, actorAccountID string) ([]Membership, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionOrganizationRead, true)
	if err != nil {
		return nil, err
	}
	organizationID = organization.ID
	members, err := s.store.ListOrganizationMemberships(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, 0, len(members))
	for _, member := range members {
		if member.Status != MembershipStatusRemoved {
			out = append(out, member)
		}
	}
	return out, nil
}

func (s *Service) CreateOrganizationInvite(ctx context.Context, req CreateOrganizationInviteRequest) (OrganizationInviteResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionMemberInvite, true)
	if err != nil {
		return OrganizationInviteResult{}, err
	}
	if _, err := s.activeOrganizationOwnerLocked(ctx, organization); err != nil {
		return OrganizationInviteResult{}, err
	}
	if err := s.enforcePrivateLicenseOrganizationOperationLocked(ctx, organization.ID, req.ActorAccountID, licensing.OperationNewMember, "create_organization_invite"); err != nil {
		return OrganizationInviteResult{}, err
	}
	targetID := strings.TrimSpace(req.InvitedAccountID)
	if targetID == "" {
		return OrganizationInviteResult{}, fmt.Errorf("invited_account_id is required")
	}
	target, err := s.store.GetAccount(ctx, targetID)
	if err != nil {
		return OrganizationInviteResult{}, fmt.Errorf("invited account was not found: %w", err)
	}
	target, err = s.refreshAccountSubscriptionLocked(ctx, target)
	if err != nil {
		return OrganizationInviteResult{}, err
	}
	if err := ensureAccountActive(target); err != nil {
		return OrganizationInviteResult{}, err
	}
	if existing, err := s.store.GetOrganizationMembership(ctx, organization.ID, target.ID); err == nil && existing.Status != MembershipStatusRemoved {
		return OrganizationInviteResult{}, fmt.Errorf("organization membership already exists: %w", ErrConflict)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return OrganizationInviteResult{}, err
	}
	role, err := normalizeMembershipRole(req.Role)
	if err != nil {
		return OrganizationInviteResult{}, err
	}
	if actor.Role == MembershipRoleAdmin && role == MembershipRoleOwner {
		return OrganizationInviteResult{}, fmt.Errorf("admin cannot invite an owner: %w", ErrForbidden)
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = defaultOrganizationInviteTTL
	}
	if ttl > maxOrganizationInviteTTL {
		ttl = maxOrganizationInviteTTL
	}
	token, err := randomTokenBytes(32)
	if err != nil {
		return OrganizationInviteResult{}, err
	}
	code, err := randomCode()
	if err != nil {
		return OrganizationInviteResult{}, err
	}
	now := s.nowTime()
	invite := OrganizationInvite{
		ID:                 mustID("orginv"),
		OrganizationID:     organization.ID,
		InvitedAccountID:   target.ID,
		InvitedByAccountID: strings.TrimSpace(req.ActorAccountID),
		Role:               role,
		TokenDigest:        hashOrganizationInviteToken(token),
		CodeDigest:         hashOrganizationInviteCode(token, code),
		CreatedAt:          now,
		ExpiresAt:          now.Add(ttl),
	}
	created, err := s.store.CreateOrganizationInvite(ctx, invite)
	if err != nil {
		return OrganizationInviteResult{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationInviteCreated, req.ActorAccountID, organization.ID, target.ID, "create_organization_invite", "created", map[string]any{
		"invite_id":         created.ID,
		"role":              string(created.Role),
		"expires_at":        created.ExpiresAt,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return OrganizationInviteResult{}, err
	}
	return organizationInviteResult(created, token, code), nil
}

func (s *Service) RevokeOrganizationInvite(ctx context.Context, req RevokeOrganizationInviteRequest) (OrganizationInvite, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionMemberInvite, true)
	if err != nil {
		return OrganizationInvite{}, err
	}
	inviteID := strings.TrimSpace(req.InviteID)
	if inviteID == "" {
		return OrganizationInvite{}, fmt.Errorf("invite_id is required")
	}
	invite, err := s.store.GetOrganizationInvite(ctx, inviteID)
	if err != nil {
		return OrganizationInvite{}, fmt.Errorf("organization invite was not found: %w", err)
	}
	if invite.OrganizationID != organization.ID {
		return OrganizationInvite{}, fmt.Errorf("organization invite does not belong to organization: %w", ErrForbidden)
	}
	now := s.nowTime()
	if invite.RevokedAt == nil {
		invite.RevokedAt = &now
	}
	updated, err := s.store.UpdateOrganizationInvite(ctx, invite)
	if err != nil {
		return OrganizationInvite{}, err
	}
	if _, err := s.store.CreateRevocation(ctx, Revocation{
		ID:        mustID("rev"),
		Scope:     RevocationScopeInvite,
		TargetID:  updated.ID,
		CreatedAt: now,
	}); err != nil && !errors.Is(err, ErrAlreadyExists) {
		return OrganizationInvite{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationInviteRevoked, req.ActorAccountID, organization.ID, invite.InvitedAccountID, "revoke_organization_invite", "revoked", map[string]any{
		"invite_id": updated.ID, "permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return OrganizationInvite{}, err
	}
	return updated, nil
}

func (s *Service) AcceptOrganizationInvite(ctx context.Context, req AcceptOrganizationInviteRequest) (Membership, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	organizationID := strings.TrimSpace(req.OrganizationID)
	accountID := strings.TrimSpace(req.AccountID)
	token := strings.TrimSpace(req.Token)
	code := strings.TrimSpace(req.Code)
	if organizationID == "" {
		return Membership{}, fmt.Errorf("organization_id is required")
	}
	if accountID == "" {
		return Membership{}, fmt.Errorf("account_id is required")
	}
	if token == "" || code == "" {
		return Membership{}, fmt.Errorf("token and code are required")
	}
	tokenDigest := hashOrganizationInviteToken(token)
	codeDigest := hashOrganizationInviteCode(token, code)
	invite, err := s.store.GetOrganizationInviteByTokenDigest(ctx, tokenDigest)
	if err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "not_found", nil)
		return Membership{}, fmt.Errorf("organization invite was not found: %w", err)
	}
	if invite.OrganizationID != organizationID {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "organization_mismatch", map[string]any{
			"invite_id": invite.ID,
		})
		return Membership{}, fmt.Errorf("organization invite does not belong to organization: %w", ErrForbidden)
	}
	if invite.InvitedAccountID != accountID {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, invite.InvitedAccountID, "accept_organization_invite", "account_mismatch", map[string]any{
			"invite_id": invite.ID,
		})
		return Membership{}, fmt.Errorf("organization invite does not belong to account: %w", ErrForbidden)
	}
	organization, err := s.organizationForMutationLocked(ctx, organizationID)
	if err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "organization_not_found", map[string]any{"invite_id": invite.ID})
		return Membership{}, err
	}
	if err := ensureOrganizationActive(organization); err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "organization_suspended", map[string]any{"invite_id": invite.ID})
		return Membership{}, err
	}
	owner, err := s.activeOrganizationOwnerLocked(ctx, organization)
	if err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "owner_inactive", map[string]any{"invite_id": invite.ID})
		return Membership{}, err
	}
	if err := s.enforcePrivateLicenseOrganizationOperationLocked(ctx, organization.ID, accountID, licensing.OperationNewMember, "accept_organization_invite"); err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "license_denied", map[string]any{"invite_id": invite.ID})
		return Membership{}, err
	}
	account, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "account_not_found", map[string]any{"invite_id": invite.ID})
		return Membership{}, fmt.Errorf("account was not found: %w", err)
	}
	account, err = s.refreshAccountSubscriptionLocked(ctx, account)
	if err != nil {
		return Membership{}, err
	}
	if err := ensureAccountActive(account); err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "account_inactive", map[string]any{"invite_id": invite.ID})
		return Membership{}, err
	}
	plan, decision, err := s.memberQuotaDecisionLocked(ctx, owner)
	if err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", "quota_unavailable", map[string]any{"invite_id": invite.ID})
		return Membership{}, err
	}
	now := s.nowTime()
	member := Membership{
		ID:             mustID("mem"),
		OrganizationID: organizationID,
		AccountID:      accountID,
		Role:           invite.Role,
		Status:         MembershipStatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	created, err := s.store.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteStoreRequest{
		OrganizationID: organizationID,
		AccountID:      accountID,
		TokenDigest:    tokenDigest,
		CodeDigest:     codeDigest,
		Member:         member,
		Now:            now,
		Quota:          decision,
		PlanID:         plan.ID,
	})
	if err != nil {
		_ = s.recordOrganizationAudit(ctx, AuditOrganizationInviteRejected, accountID, organizationID, accountID, "accept_organization_invite", organizationInviteAcceptFailureResult(err), map[string]any{
			"invite_id": invite.ID,
		})
		var quotaErr *QuotaError
		if errors.As(err, &quotaErr) {
			s.recordQuotaDeniedLocked(ctx, owner.ID, "", "", quotaErr)
		}
		return Membership{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationInviteAccepted, accountID, organizationID, accountID, "accept_organization_invite", "accepted", map[string]any{
		"invite_id": invite.ID,
		"role":      string(created.Role),
		"plan_id":   string(plan.ID),
	}); err != nil {
		return Membership{}, err
	}
	return created, nil
}

func (s *Service) SuspendOrganizationMember(ctx context.Context, req OrganizationMemberStatusRequest) (Membership, error) {
	return s.changeOrganizationMemberStatus(ctx, req, MembershipStatusSuspended)
}

func (s *Service) ResumeOrganizationMember(ctx context.Context, req OrganizationMemberStatusRequest) (Membership, error) {
	return s.changeOrganizationMemberStatus(ctx, req, MembershipStatusActive)
}

func (s *Service) RemoveOrganizationMember(ctx context.Context, req OrganizationMemberStatusRequest) (Membership, error) {
	return s.changeOrganizationMemberStatus(ctx, req, MembershipStatusRemoved)
}

func (s *Service) changeOrganizationMemberStatus(ctx context.Context, req OrganizationMemberStatusRequest, status MembershipStatus) (Membership, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionMemberManage, true)
	if err != nil {
		return Membership{}, err
	}
	targetID := strings.TrimSpace(req.AccountID)
	if targetID == "" {
		return Membership{}, fmt.Errorf("account_id is required")
	}
	member, err := s.store.GetOrganizationMembership(ctx, organization.ID, targetID)
	if err != nil {
		return Membership{}, fmt.Errorf("organization membership was not found: %w", err)
	}
	if actor.Role == MembershipRoleAdmin && member.Role == MembershipRoleOwner {
		return Membership{}, fmt.Errorf("admin cannot modify an owner: %w", ErrForbidden)
	}
	if actor.Role == MembershipRoleAdmin && member.AccountID == actor.AccountID && status != MembershipStatusActive {
		return Membership{}, fmt.Errorf("admin cannot disable their own membership: %w", ErrForbidden)
	}
	if status != MembershipStatusActive {
		if err := s.ensureMemberCanBeDisabledLocked(ctx, organization, member); err != nil {
			return Membership{}, err
		}
	}
	now := s.nowTime()
	event := AuditOrganizationMemberResumed
	action := "resume_organization_member"
	result := "resumed"
	switch status {
	case MembershipStatusSuspended:
		if member.Status == MembershipStatusRemoved {
			return Membership{}, fmt.Errorf("removed membership cannot be suspended: %w", ErrConflict)
		}
		if member.Status == MembershipStatusSuspended {
			return member, nil
		}
		member.Status = MembershipStatusSuspended
		member.SuspendedAt = &now
		event = AuditOrganizationMemberSuspended
		action = "suspend_organization_member"
		result = "suspended"
	case MembershipStatusActive:
		if member.Status == MembershipStatusRemoved {
			return Membership{}, fmt.Errorf("removed membership cannot be resumed: %w", ErrConflict)
		}
		if member.Status == MembershipStatusActive {
			return member, nil
		}
		member.Status = MembershipStatusActive
		member.SuspendedAt = nil
	case MembershipStatusRemoved:
		if member.Status == MembershipStatusRemoved {
			return member, nil
		}
		member.Status = MembershipStatusRemoved
		member.RemovedAt = &now
		event = AuditOrganizationMemberRemoved
		action = "remove_organization_member"
		result = "removed"
	default:
		return Membership{}, fmt.Errorf("membership status %q is not supported", status)
	}
	member.UpdatedAt = now
	updated, err := s.store.UpdateMembership(ctx, member)
	if err != nil {
		return Membership{}, err
	}
	if status == MembershipStatusRemoved {
		grants, listErr := s.store.ListConnectionGrants(ctx, organization.ID)
		if listErr != nil {
			return Membership{}, listErr
		}
		for _, grant := range grants {
			if grant.MemberAccountID == updated.AccountID {
				if _, deleteErr := s.store.DeleteConnectionGrant(ctx, organization.ID, grant.ID); deleteErr != nil {
					return Membership{}, deleteErr
				}
			}
		}
		devices, listErr := s.store.ListOrganizationDevices(ctx, organization.ID)
		if listErr != nil {
			return Membership{}, listErr
		}
		for _, device := range devices {
			if device.AccountID == updated.AccountID {
				if _, deleteErr := s.store.DeleteOrganizationDevice(ctx, organization.ID, device.DeviceID); deleteErr != nil {
					return Membership{}, deleteErr
				}
			}
		}
	}
	if err := s.recordOrganizationAudit(ctx, event, req.ActorAccountID, organization.ID, targetID, action, result, map[string]any{
		"role": string(updated.Role), "permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return Membership{}, err
	}
	return updated, nil
}

func (s *Service) memberQuotaDecisionLocked(ctx context.Context, owner Account) (Plan, QuotaDecision, error) {
	if strings.TrimSpace(string(owner.PlanID)) == "" {
		quotaErr := newQuotaError(QuotaDimensionMemberCount, 0, 0, PlanQuotaUnavailable, "", "member count quota unavailable")
		s.recordQuotaDeniedLocked(ctx, owner.ID, "", "", quotaErr)
		return Plan{}, QuotaDecision{}, quotaErr
	}
	plan, err := s.planForAccount(owner)
	if err != nil {
		return Plan{}, QuotaDecision{}, err
	}
	decision, err := s.evaluateQuotaLocked(ctx, owner.ID, QuotaDimensionMemberCount, plan.Entitlements.MemberCount)
	if err != nil {
		err = withQuotaPlan(err, plan.ID)
		s.recordQuotaDeniedLocked(ctx, owner.ID, "", "", err)
		return Plan{}, QuotaDecision{}, err
	}
	return plan, decision, nil
}

func (s *Service) activeOrganizationOwnerLocked(ctx context.Context, organization Organization) (Account, error) {
	owner, err := s.store.GetAccount(ctx, organization.OwnerAccountID)
	if err != nil {
		return Account{}, fmt.Errorf("organization owner account was not found: %w", err)
	}
	owner, err = s.refreshAccountSubscriptionLocked(ctx, owner)
	if err != nil {
		return Account{}, err
	}
	if err := ensureAccountActive(owner); err != nil {
		return Account{}, err
	}
	return owner, nil
}

func (s *Service) organizationForMutationLocked(ctx context.Context, organizationID string) (Organization, error) {
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return Organization{}, fmt.Errorf("organization_id is required")
	}
	organization, err := s.store.GetOrganization(ctx, organizationID)
	if err != nil {
		return Organization{}, fmt.Errorf("organization was not found: %w", err)
	}
	return organization, nil
}

func (s *Service) ensureMemberCanBeDisabledLocked(ctx context.Context, organization Organization, member Membership) error {
	if member.Role != MembershipRoleOwner {
		return nil
	}
	if member.AccountID == organization.OwnerAccountID {
		return fmt.Errorf("organization owner cannot be suspended or removed: %w", ErrForbidden)
	}
	members, err := s.store.ListOrganizationMemberships(ctx, organization.ID)
	if err != nil {
		return err
	}
	var activeOwners int
	for _, got := range members {
		if got.ID == member.ID || got.Status != MembershipStatusActive || got.Role != MembershipRoleOwner {
			continue
		}
		activeOwners++
	}
	if activeOwners == 0 {
		return fmt.Errorf("last organization owner cannot be suspended or removed: %w", ErrForbidden)
	}
	return nil
}

func ensureOrganizationActive(organization Organization) error {
	switch organization.Status {
	case "", OrganizationStatusActive:
		return nil
	case OrganizationStatusSuspended:
		return fmt.Errorf("organization is suspended: %w", ErrForbidden)
	default:
		return fmt.Errorf("organization status %q is not supported: %w", organization.Status, ErrForbidden)
	}
}

func normalizeMembershipRole(role MembershipRole) (MembershipRole, error) {
	switch MembershipRole(strings.TrimSpace(string(role))) {
	case "":
		return MembershipRoleMember, nil
	case MembershipRoleOwner:
		return MembershipRoleOwner, nil
	case MembershipRoleAdmin:
		return MembershipRoleAdmin, nil
	case MembershipRoleOperator:
		return MembershipRoleOperator, nil
	case MembershipRoleMember:
		return MembershipRoleMember, nil
	default:
		return "", fmt.Errorf("membership role %q is not supported", role)
	}
}

func organizationInviteResult(invite OrganizationInvite, token, code string) OrganizationInviteResult {
	return OrganizationInviteResult{
		ID:                 invite.ID,
		OrganizationID:     invite.OrganizationID,
		InvitedAccountID:   invite.InvitedAccountID,
		InvitedByAccountID: invite.InvitedByAccountID,
		Role:               invite.Role,
		Token:              token,
		Code:               code,
		CreatedAt:          invite.CreatedAt,
		ExpiresAt:          invite.ExpiresAt,
	}
}

func hashOrganizationInviteToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func hashOrganizationInviteCode(token, code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token) + ":" + strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

func verifyOrganizationInviteCode(invite OrganizationInvite, token, code string) bool {
	got := hashOrganizationInviteCode(token, code)
	return constantTimeStringEqual(got, invite.CodeDigest)
}

func constantTimeStringEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Service) recordOrganizationAudit(ctx context.Context, event, actorAccountID, organizationID, targetAccountID, action, result string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["organization_id"] = strings.TrimSpace(organizationID)
	if strings.TrimSpace(targetAccountID) != "" {
		metadata["target_account_id"] = strings.TrimSpace(targetAccountID)
	}
	if strings.TrimSpace(action) != "" {
		metadata["action"] = strings.TrimSpace(action)
	}
	if strings.TrimSpace(result) != "" {
		metadata["result"] = strings.TrimSpace(result)
	}
	return s.recordAudit(ctx, event, strings.TrimSpace(actorAccountID), "", "", metadata)
}

func organizationInviteAcceptFailureResult(err error) string {
	switch {
	case errors.Is(err, ErrQuotaExceeded):
		return "quota_exceeded"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrRevoked):
		return "revoked"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	default:
		return "rejected"
	}
}
