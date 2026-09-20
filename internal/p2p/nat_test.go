package p2p

import "testing"

func TestClassifyNATProbeCoversCommonResults(t *testing.T) {
	tests := []struct {
		name       string
		observed   []NATProbeObservation
		wantType   NATType
		wantUDP    bool
		wantStable bool
		wantPunch  bool
		wantRelay  bool
	}{
		{
			name:     "unknown without observations",
			wantType: NATTypeUnknown,
		},
		{
			name: "open internet no NAT",
			observed: []NATProbeObservation{
				natObservation("stun-a", "198.51.100.10", 51820, "198.51.100.10", 51820, true, true),
			},
			wantType:   NATTypeOpenInternet,
			wantUDP:    true,
			wantStable: true,
			wantPunch:  true,
		},
		{
			name: "full cone",
			observed: []NATProbeObservation{
				natObservation("stun-a", "10.0.0.10", 51820, "203.0.113.10", 41000, true, true),
				natObservation("stun-b", "10.0.0.10", 51820, "203.0.113.10", 41000, true, true),
			},
			wantType:   NATTypeFullCone,
			wantUDP:    true,
			wantStable: true,
			wantPunch:  true,
		},
		{
			name: "restricted cone",
			observed: []NATProbeObservation{
				natObservation("stun-a", "10.0.0.10", 51820, "203.0.113.11", 41001, false, true),
				natObservation("stun-b", "10.0.0.10", 51820, "203.0.113.11", 41001, false, true),
			},
			wantType:   NATTypeRestrictedCone,
			wantUDP:    true,
			wantStable: true,
			wantPunch:  true,
		},
		{
			name: "port restricted cone",
			observed: []NATProbeObservation{
				natObservation("stun-a", "10.0.0.10", 51820, "203.0.113.12", 41002, false, false),
				natObservation("stun-b", "10.0.0.10", 51820, "203.0.113.12", 41002, false, false),
			},
			wantType:   NATTypePortRestrictedCone,
			wantUDP:    true,
			wantStable: true,
			wantPunch:  true,
		},
		{
			name: "symmetric NAT",
			observed: []NATProbeObservation{
				natObservation("stun-a", "10.0.0.10", 51820, "203.0.113.13", 41003, false, false),
				natObservation("stun-b", "10.0.0.10", 51820, "203.0.113.13", 49152, false, false),
			},
			wantType:  NATTypeSymmetric,
			wantUDP:   true,
			wantRelay: true,
		},
		{
			name: "UDP blocked",
			observed: []NATProbeObservation{
				{ProbeServer: "stun-a", LocalAddress: "10.0.0.10", LocalPort: 51820},
				{ProbeServer: "stun-b", LocalAddress: "10.0.0.10", LocalPort: 51820},
			},
			wantType:  NATTypeUDPBlocked,
			wantRelay: true,
		},
		{
			name: "probe failed",
			observed: []NATProbeObservation{
				{ProbeServer: "stun-a", LocalAddress: "10.0.0.10", LocalPort: 51820, ProbeError: "probe socket failed"},
			},
			wantType:  NATTypeProbeFailed,
			wantRelay: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyNATProbe(tc.observed)
			if got.Type != tc.wantType {
				t.Fatalf("Type = %q, want %q; summary = %+v", got.Type, tc.wantType, got)
			}
			if got.UDPAvailable != tc.wantUDP {
				t.Fatalf("UDPAvailable = %v, want %v; summary = %+v", got.UDPAvailable, tc.wantUDP, got)
			}
			if got.MappingStable != tc.wantStable {
				t.Fatalf("MappingStable = %v, want %v; summary = %+v", got.MappingStable, tc.wantStable, got)
			}
			if got.HolePunchRecommended != tc.wantPunch {
				t.Fatalf("HolePunchRecommended = %v, want %v; summary = %+v", got.HolePunchRecommended, tc.wantPunch, got)
			}
			if got.RelayRecommended != tc.wantRelay {
				t.Fatalf("RelayRecommended = %v, want %v; summary = %+v", got.RelayRecommended, tc.wantRelay, got)
			}
			if got.Reason == "" {
				t.Fatalf("Reason is empty for summary %+v", got)
			}
			assertP2PJSONDoesNotContain(t, got, "private_key", "invite_token", "join_token", "probe socket failed")
		})
	}
}

func TestClassifyNATProbeMappingStabilityAndRecommendations(t *testing.T) {
	stable := ClassifyNATProbe([]NATProbeObservation{
		natObservation("stun-a", "10.0.0.20", 51820, "203.0.113.20", 42000, false, false),
		natObservation("stun-b", "10.0.0.20", 51820, "203.0.113.20", 42000, false, false),
	})
	if !stable.MappingStable || !stable.HolePunchRecommended || stable.RelayRecommended {
		t.Fatalf("stable cone summary = %+v, want stable mapping with hole punching before Relay", stable)
	}

	unstable := ClassifyNATProbe([]NATProbeObservation{
		natObservation("stun-a", "10.0.0.20", 51820, "203.0.113.20", 42000, false, false),
		natObservation("stun-b", "10.0.0.20", 51820, "203.0.113.20", 42001, false, false),
	})
	if unstable.MappingStable || unstable.HolePunchRecommended || !unstable.RelayRecommended || unstable.Type != NATTypeSymmetric {
		t.Fatalf("unstable summary = %+v, want symmetric NAT and direct Relay recommendation", unstable)
	}
}

func natObservation(probeServer, localAddress string, localPort int, mappedAddress string, mappedPort int, changedAddressResponse, changedPortResponse bool) NATProbeObservation {
	return NATProbeObservation{
		ProbeServer:            probeServer,
		LocalAddress:           localAddress,
		LocalPort:              localPort,
		MappedAddress:          mappedAddress,
		MappedPort:             mappedPort,
		ResponseReceived:       true,
		ChangedAddressResponse: changedAddressResponse,
		ChangedPortResponse:    changedPortResponse,
	}
}
