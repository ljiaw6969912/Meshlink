package onboarding

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/deployssh"
)

func TestCreateHubWritesManagedConfig(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)

	result, err := mgr.CreateHub(CreateHubRequest{
		NetworkName: "我的组网",
		NodeName:    "home-pc",
		ListenPort:  8443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigPath != filepath.Join(dir, "configs", "active.json") {
		t.Fatalf("ConfigPath = %q", result.ConfigPath)
	}
	if result.VirtualIP != "10.77.0.1" {
		t.Fatalf("VirtualIP = %q, want 10.77.0.1", result.VirtualIP)
	}
	assertFileExists(t, filepath.Join(dir, "certs", "ca.pem"))
	assertFileExists(t, filepath.Join(dir, "certs", "ca-key.pem"))
	assertFileExists(t, filepath.Join(dir, "certs", "home-pc.pem"))
	assertFileExists(t, filepath.Join(dir, "certs", "home-pc-key.pem"))

	var cfg config.Config
	readJSON(t, result.ConfigPath, &cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "hub" || cfg.NodeID != "home-pc" {
		t.Fatalf("unexpected config identity: mode=%q node=%q", cfg.Mode, cfg.NodeID)
	}
	if cfg.Transport.Protocol != "tcp_tls_v1" || cfg.Transport.Listen != "192.168.1.23:8443" {
		t.Fatalf("unexpected transport: %+v", cfg.Transport)
	}
	if result.Listen != "192.168.1.23:8443" {
		t.Fatalf("result.Listen = %q, want local LAN listener", result.Listen)
	}
	if cfg.Setup.Address != "10.77.0.1/24" {
		t.Fatalf("setup address = %q", cfg.Setup.Address)
	}
}

func TestStartServerModeCreatesHubAndOneTimeInvite(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)

	result, err := mgr.StartServerMode(StartServerRequest{
		ServerAddress: "desk.example.com",
		ListenPort:    9443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigPath != filepath.Join(dir, "configs", "active.json") {
		t.Fatalf("ConfigPath = %q", result.ConfigPath)
	}
	if result.VirtualIP != "10.77.0.1" {
		t.Fatalf("VirtualIP = %q, want 10.77.0.1", result.VirtualIP)
	}
	if result.Invite.Server != "desk.example.com:9443" {
		t.Fatalf("Invite.Server = %q, want desk.example.com:9443", result.Invite.Server)
	}
	if !regexp.MustCompile(`^\d{6}$`).MatchString(result.Invite.Code) {
		t.Fatalf("Invite.Code = %q, want 6 digits", result.Invite.Code)
	}
	if result.Invite.Link == "" {
		t.Fatal("invite link was not generated")
	}

	var store InviteStore
	readJSON(t, filepath.Join(dir, "invites", "invites.json"), &store)
	if len(store.Invites) != 1 {
		t.Fatalf("invite store length = %d, want 1", len(store.Invites))
	}
	stored := store.Invites[0]
	if stored.Code != result.Invite.Code {
		t.Fatalf("stored invite code = %q, want %q for restart recovery", stored.Code, result.Invite.Code)
	}
	if stored.CodeHash == "" {
		t.Fatal("stored invite missing access code hash")
	}
	if stored.MaxUses != 1 {
		t.Fatalf("MaxUses = %d, want one-time invite", stored.MaxUses)
	}
	if stored.ExpiresAt.Sub(stored.CreatedAt) != 10*time.Minute {
		t.Fatalf("invite TTL = %s, want 10m", stored.ExpiresAt.Sub(stored.CreatedAt))
	}
}

func TestStartServerModeIssuesHubCertificateForPublicAddress(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)

	result, err := mgr.StartServerMode(StartServerRequest{
		ServerAddress: "desk.example.com",
		ListenPort:    9443,
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	readJSON(t, result.ConfigPath, &cfg)
	cert := readCertificate(t, filepath.Join(dir, "certs", cfg.NodeID+".pem"))
	if err := cert.VerifyHostname("desk.example.com"); err != nil {
		t.Fatalf("hub certificate does not verify for invite host: %v", err)
	}
}

func TestPreferredListenIPv4RejectsVirtualAndFakeIPRanges(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"192.168.1.23", true},
		{"172.16.2.3", true},
		{"10.0.0.9", true},
		{"10.77.0.1", false},
		{"198.18.0.108", false},
		{"127.0.0.1", false},
	}
	for _, tt := range tests {
		if got := isPreferredListenIPv4(net.ParseIP(tt.ip)); got != tt.want {
			t.Fatalf("isPreferredListenIPv4(%s) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

func TestCreateInviteAndEnrollConsumesInvite(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	if _, err := mgr.CreateHub(CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(CreateInviteRequest{
		Server: "example.com:8443",
		TTL:    10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseInviteLink(invite.Link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Protocol != "tcp_tls_v1" || parsed.Server != "example.com:8443" || parsed.Token != invite.Token {
		t.Fatalf("unexpected parsed invite: %+v", parsed)
	}

	spokeDir := t.TempDir()
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: spokeDir, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := mgr.HandleEnroll(EnrollRequest{
		Token:      invite.Token,
		Code:       invite.Code,
		NodeName:   "laptop",
		CSRPEM:     csr.CSRPEM,
		SourceAddr: "198.51.100.20:55123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.CAPEM) == 0 || len(response.CertPEM) == 0 {
		t.Fatal("enrollment did not return CA and certificate PEM")
	}
	if len(response.PrivateKeyPEM) != 0 {
		t.Fatal("hub must not return spoke private key")
	}
	if response.Config.VirtualIP != "10.77.0.2" {
		t.Fatalf("VirtualIP = %q, want 10.77.0.2", response.Config.VirtualIP)
	}
	if response.Config.Transport.Connect != "example.com:8443" {
		t.Fatalf("Connect = %q, want example.com:8443", response.Config.Transport.Connect)
	}

	var registry DeviceRegistry
	readJSON(t, filepath.Join(dir, "configs", "devices.json"), &registry)
	if len(registry.Nodes) != 1 || registry.Nodes[0].NodeID != "laptop" {
		t.Fatalf("unexpected registry: %+v", registry)
	}
	if registry.Nodes[0].SourceAddr == "" {
		t.Fatalf("registry did not record enrollment source address: %+v", registry.Nodes[0])
	}

	if _, err := mgr.HandleEnroll(EnrollRequest{
		Token:    invite.Token,
		Code:     invite.Code,
		NodeName: "another",
		CSRPEM:   csr.CSRPEM,
	}); err == nil {
		t.Fatal("expected reused invite to fail")
	}
}

func TestCreateLongLivedInviteDoesNotExpireAfterUse(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	if _, err := mgr.CreateHub(CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(CreateInviteRequest{
		Server:    "example.com:8443",
		LongLived: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	csr1, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.HandleEnroll(EnrollRequest{
		Token:    invite.Token,
		Code:     invite.Code,
		NodeName: "laptop",
		CSRPEM:   csr1.CSRPEM,
	}); err != nil {
		t.Fatal(err)
	}

	csr2, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "tablet"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.HandleEnroll(EnrollRequest{
		Token:    invite.Token,
		Code:     invite.Code,
		NodeName: "tablet",
		CSRPEM:   csr2.CSRPEM,
	}); err != nil {
		t.Fatal(err)
	}

	var store InviteStore
	readJSON(t, filepath.Join(dir, "invites", "invites.json"), &store)
	if len(store.Invites) != 1 {
		t.Fatalf("invite store length = %d, want 1", len(store.Invites))
	}
	if !store.Invites[0].LongLived {
		t.Fatal("invite was not marked long-lived")
	}
	if !store.Invites[0].ExpiresAt.IsZero() {
		t.Fatalf("long-lived invite ExpiresAt = %s, want zero", store.Invites[0].ExpiresAt)
	}
	if store.Invites[0].Uses != 2 {
		t.Fatalf("Uses = %d, want 2", store.Invites[0].Uses)
	}
}

func TestCreateInviteCanReplaceExistingInvites(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)

	first, err := mgr.CreateInvite(CreateInviteRequest{Server: "example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.CreateInvite(CreateInviteRequest{
		Server:          "example.com:8443",
		ReplaceExisting: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("replacement invite reused the old token")
	}

	var store InviteStore
	readJSON(t, filepath.Join(dir, "invites", "invites.json"), &store)
	if len(store.Invites) != 1 {
		t.Fatalf("invite store length = %d, want 1", len(store.Invites))
	}
	if store.Invites[0].Token != second.Token {
		t.Fatalf("stored token = %q, want replacement token %q", store.Invites[0].Token, second.Token)
	}
}

func TestCreateInviteStoresDisplayCodeForRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)

	invite, err := mgr.CreateInvite(CreateInviteRequest{Server: "example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	var store InviteStore
	readJSON(t, filepath.Join(dir, "invites", "invites.json"), &store)
	if len(store.Invites) != 1 {
		t.Fatalf("invite store length = %d, want 1", len(store.Invites))
	}
	if store.Invites[0].Code != invite.Code {
		t.Fatalf("stored code = %q, want %q", store.Invites[0].Code, invite.Code)
	}
	if store.Invites[0].CodeHash == "" {
		t.Fatal("stored invite should keep a code hash for verification")
	}

	latest, ok, err := mgr.LatestInvite()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("LatestInvite returned ok=false")
	}
	if latest.Code != invite.Code || latest.Link != invite.Link {
		t.Fatalf("latest invite = %+v, want code/link from created invite %+v", latest, invite)
	}
}

func TestInviteFiveFailuresInvalidates(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	if _, err := mgr.CreateHub(CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(CreateInviteRequest{Server: "example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := mgr.HandleEnroll(EnrollRequest{
			Token:    invite.Token,
			Code:     "000000",
			NodeName: "laptop",
			CSRPEM:   csr.CSRPEM,
		}); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", i+1)
		}
	}
	if _, err := mgr.HandleEnroll(EnrollRequest{
		Token:    invite.Token,
		Code:     invite.Code,
		NodeName: "laptop",
		CSRPEM:   csr.CSRPEM,
	}); err == nil {
		t.Fatal("expected invalidated invite to fail")
	}
}

func TestJoinSpokeWritesLocalMaterialAndConfig(t *testing.T) {
	hubDir := t.TempDir()
	hub := testManager(hubDir)
	if _, err := hub.CreateHub(CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/enroll/request" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var req EnrollHTTPRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		resp, err := hub.HandleEnroll(EnrollRequest{
			Token:    req.Token,
			Code:     req.Code,
			NodeName: req.NodeName,
			CSRPEM:   []byte(req.CSRPEM),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(EnrollHTTPResponse{
			OK:      true,
			CAPEM:   string(resp.CAPEM),
			CertPEM: string(resp.CertPEM),
			Config:  resp.Config,
		})
	}))
	defer server.Close()
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	spokeDir := t.TempDir()
	spoke := Manager{BaseDir: spokeDir, HTTPClient: server.Client(), LocalIPv4: func() (string, error) { return "192.168.1.24", nil }}
	result, err := spoke.JoinSpoke(JoinSpokeRequest{
		InviteLink: invite.Link,
		Code:       invite.Code,
		NodeName:   "laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigPath != filepath.Join(spokeDir, "configs", "active.json") {
		t.Fatalf("ConfigPath = %q", result.ConfigPath)
	}
	assertFileExists(t, filepath.Join(spokeDir, "certs", "ca.pem"))
	assertFileExists(t, filepath.Join(spokeDir, "certs", "laptop.pem"))
	assertFileExists(t, filepath.Join(spokeDir, "certs", "laptop-key.pem"))
	var cfg config.Config
	readJSON(t, result.ConfigPath, &cfg)
	if cfg.Mode != "spoke" || cfg.VirtualIP != "10.77.0.2" {
		t.Fatalf("unexpected joined config: %+v", cfg)
	}
}

func TestSelfHostedProductLoopCreatesJoinableDevices(t *testing.T) {
	hubDir := t.TempDir()
	hub := testManager(hubDir)
	server := httptest.NewServer(hub.EnrollHTTPHandler())
	defer server.Close()

	started, err := hub.StartServerMode(StartServerRequest{
		ServerAddress: server.URL,
		ListenPort:    9443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.VirtualIP != "10.77.0.1" || started.Invite.Link == "" || started.Invite.Code == "" {
		t.Fatalf("started server missing product outputs: %+v", started)
	}

	spokeDir := t.TempDir()
	spoke := Manager{BaseDir: spokeDir, HTTPClient: server.Client(), LocalIPv4: func() (string, error) { return "192.168.1.24", nil }}
	joined, err := spoke.JoinSpoke(JoinSpokeRequest{
		InviteLink: started.Invite.Link,
		Code:       started.Invite.Code,
		NodeName:   "office-pc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if joined.VirtualIP != "10.77.0.2" {
		t.Fatalf("joined virtual IP = %q, want 10.77.0.2", joined.VirtualIP)
	}
	assertFileExists(t, filepath.Join(spokeDir, "certs", "office-pc-key.pem"))

	devices, err := hub.Devices("")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices.Nodes) != 2 {
		t.Fatalf("device count = %d, want self and joined peer: %+v", len(devices.Nodes), devices.Nodes)
	}
	var peer DeviceSummary
	for _, node := range devices.Nodes {
		if node.Kind == "peer" && node.NodeID == "office-pc" {
			peer = node
			break
		}
	}
	if peer.NodeID == "" {
		t.Fatalf("joined peer missing from device list: %+v", devices.Nodes)
	}
	if peer.Status != "offline" || peer.VirtualIP != "10.77.0.2" || peer.RemoteAddr == "" || peer.Fingerprint == "" || peer.LastSeen.IsZero() {
		t.Fatalf("joined peer missing device list fields: %+v", peer)
	}
}

func TestDeploySelfHostedRelayStagesRemoteHubAndInvite(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "mesh-agent-linux")
	if err := os.WriteFile(agentPath, []byte("linux-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &captureSelfRelayDeployer{
		result: deployssh.ProvisionResult{
			ServiceName:   deployssh.ServiceName,
			ServiceStatus: "running",
			HostTrust: deployssh.HostTrust{
				Status:      deployssh.HostTrustNew,
				Fingerprint: "SHA256:test",
			},
			Health: []deployssh.HealthCheck{{Name: "enroll health", Status: "ok", Detail: "reachable"}},
		},
	}
	mgr := Manager{
		BaseDir:           dir,
		SelfRelayDeployer: fake,
		ResolveHost: func(host string) ([]net.IP, error) {
			if host != "relay.example.com" {
				t.Fatalf("resolved host = %q, want public access host", host)
			}
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://relay.example.com:9443/enroll/health" {
					t.Fatalf("public health URL = %s", r.URL.String())
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
					Header:     make(http.Header),
					Request:    r,
				}, nil
			}),
		},
	}

	result, err := mgr.DeploySelfHostedRelay(context.Background(), SelfHostedRelayRequest{
		CloudServerAddress: "203.0.113.10",
		SSHPort:            22,
		SSHUsername:        "root",
		SSHPassword:        "secret",
		SSHPrivateKey:      "BEGIN OPENSSH PRIVATE KEY secret-key",
		ListenPort:         9443,
		PublicAddress:      "relay.example.com",
		AgentBinaryPath:    agentPath,
		LongLived:          true,
		MaxUses:            10,
		AcceptHostKey:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Invite.Code == "" || result.Invite.Link == "" || result.Invite.Server != "relay.example.com:9443" {
		t.Fatalf("invite = %+v, want relay.example.com:9443 with code/link", result.Invite)
	}
	if result.ServiceStatus != "running" || result.HostFingerprint != "SHA256:test" {
		t.Fatalf("result = %+v, want running with fingerprint", result)
	}
	if !hasHealthCheck(result.Health, "公网接入接口", "ok") {
		t.Fatalf("result health = %+v, want public enroll health ok", result.Health)
	}
	if fake.request.AcceptHostKey {
		t.Fatal("deployment should require a previously confirmed SSH host fingerprint")
	}
	if strings.Contains(fake.request.String(), "secret") {
		t.Fatalf("deployer request leaked SSH password: %+v", fake.request)
	}
	auditLog, err := os.ReadFile(filepath.Join(dir, "logs", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "BEGIN OPENSSH PRIVATE KEY", "secret-key"} {
		if strings.Contains(string(auditLog), forbidden) {
			t.Fatalf("audit log leaked %q: %s", forbidden, auditLog)
		}
	}

	layout := deployssh.DefaultLayout()
	cfgFile, ok := fake.fileByPath(layout.ConfigPath)
	if !ok {
		t.Fatalf("staged files missing remote config: %+v", fake.request.Bundle.Files)
	}
	var cfg config.Config
	if err := json.Unmarshal(cfgFile.Content, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "hub" || cfg.Listen != "0.0.0.0:9443" {
		t.Fatalf("remote config = %+v, want hub listening on 0.0.0.0:9443", cfg)
	}
	for _, want := range []string{layout.AgentBinaryPath, layout.CAKeyPath, layout.InviteStorePath} {
		if _, ok := fake.fileByPath(want); !ok {
			t.Fatalf("staged files missing %s", want)
		}
	}
}

func TestDeploySelfHostedRelayValidatesInputsBeforeSSH(t *testing.T) {
	mgr := Manager{BaseDir: t.TempDir(), SelfRelayDeployer: &captureSelfRelayDeployer{}}
	_, err := mgr.DeploySelfHostedRelay(context.Background(), SelfHostedRelayRequest{
		CloudServerAddress: "",
		SSHPort:            22,
		SSHUsername:        "root",
		ListenPort:         9443,
		PublicAddress:      "relay.example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "云服务器地址不能为空") {
		t.Fatalf("error = %v, want cloud server address validation", err)
	}
}

func TestDeploySelfHostedRelayFailsWhenPublicEnrollHealthIsUnreachable(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "mesh-agent-linux")
	if err := os.WriteFile(agentPath, []byte("linux-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := Manager{
		BaseDir: dir,
		SelfRelayDeployer: &captureSelfRelayDeployer{
			result: deployssh.ProvisionResult{
				ServiceName:   deployssh.ServiceName,
				ServiceStatus: "running",
			},
		},
		ResolveHost: func(host string) ([]net.IP, error) {
			if host != "relay.example.com" {
				t.Fatalf("resolved host = %q, want public access host", host)
			}
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://relay.example.com:9443/enroll/health" {
					t.Fatalf("public health URL = %s", r.URL.String())
				}
				return nil, io.EOF
			}),
		},
	}

	_, err := mgr.DeploySelfHostedRelay(context.Background(), SelfHostedRelayRequest{
		CloudServerAddress: "203.0.113.10",
		SSHPort:            22,
		SSHUsername:        "root",
		SSHPassword:        "secret",
		ListenPort:         9443,
		PublicAddress:      "relay.example.com",
		AgentBinaryPath:    agentPath,
	})
	if err == nil {
		t.Fatal("expected public health failure")
	}
	for _, want := range []string{"公网接入接口不可访问", "relay.example.com:9443", "端口映射"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}

func TestDeploySelfHostedRelayRejectsPublicAddressResolvingToProxyFakeIPBeforeSSH(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "mesh-agent-linux")
	if err := os.WriteFile(agentPath, []byte("linux-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &captureSelfRelayDeployer{}
	mgr := Manager{
		BaseDir:           dir,
		SelfRelayDeployer: fake,
		ResolveHost: func(host string) ([]net.IP, error) {
			if host != "relay.example.test" {
				t.Fatalf("resolved host = %q, want public access host", host)
			}
			return []net.IP{net.ParseIP("198.18.0.146")}, nil
		},
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("public health should not run after fake-ip detection")
				return nil, nil
			}),
		},
	}

	_, err := mgr.DeploySelfHostedRelay(context.Background(), SelfHostedRelayRequest{
		CloudServerAddress: "192.168.1.32",
		SSHPort:            22,
		SSHUsername:        "root",
		SSHPassword:        "secret",
		ListenPort:         8443,
		PublicAddress:      "relay.example.test",
		AgentBinaryPath:    agentPath,
	})
	if err == nil {
		t.Fatal("expected fake-ip validation error")
	}
	if fake.called {
		t.Fatal("deployment should stop before SSH when public address resolves to fake-ip")
	}
	for _, want := range []string{"代理 TUN", "fake-ip", "198.18.0.146", "DIRECT", "fake-ip-filter"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}

func TestJoinSpokeWrapsEnrollConnectionErrors(t *testing.T) {
	dir := t.TempDir()
	mgr := Manager{
		BaseDir: dir,
		ResolveHost: func(string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, io.EOF
			}),
		},
	}

	_, err := mgr.JoinSpoke(JoinSpokeRequest{
		InviteLink: "meshlink://join?server=example.com:5858&protocol=tcp_tls_v1&token=tok123",
		Code:       "123456",
		NodeName:   "client",
	})
	if err == nil {
		t.Fatal("expected connection error")
	}
	for _, want := range []string{"无法连接服务器接入接口", "example.com:5858", "域名", "端口"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}

func TestJoinSpokeMapsEnrollmentFailuresToUserActions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "wrong code",
			body: "verification code is incorrect",
			want: []string{"接入码不正确", "6 位"},
		},
		{
			name: "expired invite",
			body: "invite has expired",
			want: []string{"接入链接已过期", "重新生成"},
		},
		{
			name: "used invite",
			body: "invite has already been used",
			want: []string{"接入链接已被使用", "重新生成"},
		},
		{
			name: "device limit",
			body: "invite device limit has been reached",
			want: []string{"设备数量已达到上限", "新的接入链接"},
		},
		{
			name: "token missing",
			body: "invite token was not found",
			want: []string{"接入链接无效", "复制完整"},
		},
		{
			name: "invalidated",
			body: "invite has been invalidated",
			want: []string{"错误次数过多", "重新生成"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, tt.body, http.StatusBadRequest)
			}))
			defer server.Close()

			dir := t.TempDir()
			mgr := Manager{
				BaseDir:    dir,
				HTTPClient: server.Client(),
			}
			_, err := mgr.JoinSpoke(JoinSpokeRequest{
				InviteLink: "meshlink://join?server=" + server.URL + "&protocol=tcp_tls_v1&token=tok123",
				Code:       "123456",
				NodeName:   "client",
			})
			if err == nil {
				t.Fatal("expected join failure")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestJoinSpokeDetectsProxyFakeIPResolution(t *testing.T) {
	dir := t.TempDir()
	mgr := Manager{
		BaseDir: dir,
		ResolveHost: func(host string) ([]net.IP, error) {
			if host != "relay.example.test" {
				t.Fatalf("resolved host = %q, want invite host", host)
			}
			return []net.IP{net.ParseIP("198.18.0.8")}, nil
		},
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("HTTP request should not run after fake-ip detection")
				return nil, nil
			}),
		},
	}

	_, err := mgr.JoinSpoke(JoinSpokeRequest{
		InviteLink: "meshlink://join?server=relay.example.test:5858&protocol=tcp_tls_v1&token=tok123",
		Code:       "123456",
		NodeName:   "client",
	})
	if err == nil {
		t.Fatal("expected fake-ip detection error")
	}
	for _, want := range []string{"代理 TUN", "fake-ip", "198.18.0.8", "DIRECT", "fake-ip-filter"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory", path)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func hasHealthCheck(checks []deployssh.HealthCheck, name, status string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

func testManager(baseDir string) Manager {
	return Manager{
		BaseDir: baseDir,
		LocalIPv4: func() (string, error) {
			return "192.168.1.23", nil
		},
	}
}

func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("%s did not contain a PEM certificate", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

type captureSelfRelayDeployer struct {
	request deployssh.DeployRequest
	result  deployssh.ProvisionResult
	called  bool
}

func (d *captureSelfRelayDeployer) Deploy(_ context.Context, req deployssh.DeployRequest) (deployssh.ProvisionResult, error) {
	d.called = true
	d.request = req
	return d.result, nil
}

func (d *captureSelfRelayDeployer) fileByPath(path string) (deployssh.BundleFile, bool) {
	for _, file := range d.request.Bundle.Files {
		if file.RemotePath == path {
			return file, true
		}
	}
	return deployssh.BundleFile{}, false
}
