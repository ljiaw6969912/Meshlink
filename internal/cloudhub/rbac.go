package cloudhub

import (
	"context"
	"fmt"
	"strings"
)

type organizationPermission string

const (
	permissionOrganizationRead      organizationPermission = "organization_read"
	permissionOrganizationManage    organizationPermission = "organization_manage"
	permissionMemberInvite          organizationPermission = "member_invite"
	permissionMemberManage          organizationPermission = "member_manage"
	permissionMemberRoleChange      organizationPermission = "member_role_change"
	permissionDeviceManage          organizationPermission = "device_manage"
	permissionGroupManage           organizationPermission = "group_manage"
	permissionConnectionGrantManage organizationPermission = "connection_grant_manage"
	permissionAuditRead             organizationPermission = "audit_read"
	permissionAuditMaintain         organizationPermission = "audit_maintain"
	permissionDeploymentManage      organizationPermission = "deployment_manage"
	permissionRolloutManage         organizationPermission = "rollout_manage"
	permissionPrivateLicenseManage  organizationPermission = "private_license_manage"
)

func (s *Service) authorizeOrganizationLocked(ctx context.Context, organizationID, actorAccountID string, permission organizationPermission, requireActive bool) (Organization, Account, Membership, error) {
	organizationID = strings.TrimSpace(organizationID)
	actorAccountID = strings.TrimSpace(actorAccountID)
	if organizationID == "" {
		return Organization{}, Account{}, Membership{}, fmt.Errorf("organization_id is required")
	}
	if actorAccountID == "" {
		return Organization{}, Account{}, Membership{}, fmt.Errorf("actor identity is required: %w", ErrForbidden)
	}
	organization, err := s.store.GetOrganization(ctx, organizationID)
	if err != nil {
		return Organization{}, Account{}, Membership{}, fmt.Errorf("organization is not available to actor: %w", ErrForbidden)
	}
	actor, err := s.store.GetAccount(ctx, actorAccountID)
	if err != nil {
		s.recordOrganizationAuthorizationDenied(ctx, actorAccountID, organizationID, permission, ErrForbidden)
		return Organization{}, Account{}, Membership{}, fmt.Errorf("actor account was not found: %w", ErrForbidden)
	}
	actor, err = s.refreshAccountSubscriptionLocked(ctx, actor)
	if err != nil {
		return Organization{}, Account{}, Membership{}, err
	}
	if err := ensureAccountActive(actor); err != nil {
		s.recordOrganizationAuthorizationDenied(ctx, actor.ID, organizationID, permission, err)
		return Organization{}, Account{}, Membership{}, err
	}
	membership, err := s.store.GetOrganizationMembership(ctx, organizationID, actor.ID)
	if err != nil || membership.Status != MembershipStatusActive {
		s.recordOrganizationAuthorizationDenied(ctx, actor.ID, organizationID, permission, ErrForbidden)
		return Organization{}, Account{}, Membership{}, fmt.Errorf("actor organization membership is not active: %w", ErrForbidden)
	}
	if requireActive {
		if err := ensureOrganizationActive(organization); err != nil {
			s.recordOrganizationAuthorizationDenied(ctx, actor.ID, organizationID, permission, err)
			return Organization{}, Account{}, Membership{}, err
		}
	}
	allowed := false
	switch permission {
	case permissionOrganizationRead:
		switch membership.Role {
		case MembershipRoleOwner, MembershipRoleAdmin, MembershipRoleOperator, MembershipRoleMember:
			allowed = true
		default:
			allowed = false
		}
	case permissionOrganizationManage:
		allowed = membership.Role == MembershipRoleOwner
	case permissionMemberInvite, permissionMemberManage, permissionMemberRoleChange,
		permissionDeviceManage, permissionGroupManage, permissionConnectionGrantManage,
		permissionAuditRead, permissionAuditMaintain, permissionDeploymentManage, permissionRolloutManage, permissionPrivateLicenseManage:
		allowed = membership.Role == MembershipRoleOwner || membership.Role == MembershipRoleAdmin
	default:
		allowed = false
	}
	if !allowed {
		s.recordOrganizationAuthorizationDenied(ctx, actor.ID, organizationID, permission, ErrForbidden)
		return Organization{}, Account{}, Membership{}, fmt.Errorf("organization role %q lacks %s permission: %w", membership.Role, permission, ErrForbidden)
	}
	return organization, actor, membership, nil
}

func (s *Service) recordOrganizationAuthorizationDenied(ctx context.Context, actorAccountID, organizationID string, permission organizationPermission, cause error) {
	_ = s.recordOrganizationAudit(ctx, AuditOrganizationAuthorizationDenied, actorAccountID, organizationID, "", string(permission), "denied", map[string]any{
		"permission_source": string(ConnectionPermissionDeny),
		"error_kind":        organizationAuthorizationErrorKind(cause),
	})
}

func organizationAuthorizationErrorKind(err error) string {
	switch {
	case err == nil:
		return "denied"
	case strings.Contains(strings.ToLower(err.Error()), "suspend"):
		return "suspended"
	default:
		return "forbidden"
	}
}

func managementPermissionSource(role MembershipRole) ConnectionPermissionSource {
	if role == MembershipRoleOwner {
		return ConnectionPermissionOwner
	}
	if role == MembershipRoleAdmin {
		return ConnectionPermissionAdmin
	}
	return ConnectionPermissionDeny
}
