package onboarding

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
)

// EnsureServerNode upgrades an existing coordinator to also host its own mesh
// endpoint. The caller stops its agent before changing the on-disk config.
// Enrollment quotas are intentionally not involved in this local registration.
func (m Manager) EnsureServerNode(server string) error {
	enrollmentMu.Lock()
	defer enrollmentMu.Unlock()
	hub, err := config.Load(m.activeConfigPath())
	if err != nil {
		return err
	}
	if hub.Mode != "hub" {
		return fmt.Errorf("server node requires a hub config")
	}
	endpoint := meshConnectAddress(server)
	serverName, _, err := net.SplitHostPort(endpoint)
	if err != nil || serverName == "" {
		return fmt.Errorf("server address must be host:port")
	}
	hostIP, port, err := net.SplitHostPort(hub.Listen)
	if err != nil {
		return err
	}
	if hostIP == "" || hostIP == "0.0.0.0" {
		hostIP = "127.0.0.1"
	}
	connect := net.JoinHostPort(hostIP, port)
	childName := hub.ServerNodeConfig
	if childName == "" {
		childName = "server-node.json"
	}
	childPath := resolveConfigPath(m.configsDir(), childName)
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		return err
	}
	host, err := config.Load(childPath)
	if os.IsNotExist(err) {
		nodeID := hub.NodeID + "-node"
		for suffix := 2; registryHasNodeID(registry, nodeID); suffix++ {
			nodeID = hub.NodeID + "-node-" + strconv.Itoa(suffix)
		}
		certDir := filepath.Join(m.certsDir(), "server-node")
		if files, readErr := os.ReadDir(certDir); readErr == nil && len(files) != 0 {
			return fmt.Errorf("server node config is missing; restore it to preserve its existing identity")
		}
		for _, node := range registry.Nodes {
			if node.VirtualIP == "10.77.0.1" {
				return fmt.Errorf("server node address already belongs to %q", node.NodeID)
			}
		}
		if _, err = certutil.Issue(certutil.IssueOptions{OutDir: certDir, Name: nodeID, CAPath: resolveConfigPath(m.configsDir(), hub.CAFile), CAKeyPath: filepath.Join(m.certsDir(), "ca-key.pem"), Days: 825}); err != nil {
			return err
		}
		cfg := spokeConfig(nodeID, "10.77.0.1", Invite{Server: endpoint})
		cfg.DisplayName = hub.NodeID
		cfg.CertFile = "../certs/server-node/" + nodeID + ".pem"
		cfg.KeyFile = "../certs/server-node/" + nodeID + "-key.pem"
		host = &cfg
	} else if err != nil {
		return fmt.Errorf("load existing server node: %w", err)
	}
	if host.Mode != "spoke" || host.VirtualIP != "10.77.0.1" {
		return fmt.Errorf("existing server node has an unexpected role or address")
	}
	pair, err := tls.LoadX509KeyPair(resolveConfigPath(m.configsDir(), host.CertFile), resolveConfigPath(m.configsDir(), host.KeyFile))
	if err != nil {
		return fmt.Errorf("restore existing server node certificate and key: %w", err)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	caPEM, err := os.ReadFile(resolveConfigPath(m.configsDir(), hub.CAFile))
	if err != nil || !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("server node CA is unavailable")
	}
	if certificate.Subject.CommonName != host.NodeID {
		return fmt.Errorf("server node certificate identity does not match its config")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("server node certificate is invalid: %w", err)
	}
	fp := certificateFingerprint(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	found := false
	for _, node := range registry.Nodes {
		if sameNodeID(node.NodeID, host.NodeID) {
			if found || node.CertFingerprint != fp || node.VirtualIP != host.VirtualIP || node.Disabled || node.DeletedAt != nil {
				return fmt.Errorf("existing server node registry identity is invalid or disabled")
			}
			found = true
		}
	}
	if !found {
		for _, node := range registry.Nodes {
			if node.VirtualIP == host.VirtualIP {
				return fmt.Errorf("server node address is already registered")
			}
		}
		registry.Nodes = append(registry.Nodes, RegisteredNode{NodeID: host.NodeID, DisplayName: host.DisplayName, VirtualIP: host.VirtualIP, CertFingerprint: fp, CreatedAt: m.now(), LastSeen: m.now(), Status: "offline"})
	}
	host.Connect, host.Transport.Connect = connect, connect
	host.ServerName, host.Transport.ServerName = serverName, serverName
	host.P2P.Listen = hub.Listen
	if err := host.Validate(); err != nil {
		return err
	}
	if err := config.Write(childPath, *host); err != nil {
		return err
	}
	if !found {
		if err := m.saveDeviceRegistry(registry); err != nil {
			return err
		}
	}
	hub.ServerNodeConfig, hub.ServerPublicEndpoint = childName, endpoint
	return config.Write(m.activeConfigPath(), *hub)
}

func registryHasNodeID(registry DeviceRegistry, id string) bool {
	for _, node := range registry.Nodes {
		if sameNodeID(node.NodeID, id) {
			return true
		}
	}
	return false
}
