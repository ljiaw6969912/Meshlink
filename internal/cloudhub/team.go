package cloudhub

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Service) ChangeOrganizationMemberRole(ctx context.Context, req ChangeOrganizationMemberRoleRequest) (Membership, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	organization, _, actorMembership, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionMemberRoleChange, true)
	if err != nil {
		return Membership{}, err
	}
	targetID := strings.TrimSpace(req.AccountID)
	if targetID == "" {
		return Membership{}, fmt.Errorf("account_id is required")
	}
	target, err := s.store.GetOrganizationMembership(ctx, organization.ID, targetID)
	if err != nil || target.Status == MembershipStatusRemoved {
		return Membership{}, fmt.Errorf("organization membership was not found: %w", ErrNotFound)
	}
	role, err := normalizeMembershipRole(req.Role)
	if err != nil {
		return Membership{}, err
	}
	if target.AccountID == organization.OwnerAccountID && role != MembershipRoleOwner {
		return Membership{}, fmt.Errorf("primary organization owner cannot be demoted: %w", ErrForbidden)
	}
	if actorMembership.Role == MembershipRoleAdmin {
		if target.Role == MembershipRoleOwner || role == MembershipRoleOwner {
			return Membership{}, fmt.Errorf("admin cannot modify or grant owner role: %w", ErrForbidden)
		}
		if target.AccountID == actorMembership.AccountID && role != actorMembership.Role {
			return Membership{}, fmt.Errorf("admin cannot change their own role: %w", ErrForbidden)
		}
	}
	if target.Role == MembershipRoleOwner && role != MembershipRoleOwner {
		if err := s.ensureMemberCanLoseOwnerRoleLocked(ctx, organization, target); err != nil {
			return Membership{}, err
		}
	}
	if target.Role == role {
		return target, nil
	}
	oldRole := target.Role
	target.Role = role
	target.UpdatedAt = s.nowTime()
	updated, err := s.store.UpdateMembership(ctx, target)
	if err != nil {
		return Membership{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationMemberRoleChanged, actorMembership.AccountID, organization.ID, target.AccountID, "change_member_role", "changed", map[string]any{
		"old_role":          string(oldRole),
		"new_role":          string(role),
		"permission_source": string(managementPermissionSource(actorMembership.Role)),
	}); err != nil {
		return Membership{}, err
	}
	return updated, nil
}

func (s *Service) ensureMemberCanLoseOwnerRoleLocked(ctx context.Context, organization Organization, member Membership) error {
	if member.Role != MembershipRoleOwner {
		return nil
	}
	if member.AccountID == organization.OwnerAccountID {
		return fmt.Errorf("primary organization owner cannot be demoted: %w", ErrForbidden)
	}
	members, err := s.store.ListOrganizationMemberships(ctx, organization.ID)
	if err != nil {
		return err
	}
	activeOwners := 0
	for _, candidate := range members {
		if candidate.ID != member.ID && candidate.Status == MembershipStatusActive && candidate.Role == MembershipRoleOwner {
			activeOwners++
		}
	}
	if activeOwners == 0 {
		return fmt.Errorf("last organization owner cannot be demoted: %w", ErrForbidden)
	}
	return nil
}

func (s *Service) CreateDeviceGroup(ctx context.Context, req CreateDeviceGroupRequest) (DeviceGroup, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionGroupManage, true)
	if err != nil {
		return DeviceGroup{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return DeviceGroup{}, fmt.Errorf("device group name is required")
	}
	now := s.nowTime()
	created, err := s.store.CreateDeviceGroup(ctx, DeviceGroup{
		ID: mustID("grp"), OrganizationID: organization.ID, Name: name, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return DeviceGroup{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditDeviceGroupCreated, actor.AccountID, organization.ID, "", "create_device_group", "created", map[string]any{
		"group_id": created.ID, "permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return DeviceGroup{}, err
	}
	return created, nil
}

func (s *Service) GetDeviceGroup(ctx context.Context, actorAccountID, organizationID, groupID string) (DeviceGroup, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionOrganizationRead, true); err != nil {
		return DeviceGroup{}, err
	}
	group, err := s.store.GetDeviceGroup(ctx, strings.TrimSpace(organizationID), strings.TrimSpace(groupID))
	if err != nil || group.DeletedAt != nil {
		return DeviceGroup{}, fmt.Errorf("device group was not found: %w", ErrNotFound)
	}
	return group, nil
}

func (s *Service) ListDeviceGroups(ctx context.Context, actorAccountID, organizationID string) ([]DeviceGroup, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionOrganizationRead, true); err != nil {
		return nil, err
	}
	return s.store.ListDeviceGroups(ctx, strings.TrimSpace(organizationID))
}

func (s *Service) RenameDeviceGroup(ctx context.Context, req RenameDeviceGroupRequest) (DeviceGroup, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionGroupManage, true)
	if err != nil {
		return DeviceGroup{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return DeviceGroup{}, fmt.Errorf("device group name is required")
	}
	group, err := s.store.GetDeviceGroup(ctx, organization.ID, strings.TrimSpace(req.GroupID))
	if err != nil || group.DeletedAt != nil {
		return DeviceGroup{}, fmt.Errorf("device group was not found: %w", ErrNotFound)
	}
	if group.Name == name {
		return group, nil
	}
	group.Name = name
	group.UpdatedAt = s.nowTime()
	updated, err := s.store.UpdateDeviceGroup(ctx, group)
	if err != nil {
		return DeviceGroup{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditDeviceGroupRenamed, actor.AccountID, organization.ID, "", "rename_device_group", "renamed", map[string]any{
		"group_id": updated.ID, "permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return DeviceGroup{}, err
	}
	return updated, nil
}

func (s *Service) DeleteDeviceGroup(ctx context.Context, req DeleteDeviceGroupRequest) (DeviceGroup, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionGroupManage, true)
	if err != nil {
		return DeviceGroup{}, err
	}
	deleted, err := s.store.DeleteDeviceGroup(ctx, organization.ID, strings.TrimSpace(req.GroupID), s.nowTime())
	if err != nil {
		return DeviceGroup{}, fmt.Errorf("device group was not found: %w", err)
	}
	if err := s.recordOrganizationAudit(ctx, AuditDeviceGroupDeleted, actor.AccountID, organization.ID, "", "delete_device_group", "deleted", map[string]any{
		"group_id": deleted.ID, "permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return DeviceGroup{}, err
	}
	return deleted, nil
}

func (s *Service) EnrollOrganizationDevice(ctx context.Context, req EnrollOrganizationDeviceRequest) (OrganizationDevice, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionDeviceManage, true)
	if err != nil {
		return OrganizationDevice{}, err
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	device, err := s.store.GetDevice(ctx, deviceID)
	if err != nil {
		return OrganizationDevice{}, fmt.Errorf("device was not found: %w", err)
	}
	if device.Status == DeviceStatusRevoked || device.RevokedAt != nil {
		return OrganizationDevice{}, fmt.Errorf("device has been revoked: %w", ErrRevoked)
	}
	owner, err := s.store.GetAccount(ctx, device.AccountID)
	if err != nil || ensureAccountActive(owner) != nil {
		return OrganizationDevice{}, fmt.Errorf("device account is not active: %w", ErrForbidden)
	}
	membership, err := s.store.GetOrganizationMembership(ctx, organization.ID, device.AccountID)
	if err != nil || membership.Status != MembershipStatusActive {
		return OrganizationDevice{}, fmt.Errorf("device account is not an active organization member: %w", ErrForbidden)
	}
	now := s.nowTime()
	enrolled, err := s.store.CreateOrganizationDevice(ctx, OrganizationDevice{
		ID: mustID("orgdev"), OrganizationID: organization.ID, DeviceID: device.ID, AccountID: device.AccountID,
		EnrolledByAccountID: actor.AccountID, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return OrganizationDevice{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationDeviceEnrolled, actor.AccountID, organization.ID, device.AccountID, "enroll_organization_device", "enrolled", map[string]any{
		"target_device_id": device.ID, "permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return OrganizationDevice{}, err
	}
	return enrolled, nil
}

func (s *Service) RemoveOrganizationDevice(ctx context.Context, req RemoveOrganizationDeviceRequest) (OrganizationDevice, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionDeviceManage, true)
	if err != nil {
		return OrganizationDevice{}, err
	}
	removed, err := s.store.DeleteOrganizationDevice(ctx, organization.ID, strings.TrimSpace(req.DeviceID))
	if err != nil {
		return OrganizationDevice{}, fmt.Errorf("organization device was not found: %w", err)
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationDeviceRemoved, actor.AccountID, organization.ID, removed.AccountID, "remove_organization_device", "removed", map[string]any{
		"target_device_id": removed.DeviceID, "permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return OrganizationDevice{}, err
	}
	return removed, nil
}

func (s *Service) ListOrganizationDevices(ctx context.Context, actorAccountID, organizationID string) ([]OrganizationDevice, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionOrganizationRead, true); err != nil {
		return nil, err
	}
	return s.store.ListOrganizationDevices(ctx, strings.TrimSpace(organizationID))
}

func (s *Service) AddDeviceToGroup(ctx context.Context, req OrganizationDeviceGroupRequest) (OrganizationDevice, error) {
	return s.changeOrganizationDeviceGroup(ctx, req, true)
}

func (s *Service) RemoveDeviceFromGroup(ctx context.Context, req OrganizationDeviceGroupRequest) (OrganizationDevice, error) {
	return s.changeOrganizationDeviceGroup(ctx, req, false)
}

func (s *Service) changeOrganizationDeviceGroup(ctx context.Context, req OrganizationDeviceGroupRequest, add bool) (OrganizationDevice, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionGroupManage, true)
	if err != nil {
		return OrganizationDevice{}, err
	}
	groupID := strings.TrimSpace(req.GroupID)
	if add {
		group, err := s.store.GetDeviceGroup(ctx, organization.ID, groupID)
		if err != nil || group.DeletedAt != nil {
			return OrganizationDevice{}, fmt.Errorf("device group was not found: %w", ErrNotFound)
		}
	}
	device, err := s.store.GetOrganizationDevice(ctx, organization.ID, strings.TrimSpace(req.DeviceID))
	if err != nil {
		return OrganizationDevice{}, fmt.Errorf("organization device was not found: %w", ErrNotFound)
	}
	if add && device.GroupID == groupID {
		return device, nil
	}
	if !add {
		if device.GroupID == "" {
			return device, nil
		}
		if groupID != "" && device.GroupID != groupID {
			return OrganizationDevice{}, fmt.Errorf("device is not in requested group: %w", ErrConflict)
		}
		groupID = ""
	}
	device.GroupID = groupID
	device.UpdatedAt = s.nowTime()
	updated, err := s.store.UpdateOrganizationDevice(ctx, device)
	if err != nil {
		return OrganizationDevice{}, err
	}
	action, result, event := "add_device_to_group", "added", AuditOrganizationDeviceGrouped
	if !add {
		action, result, event = "remove_device_from_group", "removed", AuditOrganizationDeviceUngrouped
	}
	if err := s.recordOrganizationAudit(ctx, event, actor.AccountID, organization.ID, updated.AccountID, action, result, map[string]any{
		"target_device_id": updated.DeviceID, "group_id": req.GroupID,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return OrganizationDevice{}, err
	}
	return updated, nil
}

func (s *Service) GrantConnectionAccess(ctx context.Context, req ConnectionGrantRequest) (ConnectionGrant, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionConnectionGrantManage, true)
	if err != nil {
		return ConnectionGrant{}, err
	}
	memberID := strings.TrimSpace(req.MemberAccountID)
	membership, err := s.store.GetOrganizationMembership(ctx, organization.ID, memberID)
	if err != nil || membership.Status != MembershipStatusActive {
		return ConnectionGrant{}, fmt.Errorf("grant member is not active: %w", ErrForbidden)
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	groupID := strings.TrimSpace(req.GroupID)
	if (deviceID == "") == (groupID == "") {
		return ConnectionGrant{}, fmt.Errorf("exactly one of device_id or group_id is required")
	}
	grant := ConnectionGrant{
		ID: mustID("grant"), OrganizationID: organization.ID, MemberAccountID: memberID,
		GrantedByAccountID: actor.AccountID, CreatedAt: s.nowTime(),
	}
	if deviceID != "" {
		if _, err := s.store.GetOrganizationDevice(ctx, organization.ID, deviceID); err != nil {
			return ConnectionGrant{}, fmt.Errorf("organization device was not found: %w", ErrNotFound)
		}
		grant.Scope = ConnectionGrantDevice
		grant.DeviceID = deviceID
	} else {
		group, err := s.store.GetDeviceGroup(ctx, organization.ID, groupID)
		if err != nil || group.DeletedAt != nil {
			return ConnectionGrant{}, fmt.Errorf("device group was not found: %w", ErrNotFound)
		}
		grant.Scope = ConnectionGrantGroup
		grant.GroupID = groupID
	}
	created, err := s.store.CreateConnectionGrant(ctx, grant)
	if err != nil {
		return ConnectionGrant{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditConnectionGrantCreated, actor.AccountID, organization.ID, memberID, "grant_connect", "granted", map[string]any{
		"grant_id": created.ID, "target_device_id": created.DeviceID, "group_id": created.GroupID,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return ConnectionGrant{}, err
	}
	return created, nil
}

func (s *Service) RevokeConnectionAccess(ctx context.Context, req RevokeConnectionGrantRequest) (ConnectionGrant, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionConnectionGrantManage, true)
	if err != nil {
		return ConnectionGrant{}, err
	}
	revoked, err := s.store.DeleteConnectionGrant(ctx, organization.ID, strings.TrimSpace(req.GrantID))
	if err != nil {
		return ConnectionGrant{}, fmt.Errorf("connection grant was not found: %w", err)
	}
	if err := s.recordOrganizationAudit(ctx, AuditConnectionGrantRevoked, actor.AccountID, organization.ID, revoked.MemberAccountID, "revoke_connect", "revoked", map[string]any{
		"grant_id": revoked.ID, "target_device_id": revoked.DeviceID, "group_id": revoked.GroupID,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return ConnectionGrant{}, err
	}
	return revoked, nil
}

func (s *Service) ListConnectionGrants(ctx context.Context, actorAccountID, organizationID string) ([]ConnectionGrant, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionOrganizationRead, true); err != nil {
		return nil, err
	}
	return s.store.ListConnectionGrants(ctx, strings.TrimSpace(organizationID))
}

func (s *Service) AuthorizeConnection(ctx context.Context, sourceDeviceID, targetDeviceID string) (ConnectionAuthorization, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.authorizeConnectionLocked(ctx, sourceDeviceID, targetDeviceID)
}

func (s *Service) authorizeConnectionLocked(ctx context.Context, sourceDeviceID, targetDeviceID string) (ConnectionAuthorization, error) {
	sourceDeviceID = strings.TrimSpace(sourceDeviceID)
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	decision := ConnectionAuthorization{
		SourceDeviceID: sourceDeviceID, TargetDeviceID: targetDeviceID, PermissionSource: ConnectionPermissionDeny,
	}
	source, err := s.store.GetDevice(ctx, sourceDeviceID)
	if err != nil {
		return decision, fmt.Errorf("source device was not found: %w", err)
	}
	target, err := s.store.GetDevice(ctx, targetDeviceID)
	if err != nil {
		return decision, fmt.Errorf("target device was not found: %w", err)
	}
	if source.Status == DeviceStatusRevoked || source.RevokedAt != nil {
		return decision, fmt.Errorf("source device has been revoked: %w", ErrRevoked)
	}
	if target.Status == DeviceStatusRevoked || target.RevokedAt != nil {
		return decision, fmt.Errorf("target device has been revoked: %w", ErrRevoked)
	}
	organizationDevice, err := s.store.FindOrganizationDevice(ctx, target.ID)
	if errors.Is(err, ErrNotFound) {
		if source.AccountID == target.AccountID && source.NetworkID == target.NetworkID {
			decision.Allowed = true
			decision.MemberAccountID = source.AccountID
			decision.PermissionSource = ConnectionPermissionLegacy
			return decision, nil
		}
		return decision, fmt.Errorf("devices do not share a legacy account and network: %w", ErrForbidden)
	}
	if err != nil {
		return decision, err
	}
	decision.OrganizationID = organizationDevice.OrganizationID
	decision.MemberAccountID = source.AccountID
	decision.GroupID = organizationDevice.GroupID
	deny := func(kind error) (ConnectionAuthorization, error) {
		_ = s.recordConnectionAuthorizationAudit(ctx, AuditOrganizationConnectionDenied, decision, "denied")
		return decision, kind
	}
	organization, err := s.store.GetOrganization(ctx, organizationDevice.OrganizationID)
	if err != nil {
		return deny(fmt.Errorf("organization was not found: %w", ErrForbidden))
	}
	if err := ensureOrganizationActive(organization); err != nil {
		return deny(err)
	}
	if organizationDevice.AccountID != target.AccountID {
		return deny(fmt.Errorf("organization device owner mismatch: %w", ErrForbidden))
	}
	for _, deviceAccountID := range []string{source.AccountID, target.AccountID} {
		account, err := s.store.GetAccount(ctx, deviceAccountID)
		if err != nil || ensureAccountActive(account) != nil {
			return deny(fmt.Errorf("device account is not active: %w", ErrForbidden))
		}
		membership, err := s.store.GetOrganizationMembership(ctx, organization.ID, deviceAccountID)
		if err != nil || membership.Status != MembershipStatusActive {
			return deny(fmt.Errorf("device account membership is not active: %w", ErrForbidden))
		}
	}
	grants, err := s.store.ListConnectionGrants(ctx, organization.ID)
	if err != nil {
		return decision, err
	}
	for _, grant := range grants {
		if grant.MemberAccountID == source.AccountID && grant.Scope == ConnectionGrantDevice && grant.DeviceID == target.ID {
			decision.Allowed = true
			decision.PermissionSource = ConnectionPermissionDeviceGrant
			_ = s.recordConnectionAuthorizationAudit(ctx, AuditOrganizationConnectionAuthorized, decision, "allowed")
			return decision, nil
		}
	}
	if organizationDevice.GroupID != "" {
		group, err := s.store.GetDeviceGroup(ctx, organization.ID, organizationDevice.GroupID)
		if err == nil && group.DeletedAt == nil {
			for _, grant := range grants {
				if grant.MemberAccountID == source.AccountID && grant.Scope == ConnectionGrantGroup && grant.GroupID == organizationDevice.GroupID {
					decision.Allowed = true
					decision.PermissionSource = ConnectionPermissionGroupGrant
					_ = s.recordConnectionAuthorizationAudit(ctx, AuditOrganizationConnectionAuthorized, decision, "allowed")
					return decision, nil
				}
			}
		}
	}
	return deny(fmt.Errorf("connect permission was not granted: %w", ErrForbidden))
}

func (s *Service) recordConnectionAuthorizationAudit(ctx context.Context, event string, decision ConnectionAuthorization, result string) error {
	return s.recordOrganizationAudit(ctx, event, decision.MemberAccountID, decision.OrganizationID, decision.MemberAccountID, "authorize_connection", result, map[string]any{
		"member_account_id": decision.MemberAccountID,
		"source_device_id":  decision.SourceDeviceID,
		"target_device_id":  decision.TargetDeviceID,
		"group_id":          decision.GroupID,
		"permission_source": string(decision.PermissionSource),
	})
}
