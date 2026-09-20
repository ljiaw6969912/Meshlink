package cloudhub

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type PlanID string

const (
	PlanFree       PlanID = "free"
	PlanPersonal   PlanID = "personal"
	PlanFamily     PlanID = "family"
	PlanTeam       PlanID = "team"
	PlanEnterprise PlanID = "enterprise"
)

type PlanQuotaMode string

const (
	PlanQuotaUnavailable    PlanQuotaMode = "unavailable"
	PlanQuotaLimited        PlanQuotaMode = "limited"
	PlanQuotaUnlimited      PlanQuotaMode = "unlimited"
	PlanQuotaContractCustom PlanQuotaMode = "contract_custom"
)

type PlanQuotaSource string

const (
	PlanQuotaBuiltIn            PlanQuotaSource = "built_in"
	PlanQuotaOperatorConfigured PlanQuotaSource = "operator_configured"
	PlanQuotaContract           PlanQuotaSource = "contract"
)

type PlanQuotaUnit string

const (
	PlanQuotaUnitDevices       PlanQuotaUnit = "devices"
	PlanQuotaUnitBytesPerMonth PlanQuotaUnit = "bytes_per_month"
	PlanQuotaUnitMembers       PlanQuotaUnit = "members"
	PlanQuotaUnitDays          PlanQuotaUnit = "days"
	PlanQuotaUnitSLA           PlanQuotaUnit = "sla"
	PlanQuotaUnitCapability    PlanQuotaUnit = "capability"
)

type PlanQuota struct {
	Mode      PlanQuotaMode   `json:"mode"`
	Unit      PlanQuotaUnit   `json:"unit"`
	Limit     *int64          `json:"limit,omitempty"`
	Source    PlanQuotaSource `json:"source,omitempty"`
	ConfigKey string          `json:"-"`
	Notes     string          `json:"notes,omitempty"`
}

type PlanCapability struct {
	Mode  PlanQuotaMode `json:"mode"`
	Notes string        `json:"notes,omitempty"`
}

type PlanEntitlements struct {
	DeviceCount             PlanQuota      `json:"device_count"`
	ConcurrentOnlineDevices PlanQuota      `json:"concurrent_online_devices"`
	OfficialRelayTraffic    PlanQuota      `json:"official_relay_traffic"`
	MemberCount             PlanQuota      `json:"member_count"`
	AuditLogRetention       PlanQuota      `json:"audit_log_retention"`
	FamilyManagement        PlanCapability `json:"family_management"`
	TeamManagement          PlanCapability `json:"team_management"`
	PrivateDeployment       PlanCapability `json:"private_deployment"`
	SupportSLA              PlanCapability `json:"support_sla"`
	SelfHostedServer        PlanCapability `json:"self_hosted_server"`
	SelfHostedRelay         PlanCapability `json:"self_hosted_relay"`
	BasicDeviceInterconnect PlanCapability `json:"basic_device_interconnect"`
	OfficialHub             PlanCapability `json:"official_hub"`
}

type PlanBoundaries struct {
	PrimaryUse     string   `json:"primary_use"`
	DisallowedUses []string `json:"disallowed_uses"`
}

type Plan struct {
	ID           PlanID           `json:"id"`
	DisplayName  string           `json:"display_name"`
	SortOrder    int              `json:"sort_order"`
	Audience     string           `json:"audience,omitempty"`
	Entitlements PlanEntitlements `json:"entitlements"`
	Boundaries   PlanBoundaries   `json:"boundaries"`
}

type PlanAccountPolicyMapping struct {
	PlanID                  PlanID         `json:"plan_id"`
	RelayBytesQuota         PlanQuota      `json:"relay_bytes_quota"`
	RelayBytesQuotaMapped   bool           `json:"relay_bytes_quota_mapped"`
	AccountPolicy           *AccountPolicy `json:"account_policy,omitempty"`
	UnmappedEntitlements    []string       `json:"unmapped_entitlements,omitempty"`
	CompatibilityConstraint string         `json:"compatibility_constraint"`
}

func BuiltinPlanCatalog() []Plan {
	plans := []Plan{
		{
			ID:          PlanFree,
			DisplayName: "Free",
			SortOrder:   10,
			Audience:    "Self-hosted private device interconnect",
			Entitlements: PlanEntitlements{
				DeviceCount:             UnlimitedPlanQuota(PlanQuotaUnitDevices),
				ConcurrentOnlineDevices: UnlimitedPlanQuota(PlanQuotaUnitDevices),
				OfficialRelayTraffic:    UnavailablePlanQuota(PlanQuotaUnitBytesPerMonth),
				MemberCount:             UnavailablePlanQuota(PlanQuotaUnitMembers),
				AuditLogRetention:       UnavailablePlanQuota(PlanQuotaUnitDays),
				FamilyManagement:        UnavailablePlanCapability(),
				TeamManagement:          UnavailablePlanCapability(),
				PrivateDeployment:       UnavailablePlanCapability(),
				SupportSLA:              UnavailablePlanCapability(),
				SelfHostedServer:        UnlimitedPlanCapability(),
				SelfHostedRelay:         UnlimitedPlanCapability(),
				BasicDeviceInterconnect: UnlimitedPlanCapability(),
				OfficialHub:             UnavailablePlanCapability(),
			},
			Boundaries: DefaultPlanBoundaries(),
		},
		{
			ID:          PlanPersonal,
			DisplayName: "Personal",
			SortOrder:   20,
			Audience:    "One person's official Hub account",
			Entitlements: PlanEntitlements{
				DeviceCount:             LimitedPlanQuota(3, PlanQuotaUnitDevices),
				ConcurrentOnlineDevices: ConfiguredLimitedPlanQuota(PlanQuotaUnitDevices, "cloudhub.plans.personal.concurrent_online_devices"),
				OfficialRelayTraffic:    ConfiguredLimitedPlanQuota(PlanQuotaUnitBytesPerMonth, "cloudhub.plans.personal.official_relay_bytes_per_month"),
				MemberCount:             UnavailablePlanQuota(PlanQuotaUnitMembers),
				AuditLogRetention:       ConfiguredLimitedPlanQuota(PlanQuotaUnitDays, "cloudhub.plans.personal.audit_log_retention_days"),
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
		},
		{
			ID:          PlanFamily,
			DisplayName: "Family",
			SortOrder:   30,
			Audience:    "Family devices and household device management",
			Entitlements: PlanEntitlements{
				DeviceCount:             LimitedPlanQuota(10, PlanQuotaUnitDevices),
				ConcurrentOnlineDevices: ConfiguredLimitedPlanQuota(PlanQuotaUnitDevices, "cloudhub.plans.family.concurrent_online_devices"),
				OfficialRelayTraffic:    ConfiguredLimitedPlanQuota(PlanQuotaUnitBytesPerMonth, "cloudhub.plans.family.official_relay_bytes_per_month"),
				MemberCount:             ConfiguredLimitedPlanQuota(PlanQuotaUnitMembers, "cloudhub.plans.family.members"),
				AuditLogRetention:       ConfiguredLimitedPlanQuota(PlanQuotaUnitDays, "cloudhub.plans.family.audit_log_retention_days"),
				FamilyManagement:        UnlimitedPlanCapability(),
				TeamManagement:          UnavailablePlanCapability(),
				PrivateDeployment:       UnavailablePlanCapability(),
				SupportSLA:              UnavailablePlanCapability(),
				SelfHostedServer:        UnlimitedPlanCapability(),
				SelfHostedRelay:         UnlimitedPlanCapability(),
				BasicDeviceInterconnect: UnlimitedPlanCapability(),
				OfficialHub:             UnlimitedPlanCapability(),
			},
			Boundaries: DefaultPlanBoundaries(),
		},
		{
			ID:          PlanTeam,
			DisplayName: "Team",
			SortOrder:   40,
			Audience:    "Managed team accounts, members, audit, and admin controls",
			Entitlements: PlanEntitlements{
				DeviceCount:             ConfiguredLimitedPlanQuota(PlanQuotaUnitDevices, "cloudhub.plans.team.devices"),
				ConcurrentOnlineDevices: ConfiguredLimitedPlanQuota(PlanQuotaUnitDevices, "cloudhub.plans.team.concurrent_online_devices"),
				OfficialRelayTraffic:    ConfiguredLimitedPlanQuota(PlanQuotaUnitBytesPerMonth, "cloudhub.plans.team.official_relay_bytes_per_month"),
				MemberCount:             ConfiguredLimitedPlanQuota(PlanQuotaUnitMembers, "cloudhub.plans.team.members"),
				AuditLogRetention:       ConfiguredLimitedPlanQuota(PlanQuotaUnitDays, "cloudhub.plans.team.audit_log_retention_days"),
				FamilyManagement:        UnavailablePlanCapability(),
				TeamManagement:          UnlimitedPlanCapability(),
				PrivateDeployment:       UnavailablePlanCapability(),
				SupportSLA:              ContractCustomPlanCapability(),
				SelfHostedServer:        UnlimitedPlanCapability(),
				SelfHostedRelay:         UnlimitedPlanCapability(),
				BasicDeviceInterconnect: UnlimitedPlanCapability(),
				OfficialHub:             UnlimitedPlanCapability(),
			},
			Boundaries: DefaultPlanBoundaries(),
		},
		{
			ID:          PlanEnterprise,
			DisplayName: "Enterprise",
			SortOrder:   50,
			Audience:    "Private deployment, contract authorization, support, and custom controls",
			Entitlements: PlanEntitlements{
				DeviceCount:             ContractCustomPlanQuota(PlanQuotaUnitDevices),
				ConcurrentOnlineDevices: ContractCustomPlanQuota(PlanQuotaUnitDevices),
				OfficialRelayTraffic:    ContractCustomPlanQuota(PlanQuotaUnitBytesPerMonth),
				MemberCount:             ContractCustomPlanQuota(PlanQuotaUnitMembers),
				AuditLogRetention:       ContractCustomPlanQuota(PlanQuotaUnitDays),
				FamilyManagement:        UnavailablePlanCapability(),
				TeamManagement:          ContractCustomPlanCapability(),
				PrivateDeployment:       ContractCustomPlanCapability(),
				SupportSLA:              ContractCustomPlanCapability(),
				SelfHostedServer:        UnlimitedPlanCapability(),
				SelfHostedRelay:         UnlimitedPlanCapability(),
				BasicDeviceInterconnect: UnlimitedPlanCapability(),
				OfficialHub:             ContractCustomPlanCapability(),
			},
			Boundaries: DefaultPlanBoundaries(),
		},
	}
	return clonePlans(plans)
}

func LookupPlan(planID PlanID) (Plan, bool) {
	target := PlanID(strings.TrimSpace(string(planID)))
	for _, plan := range BuiltinPlanCatalog() {
		if plan.ID == target {
			return plan, true
		}
	}
	return Plan{}, false
}

func ValidatePlanCatalog(plans []Plan) error {
	if len(plans) == 0 {
		return fmt.Errorf("plan catalog is empty")
	}
	seen := map[PlanID]struct{}{}
	lastSort := -1
	for i, plan := range plans {
		if err := validatePlan(plan); err != nil {
			return fmt.Errorf("plan %d: %w", i, err)
		}
		if _, ok := seen[plan.ID]; ok {
			return fmt.Errorf("plan id %q is duplicated", plan.ID)
		}
		seen[plan.ID] = struct{}{}
		if plan.SortOrder <= lastSort {
			return fmt.Errorf("plan %q sort order %d must be greater than previous sort order %d", plan.ID, plan.SortOrder, lastSort)
		}
		lastSort = plan.SortOrder
	}
	return nil
}

func AccountPolicyMappingForPlan(plan Plan) PlanAccountPolicyMapping {
	mapping := PlanAccountPolicyMapping{
		PlanID:          plan.ID,
		RelayBytesQuota: plan.Entitlements.OfficialRelayTraffic,
		UnmappedEntitlements: []string{
			"device_count",
			"concurrent_online_devices",
			"member_count",
			"audit_log_retention",
			"family_management",
			"team_management",
			"private_deployment",
			"support_sla",
		},
		CompatibilityConstraint: "legacy AccountPolicy only represents relay byte quota and relay-session guardrails; Task 9B must enforce plan quotas directly",
	}
	if plan.Entitlements.OfficialRelayTraffic.Mode == PlanQuotaLimited && plan.Entitlements.OfficialRelayTraffic.Limit != nil {
		policy := AccountPolicy{
			Name:            "plan-" + string(plan.ID) + "-relay",
			RelayBytesQuota: *plan.Entitlements.OfficialRelayTraffic.Limit,
		}
		mapping.RelayBytesQuotaMapped = true
		mapping.AccountPolicy = &policy
	}
	return mapping
}

func (s *Service) ListPlans(_ context.Context) ([]Plan, error) {
	plans := BuiltinPlanCatalog()
	if err := ValidatePlanCatalog(plans); err != nil {
		return nil, err
	}
	return plans, nil
}

func (s *Service) GetPlan(_ context.Context, planID PlanID) (Plan, error) {
	plan, ok := LookupPlan(planID)
	if !ok {
		return Plan{}, fmt.Errorf("plan was not found: %w", ErrNotFound)
	}
	return plan, nil
}

func LimitedPlanQuota(limit int64, unit PlanQuotaUnit) PlanQuota {
	return PlanQuota{
		Mode:   PlanQuotaLimited,
		Unit:   unit,
		Limit:  int64Ptr(limit),
		Source: PlanQuotaBuiltIn,
	}
}

func ConfiguredLimitedPlanQuota(unit PlanQuotaUnit, configKey string) PlanQuota {
	return PlanQuota{
		Mode:      PlanQuotaLimited,
		Unit:      unit,
		Source:    PlanQuotaOperatorConfigured,
		ConfigKey: strings.TrimSpace(configKey),
		Notes:     "finite quota configured by the official Hub operator",
	}
}

func UnlimitedPlanQuota(unit PlanQuotaUnit) PlanQuota {
	return PlanQuota{
		Mode: PlanQuotaUnlimited,
		Unit: unit,
	}
}

func UnavailablePlanQuota(unit PlanQuotaUnit) PlanQuota {
	return PlanQuota{
		Mode: PlanQuotaUnavailable,
		Unit: unit,
	}
}

func ContractCustomPlanQuota(unit PlanQuotaUnit) PlanQuota {
	return PlanQuota{
		Mode:   PlanQuotaContractCustom,
		Unit:   unit,
		Source: PlanQuotaContract,
		Notes:  "quota customized by contract",
	}
}

func UnlimitedPlanCapability() PlanCapability {
	return PlanCapability{Mode: PlanQuotaUnlimited}
}

func UnavailablePlanCapability() PlanCapability {
	return PlanCapability{Mode: PlanQuotaUnavailable}
}

func ContractCustomPlanCapability() PlanCapability {
	return PlanCapability{Mode: PlanQuotaContractCustom, Notes: "capability customized by contract"}
}

func DefaultPlanBoundaries() PlanBoundaries {
	return PlanBoundaries{
		PrimaryUse: "private_remote_desktop",
		DisallowedUses: []string{
			"anonymous_proxy",
			"public_internet_egress",
			"full_tunnel",
			"arbitrary_traffic_forwarding",
		},
	}
}

func validatePlan(plan Plan) error {
	if strings.TrimSpace(string(plan.ID)) == "" {
		return fmt.Errorf("id is required")
	}
	if plan.ID != PlanID(strings.TrimSpace(string(plan.ID))) {
		return fmt.Errorf("id %q must be trimmed", plan.ID)
	}
	if strings.TrimSpace(plan.DisplayName) == "" {
		return fmt.Errorf("display_name is required")
	}
	if plan.SortOrder <= 0 {
		return fmt.Errorf("sort_order must be positive")
	}
	quotas := []struct {
		name  string
		quota PlanQuota
	}{
		{name: "device_count", quota: plan.Entitlements.DeviceCount},
		{name: "concurrent_online_devices", quota: plan.Entitlements.ConcurrentOnlineDevices},
		{name: "official_relay_traffic", quota: plan.Entitlements.OfficialRelayTraffic},
		{name: "member_count", quota: plan.Entitlements.MemberCount},
		{name: "audit_log_retention", quota: plan.Entitlements.AuditLogRetention},
	}
	for _, item := range quotas {
		if err := validatePlanQuota(item.quota); err != nil {
			return fmt.Errorf("%s: %w", item.name, err)
		}
	}
	capabilities := []struct {
		name       string
		capability PlanCapability
	}{
		{name: "family_management", capability: plan.Entitlements.FamilyManagement},
		{name: "team_management", capability: plan.Entitlements.TeamManagement},
		{name: "private_deployment", capability: plan.Entitlements.PrivateDeployment},
		{name: "support_sla", capability: plan.Entitlements.SupportSLA},
		{name: "self_hosted_server", capability: plan.Entitlements.SelfHostedServer},
		{name: "self_hosted_relay", capability: plan.Entitlements.SelfHostedRelay},
		{name: "basic_device_interconnect", capability: plan.Entitlements.BasicDeviceInterconnect},
		{name: "official_hub", capability: plan.Entitlements.OfficialHub},
	}
	for _, item := range capabilities {
		if err := validatePlanCapability(item.capability); err != nil {
			return fmt.Errorf("%s: %w", item.name, err)
		}
	}
	return validatePlanBoundaries(plan.Boundaries)
}

func validatePlanQuota(quota PlanQuota) error {
	if quota.Unit == "" {
		return fmt.Errorf("unit is required")
	}
	switch quota.Mode {
	case PlanQuotaLimited:
		if quota.Limit != nil {
			if *quota.Limit <= 0 {
				return fmt.Errorf("limited quota must have a positive limit")
			}
			if quota.ConfigKey != "" {
				return fmt.Errorf("limited quota cannot have both limit and config key")
			}
			if quota.Source != "" && quota.Source != PlanQuotaBuiltIn {
				return fmt.Errorf("limited quota with built-in limit must use built_in source")
			}
			return nil
		}
		if strings.TrimSpace(quota.ConfigKey) == "" || quota.Source != PlanQuotaOperatorConfigured {
			return fmt.Errorf("limited quota must have a positive limit or operator-configured source")
		}
	case PlanQuotaUnavailable, PlanQuotaUnlimited:
		if quota.Limit != nil || strings.TrimSpace(quota.ConfigKey) != "" {
			return fmt.Errorf("%s quota cannot carry numeric or configured limit", quota.Mode)
		}
		if quota.Source != "" {
			return fmt.Errorf("%s quota cannot carry source", quota.Mode)
		}
	case PlanQuotaContractCustom:
		if quota.Limit != nil || strings.TrimSpace(quota.ConfigKey) != "" {
			return fmt.Errorf("contract custom quota cannot carry numeric or configured limit")
		}
		if quota.Source != "" && quota.Source != PlanQuotaContract {
			return fmt.Errorf("contract custom quota must use contract source")
		}
	default:
		return fmt.Errorf("unsupported quota mode %q", quota.Mode)
	}
	return nil
}

func validatePlanCapability(capability PlanCapability) error {
	switch capability.Mode {
	case PlanQuotaUnavailable, PlanQuotaUnlimited, PlanQuotaContractCustom:
		return nil
	case PlanQuotaLimited:
		return fmt.Errorf("capability cannot use limited mode without a quota field")
	default:
		return fmt.Errorf("unsupported capability mode %q", capability.Mode)
	}
}

func validatePlanBoundaries(boundaries PlanBoundaries) error {
	if strings.TrimSpace(boundaries.PrimaryUse) == "" {
		return fmt.Errorf("primary use is required")
	}
	required := map[string]struct{}{
		"anonymous_proxy":              {},
		"public_internet_egress":       {},
		"full_tunnel":                  {},
		"arbitrary_traffic_forwarding": {},
	}
	for _, use := range boundaries.DisallowedUses {
		delete(required, use)
	}
	if len(required) > 0 {
		var missing []string
		for use := range required {
			missing = append(missing, use)
		}
		sort.Strings(missing)
		return fmt.Errorf("missing disallowed uses: %s", strings.Join(missing, ", "))
	}
	return nil
}

func clonePlans(in []Plan) []Plan {
	out := make([]Plan, len(in))
	for i, plan := range in {
		out[i] = clonePlan(plan)
	}
	return out
}

func clonePlan(plan Plan) Plan {
	plan.Entitlements.DeviceCount = clonePlanQuota(plan.Entitlements.DeviceCount)
	plan.Entitlements.ConcurrentOnlineDevices = clonePlanQuota(plan.Entitlements.ConcurrentOnlineDevices)
	plan.Entitlements.OfficialRelayTraffic = clonePlanQuota(plan.Entitlements.OfficialRelayTraffic)
	plan.Entitlements.MemberCount = clonePlanQuota(plan.Entitlements.MemberCount)
	plan.Entitlements.AuditLogRetention = clonePlanQuota(plan.Entitlements.AuditLogRetention)
	if len(plan.Boundaries.DisallowedUses) > 0 {
		plan.Boundaries.DisallowedUses = append([]string(nil), plan.Boundaries.DisallowedUses...)
	}
	return plan
}

func clonePlanQuota(quota PlanQuota) PlanQuota {
	if quota.Limit != nil {
		quota.Limit = int64Ptr(*quota.Limit)
	}
	return quota
}

func int64Ptr(v int64) *int64 {
	return &v
}
