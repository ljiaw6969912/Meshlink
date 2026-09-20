package onboarding

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshlink/internal/cloudhub"
)

func TestOfficialHubSubscriptionExperienceExplainsAllSubscriptionStates(t *testing.T) {
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		status       cloudhub.SubscriptionStatus
		expires      *time.Time
		wantPlan     cloudhub.PlanID
		wantContains []string
		wantAvoid    []string
	}{
		{
			status:       cloudhub.SubscriptionStatusPending,
			wantPlan:     cloudhub.PlanPersonal,
			wantContains: []string{"正在处理中", "当前仍按现有可用能力"},
			wantAvoid:    []string{"已升级"},
		},
		{
			status:       cloudhub.SubscriptionStatusActive,
			wantPlan:     cloudhub.PlanFamily,
			wantContains: []string{"已生效"},
		},
		{
			status:       cloudhub.SubscriptionStatusPastDue,
			wantPlan:     cloudhub.PlanPersonal,
			wantContains: []string{"需要处理", "不能当作已升级"},
			wantAvoid:    []string{"网络错误"},
		},
		{
			status:       cloudhub.SubscriptionStatusCanceled,
			expires:      timePtr(now.Add(72 * time.Hour)),
			wantPlan:     cloudhub.PlanFamily,
			wantContains: []string{"已取消", "可用至", "2026"},
		},
		{
			status:       cloudhub.SubscriptionStatusExpired,
			wantPlan:     cloudhub.PlanFree,
			wantContains: []string{"已到期", "自建服务器", "自建 Relay", "基础设备互联"},
			wantAvoid:    []string{"已升级"},
		},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			mgr, hubURL, accountID, postWebhook := subscriptionExperienceHub(t, now, cloudhub.PlanPersonal)
			postWebhook(t, accountID, cloudhub.PlanFamily, tc.status, tc.expires, 1)

			experience, err := mgr.OfficialHubSubscriptionExperience(context.Background(), OfficialHubSubscriptionExperienceRequest{
				HubAPIURL: hubURL,
			})
			if err != nil {
				t.Fatalf("OfficialHubSubscriptionExperience returned error: %v", err)
			}
			if experience.Subscription.Status != tc.status {
				t.Fatalf("subscription status = %q, want %q", experience.Subscription.Status, tc.status)
			}
			if experience.CurrentPlan.ID != tc.wantPlan {
				t.Fatalf("current plan = %q, want %q; experience=%+v", experience.CurrentPlan.ID, tc.wantPlan, experience)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(experience.Subscription.Message, want) {
					t.Fatalf("subscription message = %q, want %q", experience.Subscription.Message, want)
				}
			}
			for _, forbidden := range tc.wantAvoid {
				if strings.Contains(experience.Subscription.Message, forbidden) {
					t.Fatalf("subscription message = %q, should not contain %q", experience.Subscription.Message, forbidden)
				}
			}
			if tc.status == cloudhub.SubscriptionStatusCanceled && experience.Subscription.EffectiveUntil == nil {
				t.Fatal("canceled subscription should carry usable-until date")
			}
		})
	}
}

func TestOfficialHubSubscriptionExperienceQuotasCatalogAndRelayReminder(t *testing.T) {
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	mgr, hubURL, accountID, postWebhook := subscriptionExperienceHub(t, now, cloudhub.PlanPersonal)
	postWebhook(t, accountID, cloudhub.PlanPersonal, cloudhub.SubscriptionStatusActive, nil, 1)

	client := cloudhub.Client{BaseURL: hubURL, HTTPClient: mgr.HTTPClient}
	network, err := client.CreateNetwork(context.Background(), cloudhub.CreateNetworkRequest{AccountID: accountID, Name: "Home"})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := client.CreateInvite(context.Background(), cloudhub.CreateInviteRequest{AccountID: accountID, NetworkID: network.ID, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.JoinDevice(context.Background(), cloudhub.JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "desk"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.JoinDevice(context.Background(), cloudhub.JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.HeartbeatDevice(context.Background(), cloudhub.HeartbeatDeviceRequest{DeviceID: first.ID, Status: cloudhub.DeviceStatusOnline}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.writeOfficialHubState(OfficialHubState{
		HubAPIURL: hubURL,
		AccountID: accountID,
		NetworkID: network.ID,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateRelaySession(context.Background(), cloudhub.CreateRelaySessionRequest{
		AccountID:      accountID,
		NetworkID:      network.ID,
		SourceDeviceID: first.ID,
		TargetDeviceID: second.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RecordRelaySessionUsage(context.Background(), cloudhub.RecordRelaySessionUsageRequest{
		SessionID:    session.Session.ID,
		RelayBytesIn: 8 * 1024 * 1024,
	}); err != nil {
		t.Fatal(err)
	}

	experience, err := mgr.OfficialHubSubscriptionExperience(context.Background(), OfficialHubSubscriptionExperienceRequest{
		HubAPIURL: hubURL,
	})
	if err != nil {
		t.Fatalf("OfficialHubSubscriptionExperience returned error: %v", err)
	}
	assertQuota := func(dimension cloudhub.QuotaDimension, used int64, limit int64, remaining int64) {
		t.Helper()
		quota := findExperienceQuota(t, experience.Quotas, dimension)
		if quota.Used != used || quota.Limit == nil || *quota.Limit != limit || quota.Remaining == nil || *quota.Remaining != remaining {
			t.Fatalf("%s quota = %+v, want used=%d limit=%d remaining=%d", dimension, quota, used, limit, remaining)
		}
		quotaText := quota.Message + quota.Impact + quota.Recommendation
		if strings.Contains(quotaText, "请检查网络") || strings.Contains(quotaText, "网络故障") {
			t.Fatalf("%s quota text should not masquerade as a network failure: %+v", dimension, quota)
		}
	}
	assertQuota(cloudhub.QuotaDimensionDeviceCount, 2, 3, 1)
	assertQuota(cloudhub.QuotaDimensionConcurrentOnlineDevices, 1, 2, 1)
	assertQuota(cloudhub.QuotaDimensionOfficialRelayTraffic, 8*1024*1024, 10*1024*1024, 2*1024*1024)
	if experience.RelayUsageReminder == nil || experience.RelayUsageReminder.Level != "approaching" ||
		experience.RelayUsageReminder.Title != "接近中继流量上限" {
		t.Fatalf("relay reminder = %+v, want reused 80%% reminder", experience.RelayUsageReminder)
	}

	plans := map[cloudhub.PlanID]OfficialHubPlanComparison{}
	for _, plan := range experience.PlanComparison {
		plans[plan.ID] = plan
	}
	for _, id := range []cloudhub.PlanID{cloudhub.PlanFree, cloudhub.PlanPersonal, cloudhub.PlanFamily, cloudhub.PlanTeam, cloudhub.PlanEnterprise} {
		if _, ok := plans[id]; !ok {
			t.Fatalf("plan comparison missing %s: %+v", id, experience.PlanComparison)
		}
	}
	if !strings.Contains(plans[cloudhub.PlanPersonal].DeviceCount, "3 台") {
		t.Fatalf("personal device count = %q, want 3 devices", plans[cloudhub.PlanPersonal].DeviceCount)
	}
	if !strings.Contains(plans[cloudhub.PlanFamily].DeviceCount, "10 台") {
		t.Fatalf("family device count = %q, want 10 devices", plans[cloudhub.PlanFamily].DeviceCount)
	}
	if !strings.Contains(plans[cloudhub.PlanFree].OfficialRelayTraffic, "不可用") ||
		!strings.Contains(plans[cloudhub.PlanFree].SelfHostedServer, "不限制") ||
		!strings.Contains(plans[cloudhub.PlanFree].SelfHostedRelay, "不限制") {
		t.Fatalf("free plan comparison should preserve self-hosted free capability: %+v", plans[cloudhub.PlanFree])
	}
	if !strings.Contains(plans[cloudhub.PlanTeam].DeviceCount, "需配置") ||
		!strings.Contains(plans[cloudhub.PlanEnterprise].DeviceCount, "按合同") {
		t.Fatalf("commercial plan comparison invented or hid custom quota semantics: team=%+v enterprise=%+v", plans[cloudhub.PlanTeam], plans[cloudhub.PlanEnterprise])
	}
	if experience.UpgradeEntry.TargetView != "plan_comparison" ||
		strings.Contains(experience.UpgradeEntry.Message, "支付") ||
		strings.Contains(experience.UpgradeEntry.Message, "价格") {
		t.Fatalf("upgrade placeholder = %+v, want local comparison placeholder without payment promise", experience.UpgradeEntry)
	}
}

func TestOfficialHubSubscriptionExperienceDegradesForMissingLegacyAndUnreachableHub(t *testing.T) {
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	t.Run("hub not configured", func(t *testing.T) {
		mgr := Manager{BaseDir: t.TempDir()}
		experience, err := mgr.OfficialHubSubscriptionExperience(context.Background(), OfficialHubSubscriptionExperienceRequest{})
		if err != nil {
			t.Fatalf("OfficialHubSubscriptionExperience returned error: %v", err)
		}
		if !experience.Degraded || experience.DegradeReason != "hub_not_configured" || experience.CurrentPlan.ID != cloudhub.PlanFree {
			t.Fatalf("experience = %+v, want stable not-configured free fallback", experience)
		}
		if !strings.Contains(experience.Subscription.Message, "自建服务器") ||
			!strings.Contains(experience.Subscription.Message, "自建 Relay") {
			t.Fatalf("message = %q, want self-hosted continuity", experience.Subscription.Message)
		}
	})
	t.Run("legacy account without subscription", func(t *testing.T) {
		mgr, hubURL, accountID, _ := subscriptionExperienceHub(t, now, cloudhub.PlanPersonal)
		experience, err := mgr.OfficialHubSubscriptionExperience(context.Background(), OfficialHubSubscriptionExperienceRequest{HubAPIURL: hubURL})
		if err != nil {
			t.Fatalf("OfficialHubSubscriptionExperience returned error: %v", err)
		}
		if experience.Subscription.State != "legacy_no_subscription" || experience.CurrentPlan.ID != cloudhub.PlanPersonal {
			t.Fatalf("experience = %+v, want legacy subscription fallback preserving current plan", experience)
		}
		if !strings.Contains(experience.Subscription.Message, "旧账号") {
			t.Fatalf("message = %q, want legacy account text", experience.Subscription.Message)
		}
		if experience.State.AccountID != accountID {
			t.Fatalf("state overwritten: %+v", experience.State)
		}
	})
	t.Run("hub unreachable", func(t *testing.T) {
		mgr := Manager{BaseDir: t.TempDir()}
		closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		hubURL := closed.URL
		closed.Close()
		if err := mgr.writeOfficialHubState(OfficialHubState{HubAPIURL: hubURL, AccountID: "acct_legacy", UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		experience, err := mgr.OfficialHubSubscriptionExperience(context.Background(), OfficialHubSubscriptionExperienceRequest{})
		if err != nil {
			t.Fatalf("OfficialHubSubscriptionExperience returned error: %v", err)
		}
		if !experience.Degraded || experience.DegradeReason != "hub_unreachable" || experience.CurrentPlan.ID != cloudhub.PlanFree {
			t.Fatalf("experience = %+v, want unreachable fallback", experience)
		}
		state, err := mgr.OfficialHubState()
		if err != nil {
			t.Fatal(err)
		}
		if state.AccountID != "acct_legacy" || state.HubAPIURL != hubURL {
			t.Fatalf("subscription experience must not overwrite onboarding state: %+v", state)
		}
	})
}

func findExperienceQuota(t *testing.T, quotas []OfficialHubQuotaSummary, dimension cloudhub.QuotaDimension) OfficialHubQuotaSummary {
	t.Helper()
	for _, quota := range quotas {
		if quota.Dimension == dimension {
			return quota
		}
	}
	t.Fatalf("quota %s missing from %+v", dimension, quotas)
	return OfficialHubQuotaSummary{}
}

func subscriptionExperienceHub(t *testing.T, now time.Time, planID cloudhub.PlanID) (Manager, string, string, func(*testing.T, string, cloudhub.PlanID, cloudhub.SubscriptionStatus, *time.Time, int64)) {
	t.Helper()
	secret := []byte("task-9d-secret")
	store := cloudhub.NewMemoryStore()
	svc := cloudhub.NewService(store,
		cloudhub.WithNow(func() time.Time { return now }),
		cloudhub.WithSubscriptionProviders(cloudhub.NewHMACSubscriptionProvider("test-provider", secret, 5*time.Minute)),
		cloudhub.WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.personal.concurrent_online_devices":      2,
			"cloudhub.plans.personal.official_relay_bytes_per_month": 10 * 1024 * 1024,
			"cloudhub.plans.personal.audit_log_retention_days":       30,
			"cloudhub.plans.family.concurrent_online_devices":        5,
			"cloudhub.plans.family.official_relay_bytes_per_month":   50 * 1024 * 1024,
			"cloudhub.plans.family.members":                          5,
			"cloudhub.plans.family.audit_log_retention_days":         90,
			"cloudhub.plans.team.devices":                            20,
			"cloudhub.plans.team.concurrent_online_devices":          10,
			"cloudhub.plans.team.official_relay_bytes_per_month":     100 * 1024 * 1024,
			"cloudhub.plans.team.members":                            10,
			"cloudhub.plans.team.audit_log_retention_days":           180,
		}),
	)
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	t.Cleanup(hub.Close)
	ctx := context.Background()
	account, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{
		Email:  "owner@example.com",
		PlanID: planID,
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr := Manager{BaseDir: t.TempDir(), HTTPClient: hub.Client()}
	if err := mgr.writeOfficialHubState(OfficialHubState{
		HubAPIURL:    hub.URL,
		AccountID:    account.ID,
		AccountEmail: account.Email,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	postWebhook := func(t *testing.T, accountID string, planID cloudhub.PlanID, status cloudhub.SubscriptionStatus, expires *time.Time, version int64) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"event_id":                 "evt_" + string(status) + "_" + strconv.FormatInt(version, 10),
			"type":                     "subscription.updated",
			"account_id":               accountID,
			"plan_id":                  planID,
			"status":                   status,
			"provider_subscription_id": "sub_" + strconv.FormatInt(version, 10),
			"effective_at":             now.Format(time.RFC3339),
			"effective_until":          formatOptionalTime(expires),
			"version":                  version,
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, hub.URL+"/api/subscriptions/webhooks/test-provider", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CloudHub-Timestamp", strconv.FormatInt(now.Unix(), 10))
		req.Header.Set("X-CloudHub-Signature", "v1="+subscriptionExperienceSignature(secret, now.Unix(), body))
		resp, err := hub.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook returned %d", resp.StatusCode)
		}
	}
	return mgr, hub.URL, account.ID, postWebhook
}

func subscriptionExperienceSignature(secret []byte, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func timePtr(t time.Time) *time.Time {
	t = t.UTC()
	return &t
}
