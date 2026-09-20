# 纯 P2P 数据面实现与验证证据（2026-09-14）

## 结论与版本边界

- 验证日期：2026-09-14（Asia/Shanghai；证据整理时间 `2026-09-14T22:49:15+08:00`）。
- Task 10 基线 SHA：`814d2ad1c93d442a879b2567fde5f17551c8d4fa`。
- 最终 SHA（被验证的生产实现）：`814d2ad1c93d442a879b2567fde5f17551c8d4fa`。本 Task 未发现需要修改生产代码的验证失败；新增内容仅为 AC-02 真实运行路径集成测试与本 QA 记录。
- Task 10 证据提交 SHA：本文件与 AC-02 测试位于同一提交；Git 提交对象不能在自身内容中嵌入自己的最终 SHA，精确提交 SHA 由提交后的交付报告记录。
- Go 工具链：`go version go1.26.1 windows/amd64`。
- QUIC 依赖：`github.com/quic-go/quic-go v0.61.0`。

实现完成，公网实测待验收

上述状态只表示生产路径实现和本机自动化验收完成，不表示 Windows 三机、双 NAT 或公网抓包已经通过。

## AC-02 自动化验收

新增 `TestTwentyIdlePeersDoNotCreatePairSessions`，复用现有 `pureP2PHarness`，没有 fake dialer、mock coordinator 或静态源码搜索：

1. 生成一套真实 CA、Coordinator 证书和 20 个唯一 Peer 证书，并把 20 个唯一 NodeID/`10.77.0.x` 地址写入真实设备注册表。
2. 通过生产 `Agent.Run` 启动 A 和 20 个 Peer；控制面使用真实 TCP/TLS，候选探测使用真实 UDP，Peer runtime 持有真实 quic-go transport。
3. 不向任何测试 TUN 注入业务包；全部 Peer 就绪并取完初始计数后启动单调计时，只有实际经过至少 90 秒才允许完成，从而覆盖 3 个完整的 30 秒控制心跳/候选刷新周期。
4. 断言 A 始终保有 20 条控制连接，candidate-refresh 至少增长 60 次，probe success + failure 的已计数结果至少增长 60 次。测试在观测期间保存每个真实 ControlClient 收到的 probe credential，并用生产 `p2p.EncodeProbeRequest`/`p2p.DecodeProbeResponse` 对 A 边界每个 UDP 帧逐一校验版本、ID、时间、nonce、HMAC、observed address 和 request/response 相关性。
5. 断言 A 的 requested/prepared/offered/started/succeeded/aborted/timed-out 全部协商计数均为 0，且 `TypePacketViolations=0`。
6. 对每个 Peer 断言 `ActiveSessions` 为空、其余 19 个 pair 均无 `SessionSnapshot`、状态投影为 nil session + 空 path + `idle`。
7. 断言所有测试 TUN 写入队列为空、业务 payload ledger 为空、Coordinator 未打开 packet device、无 Relay 路径。

该新增验收第一次在未改生产代码的基线上即通过；本次缺口是 AC-02 自动化证据缺失，不是已发现的生产 bug，因此没有制造或提交生产修复。独立审查强化认证断言后的首轮复跑在 90 秒 credential TTL 边界观察到 `ProbeSuccesses=99`、`ProbeFailures=1`；这是有效 HMAC 请求在续期交错处过期的已计数失败，不是 P2P 会话。最终测试要求 response 数精确等于 success、无响应但 HMAC 有效的 request 数精确等于 failure、总 request 数等于两者之和，并把续期边界 failure 总数限制为最多 20 次，因而不会接受任意 probe-shaped 流量。

精确命令：

```powershell
go test ./internal/agent -run '^TestTwentyIdlePeersDoNotCreatePairSessions$' -count=1 -timeout=3m -v
```

- 退出码：`0`。
- 最新结果：`PASS`；测试用时 `90.38s`，包用时 `91.443s`。

把三周期完成条件修正为 probe success + failure 合计后，第一次精确复跑在测试启动约 1.36 秒、尚未进入 90 秒观测前，Go/Windows 进程于 `os.ReadFile`/`peerNetworkID` 证书读取路径发生 `0xc0000005` 致命访问异常并退出 `1`。相同命令立即完整复跑通过，且后续最终全仓门禁另行记录；该异常未被计作通过，也没有证据表明它来自本次仅改变计数谓词的测试修改。

## 格式化与依赖审计

| 命令 | 退出码 | 结果 |
|---|---:|---|
| `$changedGo = git diff --name-only 4360b00 -- '*.go'; if ($changedGo) { gofmt -w $changedGo }` | 0 | 已格式化计划覆盖的完整纯 P2P changed-Go 范围；归一化内容 diff 仅保留本 Task 的集成测试。 |
| `go mod tidy` | 0 | `go.mod`/`go.sum` 无内容变化。 |
| `git diff --exit-code 814d2ad1c93d442a879b2567fde5f17551c8d4fa -- go.mod go.sum` | 0 | 相对 Task 10 基线无依赖漂移。 |
| `go mod verify` | 0 | `all modules verified`。 |
| `go list -m -f '{{.Path}} {{.Version}}' github.com/quic-go/quic-go` | 0 | `github.com/quic-go/quic-go v0.61.0`。 |

Windows 的系统 Git 配置为 `core.autocrlf=true`；`gofmt` 将工作树文件写为 LF 后曾使 stat cache 暂时显示多个 modified 文件。`git diff` 证明除新增测试外没有内容差异，重新索引后 staged 范围仅为预期文件；这不是依赖或源码漂移。

## 首次全仓验证

| 命令 | 退出码 | 结果 |
|---|---:|---|
| `go vet ./...` | 0 | 无诊断输出。 |
| `go test ./... -count=1` | 0 | 全部包通过；`internal/agent 140.423s`，`internal/p2p 28.268s`。 |

全仓测试包含以下真实路径证据：

- AC-01/03/04/05/06/13：`TestPureP2PDataPlaneFaultMatrix` 使用真实 A/B/C/D/E、TCP/TLS、UDP、QUIC 和 channel-backed TUN，覆盖双向传输、A 离线存活、直连自身断开后的 fail-closed、A 恢复后的新 generation、D/E 隔离和入站反欺骗。
- AC-02：`TestTwentyIdlePeersDoNotCreatePairSessions` 覆盖 20 个无业务 Peer 在 3 个控制周期内不建立 N² 会话。
- 并发首包：`TestConcurrentBidirectionalFirstPacketCreatesOnePairGeneration` 证明双向首包只生成一个 pair generation。
- AC-14：`TestRevocationAfterCoordinatorReturnClosesOnlyAffectedPair` 证明撤销只关闭受影响 pair，独立 D/E 会话保持。
- AC-10/11：`TestLegacyAndMaliciousControlClientsCannotSendData` 证明 v1 收到升级错误、v2 `TypePacket` 被关闭且不送入 TUN。
- 其余 `internal/p2p`、`internal/proto`、`internal/onboarding`、`internal/ui` 测试覆盖会话身份、一次性授权、分片重组、路由、状态真实性、迁移与无 Relay 界面。

## Race 状态

要求的原始命令已实际尝试：

```powershell
go test -race ./... -count=1 -timeout=10m
```

- 退出码：`1`。
- 原始失败：`go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`。
- `go env CGO_ENABLED CC CXX GOOS GOARCH` 返回 `0`, `gcc`, `g++`, `windows`, `amd64`。
- `Get-Command gcc` 与 `Get-Command clang` 均退出 `1`，两者均不在 PATH。

为排除“只因默认开关关闭”的情况，又执行：

```powershell
$env:CGO_ENABLED = '1'; go test -race ./... -count=1 -timeout=10m
```

- 退出码：`1`。
- 精确根因：`cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%`。

因此本机没有产生 race PASS 证据；状态是 Windows CGO C 编译器缺失导致未能构建 race runtime，不得解释为竞态检查通过。

## Windows 构建产物

输出目录经过 `Resolve-Path`/前缀检查，确认位于仓库外：

`C:\Users\Administrator\AppData\Local\Temp\meshlink-p2p-windows-build-01a0a05396357d93a160eacac7a461bd`

以下六条命令分别退出 `0`：

```powershell
go build -o 'C:\Users\Administrator\AppData\Local\Temp\meshlink-p2p-windows-build-01a0a05396357d93a160eacac7a461bd\mesh-agent.exe' ./cmd/mesh-agent
go build -o 'C:\Users\Administrator\AppData\Local\Temp\meshlink-p2p-windows-build-01a0a05396357d93a160eacac7a461bd\mesh-desktop.exe' ./cmd/mesh-desktop
go build -o 'C:\Users\Administrator\AppData\Local\Temp\meshlink-p2p-windows-build-01a0a05396357d93a160eacac7a461bd\mesh-ui.exe' ./cmd/mesh-ui
go build -o 'C:\Users\Administrator\AppData\Local\Temp\meshlink-p2p-windows-build-01a0a05396357d93a160eacac7a461bd\meshctl.exe' ./cmd/meshctl
go build -o 'C:\Users\Administrator\AppData\Local\Temp\meshlink-p2p-windows-build-01a0a05396357d93a160eacac7a461bd\mesh-cloudhub.exe' ./cmd/mesh-cloudhub
go build -o 'C:\Users\Administrator\AppData\Local\Temp\meshlink-p2p-windows-build-01a0a05396357d93a160eacac7a461bd\mesh-update-server.exe' ./cmd/mesh-update-server
```

产物存在性、非零大小、数量和 SHA-256 检查退出 `0`：

| 产物 | 字节 | SHA-256 |
|---|---:|---|
| `mesh-agent.exe` | 13,545,472 | `F4F160FF4E4FDF8050EACF399A84CC900FA96196113BECD25E4FA80E9CB2CEBD` |
| `mesh-desktop.exe` | 18,667,008 | `EB1BDEBB9B49CED1F35CAAF8FF33B7BE9AF7DEA28E18C0CD7BE1E40942FD71AA` |
| `mesh-ui.exe` | 13,082,112 | `50E83C5B5B6EDC1B551FAD8A927A9A0A38DAF36EF29785F411EFE4854CA1C230` |
| `meshctl.exe` | 5,175,808 | `72DE3E750247DAA646A75F16DC29C726BD74AA7E288DCC8F4AA7D7B612315E25` |
| `mesh-cloudhub.exe` | 11,620,864 | `A18B870EFD313959AF2A733264811F7996C6728F3C32D01954B366BD20708558` |
| `mesh-update-server.exe` | 9,775,104 | `11E9452BE3DD22A0696BBAA41048D8B208BD5B4BB219C88880C348A192F58E91` |

仓库内未写入上述构建产物。

## 未执行的公网物理验收

以下步骤没有可用的三台 Windows 物理设备、两个独立家庭 NAT 和公网 A，因此全部为“未执行”，不能标记为通过：

1. 未执行：三台 Windows 设备位于同一 LAN，使用真实 TUN 与 RDP 流量验证 `lan_direct`。
2. 未执行：B/C 分别位于两个普通家庭 NAT 后、A 位于公网，验证服务器观察候选、UDP 打洞和 `public_direct`。
3. 未执行：保持真实 RDP 会话时停止 A，持续操作并记录既有直连不中断。
4. 未执行：分别在 A 在线和 A 离线时断开并恢复 B/C 网络，验证在线时自动重连、离线时等待、A 恢复后取得新 generation。
5. 未执行：抓取 A 的公网网卡流量，证明只有控制帧和小型 UDP probe，不含 B/C 的 RDP 业务流量。
6. 未执行：在 UDP 封锁或对称 NAT 环境验证 `direct_unreachable_no_relay`，且不出现 Relay 或虚假 direct。

当前 fail-closed 限制：同 NodeID 重新启用/换证后，恢复通常需要先重启 Coordinator A，再重启受影响 Peer；仅重启 Peer 不足。

## 文档后完成门与独立审查

在三周期谓词稳定性修正和审查加固后执行的最终完成门如下：

| 命令 | 退出码 | 结果 |
|---|---:|---|
| `go test ./internal/agent -run '^TestTwentyIdlePeersDoNotCreatePairSessions$' -count=1 -timeout=3m -v` | 0 | `PASS`；测试 `90.38s`，包 `91.443s`。 |
| `gofmt -d <本提交 changed-Go 文件>` | 0 | 无格式差异。 |
| `go vet ./...` | 0 | 无诊断输出。 |
| `go test ./... -count=1` | 0 | 全部包通过；`internal/agent 140.355s`，`internal/p2p 29.295s`。 |
| `git diff --cached --check` | 0 | 无空白错误。 |
| `git diff --exit-code 814d2ad1c93d442a879b2567fde5f17551c8d4fa -- go.mod go.sum` | 0 | 无依赖漂移。 |
| 仓库内递归 `*.exe` 审计 | 0 | 未发现构建产物。 |
| `git status --short` | 0 | 仅 `A  docs/qa/pure-p2p-data-plane-implementation-2026-09-14.md` 与 `M  internal/agent/pure_p2p_integration_test.go`。 |

独立只读审查首轮结论为 `Critical: 0`、`Important: 3`。三项 Important 均在提交前修复：

1. 从“仅由计数增长间接推断周期”改为单调时钟明确等待至少 90 秒。
2. 从只判断 UDP probe kind 改为使用实际下发 credential 对全部帧执行严格格式、HMAC、observed-address 与 request/response 对账，并把 90 秒 credential 续期边界的已计数 failure 纳入完成条件。
3. 用本节的实际最终命令、退出码和审查状态替换占位文字。

第一次修复后复审结论为 `Critical: 0`、`Important: 3`、`Ready: No`，并给出了精确代码/文档位置。提交前继续完成三项修复：

1. 对 UDP 映射端口未进入帧快照的非 Coordinator 数据增加 `dropped == 0` 断言，覆盖潜在 punch/QUIC 旁路。
2. 在完成门中记录精确的 `git status --short` 及唯一两个 staged 文件。
3. 把 credential 续期 failure 上限从不准确的“每 Peer”表述改为与代码一致的“全局最多 20 次”。

上述追加修复后的最终只读复审结论为 `Critical: 0`、`Important: 0`、`Ready: Yes`；没有剩余阻塞项。
