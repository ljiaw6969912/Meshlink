# Cloud Hub 进程内监控与告警

本文档对应 Task 11.4（11D）。当前交付只提供 Cloud Hub 进程内的采集、滚动快照、告警状态机和受信运维查询边界；不接入 Prometheus、Grafana、短信、邮件或任何第三方平台，也不提供 Dashboard。

## 启用与权限

监控默认不开放。服务启动时必须同时注入监控实例和显式授权函数：

```go
monitor := cloudhub.NewMonitor(cloudhub.MonitoringConfig{
	Service: "cloud-hub",
	Region:  "cn-east-1",
	Window:  5 * time.Minute,
	Thresholds: cloudhub.MonitoringThresholds{
		RelayErrorRate:               0.05,
		RelayBandwidthBytesPerSecond: 10 * 1024 * 1024,
	},
})

handler := cloudhub.NewServer(service,
	cloudhub.WithMonitoring(monitor),
	cloudhub.WithMonitoringAuthorizer(func(ctx context.Context, actorAccountID string) bool {
		return trustedOperationsAccounts[actorAccountID]
	}),
)
```

查询接口为 `GET /api/operations/monitoring`。它沿用现有 `X-Mesh-Actor-Account-ID` 受信身份头，并在此基础上要求授权函数明确返回 `true`：缺少身份、未注入授权函数或授权失败都返回 `403`。授权成功但没有监控实例时返回 `404`。

该身份头只能由已经完成认证的可信上游写入；不得把可由公网客户端自行设置该头的服务直接暴露为运维接口。客户端使用 `Client.ActorAccountID` 和 `GetMonitoringSnapshot` 查询，不能通过请求体覆盖身份。

## 采集边界

`Monitor` 暴露下列进程内事件边界，业务调用点只传聚合所需的最小字段：

| 事件 | 调用 | 快照效果 |
| --- | --- | --- |
| 账号状态 | `RecordAccountStatus(accountID, status)` | 更新账号当前状态 |
| 登录失败 | `RecordLoginFailure(accountID)` | 窗口内登录失败数加一；账号未知时传空值并进入显式系统维度 |
| 设备状态 | `RecordDeviceStatus(accountID, deviceID, status)` | 按设备最新状态聚合在线设备数 |
| Relay 会话开始 | `RecordRelaySessionStarted(accountID, sessionID)` | 活跃 Relay 会话数加一 |
| Relay 会话结束 | `RecordRelaySessionStopped(sessionID)` | 活跃 Relay 会话数减一 |
| Relay 观察 | `RecordRelayObservation(accountID, bytes, failed)` | 记录窗口内字节数和成功/失败观察 |
| 封禁 | `RecordBanEvent(accountID)` | 窗口内封禁事件加一，并把账号状态置为 `banned` |
| 存储延迟 | `RecordStorageLatency(duration, err)` | 成功观察进入窗口平均值；失败观察不伪造延迟 |

`StorageLatencyRecorder` 是未来数据库或存储包装器的采集接口。当前仓库没有真实生产数据库采集，因此快照固定返回 `backend=memory`；`storage_latency` 仅表示进程内显式记录的 memory/storage 调用延迟，不能解释为数据库生产指标。

进程重启会清空窗口、设备/会话状态和告警历史。生产调用点应在状态变化成功后记录事件；登录认证层没有事件时，登录失败指标只能显示已收到的本地记录，不能从缺失数据推断真实失败率。

## 快照与指标语义

默认窗口为 5 分钟，可通过 `MonitoringConfig.Window` 修改。快照同时返回 `window_seconds` 和实际使用的阈值。账号按字典序输出；不会输出设备 ID 或 Relay 会话 ID。

| 指标 | 单位 | 计算 |
| --- | --- | --- |
| `login_failures` | `count` | 窗口内登录失败事件数；有账号时归属账号，未知账号时归属系统维度 |
| `online_devices` | `count` | 账号下最新状态为 `online` 的设备数 |
| `relay_sessions` | `count` | 账号当前活跃 Relay 会话数 |
| `relay_bandwidth` | `bytes_per_second` | 窗口内 Relay 字节总量 ÷ 窗口秒数 |
| `relay_error_rate` | `ratio` | 窗口内失败 Relay 观察数 ÷ Relay 观察总数 |
| `ban_events` | `count` | 窗口内账号封禁事件数 |
| `storage_latency` | `milliseconds` | 窗口内成功 storage latency 观察的算术平均值 |

每个值都有 `data_status`：

- `available`：采集边界能给出该值；计数或状态型指标可以合法为零。
- `missing`：窗口内没有 Relay 观察或成功 storage latency 观察，不能把零当成真实测量值。

账号指标维度固定包含 `service`、`region`、`account` 和 `account_scope=account`。账号未知的登录失败和没有账号归属的系统存储指标使用 `account=__system__`、`account_scope=system`，不会记录登录名或用空账号制造歧义。

## 告警状态机

当前只评估完成标准指定的两类告警：

- `relay_error_rate_high`：`relay_error_rate >= RelayErrorRate`。
- `relay_bandwidth_high`：`relay_bandwidth >= RelayBandwidthBytesPerSecond`。

阈值小于等于零表示禁用对应告警；错误率阈值单位为 0 到 1 的比例，带宽阈值单位为 bytes/s。达到阈值即触发，不要求超过阈值。

`EvaluateAlerts` 返回本次评估产生的状态转换；查询快照也会执行一次评估。每个 `告警名 + service + region + account` 维度只保留一个活跃状态：

1. 未告警变为达到阈值时返回 `firing`，记录 `started_at` 和 `changed_at`。
2. 持续达到阈值时只更新当前值，不重复产生事件。
3. 降到阈值以下，或窗口数据过期变为 `missing` 时返回一次 `resolved`；`started_at` 保留原触发时间，`changed_at` 为恢复时间。
4. 持续恢复不重复产生事件；以后再次超限会建立新的告警周期。

活跃状态在 `active_alerts`，最近状态转换在 `alert_events`；默认最多保留 256 个转换，可通过 `MaxAlertEvents` 调整。要保证在 5 分钟内观察到触发，进程内调用方应在收到 Relay 观察后立即评估，或以小于等于 5 分钟的周期调用 `EvaluateAlerts`/查询快照。本模块不启动后台发送器。

## 隐私与排障

指标和告警 DTO 是字段白名单，只包含时间、数值、状态和 `service/region/account` 维度。不得向采集方法传入，也不会在快照中输出：密码、token、私钥、P2P 候选地址、设备 ID、Relay 会话 ID、原始流量、用户内容或错误原文。

排查时按以下顺序判断：

1. `403`：检查可信身份头是否由认证上游写入，以及授权函数是否明确允许该运维账号。
2. `404`：检查是否向 `NewServer` 注入同一进程内的 `Monitor`。
3. `data_status=missing`：检查对应业务调用点是否记录事件；不要把缺失值解释为零。
4. 告警未触发：核对 `window_seconds`、返回阈值、单位和 Relay 观察时间；零或负阈值表示禁用。
5. 告警未恢复：确认已执行新的评估；窗口边界包含恰好位于 `now-window` 的观察，超过该边界后才过期。
