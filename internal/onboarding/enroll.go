package onboarding

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
)

type EnrollRequest struct {
	Token      string `json:"token"`
	Code       string `json:"code"`
	NodeName   string `json:"node_name"`
	CSRPEM     []byte `json:"csr_pem"`
	SourceAddr string `json:"source_addr,omitempty"`
}

type EnrollResponse struct {
	CAPEM         []byte        `json:"ca_pem"`
	CertPEM       []byte        `json:"cert_pem"`
	PrivateKeyPEM []byte        `json:"private_key_pem,omitempty"`
	Config        config.Config `json:"config"`
}

type EnrollHTTPRequest struct {
	Token    string `json:"token"`
	Code     string `json:"code"`
	NodeName string `json:"node_name"`
	CSRPEM   string `json:"csr_pem"`
}

type EnrollHTTPResponse struct {
	OK      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	CAPEM   string        `json:"ca_pem,omitempty"`
	CertPEM string        `json:"cert_pem,omitempty"`
	Config  config.Config `json:"config,omitempty"`
}

type DeviceRegistry struct {
	Nodes []RegisteredNode `json:"nodes"`
}

type RegisteredNode struct {
	NodeID          string     `json:"node_id"`
	DisplayName     string     `json:"display_name,omitempty"`
	VirtualIP       string     `json:"virtual_ip"`
	SourceAddr      string     `json:"source_addr,omitempty"`
	CertFingerprint string     `json:"cert_fingerprint"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeen        time.Time  `json:"last_seen"`
	Status          string     `json:"status"`
	Disabled        bool       `json:"disabled,omitempty"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

func (m Manager) HandleEnroll(req EnrollRequest) (EnrollResponse, error) {
	if req.Token == "" || req.Code == "" {
		return EnrollResponse{}, fmt.Errorf("token and code are required")
	}
	if req.NodeName == "" {
		return EnrollResponse{}, fmt.Errorf("node_name is required")
	}
	if len(req.CSRPEM) == 0 {
		return EnrollResponse{}, fmt.Errorf("csr_pem is required")
	}
	invite, err := m.validateInvite(req)
	if err != nil {
		return EnrollResponse{}, err
	}
	caPath := filepath.Join(m.certsDir(), "ca.pem")
	caKeyPath := filepath.Join(m.certsDir(), "ca-key.pem")
	issued, err := certutil.IssueCSR(certutil.IssueCSROptions{
		CSRPEM:    req.CSRPEM,
		Name:      req.NodeName,
		CAPath:    caPath,
		CAKeyPath: caKeyPath,
		Days:      825,
	})
	if err != nil {
		return EnrollResponse{}, err
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return EnrollResponse{}, err
	}
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return EnrollResponse{}, err
	}
	virtualIP, err := nextVirtualIP(registry)
	if err != nil {
		return EnrollResponse{}, err
	}
	cfg := spokeConfig(req.NodeName, virtualIP, invite)
	if err := cfg.Validate(); err != nil {
		return EnrollResponse{}, err
	}
	now := m.now()
	registry.Nodes = append(registry.Nodes, RegisteredNode{
		NodeID:          req.NodeName,
		DisplayName:     req.NodeName,
		VirtualIP:       virtualIP,
		SourceAddr:      req.SourceAddr,
		CertFingerprint: certificateFingerprint(issued.CertPEM),
		CreatedAt:       now,
		LastSeen:        now,
		Status:          "offline",
	})
	if err := m.saveDeviceRegistry(registry); err != nil {
		return EnrollResponse{}, err
	}
	if err := m.markInviteUsed(invite.Token, req.SourceAddr); err != nil {
		return EnrollResponse{}, err
	}
	if err := m.writeAudit("enroll_succeeded", map[string]any{
		"node_id":      req.NodeName,
		"display_name": req.NodeName,
		"virtual_ip":   virtualIP,
		"source_addr":  req.SourceAddr,
		"fingerprint":  certificateFingerprint(issued.CertPEM),
		"invite_type":  inviteType(invite),
	}); err != nil {
		return EnrollResponse{}, err
	}
	return EnrollResponse{
		CAPEM:   caPEM,
		CertPEM: issued.CertPEM,
		Config:  cfg,
	}, nil
}

func spokeConfig(nodeName, virtualIP string, invite Invite) config.Config {
	connect := meshConnectAddress(invite.Server)
	host, _, err := net.SplitHostPort(connect)
	serverName := host
	if err != nil {
		serverName = connect
	}
	return config.Config{
		NodeID: nodeName,
		Mode:   "spoke",
		Transport: config.TransportConfig{
			Protocol:   invite.Protocol,
			Connect:    connect,
			ServerName: serverName,
		},
		Connect:    connect,
		ServerName: serverName,
		CAFile:     "../certs/ca.pem",
		CertFile:   "../certs/" + nodeName + ".pem",
		KeyFile:    "../certs/" + nodeName + "-key.pem",
		VirtualIP:  virtualIP,
		Routes:     []config.Route{},
		MTU:        1280,
		Device: config.DeviceConfig{
			Type: "tun",
			Name: "meshlink0",
		},
		Setup: config.SetupConfig{
			Enabled: true,
			Address: virtualIP + "/24",
			Routes:  []config.Route{},
		},
	}
}

func meshConnectAddress(server string) string {
	u, err := url.Parse(server)
	if err == nil && u.Scheme != "" && u.Host != "" {
		return u.Host
	}
	return server
}

func (m Manager) validateInvite(req EnrollRequest) (Invite, error) {
	store, err := m.loadInviteStore()
	if err != nil {
		return Invite{}, err
	}
	now := m.now()
	for _, invite := range store.Invites {
		if invite.Token != req.Token {
			continue
		}
		if invite.MaxFailures == 0 {
			invite.MaxFailures = defaultInviteMaxFailures
		}
		if invite.Failures >= invite.MaxFailures {
			return Invite{}, fmt.Errorf("invite has been invalidated")
		}
		if !invite.LongLived && (invite.UsedAt != nil || invite.Uses >= invite.MaxUses) {
			return Invite{}, fmt.Errorf("invite has already been used")
		}
		if invite.LongLived && invite.MaxUses > 0 && invite.Uses >= invite.MaxUses {
			return Invite{}, fmt.Errorf("invite device limit has been reached")
		}
		if !invite.ExpiresAt.IsZero() && now.After(invite.ExpiresAt) {
			return Invite{}, fmt.Errorf("invite has expired")
		}
		if !verifyInviteCode(invite, req.Code) {
			_ = m.recordInviteFailure(req.Token, req.SourceAddr)
			return Invite{}, fmt.Errorf("verification code is incorrect")
		}
		return invite, nil
	}
	return Invite{}, fmt.Errorf("invite token was not found")
}

func (m Manager) recordInviteFailure(token, sourceAddr string) error {
	store, err := m.loadInviteStore()
	if err != nil {
		return err
	}
	for i := range store.Invites {
		if store.Invites[i].Token == token {
			store.Invites[i].Failures++
			failures := store.Invites[i].Failures
			maxFailures := store.Invites[i].MaxFailures
			if maxFailures == 0 {
				maxFailures = defaultInviteMaxFailures
				store.Invites[i].MaxFailures = maxFailures
			}
			if err := m.saveInviteStore(store); err != nil {
				return err
			}
			if err := m.writeAudit("enroll_verification_failed", map[string]any{
				"source_addr":  sourceAddr,
				"failures":     failures,
				"max_failures": maxFailures,
			}); err != nil {
				return err
			}
			if failures >= maxFailures {
				return m.writeAudit("invite_invalidated", map[string]any{
					"source_addr":  sourceAddr,
					"failures":     failures,
					"max_failures": maxFailures,
				})
			}
			return nil
		}
	}
	return nil
}

func (m Manager) markInviteUsed(token, sourceAddr string) error {
	store, err := m.loadInviteStore()
	if err != nil {
		return err
	}
	now := m.now()
	for i := range store.Invites {
		if store.Invites[i].Token == token {
			store.Invites[i].Uses++
			store.Invites[i].UsedAt = &now
			store.Invites[i].SourceAddr = sourceAddr
			return m.saveInviteStore(store)
		}
	}
	return fmt.Errorf("invite token was not found")
}

func (m Manager) loadDeviceRegistry() (DeviceRegistry, error) {
	b, err := os.ReadFile(m.deviceRegistryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return DeviceRegistry{}, nil
		}
		return DeviceRegistry{}, err
	}
	var registry DeviceRegistry
	if err := jsonUnmarshalStrict(b, &registry); err != nil {
		return DeviceRegistry{}, err
	}
	return registry, nil
}

func (m Manager) saveDeviceRegistry(registry DeviceRegistry) error {
	return writePrettyJSON(m.deviceRegistryPath(), registry)
}

func nextVirtualIP(registry DeviceRegistry) (string, error) {
	used := map[netip.Addr]bool{netip.MustParseAddr("10.77.0.1"): true}
	for _, node := range registry.Nodes {
		addr, err := netip.ParseAddr(node.VirtualIP)
		if err == nil {
			used[addr] = true
		}
	}
	for i := 2; i <= 254; i++ {
		addr := netip.AddrFrom4([4]byte{10, 77, 0, byte(i)})
		if !used[addr] {
			return addr.String(), nil
		}
	}
	return "", fmt.Errorf("no available virtual IP in 10.77.0.0/24")
}

func certificateFingerprint(certPEM []byte) string {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	raw := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		parts = append(parts, raw[i:i+2])
	}
	return "SHA256:" + strings.Join(parts, ":")
}

func inviteType(invite Invite) string {
	if invite.LongLived {
		return "long_lived"
	}
	return "one_time"
}
