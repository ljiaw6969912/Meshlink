# Task 8C QA - 一键诊断报告最小闭环

日期：2026-07-09

## 完成范围

- `internal/diagnose` 新增一键诊断报告模型和 `RunOneClick`/`BuildOneClickReport`。
- 报告输出结构化字段：发现的问题、影响原因、建议处理、下一步动作。
- 报告复用已有服务诊断、`diagnose.CheckRDPTarget`，并消费 Task 8A/8B 的 `path_type`、`path_state`、`quality_score`、`latency_ms`、`switch_count` 等状态字段。
- Web 本地 UI 新增设备列表顶部“一键诊断”按钮和最小报告展示区。
- Windows 桌面设备列表新增“一键诊断”按钮，并在右侧详情区展示同一份普通文本报告。

## 覆盖场景

- 后台连接服务未运行或不可用。
- 虚拟网卡暂不可用。
- 防火墙或网络阻断。
- 目标设备离线。
- 远程桌面不可达。
- 设备连接失败。
- 中继连接质量异常。

## 普通用户文案检查

- 报告公开字段只展示普通用户可理解的标题、问题、影响、建议和下一步动作。
- 自动化检查禁止报告公开文案出现 `NAT`、`CSR`、`CA`、`证书路径`、`路由表`、`服务名`、`JSON`、`MeshlinkAgent`。
- Web 报告渲染函数不展示底层 `checks` 原始明细。

## 前端布局检查

本轮采用静态等价检查，没有生成浏览器截图：

- `internal/ui/server_test.go` 检查一键诊断入口、报告渲染函数和四个固定字段存在。
- `internal/ui/server_test.go` 检查 `.diagnostic-report`、`.finding`、`.finding-head` 具备 `flex-wrap: wrap` 和 `overflow-wrap: anywhere`，用于避免窄屏文字重叠。
- `internal/ui/server_test.go` 检查报告渲染函数不输出底层术语或原始低层检查。

## 已执行验收

本机普通 `go test` 仍会先失败于 Go 标准库环境问题：

```text
C:\Program Files\Go\src\go\ast\ast.go:606:44: undefined: tgken
```

这是本机 Go 标准库 `go/ast` 中 `token` 被拼写损坏为 `tgken` 的环境问题，不写入仓库。按任务说明使用临时 overlay：

```text
C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json
```

- `go test -count=1 ./internal/diagnose ./internal/ui ./cmd/mesh-desktop`：FAIL，失败原因是本机 Go 标准库 `go/ast` 的 `tgken` 拼写损坏。
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./internal/diagnose ./internal/ui ./cmd/mesh-desktop`：PASS。
- `go test -count=1 ./internal/diagnose ./internal/ui ./internal/onboarding ./internal/agent ./internal/p2p`：FAIL，失败原因同上。
- `go test -count=1 ./...`：FAIL，失败原因同上。
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./internal/diagnose ./internal/ui ./internal/onboarding ./internal/agent ./internal/p2p`：PASS。
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./...`：PASS。

## 未完成项

- 未做 Relay 流量提醒、套餐提示或订阅能力。
- 未改 RDP 数据桥；远程桌面诊断只检查端口可达性和已有连接状态。
- 未引入真实公网 STUN/TURN。
- 未引入真实 QUIC 依赖。
- 未做复杂诊断中心页面。
- 未做真实公网测试机、真实复杂 NAT 或真实 RDP 数据面验证。
- 未写入任何测试机密码、token、私钥或真实公网测试信息。
