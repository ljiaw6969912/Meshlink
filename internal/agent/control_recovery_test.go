package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"meshlink/internal/proto"
)

func TestOnlineMembershipRequestsEachEligiblePeerOnceWithoutTraffic(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D", "E")
	r := f.r["B"]
	if err := r.applyDelta(proto.MemberDelta{Revision: 2, RemovedNodeIDs: []string{"E"}}); err != nil {
		t.Fatal(err)
	}
	f.members[2].Status = "offline"
	// Revoked E remains absent. A fresh online entry would explicitly admit
	// it again (for example after same-MAC certificate renewal).
	f.members = f.members[:3]
	c := attachRuntimeControl(t, r)
	if err := r.applyMembers(proto.MemberSnapshot{Revision: 3, Members: f.members}); err != nil {
		t.Fatal(err)
	}
	request, err := proto.DecodeControlBody[proto.ConnectRequest](c.want(t, proto.ControlTypeConnectRequest))
	if err != nil || request.TargetNodeID != "C" {
		t.Fatalf("eligible peer request = %+v, %v", request, err)
	}
	for revision := uint64(4); revision < 10; revision++ {
		if err := r.applyMembers(proto.MemberSnapshot{Revision: revision, Members: f.members}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case extra := <-c.frames:
		t.Fatalf("membership requested self, offline, revoked, or in-flight peer: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	snapshot, ok := r.sessions.Snapshot("C")
	if !ok || snapshot.PendingPackets != 0 {
		t.Fatalf("proactive request must not create business packets: %+v", snapshot)
	}
}

func TestOnlineMembershipPreservesInstalledOffer(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C")
	r := f.r["B"]
	c := attachRuntimeControl(t, r)
	member, _ := r.member("C")
	if err := r.sessions.InstallOffer(proto.SessionOffer{
		SessionID: "existing-offer", Generation: 1, PeerNodeID: "C", PeerFingerprint: member.Fingerprint,
		DialerNodeID: "B", PairingKey: strings.Repeat("42", 32), ExpiresAt: time.Now().Add(time.Minute),
		Candidates: []proto.Candidate{{Address: "127.0.0.1", Port: uint16(f.r["C"].candidates.LocalAddr().Port), Scope: "lan", ExpiresAt: time.Now().Add(time.Minute)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.applyMembers(proto.MemberSnapshot{Revision: 2, Members: f.members}); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-c.frames:
		t.Fatalf("installed offer triggered another request: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	snapshot, _ := r.sessions.Snapshot("C")
	if snapshot.SessionID != "existing-offer" || snapshot.Generation != 1 || snapshot.State != "preparing" {
		t.Fatalf("membership replaced installed offer: %+v", snapshot)
	}
}

func TestPrepareBurstSharesFailedProbeAndStillRefreshesLANCandidates(t *testing.T) {
	f := newRuntimeFixture(t, "B", "C", "D")
	r := f.r["B"]
	blackhole, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	r.a.cfg.Connect = blackhole.LocalAddr().String()
	c := attachRuntimeControl(t, r)
	c.send(t, proto.ControlTypeProbeCredential, proto.ProbeCredential{ProbeID: "unreachable-probe", Key: strings.Repeat("42", 32), ExpiresAt: time.Now().Add(time.Minute)})
	first, err := proto.DecodeControlBody[proto.CandidateUpdate](c.want(t, proto.ControlTypeCandidateUpdate))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	previousRevision := first.Revision
	for _, peer := range []string{"C", "D"} {
		c.send(t, proto.ControlTypeConnectPrepare, proto.ConnectPrepare{SessionID: "prepare-" + peer, Generation: 1, PeerNodeID: peer})
		update, err := proto.DecodeControlBody[proto.CandidateUpdate](c.want(t, proto.ControlTypeCandidateUpdate))
		if err != nil || update.Revision <= previousRevision || len(update.Candidates) == 0 {
			t.Fatalf("prepare reused stale LAN candidate revision: %+v, %v", update, err)
		}
		ready, err := proto.DecodeControlBody[proto.ConnectReady](c.want(t, proto.ControlTypeConnectReady))
		if err != nil || !ready.Ready || ready.CandidateRevision != update.Revision {
			t.Fatalf("prepare did not use the fresh LAN revision: %+v, %v", ready, err)
		}
		previousRevision = update.Revision
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("prepare burst repeated the known-unreachable public probe: %v", elapsed)
	}
}

func TestControlDisconnectBeforeServerHelloIsRetryable(t *testing.T) {
	f := newRuntimeFixture(t, "B")
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
	remote.Close()
	select {
	case err := <-done:
		var permanent *controlCompatibilityError
		if errors.As(err, &permanent) || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe)) {
			t.Fatalf("temporary disconnect became permanent: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("control disconnect did not return")
	}
}

func TestAdmissionRegistryOutageIsRetryableButIdentityRejectionIsPermanent(t *testing.T) {
	for _, code := range []string{"registry_unavailable", "identity_mismatch"} {
		t.Run(code, func(t *testing.T) {
			f := newRuntimeFixture(t, "B")
			local, remote := net.Pipe()
			defer remote.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.r["B"].control.serve(ctx, local) }()
			for range 3 {
				if _, err := proto.Read(remote); err != nil {
					t.Fatal(err)
				}
			}
			payload, err := proto.MarshalControl(proto.ControlTypeError, "", proto.ControlError{Code: code, Message: "admission unavailable"})
			if err != nil {
				t.Fatal(err)
			}
			if err := proto.Write(remote, proto.TypeControl, payload); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var permanent *controlCompatibilityError
				if err == nil || errors.As(err, &permanent) != (code == "identity_mismatch") {
					t.Fatalf("wrong admission retry policy for %s: %v", code, err)
				}
			case <-time.After(time.Second):
				t.Fatal("admission did not return")
			}
		})
	}
}

func TestControlTLSHandshakeTimesOutWithoutParentDeadline(t *testing.T) {
	f := newRuntimeFixture(t, "B")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	f.r["B"].a.cfg.Connect = listener.Addr().String()
	f.r["B"].control.tlsConfig.ServerName = "127.0.0.1"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.r["B"].control.connect(ctx) }()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case err := <-done:
		var networkError net.Error
		if !errors.As(err, &networkError) || !networkError.Timeout() {
			t.Fatalf("stalled TLS handshake returned %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("TLS handshake has no independent timeout; reconnect loop cannot advance")
	}
}
