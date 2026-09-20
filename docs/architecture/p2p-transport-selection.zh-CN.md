# P2P 传输选型与可审计接入边界

日期：2026-07-09

## 背景

Task 6 已完成 LAN/public TCP direct 和 Relay fallback。Task 7A 已完成 NAT 探测摘要和控制面回传。Task 7B 在此基础上建立传输选择层，但不把真实 QUIC、STUN/TURN、NAT 打洞状态机或 Windows RDP 数据面接入生产路径。

## 候选路线

| 路线 | 当前定位 | 优点 | 风险 | Task 7B 决策 |
| --- | --- | --- | --- | --- |
| TCP/TLS (`tcp_tls_v1`) | 默认 direct 传输 | 与现有候选地址、拨号器和 Relay fallback 兼容；不新增依赖；易审计 | 穿透能力弱，复杂 NAT 下成功率有限 | 继续作为默认和回滚路径 |
| 标准库 UDP datagram (`udp_datagram_v1`) | 实验候选和本地 smoke | 不新增第三方依赖；可验证 datagram 接口、超时、取消和审计语义 | 无拥塞控制、重传、加密和会话层；不能直接承载 RDP 数据面 | 仅做 loopback/fake smoke，不跨公网 |
| QUIC (`quic_v1`) | 未来候选传输 | 自带流、多路复用、拥塞控制和 TLS 语义，更适合后续数据面 | 需要第三方依赖或运行时选型；维护、许可证、安全升级和平台兼容需单独审查 | 本任务只保留候选枚举和选择结果，不引入依赖 |

## 选择规则

- 默认策略不暴露实验传输，仍选择 `tcp_tls_v1` 或 Relay fallback。
- 只有策略显式允许，并且双方 NAT 摘要都存在且 UDP 可用时，才返回 `udp_datagram_v1` 或 `quic_v1` 候选。
- 任一端 NAT 摘要建议 Relay、UDP 被阻断、探测失败或对称 NAT 时，排除 UDP/QUIC 候选，并按既有 Relay fallback 处理。
- Cloud Hub 仍先校验账号、网络、设备、在线状态和吊销状态；传输选择不绕过控制面授权。
- 传输选择只输出稳定原因和候选类型，不复制底层探测原文或敏感材料。

## 审计点

- `transport_selection.preferred.kind` 记录首选传输。
- `transport_selection.preferred.experimental` 标记实验传输。
- `transport_selection.candidates` 记录可尝试候选。
- `transport_selection.excluded` 记录被策略或 NAT 摘要排除的候选。
- Cloud Hub P2P negotiation audit metadata 记录首选传输和是否实验传输。

## 回滚

- 关闭实验传输策略即可回到 `tcp_tls_v1`。
- 如果没有 direct 候选或 NAT 摘要要求 Relay，仍使用既有 Relay fallback。
- 本任务未改候选地址归一化规则，未让客户端注册 UDP 候选地址，因此回滚不需要迁移已存候选数据。

## 未完成

- 真实 QUIC 依赖接入和许可证复核。
- STUN/TURN 服务接入。
- UDP/QUIC 跨公网 E2E。
- NAT 打洞状态机。
- 多路径、质量评分和热切换。
- 真实 Windows RDP 数据面桥接。
