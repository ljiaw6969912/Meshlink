package onboarding

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
)

type CreateHubRequest struct {
	NetworkName string   `json:"network_name"`
	NodeName    string   `json:"node_name"`
	ListenPort  int      `json:"listen_port"`
	Protocol    string   `json:"protocol"`
	VirtualCIDR string   `json:"virtual_cidr"`
	VirtualIP   string   `json:"virtual_ip"`
	DNSNames    []string `json:"dns_names,omitempty"`
	IPAddrs     []string `json:"ip_addrs,omitempty"`
}

type CreateHubResult struct {
	ConfigPath string `json:"config_path"`
	VirtualIP  string `json:"virtual_ip"`
	Listen     string `json:"listen"`
	State      string `json:"state"`
}

type StartServerRequest struct {
	NodeName      string `json:"node_name,omitempty"`
	ServerAddress string `json:"server_address"`
	ListenPort    int    `json:"listen_port"`
	LongLived     bool   `json:"long_lived,omitempty"`
	MaxUses       int    `json:"max_uses,omitempty"`
}

type StartServerResult struct {
	ConfigPath string             `json:"config_path"`
	VirtualIP  string             `json:"virtual_ip"`
	Listen     string             `json:"listen"`
	State      string             `json:"state"`
	Invite     CreateInviteResult `json:"invite"`
}

func (m Manager) StartServerMode(req StartServerRequest) (StartServerResult, error) {
	if req.NodeName != "" {
		if err := validateEnrollmentNodeName(req.NodeName); err != nil {
			return StartServerResult{}, err
		}
	}
	if existing, err := config.Load(m.activeConfigPath()); err == nil && existing.Mode == "hub" && existing.NetworkCIDR != "" && existing.NetworkCIDR != "10.77.0.0/24" {
		return StartServerResult{}, fmt.Errorf("现有服务器使用自定义网段 %s，已保留原配置；当前简化组网模式需要 10.77.0.0/24", existing.NetworkCIDR)
	}
	hubReq := defaultCreateHubRequest(CreateHubRequest{NodeName: req.NodeName, ListenPort: req.ListenPort})
	if hubReq.ListenPort <= 0 || hubReq.ListenPort > 65535 {
		return StartServerResult{}, fmt.Errorf("listen_port must be between 1 and 65535")
	}
	server := normalizeServerAddress(req.ServerAddress, hubReq.ListenPort)
	hubReq.DNSNames, hubReq.IPAddrs = certificateSANsForServer(server)
	hub, err := m.CreateHub(hubReq)
	if err != nil {
		return StartServerResult{}, err
	}
	if err := m.EnsureServerNode(server); err != nil {
		return StartServerResult{}, err
	}
	invite, reusable, err := m.LatestInvite()
	if err != nil {
		return StartServerResult{}, err
	}
	maxUses := req.MaxUses
	if req.LongLived {
		if maxUses == 0 || maxUses < -1 {
			maxUses = defaultLongLivedMaxUses
		}
	} else if maxUses <= 0 {
		maxUses = defaultInviteMaxUses
	}
	if reusable && (invite.Server != server || invite.Protocol != hubReq.Protocol || invite.Code == "" || invite.LongLived != req.LongLived || invite.MaxUses != maxUses) {
		reusable = false
	}
	if reusable {
		_, err = m.validateInvite(EnrollRequest{Token: invite.Token, Code: invite.Code})
		reusable = err == nil
	}
	if !reusable {
		invite, err = m.CreateInvite(CreateInviteRequest{
			Server:          server,
			Protocol:        hubReq.Protocol,
			LongLived:       req.LongLived,
			MaxUses:         req.MaxUses,
			ReplaceExisting: true,
		})
	}
	if err != nil {
		return StartServerResult{}, err
	}
	return StartServerResult{
		ConfigPath: hub.ConfigPath,
		VirtualIP:  hub.VirtualIP,
		Listen:     hub.Listen,
		State:      hub.State,
		Invite:     invite,
	}, nil
}

func (m Manager) CreateHub(req CreateHubRequest) (CreateHubResult, error) {
	req = defaultCreateHubRequest(req)
	var existing *config.Config
	if _, err := os.Stat(m.activeConfigPath()); err == nil {
		loaded, err := config.Load(m.activeConfigPath())
		if err != nil {
			return CreateHubResult{}, fmt.Errorf("existing config is invalid; preserve and repair it: %w", err)
		}
		if loaded.Mode == "hub" {
			existing = loaded
		}
	}
	if err := os.MkdirAll(m.configsDir(), 0o700); err != nil {
		return CreateHubResult{}, err
	}
	if err := os.MkdirAll(m.certsDir(), 0o700); err != nil {
		return CreateHubResult{}, err
	}
	if err := m.ensureHubCA(certutil.CAOptions{
		OutDir: m.certsDir(),
		Name:   req.NetworkName,
		Days:   3650,
	}); err != nil {
		return CreateHubResult{}, err
	}
	if err := m.ensureHubCertificate(certutil.IssueOptions{
		OutDir:    m.certsDir(),
		Name:      req.NodeName,
		CAPath:    filepath.Join(m.certsDir(), "ca.pem"),
		CAKeyPath: filepath.Join(m.certsDir(), "ca-key.pem"),
		DNSNames:  req.DNSNames,
		IPAddrs:   req.IPAddrs,
		Days:      825,
	}); err != nil {
		return CreateHubResult{}, err
	}

	listenHost, err := m.localListenIPv4()
	if err != nil {
		return CreateHubResult{}, err
	}
	listen := net.JoinHostPort(listenHost, strconv.Itoa(req.ListenPort))
	cfg := config.Config{
		Version: config.ConfigVersion,
		NodeID:  req.NodeName,
		Mode:    "hub",
		Transport: config.TransportConfig{
			Protocol: config.ControlProtocolV2,
			Listen:   listen,
		},
		Listen:      listen,
		CAFile:      "../certs/ca.pem",
		CertFile:    "../certs/" + req.NodeName + ".pem",
		KeyFile:     "../certs/" + req.NodeName + "-key.pem",
		NetworkCIDR: req.VirtualCIDR,
	}
	if existing != nil {
		cfg.DisplayName = existing.DisplayName
		cfg.ServerNodeConfig = existing.ServerNodeConfig
		cfg.ServerPublicEndpoint = existing.ServerPublicEndpoint
	}
	if err := cfg.Validate(); err != nil {
		return CreateHubResult{}, err
	}
	if err := writePrettyJSON(m.activeConfigPath(), cfg); err != nil {
		return CreateHubResult{}, err
	}
	return CreateHubResult{
		ConfigPath: m.activeConfigPath(),
		VirtualIP:  req.VirtualIP,
		Listen:     listen,
		State:      "created",
	}, nil
}

func (m Manager) ensureHubCertificate(opts certutil.IssueOptions) error {
	certPath := filepath.Join(opts.OutDir, opts.Name+".pem")
	keyPath := filepath.Join(opts.OutDir, opts.Name+"-key.pem")
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if os.IsNotExist(certErr) && os.IsNotExist(keyErr) {
		_, err := certutil.Issue(opts)
		return err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("existing coordinator certificate or key is damaged; restore its identity: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(opts.CAPath)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return fmt.Errorf("invalid coordinator CA")
	}
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots}); err == nil && cert.Subject.CommonName == opts.Name {
		validNames := true
		for _, name := range append(append([]string(nil), opts.DNSNames...), opts.IPAddrs...) {
			if cert.VerifyHostname(name) != nil {
				validNames = false
				break
			}
		}
		if validNames {
			return nil
		}
	}
	// A changed public hostname or expired leaf can be reissued under the same
	// preserved CA. Unchanged starts retain the existing certificate and key.
	_, err = certutil.Issue(opts)
	return err
}

// Restarting a network must preserve the trust root used by enrolled clients.
func (m Manager) ensureHubCA(opts certutil.CAOptions) error {
	certPath := filepath.Join(opts.OutDir, "ca.pem")
	keyPath := filepath.Join(opts.OutDir, "ca-key.pem")
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if os.IsNotExist(certErr) && os.IsNotExist(keyErr) {
		for _, path := range []string{m.activeConfigPath(), m.deviceRegistryPath(), m.inviteStorePath()} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				return fmt.Errorf("existing network CA is missing; restore its backup before starting (identity was preserved)")
			}
		}
		entries, err := os.ReadDir(opts.OutDir)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("existing certificate directory has no CA; restore its backup before starting (identity was preserved)")
		}
		_, err = certutil.InitCA(opts)
		return err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("existing network CA is missing, damaged or mismatched; restore its backup (identity was preserved): %w", err)
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 || time.Now().Before(ca.NotBefore) || time.Now().After(ca.NotAfter) {
		return fmt.Errorf("existing network CA is invalid or expired; repair its certificate (identity was preserved)")
	}
	return nil
}

func defaultCreateHubRequest(req CreateHubRequest) CreateHubRequest {
	if req.NetworkName == "" {
		req.NetworkName = "我的组网"
	}
	if req.NodeName == "" {
		if host, err := os.Hostname(); err == nil && host != "" {
			req.NodeName = host
		} else {
			req.NodeName = "home-hub"
		}
	}
	if req.ListenPort == 0 {
		req.ListenPort = 8443
	}
	if req.Protocol == "" {
		req.Protocol = "tcp_tls_v1"
	}
	if req.VirtualCIDR == "" {
		req.VirtualCIDR = "10.77.0.0/24"
	}
	if req.VirtualIP == "" {
		req.VirtualIP = "10.77.0.1"
	}
	return req
}

func certificateSANsForServer(server string) ([]string, []string) {
	host := hostFromServerAddress(server)
	if host == "" {
		return nil, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return nil, nil
		}
		return nil, []string{ip.String()}
	}
	return []string{host}, nil
}

func hostFromServerAddress(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if u, err := url.Parse(server); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Hostname()
	}
	if host, _, err := net.SplitHostPort(server); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(server, "[]")
}

func normalizeServerAddress(raw string, port int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = firstUsableIPv4()
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		host := u.Hostname()
		if host != "" && u.Port() == "" {
			u.Host = net.JoinHostPort(host, strconv.Itoa(port))
		}
		return u.String()
	}
	if host, _, err := net.SplitHostPort(raw); err == nil && host != "" {
		return raw
	}
	return net.JoinHostPort(raw, strconv.Itoa(port))
}

func (m Manager) localListenIPv4() (string, error) {
	if m.LocalIPv4 != nil {
		ip, err := m.LocalIPv4()
		if err != nil {
			return "", err
		}
		if parsed := net.ParseIP(strings.TrimSpace(ip)); parsed == nil || parsed.To4() == nil {
			return "", fmt.Errorf("local IPv4 address %q is invalid", ip)
		}
		return strings.TrimSpace(ip), nil
	}
	return "0.0.0.0", nil
}

func firstIntranetIPv4() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := ipv4FromAddr(addr)
			if ip == nil {
				continue
			}
			if isPreferredListenIPv4(ip) {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("未找到本机内网 IPv4 地址，请确认网卡已连接并分配 10.x、172.16-31.x 或 192.168.x 地址")
}

func firstUsableIPv4() string {
	if ip, err := firstIntranetIPv4(); err == nil {
		return ip
	}
	return "127.0.0.1"
}

func ipv4FromAddr(addr net.Addr) net.IP {
	var ip net.IP
	switch v := addr.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	}
	if ip == nil {
		return nil
	}
	return ip.To4()
}

func isPreferredListenIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil || !isRFC1918IPv4(ip4) {
		return false
	}
	if ip4[0] == 10 && ip4[1] == 77 {
		return false
	}
	return true
}

func isRFC1918IPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip4[0] == 10 {
		return true
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	return ip4[0] == 192 && ip4[1] == 168
}

func writePrettyJSON(path string, v any) error {
	if cfg, ok := v.(config.Config); ok {
		return config.Write(path, cfg)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceJSONFile(tmp.Name(), path)
}
