package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"meshlink/internal/onboarding"
	"meshlink/internal/proto"
	"meshlink/internal/tlsutil"
)

type peer struct {
	id        string
	virtualIP netip.Addr
	routes    []netip.Prefix
	conn      net.Conn
	sendMu    sync.Mutex
	log       *slog.Logger
}

const (
	peerHelloTimeout       = 20 * time.Second
	peerHeartbeatInterval  = 30 * time.Second
	peerReadTimeout        = 95 * time.Second
	peerWriteTimeout       = 10 * time.Second
	spokeReconnectMinDelay = 1 * time.Second
	spokeReconnectMaxDelay = 60 * time.Second
	spokeStableResetAfter  = 2 * time.Minute
)

type router struct {
	mu    sync.RWMutex
	peers map[string]*peer
}

func newRouter() *router {
	return &router{peers: make(map[string]*peer)}
}

func (r *router) add(p *peer) *peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.peers[p.id]
	r.peers[p.id] = p
	return old
}

func (r *router) removeIf(id string, p *peer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers[id] != p {
		return false
	}
	delete(r.peers, id)
	return true
}

func (r *router) list() []*peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	peers := make([]*peer, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	return peers
}

func (r *router) find(dst netip.Addr) *peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.peers {
		if p.virtualIP == dst {
			return p
		}
		for _, route := range p.routes {
			if route.Contains(dst) {
				return p
			}
		}
	}
	return nil
}

func (a *Agent) runHub(ctx context.Context) error {
	tlsCfg, err := tlsutil.ServerConfigWithClientAuth(a.cfg.CAFile, a.cfg.CertFile, a.cfg.KeyFile, tls.VerifyClientCertIfGiven)
	if err != nil {
		return err
	}
	listener, err := tls.Listen("tcp4", a.cfg.Listen, tlsCfg)
	if err != nil {
		return err
	}
	defer listener.Close()

	a.log.Info("hub listening", "addr", a.cfg.Listen)
	rt := newRouter()

	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		errCh <- a.deviceToPeers(ctx, rt)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return err
			}
		}
		go a.handleHubConn(ctx, rt, conn)

		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		default:
		}
	}
}

func (a *Agent) handleHubConn(ctx context.Context, rt *router, conn net.Conn) {
	reader := bufio.NewReader(conn)
	if err := conn.SetReadDeadline(time.Now().Add(peerHelloTimeout)); err != nil {
		a.log.Warn("failed to set connection sniff deadline", "remote", conn.RemoteAddr(), "err", err)
		_ = conn.Close()
		return
	}
	prefix, err := reader.Peek(4)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		a.log.Warn("failed to sniff hub connection", "remote", conn.RemoteAddr(), "err", err)
		_ = conn.Close()
		return
	}
	buffered := &bufferedConn{Conn: conn, reader: reader}
	if isHTTPPreface(prefix) {
		a.serveEnrollHTTP(buffered)
		return
	}
	if !hasVerifiedClientCertificate(buffered) {
		a.log.Warn("rejecting mesh connection without a verified client certificate", "remote", conn.RemoteAddr())
		_ = conn.Close()
		return
	}
	a.handlePeer(ctx, rt, buffered)
}

func (a *Agent) serveEnrollHTTP(conn net.Conn) {
	listener := newSingleConnListener(conn)
	manager := onboarding.Manager{BaseDir: a.baseDir}
	server := &http.Server{
		Handler:           manager.EnrollHTTPHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateIdle, http.StateClosed, http.StateHijacked:
				_ = listener.Close()
			}
		},
	}
	err := server.Serve(listener)
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		a.log.Warn("enrollment HTTP connection failed", "remote", conn.RemoteAddr(), "err", err)
	}
}

func (a *Agent) handlePeer(ctx context.Context, rt *router, conn net.Conn) {
	defer conn.Close()

	helloFrame, err := readFrameWithTimeout(conn, peerHelloTimeout)
	if err != nil {
		a.log.Warn("failed to read hello", "remote", conn.RemoteAddr(), "err", err)
		return
	}
	if helloFrame.Type != proto.TypeHello {
		a.log.Warn("first frame was not hello", "remote", conn.RemoteAddr(), "type", helloFrame.Type)
		return
	}
	hello, err := proto.ParseHello(helloFrame.Payload)
	if err != nil {
		a.log.Warn("invalid hello", "remote", conn.RemoteAddr(), "err", err)
		return
	}
	addr, _ := netip.ParseAddr(hello.VirtualIP)
	routes := make([]netip.Prefix, 0, len(hello.Routes))
	for _, raw := range hello.Routes {
		prefix, _ := netip.ParsePrefix(raw)
		routes = append(routes, prefix)
	}
	commonName, fingerprint := certInfoFromTLS(conn)
	connectedAt := time.Now()

	p := &peer{
		id:        hello.NodeID,
		virtualIP: addr,
		routes:    routes,
		conn:      conn,
		log:       a.log.With("peer", hello.NodeID, "remote", conn.RemoteAddr()),
	}
	old := rt.add(p)
	if old != nil && old != p {
		p.log.Warn("replacing existing peer connection", "old_remote", old.conn.RemoteAddr())
		_ = old.conn.Close()
	}
	a.status.upsertPeer(PeerStatus{
		NodeID:      hello.NodeID,
		VirtualIP:   hello.VirtualIP,
		Routes:      hello.Routes,
		RemoteAddr:  conn.RemoteAddr().String(),
		Fingerprint: fingerprint,
		CommonName:  commonName,
		ConnectedAt: connectedAt,
	})
	a.broadcastRoster(rt)
	defer func() {
		if rt.removeIf(p.id, p) {
			a.status.removePeer(p.id)
			a.broadcastRoster(rt)
		}
	}()

	p.log.Info("peer connected", "virtual_ip", hello.VirtualIP, "routes", hello.Routes, "mtu", hello.MTU)
	ticker := time.NewTicker(peerHeartbeatInterval)
	defer ticker.Stop()

	errCh := make(chan error, 1)
	go func() {
		for {
			frame, err := readFrameWithTimeout(conn, peerReadTimeout)
			if err != nil {
				errCh <- err
				return
			}
			if err := a.handlePeerFrame(rt, p, frame); err != nil {
				errCh <- err
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if isTimeout(err) {
				p.log.Warn("peer heartbeat timed out", "timeout", peerReadTimeout)
			} else if err != nil && !errors.Is(err, io.EOF) {
				p.log.Warn("peer disconnected", "err", err)
			} else {
				p.log.Info("peer disconnected")
			}
			return
		case <-ticker.C:
			if err := p.write(proto.TypePing, nil); err != nil {
				p.log.Warn("peer ping failed", "err", err)
				return
			}
		}
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func (c *bufferedConn) ConnectionState() tls.ConnectionState {
	tlsConn, ok := c.Conn.(*tls.Conn)
	if !ok {
		return tls.ConnectionState{}
	}
	return tlsConn.ConnectionState()
}

type singleConnListener struct {
	conn      net.Conn
	accepted  bool
	closeOnce sync.Once
	closed    chan struct{}
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{conn: conn, closed: make(chan struct{})}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		_ = l.conn.Close()
	})
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

func isHTTPPreface(prefix []byte) bool {
	if len(prefix) < 4 {
		return false
	}
	switch string(prefix[:4]) {
	case "GET ", "POST", "HEAD", "PUT ", "PATC", "DELE", "OPTI":
		return true
	default:
		return false
	}
}

func hasVerifiedClientCertificate(conn net.Conn) bool {
	stateProvider, ok := conn.(interface {
		ConnectionState() tls.ConnectionState
	})
	if !ok {
		return false
	}
	state := stateProvider.ConnectionState()
	return len(state.VerifiedChains) > 0
}

func (a *Agent) handlePeerFrame(rt *router, p *peer, frame proto.Frame) error {
	a.status.touchPeer(p.id)
	switch frame.Type {
	case proto.TypePacket:
		dst, err := proto.DestinationIP(frame.Payload)
		if err != nil {
			if errors.Is(err, proto.ErrNotIPPacket) {
				p.log.Debug("dropping non-IPv4 packet", "err", err)
			} else {
				p.log.Warn("dropping invalid packet", "err", err)
			}
			return nil
		}
		if a.ownsDestination(dst) {
			return a.dev.WritePacket(frame.Payload)
		}
		target := rt.find(dst)
		if target == nil {
			p.log.Debug("no route for packet", "dst", dst)
			return nil
		}
		if err := target.write(proto.TypePacket, frame.Payload); err != nil {
			a.dropPeer(rt, target, "dropping peer after packet forward failed", err)
		}
		return nil
	case proto.TypePing:
		return p.write(proto.TypePong, nil)
	case proto.TypePong:
		return nil
	default:
		return fmt.Errorf("unsupported frame type from peer: %d", frame.Type)
	}
}

func (a *Agent) broadcastRoster(rt *router) {
	payload, err := a.roster().Marshal()
	if err != nil {
		a.log.Warn("failed to marshal roster", "err", err)
		return
	}
	for _, p := range rt.list() {
		if err := p.write(proto.TypeRoster, payload); err != nil {
			p.log.Warn("failed to send roster", "err", err)
			_ = p.conn.Close()
		}
	}
}

func (a *Agent) roster() proto.Roster {
	status := a.status.snapshot()
	selfStatus := PeerStatusOnline
	if status.State != "running" {
		selfStatus = PeerStatusOffline
	}
	nodes := []proto.RosterNode{
		{
			NodeID:      status.Self.NodeID,
			Mode:        status.Self.Mode,
			Status:      selfStatus,
			VirtualIP:   status.Self.VirtualIP,
			Routes:      status.Self.Routes,
			Fingerprint: status.Self.Fingerprint,
			CommonName:  status.Self.CommonName,
			LastSeen:    status.UpdatedAt,
		},
	}
	for _, peer := range status.Peers {
		nodes = append(nodes, proto.RosterNode{
			NodeID:         peer.NodeID,
			Status:         peer.Status,
			VirtualIP:      peer.VirtualIP,
			Routes:         peer.Routes,
			RemoteAddr:     peer.RemoteAddr,
			Fingerprint:    peer.Fingerprint,
			CommonName:     peer.CommonName,
			ConnectedAt:    peer.ConnectedAt,
			LastSeen:       peer.LastSeen,
			DisconnectedAt: peer.DisconnectedAt,
		})
	}
	return proto.Roster{
		UpdatedAt: status.UpdatedAt,
		State:     status.State,
		Nodes:     nodes,
	}
}

func (a *Agent) deviceToPeers(ctx context.Context, rt *router) error {
	for {
		packet, err := a.dev.ReadPacket(ctx)
		if err != nil {
			return err
		}
		dst, err := proto.DestinationIP(packet)
		if err != nil {
			if errors.Is(err, proto.ErrNotIPPacket) {
				a.log.Debug("dropping non-IPv4 packet from device", "err", err)
			} else {
				a.log.Warn("dropping invalid packet from device", "err", err)
			}
			continue
		}
		target := rt.find(dst)
		if target == nil {
			a.log.Debug("no route for device packet", "dst", dst)
			continue
		}
		if err := target.write(proto.TypePacket, packet); err != nil {
			a.dropPeer(rt, target, "dropping peer after device packet forward failed", err)
			continue
		}
	}
}

func (a *Agent) dropPeer(rt *router, p *peer, msg string, err error) {
	if p == nil {
		return
	}
	p.log.Warn(msg, "err", err)
	if rt.removeIf(p.id, p) {
		a.status.removePeer(p.id)
		a.broadcastRoster(rt)
	}
	_ = p.conn.Close()
}

func (p *peer) write(typ byte, payload []byte) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	if err := p.conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout)); err != nil {
		return err
	}
	err := proto.Write(p.conn, typ, payload)
	_ = p.conn.SetWriteDeadline(time.Time{})
	return err
}

func readFrameWithTimeout(conn net.Conn, timeout time.Duration) (proto.Frame, error) {
	if timeout > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return proto.Frame{}, err
		}
	}
	frame, err := proto.Read(conn)
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Time{})
	}
	return frame, err
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func mustAddr(s string) netip.Addr {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		panic(err)
	}
	if !addr.Is4() {
		panic("IPv6 address is not supported")
	}
	return addr
}

func (a *Agent) ownsDestination(dst netip.Addr) bool {
	if dst == mustAddr(a.cfg.VirtualIP) {
		return true
	}
	for _, route := range a.routes {
		if route.Contains(dst) {
			return true
		}
	}
	return false
}
