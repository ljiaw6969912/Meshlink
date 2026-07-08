package deployssh

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestProvisionUploadsFilesInstallsSystemdServiceAndChecksHealth(t *testing.T) {
	layout := DefaultLayout()
	client := newFakeClient()
	bundle := Bundle{
		PublicAddress: "relay.example.com:9443",
		ListenPort:    9443,
		Files: []BundleFile{
			{RemotePath: layout.AgentBinaryPath, Content: []byte("agent"), Mode: 0o755},
			{RemotePath: layout.ConfigPath, Content: []byte(`{"mode":"hub"}`), Mode: 0o600},
			{RemotePath: layout.CAPath, Content: []byte("ca"), Mode: 0o644},
			{RemotePath: layout.CAKeyPath, Content: []byte("ca-key"), Mode: 0o600},
			{RemotePath: layout.HubCertPath("meshlink-hub"), Content: []byte("cert"), Mode: 0o644},
			{RemotePath: layout.HubKeyPath("meshlink-hub"), Content: []byte("key"), Mode: 0o600},
			{RemotePath: layout.InviteStorePath, Content: []byte(`{"invites":[]}`), Mode: 0o600},
		},
	}

	result, err := Provision(context.Background(), client, ProvisionRequest{Layout: layout, Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		layout.AgentBinaryPath,
		layout.ConfigPath,
		layout.CAPath,
		layout.CAKeyPath,
		layout.InviteStorePath,
		layout.ServicePath,
	} {
		if _, ok := client.uploads[path]; !ok {
			t.Fatalf("expected upload for %s, uploads=%v", path, keys(client.uploads))
		}
	}
	service := string(client.uploads[layout.ServicePath].content)
	for _, want := range []string{
		"Description=Meshlink self-hosted relay hub",
		"ExecStart=/opt/meshlink/bin/mesh-agent -config /etc/meshlink/configs/active.json",
		"Restart=on-failure",
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("service unit missing %q:\n%s", want, service)
		}
	}
	assertCommandContains(t, client.commands, "uname -s")
	assertCommandContains(t, client.commands, "command -v systemctl")
	assertCommandContains(t, client.commands, "mkdir -p /opt/meshlink/bin /etc/meshlink/configs /etc/meshlink/certs /etc/meshlink/invites /var/log/meshlink")
	assertCommandContains(t, client.commands, "systemctl restart meshlink-agent")
	assertCommandContains(t, client.commands, "systemctl is-active meshlink-agent")
	assertCommandContains(t, client.commands, "-version")
	assertCommandContains(t, client.commands, "curl -fsSk https://127.0.0.1:9443/enroll/health")
	if result.ServiceName != ServiceName || result.ServiceStatus != "running" {
		t.Fatalf("result = %+v, want running %s", result, ServiceName)
	}
	if !hasHealth(result.Health, "远程版本") {
		t.Fatalf("health = %+v, want remote version check", result.Health)
	}
}

func TestProvisionPreflightFailuresAreActionable(t *testing.T) {
	layout := DefaultLayout()
	bundle := minimalBundle(layout)
	tests := []struct {
		name       string
		match      string
		err        error
		wantStage  Stage
		wantDetail string
	}{
		{
			name:       "systemd missing",
			match:      "command -v systemctl",
			err:        errors.New("systemctl: not found"),
			wantStage:  StageCheckSystemd,
			wantDetail: "远程 systemd 不可用",
		},
		{
			name:       "permission denied",
			match:      "test-directory-permission",
			err:        errors.New("permission denied"),
			wantStage:  StageCheckPermissions,
			wantDetail: "远程目录权限不足",
		},
		{
			name:       "port occupied",
			match:      "check-listen-port",
			err:        errors.New("address already in use"),
			wantStage:  StageCheckPort,
			wantDetail: "监听端口 9443 已被占用",
		},
		{
			name:       "read only disk",
			match:      "mkdir -p /opt/meshlink/bin",
			err:        errors.New("read-only file system"),
			wantStage:  StageCreateDirs,
			wantDetail: "创建远程目录失败",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient()
			client.fail[tt.match] = tt.err
			_, err := Provision(context.Background(), client, ProvisionRequest{Layout: layout, Bundle: bundle})
			if err == nil {
				t.Fatal("expected error")
			}
			var perr *ProvisionError
			if !errors.As(err, &perr) {
				t.Fatalf("error = %T %v, want ProvisionError", err, err)
			}
			if perr.Stage != tt.wantStage || !strings.Contains(perr.Error(), tt.wantDetail) {
				t.Fatalf("error = %+v, want stage %s detail %q", perr, tt.wantStage, tt.wantDetail)
			}
		})
	}
}

func TestProvisionRejectsMissingLinuxAgentBinary(t *testing.T) {
	layout := DefaultLayout()
	bundle := minimalBundle(layout)
	bundle.Files = bundle.Files[1:]

	_, err := Provision(context.Background(), newFakeClient(), ProvisionRequest{Layout: layout, Bundle: bundle})
	if err == nil || !strings.Contains(err.Error(), "Linux mesh-agent 产物不存在") {
		t.Fatalf("error = %v, want missing Linux agent", err)
	}
}

func TestProvisionRollsBackWhenServiceRestartFails(t *testing.T) {
	layout := DefaultLayout()
	client := newFakeClient()
	client.fail["systemctl restart meshlink-agent"] = errors.New("service failed")

	_, err := Provision(context.Background(), client, ProvisionRequest{Layout: layout, Bundle: minimalBundle(layout)})
	if err == nil {
		t.Fatal("expected service restart failure")
	}
	var perr *ProvisionError
	if !errors.As(err, &perr) {
		t.Fatalf("error = %T %v, want ProvisionError", err, err)
	}
	if perr.Stage != StageRestartService {
		t.Fatalf("stage = %s, want %s", perr.Stage, StageRestartService)
	}
	if perr.Rollback.Status != "restored" {
		t.Fatalf("rollback = %+v, want restored", perr.Rollback)
	}
	assertCommandContains(t, client.commands, "journalctl -u meshlink-agent")
	assertCommandContains(t, client.commands, "rollback-restore")
}

func TestHostFingerprintTrustOnFirstUseAndChangeBlocking(t *testing.T) {
	store := NewMemoryKnownHostStore()
	_, err := VerifyHostFingerprint("203.0.113.10", 22, "SHA256:first", store, TrustStrict)
	if err == nil || !strings.Contains(err.Error(), "首次连接云服务器") {
		t.Fatalf("error = %v, want first-connect fingerprint prompt", err)
	}
	trust, err := VerifyHostFingerprint("203.0.113.10", 22, "SHA256:first", store, TrustOnFirstUse)
	if err != nil {
		t.Fatal(err)
	}
	if trust.Status != HostTrustNew || trust.Fingerprint != "SHA256:first" {
		t.Fatalf("trust = %+v, want new first fingerprint", trust)
	}

	trust, err = VerifyHostFingerprint("203.0.113.10", 22, "SHA256:first", store, TrustStrict)
	if err != nil {
		t.Fatal(err)
	}
	if trust.Status != HostTrustMatched {
		t.Fatalf("trust = %+v, want matched", trust)
	}

	_, err = VerifyHostFingerprint("203.0.113.10", 22, "SHA256:changed", store, TrustStrict)
	if err == nil || !strings.Contains(err.Error(), "SSH 主机指纹发生变化") {
		t.Fatalf("error = %v, want fingerprint change block", err)
	}
}

func TestInvalidPrivateKeyHasClearError(t *testing.T) {
	_, err := BuildAuthMethods(AuthConfig{Username: "root", PrivateKeyPEM: "not a private key"})
	if err == nil || !strings.Contains(err.Error(), "SSH 私钥格式无效") {
		t.Fatalf("error = %v, want invalid private key message", err)
	}
}

func TestSSHConnectionErrorsAreClassified(t *testing.T) {
	networkErr := friendlyDialError(errors.New("i/o timeout"))
	if !strings.Contains(networkErr.Error(), "SSH 网络不可达") {
		t.Fatalf("network error = %v, want network unreachable message", networkErr)
	}
	authErr := friendlyHandshakeError(errors.New("ssh: unable to authenticate, attempted methods [none password]"))
	if !strings.Contains(authErr.Error(), "SSH 认证失败") {
		t.Fatalf("auth error = %v, want authentication failure message", authErr)
	}
	otherErr := friendlyHandshakeError(errors.New("ssh: handshake failed"))
	if !strings.Contains(otherErr.Error(), "SSH 连接失败") {
		t.Fatalf("other error = %v, want generic SSH failure", otherErr)
	}
}

type fakeUpload struct {
	content []byte
	mode    fs.FileMode
}

type fakeClient struct {
	uploads  map[string]fakeUpload
	commands []string
	fail     map[string]error
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		uploads: make(map[string]fakeUpload),
		fail:    make(map[string]error),
	}
}

func (f *fakeClient) Run(_ context.Context, command string) (RunResult, error) {
	f.commands = append(f.commands, command)
	for match, err := range f.fail {
		if strings.Contains(command, match) {
			delete(f.fail, match)
			return RunResult{Stderr: err.Error()}, err
		}
	}
	switch {
	case strings.Contains(command, "uname -s"):
		return RunResult{Stdout: "Linux\n"}, nil
	case strings.Contains(command, "systemctl is-active meshlink-agent"):
		return RunResult{Stdout: "active\n"}, nil
	case strings.Contains(command, "/opt/meshlink/bin/mesh-agent -version"):
		return RunResult{Stdout: "0.1.0-test\n"}, nil
	case strings.Contains(command, "/enroll/health"):
		return RunResult{Stdout: `{"ok":true}` + "\n"}, nil
	default:
		return RunResult{Stdout: "ok\n"}, nil
	}
}

func (f *fakeClient) Upload(_ context.Context, remotePath string, content []byte, mode fs.FileMode) error {
	copied := append([]byte(nil), content...)
	f.uploads[remotePath] = fakeUpload{content: copied, mode: mode}
	return nil
}

func minimalBundle(layout Layout) Bundle {
	return Bundle{
		PublicAddress: "relay.example.com:9443",
		ListenPort:    9443,
		Files: []BundleFile{
			{RemotePath: layout.AgentBinaryPath, Content: []byte("agent"), Mode: 0o755},
			{RemotePath: layout.ConfigPath, Content: []byte(`{"mode":"hub"}`), Mode: 0o600},
			{RemotePath: layout.CAPath, Content: []byte("ca"), Mode: 0o644},
			{RemotePath: layout.CAKeyPath, Content: []byte("ca-key"), Mode: 0o600},
			{RemotePath: layout.HubCertPath("meshlink-hub"), Content: []byte("cert"), Mode: 0o644},
			{RemotePath: layout.HubKeyPath("meshlink-hub"), Content: []byte("key"), Mode: 0o600},
			{RemotePath: layout.InviteStorePath, Content: []byte(`{"invites":[]}`), Mode: 0o600},
		},
	}
}

func assertCommandContains(t *testing.T, commands []string, want string) {
	t.Helper()
	for _, command := range commands {
		if strings.Contains(command, want) {
			return
		}
	}
	t.Fatalf("commands do not contain %q:\n%s", want, strings.Join(commands, "\n"))
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

func hasHealth(checks []HealthCheck, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return true
		}
	}
	return false
}
