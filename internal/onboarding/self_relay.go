package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"meshlink/internal/deployssh"
)

const selfRelayHubNodeName = "meshlink-hub"

type SelfHostedRelayRequest struct {
	CloudServerAddress string `json:"cloud_server_address"`
	SSHPort            int    `json:"ssh_port"`
	SSHUsername        string `json:"ssh_username"`
	SSHPassword        string `json:"ssh_password,omitempty"`
	SSHPrivateKey      string `json:"ssh_private_key,omitempty"`
	ListenPort         int    `json:"listen_port"`
	PublicAddress      string `json:"public_address"`
	AgentBinaryPath    string `json:"agent_binary_path,omitempty"`
	LongLived          bool   `json:"long_lived,omitempty"`
	MaxUses            int    `json:"max_uses,omitempty"`
	AcceptHostKey      bool   `json:"accept_host_key,omitempty"`
}

type SelfHostedRelayCheckResult struct {
	ServiceStatus   string                  `json:"service_status"`
	HostTrust       deployssh.HostTrust     `json:"host_trust"`
	HostFingerprint string                  `json:"host_fingerprint,omitempty"`
	Health          []deployssh.HealthCheck `json:"health"`
}

type SelfHostedRelayDeployResult struct {
	ServiceName     string                   `json:"service_name"`
	ServiceStatus   string                   `json:"service_status"`
	HostTrust       deployssh.HostTrust      `json:"host_trust"`
	HostFingerprint string                   `json:"host_fingerprint,omitempty"`
	Invite          CreateInviteResult       `json:"invite"`
	Health          []deployssh.HealthCheck  `json:"health"`
	Rollback        deployssh.RollbackResult `json:"rollback,omitempty"`
	Layout          deployssh.Layout         `json:"layout"`
}

func (m Manager) CheckSelfHostedRelay(ctx context.Context, req SelfHostedRelayRequest) (SelfHostedRelayCheckResult, error) {
	req = defaultSelfHostedRelayRequest(req)
	if err := validateSelfHostedRelaySSH(req); err != nil {
		return SelfHostedRelayCheckResult{}, err
	}
	client, trust, err := deployssh.Dial(ctx, deployssh.DeployRequest{
		Host:          sshHost(req.CloudServerAddress),
		Port:          req.SSHPort,
		Auth:          deployAuth(req),
		KnownHosts:    deployssh.FileKnownHostStore{Path: m.sshKnownHostsPath()},
		AcceptHostKey: req.AcceptHostKey,
	})
	if err != nil {
		return SelfHostedRelayCheckResult{}, err
	}
	defer client.Close()
	layout := deployssh.DefaultLayout()
	health, status, _ := deployssh.Health(ctx, client, layout, req.ListenPort)
	return SelfHostedRelayCheckResult{
		ServiceStatus:   status,
		HostTrust:       trust,
		HostFingerprint: trust.Fingerprint,
		Health:          health,
	}, nil
}

func (m Manager) DeploySelfHostedRelay(ctx context.Context, req SelfHostedRelayRequest) (SelfHostedRelayDeployResult, error) {
	req = defaultSelfHostedRelayRequest(req)
	if err := validateSelfHostedRelaySSH(req); err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	if strings.TrimSpace(req.PublicAddress) == "" {
		return SelfHostedRelayDeployResult{}, errors.New("服务器公网访问地址不能为空")
	}
	agentPath := strings.TrimSpace(req.AgentBinaryPath)
	if agentPath == "" {
		found, err := findLinuxAgentBinary(m.baseDir())
		if err != nil {
			return SelfHostedRelayDeployResult{}, err
		}
		agentPath = found
	}
	agentBytes, err := os.ReadFile(agentPath)
	if err != nil {
		return SelfHostedRelayDeployResult{}, fmt.Errorf("读取 Linux mesh-agent 产物失败：%w", err)
	}
	stageDir, err := os.MkdirTemp("", "meshlink-self-relay-*")
	if err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	defer os.RemoveAll(stageDir)

	server := normalizeServerAddress(req.PublicAddress, req.ListenPort)
	if err := m.validateInviteServerResolution(server); err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	dnsNames, ipAddrs := certificateSANsForServer(server)
	stage := Manager{
		BaseDir: stageDir,
		LocalIPv4: func() (string, error) {
			return "0.0.0.0", nil
		},
	}
	if _, err := stage.CreateHub(CreateHubRequest{
		NetworkName: "我的组网",
		NodeName:    selfRelayHubNodeName,
		ListenPort:  req.ListenPort,
		DNSNames:    dnsNames,
		IPAddrs:     ipAddrs,
	}); err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	invite, err := stage.CreateInvite(CreateInviteRequest{
		Server:          server,
		Protocol:        "tcp_tls_v1",
		LongLived:       req.LongLived,
		MaxUses:         req.MaxUses,
		ReplaceExisting: true,
	})
	if err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	layout := deployssh.DefaultLayout()
	bundle, err := relayBundle(stageDir, layout, agentBytes, req.ListenPort, server)
	if err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	deployer := m.SelfRelayDeployer
	if deployer == nil {
		deployer = deployssh.DefaultDeployer{}
	}
	provision, err := deployer.Deploy(ctx, deployssh.DeployRequest{
		Host:          sshHost(req.CloudServerAddress),
		Port:          req.SSHPort,
		Auth:          deployAuth(req),
		Bundle:        bundle,
		Layout:        layout,
		KnownHosts:    deployssh.FileKnownHostStore{Path: m.sshKnownHostsPath()},
		AcceptHostKey: false,
	})
	if err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	publicHealth, err := m.checkPublicRelayHealth(ctx, server)
	provision.Health = append(provision.Health, publicHealth)
	if err != nil {
		return SelfHostedRelayDeployResult{}, &deployssh.ProvisionError{Stage: deployssh.StageHealthCheck, Err: err}
	}
	if err := m.writeAudit("self_relay_deployed", map[string]any{
		"cloud_server": sshHost(req.CloudServerAddress),
		"ssh_port":     req.SSHPort,
		"listen_port":  req.ListenPort,
		"public_addr":  server,
		"service":      provision.ServiceName,
		"status":       provision.ServiceStatus,
		"fingerprint":  provision.HostTrust.Fingerprint,
	}); err != nil {
		return SelfHostedRelayDeployResult{}, err
	}
	return SelfHostedRelayDeployResult{
		ServiceName:     provision.ServiceName,
		ServiceStatus:   provision.ServiceStatus,
		HostTrust:       provision.HostTrust,
		HostFingerprint: provision.HostTrust.Fingerprint,
		Invite:          invite,
		Health:          provision.Health,
		Rollback:        provision.Rollback,
		Layout:          provision.Layout,
	}, nil
}

func (m Manager) checkPublicRelayHealth(ctx context.Context, server string) (deployssh.HealthCheck, error) {
	url := relayEnrollHealthURL(server)
	client := m.HTTPClient
	if client == nil {
		client = bootstrapHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return deployssh.HealthCheck{Name: "公网接入接口", Status: "fail", Detail: err.Error()}, err
	}
	res, err := client.Do(req)
	if err != nil {
		detail := fmt.Sprintf("公网接入接口不可访问 %s：%v。请检查服务器公网访问地址、云服务器安全组、防火墙和路由器端口映射。", server, err)
		return deployssh.HealthCheck{Name: "公网接入接口", Status: "fail", Detail: detail}, errors.New(detail)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail := fmt.Sprintf("公网接入接口不可访问 %s：HTTP %s %s。请检查服务器公网访问地址、云服务器安全组、防火墙和路由器端口映射。", server, res.Status, strings.TrimSpace(string(body)))
		return deployssh.HealthCheck{Name: "公网接入接口", Status: "fail", Detail: detail}, errors.New(detail)
	}
	return deployssh.HealthCheck{Name: "公网接入接口", Status: "ok", Detail: url}, nil
}

func relayEnrollHealthURL(server string) string {
	if strings.HasPrefix(server, "http://") || strings.HasPrefix(server, "https://") {
		return strings.TrimRight(server, "/") + "/enroll/health"
	}
	return "https://" + strings.TrimRight(server, "/") + "/enroll/health"
}

func defaultSelfHostedRelayRequest(req SelfHostedRelayRequest) SelfHostedRelayRequest {
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	if req.ListenPort == 0 {
		req.ListenPort = 8443
	}
	if !req.LongLived && req.MaxUses == 0 {
		req.LongLived = true
		req.MaxUses = 10
	}
	if req.LongLived && req.MaxUses <= 0 {
		req.MaxUses = 10
	}
	return req
}

func validateSelfHostedRelaySSH(req SelfHostedRelayRequest) error {
	if strings.TrimSpace(req.CloudServerAddress) == "" {
		return errors.New("云服务器地址不能为空")
	}
	if req.SSHPort <= 0 || req.SSHPort > 65535 {
		return errors.New("SSH 端口必须在 1-65535 之间")
	}
	if strings.TrimSpace(req.SSHUsername) == "" {
		return errors.New("SSH 用户名不能为空")
	}
	if strings.TrimSpace(req.SSHPassword) == "" && strings.TrimSpace(req.SSHPrivateKey) == "" {
		return errors.New("请填写 SSH 密码或私钥")
	}
	if req.ListenPort <= 0 || req.ListenPort > 65535 {
		return errors.New("Meshlink 监听端口必须在 1-65535 之间")
	}
	return nil
}

func deployAuth(req SelfHostedRelayRequest) deployssh.AuthConfig {
	return deployssh.AuthConfig{
		Username:      strings.TrimSpace(req.SSHUsername),
		Password:      req.SSHPassword,
		PrivateKeyPEM: req.SSHPrivateKey,
	}
}

func relayBundle(stageDir string, layout deployssh.Layout, agent []byte, listenPort int, publicAddress string) (deployssh.Bundle, error) {
	files := []deployssh.BundleFile{
		{RemotePath: layout.AgentBinaryPath, Content: agent, Mode: 0o755},
	}
	add := func(remotePath, localPath string, mode fs.FileMode) error {
		b, err := os.ReadFile(localPath)
		if err != nil {
			return err
		}
		files = append(files, deployssh.BundleFile{RemotePath: remotePath, Content: b, Mode: mode})
		return nil
	}
	if err := add(layout.ConfigPath, filepath.Join(stageDir, "configs", "active.json"), 0o600); err != nil {
		return deployssh.Bundle{}, err
	}
	if err := add(layout.CAPath, filepath.Join(stageDir, "certs", "ca.pem"), 0o644); err != nil {
		return deployssh.Bundle{}, err
	}
	if err := add(layout.CAKeyPath, filepath.Join(stageDir, "certs", "ca-key.pem"), 0o600); err != nil {
		return deployssh.Bundle{}, err
	}
	if err := add(layout.HubCertPath(selfRelayHubNodeName), filepath.Join(stageDir, "certs", selfRelayHubNodeName+".pem"), 0o644); err != nil {
		return deployssh.Bundle{}, err
	}
	if err := add(layout.HubKeyPath(selfRelayHubNodeName), filepath.Join(stageDir, "certs", selfRelayHubNodeName+"-key.pem"), 0o600); err != nil {
		return deployssh.Bundle{}, err
	}
	if err := add(layout.InviteStorePath, filepath.Join(stageDir, "invites", "invites.json"), 0o600); err != nil {
		return deployssh.Bundle{}, err
	}
	return deployssh.Bundle{PublicAddress: publicAddress, ListenPort: listenPort, Files: files}, nil
}

func findLinuxAgentBinary(baseDir string) (string, error) {
	exe, _ := os.Executable()
	candidates := []string{
		filepath.Join(baseDir, "bin", "linux", "mesh-agent"),
		filepath.Join(baseDir, "bin", "mesh-agent-linux"),
		filepath.Join(baseDir, "mesh-agent-linux"),
	}
	if exe != "" {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "linux", "mesh-agent"),
			filepath.Join(exeDir, "mesh-agent-linux"),
		)
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("Linux mesh-agent 产物不存在，请先运行 scripts/build-linux-agent.ps1 或 scripts/build.ps1")
}

func (m Manager) sshKnownHostsPath() string {
	return filepath.Join(m.configsDir(), "ssh_known_hosts.json")
}

func sshHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Hostname()
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(raw, "[]")
}

func (r SelfHostedRelayRequest) String() string {
	type safe SelfHostedRelayRequest
	copy := safe(r)
	copy.SSHPassword = redacted(r.SSHPassword)
	copy.SSHPrivateKey = redacted(r.SSHPrivateKey)
	b, _ := json.Marshal(copy)
	return string(b)
}

func redacted(value string) string {
	if value == "" {
		return ""
	}
	return "<redacted>"
}

func formatRelayInviteExpiry(invite CreateInviteResult) string {
	if invite.LongLived {
		return "长期有效"
	}
	if invite.ExpiresAt.IsZero() {
		return "未设置"
	}
	return invite.ExpiresAt.Format("2006-01-02 15:04:05")
}

func parseRelayPort(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 || n > 65535 {
		return fallback
	}
	return n
}
