package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"meshlink/internal/networkstate"
	"meshlink/internal/onboarding"
	"meshlink/internal/proto"
	"meshlink/internal/tlsutil"
	meshupdate "meshlink/internal/update"
)

const (
	peerHelloTimeout       = 20 * time.Second
	peerHeartbeatInterval  = 30 * time.Second
	peerReadTimeout        = 95 * time.Second
	peerWriteTimeout       = 10 * time.Second
	spokeReconnectMinDelay = time.Second
	spokeReconnectMaxDelay = 60 * time.Second
	spokeStableResetAfter  = 2 * time.Minute
)

// The coordinator owns only control. An optional, independently authenticated
// local node owns host packets and shares the coordinator's UDP socket.
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
	if a.serverNode != nil {
		public, err := net.ResolveUDPAddr("udp4", a.cfg.ServerPublicEndpoint)
		if err != nil {
			return fmt.Errorf("resolve server public endpoint: %w", err)
		}
		if public.Port <= 0 || public.IP.IsUnspecified() {
			return fmt.Errorf("server public endpoint is not usable")
		}
		if netip.MustParsePrefix("198.18.0.0/15").Contains(public.AddrPort().Addr().Unmap()) {
			return fmt.Errorf("服务器地址 %s 被解析为代理 fake-IP %s；请让该域名使用真实 DNS 解析，或填写真实公网 IPv4 地址", a.cfg.ServerPublicEndpoint, public.IP)
		}
		a.serverPublicAddress = public.AddrPort()
		a.serverNode.cfg.P2P.Listen = listener.Addr().String()
		host, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			return err
		}
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		a.serverNode.cfg.Connect = net.JoinHostPort(host, port)
		a.serverNode.cfg.Transport.Connect = a.serverNode.cfg.Connect
		runtime, err := newPeerRuntime(a.serverNode)
		if err != nil {
			return fmt.Errorf("start server mesh node: %w", err)
		}
		defer runtime.Close()
		return a.runHubSockets(ctx, listener, sharedProbeSocket{runtime.candidates}, runtime)
	}
	udpAddr, err := net.ResolveUDPAddr("udp4", listener.Addr().String())
	if err != nil {
		return err
	}
	udp, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return err
	}
	defer udp.Close()
	return a.runHubListeners(ctx, listener, udp)
}

func (a *Agent) runHubListeners(ctx context.Context, listener net.Listener, udp *net.UDPConn) error {
	return a.runHubSockets(ctx, listener, udpProbeSocket{udp}, nil)
}

func (a *Agent) runHubSockets(ctx context.Context, listener net.Listener, udp probeSocket, hostRuntime *peerRuntime) error {
	ctx, cancel := context.WithCancel(ctx)
	c := newCoordinator(a)
	var workers sync.WaitGroup
	defer func() { cancel(); _ = listener.Close(); _ = udp.Close(); c.close(); workers.Wait() }()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close(); _ = udp.Close() })
	defer stop()
	workers.Go(func() { c.runMaintenance(ctx) })
	probeFailure := make(chan error, 2)
	workers.Go(func() {
		if err := c.runProbePackets(ctx, udp); err != nil && ctx.Err() == nil {
			probeFailure <- fmt.Errorf("probe listener: %w", err)
			_ = listener.Close() // Wake Accept so the owner can return the failure.
		}
	})
	if hostRuntime != nil {
		workers.Go(func() {
			err := a.serverNode.runSpokeRuntime(ctx, hostRuntime)
			if err != nil && ctx.Err() == nil {
				probeFailure <- fmt.Errorf("server mesh node: %w", err)
				_ = listener.Close()
			}
		})
	}
	a.status.setState("running")
	a.status.setNetworkState(networkstate.Connected)
	a.log.Info("coordinator listening", "control", listener.Addr(), "probe", udp.LocalAddr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case probeErr := <-probeFailure:
				return probeErr
			default:
			}
			return err
		}
		workers.Go(func() {
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			defer conn.Close()
			a.handleHubConn(ctx, c, conn)
		})
	}
}

func (a *Agent) handleHubConn(ctx context.Context, c *coordinator, conn net.Conn) {
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(peerHelloTimeout))
	prefix, err := reader.Peek(4)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return
	}
	buffered := &bufferedConn{Conn: conn, reader: reader}
	if isHTTPPreface(prefix) {
		a.serveEnrollHTTP(buffered)
		return
	}
	c.serveControl(ctx, buffered)
}

func (a *Agent) serveEnrollHTTP(conn net.Conn) {
	listener := newSingleConnListener(conn)
	manager := onboarding.Manager{BaseDir: a.baseDir}
	mux := http.NewServeMux()
	mux.Handle("/", manager.EnrollHTTPHandler())
	updates, err := meshupdate.NewServerHandler(meshupdate.ResolveReleaseDirectory(a.baseDir))
	if err != nil {
		_ = listener.Close()
		return
	}
	mux.Handle("/updates/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// net/http sees a buffered connection, so use the already verified
		// TLS state on that connection rather than r.TLS (which is nil).
		if !hasVerifiedClientCertificate(conn) {
			http.Error(w, "network certificate required", http.StatusUnauthorized)
			return
		}
		http.StripPrefix("/updates", updates).ServeHTTP(w, r)
	}))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateIdle, http.StateClosed, http.StateHijacked:
				_ = listener.Close()
			}
		},
	}
	err = server.Serve(listener)
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		a.log.Warn("enrollment HTTP connection failed", "err", err)
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

// closeControlWrite sends TLS close_notify and then half-closes the underlying
// TCP write side. This lets a terminal control error reach the peer even when
// the rejected frame body remains intentionally unread.
func closeControlWrite(conn net.Conn) {
	if buffered, ok := conn.(*bufferedConn); ok {
		conn = buffered.Conn
	}
	if secure, ok := conn.(*tls.Conn); ok {
		_ = secure.CloseWrite()
		conn = secure.NetConn()
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
func (c *bufferedConn) ConnectionState() tls.ConnectionState {
	if conn, ok := c.Conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
		return conn.ConnectionState()
	}
	return tls.ConnectionState{}
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
	l.closeOnce.Do(func() { close(l.closed); _ = l.conn.Close() })
	return nil
}
func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
func isHTTPPreface(prefix []byte) bool {
	if len(prefix) < 4 {
		return false
	}
	switch string(prefix[:4]) {
	case "GET ", "POST", "HEAD", "PUT ", "PATC", "DELE", "OPTI":
		return true
	}
	return false
}
func hasVerifiedClientCertificate(conn net.Conn) bool {
	state, ok := conn.(interface{ ConnectionState() tls.ConnectionState })
	return ok && len(state.ConnectionState().VerifiedChains) > 0
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
