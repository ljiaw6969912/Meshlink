# Meshlink 部署清单

这份清单用于两台真实机器测试：

- 家里机器：`hub`
- 外地机器：`spoke`
- 虚拟网段：`10.77.0.0/24`
- 家里内网：示例为 `192.168.1.0/24`

Meshlink 当前是 IPv4-only：不使用、不监听、不路由 IPv6。

## 1. 准备证书

在任意一台管理机上执行：

```powershell
.\bin\meshctl.exe init-ca -out certs -name my-mesh
.\bin\meshctl.exe issue -out certs -name home-hub -ca certs\ca.pem -ca-key certs\ca-key.pem -dns home.example.com
.\bin\meshctl.exe issue -out certs -name laptop -ca certs\ca.pem -ca-key certs\ca-key.pem
```

如果 hub 用公网 IPv4 直接连接，给 hub 证书加入 IPv4 备用名：

```powershell
.\bin\meshctl.exe issue -out certs -name home-hub -ca certs\ca.pem -ca-key certs\ca-key.pem -ip 112.120.48.180
```

## 2. 配置 hub

复制 `configs\hub.example.json` 为 `hub.json`，确认：

- `listen` 是 IPv4 TCP 监听地址，例如 `0.0.0.0:8443`
- `routes` 写家里内网，例如 `192.168.1.0/24`
- `setup.forwarding` 为 `true`
- `setup.nat.enabled` 为 `true`

hub 需要管理员权限运行：

```powershell
.\bin\mesh-agent.exe -config .\hub.json
```

## 3. 配置 spoke

复制 `configs\spoke.example.json` 为 `spoke.json`，确认：

- `connect` 指向 hub 的公网 IPv4 或域名，例如 `112.120.48.180:8443`
- `server_name` 和 hub 证书的 DNS 或 IP 备用名匹配
- `setup.routes` 包含家里内网，例如 `192.168.1.0/24`

spoke 也需要管理员权限运行：

```powershell
.\bin\mesh-agent.exe -config .\spoke.json
```

## 4. 安装为 Windows 服务

在管理员 PowerShell 中执行：

```powershell
.\bin\mesh-agent.exe -service install -service-name MeshlinkAgent -config C:\path\to\spoke.json
.\bin\mesh-agent.exe -service start -service-name MeshlinkAgent
```

停止和卸载：

```powershell
.\bin\mesh-agent.exe -service stop -service-name MeshlinkAgent
.\bin\mesh-agent.exe -service uninstall -service-name MeshlinkAgent
```

## 5. 打开桌面控制台

```powershell
.\bin\mesh-desktop.exe
```

建议右键选择“以管理员身份运行”。在控制台里执行：

- 读取配置
- 开始诊断
- 检查 RDP
- 打开远程桌面

## 5.1 私有更新服务

在 hub 机器上执行构建打包：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Package
```

打包成功后会生成：

```text
release\manifest.json
release\meshlink-版本号.zip
```

启动更新服务：

```powershell
.\bin\mesh-update-server.exe -listen 10.77.0.1:1263 -dir .\release
```

或安装为 Windows 服务：

```powershell
.\bin\mesh-update-server.exe -service install -service-name MeshlinkUpdateServer -listen 10.77.0.1:1263 -dir C:\path\to\meshlink\release
.\bin\mesh-update-server.exe -service start -service-name MeshlinkUpdateServer
```

客户端打开桌面控制台，在菜单栏选择“帮助 -> 关于 / 检查更新”，填写默认地址：

```text
更新地址：10.77.0.1
更新端口：1263
```

点击“检查更新”，确认后点击“立即更新”。更新只替换程序文件和示例配置，不覆盖真实配置、证书和日志。

版本迭代建议：

推荐使用封装脚本，避免 `manifest.json` 版本和 zip 包版本不一致：

```bat
publish-update.bat 0.1.2 10.77.0.1:1263 C:\path\to\meshlink\release
```

参数依次是发布版本、更新服务监听地址、release 目录；后两个参数不填时默认使用 `10.77.0.1:1263` 和当前目录下的 `release`。

手动流程：

1. 每次发布先修改根目录 `VERSION`，例如 `0.1.2`。
2. 执行 `powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Package`。
3. 将生成的 `release\manifest.json` 和 `release\meshlink-版本号.zip` 同步到 hub 的 release 目录。
4. 更新服务安装命令可以重复执行；如果服务已经存在，会更新启动参数中的监听地址和 release 目录。
5. 客户端通过关于窗口检查更新，按 manifest 中的版本号决定是否下载。客户端会拒绝 `manifest.json` 版本和 zip 包文件名版本不一致的发布。

## 6. 验证链路

spoke 机器上测试：

```powershell
ping 10.77.0.1
ping 192.168.1.50
Test-NetConnection 192.168.1.50 -Port 3389
mstsc /v:192.168.1.50
```

如果 `10.77.0.1` 通但 `192.168.1.50` 不通，重点检查 hub 的转发和 NAT。

如果 TCP 端口不通，重点检查：

- hub 公网 IPv4 或域名是否正确
- 路由器 IPv4 端口转发和系统防火墙是否放行 TCP 8443
- hub 服务是否启动
- spoke 的 `connect` 和 `server_name` 是否写对
