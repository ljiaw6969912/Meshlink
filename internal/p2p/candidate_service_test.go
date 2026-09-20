package p2p

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"meshlink/internal/proto"
)

func TestCandidateServiceProbePunchAndQUICShareOneSocket(t *testing.T) {
	clock := newTestClock(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC))
	authority := NewProbeAuthority(clock.Now)
	credential, err := authority.Issue()
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	rendezvous := listenTestUDP(t)

	type receivedPacket struct {
		remote  *net.UDPAddr
		payload []byte
	}
	probeSeen := make(chan receivedPacket, 1)
	punchSeen := make(chan receivedPacket, 1)
	serverErr := make(chan error, 1)
	wrongSource := listenTestUDP(t)
	go func() {
		buffer := make([]byte, 2048)
		if err := rendezvous.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			serverErr <- err
			return
		}
		n, remote, err := rendezvous.ReadFromUDP(buffer)
		if err != nil {
			serverErr <- err
			return
		}
		request := append([]byte(nil), buffer[:n]...)
		probeSeen <- receivedPacket{remote: cloneUDPAddr(remote), payload: request}
		response, err := authority.Handle(request, remote)
		if err != nil {
			serverErr <- err
			return
		}
		if _, err := wrongSource.WriteToUDP(response, remote); err != nil {
			serverErr <- err
			return
		}
		if _, err := rendezvous.WriteToUDP(response, remote); err != nil {
			serverErr <- err
			return
		}

		n, remote, err = rendezvous.ReadFromUDP(buffer)
		if err != nil {
			serverErr <- err
			return
		}
		punchSeen <- receivedPacket{remote: cloneUDPAddr(remote), payload: append([]byte(nil), buffer[:n]...)}
	}()

	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:    "node-b",
		NetworkID: "network-1",
		Listen:    "127.0.0.1:0",
		TLSConfig: testQUICServerTLSConfig(t),
		EnumerateIPv4: func() ([]netip.Addr, error) {
			return nil, nil
		},
		Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("new candidate service: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close candidate service: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := service.Refresh(ctx, rendezvous.LocalAddr().String(), credential)
	if err != nil {
		t.Fatalf("refresh candidates: %v", err)
	}
	probe := receiveTestPacket(t, ctx, probeSeen, serverErr)
	local := service.LocalAddr()
	if probe.remote.Port != local.Port || !probe.remote.IP.Equal(local.IP) {
		t.Fatalf("probe source = %s, service local address = %s", probe.remote, local)
	}
	if len(probe.payload) == 0 || probe.payload[0] != 0x00 {
		t.Fatalf("probe payload begins %x, want non-QUIC marker", probe.payload)
	}
	if service.Transport() == nil || service.Transport().Conn != service.conn {
		t.Fatal("quic transport does not own the candidate service UDPConn")
	}
	if service.Transport() != service.transport || service.Listener() != service.listener {
		t.Fatal("candidate service did not retain its one transport and listener")
	}
	assertSnapshotHasCandidate(t, snapshot, "public", local.IP.String(), uint16(local.Port))
	assertSnapshotHasCandidate(t, snapshot, "lan", local.IP.String(), uint16(local.Port))

	pairingKey := bytes.Repeat([]byte{0xab}, 32)
	pairingKeyText := encodeSecret(pairingKey)
	target := rendezvous.LocalAddr().(*net.UDPAddr)
	err = service.Punch(ctx, "session-7", 3, pairingKeyText, []proto.Candidate{{
		Address:   target.IP.String(),
		Port:      uint16(target.Port),
		Scope:     "public",
		Priority:  50,
		ExpiresAt: clock.Now().Add(time.Minute),
	}})
	if err != nil {
		t.Fatalf("punch: %v", err)
	}
	punch := receiveTestPacket(t, ctx, punchSeen, serverErr)
	if punch.remote.Port != local.Port || !punch.remote.IP.Equal(local.IP) {
		t.Fatalf("punch source = %s, service local address = %s", punch.remote, local)
	}
	decoded, err := DecodePunchPacket(punch.payload, pairingKeyText)
	if err != nil {
		t.Fatalf("decode punch: %v", err)
	}
	if decoded.SessionID != "session-7" || decoded.Generation != 3 || len(decoded.Nonce) != PunchNonceSize {
		t.Fatalf("decoded punch = %+v, want authenticated session-7 generation 3", decoded)
	}
	tampered := bytes.Replace(punch.payload, []byte("session-7"), []byte("session-8"), 1)
	if bytes.Equal(tampered, punch.payload) {
		t.Fatal("test setup did not alter the punch session ID")
	}
	if _, err := DecodePunchPacket(tampered, pairingKeyText); !errors.Is(err, ErrPunchAuthentication) {
		t.Fatalf("tampered punch error = %v, want %v", err, ErrPunchAuthentication)
	}
}

func TestCandidateServiceExcludesExpiredAndDuplicateCandidates(t *testing.T) {
	clock := newTestClock(time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC))
	authority := NewProbeAuthority(clock.Now)
	credential, err := authority.Issue()
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	rendezvous := listenTestUDP(t)
	serverErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		if err := rendezvous.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			serverErr <- err
			return
		}
		n, remote, err := rendezvous.ReadFromUDP(buffer)
		if err != nil {
			serverErr <- err
			return
		}
		response, err := authority.Handle(buffer[:n], remote)
		if err != nil {
			serverErr <- err
			return
		}
		if _, err := rendezvous.WriteToUDP(response, remote); err != nil {
			serverErr <- err
		}
	}()

	duplicateLAN := netip.MustParseAddr("192.0.2.44")
	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:    "node-c",
		NetworkID: "network-1",
		Listen:    "0.0.0.0:0",
		TLSConfig: testQUICServerTLSConfig(t),
		EnumerateIPv4: func() ([]netip.Addr, error) {
			return []netip.Addr{duplicateLAN, duplicateLAN}, nil
		},
		Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("new candidate service: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close candidate service: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initial, err := service.Refresh(ctx, rendezvous.LocalAddr().String(), credential)
	if err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if got := candidateScopeCount(initial, "lan"); got != 1 {
		t.Fatalf("initial LAN candidate count = %d, want one deduplicated candidate: %+v", got, initial.Candidates)
	}
	if got := candidateScopeCount(initial, "public"); got != 1 {
		t.Fatalf("initial public candidate count = %d, want one observation: %+v", got, initial.Candidates)
	}
	if initial.Revision != 1 || !initial.ObservedAt.Equal(clock.Now()) {
		t.Fatalf("initial snapshot = %+v, want revision 1 at fixed clock", initial)
	}

	clock.Advance(121 * time.Second)
	current, err := service.Refresh(ctx, "", proto.ProbeCredential{})
	if err != nil {
		t.Fatalf("LAN-only refresh: %v", err)
	}
	if got := candidateScopeCount(current, "lan"); got != 1 {
		t.Fatalf("refreshed LAN candidate count = %d, want one live candidate: %+v", got, current.Candidates)
	}
	if got := candidateScopeCount(current, "public"); got != 0 {
		t.Fatalf("refreshed public candidate count = %d, want expired observation pruned: %+v", got, current.Candidates)
	}
	for _, candidate := range current.Candidates {
		if !candidate.ExpiresAt.After(clock.Now()) {
			t.Fatalf("snapshot contains expired candidate: %+v", candidate)
		}
	}
	if current.Revision != 2 || !current.ObservedAt.Equal(clock.Now()) {
		t.Fatalf("current snapshot = %+v, want revision 2 at advanced clock", current)
	}
	select {
	case err := <-serverErr:
		t.Fatalf("rendezvous server: %v", err)
	default:
	}
}

func TestCandidateServiceDiscardsStaleResponseBeforeCurrentResponse(t *testing.T) {
	clock := newTestClock(time.Date(2026, 9, 14, 10, 30, 0, 0, time.UTC))
	authority := NewProbeAuthority(clock.Now)
	credential, err := authority.Issue()
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	rendezvous := listenTestUDP(t)
	firstHandled := make(chan struct{})
	serverErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 2048)
		if err := rendezvous.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			serverErr <- err
			return
		}
		n, remote, err := rendezvous.ReadFromUDP(buffer)
		if err != nil {
			serverErr <- err
			return
		}
		staleResponse, err := authority.Handle(buffer[:n], remote)
		if err != nil {
			serverErr <- err
			return
		}
		close(firstHandled)

		n, remote, err = rendezvous.ReadFromUDP(buffer)
		if err != nil {
			serverErr <- err
			return
		}
		currentResponse, err := authority.Handle(buffer[:n], remote)
		if err != nil {
			serverErr <- err
			return
		}
		if _, err := rendezvous.WriteToUDP(staleResponse, remote); err != nil {
			serverErr <- err
			return
		}
		if _, err := rendezvous.WriteToUDP(currentResponse, remote); err != nil {
			serverErr <- err
		}
	}()

	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:        "node-stale",
		NetworkID:     "network-1",
		Listen:        "127.0.0.1:0",
		TLSConfig:     testQUICServerTLSConfig(t),
		EnumerateIPv4: func() ([]netip.Addr, error) { return nil, nil },
		Now:           clock.Now,
	})
	if err != nil {
		t.Fatalf("new candidate service: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.Refresh(firstContext, rendezvous.LocalAddr().String(), credential)
		firstResult <- err
	}()
	select {
	case <-firstHandled:
		cancelFirst()
	case err := <-serverErr:
		t.Fatalf("rendezvous server: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("rendezvous did not handle first request")
	}
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first refresh error = %v, want context cancellation", err)
	}

	secondContext, cancelSecond := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSecond()
	snapshot, err := service.Refresh(secondContext, rendezvous.LocalAddr().String(), credential)
	if err != nil {
		t.Fatalf("second refresh rejected after stale response: %v", err)
	}
	local := service.LocalAddr()
	assertSnapshotHasCandidate(t, snapshot, "public", local.IP.String(), uint16(local.Port))
	select {
	case err := <-serverErr:
		t.Fatalf("rendezvous server: %v", err)
	default:
	}
}

func TestCandidateServiceRejectsOperationsAfterClose(t *testing.T) {
	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:        "node-closed",
		NetworkID:     "network-1",
		Listen:        "127.0.0.1:0",
		TLSConfig:     testQUICServerTLSConfig(t),
		EnumerateIPv4: func() ([]netip.Addr, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new candidate service: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close candidate service: %v", err)
	}
	if _, err := service.Refresh(context.Background(), "", proto.ProbeCredential{}); !errors.Is(err, ErrCandidateServiceClosed) {
		t.Fatalf("refresh after close error = %v, want %v", err, ErrCandidateServiceClosed)
	}
	if err := service.Punch(context.Background(), "session-closed", 1, encodeSecret(bytes.Repeat([]byte{0xcd}, 32)), nil); !errors.Is(err, ErrCandidateServiceClosed) {
		t.Fatalf("punch after close error = %v, want %v", err, ErrCandidateServiceClosed)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestCandidateServiceAcceptsCanceledOrDiscardedNonQUICPrime(t *testing.T) {
	for _, input := range []error{context.Canceled, nil} {
		if err := normalizeNonQUICPrimeError(input); err != nil {
			t.Fatalf("prime result %v normalized to %v, want success", input, err)
		}
	}
	unexpected := errors.New("transport failed")
	if err := normalizeNonQUICPrimeError(unexpected); !errors.Is(err, unexpected) {
		t.Fatalf("unexpected prime error normalized to %v, want wrapped transport failure", err)
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func (c *testClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func listenTestUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func testQUICServerTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  privateKey,
		}},
		NextProtos: []string{"meshlink-p2p-test"},
	}
}

func receiveTestPacket[T any](t *testing.T, ctx context.Context, packets <-chan T, errs <-chan error) T {
	t.Helper()
	var zero T
	select {
	case packet := <-packets:
		return packet
	case err := <-errs:
		t.Fatalf("rendezvous server: %v", err)
	case <-ctx.Done():
		t.Fatalf("waiting for rendezvous packet: %v", ctx.Err())
	}
	return zero
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}

func assertSnapshotHasCandidate(t *testing.T, snapshot CandidateSnapshot, scope, address string, port uint16) {
	t.Helper()
	for _, candidate := range snapshot.Candidates {
		if candidate.Scope == scope && candidate.Address == address && candidate.Port == port {
			return
		}
	}
	t.Fatalf("snapshot lacks %s candidate %s:%d: %+v", scope, address, port, snapshot.Candidates)
}

func candidateScopeCount(snapshot CandidateSnapshot, scope string) int {
	count := 0
	for _, candidate := range snapshot.Candidates {
		if candidate.Scope == scope {
			count++
		}
	}
	return count
}
