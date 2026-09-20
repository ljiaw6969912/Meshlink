package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/cloudhub"
	"meshlink/internal/onboarding"
)

func TestTask10DDeploymentUIHasManagementClosureAndNarrowLayout(t *testing.T) {
	indexBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	index := string(indexBytes)
	for _, want := range []string{
		"officialDeploymentPanel", "officialDeploymentPlatform", "officialDeploymentArchitecture",
		"officialDeploymentMaxUses", "officialDeploymentCredential", "officialRevokeDeploymentCredential",
		"officialRolloutTargetVersion", "officialCreateRollout", "officialCancelRollout",
		"officialRolloutList", "officialRolloutTargets", "失败设备可单独重试",
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("deployment UI missing %q", want)
		}
	}
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"/api/official-hub/team/deployment-bundle", "/api/official-hub/team/deployment-bundles",
		"/api/official-hub/team/bootstrap-credential/revoke", "/api/official-hub/team/rollout",
		"/api/official-hub/team/rollout/cancel", "/api/official-hub/team/rollout/retry",
		"renderOfficialDeploymentBundles", "renderOfficialRollouts", "officialDeploymentErrorText",
		"renderOfficialDeploymentArtifacts", "URL.createObjectURL", "file.sha256",
		"preserveOneTime", "一次性凭据已从当前界面清除",
		"entry.bundle_id", "entry.rollout_id", "entry.group_id", "entry.target_version",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("deployment app.js missing %q", want)
		}
	}
	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	for _, want := range []string{".deployment-management", ".deployment-grid", ".rollout-target-row", "@media (max-width: 720px)"} {
		if !strings.Contains(css, want) {
			t.Fatalf("deployment CSS missing %q", want)
		}
	}
}

func TestTask10DDeploymentUIHTTPFlowRBACAndResponsiveDOM(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	store := cloudhub.NewMemoryStore()
	svc := cloudhub.NewService(store,
		cloudhub.WithNow(func() time.Time { return now }),
		cloudhub.WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.team.members": 10, "cloudhub.plans.team.devices": 5,
		}),
		cloudhub.WithTrustedUpdateVersions(cloudhub.TrustedUpdateVersion{
			Version: "0.2.0", PackageFile: "meshlink-0.2.0.zip", SHA256: strings.Repeat("c", 64),
		}),
	)
	owner, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "ui-owner-10d@example.com", PlanID: cloudhub.PlanTeam})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "ui-operator-10d@example.com", PlanID: cloudhub.PlanPersonal})
	if err != nil {
		t.Fatal(err)
	}
	organization, err := svc.CreateOrganization(ctx, cloudhub.CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "UI Task 10D"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMembership(ctx, cloudhub.Membership{
		ID: "mem_ui_10d_operator", OrganizationID: organization.ID, AccountID: operator.ID,
		Role: cloudhub.MembershipRoleOperator, Status: cloudhub.MembershipStatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	network, err := svc.CreateNetwork(ctx, cloudhub.CreateNetworkRequest{AccountID: owner.ID, Name: "UI deployment network"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := svc.CreateDeviceGroup(ctx, cloudhub.CreateDeviceGroupRequest{ActorAccountID: owner.ID, OrganizationID: organization.ID, Name: "UI managed"})
	if err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	baseDir := t.TempDir()
	writeUIOfficialHubState(t, baseDir, onboarding.OfficialHubState{
		HubAPIURL: hub.URL, AccountID: owner.ID, NetworkID: network.ID, OrganizationID: organization.ID,
	})
	handler := NewServerWithBaseDir(nil, baseDir)

	bundleResp := postJSONForTest(t, handler, "/api/official-hub/team/deployment-bundle", map[string]any{
		"group_id": group.ID, "platform": "windows", "architecture": "amd64", "ttl_seconds": 600, "max_uses": 1,
	})
	result := bundleResp["result"].(map[string]any)
	secret := result["credential"].(string)
	bundle := result["bundle"].(map[string]any)
	credentialID := bundle["credential_id"].(string)
	listResp := getJSONForTest(t, handler, "/api/official-hub/team/deployment-bundles")
	encodedList, _ := json.Marshal(listResp)
	if len(listResp["bundles"].([]any)) != 1 || bytes.Contains(encodedList, []byte(secret)) {
		t.Fatalf("deployment bundle list leaked or missing data: %s", encodedList)
	}
	client := &cloudhub.Client{BaseURL: hub.URL, HTTPClient: hub.Client(), ActorAccountID: owner.ID}
	redemption, err := client.RedeemBootstrapCredential(ctx, cloudhub.RedeemBootstrapCredentialRequest{
		Credential: secret, OrganizationID: organization.ID, GroupID: group.ID,
		Platform: cloudhub.DeploymentPlatformWindows, Architecture: cloudhub.DeploymentArchitectureAMD64,
		DeviceName: "ui-device-10d", Fingerprint: "sha256:ui-device-10d", CurrentVersion: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	rolloutResp := postJSONForTest(t, handler, "/api/official-hub/team/rollout", map[string]any{
		"device_ids": []string{redemption.Device.ID}, "target_version": "0.2.0",
	})
	rolloutID := rolloutResp["result"].(map[string]any)["rollout"].(map[string]any)["id"].(string)
	if _, err := client.ReportRolloutTarget(ctx, cloudhub.ReportRolloutTargetRequest{
		RolloutID: rolloutID, DeviceID: redemption.Device.ID, Sequence: 1, Status: cloudhub.DeploymentTargetFailed,
		CurrentVersion: "0.1.0", TargetVersion: "0.2.0", ErrorCode: cloudhub.RolloutErrorApplyFailed,
		Fingerprint: redemption.Device.Fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	retryResp := postJSONForTest(t, handler, "/api/official-hub/team/rollout/retry", map[string]any{"rollout_id": rolloutID, "device_id": redemption.Device.ID})
	if retryResp["target"].(map[string]any)["attempt"].(float64) != 2 {
		t.Fatalf("retry response = %+v", retryResp)
	}
	postJSONForTest(t, handler, "/api/official-hub/team/rollout/cancel", map[string]any{"rollout_id": rolloutID})
	postJSONForTest(t, handler, "/api/official-hub/team/bootstrap-credential/revoke", map[string]any{"credential_id": credentialID})

	for _, userAgent := range []string{"Meshlink Desktop", "Mozilla/5.0 (iPhone; Mobile)"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("User-Agent", userAgent)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "officialDeploymentPanel") || !strings.Contains(rec.Body.String(), "officialRolloutTargets") {
			t.Fatalf("responsive DOM request %q returned %d without deployment controls", userAgent, rec.Code)
		}
	}

	writeUIOfficialHubState(t, baseDir, onboarding.OfficialHubState{
		HubAPIURL: hub.URL, AccountID: operator.ID, NetworkID: network.ID, OrganizationID: organization.ID,
	})
	denied := postJSONAllowErrorForTest(t, handler, "/api/official-hub/team/deployment-bundle", map[string]any{
		"platform": "windows", "architecture": "amd64", "ttl_seconds": 60, "max_uses": 1,
	})
	if !strings.Contains(strings.ToLower(denied["error"].(string)), "forbidden") {
		t.Fatalf("operator deployment error = %+v, want explicit forbidden", denied)
	}
}

func writeUIOfficialHubState(t *testing.T, baseDir string, state onboarding.OfficialHubState) {
	t.Helper()
	path := filepath.Join(baseDir, "configs", "official-hub.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
