package onboarding

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	ServerAddress string `json:"server_address"`
	ListenPort    int    `json:"listen_port"`
	LongLived     bool   `json:"long_lived,omitempty"`
}

type StartServerResult struct {
	ConfigPath string             `json:"config_path"`
	VirtualIP  string             `json:"virtual_ip"`
	Listen     string             `json:"listen"`
	State      string             `json:"state"`
	Invite     CreateInviteResult `json:"invite"`
}

func (m Manager) StartServerMode(req StartServerRequest) (StartServerResult, error) {
	hubReq := defaultCreateHubRequest(CreateHubRequest{ListenPort: req.ListenPort})
	if hubReq.ListenPort <= 0 || hubReq.ListenPort > 65535 {
		return StartServerResult{}, fmt.Errorf("listen_port must be between 1 and 65535")
	}
	server := normalizeServerAddress(req.ServerAddress, hubReq.ListenPort)
	hubReq.DNSNames, hubReq.IPAddrs = certificateSANsForServer(server)
	hub, err := m.CreateHub(hubReq)
	if err != nil {
		return StartServerResult{}, err
	}
	invite, err := m.CreateInvite(CreateInviteRequest{
		Server:          server,
		Protocol:        hubReq.Protocol,
		LongLived:       req.LongLived,
		ReplaceExisting: true,
	})
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
	if err := os.MkdirAll(m.configsDir(), 0o700); err != nil {
		return CreateHubResult{}, err
	}
	if err := os.MkdirAll(m.certsDir(), 0o700); err != nil {
		return CreateHubResult{}, err
	}
	if _, err := certutil.InitCA(certutil.CAOptions{
		OutDir: m.certsDir(),
		Name:   req.NetworkName,
		Days:   3650,
	}); err != nil {
		return CreateHubResult{}, err
	}
	if _, err := certutil.Issue(certutil.IssueOptions{
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
		NodeID: req.NodeName,
		Mode:   "hub",
		Transport: config.TransportConfig{
			Protocol: req.Protocol,
			Listen:   listen,
		},
		Listen:    listen,
		CAFile:    "../certs/ca.pem",
		CertFile:  "../certs/" + req.NodeName + ".pem",
		KeyFile:   "../certs/" + req.NodeName + "-key.pem",
		VirtualIP: req.VirtualIP,
		MTU:       1280,
		Device: config.DeviceConfig{
			Type: "tun",
			Name: "meshlink0",
		},
		Setup: config.SetupConfig{
			Enabled:    true,
			Address:    req.VirtualIP + "/24",
			Routes:     []config.Route{},
			Forwarding: true,
		},
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
	return firstIntranetIPv4()
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
