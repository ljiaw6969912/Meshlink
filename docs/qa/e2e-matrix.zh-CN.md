# E2E 网络测试矩阵

日期：2026-09-14

## 目的与发布判定

本矩阵覆盖自建 `mesh-agent` v2 纯 P2P 数据面的真实网络门禁，以及保留的官方云实验后端场景。自建 v2 以 E2E-NET-001 至 005 为准；E2E-NET-006 之后涉及官方 Relay/额度的场景只验证独立云实验后端，不表示自建桌面或 `mesh-agent` v2 具有 Relay、Hub 数据转发或 TCP 回退。

任一标为“发布候选必须”的真实环境项未执行、失败或证据不完整时，对应阶段应判定为“暂缓”，不能用仓库自动化、单机回环、fake dialer 或历史记录代替。

在 E2E-NET-001（三台 Windows）、002（两个独立 NAT）和 003（协调服务器物理网卡抓包）全部取得当前候选的 M 级证据前，自建 v2 唯一允许的状态是：`实现完成，公网实测待验收`。

## 证据分级与记录规则

| 级别 | 能证明什么 | 不能证明什么 |
| --- | --- | --- |
| A：仓库自动化 | 纯逻辑、授权边界、API、状态机、额度、诊断文案及进程内运行时行为。 | 真实 NAT、防火墙、运营商、企业网、Windows 服务/TUN、系统 RDP 会话。 |
| S：本机模拟 | `httptest`、临时目录、回环 TCP/UDP、隔离运行目录、模拟 SSH 或 fake dialer 下的可重复闭环。 | 两台真实设备、不同 NAT、公网可达性、真实云主机、真实 RDP 数据面。 |
| M：真实环境手工 | 指定设备、真实网络和真实系统服务下的用户主流程与可见结果。 | 仅对记录的版本、环境和时段有效，不能自动外推到所有网络。 |
| H：历史记录 | 说明此前某一版本和环境曾执行，可帮助复用步骤与风险点。 | 不能作为当前发布候选的 M 级通过证据。 |

执行记录只保留版本、场景 ID、脱敏网络类型、步骤时间、路径状态、错误类别、诊断包校验值和回滚结果。不得保存任何实际凭据、一次性接入材料、密钥内容、真实公网主机地址或远程桌面内容。

## 执行前公共检查

1. 记录待测 commit、客户端/服务端版本、操作系统版本、场景 ID、开始时间和执行人角色。
2. 使用专用测试账号、设备、网络和额度；确认可撤销、可恢复，且不含生产数据。
3. 确认两端时钟同步；需要判断 5 分钟或 15 秒门禁时，使用同一计时来源。
4. 自建 v2 预先确认诊断报告、客户端日志、Windows 事件日志、`coordinator_state`、`p2p_listen`、会话 generation 和协调服务器物理网卡 pcap 的导出位置；官方云实验另行记录其最小元数据。
5. 先验证清理路径：可撤销测试设备、恢复 TCP/UDP 防火墙规则、恢复 RDP 设置、撤销临时成员与设备授权；官方云实验的 Relay 清理不得混入自建 v2 结果。

## 场景索引

| 场景 ID | 核心场景 | 证据要求 | 发布候选要求 |
| --- | --- | --- | --- |
| E2E-NET-001 | 三台 Windows 同 LAN 纯 P2P | A + S + M | 自建 v2 必须 |
| E2E-NET-002 | 两个独立 NAT 的 UDP/QUIC 直连 | A + S + M | 自建 v2 公网门禁必须 |
| E2E-NET-003 | 协调服务器抓包与离线存活 | A + S + M | 自建 v2 必须 |
| E2E-NET-004 | v1 备份与原子迁移 | A + S + M | 升级发布必须 |
| E2E-NET-005 | UDP 不可达且无中继回退 | A + S + M | 自建 v2 必须 |
| E2E-NET-006 | 官方 Hub 不同 NAT RDP | A + S + M | 阶段三必须 |
| E2E-NET-007 | CGNAT 降级 | A + S + M | 阶段三、四必须 |
| E2E-NET-008 | 公司网络受限出口 | A + S + M | 阶段三、四必须 |
| E2E-NET-009 | 酒店/校园网络 | A + S + M | 阶段三、四必须 |
| E2E-NET-010 | Relay 自动回退 | A + S + M | 阶段三、四必须 |
| E2E-NET-011 | Relay 超额失败闭合 | A + S + M | 阶段三、五必须 |
| E2E-NET-012 | 目标 RDP 关闭 | A + S + M | 阶段一至四必须 |
| E2E-NET-013 | 官方账号封禁 | A + S + M | 阶段三必须 |
| E2E-NET-014 | 单设备吊销隔离 | A + S + M | 阶段三、五必须 |
| E2E-NET-015 | 无权限连接拒绝 | A + S + M | 阶段四、五必须 |
| E2E-NET-016 | 团队授权连接与审计 | A + S + M | 阶段五必须 |
| E2E-NET-017 | 十设备批量部署与单机重试 | A + S + M | 阶段五必须 |
| E2E-NET-018 | 隔离网私有 Hub | A + S + M | 阶段五必须 |

## E2E-NET-001 三台 Windows 同 LAN 纯 P2P

- **适用阶段/发布门禁：** 自建 v2 的 AC-01、AC-03、AC-04、AC-05、AC-07、AC-15；每个候选必须执行。
- **环境与前置条件：** 三台独立 Windows：A 只运行协调服务器，B/C 运行真实 Wintun 和当前候选；三机位于同一 LAN，C 启用 RDP。A 的同一数字端口同时放行 TCP 与 UDP；B/C 防火墙允许 `mesh-agent` 的单一 P2P UDP socket。
- **最短步骤：** A 创建 v2 网络；B/C 加入并记录 `p2p_listen`；B 首次向 C 虚拟 IP 发包并完成 RDP，再验证 C→B。保持双向业务，停止 A 至少三个 Peer 心跳周期并确认数据继续。A 离线时断开 B↔C，确认 `waiting_coordinator` 且旧 generation 不重用；恢复 A，确认更大 generation 和直连恢复。
- **预期路径：** 只有真实 QUIC 与双向 `SessionHello` 完成后显示 `lan_direct`。A 离线时健康直连保持，协调状态单独显示“重连中”；直连自身断开后不经 A、第三台设备或 TCP 转发。
- **用户可见结果：** 协调服务器和对端路径分维显示；候选或成员在线不冒充直连；A 离线时显示“协调服务器离线，当前直连不受影响”。
- **自动化/手工级别与证据：** A/S 的真实 socket/QUIC 集成只证明本机进程行为；M 必须记录三台 Windows、真实 Wintun、双向虚拟 IP/RDP、状态序列、session/generation、时间和清理结果。本机 loopback 不能替代。
- **失败采集项：** 三端脱敏诊断、A 的 TCP/UDP 监听、B/C `p2p_listen`、会话状态/generation、心跳时间、RDP 可交互结果和稳定错误码；不采集 RDP 内容。
- **清理/回滚：** 断开 RDP，恢复网络和防火墙，移除测试设备/邀请，停止服务，确认 CA、证书和 registry 未被覆盖。
- **执行频率：** 每个自建 v2 发布候选；控制/会话/状态/UI/TUN 变化后加跑。
- **责任边界：** QA 负责三机和证据；Windows 负责人确认 Wintun/RDP；P2P 负责人核对会话真值，禁止用默认状态制造通过。

## E2E-NET-002 两个独立 NAT 的 UDP/QUIC 直连

- **适用阶段/发布门禁：** 自建 v2 的 AC-01、AC-03、AC-05、AC-08；公网发布必须执行。
- **环境与前置条件：** A 位于公网可达环境；其协调端口的 TCP/UDP 同时放行。B/C 为两台真实 Windows，分别位于两个普通家庭 NAT 后，无公网入站和人工端口映射；目标 RDP 已启用，两端允许 `mesh-agent` UDP。
- **最短步骤：** B/C 加入并刷新服务器观察候选；从 B 发起 C 的虚拟 IP/RDP 流量，确认双向 UDP 打洞和 QUIC 数据。保持业务时停止 A 至少三个心跳周期；再断开 B↔C 并恢复 A，确认等待和新 generation 重协商。
- **预期路径：** 真实握手完成后为 `public_direct`；业务流量直接 B↔C，A 只承载控制和小型认证探测。没有 Relay、Hub 转发或 TCP 数据回退。
- **用户可见结果：** 显示“公网直连”；失败时显示稳定错误码及“本版本未启用中继”，不承诺所有 NAT 均可达。
- **自动化/手工级别与证据：** 本机 UDP/QUIC、fake dialer、NAT 分类或历史 smoke 都不是此场景 M 证据。必须记录当前候选、两个独立真实 NAT、双向 RDP、候选类型、路径、A 离线存活和恢复结果。
- **失败采集项：** 脱敏 NAT 类型、B/C 实际 `p2p_listen`、A 协调端口、候选更新时间、打洞/QUIC 耗时、generation、错误码和 RDP 诊断。
- **清理/回滚：** 恢复网络与防火墙；撤销测试设备；确认未遗留端口映射、临时公网规则或抓包文件中的敏感地址。
- **执行频率：** 每个公网发布候选；UDP、候选、QUIC、心跳或重协商变化后加跑。
- **责任边界：** 网络实验室保证两个独立 NAT；QA 验证 RDP；P2P 负责人核对直连和 A 无数据面，不得用同路由器双主机冒充双 NAT。

## E2E-NET-003 协调服务器抓包与离线存活

- **适用阶段/发布门禁：** 自建 v2 的 AC-01、AC-03、AC-11；三机 LAN 与双 NAT 均要执行。
- **环境与前置条件：** E2E-NET-001 或 002 已建立 B↔C 双向业务；能在 A 的物理网卡抓包并读取协调指标，抓包范围和保存位置已获授权。
- **最短步骤：** 在 A 上以 `tcp.port == <协调端口> || udp.port == <协调端口>` 捕获控制/探测；持续产生 B↔C RDP/虚拟 IP 流量；对比 A 停止前后 B/C 业务；A 恢复后观察重连、候选刷新和授权。另用受控 v2 客户端发送一次 `TypePacket`，确认连接被关闭且违规计数增加。
- **预期路径：** A 只出现低流量 TLS 控制与小型 UDP 探测；无内部 IPv4 明文、无 `TypePacket` 转发、无随 B↔C 业务同步增长的用户字节。A 停止不关闭健康会话。
- **用户可见结果：** A 停止时协调状态变化，真实 direct 路径和在线状态保持；违规客户端得到受控协议错误。
- **自动化/手工级别与证据：** 协调指标和本机集成是 A/S；M 必须保存脱敏 pcap 的 SHA-256、接口、过滤器、时间窗口、包/字节摘要以及 B/C 同期业务证据，不能只截取 UI。
- **失败采集项：** A 指标、pcap 摘要、B/C 会话 ID/generation/心跳、停止恢复时间、违规错误码；不得保存用户内容或完整公网地址。
- **清理/回滚：** 停止抓包并安全删除未脱敏原件；恢复 A；关闭违规客户端；确认 B/C 和非目标设备对状态收敛。
- **执行频率：** 每个自建 v2 发布候选；协调协议、指标、会话生命周期或状态投影变化后加跑。
- **责任边界：** QA 控制业务和计时；网络负责人审查 pcap；安全负责人确认最小留存；实现者不能用“代码没有转发函数”代替抓包证据。

## E2E-NET-004 v1 备份与原子迁移

- **适用阶段/发布门禁：** 所有从 v1/versionless 升级到自建 v2 的候选。
- **环境与前置条件：** A、B、C 各有一份可还原的 v1 测试配置和现有 CA、设备证书、邀请、registry；记录原文件 SHA-256 和原始字节，停止服务后执行。
- **最短步骤：** 依次首次加载 Hub/Spoke v1 配置；验证同目录生成一次 `.v1.bak` 且字节/SHA-256 与原文件一致；检查 v2 活动配置；第二次加载确认备份不变。受控制造备份或替换失败，确认原活动文件不变且服务不启动。最后让所有节点同时使用 v2 加入并复测身份、路由和 RDP。
- **预期路径：** Spoke 保留 node ID、CA/证书、虚拟 IP、路由、MTU、device/setup 并增加 `quic_udp_v1`；Hub 保留身份、监听、证书、邀请和 registry，只留下 `network_cidr`，不再包含 TUN/转发字段。v1/v2 不混跑。
- **用户可见结果：** 升级无需重新输入邀请码；旧客户端收到 `control_upgrade_required`，不会静默回退到旧 Hub 数据转发；失败有可恢复的原文件。
- **自动化/手工级别与证据：** 单元测试可证明原子写入分支；M 必须在发行包和真实服务目录验证文件权限、服务停止/启动、备份校验、证书/registry 保留及升级后真实连接。
- **失败采集项：** 脱敏路径、迁移前后版本、文件 SHA-256/权限、服务退出码、保留字段、错误码和回滚结果；不得采集私钥内容。
- **清理/回滚：** 停止全部节点；恢复完整同版本快照而不是单节点混跑；保留失败现场的活动文件与 `.v1.bak` 校验信息。
- **执行频率：** 每个改变配置版本、迁移、写入或安装/升级流程的候选。
- **责任边界：** 发布负责人提供真实旧版制品；QA 执行文件/服务验证；PKI 负责人核对身份不变，禁止手工编辑备份制造通过。

## E2E-NET-005 UDP 不可达且无中继回退

- **适用阶段/发布门禁：** 自建 v2 的 AC-04、AC-09、AC-15；每个候选必须执行一个真实受限网络。
- **环境与前置条件：** B/C 位于获授权的测试网络；可由网络管理员阻断 Peer UDP 或提供已确认不可打洞的对称 NAT/严格 CGNAT，A 的控制 TCP 保持可用；目标 RDP 状态另行确认正常。
- **最短步骤：** 阻断 B/C 的 P2P UDP，保留 A 的 TCP 控制；发起虚拟 IP/RDP 流量并观察协商。检查桌面、状态 JSON、A 抓包和进程/端口；恢复 UDP 后申请新 generation 并复测。
- **预期路径：** 尝试按 `requesting`→`preparing`→`punching`/`authenticating` 受控结束，最终 `failed` 且错误为 `direct_unreachable_no_relay`（具体较早失败可为 `candidate_unavailable`、`udp_probe_failed`、`hole_punch_timeout` 或 `quic_handshake_failed`）。不得出现 Relay、第三 Peer、Hub `TypePacket` 或 TCP 数据通道。
- **用户可见结果：** 显示“直连失败 · 本版本未启用中继”，不显示虚假 direct，不把网络不可达误报成 RDP 故障。
- **自动化/手工级别与证据：** A/S 只证明状态机和错误映射；M 必须由真实防火墙/NAT 执行并记录管理员批准的策略类别、路径状态、错误码、A 抓包和恢复结果。
- **失败采集项：** UDP 规则摘要、状态时间线、候选/握手错误码、A 端包/字节摘要、是否存在额外数据端口、RDP 独立健康检查；不保存企业拓扑细节。
- **清理/回滚：** 删除阻断规则，确认 `p2p_listen` 恢复收发并通过新的 generation 建链；撤销测试设备和临时诊断包。
- **执行频率：** 每个自建 v2 候选；UDP、错误处理、桌面映射或连接回退逻辑变化后加跑。
- **责任边界：** 网络管理员授权/恢复策略；QA 验证失败闭合；实现者不得启用仓库中保留的官方云 Relay 代码让场景“通过”。

以下 E2E-NET-006 至 018 保留给官方云/企业实验后端。它们涉及的 Relay、额度和审计能力不得出现在自建桌面，不得接入 `mesh-agent` v2 数据面，也不得替代前五项自建门禁。

## E2E-NET-006 官方 Hub 不同 NAT RDP

- **适用阶段/发布门禁：** 阶段三“官方 Hub MVP”手工 E2E；PTR-09、PTR-10。
- **环境与前置条件：** 官方测试 Hub/Relay、专用正常额度账号、两台 Windows 位于不同 NAT 且均无公网入站；目标 RDP 已启用；管理后台可查最小元数据。
- **最短步骤：** 登录账号；创建网络并加入两台设备；确认在线；发起 RDP；确认 Relay 会话、用量与连接元数据；关闭会话并确认用量闭合。
- **预期路径：** 直连不可用时为 `relay`；若测试 NAT 实际允许直连，则另行记录 direct 并通过阻断规则强制执行 E2E-NET-010，不能把偶然 direct 当作 Relay 验收。
- **用户可见结果：** 用户无需理解 NAT、端口映射或证书；显示“中继”并可完成 RDP；后台可按账号、设备和会话定位最小元数据。
- **自动化/手工级别与证据：** A/S 证明账号/网络/设备/API、Relay runtime 和 UI：[`TestOfficialHubAPIFlow`](../../internal/ui/server_test.go#L168)、[`TestCloudHubRelayAPIFlow`](../../internal/cloudhub/cloudhub_test.go#L1082)、[`TestP2PAutomaticRelayFallbackRuntimeSmoke`](../../internal/relay/p2p_fallback_smoke_test.go#L15)。这些使用本机 HTTP/回环或受控 runtime，不是不同 NAT/真实 RDP；M 必须执行。
- **失败采集项：** 两端 NAT 摘要、在线状态、连接耗时、最终路径、Relay 会话/用量、后台检索结果、RDP 诊断和用户提示；不采集内容数据。
- **清理/回滚：** 关闭 Relay/RDP；撤销测试设备、网络与账号；确认会话关闭和用量已结算；恢复阻断规则。
- **执行频率：** 每个阶段三发布候选；官方 Hub、Relay、账号、设备、用量或客户端主流程变化后加跑。
- **责任边界：** QA 执行客户端；官方 Hub/Relay 运维保障测试环境；后台负责人核对元数据；任何本机回环结果都不能标记此场景 M 通过。

## E2E-NET-007 CGNAT 降级

- **适用阶段/发布门禁：** 阶段三、四手工 E2E；PTR-09、PTR-10。
- **环境与前置条件：** 至少一端位于已确认的运营商 CGNAT/蜂窝网络；另一端位于独立 NAT；官方 Relay 可用且额度充足；目标 RDP 已启用。
- **最短步骤：** 记录 WAN 地址类别和 NAT 摘要；两端上线并发起 RDP；观察 direct 尝试与 fallback；切换一次蜂窝连接后重试并确认旧候选不被误用。
- **预期路径：** 不声称公网入站；direct/打洞不可用时自动 `relay`；映射变化后使用最新摘要和候选。
- **用户可见结果：** 显示“中继”或明确失败，不要求用户配置端口映射；诊断说明网络条件需要中继，不暴露底层观测详情。
- **自动化/手工级别与证据：** A/S 只用等价 NAT 观测证明分类、最新摘要与保守降级：[`TestClassifyNATProbeCoversCommonResults`](../../internal/p2p/nat_test.go#L5)、[`TestServiceHeartbeatStoresAndReturnsNATProbeSummary`](../../internal/cloudhub/nat_probe_test.go#L13)、[`network-lab-matrix.zh-CN.md`](network-lab-matrix.zh-CN.md)。这不是实际 CGNAT；M 必须由运营商网络执行。
- **失败采集项：** 运营商网络类型、WAN 地址类别、NAT 摘要、候选更新时间、direct/fallback 耗时、最终路径、重连次数、Relay 用量和错误类别。
- **清理/回滚：** 恢复网络连接；清理旧测试候选/设备和 Relay 会话；确认无蜂窝共享或临时防火墙规则残留。
- **执行频率：** 每个阶段四发布候选至少一次；NAT、候选 TTL、打洞、fallback 或移动网络支持变化后加跑。
- **责任边界：** 网络实验室确认 CGNAT，不以私网地址截图代替完整判断；P2P 负责人判定候选；QA 验证 RDP 与用户文案。

## E2E-NET-008 公司网络受限出口

- **适用阶段/发布门禁：** 阶段三、四手工 E2E；PTR-09、PTR-10。
- **环境与前置条件：** 获授权的企业测试网络，具备 UDP 阻断、出站白名单或 TLS inspection 中至少一种限制；不得绕过企业安全策略；另一端为独立网络。
- **最短步骤：** 在网络管理员批准的测试窗口登录/上线；发起 RDP；记录 UDP/direct/Relay 结果；分别测试 Relay 允许与被策略阻断；运行一键诊断。
- **预期路径：** UDP/direct 被阻断时选择 Relay；Relay 也被阻断时失败闭合，不尝试公网出口、匿名代理或任意端口规避。
- **用户可见结果：** 成功时显示“中继”；失败时明确为网络策略/服务不可达并给出联系管理员等可行动建议，不伪装成 RDP 或额度问题。
- **自动化/手工级别与证据：** A/S 通过 `udp_blocked`、symmetric NAT、runtime unavailable 组合证明决策：[`TestClassifyNATProbeCoversCommonResults`](../../internal/p2p/nat_test.go#L5)、[`TestHolePuncherUsesRelayWithoutDirectTrafficWhenNATRequiresRelay`](../../internal/p2p/hole_punch_test.go#L116)、[`TestP2PAutomaticRelayFallbackDoesNotReportSuccessWhenRuntimeUnavailable`](../../internal/relay/p2p_fallback_smoke_test.go#L164)。真实企业出口策略只能由 M 验证。
- **失败采集项：** 经管理员允许的策略类别、DNS/HTTP/UDP/Relay 错误类别、路径状态、延迟/丢包、诊断报告和时间点；不采集企业内部拓扑或完整代理配置。
- **清理/回滚：** 删除临时白名单/阻断规则；注销测试设备；关闭会话；由网络管理员确认策略恢复。
- **执行频率：** 每个阶段三/四大版本发布候选；传输协议、端口、TLS、Relay 或诊断变化后加跑。
- **责任边界：** 企业网络管理员授权和恢复策略；QA 不绕过控制；产品负责人确认失败闭合边界。

## E2E-NET-009 酒店/校园网络

- **适用阶段/发布门禁：** 阶段三、四手工 E2E；PTR-09、PTR-10。
- **环境与前置条件：** 合法获得的酒店或校园测试接入，可能存在门户、客户端隔离、短租期、UDP 阻断或 DNS 改写；另一端为稳定独立网络；官方 Relay 正常。
- **最短步骤：** 完成门户登录后启动客户端；确认账号/设备上线；发起 RDP；切换一次 Wi-Fi 或重新认证门户后重试；在 Relay 被阻断时确认失败诊断。
- **预期路径：** LAN 客户端隔离时不得误判 `lan_direct`；可用时经 `relay`；门户失效或 Relay 不可达时失败闭合。
- **用户可见结果：** 门户未完成时提示先完成网络认证；成功后显示“中继”；失败时给出网络受限建议，不要求用户修改证书或系统路由。
- **自动化/手工级别与证据：** A/S 只能组合 UDP 阻断、候选变化和 fallback：[`TestClassifyNATProbeMappingStabilityAndRecommendations`](../../internal/p2p/nat_test.go#L117)、[`TestHolePuncherStopsAtRetryLimitAndFallsBackAfterTimeoutsAndHalfConnections`](../../internal/p2p/hole_punch_test.go#L81)、[`TestOneClickReportExplainsRequiredScenarios`](../../internal/diagnose/diagnose_test.go#L34)。真实门户、隔离和 DNS 行为必须 M。
- **失败采集项：** 网络类型、门户完成状态、客户端隔离现象、NAT 摘要、DNS/UDP/Relay 错误类别、最终路径、重连耗时和诊断报告；不保存门户实际凭据。
- **清理/回滚：** 注销门户/测试账号；关闭 Wi-Fi 共享和会话；撤销设备；清理诊断包中的敏感网络标识。
- **执行频率：** 每个阶段四大版本发布候选至少覆盖酒店或校园之一，两个环境每半年各复测；网络栈/诊断变化后加跑。
- **责任边界：** QA 只使用合法接入；网络所有者管理门户；不能用本机防火墙模拟结果冒充真实酒店/校园证据。

## E2E-NET-010 Relay 自动回退

- **适用阶段/发布门禁：** 阶段三、四手工 E2E；PTR-10。
- **环境与前置条件：** 两台不同网络 Windows；初始 direct 可成功；官方 Relay 可用且额度充足；可受控阻断当前 direct 端口；目标 RDP 已启用。
- **最短步骤：** 建立 RDP 并确认 direct；开始计时并阻断 direct；观察路径、用户提示和 RDP 连通性；确认 15 秒内进入 Relay；恢复端口并建立新会话观察策略。
- **预期路径：** `lan_direct`/`public_direct` 失败后 15 秒内 `relay`；不创建未授权会话；direct 成功阶段 Relay 字节为零。
- **用户可见结果：** 自动从“直连”转为“中继”，无需手工切换；若数据面不能保持会话，必须明确展示重连/失败而非静默卡死。
- **自动化/手工级别与证据：** A/S 证明编排和本机双向 Relay 字节闭环：[`TestAutoFallbackConnectorCreatesAndAuthorizesRelayAfterDirectFailure`](../../internal/p2p/fallback_test.go#L14)、[`TestP2PAutomaticRelayFallbackRuntimeSmoke`](../../internal/relay/p2p_fallback_smoke_test.go#L15)、[`Task 6D 证据`](task6d-p2p-relay-automatic-fallback-2026-07-09.md)。direct 失败由 fake dialer 或回环条件制造，不能证明真实网络/RDP 热切换；M 必须执行。
- **失败采集项：** 阻断前后时间、路径状态序列、fallback reason、Relay 授权/会话/用量、RDP 中断时长、用户提示、两端诊断。
- **清理/回滚：** 恢复防火墙/端口；关闭 Relay 和 RDP；确认后续 direct 测试恢复且临时阻断规则消失。
- **执行频率：** 每个阶段四发布候选；路径状态机、Relay runtime、传输选择或 UI 状态变化后加跑。
- **责任边界：** QA 负责计时和用户体验；网络实验室控制阻断；P2P/Relay 负责人分别解释切换与授权；不修改产品行为制造通过。

## E2E-NET-011 Relay 超额失败闭合

- **适用阶段/发布门禁：** 阶段三、五手工 E2E；PTR-46 与 Relay/套餐门禁。
- **环境与前置条件：** 专用低额度官方测试账号；两台不同 NAT Windows；direct 被受控阻断；额度可预置到临界值；后台可查看额度与风险事件。
- **最短步骤：** 先确认额度内 Relay 可用；消耗或预置到上限；再次发起连接；查看客户端额度提示、后台风险/审计和会话列表；补充额度后复测恢复。
- **预期路径：** direct 失败后 Relay 创建被拒，最终状态 `failed`；不得绕过额度、创建未授权会话或误报 Relay 成功。
- **用户可见结果：** 明确显示“中继额度已达上限/超额”和处理建议；不得伪装成普通网络错误；自建能力的免费边界仍清晰。
- **自动化/手工级别与证据：** A/S 证明原子额度门禁、fallback 拒绝和 UI 分类：[`TestP2PFallbackQuotaInsufficientDoesNotAuthorizeRelay`](../../internal/cloudhub/plan_quota_enforcement_test.go#L465)、[`TestServerStopsRelayDataPathAtSessionByteBudget`](../../internal/relay/server_test.go#L89)、[`TestOfficialHubQuotaErrorsPassThroughLocalAPIAsQuotaNotNetworkError`](../../internal/ui/server_test.go#L502)、[`Task 9B 证据`](task9b-plan-quota-enforcement-2026-07-10.md)。真实客户端与官方环境仍需 M。
- **失败采集项：** 套餐/额度维度、已用/上限的脱敏数值、拒绝时间、最终路径、风险/审计事件类型、客户端提示和恢复结果。
- **清理/回滚：** 关闭测试会话；恢复测试额度/账号；删除专用设备；确认没有遗留强制阻断规则。
- **执行频率：** 每个阶段三或五发布候选；套餐、额度、Relay 用量上报、错误映射或订阅 UI 变化后加跑。
- **责任边界：** 计费/套餐负责人预置额度；Relay 负责人核对数据面停止；QA 验证用户提示；不得通过放宽额度逻辑制造通过。

## E2E-NET-012 目标 RDP 关闭

- **适用阶段/发布门禁：** 阶段一至四的 RDP/诊断手工门禁；PTR-08、PTR-09。
- **环境与前置条件：** 网络路径已成功的两台真实 Windows；目标端可受控关闭系统 RDP 或防火墙 3389；传输路径保持可用。
- **最短步骤：** 先完成一次 RDP；关闭目标 RDP；再次点击远程桌面并运行 RDP/一键诊断；确认路径仍可区分、提示正确；恢复 RDP 后复测。
- **预期路径：** 网络路径可保持 direct 或 relay，但路径状态/诊断明确为 `rdp-unreachable`；不得错误切换 Relay 来掩盖目标服务关闭。
- **用户可见结果：** 显示“RDP 不可达”，包含目标设备/IP/端口、影响原因、开启 Windows 远程桌面或检查防火墙的建议。
- **自动化/手工级别与证据：** A/S 使用回环端口验证诊断结构和 UI：[`TestCheckRDPTargetIncludesPlainLanguageContext`](../../internal/diagnose/diagnose_test.go#L11)、[`TestRDPDiagnosticsAPIIncludesUserContext`](../../internal/ui/server_test.go#L775)、[`TestDesktopDeviceListShowsConnectionPathQualityStatus`](../../cmd/mesh-desktop/main_windows_test.go#L308)。`internal/rdp` 当前无测试文件，包可编译不等于系统 RDP 会话；M 必须真实 Windows 执行。
- **失败采集项：** 关闭/恢复时间、网络路径、RDP 检查结果、用户提示、Windows RDP 服务/防火墙状态、事件类别；不采集会话内容或用户输入。
- **清理/回滚：** 恢复 RDP 服务和防火墙原设置；完成一次成功 RDP；删除临时诊断包。
- **执行频率：** 每个涉及 RDP/诊断的发布候选；RDP 启动、诊断、设备状态/UI 变化后加跑。
- **责任边界：** Windows QA 负责真实系统设置与恢复；诊断负责人核对分类；网络负责人确认链路未失败；不修改 P2P/Relay 使场景通过。

## E2E-NET-013 官方账号封禁

- **适用阶段/发布门禁：** 阶段三官方 Hub 风控手工 E2E。
- **环境与前置条件：** 专用官方测试账号、至少两台在线 Windows、活动 Relay/RDP 会话、具备授权的测试后台操作员。
- **最短步骤：** 建立 Relay RDP；后台封禁账号并开始计时；观察会话关闭、设备 heartbeat/新建连接/加入行为；确认 1 分钟内断开；解除封禁后按流程恢复。
- **预期路径：** 活动/等待 Relay 会话被撤销；新 direct/relay 协商均被控制面拒绝；不得因 direct 路径绕过账号状态。
- **用户可见结果：** 1 分钟内连接断开并显示账号受限的可理解提示；不显示敏感风控细节；解除后需重新正常鉴权。
- **自动化/手工级别与证据：** A/S 证明账号冻结/封禁关闭会话并阻断控制面：[`TestServiceAccountEnforcementClosesPendingAndActiveRelaySessions`](../../internal/cloudhub/cloudhub_test.go#L612)、[`TestServiceAccountFreezeBanBlocksControlPlaneJoinHeartbeatAndRelay`](../../internal/cloudhub/cloudhub_test.go#L749)、[`TestServerRevokesActiveTCPRelayWhenAccountIsFrozen`](../../internal/relay/server_test.go#L205)、[`Task 5B 证据`](task5b-official-hub-risk-enforcement-2026-07-09.md)。1 分钟真实客户端断开仍需 M。
- **失败采集项：** 封禁/断开时间、原/最终路径、会话状态、heartbeat/协商错误类别、风险与审计事件类型、客户端提示；不记录内部风控评分。
- **清理/回滚：** 解除测试封禁；关闭遗留会话；重新认证测试设备；确认非测试账号不受影响。
- **执行频率：** 每个阶段三发布候选；账号状态、heartbeat、Relay runtime 或策略缓存变化后加跑。
- **责任边界：** 风控/后台人员执行封禁；QA 计时；Hub/Relay 负责人核对强制关闭；不得在生产账号演练。

## E2E-NET-014 单设备吊销隔离

- **适用阶段/发布门禁：** 阶段三、五设备安全手工 E2E；PTR-46。
- **环境与前置条件：** 一个专用账号下至少四台测试设备；两组独立活动连接；后台操作员有设备吊销权限。
- **最短步骤：** 建立 A-B 和 C-D 两组连接；吊销 A；观察 A-B 关闭、A heartbeat/新连接拒绝；确认 C-D 不受影响；尝试用 A 重新加入并确认需新授权。
- **预期路径：** 所有包含 A 的 direct/relay 路径关闭或拒绝；无关 C-D 保持原路径；吊销不能扩大到整个账号。
- **用户可见结果：** A 显示已吊销/无法连接，相关对端收到明确提示；其他设备继续可用；后台记录设备和原因类别。
- **自动化/手工级别与证据：** A/S 证明只关闭相关 Relay、拒绝 heartbeat 和 P2P：[`TestServiceDeviceRevokeClosesOnlyRelatedRelaySessions`](../../internal/cloudhub/cloudhub_test.go#L680)、[`TestOfficialHubDeviceRevokeRejectsHeartbeat`](../../internal/onboarding/onboarding_test.go#L772)、[`TestServiceP2PControlPlaneNegativeAuthorization`](../../internal/cloudhub/p2p_control_test.go#L125)。真实客户端缓存、直连和 RDP 断开仍需 M。
- **失败采集项：** 吊销时间、涉及设备的脱敏 ID、两组路径状态序列、heartbeat/协商结果、审计事件和客户端提示。
- **清理/回滚：** 删除被吊销测试设备并重新注册新的测试身份；关闭会话；确认无关设备状态和权限未变化。
- **执行频率：** 每个阶段三/五发布候选；设备凭据、吊销、缓存、P2P 或 Relay 授权变化后加跑。
- **责任边界：** 设备管理员执行吊销；QA 验证隔离范围；Hub/P2P/Relay 负责人分别核对控制面和数据面；不得复用已吊销身份制造恢复。

## E2E-NET-015 无权限连接拒绝

- **适用阶段/发布门禁：** 阶段四合规/风控、阶段五团队 RBAC 手工 E2E；PTR-46。
- **环境与前置条件：** 专用组织，普通成员、授权设备组、未授权设备组各至少一项；source/target 在线并可产生候选；已有允许路径作为对照。
- **最短步骤：** 管理员只授予 source 到允许组；成员连接允许目标并记录成功；连接未授权目标；检查在候选下发和 Relay 创建前即拒绝；再授予权限并复测。
- **预期路径：** 未授权请求没有 direct 候选、没有 Relay 授权、最终失败；授权请求按网络条件 direct 或 relay。
- **用户可见结果：** 显示“无权连接此设备/请联系管理员”，不得伪装为网络错误或泄露目标候选、一次性接入材料。
- **自动化/手工级别与证据：** A/S 证明连接门禁位于候选、direct 和 Relay 之前：[`TestTask10BConnectionGateBeforeCandidatesDirectAndRelay`](../../internal/cloudhub/rbac_device_group_test.go#L450)、[`TestTask10BAllRolesNeedExplicitConnectionGrant`](../../internal/cloudhub/rbac_device_group_test.go#L552)、[`TestClientP2PPublicDirectAuthorizationBoundariesDoNotNegotiateDirect`](../../internal/cloudhub/p2p_control_test.go#L468)、[`Task 10B 证据`](task10b-rbac-device-group-connection-auth-2026-07-13.md)。真实客户端/UI 仍需 M。
- **失败采集项：** 角色、授权组/目标的脱敏 ID、候选数、Relay 会话数、错误类别、审计事件和用户提示；不保存实际接入材料。
- **清理/回滚：** 撤销临时 grant、成员和设备组；关闭测试会话；确认默认仍为拒绝。
- **执行频率：** 每个阶段五发布候选；RBAC、设备组、候选/Relay 授权或客户端错误映射变化后加跑。
- **责任边界：** 团队管理员配置授权；QA 用普通成员验证；安全负责人复核无数据泄露；不得通过管理员身份代替无权限用户。

## E2E-NET-016 团队授权连接与审计

- **适用阶段/发布门禁：** 阶段五团队和商业化能力手工 E2E；PTR-46。
- **环境与前置条件：** 专用组织 owner/admin/operator/member、至少两个设备组和两台真实 Windows；审计查询入口可用；测试套餐额度足够。
- **最短步骤：** owner 邀请成员并分配角色；admin 建组、分配设备、授权 member 连接；member 完成一次 RDP；admin 查询该连接审计；再执行 E2E-NET-015 的拒绝对照。
- **预期路径：** 已授权连接按网络条件 direct 或 relay；路径不绕过 RBAC；审计包含组织、成员、设备、时间和连接方式的最小字段。
- **用户可见结果：** 管理操作与连接结果清晰；普通成员看不到管理员动作；连接成功显示实际路径，无权限显示授权错误。
- **自动化/手工级别与证据：** A/S 证明组织生命周期、角色矩阵、设备组 grant 与审计查询：[`TestOrganizationLifecycleInvitesMembersAndAuditRedaction`](../../internal/cloudhub/organization_test.go#L16)、[`TestTask10BRBACRoleMatrixAndOwnerInvariants`](../../internal/cloudhub/rbac_device_group_test.go#L92)、[`TestTask10CAuditQueryRBACIsolationFiltersAndDTO`](../../internal/cloudhub/audit_query_test.go#L103)、[`TestOfficialHubTeamManagementAPIAndStaticUI`](../../internal/ui/server_test.go#L266)。真实两 Windows/RDP 仍需 M。
- **失败采集项：** 角色、组、设备的脱敏 ID、授权时间、路径、审计查询条件/结果字段、错误类别和 UI 提示；不采集 RDP 内容。
- **清理/回滚：** 移除临时成员、grant、设备组和设备；导出脱敏审计后按测试保留策略清理；确认 owner 不会被意外移除。
- **执行频率：** 每个阶段五发布候选；组织、RBAC、设备组、审计或连接授权变化后加跑。
- **责任边界：** 团队 QA 执行角色分离；安全负责人复核授权；审计负责人核对最小字段；P2P/Relay 只消费已授权决策。

## E2E-NET-017 十设备批量部署与单机重试

- **适用阶段/发布门禁：** 阶段五批量部署/版本管理手工 E2E；PTR-46。
- **环境与前置条件：** 专用组织、10 台测试 Windows 或等价受管 VM、候选预配置包、一个受控失败设备、部署/rollout 管理入口和回滚包。
- **最短步骤：** 生成绑定组织的部署包；在 10 台设备执行 bootstrap；确认自动绑定与目标版本；令一台受控失败；只重试该设备；取消/回滚一次 rollout 并核对审计。
- **预期路径：** 设备绑定后才允许已授权连接；失败设备不得获得可用连接身份；单机重试不影响其余九台。
- **用户可见结果：** 管理员看到每台成功/失败/重试/版本状态和可行动错误；不得把部署或额度错误显示成网络错误。
- **自动化/手工级别与证据：** A/S 使用十次模拟 bootstrap 和 rollout 状态机：[`TestTask10DTenSimulatedBootstrapRedemptionsQuotaAndAudit`](../../internal/cloudhub/deployment_test.go#L228)、[`TestTask10DRolloutStateMachineIdempotencyCancelAndSingleDeviceRetry`](../../internal/cloudhub/deployment_test.go#L354)、[`TestTask10DOfficialHubDeploymentAndRolloutManagerClosure`](../../internal/onboarding/deployment_test.go#L13)、[`Task 10D 证据`](task10d-bulk-deployment-rollout-2026-07-15.md)。模拟 redemption 不是 10 台真实 Windows 安装，发布前必须 M。
- **失败采集项：** 包版本/校验值、十台脱敏设备 ID、bootstrap/版本状态、失败阶段、单机重试结果、组织绑定和审计字段；不保存预配置敏感内容。
- **清理/回滚：** 撤销测试 bootstrap 和设备；执行 rollout 回滚；卸载测试版本或恢复快照；确认非失败设备版本未被单机重试改变。
- **执行频率：** 每个阶段五发布候选；部署包、bootstrap、rollout、版本管理或 UI 变化后加跑。
- **责任边界：** 发布/IT 管理员维护设备池和包；团队 QA 验证组织绑定；版本负责人执行回滚；本任务不建设 MDM/SCCM/Intune/Ansible 平台。

## E2E-NET-018 隔离网私有 Hub

- **适用阶段/发布门禁：** 阶段五企业私有化、授权、版本管理和支持入口手工 E2E；PTR-46。
- **环境与前置条件：** 无公网出口的隔离测试网、私有 Hub 候选包、测试授权文件、两台 Windows、离线更新包和上一稳定回滚包；组织/部署绑定信息已准备。
- **最短步骤：** 在隔离网安装 Hub；导入并校验授权；两端加入、授权并完成一次 RDP；验证支持入口不上传内容；导入离线更新并升级；模拟授权到期策略和更新失败；回滚上一版本并复测连接。
- **预期路径：** 自建 `mesh-agent` v2 只允许私有网络内 direct；如本场景另测独立企业云实验后端，可使用其管理员中继，但该能力不得接入自建 v2。不得尝试未授权公网出口；授权策略作用于真实加入、连接协商和 heartbeat；回滚后已有合同内设备按策略恢复。
- **用户可见结果：** 授权、到期、离线更新和支持边界清晰；付费/授权限制不伪装为网络错误；既有配置、证书和设备不丢失。
- **自动化/手工级别与证据：** A/S 证明 loopback 私有部署、真实 service path 授权门禁、离线更新校验和 UI 支持边界：[`TestPrivateDeploymentLoopbackImportJoinAndExpiryClosure`](../../internal/cloudhub/private_license_http_test.go#L18)、[`TestPrivateLicenseContinueExistingEnforcedOnRealServicePaths`](../../internal/cloudhub/private_license_test.go#L246)、[`TestPrivateLicenseUIHTTPDOMRBACAndSupportBoundary`](../../internal/ui/private_license_test.go#L20)、[`TestOfficialHubPrivateLicenseManagerClosure`](../../internal/onboarding/private_license_test.go#L16)、[`Task 10E 证据`](task10e-private-deployment-license-offline-update-2026-07-15.md)。loopback/临时目录不是隔离企业网，发布前必须 M。
- **失败采集项：** 包/授权/更新校验结果、组织与部署脱敏 ID、路径、到期策略、升级/回滚阶段、服务状态、用户提示；不保存签名密钥、授权原文或主机信息。
- **清理/回滚：** 回滚上一稳定包；恢复配置/证书/状态备份；撤销测试授权和设备；确认隔离网无新增公网路由或出口。
- **执行频率：** 每个阶段五发布候选；私有包、授权策略、离线更新、支持入口或状态兼容性变化后加跑。
- **责任边界：** 企业发布 QA 维护隔离网；授权/更新负责人提供测试制品；安全负责人确认无外连与最小数据；本任务不实现新私有化能力。

## 发布人员汇总模板

每个发布候选复制以下表格到执行记录；“自动化通过”不自动把“真实环境”改为通过。

| 场景 ID | 候选版本 | A/S 结果 | M 结果 | 最终路径/失败类别 | 用户可见结果 | 证据位置 | 清理/回滚 | 执行人/日期 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| E2E-NET-xxx |  | 通过/失败 | 通过/失败/未执行 |  |  |  | 通过/失败 |  |

## 明确不构成通过的材料

- fake dialer 成功或失败，只证明状态机分支。
- 单机回环 TCP/UDP/QUIC、`httptest` 或临时目录，只证明本机进程级闭环，不满足两 NAT 公网门禁。
- 模拟 SSH、模拟十设备 redemption、loopback 私有 Hub，不证明真实云主机、十台 Windows 或隔离企业网。
- 历史 QA 文档只作为 H 级参考；没有当前候选、环境、实际结果和清理记录时，不得标为本次 M 通过。
- 仅成功加入设备、健康检查通过或 `internal/rdp` 包编译通过，均不证明真实 RDP 会话成功。

## 失败升级与发布阻断

出现以下任一情况立即停止自建 v2 发布：协调服务器出现用户数据或 `TypePacket` 转发；成员/候选被显示成虚假 direct；协调断开关闭健康直连；直连失败后出现 Relay、第三 Peer 或 TCP 数据回退；`direct_unreachable_no_relay` 未给出明确提示；v1 迁移覆盖备份或留下半迁移文件；三机/两 NAT/pcap 门禁被本机自动化代替。官方云实验另按其场景阻断：无权限/被吊销设备仍获得候选或 Relay、账号封禁后仍可连接、额度错误被显示为网络错误。任何清理/回滚不能恢复原状态，或日志/证据包含敏感材料、真实公网主机信息或远程桌面内容，也必须停止发布。
