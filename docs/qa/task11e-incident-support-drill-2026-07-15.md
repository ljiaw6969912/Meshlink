# Task 11E 事故响应与客服排查演练记录

日期：2026-07-15

## 结论

本次在本机使用 fake JSON 元数据演练“用户无法远程桌面”。证据按安全、账号/额度、登录、入网、连接路径、Relay、RDP 顺序收敛：前六层无失败，RDP 为 `unreachable`、`enabled=false`、`service_state=stopped`，现有诊断 finding 为 `rdp_unavailable`。配套脚本最终分类为：

```text
category = target_rdp
layer = rdp
priority = P3
code = rdp_not_enabled
stop_and_escalate_security = false
```

因此演练定位为“目标机 RDP 未开启或服务未运行”，不是登录、入网、连接路径或 Relay 故障。该结论只证明本地 fake 输入、schema、分类与脱敏逻辑可重复；本次没有真实用户、两台真实 Windows、真实 RDP 会话、真实 Hub/Relay 或公网/NAT 环境，不能表述为真实用户恢复或双机演练通过。

## 演练范围与角色

- L1 客服：收集虚构 account/device 和 UTC 时间范围，运行本地聚合脚本，读取 classification。
- L2 支持：核对证据顺序、隐私边界、错误路径和交接条件。
- 目标机管理员（演练中仅为步骤角色，没有真实设备）：负责启用 RDP、确认服务/防火墙并复测。
- 安全负责人：本场景 `security=clear`，未触发；若为 suspected/confirmed 应立即停止普通排查。
- 观察员：核对没有采集用户内容、凭据、地址或原始流量，没有自动上传。

演练依据为 [`事故响应与客服排查手册`](../ops/incident-response.zh-CN.md)、[`support-triage.ps1`](../../scripts/support-triage.ps1) 和 [`support-triage.tests.ps1`](../../scripts/support-triage.tests.ps1)。

## Fake 输入与预期推理

测试脚本运行时在系统临时目录生成输入，结束后删除；仓库不保存 account/device 原值或输出报告。核心 fake 状态如下：

| 层 | Fake 状态 | 结论 |
| --- | --- | --- |
| 安全 | `clear` | 不触发安全停止 |
| 账号/额度 | `ok` | 非冻结、停用或额度问题 |
| 登录 | `ok` | 非登录层 |
| 入网 | `ok` | 非设备入网层 |
| 连接路径 | `status=connected`、`path_type=lan_direct`、`path_state=lan_direct_connected` | Meshlink 路径已建立 |
| Relay | `not_used` | 非 Relay 降级/失败；未使用 Relay 本身不是故障 |
| RDP | `unreachable`、`enabled=false`、`service_state=stopped`、`firewall_status=unknown` | 最早失败层为目标机 RDP |
| 现有诊断 | `code=rdp_unavailable`、`severity=fail` | 与目标机 RDP 层一致 |

脚本输出仅含 account/device 的 `sha256:` 短引用，没有原始 ID。输出声明 `local_only=true`、`telemetry_sent=false`、`user_content_collected=false`、`credentials_collected=false`。

## 演练步骤

1. 从测试中的虚构 account ID、device ID 和 `2026-07-15T01:00:00Z` 至 `2026-07-15T01:15:00Z` 建立最小输入。
2. 输入登录、入网、连接、Relay 和 RDP 受控状态，不输入自由文本日志或 `checks/detail`。
3. 运行：

   ```powershell
   powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\support-triage.tests.ps1
   ```

4. 测试验证脚本语法/参数、固定输出 schema、RDP 分类、账号/设备脱敏、默认本地落盘、敏感/地址字段拒绝、反向时间范围和缺失文件错误路径。
5. 根据 `rdp_not_enabled` 给出目标机处理步骤，不继续检查 Relay 或索取内容。

## 处理与恢复步骤

由用户或目标机授权管理员在目标 Windows 本地执行：

1. 在 Windows 设置中启用“远程桌面”。
2. 确认 Remote Desktop 服务为 running，并确认预期用户拥有远程登录授权。
3. 只启用组织批准的远程桌面防火墙规则；不要关闭整个 Windows 防火墙。
4. 重跑本地一键诊断，确认 RDP 从 unreachable 变为 reachable。
5. 以相同 account/device 和新的 UTC 时间范围重跑 `support-triage.ps1`；只有真实设备复测成功后，工单才可写“用户已恢复”。
6. 若仍 unreachable，保持路径层已通过的证据，升级目标机 RDP 负责人；不得转而收集屏幕、凭据、候选地址或抓包。

本次没有真实目标机，所以上述恢复步骤没有执行，演练结论保持“处理步骤已给出，真实恢复待授权设备验证”。

## 实际测试结果

### 1. 脚本测试

命令：

```powershell
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\support-triage.tests.ps1
```

结果：退出码 0；6 个场景全部 PASS：

```text
PASS: script syntax and parameter contract
PASS: fake RDP disabled scenario has fixed redacted schema
PASS: default output remains in the local current directory
PASS: sensitive and address fields are rejected without report
PASS: reversed time range is rejected
PASS: missing input file returns an error
PASS: all support triage tests
```

### 2. 直接相关 Go 包

本任务没有修改 Go 代码。按 11E 约束只运行 diagnose/RDP/agent/cloudhub 直接相关最小包，不重复全仓 `go test -count=1 ./...`：

```powershell
go test -count=1 ./internal/diagnose ./internal/rdp ./internal/agent ./internal/cloudhub
```

结果：退出码 0。

```text
ok   meshlink/internal/diagnose
?    meshlink/internal/rdp [no test files]
ok   meshlink/internal/agent
ok   meshlink/internal/cloudhub
```

`internal/rdp` 的 `[no test files]` 只表示该包在当前平台可构建，不代表真实 Windows RDP 已验证。

### 3. 差异与文档校验

最终验证包括 PowerShell 语法解析、测试、文档本地链接/必需主题检查、专属范围检查、敏感字面量检查和 `git diff --check`。本任务不调用 Browser MCP、不联网、不安装依赖、不使用公网测试机。

## 证据边界与清理

- fake 输入由测试在 `%TEMP%` 下创建并在 `finally` 中递归删除；默认输出测试也落在同一临时目录并随之删除。
- 敏感字段用例只验证脚本拒绝属性且不生成报告；其中的保留测试字符串不是现实 token，不写入交付报告。
- 脚本未自动上传、未调用网络、未修改账号、设备、Cloud Hub、Relay、RDP 或系统配置。
- 本轮没有采集屏幕、文件、剪贴板、密码、token、私钥、候选/远端地址或原始流量。
- 未建设工单、远控、内容查看或监控平台，未修改 11D 文件。

## 真实环境限制

未具备两台真实 Windows、可交互目标机 RDP、本地/公网/NAT 网络组合、官方测试 Hub/Relay、真实账号与套餐、真实故障时间窗。因此本演练没有证明：

- 真实 Windows 启用/停用 RDP 后系统服务与防火墙的实际变化。
- 真实 direct/Relay 路径上的 RDP 会话建立、画面或性能。
- 生产账号、入网、连接和 Relay 元数据查询的权限、延迟与保留期。
- 多用户服务故障的 P1/P2 范围判断、生产缓解、回滚或客户沟通节奏。

这些限制必须保留在发布/事故证据中，不能由本机 fake 演练替代。
