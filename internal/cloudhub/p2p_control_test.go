package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestServiceP2PCandidatesAndNegotiationAuthorizeLANDirect(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
	source = mustHeartbeatOnline(t, ctx, svc, source.ID)
	target = mustHeartbeatOnline(t, ctx, svc, target.ID)

	sourceCandidates, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Candidates: []p2p.Candidate{
			{
				Address:      "192.168.1.20",
				Port:         9000,
				Protocol:     p2p.ProtocolTCP,
				Scope:        p2p.CandidateScopeLAN,
				Priority:     10,
				Source:       "interface",
				ObservedFrom: "198.51.100.5:44000",
				TTLSeconds:   60,
			},
			{
				Address:    "192.168.1.20",
				Port:       9000,
				Protocol:   p2p.ProtocolTCP,
				Scope:      p2p.CandidateScopeLAN,
				Priority:   80,
				Source:     "interface-rescan",
				TTLSeconds: 60,
			},
		},
	})
	if err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if len(sourceCandidates) != 1 || sourceCandidates[0].DeviceID != source.ID || sourceCandidates[0].NetworkID != network.ID {
		t.Fatalf("source candidates = %+v, want one normalized source candidate", sourceCandidates)
	}
	if sourceCandidates[0].Priority != 80 || !sourceCandidates[0].SeenAt.Equal(now) || !sourceCandidates[0].ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("source candidate = %+v, want deduped TTL metadata", sourceCandidates[0])
	}
	if _, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Candidates: []p2p.Candidate{
			{
				Address:    "192.168.1.30",
				Port:       9000,
				Protocol:   p2p.ProtocolTCP,
				Scope:      p2p.CandidateScopeLAN,
				Priority:   70,
				Source:     "interface",
				TTLSeconds: 60,
			},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
	}

	targetCandidates, err := svc.QueryP2PCandidates(ctx, QueryP2PCandidatesRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		RequestingDeviceID: source.ID,
		TargetDeviceID:     target.ID,
	})
	if err != nil {
		t.Fatalf("QueryP2PCandidates returned error: %v", err)
	}
	if len(targetCandidates) != 1 || targetCandidates[0].Address != "192.168.1.30" {
		t.Fatalf("target candidates = %+v, want target LAN candidate", targetCandidates)
	}

	negotiation, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("NegotiateP2PConnection returned error: %v", err)
	}
	if negotiation.State != p2p.PathStateConnecting || negotiation.PreferredPathType != p2p.PathTypeLANDirect {
		t.Fatalf("negotiation = %+v, want connecting LAN direct preference", negotiation)
	}
	if len(negotiation.CandidatePairs) != 1 || negotiation.CandidatePairs[0].PathType != p2p.PathTypeLANDirect {
		t.Fatalf("candidate pairs = %+v, want one LAN direct pair", negotiation.CandidatePairs)
	}
	if !negotiation.RelayFallback.CreateRelaySession || negotiation.RelayFallback.Reason != "direct_attempt_failed" {
		t.Fatalf("relay fallback = %+v, want create-on-failure semantics", negotiation.RelayFallback)
	}
	assertNoSensitiveJSON(t, negotiation)

	summary, err := svc.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if !hasAuditEvent(summary.AuditEvents, AuditP2PCandidatesRegistered) ||
		!hasAuditEvent(summary.AuditEvents, AuditP2PConnectionNegotiated) {
		t.Fatalf("audit events = %+v, want p2p candidate and negotiation audit", summary.AuditEvents)
	}
	if len(summary.ConnectionLogs) != 1 || summary.ConnectionLogs[0].PathType != string(p2p.PathTypeLANDirect) {
		t.Fatalf("connection logs = %+v, want LAN direct negotiation metadata", summary.ConnectionLogs)
	}
}

func TestServiceP2PControlPlaneNegativeAuthorization(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)

	t.Run("no candidates falls back to relay without claiming direct", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		source = mustHeartbeatOnline(t, ctx, svc, source.ID)
		target = mustHeartbeatOnline(t, ctx, svc, target.ID)

		negotiation, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err != nil {
			t.Fatalf("NegotiateP2PConnection returned error: %v", err)
		}
		if negotiation.State != p2p.PathStateFallbackRelay || negotiation.PreferredPathType != p2p.PathTypeRelay {
			t.Fatalf("negotiation = %+v, want Relay fallback when no candidates exist", negotiation)
		}
		if len(negotiation.CandidatePairs) != 0 || strings.Contains(negotiation.DiagnosticMessage, string(p2p.PathStateLANDirectConnected)) {
			t.Fatalf("negotiation = %+v, must not misreport direct", negotiation)
		}
	})

	t.Run("offline target is forbidden", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		source = mustHeartbeatOnline(t, ctx, svc, source.ID)

		_, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "online") {
			t.Fatalf("offline target error = %v, want ErrForbidden online requirement", err)
		}
	})

	t.Run("target in another network is forbidden", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		otherNetwork, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "Other"})
		if err != nil {
			t.Fatalf("CreateNetwork other returned error: %v", err)
		}
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		target, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, otherNetwork.ID)
		source = mustHeartbeatOnline(t, ctx, svc, source.ID)
		target = mustHeartbeatOnline(t, ctx, svc, target.ID)

		_, err = svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) {
			t.Fatalf("cross-network error = %v, want ErrForbidden", err)
		}
	})

	t.Run("target in another account is forbidden", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		otherAccount, otherNetwork := mustAccountAndNetwork(t, ctx, svc)
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		target, _ := mustJoinTwoDevices(t, ctx, svc, otherAccount.ID, otherNetwork.ID)
		source = mustHeartbeatOnline(t, ctx, svc, source.ID)
		target = mustHeartbeatOnline(t, ctx, svc, target.ID)

		_, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) {
			t.Fatalf("cross-account error = %v, want ErrForbidden", err)
		}
	})

	t.Run("revoked target is forbidden", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		source = mustHeartbeatOnline(t, ctx, svc, source.ID)
		target = mustHeartbeatOnline(t, ctx, svc, target.ID)
		if _, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: target.ID, Reason: "retired"}); err != nil {
			t.Fatalf("RevokeDevice returned error: %v", err)
		}

		_, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrRevoked) {
			t.Fatalf("revoked target error = %v, want ErrRevoked", err)
		}
	})

	t.Run("IPv6 candidate is rejected for task 6a", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		source = mustHeartbeatOnline(t, ctx, svc, source.ID)

		_, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
			AccountID: account.ID,
			NetworkID: network.ID,
			DeviceID:  source.ID,
			Candidates: []p2p.Candidate{
				{
					Address:    "2001:db8::20",
					Port:       9000,
					Protocol:   p2p.ProtocolTCP,
					Scope:      p2p.CandidateScopeLAN,
					Priority:   10,
					TTLSeconds: 60,
				},
			},
		})
		if err == nil || !strings.Contains(err.Error(), "IPv4") {
			t.Fatalf("IPv6 register error = %v, want IPv4-only rejection", err)
		}
	})
}

func TestClientP2PControlPlaneFlowAgainstHTTPServer(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	account, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "p2p-client@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "P2PNet"})
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

	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Candidates: []p2p.Candidate{
			{Address: "10.10.0.10", Port: 9000, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopeLAN, Priority: 50, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Candidates: []p2p.Candidate{
			{Address: "10.10.0.11", Port: 9000, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopeLAN, Priority: 70, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
	}
	targetCandidates, err := client.QueryP2PCandidates(ctx, QueryP2PCandidatesRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		RequestingDeviceID: source.ID,
		TargetDeviceID:     target.ID,
	})
	if err != nil {
		t.Fatalf("QueryP2PCandidates returned error: %v", err)
	}
	if len(targetCandidates) != 1 || targetCandidates[0].Address != "10.10.0.11" {
		t.Fatalf("target candidates = %+v, want registered target LAN candidate", targetCandidates)
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
	if negotiation.PreferredPathType != p2p.PathTypeLANDirect || !negotiation.RelayFallback.CreateRelaySession {
		t.Fatalf("negotiation = %+v, want LAN direct with Relay fallback semantics", negotiation)
	}
	encoded, err := json.Marshal(negotiation)
	if err != nil {
		t.Fatalf("marshal negotiation: %v", err)
	}
	assertNoSensitiveJSON(t, negotiation)
	for _, forbiddenValue := range []string{invite.Token, invite.Code} {
		if forbiddenValue != "" && bytes.Contains(encoded, []byte(forbiddenValue)) {
			t.Fatalf("p2p negotiation leaked invite secret in JSON: %s", encoded)
		}
	}
}

func TestClientP2PControlPlaneNegotiatesPublicDirectAgainstHTTPServer(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	account, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "p2p-public-client@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "P2PPublic"})
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

	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.31", Port: 8443, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 50, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.41", Port: 8443, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 70, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
	}

	targetCandidates, err := client.QueryP2PCandidates(ctx, QueryP2PCandidatesRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		RequestingDeviceID: source.ID,
		TargetDeviceID:     target.ID,
	})
	if err != nil {
		t.Fatalf("QueryP2PCandidates returned error: %v", err)
	}
	if len(targetCandidates) != 1 || targetCandidates[0].Scope != p2p.CandidateScopePublic || targetCandidates[0].Address != "198.51.100.41" {
		t.Fatalf("target candidates = %+v, want registered public candidate", targetCandidates)
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
	if negotiation.State != p2p.PathStateConnecting || negotiation.PreferredPathType != p2p.PathTypePublicDirect {
		t.Fatalf("negotiation = %+v, want connecting public direct preference", negotiation)
	}
	if len(negotiation.CandidatePairs) != 1 || negotiation.CandidatePairs[0].PathType != p2p.PathTypePublicDirect {
		t.Fatalf("candidate pairs = %+v, want one public direct pair", negotiation.CandidatePairs)
	}
	if !negotiation.RelayFallback.CreateRelaySession || negotiation.RelayFallback.Reason != "direct_attempt_failed" {
		t.Fatalf("relay fallback = %+v, want create-on-failure semantics", negotiation.RelayFallback)
	}
	assertNoSensitiveJSON(t, negotiation)
	assertJSONDoesNotContainValues(t, negotiation, invite.Token, invite.Code)

	summary, err := client.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if !hasAuditEvent(summary.AuditEvents, AuditP2PCandidatesRegistered) ||
		!hasAuditEvent(summary.AuditEvents, AuditP2PConnectionNegotiated) {
		t.Fatalf("audit events = %+v, want p2p candidate and negotiation audit", summary.AuditEvents)
	}
	if len(summary.ConnectionLogs) != 1 || summary.ConnectionLogs[0].PathType != string(p2p.PathTypePublicDirect) {
		t.Fatalf("connection logs = %+v, want public direct negotiation metadata", summary.ConnectionLogs)
	}
	assertNoSensitiveJSON(t, summary)
	assertJSONDoesNotContainValues(t, summary, invite.Token, invite.Code)
}

func TestClientP2PPublicDirectAuthorizationBoundariesDoNotNegotiateDirect(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC)

	t.Run("offline target", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		server := httptest.NewServer(NewServer(svc))
		defer server.Close()
		client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}
		account, network, source, target, invite := mustClientP2PPublicDevices(t, ctx, client)

		if _, err := client.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOffline}); err != nil {
			t.Fatalf("HeartbeatDevice target offline returned error: %v", err)
		}
		_, err := client.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "online") {
			t.Fatalf("offline target negotiate error = %v, want ErrForbidden online requirement", err)
		}
		assertNoPublicDirectConnectionLog(t, ctx, client, account.ID)
		assertJSONDoesNotContainValues(t, target, invite.Token, invite.Code)
	})

	t.Run("revoked target", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		server := httptest.NewServer(NewServer(svc))
		defer server.Close()
		client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}
		account, network, source, target, _ := mustClientP2PPublicDevices(t, ctx, client)

		if _, err := client.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: target.ID, Reason: "retired"}); err != nil {
			t.Fatalf("RevokeDevice returned error: %v", err)
		}
		_, err := client.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrRevoked) {
			t.Fatalf("revoked target negotiate error = %v, want ErrRevoked", err)
		}
		assertNoPublicDirectConnectionLog(t, ctx, client, account.ID)
	})

	t.Run("cross network target", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		server := httptest.NewServer(NewServer(svc))
		defer server.Close()
		client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}
		account, network, source, _, _ := mustClientP2PPublicDevices(t, ctx, client)
		otherNetwork, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "OtherPublic"})
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
		if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
			AccountID: account.ID,
			NetworkID: otherNetwork.ID,
			DeviceID:  otherTarget.ID,
			Candidates: []p2p.Candidate{
				{Address: "198.51.100.71", Port: 8443, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 70, TTLSeconds: 60},
			},
		}); err != nil {
			t.Fatalf("RegisterP2PCandidates other target returned error: %v", err)
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
		assertNoPublicDirectConnectionLog(t, ctx, client, account.ID)
	})
}

func mustClientP2PPublicDevices(t *testing.T, ctx context.Context, client *Client) (Account, Network, Device, Device, InviteResult) {
	t.Helper()
	account, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "p2p-public-boundary@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "P2PPublicBoundary"})
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
	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.61", Port: 8443, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 50, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if _, err := client.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.62", Port: 8443, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 70, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
	}
	return account, network, source, target, invite
}

func assertNoPublicDirectConnectionLog(t *testing.T, ctx context.Context, client *Client, accountID string) {
	t.Helper()
	summary, err := client.GetAccountManagementSummary(ctx, accountID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	for _, log := range summary.ConnectionLogs {
		if log.PathType == string(p2p.PathTypePublicDirect) {
			t.Fatalf("connection logs = %+v, must not record public direct after rejected negotiation", summary.ConnectionLogs)
		}
	}
	assertNoSensitiveJSON(t, summary)
}

func mustHeartbeatOnline(t *testing.T, ctx context.Context, svc *Service, deviceID string) Device {
	t.Helper()
	device, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: deviceID,
		Status:   DeviceStatusOnline,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice %s returned error: %v", deviceID, err)
	}
	return device
}
