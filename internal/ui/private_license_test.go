package ui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/cloudhub"
	"meshlink/internal/licensing"
	"meshlink/internal/onboarding"
)

func TestPrivateLicenseUIHTTPDOMRBACAndSupportBoundary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	store := cloudhub.NewMemoryStore()
	svc := cloudhub.NewService(store, cloudhub.WithNow(func() time.Time { return now }), cloudhub.WithPlanQuotaLimits(map[string]int64{"cloudhub.plans.team.members": 10}))
	owner, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "ui-private-owner@example.invalid", PlanID: cloudhub.PlanTeam})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "ui-private-operator@example.invalid", PlanID: cloudhub.PlanPersonal})
	if err != nil {
		t.Fatal(err)
	}
	organization, err := svc.CreateOrganization(ctx, cloudhub.CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Private UI"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMembership(ctx, cloudhub.Membership{ID: "mem_private_ui_operator", OrganizationID: organization.ID, AccountID: operator.ID, Role: cloudhub.MembershipRoleOperator, Status: cloudhub.MembershipStatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := licensing.NewManager(licensing.ManagerConfig{Path: filepath.Join(t.TempDir(), "license.json"), TrustedKeys: licensing.TrustedKeys{"license-2026": publicKey}, Binding: licensing.Binding{OrganizationID: organization.ID, DeploymentID: "deployment-private"}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetPrivateLicenseManager(manager)
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	baseDir := t.TempDir()
	writeUIOfficialHubState(t, baseDir, onboarding.OfficialHubState{HubAPIURL: hub.URL, AccountID: owner.ID, OrganizationID: organization.ID})
	handler := NewServerWithBaseDir(nil, baseDir)
	payload := licensing.Payload{
		LicenseID: "lic-private-ui", KeyID: "license-2026", Customer: "Example Customer", OrganizationID: organization.ID, DeploymentID: "deployment-private",
		IssuedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		ExpiryPolicy: licensing.ExpiryPolicyContinueExisting,
		Entitlements: licensing.Entitlements{DeviceCount: 25, MemberCount: 8, ConcurrentOnlineDevices: 10, RelayBytesPerMonth: 1 << 30, ActiveRelaySessions: 4, AuditRetentionDays: 90, DeploymentCount: 3, PrivateDeployment: true, Relay: true, Rollout: true, OfflineUpdates: true},
		Support:      licensing.Support{ID: "support-standard", Contact: "support@example.invalid"},
	}
	raw, err := licensing.Sign(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	imported := postJSONForTest(t, handler, "/api/official-hub/team/private-license", map[string]any{"signed_license": string(raw)})
	if imported["license"].(map[string]any)["license_id"] != payload.LicenseID {
		t.Fatalf("import response = %+v", imported)
	}
	status := getJSONForTest(t, handler, "/api/official-hub/team/private-license")
	text := string(mustJSON(t, status))
	if !strings.Contains(text, payload.Support.ID) || strings.Contains(text, "signature") || strings.Contains(text, "signed_license") || strings.Contains(text, "payload") || strings.Contains(text, payload.Customer) {
		t.Fatalf("license UI status is missing support or leaked signed material: %s", text)
	}

	for _, userAgent := range []string{"Meshlink Desktop", "Mozilla/5.0 (iPhone; Mobile)"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("User-Agent", userAgent)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		for _, want := range []string{"officialPrivateLicensePanel", "officialPrivateLicenseDocument", "officialPrivateLicenseSummary", "officialPrivateLicenseSupport", "不会自动上传用户内容"} {
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("DOM %q missing %q (status %d)", userAgent, want, rec.Code)
			}
		}
	}
	css, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".private-license-management", ".private-license-management .section-head", "@media (max-width: 720px)", "overflow-wrap: anywhere"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("private license narrow CSS missing %q", want)
		}
	}
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"officialImportPrivateLicense", "officialRefreshPrivateLicense", "私有部署授权拒绝"} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("private license UI JavaScript missing %q", want)
		}
	}

	writeUIOfficialHubState(t, baseDir, onboarding.OfficialHubState{HubAPIURL: hub.URL, AccountID: operator.ID, OrganizationID: organization.ID})
	denied := getJSONAllowErrorForTest(t, handler, "/api/official-hub/team/private-license")
	if !strings.Contains(strings.ToLower(denied["error"].(string)), "forbidden") {
		t.Fatalf("operator license error = %+v", denied)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func getJSONAllowErrorForTest(t *testing.T, handler http.Handler, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s response %q: %v", path, rec.Body.String(), err)
	}
	return out
}
