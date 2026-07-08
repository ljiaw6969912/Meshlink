# Task 1 自托管闭环 E2E 记录

日期：2026-07-07

## 范围

本记录覆盖 Task 1 的自托管闭环验证：

```text
启动服务器 -> 生成接入链接和接入码 -> 客户端加入 -> 设备列表显示 -> RDP 诊断入口
```

发布门禁允许使用“两台 Windows 设备或两个隔离运行目录”。本轮使用两个临时隔离目录执行 onboarding 闭环，并使用本地 Web UI 做主流程可见性检查，避免写入仓库真实 `configs/`、`certs/`、`invites/`、`release/` 或 `dist/` 内容。

## 环境

- 工作目录：`C:\Users\Administrator\Desktop\wireguard`
- 分支：`codex/task1-selfhosted-rdp-loop`
- 日期/时区：2026-07-07，Asia/Shanghai
- 验证方式：Go 测试临时目录 + 本地 `mesh-ui` 只读浏览器检查

## 隔离目录闭环

命令：

```powershell
go test ./internal/onboarding -run TestSelfHostedProductLoopCreatesJoinableDevices -count=1 -v
```

结果：

```text
=== RUN   TestSelfHostedProductLoopCreatesJoinableDevices
--- PASS: TestSelfHostedProductLoopCreatesJoinableDevices (0.06s)
PASS
ok  	meshlink/internal/onboarding	0.120s
```

覆盖项：

- hub 临时目录执行 `StartServerMode`，生成 hub 配置、CA、hub 证书、短期接入链接和 6 位接入码。
- spoke 临时目录使用接入链接、接入码和本机名称 `office-pc` 加入。
- spoke 私钥只出现在 spoke 临时目录的 `certs/office-pc-key.pem`。
- hub 设备列表返回本机和 peer，peer 包含名称、离线状态、虚拟 IP、来源地址、证书指纹、上次在线时间。

## Web UI 主流程检查

本地启动：

```powershell
go run ./cmd/mesh-ui -listen 127.0.0.1:18080
```

只读浏览器检查结果：

- 首屏展示四入口：
  - 我有公网 IP，创建服务器
  - 我没有公网 IP，使用官方 Hub
  - 我有云服务器，帮我自建中继
  - 加入已有网络
- 设备列表默认可见。
- 官方 Hub、自建中继入口只显示“尚未开放”灰度说明。
- 创建服务器主流程只显示域名/公网地址、监听端口、本机虚拟 IP、服务状态、接入链接、接入码。
- 加入网络主流程只显示接入链接、接入码、本机名称。
- 普通主流程未出现 `ca_file`、`cert_file`、`key_file`、`server_name`、`service_name`、`routes`、JSON 配置、服务名、配置文件路径。
- 高级设置默认折叠；展开后保留服务、证书、JSON 配置、日志和详细诊断工具。
- 1366x768 和 390x844 视口下，入口、按钮、标题和标签未检测到文本溢出。

## 未覆盖项

本轮没有在两台真实 Windows 设备上以管理员权限运行完整服务/TUN/RDP E2E，因此以下项仍属于真实环境发布前风险：

- Windows 服务实际安装、启动并写出 `logs/MeshlinkAgent.status.json`。
- Wintun/TUN 虚拟网卡和路由实际生效。
- 两台真实设备之间通过虚拟 IP 打开系统 RDP 客户端并完成会话。

这些项需要发布候选前在真实 Windows 环境按发布门禁补测。
