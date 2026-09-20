# Task 10D 批量部署与版本 rollout QA 记录（2026-07-15）

## 范围与依据

- 基线：`c3f042d TASK 10C add team audit query and retention`
- 范围：只完成 Task 10D；未开展 Task 10E 私有化授权，也未开展 Task 11 发布流水线总闭环。
- 依据：实施计划 10.4、PTR-06、PTR-13、PTR-17、PTR-26、PTR-46 与阶段五门禁中批量部署/版本管理相关条目。
- 当前阶段五整体仍不能判定通过：Task 10E 企业私有化授权、真实隔离网络私有化包、离线授权与相关构建门禁不在本任务范围。

## 实现边界

| 能力 | 本次真实实现 | 明确未实现/未宣称 |
| --- | --- | --- |
| 部署模型与 Store | `DeploymentBundle`、`BootstrapCredential`、`Deployment`/`Rollout`、`DeploymentTarget` 稳定 ID/状态；MemoryStore organization scoped、`RWMutex` 并发安全；兑换在单次 Store 锁内原子完成凭据计数、设备注册、组织/分组绑定、额度校验和审计。 | 未新增磁盘数据库或外部持久化 provider。MemoryStore 重启不保留数据。 |
| 固定部署模板 | `windows/amd64`、`linux/amd64` 固定模板；一次创建响应返回脚本、最小配置、manifest、文件长度和 SHA-256；文件名、平台、架构、模板版本与 manifest 一致。严格 DTO 拒绝未知 command/download URL 字段。 | 未下载、打包或执行真实第三方二进制；未接 MDM、SCCM、Intune、Ansible、外部制品库或公网更新服务。 |
| 引导凭据 | 绑定 organization、可选 group、network、platform、architecture、过期时间和 max uses；服务端只存不可逆摘要；创建响应/生成脚本时出现一次。撤销、过期、超次数、错绑定、创建人 membership 失效、重复指纹和额度超限拒绝。 | 不把明文写入 bundle/credential 列表、状态、审计、日志或 `official-hub.json`；服务端不能重新显示明文，UI 后续刷新也会清除当前内存中的一次性文本与下载对象。 |
| 设备加入 | 复用官方 Hub `Device`、设备指纹、network、套餐 evaluator 与 `OrganizationDevice` 目录；兑换自动绑定 owner 账号、organization 与目标 group。 | 未在 10 台真实 Windows 主机运行安装器；未签发新的证书体系或改变 P2P/Relay 数据面协议。 |
| Rollout | 受控 `TrustedUpdateVersion` 目录，只接收 target version；organization/group/device 目标；每设备 pending/in_progress/succeeded/failed/canceled；heartbeat 返回 rollout/目标版本指派；fingerprint + 单调 sequence 上报；取消 pending；失败单设备重试；成功/陈旧状态不回退。 | 未接受任意下载 URL/命令；未连接公网更新服务；未远程执行 shell；未实现通用文件传输。 |
| 完整性与签名 | manifest 与 SHA-256 校验信息；`DeploymentManifestSigner` 仅作为未来签名设施接口边界。 | 当前没有生产代码签名密钥或服务，不能宣称部署脚本/包已进行生产签名。 |
| UI/onboarding | 官方 Hub 团队管理区可生成并下载本次响应的固定 artifacts、查看脱敏 bundle/credential 状态、撤销凭据、创建/查看/取消 rollout、查看逐设备状态和重试单个失败设备；明确中文错误。 | 未使用 Browser MCP；未执行真实浏览器截图/视觉验收。 |

## 安全与原子性验收

- owner/admin 允许；operator/member 在 service 和 UI 转发路径均明确 `forbidden`。
- 跨 organization bundle/group/device/rollout ID 不可读写；错误不开放跨组织资源探测。
- credential 摘要字段使用 `json:"-"` 且公开 DTO 再清空内部 digest/network；bundle/list/status/audit 不出现创建明文或摘要。
- wrong organization/group/platform/architecture、revoked、expired、exhausted、重复 device fingerprint 均在创建设备前拒绝。
- MemoryStore 在一把写锁内重新检查 organization、owner account/membership、network/group、credential 状态/uses、设备额度和 fingerprint；失败不增加 uses、不创建 Device、不创建 OrganizationDevice。
- 凭据创建人的账号与 owner/admin membership 在兑换时由 service 与原子 Store 路径再次校验；被停用、移除或降权的创建人不能继续授权新设备。
- rollout report 必须匹配已注册 fingerprint、target version 和受控状态/错误码。较小或重复 sequence 幂等忽略；`succeeded`/`canceled` 为不可回退终态。
- 审计白名单扩展 bundle、credential ID、rollout、target、device、group、current/target version；不开放任意 metadata、命令、自由文本错误、用户内容或内部摘要。

## 10 台本地模拟设备结果

- 自动化测试在同一进程内创建 team organization、owner network、目标 device group 和一个 `max_uses=11` 的短期 Windows/amd64 引导凭据。
- 10 个本地模拟设备使用不同受控设备名和 fingerprint 并发安全地依次兑换；每个都生成官方 Hub `Device` 与同 ID 指向的 `OrganizationDevice`，自动绑定目标 organization/group。
- organization device 列表最终为 10；credential uses 为 10；成功兑换审计为 10。
- team device quota 配置为 10 后，第 11 次兑换返回结构化 `device_count` quota error；uses 仍为 10，organization device 仍为 10，没有半注册设备。
- 这是 Store/service/API 的本地模拟验收，不代表执行了 10 台真实 Windows 安装，也不代表脚本通过生产代码签名。

## Rollout/重试语义结果

- 对上述目标 group 创建受控 `0.2.0` rollout，生成 10 个独立 pending target。
- rollout 创建会在同一 Store 锁内给每台设备写入 rollout ID/target version；heartbeat 能读取指派，活动 rollout 期间的无状态心跳不能越过状态机覆盖 current version。
- 一个设备按 sequence 1/2 报告 in_progress/succeeded 后，current/last known good 更新为 `0.2.0`；随后较旧 failed report 不会回退。
- 另一个设备报告 `apply_failed` 时 current/last known good 保留 `0.1.0`；管理员只重试该 target 后 attempt 由 1 变为 2、状态回到 pending，其他 target 不变。
- succeeded target 不能重试，已处于 target version 的设备不会被新的同版本 rollout 重复升级。
- 取消 rollout 只把 pending target 改为 canceled 并清除这些设备的指派；succeeded/failed/in_progress 保持原状态，in_progress 指派不被取消。

## 自动化与环境记录

- 开发阶段按 TDD 先观察缺少 Task 10D API/模型的编译失败，再实现 service/Store/API/client/onboarding/UI；安全边界测试覆盖 RBAC、IDOR、凭据摘要与一次显示、失效条件、并发兑换、额度原子性、固定模板/manifest/SHA-256、严格 DTO、rollout 状态机和审计。
- 既有 Task 10C 游标测试暴露 Raw URL Base64 非规范末位编码可偶发解码为相同 HMAC 字节；新增确定性失败测试，并在解码边界要求 re-encode 完全一致。相关两条测试连续 20 次通过。
- UI 使用 Go `httptest` 分别以 desktop/mobile User-Agent 请求实际 DOM，验证部署控件存在；HTTP 自动化完成生成、脱敏列表、兑换、rollout、失败单设备重试、取消、撤销和 operator 错误态。CSS 静态断言覆盖 `max-width: 720px` 单列布局和长 ID/凭据换行。
- 按约束未调用 Browser MCP。提交前检查 `chrome`、`msedge`、`chromium`、`firefox` 及其 `.exe` 命令均不可用，因此没有无交互真实浏览器可供截图、视觉或点击验收；上述 DOM/窄屏结论仅来自 HTTP/静态自动化，未伪称浏览器实测。
- 前端语法使用现有 Node 运行时执行 `node --check internal/ui/static/app.js`；未安装依赖。
- 提交前指定回归 `go test -count=1 ./internal/cloudhub ./internal/onboarding ./internal/ui ./internal/update` 通过：四个包均为 `ok`。
- 提交前唯一一次完整回归 `go test -count=1 ./...` 通过：所有有测试的 command/internal 包均为 `ok`，其余包如实显示 `[no test files]`；未出现 `go/ast` 环境故障，未使用临时 overlay。
- 前端语法检查 `node --check internal/ui/static/app.js` 通过；未修改 `scripts/build.ps1` 或 `scripts/package.ps1`，因此没有构建脚本测试模式需要运行。
- `git diff --check` 在最终文档更新后通过；仅出现仓库当前 Windows 工作树的 LF/CRLF 转换提示，没有空白错误。

## 未做内容

- 未做 Task 10E 私有化授权、企业合同、离线授权文件或支持 SLA。
- 未做 Task 11 发布流水线、真实制品发布、生产签名、MDM/SCCM/Intune/Ansible 或外部制品库。
- 未改变支付 provider、套餐定义、P2P/Relay 数据面协议；未使用公网测试机。
- 未实现任意远程 shell/命令执行、通用文件传输、通用代理、公网出口或全隧道。
