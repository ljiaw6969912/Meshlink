# Task 5A 官方 Hub 风控策略、Relay 额度与合规草案基础 QA

日期：2026-07-09

## 交付边界

- 在 `internal/cloudhub` 内新增账号策略、quota 状态、风险事件和账号冻结/解冻/封禁基础能力；未新增 `internal/policy` 包。
- 默认新账号使用 `official-starter-low` 策略，包含账号级 Relay byte quota、active Relay session 限制和每日 Relay session 创建次数限制。
- Relay usage 复用 Task 4D 的 `RelayUsage` 与 `RelaySession` byte 字段；已记录 usage 会计入下一次 Relay session 创建前的 quota 判断。
- Frozen/banned 账号不能创建网络、邀请、设备加入、heartbeat、Relay session 创建或 Relay session 激活；设备吊销仍只影响单设备。
- 风险事件覆盖 `quota_exceeded`、`account_frozen`、`account_banned`、`relay_session_denied`、`invite_abuse_suspected`，并复用敏感字段过滤，避免记录 token/code/private key/内容数据。
- 新增 HTTP API 和 client 方法：查询账号策略额度状态、冻结/解冻/封禁账号、查询风险事件。
- 新增 `docs/compliance/` 中文合规草案，明确不提供匿名代理、公网出口、全隧道、HTTP CONNECT、SOCKS、公开转发节点、长期匿名中继或监管规避用途。
- 本任务不包含真实支付、订阅回调、套餐购买页面、团队/RBAC/企业合同、P2P 直连或 NAT 打洞。

## 测试命令

- `go test ./internal/cloudhub`
  - 结果：PASS。
- `go test ./internal/cloudhub ./internal/relay ./cmd/mesh-cloudhub`
  - 结果：PASS。
- `go test ./...`
  - 结果：PASS。

## 本地 API smoke

启动真实进程：

```powershell
go run ./cmd/mesh-cloudhub -listen 127.0.0.1:18090
```

使用 PowerShell `Invoke-WebRequest -SkipHttpErrorCheck` 脚本执行以下流程，脚本只输出脱敏摘要，不打印 invite token、invite code 或 relay join token：

1. 创建账号，查询 `/api/accounts/{id}/policy`，确认默认策略为 `official-starter-low`，Relay byte quota 为 `10485760`。
2. 创建网络、邀请，两台设备加入并 heartbeat online。
3. 创建 Relay session，调用 close 写入 `relay_bytes_in = 6291456`。
4. 再次创建 Relay session，返回 HTTP `429`，错误包含 `relay byte quota exceeded`。
5. 冻结账号，确认创建邀请、创建 Relay session、heartbeat 均返回 HTTP `403` 且错误包含 `frozen`。
6. 查询 `/api/risk-events?account_id=...`，确认包含 `quota_exceeded`、`account_frozen`、`relay_session_denied`。
7. 检查 risk event 响应不包含本次 invite token、invite code、relay join token、`private_key`、`clipboard`、`file_content`、`rdp_content`。

Smoke 结果：PASS。

## 文档检查

合规草案均包含“草案”和“律师审核前不得作为法律意见”提示。草案明确官方 Hub 不主动查看远程桌面内容、文件内容、剪贴板内容，只记录账号安全、滥用处理、计费/额度、客服排查和合规响应所需最小元数据。

## 后续事项

- Task 9 再接入真实支付、订阅回调、套餐购买页面和商业计费闭环。
- Task 10 再设计团队/组织/RBAC/企业合同和组织级风控。
- Task 6/7 再推进 P2P 直连、NAT 打洞等非 Relay 数据路径。
- 商业上线前需要律师审核并确认地区化隐私、留存、执法协助、消费者保护和跨境数据要求。
