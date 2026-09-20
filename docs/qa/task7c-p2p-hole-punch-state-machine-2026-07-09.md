# Task 7C QA - P2P UDP/NAT 打洞状态机最小闭环

日期：2026-07-09

## 范围

Task 7C 完成 `internal/p2p` 的 UDP/NAT 打洞状态机模型：双方已同步 NAT 摘要和 UDP 候选端口时，按 `simultaneous_udp` 策略尝试 direct；端口变化会重同步端口并有限重试；超时、半连接或达到重试上限后自动进入 Relay fallback。

Cloud Hub 继续复用既有 P2P negotiation 透传候选和 NAT 摘要，只新增最小审计摘要：是否具备 hole punch 条件、候选 UDP 端口数量、模式和重试上限。审计不写入 NAT 原始 reason、token、私钥或 join secret。

## 自动化覆盖

- `internal/p2p/hole_punch_test.go`
  - 同时拨号成功：双方 NAT 可打洞且 UDP 端口已同步时进入 public direct。
  - 端口变化：fake network 返回端口变化后，下一次尝试使用更新后的端口。
  - 超时和半连接：达到最大重试次数后停止 direct 尝试并 Relay fallback。
  - NAT 不适合打洞：不产生 direct fake network 流量，直接 Relay fallback。
  - Relay fallback 禁用：direct 失败后 fail closed，不创建 Relay。
  - 摘要 JSON 不暴露原始 NAT reason 或敏感材料。
- `internal/cloudhub/hole_punch_negotiation_test.go`
  - Cloud Hub negotiation 审计包含脱敏 hole punch 摘要。
  - UDP candidate registration 仍经过账号、网络、设备在线和候选规范化校验。

## 实际测试命令

本机系统 Go 安装的 `go/ast` 源文件存在拼写损坏，普通 `go test` 会先在标准库编译阶段失败：

- `go test -count=1 ./internal/p2p ./internal/cloudhub`：FAIL，`C:\Program Files\Go\src\go\ast\ast.go:606:44: undefined: tgken`
- `go test -count=1 ./...`：FAIL，同一 `go/ast` 本机环境问题

为验证项目代码，本任务按要求使用临时 overlay，只替换该标准库拼写错误，不写入仓库：

- `go test -count=1 -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json ./internal/p2p ./internal/cloudhub`：PASS
- `go test -count=1 -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json ./...`：PASS

## 边界和未完成项

- 未做真实公网 STUN/TURN 探测，NAT 摘要仍来自既有模型和测试输入。
- 未引入真实 QUIC 依赖；`quic_v1` 仍只是传输选择候选枚举。
- 未实现真实 UDP 数据面、加密会话、拥塞控制、重传或多路径热切换。
- 未做 UI、Windows RDP 数据桥或公网测试机验证。
- 未写入任何测试机密码、token、私钥或真实公网测试信息。
