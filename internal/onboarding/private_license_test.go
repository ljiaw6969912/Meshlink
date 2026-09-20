package onboarding

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"meshlink/internal/cloudhub"
	"meshlink/internal/licensing"
)

func TestOfficialHubPrivateLicenseManagerClosure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	store := cloudhub.NewMemoryStore()
	svc := cloudhub.NewService(store, cloudhub.WithNow(func() time.Time { return now }), cloudhub.WithPlanQuotaLimits(map[string]int64{"cloudhub.plans.team.members": 10}))
	owner, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "private-owner@example.invalid", PlanID: cloudhub.PlanTeam})
	if err != nil {
		t.Fatal(err)
	}
	organization, err := svc.CreateOrganization(ctx, cloudhub.CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Private Organization"})
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := licensing.NewManager(licensing.ManagerConfig{
		Path: filepath.Join(t.TempDir(), "license.json"), TrustedKeys: licensing.TrustedKeys{"license-2026": publicKey},
		Binding: licensing.Binding{OrganizationID: organization.ID, DeploymentID: "deployment-private"}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetPrivateLicenseManager(manager)
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	mgr := Manager{BaseDir: t.TempDir(), HTTPClient: hub.Client(), Now: func() time.Time { return now }}
	if err := mgr.writeOfficialHubState(OfficialHubState{HubAPIURL: hub.URL, AccountID: owner.ID, OrganizationID: organization.ID, OrganizationName: organization.Name}); err != nil {
		t.Fatal(err)
	}
	payload := licensing.Payload{
		LicenseID: "lic-private-001", KeyID: "license-2026", Customer: "Example Customer", OrganizationID: organization.ID, DeploymentID: "deployment-private",
		IssuedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		ExpiryPolicy: licensing.ExpiryPolicyContinueExisting,
		Entitlements: licensing.Entitlements{DeviceCount: 25, MemberCount: 8, ConcurrentOnlineDevices: 10, RelayBytesPerMonth: 1 << 30, ActiveRelaySessions: 4,
			AuditRetentionDays: 90, DeploymentCount: 3, PrivateDeployment: true, Relay: true, Rollout: true, OfflineUpdates: true},
		Support: licensing.Support{ID: "support-standard", Contact: "support@example.invalid"},
	}
	raw, err := licensing.Sign(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := mgr.ImportOfficialHubPrivateLicense(ctx, OfficialHubPrivateLicenseRequest{SignedLicense: string(raw)})
	if err != nil || imported.LicenseID != payload.LicenseID {
		t.Fatalf("ImportOfficialHubPrivateLicense = %+v, %v", imported, err)
	}
	summary, err := mgr.GetOfficialHubPrivateLicense(ctx, OfficialHubTeamRequest{})
	if err != nil || summary.Support.ID != payload.Support.ID || summary.Signature != "" || summary.RawPayload != "" {
		t.Fatalf("GetOfficialHubPrivateLicense = %+v, %v", summary, err)
	}
}
