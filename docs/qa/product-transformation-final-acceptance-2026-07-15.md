# Meshlink 产品改造最终发布门禁与闭环审计

日期：2026-07-15（Asia/Shanghai）

## 结论

- 审计基线为 `main` 的 `519a5e8`（`TASK 11E add incident response and support triage`）；开始时工作区无未提交改动。
- Task 0-11 的仓库实现、自动化证据、历史 QA、发布脚本和限制已完成最终归档。当前非缓存全仓 Go 回归、PowerShell 脚本测试、开发模式构建、标准包、重复打包校验、发布包校验和企业私有包校验全部通过。
- 本次生成的是 `development`、`code_signed=false` 的 0.1.5 开发验收制品，不是生产发布包。未提供受控 Authenticode 证书、上一稳定版本回滚包或律师审核记录，正式模式按设计保持 fail-closed。
- 五阶段的自动化与本机工程门禁大体闭环，但每阶段要求的真实 Windows/RDP/NAT/隔离网手工证据均未全部具备。因此最终发布判断为：**工程收尾通过，正式/生产发布暂缓**。不得把本机回环、fake、模拟设备或历史公网 TCP 健康检查写成生产通过。
- 本轮未连接公网测试机，未执行真实用户/真实支付/真实订阅/生产环境测试，未新增产品方向或外部服务。

证据等级：`A` 为本次自动化/本机制品；`H` 为已有历史 QA；`M` 为门禁要求的当前真实环境手工证据。缺少 `M` 时结论保持暂缓。

## 权威证据索引

| 简称 | 证据 |
| --- | --- |
| T0 | [需求追踪矩阵](product-transformation-requirements.md)、[五阶段发布门禁](release-gates.md) |
| T1 | [Task 1 自托管闭环](task1-selfhosted-e2e-2026-07-07.md) |
| T2 | [配置安全测试](../../internal/config/config_test.go)、[设备管理测试](../../internal/onboarding/device_admin_test.go)、[禁用设备运行时测试](../../internal/agent/hub_test.go)；本报告补齐 Task 2 最终证据入口 |
| T3 | [Task 3 自建中继 E2E](task3-self-hosted-relay-e2e-2026-07-08.md) |
| T4 | [Task 4C Relay 会话](task4c-official-relay-session-skeleton-2026-07-08.md)、[Task 4D Relay runtime](task4d-official-relay-runtime-wiring-2026-07-09.md) |
| T5 | [Task 5A 策略/额度](task5a-official-hub-policy-quotas-2026-07-09.md)、[Task 5B 风控执行](task5b-official-hub-risk-enforcement-2026-07-09.md) |
| T6 | [Task 6A 控制面](task6a-p2p-control-plane-lan-direct-2026-07-09.md)、[6B LAN runtime](task6b-p2p-lan-direct-tcp-runtime-2026-07-09.md)、[6C public direct](task6c-p2p-public-direct-smoke-2026-07-09.md)、[6D Relay fallback](task6d-p2p-relay-automatic-fallback-2026-07-09.md) |
| T7 | [Task 7A NAT 模型](task7a-p2p-nat-probe-network-lab-2026-07-09.md)、[7B 传输选择](task7b-p2p-udp-quic-transport-selection-2026-07-09.md)、[7C 打洞状态机](task7c-p2p-hole-punch-state-machine-2026-07-09.md)、[7D 质量评分](task7d-p2p-connection-quality-scoring-2026-07-09.md)、[7E 多路径决策](task7e-p2p-multipath-hot-switch-decisions-2026-07-09.md) |
| T8 | [Task 8A 状态字段](task8a-connection-status-path-quality-fields-2026-07-09.md)、[8B 设备列表](task8b-device-list-connection-status-display-2026-07-09.md)、[8C 一键诊断](task8c-one-click-diagnostic-report-2026-07-09.md)、[8D Relay 提醒](task8d-relay-traffic-usage-reminders-2026-07-09.md) |
| T9 | [Task 9A 套餐](task9a-plan-quota-model-2026-07-10.md)、[9B 额度执行](task9b-plan-quota-enforcement-2026-07-10.md)、[9C Provider/幂等](task9c-subscription-provider-idempotency-2026-07-10.md)、[9D 客户端体验](task9d-subscription-quota-client-experience-2026-07-10.md) |
| T10 | [Task 10A 组织](task10a-organization-membership-foundation-2026-07-10.md)、[10B RBAC](task10b-rbac-device-group-connection-auth-2026-07-13.md)、[10C 审计保留](task10c-team-audit-query-retention-2026-07-14.md)、[10D 批量部署](task10d-bulk-deployment-rollout-2026-07-15.md)、[10E 私有部署](task10e-private-deployment-license-offline-update-2026-07-15.md) |
| T11 | [Task 11A E2E 矩阵](task11a-e2e-matrix-results-2026-07-15.md)、[11B 性能/成本](task11b-performance-cost-baseline-2026-07-15.md)、[11C 发布流水线](task11c-release-pipeline-2026-07-15.md)、[11D 监控告警](task11d-monitoring-alerts-2026-07-15.md)、[11E 事故演练](task11e-incident-support-drill-2026-07-15.md) |
| A-final | 本报告“最终执行记录”中的当前命令和产物 |

## PTR-01 至 PTR-48 映射

| ID | 要求摘要 | 当前证据 | 结论 |
| --- | --- | --- | --- |
| PTR-01 | 私有远程桌面组网定位 | [README](../../README.md)、T0、T1 | 通过（工程） |
| PTR-02 | 家庭/办公室/云服务器私网 | T1、T3、T11 | 部分；完整真实拓扑 RDP 的 M 待补 |
| PTR-03 | 设备列表点击 RDP | T1、T8、T11 | UI/诊断通过；真实 RDP M 待补 |
| PTR-04 | 自建中继或官方 Hub 支持无公网 IP | T3、T4、T6、T11 | 自建中继历史部署可用；官方 Hub 不同 NAT RDP M 待补 |
| PTR-05 | 高级用户自建完整控制面 | T3、T10、[私有部署手册](../ops/private-deployment.zh-CN.md) | 工程闭环；真实隔离网 M 待补 |
| PTR-06 | 小团队设备/审计/更新/权限 | T2、T10 | 工程闭环；真实团队 M 待补 |
| PTR-07 | 禁止匿名代理/出口/全隧道等 | T2、T4、T5、T9、A-final | 通过（强制边界） |
| PTR-08 | 首次 RDP 小于 5 分钟 | T1、T11 | 未满足 M；没有真实首次 RDP 计时 |
| PTR-09 | 无公网 IP 主流程隐藏网络细节 | T3、T4、T8、T11 | UI/部署工程证据通过；真实官方 Hub RDP M 待补 |
| PTR-10 | P2P 优先、Relay 必要兜底 | T6、T7、T8、T11 | 自动化通过；真实 direct 成功率/Relay 流量 M 待补 |
| PTR-11 | 个人远程连接与即开即用 | T1、T8、T9 | 工程闭环；真实用户流程 M 待补 |
| PTR-12 | 家庭多设备/邀请/管理 | T2、T4、T9、T10 | 额度与管理自动化通过；家庭 E2E M 待补 |
| PTR-13 | 团队准入/日志/私有化 | T2、T5、T10 | 工程闭环；真实团队/私有网 M 待补 |
| PTR-14 | 自托管自动 CA/证书/服务/邀请 | T1、T2、A-final | 临时目录与构建通过；真实服务/TUN/RDP M 待补 |
| PTR-15 | SSH 自动部署公网 Hub | T3、A-final | 历史真实部署/健康/双客户端登记通过；RDP/升级回滚 M 待补 |
| PTR-16 | 官方 Hub 账号/发现/身份/协商/Relay | T4、T5、T6、T9 | 本机 API/runtime 通过；生产认证与不同 NAT RDP 未通过 |
| PTR-17 | Hub 控制面职责 | T2、T4、T6、T7、T10、T11 | 工程闭环；当前 MemoryStore/受信身份头不构成生产控制面验收 |
| PTR-18 | 数据面 direct 优先、失败 Relay | T4、T6、T7、T8 | 自动化通过；真实 RDP 数据面 M 待补 |
| PTR-19 | 身份/权限/候选/direct/NAT/Relay 流程 | T4、T6、T7、T10 | fake/回环/历史公网 TCP 通过；真实 LAN/NAT/RDP M 待补 |
| PTR-20 | 仅展示用户可理解连接状态 | T1、T8 | 通过（自动化/UI） |
| PTR-21 | direct 失败 15 秒内自动 Relay | T6、T8、T11 | 本机 runtime 门禁通过；真实 RDP fallback M 待补 |
| PTR-22 | LAN/public direct 第一阶段 | T6、T11 | loopback LAN 与历史公网 TCP 有证据；真实双机 RDP M 待补 |
| PTR-23 | NAT 探测、UDP/QUIC、成功率 | T7、[网络实验室矩阵](network-lab-matrix.zh-CN.md)、T11 | 部分；模型/fake/UDP loopback 已有，真实 QUIC/复杂 NAT/成功率未完成 |
| PTR-24 | 多路径、质量评分、热切换 | T7、T8 | 决策/评分闭环；真实数据面热切换 M 未完成 |
| PTR-25 | Free/Personal/Family/Team/Enterprise | T9、T10 | 通过（套餐模型与客户端表达） |
| PTR-26 | 设备/在线/Relay/成员/审计/授权/SLA 维度 | T9、T10 | 工程闭环；无真实价格、合同或 SLA 上线 |
| PTR-27 | P2P/Relay 成本控制和提醒 | T5、T6、T8、T9、T11 | 额度/提醒/公式通过；真实云成本与 Relay 流量 M 待补 |
| PTR-28 | 可接受使用与封禁流程 | T5、[可接受使用政策草案](../compliance/acceptable-use-policy.zh-CN.md) | 技术执行通过；法律文本仍为草案 |
| PTR-29 | 拒绝全隧道/任意转发/匿名中继 | T2、T4、T5、T9、A-final | 通过（配置、API、Relay、包秘密扫描） |
| PTR-30 | 账号/绑定/吊销/限速/异常/投诉/封禁 | T4、T5、T9、T10 | 工程闭环；生产身份认证与运营流程未验收 |
| PTR-31 | 不查看内容，仅最小元数据 | T4、T5、T10、T11、[隐私草案](../compliance/privacy-minimal-metadata.zh-CN.md) | 通过（工程/文档）；正式隐私政策待律师审核 |
| PTR-32 | 非匿名账号、主体绑定、低额度、风控 | T5、T9、T10 | 部分；低额度/冻结已实现，真实手机/支付/企业主体绑定未实现 |
| PTR-33 | 商业上线前法律确认 | T5、T11 | 未满足外部前置；没有律师审核记录 |
| PTR-34 | 四入口首页 | T1、T3、T4、T8 | 通过（工程/UI） |
| PTR-35 | 设备列表状态/地址/路径/延迟/RDP/版本 | T1、T8 | 工程/UI 通过；真实状态截图 M 待补 |
| PTR-36 | RDP/复制 IP/详情/诊断/禁用/移除 | T1、T2、T8、T10 | 通过（工程/自动化）；真实设备操作 M 待补 |
| PTR-37 | 隐藏 NAT/证书/JSON 等底层术语 | T1、T8 | 通过（静态与浏览器历史证据） |
| PTR-38 | 本机服务/TUN/路由/防火墙/证书诊断 | T1、T8、T11 | 诊断模型/文案通过；真实故障设备 M 待补 |
| PTR-39 | 服务器监听/防火墙/公网/邀请诊断 | T3、T8 | 历史公网健康与错误分类通过；完整真实故障矩阵 M 待补 |
| PTR-40 | P2P 在线/direct/fallback/质量/流量诊断 | T6、T7、T8 | 工程闭环；真实丢包/NAT/RDP M 待补 |
| PTR-41 | 问题/影响/建议/动作统一输出 | T8、T11 | 通过（自动化） |
| PTR-42 | 阶段 0 自托管主流程稳定 | T1、T2、T11、A-final | 自动化/构建通过；真实服务/TUN/RDP/升级 M 待补 |
| PTR-43 | 阶段 1 自建中继 | T3、T8、T11 | 历史部署部分通过；两台 Windows RDP/重启恢复 M 待补 |
| PTR-44 | 阶段 2 官方 Hub MVP | T4、T5、T8、T11 | 本机 MVP 通过；不同 NAT RDP/生产后台 M 未通过 |
| PTR-45 | 阶段 3 P2P 优先/Relay 回退 | T6、T7、T8、T11 | 自动化通过；真实 LAN/public/RDP/fallback M 未通过 |
| PTR-46 | 阶段 4 团队/企业能力 | T9、T10、T11、A-final | 本机/模拟闭环；真实 10 设备和隔离网 M 未通过 |
| PTR-47 | Goal/Task 可追踪 | T0、本报告 Task 表 | 通过 |
| PTR-48 | 跨任务产品原则 | T1-T11、A-final | 工程边界通过；生产发布仍受本报告限制 |

## 排除项映射

| ID | 当前证据 | 结论 |
| --- | --- | --- |
| PTE-01 | T11 与本次包清单均未引入文件传输、剪贴板、终端或通用端口访问 | 保持排除；不宣传为完整远控套件 |
| PTE-02 | T2/T4/T5/T9 的配置、API、Relay 和套餐边界；A-final 秘密/凭据扫描 | 保持禁止匿名代理、公网出口、全隧道、公开转发和长期匿名中继 |
| PTE-03 | T6 第一阶段仅 LAN/public TCP；复杂 NAT 进入 T7 模型/实验室 | 阶段边界保持；真实复杂 NAT/QUIC 仍未完成 |
| PTE-04 | 合规文件均标注草案且非法律意见 | 保持排除；没有把工程文档冒充律师结论 |

## Task 0-11 闭环映射

| Task | 要求 | 当前证据 | 结论 |
| --- | --- | --- | --- |
| 0 | 冻结需求与五阶段门禁 | T0 | 通过 |
| 1 | 自托管远程桌面闭环 | T1、A-final | 工程闭环；真实 Windows RDP M 待补 |
| 2 | 设备管理、安全边界、吊销 | T2、A-final | 自动化闭环；无独立历史 QA 文件的缺口由本报告归档 |
| 3 | SSH 自建中继 | T3、A-final | 历史真实部署/加入通过；RDP/回滚 M 待补 |
| 4 | 官方 Hub/基础 Relay | T4、A-final | 本机控制面和嵌入式 Relay runtime 闭环；生产认证/TLS/真实 RDP 未闭环 |
| 5 | 风控、合规、成本 | T5、A-final | 技术门禁闭环；律师审核未闭环 |
| 6 | LAN/public direct/Relay fallback | T6、A-final | 自动化与历史公网 TCP 证据闭环；真实 RDP M 待补 |
| 7 | NAT/UDP/QUIC/质量/多路径 | T7、A-final | 模型、决策与回环闭环；真实 QUIC/复杂 NAT/热切换未闭环 |
| 8 | 状态可视化与诊断 | T8、A-final | 工程闭环；真实路径/RDP 展示 M 待补 |
| 9 | 套餐、额度、订阅适配 | T9、A-final | 工程闭环；真实支付/订阅明确未做 |
| 10 | 团队与企业 | T10、A-final | 本机/模拟闭环；真实 10 设备、客户隔离网、生产签名未闭环 |
| 11 | QA/性能/发布/监控/事故 | T11、A-final | 方法与本机工程闭环；真实网络/用户/生产运维证据未闭环 |

## 五阶段发布门禁逐项判定

| 阶段 | 类别 | 要求与当前证据 | 结论 |
| --- | --- | --- | --- |
| 一：自托管 | 自动化 | A-final 全仓回归；T1/T2/T8 对应包测试包含在内 | 通过 |
| 一：自托管 | 手工 E2E | T1 仅隔离目录与 UI；没有 5 分钟真实 RDP | 未通过 |
| 一：自托管 | 构建/打包 | A-final 标准开发包、manifest、ZIP、版本和哈希通过 | 开发门禁通过；非正式包 |
| 一：自托管 | 合规/风控 | README 定位、T2 全隧道/公网出口拒绝、设备禁用/审计 | 通过（工程） |
| 一：自托管 | 回滚 | T11C 更新/last-known-good 自动化通过；当前 rollback manifest 无上一稳定包 | 机制通过；正式回滚证据未提供 |
| 二：自建中继 | 自动化 | A-final 全仓回归覆盖 deployssh/onboarding/ui | 通过 |
| 二：自建中继 | 手工 E2E | T3 有历史 Linux VM、公网 health 和双客户端登记；无完整 Windows RDP/重启恢复 | 未通过 |
| 二：自建中继 | 构建/打包 | A-final 含 Linux agent、部署脚本与自建中继 runbook | 通过（开发包） |
| 二：自建中继 | 合规/风控 | T3 权限/脱敏；runbook 明确用户自控且非公开转发 | 通过（工程） |
| 二：自建中继 | 回滚 | T3/T11A 有自动回滚与失败采集证据；无本轮真实远端升级回滚 | 自动化通过，M 待补 |
| 三：官方 Hub | 自动化 | A-final 全仓回归覆盖 cloudhub 内策略、relay 和 ui | 通过 |
| 三：官方 Hub | 手工 E2E | T4/T5 为本机 HTTP/TCP；无两台不同 NAT Windows 官方账号 Relay RDP | 未通过 |
| 三：官方 Hub | 构建/打包 | `mesh-cloudhub.exe` 含 `-relay-listen` 嵌入式 Relay runtime；无独立 `mesh-relay`、数据库迁移包 | 部分通过；按生产门禁暂缓 |
| 三：官方 Hub | 合规/风控 | T5 执行通过，合规/隐私/协议为草案；无律师审核 | 工程通过，商业外放未通过 |
| 三：官方 Hub | 回滚 | 当前为 MemoryStore，无真实数据库迁移/备份回滚；正式上一版本包缺失 | 未通过生产门禁 |
| 四：P2P | 自动化 | A-final 全仓回归覆盖候选、状态机、direct、fallback、NAT、评分 | 通过 |
| 四：P2P | 手工 E2E | T6C 仅历史公网 TCP；无同 LAN/public RDP、15 秒真实 fallback | 未通过 |
| 四：P2P | 构建/打包 | 新旧协议代码均构建；开发包无正式回滚包，网络实验室真实结果仍待补 | 部分通过 |
| 四：P2P | 合规/风控 | 身份/RBAC/吊销/额度前置测试通过，无匿名/出口能力 | 通过（工程） |
| 四：P2P | 回滚 | fallback 禁用与旧协议兼容有自动化；无发布策略关闭 P2P 的真实回滚演练 | 部分通过，M 待补 |
| 五：团队/商业化 | 自动化 | A-final 全仓回归覆盖套餐、幂等、组织、RBAC、审计、部署、授权 | 通过 |
| 五：团队/商业化 | 手工 E2E | T10 为本机/模拟；无 10 台真实设备或真实隔离网 | 未通过 |
| 五：团队/商业化 | 构建/打包 | A-final 私有包校验通过但 `code_signed=false`；无生产授权/签名 | 开发门禁通过，生产门禁未通过 |
| 五：团队/商业化 | 合规/风控 | 额度/审计/最小元数据通过；合同、SLA、正式隐私/AUP 未完成 | 商业外放未通过 |
| 五：团队/商业化 | 回滚 | 授权/离线更新/last-known-good 自动化通过；MemoryStore/真实数据迁移和正式回滚包缺失 | 未通过生产门禁 |

依据 [发布判定](release-gates.md#发布判定)，任一类别未达标即暂缓，因此五阶段均不得据此报告宣称生产发布通过。

## 最终执行记录

| 命令 | 结果 |
| --- | --- |
| `powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-release-pipeline.ps1` | PASS；包含 PowerShell 语法、篡改哈希、重复打包差异、unsigned formal release、非法 rollback 等正负门禁 |
| `powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\benchmark.tests.ps1` | PASS |
| `powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\support-triage.tests.ps1` | PASS；6 个场景全部通过 |
| `powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\release.ps1 -Mode Development` | PASS；内部仅一次 `go test -count=1 ./...`，所有包 `ok` 或 `[no test files]`；随后构建、两次确定性打包并调用发布校验器 |
| `powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File .\scripts\package-private.ps1` | PASS；内部调用私有包校验器 |

本机 Go 为 `go1.26.1 windows/amd64`，标准库当前正常，本轮未使用 overlay。

## 产物、许可、隐私与敏感信息

| 产物 | 结果 |
| --- | --- |
| `release/meshlink-0.1.5.zip` | 35,434,224 bytes；SHA-256 `2a7f2228c95c0e74a48db85a4fc1c6ef8b2dead36a3271c71d1033ba25425c5f` |
| `dist/meshlink.zip` | 与 release ZIP 字节一致；重复打包 SHA-256 一致 |
| `release/manifest.json` | `meshlink-release-v1`，version `0.1.5`，mode `development`，`code_signed=false` |
| `release/checksums-0.1.5.sha256` | 发布包、manifest、release notes、rollback manifest 校验和均通过 |
| `release/rollback-manifest-0.1.5.json` | development 模式，`previous_version` 为空、`package=null`；不得作为正式回滚包 |
| `dist/private/meshlink-private-0.1.5.zip` | 29,488,371 bytes；SHA-256 `03d9b729a2b5f25fa538d05be35fb14b5d318c2839cf775b5c7c21d34d8e6fe9`；私有包校验通过 |

标准 ZIP 已确认包含 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`README.md`、`DEPLOY.zh-CN.md`、发布/自建中继 runbook、`VERSION` 和包内 manifest。发布校验器验证逐文件大小/SHA-256、版本、必需文件、重复/越界 ZIP 条目、签名语义及常见私钥/凭据；私有包校验器验证清单、文件哈希和秘密边界。

[LICENSE](../../LICENSE) 为项目专有许可；[THIRD_PARTY_NOTICES.md](../../THIRD_PARTY_NOTICES.md) 记录 Wintun 与 Go 依赖边界。隐私、可接受使用和协议文件仍明确标记为草案，不能替代正式法律文本。

## 已知限制、发布与回滚判断

- 未具备两台真实 Windows、可交互 RDP、不同 NAT/CGNAT/企业或校园网络、官方测试 Hub/Relay、10 台真实设备池或真实客户隔离网；未获授权连接公网测试机，因此没有执行这些 M 级测试。
- `internal/rdp` 当前为 `[no test files]`；诊断和 UI 自动化不能证明真实 RDP 会话。
- 官方 Hub 当前生产认证边界仍依赖受信身份头，Store 为内存实现；Relay runtime 集成于 `mesh-cloudhub`，且已有 QA 明确生产 TLS/mTLS、多实例、持久化尚未验收。
- UDP/QUIC 与复杂 NAT 主要是模型、fake network 和 UDP loopback；没有真实 QUIC 数据面、STUN/TURN、复杂 NAT 成功率或真实多路径热切换结果。
- 真实性能、RDP 首屏、direct 成功率、Relay 流量/成本和生产告警/事故恢复仍待真实环境；本机公式或 fake 演练不能替代。
- 正式商业上线仍缺律师审核、正式隐私/AUP/协议、生产合同/SLA、真实支付/订阅、生产代码签名和生产密钥流程。
- 当前开发包无 Authenticode 签名、无上一稳定回滚 ZIP。允许用于本地 QA；禁止用于正式/生产发布。正式发布必须以 `release.ps1 -Mode Release` 配置受控证书与已校验上一稳定包，完成对应 M 级门禁后重新生成并校验。
- 回滚触发条件沿用发布门禁：数据丢失、配置/证书损坏、安全边界失效、官方 Hub 成为公网出口、主 RDP 流程无法恢复或错误率越阈值时立即回滚。当前只证明 last-known-good 与 fail-closed 机制，未证明生产数据迁移或真实回滚演练。

## 明确未做

- 未新增外部 SaaS、监控平台、Dashboard、CI/CD 厂商、制品库、签名服务、支付上线、真实订阅、工单系统或客服平台。
- 未新增匿名代理、公网出口、全隧道、公开转发、任意公网端口转发、通用文件传输、远程 shell、剪贴板或终端能力。
- 未生成或提交生产/固定测试私钥、PFX/P12、真实授权、密码、长期 token、支付数据、客户数据或生产配置。
- 未把 development、fake、回环、本机模拟或历史网络证据描述为生产通过。
