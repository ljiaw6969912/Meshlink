# 企业私有部署运维手册

本文只描述 Meshlink 私有 Hub 在隔离网络中的最小部署、签名授权校验、离线更新与支持边界，不是授权签发系统或商业合同系统。

## 自建 v2 数据面边界

自建 `mesh-agent` v2 中的 Hub 是协调服务器，不是数据转发节点：它不创建 TUN、不读取内部 IPv4 包、不做 NAT，也不承载 Relay 或 TCP 数据回退。协调服务器的 `listen` 在同一个数字端口上分别绑定 TCP mTLS 控制与 UDP 地址观察；隔离区防火墙必须同时放行这两种协议。每台普通设备还必须允许 `mesh-agent` 使用其 `p2p.listen` 对应的单一 UDP socket 收发打洞、QUIC 和心跳数据。

本手册后文出现的 P2P/Relay 授权字段属于保留的官方云/企业实验后端合同语义，不会为自建桌面或 `mesh-agent` v2 启用中继。自建设备无法直连时以 `direct_unreachable_no_relay` 失败，不得经协调服务器、第三台设备或 TCP 数据通道转发。

## 隔离网络部署

1. 在联网构建区运行 `scripts/build.ps1` 生成现有二进制，再运行 `scripts/package-private.ps1`；用 `scripts/verify-private-package.ps1` 复核 ZIP。
2. 通过组织批准的介质把 ZIP 导入隔离区，复核介质来源与脚本输出的 SHA-256。包不会下载公网资源。
3. 为该环境分配不可变的 `deployment ID` 和对应 `organization ID`。两者必须与签名授权完全一致，迁移或灾备时不能自行改写。
4. 把合同方提供的 Ed25519 公钥写入 `configs/trusted-license-keys.json`。包内示例只有占位符；`private-hub.example.env` 只是参数模板，不会被脚本自动读取。
5. 使用 `scripts/run-private-hub.ps1 -DeploymentID <deployment-id> -OrganizationID <organization-id>` 启动。首次启动允许创建一个使用配置中 organization ID 的 enterprise owner 组织，以解除导入前的 bootstrap 循环；此时设备加入等授权操作仍安全拒绝。随后 owner/admin 在团队管理页导入已签名授权。也可以在启动前把已批准的签名文件放到 `licenses/private-license.json`，Hub 会在启动时重新验证。

隔离网络地址、TLS 终止、防火墙与进程守护由部署方环境提供；默认示例不包含 TLS 私钥、账号密码或公网地址。

启动时 Hub 会重新校验签名、key ID、组织、deployment 和时间。授权损坏或绑定不符时拒绝启动私有授权模式。在线导入先完成全部校验，再以临时文件原子替换；失败不会覆盖当前有效授权。

## 公钥变更

可信配置可同时列出旧、新公钥，每项以稳定 `key_id` 标识。先受控分发含新公钥的配置并重启验证，再导入由新 key ID 签名的授权；确认所有节点完成后才移除旧公钥。仓库和部署包都不得保存签名私钥。

## 授权与到期策略

owner/admin 可在团队管理页查看最小摘要和导入已签名授权；其他角色拒绝。页面不返回原始载荷、签名或公钥材料。导入前应备份当前签名授权文件及可信公钥配置，回滚时只能恢复仍能通过当前 deployment、organization 与时间校验的签名文件。

授权中的额度会解析为 `contract_custom`，缺失额度安全拒绝，不会退回 unlimited。到期后始终拒绝新设备、新成员、新部署凭据和 rollout：

- `continue_existing`：已有设备的 heartbeat、P2P/Relay 新协商可继续，但仍受连接授权与额度约束。
- `grace_period`：宽限期内仅已有设备可继续；宽限结束按授权中签名的终止策略执行。
- `deny_all`：到期后已有设备的 heartbeat 与新 P2P/Relay 协商也拒绝。

时间边界使用 UTC：`not_before` 包含该时刻，`expires_at` 不包含该时刻。系统时钟应由隔离区可信时间源维护。

## 离线更新

离线更新由签名 manifest 与同目录更新 ZIP 组成。验证器检查 key ID、Ed25519 签名、SHA-256、大小、平台、架构、deployment、license ID、到期时间、前序版本和连续序号；授权还必须允许离线更新。任何一项失败都不会替换当前版本或 last-known-good。当前实现只把通过验证的 ZIP 原子放入 staging 边界，实际切换仍复用既有更新流程，不执行包内任意脚本，也不访问公网。

仓库只提供签名数据结构和验证接口；生产私钥管理、授权签发与制品代码签名必须在外部受控系统完成。当前构建没有生产代码签名，因此包清单明确标记 `code_signed=false`，不能把测试结果表述为生产签名验收。

## 备份、灾备与支持

备份范围只需包含签名授权文件、可信公钥配置、deployment/organization 配置和现有业务持久化数据；不要备份或复制签名私钥。恢复后先离线验证授权和更新包，再开放客户端访问。损坏授权应保留现场并恢复上一个仍有效的签名文件，不得编辑载荷或延长时间。

versionless/v1 `mesh-agent` 配置首次加载时会在原目录创建一次 `<配置路径>.v1.bak`，随后才原子写入已验证的 v2 配置；备份创建或替换失败时原活动文件保持不变，服务不得以半迁移状态启动。灾备演练必须同时保存活动 v2 配置与该一次性备份，并验证 CA、设备证书、node ID、虚拟 IP、路由、邀请和 registry 均保留。v1/v2 不能混跑，禁止只回滚单个节点后恢复生产流量。

支持入口只显示授权内的支持标识和联系方式，不自动上传用户内容。需要排查时由管理员主动导出最小诊断：稳定 license ID、organization、deployment、版本、时间、动作结果和非敏感错误类别；不得包含原始授权、签名、密钥、账号口令、长期 token、远程桌面内容或自由文本业务数据。

## 已知限制

- 本任务不实现在线激活、授权签发后台、订单计费、联网遥测或外部支持系统。
- 示例 Hub 使用仓库现有内存 Store；生产持久化、备份一致性和多实例协调必须在后续受控实现中单独验收。
- 本地回环测试只证明完全不访问外网的工程闭环，不代表真实客户隔离网、生产 PKI、TLS 或代码签名验收。
- 对称 NAT、严格 CGNAT、UDP 封锁或严格企业出口策略可能让自建设备不可达；本版本没有 Relay 回退。
- 发布前必须完成三台 Windows 同 LAN、两个独立 NAT、协调服务器物理网卡抓包和故障恢复记录。本机自动化 QUIC/loopback 不能替代公网验收；未完成时状态只能是 `实现完成，公网实测待验收`。
