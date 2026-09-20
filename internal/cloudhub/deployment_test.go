package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type task10DFixture struct {
	ctx      context.Context
	now      *time.Time
	store    *MemoryStore
	svc      *Service
	org      Organization
	owner    Account
	admin    Account
	operator Account
	member   Account
	network  Network
	group    DeviceGroup
}

func newTask10DFixture(t *testing.T, deviceLimit int64) task10DFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.team.members":                   20,
			"cloudhub.plans.team.devices":                   deviceLimit,
			"cloudhub.plans.team.concurrent_online_devices": deviceLimit,
			"cloudhub.plans.team.audit_log_retention_days":  30,
		}),
		WithTrustedUpdateVersions(TrustedUpdateVersion{
			Version: "0.2.0", PackageFile: "meshlink-0.2.0.zip",
			SHA256: strings.Repeat("a", 64),
		}),
	)
	createAccount := func(email string, plan PlanID) Account {
		account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: email, PlanID: plan})
		if err != nil {
			t.Fatalf("CreateAccount(%s): %v", email, err)
		}
		return account
	}
	owner := createAccount("owner-10d@example.com", PlanTeam)
	admin := createAccount("admin-10d@example.com", PlanPersonal)
	operator := createAccount("operator-10d@example.com", PlanPersonal)
	member := createAccount("member-10d@example.com", PlanPersonal)
	org, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Task 10D Team"})
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	for _, item := range []struct {
		account Account
		role    MembershipRole
	}{{admin, MembershipRoleAdmin}, {operator, MembershipRoleOperator}, {member, MembershipRoleMember}} {
		if _, err := store.CreateMembership(ctx, Membership{
			ID: mustID("mem"), OrganizationID: org.ID, AccountID: item.account.ID,
			Role: item.role, Status: MembershipStatusActive, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreateMembership(%s): %v", item.role, err)
		}
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: owner.ID, Name: "Task 10D devices"})
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	group, err := svc.CreateDeviceGroup(ctx, CreateDeviceGroupRequest{
		ActorAccountID: owner.ID, OrganizationID: org.ID, Name: "Managed Windows",
	})
	if err != nil {
		t.Fatalf("CreateDeviceGroup: %v", err)
	}
	return task10DFixture{
		ctx: ctx, now: &now, store: store, svc: svc, org: org, owner: owner,
		admin: admin, operator: operator, member: member, network: network, group: group,
	}
}

func TestTask10DDeploymentAuditPublicDTOCarriesControlledResourceFields(t *testing.T) {
	f := newTask10DFixture(t, 2)
	bundle, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.owner, 1))
	if err != nil {
		t.Fatal(err)
	}
	joined, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(bundle.Credential, 1))
	if err != nil {
		t.Fatal(err)
	}
	rollout, err := f.svc.CreateRollout(f.ctx, CreateRolloutRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceIDs: []string{joined.Device.ID}, TargetVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, PageSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sawBundle, sawRollout bool
	for _, entry := range page.Entries {
		if entry.BundleID == bundle.Bundle.ID && entry.Action == "create_deployment_bundle" {
			sawBundle = entry.GroupID == f.group.ID
		}
		if entry.RolloutID == rollout.Rollout.ID && entry.Action == "create_rollout" {
			sawRollout = entry.TargetVersion == "0.2.0"
		}
		encoded, _ := json.Marshal(entry)
		if bytes.Contains(encoded, []byte(bundle.Credential)) || bytes.Contains(encoded, []byte("digest")) {
			t.Fatalf("public audit DTO leaked credential material: %s", encoded)
		}
	}
	if !sawBundle || !sawRollout {
		t.Fatalf("public audit entries missing deployment fields: %+v", page.Entries)
	}
}

func (f task10DFixture) bundleRequest(actor Account, maxUses int) CreateDeploymentBundleRequest {
	return CreateDeploymentBundleRequest{
		ActorAccountID: actor.ID, OrganizationID: f.org.ID, NetworkID: f.network.ID,
		GroupID: f.group.ID, Platform: DeploymentPlatformWindows,
		Architecture: DeploymentArchitectureAMD64, TTL: 15 * time.Minute, MaxUses: maxUses,
	}
}

func (f task10DFixture) redeemRequest(secret string, index int) RedeemBootstrapCredentialRequest {
	return RedeemBootstrapCredentialRequest{
		Credential: secret, OrganizationID: f.org.ID, GroupID: f.group.ID,
		Platform: DeploymentPlatformWindows, Architecture: DeploymentArchitectureAMD64,
		DeviceName: fmt.Sprintf("task10d-device-%02d", index), Fingerprint: fmt.Sprintf("sha256:task10d-%02d", index),
		CurrentVersion: "0.1.0",
	}
}

func TestTask10DDeploymentBundleRBACBindingManifestAndRedaction(t *testing.T) {
	f := newTask10DFixture(t, 20)
	for _, tc := range []struct {
		name    string
		actor   Account
		allowed bool
	}{{"owner", f.owner, true}, {"admin", f.admin, true}, {"operator", f.operator, false}, {"member", f.member, false}} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(tc.actor, 2))
			if !tc.allowed {
				if err == nil || !errors.Is(err, ErrForbidden) {
					t.Fatalf("CreateDeploymentBundle error = %v, want forbidden", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateDeploymentBundle: %v", err)
			}
			if result.Bundle.ID == "" || result.Credential == "" || result.Bundle.OrganizationID != f.org.ID || result.Bundle.GroupID != f.group.ID {
				t.Fatalf("bundle result = %+v, want stable IDs, binding, and one-time plaintext", result)
			}
			if result.Bundle.Platform != DeploymentPlatformWindows || result.Bundle.Architecture != DeploymentArchitectureAMD64 || len(result.Files) != 3 {
				t.Fatalf("bundle = %+v files=%+v, want fixed windows/amd64 template", result.Bundle, result.Files)
			}
			manifestFile := result.Files[2]
			if manifestFile.Name != result.Bundle.ManifestFile || manifestFile.SHA256 == "" {
				t.Fatalf("manifest artifact = %+v, bundle = %+v", manifestFile, result.Bundle)
			}
			var manifest DeploymentManifest
			if err := json.Unmarshal([]byte(manifestFile.Content), &manifest); err != nil {
				t.Fatalf("manifest JSON: %v", err)
			}
			if manifest.BundleID != result.Bundle.ID || manifest.TemplateVersion != result.Bundle.TemplateVersion || manifest.Platform != result.Bundle.Platform || manifest.Architecture != result.Bundle.Architecture {
				t.Fatalf("manifest = %+v, inconsistent with bundle %+v", manifest, result.Bundle)
			}
			for i, file := range result.Files[:2] {
				if manifest.Files[i].Name != file.Name || manifest.Files[i].SHA256 != file.SHA256 || manifest.Files[i].Size != file.Size {
					t.Fatalf("manifest file[%d] = %+v, artifact = %+v", i, manifest.Files[i], file)
				}
			}
			stored, err := f.store.GetBootstrapCredential(f.ctx, f.org.ID, result.Bundle.CredentialID)
			if err != nil || stored.Digest == "" || stored.Uses != 0 || stored.MaxUses != 2 {
				t.Fatalf("stored credential = %+v, err=%v", stored, err)
			}
			if strings.Contains(stored.Digest, result.Credential) {
				t.Fatal("stored digest contains plaintext credential")
			}
			listed, err := f.svc.ListDeploymentBundles(f.ctx, tc.actor.ID, f.org.ID)
			if err != nil || len(listed) == 0 || listed[len(listed)-1].Credential == nil || listed[len(listed)-1].Credential.Status != BootstrapCredentialActive {
				t.Fatalf("ListDeploymentBundles = %+v, %v", listed, err)
			}
			publicJSON, _ := json.Marshal(struct {
				Bundle     DeploymentBundle    `json:"bundle"`
				Credential BootstrapCredential `json:"credential"`
				Listed     []DeploymentBundle  `json:"listed"`
			}{result.Bundle, stored, listed})
			if bytes.Contains(publicJSON, []byte(result.Credential)) || bytes.Contains(publicJSON, []byte(stored.Digest)) || bytes.Contains(publicJSON, []byte("digest")) {
				t.Fatalf("public DTO leaked bootstrap secret/digest: %s", publicJSON)
			}
		})
	}

	bad := f.bundleRequest(f.owner, 1)
	bad.Platform = DeploymentPlatform("windows; Invoke-WebRequest https://evil.invalid")
	if _, err := f.svc.CreateDeploymentBundle(f.ctx, bad); err == nil {
		t.Fatal("injected platform must be rejected")
	}
	bad = f.bundleRequest(f.owner, 1)
	bad.GroupID = "grp_other_organization"
	if _, err := f.svc.CreateDeploymentBundle(f.ctx, bad); err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-organization group error = %v, want not found", err)
	}
	other := newTask10DFixture(t, 5)
	created, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.owner, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetDeploymentBundle(f.ctx, other.owner.ID, other.org.ID, created.Bundle.ID); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-organization bundle read error = %v, want forbidden", err)
	}
}

func TestTask10DTenSimulatedBootstrapRedemptionsQuotaAndAudit(t *testing.T) {
	f := newTask10DFixture(t, 10)
	result, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.owner, 11))
	if err != nil {
		t.Fatalf("CreateDeploymentBundle: %v", err)
	}
	for i := 1; i <= 10; i++ {
		redeemed, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(result.Credential, i))
		if err != nil {
			t.Fatalf("RedeemBootstrapCredential(%d): %v", i, err)
		}
		if redeemed.Device.ID == "" || redeemed.OrganizationDevice.DeviceID != redeemed.Device.ID || redeemed.OrganizationDevice.OrganizationID != f.org.ID || redeemed.OrganizationDevice.GroupID != f.group.ID {
			t.Fatalf("redemption %d = %+v, want automatic organization/group binding", i, redeemed)
		}
	}
	devices, err := f.store.ListOrganizationDevices(f.ctx, f.org.ID)
	if err != nil || len(devices) != 10 {
		t.Fatalf("organization devices = %d, %v, want 10", len(devices), err)
	}
	credentialBefore, _ := f.store.GetBootstrapCredential(f.ctx, f.org.ID, result.Bundle.CredentialID)
	if credentialBefore.Uses != 10 {
		t.Fatalf("uses = %d, want 10", credentialBefore.Uses)
	}
	if _, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(result.Credential, 11)); err == nil || !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("11th redemption error = %v, want quota exceeded", err)
	}
	credentialAfter, _ := f.store.GetBootstrapCredential(f.ctx, f.org.ID, result.Bundle.CredentialID)
	devicesAfter, _ := f.store.ListOrganizationDevices(f.ctx, f.org.ID)
	if credentialAfter.Uses != 10 || len(devicesAfter) != 10 {
		t.Fatalf("failed redemption consumed state: uses=%d devices=%d", credentialAfter.Uses, len(devicesAfter))
	}
	events, _ := f.store.ListOrganizationAuditEvents(f.ctx, f.org.ID)
	joined := 0
	for _, event := range events {
		if event.Event == AuditBootstrapCredentialRedeemed && event.Metadata["result"] == "redeemed" {
			joined++
		}
		encoded, _ := json.Marshal(event)
		if bytes.Contains(encoded, []byte(result.Credential)) {
			t.Fatalf("audit leaked bootstrap credential: %s", encoded)
		}
	}
	if joined != 10 {
		t.Fatalf("bootstrap redemption audits = %d, want 10", joined)
	}
}

func TestTask10DBootstrapExpiryRevocationBindingAndConcurrentLimit(t *testing.T) {
	f := newTask10DFixture(t, 20)
	result, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.admin, 3))
	if err != nil {
		t.Fatal(err)
	}
	wrongOrg := f.redeemRequest(result.Credential, 1)
	wrongOrg.OrganizationID = "org_wrong"
	if _, err := f.svc.RedeemBootstrapCredential(f.ctx, wrongOrg); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong organization error = %v", err)
	}
	wrongPlatform := f.redeemRequest(result.Credential, 1)
	wrongPlatform.Platform = DeploymentPlatformLinux
	if _, err := f.svc.RedeemBootstrapCredential(f.ctx, wrongPlatform); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong platform error = %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(result.Credential, index+1))
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	successes := 0
	for err := range errCh {
		if err == nil {
			successes++
		}
	}
	if successes != 3 {
		t.Fatalf("concurrent successes = %d, want exactly 3", successes)
	}

	inactiveCreatorBundle, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.admin, 1))
	if err != nil {
		t.Fatal(err)
	}
	adminMembership, err := f.store.GetOrganizationMembership(f.ctx, f.org.ID, f.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	adminMembership.Status = MembershipStatusRemoved
	adminMembership.UpdatedAt = *f.now
	if _, err := f.store.UpdateMembership(f.ctx, adminMembership); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(inactiveCreatorBundle.Credential, 19)); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("inactive credential creator membership error = %v, want forbidden", err)
	}

	revokedBundle, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.owner, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RevokeBootstrapCredential(f.ctx, RevokeBootstrapCredentialRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, CredentialID: revokedBundle.Bundle.CredentialID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(revokedBundle.Credential, 20)); err == nil || !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked redemption error = %v", err)
	}

	expiredBundle, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.owner, 1))
	if err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(16 * time.Minute)
	if _, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(expiredBundle.Credential, 21)); err == nil || !errors.Is(err, ErrExpired) {
		t.Fatalf("expired redemption error = %v", err)
	}
}

func TestTask10DRolloutStateMachineIdempotencyCancelAndSingleDeviceRetry(t *testing.T) {
	f := newTask10DFixture(t, 10)
	bundle, err := f.svc.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.owner, 10))
	if err != nil {
		t.Fatal(err)
	}
	var deviceIDs []string
	for i := 1; i <= 10; i++ {
		joined, err := f.svc.RedeemBootstrapCredential(f.ctx, f.redeemRequest(bundle.Credential, i))
		if err != nil {
			t.Fatal(err)
		}
		deviceIDs = append(deviceIDs, joined.Device.ID)
	}

	for _, actor := range []Account{f.operator, f.member} {
		if _, err := f.svc.CreateRollout(f.ctx, CreateRolloutRequest{
			ActorAccountID: actor.ID, OrganizationID: f.org.ID, GroupID: f.group.ID, TargetVersion: "0.2.0",
		}); err == nil || !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s CreateRollout error = %v, want forbidden", actor.ID, err)
		}
	}
	if _, err := f.svc.CreateRollout(f.ctx, CreateRolloutRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, GroupID: f.group.ID,
		TargetVersion: "https://evil.invalid/payload.ps1",
	}); err == nil {
		t.Fatal("arbitrary rollout URL must be rejected")
	}
	created, err := f.svc.CreateRollout(f.ctx, CreateRolloutRequest{
		ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, GroupID: f.group.ID, TargetVersion: "0.2.0",
	})
	if err != nil {
		t.Fatalf("CreateRollout: %v", err)
	}
	if created.Rollout.ID == "" || len(created.Targets) != 10 || created.Rollout.Status != RolloutStatusPending {
		t.Fatalf("rollout = %+v targets=%d", created.Rollout, len(created.Targets))
	}
	for _, deviceID := range deviceIDs {
		device, deviceErr := f.store.GetDevice(f.ctx, deviceID)
		if deviceErr != nil || device.RolloutID != created.Rollout.ID || device.TargetVersion != "0.2.0" {
			t.Fatalf("device rollout assignment = %+v, %v", device, deviceErr)
		}
	}

	succeededID := deviceIDs[0]
	if _, err := f.svc.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: created.Rollout.ID, DeviceID: succeededID, Sequence: 1, Status: DeploymentTargetInProgress,
		CurrentVersion: "0.1.0", TargetVersion: "0.2.0", Fingerprint: "sha256:wrong-device",
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong device identity report error = %v, want forbidden", err)
	}
	inProgress, err := f.svc.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: created.Rollout.ID, DeviceID: succeededID, Sequence: 1, Status: DeploymentTargetInProgress,
		CurrentVersion: "0.1.0", TargetVersion: "0.2.0", Fingerprint: "sha256:task10d-01",
	})
	if err != nil || inProgress.Status != DeploymentTargetInProgress {
		t.Fatalf("in-progress report = %+v, %v", inProgress, err)
	}
	succeeded, err := f.svc.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: created.Rollout.ID, DeviceID: succeededID, Sequence: 2, Status: DeploymentTargetSucceeded,
		CurrentVersion: "0.2.0", TargetVersion: "0.2.0", Fingerprint: "sha256:task10d-01",
	})
	if err != nil || succeeded.Status != DeploymentTargetSucceeded || succeeded.CurrentVersion != "0.2.0" {
		t.Fatalf("success report = %+v, %v", succeeded, err)
	}
	succeededDevice, err := f.store.GetDevice(f.ctx, succeededID)
	if err != nil || succeededDevice.CurrentVersion != "0.2.0" || succeededDevice.TargetVersion != "" || succeededDevice.RolloutID != "" {
		t.Fatalf("successful device assignment was not cleared: %+v, %v", succeededDevice, err)
	}
	if _, err := f.svc.CreateRollout(f.ctx, CreateRolloutRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceIDs: []string{succeededID}, TargetVersion: "0.2.0",
	}); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("already successful device was scheduled again: %v", err)
	}
	stale, err := f.svc.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: created.Rollout.ID, DeviceID: succeededID, Sequence: 1, Status: DeploymentTargetFailed,
		CurrentVersion: "0.1.0", TargetVersion: "0.2.0", ErrorCode: RolloutErrorApplyFailed, Fingerprint: "sha256:task10d-01",
	})
	if err != nil || stale.Status != DeploymentTargetSucceeded || stale.CurrentVersion != "0.2.0" {
		t.Fatalf("stale report regressed success: %+v, %v", stale, err)
	}
	duplicate, err := f.svc.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: created.Rollout.ID, DeviceID: succeededID, Sequence: 2, Status: DeploymentTargetSucceeded,
		CurrentVersion: "0.2.0", TargetVersion: "0.2.0", Fingerprint: "sha256:task10d-01",
	})
	if err != nil || duplicate.Status != DeploymentTargetSucceeded {
		t.Fatalf("idempotent report = %+v, %v", duplicate, err)
	}
	if _, err := f.svc.RetryRolloutTarget(f.ctx, RetryRolloutTargetRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, RolloutID: created.Rollout.ID, DeviceID: succeededID,
	}); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("retry succeeded device error = %v, want conflict", err)
	}

	failedID := deviceIDs[1]
	failed, err := f.svc.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: created.Rollout.ID, DeviceID: failedID, Sequence: 1, Status: DeploymentTargetFailed,
		CurrentVersion: "0.1.0", TargetVersion: "0.2.0", ErrorCode: RolloutErrorApplyFailed, Fingerprint: "sha256:task10d-02",
	})
	if err != nil || failed.Status != DeploymentTargetFailed || failed.CurrentVersion != "0.1.0" {
		t.Fatalf("failed report = %+v, %v; current version must remain last known good", failed, err)
	}
	failedDevice, err := f.store.GetDevice(f.ctx, failedID)
	if err != nil || failedDevice.CurrentVersion != "0.1.0" || failedDevice.TargetVersion != "0.2.0" || failedDevice.RolloutID != created.Rollout.ID {
		t.Fatalf("failed device lost its retry assignment or last known good version: %+v, %v", failedDevice, err)
	}
	retried, err := f.svc.RetryRolloutTarget(f.ctx, RetryRolloutTargetRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, RolloutID: created.Rollout.ID, DeviceID: failedID,
	})
	if err != nil || retried.Status != DeploymentTargetPending || retried.Attempt != 2 || retried.DeviceID != failedID {
		t.Fatalf("retry = %+v, %v", retried, err)
	}
	other, err := f.svc.GetRolloutTarget(f.ctx, f.owner.ID, f.org.ID, created.Rollout.ID, deviceIDs[2])
	if err != nil || other.Attempt != 1 || other.Status != DeploymentTargetPending {
		t.Fatalf("retry changed other target: %+v, %v", other, err)
	}
	inProgressID := deviceIDs[2]
	if _, err := f.svc.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: created.Rollout.ID, DeviceID: inProgressID, Sequence: 1, Status: DeploymentTargetInProgress,
		CurrentVersion: "0.1.0", TargetVersion: "0.2.0", Fingerprint: "sha256:task10d-03",
	}); err != nil {
		t.Fatalf("second in-progress report: %v", err)
	}

	canceled, err := f.svc.CancelRollout(f.ctx, CancelRolloutRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, RolloutID: created.Rollout.ID,
	})
	if err != nil || canceled.Rollout.Status != RolloutStatusCanceled {
		t.Fatalf("CancelRollout = %+v, %v", canceled.Rollout, err)
	}
	for _, target := range canceled.Targets {
		switch target.DeviceID {
		case succeededID:
			if target.Status != DeploymentTargetSucceeded {
				t.Fatalf("cancel regressed succeeded target: %+v", target)
			}
		case inProgressID:
			if target.Status != DeploymentTargetInProgress {
				t.Fatalf("cancel changed in-progress target: %+v", target)
			}
		default:
			if target.Status == DeploymentTargetPending {
				t.Fatalf("cancel left pending target: %+v", target)
			}
		}
		device, deviceErr := f.store.GetDevice(f.ctx, target.DeviceID)
		if deviceErr != nil {
			t.Fatal(deviceErr)
		}
		if target.Status == DeploymentTargetCanceled && (device.TargetVersion != "" || device.RolloutID != "") {
			t.Fatalf("canceled device kept rollout assignment: %+v", device)
		}
		if target.Status == DeploymentTargetInProgress && (device.TargetVersion != "0.2.0" || device.RolloutID != created.Rollout.ID) {
			t.Fatalf("in-progress device lost rollout assignment: %+v", device)
		}
	}
	events, _ := f.store.ListOrganizationAuditEvents(f.ctx, f.org.ID)
	for _, want := range []string{AuditRolloutCreated, AuditRolloutTargetReported, AuditRolloutTargetRetried, AuditRolloutCanceled} {
		if !hasAuditEvent(events, want) {
			t.Fatalf("audit events missing %q: %+v", want, events)
		}
	}
}
