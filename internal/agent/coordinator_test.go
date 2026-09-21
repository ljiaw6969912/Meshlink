package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/onboarding"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
	"meshlink/internal/tlsutil"
)

// These integration tests catch admission bypass, packet forwarding, premature
// session start, credential reuse, address disclosure, and broad revocation.
// They use real TLS, TCP/UDP sockets, certificates and the persisted registry.
type coordinatorFixture struct {
	t           *testing.T
	a           *Agent
	dir, addr   string
	nodes       []map[string]any
	device      *coordinatorSink
	coordinator *coordinator
}

type coordinatorSink struct{ reads, writes atomic.Int32 }

// runHub binds both protocols. Keep both reservations until configuration is
// ready, then release immediately before handing the address to production.
func reserveCoordinatorPort(first string) (net.Listener, *net.UDPConn, error) {
	var last error
	for attempt := 0; attempt < 100; attempt++ {
		address := first
		if attempt > 0 {
			address = "127.0.0.1:0"
		}
		tcp, err := net.Listen("tcp4", address)
		if err != nil {
			last = err
			continue
		}
		udpAddr, err := net.ResolveUDPAddr("udp4", tcp.Addr().String())
		if err != nil {
			tcp.Close()
			return nil, nil, err
		}
		udp, err := net.ListenUDP("udp4", udpAddr)
		if err == nil {
			return tcp, udp, nil
		}
		tcp.Close()
		last = err
	}
	return nil, nil, last
}

func TestCoordinatorPortReservationSkipsUDPOnlyOccupancy(t *testing.T) {
	seed, occupied, err := reserveCoordinatorPort("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := seed.Addr().String()
	defer occupied.Close()
	seed.Close()
	tcp, udp, err := reserveCoordinatorPort(address)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	defer udp.Close()
	if tcp.Addr().String() == address || tcp.Addr().String() != udp.LocalAddr().String() {
		t.Fatalf("not a free dual-protocol reservation: TCP=%s UDP=%s occupied=%s", tcp.Addr(), udp.LocalAddr(), address)
	}
}

func (d *coordinatorSink) Name() string { return "forbidden-coordinator-device" }
func (d *coordinatorSink) MTU() int     { return 1280 }
func (d *coordinatorSink) Close() error { return nil }
func (d *coordinatorSink) ReadPacket(ctx context.Context) ([]byte, error) {
	d.reads.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (d *coordinatorSink) WritePacket([]byte) error { d.writes.Add(1); return nil }

func newCoordinatorFixture(t *testing.T, manualRegistry ...bool) *coordinatorFixture {
	t.Helper()
	dir := t.TempDir()
	certs := filepath.Join(dir, "certs")
	if _, err := certutil.InitCA(certutil.CAOptions{OutDir: certs}); err != nil {
		t.Fatal(err)
	}
	f := &coordinatorFixture{t: t, dir: dir, device: &coordinatorSink{}}
	for i, id := range []string{"coordinator", "B", "C", "D", "E", "unknown"} {
		if _, err := certutil.Issue(certutil.IssueOptions{OutDir: certs, Name: id, CAPath: filepath.Join(certs, "ca.pem"), CAKeyPath: filepath.Join(certs, "ca-key.pem"), IPAddrs: []string{"127.0.0.1"}}); err != nil {
			t.Fatal(err)
		}
		if i > 0 && id != "unknown" {
			_, fp := certInfoFromFile(filepath.Join(certs, id+".pem"))
			f.nodes = append(f.nodes, map[string]any{"node_id": id, "virtual_ip": fmt.Sprintf("10.77.0.%d", i+1), "cert_fingerprint": fp, "routes": []string{"192.168." + fmt.Sprint(i) + ".0/24"}})
		}
	}
	f.saveRegistry()
	reservation, udpReservation, err := reserveCoordinatorPort("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.addr = reservation.Addr().String()
	defer reservation.Close()
	defer udpReservation.Close()
	cfg := coordinatorConfigForTest()
	cfg.Listen = f.addr
	cfg.CAFile, cfg.CertFile, cfg.KeyFile = filepath.Join(certs, "ca.pem"), filepath.Join(certs, "coordinator.pem"), filepath.Join(certs, "coordinator-key.pem")
	f.a, err = New(&cfg, discardLogger(), WithBaseDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	f.a.dev = f.device // A hub must not touch a device even if one is present.
	run := f.a.runHub
	if len(manualRegistry) > 0 && manualRegistry[0] {
		f.coordinator = newCoordinator(f.a)
		run = func(ctx context.Context) error { return serveCoordinatorControlsForTest(ctx, f.a, f.coordinator) }
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	_ = reservation.Close()
	_ = udpReservation.Close()
	go func() { done <- run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("runHub: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("coordinator did not stop")
		}
	})
	return f
}

// Keep real TLS and the production connection handler, but let the test invoke
// registry reconciliation itself to exercise changes before a watcher tick.
func serveCoordinatorControlsForTest(ctx context.Context, a *Agent, c *coordinator) error {
	cfg, err := tlsutil.ServerConfigWithClientAuth(a.cfg.CAFile, a.cfg.CertFile, a.cfg.KeyFile, tls.VerifyClientCertIfGiven)
	if err != nil {
		return err
	}
	listener, err := tls.Listen("tcp4", a.cfg.Listen, cfg)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	var handlers sync.WaitGroup
	defer func() { _ = listener.Close(); c.close(); handlers.Wait() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		handlers.Go(func() {
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			defer conn.Close()
			a.handleHubConn(ctx, c, conn)
		})
	}
}

func unavailableCoordinatorRegistry(t *testing.T, f *coordinatorFixture, kind string) func() {
	t.Helper()
	path := filepath.Join(f.dir, "configs", "devices.json")
	if err := os.Rename(path, path+".available"); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "corrupt":
		if err := os.WriteFile(path, []byte(`{"nodes":`), 0600); err != nil {
			t.Fatal(err)
		}
	case "unreadable":
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	case "missing":
	default:
		t.Fatalf("unknown registry failure %q", kind)
	}
	return func() {
		if kind != "missing" {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Rename(path+".available", path); err != nil {
			t.Fatal(err)
		}
	}
}

func coordinatorReport(p *coordinatorClient, peer string) {
	p.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{{SessionID: "reconciled-existing", Generation: 1, PeerNodeID: peer, PathType: "quic_udp"}}})
	p.barrier()
}

func TestCoordinatorRegistryUnavailablePreservesControlsAndRelationships(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			f := newCoordinatorFixture(t, true)
			b, _ := f.connect("B")
			c, _ := f.connect("C")
			d, _ := f.connect("D")
			e, _ := f.connect("E")
			// Only the surviving peers report these relationships; reverse reports
			// cannot mask accidental loss of their reconciliation maps.
			coordinatorReport(b, "C")
			coordinatorReport(d, "E")
			restore := unavailableCoordinatorRegistry(t, f, kind)
			f.coordinator.reconcileRegistry()
			for _, p := range []*coordinatorClient{b, c, d, e} {
				p.barrier()
			}
			b.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{})
			b.barrier()
			coordinatorReport(b, "C")
			for _, p := range []*coordinatorClient{b, c, d, e} {
				p.absent(proto.ControlTypeDisconnectPeer)
			}
			// Availability failure must still deny fresh admission and authorization.
			newControl := f.dial("E")
			newControl.send(proto.ControlTypeClientHello, f.hello("E"))
			newControl.want(proto.ControlTypeError)
			newControl.closed()
			e.barrier()
			b.send(proto.ControlTypeConnectRequest, proto.ConnectRequest{TargetNodeID: "C"})
			b.want(proto.ControlTypeError)
			c.absent(proto.ControlTypeConnectPrepare)
			restore()
			if _, err := (onboarding.Manager{BaseDir: f.dir}).DisableDevice("C"); err != nil {
				t.Fatal(err)
			}
			f.coordinator.reconcileRegistry()
			if got := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "C" {
				t.Fatalf("wrong revoked peer: %+v", got)
			}
			c.closed()
			d.barrier()
			e.barrier()
			// A member missing from a successfully read registry is a real removal.
			f.nodes = f.nodes[:3]
			f.nodes[1]["disabled"] = true
			f.saveRegistry()
			f.coordinator.reconcileRegistry()
			if got := decodeCoordinatorBody[proto.DisconnectPeer](t, d.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "E" {
				t.Fatalf("unrelated report relationship lost: %+v", got)
			}
			e.closed()
			b.barrier()
			d.barrier()
		})
	}
}

func coordinatorCompleteSession(t *testing.T, b, c *coordinatorClient) proto.ConnectPrepare {
	t.Helper()
	coordinatorCandidates(b, "192.168.1.20")
	coordinatorCandidates(c, "192.168.2.20")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	coordinatorReady(c, s)
	b.want(proto.ControlTypeSessionOffer)
	c.want(proto.ControlTypeSessionOffer)
	coordinatorAck(b, s)
	coordinatorAck(c, s)
	b.want(proto.ControlTypeSessionStart)
	c.want(proto.ControlTypeSessionStart)
	for _, p := range []*coordinatorClient{b, c} {
		p.send(proto.ControlTypeSessionResult, proto.SessionResult{SessionID: s.SessionID, Generation: s.Generation, Success: true})
		p.barrier()
	}
	return s
}

func coordinatorWaitMemberAbsent(t *testing.T, p *coordinatorClient, id string) {
	t.Helper()
	for {
		snapshot := decodeCoordinatorBody[proto.MemberSnapshot](t, p.want(proto.ControlTypeMemberSnapshot))
		present := false
		for _, member := range snapshot.Members {
			present = present || member.NodeID == id
		}
		if !present {
			return
		}
	}
}

func TestCoordinatorOfflinePeerReceivesIdentityRevocation(t *testing.T) {
	for _, state := range []string{"survivor_offline", "both_offline", "same_tuple"} {
		t.Run(state, func(t *testing.T) {
			f := newCoordinatorFixture(t, true)
			b, _ := f.connect("B")
			c, _ := f.connect("C")
			d, _ := f.connect("D")
			e, _ := f.connect("E")
			old := coordinatorCompleteSession(t, b, c)
			coordinatorReport(d, "E")
			_ = b.conn.Close()
			b.closed()
			coordinatorWaitMemberAbsent(t, d, "B")
			if state == "both_offline" {
				_ = c.conn.Close()
				c.closed()
				coordinatorWaitMemberAbsent(t, d, "C")
			}
			if state != "same_tuple" {
				f.nodes[1]["routes"] = []string{"192.168.99.0/24"}
				f.saveRegistry()
			}
			replacement, _ := f.connect("C")
			if state != "both_offline" {
				c.closed()
			}
			b, _ = f.connect("B")
			report := func(s proto.ConnectPrepare) {
				b.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{{SessionID: s.SessionID, Generation: s.Generation, PeerNodeID: "C", PathType: "quic_udp"}}})
			}
			report(old)
			if state == "same_tuple" {
				b.barrier()
				b.absent(proto.ControlTypeDisconnectPeer)
				return
			}
			if got := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "C" {
				t.Fatalf("offline survivor lost targeted revocation: %+v", got)
			}
			// Merely reporting a larger generation is not new authorization.
			forged := old
			forged.Generation += 1000000000
			report(forged)
			b.want(proto.ControlTypeDisconnectPeer)
			fresh := coordinatorCompleteSession(t, b, replacement)
			if fresh.Generation <= old.Generation || fresh.SessionID == old.SessionID {
				t.Fatalf("replacement did not receive fresh authorization: old=%+v fresh=%+v", old, fresh)
			}
			report(fresh)
			b.barrier()
			b.absent(proto.ControlTypeDisconnectPeer)
			// DisconnectPeer is node-scoped: a stale duplicate in the same report
			// must not tear down a simultaneously reported fresh authorization.
			b.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{
				{SessionID: old.SessionID, Generation: old.Generation, PeerNodeID: "C"},
				{SessionID: fresh.SessionID, Generation: fresh.Generation, PeerNodeID: "C"},
			}})
			b.barrier()
			b.absent(proto.ControlTypeDisconnectPeer)
			for _, p := range []*coordinatorClient{replacement, d, e} {
				p.barrier()
				p.absent(proto.ControlTypeDisconnectPeer)
			}
			f.nodes[3]["disabled"] = true
			f.saveRegistry()
			f.coordinator.reconcileRegistry()
			if got := decodeCoordinatorBody[proto.DisconnectPeer](t, d.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "E" {
				t.Fatalf("unrelated relationship lost: %+v", got)
			}
			b.barrier()
			replacement.barrier()
		})
	}
}

func TestCoordinatorReflectedRequestIDIsBoundedBeforeNegotiation(t *testing.T) {
	for _, size := range []int{256, 257, 525 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := newCoordinatorFixture(t, true)
			b, _ := f.connect("B")
			c, _ := f.connect("C")
			requestID := strings.Repeat("r", size)
			payload, err := proto.MarshalControl(proto.ControlTypeConnectRequest, requestID, proto.ConnectRequest{TargetNodeID: "C"})
			if err != nil || len(payload) >= proto.MaxPayload {
				t.Fatalf("regression request must fit inbound frame: bytes=%d err=%v", len(payload), err)
			}
			_ = b.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := proto.Write(b.conn, proto.TypeControl, payload); err != nil {
				t.Fatal(err)
			}
			if size == 256 {
				for _, p := range []*coordinatorClient{b, c} {
					env := p.want(proto.ControlTypeConnectPrepare)
					prepare := decodeCoordinatorBody[proto.ConnectPrepare](t, env)
					if env.RequestID != requestID || prepare.RequestID != requestID {
						t.Fatal("valid boundary request ID was not preserved")
					}
					p.barrier()
				}
				return
			}
			b.closed()
			// This round trip must succeed after processing the attack. Merely
			// observing no prepare frame would miss closure of the innocent peer.
			c.barrier()
			if f.metric("negotiations_prepared") != 0 {
				t.Fatal("oversized reflected ID created a negotiation")
			}
			f.coordinator.mu.Lock()
			defer f.coordinator.mu.Unlock()
			if len(f.coordinator.negotiations) != 0 || len(f.coordinator.relationships) != 0 {
				t.Fatal("offending input created negotiation or revocation state")
			}
		})
	}
}

func TestCoordinatorRevocationHistoryCapacityFailsClosed(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	f.coordinator.mu.Lock()
	for i := 0; i < maxCoordinatorRelationships; i++ {
		f.coordinator.relationships[unorderedPair("offline", fmt.Sprint(i))] = &coordinatorRelationship{revoked: true}
	}
	f.coordinator.mu.Unlock()
	b.send(proto.ControlTypeConnectRequest, proto.ConnectRequest{TargetNodeID: "C"})
	requestError := b.want(proto.ControlTypeError)
	if requestError.RequestID != "request" {
		t.Error("per-request capacity error lost initiating RequestID")
	}
	if got := decodeCoordinatorBody[proto.ControlError](t, requestError); got.Code != "coordinator_capacity" {
		t.Fatalf("unbounded new pair authorization: %+v", got)
	}
	b.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{{SessionID: "previous", Generation: 1, PeerNodeID: "C"}}})
	globalError := b.want(proto.ControlTypeError)
	if globalError.RequestID != "" {
		t.Error("global ActiveSessions capacity error acquired pair scope")
	}
	if got := decodeCoordinatorBody[proto.ControlError](t, globalError); got.Code != "coordinator_capacity" {
		t.Fatalf("unbounded report relationship: %+v", got)
	}
	b.barrier()
	c.barrier()
	f.coordinator.mu.Lock()
	defer f.coordinator.mu.Unlock()
	if len(f.coordinator.relationships) != maxCoordinatorRelationships || len(f.coordinator.negotiations) != 0 {
		t.Fatal("capacity overflow evicted revocation evidence or authorized a new pair")
	}
	for _, relationship := range f.coordinator.relationships {
		if !relationship.revoked {
			t.Fatal("capacity handling discarded a pending revocation")
		}
	}
}

func TestCoordinatorHistoryCapacityDoesNotMaskPendingRevocation(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	d, _ := f.connect("D")
	f.coordinator.mu.Lock()
	f.coordinator.relationships[unorderedPair("B", "C")] = &coordinatorRelationship{revoked: true}
	for i := 1; i < maxCoordinatorRelationships; i++ {
		f.coordinator.relationships[unorderedPair("offline", fmt.Sprint(i))] = &coordinatorRelationship{revoked: true}
	}
	f.coordinator.mu.Unlock()
	b.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{
		{SessionID: "revoked", Generation: 1, PeerNodeID: "C"},
		{SessionID: "untracked", Generation: 1, PeerNodeID: "D"},
	}})
	if got := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "C" {
		t.Fatalf("capacity masked pending revocation: %+v", got)
	}
	b.want(proto.ControlTypeError)
	for _, p := range []*coordinatorClient{b, c, d} {
		p.barrier()
	}
	f.coordinator.mu.Lock()
	defer f.coordinator.mu.Unlock()
	if len(f.coordinator.relationships) != maxCoordinatorRelationships {
		t.Fatal("over-capacity report changed bounded revocation history")
	}
}

func TestCoordinatorChangedIdentityReconnectRevokesCompletedSessions(t *testing.T) {
	for _, oldState := range []string{"connected", "disconnected"} {
		for _, change := range []string{"ip", "routes", "fingerprint", "same_tuple"} {
			t.Run(oldState+"/"+change, func(t *testing.T) {
				f := newCoordinatorFixture(t, true)
				b, _ := f.connect("B")
				old, _ := f.connect("C")
				d, _ := f.connect("D")
				e, _ := f.connect("E")
				coordinatorCompleteSession(t, b, old)
				coordinatorReport(d, "E")
				if oldState == "disconnected" {
					_ = old.conn.Close()
					old.closed()
					for {
						snapshot := decodeCoordinatorBody[proto.MemberSnapshot](t, b.want(proto.ControlTypeMemberSnapshot))
						present := false
						for _, member := range snapshot.Members {
							if member.NodeID == "C" {
								present = true
							}
						}
						if !present {
							break
						}
					}
				}
				switch change {
				case "ip":
					f.nodes[1]["virtual_ip"] = "10.77.0.99"
				case "routes":
					f.nodes[1]["routes"] = []string{"192.168.99.0/24"}
				case "fingerprint":
					certs := filepath.Join(f.dir, "certs")
					if _, err := certutil.Issue(certutil.IssueOptions{OutDir: certs, Name: "C", CAPath: filepath.Join(certs, "ca.pem"), CAKeyPath: filepath.Join(certs, "ca-key.pem"), IPAddrs: []string{"127.0.0.1"}}); err != nil {
						t.Fatal(err)
					}
					_, fingerprint := certInfoFromFile(filepath.Join(certs, "C.pem"))
					f.nodes[1]["cert_fingerprint"] = fingerprint
				}
				f.saveRegistry()
				// This fixture has no automatic watcher: admission itself must
				// reconcile the old tuple before it overwrites the retained identity.
				replacement, _ := f.connect("C")
				if oldState == "connected" {
					old.closed()
				}
				if change == "same_tuple" {
					b.barrier()
					b.absent(proto.ControlTypeDisconnectPeer)
				} else {
					if got := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "C" {
						t.Fatalf("wrong changed identity: %+v", got)
					}
				}
				replacement.barrier()
				d.barrier()
				e.barrier()
				d.absent(proto.ControlTypeDisconnectPeer)
				e.absent(proto.ControlTypeDisconnectPeer)
				if _, err := (onboarding.Manager{BaseDir: f.dir}).DisableDevice("E"); err != nil {
					t.Fatal(err)
				}
				f.coordinator.reconcileRegistry()
				if got := decodeCoordinatorBody[proto.DisconnectPeer](t, d.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "E" {
					t.Fatalf("unrelated D-E relationship lost: %+v", got)
				}
				b.barrier()
				replacement.barrier()
			})
		}
	}
}

func TestCoordinatorDuplicateRegistryIdentityRevokesOnlyAffectedPairs(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	d, _ := f.connect("D")
	e, _ := f.connect("E")
	coordinatorCompleteSession(t, b, c)
	coordinatorReport(d, "E")
	f.nodes = append(f.nodes, f.nodes[1])
	f.saveRegistry()
	// Fresh admission already fails for the duplicate. Existing authorization
	// must also converge, without treating this as a whole-registry outage.
	rejected := f.dial("C")
	rejected.send(proto.ControlTypeClientHello, f.hello("C"))
	rejected.want(proto.ControlTypeError)
	rejected.closed()
	f.coordinator.reconcileRegistry()
	if got := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "C" {
		t.Fatalf("wrong ambiguous identity: %+v", got)
	}
	c.closed()
	d.barrier()
	e.barrier()
	d.absent(proto.ControlTypeDisconnectPeer)
	e.absent(proto.ControlTypeDisconnectPeer)
	// Keep the duplicate present while revoking E: ambiguity must not prevent
	// another valid identity's persisted change from converging independently.
	f.nodes[3]["disabled"] = true
	f.saveRegistry()
	f.coordinator.reconcileRegistry()
	if got := decodeCoordinatorBody[proto.DisconnectPeer](t, d.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "E" {
		t.Fatalf("unrelated relationship lost: %+v", got)
	}
	e.closed()
	b.barrier()
	d.barrier()
}

func (f *coordinatorFixture) saveRegistry() {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.dir, "configs"), 0700); err != nil {
		f.t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"nodes": f.nodes})
	if err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(f.dir, "configs", "devices.json")
	if err := os.WriteFile(path+".tmp", b, 0600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		f.t.Fatal(err)
	}
}

type coordinatorClient struct {
	t      *testing.T
	conn   net.Conn
	frames chan proto.ControlEnvelope
	errors chan error
}

func (f *coordinatorFixture) dial(id string) *coordinatorClient {
	f.t.Helper()
	certs := filepath.Join(f.dir, "certs")
	cfg, err := tlsutil.ClientConfig(filepath.Join(certs, "ca.pem"), filepath.Join(certs, id+".pem"), filepath.Join(certs, id+"-key.pem"), "127.0.0.1")
	if err != nil {
		f.t.Fatal(err)
	}
	var conn net.Conn
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 300 * time.Millisecond}, "tcp4", f.addr, cfg)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	p := &coordinatorClient{t: f.t, conn: conn, frames: make(chan proto.ControlEnvelope, 100), errors: make(chan error, 1)}
	f.t.Cleanup(func() { _ = conn.Close() })
	go func() {
		for {
			frame, err := proto.Read(conn)
			if err != nil {
				p.errors <- err
				return
			}
			if frame.Type != proto.TypeControl {
				p.errors <- fmt.Errorf("received legacy frame type %d", frame.Type)
				return
			}
			env, err := proto.ParseControl(frame.Payload)
			if err != nil {
				p.errors <- err
				return
			}
			p.frames <- env
		}
	}()
	return p
}

func (f *coordinatorFixture) hello(id string) proto.ClientHello {
	for _, n := range f.nodes {
		if n["node_id"] == id {
			return proto.ClientHello{ProtocolVersion: 2, Role: "peer", NodeID: id, VirtualIP: n["virtual_ip"].(string), Routes: n["routes"].([]string), MTU: 1280, Capabilities: []string{"quic_udp_v1"}}
		}
	}
	return proto.ClientHello{ProtocolVersion: 2, Role: "peer", NodeID: id, VirtualIP: "10.77.0.99", MTU: 1280, Capabilities: []string{"quic_udp_v1"}}
}

func (f *coordinatorFixture) connect(id string) (*coordinatorClient, proto.ProbeCredential) {
	f.t.Helper()
	p := f.dial(id)
	p.send(proto.ControlTypeClientHello, f.hello(id))
	p.want(proto.ControlTypeServerHello)
	credential := decodeCoordinatorBody[proto.ProbeCredential](f.t, p.want(proto.ControlTypeProbeCredential))
	if !credential.ExpiresAt.After(time.Now()) || credential.ExpiresAt.After(time.Now().Add(91*time.Second)) {
		f.t.Fatalf("probe credential lifetime: %v", credential.ExpiresAt)
	}
	p.want(proto.ControlTypeMemberSnapshot)
	return p, credential
}

func (p *coordinatorClient) send(typ string, body any) {
	p.t.Helper()
	payload, err := proto.MarshalControl(typ, "request", body)
	if err != nil {
		p.t.Fatal(err)
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := proto.Write(p.conn, proto.TypeControl, payload); err != nil {
		p.t.Fatal(err)
	}
}
func (p *coordinatorClient) want(typ string) proto.ControlEnvelope {
	p.t.Helper()
	return p.wantWithin(typ, 3*time.Second)
}
func (p *coordinatorClient) wantWithin(typ string, wait time.Duration) proto.ControlEnvelope {
	p.t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case env := <-p.frames:
			if env.Type == typ {
				return env
			}
			if env.Type != proto.ControlTypeMemberSnapshot && env.Type != proto.ControlTypeMemberDelta {
				p.t.Fatalf("wanted %s, got %s: %s", typ, env.Type, env.Body)
			}
		case err := <-p.errors:
			p.t.Fatalf("wanted %s, control ended: %v", typ, err)
		case <-timer.C:
			p.t.Fatalf("timed out waiting for %s", typ)
		}
	}
}
func (p *coordinatorClient) absent(typ string) {
	p.t.Helper()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case env := <-p.frames:
			if env.Type == typ {
				p.t.Fatalf("premature %s: %s", typ, env.Body)
			}
		case err := <-p.errors:
			p.t.Fatalf("control closed unexpectedly: %v", err)
		case <-timer.C:
			return
		}
	}
}
func (p *coordinatorClient) closed() {
	p.t.Helper()
	select {
	case <-p.errors:
	case <-time.After(3 * time.Second):
		p.t.Fatal("control connection remained open")
	}
}
func (p *coordinatorClient) barrier() {
	p.send(proto.ControlTypePing, proto.ControlPing{Nonce: "barrier"})
	p.want(proto.ControlTypePong)
}
func decodeCoordinatorBody[T any](t *testing.T, env proto.ControlEnvelope) T {
	t.Helper()
	body, err := proto.DecodeControlBody[T](env)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func (f *coordinatorFixture) metric(name string) float64 {
	b, _ := json.Marshal(f.a.status.snapshot())
	var status map[string]any
	_ = json.Unmarshal(b, &status)
	m, ok := status["coordinator_metrics"].(map[string]any)
	if !ok {
		f.t.Fatal("coordinator metrics missing from runtime status")
	}
	v, _ := m[name].(float64)
	return v
}

func TestCoordinatorRejectsV1AndTypePacketWithoutForwarding(t *testing.T) {
	for _, legacy := range []string{"hello", "missing", "v1"} {
		t.Run(legacy, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			p := f.dial("B")
			switch legacy {
			case "hello":
				payload, _ := (proto.Hello{NodeID: "B", VirtualIP: "10.77.0.2", MTU: 1280}).Marshal()
				_ = proto.Write(p.conn, proto.TypeHello, payload)
			case "missing":
				p.send(proto.ControlTypePing, proto.ControlPing{})
			case "v1":
				_ = proto.Write(p.conn, proto.TypeControl, []byte(`{"protocol_version":1,"type":"client_hello","body":{}}`))
			}
			err := decodeCoordinatorBody[proto.ControlError](t, p.want(proto.ControlTypeError))
			if err.Code != "control_upgrade_required" || err.Message == "" {
				t.Fatalf("upgrade error = %+v", err)
			}
			p.closed()
		})
	}
	t.Run("packet", func(t *testing.T) {
		f := newCoordinatorFixture(t)
		p, _ := f.connect("B")
		c, _ := f.connect("C")
		packet := make([]byte, 20)
		packet[0] = 0x45
		packet[2] = 0
		packet[3] = 20
		copy(packet[12:16], []byte{10, 77, 0, 2})
		copy(packet[16:20], []byte{10, 77, 0, 3})
		_ = proto.Write(p.conn, proto.TypePacket, packet)
		p.closed()
		c.barrier()
		if f.metric("type_packet_violations") != 1 {
			t.Fatal("TypePacket must increment exactly once")
		}
		if f.device.reads.Load() != 0 || f.device.writes.Load() != 0 {
			t.Fatal("coordinator accessed a packet device")
		}
	})
}

func TestCoordinatorBindsHelloNodeAndFingerprintToRegistry(t *testing.T) {
	for _, mismatch := range []string{"node", "case", "fingerprint", "ip", "routes", "cert_identity", "unknown", "exact"} {
		t.Run(mismatch, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			hello := f.hello("B")
			certID := "B"
			switch mismatch {
			case "node":
				hello.NodeID = "C"
			case "case":
				hello.NodeID = "b"
			case "fingerprint":
				f.nodes[0]["cert_fingerprint"] = "SHA256:WRONG"
				f.saveRegistry()
			case "ip":
				hello.VirtualIP = "10.77.0.9"
			case "routes":
				hello.Routes = []string{"192.168.99.0/24"}
			case "cert_identity":
				certID = "C"
				f.nodes[0]["cert_fingerprint"] = f.nodes[1]["cert_fingerprint"]
				f.saveRegistry()
			case "unknown":
				certID = "unknown"
				hello = f.hello(certID)
			}
			p := f.dial(certID)
			p.send(proto.ControlTypeClientHello, hello)
			if mismatch == "exact" {
				p.want(proto.ControlTypeServerHello)
				return
			}
			err := decodeCoordinatorBody[proto.ControlError](t, p.want(proto.ControlTypeError))
			if err.Code != "identity_mismatch" {
				t.Fatalf("admission error: %+v", err)
			}
			p.closed()
		})
	}
}

func TestCoordinatorMemberSnapshotHasNoPhysicalAddresses(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	coordinatorCandidates(b, "192.168.1.20")
	c, _ := f.connect("C")
	snapshot := b.want(proto.ControlTypeMemberSnapshot)
	members := decodeCoordinatorBody[proto.MemberSnapshot](t, snapshot)
	if members.Revision < 2 {
		t.Fatalf("member revision = %d", members.Revision)
	}
	found := false
	for _, m := range members.Members {
		if m.NodeID == "C" {
			found = true
			if m.VirtualIP != "10.77.0.3" || len(m.Routes) != 1 || m.Routes[0] != "192.168.2.0/24" || m.Status != "online" || m.Fingerprint == "" {
				t.Fatalf("member identity missing: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("snapshot omitted C")
	}
	for _, forbidden := range []string{b.conn.LocalAddr().String(), c.conn.LocalAddr().String(), "127.0.0.1", "candidate", "probe_id"} {
		if strings.Contains(string(snapshot.Body), forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, snapshot.Body)
		}
	}
}

func coordinatorCandidates(p *coordinatorClient, address string) {
	p.send(proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: 1, Candidates: []proto.Candidate{{Address: address, Port: 45000, Scope: "lan", Priority: 100, ExpiresAt: time.Now().Add(time.Minute)}}})
	p.barrier()
}
func coordinatorPrepare(t *testing.T, b, c *coordinatorClient) proto.ConnectPrepare {
	t.Helper()
	b.send(proto.ControlTypeConnectRequest, proto.ConnectRequest{TargetNodeID: "C"})
	c.send(proto.ControlTypeConnectRequest, proto.ConnectRequest{TargetNodeID: "B"})
	bp := decodeCoordinatorBody[proto.ConnectPrepare](t, b.want(proto.ControlTypeConnectPrepare))
	cp := decodeCoordinatorBody[proto.ConnectPrepare](t, c.want(proto.ControlTypeConnectPrepare))
	if bp.SessionID == "" || bp.SessionID != cp.SessionID || bp.Generation != cp.Generation || bp.PeerNodeID != "C" || cp.PeerNodeID != "B" {
		t.Fatalf("prepare mismatch: %+v / %+v", bp, cp)
	}
	return bp
}
func coordinatorReady(p *coordinatorClient, s proto.ConnectPrepare) {
	p.send(proto.ControlTypeConnectReady, proto.ConnectReady{SessionID: s.SessionID, Generation: s.Generation, CandidateRevision: 1, Ready: true})
}
func coordinatorAck(p *coordinatorClient, s proto.ConnectPrepare) {
	p.send(proto.ControlTypeSessionOfferAck, proto.SessionOfferAck{SessionID: s.SessionID, Generation: s.Generation, Ready: true})
}

func TestCoordinatorConnectSingleFlightRequiresBothReadyAndBothAck(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(b, "192.168.1.20")
	coordinatorCandidates(c, "192.168.2.20")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	b.absent(proto.ControlTypeSessionOffer)
	c.absent(proto.ControlTypeSessionOffer)
	coordinatorReady(c, s)
	bo := decodeCoordinatorBody[proto.SessionOffer](t, b.want(proto.ControlTypeSessionOffer))
	co := decodeCoordinatorBody[proto.SessionOffer](t, c.want(proto.ControlTypeSessionOffer))
	if len(bo.Candidates) != 1 || bo.Candidates[0].Address != "192.168.2.20" || co.Candidates[0].Address != "192.168.1.20" {
		t.Fatalf("authorized candidates: %+v / %+v", bo.Candidates, co.Candidates)
	}
	coordinatorAck(b, s)
	b.absent(proto.ControlTypeSessionStart)
	c.absent(proto.ControlTypeSessionStart)
	coordinatorAck(c, s)
	b.want(proto.ControlTypeSessionStart)
	c.want(proto.ControlTypeSessionStart)
	if f.metric("negotiations_started") != 1 || f.metric("negotiations_requested") != 2 {
		t.Fatal("simultaneous requests were not single-flight")
	}
}

func TestCoordinatorOffersNewerGenerationAndOneTimeKey(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(b, "192.168.1.20")
	coordinatorCandidates(c, "192.168.2.20")
	seed := uint64(time.Now().UnixNano())
	var prior proto.SessionOffer
	for i := 0; i < 2; i++ {
		s := coordinatorPrepare(t, b, c)
		coordinatorReady(b, s)
		coordinatorReady(c, s)
		bo := decodeCoordinatorBody[proto.SessionOffer](t, b.want(proto.ControlTypeSessionOffer))
		co := decodeCoordinatorBody[proto.SessionOffer](t, c.want(proto.ControlTypeSessionOffer))
		key, err := hex.DecodeString(bo.PairingKey)
		if err != nil || len(key) != 32 || bo.PairingKey != co.PairingKey {
			t.Fatal("offer must share one 32-byte key")
		}
		if bo.Generation <= seed || bo.Generation <= prior.Generation || bo.SessionID == prior.SessionID || bo.PairingKey == prior.PairingKey {
			t.Fatal("session material or generation reused")
		}
		coordinatorAck(b, s)
		coordinatorAck(c, s)
		b.want(proto.ControlTypeSessionStart)
		c.want(proto.ControlTypeSessionStart)
		b.send(proto.ControlTypeSessionResult, proto.SessionResult{SessionID: s.SessionID, Generation: s.Generation, Success: true})
		c.send(proto.ControlTypeSessionResult, proto.SessionResult{SessionID: s.SessionID, Generation: s.Generation, Success: true})
		b.barrier()
		c.barrier()
		prior = bo
	}
}

func TestCoordinatorRevocationTargetsOnlyAffectedPairs(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	d, _ := f.connect("D")
	e, _ := f.connect("E")
	for _, pair := range []struct {
		p    *coordinatorClient
		peer string
	}{{b, "C"}, {c, "B"}, {d, "E"}, {e, "D"}} {
		pair.p.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{{SessionID: "reconciled-existing", Generation: 1, PeerNodeID: pair.peer, PathType: "quic_udp"}}})
		pair.p.barrier()
	}
	if _, err := (onboarding.Manager{BaseDir: f.dir}).DisableDevice("C"); err != nil {
		t.Fatal(err)
	}
	disconnect := decodeCoordinatorBody[proto.DisconnectPeer](t, b.wantWithin(proto.ControlTypeDisconnectPeer, 4*time.Second))
	if disconnect.NodeID != "C" {
		t.Fatalf("disconnect = %+v", disconnect)
	}
	c.closed()
	d.barrier()
	e.barrier()
	d.absent(proto.ControlTypeDisconnectPeer)
	e.absent(proto.ControlTypeDisconnectPeer)
}

func TestCoordinatorNewestControlReplacesOldConnection(t *testing.T) {
	f := newCoordinatorFixture(t)
	old, _ := f.connect("B")
	replacement, _ := f.connect("B")
	old.closed()
	replacement.barrier()
	if f.metric("control_connections") != 2 || f.metric("control_reconnects") != 1 {
		t.Fatal("reconnect metrics incorrect")
	}
}

func TestCoordinatorFatalProbeListenerErrorStopsHub(t *testing.T) {
	cfg := coordinatorConfigForTest()
	a, err := New(&cfg, discardLogger(), WithBaseDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.runHubListeners(ctx, listener, udp) }()
	// An unexpected listener close is fatal, not an unauthenticated packet or
	// ordinary context cancellation. The control accept loop must unblock too.
	_ = udp.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) || !strings.Contains(err.Error(), "probe listener") {
			t.Fatalf("hub lost probe listener failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hub continued after its probe listener failed")
	}
}

func TestCoordinatorOversizedDatagramDoesNotStopProbes(t *testing.T) {
	f := newCoordinatorFixture(t)
	p, credential := f.connect("B")
	udp, err := net.Dial("udp4", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if _, err := udp.Write(bytes.Repeat([]byte{0xff}, 4096)); err != nil {
		t.Fatal(err)
	}
	request, err := p2p.EncodeProbeRequest(credential, time.Now(), bytes.Repeat([]byte{9}, 16))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := udp.Write(request); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 2048)
	n, err := udp.Read(buffer)
	if err != nil {
		t.Fatalf("authenticated probe failed after oversized datagram: %v", err)
	}
	response, err := p2p.DecodeProbeResponse(buffer[:n], credential, time.Now())
	if err != nil || response.ObservedAddress.String() != udp.LocalAddr().String() {
		t.Fatalf("authenticated response: %+v %v", response, err)
	}
	p.barrier()
	if f.metric("probe_successes") != 1 || f.metric("probe_failures") != 1 {
		t.Fatal("oversized datagram must count as one failed probe")
	}
}

func TestCoordinatorProbeUsesSamePortAndRejectsReplay(t *testing.T) {
	f := newCoordinatorFixture(t)
	p, credential := f.connect("B")
	udp, err := net.Dial("udp4", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	request, err := p2p.EncodeProbeRequest(credential, time.Now(), bytes.Repeat([]byte{1}, 16))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = udp.Write(request); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 2048)
	n, err := udp.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	response, err := p2p.DecodeProbeResponse(buf[:n], credential, time.Now())
	if err != nil || response.ObservedAddress.String() != udp.LocalAddr().String() {
		t.Fatalf("observed source: %+v %v", response, err)
	}
	_, _ = udp.Write(request)
	_ = udp.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, err = udp.Read(buf); err == nil {
		t.Fatal("replayed probe received a response")
	}
	p.barrier()
	if f.metric("probe_successes") != 1 || f.metric("probe_failures") != 1 {
		t.Fatal("probe counters incorrect")
	}
}

func TestCoordinatorPrepareAndAckTimeoutsDoNotStart(t *testing.T) {
	for _, stage := range []string{"prepare", "ack"} {
		t.Run(stage, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			b, _ := f.connect("B")
			c, _ := f.connect("C")
			coordinatorCandidates(b, "192.168.1.20")
			coordinatorCandidates(c, "192.168.2.20")
			s := coordinatorPrepare(t, b, c)
			if stage == "ack" {
				coordinatorReady(b, s)
				coordinatorReady(c, s)
				b.want(proto.ControlTypeSessionOffer)
				c.want(proto.ControlTypeSessionOffer)
				coordinatorAck(b, s)
			}
			abort := decodeCoordinatorBody[proto.SessionAbort](t, b.wantWithin(proto.ControlTypeSessionAbort, 6*time.Second))
			if abort.SessionID != s.SessionID || abort.Code != stage+"_timeout" {
				t.Fatalf("abort: %+v", abort)
			}
			c.want(proto.ControlTypeSessionAbort)
			coordinatorReady(b, s)
			coordinatorReady(c, s)
			coordinatorAck(b, s)
			coordinatorAck(c, s)
			b.absent(proto.ControlTypeSessionStart)
			c.absent(proto.ControlTypeSessionStart)
		})
	}
}

func TestCoordinatorExpiredCandidatesCannotEnterOffer(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	b.send(proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: 1, Candidates: []proto.Candidate{{Address: "192.168.1.20", Port: 1234, Scope: "lan", ExpiresAt: time.Now().Add(-time.Second)}}})
	b.barrier()
	coordinatorCandidates(c, "192.168.2.20")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	coordinatorReady(c, s)
	abort := decodeCoordinatorBody[proto.SessionAbort](t, b.want(proto.ControlTypeSessionAbort))
	if abort.Code != "candidate_unavailable" {
		t.Fatalf("expired candidate abort: %+v", abort)
	}
}

func TestCoordinatorCompletedSessionRevokedBeforeActiveReport(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(b, "192.168.1.20")
	coordinatorCandidates(c, "192.168.2.20")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	coordinatorReady(c, s)
	b.want(proto.ControlTypeSessionOffer)
	c.want(proto.ControlTypeSessionOffer)
	coordinatorAck(b, s)
	coordinatorAck(c, s)
	b.want(proto.ControlTypeSessionStart)
	c.want(proto.ControlTypeSessionStart)
	for _, p := range []*coordinatorClient{b, c} {
		p.send(proto.ControlTypeSessionResult, proto.SessionResult{SessionID: s.SessionID, Generation: s.Generation, Success: true})
		p.barrier()
	}
	if _, err := (onboarding.Manager{BaseDir: f.dir}).DisableDevice("C"); err != nil {
		t.Fatal(err)
	}
	disconnect := decodeCoordinatorBody[proto.DisconnectPeer](t, b.wantWithin(proto.ControlTypeDisconnectPeer, 4*time.Second))
	if disconnect.NodeID != "C" {
		t.Fatalf("wrong revoked peer: %+v", disconnect)
	}
}

func TestCoordinatorRejectsConflictingAndNonPrivateRegistryRoutes(t *testing.T) {
	for _, kind := range []string{"duplicate_ip", "equal_prefix", "public_route", "public_ip", "noncanonical_route", "route_order"} {
		t.Run(kind, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			b, _ := f.connect("B")
			switch kind {
			case "duplicate_ip":
				f.nodes[1]["virtual_ip"] = "10.77.0.2"
			case "equal_prefix":
				f.nodes[1]["routes"] = []string{"192.168.1.0/24"}
			case "public_route":
				f.nodes[1]["routes"] = []string{"0.0.0.0/0"}
			case "public_ip":
				f.nodes[1]["virtual_ip"] = "8.8.8.8"
			case "noncanonical_route":
				f.nodes[1]["routes"] = []string{"192.168.2.7/24"}
			case "route_order":
				f.nodes[1]["routes"] = []string{"192.168.2.0/24", "192.168.3.0/24"}
			}
			f.saveRegistry()
			p := f.dial("C")
			hello := f.hello("C")
			if kind == "route_order" {
				hello.Routes = []string{"192.168.3.0/24", "192.168.2.0/24"}
			}
			p.send(proto.ControlTypeClientHello, hello)
			p.want(proto.ControlTypeError)
			p.closed()
			b.barrier()
		})
	}
}

func TestCoordinatorAllowsNestedRegisteredRoutes(t *testing.T) {
	for _, routes := range []struct{ name, b, c string }{
		{"lpm_example", "10.0.0.0/8", "10.77.0.0/24"},
		{"nested_lan", "192.168.1.0/24", "192.168.1.0/25"},
		{"virtual_ip_in_route", "192.168.1.0/24", "10.77.0.0/24"},
	} {
		t.Run(routes.name, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			f.nodes[0]["routes"] = []string{routes.b}
			f.nodes[1]["routes"] = []string{routes.c}
			f.saveRegistry()
			b, _ := f.connect("B")
			c, _ := f.connect("C")
			// The future packet router resolves these literal different-length
			// prefixes by LPM. The coordinator must admit both owners and prepare.
			coordinatorPrepare(t, b, c)
		})
	}
}

func TestCoordinatorRejectsPacketHeaderBeforeReadingPayload(t *testing.T) {
	for _, declared := range []uint32{proto.MaxPayload, proto.MaxPayload + 1} {
		t.Run(fmt.Sprint(declared), func(t *testing.T) {
			f := newCoordinatorFixture(t)
			p, _ := f.connect("B")
			header := []byte{'M', 'S', 'H', '1', 1, proto.TypePacket, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(header[6:], declared)
			if _, err := p.conn.Write(header); err != nil {
				t.Fatal(err)
			}
			p.closed()
			if f.metric("type_packet_violations") != 1 {
				t.Fatal("packet header must be rejected immediately and counted once")
			}
		})
	}
}

func TestCoordinatorOverallTimeoutAndActiveReportsCannotReauthorize(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(b, "192.168.1.20")
	coordinatorCandidates(c, "192.168.2.20")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	coordinatorReady(c, s)
	b.want(proto.ControlTypeSessionOffer)
	c.want(proto.ControlTypeSessionOffer)
	coordinatorAck(b, s)
	coordinatorAck(c, s)
	b.want(proto.ControlTypeSessionStart)
	c.want(proto.ControlTypeSessionStart)
	abort := decodeCoordinatorBody[proto.SessionAbort](t, b.wantWithin(proto.ControlTypeSessionAbort, 16*time.Second))
	if abort.Code != "negotiation_timeout" {
		t.Fatalf("overall timeout = %+v", abort)
	}
	c.want(proto.ControlTypeSessionAbort)
	for _, entry := range []struct {
		p  *coordinatorClient
		id string
	}{{b, "C"}, {c, "B"}} {
		entry.p.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{{SessionID: s.SessionID, Generation: s.Generation, PeerNodeID: entry.id, PathType: "quic_udp"}}})
		coordinatorReady(entry.p, s)
		coordinatorAck(entry.p, s)
		entry.p.barrier()
		entry.p.absent(proto.ControlTypeSessionStart)
	}
	next := coordinatorPrepare(t, b, c)
	if next.SessionID == s.SessionID || next.Generation <= s.Generation {
		t.Fatal("expired active report revived old authorization")
	}
}

func TestCoordinatorProbeCredentialIsBoundToCurrentControl(t *testing.T) {
	f := newCoordinatorFixture(t)
	old, credential := f.connect("B")
	replacement, _ := f.connect("B")
	old.closed()
	udp, err := net.Dial("udp4", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	packet, err := p2p.EncodeProbeRequest(credential, time.Now(), bytes.Repeat([]byte{2}, 16))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = udp.Write(packet)
	_ = udp.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, err = udp.Read(make([]byte, 2048)); err == nil {
		t.Fatal("superseded control's probe credential remained usable")
	}
	replacement.barrier()
}

func TestCoordinatorCandidateRenewalPreservesOtherPairReadiness(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(b, "192.168.1.20")
	coordinatorCandidates(c, "192.168.2.20")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	b.barrier()
	// Preparing another pair refreshes the same endpoints with a newer TTL.
	b.send(proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: 2, Candidates: []proto.Candidate{{Address: "192.168.1.20", Port: 45000, Scope: "lan", Priority: 100, ExpiresAt: time.Now().Add(time.Minute)}}})
	b.barrier()
	coordinatorReady(c, s)
	b.want(proto.ControlTypeSessionOffer)
	c.want(proto.ControlTypeSessionOffer)
}

func TestCoordinatorCandidateChangeAfterReadyRequiresNewReady(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(b, "192.168.1.20")
	coordinatorCandidates(c, "192.168.2.20")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	b.barrier()
	b.send(proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: 2, Candidates: []proto.Candidate{{Address: "192.168.1.21", Port: 45001, Scope: "lan", ExpiresAt: time.Now().Add(time.Minute)}}})
	b.barrier()
	coordinatorReady(c, s)
	b.absent(proto.ControlTypeSessionOffer)
	c.absent(proto.ControlTypeSessionOffer)
	b.send(proto.ControlTypeConnectReady, proto.ConnectReady{SessionID: s.SessionID, Generation: s.Generation, CandidateRevision: 2, Ready: true})
	b.want(proto.ControlTypeSessionOffer)
	c.want(proto.ControlTypeSessionOffer)
}

func TestCoordinatorCurrentConnectionMetricTracksDisconnect(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	if f.metric("active_control_connections") != 2 {
		t.Fatal("current control connection count must include both connected peers")
	}
	_ = c.conn.Close()
	c.closed()
	for {
		snapshot := decodeCoordinatorBody[proto.MemberSnapshot](t, b.want(proto.ControlTypeMemberSnapshot))
		if len(snapshot.Members) == 1 {
			break
		}
	}
	b.barrier()
	if f.metric("active_control_connections") != 1 {
		t.Fatal("current connection count did not decrement after disconnect")
	}
}

func TestCoordinatorObservedCandidateDisclosedOnlyInAuthorizedOffer(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, credential := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(c, "192.168.2.20")
	udp, err := net.Dial("udp4", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	request, err := p2p.EncodeProbeRequest(credential, time.Now(), bytes.Repeat([]byte{3}, 16))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = udp.Write(request)
	_ = udp.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = udp.Read(make([]byte, 2048)); err != nil {
		t.Fatal(err)
	}
	s := coordinatorPrepare(t, b, c)
	b.send(proto.ControlTypeConnectReady, proto.ConnectReady{SessionID: s.SessionID, Generation: s.Generation, Ready: true})
	coordinatorReady(c, s)
	b.want(proto.ControlTypeSessionOffer)
	offer := decodeCoordinatorBody[proto.SessionOffer](t, c.want(proto.ControlTypeSessionOffer))
	if len(offer.Candidates) != 1 || offer.Candidates[0].Scope != "public" || net.JoinHostPort(offer.Candidates[0].Address, fmt.Sprint(offer.Candidates[0].Port)) != udp.LocalAddr().String() || offer.Candidates[0].ExpiresAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("observed candidate not authorized with TTL: %+v", offer.Candidates)
	}
}

func TestCoordinatorLoopbackLANCandidateReachesAuthorizedOffer(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCandidates(b, "127.0.0.1")
	coordinatorCandidates(c, "127.0.0.1")
	s := coordinatorPrepare(t, b, c)
	coordinatorReady(b, s)
	coordinatorReady(c, s)
	for _, p := range []*coordinatorClient{b, c} {
		offer := decodeCoordinatorBody[proto.SessionOffer](t, p.want(proto.ControlTypeSessionOffer))
		if len(offer.Candidates) != 1 || offer.Candidates[0].Address != "127.0.0.1" || offer.Candidates[0].Scope != "lan" || offer.Candidates[0].Port != 45000 {
			t.Fatalf("loopback LAN candidate lost: %+v", offer.Candidates)
		}
		p.barrier()
	}
}

func TestCoordinatorEnrollmentHTTPAndControlCertificateBoundary(t *testing.T) {
	f := newCoordinatorFixture(t)
	b, _ := f.connect("B")
	ca, err := os.ReadFile(f.a.cfg.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("invalid test CA")
	}
	// Enrollment is deliberately usable before a client certificate exists.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: "127.0.0.1"}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get("https://" + f.addr + "/enroll/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("enrollment status = %d", response.StatusCode)
	}
	conn, err := tls.Dial("tcp4", f.addr, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, _ := proto.MarshalControl(proto.ControlTypeClientHello, "", f.hello("B"))
	_ = proto.Write(conn, proto.TypeControl, hello)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := proto.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	env, err := proto.ParseControl(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if decodeCoordinatorBody[proto.ControlError](t, env).Code != "identity_mismatch" {
		t.Fatal("control admitted a client without a certificate")
	}
	b.barrier()
}
