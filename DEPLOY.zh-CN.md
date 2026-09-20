# Meshlink v2 纯 P2P 部署与验收清单

本清单适用于自建 `mesh-agent` v2。协调服务器只负责 mTLS 控制、成员/候选交换、UDP 地址观察和会话授权；虚拟网卡数据只在普通设备之间的 QUIC 直连上传输。

自建 v2 没有 Relay、TURN、第三方转发或 TCP 数据回退。无法 UDP 直连的网络会明确不可达。

## 1. 角色与端口

- A：协调服务器，`mode=hub`，不创建 TUN、不读取内部 IPv4 包、不做转发或 NAT。
- B、C：普通设备，`mode=spoke`，各自创建 TUN，并各自持有一个长期 P2P UDP socket。
- A 的 `listen` 同时绑定 TCP 控制和 UDP 会合服务。两者协议不同，可以且必须使用同一个数字端口，例如 `8443`。
- B/C 的 `connect` 同时给出 A 的 TCP 控制地址和 UDP 会合地址。
- B/C 的 `p2p.listen` 默认是 `0.0.0.0:0`；系统选定的端口在进程生命周期内保持不变，高级诊断中的 `p2p_listen` 是实际绑定值。

当前实现只接受 IPv4 配置和 IPv4 虚拟网段。

## 2. 准备证书

在受控管理机上创建一个网络 CA，并为 A、B、C 分别签发证书：

```powershell
.\bin\meshctl.exe init-ca -out certs -name my-mesh
.\bin\meshctl.exe issue -out certs -name coordinator-a -ca certs\ca.pem -ca-key certs\ca-key.pem -dns coordinator.example.com
.\bin\meshctl.exe issue -out certs -name peer-b -ca certs\ca.pem -ca-key certs\ca-key.pem
.\bin\meshctl.exe issue -out certs -name peer-c -ca certs\ca.pem -ca-key certs\ca-key.pem
```

如果 A 的控制地址直接使用公网 IPv4，把该地址加入 A 证书的 IP SAN：

```powershell
.\bin\meshctl.exe issue -out certs -name coordinator-a -ca certs\ca.pem -ca-key certs\ca-key.pem -ip 203.0.113.10
```

证书 CN/node ID 必须与设备登记身份一致；不要通过放宽身份校验兼容异常旧证书。

## 3. 配置并启动协调服务器 A

复制 `configs\hub.example.json`。关键字段示例：

```json
{
  "version": 2,
  "node_id": "coordinator-a",
  "mode": "hub",
  "transport": {
    "protocol": "tcp_tls_control_v2",
    "listen": "0.0.0.0:8443"
  },
  "listen": "0.0.0.0:8443",
  "network_cidr": "10.77.0.0/24"
}
```

Hub v2 配置不应包含 `virtual_ip`、`routes`、`mtu`、`device`、`setup` 或 `p2p`。启动前在 A 的主机防火墙、云安全组和必要的边界网关上同时放行：

- TCP `8443` 入站：mTLS 控制和 enrollment；
- UDP `8443` 入站：认证地址观察；
- 相应返回流量。

只放行 TCP 不足以建立公网 P2P。启动：

```powershell
.\bin\mesh-agent.exe -config .\hub.json
```

## 4. 配置并启动普通设备 B/C

复制 `configs\spoke.example.json`，分别设置唯一 node ID、证书和虚拟 IP。关键字段：

```json
{
  "version": 2,
  "mode": "spoke",
  "connect": "coordinator.example.com:8443",
  "server_name": "coordinator.example.com",
  "virtual_ip": "10.77.0.2",
  "mtu": 1280,
  "p2p": {
    "protocol": "quic_udp_v1",
    "listen": "0.0.0.0:0"
  }
}
```

每台 Peer 必须允许 `mesh-agent.exe` 的 UDP 收发。使用固定 `p2p.listen` 时，放行该 UDP 端口入站；使用 `0.0.0.0:0` 时，建议创建按程序授权的 Windows 防火墙规则，让同一个动态绑定 socket 接收打洞与 QUIC 返回流量。不要为探测、打洞和 QUIC 分配不同端口。

以管理员权限启动 B 和 C：

```powershell
.\bin\mesh-agent.exe -config .\spoke.json
```

安装为 Windows 服务：

```powershell
.\bin\mesh-agent.exe -service install -service-name MeshlinkAgent -config C:\path\to\spoke.json
.\bin\mesh-agent.exe -service start -service-name MeshlinkAgent
```

## 5. 桌面状态与诊断

```powershell
.\bin\mesh-desktop.exe
```

桌面分别显示：

- 协调服务器：`已连接`、`重连中`、`已断开`；
- 对端路径：`局域网直连`、`公网直连`、`正在协商`、`等待协调服务器`、`直连失败`、`离线或未知`；
- 高级诊断：实际 `P2P UDP 监听地址`。

成员在线或候选探测成功不代表已经直连。只有 QUIC 和双向会话授权完成后才能显示 `lan_direct`/`public_direct`。协调服务器离线但直连心跳仍新鲜时，对端保持在线并显示“协调服务器离线，当前直连不受影响”。

常用稳定错误码：

- `control_upgrade_required`、`control_unavailable`
- `peer_offline`、`peer_revoked`、`route_conflict`
- `candidate_unavailable`、`udp_probe_failed`、`hole_punch_timeout`
- `quic_handshake_failed`、`peer_identity_mismatch`、`session_authorization_failed`
- `direct_heartbeat_timeout`、`direct_unreachable_no_relay`

`direct_unreachable_no_relay` 必须显示为“直连失败 · 本版本未启用中继”，不能切换到 Hub、其他 Peer 或 TCP 数据通道。

## 6. v1 配置迁移与回滚边界

首次加载 versionless/v1 配置时，程序会：

1. 在内存迁移并完整验证 v2 配置；
2. 在活动配置同目录以独占方式创建一次 `<配置路径>.v1.bak`，内容与迁移前原始字节一致；
3. 原子写入 v2 活动配置。

第二次加载不会覆盖 `.v1.bak`。Spoke 保留 CA、证书、node ID、虚拟 IP、路由、MTU、device/setup，并增加默认 P2P UDP 段；Hub 保留身份、证书、监听地址、邀请和设备 registry，但移除旧 TUN/转发字段。任何备份或替换失败都会保留原活动文件并终止启动，不会运行半迁移配置。

v1 与 v2 不混跑。旧客户端连接 v2 A 会收到 `control_upgrade_required` 并断开；新客户端不会静默回退到 `TypePacket` Hub 转发。回滚前停止服务、保存当前 v2 配置并确认所有 Peer 使用同一协议版本；不要只恢复单个节点的 `.v1.bak` 后继续混跑。

## 7. 三台 Windows 同 LAN 验收

此场景必须使用三台物理或独立 Windows 设备，不能用同机回环替代：

1. A 启动协调服务器，确认同一数字端口的 TCP/UDP 均已监听；B、C 加入同一网络。
2. 在 B/C 高级诊断记录各自 `p2p_listen`；确认 A 的成员快照没有暴露对端物理地址。
3. 从 B 向 C 的虚拟 IP 发送流量并建立真实 RDP；再从 C 向 B 发送流量。只有会话握手完成后才记录 `lan_direct`。
4. 保持 B↔C 双向流量，停止 A 的 TCP 和 UDP 服务，持续至少三个 Peer 心跳周期；数据和 RDP 必须继续，路径保持 direct。
5. A 离线期间仅断开 B↔C 网络；双方必须进入 `waiting_coordinator`，不得复用旧 generation。
6. 恢复 A；B/C 必须取得更大的 generation 并恢复直连。
7. 如有 D↔E 对照设备对，在 B↔C 故障/恢复期间持续发包；对照会话 ID、状态和序列不能改变。

记录版本、三台设备、时间、状态序列、会话 generation、RDP 可交互结果、诊断摘要和清理结果，不记录证书私钥或 RDP 内容。

## 8. 两个独立 NAT 的公网验收

1. A 位于公网可达环境；TCP 和 UDP 使用同一协调端口并均已放行。
2. B、C 分别位于两个普通家庭 NAT 后，无公网入站、无人工端口映射；两端允许 `mesh-agent` UDP。
3. B/C 加入并刷新服务器观察候选，从 B 发起 C 的 RDP/虚拟 IP 流量。
4. 确认真正完成 UDP 打洞和 QUIC 会话后显示 `public_direct`，并验证双向业务数据。
5. 在会话保持流量时停止 A，跨越至少三个心跳周期验证不中断；再按第 7 节步骤验证旧授权不能在断链后自行复用。
6. 在封锁 UDP 或确认无法打洞的对照网络中，必须得到 `direct_unreachable_no_relay`，且不存在 Relay、Hub 数据转发或 TCP 回退。

本机自动化、loopback UDP、fake dialer 和本机 QUIC 集成测试均不能把本场景标记为通过。

## 9. 协调服务器物理网卡抓包门禁

在第 7、8 节产生持续 B↔C RDP/虚拟 IP 流量时，在 A 的物理网卡抓包：

1. 记录 A 的协调端口，例如 Wireshark 显示过滤器 `tcp.port == 8443 || udp.port == 8443`。
2. 确认只有低流量 TLS 控制帧和小型认证 UDP 探测；A 不应出现 `TypePacket`、内部 IPv4 明文或与 B↔C 业务字节同步增长的转发流。
3. 停止 A 后继续 B↔C 流量，确认业务仍在 B/C 之间；A 恢复后只观察重连、候选刷新和会话授权。
4. 保存脱敏 pcap 校验值、接口、时间窗口和包/字节摘要；不得保存真实凭据、私钥或远程桌面内容。

任何三台 LAN、两 NAT 或 A 抓包步骤未执行/失败时，发布状态必须保持：

`实现完成，公网实测待验收`

## 10. 私有更新服务

构建打包后，可在普通 Peer 的虚拟地址或独立管理地址发布更新制品；该服务不是数据面。协调服务器 A 没有 TUN，不能沿用旧的 `10.77.0.1` 监听示例。以下示例使用普通 Peer B：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Package
.\bin\mesh-update-server.exe -listen 10.77.0.2:1263 -dir .\release
```

更新只替换程序和示例配置，不覆盖真实配置、证书、`.v1.bak` 或日志。更新前后都要重跑本清单中与协议版本、状态投影和实际网络有关的门禁。
