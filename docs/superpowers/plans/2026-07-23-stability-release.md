# Meshlink Stability Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付一个状态可信的稳定修复版，使节点列表、断线重连、断开连接、退出网络、官方 Hub 入口和远程桌面诊断符合已批准的产品规格。

**Architecture:** 新增共享网络状态契约，把“服务进程运行”和“组网连接可用”分离；Agent 负责产生可信状态，Onboarding 负责将运行状态投影为用户设备列表，桌面端和网页端只消费该投影。退出网络通过统一生命周期服务停止并卸载客户端服务，再清理受管身份材料；官方 Hub 通过默认关闭的产品开关控制可见性。

**Tech Stack:** Go 1.26、标准库、现有 TCP/TLS Agent、`github.com/lxn/walk` Windows 桌面 UI、嵌入式 HTML/CSS/JavaScript、Go `testing`。

## Global Constraints

- 不接入 P2P 打洞或点对点数据传输。
- 不上线官方 Relay 数据面、订阅、计费或付费能力。
- 不替换现有自建 Hub 转发架构。
- 正式版默认隐藏官方 Hub；内部测试仅通过 `MESHLINK_ENABLE_OFFICIAL_HUB_MVP=1` 显示。
- Hub、Relay、Control 等基础设施角色不得进入普通节点列表或设备数量统计。
- “断开连接”保留身份和配置；“退出网络”卸载自动启动服务并删除本机加入身份。
- 不新增第三方依赖。
- 保持现有 JSON 字段向后兼容；旧状态文件缺少 `network_state` 时必须保守映射。
- 所有实现采用测试先行，并按任务独立提交。

---

## File Map

### Shared contracts

- Create: `internal/networkstate/state.go` — 网络状态枚举、兼容映射和中文显示文本。
- Create: `internal/networkstate/state_test.go` — 网络状态兼容与在线判定测试。
- Modify: `internal/proto/roster.go` — 基础设施模式判定。
- Create: `internal/proto/roster_test.go` — Hub/Relay/Control 角色判定测试。

### Agent and device projection

- Modify: `internal/agent/status.go` — 保存独立网络状态、节点模式及全量离线操作。
- Modify: `internal/agent/status_test.go` — 断线、Roster 替换与基础设施过滤测试。
- Modify: `internal/agent/agent.go` — 按 Hub/Spoke 模式初始化网络状态。
- Modify: `internal/agent/spoke.go` — 首连、断线、重连和重新同步状态流。
- Modify: `internal/agent/hub.go` — 为 Roster 节点写入明确模式。
- Modify: `internal/onboarding/status.go` — 只输出真实设备，并返回全局网络状态。
- Modify: `internal/onboarding/device_admin_test.go` — 设备投影与旧状态兼容测试。

### Leave lifecycle

- Create: `internal/onboarding/leave.go` — 仅清理受管 Spoke 身份、配置和状态。
- Create: `internal/onboarding/leave_test.go` — 清理、幂等、Hub 保护和路径边界测试。
- Create: `internal/networklifecycle/leave.go` — 停止/卸载服务并调用本地身份清理。
- Create: `internal/networklifecycle/leave_test.go` — 生命周期顺序和失败传播测试。
- Modify: `internal/ui/server.go` — 新增退出网络 API。
- Modify: `internal/ui/server_test.go` — 退出 API 与空节点列表测试。

### Product flag and user interfaces

- Create: `internal/productflags/flags.go` — 官方 Hub 内测开关。
- Create: `internal/productflags/flags_test.go` — 默认关闭及显式开启测试。
- Modify: `cmd/mesh-desktop/main_windows.go` — 桌面端按钮、确认、状态、清理、滚动和官方 Hub 可见性。
- Modify: `cmd/mesh-desktop/main_windows_test.go` — 桌面端静态契约和纯函数测试。
- Modify: `internal/ui/static/index.html` — 网页端断开/重连/退出操作和默认隐藏官方 Hub。
- Modify: `internal/ui/static/app.js` — 网页端状态轮询、确认退出、重连和功能开关。
- Modify: `internal/ui/static/style.css` — 隐藏规则和邀请链接滚动边界。
- Modify: `internal/ui/server.go` — `/api/info` 返回产品能力开关。
- Modify: `internal/ui/server_test.go` — 正式版/测试版可见性契约。

### Diagnostics and acceptance

- Modify: `internal/diagnose/diagnose.go` — 网络或设备离线时终止 RDP 端口探测。
- Modify: `internal/diagnose/diagnose_test.go` — 离线前置判断测试。
- Modify: `internal/ui/server_test.go` — RDP API 离线结果测试。
- Create: `docs/qa/stability-release-acceptance-2026-07-23.md` — AC-01 至 AC-12 的验收证据索引。

## Spec Coverage

| Acceptance | Implemented by | Verified by |
|---|---|---|
| AC-01 邀请信息完整显示和复制 | Tasks 5-6 | Task 8 |
| AC-02 Hub/Relay 不进入节点列表 | Tasks 1-2 | Task 8 |
| AC-03 Hub 断开后全部离线 | Tasks 1-2 | Task 8 |
| AC-04 自动重连后刷新完整状态 | Task 2 | Task 8 |
| AC-05 重连失败不恢复虚假在线 | Tasks 1-2 | Task 8 |
| AC-06 断开连接保留身份 | Tasks 3、5-6 | Task 8 |
| AC-07 退出网络彻底清理 | Tasks 3、5-6 | Task 8 |
| AC-08 取消退出不改变状态 | Tasks 5-6 | Task 8 |
| AC-09 正式版隐藏官方 Hub | Tasks 4-6 | Task 8 |
| AC-10 测试开关显示官方 Hub | Tasks 4-6 | Task 8 |
| AC-11 离线诊断不探测 RDP | Task 7 | Task 8 |
| AC-12 状态自动刷新 | Tasks 5-6 | Task 8 |

---

### Task 1: Shared Network State and Infrastructure Role Contract

**Files:**
- Create: `internal/networkstate/state.go`
- Create: `internal/networkstate/state_test.go`
- Modify: `internal/proto/roster.go`
- Create: `internal/proto/roster_test.go`
- Modify: `internal/agent/status.go`
- Modify: `internal/agent/status_test.go`

**Interfaces:**
- Produces: `networkstate.State`, constants `NotJoined`, `Disconnected`, `Connecting`, `Reconnecting`, `Connected`.
- Produces: `networkstate.Effective(raw State, processState string) State` and `networkstate.IsOnline(State) bool`.
- Produces: `proto.IsInfrastructureMode(mode string) bool`.
- Produces: `statusStore.setNetworkState(networkstate.State)` and `statusStore.markAllPeersOffline()`.
- Produces: `RuntimeStatus.NetworkState` and `PeerStatus.Mode` JSON fields used by later tasks.

- [ ] **Step 1: Write failing shared-contract tests**

Add table tests that prove the default is conservative while preserving old running status compatibility:

```go
func TestEffectiveStateBackfillsLegacyRuntimeStatus(t *testing.T) {
	tests := []struct {
		raw     State
		process string
		want    State
	}{
		{raw: Connected, process: "running", want: Connected},
		{raw: "", process: "running", want: Connected},
		{raw: "", process: "stopped", want: Disconnected},
		{raw: "unexpected", process: "running", want: Disconnected},
	}
	for _, tc := range tests {
		if got := Effective(tc.raw, tc.process); got != tc.want {
			t.Fatalf("Effective(%q, %q) = %q, want %q", tc.raw, tc.process, got, tc.want)
		}
	}
}

func TestInfrastructureModes(t *testing.T) {
	for _, mode := range []string{"hub", "relay", "control", "HUB"} {
		if !IsInfrastructureMode(mode) {
			t.Fatalf("mode %q must be infrastructure", mode)
		}
	}
	if IsInfrastructureMode("spoke") {
		t.Fatal("spoke must remain a user device")
	}
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/networkstate ./internal/proto`

Expected: FAIL because the package and role helper do not exist.

- [ ] **Step 3: Implement the shared state and role contracts**

Create the exact state API:

```go
package networkstate

import "strings"

type State string

const (
	NotJoined    State = "not_joined"
	Disconnected State = "disconnected"
	Connecting   State = "connecting"
	Reconnecting State = "reconnecting"
	Connected    State = "connected"
)

func (s State) Valid() bool {
	switch s {
	case NotJoined, Disconnected, Connecting, Reconnecting, Connected:
		return true
	default:
		return false
	}
}

func Effective(raw State, processState string) State {
	if raw.Valid() {
		return raw
	}
	if raw == "" && strings.EqualFold(strings.TrimSpace(processState), "running") {
		return Connected
	}
	return Disconnected
}

func IsOnline(state State) bool { return state == Connected }

func LabelCN(state State) string {
	switch state {
	case NotJoined:
		return "未加入"
	case Disconnected:
		return "已断开"
	case Connecting:
		return "连接中"
	case Reconnecting:
		return "重连中"
	case Connected:
		return "已连接"
	default:
		return "已断开"
	}
}
```

In `internal/proto/roster.go`, add:

```go
func IsInfrastructureMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "hub", "relay", "control":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Add status-store failure tests**

Extend `internal/agent/status_test.go` with a Roster containing one Hub and two Spokes. Assert that Hub is absent, the two Spokes are present, `markAllPeersOffline` preserves both records but clears online/path state, and a later Roster containing only one Spoke removes the stale peer.

```go
func TestStatusStoreDropsInfrastructureAndMarksAllPeersOffline(t *testing.T) {
	store := newStatusStore("", NodeStatus{NodeID: "desk", Mode: "spoke"})
	store.setState("running")
	store.setNetworkState(networkstate.Connected)
	store.applyRoster(proto.Roster{Nodes: []proto.RosterNode{
		{NodeID: "hub", Mode: "hub", Status: PeerStatusOnline},
		{NodeID: "laptop", Mode: "spoke", Status: PeerStatusOnline},
		{NodeID: "office", Mode: "spoke", Status: PeerStatusOnline},
	}})
	if findAgentPeerStatus(store.snapshot(), "hub") != nil {
		t.Fatal("hub must not enter peer status")
	}
	store.markAllPeersOffline()
	status := store.snapshot()
	if status.NetworkState != networkstate.Reconnecting {
		t.Fatalf("network state = %q, want reconnecting", status.NetworkState)
	}
	for _, id := range []string{"laptop", "office"} {
		peer := findAgentPeerStatus(status, id)
		if peer == nil || peer.Status != PeerStatusOffline || peer.PathState != p2p.PathStateOffline {
			t.Fatalf("peer %q = %+v, want offline", id, peer)
		}
	}
}
```

- [ ] **Step 5: Implement the status-store contract**

Add `NetworkState networkstate.State \`json:"network_state,omitempty"\`` to `RuntimeStatus`, `Mode string \`json:"mode,omitempty"\`` to `PeerStatus`, and implement the two store methods. `markAllPeersOffline` must set one shared timestamp, normalize every peer, set `NetworkState` to `Reconnecting`, and write once. `applyRoster` must skip `proto.IsInfrastructureMode(node.Mode)` and copy `Mode` into each retained peer.

- [ ] **Step 6: Run focused tests**

Run: `go test ./internal/networkstate ./internal/proto ./internal/agent`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/networkstate internal/proto/roster.go internal/proto/roster_test.go internal/agent/status.go internal/agent/status_test.go
git commit -m "CORE add explicit network state contract"
```

---

### Task 2: Agent Reconnect State and User Device Projection

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/spoke.go`
- Modify: `internal/agent/hub.go`
- Modify: `internal/agent/status_test.go`
- Modify: `internal/onboarding/status.go`
- Modify: `internal/onboarding/device_admin_test.go`

**Interfaces:**
- Consumes: `networkstate` constants and `proto.IsInfrastructureMode` from Task 1.
- Produces: `DeviceList.NetworkState networkstate.State`.
- Produces: device projection where infrastructure nodes are excluded and no active config returns `NotJoined` with an empty list.

- [ ] **Step 1: Write failing device-projection tests**

Add tests for these exact cases:

```go
func TestDevicesHideInfrastructureAndRespectReconnectState(t *testing.T) {
	dir := t.TempDir()
	writeSpokeConfigForDeviceTest(t, dir, "desk")
	writeRuntimeStatusForDeviceTest(t, dir, `{
  "state":"running",
  "network_state":"reconnecting",
  "self":{"node_id":"desk","mode":"spoke","virtual_ip":"10.77.0.2"},
  "peers":[
    {"node_id":"hub","mode":"hub","status":"online"},
    {"node_id":"office","mode":"spoke","status":"offline","virtual_ip":"10.77.0.3"}
  ]
}`)
	devices, err := (Manager{BaseDir: dir}).Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	if devices.NetworkState != networkstate.Reconnecting {
		t.Fatalf("network state = %q", devices.NetworkState)
	}
	if findDeviceSummary(devices, "hub") != nil {
		t.Fatal("hub must be hidden")
	}
	if peer := findDeviceSummary(devices, "office"); peer == nil || peer.Status != "offline" {
		t.Fatalf("office = %+v, want offline", peer)
	}
}

func TestDevicesWithoutActiveConfigAreNotJoinedAndEmpty(t *testing.T) {
	devices, err := (Manager{BaseDir: t.TempDir()}).Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	if devices.NetworkState != networkstate.NotJoined || len(devices.Nodes) != 0 {
		t.Fatalf("devices = %+v", devices)
	}
}
```

Add the concrete fixture helpers in the same test file:

```go
func writeSpokeConfigForDeviceTest(t *testing.T, dir, nodeID string) {
	t.Helper()
	path := filepath.Join(dir, "configs", "active.json")
	cfg := config.Config{
		NodeID: nodeID, Mode: "spoke", Connect: "example.com:8443",
		CAFile: "../certs/ca.pem", CertFile: "../certs/" + nodeID + ".pem",
		KeyFile: "../certs/" + nodeID + "-key.pem", VirtualIP: "10.77.0.2",
		Device: config.DeviceConfig{Type: "null"},
	}
	if err := writePrettyJSON(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func writeRuntimeStatusForDeviceTest(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "configs", "logs", "mesh-agent.status.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run projection tests and verify failure**

Run: `go test ./internal/onboarding -run 'TestDevices(HideInfrastructure|WithoutActiveConfig)' -count=1`

Expected: FAIL because `DeviceList` has no network state and Hub is still projected.

- [ ] **Step 3: Implement Agent state transitions**

Update Agent behavior to follow this sequence:

```go
// hub startup
a.status.setNetworkState(networkstate.Connected)

// spoke startup before first dial
a.status.setNetworkState(networkstate.Connecting)

// after every failed/closed spoke session
a.status.markAllPeersOffline() // also sets Reconnecting

// only after a complete roster has been applied
a.status.applyRoster(roster)
a.status.setNetworkState(networkstate.Connected)
```

Remove the Hub `upsertPeer`/`touchPeer` bookkeeping from `connectOnce`; the control connection is represented by `NetworkState`, not as a user device. Preserve exponential backoff. On context cancellation set `Disconnected` before returning. In Hub peer registration set `Mode: "spoke"`, and copy each peer mode into the broadcast Roster.

- [ ] **Step 4: Implement the device projection**

Add `NetworkState` and `Mode` to the onboarding runtime structs. Before reading runtime status, return `DeviceList{NetworkState: networkstate.NotJoined, Nodes: []DeviceSummary{}}` when `active.json` does not exist. Use `networkstate.Effective(status.NetworkState, status.State)` for compatibility. Add the self node only when its mode is not infrastructure; skip every peer whose mode is infrastructure; force all non-disabled nodes offline whenever the effective network state is not `Connected`.

The result must be constructed with this invariant:

```go
effective := networkstate.Effective(status.NetworkState, status.State)
onlineAllowed := networkstate.IsOnline(effective)
if !onlineAllowed && peerStatus == "online" {
	peerStatus = "offline"
}
return DeviceList{
	UpdatedAt:    status.UpdatedAt,
	NetworkState: effective,
	Nodes:        nodes,
}, nil
```

- [ ] **Step 5: Add legacy compatibility coverage**

Keep the existing old runtime JSON tests and add an assertion that a legacy `state:"running"` status maps to `Connected`. Add a Hub-self test proving that Hub mode omits self but retains real Spoke peers.

- [ ] **Step 6: Run focused tests**

Run: `go test ./internal/agent ./internal/onboarding -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/agent.go internal/agent/spoke.go internal/agent/hub.go internal/onboarding/status.go internal/onboarding/device_admin_test.go
git commit -m "FIX make reconnect and device status trustworthy"
```

---

### Task 3: Disconnect and Leave-Network Lifecycle

**Files:**
- Create: `internal/onboarding/leave.go`
- Create: `internal/onboarding/leave_test.go`
- Create: `internal/networklifecycle/leave.go`
- Create: `internal/networklifecycle/leave_test.go`
- Modify: `internal/ui/server.go`
- Modify: `internal/ui/server_test.go`

**Interfaces:**
- Produces: `onboarding.Manager.LeaveNetwork(serviceName string) error`.
- Produces: `networklifecycle.NetworkLeaver`, `networklifecycle.ServiceOps`, `networklifecycle.Leave(NetworkLeaver, string) error`, and `networklifecycle.LeaveWithOps(NetworkLeaver, string, ServiceOps) error`.
- Produces: `POST /api/onboarding/leave` accepting `{ "service_name": "MeshlinkAgent" }`.

- [ ] **Step 1: Write failing local-cleanup tests**

Build a managed Spoke fixture with `configs/active.json`, the three referenced identity files, and one runtime status file. Verify that all are removed, unrelated logs remain, and the manager returns an empty `NotJoined` device list. Also test that Hub mode is rejected without deleting any Hub file and that a second leave is successful.

```go
func TestLeaveNetworkClearsManagedSpokeIdentity(t *testing.T) {
	dir := t.TempDir()
	mgr := Manager{BaseDir: dir}
	writeManagedSpokeFixture(t, dir, "desk")
	if err := mgr.LeaveNetwork("MeshlinkAgent"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "configs", "active.json"),
		filepath.Join(dir, "configs", "logs", "MeshlinkAgent.status.json"),
		filepath.Join(dir, "certs", "ca.pem"),
		filepath.Join(dir, "certs", "desk.pem"),
		filepath.Join(dir, "certs", "desk-key.pem"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("managed file still exists: %s", path)
		}
	}
	if err := mgr.LeaveNetwork("MeshlinkAgent"); err != nil {
		t.Fatalf("second leave must be idempotent: %v", err)
	}
}
```

Create the fixture explicitly:

```go
func writeManagedSpokeFixture(t *testing.T, dir, nodeID string) {
	t.Helper()
	writeSpokeConfigForLeaveTest(t, dir, nodeID)
	for path, body := range map[string]string{
		filepath.Join(dir, "certs", "ca.pem"):                 "ca",
		filepath.Join(dir, "certs", nodeID+".pem"):           "cert",
		filepath.Join(dir, "certs", nodeID+"-key.pem"):       "key",
		filepath.Join(dir, "configs", "logs", "MeshlinkAgent.status.json"): `{\"state\":\"running\"}`,
		filepath.Join(dir, "logs", "audit.jsonl"):            "keep\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeSpokeConfigForLeaveTest(t *testing.T, dir, nodeID string) {
	t.Helper()
	cfg := config.Config{
		NodeID: nodeID, Mode: "spoke", Connect: "example.com:8443",
		CAFile: "../certs/ca.pem", CertFile: "../certs/" + nodeID + ".pem",
		KeyFile: "../certs/" + nodeID + "-key.pem", VirtualIP: "10.77.0.2",
		Device: config.DeviceConfig{Type: "null"},
	}
	if err := writePrettyJSON(filepath.Join(dir, "configs", "active.json"), cfg); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run local-cleanup tests and verify failure**

Run: `go test ./internal/onboarding -run TestLeaveNetwork -count=1`

Expected: FAIL because `LeaveNetwork` does not exist.

- [ ] **Step 3: Implement bounded identity cleanup**

`LeaveNetwork` must load `active.json`, reject modes other than `spoke`, resolve relative CA/cert/key paths against the config directory, and remove only files contained by the manager base directory. Use a helper that compares `filepath.Rel(baseDir, candidate)` and rejects `..` or absolute escapes. Delete the runtime status first so a partial failure can never leave stale online nodes visible; then remove key, certificate, CA, and active config. Missing files are success.

Do not delete the whole `certs`, `configs`, or `logs` directory, and do not delete unrelated audit or application logs.

- [ ] **Step 4: Write failing lifecycle-order tests**

Use function fields instead of real Windows services:

```go
type fakeLeaveManager struct {
	leave func(string) error
}

func (f fakeLeaveManager) LeaveNetwork(serviceName string) error {
	return f.leave(serviceName)
}

func TestLeaveStopsThenUninstallsThenClearsIdentity(t *testing.T) {
	var calls []string
	ops := ServiceOps{
		Status: func(string) (winservice.ServiceStatus, error) {
			return winservice.ServiceStatus{Installed: true, State: "running"}, nil
		},
		Stop: func(string) error { calls = append(calls, "stop"); return nil },
		Uninstall: func(string) error { calls = append(calls, "uninstall"); return nil },
	}
	manager := fakeLeaveManager{leave: func(string) error {
		calls = append(calls, "clear")
		return nil
	}}
	if err := LeaveWithOps(manager, "MeshlinkAgent", ops); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(calls, ","), "stop,uninstall,clear"; got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}
```

- [ ] **Step 5: Implement service lifecycle orchestration**

`ServiceOps` must default to `winservice.Status`, `winservice.Stop`, and `winservice.Uninstall`. If the service is installed and not already stopped, stop it. Then uninstall it to prevent restart after reboot. Only then clear identity. Propagate the first error and never report success after a partial failure.

Use these exact public boundaries:

```go
type NetworkLeaver interface {
	LeaveNetwork(serviceName string) error
}

type ServiceOps struct {
	Status    func(string) (winservice.ServiceStatus, error)
	Stop      func(string) error
	Uninstall func(string) error
}

func Leave(manager NetworkLeaver, serviceName string) error {
	return LeaveWithOps(manager, serviceName, ServiceOps{
		Status: winservice.Status, Stop: winservice.Stop, Uninstall: winservice.Uninstall,
	})
}
```

- [ ] **Step 6: Add and test the local API**

Register `POST /api/onboarding/leave`. The handler decodes `service_name`, calls the shared lifecycle, and returns `{ "ok": true, "devices": { "network_state": "not_joined", "nodes": [] } }` only after completion. Add an API test with an already-uninstalled service and a managed Spoke fixture.

- [ ] **Step 7: Run focused tests**

Run: `go test ./internal/onboarding ./internal/networklifecycle ./internal/ui -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/onboarding/leave.go internal/onboarding/leave_test.go internal/networklifecycle internal/ui/server.go internal/ui/server_test.go
git commit -m "FIX separate disconnect from leaving a network"
```

---

### Task 4: Official Hub Product Flag

**Files:**
- Create: `internal/productflags/flags.go`
- Create: `internal/productflags/flags_test.go`
- Modify: `internal/ui/server.go`
- Modify: `internal/ui/server_test.go`

**Interfaces:**
- Produces: `productflags.OfficialHubMVPEnabled() bool`.
- Produces: `/api/info.features.official_hub_mvp`.
- Consumed by: desktop and web UI tasks.

- [ ] **Step 1: Write failing flag tests**

```go
func TestOfficialHubMVPDefaultsOffAndRequiresExplicitTrue(t *testing.T) {
	t.Setenv(OfficialHubMVPEnv, "")
	if OfficialHubMVPEnabled() {
		t.Fatal("official Hub must default off")
	}
	for _, value := range []string{"1", "true", "TRUE"} {
		t.Setenv(OfficialHubMVPEnv, value)
		if !OfficialHubMVPEnabled() {
			t.Fatalf("value %q must enable MVP", value)
		}
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./internal/productflags -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the flag**

Use a strict allowlist so arbitrary non-empty text cannot expose the feature:

```go
package productflags

import (
	"os"
	"strings"
)

const OfficialHubMVPEnv = "MESHLINK_ENABLE_OFFICIAL_HUB_MVP"

func OfficialHubMVPEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(OfficialHubMVPEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Expose the capability in `/api/info`**

Add:

```go
"features": map[string]bool{
	"official_hub_mvp": productflags.OfficialHubMVPEnabled(),
},
```

Test both default false and `MESHLINK_ENABLE_OFFICIAL_HUB_MVP=1` true.

- [ ] **Step 5: Run focused tests**

Run: `go test ./internal/productflags ./internal/ui -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/productflags internal/ui/server.go internal/ui/server_test.go
git commit -m "PRODUCT hide official Hub behind an explicit test flag"
```

---

### Task 5: Windows Desktop Behavior

**Files:**
- Modify: `cmd/mesh-desktop/main_windows.go`
- Modify: `cmd/mesh-desktop/main_windows_test.go`

**Interfaces:**
- Consumes: `networklifecycle.Leave`, `networkstate.LabelCN`, `productflags.OfficialHubMVPEnabled`, and `DeviceList.NetworkState`.
- Produces: desktop actions `disconnectNetwork`, `reconnectNetwork`, `exitNetwork`, and `clearCurrentNetworkUI`.

- [ ] **Step 1: Update static UI tests first**

Replace the old official-Hub-visible assertion with an assertion that the button contains `Visible: productflags.OfficialHubMVPEnabled()`. Update the invite-output assertion to require `VScroll: true`. Add source-contract assertions for three distinct actions and the confirmation copy:

```go
for _, want := range []string{
	`PushButton{Text: "断开连接", OnClicked: a.disconnectNetwork`,
	`PushButton{Text: "重新连接", OnClicked: a.reconnectNetwork`,
	`PushButton{Text: "退出网络", OnClicked: a.exitNetwork`,
	`退出后需要重新使用邀请码才能加入`,
	`networklifecycle.Leave`,
} {
	if !strings.Contains(src, want) {
		t.Fatalf("desktop lifecycle UI missing %s", want)
	}
}
```

- [ ] **Step 2: Run desktop tests and verify failure**

Run: `go test ./cmd/mesh-desktop -count=1`

Expected: FAIL on the new controls, scroll property, and feature flag.

- [ ] **Step 3: Implement invitation scrolling and Hub visibility**

Set the invitation widget to:

```go
TextEdit{
	AssignTo: &a.inviteOutput,
	ReadOnly: true,
	VScroll: true,
	MinSize: Size{0, 76},
	MaxSize: Size{10000, 96},
	ColumnSpan: 4,
}
```

Set the official Hub button's `Visible` property from `productflags.OfficialHubMVPEnabled()`; keep the internal dialog code compiled for test builds.

- [ ] **Step 4: Implement distinct connection actions**

Add these behaviors:

```go
func (a *desktopApp) disconnectNetwork() {
	if err := winservice.Stop(a.currentServiceName()); err != nil && !isServiceNotRunningError(err) {
		a.fail("断开连接失败", err)
		return
	}
	a.onboardingState.SetText("状态：已断开")
	a.loadMeshStatus()
}

func (a *desktopApp) reconnectNetwork() {
	if err := a.installAndStartAgent(a.currentConfigPath()); err != nil {
		a.onboardingState.SetText("状态：重连失败")
		a.fail("重新连接失败", err)
		return
	}
	a.onboardingState.SetText("状态：已连接")
	a.loadMeshStatus()
}
```

`exitNetwork` must show a Yes/No confirmation. On Yes, call `networklifecycle.Leave`, clear `LastConfigPath`, clear list selection/model/details immediately, set the state to “未加入”, and then reload the empty device projection. On No, perform no action.

- [ ] **Step 5: Make automatic refresh display network state**

When `loadMeshStatus` receives a successful `DeviceList`, trust it even when `Nodes` is empty; do not fall back to a stale runtime file based on `len(nodes)`. Include `networkstate.LabelCN(devices.NetworkState)` in `meshSummary` and `quickState`. Preserve the existing two-second refresh loop.

- [ ] **Step 6: Run desktop tests and build**

Run: `go test ./cmd/mesh-desktop -count=1`

Expected: PASS.

Run: `go build ./cmd/mesh-desktop`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/mesh-desktop/main_windows.go cmd/mesh-desktop/main_windows_test.go
git commit -m "UI fix desktop network lifecycle and invite display"
```

---

### Task 6: Web UI Behavior and Automatic Refresh

**Files:**
- Modify: `internal/ui/static/index.html`
- Modify: `internal/ui/static/app.js`
- Modify: `internal/ui/static/style.css`
- Modify: `internal/ui/server_test.go`

**Interfaces:**
- Consumes: `POST /api/onboarding/leave`, `DeviceList.NetworkState`, and `/api/info.features.official_hub_mvp`.
- Produces: web actions `disconnectNetwork`, `reconnectNetwork`, `exitNetwork`, and `startStatusPolling`.

- [ ] **Step 1: Add failing static-contract tests**

Read embedded HTML/JS/CSS and assert:

```go
for _, want := range []string{
	`id="disconnectNetwork"`,
	`id="reconnectNetwork"`,
	`data-official-hub hidden`,
	`window.confirm("退出后需要重新使用邀请码才能加入。确定退出网络吗？")`,
	`/api/onboarding/leave`,
	`startStatusPolling`,
} {
	if !strings.Contains(html+js, want) {
		t.Fatalf("web lifecycle contract missing %q", want)
	}
}
if !strings.Contains(css, `[hidden]`) || !strings.Contains(css, `display: none !important`) {
	t.Fatal("hidden official Hub content must not be overridden by active view styles")
}
```

- [ ] **Step 2: Run UI tests and verify failure**

Run: `go test ./internal/ui -count=1`

Expected: FAIL on new lifecycle and hidden-feature contracts.

- [ ] **Step 3: Implement default-hidden official Hub UI**

Add `data-official-hub hidden` to both the entry button and the official Hub view. Add a global CSS rule:

```css
[hidden] {
  display: none !important;
}
```

In `loadInfo`, set `state.officialHubEnabled` from `info.features.official_hub_mvp` and reveal only matching `[data-official-hub]` elements when true. Skip `loadOfficialHubState()` and `loadSubscriptionExperience()` entirely when false.

- [ ] **Step 4: Implement web connection actions**

Add “断开连接”, “重新连接”, and “退出网络” buttons. Disconnect uses the existing service stop action and keeps devices offline. Reconnect uses the existing install/start path with the current config. Exit requires exact confirmation copy and calls the dedicated leave API; only on success clear selected device state, render an empty list, and set `clientStatus` to “未加入”.

- [ ] **Step 5: Display network state and poll safely**

Map states to Chinese:

```js
function networkStateText(value) {
  return {
    not_joined: "未加入",
    disconnected: "已断开",
    connecting: "连接中",
    reconnecting: "重连中",
    connected: "已连接",
  }[value] || "已断开";
}
```

`refreshDevices` must update `clientStatus` from `data.devices.network_state`. Start one two-second interval after initial load; guard against overlapping refreshes with a boolean and refresh both service and devices. State changes must appear without clicking “刷新列表”.

- [ ] **Step 6: Bound invitation display**

Keep the existing textarea and add:

```css
#accessLink {
  min-height: 86px;
  max-height: 160px;
  overflow-y: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
```

The existing copy action must continue reading the textarea's full `.value`.

- [ ] **Step 7: Run focused tests**

Run: `go test ./internal/ui -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/ui/static/index.html internal/ui/static/app.js internal/ui/static/style.css internal/ui/server_test.go
git commit -m "UI align web network status and hidden Hub behavior"
```

---

### Task 7: Offline-First RDP Diagnostics

**Files:**
- Modify: `internal/diagnose/diagnose.go`
- Modify: `internal/diagnose/diagnose_test.go`
- Modify: `internal/ui/server_test.go`

**Interfaces:**
- Consumes: `RDPCheckRequest.TunnelStatus`.
- Produces: deterministic offline checks that never dial the target when network/device state is offline.

- [ ] **Step 1: Replace the environment-dependent test with a dial-proof test seam**

Add an internal package variable:

```go
var dialRDP = func(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}
```

In the test, replace it temporarily with a function that records calls and fails the test if invoked for an offline request.

```go
func TestCheckRDPTargetStopsBeforeDialWhenTunnelOffline(t *testing.T) {
	original := dialRDP
	t.Cleanup(func() { dialRDP = original })
	called := false
	dialRDP = func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("must not dial")
	}
	check := CheckRDPTarget(RDPCheckRequest{
		Target: "10.77.0.9", TargetDevice: "office-pc", TunnelStatus: "offline",
	})
	if called || check.Status != Fail || !strings.Contains(check.Detail, "网络未连接") {
		t.Fatalf("check = %+v, called = %v", check, called)
	}
}
```

- [ ] **Step 2: Run diagnostics tests and verify failure**

Run: `go test ./internal/diagnose ./internal/ui -run 'RDP|RDPTarget' -count=1`

Expected: FAIL because the current function dials before evaluating `TunnelStatus`.

- [ ] **Step 3: Implement offline preconditions**

Before dialing, normalize the tunnel status. Target parsing may still occur so the response can retain IP and port context. For `offline`, `disconnected`, `not_joined`, `connecting`, `reconnecting`, or `stopped`, return Fail with “网络未连接” and a reconnect instruction. Keep target/device/IP/port context in the detail. Only `online` or `connected` may proceed to the TCP probe; an empty/unknown status preserves legacy probing behavior.

Use `dialRDP` in place of a local `net.Dialer` so the test proves no probe occurred.

- [ ] **Step 4: Update API expectations**

Update `TestRDPDiagnosticsAPIIncludesUserContext` to require a failing response containing “网络未连接”, “目标设备：office-pc”, and “隧道状态：offline”. Remove the old expectation that offline users should be told to enable RDP before reconnecting the network.

- [ ] **Step 5: Run focused tests**

Run: `go test ./internal/diagnose ./internal/ui -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/diagnose/diagnose.go internal/diagnose/diagnose_test.go internal/ui/server_test.go
git commit -m "FIX stop RDP probes when the mesh is offline"
```

---

### Task 8: Integrated Acceptance and Evidence

**Files:**
- Create: `docs/qa/stability-release-acceptance-2026-07-23.md`
- Modify only when verification exposes a defect: files owned by Tasks 1-7.

**Interfaces:**
- Consumes: all prior task outputs.
- Produces: one evidence row for every AC-01 through AC-12 and a disclosed environment gap for any unavailable real-machine scenario.

- [ ] **Step 1: Run the complete automated suite**

Run: `go test -count=1 ./...`

Expected: PASS with no failing packages.

- [ ] **Step 2: Run static analysis**

Run: `go vet ./...`

Expected: PASS with no diagnostics.

- [ ] **Step 3: Produce a development build**

Run: `powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Mode Development`

Expected: PASS and produce the existing Windows/Linux binaries without enabling the official Hub flag.

- [ ] **Step 4: Verify formal/default UI behavior on Windows**

Start the development desktop build with `MESHLINK_ENABLE_OFFICIAL_HUB_MVP` unset. Record that the official Hub entry is absent, invitation output scrolls, and the three connection actions are distinct. Repeat with the variable set to `1` and record that the internal official Hub entry appears.

- [ ] **Step 5: Verify disconnect, reconnect, and leave on Windows**

Using one Hub and at least one Spoke:

1. Disconnect the Hub or block its TCP path and record that the Spoke enters “重连中” and all remote nodes become offline after disconnect detection.
2. Restore the path and record that a fresh Roster repopulates online devices.
3. Choose “断开连接” and record that identity remains and manual reconnect succeeds without an invite.
4. Choose “退出网络”, confirm, restart the application and Windows service manager, and record that no old service/config reconnects and the list remains empty.
5. Rejoin with a new invite and record successful recovery.

- [ ] **Step 6: Verify diagnostics**

While disconnected, run RDP diagnostics and record the immediate “网络未连接” result. While connected to an online target, run the same diagnostic and record that normal RDP probing still occurs.

- [ ] **Step 7: Write the acceptance evidence file**

Create a table with columns `AC`, `Result`, `Evidence`, and `Risk`. Every AC-01 through AC-12 must be present. Use only `PASS`, `FAIL`, or `BLOCKED` results. A missing second Windows machine or controllable Hub outage must be recorded as `BLOCKED`; do not convert simulated or unit evidence into a real-environment pass.

- [ ] **Step 8: Re-run the complete suite after any acceptance fix**

Run: `go test -count=1 ./...`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add docs/qa/stability-release-acceptance-2026-07-23.md
git commit -m "QA record stability release acceptance evidence"
```

---

## Delivery Gate

The implementation is complete only when:

1. All eight tasks are committed in dependency order.
2. `go test -count=1 ./...` and `go vet ./...` pass.
3. The development build succeeds with the official Hub hidden by default.
4. AC-01 through AC-12 each have explicit evidence.
5. Any unavailable real Windows/NAT scenario is disclosed as blocked rather than reported as passed.
6. The product owner reviews the evidence and gives final acceptance.
