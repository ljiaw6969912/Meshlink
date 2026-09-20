# Task 6A P2P 控制面协议、候选地址模型与 LAN direct 骨架

日期：2026-07-09

## 交付边界

- 新增 `internal/p2p`，定义候选地址、候选地址对、连接协商、路径状态、连接尝试和 Relay fallback 语义。
- 候选地址支持 `lan`、`public`、`relay` scope；Task 6A 当前仅验收 IPv4/TCP，IPv6 或非 IPv4 地址会被拒绝。
- 路径状态机表达 `connecting`、`trying_lan_direct`、`lan_direct_connected`、`fallback_relay`、`failed`，并保留 `public_direct` 状态枚举给后续 Task 6B/6C 使用。
- Cloud Hub 新增 P2P 候选注册、候选查询和连接协商 API/client/service；仅同 account、同 network、online、未 revoked 的合法设备可参与。
- 连接协商优先返回 direct candidate pairs；无候选或 direct 失败时通过 `relay_fallback.create_relay_session` 表达“客户端应创建 Relay session”的语义，但不会直接生成 Relay join token。
- 候选注册记录 `p2p_candidates_registered` audit；连接协商记录 `p2p_connection_negotiated` audit 和 connection log，供 Task 8/诊断继续展示。
- 响应和审计元数据不返回 invite token、invite code、relay join token、relay token hash 或 private key。

## 自动化覆盖

- 候选地址注册、IPv4-only 校验、去重和排序。
- 同网授权下发 LAN direct candidate pair。
- fake dialer LAN direct 成功进入 `lan_direct_connected`。
- fake dialer LAN direct 失败进入 `fallback_relay`，并携带创建 Relay session 的语义。
- 无候选时不误报 direct；Hub 协商返回 Relay fallback，纯 p2p connector 在禁用 fallback 时返回 `failed`。
- 跨 account、跨 network、revoked device、offline device 均拒绝协商。
- HTTP client/server P2P 控制面流通过，并验证响应不泄露敏感字段或本次邀请凭据。
- 现有 Relay session、Relay runtime 和 `mesh-cloudhub` 入口继续通过既有测试。

## 已运行测试

```powershell
go test ./internal/p2p
```

结果：PASS。

```powershell
go test ./internal/cloudhub
```

结果：PASS。

```powershell
go test ./internal/p2p ./internal/cloudhub ./internal/relay ./cmd/mesh-cloudhub
```

结果：PASS。

```powershell
go test ./...
```

结果：PASS。

## Smoke 说明

- 本任务没有运行真实双机 LAN direct 或公网 direct smoke：当前交付是控制面协议、候选模型和 LAN direct fake dialer 骨架，尚未接真实客户端 socket/transport。
- LAN direct 成功、LAN direct 失败回 Relay 和无候选失败/回退均由 `internal/p2p` 自动化 fake dialer 覆盖。

## 未完成事项

- 公网 direct 真实 E2E。
- 复杂 NAT 探测、UDP/QUIC 打洞、多路径热切换和质量评分。
- 客户端 UI 连接方式展示、P2P 诊断中心和用户可见诊断动作。
- Windows RDP 端到端 direct/relay 自动切换验收。
- 真实双机 LAN direct smoke 与网络实验室矩阵。
