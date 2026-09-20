# 性能与成本基准方法

本文定义 Task 11.2（11B）的可重复基准。它覆盖连接建立耗时、RDP 首屏耗时、direct 成功率、Relay 流量、Relay 带宽成本以及客户端 CPU/内存，并为后续版本提供相同字段的趋势对比。本文不定义监控平台、Dashboard、告警、CI 或新的产品功能。

## 1. 证据等级与边界

所有结果必须标明 `measurement_scope`，不得跨等级混用：

| 等级 | `measurement_scope` | 可以证明 | 不能证明 |
| --- | --- | --- | --- |
| 本机/模拟基准 | `local_simulated` | 脚本可运行、结构化结果稳定、本机进程资源、显式本机 TCP 目标的握手耗时、公式计算正确 | 真实产品连接耗时、跨 NAT direct 成功率、真实 RDP 首屏、云端 Relay 流量或成本 |
| 发布前真实基准 | `real_windows_network` | 两台真实 Windows、不同 NAT、指定 Hub/Relay 区域与固定 RDP 负载下的产品体验和成本输入 | 未执行场景、其他地区/运营商/硬件的表现 |

`active_tcp_probe` 只测 TCP 建连，不能冒充产品从“请求连接”到 `path_state` 已连接的完整耗时。真实产品连接耗时必须填入 `observations.connection_establishment_ms`；RDP 首屏、direct 路径和 Relay 计数也只能来自真实执行后的显式观测。

## 2. 一条命令采集本机可得指标

示例配置默认只读取本机 `mesh-desktop`、`mesh-agent` 进程；未提供的真实网络/RDP/成本项会保留待测状态：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\benchmark.ps1 -ConfigPath .\scripts\benchmark.config.example.json -OutputPath .\benchmark-result.json
```

输出为 UTF-8 JSON，schema 为 `task11b.performance-result.v1`。脚本会创建输出目录，但不会上传结果。要探测非回环的显式目标，必须额外传入 `-AllowNonLoopback`；该开关只授权配置中的 TCP 目标，不启用遥测或结果外发。

发布人员应复制示例配置到不纳入版本库的工作目录，填写中性场景标签和纯数值观测，再运行同一命令。不要把用户名、主机名、IP 地址、账号、设备 ID、凭据或用户内容写入标签；结果不需要这些信息。

## 3. 指标定义

| 指标 | 起止点或公式 | 数据源 | 发布前最小样本 |
| --- | --- | --- | --- |
| 连接建立耗时 | 产品发起连接请求至 `lan_direct_connected`、`public_direct_connected` 或 `fallback_relay`，单位 ms | 同一时钟下的产品状态时间点或人工秒表差值，写入 `connection_establishment_ms` | 每场景预热 5 次后 30 次 |
| RDP 首屏耗时 | 用户触发 RDP 至第一帧桌面可辨识且可交互，单位 ms | 人工秒表；不截图、不录屏、不采集屏幕内容，写入 `rdp_first_frame_ms` | 每场景预热 5 次后 30 次 |
| direct 成功率 | `(lan_direct + public_direct) / 全部连接尝试` | 每次最终 `path_type`，写入 `direct_path_types`；允许值为 `lan_direct`、`public_direct`、`relay`、`failed` | 每个 NAT/网络场景 100 次 |
| Relay 流量 | `delta(relay_bytes_in) + delta(relay_bytes_out)` | 固定负载窗口前后的现有 Relay 计数器，配置中只填非负差值 | 每个负载档位至少 3 个完整窗口 |
| Relay 带宽成本 | `(bytes_in + bytes_out) / 1073741824 × price_per_gib` | 上述流量及操作者显式填写的区域、币种、每 GiB 单价 | 每个实际 Relay 区域一组显式输入 |
| 客户端 CPU | 配置进程的总 CPU 时间差；不可读时回退 Windows 格式化进程计数器，并按逻辑处理器数归一到 0–100% | 本机进程与 Windows CIM，只输出聚合分位数 | 预热 5 个间隔后 30 个 1 秒间隔 |
| 客户端内存 | 所有同名目标进程 Working Set 之和，单位 byte | 本机进程，只输出聚合分位数 | 与 CPU 同步 |

“第一帧”必须在测试前统一人工判定规则；不得以 TCP 端口可达、RDP 窗口出现或认证框出现代替。连接失败同样计入 direct 分母，最终走 Relay 不算 direct 成功。

## 4. 固定方法

### 4.1 环境字段

每次结果必须保留以下字段；条件不同的结果不得直接计算版本回归：

| 字段 | 要求 |
| --- | --- |
| `product_version`、`source_revision` | 发布前真实基准必须显式填写已部署制品版本/revision；仅当被测进程确认由当前 checkout 构建时，才可省略并使用仓库 `VERSION` 和当前 Git revision 回退值 |
| `hardware_label` | 源 Windows 的稳定匿名标签；JSON 另含本机 OS、架构、CPU 型号、逻辑处理器和总内存 |
| `peer_hardware_label` | 对端 Windows 的稳定匿名硬件档位，不写主机名 |
| `hub_version`、`relay_version` | 实际部署版本或制品 revision；未参与时写 `not-measured` |
| `network_profile`、`nat_profile` | 固定的网络与 NAT 场景标签，例如 `home-nat-to-mobile-hotspot`，不写 IP |
| `rdp_profile` | 分辨率、色深、重定向开关和负载档位的稳定标签 |
| `scenario` | 唯一的可复现场景名；不得把多个网络/负载混为一组 |

### 4.2 预热、样本与分位数

- 延迟、RDP 和资源发布基准：先执行 5 次/5 个采样间隔预热并丢弃，再记录至少 30 个样本。
- direct 成功率：每个网络/NAT 场景至少 100 次独立尝试；每次完成后关闭会话并等待状态回到空闲。
- Relay 流量：同一负载档位至少执行 3 个完整窗口，逐窗口保存结果；不要先把不同窗口平均后再计算分位数。
- 脚本使用 nearest-rank：升序样本中第 `ceil(p × n)` 个值，输出 `p50`、`p95`、`p99`、`min`、`max`、`mean` 和 `count`。
- 同一对比组保持样本量、预热数、进程采样间隔、超时、时区/时钟同步方式一致。发布前建议接通电源、固定 Windows 电源模式并停止无关更新任务。

本机探索可以使用更少样本，但必须保留 `local_simulated`，且不能成为官方 Hub MVP 的发布前真实基准。

### 4.3 固定 RDP/Relay 负载

每次发布至少执行两个独立档位，不能混合统计：

1. `rdp-idle-10m`：1920×1080、32 位色、关闭音频/打印机/剪贴板/磁盘重定向；首屏稳定后 10 分钟无输入。
2. `rdp-ui-5m`：同一显示和重定向设置；5 分钟内每 5 秒打开或关闭一次 Windows 开始菜单，共 60 次，只操作系统 UI，不打开用户文件。

在窗口开始前读取 `relay_bytes_in/out`，结束后再次读取，只把两个非负差值写入配置。若计数器重置、会话切路或负载步骤中断，该窗口作废并重测。CPU/内存采样应覆盖整个负载窗口中的固定 30 秒区间，并在各版本选择同一相对时间段。

## 5. 配置输入

`scripts/benchmark.config.example.json` 是最小安全示例。关键字段如下：

| JSON 路径 | 含义 |
| --- | --- |
| `schema_version` | 固定为 `task11b.benchmark-config.v1` |
| `environment.measurement_scope` | `local_simulated` 或 `real_windows_network` |
| `sampling.*` | 预热数、正式样本数、资源间隔和 TCP 超时 |
| `connection_probe` | 可选主动 TCP 探测；输出不记录 host，只记录中性 `target_label` |
| `process_names` | 只读取这些本机进程的 CPU/Working Set，不读取进程参数或内存内容 |
| `observations.connection_establishment_ms` | 真实产品建连耗时纯数字数组；存在时优先于 TCP 探测统计 |
| `observations.rdp_first_frame_ms` | 真实 RDP 首屏耗时纯数字数组 |
| `observations.direct_path_types` | 每次尝试的最终路径枚举数组 |
| `observations.relay_bytes_in/out` | 固定窗口的 Relay 入/出方向字节差值，必须成对提供 |
| `relay_cost.price_per_gib` | 操作者从当前合同/账单显式填写的非负单价 |
| `relay_cost.region`、`currency` | 与单价对应的显式区域和三字母币种；缺任一项即拒绝计算 |

脚本没有内置云厂商、市场价格、汇率、税费或折扣。v1 成本公式明确假设入站和出站字节都计费，`1 GiB = 1073741824 bytes`；若实际合同的计费方向或单位不同，不得套用该结果，应保留待测并在后续 schema 明确建模。

## 6. 机器可读结果 schema

| 路径 | 稳定语义 |
| --- | --- |
| `schema_version` | `task11b.performance-result.v1` |
| `generated_at_utc`、`benchmark_id` | UTC 生成时间和本次基准 ID |
| `environment` | 版本、匿名硬件档位、网络/NAT/RDP 档位及本机硬件字段 |
| `methodology` | 预热、样本量、间隔、超时、`nearest_rank` |
| `privacy` | 固定声明不含用户内容、凭据，且未发送遥测 |
| `metrics.connection_establishment_ms` | 耗时分位数；`source` 区分产品观测与 TCP 探测 |
| `metrics.rdp_first_frame_ms` | RDP 首屏分位数 |
| `metrics.direct_success_rate` | 尝试数、direct 成功数、比例和百分比 |
| `metrics.relay_traffic` | 入/出/总字节和 GiB |
| `metrics.relay_bandwidth_cost` | 显式区域、币种、单价、计费 GiB、金额、公式和假设 |
| `metrics.client_resources` | 匹配进程数、CPU 与 Working Set 子指标状态及分位数 |
| `pending_real_environment` | 所有未达到 `measured` 的指标名，供发布人员逐项补测 |

状态含义：`measured` 表示有实际输入/采样；`pending_real_environment` 表示必须在真实场景补测；`pending_explicit_cost_input` 表示没有显式价格三元组；`pending_relay_traffic` 表示有价格但没有流量；`partial` 表示资源子指标只取得一部分；`not_available` 表示本机没有匹配进程或计数器不可读。

## 7. 两台真实 Windows / 不同 NAT 的发布前步骤

1. 准备源机 A 和目标机 B，确认是两条独立上网链路/NAT；从实际部署制品记录客户端/Hub/Relay 版本与 revision，并记录匿名硬件档位、Windows build、区域、网络/NAT 和 RDP 档位。不要用当前源码 revision 代替未验证的运行中二进制版本。
2. 同步两台机器时钟。确认 B 已启用 RDP，A 到 B 的连接流程可用；不要在配置或报告中记录账号、凭据、设备 ID、主机名或地址。
3. 每个网络场景先执行 5 次完整连接和 RDP 预热并丢弃。正式执行 30 次，逐次记录产品建连 ms、首屏 ms 和最终 `path_type`。
4. 为 direct 成功率继续执行至 100 次。公司网络、酒店/校园网络、CGNAT 等场景必须分别出结果，不能合并分母。
5. 对确认走 Relay 的会话分别执行 `rdp-idle-10m` 和 `rdp-ui-5m`，记录计数器差值；在固定 30 秒区间采集客户端资源。
6. 从本次实际 Relay 合同或账单取得区域、币种和每 GiB 单价，人工确认 v1 的双向计费假设后填入 `relay_cost`。没有有效输入就保持待测，不估算市场价。
7. 运行脚本；若 `connection_probe.host` 不是回环地址，显式加入 `-AllowNonLoopback`。检查所有发布必需指标均为 `measured`，并保留 JSON 与人工执行记录。

## 8. 结果表与跨版本对比

每个版本先保留原始 JSON，再在报告中使用以下一行一个场景的格式：

| 版本 / revision | 场景 / 环境指纹 | n / 预热 | 建连 p50 / p95 / p99 ms | RDP 首屏 p50 / p95 / p99 ms | direct 成功数 / 尝试数 / % | Relay in / out / total bytes | 区域 / 币种 / 单价 / 成本 | CPU p50 / p95 % | Working Set p50 / p95 MiB | 状态 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `x.y.z / revision` | `scenario + hardware/network/NAT/RDP labels` | `30 / 5` | 原始结果 | 原始结果 | 原始结果 | 原始结果 | 显式输入与结果 | 原始结果 | 原始结果 | `measured` 或待测项 |

候选版本相对基线的延迟、资源、流量和成本变化统一为 `(candidate - baseline) / baseline × 100%`；direct 成功率报告百分点差 `candidate% - baseline%`，同时保留成功数和分母。基线为 0 时不计算百分比，标记 `not_comparable`。只有环境指纹和方法字段一致时才作趋势结论；性能门限必须由发布负责人在看见候选结果前另行确定，本文不替代发布决策。

## 9. 隐私与安全

- 脚本不解析 RDP 画面、剪贴板、文件、命令行或进程内存，不收集用户内容和凭据。
- 配置出现密码、令牌、私钥、剪贴板、RDP 内容或文件内容等敏感字段名时直接拒绝；操作者仍需确保标签值本身是中性的。
- 输出不包含探测 host/IP，只保留 `target_label`；不发 HTTP 请求、不上传 JSON、不发送遥测。
- 主动网络动作仅限配置中的 TCP 建连；默认只允许回环，非回环必须由操作者显式授权。发布前真实测试只使用获准的两台 Windows、Hub 和 Relay。
- 原始产品日志只在本地用于提取时间差、最终路径和字节计数；不要把整段日志或标识符复制进基准配置。
