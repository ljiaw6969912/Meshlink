package p2p

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"meshlink/internal/proto"
)

type sessionTestDelivery struct {
	peerID string
	packet []byte
}

func TestFailRequestChurnHasOneRetryWorkerPerPair(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	packet := []byte{0x45, 0, 0, 20, 0, 1, 0, 0, 64, 17, 0, 0, 10, 77, 0, 2, 10, 77, 0, 3}
	for i := 0; i < 100; i++ {
		before := time.Now()
		if err := b.manager.Send("node-c", packet); err != nil {
			t.Fatal(err)
		}
		b.manager.mu.Lock()
		pair := b.manager.pairs["node-c"]
		deadline := pair.retryAt
		want := requestBackoff(pair.requestAttempts)
		after := time.Now()
		b.manager.mu.Unlock()
		if deadline.Before(before.Add(want)) || deadline.After(after.Add(want)) {
			t.Fatalf("retry deadline %s is outside scheduling interval [%s,%s] + %s", deadline, before, after, want)
		}
		if err := b.manager.FailRequest("node-c", "peer_offline"); err != nil {
			t.Fatal(err)
		}
		b.manager.mu.Lock()
		active := !pair.retryAt.IsZero() || pair.retryTimer.Stop()
		b.manager.mu.Unlock()
		if active {
			t.Fatal("FailRequest did not immediately stop its retry timer")
		}
	}
	stack := make([]byte, 1<<20)
	n := runtime.Stack(stack, true)
	workers := strings.Count(string(stack[:n]), ".requestRetryAfter(")
	if workers > 1 {
		t.Errorf("Send/FailRequest churn left %d live retry workers for one pair", workers)
	}
	snapshot, _ := b.manager.Snapshot("node-c")
	if snapshot.State != PathStateFailed || snapshot.PendingPackets > 64 || snapshot.PendingDropped == 0 {
		t.Errorf("queue/state after churn: %+v", snapshot)
	}
	closed := make(chan struct{})
	go func() { _ = b.manager.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close failed to stop retry owner promptly")
	}
}

func TestSessionManagerFailRequestPreservesQueueAndAllowsLaterRequest(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	requests := make(chan string, 10)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(cfg *SessionManagerConfig) { cfg.RequestSession = func(id string) { requests <- id } })
	packet := []byte{0x45, 0, 0, 20, 0, 1, 0, 0, 64, 17, 0, 0, 10, 77, 0, 2, 10, 77, 0, 3}
	if err := b.manager.Send("node-c", packet); err != nil {
		t.Fatal(err)
	}
	<-requests
	if err := b.manager.FailRequest("node-c", "peer_unavailable"); err != nil {
		t.Fatal(err)
	}
	s, _ := b.manager.Snapshot("node-c")
	if s.State != PathStateFailed || s.ErrorCode != "peer_unavailable" || s.PendingPackets != 1 {
		t.Fatalf("rejection %+v", s)
	}
	if err := b.manager.Send("node-c", packet); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("later Send did not request again")
	}
	b.manager.SetCoordinatorAvailable(false)
	if err := b.manager.FailRequest("node-c", "peer_unavailable"); err != nil {
		t.Fatal(err)
	}
	s, _ = b.manager.Snapshot("node-c")
	if s.State != PathStateWaitingCoordinator {
		t.Fatalf("late rejection replaced waiting state %+v", s)
	}
}

type sessionTestEndpoint struct {
	nodeID     string
	service    *CandidateService
	manager    *SessionManager
	deliveries chan sessionTestDelivery
	changes    chan SessionSnapshot
}

func TestSessionManagersExchangeEncryptedDatagramsOverRealQUIC(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)

	pairingKey := strings.Repeat("42", 32)
	expiresAt := time.Now().Add(time.Minute)
	bOffer := proto.SessionOffer{
		SessionID:       "session-bc-7",
		Generation:      7,
		PeerNodeID:      "node-c",
		PeerFingerprint: identities["node-c"].fingerprint,
		DialerNodeID:    "node-b",
		Candidates:      []proto.Candidate{sessionTestCandidate(c.service, "lan", expiresAt)},
		ExpiresAt:       expiresAt,
		PairingKey:      pairingKey,
	}
	cOffer := proto.SessionOffer{
		SessionID:       "session-bc-7",
		Generation:      7,
		PeerNodeID:      "node-b",
		PeerFingerprint: identities["node-b"].fingerprint,
		DialerNodeID:    "node-b",
		Candidates:      []proto.Candidate{sessionTestCandidate(b.service, "lan", expiresAt)},
		ExpiresAt:       expiresAt,
		PairingKey:      pairingKey,
	}
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install B offer: %v", err)
	}
	if err := c.manager.InstallOffer(cOffer); err != nil {
		t.Fatalf("install C offer: %v", err)
	}
	start := proto.SessionStart{SessionID: "session-bc-7", Generation: 7}
	if err := c.manager.StartOffer(start); err != nil {
		t.Fatalf("start C offer: %v", err)
	}
	if err := b.manager.StartOffer(start); err != nil {
		t.Fatalf("start B offer: %v", err)
	}

	waitSessionState(t, b.manager, "node-c", PathStateLANDirect, 5*time.Second)
	waitSessionState(t, c.manager, "node-b", PathStateLANDirect, 5*time.Second)
	assertSessionTransportSecurity(t, b.manager, "node-c")
	assertSessionTransportSecurity(t, c.manager, "node-b")
	if err := b.manager.FailRequest("node-c", "peer_offline"); err != nil {
		t.Fatal(err)
	}
	if snapshot, _ := b.manager.Snapshot("node-c"); snapshot.State != PathStateLANDirect {
		t.Fatalf("late request failure harmed active session: %+v", snapshot)
	}

	bToC := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x01, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	cToB := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x02, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 3,
		10, 77, 0, 2,
	}
	if err := b.manager.Send("node-c", bToC); err != nil {
		t.Fatalf("send B to C: %v", err)
	}
	if err := c.manager.Send("node-b", cToB); err != nil {
		t.Fatalf("send C to B: %v", err)
	}
	assertSessionDelivery(t, c.deliveries, "node-b", bToC)
	assertSessionDelivery(t, b.deliveries, "node-c", cToB)

	bActive := b.manager.ActiveSessions().Sessions
	cActive := c.manager.ActiveSessions().Sessions
	if len(bActive) != 1 || len(cActive) != 1 || bActive[0].SessionID != "session-bc-7" || cActive[0].SessionID != "session-bc-7" {
		t.Fatalf("active sessions B=%+v C=%+v", bActive, cActive)
	}
	if snapshot, ok := b.manager.Snapshot("node-c"); !ok || snapshot.BytesSent != uint64(len(bToC)) || snapshot.BytesReceived != uint64(len(cToB)) {
		t.Fatalf("B snapshot after exchange = %+v, present=%v", snapshot, ok)
	}
}

func TestSessionManagerFlushesPendingPacketIntoAuthorizedPublicPath(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	requests := make(chan string, 2)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x05, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	if err := b.manager.Send("node-c", packet); err != nil {
		t.Fatalf("queue first B-C packet: %v", err)
	}
	select {
	case peerID := <-requests:
		if peerID != "node-c" {
			t.Fatalf("first packet requested %q", peerID)
		}
	case <-time.After(time.Second):
		t.Fatal("first packet did not request a session")
	}
	bOffer, cOffer := sessionTestOffers(b, c, identities, "session-public", 8, strings.Repeat("44", 32))
	bOffer.Candidates[0].Scope = "public"
	cOffer.Candidates[0].Scope = "public"
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install B public offer: %v", err)
	}
	if err := c.manager.InstallOffer(cOffer); err != nil {
		t.Fatalf("install C public offer: %v", err)
	}
	start := proto.SessionStart{SessionID: "session-public", Generation: 8}
	if err := c.manager.StartOffer(start); err != nil {
		t.Fatalf("start C public offer: %v", err)
	}
	if err := b.manager.StartOffer(start); err != nil {
		t.Fatalf("start B public offer: %v", err)
	}
	waitSessionState(t, b.manager, "node-c", PathStatePublicDirect, 5*time.Second)
	waitSessionState(t, c.manager, "node-b", PathStatePublicDirect, 5*time.Second)
	assertSessionDelivery(t, c.deliveries, "node-b", packet)
	if snapshot, ok := b.manager.Snapshot("node-c"); !ok || snapshot.PathType != PathTypePublicDirect || snapshot.PendingPackets != 0 {
		t.Fatalf("public session snapshot = %+v, present=%v", snapshot, ok)
	}
}

func TestSessionManagerClonesAndEnforcesTransportConfiguration(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	inputQUIC := &quic.Config{
		Allow0RTT:       true,
		EnableDatagrams: false,
		KeepAlivePeriod: time.Second,
		MaxIdleTimeout:  2 * time.Second,
	}
	inputTLS := identities["node-b"].tlsConfig.Clone()
	inputTLS.MinVersion = tls.VersionTLS12
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.TLSConfig = inputTLS
		config.QUICConfig = inputQUIC
	})
	if b.manager.tlsConfig == inputTLS || b.manager.tlsConfig.MinVersion != tls.VersionTLS13 || len(b.manager.tlsConfig.NextProtos) != 1 || b.manager.tlsConfig.NextProtos[0] != sessionALPN {
		t.Fatalf("normalized TLS config = %+v", b.manager.tlsConfig)
	}
	if b.manager.quicConfig == inputQUIC || !b.manager.quicConfig.EnableDatagrams || b.manager.quicConfig.Allow0RTT || b.manager.quicConfig.KeepAlivePeriod != 15*time.Second || b.manager.quicConfig.MaxIdleTimeout != 45*time.Second {
		t.Fatalf("normalized QUIC config = %+v", b.manager.quicConfig)
	}
	if !inputQUIC.Allow0RTT || inputQUIC.EnableDatagrams || inputQUIC.KeepAlivePeriod != time.Second || inputQUIC.MaxIdleTimeout != 2*time.Second || inputTLS.MinVersion != tls.VersionTLS12 {
		t.Fatal("session manager mutated caller-owned TLS or QUIC configuration")
	}
	insecureTLS := identities["node-b"].tlsConfig.Clone()
	insecureTLS.InsecureSkipVerify = true
	if _, err := NewSessionManager(SessionManagerConfig{
		NodeID:         "node-b",
		NetworkID:      "network-1",
		MTU:            1280,
		Candidates:     b.service,
		TLSConfig:      insecureTLS,
		LocalVirtualIP: netip.MustParseAddr("10.77.0.2"),
		PeerMember:     func(string) (proto.Member, bool) { return proto.Member{}, false },
		DeliverPacket:  func(string, []byte) error { return nil },
	}); err == nil || !strings.Contains(err.Error(), "may not skip verification") {
		t.Fatalf("caller-provided InsecureSkipVerify error = %v", err)
	}
}

func TestNewSessionManagerRejectsCandidateServiceIdentityMismatch(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:        "node-b",
		NetworkID:     "network-1",
		Listen:        "127.0.0.1:0",
		TLSConfig:     identities["node-b"].tlsConfig,
		QUICConfig:    &quic.Config{EnableDatagrams: true},
		EnumerateIPv4: func() ([]netip.Addr, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new candidate service: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	for _, mismatch := range []struct {
		name      string
		nodeID    string
		networkID string
	}{
		{name: "node", nodeID: "node-c", networkID: "network-1"},
		{name: "network", nodeID: "node-b", networkID: "network-2"},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			manager, err := NewSessionManager(SessionManagerConfig{
				NodeID:         mismatch.nodeID,
				NetworkID:      mismatch.networkID,
				MTU:            1280,
				Candidates:     service,
				TLSConfig:      identities["node-b"].tlsConfig,
				LocalVirtualIP: netip.MustParseAddr("10.77.0.2"),
				PeerMember:     func(string) (proto.Member, bool) { return proto.Member{}, false },
				DeliverPacket:  func(string, []byte) error { return nil },
			})
			if manager != nil {
				_ = manager.Close()
			}
			if err == nil {
				t.Fatalf("accepted %s mismatch", mismatch.name)
			}
		})
	}
}

func TestCandidateServiceAllowsOnlyOneSessionManager(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:        "node-b",
		NetworkID:     "network-1",
		Listen:        "127.0.0.1:0",
		TLSConfig:     identities["node-b"].tlsConfig,
		QUICConfig:    &quic.Config{EnableDatagrams: true},
		EnumerateIPv4: func() ([]netip.Addr, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new candidate service: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	config := SessionManagerConfig{
		NodeID:         "node-b",
		NetworkID:      "network-1",
		MTU:            1280,
		Candidates:     service,
		TLSConfig:      identities["node-b"].tlsConfig,
		LocalVirtualIP: netip.MustParseAddr("10.77.0.2"),
		PeerMember:     func(string) (proto.Member, bool) { return proto.Member{}, false },
		DeliverPacket:  func(string, []byte) error { return nil },
	}
	first, err := NewSessionManager(config)
	if err != nil {
		t.Fatalf("new first manager: %v", err)
	}
	second, secondErr := NewSessionManager(config)
	if second != nil {
		_ = second.Close()
	}
	if secondErr == nil {
		_ = first.Close()
		t.Fatal("same CandidateService accepted a second live SessionManager")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first manager: %v", err)
	}
	third, err := NewSessionManager(config)
	if err != nil {
		t.Fatalf("candidate service claim was not released after Close: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("close replacement manager: %v", err)
	}
}

func TestSessionManagerDoesNotMarkCandidateOrTLSOnlyProbeDirect(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)

	refreshContext, cancelRefresh := context.WithTimeout(context.Background(), time.Second)
	defer cancelRefresh()
	if snapshot, err := c.service.Refresh(refreshContext, "", proto.ProbeCredential{}); err != nil || len(snapshot.Candidates) != 1 {
		t.Fatalf("refresh C LAN candidate = %+v, err=%v", snapshot, err)
	}
	_, cOffer := sessionTestOffers(b, c, identities, "session-bare-tls", 13, strings.Repeat("6b", 32))
	if err := c.manager.InstallOffer(cOffer); err != nil {
		t.Fatalf("install C offer: %v", err)
	}
	if err := c.manager.StartOffer(proto.SessionStart{SessionID: cOffer.SessionID, Generation: cOffer.Generation}); err != nil {
		t.Fatalf("start C offer: %v", err)
	}

	dialContext, cancelDial := context.WithTimeout(context.Background(), time.Second)
	defer cancelDial()
	remote := c.service.LocalAddr()
	conn, err := b.service.Transport().Dial(
		dialContext,
		&net.UDPAddr{IP: append(net.IP(nil), remote.IP...), Port: remote.Port},
		b.manager.tlsConfigForPeer("node-c", identities["node-c"].fingerprint),
		b.manager.quicConfig.Clone(),
	)
	if err != nil {
		t.Fatalf("complete bare QUIC TLS: %v", err)
	}
	defer func() { _ = conn.CloseWithError(sessionApplicationError, "test complete") }()
	waitSessionState(t, c.manager, "node-b", PathStateAuthenticating, time.Second)

	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x03, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	fragments, err := FragmentPacket(91, packet, 1280)
	if err != nil {
		t.Fatalf("fragment literal probe packet: %v", err)
	}
	for _, fragment := range fragments {
		if err := conn.SendDatagram(fragment); err != nil {
			t.Fatalf("send unauthorized bare-TLS datagram: %v", err)
		}
	}
	if err := c.manager.Send("node-b", packet); err != nil {
		t.Fatalf("queue while authenticating: %v", err)
	}
	select {
	case delivery := <-c.deliveries:
		t.Fatalf("bare TLS delivered unauthorized packet: %+v", delivery)
	case <-time.After(150 * time.Millisecond):
	}
	if snapshot, ok := c.manager.Snapshot("node-b"); !ok || isDirectSessionState(snapshot.State) || snapshot.PendingPackets != 1 {
		t.Fatalf("C bare-TLS snapshot = %+v, present=%v", snapshot, ok)
	}
}

func TestSessionFailureWaitsForCoordinatorAndConsumesOffer(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	requests := make(chan string, 4)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	bAttempt, cAttempt := establishSessionTestPair(t, b, c, identities, "session-consumed", 7, strings.Repeat("7c", 32))
	if len(bAttempt.keyCopy()) != 0 || len(cAttempt.keyCopy()) != 0 {
		t.Fatal("successful SessionHello retained a pairing key in memory")
	}

	b.manager.SetCoordinatorAvailable(false)
	c.manager.SetCoordinatorAvailable(false)
	time.Sleep(4 * 50 * time.Millisecond)
	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x03, 0x01, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	if err := b.manager.Send("node-c", packet); err != nil {
		t.Fatalf("send while coordinator unavailable: %v", err)
	}
	assertSessionDelivery(t, c.deliveries, "node-b", packet)
	if snapshot := waitSessionState(t, b.manager, "node-c", PathStateLANDirect, time.Second); snapshot.SessionID != "session-consumed" {
		t.Fatalf("coordinator outage replaced healthy session: %+v", snapshot)
	}
	if err := c.manager.ClosePeer("node-b", "test_closed"); err != nil {
		t.Fatalf("close C-to-B session: %v", err)
	}
	waitSessionState(t, b.manager, "node-c", PathStateWaitingCoordinator, 2*time.Second)
	oldStart := proto.SessionStart{SessionID: "session-consumed", Generation: 7}
	if err := b.manager.StartOffer(oldStart); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("restart consumed generation error = %v, want %v", err, ErrSessionOfferRejected)
	}
	bOffer, _ := sessionTestOffers(b, c, identities, oldStart.SessionID, oldStart.Generation, strings.Repeat("7c", 32))
	if err := b.manager.InstallOffer(bOffer); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("reinstall consumed generation error = %v, want %v", err, ErrSessionOfferRejected)
	}
	if active := b.manager.ActiveSessions().Sessions; len(active) != 0 {
		t.Fatalf("B retained active session after direct failure: %+v", active)
	}
	select {
	case peerID := <-requests:
		t.Fatalf("coordinator-unavailable failure requested %s", peerID)
	case <-time.After(100 * time.Millisecond):
	}
	b.manager.SetCoordinatorAvailable(true)
	select {
	case peerID := <-requests:
		if peerID != "node-c" {
			t.Fatalf("coordinator recovery requested %q", peerID)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator recovery did not request a fresh offer")
	}
}

func TestPairFailureDoesNotCancelAnotherReadyPair(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c", "node-d")
	b := newSessionTestEndpointWithPeers(t, "node-b", "10.77.0.2", map[string]string{
		"node-c": "10.77.0.3",
		"node-d": "10.77.0.4",
	}, identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	d := newSessionTestEndpoint(t, "node-d", "10.77.0.4", "node-b", "10.77.0.2", identities)
	establishSessionTestPair(t, b, c, identities, "session-bc", 21, strings.Repeat("81", 32))
	establishSessionTestPair(t, b, d, identities, "session-bd", 22, strings.Repeat("82", 32))
	before := waitSessionState(t, b.manager, "node-d", PathStateLANDirect, time.Second)

	if err := c.manager.ClosePeer("node-b", "test_closed"); err != nil {
		t.Fatalf("close only B-C: %v", err)
	}
	waitSessionState(t, b.manager, "node-c", PathStateReconnecting, 2*time.Second)
	for sequence := byte(1); sequence <= 3; sequence++ {
		bToD := []byte{
			0x45, 0x00, 0x00, 0x14, 0x00, sequence, 0x00, 0x00,
			0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
			10, 77, 0, 4,
		}
		dToB := []byte{
			0x45, 0x00, 0x00, 0x14, 0x01, sequence, 0x00, 0x00,
			0x40, 0x11, 0x00, 0x00, 10, 77, 0, 4,
			10, 77, 0, 2,
		}
		if err := b.manager.Send("node-d", bToD); err != nil {
			t.Fatalf("send B-D sequence %d: %v", sequence, err)
		}
		if err := d.manager.Send("node-b", dToB); err != nil {
			t.Fatalf("send D-B sequence %d: %v", sequence, err)
		}
		assertSessionDelivery(t, d.deliveries, "node-b", bToD)
		assertSessionDelivery(t, b.deliveries, "node-d", dToB)
	}
	after := waitSessionState(t, b.manager, "node-d", PathStateLANDirect, time.Second)
	if after.SessionID != before.SessionID || after.Generation != before.Generation {
		t.Fatalf("B-D changed while B-C failed: before=%+v after=%+v", before, after)
	}
}

func TestSessionManagerBoundsPendingQueueAndStopsRequestsAfterExpiry(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	clock := newTestClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	requests := make(chan string, 8)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.Now = clock.Now
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x04, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	for index := 0; index < 100; index++ {
		if err := b.manager.Send("node-c", packet); err != nil {
			t.Fatalf("queue packet %d: %v", index, err)
		}
	}
	if snapshot, ok := b.manager.Snapshot("node-c"); !ok || snapshot.PendingPackets != 64 || snapshot.PendingBytes != 64*len(packet) || snapshot.PendingDropped != 36 {
		t.Fatalf("bounded pending snapshot = %+v, present=%v", snapshot, ok)
	}
	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		select {
		case peerID := <-requests:
			if peerID != "node-c" {
				t.Fatalf("request %d targeted %q", requestIndex, peerID)
			}
		case <-time.After(1500 * time.Millisecond):
			t.Fatalf("timed out waiting for request %d", requestIndex+1)
		}
	}
	clock.Advance(4 * time.Second)
	select {
	case peerID := <-requests:
		t.Fatalf("expired pending traffic triggered another request for %s", peerID)
	case <-time.After(2200 * time.Millisecond):
	}
	if snapshot, ok := b.manager.Snapshot("node-c"); !ok || snapshot.PendingPackets != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("expired pending snapshot = %+v, present=%v", snapshot, ok)
	}
}

func TestSessionManagerExpiresPendingQueueWhileCoordinatorUnavailable(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	clock := newTestClock(time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC))
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.Now = clock.Now
	})
	b.manager.SetCoordinatorAvailable(false)
	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x06, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	if err := b.manager.Send("node-c", packet); err != nil {
		t.Fatalf("queue while coordinator unavailable: %v", err)
	}
	clock.Advance(4 * time.Second)
	time.Sleep(2 * reassemblySweepInterval)
	if snapshot, ok := b.manager.Snapshot("node-c"); !ok || snapshot.PendingPackets != 0 || snapshot.PendingBytes != 0 || snapshot.PendingDropped != 1 {
		t.Fatalf("offline expired pending snapshot = %+v, present=%v", snapshot, ok)
	}
}

func TestSessionManagerEnforcesGlobalPendingQueueBound(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	peerVirtualIPs := make(map[string]string, 1100)
	for index := 0; index < 1100; index++ {
		peerVirtualIPs[fmt.Sprintf("peer-%04d", index)] = "10.77.0.3"
	}
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", peerVirtualIPs, identities, nil)
	b.manager.SetCoordinatorAvailable(false)
	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x07, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	for index := 0; index < 1100; index++ {
		peerID := fmt.Sprintf("peer-%04d", index)
		if err := b.manager.Send(peerID, packet); err != nil {
			t.Fatalf("queue global packet %d: %v", index, err)
		}
	}
	b.manager.mu.Lock()
	totalPackets := 0
	totalBytes := 0
	for _, pair := range b.manager.pairs {
		totalPackets += len(pair.pending)
		totalBytes += pair.pendingBytes
	}
	b.manager.mu.Unlock()
	if totalPackets != globalPendingPacketLimit || totalBytes != globalPendingPacketLimit*len(packet) || totalBytes > globalPendingByteLimit {
		t.Fatalf("global pending usage = %d packets/%d bytes", totalPackets, totalBytes)
	}
	if oldest, ok := b.manager.Snapshot("peer-0000"); !ok || oldest.PendingPackets != 0 || oldest.PendingDropped != 1 {
		t.Fatalf("oldest global pending snapshot = %+v, present=%v", oldest, ok)
	}
	if newest, ok := b.manager.Snapshot("peer-1099"); !ok || newest.PendingPackets != 1 {
		t.Fatalf("newest global pending snapshot = %+v, present=%v", newest, ok)
	}
}

func TestSessionManagerEnforcesGlobalPendingByteBound(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	peerVirtualIPs := make(map[string]string, 950)
	for index := 0; index < 950; index++ {
		peerVirtualIPs[fmt.Sprintf("large-peer-%04d", index)] = "10.77.0.3"
	}
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", peerVirtualIPs, identities, func(config *SessionManagerConfig) {
		config.MTU = MaxOriginalPacketSize
	})
	b.manager.SetCoordinatorAvailable(false)
	packet := bytes.Repeat([]byte{0x5a}, MaxOriginalPacketSize)
	for index := 0; index < 950; index++ {
		if err := b.manager.Send(fmt.Sprintf("large-peer-%04d", index), packet); err != nil {
			t.Fatalf("queue global byte packet %d: %v", index, err)
		}
	}
	b.manager.mu.Lock()
	totalPackets := 0
	totalBytes := 0
	for _, pair := range b.manager.pairs {
		totalPackets += len(pair.pending)
		totalBytes += pair.pendingBytes
	}
	b.manager.mu.Unlock()
	wantPackets := globalPendingByteLimit / MaxOriginalPacketSize
	if totalPackets != wantPackets || totalBytes != wantPackets*MaxOriginalPacketSize || totalBytes > globalPendingByteLimit {
		t.Fatalf("global byte usage = %d packets/%d bytes, want %d packets", totalPackets, totalBytes, wantPackets)
	}
}

func TestSessionManagerSendRejectsUnknownPeerWithoutAllocatingPair(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	requests := make(chan string, 1)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x00, 0x08, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 9,
	}
	if err := b.manager.Send("node-unknown", packet); !errors.Is(err, ErrSessionNotReady) {
		t.Fatalf("unknown peer Send error = %v, want %v", err, ErrSessionNotReady)
	}
	if snapshot, ok := b.manager.Snapshot("node-unknown"); ok {
		t.Fatalf("unknown peer allocated snapshot: %+v", snapshot)
	}
	select {
	case peerID := <-requests:
		t.Fatalf("unknown peer triggered request for %s", peerID)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSessionManagerBoundsPendingQueueBytes(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.MTU = MaxOriginalPacketSize
	})
	packet := bytes.Repeat([]byte{0xa5}, MaxOriginalPacketSize)
	for index := 0; index < 40; index++ {
		if err := b.manager.Send("node-c", packet); err != nil {
			t.Fatalf("queue large packet %d: %v", index, err)
		}
	}
	snapshot, ok := b.manager.Snapshot("node-c")
	if !ok || snapshot.PendingPackets != 29 || snapshot.PendingBytes != 29*MaxOriginalPacketSize || snapshot.PendingBytes > pendingByteLimit || snapshot.PendingDropped != 11 {
		t.Fatalf("byte-bounded pending snapshot = %+v, present=%v", snapshot, ok)
	}
}

func TestSessionManagerRejectsSpoofedInboundPacketOverRealQUIC(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	establishSessionTestPair(t, b, c, identities, "session-spoof", 31, strings.Repeat("91", 32))
	spoofed := []byte{
		0x45, 0x00, 0x00, 0x14, 0x02, 0x01, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 9,
		10, 77, 0, 3,
	}
	if err := b.manager.Send("node-c", spoofed); err != nil {
		t.Fatalf("send spoofed packet over authenticated session: %v", err)
	}
	select {
	case delivery := <-c.deliveries:
		t.Fatalf("spoofed packet reached delivery callback: %+v", delivery)
	case <-time.After(150 * time.Millisecond):
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if snapshot, ok := c.manager.Snapshot("node-b"); ok && snapshot.InboundDropped == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, _ := c.manager.Snapshot("node-b")
	t.Fatalf("spoof drop was not counted: %+v", snapshot)
}

func TestSessionManagerExpiresIncompleteReassemblyWithoutMoreTraffic(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	establishSessionTestPair(t, b, c, identities, "session-partial", 32, strings.Repeat("92", 32))
	packet := make([]byte, 1280)
	copy(packet, []byte{
		0x45, 0x00, 0x05, 0x00, 0x02, 0x02, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	})
	fragments, err := FragmentPacket(93, packet, 1280)
	if err != nil || len(fragments) != 2 {
		t.Fatalf("fragment partial packet = %d fragments, err=%v", len(fragments), err)
	}
	b.manager.mu.Lock()
	conn := b.manager.pairs["node-c"].active.conn
	b.manager.mu.Unlock()
	if err := conn.SendDatagram(fragments[0]); err != nil {
		t.Fatalf("send first fragment only: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		peerUsage, _ := c.manager.reassembler.Usage("node-b")
		if peerUsage.Packets == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if peerUsage, _ := c.manager.reassembler.Usage("node-b"); peerUsage.Packets != 1 {
		t.Fatalf("partial fragment usage = %+v, want one packet", peerUsage)
	}
	time.Sleep(DefaultReassemblyExpiry + 500*time.Millisecond)
	if peerUsage, globalUsage := c.manager.reassembler.Usage("node-b"); peerUsage.Packets != 0 || peerUsage.Bytes != 0 || globalUsage.Packets != 0 || globalUsage.Bytes != 0 {
		t.Fatalf("expired partial fragment retained: peer=%+v global=%+v", peerUsage, globalUsage)
	}
}

func TestSessionManagerReplacesOnlyWithAuthenticatedNewerGeneration(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	establishSessionTestPair(t, b, c, identities, "session-stable", 41, strings.Repeat("a1", 32))

	bOffer, cOffer := sessionTestOffers(b, c, identities, "session-rejected-newer", 42, strings.Repeat("a2", 32))
	cOffer.PairingKey = "a3" + cOffer.PairingKey[2:]
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install B rejected-newer offer: %v", err)
	}
	if err := c.manager.InstallOffer(cOffer); err != nil {
		t.Fatalf("install C rejected-newer offer: %v", err)
	}
	b.manager.mu.Lock()
	bRejectedAttempt := b.manager.pairs["node-c"].attempt
	b.manager.mu.Unlock()
	c.manager.mu.Lock()
	cRejectedAttempt := c.manager.pairs["node-b"].attempt
	c.manager.mu.Unlock()
	if err := c.manager.StartOffer(proto.SessionStart{SessionID: cOffer.SessionID, Generation: cOffer.Generation}); err != nil {
		t.Fatalf("start C rejected-newer offer: %v", err)
	}
	if err := b.manager.StartOffer(proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation}); err != nil {
		t.Fatalf("start B rejected-newer offer: %v", err)
	}
	waitSessionAttemptConsumed(t, b.manager, "node-c", 3*time.Second)
	waitSessionAttemptConsumed(t, c.manager, "node-b", 3*time.Second)
	if len(bRejectedAttempt.keyCopy()) != 0 || len(cRejectedAttempt.keyCopy()) != 0 {
		t.Fatal("failed newer authorization retained a pairing key")
	}
	if snapshot := waitSessionState(t, b.manager, "node-c", PathStateLANDirect, time.Second); snapshot.SessionID != "session-stable" || snapshot.Generation != 41 {
		t.Fatalf("failed generation replaced B's healthy session: %+v", snapshot)
	}
	if snapshot := waitSessionState(t, c.manager, "node-b", PathStateLANDirect, time.Second); snapshot.SessionID != "session-stable" || snapshot.Generation != 41 {
		t.Fatalf("failed generation replaced C's healthy session: %+v", snapshot)
	}

	establishSessionTestPair(t, b, c, identities, "session-newer", 43, strings.Repeat("a4", 32))
	for _, endpoint := range []struct {
		manager *SessionManager
		peerID  string
	}{
		{manager: b.manager, peerID: "node-c"},
		{manager: c.manager, peerID: "node-b"},
	} {
		snapshot := waitSessionState(t, endpoint.manager, endpoint.peerID, PathStateLANDirect, time.Second)
		if snapshot.SessionID != "session-newer" || snapshot.Generation != 43 {
			t.Fatalf("newest authenticated session snapshot = %+v", snapshot)
		}
		active := endpoint.manager.ActiveSessions().Sessions
		if len(active) != 1 || active[0].Generation != 43 {
			t.Fatalf("active generation arbitration = %+v", active)
		}
	}
	oldOffer, _ := sessionTestOffers(b, c, identities, "session-old", 42, strings.Repeat("a5", 32))
	if err := b.manager.InstallOffer(oldOffer); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("old generation install error = %v, want %v", err, ErrSessionOfferRejected)
	}
	if err := b.manager.StartOffer(proto.SessionStart{SessionID: "session-newer", Generation: 43}); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("duplicate Start error = %v, want %v", err, ErrSessionOfferRejected)
	}
}

func TestSessionManagerReadySendQueueDropsOldestUnderBurst(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpointWithConfig(t, "node-c", "10.77.0.3", map[string]string{"node-b": "10.77.0.2"}, identities, func(config *SessionManagerConfig) {
		config.DeliverPacket = func(string, []byte) error { return nil }
	})
	establishSessionTestPair(t, b, c, identities, "session-burst", 51, strings.Repeat("b1", 32))
	packet := []byte{
		0x45, 0x00, 0x00, 0x14, 0x04, 0x01, 0x00, 0x00,
		0x40, 0x11, 0x00, 0x00, 10, 77, 0, 2,
		10, 77, 0, 3,
	}
	for index := 0; index < 5000; index++ {
		if err := b.manager.Send("node-c", packet); err != nil {
			t.Fatalf("burst send %d: %v", index, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := b.manager.Snapshot("node-c")
		if ok && snapshot.SendQueueDropped > 0 {
			if snapshot.State != PathStateLANDirect || len(b.manager.ActiveSessions().Sessions) != 1 {
				t.Fatalf("burst changed ready session: %+v", snapshot)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, _ := b.manager.Snapshot("node-c")
	t.Fatalf("5000-packet burst did not exercise the 256-packet drop-oldest queue: %+v", snapshot)
}

func TestSessionManagerAbortOfferConsumesExactGenerationAndConverges(t *testing.T) {
	t.Run("coordinator available requests once", func(t *testing.T) {
		identities := newSessionTestIdentities(t, "node-b", "node-c")
		requests := make(chan string, 4)
		b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
			config.RequestSession = func(peerID string) { requests <- peerID }
		})
		c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
		bOffer, _ := sessionTestOffers(b, c, identities, "session-abort-online", 61, strings.Repeat("c1", 32))
		if err := b.manager.InstallOffer(bOffer); err != nil {
			t.Fatalf("install offer: %v", err)
		}
		b.manager.mu.Lock()
		attempt := b.manager.pairs["node-c"].attempt
		b.manager.mu.Unlock()
		if err := b.manager.AbortOffer(proto.SessionAbort{SessionID: bOffer.SessionID, Generation: bOffer.Generation, Code: "peer_offline"}); err != nil {
			t.Fatalf("abort installed offer: %v", err)
		}
		if len(attempt.keyCopy()) != 0 {
			t.Fatal("aborted offer retained pairing key")
		}
		if err := b.manager.StartOffer(proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation}); !errors.Is(err, ErrSessionOfferRejected) {
			t.Fatalf("start aborted offer error = %v, want %v", err, ErrSessionOfferRejected)
		}
		if snapshot := waitSessionState(t, b.manager, "node-c", PathStateReconnecting, time.Second); snapshot.Generation != 61 {
			t.Fatalf("aborted online snapshot = %+v", snapshot)
		}
		select {
		case peerID := <-requests:
			if peerID != "node-c" {
				t.Fatalf("abort requested %q", peerID)
			}
		case <-time.After(time.Second):
			t.Fatal("online abort did not request a fresh offer")
		}
		select {
		case peerID := <-requests:
			t.Fatalf("online abort immediately duplicated request for %s", peerID)
		case <-time.After(150 * time.Millisecond):
		}
	})

	t.Run("coordinator unavailable waits without request", func(t *testing.T) {
		identities := newSessionTestIdentities(t, "node-b", "node-c")
		requests := make(chan string, 1)
		b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
			config.RequestSession = func(peerID string) { requests <- peerID }
		})
		c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
		bOffer, _ := sessionTestOffers(b, c, identities, "session-abort-offline", 62, strings.Repeat("c2", 32))
		b.manager.SetCoordinatorAvailable(false)
		if err := b.manager.InstallOffer(bOffer); err != nil {
			t.Fatalf("install offer: %v", err)
		}
		if err := b.manager.AbortOffer(proto.SessionAbort{SessionID: bOffer.SessionID, Generation: bOffer.Generation, Code: "peer_offline"}); err != nil {
			t.Fatalf("abort offline offer: %v", err)
		}
		waitSessionState(t, b.manager, "node-c", PathStateWaitingCoordinator, time.Second)
		select {
		case peerID := <-requests:
			t.Fatalf("offline abort requested %s", peerID)
		case <-time.After(150 * time.Millisecond):
		}
	})
}

func TestSessionManagerAbortOfferDoesNotAffectDifferentOrActiveSession(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	establishSessionTestPair(t, b, c, identities, "session-active-before-abort", 70, strings.Repeat("c3", 32))
	bOffer, _ := sessionTestOffers(b, c, identities, "session-pending-abort", 71, strings.Repeat("c4", 32))
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install pending replacement: %v", err)
	}
	b.manager.mu.Lock()
	attempt := b.manager.pairs["node-c"].attempt
	b.manager.mu.Unlock()
	if err := b.manager.AbortOffer(proto.SessionAbort{SessionID: "different-session", Generation: 71, Code: "peer_offline"}); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("different-session abort error = %v, want %v", err, ErrSessionOfferRejected)
	}
	if len(attempt.keyCopy()) != 32 {
		t.Fatal("mismatched abort consumed the current offer")
	}
	if err := b.manager.AbortOffer(proto.SessionAbort{SessionID: bOffer.SessionID, Generation: 70, Code: "peer_offline"}); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("old-generation abort error = %v, want %v", err, ErrSessionOfferRejected)
	}
	if err := b.manager.AbortOffer(proto.SessionAbort{SessionID: bOffer.SessionID, Generation: bOffer.Generation, Code: "peer_offline"}); err != nil {
		t.Fatalf("abort exact pending replacement: %v", err)
	}
	if len(attempt.keyCopy()) != 0 {
		t.Fatal("exact abort retained pending replacement key")
	}
	snapshot := waitSessionState(t, b.manager, "node-c", PathStateLANDirect, time.Second)
	if snapshot.SessionID != "session-active-before-abort" || snapshot.Generation != 70 {
		t.Fatalf("abort harmed active session: %+v", snapshot)
	}
}

func TestSessionManagerCoordinatorLossConsumesUnstartedOffer(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	requests := make(chan string, 2)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	bOffer, _ := sessionTestOffers(b, c, identities, "session-never-started", 72, strings.Repeat("c5", 32))
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install unstarted offer: %v", err)
	}
	b.manager.mu.Lock()
	attempt := b.manager.pairs["node-c"].attempt
	b.manager.mu.Unlock()
	b.manager.SetCoordinatorAvailable(false)
	if snapshot := waitSessionState(t, b.manager, "node-c", PathStateWaitingCoordinator, time.Second); snapshot.Generation != 72 {
		t.Fatalf("coordinator-loss snapshot = %+v", snapshot)
	}
	if len(attempt.keyCopy()) != 0 {
		t.Fatal("coordinator loss retained an unstarted offer key")
	}
	if err := b.manager.StartOffer(proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation}); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("late Start after coordinator loss error = %v, want %v", err, ErrSessionOfferRejected)
	}
	b.manager.SetCoordinatorAvailable(true)
	select {
	case peerID := <-requests:
		if peerID != "node-c" {
			t.Fatalf("coordinator recovery requested %q", peerID)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator recovery did not request a replacement generation")
	}
	select {
	case peerID := <-requests:
		t.Fatalf("coordinator recovery duplicated immediate request for %s", peerID)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSessionManagerHeartbeatTimeoutUsesOnlyAuthenticatedPair(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	_, cOffer := sessionTestOffers(b, c, identities, "session-silent-peer", 73, strings.Repeat("c6", 32))
	if err := c.manager.InstallOffer(cOffer); err != nil {
		t.Fatalf("install silent-peer offer: %v", err)
	}
	if err := c.manager.StartOffer(proto.SessionStart{SessionID: cOffer.SessionID, Generation: cOffer.Generation}); err != nil {
		t.Fatalf("start silent-peer offer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	remote := c.service.LocalAddr()
	conn, err := b.service.Transport().Dial(ctx, remote, b.manager.tlsConfigForPeer("node-c", identities["node-c"].fingerprint), b.manager.quicConfig.Clone())
	if err != nil {
		t.Fatalf("dial silent authenticated peer: %v", err)
	}
	defer func() { _ = conn.CloseWithError(sessionApplicationError, "test complete") }()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open silent peer SessionHello stream: %v", err)
	}
	key, err := decodeSecret(strings.Repeat("c6", 32))
	if err != nil {
		t.Fatalf("decode test pairing key: %v", err)
	}
	if err := exchangeSessionHello(ctx, stream, &sessionWireWriter{}, cOffer.SessionID, cOffer.Generation, "node-b", "node-c", key); err != nil {
		t.Fatalf("authorize silent peer: %v", err)
	}
	zeroBytes(key)
	waitSessionState(t, c.manager, "node-b", PathStateLANDirect, time.Second)
	c.manager.SetCoordinatorAvailable(false)
	snapshot := waitSessionState(t, c.manager, "node-b", PathStateWaitingCoordinator, 2*time.Second)
	if snapshot.ErrorCode != "direct_heartbeat_timeout" {
		t.Fatalf("heartbeat failure code = %q, want direct_heartbeat_timeout; snapshot=%+v", snapshot.ErrorCode, snapshot)
	}
}

func TestSessionManagerRejectsSessionHelloCompletedAfterOfferExpiry(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	_, cOffer := sessionTestOffers(b, c, identities, "session-expiring", 74, strings.Repeat("c7", 32))
	cOffer.ExpiresAt = time.Now().Add(150 * time.Millisecond)
	for index := range cOffer.Candidates {
		cOffer.Candidates[index].ExpiresAt = time.Now().Add(time.Minute)
	}
	if err := c.manager.InstallOffer(cOffer); err != nil {
		t.Fatalf("install expiring offer: %v", err)
	}
	if err := c.manager.StartOffer(proto.SessionStart{SessionID: cOffer.SessionID, Generation: cOffer.Generation}); err != nil {
		t.Fatalf("start expiring offer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := b.service.Transport().Dial(ctx, c.service.LocalAddr(), b.manager.tlsConfigForPeer("node-c", identities["node-c"].fingerprint), b.manager.quicConfig.Clone())
	if err != nil {
		t.Fatalf("complete TLS before offer expiry: %v", err)
	}
	defer func() { _ = conn.CloseWithError(sessionApplicationError, "test complete") }()
	time.Sleep(200 * time.Millisecond)
	stream, err := conn.OpenStreamSync(ctx)
	if err == nil {
		key, decodeErr := decodeSecret(strings.Repeat("c7", 32))
		if decodeErr != nil {
			t.Fatalf("decode test pairing key: %v", decodeErr)
		}
		_ = exchangeSessionHello(ctx, stream, &sessionWireWriter{}, cOffer.SessionID, cOffer.Generation, "node-b", "node-c", key)
		zeroBytes(key)
	}
	assertNeverDirect(t, c, "node-b", 500*time.Millisecond)
	waitSessionAttemptConsumed(t, c.manager, "node-b", 2*time.Second)
}

func TestSessionManagerExpiredStartRequestsNewGeneration(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	clock := newTestClock(time.Now().UTC())
	requests := make(chan string, 2)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.Now = clock.Now
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	bOffer, _ := sessionTestOffers(b, c, identities, "session-expired-start", 75, strings.Repeat("c8", 32))
	bOffer.ExpiresAt = clock.Now().Add(time.Second)
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install soon-expiring offer: %v", err)
	}
	b.manager.mu.Lock()
	attempt := b.manager.pairs["node-c"].attempt
	b.manager.mu.Unlock()
	clock.Advance(2 * time.Second)
	if err := b.manager.StartOffer(proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation}); !errors.Is(err, ErrSessionOfferRejected) {
		t.Fatalf("expired Start error = %v, want %v", err, ErrSessionOfferRejected)
	}
	if len(attempt.keyCopy()) != 0 {
		t.Fatal("expired Start retained pairing key")
	}
	if snapshot := waitSessionState(t, b.manager, "node-c", PathStateReconnecting, time.Second); snapshot.ErrorCode != "session_authorization_failed" {
		t.Fatalf("expired Start snapshot = %+v", snapshot)
	}
	select {
	case peerID := <-requests:
		if peerID != "node-c" {
			t.Fatalf("expired Start requested %q", peerID)
		}
	case <-time.After(time.Second):
		t.Fatal("expired Start did not request a new generation")
	}
	select {
	case peerID := <-requests:
		t.Fatalf("expired Start immediately duplicated request for %s", peerID)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSessionManagerRejectsWrongCAFingerprintNodeKeyAndGeneration(t *testing.T) {
	tests := []struct {
		name       string
		identities func(*testing.T) map[string]sessionTestIdentity
		mutate     func(*proto.SessionOffer, *proto.SessionOffer)
	}{
		{
			name: "wrong CA",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				b := newSessionTestIdentities(t, "node-b")
				c := newSessionTestIdentities(t, "node-c")
				return map[string]sessionTestIdentity{"node-b": b["node-b"], "node-c": c["node-c"]}
			},
			mutate: func(_, _ *proto.SessionOffer) {},
		},
		{
			name: "expired peer certificate",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				return newSessionTestIdentitiesWithDays(t, map[string]int{"node-c": -1}, "node-b", "node-c")
			},
			mutate: func(_, _ *proto.SessionOffer) {},
		},
		{
			name: "peer certificate lacks server authentication usage",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				return newSessionTestIdentitiesWithUsages(t, map[string][]x509.ExtKeyUsage{
					"node-c": {x509.ExtKeyUsageClientAuth},
				}, "node-b", "node-c")
			},
			mutate: func(_, _ *proto.SessionOffer) {},
		},
		{
			name: "dialer certificate lacks client authentication usage",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				return newSessionTestIdentitiesWithUsages(t, map[string][]x509.ExtKeyUsage{
					"node-b": {x509.ExtKeyUsageServerAuth},
				}, "node-b", "node-c")
			},
			mutate: func(_, _ *proto.SessionOffer) {},
		},
		{
			name: "wrong production fingerprint",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				return newSessionTestIdentities(t, "node-b", "node-c")
			},
			mutate: func(bOffer, _ *proto.SessionOffer) {
				if strings.HasSuffix(bOffer.PeerFingerprint, ":00") {
					bOffer.PeerFingerprint = strings.TrimSuffix(bOffer.PeerFingerprint, ":00") + ":01"
				} else {
					bOffer.PeerFingerprint = bOffer.PeerFingerprint[:len(bOffer.PeerFingerprint)-2] + "00"
				}
			},
		},
		{
			name: "certificate CN differs from offered node",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				return newSessionTestIdentities(t, "node-b", "node-c")
			},
			mutate: func(bOffer, _ *proto.SessionOffer) {
				bOffer.PeerNodeID = "node-c-impersonated"
			},
		},
		{
			name: "pairing key differs by one byte",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				return newSessionTestIdentities(t, "node-b", "node-c")
			},
			mutate: func(_, cOffer *proto.SessionOffer) {
				cOffer.PairingKey = "43" + cOffer.PairingKey[2:]
			},
		},
		{
			name: "generation differs",
			identities: func(t *testing.T) map[string]sessionTestIdentity {
				return newSessionTestIdentities(t, "node-b", "node-c")
			},
			mutate: func(_, cOffer *proto.SessionOffer) {
				cOffer.Generation++
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identities := test.identities(t)
			b := newSessionTestEndpoint(t, "node-b", "10.77.0.2", "node-c", "10.77.0.3", identities)
			c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
			bOffer, cOffer := sessionTestOffers(b, c, identities, "session-negative", 11, strings.Repeat("5a", 32))
			test.mutate(&bOffer, &cOffer)
			if err := b.manager.InstallOffer(bOffer); err != nil {
				t.Fatalf("install B offer: %v", err)
			}
			if err := c.manager.InstallOffer(cOffer); err != nil {
				t.Fatalf("install C offer: %v", err)
			}
			b.manager.mu.Lock()
			bAttempt := b.manager.pairs[bOffer.PeerNodeID].attempt
			b.manager.mu.Unlock()
			c.manager.mu.Lock()
			cAttempt := c.manager.pairs[cOffer.PeerNodeID].attempt
			c.manager.mu.Unlock()
			if err := c.manager.StartOffer(proto.SessionStart{SessionID: cOffer.SessionID, Generation: cOffer.Generation}); err != nil {
				t.Fatalf("start C offer: %v", err)
			}
			if err := b.manager.StartOffer(proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation}); err != nil {
				t.Fatalf("start B offer: %v", err)
			}
			assertNeverDirect(t, b, bOffer.PeerNodeID, 1200*time.Millisecond)
			assertNeverDirect(t, c, cOffer.PeerNodeID, 100*time.Millisecond)
			waitSessionAttemptConsumed(t, b.manager, bOffer.PeerNodeID, 3*time.Second)
			waitSessionAttemptConsumed(t, c.manager, cOffer.PeerNodeID, 3*time.Second)
			if len(bAttempt.keyCopy()) != 0 || len(cAttempt.keyCopy()) != 0 {
				t.Fatal("failed authorization retained a pairing key")
			}
		})
	}
}

func TestSessionManagerExpiresUnstartedOfferAndZerosKey(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	clock := newTestClock(time.Now().UTC())
	requests := make(chan string, 2)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.Now = clock.Now
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	bOffer, _ := sessionTestOffers(b, c, identities, "session-unstarted-expiry", 81, strings.Repeat("d1", 32))
	bOffer.ExpiresAt = clock.Now().Add(time.Second)
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install expiring unstarted offer: %v", err)
	}
	b.manager.mu.Lock()
	attempt := b.manager.pairs["node-c"].attempt
	b.manager.mu.Unlock()

	clock.Advance(2 * time.Second)
	waitSessionAttemptConsumed(t, b.manager, "node-c", time.Second)
	if len(attempt.keyCopy()) != 0 {
		t.Fatal("expired unstarted offer retained its pairing key")
	}
	if snapshot := waitSessionState(t, b.manager, "node-c", PathStateReconnecting, time.Second); snapshot.SessionID != bOffer.SessionID {
		t.Fatalf("expired unstarted offer snapshot = %+v", snapshot)
	}
	select {
	case peerID := <-requests:
		if peerID != "node-c" {
			t.Fatalf("expired offer requested %q", peerID)
		}
	case <-time.After(time.Second):
		t.Fatal("expired unstarted offer did not request a fresh generation")
	}
}

func TestSessionManagerAbortRequestsAtMostOnceBeyondRetryWindow(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	requests := make(chan string, 4)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	bOffer, _ := sessionTestOffers(b, c, identities, "session-abort-once", 82, strings.Repeat("d2", 32))
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install abort-once offer: %v", err)
	}
	if err := b.manager.AbortOffer(proto.SessionAbort{SessionID: bOffer.SessionID, Generation: bOffer.Generation, Code: "candidate_unavailable"}); err != nil {
		t.Fatalf("abort offer: %v", err)
	}
	select {
	case peerID := <-requests:
		if peerID != "node-c" {
			t.Fatalf("abort requested %q", peerID)
		}
	case <-time.After(time.Second):
		t.Fatal("online abort did not request a fresh generation")
	}
	select {
	case peerID := <-requests:
		t.Fatalf("abort retried RequestSession for %s without new traffic", peerID)
	case <-time.After(1200 * time.Millisecond):
	}
}

func TestSessionManagerKeepsNewerAttemptSingleFlightWhenOldSessionEnds(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	requests := make(chan string, 2)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.RequestSession = func(peerID string) { requests <- peerID }
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	establishSessionTestPair(t, b, c, identities, "session-old-active", 83, strings.Repeat("d3", 32))

	b.manager.mu.Lock()
	old := b.manager.pairs["node-c"].active
	b.manager.mu.Unlock()
	bOffer, cOffer := sessionTestOffers(b, c, identities, "session-new-auth", 84, strings.Repeat("d4", 32))
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install B replacement offer: %v", err)
	}
	if err := c.manager.InstallOffer(cOffer); err != nil {
		t.Fatalf("install C replacement offer: %v", err)
	}
	if err := b.manager.StartOffer(proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation}); err != nil {
		t.Fatalf("start B replacement offer: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.manager.mu.Lock()
		attempt := b.manager.pairs["node-c"].attempt
		claimed := attempt != nil && attempt.claimed
		b.manager.mu.Unlock()
		if claimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement attempt never entered SessionHello authentication")
		}
		time.Sleep(10 * time.Millisecond)
	}

	old.stop("direct_unreachable_no_relay")
	snapshot, ok := b.manager.Snapshot("node-c")
	if !ok || snapshot.State != PathStateAuthenticating || snapshot.SessionID != bOffer.SessionID || snapshot.Generation != bOffer.Generation {
		t.Fatalf("old-session loss discarded in-progress replacement: %+v, present=%v", snapshot, ok)
	}
	select {
	case peerID := <-requests:
		t.Fatalf("old-session loss requested a third generation for %s", peerID)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSessionManagerCloseWaitsForOfferStartedBeforeStateCallback(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	closeReturned := make(chan error, 1)
	releaseCallback := make(chan struct{})
	var manager *SessionManager
	var once atomic.Bool
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(config *SessionManagerConfig) {
		config.SessionChanged = func(snapshot SessionSnapshot) {
			if snapshot.State == PathStatePunching && once.CompareAndSwap(false, true) {
				closeReturned <- manager.Close()
				<-releaseCallback
			}
		}
	})
	manager = b.manager
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	bOffer, _ := sessionTestOffers(b, c, identities, "session-close-start", 85, strings.Repeat("d5", 32))
	if err := b.manager.InstallOffer(bOffer); err != nil {
		t.Fatalf("install close/start offer: %v", err)
	}
	b.manager.mu.Lock()
	attempt := b.manager.pairs["node-c"].attempt
	b.manager.mu.Unlock()
	startResult := make(chan error, 1)
	go func() {
		startResult <- b.manager.StartOffer(proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation})
	}()

	select {
	case err := <-closeReturned:
		if err != nil {
			t.Errorf("close from state callback: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(releaseCallback)
		t.Fatal("Close from punching callback did not return")
	}
	startedBeforeCloseReturned := false
	select {
	case <-attempt.punchDone:
		startedBeforeCloseReturned = true
	default:
	}
	close(releaseCallback)
	if err := <-startResult; err != nil {
		t.Fatalf("StartOffer result: %v", err)
	}
	if !startedBeforeCloseReturned {
		t.Fatal("Close returned before the accepted offer goroutine was started and joined")
	}
}

func TestSessionManagerCloseNotificationCanReenterClose(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	var manager *SessionManager
	reentered := make(chan error, 1)
	var once atomic.Bool
	service, created := newUnmanagedSessionTestManager(t, identities["node-b"], func(string) {}, func(snapshot SessionSnapshot) {
		if snapshot.State == PathStateClosed && once.CompareAndSwap(false, true) {
			reentered <- manager.Close()
		}
	})
	manager = created
	if err := manager.Send("node-c", []byte{0x45}); err != nil {
		t.Fatalf("create observable pair: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("outer Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("outer Close deadlocked in its close-state callback")
		return
	}
	select {
	case err := <-reentered:
		if err != nil {
			t.Errorf("reentrant Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("close-state callback did not complete reentrant Close")
		return
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close callback test candidate service: %v", err)
	}
}

func TestSessionManagerRetryCallbackCanCloseManager(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	var manager *SessionManager
	closeResult := make(chan error, 1)
	var requests atomic.Int32
	service, created := newUnmanagedSessionTestManager(t, identities["node-b"], func(string) {
		if requests.Add(1) == 2 {
			closeResult <- manager.Close()
		}
	}, nil)
	manager = created
	if err := manager.Send("node-c", []byte{0x45}); err != nil {
		t.Fatalf("queue packet for retry callback: %v", err)
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Errorf("Close from retry callback: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("RequestSession retry callback deadlocked while closing its manager")
		return
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close retry callback candidate service: %v", err)
	}
}

func TestCandidateServiceRefusesCloseWhileSessionManagerOwnsListener(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b")
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, nil)
	if err := b.service.Close(); err == nil || !strings.Contains(err.Error(), "session manager") {
		t.Fatalf("candidate Close while claimed error = %v", err)
	}
	if err := b.service.checkOpen(); err != nil {
		t.Fatalf("refused CandidateService.Close still closed the service: %v", err)
	}
}

func TestNewSessionManagerRejectsCandidateListenerIdentityMismatch(t *testing.T) {
	listenerIdentity := newSessionTestIdentities(t, "node-b")["node-b"]
	managerIdentity := newSessionTestIdentities(t, "node-b")["node-b"]
	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:        "node-b",
		NetworkID:     "network-1",
		Listen:        "127.0.0.1:0",
		TLSConfig:     listenerIdentity.tlsConfig,
		QUICConfig:    &quic.Config{EnableDatagrams: true},
		EnumerateIPv4: func() ([]netip.Addr, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new mismatched candidate service: %v", err)
	}
	defer func() { _ = service.Close() }()
	manager, err := NewSessionManager(SessionManagerConfig{
		NodeID:         "node-b",
		NetworkID:      "network-1",
		MTU:            1280,
		Candidates:     service,
		TLSConfig:      managerIdentity.tlsConfig,
		LocalVirtualIP: netip.MustParseAddr("10.77.0.2"),
		PeerMember: func(peerID string) (proto.Member, bool) {
			return proto.Member{NodeID: peerID, VirtualIP: "10.77.0.3"}, true
		},
		DeliverPacket: func(string, []byte) error { return nil },
	})
	if manager != nil {
		_ = manager.Close()
	}
	if err == nil {
		t.Fatal("SessionManager accepted a different certificate/CA profile from its existing QUIC listener")
	}
}

func newSessionTestEndpoint(t *testing.T, nodeID, virtualIP, peerID, peerVirtualIP string, identities map[string]sessionTestIdentity) sessionTestEndpoint {
	t.Helper()
	return newSessionTestEndpointWithPeers(t, nodeID, virtualIP, map[string]string{peerID: peerVirtualIP}, identities)
}

func newUnmanagedSessionTestManager(t *testing.T, identity sessionTestIdentity, request func(string), changed func(SessionSnapshot)) (*CandidateService, *SessionManager) {
	t.Helper()
	quicConfig := &quic.Config{
		EnableDatagrams: true,
		KeepAlivePeriod: 15 * time.Second,
		MaxIdleTimeout:  45 * time.Second,
	}
	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:        "node-b",
		NetworkID:     "network-1",
		Listen:        "127.0.0.1:0",
		TLSConfig:     identity.tlsConfig,
		QUICConfig:    quicConfig,
		EnumerateIPv4: func() ([]netip.Addr, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new unmanaged candidate service: %v", err)
	}
	manager, err := NewSessionManager(SessionManagerConfig{
		NodeID:         "node-b",
		NetworkID:      "network-1",
		MTU:            1280,
		Candidates:     service,
		TLSConfig:      identity.tlsConfig,
		QUICConfig:     quicConfig,
		LocalVirtualIP: netip.MustParseAddr("10.77.0.2"),
		PeerMember: func(peerID string) (proto.Member, bool) {
			return proto.Member{NodeID: peerID, VirtualIP: "10.77.0.3"}, true
		},
		RequestSession: request,
		DeliverPacket:  func(string, []byte) error { return nil },
		SessionChanged: changed,
	})
	if err != nil {
		_ = service.Close()
		t.Fatalf("new unmanaged session manager: %v", err)
	}
	return service, manager
}

func newSessionTestEndpointWithPeers(t *testing.T, nodeID, virtualIP string, peerVirtualIPs map[string]string, identities map[string]sessionTestIdentity) sessionTestEndpoint {
	t.Helper()
	return newSessionTestEndpointWithConfig(t, nodeID, virtualIP, peerVirtualIPs, identities, nil)
}

func newSessionTestEndpointWithConfig(t *testing.T, nodeID, virtualIP string, peerVirtualIPs map[string]string, identities map[string]sessionTestIdentity, configure func(*SessionManagerConfig)) sessionTestEndpoint {
	t.Helper()
	quicConfig := &quic.Config{
		EnableDatagrams:      true,
		KeepAlivePeriod:      15 * time.Second,
		MaxIdleTimeout:       45 * time.Second,
		HandshakeIdleTimeout: time.Second,
	}
	service, err := NewCandidateService(CandidateServiceConfig{
		NodeID:        nodeID,
		NetworkID:     "network-1",
		Listen:        "127.0.0.1:0",
		TLSConfig:     identities[nodeID].tlsConfig,
		QUICConfig:    quicConfig,
		EnumerateIPv4: func() ([]netip.Addr, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new %s candidate service: %v", nodeID, err)
	}
	deliveries := make(chan sessionTestDelivery, 32)
	changes := make(chan SessionSnapshot, 128)
	managerConfig := SessionManagerConfig{
		NodeID:         nodeID,
		NetworkID:      "network-1",
		MTU:            1280,
		Candidates:     service,
		TLSConfig:      identities[nodeID].tlsConfig,
		QUICConfig:     quicConfig,
		LocalVirtualIP: netip.MustParseAddr(virtualIP),
		PeerMember: func(gotPeerID string) (proto.Member, bool) {
			peerVirtualIP, ok := peerVirtualIPs[gotPeerID]
			if !ok {
				return proto.Member{}, false
			}
			return proto.Member{NodeID: gotPeerID, VirtualIP: peerVirtualIP}, true
		},
		DeliverPacket: func(gotPeerID string, packet []byte) error {
			deliveries <- sessionTestDelivery{peerID: gotPeerID, packet: append([]byte(nil), packet...)}
			return nil
		},
		SessionChanged: func(snapshot SessionSnapshot) {
			select {
			case changes <- snapshot:
			default:
			}
		},
		HeartbeatInterval: 50 * time.Millisecond,
		HeartbeatTimeout:  500 * time.Millisecond,
		DialTimeout:       2 * time.Second,
	}
	if configure != nil {
		configure(&managerConfig)
	}
	manager, err := NewSessionManager(managerConfig)
	if err != nil {
		_ = service.Close()
		t.Fatalf("new %s session manager: %v", nodeID, err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close %s session manager: %v", nodeID, err)
		}
		if err := service.Close(); err != nil {
			t.Errorf("close %s candidate service: %v", nodeID, err)
		}
	})
	return sessionTestEndpoint{nodeID: nodeID, service: service, manager: manager, deliveries: deliveries, changes: changes}
}

func establishSessionTestPair(t *testing.T, left, right sessionTestEndpoint, identities map[string]sessionTestIdentity, sessionID string, generation uint64, pairingKey string) (*sessionAttempt, *sessionAttempt) {
	t.Helper()
	dialer := left.nodeID
	if right.nodeID < dialer {
		dialer = right.nodeID
	}
	expiresAt := time.Now().Add(time.Minute)
	leftOffer := proto.SessionOffer{
		SessionID:       sessionID,
		Generation:      generation,
		PeerNodeID:      right.nodeID,
		PeerFingerprint: identities[right.nodeID].fingerprint,
		DialerNodeID:    dialer,
		Candidates:      []proto.Candidate{sessionTestCandidate(right.service, "lan", expiresAt)},
		ExpiresAt:       expiresAt,
		PairingKey:      pairingKey,
	}
	rightOffer := proto.SessionOffer{
		SessionID:       sessionID,
		Generation:      generation,
		PeerNodeID:      left.nodeID,
		PeerFingerprint: identities[left.nodeID].fingerprint,
		DialerNodeID:    dialer,
		Candidates:      []proto.Candidate{sessionTestCandidate(left.service, "lan", expiresAt)},
		ExpiresAt:       expiresAt,
		PairingKey:      pairingKey,
	}
	if err := left.manager.InstallOffer(leftOffer); err != nil {
		t.Fatalf("install %s offer: %v", left.nodeID, err)
	}
	if err := right.manager.InstallOffer(rightOffer); err != nil {
		t.Fatalf("install %s offer: %v", right.nodeID, err)
	}
	left.manager.mu.Lock()
	leftAttempt := left.manager.pairs[right.nodeID].attempt
	left.manager.mu.Unlock()
	right.manager.mu.Lock()
	rightAttempt := right.manager.pairs[left.nodeID].attempt
	right.manager.mu.Unlock()
	start := proto.SessionStart{SessionID: sessionID, Generation: generation}
	if dialer == left.nodeID {
		if err := right.manager.StartOffer(start); err != nil {
			t.Fatalf("start %s offer: %v", right.nodeID, err)
		}
		if err := left.manager.StartOffer(start); err != nil {
			t.Fatalf("start %s offer: %v", left.nodeID, err)
		}
	} else {
		if err := left.manager.StartOffer(start); err != nil {
			t.Fatalf("start %s offer: %v", left.nodeID, err)
		}
		if err := right.manager.StartOffer(start); err != nil {
			t.Fatalf("start %s offer: %v", right.nodeID, err)
		}
	}
	waitSessionGeneration(t, left.manager, right.nodeID, sessionID, generation, 5*time.Second)
	waitSessionGeneration(t, right.manager, left.nodeID, sessionID, generation, 5*time.Second)
	return leftAttempt, rightAttempt
}

func sessionTestOffers(b, c sessionTestEndpoint, identities map[string]sessionTestIdentity, sessionID string, generation uint64, pairingKey string) (proto.SessionOffer, proto.SessionOffer) {
	expiresAt := time.Now().Add(time.Minute)
	return proto.SessionOffer{
			SessionID:       sessionID,
			Generation:      generation,
			PeerNodeID:      "node-c",
			PeerFingerprint: identities["node-c"].fingerprint,
			DialerNodeID:    "node-b",
			Candidates:      []proto.Candidate{sessionTestCandidate(c.service, "lan", expiresAt)},
			ExpiresAt:       expiresAt,
			PairingKey:      pairingKey,
		}, proto.SessionOffer{
			SessionID:       sessionID,
			Generation:      generation,
			PeerNodeID:      "node-b",
			PeerFingerprint: identities["node-b"].fingerprint,
			DialerNodeID:    "node-b",
			Candidates:      []proto.Candidate{sessionTestCandidate(b.service, "lan", expiresAt)},
			ExpiresAt:       expiresAt,
			PairingKey:      pairingKey,
		}
}

func sessionTestCandidate(service *CandidateService, scope string, expiresAt time.Time) proto.Candidate {
	address := service.LocalAddr()
	return proto.Candidate{
		Address:   address.IP.String(),
		Port:      uint16(address.Port),
		Scope:     scope,
		Priority:  100,
		ExpiresAt: expiresAt,
	}
}

func waitSessionState(t *testing.T, manager *SessionManager, peerID string, want PathState, timeout time.Duration) SessionSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if snapshot, ok := manager.Snapshot(peerID); ok && snapshot.State == want {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, ok := manager.Snapshot(peerID)
	t.Fatalf("%s state = %+v, present=%v; want %s", peerID, snapshot, ok, want)
	return SessionSnapshot{}
}

func waitSessionGeneration(t *testing.T, manager *SessionManager, peerID, sessionID string, generation uint64, timeout time.Duration) SessionSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if snapshot, ok := manager.Snapshot(peerID); ok && isDirectSessionState(snapshot.State) && snapshot.SessionID == sessionID && snapshot.Generation == generation {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, ok := manager.Snapshot(peerID)
	t.Fatalf("%s session = %+v, present=%v; want %s generation %d direct", peerID, snapshot, ok, sessionID, generation)
	return SessionSnapshot{}
}

func waitSessionAttemptConsumed(t *testing.T, manager *SessionManager, peerID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		pair := manager.pairs[peerID]
		consumed := pair != nil && pair.attempt == nil
		manager.mu.Unlock()
		if consumed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager.mu.Lock()
	pair := manager.pairs[peerID]
	manager.mu.Unlock()
	t.Fatalf("%s attempt was not consumed: %+v", peerID, pair)
}

func assertSessionDelivery(t *testing.T, deliveries <-chan sessionTestDelivery, peerID string, packet []byte) {
	t.Helper()
	select {
	case delivery := <-deliveries:
		if delivery.peerID != peerID || !bytes.Equal(delivery.packet, packet) {
			t.Fatalf("delivery = peer %q packet %x, want peer %q packet %x", delivery.peerID, delivery.packet, peerID, packet)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for packet from %s", peerID)
	}
}

func assertSessionTransportSecurity(t *testing.T, manager *SessionManager, peerID string) {
	t.Helper()
	manager.mu.Lock()
	pair := manager.pairs[peerID]
	var conn *quic.Conn
	if pair != nil && pair.active != nil {
		conn = pair.active.conn
	}
	manager.mu.Unlock()
	if conn == nil {
		t.Fatalf("%s has no real QUIC connection", peerID)
	}
	state := conn.ConnectionState()
	if state.TLS.Version != tls.VersionTLS13 || state.TLS.NegotiatedProtocol != sessionALPN || state.Used0RTT || !state.SupportsDatagrams.Local || !state.SupportsDatagrams.Remote {
		t.Fatalf("%s insecure QUIC state = %+v", peerID, state)
	}
}

func assertNeverDirect(t *testing.T, endpoint sessionTestEndpoint, peerID string, duration time.Duration) {
	t.Helper()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		select {
		case snapshot := <-endpoint.changes:
			if snapshot.PeerNodeID == peerID && isDirectSessionState(snapshot.State) {
				t.Fatalf("unauthorized pair reached direct state: %+v", snapshot)
			}
		case <-deadline.C:
			if snapshot, ok := endpoint.manager.Snapshot(peerID); ok && isDirectSessionState(snapshot.State) {
				t.Fatalf("unauthorized pair ended direct: %+v", snapshot)
			}
			return
		}
	}
}
