package cloudhub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubscriptionWebhookHMACVerificationAndRejectionDoNotMutatePlan(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	secret := []byte("task-9c-local-hmac-secret")
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithSubscriptionProviders(NewHMACSubscriptionProvider("fake-hmac", secret, 5*time.Minute)),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "sub-hmac@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	body := subscriptionWebhookBody(t, map[string]any{
		"event_id":                 "evt_active_verified",
		"type":                     "subscription.updated",
		"account_id":               account.ID,
		"plan_id":                  string(PlanFamily),
		"status":                   string(SubscriptionStatusActive),
		"provider_subscription_id": "sub_fake_verified",
		"provider_customer_id":     "cus_fake_verified",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})
	resp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid webhook status = %d body=%s, want 200", resp.StatusCode, resp.Body)
	}
	account = mustStoreAccount(t, ctx, store, account.ID)
	if account.PlanID != PlanFamily {
		t.Fatalf("account plan after valid webhook = %q, want family", account.PlanID)
	}

	tampered := bytes.ReplaceAll(body, []byte(string(PlanFamily)), []byte(string(PlanTeam)))
	tamperedReq := signedSubscriptionRequest(t, server.URL+"/api/subscriptions/webhooks/fake-hmac", secret, now, body)
	tamperedReq.Body = ioNopCloser(tampered)
	tamperedReq.ContentLength = int64(len(tampered))
	tamperedResp := doHTTP(t, server.Client(), tamperedReq)
	if tamperedResp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered webhook status = %d body=%s, want 403", tamperedResp.StatusCode, tamperedResp.Body)
	}

	missingSignatureReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/subscriptions/webhooks/fake-hmac", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest missing signature: %v", err)
	}
	missingSignatureReq.Header.Set("X-CloudHub-Timestamp", fmt.Sprintf("%d", now.Unix()))
	missingSignatureResp := doHTTP(t, server.Client(), missingSignatureReq)
	if missingSignatureResp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing signature status = %d body=%s, want 403", missingSignatureResp.StatusCode, missingSignatureResp.Body)
	}

	expiredResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now.Add(-10*time.Minute), body)
	if expiredResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expired timestamp status = %d body=%s, want 403", expiredResp.StatusCode, expiredResp.Body)
	}
	futureResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now.Add(10*time.Minute), body)
	if futureResp.StatusCode != http.StatusForbidden {
		t.Fatalf("future timestamp status = %d body=%s, want 403", futureResp.StatusCode, futureResp.Body)
	}
	unknownProviderResp := postSignedSubscriptionWebhook(t, server, "unknown", secret, now, body)
	if unknownProviderResp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d body=%s, want 404", unknownProviderResp.StatusCode, unknownProviderResp.Body)
	}
	oversized := bytes.Repeat([]byte("x"), int(MaxSubscriptionWebhookBodyBytes)+1)
	oversizedResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, oversized)
	if oversizedResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d body=%s, want 413", oversizedResp.StatusCode, oversizedResp.Body)
	}

	account = mustStoreAccount(t, ctx, store, account.ID)
	if account.PlanID != PlanFamily {
		t.Fatalf("account plan after rejected webhooks = %q, want unchanged family", account.PlanID)
	}
	events, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	if !hasAuditEvent(events, AuditSubscriptionWebhookRejected) {
		t.Fatalf("audit events = %+v, want rejected webhook audit", events)
	}
	assertNoSubscriptionSensitiveJSON(t, events)
}

func TestSubscriptionWebhookRejectsUnknownAccountPlanAndProvider(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	secret := []byte("task-9c-unknown-secret")
	svc := NewService(NewMemoryStore(),
		WithNow(func() time.Time { return now }),
		WithSubscriptionProviders(NewHMACSubscriptionProvider("fake-hmac", secret, 5*time.Minute)),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	unknownAccountBody := subscriptionWebhookBody(t, map[string]any{
		"event_id":                 "evt_unknown_account",
		"type":                     "subscription.updated",
		"account_id":               "acct_missing",
		"plan_id":                  string(PlanFamily),
		"status":                   string(SubscriptionStatusActive),
		"provider_subscription_id": "sub_unknown_account",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})
	resp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, unknownAccountBody)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown account status = %d body=%s, want 404", resp.StatusCode, resp.Body)
	}

	account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "sub-unknown-plan@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	unknownPlanBody := subscriptionWebhookBody(t, map[string]any{
		"event_id":                 "evt_unknown_plan",
		"type":                     "subscription.updated",
		"account_id":               account.ID,
		"plan_id":                  "galaxy",
		"status":                   string(SubscriptionStatusActive),
		"provider_subscription_id": "sub_unknown_plan",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})
	resp = postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, unknownPlanBody)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown plan status = %d body=%s, want 404", resp.StatusCode, resp.Body)
	}

	status, err := svc.GetAccountSubscriptionStatus(ctx, account.ID)
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("subscription status = %+v err=%v, want no subscription after rejected events", status, err)
	}
	stored := mustStoreAccount(t, ctx, svc.store, account.ID)
	if stored.PlanID != PlanPersonal {
		t.Fatalf("account plan after rejected unknown plan = %q, want original personal", stored.PlanID)
	}
}

func TestPendingPastDueDoNotExpandAndMissingSubscriptionKeepsLegacyPlan(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 10, 30, 0, 0, time.UTC)
	secret := []byte("task-9c-pending-secret")
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithSubscriptionProviders(NewHMACSubscriptionProvider("fake-hmac", secret, 5*time.Minute)),
		WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.personal.concurrent_online_devices": 1,
			"cloudhub.plans.family.concurrent_online_devices":   10,
		}),
		WithAccountPolicies("legacy-subscription-test", AccountPolicy{
			Name:                   "legacy-subscription-test",
			RelayBytesQuota:        123,
			MaxActiveRelaySessions: 4,
			MaxRelaySessionsPerDay: 8,
		}),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	legacy, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "legacy-no-subscription@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount legacy returned error: %v", err)
	}
	if status, err := svc.GetAccountPolicyStatus(ctx, legacy.ID); err != nil || status.Policy.Name != "legacy-subscription-test" {
		t.Fatalf("legacy policy status = %+v err=%v, want existing AccountPolicy behavior", status, err)
	}
	if got := mustStoreAccount(t, ctx, store, legacy.ID).PlanID; got != "" {
		t.Fatalf("legacy account without subscription plan_id = %q, want unchanged empty plan", got)
	}

	account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "pending-no-expand@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	pendingBody := subscriptionWebhookBody(t, subscriptionEventMap(account.ID, "evt_pending_no_expand", "sub_pending_no_expand", PlanFamily, SubscriptionStatusPending, now, nil, 1))
	pendingResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, pendingBody)
	if pendingResp.StatusCode != http.StatusOK {
		t.Fatalf("pending webhook status=%d body=%s, want 200", pendingResp.StatusCode, pendingResp.Body)
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanPersonal {
		t.Fatalf("account plan after pending family event = %q, want unchanged personal", got)
	}

	pastDueBody := subscriptionWebhookBody(t, subscriptionEventMap(account.ID, "evt_past_due_no_expand", "sub_pending_no_expand", PlanFamily, SubscriptionStatusPastDue, now.Add(time.Minute), nil, 2))
	pastDueResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, pastDueBody)
	if pastDueResp.StatusCode != http.StatusOK {
		t.Fatalf("past_due webhook status=%d body=%s, want 200", pastDueResp.StatusCode, pastDueResp.Body)
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanPersonal {
		t.Fatalf("account plan after past_due family event = %q, want unchanged personal", got)
	}

	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "PendingQuota"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	devices := mustJoinDevices(t, ctx, svc, account.ID, network.ID, 2)
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: devices[0].ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice first pending-plan device returned error: %v", err)
	}
	_, err = svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: devices[1].ID, Status: DeviceStatusOnline})
	assertQuotaExceeded(t, err, QuotaDimensionConcurrentOnlineDevices, 1, 1)
}

func TestSubscriptionEventsAreIdempotentConflictAwareAndConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC)
	secret := []byte("task-9c-idempotency-secret")
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithSubscriptionProviders(NewHMACSubscriptionProvider("fake-hmac", secret, 5*time.Minute)),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "sub-idempotent@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}

	body := subscriptionWebhookBody(t, map[string]any{
		"event_id":                 "evt_replay",
		"type":                     "subscription.updated",
		"account_id":               account.ID,
		"plan_id":                  string(PlanFamily),
		"status":                   string(SubscriptionStatusActive),
		"provider_subscription_id": "sub_replay",
		"provider_customer_id":     "cus_replay",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})
	first := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, body)
	if first.StatusCode != http.StatusOK || first.Outcome != string(SubscriptionEventAccepted) {
		t.Fatalf("first event status=%d outcome=%q body=%s, want accepted", first.StatusCode, first.Outcome, first.Body)
	}
	duplicate := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, body)
	if duplicate.StatusCode != http.StatusOK || duplicate.Outcome != string(SubscriptionEventRepeated) {
		t.Fatalf("duplicate event status=%d outcome=%q body=%s, want repeated", duplicate.StatusCode, duplicate.Outcome, duplicate.Body)
	}
	conflictingBody := subscriptionWebhookBody(t, map[string]any{
		"event_id":                 "evt_replay",
		"type":                     "subscription.updated",
		"account_id":               account.ID,
		"plan_id":                  string(PlanTeam),
		"status":                   string(SubscriptionStatusActive),
		"provider_subscription_id": "sub_replay",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})
	conflict := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, conflictingBody)
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting event status=%d body=%s, want 409", conflict.StatusCode, conflict.Body)
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanFamily {
		t.Fatalf("account plan after replay/conflict = %q, want family", got)
	}

	concurrentAccount, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "sub-concurrent@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount concurrent returned error: %v", err)
	}
	concurrentBody := subscriptionWebhookBody(t, map[string]any{
		"event_id":                 "evt_concurrent_replay",
		"type":                     "subscription.updated",
		"account_id":               concurrentAccount.ID,
		"plan_id":                  string(PlanFamily),
		"status":                   string(SubscriptionStatusActive),
		"provider_subscription_id": "sub_concurrent_replay",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})
	const workers = 24
	var wg sync.WaitGroup
	outcomes := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, concurrentBody)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("concurrent replay status=%d body=%s, want 200", resp.StatusCode, resp.Body)
				return
			}
			outcomes <- resp.Outcome
		}()
	}
	wg.Wait()
	close(outcomes)
	var accepted, repeated int
	for outcome := range outcomes {
		switch SubscriptionEventOutcome(outcome) {
		case SubscriptionEventAccepted:
			accepted++
		case SubscriptionEventRepeated:
			repeated++
		default:
			t.Fatalf("unexpected concurrent outcome %q", outcome)
		}
	}
	if accepted != 1 || repeated != workers-1 {
		t.Fatalf("concurrent outcomes accepted=%d repeated=%d, want 1/%d", accepted, repeated, workers-1)
	}
	if got := mustStoreAccount(t, ctx, store, concurrentAccount.ID).PlanID; got != PlanFamily {
		t.Fatalf("concurrent account plan = %q, want family", got)
	}
	events, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	if countAuditEvents(events, AuditSubscriptionStatusChanged, concurrentAccount.ID) != 1 {
		t.Fatalf("status changed audits = %+v, want exactly one status change for concurrent replay", events)
	}
	assertNoSubscriptionSensitiveJSON(t, events)
}

func TestSubscriptionStateMachinePreventsRollbackAndExpiresSafely(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	secret := []byte("task-9c-state-secret")
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithSubscriptionProviders(NewHMACSubscriptionProvider("fake-hmac", secret, 5*time.Minute)),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "sub-state@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}

	active := subscriptionEventMap(account.ID, "evt_state_active", "sub_state", PlanFamily, SubscriptionStatusActive, now, nil, 2)
	if resp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, subscriptionWebhookBody(t, active)); resp.StatusCode != http.StatusOK {
		t.Fatalf("active webhook status=%d body=%s, want 200", resp.StatusCode, resp.Body)
	}
	oldPending := subscriptionEventMap(account.ID, "evt_state_old_pending", "sub_state", PlanFamily, SubscriptionStatusPending, now.Add(-time.Hour), nil, 1)
	oldPendingResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, subscriptionWebhookBody(t, oldPending))
	if oldPendingResp.StatusCode != http.StatusOK || oldPendingResp.Outcome != string(SubscriptionEventStale) {
		t.Fatalf("old pending status=%d outcome=%q body=%s, want stale", oldPendingResp.StatusCode, oldPendingResp.Outcome, oldPendingResp.Body)
	}
	status, err := svc.GetAccountSubscriptionStatus(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountSubscriptionStatus returned error: %v", err)
	}
	if status.Status != SubscriptionStatusActive || mustStoreAccount(t, ctx, store, account.ID).PlanID != PlanFamily {
		t.Fatalf("status/account after stale old event = %+v/%+v, want active family", status, mustStoreAccount(t, ctx, store, account.ID))
	}

	pastDue := subscriptionEventMap(account.ID, "evt_state_past_due", "sub_state", PlanFamily, SubscriptionStatusPastDue, now, nil, 3)
	pastDueResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, subscriptionWebhookBody(t, pastDue))
	if pastDueResp.StatusCode != http.StatusOK || pastDueResp.Outcome != string(SubscriptionEventAccepted) {
		t.Fatalf("past_due status=%d outcome=%q body=%s, want accepted", pastDueResp.StatusCode, pastDueResp.Outcome, pastDueResp.Body)
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanFree {
		t.Fatalf("account plan after past_due = %q, want fallback free", got)
	}

	cancelExpires := now.Add(2 * time.Hour)
	canceled := subscriptionEventMap(account.ID, "evt_state_canceled", "sub_state", PlanFamily, SubscriptionStatusCanceled, now, &cancelExpires, 4)
	cancelResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, subscriptionWebhookBody(t, canceled))
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("canceled status=%d body=%s, want 200", cancelResp.StatusCode, cancelResp.Body)
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanFamily {
		t.Fatalf("account plan during canceled valid period = %q, want family", got)
	}

	svc.SetNowForTest(func() time.Time { return cancelExpires.Add(time.Second) })
	status, err = svc.GetAccountSubscriptionStatus(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountSubscriptionStatus after expiry returned error: %v", err)
	}
	if status.Status != SubscriptionStatusExpired {
		t.Fatalf("status after natural expiry = %+v, want expired", status)
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanFree {
		t.Fatalf("account plan after natural expiry = %q, want fallback free", got)
	}
}

func TestSubscriptionActivationExpirationLinksToPlanQuotaEnforcement(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)
	secret := []byte("task-9c-quota-secret")
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithSubscriptionProviders(NewHMACSubscriptionProvider("fake-hmac", secret, 5*time.Minute)),
		WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.personal.concurrent_online_devices":      1,
			"cloudhub.plans.personal.official_relay_bytes_per_month": 5,
			"cloudhub.plans.family.concurrent_online_devices":        10,
			"cloudhub.plans.family.official_relay_bytes_per_month":   20,
		}),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "sub-quota@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "SubscriptionQuota"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}

	activeBody := subscriptionWebhookBody(t, subscriptionEventMap(account.ID, "evt_quota_active", "sub_quota", PlanFamily, SubscriptionStatusActive, now, nil, 1))
	for i := 0; i < 3; i++ {
		resp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, activeBody)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("active replay %d status=%d body=%s, want 200", i+1, resp.StatusCode, resp.Body)
		}
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanFamily {
		t.Fatalf("account plan after active subscription = %q, want family", got)
	}

	devices := mustJoinDevices(t, ctx, svc, account.ID, network.ID, 10)
	extraInvite, err := svc.CreateInvite(ctx, CreateInviteRequest{AccountID: account.ID, NetworkID: network.ID})
	if err != nil {
		t.Fatalf("CreateInvite extra returned error: %v", err)
	}
	_, err = svc.JoinDevice(ctx, JoinDeviceRequest{Token: extraInvite.Token, Code: extraInvite.Code, DeviceName: "device-over-family"})
	assertQuotaExceeded(t, err, QuotaDimensionDeviceCount, 10, 10)

	for _, device := range devices[:2] {
		if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: device.ID, Status: DeviceStatusOnline}); err != nil {
			t.Fatalf("HeartbeatDevice family online returned error: %v", err)
		}
	}
	result, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[0].ID,
		TargetDeviceID: devices[1].ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession under family returned error: %v", err)
	}
	if _, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID:     result.Session.ID,
		RelayBytesIn:  6,
		RelayBytesOut: 4,
	}); err != nil {
		t.Fatalf("CloseRelaySession under family returned error: %v", err)
	}

	expiredBody := subscriptionWebhookBody(t, subscriptionEventMap(account.ID, "evt_quota_expired", "sub_quota", PlanFamily, SubscriptionStatusExpired, now.Add(time.Hour), nil, 2))
	expiredResp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, expiredBody)
	if expiredResp.StatusCode != http.StatusOK {
		t.Fatalf("expired webhook status=%d body=%s, want 200", expiredResp.StatusCode, expiredResp.Body)
	}
	if got := mustStoreAccount(t, ctx, store, account.ID).PlanID; got != PlanFree {
		t.Fatalf("account plan after expired subscription = %q, want fallback free", got)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: devices[2].ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice after free fallback returned error: %v", err)
	}
	_, err = svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[0].ID,
		TargetDeviceID: devices[1].ID,
	})
	assertQuotaExceeded(t, err, QuotaDimensionOfficialRelayTraffic, 10, 0)
}

func TestSubscriptionStatusHTTPClientAndAuditDoNotExposeInternalFields(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 14, 0, 0, 0, time.UTC)
	secret := []byte("task-9c-public-secret")
	svc := NewService(NewMemoryStore(),
		WithNow(func() time.Time { return now }),
		WithSubscriptionProviders(NewHMACSubscriptionProvider("fake-hmac", secret, 5*time.Minute)),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}
	account, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "sub-public@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	body := subscriptionWebhookBody(t, map[string]any{
		"event_id":                 "evt_public_active",
		"type":                     "subscription.updated",
		"account_id":               account.ID,
		"plan_id":                  string(PlanFamily),
		"status":                   string(SubscriptionStatusActive),
		"provider_subscription_id": "sub_public_secret_ref",
		"provider_customer_id":     "cus_public_secret_ref",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})
	if resp := postSignedSubscriptionWebhook(t, server, "fake-hmac", secret, now, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("valid webhook status=%d body=%s, want 200", resp.StatusCode, resp.Body)
	}

	status, err := client.GetAccountSubscriptionStatus(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountSubscriptionStatus returned error: %v", err)
	}
	if status.Status != SubscriptionStatusActive || status.PlanID != PlanFamily || status.EffectiveAt == nil || !status.EffectiveAt.Equal(now) {
		t.Fatalf("subscription status = %+v, want active family with effective_at", status)
	}
	raw := getRaw(t, server.Client(), server.URL+"/api/accounts/"+account.ID+"/subscription")
	assertNoSubscriptionSensitiveBytes(t, []byte(raw))
	for _, allowed := range []string{"plan_id", "status", "effective_at"} {
		if !strings.Contains(raw, allowed) {
			t.Fatalf("subscription response %s missing public field %q", raw, allowed)
		}
	}

	events, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	if !hasAuditEvent(events, AuditSubscriptionWebhookAccepted) ||
		!hasAuditEvent(events, AuditSubscriptionStatusChanged) {
		t.Fatalf("audit events = %+v, want accepted and status changed", events)
	}
	assertNoSubscriptionSensitiveJSON(t, events)
}

func subscriptionEventMap(accountID, eventID, providerSubscriptionID string, planID PlanID, status SubscriptionStatus, effectiveAt time.Time, expiresAt *time.Time, version int64) map[string]any {
	out := map[string]any{
		"event_id":                 eventID,
		"type":                     "subscription.updated",
		"account_id":               accountID,
		"plan_id":                  string(planID),
		"status":                   string(status),
		"provider_subscription_id": providerSubscriptionID,
		"provider_customer_id":     "cus_" + providerSubscriptionID,
		"effective_at":             effectiveAt.Format(time.RFC3339),
		"version":                  version,
	}
	if expiresAt != nil {
		out["expires_at"] = expiresAt.Format(time.RFC3339)
	}
	return out
}

func subscriptionWebhookBody(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal subscription webhook body: %v", err)
	}
	return body
}

type subscriptionWebhookHTTPResponse struct {
	StatusCode int
	Body       string
	Outcome    string
}

func postSignedSubscriptionWebhook(t *testing.T, server *httptest.Server, provider string, secret []byte, timestamp time.Time, body []byte) subscriptionWebhookHTTPResponse {
	t.Helper()
	req := signedSubscriptionRequest(t, server.URL+"/api/subscriptions/webhooks/"+provider, secret, timestamp, body)
	return doHTTP(t, server.Client(), req)
}

func signedSubscriptionRequest(t *testing.T, url string, secret []byte, timestamp time.Time, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CloudHub-Timestamp", fmt.Sprintf("%d", timestamp.Unix()))
	req.Header.Set("X-CloudHub-Signature", "v1="+subscriptionHMACSignature(secret, timestamp, body))
	return req
}

func subscriptionHMACSignature(secret []byte, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.", timestamp.Unix())))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func doHTTP(t *testing.T, client *http.Client, req *http.Request) subscriptionWebhookHTTPResponse {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	out := subscriptionWebhookHTTPResponse{StatusCode: resp.StatusCode, Body: buf.String()}
	var decoded struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(buf.Bytes(), &decoded)
	out.Outcome = decoded.Outcome
	return out
}

func getRaw(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read GET body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s, want 200", url, resp.StatusCode, buf.String())
	}
	return buf.String()
}

func mustStoreAccount(t *testing.T, ctx context.Context, store Store, accountID string) Account {
	t.Helper()
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("GetAccount %s returned error: %v", accountID, err)
	}
	return account
}

func countAuditEvents(events []AuditEvent, event string, accountID string) int {
	var count int
	for _, got := range events {
		if got.Event == event && got.AccountID == accountID {
			count++
		}
	}
	return count
}

func ioNopCloser(body []byte) *nopReadCloser {
	return &nopReadCloser{Reader: bytes.NewReader(body)}
}

type nopReadCloser struct {
	*bytes.Reader
}

func (n *nopReadCloser) Close() error {
	return nil
}

func assertNoSubscriptionSensitiveJSON(t *testing.T, v any) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal subscription value: %v", err)
	}
	assertNoSubscriptionSensitiveBytes(t, encoded)
}

func assertNoSubscriptionSensitiveBytes(t *testing.T, encoded []byte) {
	t.Helper()
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{
		"signature",
		"secret",
		"payload",
		"digest",
		"raw_body",
		"provider_customer",
		"provider_subscription",
		"payment",
		"card",
		"checkout",
		"token",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("subscription output leaks forbidden field %q: %s", forbidden, encoded)
		}
	}
}
