package onboarding

import (
	"bytes"
	"crypto/tls"
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
	if req.InviteLink == "" {
		return JoinSpokeResult{}, fmt.Errorf("invite_link is required")
	}
	if req.Code == "" {
		return JoinSpokeResult{}, fmt.Errorf("code is required")
	}
	if req.NodeName == "" {
		if host, err := os.Hostname(); err == nil && host != "" {
			req.NodeName = host
		} else {
			req.NodeName = "spoke"
		}
	}
	invite, err := ParseInviteLink(req.InviteLink)
	if err != nil {
		return JoinSpokeResult{}, err
	}
	if err := m.validateInviteServerResolution(invite.Server); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := os.MkdirAll(m.configsDir(), 0o700); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := os.MkdirAll(m.certsDir(), 0o700); err != nil {
		return JoinSpokeResult{}, err
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{
		OutDir: m.certsDir(),
		Name:   req.NodeName,
	})
	if err != nil {
		return JoinSpokeResult{}, err
	}
	body, err := json.Marshal(EnrollHTTPRequest{
		Token:    invite.Token,
		Code:     req.Code,
		NodeName: req.NodeName,
		CSRPEM:   string(csr.CSRPEM),
	})
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
		msg := strings.TrimSpace(string(resBody))
		if msg == "" {
			msg = res.Status
		}
		return JoinSpokeResult{}, fmt.Errorf("enroll request failed: %s", msg)
	}
	var enroll EnrollHTTPResponse
	if err := json.Unmarshal(resBody, &enroll); err != nil {
		return JoinSpokeResult{}, err
	}
	if !enroll.OK {
		if enroll.Error == "" {
			enroll.Error = "enroll request failed"
		}
		return JoinSpokeResult{}, errors.New(enroll.Error)
	}
	if enroll.CAPEM == "" || enroll.CertPEM == "" {
		return JoinSpokeResult{}, fmt.Errorf("enroll response missing certificate material")
	}
	if err := os.WriteFile(filepath.Join(m.certsDir(), "ca.pem"), []byte(enroll.CAPEM), 0o600); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := os.WriteFile(filepath.Join(m.certsDir(), req.NodeName+".pem"), []byte(enroll.CertPEM), 0o600); err != nil {
		return JoinSpokeResult{}, err
	}
	cfg := enroll.Config
	if err := cfg.Validate(); err != nil {
		return JoinSpokeResult{}, err
	}
	if err := writePrettyJSON(m.activeConfigPath(), cfg); err != nil {
		return JoinSpokeResult{}, err
	}
	return JoinSpokeResult{
		ConfigPath: m.activeConfigPath(),
		VirtualIP:  cfg.VirtualIP,
		Server:     invite.Server,
		Protocol:   invite.Protocol,
	}, nil
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
