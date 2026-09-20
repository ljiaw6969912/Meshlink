# Task 10B RBAC、设备分组与连接授权 QA

日期：2026-07-13
基线：`e09ce59 TASK 10A organization membership foundation`
目标提交：`TASK 10B enforce RBAC and device access`

## 范围与结论

- 在 `internal/cloudhub` 的 Organization/Membership 基础上增加集中式、默认拒绝的 RBAC，支持 `owner`、`admin`、`operator`、`member`。
- 增加组织设备、设备分组及设备级/分组级 connect grant；内存 Store 的创建、改名、删除、纳管、移组、授权和撤销都由锁保护。
- 在候选地址查询、P2P/direct 协商和 Relay session 创建的现有控制面入口前执行组织连接授权；拒绝发生在候选地址返回、候选对构造或 Relay session 创建之前。
- 对已纳入组织的目标设备默认拒绝连接；设备 grant 优先于分组 grant，撤销后下一次协商立即重新读取 Store 并拒绝，不使用授权缓存。
- 未纳入组织的目标设备保留既有同账号、同网络个人流程；跨账号或跨网络不会借此兼容分支放行。
- 官方 Hub 管理区提供成员角色、设备分组、设备纳管/移组和 connect grant/revoke 的最小操作闭环，并显示权限不足、组织暂停、成员状态和额度拒绝原因。
- 本任务只完成阶段五中 Task 10B 的 RBAC/设备分组/连接授权切片，不声明阶段五整体通过。

## 权限矩阵

| 能力 | owner | admin | operator | member |
| --- | --- | --- | --- | --- |
| 查看本组织成员、分组、组织设备和 grant | 允许 | 允许 | 允许 | 允许 |
| 暂停/恢复组织 | 允许 | 拒绝 | 拒绝 | 拒绝 |
| 邀请、暂停、恢复、移除非 owner 成员 | 允许 | 允许 | 拒绝 | 拒绝 |
| 修改非 owner 成员角色 | 允许 | 允许，但不能修改自己 | 拒绝 | 拒绝 |
| 授予、撤销或修改 owner | 允许，但必须保持 primary/最后 owner 不变量 | 拒绝 | 拒绝 | 拒绝 |
| 创建、改名、删除设备分组 | 允许 | 允许 | 拒绝 | 拒绝 |
| 纳管/移除组织设备、加入/移出分组 | 允许 | 允许 | 拒绝 | 拒绝 |
| 授予/撤销 connect grant | 允许 | 允许 | 拒绝 | 拒绝 |
| 连接已纳入组织的设备 | 仅显式 grant | 仅显式 grant | 仅显式 grant | 仅显式 grant |

补充不变量：

- primary owner 不可降级、暂停或移除；最后一个有效 owner 不可失去 owner 角色。
- admin 不能邀请或提升 owner，不能修改 owner，也不能降低自己的角色或停用自己的成员关系。
- operator/member 不能自提权；未知角色不继承只读权限，按默认拒绝处理。
- actor 账号、组织和 membership 必须存在且有效；组织外 actor 只得到稳定拒绝，不会通过错误消息获知 suspended 等组织状态。

## 连接授权顺序

1. 读取 source/target device 并拒绝已撤销设备。
2. 若 target 未纳入任何组织，仅允许 source 与 target 同账号且同网络的旧个人流程，权限来源记为 `legacy`。
3. 若 target 已纳入组织，验证组织为 active、组织设备记录与 target 账号一致、source/target 账号有效且双方 membership 均为 active。
4. 查找 source 账号到 target device 的显式设备 grant；命中时来源为 `device_grant`。
5. 未命中设备 grant 时，仅在 target 当前所属有效分组内查找 group grant；命中时来源为 `group_grant`。
6. 其余情况按 `deny` 拒绝。owner/admin 的管理角色不会自动产生连接权限。

分组删除会清空设备的分组关系和该分组 grant；组织设备移除会清理该设备 grant；成员移除会清理其 grant 和其名下组织设备。设备移组后旧分组 grant 不再作用于该设备。所有协商在同一服务操作锁内读取当前关系，撤销完成后不存在继续放行新协商的缓存窗口。

## 组织隔离、身份与公开数据边界

- Store 的 group、organization device 和 grant 查询/修改都带 `organization_id`；同一底层 device 最多属于一个组织，跨组织重复纳管返回稳定冲突。
- 组织 HTTP API 从 `X-Mesh-Actor-Account-ID` 读取 actor，并覆盖/忽略请求体中的 actor/owner/account 声明；测试覆盖了伪造请求体不能提升权限。
- 该请求头是现有本地 Hub/上游可信身份元数据边界，不等同于生产级会话认证。当前仓库尚未提供完整的用户会话或设备证明，因此不能声称生产认证已经完成。service 层仍会重新校验账号状态、membership、角色、组织和资源归属，不能只靠请求体账号 ID 绕过。
- P2P/Relay 旧入口仍以 source device 与请求账号/网络归属作为现有身份边界；本任务只增加组织授权门禁，没有新增设备凭据协议。
- 团队公开响应只包含组织、membership、分组、组织设备和 grant 的必要字段；测试拒绝内部摘要、digest、provider 数据、风控字段、远端地址及其他组织数据。

## 审计

- 角色、分组、组织设备、grant 和连接授权/拒绝都写入现有 AuditEvent Store。
- 连接事件记录 actor/member account、organization、source device、target device、group、动作、结果和权限来源。
- 管理事件权限来源为 `owner` 或 `admin`；连接决策为 `device_grant`、`group_grant` 或 `deny`，旧个人兼容分支为 `legacy`。
- 审计清洗排除 token/code、凭据、私钥、用户内容、候选地址、远端地址和原始流量。本任务没有新增 10C 审计查询 API 或保留期清理。

## 自动化与手工证据

- 先写失败测试：最初因 RBAC/分组/grant 类型和方法缺失而失败，UI 测试因团队状态/路由缺失而失败；随后按测试实现。
- 自审新增的失败回归覆盖：未知角色默认拒绝、跨组织 actor 不得从错误消息获知 suspended 状态、组织暂停原因必须先于通用 forbidden 显示、UI 不得自行指定 Team 套餐。
- `go test -count=1 ./internal/cloudhub ./internal/ui`：通过。
- `go test -count=1 ./internal/p2p ./internal/relay ./internal/onboarding`：通过。
- `go test -race -count=1 ./internal/cloudhub`：本机 Go 报告 `-race requires cgo`，当前环境未启用 CGO，因此未形成 race 运行结果；未安装额外工具或修改机器环境。16 路并发同名分组、16 路并发幂等 grant 及撤销后新协商拒绝仍由普通 Go 测试执行。
- `go test -count=1 ./...`：通过。
- `node --check - < internal/ui/static/app.js`：通过。
- `git diff --check`：通过。
- 暂存后 `git diff --cached --check`：通过。

测试覆盖角色矩阵、角色变更、primary/最后 owner、自提权、跨组织 IDOR、分组 CRUD、设备归属、grant/revoke、删除清理、组织/成员暂停、成员移除、设备撤销、并发同名分组和并发幂等 grant、HTTP/client 脱敏、候选查询/P2P/Relay 的未授权拒绝—授权放行—撤销拒绝、个人旧流程和审计权限来源。

浏览器使用本机回环服务和预置的合法 Team 账号完成实际检查：

- 桌面 `1280x900`：五个团队管理卡片无重叠，页面无横向溢出。
- 窄屏 `390x844`：卡片单列、无重叠、无横向溢出，关键按钮可见。
- 实际完成创建组织、创建分组、设备纳管/入组、分组 connect grant、撤销、owner 降级拒绝；撤销后 grant 列表立即清空并显示“新协商将立即拒绝”。
- Codex 内置浏览器记录了无来源 URL 的 `nodeName.toLowerCase` 运行时诊断；仓库静态代码不存在该调用，页面日志中没有来自 `127.0.0.1` 应用脚本的 error/warn。以 Node 语法检查、静态测试和实际操作结果交叉验证，按浏览器运行时噪声记录，不将其伪报为应用错误。

本机未使用 Go overlay，测试缓存和浏览器夹具均放在仓库临时 `.tmp` 下并在提交前删除。

## 明确未做

- 未做 Task 10C 的审计查询、分页、保留期或清理。
- 未做 Task 10D 的批量部署、版本管理或更新编排。
- 未做 Task 10E 的私有化授权、授权文件或企业离线能力。
- 未修改支付 provider、套餐定义或订阅回调；UI 不允许自行选择 Team 套餐。
- 未重写 P2P/Relay 数据面协议，未增加匿名代理、公网出口、全隧道、公开转发或文件传输。
- 未使用公网测试机，未执行 push。
