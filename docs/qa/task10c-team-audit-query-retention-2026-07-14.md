# Task 10C 团队审计查询与保留期 QA

- 日期：2026-07-14
- 基线：`2269b11 TASK 10B enforce RBAC and device access`
- 范围：Task 10C；未开展 Task 10D/10E

## 验收摘要

组织审计查询在 Cloud Hub service 层合并显式归属 organization 的组织事件与连接日志。结果使用固定字段 DTO，按时间、类型和记录 ID 倒序稳定排序；签名游标绑定 organization、筛选、页大小、套餐保留配置和首屏查询快照。后续分页同时复用首屏快照时间计算保留期 cutoff，避免翻页期间时钟前进造成临界记录漂移。

新产生的 P2P、Relay 和直接记录连接日志会保存由权威组织设备/连接授权得出的 organization 归属；没有 organization 归属的历史记录不会按账号或设备猜测。查询中的跨组织成员/设备 ID 只会得到当前组织内的空结果，不用于资源存在性探测。

## 权限与状态

| 场景 | 查询 | 手动清理 |
| --- | --- | --- |
| active owner | 允许 | 允许 |
| active admin | 允许 | 允许 |
| operator/member | 拒绝 | 拒绝 |
| 暂停/移除成员 | 拒绝 | 拒绝 |
| 冻结/封禁账号 | 拒绝 | 拒绝 |
| suspended organization | 拒绝 | 拒绝 |

HTTP actor 只取可信请求头；cleanup 请求体中的 actor 字段不能提升权限。UI/API 之外，service 会再次检查账号状态、成员状态、角色和组织状态。系统维护入口不经公共 HTTP 暴露，但复用相同的 organization 状态、owner 有效套餐、quota evaluator、cutoff 和 Store 清理策略。

## 筛选、分页与 DTO

- 组合筛选覆盖 member（匹配 actor 或 member）、源设备、目标设备、包含边界的起止时间、`lan_direct`/`public_direct`/`relay`、仅 Relay 字节大于零及 Relay 入出字节合计下限。
- 默认每页 50，最大 100；越界页大小拒绝。
- 同一时间以记录类型、ID 稳定排序；游标篡改、跨 organization 使用或修改筛选/页大小后复用均拒绝。
- DTO 仅包含 ID/类型、时间、actor/member、organization、源/目标设备、动作、结果、连接方式、权限来源和 Relay 入/出字节。
- JSON 脱敏测试确认不出现 metadata map、network/session、质量/地址/错误自由文本、密码、token/code、私钥、候选地址、原始流量、用户内容、内部摘要或订阅 provider 数据。

## 套餐保留期

- 30/90/180 天：cutoff 为查询快照 UTC 时间减去精确天数；等于 cutoff 的记录保留，早于 cutoff 的记录过滤。
- `unlimited`：无 cutoff。
- `unavailable`：查询和清理均返回结构化 `audit_log_retention` 拒绝。
- `contract_custom`：只有 evaluator 已解析为正数有限天数时执行；未解析时安全拒绝。
- retention metadata 返回 requested/effective start/end、plan、mode、days、`truncated` 和 `range_empty`。整个请求区间早于保留期时返回明确空区间元数据，不伪装为普通“无日志”。

## 清理

Store 在同一锁内按跨审计事件/连接日志的全局最旧顺序执行受控批次删除，统计两类删除数、合计和 `has_more`。删除条件为目标 organization 且时间严格早于 cutoff；边界记录、其他 organization 记录和无归属历史安全记录保留。并发调用不会重复计数，重复运行删除数归零。有限保留、无限保留、不可用和未解析合同模式均与查询使用同一策略。

## 浏览器与无交互 UI 校验

按解阻指令停止 Browser MCP/node_repl，未等待授权，也未继续执行浏览器控制。仓库检索未发现 Playwright、Puppeteer 或 Selenium；本机没有可调用的 Chrome/Edge CLI。仅发现 Edge WebView2 运行时，但其 headless CLI 对本地页面未产生可用 DOM，因此无法完成真实桌面/窄屏截图、DOM 交互和控制台采集。

本次没有伪称完成实机浏览器检查。采用的等价无交互证据为：

- 本地 mock Hub + UI HTTP 集成测试实际调用组织审计查询和 cleanup，确认 30 天保留 metadata、查询结果和清理统计。
- 静态 UI 测试确认成员/设备/时间/连接方式/Relay 筛选、分页、保留期提示、空状态、清理确认与结果控件均存在。
- 响应式静态断言确认桌面三列筛选布局、720px 以下单列筛选/审计行、窄屏 shell 宽度限制、长 ID 自动换行和最小宽度约束。
- `node --check` 用于前端语法门禁；权限不足、组织暂停、保留期不可用、截断和保留期前空区间均有明确中文分支。

真实限制：本轮没有浏览器截图，也没有浏览器控制台结果；静态/HTTP 自动化不能替代最终人工视觉验收。

## 测试证据

开发阶段按先失败后实现执行了最小相关测试，包括：未定义查询类型的编译失败、UI 审计路由 404、DTO 自由文本泄露断言失败，以及“翻页时 cutoff 漂移导致边界记录消失”的失败测试。修复后对应测试均通过，未删除测试或降低断言。

| 命令 | 结果 |
| --- | --- |
| `go test -count=1 ./internal/cloudhub -run Task10C` | 通过 |
| `go test -count=1 ./internal/ui -run OfficialHubTeamManagementAPIAndStaticUI` | 通过 |
| `go test -count=1 ./internal/cloudhub ./internal/onboarding ./internal/ui` | 通过（cloudhub/onboarding/ui） |
| `go test -count=1 ./...` | 一次完整回归通过，所有包无失败 |
| `node --check - < internal/ui/static/app.js` | 通过；标准输入规避本机 Node 对父目录 `lstat` 的 EPERM，检查内容为当前 `app.js` |
| `git diff --check` | 通过；仅显示仓库既有 LF/CRLF 转换提示，无 whitespace error |
| `git diff --cached --check` | 未执行：沙箱拒绝创建 `.git/index.lock`，且按任务约束未申请额外权限 |

Go 测试仅把 `GOCACHE`、`APPDATA`、`LOCALAPPDATA` 临时定向到仓库 `.tmp`，并关闭遥测；没有申请额外系统权限、联网或安装依赖，也没有使用 `go/ast` overlay。测试结束后已删除 `.tmp`。

## 明确未做

- 未做 Task 10D 批量部署/版本管理或 Task 10E 私有化授权。
- 未改变角色、设备分组、连接授权或 P2P/Relay 数据面协议语义。
- 未接入真实日志仓库、SIEM、对象存储、外部分析服务或公网测试机。
- 未实现任意字段全文检索，未采集用户内容。
- 未改变支付 provider、套餐购买流程或订阅 provider 数据结构。
