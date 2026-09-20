package cloudhub

import (
	"context"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestServiceP2PNegotiationReturnsTransportSelection(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 13, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	fullCone := p2p.NATProbeSummary{
		Type:                   p2p.NATTypeFullCone,
		UDPAvailable:           true,
		MappingStable:          true,
		HolePunchRecommended:   true,
		SuccessfulObservations: 2,
		Reason:                 "raw private_key and 10.0.0.10 should not be copied",
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Status:    DeviceStatusOnline,
		NATProbe:  &fullCone,
	}); err != nil {
		t.Fatalf("HeartbeatDevice source returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Status:    DeviceStatusOnline,
		NATProbe:  &fullCone,
	}); err != nil {
		t.Fatalf("HeartbeatDevice target returned error: %v", err)
	}

	registerPublicCandidatePair(t, ctx, svc, account.ID, network.ID, source.ID, target.ID)

	negotiation, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
		TransportPolicy: p2p.TransportPolicy{
			AllowExperimentalUDP:  true,
			AllowExperimentalQUIC: true,
		},
	})
	if err != nil {
		t.Fatalf("NegotiateP2PConnection returned error: %v", err)
	}
	if negotiation.TransportSelection == nil || negotiation.TransportSelection.Preferred.Kind != p2p.TransportKindQUICV1 {
		t.Fatalf("transport selection = %+v, want QUIC preferred when policy and NAT allow it", negotiation.TransportSelection)
	}
	if negotiation.PreferredPathType != p2p.PathTypePublicDirect {
		t.Fatalf("preferred path = %q, want public direct path unchanged", negotiation.PreferredPathType)
	}
	assertNoSensitiveJSON(t, negotiation)
	assertJSONDoesNotContainValues(t, negotiation, "private_key", "10.0.0.10", "join_token")
}

func TestServiceP2PNegotiationTransportPolicyRespectsNATRelayRecommendation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	udpBlocked := p2p.NATProbeSummary{
		Type:                   p2p.NATTypeUDPBlocked,
		UDPAvailable:           false,
		MappingStable:          false,
		HolePunchRecommended:   false,
		RelayRecommended:       true,
		SuccessfulObservations: 0,
		Reason:                 "no UDP probe responses were received; use Relay fallback",
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Status:    DeviceStatusOnline,
		NATProbe:  &udpBlocked,
	}); err != nil {
		t.Fatalf("HeartbeatDevice source returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Status:    DeviceStatusOnline,
		NATProbe:  &udpBlocked,
	}); err != nil {
		t.Fatalf("HeartbeatDevice target returned error: %v", err)
	}

	registerPublicCandidatePair(t, ctx, svc, account.ID, network.ID, source.ID, target.ID)

	negotiation, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
		TransportPolicy: p2p.TransportPolicy{
			AllowExperimentalUDP:  true,
			AllowExperimentalQUIC: true,
		},
	})
	if err != nil {
		t.Fatalf("NegotiateP2PConnection returned error: %v", err)
	}
	if negotiation.State != p2p.PathStateFallbackRelay || negotiation.PreferredPathType != p2p.PathTypeRelay {
		t.Fatalf("negotiation = %+v, want Relay fallback for UDP-blocked NAT", negotiation)
	}
	if negotiation.TransportSelection == nil || negotiation.TransportSelection.Preferred.Kind != p2p.TransportKindRelay {
		t.Fatalf("transport selection = %+v, want Relay preferred", negotiation.TransportSelection)
	}
	if hasCloudHubTransportCandidate(negotiation.TransportSelection, p2p.TransportKindUDPDatagramV1) ||
		hasCloudHubTransportCandidate(negotiation.TransportSelection, p2p.TransportKindQUICV1) {
		t.Fatalf("transport selection = %+v, must not expose UDP/QUIC candidates when NAT blocks UDP", negotiation.TransportSelection)
	}
	assertNoSensitiveJSON(t, negotiation)
}

func registerPublicCandidatePair(t *testing.T, ctx context.Context, svc *Service, accountID, networkID, sourceID, targetID string) {
	t.Helper()
	if _, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: accountID,
		NetworkID: networkID,
		DeviceID:  sourceID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.80", Port: 443, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 70, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if _, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: accountID,
		NetworkID: networkID,
		DeviceID:  targetID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.90", Port: 443, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 80, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
	}
}

func hasCloudHubTransportCandidate(selection *p2p.TransportSelection, kind p2p.TransportKind) bool {
	if selection == nil {
		return false
	}
	for _, candidate := range selection.Candidates {
		if candidate.Kind == kind {
			return true
		}
	}
	return false
}
