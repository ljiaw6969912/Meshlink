//go:build windows

package main

import (
	"errors"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"meshlink/internal/p2p"

	"meshlink/internal/onboarding"
	"meshlink/internal/winservice"
)

func TestDevicePresentationKeepsMembershipAndPathSeparate(t *testing.T) {
	node := meshNode{Kind: "peer", NodeID: "office", DisplayName: "办公室电脑", VirtualIP: "10.77.0.2", Online: true}
	text := formatMeshNodePresentation(node)
	for _, want := range []string{"办公室电脑", "10.77.0.2", "尚未建立直连"} {
		if !strings.Contains(text, want) {
			t.Fatalf("device summary %q missing %q", text, want)
		}
	}
	node.ConnectionStatus = p2p.ConnectionStatus{PathType: p2p.PathTypeLANDirect, PathState: p2p.PathStateLANDirectConnected, LatencyMS: 12}
	text = formatMeshNodePresentation(node)
	if !strings.Contains(text, "局域网直连") || !strings.Contains(text, "12 ms") {
		t.Fatalf("device summary lost live path information: %q", text)
	}
}

func TestDesktopButtonPaintStateReleasedWithWindow(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	count := func() int { n := 0; buttonPaintStates.Range(func(_, _ any) bool { n++; return true }); return n }
	before := count()
	a := &desktopApp{}
	if err := a.createWindow(); err != nil {
		t.Fatal(err)
	}
	a.mw.Dispose()
	if after := count(); after != before {
		t.Fatalf("closing window retained %d button painting states", after-before)
	}
}

func TestBackgroundReconnectRequiresThisConnectionToHaveStarted(t *testing.T) {
	status := winservice.ServiceStatus{Installed: true, State: "running", ConfigPath: `C:\Meshlink\configs\active.json`}
	pending := &agentConnectionPendingError{detail: "coordinator unavailable"}
	if !isBackgroundReconnect(status.ConfigPath, status, pending) {
		t.Fatal("current connection retry not recognized")
	}
	for _, tc := range []struct {
		path string
		err  error
	}{
		{"", pending},
		{`C:\Meshlink\profiles\spoke\configs\active.json`, pending},
		{status.ConfigPath, errors.New("wrong invitation code")},
	} {
		if isBackgroundReconnect(tc.path, status, tc.err) {
			t.Fatal("unrelated service hid enrollment failure")
		}
	}
	status.State = "stopped"
	if isBackgroundReconnect(status.ConfigPath, status, pending) {
		t.Fatal("stopped service reported as retrying")
	}
}

func TestClientSnapshotRestoresFullConnectionAfterReopen(t *testing.T) {
	hub := onboarding.Manager{BaseDir: t.TempDir(), LocalIPv4: func() (string, error) { return "127.0.0.1", nil }}
	if _, err := hub.CreateHub(onboarding.CreateHubRequest{NodeName: "server", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.EnrollHTTPHandler())
	defer server.Close()
	invite, err := hub.CreateInvite(onboarding.CreateInviteRequest{Server: server.URL, LongLived: true, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	client := onboarding.Manager{BaseDir: t.TempDir(), HTTPClient: server.Client()}
	joined, err := client.JoinSpoke(onboarding.JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "my-pc"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadSimpleModeSnapshot(client.BaseDir, joined.ConfigPath, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Mode != "spoke" || snapshot.NodeName != "my-pc" || snapshot.VirtualIP != joined.VirtualIP || snapshot.InviteLink != invite.Link || snapshot.InviteCode != invite.Code {
		t.Fatalf("client form lost its connection after reopen: %+v", snapshot)
	}
	if !strings.Contains(snapshot.ConnectionText, joined.VirtualIP) || !strings.Contains(snapshot.ConnectionText, "my-pc") {
		t.Fatalf("connection information missing: %q", snapshot.ConnectionText)
	}
}

func TestDesktopRestoredModeShowsOnlyItsOwnPanel(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := &desktopApp{}
	if err := a.createWindow(); err != nil {
		t.Fatal(err)
	}
	defer a.mw.Dispose()
	t.Logf("initial window size: %+v (DPI %d)", a.mw.Size(), a.mw.DPI())
	if size := a.mw.Size(); size.Width > 1140 || size.Height > 720 {
		t.Fatalf("desktop unexpectedly expanded beyond a compact screen: %+v", size)
	}
	if a.inviteOutput.MaxSize().Height > 100 || a.inviteOutput.MinSize().Height == 0 {
		t.Fatal("invitation text must stay scrollable within a bounded form")
	}
	a.applySimpleModeSnapshot(simpleModeSnapshot{Mode: "hub", Server: "example.com:8443", ListenPort: "8443", InviteText: "saved invitation", ConnectionText: "server connection"})
	if !a.serverPanel.Visible() || a.clientPanel.Visible() || a.inviteServer.Text() != "example.com:8443" || a.inviteOutput.Text() != "saved invitation" {
		t.Fatal("server form not restored exclusively")
	}
	t.Logf("server window size: %+v, panel: %+v", a.mw.Size(), a.serverPanel.Size())
	a.applySimpleModeSnapshot(simpleModeSnapshot{Mode: "spoke", NodeName: "my-pc", InviteLink: "saved-link", InviteCode: "123456", ConnectionText: "client connection"})
	if a.serverPanel.Visible() || !a.clientPanel.Visible() || a.spokeNodeName.Text() != "my-pc" || a.inviteLink.Text() != "saved-link" || a.inviteCode.Text() != "123456" || a.clientInfo.Text() != "client connection" {
		t.Fatal("client form not restored exclusively")
	}
	// Hidden windows have not had their first layout pass yet. Give the native
	// buttons a hit area so BM_CLICK exercises their normal mouse/click routing.
	a.serverModeButton.SetSize(walk.Size{Width: 128, Height: 34})
	a.clientModeButton.SetSize(walk.Size{Width: 128, Height: 34})
	win.SendMessage(a.serverModeButton.Handle(), win.BM_CLICK, 0, 0)
	if !a.serverPanel.Visible() || a.clientPanel.Visible() || a.modeState.Text() != "服务器模式" {
		t.Fatal("server selector did not switch panels")
	}
	win.SendMessage(a.clientModeButton.Handle(), win.BM_CLICK, 0, 0)
	if a.serverPanel.Visible() || !a.clientPanel.Visible() || a.modeState.Text() != "客户端模式" {
		t.Fatal("client selector did not switch panels")
	}
	// Optional visual QA uses fictional values and never reads or controls a
	// machine's installed service. The normal suite only checks the widgets.
	if os.Getenv("MESHLINK_UI_PREVIEW") == "1" {
		a.mw.SetTitle("Meshlink 界面预览")
		a.applySimpleModeSnapshot(simpleModeSnapshot{Mode: "hub", Server: "mesh.example.com:8443", ListenPort: "8443", InviteText: "邀请链接：meshlink://join?server=mesh.example.com%3A8443\r\n接入码：123456（演示）", ConnectionText: "本机名称：办公室服务器\r\n虚拟 IP：10.77.0.1\r\n监听地址：0.0.0.0:8443"})
		a.meshModel.SetItems([]meshNode{
			{Key: "self:server", Kind: "self", NodeID: "server", DisplayName: "办公室服务器", VirtualIP: "10.77.0.1", Online: true, State: "running"},
			{Key: "peer:design", Kind: "peer", NodeID: "design", DisplayName: "设计工作站", VirtualIP: "10.77.0.2", Online: true, State: "online", ConnectionStatus: p2p.ConnectionStatus{PathType: p2p.PathTypeLANDirect, PathState: p2p.PathStateLANDirectConnected, LatencyMS: 2}},
			{Key: "peer:home", Kind: "peer", NodeID: "home", DisplayName: "家用笔记本", VirtualIP: "10.77.0.3", Online: true, State: "online", ConnectionStatus: p2p.ConnectionStatus{PathType: p2p.PathTypePublicDirect, PathState: p2p.PathStatePublicDirectConnected, LatencyMS: 26}},
			{Key: "peer:travel", Kind: "peer", NodeID: "travel", DisplayName: "出差电脑", VirtualIP: "10.77.0.4", Online: false, State: "offline"},
		})
		a.coordinatorState = "serving"
		a.quickState.SetText("服务器运行中")
		a.meshSummary.SetText("4 台设备   /   3 台在线   /   1 台离线")
		a.onboardingState.SetText("状态：服务器运行中")
		a.serviceState.SetText("服务状态：运行中")
		a.meshList.SetCurrentIndex(1)
		a.showSelectedMeshNode()
		if os.Getenv("MESHLINK_UI_PREVIEW_MODE") == "spoke" {
			a.applySimpleModeSnapshot(simpleModeSnapshot{Mode: "spoke", NodeName: "我的笔记本", InviteLink: "meshlink://join?server=mesh.example.com%3A8443", InviteCode: "123456", ConnectionText: "本机名称：我的笔记本\r\n服务器：mesh.example.com:8443\r\n虚拟 IP：10.77.0.5"})
			a.coordinatorState = "connected"
			a.quickState.SetText("已连接")
			a.onboardingState.SetText("状态：已连接")
			a.updateConnectivityOverview(a.meshModel.items, "peer:design")
		}
		a.mw.Show()
		time.AfterFunc(180*time.Second, func() { a.mw.Synchronize(func() { a.mw.Close() }) })
		a.mw.Run()
	}
}
