# Task 9C 订阅 Provider 适配层与幂等事件边界 QA

日期：2026-07-10

## 范围

- 基于 `920e94c TASK 9B enforce plan quotas and degradation`。
- 新增最小订阅 provider 接口、仅测试用 HMAC provider、订阅状态模型、Webhook 验签、原子幂等事件边界、订阅状态查询 service/API/client。
- 未接入 Stripe、微信、支付宝或任何真实支付平台；未做价格、checkout、订单、扣款、退款、发票、税务、购买 UI、升级按钮、Task 10 成员/RBAC/企业合同，也未改 RDP/P2P/Relay 数据面。

## 状态机契约

- 支持 `pending`、`active`、`past_due`、`canceled`、`expired`。
- `active` 绑定 provider 事件中的稳定套餐 ID。
- `canceled` 且明确 `effective_until`、`current_period_end` 或 `expires_at` 仍在未来时，保留其付费 plan；到期后刷新为 `expired` 并回退 `free`。
- `expired` 立即回退 `free`。
- `pending`、`past_due` 不会把账号升级到事件目标 plan；如果账号当前 plan 正是该订阅此前绑定的 plan，则安全回退 `free`。
- 没有任何订阅记录的旧账号不被改成 `free`，继续保持 9B 的空 `plan_id`/`AccountPolicy` 兼容行为。

## 幂等与一致性

- 幂等键为 `provider + event_id`。
- 同 ID、同 body digest 重放只返回 repeated，不重复改订阅、账号 plan 或额度。
- 同 ID、不同 body digest 返回 409 conflict，不改变订阅或账号 plan。
- MemoryStore 在同一锁内完成事件 claim、订阅状态更新、账号有效 `plan_id` 变更，覆盖并发重放只有一次 accepted。
- 旧版本或乱序事件返回 stale，不回退状态。

## Webhook 安全边界

- Webhook 使用原始 body 验签。
- HMAC 验签使用常量时间比较。
- 要求 `X-CloudHub-Timestamp`，并执行时间窗口检查；缺失、篡改、过期、未来过远、未知 provider 均拒绝。
- 请求体限制为 `MaxSubscriptionWebhookBodyBytes`，超过返回 413。
- 拒绝、冲突、重复、接受和状态变更审计只记录 provider、event ID、状态、套餐、生效时间和 outcome 等最小元数据。
- 审计、公开 API 和 client 响应不暴露签名、provider secret、原始 body、body digest、provider customer/subscription 引用、支付信息、凭据或用户内容。

## 额度联动

- 订阅激活 `family` 后，账号 `plan_id` 生效为 `family`，9B 的设备数、在线设备和官方 Relay 额度按 family 计算。
- `pending`/`past_due` 的 family 事件不会提升 personal 的在线设备额度。
- 订阅 `expired` 或 canceled 到期后回退 `free`；官方 Relay 按 `free` 的 `unavailable` 安全拒绝。
- 事件重放不会重复增加或重算额度；额度只来自账号当前有效 `plan_id` 和 9B evaluator。

## 测试结果

- `go test -count=1 ./internal/cloudhub`：通过。
- `go test -count=1 ./...`：通过。
- `git diff --check`：通过；仅提示 Windows 工作区下一次 Git 触碰部分文件时会做 LF/CRLF 转换。
- `git diff --cached --check`：待暂存后运行。

## 本机 Go overlay

- 当前 `go test -count=1 ./internal/cloudhub` 和 `go test -count=1 ./...` 均未触发本机 `go/ast` 的 `tgken/token` 损坏问题，未使用临时 overlay。
- 若全仓测试触发该本机问题，将仅使用既有临时 overlay，不写入仓库，并在最终结果中说明。

## 明确未做

- 未接真实支付平台，未发起外网请求。
- 未做价格、checkout、订单、扣款、退款、发票、税务。
- 未做购买 UI、升级按钮或客户端购买入口。
- 未做 Task 10 成员、RBAC、企业合同、合同授权或私有化计费。
- 未修改 RDP/P2P/Relay 数据面，未使用公网测试机。
- 未写入真实密码、token、私钥或支付数据。
