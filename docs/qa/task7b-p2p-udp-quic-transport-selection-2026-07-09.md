# Task 7B QA - UDP/QUIC 传输选型与可审计传输抽象

日期：2026-07-09

## 范围

Task 7B 完成 7.2 的最小闭环：传输选型文档、`internal/p2p` 可审计传输选择、标准库 UDP loopback smoke、Cloud Hub negotiation 传输摘要透传和审计元数据。

本任务不实现真实 QUIC 数据面、公网 UDP E2E、STUN/TURN、NAT 打洞状态机、多路径热切换、UI 或 Windows RDP 数据面桥接。

## 自动化覆盖

- `internal/p2p/transport_test.go`
  - 默认策略保持 `tcp_tls_v1`。
  - 没有双方 NAT 摘要时，即使策略开启实验传输，也保持 TCP/TLS 并排除 UDP/QUIC。
  - 双方 NAT 摘要允许 UDP 时，可选择 QUIC 并保留 UDP/TCP 候选。
  - UDP blocked / Relay 推荐时排除 UDP/QUIC 并走 Relay fallback。
  - UDP loopback prober 覆盖本地收发、取消和审计原因。
  - JSON 结果不包含敏感材料或原始探测细节。
- `internal/cloudhub/transport_selection_test.go`
  - Cloud Hub negotiation 可返回传输选择摘要。
  - NAT 阻断时不会暴露 UDP/QUIC 候选。
  - 既有账号、网络、设备、在线和吊销校验仍在传输选择前执行。

## 实际测试命令

本机系统 Go 安装的 `go/ast` 源文件存在拼写损坏，普通 `go test` 会先在标准库编译阶段失败。为验证项目代码，本任务使用临时 overlay 只替换该标准库拼写错误，不写入仓库。

- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json ./internal/p2p`：PASS
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json ./internal/cloudhub`：PASS
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json ./...`：PASS
- `go test -count=1 -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json ./internal/p2p ./internal/cloudhub`：PASS
- `go test -count=1 -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json ./...`：PASS

## 风险和边界

- `quic_v1` 目前只是候选枚举和审计结果，不代表真实 QUIC 依赖已接入。
- `udp_datagram_v1` 目前只验证本地 datagram 接口语义，不提供加密、重传、拥塞控制或会话复用。
- 传输选择依赖 Task 7A NAT 摘要；缺少双方摘要时保持保守 TCP/TLS。
- Relay quota、封禁、吊销、在线状态仍由既有 Cloud Hub 控制面负责，传输选择不能绕过这些校验。

## 后续任务

- Task 7C：打洞状态机和 fake network 覆盖。
- Task 7D：质量评分、多路径和热切换前置指标。
- Task 8：用户可见连接状态和诊断中心。
