# Task 11A E2E 网络测试矩阵执行记录

日期：2026-07-15

## 本次结论

- 基线：`0df5aae TASK 10E add private licensing and offline updates`，分支 `main`。
- 交付：新增 [`e2e-matrix.zh-CN.md`](e2e-matrix.zh-CN.md) 和本执行记录；未新增产品代码、测试平台或文档校验脚本。
- 本机结果：矩阵直接依赖的 cloudhub、P2P、Relay、diagnose、RDP、UI、自托管/部署、Windows 桌面、离线更新和授权相关包均以 `-count=1` 执行；有测试的包全部通过，`internal/rdp` 明确为 `[no test files]`。
- 真实环境结果：本轮未执行两台真实 Windows、不同 NAT、公网 IP、真实端口映射、云服务器、CGNAT、企业/酒店/校园网络、官方测试 Hub/Relay、十设备或隔离网 E2E，因此所有 M 级场景准确标记为“发布前待执行”。
- 发布判定：Task 11A 的矩阵和本机 A/S 证据可用于交付；任何阶段发布候选仍须补齐对应 M 级场景，当前记录不构成产品发布门禁通过。

## 实际运行命令与结果

### 1. 既有测试精确映射

命令：

```powershell
rg -n '^func (Test(ServiceAccountNetworkInviteJoinHeartbeatAndRevoke|ServiceAccountEnforcementClosesPendingAndActiveRelaySessions|ServiceDeviceRevokeClosesOnlyRelatedRelaySessions|ServiceAccountFreezeBanBlocksControlPlaneJoinHeartbeatAndRelay|ServiceP2PControlPlaneNegativeAuthorization|Task10BConnectionGateBeforeCandidatesDirectAndRelay|Task10BAllRolesNeedExplicitConnectionGrant|P2PFallbackQuotaInsufficientDoesNotAuthorizeRelay|ConnectorUsesLANDirectOnSuccess|ConnectorTriesPublicDirectAfterLANDirectFailure|AutoFallbackConnectorCreatesAndAuthorizesRelayAfterDirectFailure|TCPDialerConnectsLoopbackLANDirect|P2PAutomaticRelayFallbackRuntimeSmoke|ClassifyNATProbeCoversCommonResults|CheckRDPTargetIncludesPlainLanguageContext|OneClickReportExplainsRequiredScenarios|OfficialHubAPIFlow|RDPDiagnosticsAPIIncludesUserContext|SelfHostedProductLoopCreatesJoinableDevices|DeploySelfHostedRelayStagesRemoteHubAndInvite|E2ESelfHostedRelayDeploysAndEnrollsTwoClients|Task10DTenSimulatedBootstrapRedemptionsQuotaAndAudit|PrivateDeploymentLoopbackImportJoinAndExpiryClosure))\b' internal/cloudhub internal/p2p internal/relay internal/diagnose internal/rdp internal/ui internal/onboarding internal/deployssh internal/agent
```

结果：退出码 0，共定位 23 个关键测试函数；命中 cloudhub 账号/吊销/RBAC/额度/批量部署/私有部署、P2P direct/fallback/NAT、Relay runtime、diagnose/RDP 文案、UI、onboarding 和真实环境自建中继入口。矩阵链接进一步覆盖同包内的相关辅助测试与既有 QA 文档。

### 2. 矩阵核心包非缓存测试

命令：

```powershell
go test -count=1 ./internal/cloudhub ./internal/p2p ./internal/relay ./internal/diagnose ./internal/rdp ./internal/ui ./internal/onboarding ./internal/deployssh ./internal/agent
```

结果：退出码 0。

```text
ok  meshlink/internal/cloudhub
ok  meshlink/internal/p2p
ok  meshlink/internal/relay
ok  meshlink/internal/diagnose
?   meshlink/internal/rdp [no test files]
ok  meshlink/internal/ui
ok  meshlink/internal/onboarding
ok  meshlink/internal/deployssh
ok  meshlink/internal/agent
```

说明：`internal/rdp` 只能由本命令证明当前平台包可构建；没有单元测试，更不能据此声称真实 Windows RDP 已通过。

### 3. Windows 桌面、离线更新与授权补充测试

命令：

```powershell
go test -count=1 ./cmd/mesh-desktop ./internal/update ./internal/licensing
```

结果：退出码 0，三个包全部 `ok`。

### 4. 未运行全仓回归的依据

本次只新增两份 Markdown 文档，没有修改 Go、产品代码、构建脚本或运行时配置。按 Task 11A 约束只运行矩阵直接依赖的最小相关包，不重复 `go test -count=1 ./...`。release gate 对发布候选的全仓回归要求仍然保留，不能由本记录豁免。

## 本机已证明的范围

| 证据范围 | 本次结果 | 证据边界 |
| --- | --- | --- |
| 账号/网络/设备、封禁/吊销、RBAC、额度、审计、批量部署、私有授权 | 相关 cloudhub 测试通过 | 进程内 service/API/存储，不是官方生产环境或真实设备。 |
| LAN/public 候选排序、NAT 分类、打洞决策、Relay fallback、质量/状态 | P2P 测试通过 | 包含纯模型、fake dialer 和回环；不是实际 NAT 或公网 RDP。 |
| Relay 双向转发、额度停止、撤销、fallback runtime | Relay 测试通过 | 本机 TCP runtime/loopback，不是不同网络间 Relay。 |
| RDP/一键诊断与用户文案 | diagnose、UI、Windows 桌面测试通过 | 回环端口和 DOM/handler 断言，不是系统 RDP 会话。 |
| 自托管、云部署流程、失败分类与回滚 | onboarding、deployssh 测试通过 | 临时目录、模拟 SSH/HTTP；真实 E2E 测试入口未因缺少外部环境而启用。 |
| 离线更新、授权与私有部署边界 | update、licensing、cloudhub、UI、onboarding 测试通过 | 本机文件/loopback，不是隔离企业网安装升级。 |

## 未具备环境项

本轮没有申请或使用额外权限、Browser MCP、外部依赖、专用公网/OpenWrt 测试机或远程部署资源。下列环境未具备：

- 两台真实 Windows 与可交互系统 RDP 目标。
- 可控的同 LAN、独立公网 IPv4、家用网关端口映射和两个不同 NAT。
- 临时 Linux 云服务器与安全组、systemd 和公网健康入口。
- 官方测试 Hub/Relay、可预置额度的测试账号及后台风控操作员。
- 运营商 CGNAT/蜂窝、受授权企业出口、酒店/校园门户网络。
- 10 台真实 Windows/受管 VM 设备池和无公网出口的隔离企业网。

此前远程部署与公网 smoke 的 QA 文档仅作为 H 级参考。本轮没有相关产品代码变化，且 Task 11A 明确不重复已充分验证的远程部署，因此未复用任何真实公网主机。

## 发布前必须补跑

| 场景 ID | 必须补跑的真实环境结果 | 对应发布门禁 |
| --- | --- | --- |
| E2E-NET-001 | 两台 Windows 同 LAN，5 分钟内首次 RDP，`lan_direct` 且 Relay 字节为零 | 阶段一、四 |
| E2E-NET-002 | 独立公网 IPv4 设备 `public_direct` RDP | 阶段一、四 |
| E2E-NET-003 | 真实网关端口映射下 `public_direct`，撤销映射后正确降级 | 阶段一、四 |
| E2E-NET-004 | Linux 云服务器 SSH 向导部署、健康、失败回滚 | 阶段二 |
| E2E-NET-005 | 两个无公网 Windows 经自建中继完成 RDP 和服务重启恢复 | 阶段二 |
| E2E-NET-006 | 官方 Hub、不同 NAT、基础 Relay RDP、后台元数据/用量闭合 | 阶段三 |
| E2E-NET-007 | 运营商 CGNAT 下不误报公网入站并可 Relay 降级 | 阶段三、四 |
| E2E-NET-008 | 受授权公司网络 UDP/出口限制下 Relay 或可解释失败 | 阶段三、四 |
| E2E-NET-009 | 酒店或校园门户/隔离网络；半年周期内两个环境各有记录 | 阶段三、四 |
| E2E-NET-010 | 真实 direct 阻断后 15 秒内 Relay fallback，记录 RDP 中断行为 | 阶段三、四 |
| E2E-NET-011 | 官方测试账号 Relay 额度达到上限，失败闭合且不伪装网络错误 | 阶段三、五 |
| E2E-NET-012 | 真实 Windows 关闭 RDP，链路与 `rdp-unreachable` 正确区分，恢复成功 | 阶段一至四 |
| E2E-NET-013 | 官方测试账号封禁后 1 分钟内活动连接断开，解除后恢复 | 阶段三 |
| E2E-NET-014 | 吊销单设备只关闭相关连接，无关设备会话保持 | 阶段三、五 |
| E2E-NET-015 | 普通成员连接未授权设备，在候选/Relay 前拒绝并显示权限提示 | 阶段四、五 |
| E2E-NET-016 | 团队邀请、角色、分组、授权 RDP 与审计完整闭环 | 阶段五 |
| E2E-NET-017 | 10 台真实设备自动绑定、一个失败、单机重试和 rollout 回滚 | 阶段五 |
| E2E-NET-018 | 隔离网私有 Hub、授权、RDP、离线更新、到期策略与版本回滚 | 阶段五 |

## 未做内容

- 未继续 Task 11B，也未实现 Task 11.2-11.5。
- 未建设性能平台、CI/CD、监控、客服系统或新的测试平台。
- 未修改 P2P、Relay、RDP、Cloud Hub、UI 或任何产品行为。
- 未伪造真实网络结果，未把 fake dialer、单机回环、模拟 SSH、模拟设备或历史记录标为当前 M 级通过。
- 未执行远程部署、未使用公网/OpenWrt 测试机、未安装依赖、未调用 Browser MCP。

## 文档结构与引用校验

命令（一次性只读 PowerShell；未新增校验脚本）：

```powershell
$ErrorActionPreference='Stop'
$matrixPath='docs/qa/e2e-matrix.zh-CN.md'
$resultsPath='docs/qa/task11a-e2e-matrix-results-2026-07-15.md'
$matrix=Get-Content -Raw -LiteralPath $matrixPath
$results=Get-Content -Raw -LiteralPath $resultsPath
$matches=[regex]::Matches($matrix,'(?m)^## (E2E-NET-\d{3})\s+.+$')
$ids=@($matches | ForEach-Object { $_.Groups[1].Value })
$expected=@(1..18 | ForEach-Object { 'E2E-NET-{0:D3}' -f $_ })
if($ids.Count -ne 18){ throw "scenario heading count=$($ids.Count), want 18" }
if((@($ids | Sort-Object -Unique)).Count -ne 18){ throw 'scenario IDs are not unique' }
if((Compare-Object $expected ($ids | Sort-Object))){ throw 'scenario IDs do not equal E2E-NET-001..018' }
$fields=@('适用阶段/发布门禁','环境与前置条件','最短步骤','预期路径','用户可见结果','自动化/手工级别与证据','失败采集项','清理/回滚','执行频率','责任边界')
for($i=0;$i -lt $matches.Count;$i++){
  $start=$matches[$i].Index
  $end=if($i+1 -lt $matches.Count){$matches[$i+1].Index}else{$matrix.Length}
  $section=$matrix.Substring($start,$end-$start)
  foreach($field in $fields){
    if($section -notmatch ('(?m)^- \*\*'+[regex]::Escape($field)+'：\*\*\s+\S')){
      throw "$($ids[$i]) missing required field $field"
    }
  }
}
$resultIDs=@([regex]::Matches($results,'(?m)^\| (E2E-NET-\d{3}) \|') | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique)
if((Compare-Object $expected $resultIDs)){ throw 'results pre-release table does not cover E2E-NET-001..018' }
$linkCount=0
foreach($doc in @($matrixPath,$resultsPath)){
  $raw=Get-Content -Raw -LiteralPath $doc
  foreach($m in [regex]::Matches($raw,'\[[^\]]+\]\(([^)]+)\)')){
    $target=$m.Groups[1].Value
    if($target -match '^(?:https?://|mailto:|#)'){ continue }
    $filePart=($target -split '#',2)[0]
    $resolved=[IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $doc) $filePart))
    if(-not (Test-Path -LiteralPath $resolved)){ throw "missing local link in $doc -> $target" }
    $linkCount++
  }
}
"PASS: 18 unique scenario IDs; 10 required fields x 18; results cover 18 IDs; $linkCount local links exist"
```

结果：退出码 0，输出 `PASS: 18 unique scenario IDs; 10 required fields x 18; results cover 18 IDs; 66 local links exist`。

## 提交前差异校验

提交前对两份 11A 文档执行 `git diff --check`，只暂存这两份文件后执行 `git diff --cached --check`。最终结果见提交版本；其他并行任务文件不在 11A 暂存范围内。
