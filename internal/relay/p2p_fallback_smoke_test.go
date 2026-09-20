package relay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"meshlink/internal/cloudhub"
	"meshlink/internal/p2p"
)

func TestP2PAutomaticRelayFallbackRuntimeSmoke(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	runtime := NewServer(svc, WithJoinTimeout(100*time.Millisecond))
	addr := serveRelayForTest(t, runtime)
	manager := cloudhub.P2PRelaySessionManager{Service: svc, Endpoint: addr}

	account, network, source, target := mustP2PFallbackCloudHubDevices(t, ctx, svc)
	negotiation := p2p.BuildNegotiation(p2p.NegotiationRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		SourceCandidates:   []p2p.Candidate{p2pFallbackLANDirectCandidate(source.ID, network.ID, "127.0.0.1", 49120, 50)},
		TargetCandidates:   []p2p.Candidate{p2pFallbackLANDirectCandidate(target.ID, network.ID, "127.0.0.1", 49121, 60)},
		AllowRelayFallback: true,
	})

	var authorizedGrant p2p.RelaySessionGrant
	startedAt := time.Now()
	autoResult, err := p2p.AutoFallbackConnector{
		Connector:              p2p.Connector{Dialer: &p2p.FakeDialer{Err: errors.New("direct tcp refused")}},
		RelaySessionCreator:    manager,
		RelaySessionAuthorizer: relayRuntimeP2PAuthorizer{runtime: runtime, last: &authorizedGrant},
		RelaySessionCloser:     manager,
		RelaySessionTTL:        time.Minute,
	}.Connect(ctx, negotiation)
	if err != nil {
		t.Fatalf("automatic fallback Connect returned error: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 15*time.Second {
		t.Fatalf("automatic fallback took %s, want under 15s gate", elapsed)
	}
	if autoResult.FinalPath != p2p.PathTypeRelay || autoResult.RelaySessionID == "" || !autoResult.RelayAuthorized {
		t.Fatalf("auto fallback result = %+v, want authorized relay path", autoResult)
	}
	for _, want := range []string{"attempted direct", "direct failed", "Relay fallback session created", "current path is relay"} {
		if !p2pFallbackHasDiagnostic(autoResult.Diagnostics, want) {
			t.Fatalf("diagnostics = %+v, missing %q", autoResult.Diagnostics, want)
		}
	}
	assertP2PFallbackJSONDoesNotContain(t, autoResult, "join_token", "token_hash", "private_key")

	grant := authorizedGrant
	sourceConn, sourceReader := openRelayConn(t, addr, map[string]string{
		"session_id": grant.ID,
		"role":       string(RoleSource),
		"device_id":  source.ID,
		"token":      grant.SourceJoinToken,
	})
	defer sourceConn.Close()
	targetConn, targetReader := openRelayConn(t, addr, map[string]string{
		"session_id": grant.ID,
		"role":       string(RoleTarget),
		"device_id":  target.ID,
		"token":      grant.TargetJoinToken,
	})
	defer targetConn.Close()

	readRelayAck(t, sourceReader, true)
	readRelayAck(t, targetReader, true)
	active, err := svc.GetRelaySession(ctx, autoResult.RelaySessionID)
	if err != nil {
		t.Fatalf("GetRelaySession active returned error: %v", err)
	}
	if active.Status != cloudhub.RelaySessionActive {
		t.Fatalf("relay session status = %q, want active", active.Status)
	}

	if _, err := sourceConn.Write([]byte("p2p fallback ping")); err != nil {
		t.Fatalf("source Write returned error: %v", err)
	}
	assertRelayPayload(t, targetReader, "p2p fallback ping")
	if _, err := targetConn.Write([]byte("relay pong")); err != nil {
		t.Fatalf("target Write returned error: %v", err)
	}
	assertRelayPayload(t, sourceReader, "relay pong")

	if err := sourceConn.Close(); err != nil {
		t.Fatalf("source Close returned error: %v", err)
	}
	if err := targetConn.Close(); err != nil {
		t.Fatalf("target Close returned error: %v", err)
	}
	closed := waitForRelaySessionClosed(t, ctx, svc, autoResult.RelaySessionID)
	if closed.RelayBytesIn != int64(len("p2p fallback ping")) || closed.RelayBytesOut != int64(len("relay pong")) {
		t.Fatalf("closed relay bytes = in:%d out:%d, want transferred payload sizes", closed.RelayBytesIn, closed.RelayBytesOut)
	}
	logs, err := svc.ListRelaySessionConnectionLogs(ctx, autoResult.RelaySessionID)
	if err != nil {
		t.Fatalf("ListRelaySessionConnectionLogs returned error: %v", err)
	}
	if len(logs) != 1 || logs[0].PathType != cloudhub.RelayPathType || logs[0].RelayBytesIn == 0 || logs[0].RelayBytesOut == 0 {
		t.Fatalf("connection logs = %+v, want relay log with bytes", logs)
	}
	usage, err := svc.ListRelaySessionUsage(ctx, autoResult.RelaySessionID)
	if err != nil {
		t.Fatalf("ListRelaySessionUsage returned error: %v", err)
	}
	if len(usage) != 2 || usage[0].BytesIn+usage[0].BytesOut == 0 || usage[1].BytesIn+usage[1].BytesOut == 0 {
		t.Fatalf("relay usage = %+v, want source and target byte rows", usage)
	}
	summary, err := svc.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if summary.RelaySessions.Total != 1 || summary.RelaySessions.Closed != 1 || len(summary.RelayUsage) != 2 || len(summary.ConnectionLogs) == 0 {
		t.Fatalf("summary = %+v, want closed relay session with usage and logs", summary)
	}
	assertP2PFallbackJSONDoesNotContain(t, summary, grant.SourceJoinToken, grant.TargetJoinToken, "join_token", "token_hash", "private_key")
}

func TestP2PAutomaticRelayFallbackDoesNotCreateRelayOnDirectSuccess(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	runtime := NewServer(svc, WithJoinTimeout(100*time.Millisecond))
	manager := cloudhub.P2PRelaySessionManager{Service: svc, Endpoint: "127.0.0.1:18082"}
	account, network, source, target := mustP2PFallbackCloudHubDevices(t, ctx, svc)
	negotiation := p2p.BuildNegotiation(p2p.NegotiationRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		SourceCandidates:   []p2p.Candidate{p2pFallbackLANDirectCandidate(source.ID, network.ID, "127.0.0.1", 49122, 50)},
		TargetCandidates:   []p2p.Candidate{p2pFallbackLANDirectCandidate(target.ID, network.ID, "127.0.0.1", 49123, 60)},
		AllowRelayFallback: true,
	})

	autoResult, err := p2p.AutoFallbackConnector{
		Connector:              p2p.Connector{Dialer: &p2p.FakeDialer{}},
		RelaySessionCreator:    manager,
		RelaySessionAuthorizer: relayRuntimeP2PAuthorizer{runtime: runtime},
		RelaySessionCloser:     manager,
	}.Connect(ctx, negotiation)
	if err != nil {
		t.Fatalf("automatic fallback Connect returned error: %v", err)
	}
	if autoResult.FinalPath != p2p.PathTypeLANDirect || autoResult.RelaySessionID != "" || autoResult.RelayAuthorized {
		t.Fatalf("auto result = %+v, want direct path without Relay session", autoResult)
	}
	summary, err := svc.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if summary.RelaySessions.Total != 0 || len(summary.RelayUsage) != 0 {
		t.Fatalf("summary = %+v, want no Relay sessions or bytes on direct success", summary)
	}
}

func TestP2PAutomaticRelayFallbackDoesNotReportSuccessWhenRuntimeUnavailable(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	manager := cloudhub.P2PRelaySessionManager{Service: svc, Endpoint: "127.0.0.1:18082"}
	account, network, source, target := mustP2PFallbackCloudHubDevices(t, ctx, svc)
	negotiation := p2p.BuildNegotiation(p2p.NegotiationRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		SourceCandidates:   []p2p.Candidate{p2pFallbackLANDirectCandidate(source.ID, network.ID, "127.0.0.1", 49124, 50)},
		TargetCandidates:   []p2p.Candidate{p2pFallbackLANDirectCandidate(target.ID, network.ID, "127.0.0.1", 49125, 60)},
		AllowRelayFallback: true,
	})

	autoResult, err := p2p.AutoFallbackConnector{
		Connector:              p2p.Connector{Dialer: &p2p.FakeDialer{Err: errors.New("direct tcp refused")}},
		RelaySessionCreator:    manager,
		RelaySessionAuthorizer: failingP2PRelayAuthorizer{err: errors.New("relay runtime unavailable")},
		RelaySessionCloser:     manager,
	}.Connect(ctx, negotiation)
	if err == nil || !strings.Contains(err.Error(), "relay runtime unavailable") {
		t.Fatalf("Connect error = %v, want runtime unavailable", err)
	}
	if autoResult.State != p2p.PathStateFailed || autoResult.FinalPath == p2p.PathTypeRelay || autoResult.RelayAuthorized {
		t.Fatalf("auto result = %+v, must not report Relay success", autoResult)
	}
	summary, err := svc.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if summary.RelaySessions.Total != 1 || summary.RelaySessions.Closed != 1 || summary.RelaySessions.Active != 0 {
		t.Fatalf("summary relay sessions = %+v, want failed authorization closed", summary.RelaySessions)
	}
}

type relayRuntimeP2PAuthorizer struct {
	runtime *Server
	last    *p2p.RelaySessionGrant
}

func (a relayRuntimeP2PAuthorizer) AuthorizeRelaySession(ctx context.Context, grant p2p.RelaySessionGrant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.runtime == nil {
		return errors.New("relay runtime unavailable")
	}
	if a.last != nil {
		*a.last = grant
	}
	return a.runtime.AuthorizeRelaySession(cloudhub.RelaySessionResultFromP2PGrant(grant))
}

type failingP2PRelayAuthorizer struct {
	err error
}

func (a failingP2PRelayAuthorizer) AuthorizeRelaySession(ctx context.Context, _ p2p.RelaySessionGrant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.err
}

func mustP2PFallbackCloudHubDevices(t *testing.T, ctx context.Context, svc *cloudhub.Service) (cloudhub.Account, cloudhub.Network, cloudhub.Device, cloudhub.Device) {
	t.Helper()
	account, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "p2p-fallback-" + time.Now().Format("150405.000000") + "@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, cloudhub.CreateNetworkRequest{AccountID: account.ID, Name: "P2PFallback"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	invite, err := svc.CreateInvite(ctx, cloudhub.CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   2,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	source, err := svc.JoinDevice(ctx, cloudhub.JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "source"})
	if err != nil {
		t.Fatalf("JoinDevice source returned error: %v", err)
	}
	target, err := svc.JoinDevice(ctx, cloudhub.JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "target"})
	if err != nil {
		t.Fatalf("JoinDevice target returned error: %v", err)
	}
	for _, device := range []cloudhub.Device{source, target} {
		if _, err := svc.HeartbeatDevice(ctx, cloudhub.HeartbeatDeviceRequest{DeviceID: device.ID, Status: cloudhub.DeviceStatusOnline}); err != nil {
			t.Fatalf("HeartbeatDevice %s returned error: %v", device.ID, err)
		}
	}
	return account, network, source, target
}

func p2pFallbackLANDirectCandidate(deviceID, networkID, address string, port int, priority int) p2p.Candidate {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	return p2p.Candidate{
		DeviceID:   deviceID,
		NetworkID:  networkID,
		Address:    address,
		Port:       port,
		Protocol:   p2p.ProtocolTCP,
		Scope:      p2p.CandidateScopeLAN,
		Priority:   priority,
		TTLSeconds: 60,
		SeenAt:     now,
		ExpiresAt:  now.Add(time.Minute),
	}
}

func p2pFallbackHasDiagnostic(diagnostics []string, want string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, want) {
			return true
		}
	}
	return false
}

func assertP2PFallbackJSONDoesNotContain(t *testing.T, v any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	for _, value := range forbidden {
		if value != "" && strings.Contains(lower, strings.ToLower(value)) {
			t.Fatal("JSON leaks a forbidden sensitive value")
		}
	}
}
