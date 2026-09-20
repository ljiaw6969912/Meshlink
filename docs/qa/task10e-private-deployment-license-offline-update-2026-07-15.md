# Task 10E 私有部署、签名授权与离线更新 QA

## 范围与结论

- 日期：2026-07-15
- 基线：`2a3abdb TASK 10D add bulk deployment and rollout management`
- 范围：只完成实施计划 10.5 的私有部署包、签名授权校验、简单到期合同策略、离线更新验证/暂存、支持入口和隔离网本地模拟；未开展 Task 11。
- 本地工程验收通过。阶段整体仍不能表述为真实客户隔离网、生产代码签名、生产 PKI 或商业上线验收。

对应 PTR-06/PTR-13/PTR-17/PTR-26/PTR-46 与阶段门禁的工程证据包括：私有 Hub 受控启动参数、组织/deployment 绑定授权、`contract_custom` 明确额度、真实设备/成员/部署/P2P/Relay/heartbeat service 门禁、离线更新验证、owner/admin 支持入口和可重复私有包。

## 签名授权

- 授权文档使用固定 schema、确定性 JSON 结构编码和 Ed25519；签名消息包含 schema、key ID 与规范载荷。
- 载荷包含稳定 license/key/organization/deployment ID、客户字段、签发/生效/到期时间、简单到期策略、设备/成员/并发/Relay/审计/部署额度、rollout/离线更新权限和支持信息。
- 可信配置按 key ID 保存一个或多个 Ed25519 公钥。测试覆盖旧/新公钥同时可信、未知 key、签名篡改、错误 organization/deployment、未生效、到期和纳秒级边界。
- 所有测试私钥均由测试运行时生成；仓库、日志、文档和包中没有固定测试私钥、生产私钥或真实许可证。
- 导入先验证后写临时文件、`Sync`、原子 rename，并使用合理文件权限；失败导入和跨组织导入不会替换当前授权。重启重新读取并验证签名文件。
- owner/admin API/UI 只返回 license/key/organization/deployment、时间、策略、entitlement 和支持摘要；不返回客户字段、原始载荷、签名或密钥材料。operator/member 拒绝。

## 额度与到期矩阵

企业私有模式的新账号默认使用 `enterprise` 的 `contract_custom`；首次只允许以受控配置中的 organization ID 建立 owner 组织，解除导入前 bootstrap 循环。导入前设备加入等授权操作仍拒绝。免费、自建和未配置私有授权管理器的旧流程保持原行为。

| 状态/策略 | 新设备、成员、部署凭据、rollout | 已有设备 heartbeat | 已有设备新 P2P/Relay 协商 |
| --- | --- | --- | --- |
| 到期前 | 按签名 entitlement/额度 | 允许，仍受并发等额度约束 | 允许，仍受授权、Relay 权限和额度约束 |
| `continue_existing` 到期后 | 拒绝 | 允许 | 允许 |
| `grace_period` 宽限内 | 拒绝 | 允许 | 允许 |
| 宽限结束且终止策略为 `continue_existing` | 拒绝 | 允许 | 允许 |
| `deny_all` 或宽限结束终止为 `deny_all` | 拒绝 | 拒绝 | 拒绝 |

设备数、成员数、并发在线、Relay 月流量、活跃 Relay 会话、审计保留天数和部署数均显式解析为有限的 `contract_custom`。授权缺失、数值 entitlement 缺失、未知维度或账号不属于签名组织时安全拒绝，不回退 unlimited。Relay、rollout、离线更新布尔权限为 false 时对应能力拒绝。

授权导入/校验失败、即将到期/到期和策略拒绝写入现有组织审计；离线更新通过回调产生验证、暂存或拒绝记录。记录只含稳定 ID、organization/deployment、动作、结果、版本、时间和固定错误类别，不含原始授权、签名、密钥、客户名称或用户内容。

## 离线更新

- 签名 manifest 绑定 update/key/license/deployment ID、目标平台/架构、版本、前序版本、连续 sequence、制品文件名/SHA-256/大小和 manifest 到期时间。
- 验证覆盖签名篡改、未知 key、制品篡改、错误平台/架构/deployment/license、缺少离线更新 entitlement、到期 manifest、降级、错误前序版本和越序。
- 通过验证的 ZIP 先复制到受限临时文件并再次校验，再原子进入 staging；失败保留当前版本、last-known-good 和已有已验证包。该路径不下载公网资源、不执行包内脚本。
- 当前只完成验证与 staging 边界，实际版本切换继续复用既有更新流程；不声明生产更新发布或代码签名完成。

## 隔离网络与私有包

- `httptest` 本地回环模拟完全无外网的私有 Hub：受控 organization bootstrap、HTTP 导入运行时签名授权、客户端加入、到期后新设备拒绝、`continue_existing` 已有设备 heartbeat 继续。
- 该测试没有使用公网测试机，也不代表真实客户隔离网络、TLS、生产密钥托管或多实例验收。
- 私有包复用当前源码重新构建的 `mesh-cloudhub.exe` 和现有客户端产物，包含配置模板、公钥配置示例、启动脚本、授权/离线更新目录约定及运维手册。
- 连续两次构建的 `meshlink-private-0.1.5.zip` SHA-256 均为 `aecdb9e0a5590b4b9e559db7e3a63b1c32fc99678d8492f19f8dae55f87dbb46`。验证脚本确认文件名/版本/清单/逐文件哈希一致、必需文件存在，并扫描无私钥、真实授权、密码、长期 token 或 TLS 私钥；私有配置只含占位符和本地监听示例，没有真实部署地址。临时包已删除。
- 清单明确 `code_signed=false` 并保留外部生产代码签名门禁；没有宣称生产签名完成。

## UI 与支持边界

- owner/admin 可查看脱敏授权摘要、导入签名授权并查看 entitlement、状态、到期策略和支持标识/联系方式；其他角色由 service/API 拒绝。
- 支持入口明确不会自动上传用户内容、远程桌面、文件、剪贴板、日志或原始授权；仅允许管理员人工复核后提供稳定授权/deployment ID、版本、固定错误类别和时间。
- 按任务要求未使用 Browser MCP 或 node_repl。HTTP/UI 自动化覆盖 API、错误态、DOM 标识、桌面/移动 User-Agent 和窄屏 CSS 规则；未执行真实浏览器交互或截图，不将其表述为浏览器实测。

## 测试记录

- `go test -count=1 ./internal/cloudhub ./internal/onboarding ./internal/ui ./internal/update ./cmd/mesh-cloudhub`：通过。
- `go test -count=1 ./internal/licensing`：通过（包含于最终全仓回归，开发期定向运行亦通过）。
- `node --check internal/ui/static/app.js`：通过，无输出。
- PowerShell 语法解析：`build.ps1`、`package-private.ps1`、`verify-private-package.ps1`、`run-private-hub.ps1` 通过。
- 私有包真实产物双构建、验证和秘密扫描：通过；临时文件已清理。
- 首次 `go test -count=1 ./...` 暴露既有 Relay 撤销与写计量报告之间的逻辑竞态：接收端已读到 4 字节，但关闭报告可能先读取 0。保留原断言，修复为报告等待已开始的计量写结束；原失败用例连续 100 次及 `internal/relay` 全包通过。
- 修复后最终 `go test -count=1 ./...`：通过。
- `git diff --check` 与暂存后的 `git diff --cached --check`：提交前执行并记录在 Git 提交结果中。

## 明确未做

- 未实现授权签发后台、在线激活、订单/合同/发票、复杂 PKI、多租户商业系统、联网遥测、外部制品或支持系统。
- 未生成或提交生产/固定测试签名私钥、TLS 私钥、真实授权、客户数据、账号密码或长期 token。
- 未实现任意离线脚本执行、远程 shell、通用代理、公网出口、全隧道或文件传输，也未改变 P2P/Relay 数据面协议。
- 未把 MemoryStore、真实隔离网、TLS、灾备、多实例协调或生产代码签名伪装为已验收。
- 未继续 Task 11。
