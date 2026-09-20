# Meshlink 纯 P2P 数据面设计

- 日期：2026-09-14
- 状态：设计已确认，等待书面规格审阅
- 版本定位：自建网络数据面重构

## 1. 决策摘要

Meshlink 将现有 Hub 转发架构改为“协调面与数据面彻底分离”的纯 P2P 架构：

- A 是协调服务器，只负责设备认证、成员发现、候选地址交换、连接授权和重新协商。
- B、C、D 等设备按需建立一对一加密直连，虚拟网卡数据只在这两个设备之间传输。
- 每一对直连拥有独立生命周期和独立心跳，不依赖 A 的持续在线。
- A 离线时，已经建立的 B↔C 连接继续通信。
- B↔C 自身断开后，必须取得 A 发放的新一代会话授权才能重新建立；A 离线时保持“等待协调服务器”，A 恢复后自动重新协商。
- 不实现 Relay，不允许 A 代转用户数据，也不提供任何隐藏的数据转发回退。
- 不预先建立全网两两连接。只有真实流量或明确用户操作需要某个目标时，才创建该设备对的会话。

本设计把“服务器挂掉是否影响已有连接”和“服务器是否承担所有流量”变成可自动验证的协议不变量，而不是部署建议。

## 2. 背景与现状

当前 `mesh-agent` 的 `spoke` 只维持一条到 `hub` 的 TCP/TLS 连接。所有 TUN 数据以 `TypePacket` 帧发送到 Hub，Hub 再根据目标虚拟 IP 转发给另一个 Spoke。因此：

- B↔C 的全部流量消耗 A 的入站和出站带宽。
- A 的 CPU、内存、套接字和带宽压力随总业务流量增长。
- A 断开会同时切断全部设备间通信。
- 当前 `internal/p2p` 主要是候选、状态和探测模型，没有被 `mesh-agent` 接成持久数据通道；TCP direct probe 建连后立即关闭，不能承载真实数据。
- 当前状态层会在缺少真实路径观测时默认显示 `lan_direct`，存在把“设备在线”误报成“已经直连”的风险。

该实现与本次确认的产品语义不一致，必须替换真实运行路径，不能只修改界面文案。

## 3. 目标与非目标

### 3.1 目标

1. A 的带宽只承载低流量控制消息和 UDP 地址探测，不承载虚拟网卡数据。
2. 同一局域网、有公网可达地址，以及常见可打洞 NAT 下，设备使用同一套 UDP/QUIC 直连实现。
3. 已建立的任意设备对在 A 宕机或重启期间继续通信。
4. 每个设备对独立检测故障、独立重连，其他设备对不受牵连。
5. 连接方式、在线状态和错误原因全部来自真实会话事件，不再依靠默认值猜测。
6. 保留现有 CA、设备证书、邀请码、设备禁用和移除能力，升级不要求用户重新创建整个网络。
7. 失败必须可解释：没有 Relay 时，无法直连就是不可达，不伪装成在线或已连接。

### 3.2 非目标

- 不实现官方 Relay、自建 Relay、TURN 或 Hub 数据转发。
- 不承诺在对称 NAT、严格 CGNAT、完全封锁 UDP或企业防火墙环境中必然直连。
- 不实现全局代理、公网出口或 IPv6 虚拟网段；继续遵守当前 RFC1918 IPv4 路由边界。
- 不预连接所有设备组合，不维持全网 N² 空闲连接。
- 不在本次重构中删除与官方云服务实验代码相关的全部 Relay 数据模型；但这些代码不得进入自建桌面端和 `mesh-agent` 的新运行路径。
- 不增加 TCP 数据通道作为静默回退。TCP 直连不能可靠解决多数 NAT 问题，而且会形成第二套数据面；将来若增加，也必须作为明确的“设备直连”路径单独设计和显示。

## 4. 方案比较与选择

### 4.1 继续由 Hub 转发

改动最小，但无法解决服务器带宽、单点故障和已有会话独立存活的问题，直接否决。

### 4.2 使用完整 WireGuard 引擎动态配置 Peer

WireGuard 的加密和保活非常成熟，项目也已依赖其 TUN 包。但当前依赖只用于创建 TUN；完整引擎会自行占有 UDP Bind。要让地址探测、打洞和动态 Peer 共用同一 NAT 映射，需要新增自定义 Bind、UAPI 动态配置和端点生命周期桥接，改造面反而更大，也会与当前 Agent 读取 TUN 的方式冲突。本次不选择。

### 4.3 使用 ICE/WebRTC 全栈

ICE 能提供完整候选检查，但 WebRTC 同时带入 DTLS、SCTP、数据通道和较大的协议表面；TURN 又与本次“绝不 Relay”的决定相冲突。若只抽取 ICE 再接另一套数据面，套接字所有权和数据交接也更复杂。本次不选择。

### 4.4 选择：单 UDP 端口 + 轻量会合协议 + QUIC

每个 Peer 只持有一个 UDP socket。这个 socket 同时用于：

- 向 A 发送非 QUIC 的带认证探测包，让 A 观察该 socket 的公网映射；
- 向目标候选地址发送同步打洞包；
- 同时监听和拨出 QUIC 连接；
- 在 QUIC DATAGRAM 中传输加密的虚拟网卡数据。

`quic-go` 的 `Transport` 支持在同一个 UDP socket 上同时监听和拨号，并通过 `ReadNonQUICPacket` / `WriteTo` 分流非 QUIC 探测包；这使地址观察、打洞和最终数据通道使用完全相同的 NAT 映射。QUIC 提供 TLS 1.3、连接级加密、拥塞控制、保活和明确的连接生命周期。实现时固定兼容版本并锁定依赖。

参考：

- [quic-go Transport 文档](https://quic-go.net/docs/quic/transport/)
- [quic-go Datagram 文档](https://quic-go.net/docs/quic/datagrams/)
- [quic-go Connection 与 Keep-Alive 文档](https://quic-go.net/docs/quic/connection/)

## 5. 角色与进程边界

### 5.1 协调服务器 A

A 是基础设施节点，不是普通远程桌面设备。它负责：

- 通过现有 CA 和设备证书完成 mTLS 身份认证；
- 校验设备是否属于当前网络、是否被禁用或移除；
- 维护当前在线控制连接和成员快照；
- 为 Peer 下发短期 UDP 探测凭据；
- 记录 Peer 从同一个 UDP socket 发来的 LAN 候选和服务器观察候选；
- 接收按需连接请求、校验路由和权限、去重同一设备对的并发请求；
- 给双方下发同一代会话描述和一次性配对密钥；
- 在线时推送设备撤销和断链要求；
- 接收不含用户载荷的连接结果与诊断摘要。

A 明确不负责：

- 打开或读取用于 Mesh 数据面的 TUN；
- 接收、解析、缓存或转发 B/C 的内部 IP 数据包；
- 在直连失败后创建 Relay；
- 因为控制连接断开而关闭已建立的 Peer 会话。

服务器主机如需作为可远程访问的普通设备，应单独运行 Peer 身份；协调服务器角色本身不冒充普通设备。

### 5.2 普通 Peer B、C、D…

每个 Peer 包含五个彼此解耦的组件：

1. `ControlClient`：只维护到 A 的 TCP/TLS 控制连接和自动重连。
2. `CandidateService`：管理一个长期 UDP socket、LAN 地址、服务器观察地址和打洞消息。
3. `SessionManager`：按无序设备对管理唯一有效直连及其 generation。
4. `PacketRouter`：解析 TUN 目标地址，执行最长前缀匹配并把包送入对应会话。
5. `StatusStore`：分别记录协调服务器状态和每个直连会话状态。

`ControlClient` 的连接上下文不得成为 `SessionManager` 的父上下文。A 的 TCP 连接结束只能改变协调状态，不能取消任何已认证 QUIC 会话。

## 6. 配置与兼容边界

### 6.1 保留的配置语义

为降低升级成本，现有字段继续可读：

- `mode=hub` 表示协调服务器；新界面显示“协调服务器”。
- `mode=spoke` 表示普通 Peer；新界面显示“设备”。
- Hub 的 `listen` 同时决定 TCP 控制端口和 UDP 会合端口，协议不同所以可使用同一数字端口。
- Spoke 的 `connect` 同时给出 A 的 TCP 控制地址和 UDP 会合地址。
- 现有 `ca_file`、`cert_file`、`key_file` 和 `server_name` 继续使用。
- 现有 Spoke 的 `virtual_ip`、`routes`、`mtu` 和 `device` 继续使用。

Hub 配置中遗留的 `virtual_ip`、`routes`、`mtu`、`device` 和 `setup` 在新架构中不再创建数据面。加载时给出一次明确迁移提示，生成的新配置不再写这些字段。

### 6.2 新增的 P2P 配置

Spoke 增加 `p2p` 配置段：

```json
{
  "p2p": {
    "protocol": "quic_udp_v1",
    "listen": "0.0.0.0:0"
  }
}
```

- `listen` 默认使用系统分配端口；一个进程生命周期内保持不变。
- 可以显式固定端口，用于端口映射或防火墙规则。
- 不存在 `relay` 地址、`allow_relay` 或自动回退开关。
- 高级设置只显示直连监听端口和诊断信息，不提供 Relay 配置。

### 6.3 协议版本

控制 Hello 增加必填 `protocol_version=2` 和 `role=peer`。A 只接受 v2 数据面客户端：

- 旧客户端连接时，A 返回可读的 `upgrade_required` 错误后关闭。
- 新客户端连接到旧 Hub 时，必须因缺少 v2 ServerHello 而退出，不能退回 `TypePacket` 转发。
- v2 协调服务器收到 `TypePacket` 一律视为协议违规，记录计数并立即关闭该控制连接。
- 不允许同一个进程在 v2 失败后静默运行 v1 Hub 转发。

### 6.4 v2 控制消息

控制面继续使用有长度上限的 framed JSON，但数据面消息从协议中移除。核心消息如下：

| 消息 | 方向 | 用途 |
|---|---|---|
| `ClientHello` | Peer → A | 声明 v2、node ID、虚拟 IP、路由、MTU 和能力 |
| `ServerHello` | A → Peer | 确认协议、网络范围、成员 revision 和服务器能力 |
| `ProbeCredential` | A → Peer | 下发短期 UDP probe ID 与 HMAC 密钥 |
| `CandidateUpdate` | Peer → A | 上报 LAN 候选和本地 candidate revision |
| `MemberSnapshot` | A → Peer | 下发不含物理地址的完整成员与路由快照 |
| `MemberDelta` | A → Peer | 增量更新成员在线、禁用、移除和路由变化 |
| `ConnectRequest` | Peer → A | 请求与某个目标建立新 generation |
| `ConnectPrepare` | A → Peer | 要求双方刷新候选并确认可以参与本次会话 |
| `ConnectReady` | Peer → A | 返回准备结果与候选 revision |
| `SessionOffer` | A → Peer | 下发 session、generation、对端身份、候选和一次性配对密钥 |
| `SessionOfferAck` | Peer → A | 确认 Offer 已装入内存且监听/拨号组件已就绪 |
| `SessionStart` | A → Peer | 双方均 Ack 后开始同一打洞窗口，不依赖客户端时钟同步 |
| `SessionAbort` | A → Peer | 准备、授权或双 Ack 失败时终止本 generation |
| `SessionResult` | Peer → A | 上报成功/失败和受控诊断摘要，不含用户载荷 |
| `ActiveSessions` | Peer → A | 控制重连后报告仍由直连心跳证明存活的设备对 |
| `DisconnectPeer` | A → Peer | A 在线时传播撤销或管理员断链要求 |
| `Ping` / `Pong` | 双向 | 只验证控制连接本身，不代表任意 Peer 直连成功 |

所有带 request/session/revision 的消息必须幂等；未知必填字段版本、超大负载、非法状态跳转和数据帧都必须拒绝。

## 7. 候选地址与 UDP 会合

### 7.1 同一个 UDP socket

Peer 启动时先绑定 P2P UDP socket，再建立控制连接。所有候选、打洞和 QUIC 包必须从该 socket 发出。禁止用另一个临时 socket 探测公网端口，因为 NAT 映射可能不同。

### 7.2 LAN 候选

Peer 枚举已启用、非回环的 IPv4 接口，把地址与 P2P UDP 端口组合为 LAN 候选。候选只通过 mTLS 控制连接上报 A，不广播给全网。

### 7.3 服务器观察候选

控制连接建立后，A 下发短期 `probe_id` 和随机密钥。Peer 通过 QUIC Transport 的非 QUIC 发送接口向 A 的 UDP 端口发送：

- 协议版本；
- `probe_id`；
- 时间戳和随机 nonce；
- 对上述字段的 HMAC-SHA256。

A 根据控制会话中的临时密钥校验探测包，并把 UDP 源 IP/端口作为服务器观察候选。响应同样带 HMAC，Peer 验证后才认为探测成功。凭据短期有效、nonce 防重放；UDP 包不携带设备证书、邀请码或用户数据。

Peer 在以下时机刷新候选：

- 控制连接刚建立；
- 本地接口发生变化；
- UDP socket 重新绑定；
- 候选即将过期。

A 只保留短 TTL 候选。过期候选不得进入新会话描述。

### 7.4 隐私边界

A 只有在一方确实请求连接且授权通过后，才把双方候选互相披露。普通成员快照不包含其他设备的物理 IP、端口或探测凭据。

## 8. 按需建链流程

以下流程以 B 首次向 C 的虚拟 IP 发送数据为例：

1. `PacketRouter` 根据最新成员路由表识别目标 C。
2. 若已有 `ready` 的 B↔C 会话，立即发送，不经过 A。
3. 若没有会话，B 将少量初始包放入有界短期队列，并向 A 发送 `ConnectRequest(C)`。
4. A 校验 B/C 均为有效成员、C 控制在线、路由没有冲突，随后给双方发送 `ConnectPrepare`。
5. B/C 刷新候选并分别返回 `ConnectReady`。拒绝、离线或超时会成为明确失败原因。
6. A 为无序设备对 `{B,C}` 创建新的 `session_id`、单调递增 `generation` 和 256 位随机一次性配对密钥。
7. A 向双方发送相同的 `SessionOffer`：对端身份与证书指纹、双方候选、固定拨号角色、有效期和配对密钥。
8. 双方把 Offer 装入内存并返回 `SessionOfferAck`；A 只有收到两个 Ack 才发送 `SessionStart`。任一方失败或超时则向另一方发送 `SessionAbort`。
9. 双方收到 `SessionStart` 后立即进入同一持续数秒的打洞窗口，同时向有限候选集合发送若干个带认证的打洞包，不依赖客户端时钟精确同步。
10. 固定角色的一方使用同一 UDP socket 发起 QUIC，另一方已经在同一 socket 上监听。角色按 node ID 稳定排序，由 Offer 明确给出，避免双连接竞态。
11. QUIC mTLS 完成后，双方在首个双向控制流交换 `SessionHello`，校验 session、generation、双方身份、随机数以及配对密钥 HMAC。
12. 双方都确认后会话进入 `ready`，B 冲刷尚未过期的初始包；此后所有流量直接 B↔C。
13. 双方向 A 上报不含载荷的 `SessionResult`。该上报失败不影响已经进入 `ready` 的连接。

A 对同一无序设备对的并发请求去重。同一 generation 最多保留一个有效连接；重复或旧 generation 的连接必须关闭。

## 9. 直连数据协议

### 9.1 QUIC 与 TLS

- ALPN 固定为 `meshlink-p2p/1`。
- 最低 TLS 版本为 1.3。
- 双方都提交设备证书，均使用当前网络 CA 验证。
- 除证书链外，还必须校验 Offer 中的预期 node ID、证书指纹和一次性配对密钥。
- 禁用 0-RTT，避免重放尚未建立会话授权的数据。
- 配对密钥只在内存中保存；会话成功或失败后即消费，不写日志、不持久化。
- Offer 到期后不能创建新连接，但已经认证并进入 `ready` 的 QUIC 连接不因 Offer 到期或 A 离线而关闭。

### 9.2 数据载荷

虚拟网卡 IPv4 包使用 QUIC DATAGRAM 承载：

- Datagram 端到端加密并受 QUIC 拥塞控制；丢失时不在 QUIC 层重传，保持 IP 数据报语义。
- 为兼容较小路径 MTU，内部包被切分成不超过 1000 字节的片段。
- 每个片段包含协议版本、64 位 packet ID、片段序号、片段总数、原始长度和完整性边界。
- 接收端仅在全部片段到齐后写入 TUN；缺片超时则丢弃，不能交付半包。
- 单包不得超过配置 MTU 和全局硬上限；异常长度、重复冲突片段和过量片段立即丢弃。
- 重组按 Peer 设置数量、字节和时间上限，防止内存耗尽。

### 9.3 路由与反欺骗

- 目标匹配使用最长前缀规则；设备虚拟 IP 等价于 `/32`。
- A 在成员注册时拒绝重复虚拟 IP 和冲突路由。
- 发送端只把目标属于 C 的包交给 B↔C 会话。
- 接收端校验内部源地址必须属于已认证对端的虚拟 IP 或声明路由，目标必须属于本机虚拟 IP或本机声明路由；不满足即丢弃并计数。
- 不允许 Peer 借直连伪造其他成员来源或把 Meshlink 变成公网出口。

## 10. 会话心跳与故障语义

### 10.1 独立心跳

每个 `ready` 会话使用 QUIC Keep-Alive 维持 NAT 映射，并在会话控制流上运行轻量 Ping/Pong：

- 心跳只在 B 和 C 之间发送；A 不参与。
- 心跳生成 RTT、最后收包时间和连续超时数。
- 达到超时门限后，只关闭当前设备对。
- B↔C 的故障不能取消 B↔D 或 D↔E。
- 健康会话不因空闲或 A 离线而自动回收；只有设备退出、用户主动断开、证书撤销、Peer 进程结束或该直连自身失效才关闭。这样已经牵好的设备对会持续保有自己的心跳和通道。

### 10.2 A 离线

A 的控制连接断开后：

- `coordinator_state` 进入 `reconnecting`。
- 所有 `ready` 直连保持原样，继续传输和心跳。
- 活跃对端仍由直连心跳证明在线，界面显示“协调服务器离线，直连正常”。
- 没有活跃直连的成员只保留身份和路由缓存，状态显示“未知/等待协调服务器”，不能默认显示在线或直连。
- 控制客户端在后台独立重连；重连成功不会替换或重启已有直连。
- Peer 向恢复后的 A 上报当前活跃 session ID/generation，供成员状态和撤销检查使用；这不是已有会话继续工作的前提。

### 10.3 直连自身断开

若 B↔C QUIC 连接自身失效：

- 会话立即离开 `ready`，不再向 TUN 宣称 C 可用。
- 旧 Offer 和配对密钥失效，双方不得自行用缓存授权重新拨号。
- A 在线时，SessionManager 请求新的 generation 并重新执行完整协商。
- A 离线时，状态进入 `waiting_coordinator`，不通过其他 Peer 或旧 Hub 通道转发。
- A 恢复后自动申请新 generation；成功后回到 direct。

该规则精确对应已确认语义：A 挂掉不破坏已有桥；桥本身也断掉后，必须等 A 恢复才能重新牵线。

## 11. 状态机

每个无序设备对独立运行以下状态：

| 状态 | 含义 | 是否可传数据 |
|---|---|---|
| `idle` | 已知成员，但当前无流量、无连接 | 否 |
| `requesting` | 已向 A 请求会话 | 否，短期排队 |
| `preparing` | A 等待双方就绪和候选刷新 | 否，短期排队 |
| `punching` | 双方同步发送 UDP 打洞包 | 否，短期排队 |
| `authenticating` | QUIC 与 SessionHello 校验中 | 否，短期排队 |
| `lan_direct` | 已通过 LAN 候选建立真实直连 | 是 |
| `public_direct` | 已通过服务器观察候选建立真实直连 | 是 |
| `reconnecting` | 直连已断，A 在线，申请新 generation | 否 |
| `waiting_coordinator` | 直连已断且 A 离线 | 否 |
| `failed` | 当前尝试明确失败 | 否 |
| `closed` | 用户操作、撤销或进程退出关闭 | 否 |

状态机没有 `relay` 或 `fallback_relay` 分支。`lan_direct` 和 `public_direct` 只能由完成双重身份校验的真实 QUIC 会话设置，不能由 Roster、候选存在或默认值设置。

## 12. 队列、并发与资源上限

- 每个目标在协商期间最多缓存 64 个包或 256 KiB，最长 3 秒；超过时丢弃最旧包以优先保留交互流量。
- 全局协商缓存设独立上限，防止大量不可达目标耗尽内存。
- 同一设备对只能有一个协商和一个有效会话。
- 对同一目标的 ConnectRequest 使用 single-flight，并设置指数退避，不能让每个 TUN 包触发控制请求。
- 每个有效会话有独立的有界发送队列；TUN 读取循环不能被一个慢 Peer 永久阻塞。
- UDP 打洞、QUIC 握手、SessionHello、片段重组和控制写入都有明确超时。
- 所有丢包原因进入受控计数器，日志不记录内部数据内容。

## 13. 安全模型

### 13.1 信任链

1. A 和所有 Peer 继续信任网络 CA。
2. TCP 控制通道使用 mTLS；A 将证书身份与 Hello node ID、设备登记指纹绑定。
3. QUIC 直连也使用 mTLS，并额外绑定 A 本次下发的对端指纹和一次性配对密钥。
4. 只有同时满足 CA、目标身份和本次授权的连接才能进入 `ready`。

### 13.2 重放与竞态

- probe nonce、Offer session ID、generation 和 SessionHello nonce 均参与认证。
- Offer 短期有效、单次消费、仅存内存。
- 旧 generation 永远不能替换新 generation。
- 同一 node ID 的新控制连接替换旧连接时，A 先原子更新注册表，再关闭旧连接。
- 新直连只有完整认证后才可替换旧直连；失败不能误伤仍健康的旧连接。

### 13.3 撤销语义

- A 在线时，禁用或移除设备会向有关 Peer 推送关闭命令，相关会话立即关闭。
- A 离线时，已有直连按已确认需求继续工作，因此新撤销无法即时传播。这是“已有连接不依赖 A”带来的明确一致性取舍。
- A 恢复后，Peer 上报活跃会话；A 重新检查成员状态并关闭涉及已撤销设备的会话。

## 14. 界面与诊断语义

界面把两个维度分开显示：

- 协调服务器：`已连接 / 重连中 / 已断开`。
- 对端路径：`局域网直连 / 公网直连 / 正在协商 / 等待协调服务器 / 直连失败 / 离线或未知`。

具体规则：

- A 离线但 B↔C 心跳正常：C 显示在线，路径显示真实 direct，并附“协调服务器离线，当前直连不受影响”。
- A 在线但 B/C 尚未建立会话：只显示成员在线，不显示 direct；首次流量时进入协商。
- 候选探测成功不等于直连成功。
- 直连失败后明确提示“当前网络无法点对点连接；本版本未启用中继”。
- 不再显示 Relay 流量、Relay 回退或 Relay 配置入口。
- 诊断允许显示候选类型、尝试阶段、受控错误码、RTT、最后心跳时间和丢包计数，但默认不暴露完整公网地址。

## 15. 错误处理与可观测性

关键错误使用稳定代码而不是依赖日志文本：

- `control_upgrade_required`
- `control_unavailable`
- `peer_offline`
- `peer_revoked`
- `route_conflict`
- `candidate_unavailable`
- `udp_probe_failed`
- `hole_punch_timeout`
- `quic_handshake_failed`
- `peer_identity_mismatch`
- `session_authorization_failed`
- `direct_heartbeat_timeout`
- `direct_unreachable_no_relay`

指标至少包含：

- 当前控制连接数和重连次数；
- 候选刷新成功/失败；
- direct 建链尝试、成功率和耗时；
- 按 LAN/public 分类的活跃会话数；
- 每会话 RTT、心跳超时、发送/接收字节；
- 协商队列和会话队列丢包；
- A 收到 `TypePacket` 的协议违规次数。

A 不记录用户包字节数，因为用户包不经过 A。Peer 的流量计数仅是本机诊断，不上报包内容。

## 16. 升级与迁移

1. 新版本首次加载 Hub 配置时保留 CA、证书、监听地址、邀请码和设备注册表，仅停止创建 Hub TUN 与转发路由。
2. 新版本首次加载 Spoke 配置时保留身份、虚拟 IP、路由和证书，补入默认 P2P UDP 配置。
3. 配置写入必须原子完成，并在同目录保留一次 v1 备份；迁移失败时继续使用原文件且不启动半迁移服务。
4. v1 和 v2 数据协议不混跑。升级窗口中，旧设备会收到升级要求，不能通过 A 继续转发。
5. 全部 Peer 升级后不需要重新输入邀请码；证书 CN 与 node ID 不一致的历史异常设备必须重新签发证书，不能放宽身份校验。
6. 旧 Relay/Cloud 实验配置不自动转成直连配置，也不被新 Agent 读取。

## 17. 测试策略

### 17.1 单元测试

- v2 控制帧编解码、版本拒绝和 `TypePacket` 禁止规则。
- UDP probe HMAC、过期、nonce 重放和错误来源地址处理。
- 候选去重、TTL 和 LAN/public 分类。
- 设备对 key、generation、single-flight 和重复连接仲裁。
- QUIC 对端证书、指纹、node ID、配对密钥和 SessionHello 拒绝场景。
- IPv4 目的路由、最长前缀和源地址反欺骗。
- Datagram 分片、乱序重组、缺片超时、内存上限和异常输入。
- 控制状态与直连状态完全解耦。

### 17.2 本机集成测试

用真实 UDP socket 和真实 TLS/QUIC 启动 A、B、C、D、E：

1. B 首次发包触发 B↔C，双向数据通过。
2. 在 A 的 TCP/UDP 接口记录全部帧，断言从未出现内部 IP 载荷或 `TypePacket`。
3. B↔C 持续传输时停止 A，至少跨越多个直连心跳周期，数据继续。
4. A 离线时主动断开 B↔C，双方进入 `waiting_coordinator`，不得自行复用旧授权。
5. 重启 A 后，B/C 自动取得新 generation 并恢复直连。
6. B↔C 故障和重协商期间，D↔E 持续传输不受影响。
7. 并发双向首包只产生一个设备对会话。
8. 禁用 C 后，A 在线时 B↔C 被关闭；A 离线期间的撤销在 A 恢复后生效。
9. 无 Relay 配置、进程或端口时，整套测试仍完整通过。

### 17.3 Windows 真实环境验收

- 三台 Windows 设备同一 LAN，运行真实 TUN 和 RDP 流量。
- B/C 位于两个普通家庭 NAT 后，A 位于公网，验证服务器观察候选和 UDP 打洞。
- 在 RDP 会话中停止 A，持续操作并记录直连不中断。
- 断开 B/C 网络再恢复：A 在线时自动重连；A 离线时等待；A 恢复后再自动重连。
- 抓取 A 网卡流量，确认只有控制帧和小型 UDP 探测，不出现 B/C 的 RDP 业务流量。
- 在封锁 UDP 或对称 NAT 环境中验证明确失败提示，不出现 Relay 或虚假 direct。

## 18. 验收标准

| 编号 | 场景 | 通过标准 |
|---|---|---|
| AC-01 | B 向 C 发送虚拟网段流量 | 数据只经 B↔C QUIC；A 不接收 `TypePacket` 或内部 IP 载荷 |
| AC-02 | A 承载 20 个已注册但无业务 Peer | 只有控制心跳和候选刷新，不建立 N² 会话 |
| AC-03 | B↔C 已连接后停止 A | 至少跨越 3 个心跳周期仍可双向通信，路径保持 direct |
| AC-04 | A 离线后 B↔C 自身断开 | 状态为 `waiting_coordinator`，不自行重建、不经第三方转发 |
| AC-05 | AC-04 后恢复 A | 自动取得新 generation 并恢复 B↔C direct |
| AC-06 | B↔C 重连，同时 D↔E 正常 | D↔E 无断链、无状态回退、无全局取消 |
| AC-07 | 同 LAN 建链 | 真实握手完成后显示 `lan_direct`，候选存在但握手未完成时不得显示 |
| AC-08 | 跨 NAT 打洞成功 | 显示 `public_direct`，业务流量不经过 A |
| AC-09 | UDP 被阻断或 NAT 不可打洞 | 明确显示 `direct_unreachable_no_relay`，不创建 Relay |
| AC-10 | 旧客户端连接 v2 A | 收到 `upgrade_required` 并断开，不能继续旧 Hub 转发 |
| AC-11 | v2 A 收到 `TypePacket` | 记录协议违规并关闭控制连接，载荷不被转发 |
| AC-12 | 伪造证书身份、旧 generation 或配对密钥 | QUIC 会话无法进入 `ready`，TUN 不收到数据 |
| AC-13 | 直连收到伪造内部源地址 | 数据被丢弃并计数，不写入 TUN |
| AC-14 | A 在线时撤销 C | 涉及 C 的会话关闭；其他设备对不受影响 |
| AC-15 | 桌面端查看路径和高级设置 | 不显示虚假 direct、Relay 回退或 Relay 配置；显示真实会话和协调状态 |

## 19. 完成定义

只有同时满足以下条件，才能宣称“P2P 已实现”：

- `mesh-agent` 的生产运行路径已不再把 TUN 包写入 A 的 TCP/TLS 控制连接。
- A 的生产代码没有 Peer-to-Peer 用户数据转发入口。
- 真实 QUIC 会话承载了双向 TUN 数据，而不是探测后立即关闭。
- A 宕机存活、A 离线后直连再断、A 恢复重协商以及独立设备对隔离均有自动化证据。
- Windows LAN 与跨 NAT 的真实设备验收完成。
- 所有状态来自真实事件，失败时明确说明无 Relay。
- 全仓测试、竞态检查、Windows 构建和现有加入/退出/证书撤销回归通过。

在真实跨 NAT 验收尚未完成前，只能标记为“实现完成，公网实测待验收”，不能用 loopback、fake dialer 或 TCP probe 代替真实 P2P 结论。

## 20. 已知取舍

- 不做 Relay 意味着部分网络环境必然不可达；这是本次明确接受的产品选择。
- A 离线时保留已有直连，意味着该时段内无法即时传播新的撤销；恢复后收敛。
- QUIC DATAGRAM 当前适合保持 IP 数据报语义，但需要自行做有界分片和重组，并通过真实 RDP/吞吐测试验证性能。
- A 仍是新连接授权的单点；本设计消除的是“已有数据连接依赖 A”和“业务流量压垮 A”，不宣称消除所有控制面单点。
