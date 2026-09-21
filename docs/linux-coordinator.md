# Linux 命令行协调服务器

`mesh-coordinator` 是独立的 Linux 命令行程序：同一个进程运行协调服务器和服务器本机的组网节点，不需要桌面或 GUI。初始化完成后会在终端输出接入链接、六位验证码及服务器组网地址，随后启动网络服务；请同时检查后续日志是否报告启动错误。默认组网地址为 `10.77.0.1`；实际地址以程序启动输出为准。

## 安装与首次启动

选择 CPU 对应的包：x86-64 使用 `meshlink-linux-amd64.tar.gz`，ARM64/aarch64 使用 `meshlink-linux-arm64.tar.gz`。包内只有 `meshlink-linux` 顶层目录，不包含任何预设运行身份或验证码。

Linux 主机需要 `/dev/net/tun` 和 `ip` 命令（通常由 `iproute2` 包提供）。推荐以 root 运行；自行配置非 root 运行时必须提供创建 TUN 和配置网络所需的 `CAP_NET_ADMIN` 权限。容器也需要由宿主机提供 TUN 设备与网络管理权限。

以 amd64 包、对外域名 `vpn.example.com` 为例：

```sh
sudo tar -xzf meshlink-linux-amd64.tar.gz -C /opt
cd /opt/meshlink-linux
sudo ./bin/mesh-coordinator -server vpn.example.com:3222
```

将 `vpn.example.com` 换成这台服务器可被客户端访问的公网 IPv4 地址或域名。若经过 NAT，需要把外部端口转发到服务器的监听端口。云安全组和服务器防火墙都应允许 **TCP 3222 和 UDP 3222**；改用其他端口时同步调整规则。

首次运行需要 `-server`，程序在数据目录保存服务器配置、证书、本机节点身份和邀请记录。默认监听 `0.0.0.0:3222`，默认数据目录为当前工作目录下的 `data`。以同一个数据目录再次启动时直接运行：

```sh
cd /opt/meshlink-linux
sudo ./bin/mesh-coordinator
```

把启动输出的接入链接和六位验证码交给受信任的客户端用户，在 Meshlink 客户端中加入网络。正常停止使用 `Ctrl+C`，后台服务使用 `systemctl stop`；程序处理 SIGTERM 后退出。不要同时用两个进程打开同一数据目录。

## 参数

| 参数 | 用途 |
| --- | --- |
| `-server vpn.example.com:3222` | 客户端可访问的服务器地址；首次启动必填，随后保存复用 |
| `-listen 0.0.0.0:3222` | 服务端绑定地址；首次默认上述地址，随后保存复用 |
| `-data /opt/meshlink-linux/data` | 配置及运行数据目录；默认 `./data` |
| `-max-uses 100` | 验证码允许的最大使用次数，首次默认 100；`-1` 表示不限次数，后续未显式指定时复用已有值 |
| `-new-code` | 创建新的验证码；只在需要更换验证码时使用 |
| `-devices` | 读取当前已登记设备并输出 JSON 后退出 |
| `-version` | 输出版本后退出 |

重启通常复用原来的邀请；验证码已耗尽或需要更换时可以在启动时加 `-new-code`。接入链接与六位验证码属于加入网络的凭据，启动输出和 systemd 日志只应供可信管理员访问。

## 设置 systemd 自启动

先按首次启动步骤用 `-server` 完成初始化，确认成功后按 `Ctrl+C` 退出，再安装示例服务：

```sh
sudo install -m 0644 /opt/meshlink-linux/systemd/meshlink-coordinator.service /etc/systemd/system/meshlink-coordinator.service
sudo systemctl daemon-reload
sudo systemctl enable --now meshlink-coordinator
sudo systemctl status meshlink-coordinator
sudo journalctl -u meshlink-coordinator -n 50 --no-pager
```

示例固定使用 `/opt/meshlink-linux/data`，与前述在 `/opt/meshlink-linux` 下首次运行的默认目录一致。若安装位置或数据目录不同，先修改服务文件中的 `WorkingDirectory` 和 `ExecStart`。不要把 `-new-code` 放入常驻服务的启动参数，否则服务每次重新启动都会生成新邀请。

## 通过组网访问这台 Linux 主机

客户端加入同一网络后，如果服务器已安装并启动 SSH，可用服务器组网地址连接，例如：

```sh
ssh your-user@10.77.0.1
```

SSH 通常使用 TCP 22，仍需要服务器已有账号及 SSH 认证，主机防火墙需要允许组网接口上的相应连接。该程序不会安装或启动 SSH 服务。Linux 的 RDP 只有另行安装并配置远程桌面服务后才可使用；本包不包含桌面或 RDP 服务器。

## 查看设备和更新

```sh
sudo /opt/meshlink-linux/bin/mesh-coordinator -data /opt/meshlink-linux/data -devices
```

Linux 更新通过替换对应架构的二进制包完成。更新前先停止正在运行的服务或前台进程并确认已退出；备份自己的 `data`，把新包解压到原安装位置，保留原 `data`，随后重启服务。Windows 安装器不用于 Linux 更新。跨机器部署时使用原始无配置包，不要复制正在运行的服务器数据目录。

如需向 Windows 客户端提供更新，可将配套的 `manifest.json` 和 `meshlink-无配置.zip` 放在数据目录的父目录。默认示例位置为 `/opt/meshlink-linux/manifest.json` 和 `/opt/meshlink-linux/meshlink-无配置.zip`；两者必须来自同一份经过验证的 Windows 发布。它们不属于 Linux 安装包，也不是 Linux 自更新文件。程序不额外启动公开的 HTTP 管理页面。

## 从源码构建

在 Windows 源码目录使用已安装的 Go 和 PowerShell：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-linux-coordinator.ps1
```

脚本读取 `VERSION` 并交叉编译 Linux amd64、arm64，使用 `CGO_ENABLED=0`。暂存目录为 `.cache/linux-coordinator/amd64` 和 `.cache/linux-coordinator/arm64`；压缩包输出为 `.cache/meshlink-linux-amd64.tar.gz` 和 `.cache/meshlink-linux-arm64.tar.gz`。构建脚本本身不覆盖 `release`，正式发布仍须遵循仓库的停止服务及保留数据约定。

归档仅含程序、此说明、systemd 示例、许可证、第三方声明、版本和构建信息；程序在归档中的权限为 `0755`。构建信息记录源码提交、工作区是否有未提交改动及产物 SHA-256。Windows 上的交叉编译和包验证不等于 Linux 实机组网验证；部署后应检查 TUN、端口连通性及客户端实际连接。
