# Task 6C P2P Public Direct Smoke

日期：2026-07-09

## 范围

- 验证 P2P candidate pair 在 IPv4/TCP 下按 `lan_direct` 优先、`public_direct` 次之排序，并且不把 `relay` candidate 当作 direct pair。
- 验证 `Connector` 在 LAN direct 失败后继续尝试 public direct，public TCP 可达时返回 `PathType=public_direct` 与 `State=public_direct_connected`。
- 验证 public direct 失败后按 Relay fallback 开关返回 `fallback_relay` 或 `failed`，fallback 只表达创建 Relay session 的语义，不生成 Relay join token。
- 验证 Cloud Hub HTTP client/server 协商能在 source/target 都注册 public candidate 后下发 `public_direct` candidate pair，并保持同 account/network、online、未 revoked 的授权边界。
- 验证一次真实公网 TCP smoke：本地通过 `TCPDialer`/`Connector` 连接公网 VM 的 `175.42.58.100:8443`。

## Public Candidate 排序与状态机

- `BuildCandidatePairs` 只为 `lan` 和 `public` scope 生成 direct pair；`relay` scope 不进入 direct pair。
- 即使 public candidate priority 更高，排序仍保持 `lan_direct` 在 `public_direct` 之前。
- `Connector` 会按协商顺序逐个尝试 direct pair：
  - LAN pair 失败后继续尝试 public pair。
  - public pair 成功时返回 `public_direct_connected`。
  - public pair 全部失败且 Relay fallback 开启时返回 `fallback_relay` 与 `PathType=relay`。
  - public pair 全部失败且 Relay fallback 关闭时返回 `failed`，不强制创建 Relay session。
- `TCPDialer` 仍保持 IPv4/TCP only，并尊重调用方 context cancel/deadline。

## Cloud Hub 协商

- HTTP client/server smoke 覆盖了 account、network、invite、两台 device、heartbeat online、注册 public candidate、查询 target candidate、协商 P2P。
- 协商结果包含一个 `public_direct` candidate pair，`PreferredPathType=public_direct`，`State=connecting`，并保留 Relay fallback 的 create-on-failure 语义。
- Account summary 中记录 `p2p_candidates_registered` 与 `p2p_connection_negotiated` audit event，connection log 的 path type 为 `public_direct`。
- 负面授权覆盖：
  - target offline 时拒绝协商，不记录 public direct connection log。
  - target revoked 时拒绝协商，不记录 public direct connection log。
  - target 位于另一 network 时拒绝协商，不误报 public direct。

## 真实公网 TCP Smoke

- 新增默认跳过的测试入口：`TestTCPDialerPublicDirectSmokeFromEnv`。
- 默认未设置 `MESHLINK_P2P_PUBLIC_SMOKE_ADDR` 时 `t.Skip`，因此 `go test ./...` 不依赖公网环境。
- 实测命令使用：
  - `MESHLINK_P2P_PUBLIC_SMOKE_ADDR=175.42.58.100:8443`
  - `MESHLINK_P2P_PUBLIC_SMOKE_FALLBACK_ADDR=240.0.0.1:8443`
- Smoke 过程：
  - 远端 `8443` 原有 `mesh-agent` 监听，测试窗口内临时停止并在测试后恢复。
  - 远端启动一次性 TCP listener，监听 `0.0.0.0:8443`，只 accept 一次。
  - 本地 Go smoke 返回 `public_direct_connected`。
  - 远端 listener 打印 `ACCEPTED`，证明连接实际到达 `175.42.58.100:8443`。
  - fallback 负例使用明确不可达的 public candidate `240.0.0.1:8443`，返回 `fallback_relay`。
  - 测试后确认远端 `mesh-agent` 已恢复监听 `0.0.0.0:8443`。

说明：当前网络路径会让 `175.42.58.100` 的多个非监听端口也完成 TCP connect，因此负例没有使用同一 IP 的相邻端口，以避免把中间层 TCP accept 误判成端到端 public direct 成功。

## 敏感字段边界

- P2P negotiation/result JSON 断言不包含 `join_token`、`invite_token`、`invite_code`、`private_key`。
- Cloud Hub 协商、summary、audit metadata 断言不泄露 invite token/code、relay join token、private key。
- 本文档不记录测试 VM 密码、invite token、invite code、relay join token、私钥或完整敏感 SSH 命令。

## 验收命令

- `go test ./internal/p2p -run "Test(BuildCandidatePairsOrdersLANDirectBeforePublicDirectAndSkipsRelay|ConnectorTriesPublicDirectAfterLANDirectFailure|ConnectorHandlesPublicDirectFailureWithRelayFallbackPolicy|TCPDialerPublicDirectSmokeFromEnv)" -count=1 -v`
- `go test ./internal/cloudhub -run "TestClientP2P(ControlPlaneNegotiatesPublicDirectAgainstHTTPServer|PublicDirectAuthorizationBoundariesDoNotNegotiateDirect)" -count=1 -v`
- `go test ./internal/p2p -run TestTCPDialerPublicDirectSmokeFromEnv -count=1 -v`，带公网 smoke 环境变量。
- `go test ./internal/p2p ./internal/cloudhub ./internal/relay ./cmd/mesh-cloudhub`
- `go test ./...`

## 仍未完成事项

- 真实 RDP 数据面 direct/relay 桥接。
- 复杂 NAT、端口保持、NAT hairpin、双 NAT 场景。
- UDP/QUIC/NAT 打洞。
- 多路径质量评分、延迟/丢包/带宽采样与路径切换策略。
- UI 诊断展示与用户可见的当前路径状态。
