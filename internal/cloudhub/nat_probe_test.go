package cloudhub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestServiceHeartbeatStoresAndReturnsNATProbeSummary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	sourceSummary := p2p.NATProbeSummary{
		Type:                   p2p.NATTypeFullCone,
		UDPAvailable:           true,
		MappingStable:          true,
		HolePunchRecommended:   true,
		SuccessfulObservations: 2,
		Reason:                 "raw probe saw 10.0.0.10 and private_key material",
	}
	updated, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Status:    DeviceStatusOnline,
		NATProbe:  &sourceSummary,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice with NAT probe returned error: %v", err)
	}
	if updated.NATProbe == nil || updated.NATProbe.Type != p2p.NATTypeFullCone || !updated.NATProbe.HolePunchRecommended {
		t.Fatalf("updated NAT probe = %+v, want stored full-cone summary", updated.NATProbe)
	}

	targetSummary := p2p.NATProbeSummary{
		Type:                   p2p.NATTypeSymmetric,
		UDPAvailable:           true,
		MappingStable:          false,
		RelayRecommended:       true,
		SuccessfulObservations: 2,
		Reason:                 "external UDP mapping changed across probe servers; prefer Relay fallback",
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Status:    DeviceStatusOnline,
		NATProbe:  &targetSummary,
	}); err != nil {
		t.Fatalf("HeartbeatDevice target with NAT probe returned error: %v", err)
	}

	devices, err := svc.ListNetworkDevices(ctx, network.ID)
	if err != nil {
		t.Fatalf("ListNetworkDevices returned error: %v", err)
	}
	if natProbeForDevice(devices, source.ID).Type != p2p.NATTypeFullCone {
		t.Fatalf("devices = %+v, want source NAT probe in list response", devices)
	}
	if natProbeForDevice(devices, target.ID).Type != p2p.NATTypeSymmetric {
		t.Fatalf("devices = %+v, want target NAT probe in list response", devices)
	}

	if _, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  source.ID,
		Candidates: []p2p.Candidate{
			{Address: "192.0.2.20", Port: 9000, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 80, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates source returned error: %v", err)
	}
	if _, err := svc.RegisterP2PCandidates(ctx, RegisterP2PCandidatesRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  target.ID,
		Candidates: []p2p.Candidate{
			{Address: "192.0.2.30", Port: 9000, Protocol: p2p.ProtocolTCP, Scope: p2p.CandidateScopePublic, Priority: 80, TTLSeconds: 60},
		},
	}); err != nil {
		t.Fatalf("RegisterP2PCandidates target returned error: %v", err)
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
	if negotiation.SourceNATProbe == nil || negotiation.SourceNATProbe.Type != p2p.NATTypeFullCone {
		t.Fatalf("source NAT probe = %+v, want full cone", negotiation.SourceNATProbe)
	}
	if negotiation.TargetNATProbe == nil || negotiation.TargetNATProbe.Type != p2p.NATTypeSymmetric || !negotiation.TargetNATProbe.RelayRecommended {
		t.Fatalf("target NAT probe = %+v, want symmetric NAT with Relay recommendation", negotiation.TargetNATProbe)
	}
	if negotiation.State != p2p.PathStateFallbackRelay || negotiation.PreferredPathType != p2p.PathTypeRelay || negotiation.FallbackReason != "target_nat_symmetric_nat" {
		t.Fatalf("negotiation = %+v, want target NAT to force Relay fallback despite public candidates", negotiation)
	}
	assertNoSensitiveJSON(t, devices)
	assertNoSensitiveJSON(t, negotiation)
	assertJSONDoesNotContainValues(t, negotiation, "10.0.0.10", "203.0.113.10", "private_key", "invite_token", "join_token")
}

func TestServiceNATProbeSummaryAuthorizationBoundaries(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)

	t.Run("heartbeat requires account and network ownership for NAT probe writes", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		otherAccount, otherNetwork := mustAccountAndNetwork(t, ctx, svc)
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

		_, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
			AccountID: otherAccount.ID,
			NetworkID: otherNetwork.ID,
			DeviceID:  source.ID,
			Status:    DeviceStatusOnline,
			NATProbe:  natProbeSummaryPtr(p2p.NATTypeFullCone),
		})
		if err == nil || !errors.Is(err, ErrForbidden) {
			t.Fatalf("cross-account NAT heartbeat error = %v, want ErrForbidden", err)
		}

		device, err := svc.store.GetDevice(ctx, source.ID)
		if err != nil {
			t.Fatalf("GetDevice returned error: %v", err)
		}
		if device.NATProbe != nil {
			t.Fatalf("device NAT probe = %+v, must not be written after forbidden heartbeat", device.NATProbe)
		}
	})

	t.Run("offline heartbeat must not write NAT probe", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

		_, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
			AccountID: account.ID,
			NetworkID: network.ID,
			DeviceID:  source.ID,
			Status:    DeviceStatusOffline,
			NATProbe:  natProbeSummaryPtr(p2p.NATTypeFullCone),
		})
		if err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "online") {
			t.Fatalf("offline NAT heartbeat error = %v, want ErrForbidden online requirement", err)
		}
		device, err := svc.store.GetDevice(ctx, source.ID)
		if err != nil {
			t.Fatalf("GetDevice returned error: %v", err)
		}
		if device.NATProbe != nil {
			t.Fatalf("device NAT probe = %+v, must not be written by offline heartbeat", device.NATProbe)
		}
	})

	t.Run("revoked heartbeat must not write NAT probe", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		if _, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: source.ID, Reason: "retired"}); err != nil {
			t.Fatalf("RevokeDevice returned error: %v", err)
		}

		_, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
			AccountID: account.ID,
			NetworkID: network.ID,
			DeviceID:  source.ID,
			Status:    DeviceStatusOnline,
			NATProbe:  natProbeSummaryPtr(p2p.NATTypeFullCone),
		})
		if err == nil || !errors.Is(err, ErrRevoked) {
			t.Fatalf("revoked NAT heartbeat error = %v, want ErrRevoked", err)
		}
	})

	t.Run("negotiation must not read NAT probe across network or account", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		otherAccount, otherNetwork := mustAccountAndNetwork(t, ctx, svc)
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		otherTarget, _ := mustJoinTwoDevices(t, ctx, svc, otherAccount.ID, otherNetwork.ID)
		source = mustHeartbeatOnline(t, ctx, svc, source.ID)
		if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
			AccountID: otherAccount.ID,
			NetworkID: otherNetwork.ID,
			DeviceID:  otherTarget.ID,
			Status:    DeviceStatusOnline,
			NATProbe:  natProbeSummaryPtr(p2p.NATTypeSymmetric),
		}); err != nil {
			t.Fatalf("HeartbeatDevice other target returned error: %v", err)
		}

		_, err := svc.NegotiateP2PConnection(ctx, NegotiateP2PConnectionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: otherTarget.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) {
			t.Fatalf("cross-account negotiation error = %v, want ErrForbidden", err)
		}
	})
}

func natProbeForDevice(devices []Device, deviceID string) p2p.NATProbeSummary {
	for _, device := range devices {
		if device.ID == deviceID && device.NATProbe != nil {
			return *device.NATProbe
		}
	}
	return p2p.NATProbeSummary{}
}

func natProbeSummaryPtr(natType p2p.NATType) *p2p.NATProbeSummary {
	return &p2p.NATProbeSummary{
		Type:                   natType,
		UDPAvailable:           natType != p2p.NATTypeUDPBlocked && natType != p2p.NATTypeProbeFailed,
		MappingStable:          natType != p2p.NATTypeSymmetric,
		HolePunchRecommended:   natType == p2p.NATTypeFullCone,
		RelayRecommended:       natType == p2p.NATTypeSymmetric,
		SuccessfulObservations: 2,
		Reason:                 "test NAT probe summary",
	}
}
