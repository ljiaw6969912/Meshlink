# Task 7D QA - P2P 连接质量评分与路径指标最小闭环

日期：2026-07-09

## 范围

Task 7D 完成 P2P 连接质量评分与路径指标的最小闭环：

- `internal/p2p` 新增 `ConnectionQualityInput`、`ConnectionQuality`、`ConnectionQualitySummary` 和评分/聚合函数。
- 质量摘要记录路径类型、路径状态、分数、延迟、丢包、抖动、Relay 字节数、切换次数和切换原因。
- 延迟、丢包、抖动、Relay 路径和切换次数会降低评分；模拟延迟从低到高时评分下降。
- direct 与 relay 统计分桶分开；direct 路径即使误传 Relay 字节，也会在评分和 CloudHub 日志归一化时清零。
- `internal/cloudhub` 的连接日志新增最小质量字段，并在账号管理摘要中聚合 `connection_quality`。
- `internal/relay` 关闭 Relay session 时继续只上报非敏感 session 元数据和 Relay 字节，并让 CloudHub 生成 relay-only 质量摘要。

## 自动化覆盖

- `internal/p2p/quality_test.go`
  - 同一 direct 路径下，延迟从 20ms 升到 350ms 时评分下降。
  - direct/relay 聚合分桶独立，Relay 字节不会污染 direct 指标。
  - 切换次数和原因进入可审计摘要。
- `internal/cloudhub/quality_summary_test.go`
  - `RecordConnectionLog` 会计算质量分数并保留路径状态、延迟、丢包、抖动和切换摘要。
  - direct 日志误传的 Relay 字节会被清零。
  - 账号管理摘要聚合 direct 与 relay 质量分桶。
  - 摘要 JSON 不暴露 token、私钥、RDP 内容或文件内容等敏感材料。
- `internal/relay/server_test.go`
  - Relay runtime 写出的连接日志包含 relay 路径、closed 状态、Relay 字节和质量分数。

## 实际测试命令

本机系统 Go 安装的 `go/ast` 源文件存在拼写损坏，普通 `go test` 会先在标准库编译阶段失败：

- `go test -count=1 ./internal/p2p ./internal/cloudhub ./internal/relay`：FAIL，`C:\Program Files\Go\src\go\ast\ast.go:606:44: undefined: tgken`
- `go test -count=1 ./...`：FAIL，同一 `go/ast` 本机环境问题

为验证项目代码，本任务按要求使用临时 overlay，只替换该标准库拼写错误，不写入仓库：

- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./internal/p2p ./internal/cloudhub ./internal/relay`：PASS
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./...`：PASS

## 边界和未完成项

- 未做 UI、诊断中心页面或用户可见图表。
- 未做多路径热切换，只记录切换次数和原因。
- 未接 Windows RDP 数据桥，不记录剪贴板、文件内容或 RDP 数据。
- 未引入真实公网 STUN/TURN、真实 QUIC 依赖或公网测试机。
- 未做真实网络质量探测；延迟、丢包和抖动来自调用方或测试输入。
- 未写入任何测试机密码、token、私钥或真实公网测试信息。
