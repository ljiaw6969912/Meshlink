# Meshlink

Meshlink 是一个自托管 TCP/TLS 异地组网原型，用于你自己控制的设备之间做安全远程访问。它使用标准 TLS 1.3 和双向证书认证，不修改 WireGuard，也不实现协议伪装、流量拟态或封锁规避功能。

## 当前能力

- `mesh-agent.exe`：组网后台，支持 hub / spoke。
- `mesh-desktop.exe`：原生 Windows 中文桌面控制台，不需要浏览器。
- `meshctl.exe`：命令行证书工具。
- IPv4-only：配置、传输、路由和虚拟网卡包处理都会拒绝 IPv6。
- 支持 TUN/Wintun 虚拟网卡。
- 支持 Windows/Linux 自动配置地址、路由、转发。
- 支持 hub 侧 IPv4 NAT。
- 支持 Windows 服务安装、启动、停止、卸载。
- 支持诊断、日志查看、RDP 可达性检查和打开远程桌面。

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

## 桌面程序

直接运行：

```powershell
.\bin\mesh-desktop.exe
```

桌面程序可以完成：

- 创建 CA
- 签发节点证书
- 读取和保存配置
- 安装、启动、停止、卸载 Windows 服务
- 运行诊断
- 查看日志
- 查看组网机群、来源地址和证书指纹
- 检查 RDP
- 打开远程桌面

涉及 Wintun、路由、NAT、Windows 服务的操作需要管理员权限。建议右键 `mesh-desktop.exe`，选择“以管理员身份运行”。

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
