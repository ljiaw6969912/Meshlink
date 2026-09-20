# P2P NAT 探测与网络实验室矩阵

日期：2026-07-09

## 使用范围

本矩阵用于 Task 7A 之后的 P2P 网络实验室验收。Task 7A 只提供 NAT 探测模型、纯函数分类器、Cloud Hub 摘要保存/回传和自动化等价测试；真实 STUN 服务接入、UDP/QUIC 数据面、打洞状态机、多路径/热切换、UI 文案和真实 Windows RDP 数据面桥接仍未完成。

所有手工实验只记录 NAT 类型、UDP 可用性、映射稳定性、路径状态、耗时、错误类别和 Relay 用量等最小诊断指标。不得记录任何凭据、密钥、一次性接入材料、真实公网凭据或完整内网扫描结果。

## 矩阵

| 场景 | 目标 | 自动化等价测试 | 手工网络条件 | 通过标准 | 失败回滚/降级行为 | 采集指标 |
| --- | --- | --- | --- | --- | --- | --- |
| Open internet / no NAT | 识别公网直连端点，确认无需 NAT 转换时可优先尝试 direct。 | `internal/p2p` 中 open internet 观测：本地地址端口等于映射地址端口，changed address/port 均响应，分类为 `open_internet`。 | 设备直接获得公网 IPv4，防火墙允许 UDP 入站探测和后续 direct 端口。 | `udp_available=true`、`mapping_stable=true`、`hole_punch_recommended=true`、`relay_recommended=false`；Cloud Hub heartbeat/list/negotiation 可看到摘要。 | 如果 direct 连接仍失败，进入既有 Relay fallback；不得暴露公网测试凭据。 | NAT 类型、探测耗时、成功观测数、direct 尝试耗时、最终路径、Relay 是否创建。 |
| Full cone NAT | 识别稳定映射且对 changed address 探测开放的 NAT，作为最容易打洞的一类。 | 两个 STUN-like 观测返回同一映射，changed address/port 均响应，分类为 `full_cone`。 | 家用路由器或可配置 NAT 设备开启 full-cone/endpoint-independent mapping/filtering。 | 摘要为 `full_cone`，建议尝试打洞；Cloud Hub 不改变账号/网络边界。 | 后续 UDP 打洞失败时 Relay fallback；不得继续重试到影响用户网络。 | 映射地址是否稳定、direct 成功率、打洞尝试次数、fallback 原因。 |
| Restricted cone NAT | 识别映射稳定但只允许已联系远端地址返回的 NAT。 | 稳定映射，changed port 响应、changed address 不响应，分类为 `restricted_cone`。 | NAT 过滤策略按远端地址限制，但同地址不同端口可返回。 | `mapping_stable=true`、`hole_punch_recommended=true`，诊断原因可读。 | 打洞状态机未完成前只记录建议；真实连接失败仍走 Relay。 | NAT 类型、端口映射、候选对数量、fallback 是否发生。 |
| Port restricted cone NAT | 识别映射稳定但远端地址和端口都受限的 NAT。 | 稳定映射，changed address/port 均不响应，分类为 `port_restricted_cone`。 | 常见家用或企业 NAT，要求双方同时向对方映射端口发包。 | 摘要建议 coordinated hole punching，Relay 作为后备；普通响应不包含原始观测地址列表。 | 后续打洞失败时立即 Relay fallback，不提示用户手工开放敏感端口。 | 映射稳定性、候选优先级、direct 尝试失败类别、Relay 用量。 |
| Symmetric NAT | 识别对不同探测服务器产生不同外部端口/地址的 NAT，并优先 Relay。 | 两个观测返回不同映射，分类为 `symmetric_nat`；Cloud Hub negotiation 在任一端 `relay_recommended=true` 时直接返回 `fallback_relay`。 | 企业网、部分运营商网络或严格家用网关，按目的地址/端口分配不同映射。 | `mapping_stable=false`、`hole_punch_recommended=false`、`relay_recommended=true`；协商结果 preferred path 为 `relay`。 | 不做 UDP 打洞强试；直接 Relay fallback，若 Relay quota 不足则明确失败。 | NAT 类型、Relay 创建结果、quota 状态、连接错误、用户可见诊断。 |
| CGNAT | 验证运营商级 NAT 下的诊断和降级，不把 CGNAT 误报为可公网入站。 | 以 symmetric 或 port-restricted 等价观测覆盖；候选注册不依赖真实公网入站能力。 | 宽带或移动网络处于运营商 CGNAT，设备 WAN 地址为私网/共享地址。 | 摘要不声称公网可入站；若映射不稳定或 direct 失败，Relay fallback 生效。 | Relay 可用则降级 Relay；Relay quota exceeded 时失败闭合并提示配额原因。 | WAN 地址类别、NAT 摘要、direct 失败原因、Relay session 状态、quota。 |
| UDP blocked | 识别 UDP 无响应网络，避免无意义打洞。 | 所有观测无 UDP 响应且无成功映射，分类为 `udp_blocked`。 | 企业/校园/酒店网络或本机防火墙阻断 UDP 出站/入站。 | `udp_available=false`、`relay_recommended=true`；协商不建议打洞。 | 直接 Relay fallback；若 Relay 不可用，返回失败而不误报 direct 成功。 | UDP 响应数、失败类别、Relay 是否可用、用户诊断原因。 |
| 企业/校园/酒店网络 | 验证强代理、门户、防火墙、DNS 劫持等复杂网络下的可解释失败。 | 使用 UDP blocked、symmetric NAT、Relay quota exceeded 组合测试覆盖。 | 需要网页登录门户、UDP 限制、出站白名单或 TLS inspection 的网络。 | 不泄露门户凭据或环境细节；可记录最小错误类别并走 Relay 或失败闭合。 | 无法 direct 时 Relay；Relay 也被阻断时提示网络策略阻断，不自动扩展公网出口能力。 | NAT 类型、HTTP/Relay 连接错误类别、延迟、丢包、fallback 原因。 |
| 移动热点 | 验证移动网络高抖动、CGNAT 和地址切换时摘要更新。 | 多次 heartbeat 携带不同 NAT 摘要，Cloud Hub 保存最新摘要；mapping unstable 分类覆盖。 | 手机热点、蜂窝网络、切换基站或飞行模式重连。 | 最新 heartbeat 覆盖旧摘要；协商使用最新摘要；敏感材料不进入日志。 | 地址切换导致 direct 失败时 Relay fallback；旧候选过期后不继续使用。 | 摘要更新时间、候选 TTL、连接中断次数、最终路径、Relay 用量。 |
| 双 NAT | 验证多层家用路由/虚拟化 NAT 下的稳定性判断。 | 用 port-restricted 或 symmetric 观测覆盖；候选过期和 fallback 自动化覆盖。 | 光猫路由 + 家用路由、虚拟机 NAT + 家用路由。 | 不要求识别“双 NAT”专用类型，但必须给出稳定/不稳定映射和建议路径。 | 稳定时可尝试打洞；不稳定或失败时 Relay fallback。 | NAT 类型、映射稳定性、candidate pair、direct 失败原因、Relay 成功率。 |
| Relay quota exceeded | 验证 direct 不可用且 Relay 配额不足时失败闭合。 | 复用 Cloud Hub policy/quota 与 P2P fallback 测试：Relay session 创建返回 `quota exceeded`。 | 使用低配额测试账号或预先耗尽 Relay 字节/会话配额。 | 不创建未授权 Relay session；结果包含 quota 类错误，不暴露一次性接入材料。 | 停止连接，提示配额/策略；不得绕过 Relay 风控或尝试未授权代理。 | quota 名称、已用量、剩余额、失败时间、风险事件。 |
| Direct success | 验证 direct 候选成功时不创建 Relay。 | `internal/p2p` fake dialer 成功、TCP runtime smoke 成功、Cloud Hub connection log 记录 direct path。 | 同 LAN、公网可达或未来 UDP 打洞成功网络。 | 最终路径为 LAN/public/UDP direct；Relay session 数为 0；诊断不误报 fallback。 | 如果后续质量下降，Task 7D 以后再处理热切换；Task 7A 不实现。 | 连接耗时、路径类型、尝试次数、延迟、吞吐、Relay session 数。 |
| Relay fallback | 验证 direct 失败、NAT 建议 Relay 或无候选时自动降级。 | Task 6D 自动 fallback 测试、Task 7A NAT relay recommendation negotiation 测试。 | 任一端 UDP blocked/symmetric、无 direct 候选、direct TCP 拒绝或 Relay-only 网络。 | 协商或连接结果为 `fallback_relay`，Relay session 授权成功且不返回一次性接入材料明文。 | Relay 不可用或策略拒绝时失败闭合，不误报连接成功。 | fallback reason、Relay session id、Relay endpoint、用量、关闭状态、错误类别。 |

## Task 7A 未完成边界

- 未接入真实 STUN/TURN 服务，也未选择供应商或公网探测地址。
- 未实现 UDP/QUIC 数据面、QUIC 依赖、UDP 打洞状态机或重传策略。
- 未实现路径质量评分、多路径并行探测、热切换或断线续连策略。
- 未实现 UI 文案、诊断面板或用户可操作的网络修复向导。
- 未实现真实 Windows RDP 数据面桥接。
- 未要求真实公网测试机；如后续需要外部网络验证，应使用临时安全凭据并只记录脱敏指标。
