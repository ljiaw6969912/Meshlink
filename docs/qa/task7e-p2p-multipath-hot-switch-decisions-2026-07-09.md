# Task 7E QA - P2P 多路径选择与热切换决策最小闭环

日期：2026-07-09

## 完成范围

Task 7E 完成 P2P 多路径选择与热切换决策的最小闭环：

- `internal/p2p` 新增多路径选择模型，复用 Task 7D 的 `ScoreConnectionQuality` 对 direct 与 relay 候选评分。
- direct 候选覆盖 `lan_direct` 与 `public_direct`，relay 候选覆盖 `relay`；同组候选按质量分数优先，分数相同时按既有路径 rank 保持稳定。
- 新会话选择会基于 direct/relay 分数产出 `preferred_path_type`、`score_delta` 和 `switch_reason`。
- 当前 direct 质量差时可自动切到 relay，原因记录为 `direct_quality_degraded`。
- 当前 direct 断开或关闭时可自动切到 relay，原因记录为 `direct_disconnected`。
- 当前 relay 上 direct 恢复且评分优于 relay 时，新会话优先 direct，但不会把既有 relay 会话自动切回 direct，原因记录为 `direct_recovered_better_for_new_session`。
- 策略包含最小评分差和切换冷却窗口，relay 回 direct 会受冷却窗口抑制，避免无限切换或抖动。
- 决策摘要记录来源路径、目标路径、评分差值、是否自动切换、是否被抑制和抑制原因。

## CloudHub / Relay 接入边界

- `internal/cloudhub` 的 connection log 新增非敏感切换摘要字段：
  - `switch_from_path`
  - `switch_to_path`
  - `switch_score_delta`
  - `auto_switched`
- CloudHub 继续复用 Task 7D 的 switch reason 清理逻辑；包含 token、password、secret、private key 等敏感关键词的原因会被替换为 `redacted`。
- CloudHub 只接受已知路径枚举作为切换来源/目标路径，未知值会被清空，避免把任意字符串写入审计摘要。
- `internal/relay` 本任务不新增运行时行为；既有 relay session close、usage 和 connection log 流程保持不变。

## 自动化覆盖

- `internal/p2p/multipath_test.go`
  - direct 评分优于 relay 时，新会话首选 direct。
  - direct 质量差时，当前 direct 自动切 relay，并记录 `direct_quality_degraded`。
  - direct 断开时，当前 direct 自动切 relay，并记录 `direct_disconnected`。
  - relay 活跃时 direct 恢复且评分更高，新会话首选 direct，但不自动切换现有 relay 会话。
  - relay 回 direct 在冷却窗口内被抑制，避免抖动。
- `internal/cloudhub/quality_summary_test.go`
  - connection log 记录来源路径、目标路径、评分差值和自动切换标志。
  - 敏感 switch reason 被清理，账号管理摘要不暴露敏感材料。

## 已执行验收

本机普通 `go test` 会先失败于 Go 标准库环境问题：

```text
C:\Program Files\Go\src\go\ast\ast.go:606:44: undefined: tgken
```

这是本机 Go 标准库 `go/ast` 中 `token` 被拼写损坏为 `tgken` 的环境问题，不写入仓库。按任务说明使用临时 overlay：

```text
C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json
```

- `go test -count=1 ./internal/p2p ./internal/cloudhub ./internal/relay`：FAIL，失败原因是本机 Go 标准库环境问题 `C:\Program Files\Go\src\go\ast\ast.go:606:44: undefined: tgken`。
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./internal/p2p ./internal/cloudhub ./internal/relay`：PASS。
- `go test -count=1 ./...`：FAIL，失败原因同上，为本机 Go 标准库 `go/ast` 的 `tgken` 拼写损坏。
- `go test -overlay C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json -count=1 ./...`：PASS。

## 未完成项

- 未接入真实公网 STUN/TURN。
- 未引入真实 QUIC 依赖。
- 未接 Windows RDP 数据桥。
- 未实现真实双机公网或复杂 NAT 数据面热切换。
- 未写入任何测试机密码、token、私钥或真实公网测试信息。
