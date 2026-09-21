package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
	"meshlink/internal/tlsutil"
)

type controlSender interface {
	Send(messageType, requestID string, body any) error
	Available() bool
}

// peerSessionTimings carries the SessionManager's existing optional timing
// inputs through the production Agent owner. Zero values retain the production
// defaults selected by p2p.NewSessionManager.
type peerSessionTimings struct {
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
	dialTimeout       time.Duration
}

func (a *Agent) publishPeerRuntime(runtime *peerRuntime) {
	a.peerRuntimeMu.Lock()
	defer a.peerRuntimeMu.Unlock()
	a.peerRuntime = runtime
}

func (a *Agent) clearPeerRuntime(runtime *peerRuntime) {
	a.peerRuntimeMu.Lock()
	defer a.peerRuntimeMu.Unlock()
	if a.peerRuntime == runtime {
		a.peerRuntime = nil
	}
}

func (a *Agent) configuredPeerSessionTimings() peerSessionTimings {
	a.peerRuntimeMu.RLock()
	defer a.peerRuntimeMu.RUnlock()
	return a.peerTimings
}

// This owner is created once per agent, outside all control connection contexts.
type peerRuntime struct {
	a          *Agent
	candidates *p2p.CandidateService
	sessions   *p2p.SessionManager
	control    *ControlClient
	mu         sync.RWMutex
	routes     *p2p.RouteTable
	members    map[string]proto.Member
	revoked    map[string]bool
	closeOnce  sync.Once
	closeErr   error
	closed     atomic.Bool
	eventMu    sync.Mutex
}

func peerNetworkID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("network CA certificate is missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	if !cert.IsCA {
		return "", errors.New("network trust certificate is not a CA")
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func newPeerRuntime(a *Agent) (*peerRuntime, error) {
	timings := a.configuredPeerSessionTimings()
	networkID, err := peerNetworkID(a.cfg.CAFile)
	if err != nil {
		return nil, err
	}
	controlTLS, err := tlsutil.ClientConfig(a.cfg.CAFile, a.cfg.CertFile, a.cfg.KeyFile, a.cfg.ServerName)
	if err != nil {
		return nil, err
	}
	// A separate, consistent mutual-authentication profile serves both QUIC roles.
	quicTLS := controlTLS.Clone()
	quicTLS.ServerName = ""
	quicTLS.ClientCAs = quicTLS.RootCAs
	quicTLS.ClientAuth = tls.RequireAndVerifyClientCert
	quicTLS.NextProtos = []string{"meshlink-p2p/1"}
	service, err := p2p.NewCandidateService(p2p.CandidateServiceConfig{NodeID: a.cfg.NodeID, NetworkID: networkID, Listen: a.cfg.P2P.Listen, TLSConfig: quicTLS.Clone()})
	if err != nil {
		return nil, err
	}
	r := &peerRuntime{a: a, candidates: service, routes: &p2p.RouteTable{}, members: map[string]proto.Member{}, revoked: map[string]bool{}}
	r.control = &ControlClient{runtime: r, tlsConfig: controlTLS, results: map[string]uint64{}}
	localIP, err := netip.ParseAddr(a.cfg.VirtualIP)
	if err != nil {
		service.Close()
		return nil, err
	}
	r.sessions, err = p2p.NewSessionManager(p2p.SessionManagerConfig{
		NodeID: a.cfg.NodeID, NetworkID: networkID, MTU: a.cfg.MTU, Candidates: service, TLSConfig: quicTLS.Clone(), LocalVirtualIP: localIP, LocalRoutes: a.routes,
		PeerMember: r.member, RequestSession: r.requestSession, DeliverPacket: r.deliverPacket, SessionChanged: r.sessionChanged,
		HeartbeatInterval: timings.heartbeatInterval, HeartbeatTimeout: timings.heartbeatTimeout, DialTimeout: timings.dialTimeout,
		Logger: a.log,
	})
	if err != nil {
		service.Close()
		return nil, err
	}
	r.sessions.SetCoordinatorAvailable(false)
	a.status.setCoordinatorState(networkstate.Disconnected, service.LocalAddr().String())
	a.status.startPersistence()
	return r, nil
}

func (r *peerRuntime) Close() error {
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		r.control.disconnect()
		r.closeErr = errors.Join(r.sessions.Close(), r.candidates.Close())
		r.eventMu.Lock()
		r.eventMu.Unlock()
		r.a.status.stopPersistence()
	})
	return r.closeErr
}

func (r *peerRuntime) member(id string) (proto.Member, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.members[id]
	m.Routes = append([]string(nil), m.Routes...)
	return m, ok && !r.revoked[id] && id != r.a.cfg.NodeID
}

func normalizedMember(m proto.Member) (proto.Member, error) {
	if m.NodeID == "" || strings.TrimSpace(m.NodeID) != m.NodeID {
		return m, errors.New("invalid member node ID")
	}
	fp := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(m.Fingerprint)), "sha256:"), ":", ""))
	if b, err := hex.DecodeString(fp); err != nil || len(b) != 32 {
		return m, errors.New("invalid member fingerprint")
	}
	m.Fingerprint = fp
	if m.Status != "online" && m.Status != "offline" && m.Status != "offline_or_unknown" {
		return m, errors.New("invalid member presence")
	}
	m.Routes = append([]string(nil), m.Routes...)
	for i, raw := range m.Routes {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return m, err
		}
		m.Routes[i] = p.Masked().String()
	}
	slices.Sort(m.Routes)
	m.Routes = slices.Compact(m.Routes)
	return m, nil
}

// Snapshots describe control presence. Absence retains cached authorization;
// only an explicit revocation or changed identity invalidates a data session.
func (r *peerRuntime) applyMembers(snapshot proto.MemberSnapshot) error {
	return r.applyMembership(snapshot, nil, true)
}
func (r *peerRuntime) applyMembership(snapshot proto.MemberSnapshot, removed []string, presence bool) error {
	changed, err := r.publishMembership(snapshot, removed, presence)
	if err != nil {
		return err
	}
	for _, id := range changed {
		r.control.forgetRequest(id)
		code := "peer_identity_mismatch"
		if slices.Contains(removed, id) {
			code = "peer_revoked"
		}
		_ = r.sessions.ClosePeer(id, code)
	}
	r.connectOnlineMembers()
	return nil
}

// Presence authorizes an attempt, never a direct status. The session manager
// reports online only after the authenticated peer handshake has completed.
func (r *peerRuntime) connectOnlineMembers() {
	if r.closed.Load() || !r.control.Available() {
		return
	}
	r.mu.RLock()
	var peers []string
	for id, member := range r.members {
		if id != r.a.cfg.NodeID && !r.revoked[id] && member.Status == "online" {
			peers = append(peers, id)
		}
	}
	r.mu.RUnlock()
	slices.Sort(peers)
	for _, id := range peers {
		_ = r.sessions.EnsureSession(id)
	}
}

func (r *peerRuntime) publishMembership(snapshot proto.MemberSnapshot, removed []string, presence bool) ([]string, error) {
	if snapshot.Revision == 0 {
		return nil, errors.New("member revision is required")
	}
	// Fixed order: status -> runtime. Waiting for status never holds runtime,
	// and only pure in-memory projection runs while authorization is locked.
	s := r.a.status
	s.mu.Lock()
	defer s.mu.Unlock()
	r.mu.Lock()
	next := make(map[string]proto.Member, len(r.members)+len(snapshot.Members))
	for id, m := range r.members {
		if presence {
			m.Status = "offline_or_unknown"
		}
		next[id] = m
	}
	seen := map[string]bool{}
	var changed []string
	for _, id := range removed {
		if id == "" || id == r.a.cfg.NodeID {
			r.mu.Unlock()
			return nil, permanentControlError("identity_mismatch")
		}
		if seen[id] {
			r.mu.Unlock()
			return nil, errors.New("duplicate removed member")
		}
		seen[id] = true
		delete(next, id)
		changed = append(changed, id)
	}
	for _, raw := range snapshot.Members {
		m, err := normalizedMember(raw)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		if seen[m.NodeID] {
			r.mu.Unlock()
			return nil, errors.New("duplicate member")
		}
		seen[m.NodeID] = true
		if old, ok := r.members[m.NodeID]; ok && (old.Fingerprint != m.Fingerprint || old.VirtualIP != m.VirtualIP || !slices.Equal(old.Routes, m.Routes)) {
			if m.NodeID == r.a.cfg.NodeID {
				r.mu.Unlock()
				return nil, permanentControlError("identity_mismatch")
			}
			changed = append(changed, m.NodeID)
		}
		next[m.NodeID] = m
	}
	all := make([]proto.Member, 0, len(next))
	for _, m := range next {
		all = append(all, m)
	}
	table := &p2p.RouteTable{}
	if err := table.Replace(all); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	for _, id := range changed {
		r.revoked[id] = true
	}
	r.members, r.routes = next, table
	s.applyMembersLocked(all, r.revoked)
	r.mu.Unlock()
	_ = s.writeLocked()
	return changed, nil
}

func (r *peerRuntime) disconnectPeer(message proto.DisconnectPeer) error {
	if message.NodeID == "" {
		return errors.New("disconnect peer node ID is required")
	}
	if message.NodeID == r.a.cfg.NodeID {
		return permanentControlError("identity_mismatch")
	}
	return r.applyMembership(proto.MemberSnapshot{Revision: 1}, []string{message.NodeID}, false)
}

func (r *peerRuntime) applyDelta(delta proto.MemberDelta) error {
	return r.applyMembership(proto.MemberSnapshot{Revision: delta.Revision, Members: delta.Members}, delta.RemovedNodeIDs, false)
}

func (r *peerRuntime) routePacket(packet []byte) error {
	dst, err := proto.DestinationIP(packet)
	if err != nil {
		return err
	}
	r.mu.RLock()
	m, ok := r.routes.Lookup(dst)
	revoked := r.revoked[m.NodeID]
	r.mu.RUnlock()
	if !ok || revoked || m.NodeID == r.a.cfg.NodeID {
		return p2p.ErrSessionNotReady
	}
	return r.sessions.Send(m.NodeID, packet)
}
func (r *peerRuntime) readDevice(ctx context.Context) error {
	for {
		packet, err := r.a.dev.ReadPacket(ctx)
		if err != nil {
			return err
		}
		if err := r.routePacket(packet); err != nil && !errors.Is(err, p2p.ErrSessionNotReady) {
			r.a.log.Debug("drop unroutable TUN packet", "err", err)
		}
	}
}
func (r *peerRuntime) deliverPacket(id string, packet []byte) error {
	if r.closed.Load() {
		return p2p.ErrSessionManagerClosed
	}
	m, ok := r.member(id)
	if !ok {
		return p2p.ErrSessionNotReady
	}
	if err := p2p.ValidateInboundPacket(m, netip.MustParseAddr(r.a.cfg.VirtualIP), r.a.routes, packet); err != nil {
		return err
	}
	return r.a.dev.WritePacket(packet)
}
func (r *peerRuntime) requestSession(id string) {
	if r.closed.Load() {
		return
	}
	if _, ok := r.member(id); !ok {
		return
	}
	r.control.requestSession(id)
}
func (r *peerRuntime) sessionChanged(event p2p.SessionSnapshot) {
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	if r.closed.Load() {
		return
	}
	// Read the manager only after waiting for status: an older callback may
	// have been queued before ClosePeer or a newer membership publication.
	s := r.a.status
	s.mu.Lock()
	snapshot, ok := r.sessions.Snapshot(event.PeerNodeID)
	if !ok {
		s.mu.Unlock()
		return
	}
	r.mu.RLock()
	revoked := r.revoked[event.PeerNodeID]
	r.mu.RUnlock()
	if revoked && snapshot.State != p2p.PathStateClosed {
		s.mu.Unlock()
		return
	}
	previous := s.peers[event.PeerNodeID].Session
	transition := previous == nil || previous.State != snapshot.State || previous.SessionID != snapshot.SessionID || previous.Generation != snapshot.Generation || previous.PathType != snapshot.PathType || previous.ErrorCode != snapshot.ErrorCode
	s.applySessionLocked(snapshot)
	s.status.UpdatedAt = time.Now()
	if transition {
		_ = s.writeLocked()
	} else {
		s.telemetryDirty = true
	}
	s.mu.Unlock()
	if transition {
		r.control.reportSession(snapshot)
	}
}

func permanentControlError(code string) error { return &controlCompatibilityError{code: code} }

type controlCompatibilityError struct{ code string }

func (e *controlCompatibilityError) Error() string {
	return fmt.Sprintf("permanent control incompatibility: %s", e.code)
}
