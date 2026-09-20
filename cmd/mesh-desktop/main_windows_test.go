//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/diagnose"
	"meshlink/internal/networkstate"
	"meshlink/internal/onboarding"
	"meshlink/internal/p2p"
	"meshlink/internal/winservice"
)

func TestDesktopUsesDefaultServiceAndActiveConfigWithoutAdvancedControls(t *testing.T) {
	a := &desktopApp{}
	if got := a.currentServiceName(); got != winservice.DefaultName {
		t.Fatalf("currentServiceName() = %q, want %q", got, winservice.DefaultName)
	}
	if got := defaultConfigPath(`C:\Meshlink`); got != `C:\Meshlink\configs\active.json` {
		t.Fatalf("defaultConfigPath() = %q, want active config path", got)
	}
}

func TestDesktopMainWindowOnlyExposesSimpleModeTabs(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	mainWindowSrc := src
	if index := strings.Index(src, `func (a *desktopApp) showAdvancedSettingsDialog()`); index >= 0 {
		mainWindowSrc = src[:index]
	}
	for _, want := range []string{
		`Text: "我有公网 IP，创建服务器"`,
		`Text: "我没有公网 IP，使用官方 Hub", OnClicked: a.showOfficialHubMVPDialog, Visible: productflags.OfficialHubMVPEnabled()`,
		`Text: "加入已有网络"`,
		`showOfficialHubMVPDialog`,
		`官方 Hub MVP/内测`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop main window missing entry %s", want)
		}
	}
	if strings.Contains(src, `showOfficialHubPlaceholder`) || strings.Contains(src, `官方 Hub 尚未开放`) {
		t.Fatal("desktop official Hub entry should open the MVP dialog, not the old placeholder")
	}
	for _, forbidden := range []string{`我有云服务器，帮我自建中继`, `showSelfRelayWizard`} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("desktop self-hosted flow still exposes Relay surface %q", forbidden)
		}
	}
	for _, hidden := range []string{
		`Title:  "高级设置"`,
		`Title:  "诊断和日志"`,
		`PushButton{Text: "开始诊断"`,
		`PushButton{Text: "读取日志"`,
		`Text:          "未诊断"`,
	} {
		if strings.Contains(mainWindowSrc, hidden) {
			t.Fatalf("desktop main window still exposes %s", hidden)
		}
	}
}

func TestDesktopOfficialHubDialogExposesMVPControlPlaneFields(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`func (a *desktopApp) showOfficialHubMVPDialog()`,
		`官方 Hub MVP/内测`,
		`Hub API 地址`,
		`http://127.0.0.1:18080`,
		`账号邮箱`,
		`账号名称`,
		`网络名称`,
		`本机设备名称`,
		`创建测试账号`,
		`创建官方网络`,
		`加入当前设备`,
		`发送 heartbeat`,
		`刷新设备列表`,
		`当前只是官方 Hub 控制面 MVP，不代表 Relay/P2P 已完成。`,
		`CreateOfficialHubAccount`,
		`CreateOfficialHubNetwork`,
		`CreateOfficialHubInvite`,
		`JoinOfficialHubDevice`,
		`HeartbeatOfficialHubDevice`,
		`ListOfficialHubDevices`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("official Hub dialog missing %s", want)
		}
	}
	for _, misleading := range []string{`官方订阅已开通`, `Relay 数据面已完成`} {
		if strings.Contains(src, misleading) {
			t.Fatalf("official Hub dialog should not contain misleading text %s", misleading)
		}
	}
}

func TestDesktopSelfHostedFlowHasNoRelaySurface(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, forbidden := range []string{
		`func (a *desktopApp) showSelfRelayWizard()`,
		`我有云服务器，帮我自建中继`,
		`Title:     "自建中继部署"`,
		`DeploySelfHostedRelay`,
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("self-hosted desktop still exposes Relay behavior %q", forbidden)
		}
	}
}

func TestDesktopShowsCoordinatorAndDirectStateSeparately(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`Text: "协调服务器："`,
		`AssignTo: &a.coordinatorSummary`,
		`Text: "对端路径："`,
		`AssignTo: &a.p2pPathSummary`,
		`a.coordinatorState = devices.CoordinatorState`,
		`a.p2pListen = devices.P2PListen`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop is missing truthful coordinator/P2P diagnostic %q", want)
		}
	}
}

func TestDesktopConnectionLabelsNeverInferDirectFromOnlineMembership(t *testing.T) {
	node := meshNode{Kind: "peer", Online: true, State: "online"}
	if got := meshNodeConnectionLabel(node); got != "已在线 · 尚未建立直连" {
		t.Fatalf("meshNodeConnectionLabel() = %q, want online membership without inventing a direct session", got)
	}
}

func TestDesktopMembershipStatusStaysOnlineWhilePathIsIdle(t *testing.T) {
	node := meshNode{
		Kind:   "peer",
		Online: true,
		State:  "online",
		ConnectionStatus: p2p.ConnectionStatus{
			PathState: p2p.PathState("idle"),
		},
	}
	if got := meshNodeStatusText(node); got != "在线" {
		t.Fatalf("meshNodeStatusText() = %q, want independent online membership status", got)
	}
	if got := meshNodeConnectionLabel(node); got != "已在线 · 尚未建立直连" {
		t.Fatalf("meshNodeConnectionLabel() = %q, want no inferred direct path", got)
	}
}

func TestDesktopConnectionLabelsUseTruthfulP2PStates(t *testing.T) {
	for state, want := range map[string]string{
		"connected":    "已连接",
		"reconnecting": "重连中",
		"disconnected": "已断开",
	} {
		if got := desktopCoordinatorStateText(state); got != want {
			t.Fatalf("desktopCoordinatorStateText(%q) = %q, want %q", state, got, want)
		}
	}

	tests := []struct {
		name string
		node meshNode
		want string
	}{
		{name: "LAN direct", node: meshNode{Kind: "peer", Online: true, ConnectionStatus: p2p.ConnectionStatus{PathType: p2p.PathType("lan_direct"), PathState: p2p.PathState("lan_direct")}}, want: "局域网直连"},
		{name: "public direct", node: meshNode{Kind: "peer", Online: true, ConnectionStatus: p2p.ConnectionStatus{PathType: p2p.PathType("public_direct"), PathState: p2p.PathState("public_direct")}}, want: "公网直连"},
		{name: "negotiating", node: meshNode{Kind: "peer", Online: true, ConnectionStatus: p2p.ConnectionStatus{PathState: p2p.PathState("punching")}}, want: "正在协商"},
		{name: "waiting", node: meshNode{Kind: "peer", Online: true, ConnectionStatus: p2p.ConnectionStatus{PathState: p2p.PathState("waiting_coordinator")}}, want: "等待协调服务器"},
		{name: "failed without relay", node: meshNode{Kind: "peer", Online: true, ConnectionStatus: p2p.ConnectionStatus{PathState: p2p.PathStateFailed, LastError: "direct_unreachable_no_relay"}}, want: "直连失败 · 本版本未启用中继"},
		{name: "absent", node: meshNode{Kind: "peer", Online: false, ConnectionStatus: p2p.ConnectionStatus{PathState: p2p.PathState("offline_or_unknown")}}, want: "离线或未知"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := meshNodeConnectionLabel(tt.node); got != tt.want {
				t.Fatalf("meshNodeConnectionLabel() = %q, want %q", got, tt.want)
			}
		})
	}

	directDuringOutage := meshNode{
		Kind:             "peer",
		Online:           true,
		CoordinatorState: "reconnecting",
		ConnectionStatus: p2p.ConnectionStatus{PathType: p2p.PathType("lan_direct"), PathState: p2p.PathState("lan_direct")},
	}
	if got := meshNodeConnectionSummary(directDuringOutage); !strings.Contains(got, "协调服务器离线，当前直连不受影响") {
		t.Fatalf("meshNodeConnectionSummary() = %q, want coordinator-outage continuity note", got)
	}
}

func TestJoinOnboardingUsesLocalDeviceName(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`Label{Text: "本机名称", TextColor: muted}`,
		`LineEdit{AssignTo: &a.spokeNodeName`,
		`NodeName:   strings.TrimSpace(a.spokeNodeName.Text()),`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("join flow missing %s", want)
		}
	}
}

func TestDesktopMainWindowHasAboutUpdateMenu(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`MenuItems: []MenuItem`,
		`Text: "帮助"`,
		`Text: "关于 / 检查更新"`,
		`showAboutDialog`,
		`版本：`,
		`更新地址`,
		`更新端口`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop main window missing %s", want)
		}
	}
}

func TestDesktopRemovesAdvancedToolsAndRequestsElevation(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, forbidden := range []string{
		`Text: "工具"`,
		`Text: "高级设置"`,
		`showAdvancedSettingsDialog`,
		`Title:  "服务安装 / 启停"`,
		`Title:  "JSON 配置"`,
		`Title:  "证书工具"`,
		`Title:  "日志和详细诊断"`,
		`PushButton{Text: "读取日志"`,
		`PushButton{Text: "开始诊断"`,
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("desktop still exposes removed advanced tools: %s", forbidden)
		}
	}
	mainStart := strings.Index(src, "func main()")
	runStart := strings.Index(src[mainStart:], "app.run()")
	if !strings.Contains(src[mainStart:mainStart+runStart], "relaunchAsAdministrator()") {
		t.Fatal("desktop must request elevation before opening the UI")
	}
}

func TestInviteOutputHasBoundedHeight(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `TextEdit{AssignTo: &a.inviteOutput, ReadOnly: true, VScroll: true, MinSize: Size{Width: 0, Height: 76}, MaxSize: Size{Width: 10000, Height: 96}, ColumnSpan: 4}`) {
		t.Fatal("invite output should have a bounded height and vertical scrolling")
	}
}

func TestDesktopMainWindowHasOneConnectAction(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`PushButton{Text: "断开连接", OnClicked: a.disconnectNetwork`,
		`Text: "连接", OnClicked: a.connectNetwork`,
		`PushButton{Text: "退出网络", OnClicked: a.exitNetwork`,
		`退出后需要重新使用邀请码才能加入`,
		`networklifecycle.Leave`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop lifecycle UI missing %s", want)
		}
	}
	for _, forbidden := range []string{`OnClicked: a.joinOnboarding`, `OnClicked: a.reconnectNetwork`} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("fresh clients must use the same enrollment-aware connect action: %s", forbidden)
		}
	}
}

func TestCompleteDesktopLeaveDoesNotDestroyIdentityWhenSettingsSaveFails(t *testing.T) {
	leaveCalled := false
	err := completeDesktopLeave(
		desktopSettings{LastConfigPath: `C:\Meshlink\configs\active.json`},
		func(desktopSettings) error { return errors.New("settings are read-only") },
		func() error {
			leaveCalled = true
			return nil
		},
	)
	if err == nil {
		t.Fatal("completeDesktopLeave must report settings persistence failure")
	}
	if leaveCalled {
		t.Fatal("destructive leave must not run before cleared settings are persisted")
	}
}

func TestDesktopRefreshUsesDeviceListNetworkStateEvenWhenEmpty(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`devicesErr == nil {`,
		`desktopNetworkStateText(devices.NetworkState)`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop status refresh missing %s", want)
		}
	}
	if strings.Contains(src, `devicesErr == nil && len(devices.Nodes) > 0`) {
		t.Fatal("desktop status refresh should trust an empty successful DeviceList")
	}
}

func TestDesktopRefreshFailureKeepsNodesButProjectsThemOffline(t *testing.T) {
	original := []meshNode{
		{
			ConnectionStatus: p2p.ConnectionStatus{
				PathType:     p2p.PathTypeRelay,
				PathState:    p2p.PathStateFallbackRelay,
				QualityScore: 82,
				LatencyMS:    45,
			},
			Key:       "peer:office",
			Kind:      "peer",
			Online:    true,
			NodeID:    "office",
			VirtualIP: "10.77.0.9",
			State:     "online",
		},
		{
			ConnectionStatus: p2p.ConnectionStatus{
				PathType:  p2p.PathTypeLANDirect,
				PathState: p2p.PathStateLANDirectConnected,
			},
			Key:    "peer:blocked",
			Kind:   "peer",
			NodeID: "blocked",
			State:  "disabled",
		},
	}

	state, got := conservativeDesktopDeviceSnapshot(original)
	if state != networkstate.Disconnected {
		t.Fatalf("network state = %q, want disconnected", state)
	}
	if len(got) != 2 || got[0].NodeID != "office" || got[0].VirtualIP != "10.77.0.9" {
		t.Fatalf("nodes = %+v, want preserved identity", got)
	}
	if got[0].Online || got[0].State != "offline" || got[0].PathType != "" || got[0].PathState != p2p.PathStateOffline || got[0].QualityScore != 0 || got[0].LatencyMS != 0 {
		t.Fatalf("node = %+v, want conservative offline projection", got[0])
	}
	if !original[0].Online || original[0].State != "online" {
		t.Fatalf("input snapshot was mutated: %+v", original[0])
	}
	if got[1].State != "disabled" || got[1].PathState != p2p.PathStateOffline {
		t.Fatalf("policy-disabled node = %+v, want policy retained with offline connection", got[1])
	}

	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	failureBranch := string(b)
	start := strings.Index(failureBranch, "func (a *desktopApp) loadMeshStatus()")
	end := strings.Index(failureBranch[start:], "func (a *desktopApp) showSelectedMeshNode()")
	if start < 0 || end < 0 {
		t.Fatal("loadMeshStatus source boundary missing")
	}
	failureBranch = failureBranch[start : start+end]
	if !strings.Contains(failureBranch, "conservativeDesktopDeviceSnapshot(a.meshModel.items)") {
		t.Fatal("desktop refresh failure must invalidate the retained device snapshot")
	}
	if strings.Contains(failureBranch, "os.ReadFile(statusPath)") || strings.Contains(failureBranch, "buildMeshNodes(status, statusPath)") {
		t.Fatal("desktop refresh failure must not re-project the raw status file")
	}
}

func TestDesktopRDPDiagnosticsPassSeparateNetworkAndTargetStates(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	compact := strings.ReplaceAll(src, " ", "")
	if !strings.Contains(compact, "networkStatenetworkstate.State") {
		t.Fatal("desktop RDP diagnostics do not retain DeviceList network state")
	}
	for _, want := range []string{
		"a.networkState = devices.NetworkState",
		"NetworkState: string(a.networkState)",
		"TargetStatus: node.State",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop RDP diagnostics missing separate state propagation %q", want)
		}
	}
}

func TestDesktopMainWindowUsesCompactSizing(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`MinSize:    Size{Width: 960, Height: 620}`,
		`Size:       Size{Width: 1060, Height: 690}`,
		`MaxSize:    Size{Width: 410, Height: 0}`,
		`Layout:     VBox{Margins: Margins{Left: 12, Top: 12, Right: 8, Bottom: 12}, Spacing: 8}`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop main window is missing compact sizing %s", want)
		}
	}
}

func TestRegeneratingInvitePreservesConnectedDevices(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`ReplaceExisting: true`,
		`已连接的设备继续使用原身份`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("regenerate invite flow missing %s", want)
		}
	}
}

func TestDesktopLongLivedInviteShowsRiskAndDeviceLimit(t *testing.T) {
	text := formatInviteForDesktopAt(onboarding.CreateInviteResult{
		Server:    "example.com:8443",
		Code:      "123456",
		Link:      "meshlink://join?server=example.com:8443&token=tok",
		LongLived: true,
		MaxUses:   3,
	}, time.Date(2026, 7, 8, 8, 0, 0, 0, time.UTC))
	for _, want := range []string{"长期有效", "设备数限制：3 台", "长期接入码风险"} {
		if !strings.Contains(text, want) {
			t.Fatalf("invite text = %q, want %q", text, want)
		}
	}
}

func TestDesktopMainWindowExposesDeviceAdminControls(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`Text: "重命名设备"`,
		`Text: "禁用设备"`,
		`Text: "移除设备"`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop main window missing %s", want)
		}
	}
}

func TestDesktopDeviceListShowsDisabledAndDisplayName(t *testing.T) {
	nodes := buildMeshNodesFromDevices(onboarding.DeviceList{
		Nodes: []onboarding.DeviceSummary{
			{
				Kind:        "peer",
				NodeID:      "laptop",
				DisplayName: "Alice laptop",
				Status:      "disabled",
				VirtualIP:   "10.77.0.2",
			},
		},
	}, `C:\Meshlink\configs\logs\MeshlinkAgent.status.json`)
	if len(nodes) != 1 {
		t.Fatalf("nodes = %+v, want one node", nodes)
	}
	if nodes[0].DisplayName != "Alice laptop" || nodes[0].NodeID != "laptop" {
		t.Fatalf("node = %+v, want node_id laptop display Alice laptop", nodes[0])
	}
	if got := meshNodeStatusText(nodes[0]); got != "已禁用" {
		t.Fatalf("meshNodeStatusText() = %q, want 已禁用", got)
	}
	detail := formatMeshNodeDetail(nodes[0])
	if !strings.Contains(detail, "显示名称：Alice laptop") || !strings.Contains(detail, "状态：已禁用") {
		t.Fatalf("detail = %q, want display name and disabled status", detail)
	}
}

func TestDesktopDeviceListShowsConnectionPathQualityStatus(t *testing.T) {
	nodes := buildMeshNodesFromDevices(onboarding.DeviceList{
		Nodes: []onboarding.DeviceSummary{
			{
				ConnectionStatus: p2p.ConnectionStatus{
					PathType:     p2p.PathTypeLANDirect,
					PathState:    p2p.PathStateLANDirectConnected,
					LatencyMS:    24,
					QualityScore: 96,
				},
				Kind:      "peer",
				NodeID:    "direct-pc",
				Status:    "online",
				VirtualIP: "10.77.0.2",
			},
			{
				ConnectionStatus: p2p.ConnectionStatus{
					PathType:     p2p.PathTypeRelay,
					PathState:    p2p.PathStateFallbackRelay,
					LatencyMS:    88,
					QualityScore: 72,
				},
				Kind:      "peer",
				NodeID:    "relay-pc",
				Status:    "online",
				VirtualIP: "10.77.0.3",
			},
			{
				ConnectionStatus: p2p.ConnectionStatus{
					PathState: p2p.PathStateOffline,
				},
				Kind:      "peer",
				NodeID:    "offline-pc",
				Status:    "offline",
				VirtualIP: "10.77.0.4",
			},
			{
				ConnectionStatus: p2p.ConnectionStatus{
					PathState: p2p.PathStateFailed,
				},
				Kind:      "peer",
				NodeID:    "failed-pc",
				Status:    "online",
				VirtualIP: "10.77.0.5",
			},
			{
				ConnectionStatus: p2p.ConnectionStatus{
					PathState: p2p.PathStateRDPUnreachable,
				},
				Kind:      "peer",
				NodeID:    "rdp-pc",
				Status:    "online",
				VirtualIP: "10.77.0.6",
			},
		},
	}, `C:\Meshlink\configs\logs\MeshlinkAgent.status.json`)
	want := map[string]string{
		"direct-pc":  "局域网直连 · 24 ms · 质量 96",
		"relay-pc":   "直连失败 · 本版本未启用中继",
		"offline-pc": "离线或未知",
		"failed-pc":  "直连失败",
		"rdp-pc":     "RDP 不可达",
	}
	for _, node := range nodes {
		if got := meshNodeConnectionSummary(node); got != want[node.NodeID] {
			t.Fatalf("meshNodeConnectionSummary(%s) = %q, want %q", node.NodeID, got, want[node.NodeID])
		}
		detail := formatMeshNodeDetail(node)
		if !strings.Contains(detail, "连接方式："+want[node.NodeID]) {
			t.Fatalf("detail for %s = %q, want connection summary", node.NodeID, detail)
		}
		for _, forbidden := range []string{"NAT", "CSR", "CA", "证书路径", "路由表", "JSON"} {
			if strings.Contains(detail, forbidden) {
				t.Fatalf("detail for %s exposes technical label %q: %q", node.NodeID, forbidden, detail)
			}
		}
	}
}

func TestDesktopMainWindowExposesOneClickDiagnosticReport(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`Text: "一键诊断"`,
		`runOneClickDiagnostics`,
		`RunOneClick`,
		`formatOneClickReport`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop main window missing one-click diagnostics %s", want)
		}
	}
}

func TestDesktopOneClickDiagnosticReportUsesPlainUserCopy(t *testing.T) {
	text := formatOneClickReport(diagnose.OneClickReport{
		Status:  diagnose.Fail,
		Summary: diagnose.OneClickSummary{Headline: "发现 1 个需要处理的问题。"},
		Findings: []diagnose.Finding{
			{
				Severity:       diagnose.Fail,
				Title:          "目标设备离线",
				Problem:        "目标设备当前不在线。",
				Impact:         "本机现在无法打开这台设备的远程桌面。",
				Recommendation: "请确认目标设备已开机，并保持 Meshlink 正在运行。",
				Action:         "目标设备上线后刷新设备列表，再重新诊断。",
			},
		},
	})
	for _, want := range []string{"一键诊断报告", "发现的问题", "影响原因", "建议处理", "下一步动作"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text = %q, want %q", text, want)
		}
	}
	for _, forbidden := range []string{"NAT", "CSR", "CA", "证书路径", "路由表", "服务名", "JSON", "MeshlinkAgent"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("report text exposes %q: %q", forbidden, text)
		}
	}
}

func TestServiceConfigPathPreferredOverRememberedSettings(t *testing.T) {
	dir := t.TempDir()
	remembered := filepath.Join(dir, "configs", "remembered.json")
	serviceConfig := filepath.Join(dir, "configs", "active.json")
	if err := os.MkdirAll(filepath.Dir(serviceConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remembered, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serviceConfig, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rememberConfigPath(dir, remembered); err != nil {
		t.Fatal(err)
	}

	got := configPathFromStatusOrSettings(dir, winservice.ServiceStatus{Installed: true, ConfigPath: serviceConfig})
	if got != serviceConfig {
		t.Fatalf("configPathFromStatusOrSettings() = %q, want service config %q", got, serviceConfig)
	}
}

func TestLoadSimpleModeSnapshotRestoresHubAddressAndInvite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "configs", "active.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "node_id": "LJW",
  "mode": "hub",
  "transport": {"protocol": "tcp_tls_v1", "listen": "0.0.0.0:5858"},
  "listen": "0.0.0.0:5858",
  "ca_file": "../certs/ca.pem",
  "cert_file": "../certs/LJW.pem",
  "key_file": "../certs/LJW-key.pem",
  "virtual_ip": "10.77.0.1",
  "device": {"type": "tun", "name": "meshlink0"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	invitePath := filepath.Join(dir, "invites", "invites.json")
	if err := os.MkdirAll(filepath.Dir(invitePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invitePath, []byte(`{
  "invites": [{
    "token": "tok123",
    "code": "316857",
    "code_hash": "hash",
    "server": "openwrt.example.com:5858",
    "protocol": "tcp_tls_v1",
    "created_at": "2026-07-07T09:13:12+08:00",
    "expires_at": "2026-07-07T09:23:12+08:00",
    "max_uses": 1,
    "max_failures": 5
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := loadSimpleModeSnapshot(dir, configPath, time.Date(2026, 7, 7, 9, 14, 0, 0, time.FixedZone("CST", 8*60*60)))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Mode != "hub" || snapshot.ListenPort != "5858" || snapshot.Server != "openwrt.example.com:5858" {
		t.Fatalf("snapshot = %+v, want hub openwrt.example.com:5858 port 5858", snapshot)
	}
	if !strings.Contains(snapshot.InviteText, "接入码：316857") || !strings.Contains(snapshot.InviteText, "meshlink://join") {
		t.Fatalf("invite text = %q, want code and join link", snapshot.InviteText)
	}
}

func TestWaitForAgentRunningIgnoresTransientStoppedStatusFromRestart(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "active.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(logDir, "MeshlinkAgent.status.json")
	startedAt := time.Now()
	stopped := `{"updated_at":"` + startedAt.Add(20*time.Millisecond).Format(time.RFC3339Nano) + `","state":"stopped","self":{"node_id":"hub","mode":"hub","virtual_ip":"10.77.0.1"}}`
	if err := os.WriteFile(statusPath, []byte(stopped), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		running := `{"updated_at":"` + time.Now().Format(time.RFC3339Nano) + `","state":"running","network_state":"connected","self":{"node_id":"hub","mode":"hub","virtual_ip":"10.77.0.1"}}`
		_ = os.WriteFile(statusPath, []byte(running), 0o600)
	}()
	if err := waitForAgentRunningAfterStart(configPath, "MeshlinkAgent", startedAt, time.Second); err != nil {
		t.Fatalf("waitForAgentRunningAfterStart returned %v, want nil", err)
	}
}

func TestUpdateAddressAndPortBuildBaseURL(t *testing.T) {
	got, err := updateBaseURLFromHostPort("10.77.0.1", "1263")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://10.77.0.1:1263" {
		t.Fatalf("base URL = %q, want http://10.77.0.1:1263", got)
	}
	got, err = updateBaseURLFromHostPort("https://updates.example.com", "9443")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://updates.example.com:9443" {
		t.Fatalf("base URL = %q, want https://updates.example.com:9443", got)
	}
}

func TestPublishUpdateBatPackagesAndRestartsUpdateService(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "publish-update.bat"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`scripts\build.ps1" -Package`,
		`mesh-update-server.exe" -service install`,
		`-service-name MeshlinkUpdateServer`,
		`-listen "%UPDATE_LISTEN%"`,
		`-dir "%RELEASE_DIR%"`,
		`mesh-update-server.exe" -service start`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("publish-update.bat missing %s", want)
		}
	}
}

func TestBuildScriptBuildsMeshCloudHub(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`mesh-cloudhub.exe`,
		`.\cmd\mesh-cloudhub`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("build.ps1 should include %s", want)
		}
	}
}

func TestPackageIncludesPublishUpdateBat(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "package.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`publish-update.bat`,
		`mesh-cloudhub.exe`,
		`bin\linux\mesh-agent`,
		`build-linux-agent.ps1`,
		`linux-systemd.sh`,
		`self-hosted-relay-runbook.zh-CN.md`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("package.ps1 should include %s in release zip", want)
		}
	}
}

func TestAgentRunningAfterStartReportsStoppedStatusWithLogTail(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "active.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "MeshlinkAgent.log"), []byte("agent stopped with error: bind: forbidden by its access permissions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := `{
  "updated_at": "` + time.Now().Format(time.RFC3339Nano) + `",
  "state": "stopped",
  "self": {
    "node_id": "LJW",
    "mode": "hub",
    "virtual_ip": "10.77.0.1",
    "listen": "0.0.0.0:1185"
  }
}`
	if err := os.WriteFile(filepath.Join(logDir, "MeshlinkAgent.status.json"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}

	err := waitForAgentRunningAfterStart(configPath, "MeshlinkAgent", time.Now().Add(-time.Second), 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected stopped agent status to be reported")
	}
	if !strings.Contains(err.Error(), "bind: forbidden") {
		t.Fatalf("error = %q, want log tail with bind failure", err.Error())
	}
}
