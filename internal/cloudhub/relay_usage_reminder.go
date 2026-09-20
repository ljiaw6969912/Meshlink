package cloudhub

import "fmt"

func relayUsageReminderForPolicy(status AccountPolicyStatus) *RelayUsageReminder {
	limit := status.Policy.RelayBytesQuota
	used := status.RelayBytesUsed
	if limit <= 0 || used <= 0 {
		return nil
	}
	percent := int((used * 100) / limit)
	if percent < 80 {
		return nil
	}

	reminder := &RelayUsageReminder{
		Level:        "approaching",
		Severity:     "warn",
		Title:        "接近中继流量上限",
		UsedBytes:    used,
		LimitBytes:   limit,
		UsagePercent: percent,
		Message: fmt.Sprintf("当前连接正在走中继，账号本期已用 %s / 上限 %s（%d%%）。",
			formatRelayBytes(used), formatRelayBytes(limit), percent),
		Recommendation: "优先尝试直连；如果需要长期中继，请使用自建中继或管理员中继；后续升级入口开放后可在这里处理额度。",
		Actions: []string{
			"优先尝试直连",
			"使用自建中继或管理员中继",
			"升级入口占位",
		},
	}
	switch {
	case percent >= 120:
		reminder.Level = "overage"
		reminder.Severity = "fail"
		reminder.Title = "中继流量明显超额"
		reminder.Impact = "用量已经明显超过额度，新的中继连接可能被限制或需要管理员处理，这不是普通网络错误。"
	case percent >= 100:
		reminder.Level = "reached"
		reminder.Severity = "fail"
		reminder.Title = "中继流量已达上限"
		reminder.Impact = "已达到中继额度，新建或继续中继连接可能被限制，这不是普通网络错误。"
	default:
		reminder.Impact = "继续通过中继传输可能更容易达到上限；达到上限后新的中继连接可能被限制，这不是普通网络错误。"
	}
	return reminder
}

func formatRelayBytes(bytes int64) string {
	const unit = int64(1024)
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value = value / float64(unit)
		if value < float64(unit) {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/float64(unit))
}
