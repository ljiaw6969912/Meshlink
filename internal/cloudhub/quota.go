package cloudhub

import (
	"fmt"
	"strings"
)

type QuotaDimension string

const (
	QuotaDimensionDeviceCount             QuotaDimension = "device_count"
	QuotaDimensionConcurrentOnlineDevices QuotaDimension = "concurrent_online_devices"
	QuotaDimensionOfficialRelayTraffic    QuotaDimension = "official_relay_traffic"
	QuotaDimensionMemberCount             QuotaDimension = "member_count"
	QuotaDimensionActiveRelaySessions     QuotaDimension = "active_relay_sessions"
	QuotaDimensionRelaySessionsPerDay     QuotaDimension = "relay_sessions_per_day"
	QuotaDimensionAuditLogRetention       QuotaDimension = "audit_log_retention"
	QuotaDimensionDeploymentCount         QuotaDimension = "deployment_count"
)

type QuotaDecision struct {
	Mode    PlanQuotaMode
	Limited bool
	Limit   int64
}

type PlanQuotaEvaluatorConfig struct {
	OperatorLimits map[string]int64
	ContractLimits map[string]int64
}

type PlanQuotaEvaluator struct {
	operatorLimits map[string]int64
	contractLimits map[string]int64
}

func NewPlanQuotaEvaluator(cfg PlanQuotaEvaluatorConfig) PlanQuotaEvaluator {
	return PlanQuotaEvaluator{
		operatorLimits: cloneInt64Map(cfg.OperatorLimits),
		contractLimits: cloneInt64Map(cfg.ContractLimits),
	}
}

func (e PlanQuotaEvaluator) Evaluate(accountID string, dimension QuotaDimension, quota PlanQuota) (QuotaDecision, error) {
	switch quota.Mode {
	case PlanQuotaUnavailable:
		return QuotaDecision{Mode: quota.Mode, Limited: true, Limit: 0}, nil
	case PlanQuotaUnlimited:
		return QuotaDecision{Mode: quota.Mode}, nil
	case PlanQuotaLimited:
		if quota.Limit != nil {
			return QuotaDecision{Mode: quota.Mode, Limited: true, Limit: *quota.Limit}, nil
		}
		key := strings.TrimSpace(quota.ConfigKey)
		limit, ok := e.operatorLimits[key]
		if !ok || limit < 0 {
			return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, "", "operator configured quota is not configured")
		}
		return QuotaDecision{Mode: quota.Mode, Limited: true, Limit: limit}, nil
	case PlanQuotaContractCustom:
		limit, ok := e.contractLimits[contractQuotaKey(accountID, dimension)]
		if !ok || limit < 0 {
			return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, "", "contract custom quota is not configured")
		}
		return QuotaDecision{Mode: quota.Mode, Limited: true, Limit: limit}, nil
	default:
		return QuotaDecision{}, newQuotaError(dimension, 0, 0, quota.Mode, "", fmt.Sprintf("unsupported quota mode %q", quota.Mode))
	}
}

type QuotaErrorDetail struct {
	Category  string         `json:"category"`
	Dimension QuotaDimension `json:"dimension"`
	Used      int64          `json:"used"`
	Limit     int64          `json:"limit"`
	Mode      PlanQuotaMode  `json:"mode,omitempty"`
	PlanID    PlanID         `json:"plan_id,omitempty"`
	Advice    []string       `json:"advice,omitempty"`
}

type QuotaError struct {
	Detail QuotaErrorDetail
	Reason string
}

func (e *QuotaError) Error() string {
	if e == nil {
		return ErrQuotaExceeded.Error()
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "quota exceeded"
	}
	return fmt.Sprintf("%s: dimension=%s used=%d limit=%d: %v", reason, e.Detail.Dimension, e.Detail.Used, e.Detail.Limit, ErrQuotaExceeded)
}

func (e *QuotaError) Unwrap() error {
	return ErrQuotaExceeded
}

func (e *QuotaError) Is(target error) bool {
	return target == ErrQuotaExceeded
}

type RelayByteBudget struct {
	Limited        bool  `json:"limited"`
	UsedBytes      int64 `json:"used_bytes"`
	LimitBytes     int64 `json:"limit_bytes,omitempty"`
	RemainingBytes int64 `json:"remaining_bytes,omitempty"`
}

func newQuotaError(dimension QuotaDimension, used, limit int64, mode PlanQuotaMode, planID PlanID, reason string) *QuotaError {
	return &QuotaError{
		Detail: QuotaErrorDetail{
			Category:  "quota_exceeded",
			Dimension: dimension,
			Used:      nonNegativeInt64(used),
			Limit:     nonNegativeInt64(limit),
			Mode:      mode,
			PlanID:    planID,
			Advice:    quotaAdvice(dimension),
		},
		Reason: strings.TrimSpace(reason),
	}
}

func quotaAdvice(dimension QuotaDimension) []string {
	common := []string{"查看套餐状态，确认当前账号可用权益。"}
	switch dimension {
	case QuotaDimensionDeviceCount:
		return append([]string{
			"释放或移除不再使用的设备后重试。",
			"让暂不使用的设备下线，再保留必要设备在线。",
		}, common...)
	case QuotaDimensionConcurrentOnlineDevices:
		return append([]string{
			"下线暂不使用的设备后重试。",
			"释放或移除不再使用的设备，减少同时在线数量。",
		}, common...)
	case QuotaDimensionOfficialRelayTraffic, QuotaDimensionActiveRelaySessions, QuotaDimensionRelaySessionsPerDay:
		return append([]string{
			"优先尝试直连，直连成功不会消耗官方 Relay。",
			"使用自建 Relay 或联系管理员提供的 Relay。",
		}, common...)
	case QuotaDimensionMemberCount:
		return append([]string{
			"移除不再使用的团队成员后重试。",
			"确认组织 owner 的套餐成员额度是否足够。",
		}, common...)
	case QuotaDimensionAuditLogRetention:
		return append([]string{
			"确认组织 owner 当前有效套餐包含审计日志保留权益。",
			"若为合同定制套餐，请先配置明确的审计保留天数。",
		}, common...)
	case QuotaDimensionDeploymentCount:
		return append([]string{
			"停用不再使用的部署包后重试。",
			"联系合同管理员确认私有部署额度。",
		}, common...)
	default:
		return common
	}
}

func cloneInt64Map(in map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = value
		}
	}
	return out
}

func contractQuotaKey(accountID string, dimension QuotaDimension) string {
	return strings.TrimSpace(accountID) + ":" + string(dimension)
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
