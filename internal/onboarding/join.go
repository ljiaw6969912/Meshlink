package onboarding

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/deviceidentity"
)

type JoinSpokeRequest struct {
	InviteLink string `json:"invite_link"`
	Code       string `json:"code"`
	NodeName   string `json:"node_name"`
}

type JoinSpokeResult struct {
	ConfigPath string `json:"config_path"`
	VirtualIP  string `json:"virtual_ip"`
	Server     string `json:"server"`
	Protocol   string `json:"protocol"`
}

func (m Manager) JoinSpoke(req JoinSpokeRequest) (JoinSpokeResult, error) {
	var invite InviteLink
	if req.InviteLink != "" {
		var err error
		invite, err = ParseInviteLink(req.InviteLink)
		if err != nil {
			return JoinSpokeResult{}, err
		}
	}
	if result, joined, err := m.resumeJoinedSpoke(req.NodeName, invite); joined || err != nil {
		return result, err
	}
	if req.InviteLink == "" {
		return JoinSpokeResult{}, fmt.Errorf("invite_link is required")
	}
	if req.NodeName == "" {
		if host, err := os.Hostname(); err == nil && host != "" {
			req.NodeName = host
		} else {
			req.NodeName = "spoke"
		}
	}
	if err := validateEnrollmentNodeName(req.NodeName); err != nil {
		return JoinSpokeResult{}, err
	}
	if req.Code == "" {
		return JoinSpokeResult{}, fmt.Errorf("code is required")
	}
	if err := m.validateInviteServerResolution(invite.Server); err != nil {
		return JoinSpokeResult{}, err
	}
	result, err := m.enrollSpoke(req, invite, req.NodeName)
	if !errors.Is(err, errNodeNameRegistered) {
		return result, err
	}
	// A fresh installation has its own key even if another device has the
	// same Windows hostname. Never replace that device's registered identity.
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return JoinSpokeResult{}, err
	}
	return m.enrollSpoke(req, invite, req.NodeName+"-"+hex.EncodeToString(suffix[:]))
}

var errNodeNameRegistered = errors.New("node name already registered")

func (m Manager) enrollSpoke(req JoinSpokeRequest, invite InviteLink, nodeID string, legacy ...bool) (JoinSpokeResult, error) {
	if err := os.MkdirAll(m.configsDir(), 0o700); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := os.MkdirAll(m.certsDir(), 0o700); err != nil {
		return JoinSpokeResult{}, err
	}
	pendingDir, err := os.MkdirTemp(m.certsDir(), ".join-")
	if err != nil {
		return JoinSpokeResult{}, err
	}
	defer os.RemoveAll(pendingDir)
	csr, err := certutil.CreateCSR(certutil.CSROptions{
		OutDir: pendingDir,
		Name:   nodeID,
	})
	if err != nil {
		return JoinSpokeResult{}, err
	}
	request := EnrollHTTPRequest{
		MACAddress:  m.localMAC(),
		DisplayName: req.NodeName,
		Token:       invite.Token,
		Code:        req.Code,
		NodeName:    nodeID,
		CSRPEM:      string(csr.CSRPEM),
	}
	if len(legacy) > 0 {
		request.MACAddress = ""
		request.DisplayName = ""
	}
	body, err := json.Marshal(request)
	if err != nil {
		return JoinSpokeResult{}, err
	}
	client := m.HTTPClient
	if client == nil {
		client = bootstrapHTTPClient()
	}
	enrollURL := enrollRequestURL(invite.Server)
	httpReq, err := http.NewRequest(http.MethodPost, enrollURL, bytes.NewReader(body))
	if err != nil {
		return JoinSpokeResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := client.Do(httpReq)
	if err != nil {
		return JoinSpokeResult{}, fmt.Errorf("无法连接服务器接入接口 %s：%w。请确认域名或公网地址、监听端口、路由器端口映射、防火墙和代理/隧道规则。", enrollURL, err)
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return JoinSpokeResult{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var rejected EnrollHTTPResponse
		_ = json.Unmarshal(resBody, &rejected)
		// Older servers reject the added fields before processing enrollment.
		// Retry only that exact parse error, never a potentially completed join.
		if len(legacy) == 0 && res.StatusCode == http.StatusBadRequest &&
			(rejected.Error == `json: unknown field "mac_address"` || rejected.Error == `json: unknown field "display_name"`) {
			return m.enrollSpoke(req, invite, nodeID, true)
		}
		msg := strings.TrimSpace(string(resBody))
		if msg == "" {
			msg = res.Status
		}
		return JoinSpokeResult{}, friendlyEnrollError(msg)
	}
	var enroll EnrollHTTPResponse
	if err := json.Unmarshal(resBody, &enroll); err != nil {
		return JoinSpokeResult{}, err
	}
	if !enroll.OK {
		if enroll.Error == "" {
			enroll.Error = "enroll request failed"
		}
		return JoinSpokeResult{}, friendlyEnrollError(enroll.Error)
	}
	if enroll.CAPEM == "" || enroll.CertPEM == "" {
		return JoinSpokeResult{}, fmt.Errorf("enroll response missing certificate material")
	}
	cfg := enroll.Config
	if cfg.Mode != "spoke" || validateEnrollmentNodeName(cfg.NodeID) != nil {
		return JoinSpokeResult{}, fmt.Errorf("服务器返回的客户端身份不匹配")
	}
	// The coordinator may resolve the MAC to an existing immutable node ID.
	nodeID = cfg.NodeID
	cfg.DisplayName = req.NodeName
	if err := cfg.Validate(); err != nil {
		return JoinSpokeResult{}, err
	}
	keyPEM, err := os.ReadFile(csr.KeyPath)
	if err != nil {
		return JoinSpokeResult{}, err
	}
	pair, err := tls.X509KeyPair([]byte(enroll.CertPEM), keyPEM)
	if err != nil {
		return JoinSpokeResult{}, fmt.Errorf("enrollment certificate does not match the requested key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != nodeID {
		return JoinSpokeResult{}, fmt.Errorf("服务器返回的证书身份不匹配")
	}
	if err := os.WriteFile(filepath.Join(m.certsDir(), "ca.pem"), []byte(enroll.CAPEM), 0o600); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := os.WriteFile(filepath.Join(m.certsDir(), nodeID+".pem"), []byte(enroll.CertPEM), 0o600); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := os.WriteFile(filepath.Join(m.certsDir(), nodeID+"-key.pem"), keyPEM, 0o600); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := writePrettyJSON(m.activeConfigPath(), cfg); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := m.rememberJoinedNetwork(cfg, req); err != nil {
		return JoinSpokeResult{}, fmt.Errorf("保存加入信息失败（已保留本机身份，可重新连接）：%w", err)
	}
	return JoinSpokeResult{
		ConfigPath: m.activeConfigPath(),
		VirtualIP:  cfg.VirtualIP,
		Server:     invite.Server,
		Protocol:   invite.Protocol,
	}, nil
}

// Reusing the installed identity makes joining an already joined network follow
// the same service-start path as reconnecting, without issuing another CSR.
func (m Manager) resumeJoinedSpoke(nodeName string, invite InviteLink) (JoinSpokeResult, bool, error) {
	cfg, err := config.Load(m.activeConfigPath())
	if os.IsNotExist(err) {
		return JoinSpokeResult{}, false, nil
	}
	if err != nil {
		return JoinSpokeResult{}, false, fmt.Errorf("已有网络配置无法读取，已保留原有身份：%w", err)
	}
	if cfg.Mode != "spoke" ||
		(invite.Server != "" && !strings.EqualFold(cfg.Connect, meshConnectAddress(invite.Server))) {
		return JoinSpokeResult{}, false, fmt.Errorf("此目录已有其他网络或身份的配置；切换网络或身份前请先退出原网络")
	}
	pair, err := tls.LoadX509KeyPair(resolveConfigPath(m.configsDir(), cfg.CertFile), resolveConfigPath(m.configsDir(), cfg.KeyFile))
	if err != nil {
		return JoinSpokeResult{}, false, fmt.Errorf("已有证书或私钥不可用，请修复后重新连接（未重新注册）：%w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return JoinSpokeResult{}, false, err
	}
	if leaf.Subject.CommonName != cfg.NodeID {
		return JoinSpokeResult{}, false, fmt.Errorf("已有证书身份与本机名称不一致，未重新注册")
	}
	ca, err := os.ReadFile(resolveConfigPath(m.configsDir(), cfg.CAFile))
	if err != nil {
		return JoinSpokeResult{}, false, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return JoinSpokeResult{}, false, fmt.Errorf("已有 CA 证书无效，未重新注册")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return JoinSpokeResult{}, false, fmt.Errorf("已有证书验证失败，未重新注册：%w", err)
	}
	if nodeName != "" && nodeName != cfg.DisplayName {
		if err := validateEnrollmentNodeName(nodeName); err != nil {
			return JoinSpokeResult{}, false, err
		}
		cfg.DisplayName = nodeName
		if err := writePrettyJSON(m.activeConfigPath(), cfg); err != nil {
			return JoinSpokeResult{}, false, err
		}
	}
	return JoinSpokeResult{ConfigPath: m.activeConfigPath(), VirtualIP: cfg.VirtualIP, Server: cfg.Connect, Protocol: cfg.Transport.Protocol}, true, nil
}

func (m Manager) localMAC() string {
	if m.LocalMAC != nil {
		return m.LocalMAC()
	}
	return deviceidentity.LocalMAC()
}

func friendlyEnrollError(msg string) error {
	raw := strings.TrimSpace(msg)
	normalized := strings.ToLower(raw)
	switch {
	case strings.Contains(normalized, "node name already registered"):
		return errNodeNameRegistered
	case strings.Contains(normalized, "verification code is incorrect"):
		return errors.New("接入码不正确。请重新输入服务器显示的 6 位接入码，注意不要混入空格。")
	case strings.Contains(normalized, "invite has expired"):
		return errors.New("接入链接已过期。请在服务器设备上重新生成接入链接和 6 位接入码。")
	case strings.Contains(normalized, "invite has already been used"):
		return errors.New("接入链接已被使用。一次性接入链接只能加入一台设备，请在服务器设备上重新生成。")
	case strings.Contains(normalized, "invite device limit has been reached"):
		return errors.New("设备数量已达到上限。请让服务器管理员生成新的接入链接，或改用允许更多设备的接入码。")
	case strings.Contains(normalized, "invite token was not found"):
		return errors.New("接入链接无效。请从服务器设备重新复制完整接入链接，不要只复制其中一部分。")
	case strings.Contains(normalized, "invite has been invalidated"):
		return errors.New("接入码错误次数过多，当前接入链接已失效。请在服务器设备上重新生成接入链接和接入码。")
	case raw == "":
		return errors.New("加入网络失败。服务器没有返回具体原因，请确认接入链接和接入码后重试。")
	default:
		return fmt.Errorf("加入网络失败：%s", raw)
	}
}

func bootstrapHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func (m Manager) validateInviteServerResolution(server string) error {
	host := hostFromServerAddress(server)
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isProxyFakeIPv4(ip) {
			return proxyFakeIPError(host, []net.IP{ip})
		}
		return nil
	}
	ips, err := m.resolveHost(host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	var fakeIPs []net.IP
	for _, ip := range ips {
		if isProxyFakeIPv4(ip) {
			fakeIPs = append(fakeIPs, ip)
		}
	}
	if len(fakeIPs) > 0 {
		return proxyFakeIPError(host, fakeIPs)
	}
	return nil
}

func (m Manager) resolveHost(host string) ([]net.IP, error) {
	if m.ResolveHost != nil {
		return m.ResolveHost(host)
	}
	return net.LookupIP(host)
}

func isProxyFakeIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19)
}

func proxyFakeIPError(host string, ips []net.IP) error {
	parts := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			parts = append(parts, ip4.String())
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "198.18.0.0/15")
	}
	return fmt.Errorf("检测到接入域名 %s 被代理 TUN/fake-ip 解析为 %s。Meshlink 自托管入口必须直连，请在代理规则中加入 DOMAIN,%s,DIRECT，并在 fake-ip-filter 排除该域名后刷新 DNS。", host, strings.Join(parts, ","), host)
}

func enrollRequestURL(server string) string {
	if strings.HasPrefix(server, "http://") || strings.HasPrefix(server, "https://") {
		return strings.TrimRight(server, "/") + "/enroll/request"
	}
	return "https://" + strings.TrimRight(server, "/") + "/enroll/request"
}
