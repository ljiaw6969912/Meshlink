package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"sync"
	"time"

	"meshlink/internal/onboarding"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

const (
	negotiationTimeout       = 15 * time.Second
	prepareTimeout           = 5 * time.Second
	offerAckTimeout          = 5 * time.Second
	candidateTTL             = 60 * time.Second
	registryWatchInterval    = 2 * time.Second
	maxPeerCandidates        = 32
	maxControlRequestIDBytes = 256
	// Keep targeted revocation evidence for this process lifetime. At capacity,
	// deny new pairs instead of evicting tombstones that offline peers still need.
	maxCoordinatorRelationships = 4096
	// A revoked identity's startup report is untrusted relationship evidence.
	// Retain only fixed-size peer digests, bounded per identity and process-wide;
	// a claim becomes actionable only when its survivor passes normal admission.
	maxRevokedStartupClaimsPerNode = 1024
	maxRevokedStartupClaims        = maxCoordinatorRelationships
)

type peerPair [2]string

func unorderedPair(a, b string) peerPair {
	if a > b {
		a, b = b, a
	}
	return peerPair{a, b}
}

type coordinatorPeer struct {
	node               onboarding.RegisteredNode
	conn               net.Conn
	out                chan coordinatorWrite
	done               chan struct{}
	closeOnce          sync.Once
	candidateRevision  uint64
	candidates         []proto.Candidate
	observed           *proto.Candidate
	probe              proto.ProbeCredential
	reports            map[string]struct{}
	startupRevocations map[[sha256.Size]byte]struct{}
}

type coordinatorWrite struct {
	payload        []byte
	revokedNodeIDs []string
	terminal       bool
	written        chan error
}

func (p *coordinatorPeer) close() { p.closeOnce.Do(func() { close(p.done); _ = p.conn.Close() }) }

// Queueing under the coordinator lock preserves protocol order without allowing
// one blocked network writer to block unrelated pairs or registry revocation.
func (p *coordinatorPeer) send(typ, requestID string, body any) {
	payload, err := proto.MarshalControl(typ, requestID, body)
	if err != nil {
		p.close()
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.out <- coordinatorWrite{payload: payload}:
	default:
		p.close()
	}
}

// A retained set occupies one bounded queue slot. Its per-peer writer performs
// encoding and I/O without the coordinator lock, preserving order while a slow
// survivor cannot block or disconnect unrelated pairs.
func (p *coordinatorPeer) sendRevocations(nodeIDs []string) {
	if len(nodeIDs) == 0 {
		return
	}
	write := coordinatorWrite{revokedNodeIDs: append([]string(nil), nodeIDs...)}
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.out <- write:
	default:
		p.close()
	}
}

func (p *coordinatorPeer) sendTerminalError(code, message string) {
	payload, err := proto.MarshalControl(proto.ControlTypeError, "", proto.ControlError{Code: code, Message: message})
	if err != nil {
		p.close()
		return
	}
	written := make(chan error, 1)
	timer := time.NewTimer(peerWriteTimeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return
	case p.out <- coordinatorWrite{payload: payload, terminal: true, written: written}:
	case <-timer.C:
		p.close()
		return
	}
	select {
	case <-written:
	case <-timer.C:
		p.close()
	}
}
func (p *coordinatorPeer) writeLoop() {
	for {
		select {
		case <-p.done:
			return
		case write := <-p.out:
			if len(write.revokedNodeIDs) > 0 {
				for _, nodeID := range write.revokedNodeIDs {
					payload, err := proto.MarshalControl(proto.ControlTypeDisconnectPeer, "", proto.DisconnectPeer{NodeID: nodeID, Code: "member_revoked"})
					if err != nil || p.writeControl(payload) != nil {
						p.close()
						return
					}
				}
				continue
			}
			err := p.writeControl(write.payload)
			if write.terminal {
				closeControlWrite(p.conn)
				p.close()
				write.written <- err
				return
			}
			if err != nil {
				p.close()
				return
			}
		}
	}
}

func (p *coordinatorPeer) writeControl(payload []byte) error {
	_ = p.conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
	return proto.Write(p.conn, proto.TypeControl, payload)
}

type negotiation struct {
	pair                    peerPair
	id                      string
	generation              uint64
	requestID               string
	phase                   string
	deadline, stageDeadline time.Time
	ready, ack, result      map[string]bool
	key                     string
}

type coordinatorRelationship struct {
	revoked       bool
	revokedNodeID string
	sessionID     string // Most recent coordinator-issued SessionStart, never a report.
	generation    uint64
}

type coordinator struct {
	mu                       sync.Mutex
	a                        *Agent
	manager                  onboarding.Manager
	authority                *p2p.ProbeAuthority
	peers                    map[string]*coordinatorPeer
	known                    map[string]onboarding.RegisteredNode
	probes                   map[string]*coordinatorPeer
	negotiations             map[peerPair]*negotiation
	generations              map[peerPair]uint64
	relationships            map[peerPair]*coordinatorRelationship
	revokedStartupClaims     map[[sha256.Size]byte]map[[sha256.Size]byte]struct{}
	revokedStartupClaimCount int
	revision                 uint64
	metrics                  CoordinatorMetrics
}

func newCoordinator(a *Agent) *coordinator {
	return &coordinator{a: a, manager: onboarding.Manager{BaseDir: a.baseDir}, authority: p2p.NewProbeAuthority(nil), peers: make(map[string]*coordinatorPeer), known: make(map[string]onboarding.RegisteredNode), probes: make(map[string]*coordinatorPeer), negotiations: make(map[peerPair]*negotiation), generations: make(map[peerPair]uint64), relationships: make(map[peerPair]*coordinatorRelationship), revokedStartupClaims: make(map[[sha256.Size]byte]map[[sha256.Size]byte]struct{})}
}
func (c *coordinator) publishMetrics() {
	c.metrics.ActiveControlConnections = uint64(len(c.peers))
	c.a.status.setCoordinatorMetrics(c.metrics)
}
func (c *coordinator) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.peers {
		p.close()
	}
}
func (c *coordinator) packetViolation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics.TypePacketViolations++
	c.publishMetrics()
}

func writeControlError(conn net.Conn, code, message string) {
	payload, err := proto.MarshalControl(proto.ControlTypeError, "", proto.ControlError{Code: code, Message: message})
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout))
	_ = proto.Write(conn, proto.TypeControl, payload)
}

func (c *coordinator) serveControl(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	first, err := readCoordinatorFrame(conn, reader, peerHelloTimeout)
	if err != nil {
		return
	}
	if first.Type == proto.TypePacket {
		c.packetViolation()
		writeControlError(conn, "protocol_violation", "TypePacket is forbidden on a version 2 coordinator control connection.")
		closeControlWrite(conn)
		return
	}
	env, err := proto.ParseControl(first.Payload)
	if first.Type != proto.TypeControl || err != nil || env.Type != proto.ControlTypeClientHello {
		writeControlError(conn, "control_upgrade_required", "This coordinator requires a version 2 ClientHello; upgrade the peer client.")
		return
	}
	hello, err := proto.DecodeControlBody[proto.ClientHello](env)
	if err != nil || hello.ProtocolVersion != proto.ControlProtocolVersion {
		writeControlError(conn, "control_upgrade_required", "A version 2 ClientHello is required; upgrade the peer client.")
		return
	}
	if err = hello.Validate(); err != nil {
		writeControlError(conn, "invalid_hello", err.Error())
		return
	}
	cn, fp := certInfoFromTLS(conn)
	registeredNodes, registryErr := c.manager.LoadRegisteredNodes()
	node, err := registeredNodeFromSnapshot(registeredNodes, hello.NodeID)
	if registryErr != nil {
		err = registryErr
	}
	exactIdentity := hasVerifiedClientCertificate(conn) && err == nil && cn == hello.NodeID && node.NodeID == hello.NodeID && node.CertFingerprint == fp && node.VirtualIP == hello.VirtualIP && slices.Equal(node.Routes, hello.Routes)
	if exactIdentity && (node.Disabled || node.DeletedAt != nil) {
		// A v2 client sends CandidateUpdate and ActiveSessions before waiting
		// for ServerHello. Preserve only the latter as revocation evidence so
		// restart ordering cannot erase an affected pair before its survivor
		// reconnects. This path never admits or authorizes the revoked peer.
		c.rememberRevokedStartupSessions(conn, reader, node.NodeID)
		reason := "device disabled"
		if node.DeletedAt != nil {
			reason = "device removed"
		}
		_ = c.manager.RecordCertificateRejected(hello.NodeID, fp, conn.RemoteAddr().String(), reason)
		writeControlError(conn, "identity_mismatch", "The authenticated device is disabled or removed.")
		return
	}
	if !exactIdentity || !validRegisteredNode(node) {
		reason := "identity_mismatch"
		if err == nil && cn == node.NodeID && fp == node.CertFingerprint {
			if node.DeletedAt != nil {
				reason = "device removed"
			} else if node.Disabled {
				reason = "device disabled"
			}
		}
		_ = c.manager.RecordCertificateRejected(hello.NodeID, fp, conn.RemoteAddr().String(), reason)
		writeControlError(conn, "identity_mismatch", "The authenticated certificate, node, virtual IP and routes must match an enabled registry entry.")
		return
	}
	c.mu.Lock()
	if !c.routesAvailableLocked(node) {
		c.mu.Unlock()
		writeControlError(conn, "route_conflict", "The registered virtual IP or routes conflict with an online member or the network boundary.")
		return
	}
	p := &coordinatorPeer{node: node, conn: conn, out: make(chan coordinatorWrite, 64), done: make(chan struct{}), reports: make(map[string]struct{}), startupRevocations: make(map[[sha256.Size]byte]struct{})}
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); p.writeLoop() }()
	defer func() { p.close(); <-writerDone }()
	previous, seen := c.known[node.NodeID]
	if seen {
		c.metrics.ControlReconnects++
	}
	if seen && !sameRegisteredIdentity(previous, node) {
		// Reconcile the old identity while its tuple and completed-session
		// relationships are still retained, even if its control already ended.
		c.revokeMemberLocked(node.NodeID)
	}
	old := c.peers[node.NodeID]
	if old != nil {
		c.abortForPeerLocked(node.NodeID, "control_replaced")
		delete(c.probes, old.probe.ProbeID)
	}
	c.peers[node.NodeID] = p
	c.known[node.NodeID] = node
	c.revision++
	c.metrics.ControlConnections++
	if old != nil {
		old.close()
	}
	p.send(proto.ControlTypeServerHello, "", proto.ServerHello{ProtocolVersion: 2, NetworkCIDR: c.a.cfg.NetworkCIDR, MemberRevision: c.revision, Capabilities: []string{"quic_udp_v1"}})
	c.issueProbeLocked(p)
	c.a.status.upsertPeer(PeerStatus{NodeID: node.NodeID, Mode: "spoke", VirtualIP: node.VirtualIP, Routes: node.Routes, Fingerprint: node.CertFingerprint, CommonName: node.NodeID, ConnectedAt: time.Now()})
	c.broadcastMembersLocked()
	c.sendRetainedRevocationsLocked(p, registeredNodes)
	c.publishMetrics()
	c.mu.Unlock()
	defer c.remove(p)
	for ctx.Err() == nil {
		frame, err := readCoordinatorFrame(conn, reader, peerReadTimeout)
		if err != nil {
			return
		}
		if frame.Type == proto.TypePacket {
			c.packetViolation()
			p.sendTerminalError("protocol_violation", "TypePacket is forbidden on a version 2 coordinator control connection.")
			return
		}
		if frame.Type != proto.TypeControl {
			return
		}
		env, err := proto.ParseControl(frame.Payload)
		if err != nil {
			return
		}
		if err := c.handle(p, env); err != nil {
			return
		}
	}
}

func (c *coordinator) rememberRevokedStartupSessions(conn net.Conn, reader *bufio.Reader, revokedNodeID string) {
	for range 2 {
		frame, err := readCoordinatorFrame(conn, reader, time.Second)
		if err != nil {
			return
		}
		if frame.Type == proto.TypePacket {
			c.packetViolation()
			return
		}
		if frame.Type != proto.TypeControl {
			return
		}
		envelope, err := proto.ParseControl(frame.Payload)
		if err != nil {
			return
		}
		if envelope.Type != proto.ControlTypeActiveSessions {
			continue
		}
		report, err := proto.DecodeControlBody[proto.ActiveSessions](envelope)
		if err != nil || len(report.Sessions) > 1024 {
			return
		}
		c.mu.Lock()
		for _, session := range report.Sessions {
			if session.PeerNodeID == "" || session.PeerNodeID == revokedNodeID || session.SessionID == "" || session.Generation == 0 {
				continue
			}
			claim := sha256.Sum256([]byte(session.PeerNodeID))
			revokedIdentity := sha256.Sum256([]byte(revokedNodeID))
			claims := c.revokedStartupClaims[revokedIdentity]
			if _, exists := claims[claim]; exists {
				continue
			}
			if len(claims) >= maxRevokedStartupClaimsPerNode || c.revokedStartupClaimCount >= maxRevokedStartupClaims {
				continue
			}
			if claims == nil {
				claims = make(map[[sha256.Size]byte]struct{})
				c.revokedStartupClaims[revokedIdentity] = claims
			}
			claims[claim] = struct{}{}
			c.revokedStartupClaimCount++
			// An online target has already passed current registry, certificate,
			// route, and identity admission. Offline claims remain inert digests
			// until the target proves the same facts on a later admission.
			if survivor := c.peers[session.PeerNodeID]; survivor != nil {
				survivor.send(proto.ControlTypeDisconnectPeer, "", proto.DisconnectPeer{NodeID: revokedNodeID, Code: "member_revoked"})
			}
		}
		c.mu.Unlock()
		return
	}
}

func (c *coordinator) sendRetainedRevocationsLocked(peer *coordinatorPeer, registeredNodes []onboarding.RegisteredNode) {
	var nodeIDs []string
	sendOnce := func(revokedNodeID string) {
		if revokedNodeID == "" || revokedNodeID == peer.node.NodeID {
			return
		}
		revokedIdentity := sha256.Sum256([]byte(revokedNodeID))
		if _, sent := peer.startupRevocations[revokedIdentity]; sent {
			return
		}
		peer.startupRevocations[revokedIdentity] = struct{}{}
		nodeIDs = append(nodeIDs, revokedNodeID)
	}
	for pair, relationship := range c.relationships {
		if relationship == nil || !relationship.revoked || relationship.sessionID != "" || relationship.revokedNodeID == "" || peer.node.NodeID == relationship.revokedNodeID {
			continue
		}
		if peer.node.NodeID == pair[0] || peer.node.NodeID == pair[1] {
			sendOnce(relationship.revokedNodeID)
		}
	}
	peerClaim := sha256.Sum256([]byte(peer.node.NodeID))
	registeredIDs := make(map[[sha256.Size]byte]string, len(registeredNodes))
	for _, node := range registeredNodes {
		registeredIDs[sha256.Sum256([]byte(node.NodeID))] = node.NodeID
	}
	for revokedIdentity, claims := range c.revokedStartupClaims {
		if _, claimed := claims[peerClaim]; claimed {
			if revokedNodeID := registeredIDs[revokedIdentity]; revokedNodeID != "" {
				sendOnce(revokedNodeID)
			}
		}
	}
	peer.sendRevocations(nodeIDs)
}

// Inspect the bounded header before the generic codec allocates or reads a
// user payload. A peer cannot hold a forbidden data frame open indefinitely.
func readCoordinatorFrame(conn net.Conn, reader *bufio.Reader, timeout time.Duration) (proto.Frame, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return proto.Frame{}, err
	}
	defer conn.SetReadDeadline(time.Time{})
	header, err := reader.Peek(10)
	if err != nil {
		return proto.Frame{}, err
	}
	if bytes.Equal(header[:5], []byte{'M', 'S', 'H', '1', 1}) && header[5] == proto.TypePacket {
		return proto.Frame{Type: proto.TypePacket}, nil
	}
	return proto.Read(reader)
}

func registeredNodeFromSnapshot(nodes []onboarding.RegisteredNode, nodeID string) (onboarding.RegisteredNode, error) {
	var found *onboarding.RegisteredNode
	for index := range nodes {
		if nodeID == "" || nodes[index].NodeID != nodeID {
			continue
		}
		if found != nil {
			return onboarding.RegisteredNode{}, fmt.Errorf("%w: %q", onboarding.ErrDeviceIdentityAmbiguous, nodeID)
		}
		found = &nodes[index]
	}
	if found == nil {
		return onboarding.RegisteredNode{}, fmt.Errorf("%w: %q", onboarding.ErrDeviceNotFound, nodeID)
	}
	return *found, nil
}

func validRegisteredNode(node onboarding.RegisteredNode) bool {
	if node.Disabled || node.DeletedAt != nil || node.NodeID == "" || node.CertFingerprint == "" {
		return false
	}
	ip, err := netip.ParseAddr(node.VirtualIP)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return false
	}
	for _, raw := range node.Routes {
		route, err := netip.ParsePrefix(raw)
		if err != nil || !route.Addr().Is4() || route != route.Masked() || route.String() != raw || !privatePrefix(route) {
			return false
		}
	}
	return true
}
func privatePrefix(route netip.Prefix) bool {
	for _, raw := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		private := netip.MustParsePrefix(raw)
		if route.Bits() >= private.Bits() && private.Contains(route.Addr()) {
			return true
		}
	}
	return false
}
func registeredPrefixes(node onboarding.RegisteredNode) []netip.Prefix {
	prefixes := []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr(node.VirtualIP), 32)}
	for _, raw := range node.Routes {
		prefixes = append(prefixes, netip.MustParsePrefix(raw))
	}
	return prefixes
}
func (c *coordinator) routesAvailableLocked(node onboarding.RegisteredNode) bool {
	network, err := netip.ParsePrefix(c.a.cfg.NetworkCIDR)
	if err != nil || !network.Contains(netip.MustParseAddr(node.VirtualIP)) {
		return false
	}
	for id, p := range c.peers {
		if id == node.NodeID {
			continue
		}
		for _, a := range registeredPrefixes(node) {
			for _, b := range registeredPrefixes(p.node) {
				// Different-length nested prefixes have an unambiguous LPM
				// winner. Only equal canonical prefixes have two owners.
				if a == b {
					return false
				}
			}
		}
	}
	return true
}
func sameRegisteredIdentity(a, b onboarding.RegisteredNode) bool {
	return a.NodeID == b.NodeID && a.CertFingerprint == b.CertFingerprint && a.VirtualIP == b.VirtualIP && slices.Equal(a.Routes, b.Routes)
}
func (c *coordinator) authorized(p *coordinatorPeer) bool {
	node, err := c.manager.LookupDevice(p.node.NodeID)
	return err == nil && validRegisteredNode(node) && sameRegisteredIdentity(node, p.node)
}
func (c *coordinator) remove(p *coordinatorPeer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peers[p.node.NodeID] != p {
		return
	}
	delete(c.peers, p.node.NodeID)
	delete(c.probes, p.probe.ProbeID)
	c.abortForPeerLocked(p.node.NodeID, "control_disconnected")
	c.revision++
	c.a.status.removePeer(p.node.NodeID)
	c.broadcastMembersLocked()
	c.publishMetrics()
}
func (c *coordinator) broadcastMembersLocked() {
	members := make([]proto.Member, 0, len(c.peers))
	for _, p := range c.peers {
		members = append(members, proto.Member{NodeID: p.node.NodeID, VirtualIP: p.node.VirtualIP, Routes: p.node.Routes, Status: "online", Fingerprint: p.node.CertFingerprint})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].NodeID < members[j].NodeID })
	for _, p := range c.peers {
		p.send(proto.ControlTypeMemberSnapshot, "", proto.MemberSnapshot{Revision: c.revision, Members: members})
	}
}
func (c *coordinator) issueProbeLocked(p *coordinatorPeer) {
	credential, err := c.authority.Issue()
	if err != nil {
		p.close()
		return
	}
	delete(c.probes, p.probe.ProbeID)
	p.probe = credential
	c.probes[credential.ProbeID] = p
	p.send(proto.ControlTypeProbeCredential, "", credential)
}

func (c *coordinator) handle(p *coordinatorPeer, env proto.ControlEnvelope) error {
	// Request IDs are reflected into both the envelope and ConnectPrepare body.
	// Validate before any negotiation/state change or send to another peer.
	if len(env.RequestID) > maxControlRequestIDBytes {
		return errors.New("control request ID exceeds 256 bytes")
	}
	if env.Type == proto.ControlTypeActiveSessions {
		return c.handleActiveSessions(p, env)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.publishMetrics()
	if c.peers[p.node.NodeID] != p {
		return net.ErrClosed
	}
	switch env.Type {
	case proto.ControlTypePing:
		ping, err := proto.DecodeControlBody[proto.ControlPing](env)
		if err != nil {
			return err
		}
		p.send(proto.ControlTypePong, env.RequestID, proto.ControlPong{Nonce: ping.Nonce})
	case proto.ControlTypePong:
		_, err := proto.DecodeControlBody[proto.ControlPong](env)
		return err
	case proto.ControlTypeCandidateUpdate:
		update, err := proto.DecodeControlBody[proto.CandidateUpdate](env)
		if err != nil {
			return err
		}
		return c.updateCandidatesLocked(p, update)
	case proto.ControlTypeConnectRequest:
		request, err := proto.DecodeControlBody[proto.ConnectRequest](env)
		if err != nil {
			return err
		}
		return c.requestLocked(p, request.TargetNodeID, env.RequestID)
	case proto.ControlTypeConnectReady:
		ready, err := proto.DecodeControlBody[proto.ConnectReady](env)
		if err != nil {
			return err
		}
		s := c.sessionLocked(p, ready.SessionID, ready.Generation)
		if s == nil || s.phase != "prepare" {
			return nil
		}
		if !ready.Ready {
			c.abortLocked(s, "peer_not_ready")
			return nil
		}
		if ready.CandidateRevision != p.candidateRevision {
			c.abortLocked(s, "candidate_revision_mismatch")
			return nil
		}
		s.ready[p.node.NodeID] = true
		if len(s.ready) == 2 {
			c.offerLocked(s)
		}
	case proto.ControlTypeSessionOfferAck:
		ack, err := proto.DecodeControlBody[proto.SessionOfferAck](env)
		if err != nil {
			return err
		}
		s := c.sessionLocked(p, ack.SessionID, ack.Generation)
		if s == nil || s.phase != "ack" {
			return nil
		}
		if !ack.Ready {
			c.abortLocked(s, "offer_rejected")
			return nil
		}
		s.ack[p.node.NodeID] = true
		if len(s.ack) == 2 {
			for _, id := range s.pair {
				if !c.authorized(c.peers[id]) {
					c.abortLocked(s, "identity_mismatch")
					return nil
				}
			}
			s.phase = "started"
			s.stageDeadline = s.deadline
			s.key = ""
			relationship := c.relationships[s.pair]
			relationship.sessionID, relationship.generation = s.id, s.generation
			c.metrics.NegotiationsStarted++
			for i, id := range s.pair {
				// Retain the revocation relationship even before a successful
				// peer's first ActiveSessions report reaches the coordinator.
				c.peers[id].reports[s.pair[1-i]] = struct{}{}
				c.peers[id].send(proto.ControlTypeSessionStart, s.requestID, proto.SessionStart{SessionID: s.id, Generation: s.generation})
			}
		}
	case proto.ControlTypeSessionResult:
		result, err := proto.DecodeControlBody[proto.SessionResult](env)
		if err != nil {
			return err
		}
		s := c.sessionLocked(p, result.SessionID, result.Generation)
		if s == nil || s.phase != "started" {
			return nil
		}
		if !result.Success {
			c.abortLocked(s, "connection_failed")
			return nil
		}
		s.result[p.node.NodeID] = true
		if len(s.result) == 2 {
			delete(c.negotiations, s.pair)
			c.metrics.NegotiationsSucceeded++
		}
	default:
		return fmt.Errorf("unsupported coordinator control %q", env.Type)
	}
	return nil
}

func (c *coordinator) handleActiveSessions(p *coordinatorPeer, env proto.ControlEnvelope) error {
	report, err := proto.DecodeControlBody[proto.ActiveSessions](env)
	if err != nil {
		return err
	}
	if len(report.Sessions) > 1024 {
		return errors.New("too many active session reports")
	}
	// Read and index one consistent registry snapshot before taking the global
	// coordinator lock. A hostile maximum-size report must not serialize disk
	// I/O and JSON decoding with unrelated pairs, probes, or heartbeats.
	registeredNodes, err := c.manager.LoadRegisteredNodes()
	if err != nil {
		if errors.Is(err, onboarding.ErrDeviceRegistryUnavailable) {
			// Even an empty report must not erase retained relationships during
			// a registry outage.
			return nil
		}
		return err
	}
	registryByID := make(map[string]onboarding.RegisteredNode, len(registeredNodes))
	ambiguous := make(map[string]struct{})
	for _, node := range registeredNodes {
		if _, duplicate := registryByID[node.NodeID]; duplicate {
			delete(registryByID, node.NodeID)
			ambiguous[node.NodeID] = struct{}{}
			continue
		}
		if _, duplicate := ambiguous[node.NodeID]; !duplicate {
			registryByID[node.NodeID] = node
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.publishMetrics()
	if c.peers[p.node.NodeID] != p {
		return net.ErrClosed
	}
	nextReports := make(map[string]struct{})
	newKnown := make(map[string]onboarding.RegisteredNode)
	revoked := make(map[string]struct{})
	for _, session := range report.Sessions {
		if session.PeerNodeID == "" || session.PeerNodeID == p.node.NodeID {
			continue
		}
		// A report records only revocation relationships. It cannot create,
		// extend, acknowledge, or start a negotiation, even with a valid ID.
		node, found := registryByID[session.PeerNodeID]
		known, seen := c.known[session.PeerNodeID]
		if !found || !validRegisteredNode(node) || (seen && !sameRegisteredIdentity(node, known)) {
			revoked[session.PeerNodeID] = struct{}{}
			continue
		}
		relationship := c.relationships[unorderedPair(p.node.NodeID, node.NodeID)]
		if relationship != nil && relationship.revoked && (relationship.sessionID == "" || relationship.sessionID != session.SessionID || relationship.generation != session.Generation) {
			revoked[node.NodeID] = struct{}{}
			continue
		}
		if !seen {
			newKnown[node.NodeID] = node
		}
		nextReports[node.NodeID] = struct{}{}
	}
	newPairs := 0
	for id := range nextReports {
		if c.relationships[unorderedPair(p.node.NodeID, id)] == nil {
			newPairs++
		}
	}
	// Deliver existing revocations even when the same report also contains new
	// pairs that exceed retention capacity. The following capacity error stays
	// behind this single batch in the peer writer's FIFO.
	var revokedNodeIDs []string
	for id := range revoked {
		// The wire disconnect is node-scoped, so an old duplicate must not tear
		// down a fresh, exactly authorized session in the same report.
		if _, current := nextReports[id]; current {
			continue
		}
		if _, sentAtStartup := p.startupRevocations[sha256.Sum256([]byte(id))]; !sentAtStartup {
			revokedNodeIDs = append(revokedNodeIDs, id)
		}
	}
	p.sendRevocations(revokedNodeIDs)
	clear(p.startupRevocations)
	if len(c.relationships)+newPairs > maxCoordinatorRelationships {
		p.send(proto.ControlTypeError, "", proto.ControlError{Code: "coordinator_capacity", Message: "Revocation history is full; no new peer relationships can be retained."})
		return nil
	}
	p.reports = nextReports
	for id := range nextReports {
		pair := unorderedPair(p.node.NodeID, id)
		if c.relationships[pair] == nil {
			c.relationships[pair] = &coordinatorRelationship{}
		}
	}
	for id, node := range newKnown {
		c.known[id] = node
	}
	return nil
}

func (c *coordinator) updateCandidatesLocked(p *coordinatorPeer, update proto.CandidateUpdate) error {
	if update.Revision <= p.candidateRevision {
		return nil
	}
	if len(update.Candidates) > maxPeerCandidates {
		return errors.New("too many candidates")
	}
	now := time.Now()
	candidates := make([]proto.Candidate, 0, len(update.Candidates))
	for _, candidate := range update.Candidates {
		addr, err := netip.ParseAddr(candidate.Address)
		// Public endpoints come only from the authenticated UDP observation.
		if err != nil || !addr.Is4() || (!addr.IsPrivate() && !addr.IsLoopback()) || candidate.Port == 0 || candidate.Scope != "lan" {
			return errors.New("invalid LAN candidate")
		}
		if !candidate.ExpiresAt.After(now) {
			continue
		}
		if candidate.ExpiresAt.After(now.Add(candidateTTL)) {
			candidate.ExpiresAt = now.Add(candidateTTL)
		}
		candidates = append(candidates, candidate)
	}
	p.candidateRevision = update.Revision
	if c.a.serverNode != nil && p.node.NodeID == c.a.serverNode.cfg.NodeID && c.a.serverPublicAddress.IsValid() {
		// Only the local registered server identity gets this administrator-set
		// mapping. Remote clients can never self-assert a public candidate.
		address := c.a.serverPublicAddress
		candidates = append(candidates, proto.Candidate{Address: address.Addr().Unmap().String(), Port: address.Port(), Scope: "public", Priority: 50, ExpiresAt: now.Add(candidateTTL)})
	}
	p.candidates = candidates
	c.metrics.CandidateRefreshes++
	for pair, s := range c.negotiations {
		if s.phase == "prepare" && (pair[0] == p.node.NodeID || pair[1] == p.node.NodeID) {
			delete(s.ready, p.node.NodeID)
		}
	}
	return nil
}

func (c *coordinator) requestLocked(p *coordinatorPeer, targetID, requestID string) error {
	c.metrics.NegotiationsRequested++
	target := c.peers[targetID]
	if target == nil || target == p || !c.authorized(p) || !c.authorized(target) {
		p.send(proto.ControlTypeError, requestID, proto.ControlError{Code: "peer_unavailable", Message: "Both peers must have an enabled, matching registry identity and control connection."})
		return nil
	}
	pair := unorderedPair(p.node.NodeID, targetID)
	if c.relationships[pair] == nil && len(c.relationships) >= maxCoordinatorRelationships {
		p.send(proto.ControlTypeError, requestID, proto.ControlError{Code: "coordinator_capacity", Message: "Revocation history is full; no new peer pairs can be authorized."})
		return nil
	}
	if s := c.negotiations[pair]; s != nil {
		if !c.expiredLocked(s, time.Now()) {
			return nil
		}
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	if c.relationships[pair] == nil {
		c.relationships[pair] = &coordinatorRelationship{}
	}
	// Sample wall time for every allocation, then exceed both it and the last
	// pair generation. Restarted coordinators do not restart at generation one.
	generation := uint64(time.Now().UnixNano()) + 1
	if generation <= c.generations[pair] {
		generation = c.generations[pair] + 1
	}
	c.generations[pair] = generation
	now := time.Now()
	s := &negotiation{pair: pair, id: hex.EncodeToString(id), generation: generation, requestID: requestID, phase: "prepare", deadline: now.Add(negotiationTimeout), stageDeadline: now.Add(prepareTimeout), ready: make(map[string]bool), ack: make(map[string]bool), result: make(map[string]bool)}
	c.negotiations[pair] = s
	c.metrics.NegotiationsPrepared++
	for i, id := range pair {
		c.peers[id].send(proto.ControlTypeConnectPrepare, requestID, proto.ConnectPrepare{SessionID: s.id, Generation: generation, PeerNodeID: pair[1-i], RequestID: requestID})
	}
	return nil
}
func (c *coordinator) sessionLocked(p *coordinatorPeer, id string, generation uint64) *negotiation {
	for _, s := range c.negotiations {
		if s.id == id && s.generation == generation && (s.pair[0] == p.node.NodeID || s.pair[1] == p.node.NodeID) {
			if c.expiredLocked(s, time.Now()) {
				return nil
			}
			return s
		}
	}
	return nil
}
func liveCandidates(p *coordinatorPeer, now time.Time) []proto.Candidate {
	var candidates []proto.Candidate
	for _, candidate := range p.candidates {
		if candidate.ExpiresAt.After(now) {
			candidates = append(candidates, candidate)
		}
	}
	if p.observed != nil && p.observed.ExpiresAt.After(now) {
		candidates = append(candidates, *p.observed)
	}
	return candidates
}
func (c *coordinator) offerLocked(s *negotiation) {
	now := time.Now()
	var candidates [2][]proto.Candidate
	for i, id := range s.pair {
		p := c.peers[id]
		if p == nil || !c.authorized(p) {
			c.abortLocked(s, "identity_mismatch")
			return
		}
		candidates[i] = liveCandidates(p, now)
		if len(candidates[i]) == 0 {
			c.abortLocked(s, "candidate_unavailable")
			return
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		c.abortLocked(s, "entropy_unavailable")
		return
	}
	s.key = hex.EncodeToString(key)
	s.phase = "ack"
	s.stageDeadline = minTime(now.Add(offerAckTimeout), s.deadline)
	c.metrics.NegotiationsOffered++
	for i, id := range s.pair {
		other := c.peers[s.pair[1-i]]
		c.peers[id].send(proto.ControlTypeSessionOffer, s.requestID, proto.SessionOffer{SessionID: s.id, Generation: s.generation, PeerNodeID: other.node.NodeID, PeerFingerprint: other.node.CertFingerprint, DialerNodeID: s.pair[0], Candidates: candidates[1-i], ExpiresAt: s.deadline, PairingKey: s.key})
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (c *coordinator) expiredLocked(s *negotiation, now time.Time) bool {
	code := ""
	if !now.Before(s.deadline) {
		code = "negotiation_timeout"
	} else if !now.Before(s.stageDeadline) {
		code = s.phase + "_timeout"
	}
	if code == "" {
		return false
	}
	c.metrics.NegotiationsTimedOut++
	c.abortLocked(s, code)
	return true
}
func (c *coordinator) abortLocked(s *negotiation, code string) {
	delete(c.negotiations, s.pair)
	if relationship := c.relationships[s.pair]; relationship != nil && relationship.sessionID == s.id {
		relationship.sessionID, relationship.generation = "", 0
	}
	s.key = ""
	c.metrics.NegotiationsAborted++
	for _, id := range s.pair {
		if p := c.peers[id]; p != nil {
			p.send(proto.ControlTypeSessionAbort, s.requestID, proto.SessionAbort{SessionID: s.id, Generation: s.generation, Code: code})
		}
	}
}
func (c *coordinator) abortForPeerLocked(id, code string) {
	for pair, s := range c.negotiations {
		if pair[0] == id || pair[1] == id {
			c.abortLocked(s, code)
		}
	}
}

func (c *coordinator) runMaintenance(ctx context.Context) {
	maintenance := time.NewTicker(100 * time.Millisecond)
	defer maintenance.Stop()
	registry := time.NewTicker(registryWatchInterval)
	defer registry.Stop()
	heartbeat := time.NewTicker(peerHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-maintenance.C:
			c.mu.Lock()
			for _, s := range c.negotiations {
				c.expiredLocked(s, now)
			}
			for _, p := range c.peers {
				if !now.Before(p.probe.ExpiresAt.Add(-30 * time.Second)) {
					c.issueProbeLocked(p)
				}
			}
			c.publishMetrics()
			c.mu.Unlock()
		case <-registry.C:
			c.reconcileRegistry()
		case <-heartbeat.C:
			c.mu.Lock()
			for _, p := range c.peers {
				p.send(proto.ControlTypePing, "", proto.ControlPing{Nonce: fmt.Sprint(time.Now().UnixNano())})
			}
			c.mu.Unlock()
		}
	}
}
func (c *coordinator) reconcileRegistry() {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := false
	for id, known := range c.known {
		node, err := c.manager.LookupDevice(id)
		if errors.Is(err, onboarding.ErrDeviceRegistryUnavailable) {
			continue
		}
		if err == nil && validRegisteredNode(node) && sameRegisteredIdentity(node, known) {
			continue
		}
		if c.revokeMemberLocked(id) {
			changed = true
		}
	}
	if changed {
		c.revision++
		c.broadcastMembersLocked()
	}
	c.publishMetrics()
}

// revokeMemberLocked reconciles only the affected identity. Both admission and
// the periodic watcher must use it before replacing or forgetting a known tuple.
func (c *coordinator) revokeMemberLocked(id string) bool {
	affected := make(map[string]struct{})
	for pair, relationship := range c.relationships {
		for i, member := range pair {
			if member == id {
				relationship.revoked = true
				relationship.revokedNodeID = id
				relationship.sessionID, relationship.generation = "", 0
				affected[pair[1-i]] = struct{}{}
			}
		}
	}
	for pair := range c.negotiations {
		if pair[0] == id {
			affected[pair[1]] = struct{}{}
		}
		if pair[1] == id {
			affected[pair[0]] = struct{}{}
		}
	}
	for other, p := range c.peers {
		if _, ok := p.reports[id]; ok {
			affected[other] = struct{}{}
			delete(p.reports, id)
		}
	}
	removed := false
	if p := c.peers[id]; p != nil {
		for other := range p.reports {
			affected[other] = struct{}{}
		}
		delete(c.probes, p.probe.ProbeID)
		delete(c.peers, id)
		p.close()
		c.a.status.removePeer(id)
		removed = true
	}
	for other := range affected {
		if p := c.peers[other]; p != nil {
			p.send(proto.ControlTypeDisconnectPeer, "", proto.DisconnectPeer{NodeID: id, Code: "member_revoked"})
		}
	}
	c.abortForPeerLocked(id, "member_revoked")
	delete(c.known, id)
	return removed
}

func (c *coordinator) runProbes(ctx context.Context, conn *net.UDPConn) error {
	return c.runProbePackets(ctx, udpProbeSocket{conn})
}

func (c *coordinator) runProbePackets(ctx context.Context, conn probeSocket) error {
	// Receive any IPv4 datagram in full. A smaller buffer can make Windows
	// return WSAEMSGSIZE, which must not kill rendezvous on unauthenticated input.
	// ProbeAuthority still enforces the much smaller authenticated wire limit.
	buffer := make([]byte, 65535)
	for ctx.Err() == nil {
		n, source, err := conn.read(ctx, buffer)
		if err != nil {
			return err
		}
		response, err := c.authority.Handle(buffer[:n], source)
		c.mu.Lock()
		if err != nil {
			c.metrics.ProbeFailures++
			c.publishMetrics()
			c.mu.Unlock()
			continue
		}
		// Handle has authenticated this response and its opaque probe ID.
		var identity struct {
			ProbeID string `json:"probe_id"`
		}
		_ = json.Unmarshal(response[1:], &identity)
		p := c.probes[identity.ProbeID]
		if p == nil || c.peers[p.node.NodeID] != p || !c.authorized(p) {
			c.metrics.ProbeFailures++
			c.publishMetrics()
			c.mu.Unlock()
			continue
		}
		now := time.Now()
		observed := source.AddrPort()
		p.observed = &proto.Candidate{Address: observed.Addr().Unmap().String(), Port: observed.Port(), Scope: "public", Priority: 50, ExpiresAt: minTime(now.Add(candidateTTL), p.probe.ExpiresAt)}
		c.metrics.ProbeSuccesses++
		c.metrics.CandidateRefreshes++
		c.publishMetrics()
		c.mu.Unlock()
		_, _ = conn.write(response, source)
	}
	return ctx.Err()
}
