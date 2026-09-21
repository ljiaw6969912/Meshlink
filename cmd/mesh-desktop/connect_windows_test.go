//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
	"meshlink/internal/config"
	"meshlink/internal/onboarding"
	"meshlink/internal/runner"
	"meshlink/internal/winservice"
)

func TestWindowsServerHostConnect(t *testing.T) {
	if os.Getenv("MESHLINK_WINDOWS_CONNECT_SMOKE") != "1" {
		t.Skip("explicit elevated Windows integration test")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("administrator required")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	manager := onboarding.Manager{BaseDir: filepath.Join(t.TempDir(), "服务器 空白目录"), LocalIPv4: func() (string, error) { return "127.0.0.1", nil }}
	req := onboarding.StartServerRequest{ServerAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), ListenPort: port, LongLived: true, MaxUses: -1}
	prepared, err := manager.StartServerMode(req)
	if err != nil {
		t.Fatal(err)
	}
	hubCfg, err := config.Load(prepared.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	hostConfigPath := filepath.Join(filepath.Dir(prepared.ConfigPath), hubCfg.ServerNodeConfig)
	host, err := config.Load(hostConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	host.Device.Name = fmt.Sprintf("ml-host-%d", os.Getpid())
	if err := config.Write(hostConfigPath, *host); err != nil {
		t.Fatal(err)
	}
	serviceName := fmt.Sprintf("MeshlinkHostSmoke%d", os.Getpid())
	t.Cleanup(func() {
		_ = winservice.Stop(serviceName)
		if s, _ := winservice.Status(serviceName); s.Installed {
			if err := winservice.Uninstall(serviceName); err != nil {
				t.Error(err)
			}
		}
	})
	started, err := startDesktopServer(manager, req, serviceName)
	if err != nil {
		t.Fatal(err)
	}
	assertPersistentDesktopService(t, serviceName)
	statusPath := runner.StatusPath(started.ConfigPath, serviceName) + ".server-node.json"
	status, err := readRuntimeStatusFile(statusPath)
	if err != nil || status.Self.VirtualIP != "10.77.0.1" || status.CoordinatorState != "connected" {
		t.Fatalf("server host not connected: %+v %v", status, err)
	}
	_, udpPort, err := net.SplitHostPort(status.P2PListen)
	if err != nil || udpPort != strconv.Itoa(port) {
		t.Fatalf("host requires an extra mapped UDP port: %s", status.P2PListen)
	}
	before, _ := os.ReadFile(hostConfigPath)
	restarted, err := startDesktopServer(manager, req, serviceName)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(hostConfigPath)
	if !bytes.Equal(before, after) || restarted.Invite.Link != started.Invite.Link || restarted.Invite.Code != started.Invite.Code {
		t.Fatal("server restart changed host identity or invitation")
	}
	devices, err := manager.Devices(serviceName)
	if err != nil || len(devices.Nodes) != 1 || devices.Nodes[0].Kind != "self" || devices.Nodes[0].VirtualIP != "10.77.0.1" {
		t.Fatalf("server UI projection: %+v %v", devices, err)
	}
	t.Logf("server host real Wintun %s connected as %s, shared UDP/TCP port %d; restart preserved invite and identity", host.Device.Name, status.Self.NodeID, port)
}

// Run the compiled test beside mesh-agent.exe and wintun.dll with elevation:
// MESHLINK_WINDOWS_CONNECT_SMOKE=1 connect-smoke.test.exe -test.run TestWindowsCleanClientConnect
// This uses a separate coordinator, registry, service and real Wintun adapter.
func TestWindowsCleanClientConnect(t *testing.T) {
	if os.Getenv("MESHLINK_WINDOWS_CONNECT_SMOKE") != "1" {
		t.Skip("explicit elevated Windows integration test")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("integration test requires administrator privileges")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	hubDir := t.TempDir()
	hub := onboarding.Manager{BaseDir: hubDir, LocalIPv4: func() (string, error) { return "127.0.0.1", nil }}
	created, err := hub.CreateHub(onboarding.CreateHubRequest{NodeName: "smoke-A", ListenPort: port, IPAddrs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, created.ConfigPath, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("test coordinator did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, e := net.DialTimeout("tcp4", created.Listen, 100*time.Millisecond)
		if e == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test coordinator failed to listen")
		}
		time.Sleep(50 * time.Millisecond)
	}
	invite, err := hub.CreateInvite(onboarding.CreateInviteRequest{Server: created.Listen, LongLived: true, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	serviceName := fmt.Sprintf("MeshlinkConnectSmoke%d", os.Getpid())
	t.Cleanup(func() {
		_ = winservice.Stop(serviceName)
		if err := winservice.Uninstall(serviceName); err != nil {
			t.Error(err)
		}
	})
	var ids []string
	for i := 0; i < 2; i++ {
		manager := onboarding.Manager{BaseDir: filepath.Join(t.TempDir(), "无配置 客户端")}
		req := onboarding.JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "same-Windows-name"}
		bad := req
		bad.Code = "wrong-code"
		if _, err := connectSpoke(manager, bad, func(string) error { t.Fatal("invalid invitation started a service"); return nil }); err == nil {
			t.Fatal("invalid code was accepted")
		}
		result, err := connectSpoke(manager, req, func(path string) error {
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			cfg.Device.Name = fmt.Sprintf("ml-connect-%d", os.Getpid())
			if err := config.Write(path, *cfg); err != nil {
				return err
			}
			return installAndStartAgent(serviceName, path)
		})
		if err != nil {
			t.Fatalf("fresh client %d failed: %v", i+1, err)
		}
		assertPersistentDesktopService(t, serviceName)
		cfg, err := config.Load(result.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, cfg.NodeID)
		before, _ := os.ReadFile(result.ConfigPath)
		keyPath := filepath.Join(filepath.Dir(result.ConfigPath), cfg.KeyFile)
		keyBefore, _ := os.ReadFile(keyPath)
		if err := winservice.Stop(serviceName); err != nil {
			t.Fatal(err)
		}
		resumed, err := connectSpoke(manager, onboarding.JoinSpokeRequest{}, func(path string) error { return installAndStartAgent(serviceName, path) })
		if err != nil {
			t.Fatalf("reconnect client %d failed: %v", i+1, err)
		}
		after, _ := os.ReadFile(result.ConfigPath)
		keyAfter, _ := os.ReadFile(keyPath)
		if resumed.VirtualIP != result.VirtualIP || !bytes.Equal(before, after) || len(keyBefore) == 0 || !bytes.Equal(keyBefore, keyAfter) {
			t.Fatal("reconnect replaced identity/configuration")
		}
		status, err := readRuntimeStatusFile(runner.StatusPath(result.ConfigPath, serviceName))
		if err != nil || status.CoordinatorState != "connected" {
			t.Fatalf("missing real control handshake: %+v %v", status, err)
		}
		t.Logf("fresh client %d: enrolled, real Windows service/Wintun connected, reconnected with same identity %s / %s", i+1, cfg.NodeID, result.VirtualIP)
		if err := winservice.Stop(serviceName); err != nil {
			t.Fatal(err)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("fresh clients share identity")
	}
	registryBytes, err := os.ReadFile(filepath.Join(hubDir, "configs", "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(registryBytes), `"node_id"`) != 2 {
		t.Fatalf("reconnect created duplicate registration: %s", registryBytes)
	}
}

// Exercise the actual window lifecycle while a real SCM/Wintun service is
// running, and inspect Windows boot/recovery configuration independently.
func assertPersistentDesktopService(t *testing.T, name string) {
	t.Helper()
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	cfg, err := service.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StartType != mgr.StartAutomatic || cfg.ServiceStartName != "LocalSystem" {
		t.Fatalf("service will not resume independently at boot: %+v", cfg)
	}
	actions, err := service.RecoveryActions()
	if err != nil || len(actions) == 0 || actions[0].Type != mgr.ServiceRestart {
		t.Fatalf("service crash recovery missing: %+v %v", actions, err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	window := &desktopApp{}
	if err := window.createWindow(); err != nil {
		t.Fatal(err)
	}
	window.mw.Close()
	window.mw.Dispose()
	status, err := winservice.Status(name)
	if err != nil || status.State != "running" {
		t.Fatalf("closing window stopped service: %+v %v", status, err)
	}
}

func TestSpokeRunningWithoutCoordinatorIsNotConnected(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	status := `{"updated_at":"` + startedAt.Format(time.RFC3339Nano) + `","state":"running","network_state":"connecting","coordinator_state":"connecting","self":{"mode":"spoke","node_id":"B"}}`
	if err := os.WriteFile(filepath.Join(logDir, "MeshlinkAgent.status.json"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForAgentRunningAfterStart(filepath.Join(dir, "active.json"), "MeshlinkAgent", startedAt, 20*time.Millisecond); err == nil {
		t.Fatal("service running without a control handshake was reported as connected")
	}
}

func TestStartupErrorShowsLatestFailureWithoutOldLogWall(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldLogs := strings.Repeat("old coordinator starting mode=hub\n", 100)
	latest := `agent stopped with error: listen tcp4 192.168.1.23:3222: bind: The requested address is not valid in its context.`
	if err := os.WriteFile(filepath.Join(logDir, "MeshlinkAgent.log"), []byte(oldLogs+latest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := agentLogTail(filepath.Join(dir, "active.json"), "MeshlinkAgent")
	if !strings.Contains(got, "requested address") || strings.Count(got, "\n") > 5 || strings.Contains(got, "old coordinator") {
		t.Fatalf("startup error must expose the latest failure, got %q", got)
	}
}
