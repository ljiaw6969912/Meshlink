package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestClientP2PControlPlaneWithTCPRuntimeLoopbackSmoke(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	account, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "p2p-tcp-smoke@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "P2PTCP"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	invite, err := client.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   2,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	source, err := client.JoinDevice(ctx, JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "source"})
	if err != nil {
		t.Fatalf("JoinDevice source returned error: %v", err)
	}
	target, err := client.JoinDevice(ctx, JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "target"})
	if err != nil {
		t.Fatalf("JoinDevice target returned error: %v", err)
	}
	if _, err := client.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: source.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice source returned error: %v", err)
	}
	if _, err := client.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice target returned error: %v", err)
	}

	listener, host, port := mustCloudhubListenTCP4(t)
	accepted := acceptOneCloudhubTCP(t, listener)
	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Candidates: []p2p.Candidate{
			{Address: host, Port: 49100, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopeLAN, Priority: 50, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Candidates: []p2p.Candidate{
			{Address: host, Port: port, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopeLAN, Priority: 80, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
	}

	negotiation, err := client.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("NegotiateP2PConnection returned error: %v", err)
	}
	result := p2p.Connector{Dialer: p2p.TCPDialer{Timeout: time.Second}}.Connect(ctx, negotiation)
	if result.State != p2p.PathStateLANDirectConnected || result.PathType != p2p.PathTypeLANDirect {
		t.Fatalf("result = %+v, want LAN direct connected through loopback TCP probe", result)
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("listener accept returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not observe TCP probe")
	}
	assertNoSensitiveJSON(t, negotiation)
	assertNoSensitiveJSON(t, result)
	assertJSONDoesNotContainValues(t, result, invite.Token, invite.Code)

	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	fallbackNegotiation, err := client.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("NegotiateP2PConnection fallback returned error: %v", err)
	}
	fallbackResult := p2p.Connector{Dialer: p2p.TCPDialer{Timeout: 200 * time.Millisecond}}.Connect(ctx, fallbackNegotiation)
	if fallbackResult.State != p2p.PathStateFallbackRelay || fallbackResult.PathType != p2p.PathTypeRelay {
		t.Fatalf("fallback result = %+v, want Relay fallback after listener closes", fallbackResult)
	}
	summary, err := client.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if summary.RelaySessions.Total != 0 {
		t.Fatalf("relay sessions = %+v, want fallback semantics without creating Relay session", summary.RelaySessions)
	}
	assertNoSensitiveJSON(t, fallbackResult)
	assertJSONDoesNotContainValues(t, fallbackResult, invite.Token, invite.Code)

	if _, err := client.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOffline}); err != nil {
		t.Fatalf("HeartbeatDevice target offline returned error: %v", err)
	}
	_, err = client.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("offline target negotiate error = %v, want ErrForbidden", err)
	}

	if _, err := client.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: target.ID, Reason: "retired"}); err != nil {
		t.Fatalf("RevokeDevice returned error: %v", err)
	}
	_, err = client.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err == nil || !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked target negotiate error = %v, want ErrRevoked", err)
	}

	otherNetwork, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "OtherP2PTCP"})
	if err != nil {
		t.Fatalf("CreateNetwork other returned error: %v", err)
	}
	otherInvite, err := client.CreateInvite(ctx, CreateInviteRequest{AccountID: account.ID, NetworkID: otherNetwork.ID})
	if err != nil {
		t.Fatalf("CreateInvite other returned error: %v", err)
	}
	otherTarget, err := client.JoinDevice(ctx, JoinDeviceRequest{Token: otherInvite.Token, Code: otherInvite.Code, DeviceName: "other-target"})
	if err != nil {
		t.Fatalf("JoinDevice other target returned error: %v", err)
	}
	if _, err := client.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: otherTarget.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice other target returned error: %v", err)
	}
	_, err = client.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: otherTarget.ID,
	})
	if err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-network target negotiate error = %v, want ErrForbidden", err)
	}
}

func mustCloudhubListenTCP4(t *testing.T) (net.Listener, string, int) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp4: %v", err)
	}
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatalf("split listener address %q: %v", listener.Addr().String(), err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		listener.Close()
		t.Fatalf("parse listener port %q: %v", portText, err)
	}
	return listener, host, port
}

func acceptOneCloudhubTCP(t *testing.T, listener net.Listener) <-chan error {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		accepted <- conn.Close()
	}()
	return accepted
}

func assertJSONDoesNotContainValues(t *testing.T, v any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	for _, value := range forbidden {
		if value != "" && bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("JSON leaks forbidden value %q: %s", value, encoded)
		}
	}
}
