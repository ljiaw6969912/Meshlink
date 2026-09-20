package onboarding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"meshlink/internal/cloudhub"
)

type OfficialHubSubscriptionExperienceRequest struct {
	HubAPIURL string `json:"hub_api_url,omitempty"`
}

type OfficialHubSubscriptionExperience struct {
	State                OfficialHubState               `json:"state"`
	HubConfigured        bool                           `json:"hub_configured"`
	HubReachable         bool                           `json:"hub_reachable"`
	Degraded             bool                           `json:"degraded"`
	DegradeReason        string                         `json:"degrade_reason,omitempty"`
	CurrentPlan          OfficialHubPlanSummary         `json:"current_plan"`
	Subscription         OfficialHubSubscriptionSummary `json:"subscription"`
	Quotas               []OfficialHubQuotaSummary      `json:"quotas"`
	QuotaIssues          []OfficialHubQuotaIssue        `json:"quota_issues,omitempty"`
	RelayUsageReminder   *cloudhub.RelayUsageReminder   `json:"relay_usage_reminder,omitempty"`
	PlanComparison       []OfficialHubPlanComparison    `json:"plan_comparison"`
	UpgradeEntry         OfficialHubUpgradeEntry        `json:"upgrade_entry"`
	SelfHostedContinuity string                         `json:"self_hosted_continuity"`
}

type OfficialHubPlanSummary struct {
	ID          cloudhub.PlanID `json:"id"`
	DisplayName string          `json:"display_name"`
}

type OfficialHubSubscriptionSummary struct {
	State          string                      `json:"state"`
	Status         cloudhub.SubscriptionStatus `json:"status,omitempty"`
	Label          string                      `json:"label"`
	Message        string                      `json:"message"`
	EffectiveAt    *time.Time                  `json:"effective_at,omitempty"`
	EffectiveUntil *time.Time                  `json:"effective_until,omitempty"`
}

type OfficialHubQuotaSummary struct {
	Dimension      cloudhub.QuotaDimension `json:"dimension"`
	Label          string                  `json:"label"`
	Mode           cloudhub.PlanQuotaMode  `json:"mode"`
	ModeLabel      string                  `json:"mode_label"`
	Unit           cloudhub.PlanQuotaUnit  `json:"unit"`
	Used           int64                   `json:"used"`
	Limit          *int64                  `json:"limit,omitempty"`
	Remaining      *int64                  `json:"remaining,omitempty"`
	UsedText       string                  `json:"used_text"`
	LimitText      string                  `json:"limit_text"`
	RemainingText  string                  `json:"remaining_text"`
	UsagePercent   int                     `json:"usage_percent,omitempty"`
	Severity       string                  `json:"severity"`
	Message        string                  `json:"message"`
	Impact         string                  `json:"impact"`
	Recommendation string                  `json:"recommendation"`
}

type OfficialHubQuotaIssue struct {
	Dimension cloudhub.QuotaDimension `json:"dimension"`
	Label     string                  `json:"label"`
	Used      int64                   `json:"used"`
	Limit     int64                   `json:"limit"`
	Mode      cloudhub.PlanQuotaMode  `json:"mode,omitempty"`
	PlanID    cloudhub.PlanID         `json:"plan_id,omitempty"`
	Message   string                  `json:"message"`
	Impact    string                  `json:"impact"`
	Advice    []string                `json:"advice,omitempty"`
}

type OfficialHubPlanComparison struct {
	ID                      cloudhub.PlanID `json:"id"`
	DisplayName             string          `json:"display_name"`
	DeviceCount             string          `json:"device_count"`
	ConcurrentOnlineDevices string          `json:"concurrent_online_devices"`
	OfficialRelayTraffic    string          `json:"official_relay_traffic"`
	SelfHostedServer        string          `json:"self_hosted_server"`
	SelfHostedRelay         string          `json:"self_hosted_relay"`
	BasicDeviceInterconnect string          `json:"basic_device_interconnect"`
}

type OfficialHubUpgradeEntry struct {
	Label      string `json:"label"`
	TargetView string `json:"target_view"`
	Message    string `json:"message"`
}

const selfHostedContinuityText = "免费自建服务器、自建 Relay 和基础设备互联可继续使用；官方 Hub 到期或超额不会锁死这些自建能力。"

func (m Manager) OfficialHubSubscriptionExperience(ctx context.Context, req OfficialHubSubscriptionExperienceRequest) (OfficialHubSubscriptionExperience, error) {
	state, err := m.readOfficialHubState()
	if err != nil {
		return OfficialHubSubscriptionExperience{}, err
	}
	plans := cloudhub.BuiltinPlanCatalog()
	experience := baseSubscriptionExperience(state, plans)
	rawHubURL := strings.TrimSpace(req.HubAPIURL)
	if rawHubURL == "" {
		rawHubURL = state.HubAPIURL
	}
	if rawHubURL == "" {
		return degradedSubscriptionExperience(experience, "hub_not_configured", cloudhub.PlanFree,
			"尚未配置官方 Hub",
			"还没有配置官方 Hub；你仍可继续使用免费自建服务器、自建 Relay 和基础设备互联。"), nil
	}
	baseURL, err := normalizeOfficialHubAPIURL(rawHubURL)
	if err != nil {
		return degradedSubscriptionExperience(experience, "hub_query_failed", cloudhub.PlanFree,
			"Hub 地址需要检查",
			"Hub API 地址无效；你仍可继续使用免费自建服务器、自建 Relay 和基础设备互联。"), nil
	}
	experience.HubConfigured = true
	experience.State.HubAPIURL = baseURL
	if strings.TrimSpace(state.AccountID) == "" {
		experience.HubReachable = true
		return degradedSubscriptionExperience(experience, "account_not_configured", cloudhub.PlanFree,
			"尚未创建官方账号",
			"当前本机还没有官方 Hub 账号；你仍可继续使用免费自建服务器、自建 Relay 和基础设备互联。"), nil
	}

	client := m.officialHubClient(baseURL)
	if hubPlans, err := client.ListPlans(ctx); err != nil {
		if isHubUnreachable(err) {
			return degradedSubscriptionExperience(experience, "hub_unreachable", cloudhub.PlanFree,
				"Hub 暂时不可达",
				"暂时无法连接官方 Hub；本地仍保留原有 onboarding 状态，自建服务器、自建 Relay 和基础设备互联可继续使用。"), nil
		}
		experience.Degraded = true
		experience.DegradeReason = "hub_query_failed"
		experience.QuotaIssues = appendQuotaIssue(experience.QuotaIssues, err)
	} else if len(hubPlans) > 0 {
		plans = hubPlans
		experience.PlanComparison = planComparisons(plans)
		experience.HubReachable = true
	}

	var summary cloudhub.AccountManagementSummary
	var summaryOK bool
	if got, err := client.GetAccountManagementSummary(ctx, state.AccountID); err != nil {
		if isHubUnreachable(err) {
			return degradedSubscriptionExperience(experience, "hub_unreachable", cloudhub.PlanFree,
				"Hub 暂时不可达",
				"暂时无法连接官方 Hub；本地仍保留原有 onboarding 状态，自建服务器、自建 Relay 和基础设备互联可继续使用。"), nil
		}
		experience.Degraded = true
		if experience.DegradeReason == "" {
			experience.DegradeReason = "hub_query_failed"
		}
		experience.QuotaIssues = appendQuotaIssue(experience.QuotaIssues, err)
	} else {
		summary = got
		summaryOK = true
		experience.HubReachable = true
		experience.RelayUsageReminder = summary.RelayUsageReminder
	}

	subscription, subErr := client.GetAccountSubscriptionStatus(ctx, state.AccountID)
	if subErr != nil {
		if isHubUnreachable(subErr) {
			return degradedSubscriptionExperience(experience, "hub_unreachable", cloudhub.PlanFree,
				"Hub 暂时不可达",
				"暂时无法连接官方 Hub；本地仍保留原有 onboarding 状态，自建服务器、自建 Relay 和基础设备互联可继续使用。"), nil
		}
		experience.QuotaIssues = appendQuotaIssue(experience.QuotaIssues, subErr)
	}

	planID := currentPlanID(summary, summaryOK, subscription, subErr)
	plan := lookupPlanFrom(plans, planID)
	experience.CurrentPlan = planSummary(plan)
	experience.Subscription = subscriptionSummary(subscription, subErr)

	devices, devicesOK, err := officialHubDevicesForExperience(ctx, client, state.NetworkID)
	if err != nil {
		if isHubUnreachable(err) {
			return degradedSubscriptionExperience(experience, "hub_unreachable", cloudhub.PlanFree,
				"Hub 暂时不可达",
				"暂时无法连接官方 Hub；本地仍保留原有 onboarding 状态，自建服务器、自建 Relay 和基础设备互联可继续使用。"), nil
		}
		experience.Degraded = true
		if experience.DegradeReason == "" {
			experience.DegradeReason = "hub_query_failed"
		}
		experience.QuotaIssues = appendQuotaIssue(experience.QuotaIssues, err)
	}
	experience.Quotas = quotaSummaries(plan, devices, devicesOK, summary, summaryOK)
	if len(experience.QuotaIssues) > 0 && experience.DegradeReason == "" {
		experience.Degraded = true
		experience.DegradeReason = "quota_query_limited"
	}
	return experience, nil
}

func baseSubscriptionExperience(state OfficialHubState, plans []cloudhub.Plan) OfficialHubSubscriptionExperience {
	free := lookupPlanFrom(plans, cloudhub.PlanFree)
	return OfficialHubSubscriptionExperience{
		State:                state.withSuggestion(),
		CurrentPlan:          planSummary(free),
		Subscription:         subscriptionSummary(cloudhub.AccountSubscriptionStatus{}, cloudhub.ErrNotFound),
		Quotas:               quotaSummaries(free, nil, false, cloudhub.AccountManagementSummary{}, false),
		PlanComparison:       planComparisons(plans),
		UpgradeEntry:         defaultUpgradeEntry(),
		SelfHostedContinuity: selfHostedContinuityText,
	}
}

func degradedSubscriptionExperience(experience OfficialHubSubscriptionExperience, reason string, planID cloudhub.PlanID, label, message string) OfficialHubSubscriptionExperience {
	plan := lookupPlanFrom(cloudhub.BuiltinPlanCatalog(), planID)
	experience.Degraded = true
	experience.DegradeReason = reason
	experience.HubReachable = false
	experience.CurrentPlan = planSummary(plan)
	experience.Subscription = OfficialHubSubscriptionSummary{
		State:   reason,
		Label:   label,
		Message: message,
	}
	experience.Quotas = quotaSummaries(plan, nil, false, cloudhub.AccountManagementSummary{}, false)
	return experience
}

func currentPlanID(summary cloudhub.AccountManagementSummary, summaryOK bool, subscription cloudhub.AccountSubscriptionStatus, subErr error) cloudhub.PlanID {
	if summaryOK && strings.TrimSpace(string(summary.Account.PlanID)) != "" {
		return summary.Account.PlanID
	}
	if subErr == nil && strings.TrimSpace(string(subscription.PlanID)) != "" {
		return subscription.PlanID
	}
	return cloudhub.PlanFree
}

func lookupPlanFrom(plans []cloudhub.Plan, planID cloudhub.PlanID) cloudhub.Plan {
	if strings.TrimSpace(string(planID)) == "" {
		planID = cloudhub.PlanFree
	}
	for _, plan := range plans {
		if plan.ID == planID {
			return plan
		}
	}
	if plan, ok := cloudhub.LookupPlan(planID); ok {
		return plan
	}
	plan, _ := cloudhub.LookupPlan(cloudhub.PlanFree)
	return plan
}

func planSummary(plan cloudhub.Plan) OfficialHubPlanSummary {
	return OfficialHubPlanSummary{ID: plan.ID, DisplayName: plan.DisplayName}
}

func subscriptionSummary(status cloudhub.AccountSubscriptionStatus, err error) OfficialHubSubscriptionSummary {
	if err != nil {
		if errors.Is(err, cloudhub.ErrNotFound) {
			return OfficialHubSubscriptionSummary{
				State:   "legacy_no_subscription",
				Label:   "无订阅记录",
				Message: "没有找到订阅记录；这可能是旧账号或仅使用免费自建能力，按当前账号可用能力展示。",
			}
		}
		return OfficialHubSubscriptionSummary{
			State:   "query_failed",
			Label:   "订阅状态暂不可用",
			Message: "暂时无法查询订阅状态；这不是设备网络诊断结果，免费自建服务器、自建 Relay 和基础设备互联仍可继续使用。",
		}
	}
	out := OfficialHubSubscriptionSummary{
		State:          string(status.Status),
		Status:         status.Status,
		EffectiveAt:    cloneExperienceTime(status.EffectiveAt),
		EffectiveUntil: cloneExperienceTime(status.EffectiveUntil),
	}
	switch status.Status {
	case cloudhub.SubscriptionStatusPending:
		out.Label = "等待生效"
		out.Message = "套餐变更正在处理中，当前仍按现有可用能力展示。"
	case cloudhub.SubscriptionStatusActive:
		out.Label = "已生效"
		out.Message = "当前套餐已生效。"
	case cloudhub.SubscriptionStatusPastDue:
		out.Label = "付款状态需处理"
		out.Message = "订阅状态需要处理，不能当作已升级；官方 Hub 权益可能受限。"
	case cloudhub.SubscriptionStatusCanceled:
		out.Label = "已取消"
		if status.EffectiveUntil != nil {
			out.Message = "订阅已取消，当前套餐可用至 " + formatUserDate(*status.EffectiveUntil) + "。"
		} else {
			out.Message = "订阅已取消，当前能力以官方 Hub 返回为准。"
		}
	case cloudhub.SubscriptionStatusExpired:
		out.Label = "已到期"
		out.Message = "订阅已到期，官方 Hub 付费权益不可用；免费自建服务器、自建 Relay 和基础设备互联仍可继续使用。"
	default:
		out.State = "unknown"
		out.Label = "状态未知"
		out.Message = "暂时无法识别订阅状态；免费自建服务器、自建 Relay 和基础设备互联仍可继续使用。"
	}
	return out
}

func officialHubDevicesForExperience(ctx context.Context, client *cloudhub.Client, networkID string) ([]cloudhub.Device, bool, error) {
	networkID = strings.TrimSpace(networkID)
	if networkID == "" {
		return nil, false, nil
	}
	devices, err := client.ListNetworkDevices(ctx, networkID)
	if err != nil {
		return nil, false, err
	}
	return devices, true, nil
}

func quotaSummaries(plan cloudhub.Plan, devices []cloudhub.Device, devicesOK bool, summary cloudhub.AccountManagementSummary, summaryOK bool) []OfficialHubQuotaSummary {
	deviceUsed, onlineUsed := countExperienceDevices(devices)
	if !devicesOK {
		deviceUsed = 0
		onlineUsed = 0
	}
	relayUsed := int64(0)
	var relayLimit *int64
	var deviceLimit *int64
	var onlineLimit *int64
	if summaryOK {
		relayUsed = summary.Policy.RelayBytesUsed
		if summary.Policy.Policy.RelayBytesQuota > 0 {
			relayLimit = int64Value(summary.Policy.Policy.RelayBytesQuota)
		}
		if summary.PlanQuotas != nil {
			deviceUsed = summary.PlanQuotas.DeviceCount.Used
			deviceLimit = summary.PlanQuotas.DeviceCount.Limit
			onlineUsed = summary.PlanQuotas.ConcurrentOnlineDevices.Used
			onlineLimit = summary.PlanQuotas.ConcurrentOnlineDevices.Limit
			relayUsed = summary.PlanQuotas.OfficialRelayTraffic.Used
			relayLimit = summary.PlanQuotas.OfficialRelayTraffic.Limit
		}
	}
	return []OfficialHubQuotaSummary{
		buildQuotaSummary(cloudhub.QuotaDimensionDeviceCount, "设备数量", plan.Entitlements.DeviceCount, deviceUsed, deviceLimit, devicesOK || summary.PlanQuotas != nil),
		buildQuotaSummary(cloudhub.QuotaDimensionConcurrentOnlineDevices, "同时在线设备", plan.Entitlements.ConcurrentOnlineDevices, onlineUsed, onlineLimit, devicesOK || summary.PlanQuotas != nil),
		buildQuotaSummary(cloudhub.QuotaDimensionOfficialRelayTraffic, "官方 Relay 流量", plan.Entitlements.OfficialRelayTraffic, relayUsed, relayLimit, summaryOK),
	}
}

func buildQuotaSummary(dimension cloudhub.QuotaDimension, label string, quota cloudhub.PlanQuota, used int64, runtimeLimit *int64, usageKnown bool) OfficialHubQuotaSummary {
	limit := quota.Limit
	if runtimeLimit != nil {
		limit = runtimeLimit
	}
	limitText := quotaLimitText(quota, limit)
	remaining := remainingQuota(limit, used)
	severity := quotaSeverity(quota, used, limit, usageKnown)
	out := OfficialHubQuotaSummary{
		Dimension:      dimension,
		Label:          label,
		Mode:           quota.Mode,
		ModeLabel:      quotaModeLabel(quota.Mode),
		Unit:           quota.Unit,
		Used:           nonNegativeExperienceInt(used),
		Limit:          cloneInt64Ptr(limit),
		Remaining:      cloneInt64Ptr(remaining),
		UsedText:       quotaValueText(used, quota.Unit),
		LimitText:      limitText,
		RemainingText:  remainingText(remaining, quota, limit),
		Severity:       severity,
		Message:        quotaMessage(dimension, quota, used, limit, usageKnown),
		Impact:         quotaImpact(dimension, quota, used, limit),
		Recommendation: quotaRecommendation(dimension),
	}
	if limit != nil && *limit > 0 {
		out.UsagePercent = int((nonNegativeExperienceInt(used) * 100) / *limit)
	}
	return out
}

func countExperienceDevices(devices []cloudhub.Device) (int64, int64) {
	var total, online int64
	for _, device := range devices {
		if device.Status == cloudhub.DeviceStatusRevoked || device.RevokedAt != nil {
			continue
		}
		total++
		if device.Status == cloudhub.DeviceStatusOnline {
			online++
		}
	}
	return total, online
}

func quotaLimitText(quota cloudhub.PlanQuota, limit *int64) string {
	switch quota.Mode {
	case cloudhub.PlanQuotaUnavailable:
		return "不可用"
	case cloudhub.PlanQuotaUnlimited:
		return "不限制"
	case cloudhub.PlanQuotaContractCustom:
		return "按合同"
	case cloudhub.PlanQuotaLimited:
		if limit != nil {
			return quotaValueText(*limit, quota.Unit)
		}
		return "需配置"
	default:
		return "需确认"
	}
}

func quotaValueText(value int64, unit cloudhub.PlanQuotaUnit) string {
	value = nonNegativeExperienceInt(value)
	switch unit {
	case cloudhub.PlanQuotaUnitDevices:
		return fmt.Sprintf("%d 台", value)
	case cloudhub.PlanQuotaUnitMembers:
		return fmt.Sprintf("%d 人", value)
	case cloudhub.PlanQuotaUnitDays:
		return fmt.Sprintf("%d 天", value)
	case cloudhub.PlanQuotaUnitBytesPerMonth:
		return formatExperienceBytes(value)
	default:
		return fmt.Sprintf("%d", value)
	}
}

func remainingText(remaining *int64, quota cloudhub.PlanQuota, limit *int64) string {
	switch quota.Mode {
	case cloudhub.PlanQuotaUnavailable:
		return "不可用"
	case cloudhub.PlanQuotaUnlimited:
		return "不限制"
	case cloudhub.PlanQuotaContractCustom:
		return "按合同"
	case cloudhub.PlanQuotaLimited:
		if remaining == nil || limit == nil {
			return "需配置"
		}
		return quotaValueText(*remaining, quota.Unit)
	default:
		return "需确认"
	}
}

func quotaModeLabel(mode cloudhub.PlanQuotaMode) string {
	switch mode {
	case cloudhub.PlanQuotaUnavailable:
		return "不可用"
	case cloudhub.PlanQuotaLimited:
		return "有限额度"
	case cloudhub.PlanQuotaUnlimited:
		return "不限制"
	case cloudhub.PlanQuotaContractCustom:
		return "按合同"
	default:
		return "需确认"
	}
}

func quotaSeverity(quota cloudhub.PlanQuota, used int64, limit *int64, usageKnown bool) string {
	if quota.Mode == cloudhub.PlanQuotaUnavailable {
		return "info"
	}
	if !usageKnown || limit == nil || *limit <= 0 {
		return "info"
	}
	percent := (nonNegativeExperienceInt(used) * 100) / *limit
	if percent >= 100 {
		return "fail"
	}
	if percent >= 80 {
		return "warn"
	}
	return "ok"
}

func quotaMessage(dimension cloudhub.QuotaDimension, quota cloudhub.PlanQuota, used int64, limit *int64, usageKnown bool) string {
	if !usageKnown {
		return "暂时没有可用用量数据。"
	}
	if quota.Mode == cloudhub.PlanQuotaUnavailable {
		return "当前套餐不包含这项官方 Hub 能力。"
	}
	if quota.Mode == cloudhub.PlanQuotaUnlimited {
		return "当前套餐这项能力不限制。"
	}
	if quota.Mode == cloudhub.PlanQuotaContractCustom {
		return "额度按合同配置，当前本地页面不编造数字。"
	}
	if limit == nil {
		return "这项额度需要 Hub 运营侧配置，当前本地页面不编造数字。"
	}
	return fmt.Sprintf("%s已用 %s / 上限 %s，剩余 %s。", quotaDimensionNoun(dimension), quotaValueText(used, quota.Unit), quotaValueText(*limit, quota.Unit), quotaValueText(nonNegativeExperienceInt(*limit-used), quota.Unit))
}

func quotaImpact(dimension cloudhub.QuotaDimension, quota cloudhub.PlanQuota, used int64, limit *int64) string {
	if quota.Mode == cloudhub.PlanQuotaUnavailable {
		return "这表示套餐权益不可用，不是普通网络错误。"
	}
	if limit != nil && *limit > 0 && used >= *limit {
		switch dimension {
		case cloudhub.QuotaDimensionDeviceCount:
			return "设备数量已达到上限，新设备可能无法加入官方 Hub；这不是普通网络错误。"
		case cloudhub.QuotaDimensionConcurrentOnlineDevices:
			return "同时在线数量已达到上限，新的在线状态可能被限制；这不是普通网络错误。"
		case cloudhub.QuotaDimensionOfficialRelayTraffic:
			return "官方 Relay 用量已达到上限，新的中继连接可能被限制；这不是普通网络错误。"
		}
	}
	return "接近或达到额度时，对应官方 Hub 操作可能受限；这不是普通网络错误。"
}

func quotaRecommendation(dimension cloudhub.QuotaDimension) string {
	switch dimension {
	case cloudhub.QuotaDimensionDeviceCount:
		return "移除不再使用的设备，或查看套餐/了解升级。"
	case cloudhub.QuotaDimensionConcurrentOnlineDevices:
		return "让暂不使用的设备下线，或查看套餐/了解升级。"
	case cloudhub.QuotaDimensionOfficialRelayTraffic:
		return "优先尝试直连；也可以继续使用自建 Relay，或查看套餐/了解升级。"
	default:
		return "查看套餐状态，确认当前账号可用权益。"
	}
}

func quotaDimensionNoun(dimension cloudhub.QuotaDimension) string {
	switch dimension {
	case cloudhub.QuotaDimensionDeviceCount:
		return "设备数量"
	case cloudhub.QuotaDimensionConcurrentOnlineDevices:
		return "同时在线设备"
	case cloudhub.QuotaDimensionOfficialRelayTraffic:
		return "官方 Relay 流量"
	default:
		return "额度"
	}
}

func appendQuotaIssue(issues []OfficialHubQuotaIssue, err error) []OfficialHubQuotaIssue {
	var apiErr *cloudhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Quota == nil {
		return issues
	}
	detail := apiErr.Quota
	return append(issues, OfficialHubQuotaIssue{
		Dimension: detail.Dimension,
		Label:     quotaDimensionNoun(detail.Dimension),
		Used:      detail.Used,
		Limit:     detail.Limit,
		Mode:      detail.Mode,
		PlanID:    detail.PlanID,
		Message:   quotaIssueMessage(*detail),
		Impact:    quotaIssueImpact(detail.Dimension),
		Advice:    append([]string(nil), detail.Advice...),
	})
}

func quotaIssueMessage(detail cloudhub.QuotaErrorDetail) string {
	return fmt.Sprintf("%s已超出当前套餐额度：已用 %d / 上限 %d。", quotaDimensionNoun(detail.Dimension), detail.Used, detail.Limit)
}

func quotaIssueImpact(dimension cloudhub.QuotaDimension) string {
	switch dimension {
	case cloudhub.QuotaDimensionDeviceCount:
		return "新设备加入会被限制；这不是普通网络错误。"
	case cloudhub.QuotaDimensionConcurrentOnlineDevices:
		return "新的在线状态可能被限制；这不是普通网络错误。"
	case cloudhub.QuotaDimensionOfficialRelayTraffic, cloudhub.QuotaDimensionActiveRelaySessions, cloudhub.QuotaDimensionRelaySessionsPerDay:
		return "官方 Relay 操作会被限制；这不是普通网络错误。"
	default:
		return "对应操作会被限制；这不是普通网络错误。"
	}
}

func planComparisons(plans []cloudhub.Plan) []OfficialHubPlanComparison {
	out := make([]OfficialHubPlanComparison, 0, len(plans))
	for _, plan := range plans {
		out = append(out, OfficialHubPlanComparison{
			ID:                      plan.ID,
			DisplayName:             plan.DisplayName,
			DeviceCount:             planQuotaComparisonText(plan.Entitlements.DeviceCount),
			ConcurrentOnlineDevices: planQuotaComparisonText(plan.Entitlements.ConcurrentOnlineDevices),
			OfficialRelayTraffic:    planQuotaComparisonText(plan.Entitlements.OfficialRelayTraffic),
			SelfHostedServer:        planCapabilityComparisonText(plan.Entitlements.SelfHostedServer),
			SelfHostedRelay:         planCapabilityComparisonText(plan.Entitlements.SelfHostedRelay),
			BasicDeviceInterconnect: planCapabilityComparisonText(plan.Entitlements.BasicDeviceInterconnect),
		})
	}
	return out
}

func planQuotaComparisonText(quota cloudhub.PlanQuota) string {
	return quotaLimitText(quota, quota.Limit)
}

func planCapabilityComparisonText(capability cloudhub.PlanCapability) string {
	switch capability.Mode {
	case cloudhub.PlanQuotaUnavailable:
		return "不可用"
	case cloudhub.PlanQuotaUnlimited:
		return "不限制"
	case cloudhub.PlanQuotaContractCustom:
		return "按合同"
	default:
		return "需配置"
	}
}

func defaultUpgradeEntry() OfficialHubUpgradeEntry {
	return OfficialHubUpgradeEntry{
		Label:      "查看套餐/了解升级",
		TargetView: "plan_comparison",
		Message:    "这里只展示本地套餐差异，不跳转外部页面，也不承诺购买结果。",
	}
}

func isHubUnreachable(err error) bool {
	var apiErr *cloudhub.APIError
	if errors.As(err, &apiErr) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cloud hub request failed") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "eof")
}

func formatExperienceBytes(bytes int64) string {
	bytes = nonNegativeExperienceInt(bytes)
	const unit = int64(1024)
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= float64(unit)
		if value < float64(unit) {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/float64(unit))
}

func formatUserDate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func remainingQuota(limit *int64, used int64) *int64 {
	if limit == nil {
		return nil
	}
	remaining := *limit - used
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

func cloneInt64Ptr(in *int64) *int64 {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func int64Value(value int64) *int64 {
	return &value
}

func cloneExperienceTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := in.UTC()
	return &out
}

func nonNegativeExperienceInt(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
