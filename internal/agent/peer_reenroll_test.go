package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/onboarding"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

func TestAuthorizedMembershipRestoresRevokedPeer(t *testing.T) {
	for _, removed := range []bool{false, true} {
		name := "certificate_changed"
		if removed {
			name = "readmitted_after_disconnect"
		}
		t.Run(name, func(t *testing.T) {
			f := newRuntimeFixture(t, "B", "C")
			r := f.r["B"]
			member, _ := r.member("C")
			if removed {
				if err := r.disconnectPeer(proto.DisconnectPeer{NodeID: "C", Code: "member_revoked"}); err != nil {
					t.Fatal(err)
				}
				// Absence or offline presence must never undo a revocation.
				offline := member
				offline.Status = "offline"
				if err := r.applyMembers(proto.MemberSnapshot{Revision: 2, Members: []proto.Member{offline}}); err != nil {
					t.Fatal(err)
				}
				if _, ok := r.member("C"); ok {
					t.Fatal("offline presence restored revoked authorization")
				}
			} else {
				member.Fingerprint = strings.Repeat("ab", 32)
			}
			if err := r.applyMembers(proto.MemberSnapshot{Revision: 3, Members: []proto.Member{member}}); err != nil {
				t.Fatal(err)
			}
			if got, ok := r.member("C"); !ok || got.Fingerprint != member.Fingerprint {
				t.Fatalf("fresh authorization stayed revoked: member=%+v authorized=%v", got, ok)
			}
			if err := r.sessions.EnsureSession("C"); err != nil {
				t.Fatal(err)
			}
			if got, ok := r.sessions.Snapshot("C"); !ok || got.State != p2p.PathStateWaitingCoordinator {
				t.Fatalf("reauthorized pair cannot request a new session: %+v", got)
			}
		})
	}
}

func TestDelayedRevokedSessionPacketCannotUseNewMembership(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	r := f.r["B"]
	member, _ := r.member("C")
	oldContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.disconnectPeer(proto.DisconnectPeer{NodeID: "C"}); err != nil {
		t.Fatal(err)
	}
	cancel() // Session teardown cancels this before the new identity is admitted.
	member.Fingerprint = strings.Repeat("ac", 32)
	if err := r.applyMembers(proto.MemberSnapshot{Revision: 3, Members: []proto.Member{member}}); err != nil {
		t.Fatal(err)
	}
	packet := runtimePacket(3, 2)
	if err := r.deliverPacketContext(oldContext, "C", packet); !errors.Is(err, context.Canceled) {
		t.Fatalf("delayed old packet accepted after readmission: %v", err)
	}
	select {
	case <-f.devices["B"].written:
		t.Fatal("old session wrote a packet under the new identity")
	default:
	}
	if err := r.deliverPacketContext(context.Background(), "C", packet); err != nil {
		t.Fatalf("fresh session delivery rejected: %v", err)
	}
	assertRuntimeDelivery(t, f.devices["B"], packet)
}

// Exercise real same-MAC enrollment and certificate rotation while every other
// peer process remains running. No user packet should be needed to reconnect.
func TestMACReenrollmentReconnectsAllOnlinePeersWithoutRestart(t *testing.T) {
	h := newPureP2PHarnessWithPeers(t, []string{"B", "C", "D", "E"})
	h.startPeers("B", "C", "D", "E")
	for _, id := range []string{"B", "C", "D"} {
		waitDirectPair(t, h.peers[id], "E", h.peers["E"], id)
	}
	bcBefore, _ := waitDirectPair(t, h.peers["B"], "C", h.peers["C"], "B")
	manager := onboarding.Manager{BaseDir: h.dir}
	initial, err := manager.LookupDevice("E")
	if err != nil || initial.MACAddress == "" {
		t.Fatalf("initial MAC binding: %+v %v", initial, err)
	}
	invite, err := manager.CreateInvite(onboarding.CreateInviteRequest{Server: h.recorder.addr, LongLived: true, MaxUses: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"renamed-E", "renamed-again-E"} {
		old := h.peers["E"]
		if err := old.process.stop(); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		delete(h.peers, "E")
		csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: name})
		if err != nil {
			t.Fatal(err)
		}
		enrolled, err := manager.HandleEnroll(onboarding.EnrollRequest{
			Token: invite.Token, Code: invite.Code, NodeName: name, DisplayName: name,
			MACAddress: initial.MACAddress, CSRPEM: csr.CSRPEM,
		})
		if err != nil {
			t.Fatal(err)
		}
		if enrolled.Config.NodeID != "E" || enrolled.Config.VirtualIP != "10.77.0.5" {
			t.Fatalf("same MAC changed identity/IP: %+v", enrolled.Config)
		}
		key, err := os.ReadFile(csr.KeyPath)
		if err != nil {
			t.Fatal(err)
		}
		for path, data := range map[string][]byte{"E.pem": enrolled.CertPEM, "E-key.pem": key} {
			if err := os.WriteFile(filepath.Join(h.certDir, path), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		// Wait for the real coordinator's registry watcher to revoke the old
		// certificate before the replacement appears in an authorized snapshot.
		for _, id := range []string{"B", "C", "D"} {
			integrationEventually(t, 5*time.Second, func() (bool, string) {
				s, ok := h.peers[id].runtime.sessions.Snapshot("E")
				return ok && s.State == p2p.PathStateClosed, "old E certificate was not revoked"
			})
		}
		h.startPeers("E")
		for _, id := range []string{"B", "C", "D"} {
			waitDirectPair(t, h.peers[id], "E", h.peers["E"], id)
		}
		injectAndExpect(t, h.peers["B"], h.peers["E"], runtimePacket(2, 5))
		injectAndExpect(t, h.peers["E"], h.peers["B"], runtimePacket(5, 2))
	}
	bcAfter, _ := waitDirectPair(t, h.peers["B"], "C", h.peers["C"], "B")
	if bcBefore.SessionID != bcAfter.SessionID || bcBefore.Generation != bcAfter.Generation {
		t.Fatal("E reenrollment disturbed the unrelated B/C connection")
	}
	nodes, err := manager.LoadRegisteredNodes()
	if err != nil || len(nodes) != 4 {
		t.Fatalf("reenrollment duplicated device records: count=%d err=%v", len(nodes), err)
	}
}

func TestControlReadmissionAppliesOldRevocationsBeforeFreshMembership(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	coordinatorCompleteSession(t, b, c)
	_ = b.conn.Close()
	runtimeEventually(t, func() bool {
		f.coordinator.mu.Lock()
		defer f.coordinator.mu.Unlock()
		return f.coordinator.peers["B"] == nil
	})
	certs := filepath.Join(f.dir, "certs")
	if _, err := certutil.Issue(certutil.IssueOptions{OutDir: certs, Name: "C", CAPath: filepath.Join(certs, "ca.pem"), CAKeyPath: filepath.Join(certs, "ca-key.pem"), IPAddrs: []string{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	_, fingerprint := certInfoFromFile(filepath.Join(certs, "C.pem"))
	f.nodes[1]["cert_fingerprint"] = fingerprint
	f.saveRegistry()
	f.coordinator.reconcileRegistry()
	c.closed()
	f.connect("C")
	// B reconnects after C has a fresh certificate, but before B/C can have
	// a new SessionStart that would supersede their retained revocation.
	resumed := f.dial("B")
	resumed.send(proto.ControlTypeClientHello, f.hello("B"))
	resumed.want(proto.ControlTypeServerHello)
	resumed.want(proto.ControlTypeProbeCredential)
	for _, want := range []string{proto.ControlTypeDisconnectPeer, proto.ControlTypeMemberSnapshot} {
		select {
		case frame := <-resumed.frames:
			if frame.Type != want {
				t.Fatalf("control readmission sent %s before %s; old revocation would override fresh authorization", frame.Type, want)
			}
			if want == proto.ControlTypeDisconnectPeer {
				if body := decodeCoordinatorBody[proto.DisconnectPeer](t, frame); body.NodeID != "C" {
					t.Fatalf("wrong old identity revoked: %+v", body)
				}
			} else {
				body := decodeCoordinatorBody[proto.MemberSnapshot](t, frame)
				found := false
				for _, member := range body.Members {
					found = found || (member.NodeID == "C" && member.Status == "online" && member.Fingerprint == fingerprint)
				}
				if !found {
					t.Fatal("new certificate missing from final authorization")
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing %s after control readmission", want)
		}
	}
}
