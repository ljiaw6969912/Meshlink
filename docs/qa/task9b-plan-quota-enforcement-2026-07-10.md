# Task 9B 套餐额度执行与可解释降级 QA

日期：2026-07-10

## 范围

- 基于 `ff05823 TASK 9A plan catalog and quota model` 的稳定套餐 ID、额度语义和 `AccountPolicyMappingForPlan` 边界。
- 依据 PTR-25、PTR-26、PTR-27 和阶段五发布门禁，执行设备数、同时在线数和官方 Relay 用量限制。
- 本任务不新增支付、订阅 provider、webhook、价格、订单、退款、发票、税务、购买页、升级按钮或其他 UI。

## 兼容边界

- `Account.PlanID` 是可选绑定。空 `plan_id` 的旧账号继续使用既有 `AccountPolicy`，不执行套餐设备数或在线数限制。
- 计划绑定使用 9A 稳定 ID：`free`、`personal`、`family`、`team`、`enterprise`。
- `CreateAccountRequest.plan_id` 只接受有效套餐 ID；无套餐绑定的账号不会因迁移被默认收紧或扩大权限。
- 旧 `AccountPolicy` 的 Relay 字节、并发会话和每日创建次数校验继续生效。

## 额度语义执行

- `unavailable`：安全拒绝，返回结构化 429 quota 错误。例如 `free` 的官方 Relay 不可用。
- `limited`：内置正整数直接执行；运营配置额度必须通过 `WithPlanQuotaLimits` 显式注入，未配置时安全拒绝。
- `unlimited`：不按该维度限制。例如自建基础能力和 Free 的设备互联边界不受官方 Hub 默认策略误伤。
- `contract_custom`：必须通过合同配置解析；未解析时不会静默当成无限，官方 Hub 操作安全拒绝。

## 已执行额度

- `JoinDevice` 在更新 invite 用量和创建设备前按账号维度检查设备上限；超额不消耗 invite、不创建设备、不留下部分状态。
- `HeartbeatDevice` 仅在非在线设备变为在线时占用在线名额；重复在线 heartbeat 不重复计数；超额保持原设备状态。
- 官方 Relay 创建继续检查旧字节、并发会话和每日创建次数；套餐 Relay 权益会生成有效 byte budget 并传给 runtime。
- Relay 数据路径按会话/账号剩余预算双向共享扣减；同一 runtime 内同账号多个 Relay session 共享账号预算，达到硬上限时关闭连接，只记录实际转发字节，关闭只记账一次。
- P2P 直连成功不创建 Relay session、不消耗 Relay；直连失败且额度不足时不会授权 Relay fallback。

## 可解释错误与审计

- 新增 `QuotaError`，包含稳定 `category`、`dimension`、`used`、`limit`、`mode`、`plan_id` 和用户建议。
- `errors.Is(err, ErrQuotaExceeded)` 继续成立；HTTP 返回 429，并在响应体返回 `quota` 结构；官方 client 的 `APIError.Quota` 保留同一结构化详情。
- 建议覆盖释放/移除设备、下线设备、优先直连、使用自建或管理员 Relay、查看套餐状态。
- 额度拒绝记录 `quota_exceeded` 风控事件，Relay 拒绝继续记录 `relay_session_denied`；元数据只含账号、网络、设备、维度、用量、上限、mode、plan_id 等最小字段。
- 审计和风控不记录邀请凭据、join token、私钥、用户内容或原始流量。

## Task 10 后续责任

- 成员数、家庭/团队管理、企业 RBAC/授权、日志留存和清理仍缺少完整 Task 10 数据模型。
- 本任务只保留可复用 evaluator 与接口边界，不伪造成员、RBAC、企业授权或日志清理执行结果。

## 测试结果

- `go test -count=1 ./internal/cloudhub ./internal/relay ./internal/onboarding`：通过。
- `go test -count=1 ./...`：通过。
- `git diff --check`：通过；仅提示 Windows 工作区下一次 Git 触碰部分 Go 文件时会做 LF/CRLF 转换。
- `git diff --cached --check`：通过。

## 本机 Go overlay

本次目标包和全仓 Go 测试均未触发本机 `go/ast` 的 `tgken/token` 损坏问题，未使用临时 overlay。`C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json` 未写入仓库。

## 明确未做

- 未做真实支付、订阅 provider、checkout、webhook、订单、退款、发票或税务。
- 未做购买页、升级按钮或其他 UI。
- 未做 Task 10 的成员/RBAC/企业授权/日志清理。
- 未扩展为匿名代理、公网出口、全隧道、公开转发、无账号 Relay 或任意公网端口转发。
- 未使用公网测试机，未写入密码、token、私钥或真实公网信息。
