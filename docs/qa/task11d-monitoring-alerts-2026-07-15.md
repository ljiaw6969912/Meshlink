# Task 11D 监控告警验证报告（2026-07-15）

## 结论

Task 11.4（11D）已建立 Cloud Hub 进程内监控快照、Relay 告警状态机和受信运维查询边界。覆盖账号登录失败、在线设备数、Relay 活跃会话、Relay 带宽/错误率、封禁事件和 storage latency；Relay 告警可按 `service/region/account` 定位，系统指标使用 `account=__system__` 明确标记。

本次没有接入 Prometheus、Grafana、短信、邮件或第三方平台，没有创建 Dashboard，也没有修改 UI、构建/打包/更新或 incident/support 范围。当前没有真实数据库，接口和报告固定使用 `backend=memory`，storage latency 不作为数据库生产指标。

## 交付文件

| 文件 | 作用 |
| --- | --- |
| `internal/cloudhub/monitoring.go` | 并发安全的事件采集、滚动快照、缺失数据、告警状态机和未来 storage 包装器接口 |
| `internal/cloudhub/monitoring_test.go` | Task 11D 指标、窗口、告警、维度、脱敏、并发和 API/client 权限测试 |
| `internal/cloudhub/server.go` | 增加默认拒绝的 `GET /api/operations/monitoring` 与显式授权注入点 |
| `internal/cloudhub/client.go` | 增加 `GetMonitoringSnapshot`，沿用 `ActorAccountID` 受信身份头 |
| `docs/ops/monitoring.zh-CN.md` | 运维配置、采集边界、公式、告警、权限、隐私和排障语义 |
| `docs/qa/task11d-monitoring-alerts-2026-07-15.md` | 本验证报告 |

## 指标和缺失数据

| 指标 | 结果语义 | 验证 |
| --- | --- | --- |
| 账号登录失败 | 5 分钟窗口计数；账号未知时不记录登录名，进入 `__system__` 系统维度 | PASS |
| 在线设备 | 按设备最新 `online/offline/revoked` 状态聚合，不输出设备 ID | PASS |
| Relay 会话 | 开始/结束事件维护当前活跃数，不输出会话 ID | PASS |
| Relay 带宽 | 窗口字节总量除以窗口秒数，单位 `bytes_per_second` | PASS |
| Relay 错误率 | 失败观察数除以 Relay 观察总数，单位 `ratio` | PASS |
| 封禁 | 窗口事件计数，并把账号当前状态标为 `banned` | PASS |
| storage latency | 窗口内成功观察平均毫秒数；失败或无观察为 `missing` | PASS |

Relay 观察和 storage latency 没有样本时返回 `data_status=missing`，不把缺失数据伪装成零。计数和当前状态型指标可以合法为 `available + 0`。账号状态未记录时使用 `account_status_data_status=missing`。

## 本地人工告警场景

测试使用 fake clock 固定在 `2026-07-15T11:00:00Z`，窗口为 5 分钟，阈值为：

- Relay 错误率 `0.10`。
- Relay 带宽 `1000 bytes/s`。

向账号 `acct-alert` 注入 10 条本地 Relay 观察，每条 30,000 bytes，其中 1 条失败：

- 窗口字节总量为 300,000 bytes，`300000 / 300 = 1000 bytes/s`。
- 错误率为 `1 / 10 = 0.10`。
- 两项都恰好达到阈值，首次评估返回两个 `firing`，证明阈值边界使用 `>=`。
- 两个事件均包含 `service=cloud-hub`、`region=cn-east-1`、`account=acct-alert`、`account_scope=account`。
- 未推进时钟再次评估返回 0 个转换，证明活跃告警去重。
- fake clock 推进 `5 分钟 + 1 纳秒` 后，窗口数据过期，评估返回两个 `resolved`；再次评估返回 0 个转换，证明恢复去重。
- 恢复后 `active_alerts` 为空；转换保留原 `started_at`，并用恢复时间更新 `changed_at`。

另一组阈值使用错误率 `0.11`、带宽 `1001 bytes/s`，相同本地事件没有触发告警，覆盖阈值下方行为。整个场景不使用公网测试机，不产生真实 Relay 流量。

## 权限与隐私

- 运维接口要求 `X-Mesh-Actor-Account-ID`，并要求 `WithMonitoringAuthorizer` 对该可信账号明确授权；缺身份、未授权和默认未配置均拒绝。
- 测试验证无身份和普通账号返回 `ErrForbidden`，仅允许运维账号经 `Client.GetMonitoringSnapshot` 读取快照。
- 快照 DTO 使用字段白名单。序列化测试验证不包含设备 ID、Relay 会话 ID，以及 `password`、`token`、`private_key`、`candidate`、`raw_traffic`、`content` 字段或内容。
- 采集 API 不接收密码、token、私钥、候选地址、原始流量、用户内容或错误原文；Relay 失败只记录布尔值。
- storage 延迟采集只保留 duration，错误对象不进入快照。

## TDD 与并发验证

第一轮在没有生产实现时运行 `go test -count=1 ./internal/cloudhub -run '^TestMonitoring'`，按预期编译失败，缺少 `MonitoringThresholds`、`Monitor`、`NewMonitor`、`MonitoringConfig` 等 11D 类型。添加最小进程内实现和 API/client 接线后，同一组测试转为 PASS。

未知账号登录失败的独立 RED 首先因 `SystemMonitoringMetrics.LoginFailures` 不存在而编译失败；添加显式系统维度聚合后转为 PASS。

并发测试由 20 个 goroutine 各写入 100 次登录失败和 Relay 观察，最终得到 2,000 次登录失败和对应带宽值，验证锁保护下无丢计数或 map 并发访问错误。

测试覆盖：指标计算、5 分钟包含式边界与过期、阈值等号/下方、告警触发/去重/恢复、service/region/account/system 维度、账号/设备状态、数据缺失、序列化脱敏、并发写入和 API/client 权限。

## 验证记录

| 验证 | 命令 | 结果 |
| --- | --- | --- |
| 11D 定向测试 | `go test -count=1 ./internal/cloudhub -run '^TestMonitoring'` | PASS，`ok meshlink/internal/cloudhub` |
| Cloud Hub 全量 | `go test -count=1 ./internal/cloudhub` | PASS，`ok meshlink/internal/cloudhub 1.319s` |
| 全仓 Go 回归 | `go test -count=1 ./...` | PASS，所有包为 `ok` 或 `[no test files]` |
| 补丁空白检查 | `git diff --check` | PASS（退出码 0）；仅提示现有 `client.go`、`server.go` 工作副本下次由 Git 处理时会从 LF 转为 CRLF，没有 whitespace error |

验证过程中未调用 Browser MCP、未联网、未安装依赖、未使用公网测试机。共享工作区中并行出现的 `scripts/support-triage*` 文件属于 11E，本任务没有读取、修改、删除、暂存或提交它们。本任务自身也没有执行 `git add` 或 `git commit`。

建议提交说明：`TASK 11D add monitoring and alert evaluation`。
