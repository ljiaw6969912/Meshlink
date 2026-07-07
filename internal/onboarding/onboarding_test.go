package onboarding

import (
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestJoinSpokeDetectsProxyFakeIPResolution(t *testing.T) {
	dir := t.TempDir()
	mgr := Manager{
		BaseDir: dir,
		ResolveHost: func(host string) ([]net.IP, error) {
			if host != "openwrt.ljiaw6969912.top" {
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
		InviteLink: "meshlink://join?server=openwrt.ljiaw6969912.top:5858&protocol=tcp_tls_v1&token=tok123",
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
