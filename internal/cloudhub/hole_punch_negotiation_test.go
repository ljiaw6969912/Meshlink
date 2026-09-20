package cloudhub

import (
	"context"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestServiceP2PNegotiationAuditsHolePunchSummaryWithoutSensitiveData(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 14, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	fullCone := p2p.NATProbeSummary{
		Type:                   p2p.NATTypeFullCone,
		UDPAvailable:           true,
		MappingStable:          true,
		HolePunchRecommended:   true,
		SuccessfulObservations: 2,
		Reason:                 "raw private_key and 10.0.0.10 must not enter audit",
	}
	source = mustHeartbeatWithNAT(t, ctx, svc, account.ID, network.ID, source.ID, fullCone)
	target = mustHeartbeatWithNAT(t, ctx, svc, account.ID, network.ID, target.ID, fullCone)

	registerUDPHolePunchCandidatePair(t, ctx, svc, account.ID, network.ID, source.ID, target.ID)

	negotiation, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
		TransportPolicy: p2p.TransportPolicy{
			AllowExperimentalUDP: true,
		},
	})
	if err != nil {
		t.Fatalf("NegotiateP2PConnection returned error: %v", err)
	}
	summary := p2p.SummarizeHolePunch(negotiation, p2p.HolePunchPolicy{})
	if !summary.Ready || summary.CandidatePortCount != 2 {
		t.Fatalf("hole punch summary = %+v, want ready with two synced UDP ports", summary)
	}

	management, err := svc.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	event := latestAuditEvent(management.AuditEvents, AuditP2PConnectionNegotiated)
	if event.Event == "" {
		t.Fatalf("audit events = %+v, want p2p negotiation event", management.AuditEvents)
	}
	if event.Metadata["hole_punch_ready"] != true ||
		event.Metadata["hole_punch_candidate_port_count"] != 2 ||
		event.Metadata["hole_punch_mode"] != "simultaneous_udp" {
		t.Fatalf("audit metadata = %+v, want sanitized hole punch summary", event.Metadata)
	}
	assertNoSensitiveJSON(t, management)
	assertJSONDoesNotContainValues(t, management, "private_key", "10.0.0.10", "join_token")
}

func mustHeartbeatWithNAT(t *testing.T, ctx context.Context, svc *Service, accountID, networkID, deviceID string, summary p2p.NATProbeSummary) Device {
	t.Helper()
	device, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID: accountID,
		NetworkID: networkID,
		DeviceID:  deviceID,
		Status:    DeviceStatusOnline,
		NATProbe:  &summary,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice %s returned error: %v", deviceID, err)
	}
	return device
}

func registerUDPHolePunchCandidatePair(t *testing.T, ctx context.Context, svc *Service, accountID, networkID, sourceID, targetID string) {
	t.Helper()
	if _, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: accountID,
		NetworkID: networkID,
		DeviceID:  sourceID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.10", Port: 51000, Protocol: p2p.ProtocolUDP, Scope: p2p.CandidateScopePublic, Priority: 70, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if _, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: accountID,
		NetworkID: networkID,
		DeviceID:  targetID,
		Candidates: []p2p.Candidate{
			{Address: "198.51.100.20", Port: 52000, Protocol: p2p.ProtocolUDP, Scope: p2p.CandidateScopePublic, Priority: 80, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
	}
}

func latestAuditEvent(events []AuditEvent, event string) AuditEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Event == event {
			return events[i]
		}
	}
	return AuditEvent{}
}
