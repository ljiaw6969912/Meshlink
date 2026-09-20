# Task 8B QA - 设备列表连接方式与基础质量状态

日期：2026-07-09

## 完成范围

- Web 设备列表每行新增普通用户可理解的连接方式标签：`直连`、`中继`、`离线`、`连接失败`、`RDP 不可达`。
- Web 官方设备列表复用同一展示逻辑，继续只消费 Task 8A 已有的 `path_type`、`path_state`、`latency_ms`、`quality_score` 等字段。
- 有延迟或质量分时，设备行以紧凑文案展示，例如 `24 ms · 质量 96`。
- 设备普通详情新增 `连接方式`，并从普通设备详情/行信息中移除证书指纹、状态文件、路由等偏底层内容。
- Windows 桌面设备列表把 Task 8A 的 `ConnectionStatus` 透传到 `meshNode`，列表行和详情显示同一套连接摘要。
- 窄窗口下新增 `.device-main`、`.device-title-line`、`.connection-pill` 等样式，状态标签可换行，按钮列可下移。

## 自动化覆盖

- `internal/ui/server_test.go`
  - 静态 UI 包含连接状态展示函数、用户文案和窄屏布局保护样式。
  - 检查设备列表展示文案不新增 NAT、CSR、证书路径、路由表等技术标签。
- `cmd/mesh-desktop/main_windows_test.go`
  - 覆盖 direct、relay、offline、failed、rdp-unreachable 的桌面连接摘要。
  - 覆盖延迟/质量紧凑展示。
  - 覆盖普通详情中不出现 NAT、CSR、证书路径、路由表、JSON 等技术标签。

## 浏览器检查

使用内存 mock 本地 UI 服务加载同一份 `internal/ui/static` 资源，设备数据覆盖：

- direct：`path_type=lan_direct`、`path_state=lan_direct_connected`、`latency_ms=24`、`quality_score=96`
- relay：`path_type=relay`、`path_state=fallback_relay`、`latency_ms=88`、`quality_score=72`
- offline：`path_state=offline`
- failed：`path_state=failed`
- rdp-unreachable：`path_state=rdp-unreachable`

检查视口：

- `1180x760`：无水平溢出；标题、连接标签、操作按钮无重叠。
- `390x820`：无水平溢出；标题、连接标签、操作按钮无重叠；按钮换到下一行后仍可读。

本轮浏览器检查生成过窄窗口截图用于人工确认，但截图不写入仓库。

## 已执行验收

本机普通 `go test` 仍会先失败于 Go 标准库环境问题：

```text
C:\Program Files\Go\src\go\ast\ast.go:606:44: undefined: tgken
```

这是本机 Go 标准库 `go/ast` 中 `token` 被拼写损坏为 `tgken` 的环境问题，不写入仓库。按任务说明使用临时 overlay：

```text
C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json
```

- `go test -count=1 ./internal/ui ./internal/onboarding ./internal/agent ./internal/p2p`：FAIL，失败原因是本机 Go 标准库 `go/ast` 的 `tgken` 拼写损坏。
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./internal/ui ./internal/onboarding ./internal/agent ./internal/p2p`：PASS。
- `go test -count=1 ./...`：FAIL，失败原因同上，为本机 Go 标准库 `go/ast` 的 `tgken` 拼写损坏。
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./...`：PASS。
- `git diff --check`：PASS。
- `git diff --cached --check`：PASS。

## 未完成项

- 未做一键诊断报告。
- 未做套餐、订阅、流量提醒或 Relay 用量提示。
- 未改 RDP 数据桥；`RDP 不可达` 仅消费已有 `path_state`。
- 未引入真实公网 STUN/TURN。
- 未引入真实 QUIC 依赖。
- 未做真实公网测试机、真实复杂 NAT 或真实 RDP 数据面验证。
- 未写入任何测试机密码、token、私钥或真实公网测试信息。
