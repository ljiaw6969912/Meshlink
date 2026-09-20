# 团队管理员运维手册

企业隔离网络、签名授权和离线更新操作见 [企业私有部署运维手册](private-deployment.zh-CN.md)。

## 组织审计管理

### 角色与状态边界

- 只有状态为 `active` 的 organization 中，状态为 `active` 的 `owner` 或 `admin` 可以查询全组织审计，也可以手动触发该组织的过期记录清理。
- `operator`、`member`、已暂停或已移除的成员不能查询全组织审计，也不能清理。
- actor 账号被冻结或封禁、organization 处于 `suspended`、或组织 owner 账号不可用时，查询和清理均拒绝。
- UI 和 HTTP API 只是入口；Cloud Hub service 会重新校验账号、成员关系、角色、组织状态和资源归属。不得把前端按钮隐藏当作授权控制。
- 系统定时维护使用同一套 service 保留期与 Store 删除策略。系统入口不通过公共 HTTP 暴露，也不能绕过组织状态、owner 套餐或保留期解析。

### 查询与筛选语义

组织审计查询合并两类记录：带有明确 `organization_id` 的组织/RBAC/设备分组/授权事件，以及在产生时由 Cloud Hub 根据目标组织设备建立可靠归属的连接日志。历史上没有组织归属的记录不会按设备或账号猜测归属，也不会混入组织结果。

可组合使用以下筛选条件：

- 成员账号：同时匹配记录中的 actor 或 member。
- 源设备与目标设备：按完整设备 ID 精确匹配。跨组织 ID 只会得到当前组织内的空结果，不返回“设备存在/不存在”的差异信息。
- 开始与结束时间：均为包含边界；API 使用 RFC 3339 时间，UI 的本地时间会转换为 UTC。
- 连接方式：`lan_direct`、`public_direct` 或 `relay` 精确匹配。
- Relay 流量：可要求只看 Relay 字节大于零的记录，并可设置入站与出站字节合计的最小值。

结果默认按时间倒序；同一时间使用记录类型和记录 ID 稳定排序。分页是 keyset 分页，不使用可漂移的 offset。游标带签名并绑定 organization、完整筛选条件、页大小和查询时间快照；修改游标、跨组织复用或改变筛选后继续使用旧游标都会被拒绝。单页最大 100 条。

公开审计 DTO 只包含记录 ID/类型、时间、actor/member、organization、源/目标设备、动作、结果、连接方式、权限来源和 Relay 入/出字节。不会返回密码、token/code、凭据、私钥、候选或远端地址、原始流量、连接错误自由文本、剪贴板/远程桌面/文件内容、内部摘要、订阅 provider 数据或任意 metadata map。

### 套餐保留期

保留期始终取 organization owner 的当前有效套餐，并复用 Task 9 的 quota mode/evaluator：

- `limited`：按配置的天数计算精确 UTC cutoff，例如 30/90/180 天；时间等于 cutoff 的记录保留，早于 cutoff 的记录不可查询。
- `unlimited`：不应用时间 cutoff，保留并查询全部已归属记录。
- `unavailable`：拒绝组织审计查询和清理，返回结构化 `audit_log_retention` 配额原因。
- `contract_custom`：只有合同天数已明确解析时才按有限天数执行；未解析时安全拒绝。

每次查询都返回 retention metadata：请求起止、实际有效起止、套餐 ID、mode、保留天数、是否被截断，以及请求区间是否整体落在保留期以前。若请求开始早于 cutoff，服务只返回 cutoff 及之后的数据，并设置 `truncated=true`；若整个请求区间都早于 cutoff，还会设置 `range_empty=true`，避免把套餐限制伪装成“没有日志”。

### 清理操作

1. 确认 organization、owner 当前套餐和 cutoff 正确，确认没有组织暂停、账号冻结/封禁或成员状态异常。
2. 在执行清理前完成适用环境的数据快照或备份，并记录备份时间、组织 ID、套餐和预期 cutoff。备份介质不得包含导出的密码、token、私钥或用户内容。
3. 在团队管理页“组织审计”区域设置单批上限，点击“清理过期记录”，阅读确认提示后执行。API 调用方使用组织审计 cleanup 端点并传入受控 `batch_size`；最大批次为 1000。
4. 检查返回的 `deleted_audit_events`、`deleted_connection_logs`、`deleted_total` 和 `has_more`。`has_more=true` 时按相同策略继续下一批。
5. 清理只删除目标 organization 中时间严格早于 cutoff 的组织审计和连接日志。cutoff 边界、其他组织记录、以及没有组织归属的历史安全记录都会保留。
6. 重复执行是幂等的；当没有更多过期记录时删除数为 0。并发调用由 service 与 Store 锁保护，不会把同一记录重复计数。

清理是数据删除操作，不能通过应用层“撤销”。需要回滚时，应停止后续清理、保留当前现场，使用清理前的受控备份恢复到独立环境核对，再按组织和时间范围执行恢复。不要用覆盖整个数据库的方式恢复单个组织；这样可能回滚其他组织的成员、授权或审计数据。当前仓库使用内存 Store 作为实现与测试闭环，接入持久化 Store 前必须另外验证事务、备份、恢复演练和迁移回滚。

### 隐私边界

组织审计用于账号安全、权限追踪、连接路径与 Relay 计量、客服排查和合规响应所需的最小元数据。Cloud Hub 不采集、解密或查看远程桌面画面、文件内容、剪贴板内容、键盘输入或原始连接流量；也不允许用任意字段全文检索间接建立用户内容索引。若排障必须查看超出公开 DTO 的内部数据，应按独立的安全事件流程审批，不能扩展本查询接口绕过最小化原则。

## 批量部署与版本 rollout

### 部署前置条件

- organization、organization owner 账号和操作人的 membership 都必须为 `active`。只有 `owner`、`admin` 可生成部署包、撤销引导凭据、创建/查看/取消 rollout 或重试失败设备；`operator`、`member` 在 UI、HTTP API 和 Cloud Hub service 层都会被拒绝。
- organization owner 必须有当前有效的官方 Hub 套餐和可解析的设备数额度。批量引导沿用官方 Hub 的设备注册表、设备身份、套餐 evaluator 和组织设备目录，不建立第二套旁路身份。
- 生成部署包前先创建 owner 名下的官方 Hub network；如需自动入组，先创建目标 device group。group、network 与 organization 必须属于同一受控边界。
- 当前固定部署模板只支持仓库已有产物对应的 `windows/amd64` 和 `linux/amd64`。architecture 不接受别名；platform、group、organization 均精确绑定。
- 被部署设备应先有本机的官方 Hub 配置。固定脚本只从受控本机配置读取 Hub 地址，并要求 HTTPS；生成接口不接受 Hub 下载 URL、shell、命令、脚本片段、环境变量名或用户模板。

### 固定模板与引导凭据边界

1. 在官方 Hub 团队管理区选择 organization、可选 group、平台、`amd64`、1 至 1440 分钟有效期和 1 至 1000 次最大使用次数，生成部署包。
2. 创建响应一次性返回引导凭据，以及固定安装脚本、最小 bootstrap 配置和 manifest。每个文件都有 SHA-256；manifest 的 bundle、credential、organization、group、platform、architecture、模板版本、文件名、长度与校验和必须一致。
3. 服务端只保存凭据 SHA-256 摘要。明文不会进入 bundle 列表、凭据状态、rollout、审计、日志或本地 `official-hub.json`。刷新页面后服务端不能恢复明文；若遗失，应撤销旧凭据并生成新包。
4. 脚本中只包含短期引导凭据，不包含账号密码、长期 token、私钥、真实公网地址或代码签名密钥。不要把生成响应、脚本或凭据粘贴到工单、聊天日志或审计备注。
5. 当前仓库没有生产代码签名设施。Task 10D 只提供 `DeploymentManifestSigner` 接口边界与 SHA-256 完整性校验；不得把这些包描述为“已进行生产代码签名”。

### 批量安装和兑换

1. 在受控渠道把同一次创建响应中的脚本、配置和 manifest 交付到目标设备；先核对 manifest 文件名、模板版本、长度和 SHA-256。
2. 每台设备提交 credential、绑定的 organization/group/platform/architecture、受控设备名、设备指纹和当前版本。错 organization、错 group、错平台/架构、缺少或重复设备身份均拒绝。
3. Cloud Hub 在一次原子 Store 操作中重新校验 organization、owner account、owner membership、凭据创建人的 active owner/admin membership、network、group、凭据撤销/过期/用量、设备指纹和设备额度，然后同时增加凭据用量、创建官方 Hub Device、创建 OrganizationDevice 并写兑换审计。创建人被停用、移出组织或降权后，旧凭据不能继续兑换。
4. 任一校验失败都不会增加凭据用量、创建半注册 Device、留下未分组 OrganizationDevice 或占用设备额度。并发兑换在同一 Store 锁内计数，不会突破 max uses 或套餐 device count。
5. 批量完成后，在组织设备列表核对设备数和 group；在组织审计中按 `redeem_bootstrap_credential` 检查 bundle、credential ID、device、group、platform、architecture、结果和时间。审计只记录 credential ID，不记录明文或摘要。

### 版本 rollout

- rollout 请求只接受受控目标版本字符串。Cloud Hub 从 `TrustedUpdateVersion` 目录解析 update manifest 来源、版本一致的 `meshlink-<version>.zip` 文件名和可选 SHA-256；请求不能携带下载 URL、命令或脚本。
- 可选择一个 organization device group、一组明确的 organization device ID，或 organization 中全部设备。group 与 device ID 不能在同一请求混用；跨组织 ID 返回安全拒绝。
- 创建后每台设备有独立 target，初始为 `pending`，并在设备 heartbeat 响应中得到 rollout ID 与目标版本。活动 rollout 期间，不带 rollout 状态的普通 heartbeat 不能覆盖当前版本。设备以已注册 fingerprint 上报单调递增 sequence，可进入 `in_progress`、`succeeded` 或 `failed`。成功必须报告目标版本；失败只接受受控错误码 `download_failed`、`checksum_failed`、`apply_failed` 或 `health_check_failed`。
- 成功设备更新 `current_version`/last known good；失败设备保留原 `current_version` 和 last known good。重复、陈旧或越序报告不会把 `succeeded`/`canceled` 回退。
- 取消只把尚未开始的 `pending` target 改为 `canceled` 并清除这些设备的 rollout 指派。`in_progress`、`succeeded`、`failed` 保持原状态；成功后也会清除已完成指派。已经处于目标版本的设备不会被新 rollout 重复纳入。

### 失败重试与回滚

1. 在 rollout 逐设备状态中确认 `error_code`、current/target version、attempt 和最后上报序号。不要把自由文本命令、日志内容或用户内容写入错误字段。
2. 只对 `failed` target 点击“仅重试此设备”。服务端把这一 target 置回 `pending`、attempt 加一并清除本次错误；成功设备和其他 pending/failed 设备不变。`succeeded` 或已取消 rollout 不能重试。
3. 重试仍失败时，先停止扩大 rollout，核对 update manifest、包文件名/SHA-256、设备当前版本和健康检查。失败不会覆盖 last known good。
4. 需要版本回滚时，把经现有 update manifest 信任边界登记的上一个可靠版本作为新的受控 rollout 目标。未进入受控版本目录的本地文件、临时 URL 或命令不能作为回滚内容。当前 Task 10D 不实现任意远程 shell，也不自动替换本机 last known good。

### 撤销与排障

- 怀疑凭据泄露、设备范围填错或交付结束后，立即按 credential ID 撤销。撤销幂等；此后所有兑换拒绝，已注册设备不会被隐式删除。
- `expired` 表示服务端时间已达到有效期；`exhausted` 表示 uses 已达到 max uses；`revoked` 表示管理员撤销。列表只显示状态、次数和时间，不显示明文或摘要。
- `quota_exceeded` 应按明确的设备额度问题处理，不要伪装成网络错误。先核对 owner 当前套餐和现有未撤销设备，再决定释放设备或调整正式套餐配置。
- `binding_mismatch` 应核对 organization/group/platform/architecture，不能通过修改脚本绕过。`identity_mismatch` 应核对设备注册 fingerprint；不要仅凭 device ID 手工伪造 heartbeat/status report。
- rollout 版本不受支持时，应先通过现有 update manifest/受控版本目录发布并校验，而不是向 rollout 请求加入 URL 或命令。
- 所有状态变更进入组织审计，公开字段包括 actor、organization、bundle/credential/rollout/target/device/group、action、result、current/target version 和时间；不记录引导明文、内部摘要、命令内容、私钥、用户内容或 provider 内部数据。
