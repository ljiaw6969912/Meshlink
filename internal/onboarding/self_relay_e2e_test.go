//go:build e2e

package onboarding

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshlink/internal/config"
	"meshlink/internal/deployssh"
)

type e2eRuntimeStatus struct {
	Peers []struct {
		NodeID string `json:"node_id"`
		Status string `json:"status"`
	} `json:"peers"`
}

func TestE2ESelfHostedRelayDeploysAndEnrollsTwoClients(t *testing.T) {
	if os.Getenv("MESHLINK_E2E_SELF_RELAY") != "1" {
		t.Skip("set MESHLINK_E2E_SELF_RELAY=1 and SSH environment variables to run the real self-hosted relay E2E test")
	}

	req := e2eSelfHostedRelayRequest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	manager := Manager{BaseDir: t.TempDir()}
	checkReq := req
	checkReq.AcceptHostKey = true
	check, err := manager.CheckSelfHostedRelay(ctx, checkReq)
	if err != nil {
		t.Fatalf("check self-hosted relay: %v", err)
	}
	if check.HostFingerprint == "" {
		t.Fatalf("check did not return an SSH host fingerprint")
	}
	t.Logf("host fingerprint: %s (%s)", check.HostFingerprint, check.HostTrust.Status)

	deployReq := req
	deployReq.AcceptHostKey = false
	result, err := manager.DeploySelfHostedRelay(ctx, deployReq)
	if err != nil {
		t.Fatalf("deploy self-hosted relay: %v", err)
	}
	if result.ServiceStatus != "running" {
		t.Fatalf("service status = %q, want running", result.ServiceStatus)
	}
	assertHealthOK(t, result.Health, "SSH 可达", "远程系统类型", "远程目录权限", "systemd", "mesh-agent 已安装", "远程版本", "远程服务状态", "监听端口", "/enroll/health")
	checkPublicEnrollHealth(t, result.Invite.Server)
	t.Logf("invite link: %s", result.Invite.Link)
	t.Logf("invite code: %s; expires: %s; max uses: %d", result.Invite.Code, formatRelayInviteExpiry(result.Invite), result.Invite.MaxUses)

	nodeNames := []string{
		fmt.Sprintf("e2e-home-%d", time.Now().UnixNano()),
		fmt.Sprintf("e2e-away-%d", time.Now().UnixNano()),
	}
	var clientConfigs []string
	for i, nodeName := range nodeNames {
		clientDir := filepath.Join(manager.baseDir(), fmt.Sprintf("client-%d", i+1))
		join, err := (Manager{BaseDir: clientDir}).JoinSpoke(JoinSpokeRequest{
			InviteLink: result.Invite.Link,
			Code:       result.Invite.Code,
			NodeName:   nodeName,
		})
		if err != nil {
			t.Fatalf("join client %d: %v", i+1, err)
		}
		t.Logf("client %d joined: node=%s virtual_ip=%s server=%s", i+1, nodeName, join.VirtualIP, join.Server)
		clientConfigs = append(clientConfigs, join.ConfigPath)
	}

	registry := readRemoteDeviceRegistry(t, ctx, manager, req)
	for _, nodeName := range nodeNames {
		if !registryHasNode(registry, nodeName) {
			t.Fatalf("remote device registry does not contain joined node %q; registry=%+v", nodeName, registry.Nodes)
		}
	}

	stopAgents := startE2ESpokeAgents(t, clientConfigs)
	defer stopAgents()
	time.Sleep(3 * time.Second)
	status := readRemoteHubStatus(t, ctx, manager, req)
	for _, nodeName := range nodeNames {
		if !runtimeStatusHasOnlinePeer(status, nodeName) {
			t.Fatalf("remote hub status does not show peer %q online; peers=%+v", nodeName, status.Peers)
		}
	}
}

func e2eSelfHostedRelayRequest(t *testing.T) SelfHostedRelayRequest {
	t.Helper()
	host := strings.TrimSpace(os.Getenv("MESHLINK_E2E_SSH_HOST"))
	if host == "" {
		t.Fatalf("MESHLINK_E2E_SSH_HOST is required")
	}
	password := os.Getenv("MESHLINK_E2E_SSH_PASSWORD")
	privateKey := os.Getenv("MESHLINK_E2E_SSH_PRIVATE_KEY")
	if privateKey == "" {
		if keyPath := strings.TrimSpace(os.Getenv("MESHLINK_E2E_SSH_PRIVATE_KEY_PATH")); keyPath != "" {
			b, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatalf("read MESHLINK_E2E_SSH_PRIVATE_KEY_PATH: %v", err)
			}
			privateKey = string(b)
		}
	}
	if strings.TrimSpace(password) == "" && strings.TrimSpace(privateKey) == "" {
		t.Fatalf("MESHLINK_E2E_SSH_PASSWORD or MESHLINK_E2E_SSH_PRIVATE_KEY(_PATH) is required")
	}

	publicAddress := strings.TrimSpace(os.Getenv("MESHLINK_E2E_PUBLIC_ADDRESS"))
	if publicAddress == "" {
		publicAddress = host
	}
	agentPath := strings.TrimSpace(os.Getenv("MESHLINK_E2E_AGENT_BINARY"))
	if agentPath == "" {
		agentPath = filepath.Clean(filepath.Join("..", "..", "bin", "linux", "mesh-agent"))
	}
	if info, err := os.Stat(agentPath); err != nil || info.IsDir() {
		t.Fatalf("Linux mesh-agent binary is required at %s: %v", agentPath, err)
	}

	return SelfHostedRelayRequest{
		CloudServerAddress: host,
		SSHPort:            envInt(t, "MESHLINK_E2E_SSH_PORT", 22),
		SSHUsername:        envString("MESHLINK_E2E_SSH_USER", "root"),
		SSHPassword:        password,
		SSHPrivateKey:      privateKey,
		ListenPort:         envInt(t, "MESHLINK_E2E_LISTEN_PORT", 8443),
		PublicAddress:      publicAddress,
		AgentBinaryPath:    agentPath,
		LongLived:          true,
		MaxUses:            envInt(t, "MESHLINK_E2E_MAX_USES", 10),
	}
}

func envString(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s must be an integer: %v", name, err)
	}
	return value
}

func assertHealthOK(t *testing.T, checks []deployssh.HealthCheck, names ...string) {
	t.Helper()
	byName := make(map[string]deployssh.HealthCheck, len(checks))
	for _, check := range checks {
		byName[check.Name] = check
	}
	for _, name := range names {
		check, ok := byName[name]
		if !ok {
			t.Fatalf("health check %q is missing; checks=%+v", name, checks)
		}
		if check.Status != "ok" {
			t.Fatalf("health check %q = %s (%s), want ok", name, check.Status, check.Detail)
		}
	}
}

func checkPublicEnrollHealth(t *testing.T, server string) {
	t.Helper()
	url := enrollHealthURL(server)
	client := http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	res, err := client.Get(url)
	if err != nil {
		t.Fatalf("public enroll health %s: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		t.Fatalf("public enroll health %s returned %s", url, res.Status)
	}
	t.Logf("public enroll health ok: %s", url)
}

func enrollHealthURL(server string) string {
	if strings.HasPrefix(server, "http://") || strings.HasPrefix(server, "https://") {
		return strings.TrimRight(server, "/") + "/enroll/health"
	}
	return "https://" + strings.TrimRight(server, "/") + "/enroll/health"
}

func readRemoteDeviceRegistry(t *testing.T, ctx context.Context, manager Manager, req SelfHostedRelayRequest) DeviceRegistry {
	t.Helper()
	client, _, err := deployssh.Dial(ctx, deployssh.DeployRequest{
		Host:          sshHost(req.CloudServerAddress),
		Port:          req.SSHPort,
		Auth:          deployAuth(req),
		KnownHosts:    deployssh.FileKnownHostStore{Path: manager.sshKnownHostsPath()},
		AcceptHostKey: false,
	})
	if err != nil {
		t.Fatalf("dial remote server for registry check: %v", err)
	}
	defer client.Close()
	out, err := client.Run(ctx, "cat /etc/meshlink/configs/devices.json")
	if err != nil {
		t.Fatalf("read remote device registry: %v (%s)", err, strings.TrimSpace(out.Stderr))
	}
	var registry DeviceRegistry
	if err := json.Unmarshal([]byte(out.Stdout), &registry); err != nil {
		t.Fatalf("parse remote device registry: %v", err)
	}
	return registry
}

func registryHasNode(registry DeviceRegistry, nodeName string) bool {
	for _, node := range registry.Nodes {
		if node.NodeID == nodeName {
			return true
		}
	}
	return false
}

func startE2ESpokeAgents(t *testing.T, configPaths []string) func() {
	t.Helper()
	agentPath := e2eLocalAgentBinary(t)
	ctx, cancel := context.WithCancel(context.Background())

	type process struct {
		cmd    *exec.Cmd
		output *strings.Builder
		done   chan error
	}
	processes := make([]process, 0, len(configPaths))
	for i, configPath := range configPaths {
		prepareNullDeviceConfig(t, configPath)
		var output strings.Builder
		cmd := exec.CommandContext(ctx, agentPath, "-config", configPath)
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatalf("start spoke agent %d: %v", i+1, err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		processes = append(processes, process{cmd: cmd, output: &output, done: done})
	}

	return func() {
		cancel()
		for _, process := range processes {
			select {
			case err := <-process.done:
				if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
					t.Logf("spoke agent exited: %v\n%s", err, process.output.String())
				}
			case <-time.After(5 * time.Second):
				_ = process.cmd.Process.Kill()
				<-process.done
			}
		}
	}
}

func e2eLocalAgentBinary(t *testing.T) string {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv("MESHLINK_E2E_LOCAL_AGENT_BINARY")); path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
		t.Fatalf("MESHLINK_E2E_LOCAL_AGENT_BINARY is not a file: %s", path)
	}
	candidate := filepath.Clean(filepath.Join("..", "..", "bin", "mesh-agent.exe"))
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	t.Fatalf("local mesh-agent.exe is required at %s; run scripts/build.ps1 first", candidate)
	return ""
}

func prepareNullDeviceConfig(t *testing.T, configPath string) {
	t.Helper()
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load client config %s: %v", configPath, err)
	}
	cfg.Device = config.DeviceConfig{Type: "null", Name: "null0"}
	cfg.Setup = config.SetupConfig{}
	if err := writePrettyJSON(configPath, cfg); err != nil {
		t.Fatalf("write null-device client config %s: %v", configPath, err)
	}
}

func readRemoteHubStatus(t *testing.T, ctx context.Context, manager Manager, req SelfHostedRelayRequest) e2eRuntimeStatus {
	t.Helper()
	client, _, err := deployssh.Dial(ctx, deployssh.DeployRequest{
		Host:          sshHost(req.CloudServerAddress),
		Port:          req.SSHPort,
		Auth:          deployAuth(req),
		KnownHosts:    deployssh.FileKnownHostStore{Path: manager.sshKnownHostsPath()},
		AcceptHostKey: false,
	})
	if err != nil {
		t.Fatalf("dial remote server for hub status check: %v", err)
	}
	defer client.Close()
	out, err := client.Run(ctx, "cat /etc/meshlink/configs/logs/mesh-agent.status.json")
	if err != nil {
		t.Fatalf("read remote hub status: %v (%s)", err, strings.TrimSpace(out.Stderr))
	}
	var status e2eRuntimeStatus
	if err := json.Unmarshal([]byte(out.Stdout), &status); err != nil {
		t.Fatalf("parse remote hub status: %v", err)
	}
	return status
}

func runtimeStatusHasOnlinePeer(status e2eRuntimeStatus, nodeName string) bool {
	for _, peer := range status.Peers {
		if peer.NodeID == nodeName && peer.Status == "online" {
			return true
		}
	}
	return false
}
