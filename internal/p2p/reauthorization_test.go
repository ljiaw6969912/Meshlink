package p2p

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meshlink/internal/proto"
)

func TestReassemblyDoesNotMixCertificateGenerations(t *testing.T) {
	r := NewReassembler(1280)
	oldPacket := bytes.Repeat([]byte{0x31}, 1280)
	newPacket := bytes.Repeat([]byte{0x42}, 1280)
	oldParts, _ := FragmentPacket(1, oldPacket, 1280)
	newParts, _ := FragmentPacket(1, newPacket, 1280)
	now := time.Now()
	if _, complete, err := r.addForGeneration("peer", 100, oldParts[0], now); err != nil || complete {
		t.Fatalf("first old fragment: complete=%v err=%v", complete, err)
	}
	if _, complete, err := r.addForGeneration("peer", 101, newParts[1], now); err != nil || complete {
		t.Fatalf("cross-generation fragments combined: complete=%v err=%v", complete, err)
	}
	if usage, _ := r.Usage("peer"); usage.Packets != 2 || usage.Bytes != 1280 {
		t.Fatalf("generation isolation bypassed per-peer accounting: %+v", usage)
	}
	got, complete, err := r.addForGeneration("peer", 101, newParts[0], now)
	if err != nil || !complete || !bytes.Equal(got, newPacket) {
		t.Fatalf("fresh session reassembly: complete=%v err=%v", complete, err)
	}
}

func TestReauthorizePeerRequiresOnlineMembershipAndRejectsSpentOffers(t *testing.T) {
	identities := newSessionTestIdentities(t, "node-b", "node-c")
	var authorization atomic.Int32
	authorization.Store(2)
	b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", nil, identities, func(cfg *SessionManagerConfig) {
		cfg.PeerMember = func(id string) (proto.Member, bool) {
			state := authorization.Load()
			status := "offline"
			if state == 2 {
				status = "online"
			}
			return proto.Member{NodeID: id, Status: status}, id == "node-c" && state != 0
		}
	})
	c := newSessionTestEndpoint(t, "node-c", "10.77.0.3", "node-b", "10.77.0.2", identities)
	oldOffer, _ := sessionTestOffers(b, c, identities, "old-offer", 100, strings.Repeat("91", 32))
	if err := b.manager.InstallOffer(oldOffer); err != nil {
		t.Fatal(err)
	}
	b.manager.mu.Lock()
	oldAttempt := b.manager.pairs["node-c"].attempt
	b.manager.mu.Unlock()
	if err := b.manager.ClosePeer("node-c", "peer_revoked"); err != nil {
		t.Fatal(err)
	}
	for _, state := range []int32{0, 1} {
		authorization.Store(state)
		if err := b.manager.ReauthorizePeer("node-c"); err == nil {
			t.Fatalf("authorization state %d restored a revoked peer", state)
		}
		if got, _ := b.manager.Snapshot("node-c"); got.State != PathStateClosed {
			t.Fatalf("rejected restoration changed closed state: %+v", got)
		}
	}
	authorization.Store(2)
	if err := b.manager.ReauthorizePeer("node-c"); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.manager.Snapshot("node-c"); got.State != PathStateIdle || got.SessionID != "" {
		t.Fatalf("restoration reused old session: %+v", got)
	}
	if err := b.manager.InstallOffer(oldOffer); err == nil {
		t.Fatal("restoration allowed replay of a consumed generation")
	}
	newOffer, _ := sessionTestOffers(b, c, identities, "new-offer", 101, strings.Repeat("92", 32))
	if err := b.manager.InstallOffer(newOffer); err != nil {
		t.Fatal(err)
	}
	// A timed-out worker from the old identity must not fail a newer offer.
	b.manager.failAttempt("node-c", oldAttempt, "quic_handshake_failed")
	if got, _ := b.manager.Snapshot("node-c"); got.SessionID != "new-offer" || got.State != PathStatePreparing {
		t.Fatalf("old completion damaged new authorization: %+v", got)
	}
}
