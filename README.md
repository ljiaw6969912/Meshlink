# Meshlink

Meshlink 是一个自托管 TCP/TLS 异地组网工具，用于在用户自己控制的设备之间建立安全的远程访问通道。它使用标准 TLS 1.3 和双向证书认证，支持 hub / spoke 组网模式，适合家庭、办公室或自有服务器之间的 IPv4 内网访问、远程桌面连接和链路诊断。

本项目不修改 WireGuard，不提供协议伪装、流量拟态、封锁规避、匿名代理或绕过网络监管功能。

## 产品改造方向

Meshlink 当前产品改造方向是“私有远程桌面组网 + 控制面 Hub + 数据面优先 P2P + 必要中继兜底”。现有自托管 hub / spoke 能力继续作为基础闭环保留；后续阶段按自托管闭环、自建中继、官方 Hub MVP、P2P 直连优先、团队和商业化能力推进。

本轮改造仍坚持不做匿名代理、公网出口、全局代理或公开转发节点。详细需求追踪和发布门禁见 [产品改造需求追踪矩阵](docs/qa/product-transformation-requirements.md) 与 [产品改造发布门禁](docs/qa/release-gates.md)。

## 软件功能

- 支持 hub / spoke 异地组网模式。
- 使用 TLS 1.3 和双向证书认证保护连接。
- 提供 `mesh-agent.exe` 作为组网后台程序。
- 提供 `bin\linux\mesh-agent` 作为自建中继向导上传到 Linux 云服务器的 agent 产物。
- 提供 `mesh-desktop.exe` 作为 Windows 中文桌面控制台，不需要浏览器。
- 提供 `meshctl.exe` 用于创建 CA 和签发节点证书。
- 支持 TUN / Wintun 虚拟网卡。
- 支持 IPv4 虚拟网段、路由配置和虚拟网卡数据转发。
- 支持 Windows / Linux 自动配置地址、路由和转发。
- 支持 hub 侧 IPv4 NAT。
- 支持 Windows 服务安装、启动、停止和卸载。
- 支持读取、保存和管理节点配置。
- 支持运行状态记录、日志查看和基础诊断。
- 支持通过 SSH 在用户自己的 Linux 云服务器上部署自建公网 Hub，并生成接入链接和接入码。
- 支持查看已连接节点、来源地址、虚拟 IP、证书名称和 SHA256 证书指纹。
- 支持 RDP 可达性检查，并可调用系统远程桌面客户端连接目标主机。
- 支持私有更新服务：hub 可在 `10.77.0.1:1263` 发布 `release` 目录，客户端桌面控制台可手动检查、下载并应用新版本。
- 支持连接自愈：应用层心跳超时、spoke 指数退避重连、同名节点连接替换，以及 Windows 服务失败自动重启。
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

- 服务器模式：填写公网地址和监听端口，自动生成证书、配置和接入码
- 自建中继模式：填写 Linux 云服务器 SSH 信息，自动部署公网 Hub，验证公网 `/enroll/health`，并生成接入链接和接入码
- 客户端模式：填写接入链接和接入码，自动生成本机私钥、CSR、证书和配置
- 查看组网机群、来源地址和证书指纹
- 点击节点打开远程桌面
- 通过菜单栏“帮助 -> 关于 / 检查更新”查看版本并执行私有远程升级

涉及 Wintun、路由、NAT、Windows 服务的操作需要管理员权限。建议右键 `mesh-desktop.exe`，选择“以管理员身份运行”。

## 私有更新服务

hub 机器编译并打包后，`release` 目录中会包含更新清单和 zip 包。可以在 hub 上启动更新服务：

```powershell
.\bin\mesh-update-server.exe -listen 10.77.0.1:1263 -dir .\release
```

安装为 Windows 服务：

```powershell
.\bin\mesh-update-server.exe -service install -service-name MeshlinkUpdateServer -listen 10.77.0.1:1263 -dir C:\path\to\meshlink\release
.\bin\mesh-update-server.exe -service start -service-name MeshlinkUpdateServer
```

客户端在桌面控制台菜单栏打开：

```text
帮助 -> 关于 / 检查更新
```

填写更新地址和端口：

```text
更新地址：10.77.0.1
更新端口：1263
```

点击“检查更新”，发现新版本后点击“立即更新”。更新过程会下载 zip、校验 SHA256、关闭控制台、停止并重启 `MeshlinkAgent` 服务，只更新 `bin` 里的程序和示例配置，不覆盖真实配置、证书和日志。

版本迭代流程：

推荐使用封装脚本，避免 `manifest.json` 版本和 zip 包版本不一致：

```bat
publish-update.bat 0.1.2 10.77.0.1:1263 C:\path\to\meshlink\release
```

参数依次是发布版本、更新服务监听地址、release 目录；后两个参数不填时默认使用 `10.77.0.1:1263` 和当前目录下的 `release`。

手动流程：

1. 修改根目录 `VERSION`，例如从 `0.1.1` 改为 `0.1.2`。
2. 在发布机器执行 `powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Package`。
3. 把 `release\manifest.json` 和 `release\meshlink-版本号.zip` 放在 hub 的 `release` 目录。
4. 如果更新服务已安装，重新执行 install 命令会更新监听地址和 release 目录；然后启动或重启 `MeshlinkUpdateServer`。
5. 客户端在“关于 / 检查更新”中检查并安装新版本。客户端会拒绝 `manifest.json` 版本和 zip 包文件名版本不一致的发布。

## 命令行运行

启动 hub：

```powershell
.\bin\mesh-agent.exe -config .\hub.json
```

启动 spoke：

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

桌面程序的“组网机群”页会读取这个状态文件，显示当前本机节点、已连接节点、来源地址、虚拟 IP、证书名称和 SHA256 证书指纹。

## 证书命令

```powershell
.\bin\meshctl.exe init-ca -out certs -name my-mesh
.\bin\meshctl.exe issue -out certs -name home-hub -ca certs\ca.pem -ca-key certs\ca-key.pem -dns home.example.com
.\bin\meshctl.exe issue -out certs -name laptop -ca certs\ca.pem -ca-key certs\ca-key.pem
```

如果 hub 使用公网 IPv4 直连，也可以给 hub 证书加入 IPv4 备用名：

```powershell
.\bin\meshctl.exe issue -out certs -name home-hub -ca certs\ca.pem -ca-key certs\ca-key.pem -ip 112.120.48.180
```

Meshlink 现在是 IPv4-only 工程，`listen`、`connect`、`virtual_ip`、`routes`、`setup.address`、证书 IP SAN 都不接受 IPv6。

## 配置语义

顶层 `routes` 表示“这个节点向 mesh 宣告自己背后的网段”。

`setup.routes` 表示“本机系统要把哪些网段路由进 mesh 虚拟网卡”。

hub 示例：

```json
"routes": [
  { "cidr": "192.168.1.0/24" }
],
"setup": {
  "enabled": true,
  "address": "10.77.0.1/24",
  "forwarding": true,
  "nat": {
    "enabled": true,
    "name": "MeshlinkNAT",
    "internal_prefix": "10.77.0.0/24"
  }
}
```

spoke 示例：

```json
"setup": {
  "enabled": true,
  "address": "10.77.0.2/24",
  "routes": [
    { "cidr": "192.168.1.0/24" }
  ]
}
```

完整真实机器部署流程见 [DEPLOY.zh-CN.md](DEPLOY.zh-CN.md)。
