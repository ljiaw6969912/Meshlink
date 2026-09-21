package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

// Presence is not evidence of an authenticated data session. This catches
// normalization inventing a direct path even at final JSON serialization.
func TestMemberPresenceWithoutHandshakeNeverProjectsDirect(t *testing.T) {
	s := newStatusStore("", NodeStatus{NodeID: "B", Mode: "spoke"})
	s.setState("running")
	s.applyRoster(proto.Roster{Nodes: []proto.RosterNode{{NodeID: "C", Mode: "spoke", Status: "online"}}})
	b, err := json.Marshal(s.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var got RuntimeStatus
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	p := findAgentPeerStatus(got, "C")
	if p == nil || p.PathType != "" || (p.PathState != "idle" && p.PathState != "offline_or_unknown") {
		t.Fatalf("unhandshaken presence projected a data path: %s", b)
	}
}

type runtimeDevice struct{ incoming, written chan []byte }

func (d *runtimeDevice) Name() string { return "test-tun" }
func (d *runtimeDevice) MTU() int     { return 1280 }
func (d *runtimeDevice) Close() error { return nil }
func (d *runtimeDevice) ReadPacket(ctx context.Context) ([]byte, error) {
	select {
	case p := <-d.incoming:
		return p, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (d *runtimeDevice) WritePacket(p []byte) error {
	d.written <- append([]byte(nil), p...)
	return nil
}

type runtimeFixture struct {
	r       map[string]*peerRuntime
	devices map[string]*runtimeDevice
	members []proto.Member
}

func newRuntimeFixture(t *testing.T, ids ...string) *runtimeFixture {
	t.Helper()
	return newRuntimeFixtureWithListen(t, "127.0.0.1:0", ids...)
}

func newRuntimeFixtureWithListen(t *testing.T, listen string, ids ...string) *runtimeFixture {
	t.Helper()
	dir := t.TempDir()
	if _, err := certutil.InitCA(certutil.CAOptions{OutDir: dir}); err != nil {
		t.Fatal(err)
	}
	f := &runtimeFixture{r: map[string]*peerRuntime{}, devices: map[string]*runtimeDevice{}}
	for i, id := range ids {
		if _, err := certutil.Issue(certutil.IssueOptions{OutDir: dir, Name: id, CAPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca-key.pem"), IPAddrs: []string{"127.0.0.1"}}); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{Mode: "spoke", NodeID: id, VirtualIP: fmt.Sprintf("10.77.0.%d", i+2), MTU: 1280, CAFile: filepath.Join(dir, "ca.pem"), CertFile: filepath.Join(dir, id+".pem"), KeyFile: filepath.Join(dir, id+"-key.pem"), P2P: config.P2PConfig{Listen: listen}}
		d := &runtimeDevice{incoming: make(chan []byte, 150), written: make(chan []byte, 150)}
		a, err := New(cfg, discardLogger(), WithDevice(d), withTestDeviceMAC(cfg.NodeID))
		if err != nil {
			t.Fatal(err)
		}
		r, err := newPeerRuntime(a)
		if err != nil {
			t.Fatal(err)
		}
		f.r[id], f.devices[id] = r, d
		t.Cleanup(func() {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		})
		_, fp := certInfoFromFile(cfg.CertFile)
		f.members = append(f.members, proto.Member{NodeID: id, VirtualIP: cfg.VirtualIP, Fingerprint: fp, Status: "online"})
	}
	for _, r := range f.r {
		if err := r.applyMembers(proto.MemberSnapshot{Revision: 1, Members: f.members}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func runtimePacket(src, dst byte) []byte {
	return []byte{0x45, 0, 0, 20, 0, 1, 0, 0, 64, 17, 0, 0, 10, 77, 0, src, 10, 77, 0, dst}
}
func runtimeEventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("runtime condition timed out")
}

type runtimeControl struct {
	client *ControlClient
	conn   net.Conn
	frames chan proto.ControlEnvelope
	done   chan error
	cancel context.CancelFunc
}

func attachRuntimeControl(t *testing.T, r *peerRuntime) *runtimeControl {
	t.Helper()
	local, remote := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	c := &runtimeControl{client: r.control, conn: remote, frames: make(chan proto.ControlEnvelope, 200), done: make(chan error, 1), cancel: cancel}
	go func() {
		for {
			frame, err := proto.Read(remote)
			if err != nil {
				return
			}
			if frame.Type != proto.TypeControl {
				c.done <- fmt.Errorf("data frame leaked onto control: %d", frame.Type)
				return
			}
			env, err := proto.ParseControl(frame.Payload)
			if err != nil {
				c.done <- err
				return
			}
			c.frames <- env
		}
	}()
	go func() { c.done <- c.client.serve(ctx, local) }()
	for _, typ := range []string{proto.ControlTypeClientHello, proto.ControlTypeCandidateUpdate, proto.ControlTypeActiveSessions} {
		c.want(t, typ)
	}
	c.send(t, proto.ControlTypeServerHello, proto.ServerHello{ProtocolVersion: 2, NetworkCIDR: "10.77.0.0/24", Capabilities: []string{"quic_udp_v1"}})
	runtimeEventually(t, func() bool { return r.a.status.snapshot().CoordinatorState == "connected" })
	t.Cleanup(func() { cancel(); remote.Close() })
	return c
}
func (c *runtimeControl) send(t *testing.T, typ string, body any) {
	t.Helper()
	payload, err := proto.MarshalControl(typ, "test", body)
	if err != nil {
		t.Fatal(err)
	}
	c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := proto.Write(c.conn, proto.TypeControl, payload); err != nil {
		t.Fatal(err)
	}
}
func (c *runtimeControl) want(t *testing.T, typ string) proto.ControlEnvelope {
	t.Helper()
	select {
	case env := <-c.frames:
		if env.Type != typ {
			t.Fatalf("frame %s, want %s", env.Type, typ)
		}
		return env
	case err := <-c.done:
		t.Fatalf("control stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("missing %s", typ)
	}
	return proto.ControlEnvelope{}
}
func (c *runtimeControl) stop(t *testing.T) {
	t.Helper()
	c.cancel()
	c.conn.Close()
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		t.Fatal("control shutdown deadlocked")
	}
}
func connectRuntimePair(t *testing.T, f *runtimeFixture, b, c string, bc, cc *runtimeControl) {
	t.Helper()
	until := time.Now().Add(time.Minute)
	id := "session-" + b + c
	for _, entry := range []struct {
		local, peer string
		control     *runtimeControl
	}{{b, c, bc}, {c, b, cc}} {
		r := f.r[entry.peer]
		m, _ := f.r[entry.local].member(entry.peer)
		offer := proto.SessionOffer{SessionID: id, Generation: 1, PeerNodeID: entry.peer, PeerFingerprint: m.Fingerprint, DialerNodeID: b, PairingKey: strings.Repeat("42", 32), ExpiresAt: until, Candidates: []proto.Candidate{{Address: "127.0.0.1", Port: uint16(r.candidates.LocalAddr().Port), Scope: "lan", ExpiresAt: until}}}
		entry.control.send(t, proto.ControlTypeSessionOffer, offer)
		ack, err := proto.DecodeControlBody[proto.SessionOfferAck](entry.control.want(t, proto.ControlTypeSessionOfferAck))
		if err != nil || !ack.Ready {
			t.Fatalf("ack: %+v %v", ack, err)
		}
	}
	cc.send(t, proto.ControlTypeSessionStart, proto.SessionStart{SessionID: id, Generation: 1})
	bc.send(t, proto.ControlTypeSessionStart, proto.SessionStart{SessionID: id, Generation: 1})
	runtimeEventually(t, func() bool {
		s, _ := f.r[b].sessions.Snapshot(c)
		other, _ := f.r[c].sessions.Snapshot(b)
		return s.State == p2p.PathStateLANDirect && other.State == p2p.PathStateLANDirect
	})
	for _, control := range []*runtimeControl{bc, cc} {
		result, err := proto.DecodeControlBody[proto.SessionResult](control.want(t, proto.ControlTypeSessionResult))
		if err != nil || !result.Success {
			t.Fatalf("result %+v %v", result, err)
		}
	}
}
func assertRuntimeDelivery(t *testing.T, d *runtimeDevice, want []byte) {
	t.Helper()
	select {
	case got := <-d.written:
		if !bytes.Equal(got, want) {
			t.Fatalf("packet %x want %x", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("literal datagram was not delivered")
	}
}

func TestControlDisconnectDoesNotCancelReadySessionContext(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	b.stop(t)
	c.stop(t)
	packet := runtimePacket(2, 3)
	if err := f.r["B"].routePacket(packet); err != nil {
		t.Fatal(err)
	}
	assertRuntimeDelivery(t, f.devices["C"], packet)
	s, _ := f.r["B"].sessions.Snapshot("C")
	if s.State != p2p.PathStateLANDirect {
		t.Fatalf("session canceled: %+v", s)
	}
	p := findAgentPeerStatus(f.r["B"].a.status.snapshot(), "C")
	if p == nil || p.Status != "online" || p.LastHeartbeat.IsZero() {
		t.Fatalf("healthy status lost: %+v", p)
	}
}
func TestFirstTunPacketTriggersOneConnectRequestAndBoundedQueue(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.r["B"].readDevice(ctx) }()
	for i := 0; i < 100; i++ {
		f.devices["B"].incoming <- runtimePacket(2, 3)
	}
	b.want(t, proto.ControlTypeConnectRequest)
	runtimeEventually(t, func() bool { s, _ := f.r["B"].sessions.Snapshot("C"); return s.PendingDropped >= 36 })
	s, _ := f.r["B"].sessions.Snapshot("C")
	if s.PendingPackets > 64 || s.PendingBytes > 256*1024 {
		t.Fatalf("unbounded: %+v", s)
	}
	select {
	case env := <-b.frames:
		t.Fatalf("duplicate request %s", env.Type)
	default:
	}
	b.stop(t)
	runtimeEventually(t, func() bool { s, _ := f.r["B"].sessions.Snapshot("C"); return s.PendingPackets == 0 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TUN reader did not exit")
	}
}
func TestDirectFailureTransitionsByCoordinatorAvailability(t *testing.T) {
	for _, online := range []bool{true, false} {
		t.Run(fmt.Sprint(online), func(t *testing.T) {
			f := newRuntimeFixture(t, "B", "C")
			b := attachRuntimeControl(t, f.r["B"])
			c := attachRuntimeControl(t, f.r["C"])
			connectRuntimePair(t, f, "B", "C", b, c)
			if !online {
				b.stop(t)
			}
			if err := f.r["C"].sessions.ClosePeer("B", "direct_unreachable_no_relay"); err != nil {
				t.Fatal(err)
			}
			want := p2p.PathStateWaitingCoordinator
			if online {
				want = p2p.PathStateReconnecting
			}
			runtimeEventually(t, func() bool { s, _ := f.r["B"].sessions.Snapshot("C"); return s.State == want })
			if online {
				b.want(t, proto.ControlTypeConnectRequest)
			} else {
				select {
				case env := <-b.frames:
					t.Fatalf("offline request: %s", env.Type)
				default:
				}
			}
		})
	}
}
func TestControlReconnectReportsActiveAndRequestsWaitingPairs(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	b := attachRuntimeControl(t, f.r["B"])
	d := attachRuntimeControl(t, f.r["D"])
	connectRuntimePair(t, f, "B", "D", b, d)
	b.stop(t)
	if err := f.r["B"].routePacket(runtimePacket(2, 3)); err != nil {
		t.Fatal(err)
	}
	local, remote := net.Pipe()
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.r["B"].control.serve(ctx, local) }()
	for _, typ := range []string{proto.ControlTypeClientHello, proto.ControlTypeCandidateUpdate, proto.ControlTypeActiveSessions, proto.ControlTypeConnectRequest} {
		remote.SetReadDeadline(time.Now().Add(3 * time.Second))
		frame, err := proto.Read(remote)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type != proto.TypeControl {
			t.Fatal("TypePacket control leak")
		}
		env, err := proto.ParseControl(frame.Payload)
		if err != nil || env.Type != typ {
			t.Fatalf("order got %s want %s: %v", env.Type, typ, err)
		}
		if typ == proto.ControlTypeActiveSessions {
			active, _ := proto.DecodeControlBody[proto.ActiveSessions](env)
			if len(active.Sessions) != 1 || active.Sessions[0].PeerNodeID != "D" {
				t.Fatalf("active %+v", active)
			}
			remote.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
			if frame, e := proto.Read(remote); e == nil {
				t.Fatalf("request before validated ServerHello: %d", frame.Type)
			}
			remote.SetReadDeadline(time.Time{})
			payload, _ := proto.MarshalControl(proto.ControlTypeServerHello, "", proto.ServerHello{ProtocolVersion: 2, NetworkCIDR: "10.77.0.0/24", Capabilities: []string{"quic_udp_v1"}})
			remote.SetWriteDeadline(time.Now().Add(time.Second))
			if err := proto.Write(remote, proto.TypeControl, payload); err != nil {
				t.Fatal(err)
			}
		}
		if typ == proto.ControlTypeConnectRequest {
			request, _ := proto.DecodeControlBody[proto.ConnectRequest](env)
			if request.TargetNodeID != "C" {
				t.Fatalf("request %+v", request)
			}
		}
	}
	cancel()
	remote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconnect shutdown hung")
	}
}
func TestInboundSpoofNeverReachesDevice(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	b := attachRuntimeControl(t, f.r["B"])
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	if err := f.r["C"].sessions.Send("B", runtimePacket(4, 2)); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { s, _ := f.r["B"].sessions.Snapshot("C"); return s.InboundDropped == 1 })
	select {
	case p := <-f.devices["B"].written:
		t.Fatalf("spoof reached device: %x", p)
	default:
	}
}
func TestMemberSnapshotAtomicConflictAndIdentityRevocation(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	b := attachRuntimeControl(t, f.r["B"])
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	before, _ := f.r["B"].member("C")
	bad := append([]proto.Member(nil), f.members...)
	bad[2].VirtualIP = bad[1].VirtualIP
	bad[1].Fingerprint = strings.Repeat("99", 32)
	if err := f.r["B"].applyMembers(proto.MemberSnapshot{Revision: 2, Members: bad}); err == nil {
		t.Fatal("route conflict accepted")
	}
	old, _ := f.r["B"].member("C")
	if old.Fingerprint != before.Fingerprint {
		t.Fatal("failed snapshot changed authorization")
	}
	changed := append([]proto.Member(nil), f.members...)
	changed[1].Fingerprint = strings.Repeat("99", 32)
	if err := f.r["B"].applyMembers(proto.MemberSnapshot{Revision: 4, Members: changed}); err != nil {
		t.Fatal(err)
	}
	s, _ := f.r["B"].sessions.Snapshot("C")
	if s.State != p2p.PathStateRequesting || s.SessionID != "" || s.PathType != "" {
		t.Fatalf("changed fingerprint must discard the old session and request a new one: %+v", s)
	}
	runtimeEventually(t, func() bool {
		peer, _ := f.r["C"].sessions.Snapshot("B")
		return peer.State != p2p.PathStateLANDirect && peer.State != p2p.PathStatePublicDirect
	})
}

func TestMemberSnapshotAbsencePreservesReadyDirectAuthorization(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	if err := f.r["B"].applyMembers(proto.MemberSnapshot{Revision: 3, Members: []proto.Member{f.members[0]}}); err != nil {
		t.Fatal(err)
	}
	packet := runtimePacket(2, 3)
	if err := f.r["B"].routePacket(packet); err != nil {
		t.Fatal(err)
	}
	assertRuntimeDelivery(t, f.devices["C"], packet)
	encoded, _ := json.Marshal(f.r["B"].a.status.snapshot())
	var status RuntimeStatus
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	p := findAgentPeerStatus(status, "C")
	if p == nil || p.Session == nil || p.Session.State != p2p.PathStateLANDirect || p.PathType != "lan_direct" || p.Status != PeerStatusOnline {
		t.Fatalf("snapshot absence lost real direct in final JSON: %s", encoded)
	}
}

func assertPendingControlPair(t *testing.T, r *peerRuntime, peer, requestID string, state p2p.PathState, retained bool) {
	t.Helper()
	snapshot, _ := r.sessions.Snapshot(peer)
	encoded, _ := json.Marshal(r.a.status.snapshot())
	var status RuntimeStatus
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	p := findAgentPeerStatus(status, peer)
	r.control.mu.Lock()
	mapped, ok := r.control.requests[requestID]
	r.control.mu.Unlock()
	if snapshot.State != state || p == nil || p.Session == nil || p.Session.State != state || p.Status != string(state) || ok != retained || (ok && mapped != peer) {
		t.Errorf("pair %s lost isolation: manager=%s mapped=%s retained=%v want=%s/%v JSON=%s", peer, snapshot.State, mapped, ok, state, retained, encoded)
	}
}

func TestControlPreOfferAbortRequiresLocalPrepareTuple(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	r := f.r["B"]
	b := attachRuntimeControl(t, r)
	if err := r.routePacket(runtimePacket(2, 3)); err != nil {
		t.Fatal(err)
	}
	local := b.want(t, proto.ControlTypeConnectRequest)
	if err := r.routePacket(runtimePacket(2, 4)); err != nil {
		t.Fatal(err)
	}
	other := b.want(t, proto.ControlTypeConnectRequest)
	send := func(typ string, body any) {
		t.Helper()
		payload, err := proto.MarshalControl(typ, local.RequestID, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := proto.Write(b.conn, proto.TypeControl, payload); err != nil {
			t.Fatal(err)
		}
	}
	prepare := func(peer, session string, generation uint64) {
		t.Helper()
		send(proto.ControlTypeConnectPrepare, proto.ConnectPrepare{PeerNodeID: peer, SessionID: session, Generation: generation})
		b.want(t, proto.ControlTypeCandidateUpdate)
		b.want(t, proto.ControlTypeConnectReady)
	}
	abort := func(session string, generation uint64) {
		t.Helper()
		send(proto.ControlTypeSessionAbort, proto.SessionAbort{SessionID: session, Generation: generation, Code: "prepare_timeout"})
		b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "tuple-barrier"})
		b.want(t, proto.ControlTypePong)
	}
	// D initiated D->B using its own request-1, colliding with B->C.
	prepare("D", "remote-origin", 1)
	abort("remote-origin", 1)
	assertPendingControlPair(t, r, "C", local.RequestID, p2p.PathStateRequesting, true)
	assertPendingControlPair(t, r, "D", other.RequestID, p2p.PathStateRequesting, true)
	prepare("C", "local-origin", 2)
	// A remote collision must not replace an already bound local tuple.
	prepare("D", "remote-origin", 1)
	abort("remote-origin", 1)
	abort("local-origin", 1)
	assertPendingControlPair(t, r, "C", local.RequestID, p2p.PathStateRequesting, true)
	abort("local-origin", 2)
	assertPendingControlPair(t, r, "C", local.RequestID, p2p.PathStateFailed, false)
	assertPendingControlPair(t, r, "D", other.RequestID, p2p.PathStateRequesting, true)
	snapshot, _ := r.sessions.Snapshot("C")
	if snapshot.ErrorCode != "candidate_unavailable" {
		t.Fatalf("matching abort error=%s", snapshot.ErrorCode)
	}
}

func TestControlCapacityErrorOnlyConcludesScopedRequest(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	r := f.r["B"]
	b := attachRuntimeControl(t, r)
	if err := r.routePacket(runtimePacket(2, 3)); err != nil {
		t.Fatal(err)
	}
	c := b.want(t, proto.ControlTypeConnectRequest)
	if err := r.routePacket(runtimePacket(2, 4)); err != nil {
		t.Fatal(err)
	}
	d := b.want(t, proto.ControlTypeConnectRequest)
	sendError := func(id string) {
		t.Helper()
		payload, _ := proto.MarshalControl(proto.ControlTypeError, id, proto.ControlError{Code: "coordinator_capacity"})
		if err := proto.Write(b.conn, proto.TypeControl, payload); err != nil {
			t.Fatal(err)
		}
		b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "capacity-barrier"})
		b.want(t, proto.ControlTypePong)
	}
	sendError("")
	assertPendingControlPair(t, r, "C", c.RequestID, p2p.PathStateRequesting, true)
	assertPendingControlPair(t, r, "D", d.RequestID, p2p.PathStateRequesting, true)
	sendError(c.RequestID)
	assertPendingControlPair(t, r, "C", c.RequestID, p2p.PathStateFailed, false)
	assertPendingControlPair(t, r, "D", d.RequestID, p2p.PathStateRequesting, true)
	snapshot, _ := r.sessions.Snapshot("C")
	if snapshot.ErrorCode != "session_authorization_failed" {
		t.Fatalf("capacity public error=%s", snapshot.ErrorCode)
	}
}

func TestControlPrepareTuplesShareCorrelationBoundAndLifetime(t *testing.T) {
	f := newRuntimeFixture(t, "B")
	b := attachRuntimeControl(t, f.r["B"])
	var oldest, latest string
	for i := 0; i < 600; i++ {
		peer := fmt.Sprintf("pending-%d", i)
		b.client.requestSession(peer)
		request := b.want(t, proto.ControlTypeConnectRequest)
		prepare, _ := proto.MarshalControl(proto.ControlTypeConnectPrepare, request.RequestID, proto.ConnectPrepare{PeerNodeID: peer, SessionID: "session-" + request.RequestID, Generation: 1})
		if err := proto.Write(b.conn, proto.TypeControl, prepare); err != nil {
			t.Fatal(err)
		}
		// These unrecognized peers cannot become ready/direct; only the framed
		// correlation lifecycle is under test, not fabricated membership.
		ready, err := proto.DecodeControlBody[proto.ConnectReady](b.want(t, proto.ControlTypeConnectReady))
		if err != nil || ready.Ready {
			t.Fatalf("unrecognized peer became ready: %+v %v", ready, err)
		}
		if i == 0 {
			oldest = request.RequestID
		}
		latest = request.RequestID
	}
	b.client.mu.Lock()
	count := len(b.client.prepares)
	_, retainedOldest := b.client.prepares[oldest]
	latestTuple := b.client.prepares[latest]
	consistent := count == len(b.client.requests) && count == b.client.requestOrder.Len() && count == len(b.client.requestPeers)
	b.client.mu.Unlock()
	if count != 256 || retainedOldest || !consistent || latestTuple.PeerNodeID != "pending-599" || latestTuple.SessionID != "session-"+latest || latestTuple.Generation != 1 {
		t.Fatalf("prepare tuple bound/index mismatch: count=%d oldest=%v consistent=%v latest=%+v", count, retainedOldest, consistent, latestTuple)
	}
	b.client.requestSession("pending-599")
	b.want(t, proto.ControlTypeConnectRequest)
	b.client.mu.Lock()
	_, replaced := b.client.prepares[latest]
	b.client.mu.Unlock()
	if replaced {
		t.Fatal("retry retained previous prepare tuple")
	}
	b.client.forgetRequest("pending-598")
	b.client.mu.Lock()
	count = len(b.client.prepares)
	b.client.mu.Unlock()
	if count != 254 {
		t.Fatalf("peer cleanup retained tuple: %d", count)
	}
	b.stop(t)
	b.client.mu.Lock()
	count = len(b.client.prepares) + len(b.client.requests) + b.client.requestOrder.Len() + len(b.client.requestPeers)
	b.client.mu.Unlock()
	if count != 0 {
		t.Fatalf("control teardown retained correlation state: %d", count)
	}
}

func TestPeerPacketBurstCoalescesDiskStatusAndKeepsMemoryCurrent(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	r := f.r["B"]
	b := attachRuntimeControl(t, r)
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	path := filepath.Join(t.TempDir(), "runtime.json")
	var diskWrites atomic.Int32
	r.a.status.mu.Lock()
	write := r.a.status.writeFile
	r.a.status.writeFile = func(path string, data []byte) error { diskWrites.Add(1); return write(path, data) }
	r.a.status.path = path
	_ = r.a.status.writeLocked()
	r.a.status.mu.Unlock()
	readBytes := func() uint64 {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0
		}
		var status RuntimeStatus
		_ = json.Unmarshal(data, &status)
		p := findAgentPeerStatus(status, "C")
		if p == nil || p.Session == nil {
			return 0
		}
		return p.Session.BytesSent
	}
	var previous uint64
	writes := 0
	for i := 1; i <= 40; i++ {
		packet := runtimePacket(2, 3)
		if err := r.routePacket(packet); err != nil {
			t.Fatal(err)
		}
		assertRuntimeDelivery(t, f.devices["C"], packet)
		runtimeEventually(t, func() bool {
			p := findAgentPeerStatus(r.a.status.snapshot(), "C")
			return p != nil && p.Session != nil && p.Session.BytesSent == uint64(i*len(packet))
		})
		if current := readBytes(); current != previous {
			writes++
			previous = current
		}
	}
	if writes > 3 {
		t.Errorf("40 data packets caused %d distinct disk counter publications; want coalesced cadence", writes)
	}
	if count := diskWrites.Load(); count > 3 {
		t.Errorf("40 data packets caused %d file write/rename operations; want coalesced cadence", count)
	}
	if err := r.sessions.ClosePeer("C", "peer_revoked"); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var status RuntimeStatus
		_ = json.Unmarshal(data, &status)
		p := findAgentPeerStatus(status, "C")
		return p != nil && p.Session != nil && p.Session.State == p2p.PathStateClosed
	})
}

func TestBlockedPersistenceCannotStallPeerNotificationsOrOverwriteFinalStatus(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	r := f.r["B"]
	b := attachRuntimeControl(t, r)
	path := filepath.Join(t.TempDir(), "runtime.json")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.a.status.mu.Lock()
	original := r.a.status.writeFile
	r.a.status.path = path
	r.a.status.writeFile = func(path string, data []byte) error {
		once.Do(func() { close(entered); <-release })
		return original(path, data)
	}
	r.a.status.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- r.routePacket(runtimePacket(2, 3)) }()
	<-entered
	other := make(chan error, 1)
	go func() { other <- r.routePacket(runtimePacket(2, 4)) }()
	select {
	case err := <-other:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("blocked disk persistence stalled unrelated peer notification")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b.want(t, proto.ControlTypeConnectRequest)
	b.want(t, proto.ControlTypeConnectRequest)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.a.status.terminate(); err != nil {
		t.Fatal(err)
	}
	r.sessionChanged(p2p.SessionSnapshot{PeerNodeID: "C"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var status RuntimeStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "stopped" {
		t.Fatalf("late persistence overwrote terminal state: %s", data)
	}
}

func TestControlRequestCorrelationsBoundedAndCleaned(t *testing.T) {
	t.Run("churn_bound_and_active_lookup", func(t *testing.T) {
		f := newRuntimeFixture(t, "B", "C")
		r := f.r["B"]
		b := attachRuntimeControl(t, r)
		var first string
		for i := 0; i < 600; i++ {
			r.control.requestSession(fmt.Sprintf("churn-%d", i))
			env := b.want(t, proto.ControlTypeConnectRequest)
			if i == 0 {
				first = env.RequestID
			}
		}
		r.control.mu.Lock()
		count := len(r.control.requests)
		peerCount, orderCount := len(r.control.requestPeers), r.control.requestOrder.Len()
		_, oldest := r.control.requests[first]
		r.control.mu.Unlock()
		if count > 256 || oldest || peerCount != count || orderCount != count {
			t.Errorf("correlation churn retained %d entries (peers=%d order=%d), oldest=%v; want <=256 with FIFO eviction", count, peerCount, orderCount, oldest)
		}
		if err := r.routePacket(runtimePacket(2, 3)); err != nil {
			t.Fatal(err)
		}
		request := b.want(t, proto.ControlTypeConnectRequest)
		// A retry replaces both indexes; a late response to the previous ID
		// must not fail the currently outstanding request.
		r.control.requestSession("C")
		retry := b.want(t, proto.ControlTypeConnectRequest)
		stale, _ := proto.MarshalControl(proto.ControlTypeError, request.RequestID, proto.ControlError{Code: "peer_unavailable"})
		if err := proto.Write(b.conn, proto.TypeControl, stale); err != nil {
			t.Fatal(err)
		}
		b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "stale-correlation-barrier"})
		b.want(t, proto.ControlTypePong)
		pending, _ := r.sessions.Snapshot("C")
		if pending.State != p2p.PathStateRequesting {
			t.Fatalf("old correlation failed active retry: %+v", pending)
		}
		request = retry
		payload, _ := proto.MarshalControl(proto.ControlTypeError, request.RequestID, proto.ControlError{Code: "peer_unavailable"})
		if err := proto.Write(b.conn, proto.TypeControl, payload); err != nil {
			t.Fatal(err)
		}
		b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "correlation-barrier"})
		b.want(t, proto.ControlTypePong)
		s, _ := r.sessions.Snapshot("C")
		if s.State != p2p.PathStateFailed || s.ErrorCode != "peer_offline" {
			t.Fatalf("active request lookup failed: %+v", s)
		}
		r.control.mu.Lock()
		_, retained := r.control.requests[request.RequestID]
		r.control.mu.Unlock()
		if retained {
			t.Fatal("completed failure retained correlation")
		}
	})
	t.Run("successful_session", func(t *testing.T) {
		f := newRuntimeFixture(t, "B", "C")
		b := attachRuntimeControl(t, f.r["B"])
		c := attachRuntimeControl(t, f.r["C"])
		if err := f.r["B"].routePacket(runtimePacket(2, 3)); err != nil {
			t.Fatal(err)
		}
		b.want(t, proto.ControlTypeConnectRequest)
		connectRuntimePair(t, f, "B", "C", b, c)
		b.client.mu.Lock()
		count := len(b.client.requests)
		b.client.mu.Unlock()
		if count != 0 {
			t.Fatalf("successful negotiation retained %d correlations", count)
		}
	})
	t.Run("explicit_removal", func(t *testing.T) {
		f := newRuntimeFixture(t, "B", "C")
		b := attachRuntimeControl(t, f.r["B"])
		if err := f.r["B"].routePacket(runtimePacket(2, 3)); err != nil {
			t.Fatal(err)
		}
		b.want(t, proto.ControlTypeConnectRequest)
		if err := f.r["B"].disconnectPeer(proto.DisconnectPeer{NodeID: "C"}); err != nil {
			t.Fatal(err)
		}
		b.client.mu.Lock()
		count := len(b.client.requests)
		b.client.mu.Unlock()
		if count != 0 {
			t.Fatalf("explicit removal retained %d correlations", count)
		}
	})
}

func TestMemberRefreshPreservesSessionStateAndRetriesOnlineFailure(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	r := f.r["B"]
	b := attachRuntimeControl(t, r)
	if err := r.routePacket(runtimePacket(2, 3)); err != nil {
		t.Fatal(err)
	}
	request := b.want(t, proto.ControlTypeConnectRequest)
	assertState := func(want p2p.PathState) {
		t.Helper()
		encoded, _ := json.Marshal(r.a.status.snapshot())
		var status RuntimeStatus
		if err := json.Unmarshal(encoded, &status); err != nil {
			t.Fatal(err)
		}
		p := findAgentPeerStatus(status, "C")
		if p == nil || p.Session == nil || p.Session.State != want || p.PathState != want || p.Status != string(want) {
			t.Errorf("membership replaced manager-owned %s status in final JSON: %s", want, encoded)
		}
	}
	if err := r.applyMembers(proto.MemberSnapshot{Revision: 2, Members: f.members}); err != nil {
		t.Fatal(err)
	}
	assertState(p2p.PathStateRequesting)
	payload, _ := proto.MarshalControl(proto.ControlTypeError, request.RequestID, proto.ControlError{Code: "peer_unavailable"})
	if err := proto.Write(b.conn, proto.TypeControl, payload); err != nil {
		t.Fatal(err)
	}
	b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "failed-barrier"})
	b.want(t, proto.ControlTypePong)
	assertState(p2p.PathStateFailed)
	if err := r.applyMembers(proto.MemberSnapshot{Revision: 3, Members: []proto.Member{f.members[0]}}); err != nil {
		t.Fatal(err)
	}
	assertState(p2p.PathStateFailed)
	if err := r.applyMembers(proto.MemberSnapshot{Revision: 4, Members: f.members}); err != nil {
		t.Fatal(err)
	}
	b.want(t, proto.ControlTypeConnectRequest)
	assertState(p2p.PathStateRequesting)
}

// Holding the status lock creates the exact split-publication window. No
// callback or route may see the new member until its status row can exist.
func TestMemberPublicationWaitsForStatusBeforeExposingRoute(t *testing.T) {
	f := newRuntimeFixture(t, "B")
	r := f.r["B"]
	m := proto.Member{NodeID: "C", VirtualIP: "10.77.0.3", Fingerprint: strings.Repeat("42", 32), Status: "online"}
	r.a.status.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- r.applyMembers(proto.MemberSnapshot{Revision: 2, Members: []proto.Member{m}})
	}()
	<-started
	exposed := false
	deadline := time.After(100 * time.Millisecond)
check:
	for {
		if _, ok := r.member("C"); ok {
			exposed = true
			break
		}
		select {
		case <-deadline:
			break check
		default:
		}
	}
	r.a.status.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if exposed {
		t.Error("authorization/route exposed while status publication was blocked")
	}
	if err := r.routePacket(runtimePacket(2, 3)); err != nil {
		t.Fatal(err)
	}
	p := findAgentPeerStatus(r.a.status.snapshot(), "C")
	if p == nil || p.Session == nil || p.Status != string(p2p.PathStateWaitingCoordinator) {
		t.Fatalf("first routed packet lost status callback: %+v", p)
	}
}

func TestSessionCallbackReconcilesRevocationAfterWaitingForStatus(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	r := f.r["B"]
	b := attachRuntimeControl(t, r)
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	r.a.status.mu.Lock()
	marker := r.a.status.peers["C"]
	marker.Status = "interleaving_marker"
	r.a.status.peers["C"] = marker
	callbackDone := make(chan struct{})
	go func() { r.sessionChanged(p2p.SessionSnapshot{PeerNodeID: "C"}); close(callbackDone) }()
	runtimeEventually(t, func() bool {
		if r.eventMu.TryLock() {
			r.eventMu.Unlock()
			return false
		}
		return true
	})
	// Both implementations now have a callback waiting on status. ClosePeer
	// changes the real manager state before its own callback waits on eventMu.
	closed := make(chan error, 1)
	go func() { closed <- r.sessions.ClosePeer("C", "peer_revoked") }()
	runtimeEventually(t, func() bool { s, _ := r.sessions.Snapshot("C"); return s.State == p2p.PathStateClosed })
	r.control.mu.Lock() // Stop result reporting so the later callback cannot mask a stale projection.
	r.a.status.mu.Unlock()
	var p PeerStatus
	runtimeEventually(t, func() bool {
		r.a.status.mu.Lock()
		p = r.a.status.peers["C"]
		r.a.status.mu.Unlock()
		return p.Status != "interleaving_marker"
	})
	r.control.mu.Unlock()
	<-callbackDone
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if p.Session == nil || p.Session.State != p2p.PathStateClosed {
		t.Fatalf("callback projected pre-revocation snapshot: %+v", p.Session)
	}
}

func TestPublishedRevocationRejectsStillActiveSessionCallback(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	r := f.r["B"]
	b := attachRuntimeControl(t, r)
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	// Stop at the production publication boundary, before the outer operation
	// invokes ClosePeer. A real direct manager snapshot still exists here.
	changed, err := r.publishMembership(proto.MemberSnapshot{Revision: 2}, []string{"C"}, false)
	if err != nil || len(changed) != 1 || changed[0] != "C" {
		t.Fatalf("publication: %v %v", changed, err)
	}
	active, _ := r.sessions.Snapshot("C")
	if active.State != p2p.PathStateLANDirect {
		t.Fatalf("test requires a still-active real session: %+v", active)
	}
	r.sessionChanged(active)
	p := findAgentPeerStatus(r.a.status.snapshot(), "C")
	if p == nil || p.Session != nil || p.PathType != "" || p.Status == PeerStatusOnline {
		t.Fatalf("revoked session callback restored direct: %+v", p)
	}
	if _, ok := r.member("C"); ok {
		t.Fatal("revoked authorization remained visible")
	}
	if err := r.routePacket(runtimePacket(2, 3)); !errors.Is(err, p2p.ErrSessionNotReady) {
		t.Fatalf("revoked route: %v", err)
	}
	if err := r.sessions.ClosePeer("C", "peer_revoked"); err != nil {
		t.Fatal(err)
	}
	p = findAgentPeerStatus(r.a.status.snapshot(), "C")
	if p.Session == nil || p.Session.State != p2p.PathStateClosed {
		t.Fatalf("real close did not reconcile: %+v", p)
	}
}

func TestControlRecoverablePairErrorProjectsRealSessionFailure(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	if err := f.r["B"].routePacket(runtimePacket(2, 3)); err != nil {
		t.Fatal(err)
	}
	request := b.want(t, proto.ControlTypeConnectRequest)
	payload, _ := proto.MarshalControl(proto.ControlTypeError, request.RequestID, proto.ControlError{Code: "peer_unavailable"})
	if err := proto.Write(b.conn, proto.TypeControl, payload); err != nil {
		t.Fatal(err)
	}
	b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "pair-error-barrier"})
	b.want(t, proto.ControlTypePong)
	s, _ := f.r["B"].sessions.Snapshot("C")
	if s.ErrorCode != "peer_offline" || s.State != p2p.PathStateFailed {
		t.Fatalf("recoverable error lost in real SessionManager: %+v", s)
	}
	if !b.client.Available() {
		t.Fatal("pair error killed control")
	}
	encoded, _ := json.Marshal(f.r["B"].a.status.snapshot())
	var runtime RuntimeStatus
	_ = json.Unmarshal(encoded, &runtime)
	if p := findAgentPeerStatus(runtime, "C"); p == nil || p.ErrorCode != "peer_offline" {
		t.Fatalf("stable error missing from final runtime JSON: %s", encoded)
	}
}

func TestControlPrepareAbortConcludesCorrelatedRealRequest(t *testing.T) {
	for code, want := range controlPublicErrorCases {
		t.Run(code, func(t *testing.T) {
			f := newRuntimeFixture(t, "B", "C")
			r := f.r["B"]
			b := attachRuntimeControl(t, r)
			if err := r.routePacket(runtimePacket(2, 3)); err != nil {
				t.Fatal(err)
			}
			request := b.want(t, proto.ControlTypeConnectRequest)
			prepare, _ := proto.MarshalControl(proto.ControlTypeConnectPrepare, request.RequestID, proto.ConnectPrepare{SessionID: "pre-offer", Generation: 1, PeerNodeID: "C"})
			if err := proto.Write(b.conn, proto.TypeControl, prepare); err != nil {
				t.Fatal(err)
			}
			b.want(t, proto.ControlTypeCandidateUpdate)
			ready, err := proto.DecodeControlBody[proto.ConnectReady](b.want(t, proto.ControlTypeConnectReady))
			if err != nil || !ready.Ready {
				t.Fatalf("prepare: %+v %v", ready, err)
			}
			abort, _ := proto.MarshalControl(proto.ControlTypeSessionAbort, request.RequestID, proto.SessionAbort{SessionID: "pre-offer", Generation: 1, Code: code})
			if err := proto.Write(b.conn, proto.TypeControl, abort); err != nil {
				t.Fatal(err)
			}
			b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "pre-offer-abort-barrier"})
			b.want(t, proto.ControlTypePong)
			snapshot, _ := r.sessions.Snapshot("C")
			encoded, _ := json.Marshal(r.a.status.snapshot())
			var status RuntimeStatus
			_ = json.Unmarshal(encoded, &status)
			p := findAgentPeerStatus(status, "C")
			if snapshot.State != p2p.PathStateFailed || snapshot.ErrorCode != want || snapshot.PendingPackets != 1 || p == nil || p.Session == nil || p.Status != "failed" || p.ErrorCode != want {
				t.Fatalf("pre-offer abort did not converge real request/JSON: manager=%+v JSON=%s", snapshot, encoded)
			}
			b.client.mu.Lock()
			retained := len(b.client.requests)
			b.client.mu.Unlock()
			if retained != 0 {
				t.Fatalf("abort retained %d correlations", retained)
			}
			// No initiator correlation and no installed offer is harmless.
			if err := proto.Write(b.conn, proto.TypeControl, abort); err != nil {
				t.Fatal(err)
			}
			b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "uncorrelated-abort-barrier"})
			b.want(t, proto.ControlTypePong)
		})
	}
}

var controlPublicErrorCases = map[string]string{
	"control_replaced":            "peer_offline",
	"control_disconnected":        "peer_offline",
	"member_revoked":              "peer_revoked",
	"peer_not_ready":              "candidate_unavailable",
	"candidate_revision_mismatch": "candidate_unavailable",
	"candidate_unavailable":       "candidate_unavailable",
	"prepare_timeout":             "candidate_unavailable",
	"peer_unavailable":            "peer_offline",
	"identity_mismatch":           "peer_identity_mismatch",
	"offer_rejected":              "session_authorization_failed",
	"ack_timeout":                 "session_authorization_failed",
	"entropy_unavailable":         "session_authorization_failed",
	"coordinator_capacity":        "session_authorization_failed",
	"connection_failed":           "direct_unreachable_no_relay",
	"started_timeout":             "direct_unreachable_no_relay",
	"negotiation_timeout":         "direct_unreachable_no_relay",
	"unknown_internal_reason":     "direct_unreachable_no_relay",
}

func TestControlPublicErrorVocabularyUnchanged(t *testing.T) {
	for _, code := range []string{"control_upgrade_required", "control_unavailable", "peer_offline", "peer_revoked", "route_conflict", "candidate_unavailable", "udp_probe_failed", "hole_punch_timeout", "quic_handshake_failed", "peer_identity_mismatch", "session_authorization_failed", "direct_heartbeat_timeout", "direct_unreachable_no_relay"} {
		if got := publicPeerError(code); got != code {
			t.Errorf("public code %s changed to %s", code, got)
		}
	}
}

func TestControlInstalledAbortUsesPublicErrorCodes(t *testing.T) {
	for code, want := range controlPublicErrorCases {
		t.Run(code, func(t *testing.T) {
			f := newRuntimeFixture(t, "B", "C")
			r := f.r["B"]
			b := attachRuntimeControl(t, r)
			member, _ := r.member("C")
			until := time.Now().Add(time.Minute)
			offer := proto.SessionOffer{SessionID: "public-code", Generation: 1, PeerNodeID: "C", PeerFingerprint: member.Fingerprint, DialerNodeID: "B", PairingKey: strings.Repeat("42", 32), ExpiresAt: until, Candidates: []proto.Candidate{{Address: "127.0.0.1", Port: uint16(f.r["C"].candidates.LocalAddr().Port), Scope: "lan", ExpiresAt: until}}}
			b.send(t, proto.ControlTypeSessionOffer, offer)
			ack, err := proto.DecodeControlBody[proto.SessionOfferAck](b.want(t, proto.ControlTypeSessionOfferAck))
			if err != nil || !ack.Ready {
				t.Fatalf("offer installation: %+v %v", ack, err)
			}
			b.send(t, proto.ControlTypeSessionAbort, proto.SessionAbort{SessionID: offer.SessionID, Generation: 1, Code: code})
			result, err := proto.DecodeControlBody[proto.SessionResult](b.want(t, proto.ControlTypeSessionResult))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _ := r.sessions.Snapshot("C")
			encoded, _ := json.Marshal(r.a.status.snapshot())
			var status RuntimeStatus
			_ = json.Unmarshal(encoded, &status)
			p := findAgentPeerStatus(status, "C")
			if snapshot.ErrorCode != want || result.Code != want || p == nil || p.Session == nil || p.ErrorCode != want || p.Session.ErrorCode != want {
				t.Fatalf("internal abort %q escaped public boundary: want=%s manager=%s result=%s JSON=%s", code, want, snapshot.ErrorCode, result.Code, encoded)
			}
		})
	}
}

type failedRuntimeDevice struct {
	*runtimeDevice
	err error
}

type closeOnlyRuntimeDevice struct {
	*runtimeDevice
	entered, closed chan struct{}
	once            sync.Once
	closes          atomic.Int32
}

func (d *closeOnlyRuntimeDevice) ReadPacket(context.Context) ([]byte, error) {
	close(d.entered)
	<-d.closed
	return nil, errors.New("read interrupted by device close")
}
func (d *closeOnlyRuntimeDevice) Close() error {
	d.closes.Add(1)
	d.once.Do(func() { close(d.closed) })
	return nil
}

func TestSpokeShutdownInterruptsBlockingDeviceExactlyOnce(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprintf("permanent=%v", permanent), func(t *testing.T) {
			f := newRuntimeFixture(t, "B")
			cfg := *f.r["B"].a.cfg
			cfg.Connect = "127.0.0.1:1"
			if permanent {
				cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
				if err != nil {
					t.Fatal(err)
				}
				listener, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				cfg.Connect, cfg.ServerName = listener.Addr().String(), "127.0.0.1"
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					for i := 0; i < 3; i++ {
						if _, err := proto.Read(conn); err != nil {
							return
						}
					}
					payload, _ := proto.MarshalControl(proto.ControlTypeError, "", proto.ControlError{Code: "control_upgrade_required"})
					_ = proto.Write(conn, proto.TypeControl, payload)
				}()
			}
			d := &closeOnlyRuntimeDevice{runtimeDevice: f.devices["B"], entered: make(chan struct{}), closed: make(chan struct{})}
			a, err := New(&cfg, discardLogger(), WithDevice(d), withTestDeviceMAC(cfg.NodeID))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			<-d.entered
			if !permanent {
				cancel()
			}
			var got error
			select {
			case got = <-done:
			case <-time.After(500 * time.Millisecond):
				t.Error("spoke shutdown waited for device read before closing device")
				_ = d.Close() // release the faulty production path so RED cleanup cannot hang.
				got = <-done
			}
			if permanent {
				var compatibility *controlCompatibilityError
				if !errors.As(got, &compatibility) {
					t.Errorf("close-induced read replaced permanent control error: %v", got)
				}
			} else if !errors.Is(got, context.Canceled) {
				t.Errorf("parent cancellation replaced: %v", got)
			}
			if d.closes.Load() != 1 {
				t.Errorf("device closed %d times, want once", d.closes.Load())
			}
		})
	}
}

func (d *failedRuntimeDevice) ReadPacket(context.Context) ([]byte, error) { return nil, d.err }
func TestPeerRuntimeReturnsOriginalDeviceReadFailure(t *testing.T) {
	f := newRuntimeFixture(t, "B")
	want := errors.New("literal-device-read-failure")
	a, err := New(f.r["B"].a.cfg, discardLogger(), WithDevice(&failedRuntimeDevice{runtimeDevice: f.devices["B"], err: want}))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Connect = "127.0.0.1:1"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.runSpoke(ctx); !errors.Is(err, want) {
		t.Fatalf("original TUN error replaced: %v", err)
	}
}

func TestControlInvalidBodyAfterHelloIsPermanent(t *testing.T) {
	f := newRuntimeFixture(t, "B")
	b := attachRuntimeControl(t, f.r["B"])
	payload := []byte(`{"protocol_version":2,"type":"ping","body":{"nonce":"x","forbidden":true}}`)
	if err := proto.Write(b.conn, proto.TypeControl, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-b.done:
		var permanent *controlCompatibilityError
		if !errors.As(err, &permanent) {
			t.Fatalf("malformed authenticated body is reconnectable: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid body did not stop control")
	}
}

func TestControlRejectsPacketHeaderWithoutReadingUserPayload(t *testing.T) {
	f := newRuntimeFixture(t, "B")
	b := attachRuntimeControl(t, f.r["B"])
	header := []byte{'M', 'S', 'H', '1', 1, proto.TypePacket, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[6:], 1000)
	if _, err := b.conn.Write(header); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-b.done:
		var permanent *controlCompatibilityError
		if !errors.As(err, &permanent) {
			t.Fatalf("packet header was not permanent: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client waited for forbidden TypePacket payload")
	}
}

func TestControlFailedStartReportsConvergingResult(t *testing.T) {
	f := newRuntimeFixture(t, "B")
	b := attachRuntimeControl(t, f.r["B"])
	b.send(t, proto.ControlTypeSessionStart, proto.SessionStart{SessionID: "missing-offer", Generation: 7})
	select {
	case env := <-b.frames:
		result, err := proto.DecodeControlBody[proto.SessionResult](env)
		if err != nil || result.Success || result.SessionID != "missing-offer" || result.Code != "session_authorization_failed" {
			t.Fatalf("start failure %+v %v", result, err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed StartOffer silently awaits coordinator timeout")
	}
}

func TestControlStaleMembershipCannotRevokeNewerSnapshot(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	b.send(t, proto.ControlTypeMemberSnapshot, proto.MemberSnapshot{Revision: 8, Members: f.members})
	b.want(t, proto.ControlTypeConnectRequest)
	b.send(t, proto.ControlTypeMemberDelta, proto.MemberDelta{Revision: 7, RemovedNodeIDs: []string{"C"}})
	b.send(t, proto.ControlTypePing, proto.ControlPing{Nonce: "revision-barrier"})
	b.want(t, proto.ControlTypePong)
	if _, ok := f.r["B"].member("C"); !ok {
		t.Fatal("stale removal revoked newer membership")
	}
}

func TestControlSessionResultDedupIsBounded(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	peer, _ := f.r["B"].member("C")
	for i := 1; i <= 260; i++ {
		id := fmt.Sprintf("failed-%d", i)
		offer := proto.SessionOffer{SessionID: id, Generation: uint64(i), PeerNodeID: "C", PeerFingerprint: peer.Fingerprint, DialerNodeID: "B", PairingKey: strings.Repeat("42", 32), ExpiresAt: time.Now().Add(time.Minute), Candidates: []proto.Candidate{{Address: "127.0.0.1", Port: uint16(f.r["C"].candidates.LocalAddr().Port), Scope: "lan", ExpiresAt: time.Now().Add(time.Minute)}}}
		b.send(t, proto.ControlTypeSessionOffer, offer)
		b.want(t, proto.ControlTypeSessionOfferAck)
		b.send(t, proto.ControlTypeSessionAbort, proto.SessionAbort{SessionID: id, Generation: uint64(i), Code: "direct_unreachable_no_relay"})
		b.want(t, proto.ControlTypeSessionResult)
		b.want(t, proto.ControlTypeConnectRequest)
	}
	b.client.mu.Lock()
	count := len(b.client.results)
	b.client.mu.Unlock()
	if count > 256 {
		t.Fatalf("unbounded result history: %d", count)
	}
	f.r["B"].sessionChanged(p2p.SessionSnapshot{PeerNodeID: "C"})
	select {
	case env := <-b.frames:
		t.Fatalf("duplicate result: %s", env.Type)
	default:
	}
}

func TestControlIncompatibilityIsPermanentBeforeAndAfterHello(t *testing.T) {
	for _, kind := range []string{"packet", "legacy", "upgrade", "capability", "network", "noncanonical_network", "version"} {
		t.Run(kind, func(t *testing.T) {
			f := newRuntimeFixture(t, "B")
			local, remote := net.Pipe()
			defer remote.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.r["B"].control.serve(ctx, local) }()
			for i := 0; i < 3; i++ {
				frame, err := proto.Read(remote)
				if err != nil {
					t.Fatal(err)
				}
				if frame.Type != proto.TypeControl {
					t.Fatalf("outgoing legacy frame %d", frame.Type)
				}
			}
			var typ byte = proto.TypeControl
			var payload []byte
			switch kind {
			case "packet":
				typ = proto.TypePacket
				payload = runtimePacket(2, 3)
			case "legacy":
				typ = proto.TypeRoster
				payload = []byte(`{}`)
			case "eof":
				remote.Close()
			case "upgrade":
				payload, _ = proto.MarshalControl(proto.ControlTypeError, "", proto.ControlError{Code: "control_upgrade_required"})
			default:
				hello := proto.ServerHello{ProtocolVersion: 2, NetworkCIDR: "10.77.0.0/24", Capabilities: []string{"quic_udp_v1"}}
				if kind == "capability" {
					hello.Capabilities = nil
				}
				if kind == "network" {
					hello.NetworkCIDR = "0.0.0.0/0"
				}
				if kind == "noncanonical_network" {
					hello.NetworkCIDR = "10.77.0.1/24"
				}
				if kind == "version" {
					hello.ProtocolVersion = 1
				}
				payload, _ = proto.MarshalControl(proto.ControlTypeServerHello, "", hello)
			}
			if kind != "eof" {
				if kind == "packet" || kind == "legacy" {
					// A nonzero declared payload must not be consumed. Sending
					// it with proto.Write would incorrectly require that read.
					var wire bytes.Buffer
					_ = proto.Write(&wire, typ, payload)
					if n, err := remote.Write(wire.Bytes()[:10]); err != nil || n != 10 {
						t.Fatalf("forbidden header write n=%d err=%v", n, err)
					}
				} else if err := proto.Write(remote, typ, payload); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				var permanent *controlCompatibilityError
				if !errors.As(err, &permanent) {
					t.Fatalf("incompatibility not permanent: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("incompatible control kept running")
			}
			if kind == "packet" || kind == "legacy" {
				if n, err := remote.Write(payload); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("forbidden payload was accepted: n=%d err=%v", n, err)
				}
			}
		})
	}
}

func TestControlNetworkCIDRIsCanonicalAndWhollyPrivate(t *testing.T) {
	for _, tc := range []struct {
		prefix, local string
		want          bool
	}{
		{"10.77.0.0/24", "10.77.0.2", true},
		{"172.16.0.0/12", "172.31.255.254", true},
		{"192.168.0.0/16", "192.168.1.2", true},
		{"172.16.0.0/8", "172.16.0.2", false},
		{"192.168.0.0/15", "192.168.1.2", false},
		{"10.77.0.1/24", "10.77.0.2", false},
		{"10.77.0.0/24", "10.78.0.2", false},
	} {
		if got := validPeerNetwork(tc.prefix, netip.MustParseAddr(tc.local)); got != tc.want {
			t.Fatalf("network %s local %s accepted=%v want=%v", tc.prefix, tc.local, got, tc.want)
		}
	}
}

func TestMemberDeltaExplicitRemovalClosesDirect(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	c := attachRuntimeControl(t, f.r["C"])
	connectRuntimePair(t, f, "B", "C", b, c)
	b.send(t, proto.ControlTypeMemberDelta, proto.MemberDelta{Revision: 2, RemovedNodeIDs: []string{"C"}})
	runtimeEventually(t, func() bool { s, _ := f.r["B"].sessions.Snapshot("C"); return s.State == p2p.PathStateClosed })
	if _, ok := f.r["B"].member("C"); ok {
		t.Fatal("explicit removal retained authorization")
	}
	replacement := f.members[1]
	replacement.NodeID = "replacement"
	if err := f.r["B"].applyMembers(proto.MemberSnapshot{Revision: 3, Members: []proto.Member{replacement}}); err != nil {
		t.Fatalf("removed member still reserves its old routes: %v", err)
	}
}
func TestPeerNetworkIDHashesParsedSharedCADER(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	path := f.r["B"].a.cfg.CAFile
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	sum := sha256.Sum256(block.Bytes)
	want := hex.EncodeToString(sum[:])
	got, err := peerNetworkID(path)
	if err != nil || got != want {
		t.Fatalf("CA identity %q want %q: %v", got, want, err)
	}
	other := filepath.Join(t.TempDir(), "formatted.pem")
	if err := os.WriteFile(other, append([]byte("\r\n"), b...), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = peerNetworkID(other)
	if err != nil || got != want {
		t.Fatalf("PEM formatting changed network identity: %q %v", got, err)
	}
}

func TestRuntimeStartupIncludesCoordinatorAndP2PListen(t *testing.T) {
	s := newStatusStore("", NodeStatus{NodeID: "B", Mode: "spoke"})
	b, err := json.Marshal(s.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["p2p_listen"]; !ok {
		t.Errorf("startup p2p_listen absent: %s", b)
	}
	if string(got["coordinator_state"]) != `"disconnected"` {
		t.Errorf("startup coordinator_state absent or invalid: %s", b)
	}
}

func TestMemberSnapshotFinalJSONHasNoDirectUntilSession(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b, err := json.Marshal(f.r["B"].a.status.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var status RuntimeStatus
	if err := json.Unmarshal(b, &status); err != nil {
		t.Fatal(err)
	}
	p := findAgentPeerStatus(status, "C")
	if p == nil || p.PathType != "" || p.PathState != p2p.PathStateIdle {
		t.Fatalf("snapshot presence has invented/unrecognized session state: %s", b)
	}
}

func TestControlLongMemberIDUsesBoundedCorrelatedRequest(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	long := strings.Repeat("x", 300)
	m := f.members[1]
	m.NodeID = long
	m.VirtualIP = "10.77.0.9"
	if err := f.r["B"].applyMembers(proto.MemberSnapshot{Revision: 2, Members: []proto.Member{m}}); err != nil {
		t.Fatal(err)
	}
	if err := f.r["B"].routePacket(runtimePacket(2, 9)); err != nil {
		t.Fatal(err)
	}
	env := b.want(t, proto.ControlTypeConnectRequest)
	body, _ := proto.DecodeControlBody[proto.ConnectRequest](env)
	if len(env.RequestID) > 64 || env.RequestID == "" || body.TargetNodeID != long {
		t.Fatalf("unbounded/unmapped request %+v %+v", env, body)
	}
}

func TestControlPrepareSendsCurrentCandidateRevisionAndAckAfterInstall(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	b := attachRuntimeControl(t, f.r["B"])
	b.send(t, proto.ControlTypeConnectPrepare, proto.ConnectPrepare{SessionID: "prepare", Generation: 1, PeerNodeID: "C"})
	update, err := proto.DecodeControlBody[proto.CandidateUpdate](b.want(t, proto.ControlTypeCandidateUpdate))
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range update.Candidates {
		if candidate.Address != "127.0.0.1" {
			t.Fatalf("advertised address outside bound socket: %+v", candidate)
		}
	}
	ready, err := proto.DecodeControlBody[proto.ConnectReady](b.want(t, proto.ControlTypeConnectReady))
	if err != nil || !ready.Ready || ready.CandidateRevision != update.Revision || ready.CandidateRevision < 2 {
		t.Fatalf("candidate revision mismatch %+v %+v %v", update, ready, err)
	}
	member, _ := f.r["B"].member("C")
	offer := proto.SessionOffer{SessionID: "prepare", Generation: 1, PeerNodeID: "C", PeerFingerprint: member.Fingerprint, DialerNodeID: "B", PairingKey: strings.Repeat("42", 32), ExpiresAt: time.Now().Add(time.Minute), Candidates: []proto.Candidate{{Address: "127.0.0.1", Port: uint16(f.r["C"].candidates.LocalAddr().Port), Scope: "lan", ExpiresAt: time.Now().Add(time.Minute)}}}
	b.send(t, proto.ControlTypeSessionOffer, offer)
	ack, _ := proto.DecodeControlBody[proto.SessionOfferAck](b.want(t, proto.ControlTypeSessionOfferAck))
	snapshot, ok := f.r["B"].sessions.Snapshot("C")
	if !ack.Ready || !ok || snapshot.State != p2p.PathStatePreparing {
		t.Fatalf("ACK preceded offer installation: %+v %+v", ack, snapshot)
	}
	b.send(t, proto.ControlTypeSessionAbort, proto.SessionAbort{SessionID: "prepare", Generation: 1, Code: "direct_unreachable_no_relay"})
	result, _ := proto.DecodeControlBody[proto.SessionResult](b.want(t, proto.ControlTypeSessionResult))
	if result.Success || result.Code != "direct_unreachable_no_relay" {
		t.Fatalf("failed result %+v", result)
	}
}

func TestPeerRuntimeShutdownUnblocksControlWrite(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	local, remote := net.Pipe()
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.r["B"].control.serve(ctx, local) }()
	for i := 0; i < 3; i++ {
		if _, err := proto.Read(remote); err != nil {
			t.Fatal(err)
		}
	}
	payload, _ := proto.MarshalControl(proto.ControlTypeServerHello, "", proto.ServerHello{ProtocolVersion: 2, NetworkCIDR: "10.77.0.0/24", Capabilities: []string{"quic_udp_v1"}})
	if err := proto.Write(remote, proto.TypeControl, payload); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return f.r["B"].control.Available() })
	sent := make(chan error, 1)
	go func() { sent <- f.r["B"].control.Send(proto.ControlTypePing, "", proto.ControlPing{Nonce: "blocked"}) }()
	// Reading only one header byte proves that the framed write has begun.
	var first [1]byte
	if _, err := remote.Read(first[:]); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- f.r["B"].Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		remote.Close()
		t.Fatal("runtime shutdown waited for a blocked control write")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control owner still running")
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("control callback still running")
	}
}

func TestMemberDeltaInvalidBatchCannotPartiallyRemove(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	bad := f.members[2]
	bad.VirtualIP = f.members[0].VirtualIP
	if err := f.r["B"].applyDelta(proto.MemberDelta{Revision: 2, Members: []proto.Member{bad}, RemovedNodeIDs: []string{"C"}}); err == nil {
		t.Fatal("conflicting delta accepted")
	}
	if _, ok := f.r["B"].member("C"); !ok {
		t.Fatal("invalid delta partially revoked member")
	}
	duplicate := f.members[2]
	if err := f.r["B"].applyDelta(proto.MemberDelta{Revision: 3, Members: []proto.Member{duplicate, duplicate}, RemovedNodeIDs: []string{"C"}}); err == nil {
		t.Fatal("duplicate delta accepted")
	}
}

func TestPeerRuntimeNegotiatesThroughRealTLSCoordinator(t *testing.T) {
	f := newCoordinatorFixture(t)
	runtimeEventually(t, func() bool { return f.a.status.snapshot().NetworkState == "connected" })
	var endpoints []*peerRuntime
	var devices []*runtimeDevice
	var cancels []context.CancelFunc
	var controls []chan error
	for _, id := range []string{"B", "C"} {
		hello := f.hello(id)
		dir := filepath.Join(f.dir, "certs")
		cfg := &config.Config{Mode: "spoke", NodeID: id, VirtualIP: hello.VirtualIP, MTU: 1280, Connect: f.addr, ServerName: "127.0.0.1", CAFile: filepath.Join(dir, "ca.pem"), CertFile: filepath.Join(dir, id+".pem"), KeyFile: filepath.Join(dir, id+"-key.pem"), P2P: config.P2PConfig{Listen: "127.0.0.1:0"}}
		for _, route := range hello.Routes {
			cfg.Routes = append(cfg.Routes, config.Route{CIDR: route})
		}
		d := &runtimeDevice{incoming: make(chan []byte, 10), written: make(chan []byte, 10)}
		a, err := New(cfg, discardLogger(), WithDevice(d), withTestDeviceMAC(cfg.NodeID))
		if err != nil {
			t.Fatal(err)
		}
		r, err := newPeerRuntime(a)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		done := make(chan error, 1)
		go func() { done <- r.control.connect(ctx) }()
		endpoints = append(endpoints, r)
		devices = append(devices, d)
		cancels = append(cancels, cancel)
		controls = append(controls, done)
	}
	runtimeEventually(t, func() bool {
		for _, done := range controls {
			select {
			case err := <-done:
				t.Fatalf("TLS control stopped: %v", err)
			default:
			}
		}
		_, b := endpoints[0].member("C")
		_, c := endpoints[1].member("B")
		return b && c
	})
	// Authenticated peer sessions must become visible before any TUN traffic.
	runtimeEventually(t, func() bool {
		b, _ := endpoints[0].sessions.Snapshot("C")
		c, _ := endpoints[1].sessions.Snapshot("B")
		return b.State == p2p.PathStateLANDirect && c.State == p2p.PathStateLANDirect
	})
	packet := runtimePacket(2, 3)
	if err := endpoints[0].routePacket(packet); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-devices[1].written:
		if !bytes.Equal(got, packet) {
			t.Fatalf("packet %x", got)
		}
	case <-time.After(5 * time.Second):
		bs, _ := endpoints[0].sessions.Snapshot("C")
		cs, _ := endpoints[1].sessions.Snapshot("B")
		t.Fatalf("no real TLS negotiated datagram: B=%+v C=%+v coordinator=%+v", bs, cs, f.a.status.snapshot().CoordinatorMetrics)
	}
	runtimeEventually(t, func() bool { s, _ := endpoints[0].sessions.Snapshot("C"); return s.State == p2p.PathStateLANDirect })
	// Close B's control first: C receives a presence snapshot omitting B and
	// must keep the ready direct path, then both controls can disappear.
	cancels[0]()
	select {
	case <-controls[0]:
	case <-time.After(3 * time.Second):
		t.Fatal("B control did not stop")
	}
	runtimeEventually(t, func() bool { m, ok := endpoints[1].member("B"); return ok && m.Status == "offline_or_unknown" })
	reverse := runtimePacket(3, 2)
	if err := endpoints[1].routePacket(reverse); err != nil {
		t.Fatal(err)
	}
	assertRuntimeDelivery(t, devices[0], reverse)
	cancels[1]()
	select {
	case <-controls[1]:
	case <-time.After(3 * time.Second):
		t.Fatal("C control did not stop")
	}
	if err := endpoints[0].routePacket(packet); err != nil {
		t.Fatal(err)
	}
	assertRuntimeDelivery(t, devices[1], packet)
}
