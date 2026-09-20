# Task 9A 套餐目录与额度模型 QA

日期：2026-07-10

## 范围

- 依据 `PRODUCT_TRANSFORMATION_PLAN.zh-CN.md` 9.1、9.2、Goal 9，以及 `docs/qa/product-transformation-requirements.md` 的 PTR-25、PTR-26、PTR-27。
- 本任务只建立套餐目录、额度语义、公开只读查询 API/client，以及到现有 `AccountPolicy` Relay 字节字段的适配边界。
- 未实现额度强制执行、降级策略、订阅 provider、支付、checkout、webhook、退款、发票、税务、客户端升级入口或套餐购买 UI。

## 稳定套餐 ID

| ID | 显示名 | 顺序 | 定位 |
| --- | --- | ---: | --- |
| `free` | Free | 10 | 自建服务器、自建中继、基础设备互联 |
| `personal` | Personal | 20 | 个人官方 Hub 账号 |
| `family` | Family | 30 | 家庭设备与家庭成员设备管理 |
| `team` | Team | 40 | 团队成员、审计与管理能力 |
| `enterprise` | Enterprise | 50 | 私有部署、专属授权、支持与定制 |

## 额度语义

`PlanQuota.Mode` 明确区分四种状态，不使用 `0` 或 `-1` 表示无限、不可用或定制：

| mode | 语义 |
| --- | --- |
| `unavailable` | 该套餐不可用该能力或额度 |
| `limited` | 有限额度；可以是内置正整数，也可以由运营配置注入 |
| `unlimited` | 不按该维度限制 |
| `contract_custom` | 按合同或私有化授权定制 |

`limited` 的数值如果已由产品文档明确，则写入正整数；如果未明确，则使用 `source=operator_configured` 和内部 `ConfigKey`，公开 API 只暴露“运营配置”语义，不暴露内部配置键。`contract_custom` 不携带数值。

## 套餐矩阵

| 套餐 | 设备数 | 同时在线设备 | 官方 Relay 流量 | 成员数 | 审计日志保留 | 管理能力 | 私有部署 | 支持 SLA |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `free` | `unlimited`，仅自建/基础互联边界 | `unlimited`，仅自建/基础互联边界 | `unavailable` | `unavailable` | `unavailable` | 家庭/团队管理不可用 | `unavailable` | `unavailable` |
| `personal` | `limited=3` | `limited`，运营配置 | `limited`，运营配置 | `unavailable` | `limited`，运营配置 | 家庭/团队管理不可用 | `unavailable` | `unavailable` |
| `family` | `limited=10` | `limited`，运营配置 | `limited`，运营配置 | `limited`，运营配置 | `limited`，运营配置 | 家庭管理可用，团队管理不可用 | `unavailable` | `unavailable` |
| `team` | `limited`，运营配置 | `limited`，运营配置 | `limited`，运营配置 | `limited`，运营配置 | `limited`，运营配置 | 团队管理可用 | `unavailable` | `contract_custom` |
| `enterprise` | `contract_custom` | `contract_custom` | `contract_custom` | `contract_custom` | `contract_custom` | 团队能力按合同定制 | `contract_custom` | `contract_custom` |

产品文档明确的商业数值只有个人版设备上限 3、家庭版设备上限 10。其他未明确数值均未在代码中伪装成价格、正式额度或承诺。

## 产品边界

- `free` 保留自建服务器、自建中继、基础设备互联免费心智。
- 官方 Hub 套餐的 `boundaries.disallowed_uses` 固定包含：
  - `anonymous_proxy`
  - `public_internet_egress`
  - `full_tunnel`
  - `arbitrary_traffic_forwarding`
- 套餐目录不表示匿名代理、公网出口、全隧道或任意流量转发能力。

## AccountPolicy 兼容性

- `DefaultAccountPolicy`、`WithAccountPolicies`、现有 `/api/accounts/{id}/policy` 行为保持不变。
- 新增 `AccountPolicyMappingForPlan` 只在套餐 `official_relay_traffic` 为内置正整数 `limited` 时，映射到旧 `AccountPolicy.RelayBytesQuota`。
- 设备数、同时在线设备数、成员数、审计日志保留、家庭/团队管理、私有部署、支持 SLA 不映射到旧 `AccountPolicy` 的 Relay 会话字段。
- 9B 应直接消费 `PlanQuota`/`PlanCapability` 语义执行设备、在线、Relay、成员和日志限制；不能把“同时在线设备数”等同于旧 `MaxActiveRelaySessions`。

## 公开 API

- `GET /api/plans`：返回排序稳定的套餐/权益元数据。
- `GET /api/plans/{id}`：返回单个套餐元数据；未知套餐返回 404。
- API 不返回价格、币种、订单、支付、checkout、凭据、token、内部风控或敏感信息。

## 测试结果

- `go test -count=1 ./internal/cloudhub`：通过。
- `go test -count=1 ./...`：通过。
- `git diff --check`：通过；仅提示 Windows 工作区下一次 Git 触碰 `internal/cloudhub/client.go`、`internal/cloudhub/server.go` 时会做 LF/CRLF 转换。
- `git diff --cached --check`：通过。

## 本机 Go overlay

本次单包和全仓 Go 测试均未触发本机 `go/ast` 的 `tgken/token` 损坏问题，未使用临时 overlay。`C:\Users\ADMINI~1\AppData\Local\Temp\codex-go-ast-overlay\overlay.json` 未写入仓库。

## 明确未做

- 未做真实价格、币种、订单、支付、退款、发票、税务。
- 未做订阅 provider、checkout、webhook 或回调幂等。
- 未强制执行设备、在线、成员或日志额度。
- 未做购买页、升级按钮或其他 UI。
- 未修改 RDP 数据桥、P2P/Relay 数据面，未使用公网测试机。
- 未写入密码、token、私钥或真实公网信息。
