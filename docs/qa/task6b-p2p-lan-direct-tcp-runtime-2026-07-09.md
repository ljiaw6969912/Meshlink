# Task 6B P2P LAN Direct TCP Runtime 与本地进程级 Smoke

日期：2026-07-09

## 交付边界

- `internal/p2p` 新增真实 TCP direct probe runtime：`TCPDialer` 使用 `net.Dialer.DialContext` 对候选 pair 的目标 IPv4/TCP 地址发起短连接探测，成功后立即关闭连接。
- `TCPDialer` 默认使用有限超时，并尊重调用方 `context` 的 timeout/cancel；不会无限阻塞。
- 当前 runtime 仅支持 IPv4/TCP candidate；IPv6、非 TCP protocol、非法端口会被拒绝。
- `Connector` 继续复用 Task 6A 状态机：LAN candidate probe 成功返回 `lan_direct_connected`；连接失败、listener 关闭、无候选时在 Relay fallback 开启时返回 `fallback_relay`，fallback 禁用时返回 `failed`。
- 本任务没有默认启动任何长期开放的生产 P2P listener；真实 listener 仅存在于 Go 测试 helper 中。
- Cloud Hub 控制面 smoke 通过 HTTP client/server 流程创建 account、network、两台 device、heartbeat online、注册候选、协商 candidate pair，再用真实 TCP probe 验证 LAN direct。

## 自动化覆盖

- `internal/p2p/tcp_test.go`
  - loopback `net.Listen("tcp4", "127.0.0.1:0")` listener 可连通时，`Connector + TCPDialer` 返回 `PathType=lan_direct` 与 `State=lan_direct_connected`。
  - listener 关闭后再次 probe，返回 `fallback_relay`，并保持 Relay fallback 的“创建 Relay session 语义”而不生成 join token。
  - Relay fallback 禁用时，closed listener probe 失败返回 `failed`，不强制进入 Relay。
  - 已取消 context 与已过期 deadline 会立即返回对应 context error，不挂死。
  - 非 TCP 或 IPv6 candidate 被拒绝。
- `internal/cloudhub/p2p_tcp_runtime_test.go`
  - 通过 Cloud Hub HTTP client 流程完成 P2P 控制面协商，并用真实 loopback TCP listener 验证 direct probe 成功。
  - listener 关闭后复用协商流程，真实 TCP probe 失败并返回 `fallback_relay`。
  - fallback 只表达语义，不创建 Relay session；账号 summary 中 `relay_sessions.total` 保持为 `0`。
  - offline、revoked、跨 network 目标仍由控制面拒绝，不会误报 direct。
  - negotiation、connection result、summary JSON 均通过敏感字段扫描；本次测试不会把 invite token、invite code、relay join token、token hash 或 private key 写入响应、日志或文档。
- 既有 Relay session/runtime 测试继续作为回归覆盖，确认 Task 6B 没有破坏现有 Relay fallback 后续路径。

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

- 本地真实 TCP smoke 使用同进程 loopback listener，不依赖测试机密码、invite token、invite code、relay join token 或私钥。
- direct 成功路径只验证 TCP 连接可建立，不承载 RDP 数据面。
- fallback 路径仅返回 `relay_fallback.create_relay_session=true` 的语义；不会在 P2P negotiation 或 connector result 内创建 Relay session 或生成 join token。

## 未完成事项

- 真实双机 LAN direct smoke。
- public direct 与公网地址可达性验证。
- RDP 数据面 direct/relay 桥接。
- 复杂 NAT 探测、UDP/QUIC 打洞、多路径质量评分和热切换。
- UI 连接诊断、用户可见 P2P 诊断动作和连接方式展示。
