package agent

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"meshlink/internal/config"
	"meshlink/internal/proto"
)

type RuntimeStatus struct {
	UpdatedAt time.Time    `json:"updated_at"`
	State     string       `json:"state"`
	Self      NodeStatus   `json:"self"`
	Peers     []PeerStatus `json:"peers"`
}

type NodeStatus struct {
	NodeID      string   `json:"node_id"`
	Mode        string   `json:"mode"`
	VirtualIP   string   `json:"virtual_ip"`
	Listen      string   `json:"listen,omitempty"`
	Connect     string   `json:"connect,omitempty"`
	Routes      []string `json:"routes,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	CommonName  string   `json:"common_name,omitempty"`
}

type PeerStatus struct {
	NodeID         string     `json:"node_id"`
	Status         string     `json:"status,omitempty"`
	VirtualIP      string     `json:"virtual_ip,omitempty"`
	Routes         []string   `json:"routes,omitempty"`
	RemoteAddr     string     `json:"remote_addr,omitempty"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	CommonName     string     `json:"common_name,omitempty"`
	ConnectedAt    time.Time  `json:"connected_at"`
	LastSeen       time.Time  `json:"last_seen"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
}

const (
	PeerStatusOnline  = "online"
	PeerStatusOffline = "offline"

	statusTouchInterval = 5 * time.Second
)

type statusStore struct {
	mu     sync.Mutex
	path   string
	status RuntimeStatus
	peers  map[string]PeerStatus
}

func newStatusStore(path string, self NodeStatus) *statusStore {
	return &statusStore{
		path: path,
		status: RuntimeStatus{
			UpdatedAt: time.Now(),
			State:     "starting",
			Self:      self,
		},
		peers: make(map[string]PeerStatus),
	}
}

func (s *statusStore) setState(state string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = state
	_ = s.writeLocked()
}

func (s *statusStore) upsertPeer(peer PeerStatus) {
	if s == nil {
		return
	}
	now := time.Now()
	if peer.ConnectedAt.IsZero() {
		peer.ConnectedAt = now
	}
	if peer.Status == "" {
		peer.Status = PeerStatusOnline
	}
	peer.LastSeen = now
	peer.DisconnectedAt = nil
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[peer.NodeID] = peer
	_ = s.writeLocked()
}

func (s *statusStore) touchPeer(nodeID string) {
	if s == nil || nodeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	peer, ok := s.peers[nodeID]
	if !ok {
		return
	}
	now := time.Now()
	if peer.Status == PeerStatusOnline && peer.DisconnectedAt == nil && now.Sub(peer.LastSeen) < statusTouchInterval {
		return
	}
	peer.Status = PeerStatusOnline
	peer.LastSeen = now
	peer.DisconnectedAt = nil
	s.peers[nodeID] = peer
	_ = s.writeLocked()
}

func (s *statusStore) removePeer(nodeID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	peer, ok := s.peers[nodeID]
	if !ok {
		return
	}
	now := time.Now()
	peer.Status = PeerStatusOffline
	peer.LastSeen = now
	peer.DisconnectedAt = &now
	s.peers[nodeID] = peer
	_ = s.writeLocked()
}

func (s *statusStore) snapshot() RuntimeStatus {
	if s == nil {
		return RuntimeStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	status.Peers = make([]PeerStatus, 0, len(s.peers))
	for _, peer := range s.peers {
		status.Peers = append(status.Peers, peer)
	}
	return status
}

func (s *statusStore) applyRoster(roster proto.Roster) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	peers := make(map[string]PeerStatus)
	for _, node := range roster.Nodes {
		if node.NodeID == "" || node.NodeID == s.status.Self.NodeID {
			continue
		}
		status := node.Status
		if status == "" {
			status = PeerStatusOnline
			if node.DisconnectedAt != nil {
				status = PeerStatusOffline
			}
		}
		peers[node.NodeID] = PeerStatus{
			NodeID:         node.NodeID,
			Status:         status,
			VirtualIP:      node.VirtualIP,
			Routes:         node.Routes,
			RemoteAddr:     node.RemoteAddr,
			Fingerprint:    node.Fingerprint,
			CommonName:     node.CommonName,
			ConnectedAt:    node.ConnectedAt,
			LastSeen:       node.LastSeen,
			DisconnectedAt: node.DisconnectedAt,
		}
	}
	s.peers = peers
	_ = s.writeLocked()
}

func (s *statusStore) writeLocked() error {
	if s.path == "" {
		return nil
	}
	s.status.UpdatedAt = time.Now()
	s.status.Peers = make([]PeerStatus, 0, len(s.peers))
	for _, peer := range s.peers {
		s.status.Peers = append(s.status.Peers, peer)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.status, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func nodeStatusFromConfig(cfg *config.Config, certFile string) NodeStatus {
	commonName, fingerprint := certInfoFromFile(certFile)
	routes := make([]string, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		routes = append(routes, route.CIDR)
	}
	return NodeStatus{
		NodeID:      cfg.NodeID,
		Mode:        cfg.Mode,
		VirtualIP:   cfg.VirtualIP,
		Listen:      cfg.Listen,
		Connect:     cfg.Connect,
		Routes:      routes,
		Fingerprint: fingerprint,
		CommonName:  commonName,
	}
}

func certInfoFromTLS(conn net.Conn) (string, string) {
	stateProvider, ok := conn.(interface {
		ConnectionState() tls.ConnectionState
	})
	if !ok {
		return "", ""
	}
	state := stateProvider.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", ""
	}
	cert := state.PeerCertificates[0]
	return cert.Subject.CommonName, fingerprintDER(cert.Raw)
}

func certInfoFromState(state tls.ConnectionState) (string, string) {
	if len(state.PeerCertificates) == 0 {
		return "", ""
	}
	cert := state.PeerCertificates[0]
	return cert.Subject.CommonName, fingerprintDER(cert.Raw)
}

func certInfoFromFile(path string) (string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", ""
	}
	return cert.Subject.CommonName, fingerprintDER(cert.Raw)
}

func fingerprintDER(der []byte) string {
	sum := sha256.Sum256(der)
	raw := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		parts = append(parts, raw[i:i+2])
	}
	return "SHA256:" + strings.Join(parts, ":")
}
