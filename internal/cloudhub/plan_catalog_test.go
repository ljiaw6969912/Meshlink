package cloudhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuiltinPlanCatalogValidationAndQuotas(t *testing.T) {
	plans := BuiltinPlanCatalog()
	if err := ValidatePlanCatalog(plans); err != nil {
		t.Fatalf("ValidatePlanCatalog returned error: %v", err)
	}

	wantIDs := []PlanID{PlanFree, PlanPersonal, PlanFamily, PlanTeam, PlanEnterprise}
	if len(plans) != len(wantIDs) {
		t.Fatalf("plans = %d, want %d", len(plans), len(wantIDs))
	}
	for i, wantID := range wantIDs {
		if plans[i].ID != wantID {
			t.Fatalf("plans[%d].ID = %q, want %q", i, plans[i].ID, wantID)
		}
		if i > 0 && plans[i].SortOrder <= plans[i-1].SortOrder {
			t.Fatalf("sort order is not stable: %+v then %+v", plans[i-1], plans[i])
		}
	}

	personal, ok := LookupPlan(PlanPersonal)
	if !ok {
		t.Fatal("personal plan was not found")
	}
	assertQuotaLimit(t, personal.Entitlements.DeviceCount, 3)
	if personal.Entitlements.OfficialRelayTraffic.Mode != PlanQuotaLimited ||
		personal.Entitlements.OfficialRelayTraffic.Source != PlanQuotaOperatorConfigured {
		t.Fatalf("personal relay quota = %+v, want finite operator-configured quota", personal.Entitlements.OfficialRelayTraffic)
	}

	family, ok := LookupPlan(PlanFamily)
	if !ok {
		t.Fatal("family plan was not found")
	}
	assertQuotaLimit(t, family.Entitlements.DeviceCount, 10)
	if family.Entitlements.FamilyManagement.Mode != PlanQuotaUnlimited {
		t.Fatalf("family management = %+v, want included capability", family.Entitlements.FamilyManagement)
	}

	free, ok := LookupPlan(PlanFree)
	if !ok {
		t.Fatal("free plan was not found")
	}
	if free.Entitlements.OfficialRelayTraffic.Mode != PlanQuotaUnavailable {
		t.Fatalf("free relay quota = %+v, want official relay unavailable", free.Entitlements.OfficialRelayTraffic)
	}
	if free.Entitlements.SelfHostedServer.Mode != PlanQuotaUnlimited ||
		free.Entitlements.SelfHostedRelay.Mode != PlanQuotaUnlimited ||
		free.Entitlements.BasicDeviceInterconnect.Mode != PlanQuotaUnlimited {
		t.Fatalf("free self-hosted boundary = %+v, want self-hosted basics included", free.Entitlements)
	}

	enterprise, ok := LookupPlan(PlanEnterprise)
	if !ok {
		t.Fatal("enterprise plan was not found")
	}
	if enterprise.Entitlements.PrivateDeployment.Mode != PlanQuotaContractCustom ||
		enterprise.Entitlements.SupportSLA.Mode != PlanQuotaContractCustom {
		t.Fatalf("enterprise private/support = %+v, want contract custom", enterprise.Entitlements)
	}

	encoded, err := json.Marshal(plans)
	if err != nil {
		t.Fatalf("marshal plans: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"price", "currency", "checkout", "payment", "token", "secret", "risk_score"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("plan catalog leaks forbidden public metadata %q: %s", forbidden, encoded)
		}
	}
	for _, forbiddenUse := range []string{"anonymous_proxy", "public_internet_egress", "full_tunnel", "arbitrary_traffic_forwarding"} {
		if !strings.Contains(lower, forbiddenUse) {
			t.Fatalf("plan catalog = %s, want explicit disallowed use %q", encoded, forbiddenUse)
		}
	}
}

func TestPlanQuotaValidationRejectsAmbiguousLimits(t *testing.T) {
	valid := BuiltinPlanCatalog()[0]

	cases := []struct {
		name  string
		quota PlanQuota
	}{
		{name: "limited zero magic value", quota: PlanQuota{Mode: PlanQuotaLimited, Unit: PlanQuotaUnitDevices, Limit: int64Ptr(0)}},
		{name: "limited negative magic value", quota: PlanQuota{Mode: PlanQuotaLimited, Unit: PlanQuotaUnitDevices, Limit: int64Ptr(-1)}},
		{name: "limited without limit or config", quota: PlanQuota{Mode: PlanQuotaLimited, Unit: PlanQuotaUnitDevices}},
		{name: "unlimited with numeric limit", quota: PlanQuota{Mode: PlanQuotaUnlimited, Unit: PlanQuotaUnitDevices, Limit: int64Ptr(3)}},
		{name: "unavailable with numeric limit", quota: PlanQuota{Mode: PlanQuotaUnavailable, Unit: PlanQuotaUnitDevices, Limit: int64Ptr(3)}},
		{name: "contract custom with numeric limit", quota: PlanQuota{Mode: PlanQuotaContractCustom, Unit: PlanQuotaUnitDevices, Limit: int64Ptr(3)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := valid
			plan.Entitlements.DeviceCount = tc.quota
			if err := ValidatePlanCatalog([]Plan{plan}); err == nil {
				t.Fatalf("ValidatePlanCatalog returned nil error for %+v", tc.quota)
			}
		})
	}
}

func TestPlanAccountPolicyMappingBoundary(t *testing.T) {
	free, ok := LookupPlan(PlanFree)
	if !ok {
		t.Fatal("free plan was not found")
	}
	freeMapping := AccountPolicyMappingForPlan(free)
	if freeMapping.RelayBytesQuotaMapped || freeMapping.AccountPolicy != nil ||
		freeMapping.RelayBytesQuota.Mode != PlanQuotaUnavailable {
		t.Fatalf("free mapping = %+v, want no legacy relay byte mapping", freeMapping)
	}

	synthetic := Plan{
		ID:          "synthetic",
		DisplayName: "Synthetic",
		SortOrder:   99,
		Entitlements: PlanEntitlements{
			DeviceCount:             LimitedPlanQuota(5, PlanQuotaUnitDevices),
			ConcurrentOnlineDevices: LimitedPlanQuota(2, PlanQuotaUnitDevices),
			OfficialRelayTraffic:    LimitedPlanQuota(12345, PlanQuotaUnitBytesPerMonth),
			MemberCount:             LimitedPlanQuota(1, PlanQuotaUnitMembers),
			AuditLogRetention:       UnavailablePlanQuota(PlanQuotaUnitDays),
			FamilyManagement:        UnavailablePlanCapability(),
			TeamManagement:          UnavailablePlanCapability(),
			PrivateDeployment:       UnavailablePlanCapability(),
			SupportSLA:              UnavailablePlanCapability(),
			SelfHostedServer:        UnlimitedPlanCapability(),
			SelfHostedRelay:         UnlimitedPlanCapability(),
			BasicDeviceInterconnect: UnlimitedPlanCapability(),
			OfficialHub:             UnlimitedPlanCapability(),
		},
		Boundaries: DefaultPlanBoundaries(),
	}
	mapping := AccountPolicyMappingForPlan(synthetic)
	if !mapping.RelayBytesQuotaMapped || mapping.AccountPolicy == nil {
		t.Fatalf("mapping = %+v, want relay byte quota mapped", mapping)
	}
	if mapping.AccountPolicy.RelayBytesQuota != 12345 {
		t.Fatalf("relay byte quota = %d, want 12345", mapping.AccountPolicy.RelayBytesQuota)
	}
	if mapping.AccountPolicy.MaxActiveRelaySessions != 0 || mapping.AccountPolicy.MaxRelaySessionsPerDay != 0 {
		t.Fatalf("mapping account policy = %+v, want only relay bytes mapped", mapping.AccountPolicy)
	}
	if len(mapping.UnmappedEntitlements) == 0 {
		t.Fatalf("mapping = %+v, want explicit unmapped plan quota boundary", mapping)
	}
}

func TestCloudHubPlanCatalogAPIFlow(t *testing.T) {
	server := httptest.NewServer(NewServer(NewService(NewMemoryStore())))
	defer server.Close()

	listResp := getJSON[struct {
		OK    bool   `json:"ok"`
		Plans []Plan `json:"plans"`
	}](t, server.URL+"/api/plans")
	if !listResp.OK || len(listResp.Plans) != 5 || listResp.Plans[0].ID != PlanFree {
		t.Fatalf("plans response = %+v, want ordered built-in plans", listResp)
	}

	planResp := getJSON[struct {
		OK   bool `json:"ok"`
		Plan Plan `json:"plan"`
	}](t, server.URL+"/api/plans/personal")
	if !planResp.OK || planResp.Plan.ID != PlanPersonal {
		t.Fatalf("personal response = %+v, want personal plan", planResp)
	}

	errorResp, status := getJSONStatus[struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}](t, server.URL+"/api/plans/missing")
	if status != http.StatusNotFound || errorResp.OK || !strings.Contains(errorResp.Error, "plan was not found") {
		t.Fatalf("missing plan status=%d body=%+v, want 404 plan not found", status, errorResp)
	}
}

func TestClientListsAndGetsPlans(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(NewServer(NewService(NewMemoryStore())))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	plans, err := client.ListPlans(ctx)
	if err != nil {
		t.Fatalf("ListPlans returned error: %v", err)
	}
	if len(plans) != 5 || plans[0].ID != PlanFree {
		t.Fatalf("plans = %+v, want ordered built-in plans", plans)
	}

	personal, err := client.GetPlan(ctx, PlanPersonal)
	if err != nil {
		t.Fatalf("GetPlan returned error: %v", err)
	}
	if personal.ID != PlanPersonal {
		t.Fatalf("plan = %+v, want personal", personal)
	}
}

func assertQuotaLimit(t *testing.T, quota PlanQuota, want int64) {
	t.Helper()
	if quota.Mode != PlanQuotaLimited || quota.Limit == nil || *quota.Limit != want {
		t.Fatalf("quota = %+v, want limited %d", quota, want)
	}
}

func getJSONStatus[T any](t *testing.T, url string) (T, int) {
	t.Helper()
	var got T
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response from %s: %v", url, err)
	}
	return got, resp.StatusCode
}
