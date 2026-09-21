package agent

import (
	"bytes"
	"container/list"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"meshlink/internal/deviceidentity"
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

// ControlClient owns only the current control socket. Its teardown never owns
// the candidate UDP socket, TUN reader or a peer session context.
type ControlClient struct {
	runtime          *peerRuntime
	tlsConfig        *tls.Config
	mu               sync.Mutex
	conn             net.Conn
	socketMu         sync.Mutex
	socket           net.Conn
	available        bool
	results          map[string]uint64
	resultOrder      []string
	memberRevision   uint64
	refreshMu        sync.Mutex
	credential       proto.ProbeCredential
	revision         uint64
	lastProbeRefresh time.Time
	requestSequence  uint64
	requests         map[string]string
	prepares         map[string]proto.ConnectPrepare
	requestPeers     map[string]*list.Element
	requestOrder     list.List
}

const (
	maxControlRequests           = 256
	peerCandidateRefreshInterval = 30 * time.Second
)

// Correlations, not sessions, use a bounded FIFO. Eviction ignores late errors;
// SessionManager retains its queue and owns subsequent retries. Both indexes
// and the list are changed under mu, including replacement of a peer's retry.
func (c *ControlClient) removeRequestLocked(id string) {
	if peer, ok := c.requests[id]; ok {
		c.requestOrder.Remove(c.requestPeers[peer])
		delete(c.requestPeers, peer)
		delete(c.requests, id)
		delete(c.prepares, id)
	}
}

func (c *ControlClient) forgetRequest(peer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.requestPeers[peer]; entry != nil {
		c.removeRequestLocked(entry.Value.(string))
	}
}

func (c *ControlClient) resetRequestsLocked() {
	c.requests = make(map[string]string)
	c.prepares = make(map[string]proto.ConnectPrepare)
	c.requestPeers = make(map[string]*list.Element)
	c.requestOrder.Init()
}

func (c *ControlClient) Available() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.available }
func (c *ControlClient) Send(typ, id string, body any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.available || c.conn == nil {
		return errors.New("control_unavailable")
	}
	return c.writeLocked(typ, id, body)
}
func (c *ControlClient) writeLocked(typ, id string, body any) error {
	payload, err := proto.MarshalControl(typ, id, body)
	if err != nil {
		return err
	}
	if err = c.conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout)); err == nil {
		err = proto.Write(c.conn, proto.TypeControl, payload)
	}
	if err != nil {
		c.available = false
		_ = c.conn.Close()
		return err
	}
	_ = c.conn.SetWriteDeadline(time.Time{})
	return nil
}
func (c *ControlClient) disconnect() {
	// Close outside the writer mutex to interrupt a blocked frame immediately.
	c.socketMu.Lock()
	if c.socket != nil {
		_ = c.socket.Close()
		c.socket = nil
	}
	c.socketMu.Unlock()
	c.mu.Lock()
	c.available = false
	c.resetRequestsLocked()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()
	c.runtime.sessions.SetCoordinatorAvailable(false)
	c.runtime.a.status.setCoordinatorState(networkstate.Reconnecting, "")
}
func (c *ControlClient) reportSession(s p2p.SessionSnapshot) {
	direct := s.State == p2p.PathStateLANDirect || s.State == p2p.PathStatePublicDirect
	failed := s.State == p2p.PathStateFailed || ((s.State == p2p.PathStateReconnecting || s.State == p2p.PathStateWaitingCoordinator) && s.ErrorCode != "" && s.ErrorCode != "control_unavailable")
	c.mu.Lock()
	defer c.mu.Unlock()
	if direct || failed || s.State == p2p.PathStateClosed {
		if entry := c.requestPeers[s.PeerNodeID]; entry != nil {
			c.removeRequestLocked(entry.Value.(string))
		}
	}
	if s.SessionID == "" || (!direct && !failed) {
		return
	}
	_ = c.reportResultLocked(proto.SessionResult{SessionID: s.SessionID, Generation: s.Generation, Success: direct, Code: s.ErrorCode})
}

func (c *ControlClient) reportResultLocked(result proto.SessionResult) error {
	if !c.available || c.conn == nil || c.results[result.SessionID] >= result.Generation {
		return nil
	}
	if err := c.writeLocked(proto.ControlTypeSessionResult, "", result); err != nil {
		return err
	}
	if _, ok := c.results[result.SessionID]; !ok {
		if len(c.resultOrder) >= 256 {
			delete(c.results, c.resultOrder[0])
			c.resultOrder = c.resultOrder[1:]
		}
		c.resultOrder = append(c.resultOrder, result.SessionID)
	}
	c.results[result.SessionID] = result.Generation
	return nil
}

func (c *ControlClient) connect(ctx context.Context) error {
	c.runtime.a.status.setCoordinatorState(networkstate.Connecting, "")
	dialer := net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp4", c.runtime.a.cfg.Connect)
	if err != nil {
		return err
	}
	conn := tls.Client(raw, c.tlsConfig)
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, 10*time.Second)
	err = conn.HandshakeContext(handshakeCtx)
	cancelHandshake()
	if err != nil {
		raw.Close()
		return err
	}
	return c.serve(ctx, conn)
}

func (c *ControlClient) serve(parent context.Context, conn net.Conn) error {
	c.socketMu.Lock()
	c.socket = conn
	c.socketMu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	defer func() { cancel(); conn.Close(); c.disconnect(); workers.Wait() }()
	workers.Add(1)
	go func() { defer workers.Done(); <-ctx.Done(); conn.Close() }()
	snapshot, err := c.runtime.candidates.Refresh(ctx, "", proto.ProbeCredential{})
	if err != nil {
		return err
	}
	c.refreshMu.Lock()
	c.credential = proto.ProbeCredential{}
	c.memberRevision = 0
	c.revision = snapshot.Revision
	c.lastProbeRefresh = time.Time{}
	c.refreshMu.Unlock()
	a := c.runtime.a
	displayName := a.cfg.DisplayName
	if displayName == "" {
		displayName = a.cfg.NodeID
	}
	localMAC := deviceidentity.LocalMAC
	if a.localMAC != nil {
		localMAC = a.localMAC
	}
	hello := proto.ClientHello{ProtocolVersion: 2, Role: "peer", NodeID: a.cfg.NodeID, VirtualIP: a.cfg.VirtualIP, Routes: a.hello.Routes, MTU: a.cfg.MTU, Capabilities: proto.DeviceMetadataCapabilities(displayName, localMAC())}
	c.mu.Lock()
	c.conn = conn
	c.available = false
	for _, message := range []struct {
		typ  string
		body any
	}{
		{proto.ControlTypeClientHello, hello},
		{proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: snapshot.Revision, Candidates: c.lanCandidates(snapshot.Candidates)}},
		{proto.ControlTypeActiveSessions, c.runtime.sessions.ActiveSessions()},
	} {
		if err = c.writeLocked(message.typ, "", message.body); err != nil {
			break
		}
	}
	c.resetRequestsLocked()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	first, err := readPeerControlFrame(conn, 10*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Transport failures before admission are retryable. The frame reader
		// already marks explicit malformed/protocol input as permanent.
		return err
	}
	if first.Type != proto.TypeControl {
		return permanentControlError("control_upgrade_required")
	}
	env, err := proto.ParseControl(first.Payload)
	if err != nil {
		return permanentControlError("invalid_server_hello")
	}
	if env.Type == proto.ControlTypeError {
		body, e := proto.DecodeControlBody[proto.ControlError](env)
		if e == nil {
			return permanentControlError(body.Code)
		}
	}
	server, err := proto.DecodeControlBody[proto.ServerHello](env)
	localIP, _ := netip.ParseAddr(a.cfg.VirtualIP)
	if err != nil || server.ProtocolVersion != 2 || !slices.Contains(server.Capabilities, "quic_udp_v1") || !validPeerNetwork(server.NetworkCIDR, localIP) {
		return permanentControlError("invalid_server_hello")
	}
	c.mu.Lock()
	c.available = true
	c.mu.Unlock()
	a.status.setCoordinatorState(networkstate.Connected, "")
	// The server's v2 admission must succeed before waiting pairs request again.
	c.runtime.sessions.SetCoordinatorAvailable(true)
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(peerHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if c.Send(proto.ControlTypePing, "", proto.ControlPing{Nonce: fmt.Sprint(time.Now().UnixNano())}) != nil {
					return
				}
			}
		}
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(peerCandidateRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if c.refresh(ctx, false) == nil {
					c.runtime.connectOnlineMembers()
				}
			}
		}
	}()
	for {
		frame, err := readPeerControlFrame(conn, peerReadTimeout)
		if err != nil {
			return err
		}
		if frame.Type != proto.TypeControl {
			return permanentControlError("control_upgrade_required")
		}
		env, err := proto.ParseControl(frame.Payload)
		if err != nil {
			return permanentControlError("invalid_control_message")
		}
		if err := c.handle(ctx, env); err != nil {
			var permanent *controlCompatibilityError
			var networkError net.Error
			if errors.As(err, &permanent) || errors.As(err, &networkError) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, context.Canceled) {
				return err
			}
			return permanentControlError("invalid_control_message")
		}
	}
}

func validPeerNetwork(raw string, local netip.Addr) bool {
	network, err := netip.ParsePrefix(raw)
	if err != nil || !network.Addr().Is4() || network != network.Masked() || !network.Contains(local) {
		return false
	}
	for _, allowed := range []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")} {
		if network.Bits() >= allowed.Bits() && allowed.Contains(network.Addr()) {
			return true
		}
	}
	return false
}

// Reject a forbidden frame from its header; never read an inner packet body.
func readPeerControlFrame(conn net.Conn, timeout time.Duration) (proto.Frame, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return proto.Frame{}, err
	}
	defer conn.SetReadDeadline(time.Time{})
	var header [10]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return proto.Frame{}, err
	}
	if !bytes.Equal(header[:5], []byte{'M', 'S', 'H', '1', 1}) {
		return proto.Frame{}, permanentControlError("invalid_control_frame")
	}
	if header[5] != proto.TypeControl {
		return proto.Frame{Type: header[5]}, nil
	}
	frame, err := proto.Read(io.MultiReader(bytes.NewReader(header[:]), conn))
	if err != nil {
		var networkErr net.Error
		if !errors.As(err, &networkErr) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
			return proto.Frame{}, permanentControlError("invalid_control_frame")
		}
	}
	return frame, err
}

// Serializing refresh through its write prevents candidate revisions from
// overtaking one another, including refresh vs. ConnectReady.
func (c *ControlClient) refresh(ctx context.Context, lanOnly bool) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.refreshLocked(ctx, lanOnly)
}
func (c *ControlClient) refreshLocked(ctx context.Context, lanOnly bool) error {
	address := ""
	if !lanOnly && !c.runtime.a.localServerNode && c.credential.ExpiresAt.After(time.Now()) {
		address = c.runtime.a.cfg.Connect
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshot, err := c.runtime.candidates.Refresh(probeCtx, address, c.credential)
	if address != "" {
		// Failed public probes still yield a fresh LAN fallback. Share this
		// attempt across a prepare burst instead of blocking every peer pair
		// on the same unreachable coordinator UDP endpoint.
		c.lastProbeRefresh = time.Now()
	}
	if err != nil && ctx.Err() == nil {
		snapshot, err = c.runtime.candidates.Refresh(ctx, "", proto.ProbeCredential{})
	}
	if err != nil {
		return err
	}
	if err = c.Send(proto.ControlTypeCandidateUpdate, "", proto.CandidateUpdate{Revision: snapshot.Revision, Candidates: c.lanCandidates(snapshot.Candidates)}); err != nil {
		return err
	}
	c.revision = snapshot.Revision
	return nil
}

func (c *ControlClient) handle(ctx context.Context, env proto.ControlEnvelope) error {
	switch env.Type {
	case proto.ControlTypeProbeCredential:
		body, err := proto.DecodeControlBody[proto.ProbeCredential](env)
		if err != nil {
			return err
		}
		c.refreshMu.Lock()
		defer c.refreshMu.Unlock()
		c.credential = body
		return c.refreshLocked(ctx, false)
	case proto.ControlTypeMemberSnapshot:
		body, err := proto.DecodeControlBody[proto.MemberSnapshot](env)
		if err != nil {
			return err
		}
		if body.Revision == 0 {
			return errors.New("invalid member revision")
		}
		if body.Revision <= c.memberRevision {
			return nil
		}
		if err := c.runtime.applyMembers(body); err != nil {
			return err
		}
		c.memberRevision = body.Revision
		return nil
	case proto.ControlTypeMemberDelta:
		body, err := proto.DecodeControlBody[proto.MemberDelta](env)
		if err != nil {
			return err
		}
		if body.Revision == 0 {
			return errors.New("invalid member revision")
		}
		if body.Revision <= c.memberRevision {
			return nil
		}
		if err := c.runtime.applyDelta(body); err != nil {
			return err
		}
		c.memberRevision = body.Revision
		return nil
	case proto.ControlTypeConnectPrepare:
		body, err := proto.DecodeControlBody[proto.ConnectPrepare](env)
		if err != nil {
			return err
		}
		_, authorized := c.runtime.member(body.PeerNodeID)
		c.mu.Lock()
		// Request IDs are local to each client. A reflected remote initiator's
		// ID proves nothing unless its peer matches our own pending request.
		if peer, ok := c.requests[env.RequestID]; ok && peer == body.PeerNodeID {
			c.prepares[env.RequestID] = body
		}
		c.mu.Unlock()
		c.refreshMu.Lock()
		defer c.refreshMu.Unlock()
		if authorized {
			// Always enumerate and publish current LAN candidates. Only the
			// potentially blocking public probe is reused; credential updates
			// and the periodic worker independently renew that observation.
			lanOnly := time.Since(c.lastProbeRefresh) < peerCandidateRefreshInterval
			err = c.refreshLocked(ctx, lanOnly)
		}
		return c.Send(proto.ControlTypeConnectReady, env.RequestID, proto.ConnectReady{SessionID: body.SessionID, Generation: body.Generation, CandidateRevision: c.revision, Ready: authorized && err == nil})
	case proto.ControlTypeSessionOffer:
		body, err := proto.DecodeControlBody[proto.SessionOffer](env)
		if err != nil {
			return err
		}
		err = c.runtime.sessions.InstallOffer(body)
		if err == nil {
			c.forgetRequest(body.PeerNodeID)
		}
		return c.Send(proto.ControlTypeSessionOfferAck, env.RequestID, proto.SessionOfferAck{SessionID: body.SessionID, Generation: body.Generation, Ready: err == nil})
	case proto.ControlTypeSessionStart:
		body, err := proto.DecodeControlBody[proto.SessionStart](env)
		if err != nil {
			return err
		}
		if err := c.runtime.sessions.StartOffer(body); err != nil {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.reportResultLocked(proto.SessionResult{SessionID: body.SessionID, Generation: body.Generation, Code: "session_authorization_failed"})
		}
		return nil
	case proto.ControlTypeSessionAbort:
		body, err := proto.DecodeControlBody[proto.SessionAbort](env)
		if err != nil {
			return err
		}
		body.Code = publicPeerError(body.Code)
		if err := c.runtime.sessions.AbortOffer(body); err != nil {
			// Without an installed offer, only a locally originated prepare's
			// complete tuple can authorize concluding a pending request.
			c.mu.Lock()
			peer := ""
			if prepare, ok := c.prepares[env.RequestID]; ok && prepare.SessionID == body.SessionID && prepare.Generation == body.Generation && c.requests[env.RequestID] == prepare.PeerNodeID {
				peer = prepare.PeerNodeID
				c.removeRequestLocked(env.RequestID)
			}
			c.mu.Unlock()
			if peer != "" {
				_ = c.runtime.sessions.FailRequest(peer, body.Code)
			}
		}
		return nil
	case proto.ControlTypeDisconnectPeer:
		body, err := proto.DecodeControlBody[proto.DisconnectPeer](env)
		if err != nil {
			return err
		}
		return c.runtime.disconnectPeer(body)
	case proto.ControlTypePing:
		body, err := proto.DecodeControlBody[proto.ControlPing](env)
		if err != nil {
			return err
		}
		return c.Send(proto.ControlTypePong, env.RequestID, proto.ControlPong{Nonce: body.Nonce})
	case proto.ControlTypePong:
		_, err := proto.DecodeControlBody[proto.ControlPong](env)
		return err
	case proto.ControlTypeError:
		body, err := proto.DecodeControlBody[proto.ControlError](env)
		if err != nil {
			return err
		}
		if body.Code == "control_upgrade_required" || body.Code == "identity_mismatch" || body.Code == "invalid_hello" {
			return permanentControlError(body.Code)
		}
		c.mu.Lock()
		var peers []string
		if id, ok := c.requests[env.RequestID]; ok {
			peers = append(peers, id)
			c.removeRequestLocked(env.RequestID)
		}
		c.mu.Unlock()
		for _, peer := range peers {
			_ = c.runtime.sessions.FailRequest(peer, publicPeerError(body.Code))
		}
		return nil
	default:
		return permanentControlError("unexpected_control_message")
	}
}

func publicPeerError(code string) string {
	// Design section 15 is the public vocabulary. Coordinator phase/reason
	// details must not leak into status (where onboarding would discard them).
	switch code {
	case "peer_unavailable", "control_replaced", "control_disconnected":
		return "peer_offline"
	case "member_revoked":
		return "peer_revoked"
	case "peer_not_ready", "candidate_revision_mismatch", "prepare_timeout":
		return "candidate_unavailable"
	case "identity_mismatch":
		return "peer_identity_mismatch"
	case "offer_rejected", "ack_timeout", "entropy_unavailable", "coordinator_capacity":
		// Capacity currently means authorization/revocation history is full;
		// the authenticated control link itself is still available.
		return "session_authorization_failed"
	case "control_upgrade_required", "control_unavailable", "peer_offline", "peer_revoked", "route_conflict", "candidate_unavailable", "udp_probe_failed", "hole_punch_timeout", "quic_handshake_failed", "peer_identity_mismatch", "session_authorization_failed", "direct_heartbeat_timeout", "direct_unreachable_no_relay":
		return code
	default:
		// Includes connection_failed, started_timeout and negotiation_timeout.
		return "direct_unreachable_no_relay"
	}
}

// Public candidates are owned by the coordinator's authenticated UDP observer.
func (c *ControlClient) lanCandidates(candidates []proto.Candidate) []proto.Candidate {
	lan := make([]proto.Candidate, 0, len(candidates))
	bound := c.runtime.candidates.LocalAddr().AddrPort().Addr().Unmap()
	virtualIP, _ := netip.ParseAddr(c.runtime.a.cfg.VirtualIP)
	for _, candidate := range candidates {
		ip, err := netip.ParseAddr(candidate.Address)
		// The overlay needs this direct session to exist first; advertising
		// its own TUN address would route the handshake back into Meshlink.
		if err == nil && ip.Is4() && ip != virtualIP && (bound.IsUnspecified() || bound == ip) && (ip.IsPrivate() || ip.IsLoopback()) && candidate.Scope == "lan" {
			lan = append(lan, candidate)
		}
	}
	return lan
}

func (c *ControlClient) requestSession(peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.available || c.conn == nil {
		return
	}
	if c.requests == nil {
		c.resetRequestsLocked()
	}
	if entry := c.requestPeers[peerID]; entry != nil {
		c.removeRequestLocked(entry.Value.(string))
	}
	if len(c.requests) == maxControlRequests {
		c.removeRequestLocked(c.requestOrder.Front().Value.(string))
	}
	c.requestSequence++
	id := "request-" + strconv.FormatUint(c.requestSequence, 36)
	c.requests[id] = peerID
	c.requestPeers[peerID] = c.requestOrder.PushBack(id)
	if c.writeLocked(proto.ControlTypeConnectRequest, id, proto.ConnectRequest{TargetNodeID: peerID}) != nil {
		c.removeRequestLocked(id)
	}
}
