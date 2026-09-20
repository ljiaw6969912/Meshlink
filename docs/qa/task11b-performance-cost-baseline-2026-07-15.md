# Task 11B 性能与成本基准报告（2026-07-15）

## 结论

Task 11.2 的可重复方法和机器可读输出已建立。本机实测覆盖现有 `mesh-agent` 的本地 TCP 建连与 CPU/Working Set；真实 RDP 首屏、不同 NAT 的 direct 成功率、真实 Relay 流量和显式云成本仍为发布前待测。本文没有用模拟测试或公式夹具替代这些真实结果。

永久方法见 `docs/qa/performance-benchmark.zh-CN.md`。采集入口为 `scripts/benchmark.ps1`，示例配置为 `scripts/benchmark.config.example.json`，零依赖测试为 `scripts/benchmark.tests.ps1`。

## 本机/模拟基准环境

| 字段 | 值 |
| --- | --- |
| 证据等级 | `local_simulated` |
| 基准 ID | `task11b-local-2026-07-15` |
| 采集仓库版本 / revision | `0.1.5` / `46aa7023b6d5` |
| 运行中 `mesh-agent` 制品版本 | 未从运行时验证；本机结果不得据此归因到上述源码 revision |
| OS / 架构 | Microsoft Windows NT 10.0.28000.0 / AMD64 |
| CPU | Intel Core i3-10100 3.60 GHz，8 个逻辑处理器 |
| 物理内存 | 34,190,012,416 bytes（约 31.842 GiB） |
| 目标进程 | 1 个现有 `mesh-agent` |
| 场景 | 同一 Windows 上，经私有本地网卡连接现有 `mesh-agent` TCP 监听；没有使用公网测试机 |
| 方法 | 预热 3 次，正式 20 次；资源间隔 200 ms；nearest-rank |

本地 TCP 指标只表示 TCP 握手，不是完整产品建连，不参与真实发布体验结论。目标地址未写入结果或本文。

## 实际测量结果

生成的临时 JSON 通过 `task11b.performance-result.v1` schema 写出并在读取后删除；报告保留同字段摘要：

| 指标 | 状态 | n | min | p50 | p95 | p99 | max | mean |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 本地 TCP 建连（ms） | `measured`，20/20 成功 | 20 | 0.2173 | 0.2727 | 0.4681 | 0.6210 | 0.6210 | 0.313715 |
| `mesh-agent` CPU（%） | `measured` | 20 | 0 | 0 | 0 | 0 | 0 | 0 |
| `mesh-agent` Working Set（bytes） | `measured` | 20 | 38,932,480 | 38,932,480 | 38,932,480 | 38,932,480 | 38,932,480 | 38,932,480 |

Working Set 为 37.128906 MiB。服务进程常规 CPU 总时间不可读时，脚本使用 Windows 格式化进程计数器回退；该空闲短窗口的计数器分辨率结果为 0%，只表示本次窗口未观测到可量化 CPU 使用，不表示进程始终不消耗 CPU。

## 待真实环境项

| 指标 | 当前状态 | 缺少的证据 | 发布前动作 |
| --- | --- | --- | --- |
| 完整产品连接建立耗时 | `pending_real_environment` | 两台 Windows、不同 NAT 下从连接请求至最终路径已连接的 30 个正式样本 | 按场景预热 5 次，在配置填入 `connection_establishment_ms` |
| RDP 首屏耗时 | `pending_real_environment` | 真实目标机 RDP、统一第一帧判定的 30 个正式样本 | 不截图/录屏，只记录毫秒数到 `rdp_first_frame_ms` |
| direct 成功率 | `pending_real_environment` | 每个网络/NAT 场景 100 次最终 `path_type` | 填入 `direct_path_types`，Relay 和失败保留在分母 |
| Relay 流量 | `pending_real_environment` | 固定 RDP 负载窗口前后 `relay_bytes_in/out` 差值 | 分别执行 idle 与 UI 负载，每档至少 3 个完整窗口 |
| Relay 带宽成本 | `pending_explicit_cost_input` | 实际区域、三字母币种、合同/账单每 GiB 单价，以及上述真实 Relay 流量 | 只填当次显式输入并确认双向计费假设；不使用市场估价 |
| 负载中的客户端资源 | 当前仅空闲本机短窗口 | 与真实 RDP/Relay 固定负载同步的 30 个 1 秒样本 | 在源 Windows 的固定负载时间段运行脚本 |

这些待测项使当前报告不能充当“官方 Hub MVP 发布前真实基准”；它是本机可执行性和字段基线。真实场景完成后，应按永久方法中的同一 schema 与跨版本表追加独立报告，不覆盖本次证据等级。

## 成本公式验证（非真实云成本）

自动测试仅用固定夹具验证公式：`1 GiB in + 0.5 GiB out`，显式测试单价 `0.25 USD/GiB`、区域 `test-region`，得到 `1.5 GiB × 0.25 = 0.375 USD`。这些数字只证明换算和输入透传正确，不是供应商报价、实际账单或本次 Relay 成本。

脚本不含默认单价；没有 `price_per_gib + region + currency` 时状态为 `pending_explicit_cost_input`，有价格但无 Relay 字节时为 `pending_relay_traffic`。

## 隐私、范围与复用依据

- 复用现有 `internal/p2p` 状态字段 `path_type`、`latency_ms`、`relay_bytes_in/out` 及其质量汇总测试；未修改任何产品代码。
- 脚本只读取显式配置、本机进程计数器、系统硬件摘要和显式 TCP 目标；不读取 RDP 画面、用户文件、剪贴板、进程参数或凭据，不上传结果。
- 未安装依赖，未调用 Browser MCP，未使用公网测试机，未新增监控、Dashboard、告警、CI、构建、打包、发布或更新逻辑。
- 所有仓库改动均限制在两份指定文档和 `scripts/benchmark*` 专用文件；未暂存、未提交。

## 验证记录

交付前按以下范围执行，不重复全仓回归：

| 验证 | 命令 | 结果 |
| --- | --- | --- |
| PowerShell 语法解析 | PowerShell Parser 解析 `scripts/benchmark.ps1` 与 `scripts/benchmark.tests.ps1` | PASS，两个文件均为 0 个语法错误 |
| PowerShell 参数、schema、分位数、成本公式、安全边界 | `powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\benchmark.tests.ps1` | PASS，`benchmark.tests.ps1: PASS` |
| 最小相关 P2P Go 测试 | `go test -count=1 ./internal/p2p -run 'Test(ConnectionQuality|NormalizeConnectionStatus)'` | PASS，`ok meshlink/internal/p2p` |
| 最小相关 Cloud Hub 汇总 Go 测试 | `go test -count=1 ./internal/cloudhub -run 'Test(AccountManagementSummarySeparatesDirectAndRelayConnectionQuality|ConnectionLogRecordsMultipathSwitchDecisionSummary)'` | PASS，`ok meshlink/internal/cloudhub` |
| 补丁空白检查 | `git diff --check` | PASS（退出码 0）；共享工作区中并行任务文件有 LF/CRLF 提示，但没有 whitespace error |

建议提交说明：`TASK 11B add performance and cost baseline`。
