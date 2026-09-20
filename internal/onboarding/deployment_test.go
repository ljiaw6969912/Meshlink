package onboarding

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshlink/internal/cloudhub"
)

func TestTask10DOfficialHubDeploymentAndRolloutManagerClosure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	store := cloudhub.NewMemoryStore()
	svc := cloudhub.NewService(store,
		cloudhub.WithNow(func() time.Time { return now }),
		cloudhub.WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.team.members": 10, "cloudhub.plans.team.devices": 5,
		}),
		cloudhub.WithTrustedUpdateVersions(cloudhub.TrustedUpdateVersion{
			Version: "0.2.0", PackageFile: "meshlink-0.2.0.zip", SHA256: strings.Repeat("b", 64),
		}),
	)
	owner, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "onboarding-10d@example.com", PlanID: cloudhub.PlanTeam})
	if err != nil {
		t.Fatal(err)
	}
	organization, err := svc.CreateOrganization(ctx, cloudhub.CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Onboarding 10D"})
	if err != nil {
		t.Fatal(err)
	}
	network, err := svc.CreateNetwork(ctx, cloudhub.CreateNetworkRequest{AccountID: owner.ID, Name: "Onboarding devices"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := svc.CreateDeviceGroup(ctx, cloudhub.CreateDeviceGroupRequest{ActorAccountID: owner.ID, OrganizationID: organization.ID, Name: "Staged"})
	if err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	mgr := Manager{BaseDir: t.TempDir(), HTTPClient: hub.Client(), Now: func() time.Time { return now }}
	if err := mgr.writeOfficialHubState(OfficialHubState{
		HubAPIURL: hub.URL, AccountID: owner.ID, NetworkID: network.ID,
		OrganizationID: organization.ID, OrganizationName: organization.Name,
	}); err != nil {
		t.Fatal(err)
	}

	bundle, err := mgr.CreateOfficialHubDeploymentBundle(ctx, OfficialHubDeploymentBundleRequest{
		GroupID: group.ID, Platform: cloudhub.DeploymentPlatformWindows,
		Architecture: cloudhub.DeploymentArchitectureAMD64, TTLSeconds: 600, MaxUses: 1,
	})
	if err != nil || bundle.Credential == "" || len(bundle.Files) != 3 {
		t.Fatalf("CreateOfficialHubDeploymentBundle = %+v, %v", bundle, err)
	}
	listed, err := mgr.ListOfficialHubDeploymentBundles(ctx, OfficialHubTeamRequest{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListOfficialHubDeploymentBundles = %+v, %v", listed, err)
	}
	redemption, err := mgr.RedeemOfficialHubBootstrapCredential(ctx, OfficialHubBootstrapRedeemRequest{
		Credential: bundle.Credential, OrganizationID: organization.ID, GroupID: group.ID,
		Platform: cloudhub.DeploymentPlatformWindows, Architecture: cloudhub.DeploymentArchitectureAMD64,
		DeviceName: "onboarding-device", Fingerprint: "sha256:onboarding-device", CurrentVersion: "0.1.0",
	})
	if err != nil || redemption.OrganizationDevice.GroupID != group.ID {
		t.Fatalf("RedeemOfficialHubBootstrapCredential = %+v, %v", redemption, err)
	}
	rollout, err := mgr.CreateOfficialHubRollout(ctx, OfficialHubRolloutRequest{
		DeviceIDs: []string{redemption.Device.ID}, TargetVersion: "0.2.0",
	})
	if err != nil || len(rollout.Targets) != 1 {
		t.Fatalf("CreateOfficialHubRollout = %+v, %v", rollout, err)
	}
	got, err := mgr.GetOfficialHubRollout(ctx, OfficialHubRolloutRequest{RolloutID: rollout.Rollout.ID})
	if err != nil || got.Rollout.ID != rollout.Rollout.ID {
		t.Fatalf("GetOfficialHubRollout = %+v, %v", got, err)
	}
	if _, err := mgr.HeartbeatOfficialHubDevice(ctx, OfficialHubHeartbeatRequest{
		DeviceID: redemption.Device.ID, Fingerprint: redemption.Device.Fingerprint, Status: cloudhub.DeviceStatusOffline, CurrentVersion: "0.1.0",
		RolloutID: rollout.Rollout.ID, TargetVersion: "0.2.0", VersionStatus: cloudhub.DeploymentTargetFailed,
		VersionSequence: 1, VersionErrorCode: cloudhub.RolloutErrorApplyFailed,
	}); err != nil {
		t.Fatalf("HeartbeatOfficialHubDevice rollout report: %v", err)
	}
	if _, err := mgr.RetryOfficialHubRolloutTarget(ctx, OfficialHubRolloutRequest{RolloutID: rollout.Rollout.ID, DeviceID: redemption.Device.ID}); err != nil {
		t.Fatalf("RetryOfficialHubRolloutTarget: %v", err)
	}
	if _, err := mgr.CancelOfficialHubRollout(ctx, OfficialHubRolloutRequest{RolloutID: rollout.Rollout.ID}); err != nil {
		t.Fatalf("CancelOfficialHubRollout: %v", err)
	}
	if _, err := mgr.RevokeOfficialHubBootstrapCredential(ctx, OfficialHubDeploymentCredentialRequest{CredentialID: bundle.Bundle.CredentialID}); err != nil {
		t.Fatalf("RevokeOfficialHubBootstrapCredential: %v", err)
	}
}
