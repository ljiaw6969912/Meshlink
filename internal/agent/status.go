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
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

type RuntimeStatus struct {
	CoordinatorState   networkstate.State `json:"coordinator_state"`
	P2PListen          string             `json:"p2p_listen"`
	CoordinatorMetrics CoordinatorMetrics `json:"coordinator_metrics,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
	State              string             `json:"state"`
	NetworkState       networkstate.State `json:"network_state,omitempty"`
	Self               NodeStatus         `json:"self"`
	Peers              []PeerStatus       `json:"peers"`
}

// CoordinatorMetrics counts control-plane events only, never user bytes.
type CoordinatorMetrics struct {
	ActiveControlConnections uint64 `json:"active_control_connections"`
	ControlConnections       uint64 `json:"control_connections"`
	ControlReconnects        uint64 `json:"control_reconnects"`
	ProbeSuccesses           uint64 `json:"probe_successes"`
	ProbeFailures            uint64 `json:"probe_failures"`
	CandidateRefreshes       uint64 `json:"candidate_refreshes"`
	NegotiationsRequested    uint64 `json:"negotiations_requested"`
	NegotiationsPrepared     uint64 `json:"negotiations_prepared"`
	NegotiationsOffered      uint64 `json:"negotiations_offered"`
	NegotiationsStarted      uint64 `json:"negotiations_started"`
	NegotiationsSucceeded    uint64 `json:"negotiations_succeeded"`
	NegotiationsAborted      uint64 `json:"negotiations_aborted"`
	NegotiationsTimedOut     uint64 `json:"negotiations_timed_out"`
	TypePacketViolations     uint64 `json:"type_packet_violations"`
}

func (s *statusStore) setCoordinatorMetrics(metrics CoordinatorMetrics) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.CoordinatorMetrics == metrics {
		return
	}
	s.status.CoordinatorMetrics = metrics
	_ = s.writeLocked()
}

type NodeStatus struct {
	DisplayName string `json:"display_name,omitempty"`
	p2p.ConnectionStatus
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
	DisplayName   string               `json:"display_name,omitempty"`
	LastHeartbeat time.Time            `json:"last_heartbeat"`
	ErrorCode     string               `json:"error_code,omitempty"`
	Session       *p2p.SessionSnapshot `json:"session,omitempty"`
	p2p.ConnectionStatus
	NodeID         string     `json:"node_id"`
	Mode           string     `json:"mode,omitempty"`
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
	mu             sync.Mutex
	path           string
	status         RuntimeStatus
	peers          map[string]PeerStatus
	writeFile      func(string, []byte) error
	persistence    *statusPersistence
	telemetryDirty bool
}

// One peer-runtime writer, one coalescing wake token, and one cadence timer.
// Disk I/O never owns the status mutex or the runtime publication mutex.
type statusPersistence struct {
	wake, stop, done chan struct{}
	stopped          bool // guarded by statusStore.mu
}

func (s *statusStore) startPersistence() {
	s.mu.Lock()
	if s.persistence != nil {
		s.mu.Unlock()
		return
	}
	p := &statusPersistence{wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	s.persistence = p
	s.mu.Unlock()
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(statusTouchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				s.persistLatest(true)
				return
			case <-p.wake:
				s.persistLatest(true)
			case <-ticker.C:
				s.persistLatest(false)
			}
		}
	}()
}

func (s *statusStore) stopPersistence() {
	s.mu.Lock()
	p := s.persistence
	if p != nil && !p.stopped {
		p.stopped = true
		close(p.stop)
	}
	s.mu.Unlock()
	if p != nil {
		<-p.done
	}
}

func (s *statusStore) persistLatest(force bool) {
	s.mu.Lock()
	if !force && !s.telemetryDirty {
		s.mu.Unlock()
		return
	}
	s.telemetryDirty = false
	path, write := s.path, s.writeFile
	b, err := s.encodeLocked()
	s.mu.Unlock()
	if err == nil && path != "" {
		_ = write(path, b)
	}
}

func newStatusStore(path string, self NodeStatus) *statusStore {
	self = normalizeNodeConnectionStatus(self, "starting")
	return &statusStore{
		path:      path,
		writeFile: writeRuntimeStatusFile,
		status: RuntimeStatus{
			CoordinatorState: networkstate.Disconnected,
			NetworkState:     networkstate.Disconnected,
			UpdatedAt:        time.Now(),
			State:            "starting",
			Self:             self,
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
	s.status.Self = normalizeNodeConnectionStatus(s.status.Self, s.status.State)
	_ = s.writeLocked()
}

func (s *statusStore) setNetworkState(state networkstate.State) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.NetworkState = state
	_ = s.writeLocked()
}

func (s *statusStore) terminate() error {
	if s == nil {
		return nil
	}
	// Join the sole writer before the terminal synchronous publication; an
	// older in-flight rename can never overwrite the stopped status afterward.
	s.stopPersistence()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.status.State = "stopped"
	s.status.CoordinatorState = networkstate.Disconnected
	s.status.NetworkState = networkstate.Disconnected
	s.status.Self = normalizeNodeConnectionStatus(s.status.Self, s.status.State)
	for nodeID, peer := range s.peers {
		peer.Status = PeerStatusOffline
		peer.LastSeen = now
		peer.DisconnectedAt = &now
		s.peers[nodeID] = normalizePeerConnectionStatus(peer)
	}
	return s.writeSyncLocked()
}

func (s *statusStore) markAllPeersOffline() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for nodeID, peer := range s.peers {
		peer.Status = PeerStatusOffline
		peer.LastSeen = now
		peer.DisconnectedAt = &now
		s.peers[nodeID] = normalizePeerConnectionStatus(peer)
	}
	s.status.NetworkState = networkstate.Reconnecting
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
	peer = normalizePeerConnectionStatus(peer)
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
	peer = normalizePeerConnectionStatus(peer)
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
	peer = normalizePeerConnectionStatus(peer)
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
	status.Self = normalizeNodeConnectionStatus(status.Self, status.State)
	status.Peers = make([]PeerStatus, 0, len(s.peers))
	for _, peer := range s.peers {
		peer = normalizePeerConnectionStatus(peer)
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
		if node.NodeID == "" || node.NodeID == s.status.Self.NodeID || proto.IsInfrastructureMode(node.Mode) {
			continue
		}
		status := node.Status
		if status == "" {
			status = PeerStatusOnline
			if node.DisconnectedAt != nil {
				status = PeerStatusOffline
			}
		}
		peers[node.NodeID] = normalizePeerConnectionStatus(PeerStatus{
			NodeID:         node.NodeID,
			Mode:           node.Mode,
			Status:         status,
			VirtualIP:      node.VirtualIP,
			Routes:         node.Routes,
			RemoteAddr:     node.RemoteAddr,
			Fingerprint:    node.Fingerprint,
			CommonName:     node.CommonName,
			ConnectedAt:    node.ConnectedAt,
			LastSeen:       node.LastSeen,
			DisconnectedAt: node.DisconnectedAt,
		})
	}
	s.peers = peers
	_ = s.writeLocked()
}

func (s *statusStore) writeLocked() error {
	s.status.UpdatedAt = time.Now()
	if p := s.persistence; p != nil {
		if !p.stopped {
			select {
			case p.wake <- struct{}{}:
			default:
			}
		}
		return nil
	}
	return s.writeSyncLocked()
}

func (s *statusStore) writeSyncLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := s.encodeLocked()
	if err != nil {
		return err
	}
	return s.writeFile(s.path, b)
}

func (s *statusStore) encodeLocked() ([]byte, error) {
	s.status.UpdatedAt = time.Now()
	s.status.Self = normalizeNodeConnectionStatus(s.status.Self, s.status.State)
	s.status.Peers = make([]PeerStatus, 0, len(s.peers))
	for nodeID, peer := range s.peers {
		peer = normalizePeerConnectionStatus(peer)
		s.peers[nodeID] = peer
		s.status.Peers = append(s.status.Peers, peer)
	}
	return json.MarshalIndent(s.status, "", "  ")
}

func writeRuntimeStatusFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func normalizeNodeConnectionStatus(node NodeStatus, state string) NodeStatus {
	node.PathType = ""
	node.PathState = p2p.PathStateIdle
	if strings.EqualFold(state, "stopped") {
		node.PathState = p2p.PathStateOffline
	}
	node.QualityScore = 0
	return node
}

func normalizePeerConnectionStatus(peer PeerStatus) PeerStatus {
	online := peer.Status == "" || strings.EqualFold(peer.Status, PeerStatusOnline)
	if peer.DisconnectedAt != nil || strings.EqualFold(peer.Status, PeerStatusOffline) {
		online = false
	}
	if peer.Session != nil && peer.DisconnectedAt == nil {
		peer.PathType = peer.Session.PathType
		peer.PathState = peer.Session.State
		peer.LatencyMS = peer.Session.RTT.Milliseconds()
		peer.LastError = peer.Session.ErrorCode
		peer.ErrorCode = peer.Session.ErrorCode
		peer.LastHeartbeat = peer.Session.LastHeartbeat
		return peer
	}
	if peer.PathType != "" && online {
		peer.ConnectionStatus = p2p.NormalizeConnectionStatus(peer.ConnectionStatus, p2p.ConnectionStatusDefaults{Online: true})
	} else {
		peer.PathType = ""
		peer.PathState = p2p.PathStateIdle
		peer.QualityScore = 0
		if peer.Status == "offline_or_unknown" {
			peer.PathState = p2p.PathState("offline_or_unknown")
		} else if !online && peer.Status != "idle" {
			peer.PathState = p2p.PathStateOffline
			peer.LatencyMS = 0
			peer.RelayBytesIn = 0
			peer.RelayBytesOut = 0
		}
	}
	return peer
}

func (s *statusStore) setCoordinatorState(state networkstate.State, listen string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.CoordinatorState = state
	if listen != "" {
		s.status.P2PListen = listen
	}
	s.updateNetworkLocked()
	_ = s.writeLocked()
}
func (s *statusStore) updateNetworkLocked() {
	direct := false
	for _, p := range s.peers {
		if p.Session != nil && (p.Session.State == p2p.PathStateLANDirect || p.Session.State == p2p.PathStatePublicDirect) {
			direct = true
			break
		}
	}
	s.status.NetworkState = networkstate.PeerRuntime(s.status.CoordinatorState, direct)
}
func (s *statusStore) applyMembers(members []proto.Member, revoked map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyMembersLocked(members, revoked)
	_ = s.writeLocked()
}

// In-memory projection only: callers may atomically publish this alongside
// authorization while holding the runtime lock. Never perform I/O here.
func (s *statusStore) applyMembersLocked(members []proto.Member, revoked map[string]bool) {
	for _, m := range members {
		if m.NodeID == s.status.Self.NodeID {
			if m.DisplayName != "" {
				s.status.Self.DisplayName = m.DisplayName
			}
			continue
		}
		peer := s.peers[m.NodeID]
		peer.NodeID = m.NodeID
		peer.DisplayName = m.DisplayName
		peer.Mode = "spoke"
		peer.VirtualIP = m.VirtualIP
		peer.Routes = append([]string(nil), m.Routes...)
		peer.Fingerprint = m.Fingerprint
		peer.Status = "offline_or_unknown"
		if m.Status == "online" {
			peer.Status = "idle"
		}
		if peer.Session != nil {
			peer.Status = string(peer.Session.State)
			if peer.Session.State == p2p.PathStateLANDirect || peer.Session.State == p2p.PathStatePublicDirect {
				peer.Status = PeerStatusOnline
			}
		}
		s.peers[m.NodeID] = peer
	}
	// Explicit removals are absent from members, but must invalidate old status
	// in the same publication as authorization, before ClosePeer's callback.
	for id, revoked := range revoked {
		if peer, ok := s.peers[id]; ok && revoked && (peer.Session == nil || peer.Session.State != p2p.PathStateClosed) {
			peer.Session = nil
			peer.PathType = ""
			peer.PathState = p2p.PathStateClosed
			peer.Status = PeerStatusOffline
			s.peers[id] = peer
		}
	}
	s.updateNetworkLocked()
}
func (s *statusStore) applySession(snapshot p2p.SessionSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applySessionLocked(snapshot)
	_ = s.writeLocked()
}
func (s *statusStore) applySessionLocked(snapshot p2p.SessionSnapshot) {
	peer, ok := s.peers[snapshot.PeerNodeID]
	if !ok {
		return
	}
	peer.Session = &snapshot
	peer.PathType = snapshot.PathType
	peer.PathState = snapshot.State
	peer.ErrorCode = snapshot.ErrorCode
	peer.LastHeartbeat = snapshot.LastHeartbeat
	peer.DisconnectedAt = nil
	if snapshot.State == p2p.PathStateLANDirect || snapshot.State == p2p.PathStatePublicDirect {
		peer.Status = PeerStatusOnline
		if peer.ConnectedAt.IsZero() {
			peer.ConnectedAt = time.Now()
		}
		peer.LastSeen = snapshot.LastHeartbeat
	} else {
		peer.Status = string(snapshot.State)
	}
	s.peers[snapshot.PeerNodeID] = peer
	s.updateNetworkLocked()
}

func nodeStatusFromConfig(cfg *config.Config, certFile string) NodeStatus {
	commonName, fingerprint := certInfoFromFile(certFile)
	routes := make([]string, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		routes = append(routes, route.CIDR)
	}
	return NodeStatus{
		DisplayName: cfg.DisplayName,
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

func (s *statusStore) setPeerDisplayName(id, name string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if peer, ok := s.peers[id]; ok {
		peer.DisplayName = name
		s.peers[id] = peer
		_ = s.writeLocked()
	}
}
