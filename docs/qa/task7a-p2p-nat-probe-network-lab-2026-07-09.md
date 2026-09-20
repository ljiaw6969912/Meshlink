# Task 7A P2P NAT 探测模型与网络实验室矩阵

日期：2026-07-09

## 交付边界

- 新增 `internal/p2p` NAT probe 纯模型与分类器，不依赖真实公网 STUN/TURN/QUIC 服务。
- NAT 摘要表达类型、UDP 可用性、映射稳定性、是否建议打洞、是否建议 Relay fallback 和可读原因。
- Cloud Hub heartbeat 可携带最新 NAT probe 摘要；服务端保存规范化后的摘要，并在设备列表与 P2P negotiation 响应中回传。
- 当任一端 NAT 摘要明确建议 Relay 时，P2P negotiation 直接返回 `fallback_relay`，即使已有 public candidate pair。
- 新增网络实验室矩阵 `docs/qa/network-lab-matrix.zh-CN.md`，覆盖 NAT 类型、复杂网络、配额失败、direct success 和 Relay fallback。

## NAT 分类器覆盖

- `unknown`：无观测输入。
- `open_internet`：本地端点与映射端点一致，changed address/port 均响应。
- `full_cone`：跨探测服务器映射稳定，changed address/port 均响应。
- `restricted_cone`：映射稳定，changed port 响应，changed address 不响应。
- `port_restricted_cone`：映射稳定，changed address/port 均不响应。
- `symmetric_nat`：不同探测服务器得到不同外部映射。
- `udp_blocked`：没有 UDP 响应。
- `probe_failed`：探测过程失败且没有成功响应。

## 控制面覆盖

- Heartbeat 写 NAT 摘要时要求 `account_id`、`network_id` 与设备归属一致，并要求设备以 online 状态发布摘要。
- revoked 设备无法通过 heartbeat 写入 NAT 摘要。
- 跨账号、跨网络设备无法通过 negotiation 读取对方 NAT 摘要。
- P2P negotiation 回传 source/target 的规范化摘要；摘要只包含分类和建议，不包含原始观测地址、端口清单或底层错误文本。
- 普通 JSON 响应继续通过敏感数据断言，避免暴露凭据、密钥或一次性接入材料。

## 自动化测试

```powershell
go test ./internal/p2p ./internal/cloudhub
```

结果：PASS。

```powershell
go test ./...
```

结果：PASS。

## 风险边界

- NAT 分类器只消费调用方传入的 STUN-like 观测；Task 7A 不执行真实公网探测。
- CGNAT 在本任务中通过 symmetric、port-restricted、UDP blocked 等观测等价覆盖；没有新增独立 CGNAT 枚举。
- Heartbeat 摘要保存是最小内存状态实现；持久化数据库 schema、迁移和多实例同步不在本任务范围。
- Task 7A 不实现 UDP/QUIC 数据面、真实打洞状态机、路径质量评分、多路径/热切换、UI 文案或真实 Windows RDP 数据面桥接。
- 后续如需外部网络验证，应只记录脱敏指标，不把任何凭据或真实公网接入材料写入仓库、日志或 QA 文档。
