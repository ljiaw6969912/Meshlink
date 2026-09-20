package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestPlanQuotaEvaluatorModesAreExplicit(t *testing.T) {
	evaluator := NewPlanQuotaEvaluator(PlanQuotaEvaluatorConfig{
		OperatorLimits: map[string]int64{
			"configured.devices": 7,
		},
		ContractLimits: map[string]int64{
			"acct_contract:device_count": 11,
		},
	})

	cases := []struct {
		name      string
		accountID string
		dimension QuotaDimension
		quota     PlanQuota
		want      QuotaDecision
		wantErr   string
	}{
		{
			name:      "unavailable",
			dimension: QuotaDimensionOfficialRelayTraffic,
			quota:     UnavailablePlanQuota(PlanQuotaUnitBytesPerMonth),
			want:      QuotaDecision{Mode: PlanQuotaUnavailable, Limited: true, Limit: 0},
		},
		{
			name:      "built in finite",
			dimension: QuotaDimensionDeviceCount,
			quota:     LimitedPlanQuota(3, PlanQuotaUnitDevices),
			want:      QuotaDecision{Mode: PlanQuotaLimited, Limited: true, Limit: 3},
		},
		{
			name:      "operator configured finite",
			dimension: QuotaDimensionDeviceCount,
			quota:     ConfiguredLimitedPlanQuota(PlanQuotaUnitDevices, "configured.devices"),
			want:      QuotaDecision{Mode: PlanQuotaLimited, Limited: true, Limit: 7},
		},
		{
			name:      "unlimited",
			dimension: QuotaDimensionDeviceCount,
			quota:     UnlimitedPlanQuota(PlanQuotaUnitDevices),
			want:      QuotaDecision{Mode: PlanQuotaUnlimited},
		},
		{
			name:      "contract custom configured",
			accountID: "acct_contract",
			dimension: QuotaDimensionDeviceCount,
			quota:     ContractCustomPlanQuota(PlanQuotaUnitDevices),
			want:      QuotaDecision{Mode: PlanQuotaContractCustom, Limited: true, Limit: 11},
		},
		{
			name:      "contract custom unresolved",
			accountID: "acct_missing_contract",
			dimension: QuotaDimensionDeviceCount,
			quota:     ContractCustomPlanQuota(PlanQuotaUnitDevices),
			wantErr:   "contract custom quota is not configured",
		},
		{
			name:      "operator configured unresolved",
			dimension: QuotaDimensionConcurrentOnlineDevices,
			quota:     ConfiguredLimitedPlanQuota(PlanQuotaUnitDevices, "missing.devices"),
			wantErr:   "operator configured quota is not configured",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evaluator.Evaluate(tc.accountID, tc.dimension, tc.quota)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, ErrQuotaExceeded) {
					t.Fatalf("Evaluate error = %v, want ErrQuotaExceeded containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Evaluate returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("decision = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPlanDeviceLimitRejectsJoinAtomically(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:  "plan-devices@example.com",
		PlanID: PlanPersonal,
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "PlanNet"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	invite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   4,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.JoinDevice(ctx, JoinDeviceRequest{
			Token:      invite.Token,
			Code:       invite.Code,
			DeviceName: "device-ok",
		}); err != nil {
			t.Fatalf("JoinDevice %d returned error: %v", i+1, err)
		}
	}

	_, err = svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:      invite.Token,
		Code:       invite.Code,
		DeviceName: "device-over-limit",
	})
	assertQuotaExceeded(t, err, QuotaDimensionDeviceCount, 3, 3)

	devices, err := svc.ListNetworkDevices(ctx, network.ID)
	if err != nil {
		t.Fatalf("ListNetworkDevices returned error: %v", err)
	}
	if len(devices) != 3 {
		t.Fatalf("devices = %+v, want exactly the original three devices", devices)
	}
	storedInvite, err := svc.GetInvite(ctx, invite.ID)
	if err != nil {
		t.Fatalf("GetInvite returned error: %v", err)
	}
	if storedInvite.Uses != 3 || storedInvite.UsedAt == nil {
		t.Fatalf("invite = %+v, want failed join not to consume invite use", storedInvite)
	}
	events, err := svc.ListRiskEvents(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	assertQuotaRiskEvent(t, events, QuotaDimensionDeviceCount)
	assertNoSensitiveJSON(t, events)
}

func TestPlanConcurrentOnlineLimitCountsOnlyOfflineToOnlineTransitions(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore(), WithPlanQuotaLimits(map[string]int64{
		"cloudhub.plans.personal.concurrent_online_devices": 1,
	}))
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:  "plan-online@example.com",
		PlanID: PlanPersonal,
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "OnlineNet"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	devices := mustJoinDevices(t, ctx, svc, account.ID, network.ID, 2)

	firstOnline, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: devices[0].ID,
		Status:   DeviceStatusOnline,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice first online returned error: %v", err)
	}
	repeated, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: devices[0].ID,
		Status:   DeviceStatusOnline,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice repeated online returned error: %v", err)
	}
	if repeated.ID != firstOnline.ID || repeated.Status != DeviceStatusOnline {
		t.Fatalf("repeated heartbeat = %+v, want same online device", repeated)
	}

	_, err = svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: devices[1].ID,
		Status:   DeviceStatusOnline,
	})
	assertQuotaExceeded(t, err, QuotaDimensionConcurrentOnlineDevices, 1, 1)
	stillOffline, err := svc.store.GetDevice(ctx, devices[1].ID)
	if err != nil {
		t.Fatalf("GetDevice returned error: %v", err)
	}
	if stillOffline.Status != DeviceStatusOffline || stillOffline.LastSeen != nil {
		t.Fatalf("over-limit device = %+v, want unchanged offline state", stillOffline)
	}

	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: devices[0].ID,
		Status:   DeviceStatusOffline,
	}); err != nil {
		t.Fatalf("HeartbeatDevice first offline returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: devices[1].ID,
		Status:   DeviceStatusOnline,
	}); err != nil {
		t.Fatalf("HeartbeatDevice second online after release returned error: %v", err)
	}
}

func TestLegacyAccountWithoutPlanKeepsAccountPolicyBehavior(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore(), WithAccountPolicies("legacy-tiny", AccountPolicy{
		Name:                   "legacy-tiny",
		RelayBytesQuota:        16,
		MaxActiveRelaySessions: 4,
		MaxRelaySessionsPerDay: 8,
	}))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	if account.PlanID != "" {
		t.Fatalf("legacy account plan_id = %q, want empty plan binding", account.PlanID)
	}
	_ = mustJoinDevices(t, ctx, svc, account.ID, network.ID, 5)

	status, err := svc.GetAccountPolicyStatus(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountPolicyStatus returned error: %v", err)
	}
	if status.Policy.Name != "legacy-tiny" || status.Policy.RelayBytesQuota != 16 {
		t.Fatalf("policy = %+v, want legacy AccountPolicy untouched", status.Policy)
	}
}

func TestPlanRelayEntitlementControlsOfficialRelayCreation(t *testing.T) {
	ctx := context.Background()

	t.Run("free plan official relay unavailable", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		account, network, source, target := mustPlanRelayDevices(t, ctx, svc, PlanFree)

		_, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		assertQuotaExceeded(t, err, QuotaDimensionOfficialRelayTraffic, 0, 0)
	})

	t.Run("configured finite plan relay budget reaches quota", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.personal.concurrent_online_devices":      2,
			"cloudhub.plans.personal.official_relay_bytes_per_month": 5,
		}))
		account, network, source, target := mustPlanRelayDevices(t, ctx, svc, PlanPersonal)

		result, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err != nil {
			t.Fatalf("CreateRelaySession returned error: %v", err)
		}
		if result.RelayByteBudget.RemainingBytes != 5 || result.RelayByteBudget.Limited != true {
			t.Fatalf("relay budget = %+v, want 5 remaining finite budget", result.RelayByteBudget)
		}
		if _, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
			SessionID:     result.Session.ID,
			RelayBytesIn:  3,
			RelayBytesOut: 2,
		}); err != nil {
			t.Fatalf("CloseRelaySession returned error: %v", err)
		}
		status, err := svc.GetAccountPolicyStatus(ctx, account.ID)
		if err != nil {
			t.Fatalf("GetAccountPolicyStatus returned error: %v", err)
		}
		if status.RelayBytesUsed != 5 || status.RelayBytesRemaining != 0 {
			t.Fatalf("policy status = %+v, want exact actual relay bytes 5/0", status)
		}

		_, err = svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		assertQuotaExceeded(t, err, QuotaDimensionOfficialRelayTraffic, 5, 5)
	})

	t.Run("enterprise custom relay quota is safe refused when unresolved", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		for _, device := range []Device{source, target} {
			if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: device.ID, Status: DeviceStatusOnline}); err != nil {
				t.Fatalf("HeartbeatDevice returned error: %v", err)
			}
		}
		account.PlanID = PlanEnterprise
		if _, err := svc.store.UpdateAccount(ctx, account); err != nil {
			t.Fatalf("UpdateAccount returned error: %v", err)
		}

		_, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrQuotaExceeded) || !strings.Contains(err.Error(), "contract custom quota is not configured") {
			t.Fatalf("CreateRelaySession custom error = %v, want safe custom quota refusal", err)
		}
	})
}

func TestRelayUsageAPIDeniesMidSessionOverBudgetAtomically(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore(), WithAccountPolicies("tiny-mid-session", AccountPolicy{
		Name:                   "tiny-mid-session",
		RelayBytesQuota:        5,
		MaxActiveRelaySessions: 4,
		MaxRelaySessionsPerDay: 8,
	}))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
	result, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	if _, err := svc.RecordRelaySessionUsage(ctx, RecordRelaySessionUsageRequest{
		SessionID:     result.Session.ID,
		RelayBytesIn:  3,
		RelayBytesOut: 1,
	}); err != nil {
		t.Fatalf("RecordRelaySessionUsage under budget returned error: %v", err)
	}

	_, err = svc.RecordRelaySessionUsage(ctx, RecordRelaySessionUsageRequest{
		SessionID:     result.Session.ID,
		RelayBytesIn:  0,
		RelayBytesOut: 2,
	})
	assertQuotaExceeded(t, err, QuotaDimensionOfficialRelayTraffic, 4, 5)
	got, err := svc.GetRelaySession(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("GetRelaySession returned error: %v", err)
	}
	if got.RelayBytesIn != 3 || got.RelayBytesOut != 1 {
		t.Fatalf("session bytes = in:%d out:%d, want unchanged 3/1 after over-budget usage", got.RelayBytesIn, got.RelayBytesOut)
	}
}

func TestQuotaErrorHTTPAndClientMapping(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	account, err := client.CreateAccount(ctx, CreateAccountRequest{
		Email:  "quota-http@example.com",
		PlanID: PlanPersonal,
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "HTTPQuota"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	invite, err := client.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   4,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := client.JoinDevice(ctx, JoinDeviceRequest{
			Token:      invite.Token,
			Code:       invite.Code,
			DeviceName: "quota-http-ok",
		}); err != nil {
			t.Fatalf("JoinDevice %d returned error: %v", i+1, err)
		}
	}

	_, err = client.JoinDevice(ctx, JoinDeviceRequest{
		Token:      invite.Token,
		Code:       invite.Code,
		DeviceName: "quota-http-over",
	})
	if err == nil || !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("client JoinDevice error = %v, want ErrQuotaExceeded", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("client error = %#v, want APIError 429", err)
	}
	if apiErr.Quota == nil || apiErr.Quota.Dimension != QuotaDimensionDeviceCount ||
		apiErr.Quota.Used != 3 || apiErr.Quota.Limit != 3 {
		t.Fatalf("client quota detail = %+v, want device count 3/3", apiErr.Quota)
	}

	body := map[string]string{
		"token":       invite.Token,
		"code":        invite.Code,
		"device_name": "quota-http-over",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := server.Client().Post(server.URL+"/api/devices/join", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST devices join returned error: %v", err)
	}
	defer resp.Body.Close()
	var errorResp struct {
		OK    bool             `json:"ok"`
		Error string           `json:"error"`
		Quota QuotaErrorDetail `json:"quota"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errorResp); err != nil {
		t.Fatalf("decode quota response: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests || errorResp.OK {
		t.Fatalf("status/body = %d %+v, want 429 quota response", resp.StatusCode, errorResp)
	}
	if errorResp.Quota.Category != "quota_exceeded" || errorResp.Quota.Dimension != QuotaDimensionDeviceCount ||
		errorResp.Quota.Used != 3 || errorResp.Quota.Limit != 3 || len(errorResp.Quota.Advice) == 0 {
		t.Fatalf("quota response = %+v, want structured quota detail", errorResp.Quota)
	}
	for _, want := range []string{"释放", "下线", "查看套餐状态"} {
		if !quotaAdviceContains(errorResp.Quota.Advice, want) {
			t.Fatalf("quota advice = %+v, missing %q", errorResp.Quota.Advice, want)
		}
	}
}

func TestP2PFallbackQuotaInsufficientDoesNotAuthorizeRelay(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore(), WithAccountPolicies("tiny-p2p-plan-quota", AccountPolicy{
		Name:                   "tiny-p2p-plan-quota",
		RelayBytesQuota:        1,
		MaxActiveRelaySessions: 4,
		MaxRelaySessionsPerDay: 8,
	}))
	_, account, network, source, target := mustP2PFallbackServiceDevices(t, ctx, svc)
	first, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession first returned error: %v", err)
	}
	if _, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID:     first.Session.ID,
		RelayBytesIn:  1,
		RelayBytesOut: 0,
	}); err != nil {
		t.Fatalf("CloseRelaySession first returned error: %v", err)
	}

	negotiation := p2p.BuildNegotiation(p2p.NegotiationRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		SourceCandidates:   []p2p.Candidate{p2pFallbackTestCandidate(source.ID, network.ID, "127.0.0.1", 49140)},
		TargetCandidates:   []p2p.Candidate{p2pFallbackTestCandidate(target.ID, network.ID, "127.0.0.1", 49141)},
		AllowRelayFallback: true,
	})
	authorizer := &p2pFallbackAuthorizerForCloudHubTest{}
	result, err := p2p.AutoFallbackConnector{
		Connector:              p2p.Connector{Dialer: &p2p.FakeDialer{Err: errors.New("direct refused")}},
		RelaySessionCreator:    P2PRelaySessionManager{Service: svc, Endpoint: "127.0.0.1:18082"},
		RelaySessionAuthorizer: authorizer,
		RelaySessionCloser:     P2PRelaySessionManager{Service: svc},
	}.Connect(ctx, negotiation)

	if err == nil || !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Connect error = %v, want quota exceeded", err)
	}
	if result.State != p2p.PathStateFailed || result.FinalPath == p2p.PathTypeRelay || result.RelayAuthorized {
		t.Fatalf("result = %+v, must not report Relay fallback success", result)
	}
	if authorizer.calls != 0 {
		t.Fatalf("authorizer calls = %d, want zero when quota prevents fallback", authorizer.calls)
	}
}

func TestPlanPolicyStatusCustomQuotaErrorCarriesPlanID(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:  "enterprise-status@example.com",
		PlanID: PlanEnterprise,
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}

	_, err = svc.GetAccountPolicyStatus(ctx, account.ID)
	if err == nil || !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("GetAccountPolicyStatus error = %v, want quota exceeded", err)
	}
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("error = %T %v, want QuotaError", err, err)
	}
	if quotaErr.Detail.PlanID != PlanEnterprise || quotaErr.Detail.Dimension != QuotaDimensionOfficialRelayTraffic {
		t.Fatalf("quota detail = %+v, want enterprise relay quota detail", quotaErr.Detail)
	}
}

func mustPlanRelayDevices(t *testing.T, ctx context.Context, svc *Service, planID PlanID) (Account, Network, Device, Device) {
	t.Helper()
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:  "plan-relay-" + string(planID) + "@example.com",
		PlanID: planID,
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "PlanRelay"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	devices := mustJoinDevices(t, ctx, svc, account.ID, network.ID, 2)
	for _, device := range devices {
		if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
			DeviceID: device.ID,
			Status:   DeviceStatusOnline,
		}); err != nil {
			t.Fatalf("HeartbeatDevice returned error: %v", err)
		}
	}
	return account, network, devices[0], devices[1]
}

func assertQuotaExceeded(t *testing.T, err error, dimension QuotaDimension, used, limit int64) {
	t.Helper()
	if err == nil {
		t.Fatalf("error is nil, want quota exceeded for %s", dimension)
	}
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("error = %v, want ErrQuotaExceeded", err)
	}
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("error = %T %v, want *QuotaError", err, err)
	}
	if quotaErr.Detail.Dimension != dimension || quotaErr.Detail.Used != used || quotaErr.Detail.Limit != limit {
		t.Fatalf("quota detail = %+v, want dimension %s used %d limit %d", quotaErr.Detail, dimension, used, limit)
	}
	if quotaErr.Detail.Category == "" || len(quotaErr.Detail.Advice) == 0 {
		t.Fatalf("quota detail = %+v, want category and user advice", quotaErr.Detail)
	}
}

func assertQuotaRiskEvent(t *testing.T, events []RiskEvent, dimension QuotaDimension) {
	t.Helper()
	for _, event := range events {
		if event.Kind != RiskQuotaExceeded {
			continue
		}
		if event.Metadata["dimension"] == string(dimension) {
			return
		}
	}
	t.Fatalf("risk events = %+v, want quota_exceeded dimension %s", events, dimension)
}

func quotaAdviceContains(advice []string, want string) bool {
	for _, item := range advice {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
}
