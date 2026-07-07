//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	for _, hidden := range []string{
		`Title:  "高级设置"`,
		`Title:  "诊断和日志"`,
		`PushButton{Text: "开始诊断"`,
		`PushButton{Text: "读取日志"`,
		`Text:          "未诊断"`,
	} {
		if strings.Contains(src, hidden) {
			t.Fatalf("desktop main window still exposes %s", hidden)
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

func TestInviteOutputHasBoundedHeight(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `TextEdit{AssignTo: &a.inviteOutput, ReadOnly: true, MinSize: Size{0, 76}, MaxSize: Size{10000, 96}, ColumnSpan: 4}`) {
		t.Fatal("invite output should have a bounded height")
	}
}

func TestDesktopMainWindowUsesCompactSizing(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`MinSize:    Size{960, 620}`,
		`Size:       Size{1060, 690}`,
		`MaxSize:    Size{410, 0}`,
		`Layout:     VBox{Margins: Margins{Left: 12, Top: 12, Right: 8, Bottom: 12}, Spacing: 8}`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("desktop main window is missing compact sizing %s", want)
		}
	}
}

func TestRegeneratingInviteReplacesInvitesAndRestartsAgent(t *testing.T) {
	b, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`ReplaceExisting: true`,
		`serviceErr := a.installAndStartAgent(a.currentConfigPath())`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("regenerate invite flow missing %s", want)
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
		running := `{"updated_at":"` + time.Now().Format(time.RFC3339Nano) + `","state":"running","self":{"node_id":"hub","mode":"hub","virtual_ip":"10.77.0.1"}}`
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

func TestPackageIncludesPublishUpdateBat(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "package.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `publish-update.bat`) {
		t.Fatal("package.ps1 should include publish-update.bat in release zip")
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
