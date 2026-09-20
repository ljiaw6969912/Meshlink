# Meshlink

Meshlink 是一个自托管的纯 P2P IPv4 组网工具。`hub` 是只承载认证、成员发现、候选交换和会话授权的协调服务器；`spoke` 设备使用同一个 UDP socket 建立双向认证的 QUIC 直连并传输虚拟网卡数据。控制面和数据面均使用 TLS 1.3/网络 CA 身份，适合家庭、办公室或自有服务器之间的内网访问、远程桌面连接和链路诊断。

本项目不修改 WireGuard，不提供协议伪装、流量拟态、封锁规避、匿名代理或绕过网络监管功能。

## 产品改造方向

Meshlink 自建 v2 的方向是“私有远程桌面组网 + 协调面 Hub + 纯 P2P 数据面”。协调服务器永不读取或转发 TUN 数据；设备无法经 UDP/QUIC 直连时明确失败，不提供 Relay、第三方转发或 TCP 数据回退。仓库内与官方云服务相关的 Relay 实验后端暂时保留，但不属于自建桌面和 `mesh-agent` v2 的运行路径，也不能视为可用回退。

本轮改造仍坚持不做匿名代理、公网出口、全局代理或公开转发节点。详细需求追踪和发布门禁见 [产品改造需求追踪矩阵](docs/qa/product-transformation-requirements.md) 与 [产品改造发布门禁](docs/qa/release-gates.md)。

## 软件功能

- 支持协调服务器（`hub`）/普通设备（`spoke`）组网模式。
- 使用 TCP/TLS 1.3 双向认证保护控制连接，使用 UDP/QUIC mTLS 和一次性会话授权保护设备直连。
- 提供 `mesh-agent.exe` 作为组网后台程序。
- 提供 `mesh-desktop.exe` 作为 Windows 中文桌面控制台，不需要浏览器。
- 提供 `meshctl.exe` 用于创建 CA 和签发节点证书。
- 支持 TUN / Wintun 虚拟网卡。
- 支持 IPv4 虚拟网段、最长前缀路由和端到端 QUIC DATAGRAM 数据传输。
- 普通设备支持 Windows / Linux 自动配置地址和路由；协调服务器不创建数据面 TUN、转发或 NAT。
- 支持 Windows 服务安装、启动、停止和卸载。
- 支持读取、保存和管理节点配置。
- 支持运行状态记录、日志查看和基础诊断。
- 支持查看协调服务器状态、真实 P2P 路径、P2P UDP 监听地址、虚拟 IP、证书名称和 SHA256 证书指纹。
- 支持 RDP 可达性检查，并可调用系统远程桌面客户端连接目标主机。
- 支持私有更新服务：可由普通 Peer 的虚拟地址或独立管理地址发布 `release` 目录，客户端桌面控制台可手动检查、下载并应用新版本。
- 支持连接自愈：协调连接独立重连、设备对独立心跳/重协商、同名节点连接替换，以及 Windows 服务失败自动重启。协调服务器离线不关闭仍健康的直连。
- 当前版本为 IPv4-only，不支持 IPv6 配置、监听、路由或虚拟网卡包处理。

## 版权与使用限制

Copyright (c) 2026 Meshlink Project Owner. All Rights Reserved.

本项目及其源代码、文档、界面、配置文件、构建脚本、二进制文件及相关资源，均受著作权法、计算机软件保护相关法律法规及其他适用法律保护。

除非版权所有者另行作出明确的书面授权，任何个人、组织或实体不得对本项目进行以下行为：

- 复制、修改、改编、翻译或二次开发；
- 分发、发布、上传、转载、镜像或再授权；
- 出售、出租、转让、商业部署或用于任何商业用途；
- 移除、隐藏或修改本项目中的版权声明、法律声明或权利标识；
- 基于本项目创建、发布或销售衍生软件、服务或产品；
- 将本项目用于数据集构建、模型训练、代码生成系统训练或类似用途；
- 将本项目用于侵犯他人权益、破坏网络安全、规避监管或违反法律法规的用途。

公开展示本仓库并不代表本项目以开源许可证发布，也不代表任何人获得使用、复制、修改、分发、商用或二次开发本项目的许可。

如需使用、合作、授权、商业部署或二次开发，请先取得版权所有者的明确书面许可。

## GitHub 特别说明

如果本项目以公开仓库形式托管在 GitHub 上，访问者可以按照 GitHub 平台功能查看仓库内容。GitHub 平台可能允许用户在 GitHub 服务范围内 fork 公开仓库，但这不代表版权所有者授予任何额外的软件使用权、修改权、分发权、商业使用权或二次开发权。

任何 fork、下载、复制或引用本项目的行为，均不得超出本声明允许的范围。

## 第三方组件声明

本项目可能引用或依赖第三方开源组件、系统组件或运行库。第三方组件的版权和许可条款归其各自权利人所有，并按照其原始许可证执行。

本项目的版权声明和使用限制仅适用于版权所有者拥有权利的代码、文档、配置、界面和相关资源，不改变第三方组件原有许可证授予的权利或限制。

使用者应自行确认其使用、分发或部署本项目时是否满足相关第三方组件许可证要求。

### Wintun 声明

Windows 版本可能随包附带 `wintun.dll`，用于创建和访问 Windows TUN 虚拟网卡。`wintun.dll` 是 WireGuard LLC 的第三方组件，不属于本项目原创代码，也不受本项目专有许可证的授权条款覆盖。

当前仓库中的 `bin\wintun.dll` 信息如下：

- 文件版本：`0.14.1`
- 产品名称：`Wintun Driver`
- 签名主体：`WireGuard LLC`
- SHA256：`E5DA8447DC2C320EDC0FC52FA01885C103DE8C118481F683643CACC3220DAFCE`

Wintun 源代码采用 GPLv2 发布；官方预编译并签名的 `wintun.dll` 使用其官方发布包中附带的预编译二进制许可。根据 Wintun 官方说明，预编译签名 DLL 使用比 GPLv2 更宽松的许可，适用于更多软件分发场景。

本项目仅按原样随包分发官方预编译的 `wintun.dll`，不对其进行修改、反编译、逆向工程、重新签名或声称拥有其版权。任何包含、复制、分发或使用 `wintun.dll` 的行为，均应同时遵守 Wintun 官方许可条款。

本项目与 WireGuard LLC、WireGuard 项目及 Wintun 项目不存在从属、合作、赞助、认证或官方背书关系。WireGuard、Wintun 及相关名称、标识和商标归其各自权利人所有。

更多信息见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

## 合规使用声明

本项目仅用于合法、自有或已获授权的设备之间进行远程访问、网络连接和运维管理。

使用者应确保其部署和使用行为符合所在国家或地区关于网络安全、数据安全、隐私保护、远程访问、加密技术、出口管制及其他相关法律法规的要求。

严禁将本项目用于未经授权的网络访问、入侵、攻击、扫描、数据窃取、绕过访问控制、规避监管或其他违法违规用途。因使用者违法违规使用本项目而产生的任何责任，均由使用者自行承担。

## 免责声明

本项目按“现状”提供，不作任何明示或暗示的保证，包括但不限于适销性、特定用途适用性、稳定性、安全性、兼容性、持续可用性或无错误保证。

使用者应自行承担安装、配置、运行、部署和使用本项目所产生的全部风险。因使用、误用、无法使用本项目，或因配置错误、网络环境、系统权限、防火墙规则、证书管理、第三方组件、操作系统限制等原因导致的任何直接或间接损失，版权所有者不承担责任，法律另有强制规定的除外。

版权所有者保留随时修改、暂停或终止本项目发布、维护和授权的权利。

## 构建

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1
```

同时构建并打包：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Package
```

Windows 上也可以直接双击根目录的 `build-all.bat`。它会请求管理员权限，停止并强制结束从当前仓库 `bin` 启动的 Meshlink 服务和进程，保留 `bin\wintun.dll`，清空其余旧 `bin` 产物以及 `dist`、`release`，然后执行完整测试、Windows/Linux 编译、标准包和私有包校验。安装在其他目录的发行版以及 `configs`、`certs`、`invites`、日志不会被修改。

打包文件位于：

```text
dist\meshlink.zip
```

发行目录同时会生成：

```text
release\manifest.json
release\meshlink-版本号.zip
```

## 桌面程序

直接运行：

```powershell
.\bin\mesh-desktop.exe
```

桌面程序可以完成：

- 协调服务器模式：填写公网地址和监听端口，自动生成证书、v2 配置和接入码
- 客户端模式：填写接入链接和接入码，自动生成本机私钥、CSR、证书和配置
- 分别查看协调服务器状态和真实 P2P 路径，并在高级诊断中查看本机 P2P UDP 监听地址
- 点击节点打开远程桌面
- 通过菜单栏“帮助 -> 关于 / 检查更新”查看版本并执行私有远程升级

涉及 Wintun、路由、防火墙和 Windows 服务的操作需要管理员权限。建议右键 `mesh-desktop.exe`，选择“以管理员身份运行”。

## 私有更新服务

发布机器编译并打包后，`release` 目录中会包含更新清单和 zip 包。v2 协调服务器没有 TUN/`10.77.0.1`，因此更新服务应运行在普通 Peer 的虚拟地址或所有客户端可达的独立管理地址上。以下示例使用普通 Peer `10.77.0.2`：

```powershell
.\bin\mesh-update-server.exe -listen 10.77.0.2:1263 -dir .\release
```

安装为 Windows 服务：

```powershell
.\bin\mesh-update-server.exe -service install -service-name MeshlinkUpdateServer -listen 10.77.0.2:1263 -dir C:\path\to\meshlink\release
.\bin\mesh-update-server.exe -service start -service-name MeshlinkUpdateServer
```

客户端在桌面控制台菜单栏打开：

```text
帮助 -> 关于 / 检查更新
```

填写更新地址和端口：

```text
更新地址：10.77.0.2
更新端口：1263
```

点击“检查更新”，发现新版本后点击“立即更新”。更新过程会下载 zip、校验 SHA256、关闭控制台、停止并重启 `MeshlinkAgent` 服务，只更新 `bin` 里的程序和示例配置，不覆盖真实配置、证书和日志。

版本迭代流程：

推荐使用封装脚本，避免 `manifest.json` 版本和 zip 包版本不一致：

```bat
publish-update.bat 0.1.2 10.77.0.2:1263 C:\path\to\meshlink\release
```

参数依次是发布版本、更新服务监听地址、release 目录。`publish-update.bat` 当前保留的 `10.77.0.1:1263` 默认值只兼容旧拓扑；自建 v2 必须显式传入普通 Peer 或独立管理地址，不能把协调服务器当作虚拟网段设备。

手动流程：

1. 修改根目录 `VERSION`，例如从 `0.1.1` 改为 `0.1.2`。
2. 在发布机器执行 `powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Package`。
3. 把 `release\manifest.json` 和 `release\meshlink-版本号.zip` 放在普通 Peer 或独立管理主机的 `release` 目录。
4. 如果更新服务已安装，重新执行 install 命令会更新监听地址和 release 目录；然后启动或重启 `MeshlinkUpdateServer`。
5. 客户端在“关于 / 检查更新”中检查并安装新版本。客户端会拒绝 `manifest.json` 版本和 zip 包文件名版本不一致的发布。

## 命令行运行

启动协调服务器：

```powershell
.\bin\mesh-agent.exe -config .\hub.json
```

启动普通设备：

```powershell
.\bin\mesh-agent.exe -config .\spoke.json
```

安装为 Windows 服务：

```powershell
.\bin\mesh-agent.exe -service install -service-name MeshlinkAgent -config C:\path\to\spoke.json
.\bin\mesh-agent.exe -service start -service-name MeshlinkAgent
```

停止和卸载：

```powershell
.\bin\mesh-agent.exe -service stop -service-name MeshlinkAgent
.\bin\mesh-agent.exe -service uninstall -service-name MeshlinkAgent
```

服务日志写在配置文件旁边：

```text
logs\MeshlinkAgent.log
```

运行状态也写在配置文件旁边：

```text
logs\MeshlinkAgent.status.json
```

桌面程序的设备页会读取这个状态文件，分别显示 `coordinator_state` 和真实会话路径。成员在线、候选存在或探测成功都不会被推断为 `lan_direct`/`public_direct`；高级诊断同时显示实际绑定的 `p2p_listen`。

## 证书命令

```powershell
.\bin\meshctl.exe init-ca -out certs -name my-mesh
.\bin\meshctl.exe issue -out certs -name home-hub -ca certs\ca.pem -ca-key certs\ca-key.pem -dns home.example.com
.\bin\meshctl.exe issue -out certs -name laptop -ca certs\ca.pem -ca-key certs\ca-key.pem
```

如果协调服务器控制地址使用公网 IPv4，也可以给其证书加入 IPv4 备用名：

```powershell
.\bin\meshctl.exe issue -out certs -name home-hub -ca certs\ca.pem -ca-key certs\ca-key.pem -ip 112.120.48.180
```

Meshlink 现在是 IPv4-only 工程，`listen`、`connect`、`virtual_ip`、`routes`、`setup.address`、证书 IP SAN 都不接受 IPv6。

## 配置语义

配置 `version` 固定为 `2`。协调服务器的 `listen` 同时绑定 TCP 控制端口和 UDP 会合端口；协议不同，因此使用同一个数字端口，例如 `8443`。防火墙必须同时允许该端口的 TCP 与 UDP 入站。

协调服务器只需要身份、证书、`network_cidr` 和监听地址，不包含 `virtual_ip`、`routes`、`mtu`、`device`、`setup` 或 `p2p`，也不会打开 TUN：

```json
{
  "version": 2,
  "node_id": "home-hub",
  "mode": "hub",
  "listen": "0.0.0.0:8443",
  "network_cidr": "10.77.0.0/24"
}
```

普通设备的顶层 `routes` 表示“这个设备向 mesh 宣告自己背后的网段”；`setup.routes` 表示“本机系统要把哪些网段路由进 mesh 虚拟网卡”。每个设备只有一个 P2P UDP socket：

```json
{
  "version": 2,
  "mode": "spoke",
  "connect": "home.example.com:8443",
  "p2p": {
    "protocol": "quic_udp_v1",
    "listen": "0.0.0.0:0"
  }
}
```

`0.0.0.0:0` 由系统选择端口，并在该进程生命周期内保持不变；需要固定防火墙或端口映射规则时可显式设置端口。普通设备必须允许 `mesh-agent` 的 UDP 收发。自建 v2 没有 `relay`、`allow_relay` 或 TCP 数据回退配置。

## v1 配置迁移

首次加载 versionless/v1 配置时，`mesh-agent` 会先完整验证迁移结果，在原目录以独占创建方式保存一次 `<配置路径>.v1.bak`，再原子替换活动配置。Spoke 的证书、node ID、虚拟 IP、路由、MTU 和设备配置保留，并补入默认 P2P UDP 段；Hub 保留 CA、证书、监听地址、邀请和设备注册数据，但移除旧数据面字段。第二次加载不会覆盖既有备份。

任何备份或替换失败都会返回错误，原活动文件保持不变，服务不会以半迁移状态启动。v1 与 v2 不混跑：旧客户端连接 v2 协调服务器会收到 `control_upgrade_required`，不会回退到旧 `TypePacket` 转发。回滚前先停止服务并保存当前 v2 配置；不要把 `.v1.bak` 与 v2 客户端混用。

## 直连限制与发布状态

自建 v2 不提供 Relay、TURN、第三方转发或 TCP 数据通道。对称 NAT、严格 CGNAT、完全阻断 UDP 或严格企业防火墙可能导致设备不可达；客户端应显示 `direct_unreachable_no_relay`（“直连失败 · 本版本未启用中继”），不能伪装成在线或 direct。其他稳定诊断代码包括 `control_unavailable`、`candidate_unavailable`、`udp_probe_failed`、`hole_punch_timeout`、`quic_handshake_failed`、`peer_identity_mismatch`、`session_authorization_failed` 和 `direct_heartbeat_timeout`。

本机回环和自动化 QUIC 测试不能替代真实公网/NAT 验收。在三台 Windows 同 LAN、两个独立家庭 NAT 和协调服务器物理网卡抓包完成之前，状态只能写为：`实现完成，公网实测待验收`。

完整真实机器部署流程见 [DEPLOY.zh-CN.md](DEPLOY.zh-CN.md)。
