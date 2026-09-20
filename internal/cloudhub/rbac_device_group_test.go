package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

type task10BFixture struct {
	ctx      context.Context
	now      time.Time
	store    *MemoryStore
	svc      *Service
	org      Organization
	owner    Account
	admin    Account
	operator Account
	member   Account
}

func newTask10BFixture(t *testing.T) task10BFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.team.members":                            20,
			"cloudhub.plans.personal.official_relay_bytes_per_month": 1024 * 1024,
		}),
	)
	createAccount := func(email string, plan PlanID) Account {
		account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: email, PlanID: plan})
		if err != nil {
			t.Fatalf("CreateAccount(%s): %v", email, err)
		}
		return account
	}
	owner := createAccount("owner-10b@example.com", PlanTeam)
	admin := createAccount("admin-10b@example.com", PlanPersonal)
	operator := createAccount("operator-10b@example.com", PlanPersonal)
	member := createAccount("member-10b@example.com", PlanPersonal)
	org, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Task 10B Team"})
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	for _, item := range []struct {
		account Account
		role    MembershipRole
	}{
		{admin, MembershipRoleAdmin},
		{operator, MembershipRoleOperator},
		{member, MembershipRoleMember},
	} {
		_, err := store.CreateMembership(ctx, Membership{
			ID: mustID("mem"), OrganizationID: org.ID, AccountID: item.account.ID,
			Role: item.role, Status: MembershipStatusActive, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("CreateMembership(%s): %v", item.role, err)
		}
	}
	return task10BFixture{ctx: ctx, now: now, store: store, svc: svc, org: org, owner: owner, admin: admin, operator: operator, member: member}
}

func (f task10BFixture) device(t *testing.T, account Account, name string) (Network, Device) {
	t.Helper()
	network, err := f.svc.CreateNetwork(f.ctx, CreateNetworkRequest{AccountID: account.ID, Name: name + " network"})
	if err != nil {
		t.Fatalf("CreateNetwork(%s): %v", name, err)
	}
	device, err := f.store.CreateDevice(f.ctx, Device{
		ID: mustID("dev"), AccountID: account.ID, NetworkID: network.ID,
		Name: name, Status: DeviceStatusOnline, JoinedAt: f.now,
	})
	if err != nil {
		t.Fatalf("CreateDevice(%s): %v", name, err)
	}
	return network, device
}

func TestTask10BRBACRoleMatrixAndOwnerInvariants(t *testing.T) {
	f := newTask10BFixture(t)

	for _, tc := range []struct {
		name    string
		actor   Account
		allowed bool
	}{
		{"owner", f.owner, true},
		{"admin", f.admin, true},
		{"operator", f.operator, false},
		{"member", f.member, false},
	} {
		t.Run(tc.name+" group management", func(t *testing.T) {
			_, err := f.svc.CreateDeviceGroup(f.ctx, CreateDeviceGroupRequest{
				ActorAccountID: tc.actor.ID, OrganizationID: f.org.ID, Name: "group-" + tc.name,
			})
			if tc.allowed && err != nil {
				t.Fatalf("CreateDeviceGroup: %v", err)
			}
			if !tc.allowed && (err == nil || !errors.Is(err, ErrForbidden)) {
				t.Fatalf("CreateDeviceGroup error = %v, want forbidden", err)
			}
		})
	}

	for _, tc := range []struct {
		name    string
		actor   Account
		allowed bool
	}{
		{"owner", f.owner, true},
		{"admin", f.admin, true},
		{"operator", f.operator, false},
		{"member", f.member, false},
	} {
		t.Run(tc.name+" member invitation", func(t *testing.T) {
			invited, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{
				Email: tc.name + "-invite-10b@example.com", PlanID: PlanPersonal,
			})
			if err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			_, err = f.svc.CreateOrganizationInvite(f.ctx, CreateOrganizationInviteRequest{
				ActorAccountID: tc.actor.ID, OrganizationID: f.org.ID,
				InvitedAccountID: invited.ID, Role: MembershipRoleMember,
			})
			if tc.allowed && err != nil {
				t.Fatalf("CreateOrganizationInvite: %v", err)
			}
			if !tc.allowed && (err == nil || !errors.Is(err, ErrForbidden)) {
				t.Fatalf("CreateOrganizationInvite error = %v, want forbidden", err)
			}
		})
	}
	ownerInviteTarget, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: "admin-owner-invite-10b@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount owner invite target: %v", err)
	}
	if _, err := f.svc.CreateOrganizationInvite(f.ctx, CreateOrganizationInviteRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID,
		InvitedAccountID: ownerInviteTarget.ID, Role: MembershipRoleOwner,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin owner invitation error = %v, want forbidden", err)
	}

	changed, err := f.svc.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID,
		AccountID: f.member.ID, Role: MembershipRoleOperator,
	})
	if err != nil || changed.Role != MembershipRoleOperator {
		t.Fatalf("admin ChangeOrganizationMemberRole = %+v, %v", changed, err)
	}
	if _, err := f.svc.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID,
		AccountID: f.member.ID, Role: MembershipRoleOwner,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin owner promotion error = %v, want forbidden", err)
	}
	if _, err := f.svc.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID,
		AccountID: f.owner.ID, Role: MembershipRoleMember,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin owner mutation error = %v, want forbidden", err)
	}
	if _, err := f.svc.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		ActorAccountID: f.operator.ID, OrganizationID: f.org.ID,
		AccountID: f.operator.ID, Role: MembershipRoleAdmin,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator self elevation error = %v, want forbidden", err)
	}
	if _, err := f.svc.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		ActorAccountID: f.member.ID, OrganizationID: f.org.ID,
		AccountID: f.member.ID, Role: MembershipRoleAdmin,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("member self elevation error = %v, want forbidden", err)
	}
	if _, err := f.svc.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID,
		AccountID: f.admin.ID, Role: MembershipRoleMember,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin self role change error = %v, want forbidden", err)
	}
	if _, err := f.svc.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
		AccountID: f.owner.ID, Role: MembershipRoleAdmin,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("primary owner demotion error = %v, want forbidden", err)
	}

	other := newTask10BFixture(t)
	if _, err := f.svc.CreateDeviceGroup(f.ctx, CreateDeviceGroupRequest{
		ActorAccountID: f.owner.ID, OrganizationID: other.org.ID, Name: "idor",
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross organization group creation error = %v, want forbidden", err)
	}
	if _, err := f.svc.SuspendOrganization(f.ctx, OrganizationStatusChangeRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin organization suspend error = %v, want forbidden", err)
	}
	unknownRoleAccount, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: "unknown-role-10b@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount unknown role: %v", err)
	}
	if _, err := f.store.CreateMembership(f.ctx, Membership{
		ID: mustID("mem"), OrganizationID: f.org.ID, AccountID: unknownRoleAccount.ID,
		Role: MembershipRole("unknown"), Status: MembershipStatusActive, CreatedAt: f.now, UpdatedAt: f.now,
	}); err != nil {
		t.Fatalf("CreateMembership unknown role: %v", err)
	}
	if _, err := f.svc.ListDeviceGroups(f.ctx, unknownRoleAccount.ID, f.org.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("unknown role read error = %v, want default-deny forbidden", err)
	}
}

func TestTask10BDeviceGroupGrantLifecycleCleanupAndStatuses(t *testing.T) {
	f := newTask10BFixture(t)
	_, source := f.device(t, f.member, "member-laptop")
	_, target := f.device(t, f.owner, "owner-server")
	outsider, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: "outsider-10b@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount outsider: %v", err)
	}
	_, outsiderDevice := f.device(t, outsider, "outsider-device")
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, DeviceID: outsiderDevice.ID,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider device enrollment error = %v, want forbidden", err)
	}
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.member.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("member device enrollment error = %v, want forbidden", err)
	}

	enrolled, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	})
	if err != nil || enrolled.DeviceID != target.ID || enrolled.ID == "" {
		t.Fatalf("EnrollOrganizationDevice = %+v, %v", enrolled, err)
	}
	if again, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err != nil || again.ID != enrolled.ID {
		t.Fatalf("idempotent EnrollOrganizationDevice = %+v, %v", again, err)
	}
	group, err := f.svc.CreateDeviceGroup(f.ctx, CreateDeviceGroupRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, Name: "Production",
	})
	if err != nil {
		t.Fatalf("CreateDeviceGroup: %v", err)
	}
	if _, err := f.svc.CreateDeviceGroup(f.ctx, CreateDeviceGroupRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, Name: " production ",
	}); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate group error = %v, want conflict", err)
	}
	gotGroup, err := f.svc.GetDeviceGroup(f.ctx, f.operator.ID, f.org.ID, group.ID)
	if err != nil || gotGroup.ID != group.ID {
		t.Fatalf("GetDeviceGroup = %+v, %v", gotGroup, err)
	}
	group, err = f.svc.RenameDeviceGroup(f.ctx, RenameDeviceGroupRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, GroupID: group.ID, Name: "Critical Production",
	})
	if err != nil || group.Name != "Critical Production" {
		t.Fatalf("RenameDeviceGroup = %+v, %v", group, err)
	}
	assigned, err := f.svc.AddDeviceToGroup(f.ctx, OrganizationDeviceGroupRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, GroupID: group.ID, DeviceID: target.ID,
	})
	if err != nil || assigned.GroupID != group.ID {
		t.Fatalf("AddDeviceToGroup = %+v, %v", assigned, err)
	}
	grant, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID,
		MemberAccountID: f.member.ID, GroupID: group.ID,
	})
	if err != nil || grant.ID == "" {
		t.Fatalf("GrantConnectionAccess(group) = %+v, %v", grant, err)
	}
	auth, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID)
	if err != nil || !auth.Allowed || auth.PermissionSource != ConnectionPermissionGroupGrant {
		t.Fatalf("AuthorizeConnection(group) = %+v, %v", auth, err)
	}
	if _, err := f.svc.RemoveDeviceFromGroup(f.ctx, OrganizationDeviceGroupRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, GroupID: group.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("RemoveDeviceFromGroup: %v", err)
	}
	if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("authorization after ungroup error = %v, want forbidden", err)
	}
	if _, err := f.svc.AddDeviceToGroup(f.ctx, OrganizationDeviceGroupRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, GroupID: group.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("AddDeviceToGroup after ungroup: %v", err)
	}

	otherOwner, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: "other-owner-10b@example.com", PlanID: PlanTeam})
	if err != nil {
		t.Fatalf("CreateAccount other owner: %v", err)
	}
	otherOrganization, err := f.svc.CreateOrganization(f.ctx, CreateOrganizationRequest{OwnerAccountID: otherOwner.ID, Name: "Other Task 10B Team"})
	if err != nil {
		t.Fatalf("CreateOrganization other: %v", err)
	}
	if _, err := f.svc.SuspendOrganization(f.ctx, OrganizationStatusChangeRequest{
		ActorAccountID: otherOwner.ID, OrganizationID: otherOrganization.ID, Reason: "private organization state",
	}); err != nil {
		t.Fatalf("SuspendOrganization other: %v", err)
	}
	if _, err := f.svc.ListDeviceGroups(f.ctx, f.admin.ID, otherOrganization.ID); err == nil || !errors.Is(err, ErrForbidden) || strings.Contains(strings.ToLower(err.Error()), "suspend") {
		t.Fatalf("cross organization suspended state leaked through error = %v", err)
	}
	if _, err := f.svc.ResumeOrganization(f.ctx, OrganizationStatusChangeRequest{
		ActorAccountID: otherOwner.ID, OrganizationID: otherOrganization.ID,
	}); err != nil {
		t.Fatalf("ResumeOrganization other: %v", err)
	}
	if _, err := f.svc.GetDeviceGroup(f.ctx, f.admin.ID, otherOrganization.ID, group.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross organization group read error = %v, want forbidden", err)
	}
	if _, err := f.store.CreateMembership(f.ctx, Membership{
		ID: mustID("mem"), OrganizationID: otherOrganization.ID, AccountID: f.owner.ID,
		Role: MembershipRoleMember, Status: MembershipStatusActive, CreatedAt: f.now, UpdatedAt: f.now,
	}); err != nil {
		t.Fatalf("CreateMembership other organization: %v", err)
	}
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: otherOwner.ID, OrganizationID: otherOrganization.ID, DeviceID: target.ID,
	}); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("cross organization device enrollment error = %v, want conflict", err)
	}

	deviceGrant, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
		MemberAccountID: f.member.ID, DeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("GrantConnectionAccess(device): %v", err)
	}
	auth, err = f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID)
	if err != nil || auth.PermissionSource != ConnectionPermissionDeviceGrant {
		t.Fatalf("AuthorizeConnection(device priority) = %+v, %v", auth, err)
	}
	if _, err := f.svc.RevokeConnectionAccess(f.ctx, RevokeConnectionGrantRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, GrantID: deviceGrant.ID,
	}); err != nil {
		t.Fatalf("RevokeConnectionAccess(device): %v", err)
	}
	auth, err = f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID)
	if err != nil || auth.PermissionSource != ConnectionPermissionGroupGrant {
		t.Fatalf("AuthorizeConnection(group fallback) = %+v, %v", auth, err)
	}
	if _, err := f.svc.DeleteDeviceGroup(f.ctx, DeleteDeviceGroupRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, GroupID: group.ID,
	}); err != nil {
		t.Fatalf("DeleteDeviceGroup: %v", err)
	}
	if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("authorization after group deletion error = %v, want forbidden", err)
	}
	grants, err := f.svc.ListConnectionGrants(f.ctx, f.admin.ID, f.org.ID)
	if err != nil || len(grants) != 0 {
		t.Fatalf("ListConnectionGrants after cleanup = %+v, %v", grants, err)
	}
	devices, err := f.svc.ListOrganizationDevices(f.ctx, f.admin.ID, f.org.ID)
	if err != nil || len(devices) != 1 || devices[0].GroupID != "" {
		t.Fatalf("ListOrganizationDevices after group cleanup = %+v, %v", devices, err)
	}
	directGrant, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
		MemberAccountID: f.member.ID, DeviceID: target.ID,
	})
	if err != nil || directGrant.ID == "" {
		t.Fatalf("GrantConnectionAccess before device removal = %+v, %v", directGrant, err)
	}
	if _, err := f.svc.RemoveOrganizationDevice(f.ctx, RemoveOrganizationDeviceRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("RemoveOrganizationDevice: %v", err)
	}
	if grants, err := f.svc.ListConnectionGrants(f.ctx, f.admin.ID, f.org.ID); err != nil || len(grants) != 0 {
		t.Fatalf("ListConnectionGrants after device removal = %+v, %v", grants, err)
	}
	if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("authorization after device removal error = %v, want forbidden", err)
	}
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("re-enroll organization device: %v", err)
	}
	if _, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
		MemberAccountID: f.member.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("GrantConnectionAccess for status tests: %v", err)
	}
	if _, err := f.svc.SuspendOrganization(f.ctx, OrganizationStatusChangeRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, Reason: "status test",
	}); err != nil {
		t.Fatalf("SuspendOrganization: %v", err)
	}
	if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended organization authorization error = %v, want forbidden", err)
	}
	if _, err := f.svc.ResumeOrganization(f.ctx, OrganizationStatusChangeRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
	}); err != nil {
		t.Fatalf("ResumeOrganization: %v", err)
	}

	member, _ := f.store.GetOrganizationMembership(f.ctx, f.org.ID, f.member.ID)
	member.Status = MembershipStatusSuspended
	_, _ = f.store.UpdateMembership(f.ctx, member)
	if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended member authorization error = %v, want forbidden", err)
	}
	member.Status = MembershipStatusActive
	_, _ = f.store.UpdateMembership(f.ctx, member)
	member.Status = MembershipStatusRemoved
	_, _ = f.store.UpdateMembership(f.ctx, member)
	if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed member authorization error = %v, want forbidden", err)
	}
	member.Status = MembershipStatusActive
	_, _ = f.store.UpdateMembership(f.ctx, member)
	target.Status = DeviceStatusRevoked
	now := f.now
	target.RevokedAt = &now
	_, _ = f.store.UpdateDevice(f.ctx, target)
	if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked target authorization error = %v, want revoked", err)
	}
}

func TestTask10BConnectionGateBeforeCandidatesDirectAndRelay(t *testing.T) {
	f := newTask10BFixture(t)
	sourceNetwork, source := f.device(t, f.member, "source")
	targetNetwork, target := f.device(t, f.owner, "target")
	for _, item := range []struct {
		account Account
		network Network
		device  Device
		addr    string
	}{
		{f.member, sourceNetwork, source, "10.0.0.10"},
		{f.owner, targetNetwork, target, "10.0.0.20"},
	} {
		_, err := f.svc.RegisterP2PCandidates(f.ctx, RegisterP2PCandidatesRequest{
			AccountID: item.account.ID, NetworkID: item.network.ID, DeviceID: item.device.ID,
			Candidates: []p2p.Candidate{{Address: item.addr, Port: 8443, Scope: p2p.CandidateScopeLAN}},
		})
		if err != nil {
			t.Fatalf("RegisterP2PCandidates(%s): %v", item.device.Name, err)
		}
	}
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("EnrollOrganizationDevice: %v", err)
	}

	query := QueryP2PCandidatesRequest{
		AccountID: f.member.ID, NetworkID: sourceNetwork.ID,
		RequestingDeviceID: source.ID, TargetDeviceID: target.ID,
	}
	negotiate := NegotiateP2PConnectionRequest{
		AccountID: f.member.ID, NetworkID: sourceNetwork.ID,
		SourceDeviceID: source.ID, TargetDeviceID: target.ID,
	}
	relay := CreateRelaySessionRequest{
		AccountID: f.member.ID, NetworkID: sourceNetwork.ID,
		SourceDeviceID: source.ID, TargetDeviceID: target.ID,
	}
	if got, err := f.svc.QueryP2PCandidates(f.ctx, query); err == nil || !errors.Is(err, ErrForbidden) || len(got) != 0 {
		t.Fatalf("unauthorized QueryP2PCandidates = %+v, %v", got, err)
	}
	if got, err := f.svc.NegotiateP2PConnection(f.ctx, negotiate); err == nil || !errors.Is(err, ErrForbidden) || len(got.CandidatePairs) != 0 {
		t.Fatalf("unauthorized NegotiateP2PConnection = %+v, %v", got, err)
	}
	if got, err := f.svc.CreateRelaySession(f.ctx, relay); err == nil || !errors.Is(err, ErrForbidden) || got.Session.ID != "" {
		t.Fatalf("unauthorized CreateRelaySession = %+v, %v", got, err)
	}

	grant, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
		MemberAccountID: f.member.ID, DeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("GrantConnectionAccess: %v", err)
	}
	candidates, err := f.svc.QueryP2PCandidates(f.ctx, query)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("authorized QueryP2PCandidates = %+v, %v", candidates, err)
	}
	negotiation, err := f.svc.NegotiateP2PConnection(f.ctx, negotiate)
	if err != nil || len(negotiation.CandidatePairs) == 0 {
		t.Fatalf("authorized NegotiateP2PConnection = %+v, %v", negotiation, err)
	}
	relayResult, err := f.svc.CreateRelaySession(f.ctx, relay)
	if err != nil || relayResult.Session.ID == "" {
		t.Fatalf("authorized CreateRelaySession = %+v, %v", relayResult, err)
	}
	if _, err := f.svc.RevokeConnectionAccess(f.ctx, RevokeConnectionGrantRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, GrantID: grant.ID,
	}); err != nil {
		t.Fatalf("RevokeConnectionAccess: %v", err)
	}
	if _, err := f.svc.NegotiateP2PConnection(f.ctx, negotiate); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("negotiation after revoke error = %v, want forbidden", err)
	}
	if _, err := f.svc.CreateRelaySession(f.ctx, relay); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("relay after revoke error = %v, want forbidden", err)
	}

	events, err := f.svc.ListAuditEvents(f.ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	var sources = map[string]bool{}
	for _, event := range events {
		if event.Event != AuditOrganizationConnectionAuthorized && event.Event != AuditOrganizationConnectionDenied {
			continue
		}
		if event.AccountID == "" || event.Metadata["organization_id"] != f.org.ID || event.Metadata["source_device_id"] != source.ID || event.Metadata["target_device_id"] != target.ID {
			t.Fatalf("connection audit missing actor/org/devices: %+v", event)
		}
		if source, ok := event.Metadata["permission_source"].(string); ok {
			sources[source] = true
		}
	}
	if !sources[string(ConnectionPermissionDeny)] || !sources[string(ConnectionPermissionDeviceGrant)] {
		t.Fatalf("connection audit permission sources = %+v", sources)
	}
	assertNoSensitiveJSON(t, events)
}

func TestTask10BAllRolesNeedExplicitConnectionGrant(t *testing.T) {
	f := newTask10BFixture(t)
	_, target := f.device(t, f.owner, "role-matrix-target")
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("EnrollOrganizationDevice: %v", err)
	}
	for _, tc := range []struct {
		name    string
		account Account
	}{
		{"owner", f.owner},
		{"admin", f.admin},
		{"operator", f.operator},
		{"member", f.member},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, source := f.device(t, tc.account, tc.name+"-source")
			if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
				t.Fatalf("AuthorizeConnection without grant error = %v, want forbidden", err)
			}
			grant, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
				ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
				MemberAccountID: tc.account.ID, DeviceID: target.ID,
			})
			if err != nil {
				t.Fatalf("GrantConnectionAccess: %v", err)
			}
			auth, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID)
			if err != nil || !auth.Allowed || auth.PermissionSource != ConnectionPermissionDeviceGrant {
				t.Fatalf("AuthorizeConnection with grant = %+v, %v", auth, err)
			}
			if _, err := f.svc.RevokeConnectionAccess(f.ctx, RevokeConnectionGrantRequest{
				ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, GrantID: grant.ID,
			}); err != nil {
				t.Fatalf("RevokeConnectionAccess: %v", err)
			}
			if _, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err == nil || !errors.Is(err, ErrForbidden) {
				t.Fatalf("AuthorizeConnection after revoke error = %v, want forbidden", err)
			}
		})
	}
}

func TestTask10BLegacyPersonalFlowAndConcurrentIdempotency(t *testing.T) {
	f := newTask10BFixture(t)
	network, source := f.device(t, f.member, "legacy-source")
	_, target := func() (Network, Device) {
		device, err := f.store.CreateDevice(f.ctx, Device{
			ID: mustID("dev"), AccountID: f.member.ID, NetworkID: network.ID,
			Name: "legacy-target", Status: DeviceStatusOnline, JoinedAt: f.now,
		})
		if err != nil {
			t.Fatalf("CreateDevice legacy target: %v", err)
		}
		return network, device
	}()
	if auth, err := f.svc.AuthorizeConnection(f.ctx, source.ID, target.ID); err != nil || !auth.Allowed || auth.PermissionSource != ConnectionPermissionLegacy {
		t.Fatalf("legacy AuthorizeConnection = %+v, %v", auth, err)
	}

	const workers = 16
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			group, err := f.svc.CreateDeviceGroup(f.ctx, CreateDeviceGroupRequest{
				ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, Name: "concurrent",
			})
			if err == nil {
				ids <- group.ID
				return
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	if len(ids) != 1 {
		t.Fatalf("concurrent group successes = %d, want 1", len(ids))
	}
	for err := range errs {
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("concurrent group error = %v, want conflict", err)
		}
	}

	_, target = f.device(t, f.owner, "concurrent-grant-target")
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("EnrollOrganizationDevice: %v", err)
	}
	grantIDs := make(chan string, workers)
	grantErrs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grant, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
				ActorAccountID: f.owner.ID, OrganizationID: f.org.ID,
				MemberAccountID: f.member.ID, DeviceID: target.ID,
			})
			if err != nil {
				grantErrs <- err
				return
			}
			grantIDs <- grant.ID
		}()
	}
	wg.Wait()
	close(grantIDs)
	close(grantErrs)
	for err := range grantErrs {
		t.Fatalf("concurrent grant error = %v", err)
	}
	uniqueGrantIDs := map[string]bool{}
	for id := range grantIDs {
		uniqueGrantIDs[id] = true
	}
	if len(uniqueGrantIDs) != 1 {
		t.Fatalf("concurrent grant IDs = %+v, want one stable relation", uniqueGrantIDs)
	}
	grants, err := f.svc.ListConnectionGrants(f.ctx, f.owner.ID, f.org.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("ListConnectionGrants after concurrent grants = %+v, %v", grants, err)
	}
}

func TestTask10BHTTPClientUsesTrustedActorHeaderAndRedactsResponses(t *testing.T) {
	f := newTask10BFixture(t)
	server := httptest.NewServer(NewServer(f.svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), ActorAccountID: f.admin.ID}

	group, err := client.CreateDeviceGroup(f.ctx, CreateDeviceGroupRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, Name: "API group",
	})
	if err != nil || group.ID == "" {
		t.Fatalf("client CreateDeviceGroup = %+v, %v", group, err)
	}
	group, err = client.RenameDeviceGroup(f.ctx, RenameDeviceGroupRequest{
		OrganizationID: f.org.ID, GroupID: group.ID, Name: "API renamed group",
	})
	if err != nil || group.Name != "API renamed group" {
		t.Fatalf("client RenameDeviceGroup = %+v, %v", group, err)
	}
	gotGroup, err := client.GetDeviceGroup(f.ctx, f.org.ID, group.ID)
	if err != nil || gotGroup.ID != group.ID {
		t.Fatalf("client GetDeviceGroup = %+v, %v", gotGroup, err)
	}
	groups, err := client.ListDeviceGroups(f.ctx, f.org.ID)
	if err != nil || len(groups) != 1 || groups[0].ID != group.ID {
		t.Fatalf("client ListDeviceGroups = %+v, %v", groups, err)
	}

	body, _ := json.Marshal(map[string]any{"actor_account_id": f.owner.ID, "name": "forged"})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/organizations/"+f.org.ID+"/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(ActorAccountHeader, f.member.ID)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("forged request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged request status = %d, want 403", resp.StatusCode)
	}
	changed, err := client.ChangeOrganizationMemberRole(f.ctx, ChangeOrganizationMemberRoleRequest{
		OrganizationID: f.org.ID, AccountID: f.member.ID, Role: MembershipRoleOperator,
	})
	if err != nil || changed.Role != MembershipRoleOperator {
		t.Fatalf("client ChangeOrganizationMemberRole = %+v, %v", changed, err)
	}
	_, target := f.device(t, f.owner, "api-target")
	enrolled, err := client.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{
		OrganizationID: f.org.ID, DeviceID: target.ID,
	})
	if err != nil || enrolled.DeviceID != target.ID {
		t.Fatalf("client EnrollOrganizationDevice = %+v, %v", enrolled, err)
	}
	grouped, err := client.AddDeviceToGroup(f.ctx, OrganizationDeviceGroupRequest{
		OrganizationID: f.org.ID, GroupID: group.ID, DeviceID: target.ID,
	})
	if err != nil || grouped.GroupID != group.ID {
		t.Fatalf("client AddDeviceToGroup = %+v, %v", grouped, err)
	}
	ungrouped, err := client.RemoveDeviceFromGroup(f.ctx, OrganizationDeviceGroupRequest{
		OrganizationID: f.org.ID, GroupID: group.ID, DeviceID: target.ID,
	})
	if err != nil || ungrouped.GroupID != "" {
		t.Fatalf("client RemoveDeviceFromGroup = %+v, %v", ungrouped, err)
	}
	grant, err := client.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{
		OrganizationID: f.org.ID, MemberAccountID: f.member.ID, DeviceID: target.ID,
	})
	if err != nil || grant.ID == "" {
		t.Fatalf("client GrantConnectionAccess = %+v, %v", grant, err)
	}
	grants, err := client.ListConnectionGrants(f.ctx, f.org.ID)
	if err != nil || len(grants) != 1 || grants[0].ID != grant.ID {
		t.Fatalf("client ListConnectionGrants = %+v, %v", grants, err)
	}
	if _, err := client.RevokeConnectionAccess(f.ctx, RevokeConnectionGrantRequest{
		OrganizationID: f.org.ID, GrantID: grant.ID,
	}); err != nil {
		t.Fatalf("client RevokeConnectionAccess: %v", err)
	}
	if _, err := client.RemoveOrganizationDevice(f.ctx, RemoveOrganizationDeviceRequest{
		OrganizationID: f.org.ID, DeviceID: target.ID,
	}); err != nil {
		t.Fatalf("client RemoveOrganizationDevice: %v", err)
	}
	if _, err := client.DeleteDeviceGroup(f.ctx, DeleteDeviceGroupRequest{
		OrganizationID: f.org.ID, GroupID: group.ID,
	}); err != nil {
		t.Fatalf("client DeleteDeviceGroup: %v", err)
	}

	encoded, err := json.Marshal(map[string]any{"groups": groups, "grants": grants, "membership": changed, "device": enrolled})
	if err != nil {
		t.Fatalf("marshal groups: %v", err)
	}
	for _, forbidden := range [][]byte{[]byte("digest"), []byte("provider_"), []byte("remote_addr"), []byte("risk")} {
		if bytes.Contains(bytes.ToLower(encoded), forbidden) {
			t.Fatalf("public groups response leaked %q: %s", forbidden, encoded)
		}
	}
}
