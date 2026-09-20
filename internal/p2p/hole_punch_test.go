package p2p

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHolePuncherConnectsWithSimultaneousDialWhenNATAndUDPPortsAreSynced(t *testing.T) {
	negotiation := holePunchNegotiation(
		natSummaryForTransport(NATTypeFullCone),
		natSummaryForTransport(NATTypeRestrictedCone),
		udpPublicCandidate("dev_source", "198.51.100.10", 51000, 50),
		udpPublicCandidate("dev_target", "198.51.100.20", 52000, 60),
	)
	network := &scriptedHolePunchNetwork{
		outcomes: []HolePunchOutcome{{Status: HolePunchOutcomeConnected}},
	}

	result := HolePuncher{
		Network: network,
		Policy:  HolePunchPolicy{MaxAttempts: 3, AttemptTimeout: time.Second},
	}.Connect(context.Background(), negotiation)

	if result.State != PathStatePublicDirectConnected || result.PathType != PathTypePublicDirect {
		t.Fatalf("result = %+v, want public direct connected", result)
	}
	if result.Phase != HolePunchPhaseDirectConnected {
		t.Fatalf("phase = %q, want direct connected", result.Phase)
	}
	if len(result.SyncedCandidatePorts) != 2 {
		t.Fatalf("synced candidate ports = %+v, want source and target UDP ports", result.SyncedCandidatePorts)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Status != AttemptStatusSucceeded || !result.Attempts[0].Simultaneous {
		t.Fatalf("attempts = %+v, want one simultaneous successful attempt", result.Attempts)
	}
	if len(network.calls) != 1 || !network.calls[0].Simultaneous {
		t.Fatalf("network calls = %+v, want simultaneous punch call", network.calls)
	}
	assertP2PJSONDoesNotContain(t, result, "private_key", "join_token", "invite_token")
}

func TestHolePuncherRetriesPortChangeThenConnects(t *testing.T) {
	negotiation := holePunchNegotiation(
		natSummaryForTransport(NATTypeFullCone),
		natSummaryForTransport(NATTypeFullCone),
		udpPublicCandidate("dev_source", "198.51.100.10", 51000, 50),
		udpPublicCandidate("dev_target", "198.51.100.20", 52000, 60),
	)
	network := &scriptedHolePunchNetwork{
		outcomes: []HolePunchOutcome{
			{Status: HolePunchOutcomePortChanged, ObservedTargetPort: 62000, DiagnosticMessage: "target mapping changed"},
			{Status: HolePunchOutcomeConnected},
		},
	}

	result := HolePuncher{
		Network: network,
		Policy:  HolePunchPolicy{MaxAttempts: 3},
	}.Connect(context.Background(), negotiation)

	if result.State != PathStatePublicDirectConnected {
		t.Fatalf("result = %+v, want direct connected after port resync", result)
	}
	if len(result.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want retry after port change", result.Attempts)
	}
	if result.Attempts[0].OutcomeStatus != HolePunchOutcomePortChanged {
		t.Fatalf("first attempt = %+v, want port change outcome", result.Attempts[0])
	}
	if result.Attempts[1].Pair.Target.Port != 62000 {
		t.Fatalf("second attempt target port = %d, want resynced 62000", result.Attempts[1].Pair.Target.Port)
	}
	if result.PathType == PathTypeRelay || result.RelayFallback.Reason == "hole_punch_retry_limit_reached" {
		t.Fatalf("result = %+v, must not fall back to Relay after successful retry", result)
	}
}

func TestHolePuncherStopsAtRetryLimitAndFallsBackAfterTimeoutsAndHalfConnections(t *testing.T) {
	negotiation := holePunchNegotiation(
		natSummaryForTransport(NATTypeFullCone),
		natSummaryForTransport(NATTypePortRestrictedCone),
		udpPublicCandidate("dev_source", "198.51.100.10", 51000, 50),
		udpPublicCandidate("dev_target", "198.51.100.20", 52000, 60),
	)
	network := &scriptedHolePunchNetwork{
		outcomes: []HolePunchOutcome{
			{Status: HolePunchOutcomeTimeout},
			{Status: HolePunchOutcomeHalfConnected},
			{Status: HolePunchOutcomeTimeout},
			{Status: HolePunchOutcomeConnected},
		},
	}

	result := HolePuncher{
		Network: network,
		Policy:  HolePunchPolicy{MaxAttempts: 3, AttemptTimeout: time.Millisecond},
	}.Connect(context.Background(), negotiation)

	if result.State != PathStateFallbackRelay || result.PathType != PathTypeRelay {
		t.Fatalf("result = %+v, want Relay fallback after retry limit", result)
	}
	if len(network.calls) != 3 || len(result.Attempts) != 3 {
		t.Fatalf("network calls=%d attempts=%d, want exactly retry limit", len(network.calls), len(result.Attempts))
	}
	if result.Attempts[1].OutcomeStatus != HolePunchOutcomeHalfConnected {
		t.Fatalf("attempts = %+v, want half-connected attempt recorded", result.Attempts)
	}
	if result.FallbackReason != "hole_punch_retry_limit_reached" || result.RelayFallback.Reason != "hole_punch_retry_limit_reached" {
		t.Fatalf("fallback = %+v reason=%q, want retry-limit Relay fallback", result.RelayFallback, result.FallbackReason)
	}
}

func TestHolePuncherUsesRelayWithoutDirectTrafficWhenNATRequiresRelay(t *testing.T) {
	negotiation := holePunchNegotiation(
		natSummaryForTransport(NATTypeSymmetric),
		natSummaryForTransport(NATTypeFullCone),
		udpPublicCandidate("dev_source", "198.51.100.10", 51000, 50),
		udpPublicCandidate("dev_target", "198.51.100.20", 52000, 60),
	)
	network := &scriptedHolePunchNetwork{}

	result := HolePuncher{
		Network: network,
		Policy:  HolePunchPolicy{MaxAttempts: 3},
	}.Connect(context.Background(), negotiation)

	if result.State != PathStateFallbackRelay || result.PathType != PathTypeRelay {
		t.Fatalf("result = %+v, want immediate Relay fallback", result)
	}
	if len(network.calls) != 0 || len(result.Attempts) != 0 {
		t.Fatalf("network calls=%d attempts=%d, want no direct traffic when NAT requires Relay", len(network.calls), len(result.Attempts))
	}
	if result.FallbackReason != "hole_punch_nat_not_viable" {
		t.Fatalf("fallback reason = %q, want NAT viability reason", result.FallbackReason)
	}
}

func TestHolePuncherFailsClosedWhenRelayFallbackDisabled(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{udpPublicCandidate("dev_source", "198.51.100.10", 51000, 50)},
		TargetCandidates:   []Candidate{udpPublicCandidate("dev_target", "198.51.100.20", 52000, 60)},
		SourceNATProbe:     natProbeSummaryPtrForHolePunch(NATTypeFullCone),
		TargetNATProbe:     natProbeSummaryPtrForHolePunch(NATTypeFullCone),
		AllowRelayFallback: false,
	})
	network := &scriptedHolePunchNetwork{
		outcomes: []HolePunchOutcome{{Status: HolePunchOutcomeTimeout}},
	}

	result := HolePuncher{
		Network: network,
		Policy:  HolePunchPolicy{MaxAttempts: 1},
	}.Connect(context.Background(), negotiation)

	if result.State != PathStateFailed || result.PathType == PathTypeRelay || result.RelayFallback.CreateRelaySession {
		t.Fatalf("result = %+v, want failed without Relay fallback", result)
	}
}

func TestHolePunchSummaryDoesNotExposeRawNATReasons(t *testing.T) {
	sourceNAT := NATProbeSummary{
		Type:                   NATTypeFullCone,
		UDPAvailable:           true,
		MappingStable:          true,
		HolePunchRecommended:   true,
		SuccessfulObservations: 2,
		Reason:                 "raw private_key and 10.0.0.10",
	}
	targetNAT := sourceNAT
	negotiation := holePunchNegotiation(
		sourceNAT,
		targetNAT,
		udpPublicCandidate("dev_source", "198.51.100.10", 51000, 50),
		udpPublicCandidate("dev_target", "198.51.100.20", 52000, 60),
	)

	summary := SummarizeHolePunch(negotiation, HolePunchPolicy{MaxAttempts: 2})
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	if strings.Contains(lower, "private_key") || strings.Contains(lower, "10.0.0.10") || strings.Contains(lower, "join_token") {
		t.Fatalf("summary leaked raw/sensitive data: %s", encoded)
	}
	if !summary.Ready || summary.CandidatePortCount != 2 || summary.MaxAttempts != 2 {
		t.Fatalf("summary = %+v, want ready summary with port count and max attempts", summary)
	}
}

func holePunchNegotiation(sourceNAT, targetNAT NATProbeSummary, source, target Candidate) Negotiation {
	return BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     source.DeviceID,
		TargetDeviceID:     target.DeviceID,
		SourceCandidates:   []Candidate{source},
		TargetCandidates:   []Candidate{target},
		SourceNATProbe:     &sourceNAT,
		TargetNATProbe:     &targetNAT,
		AllowRelayFallback: true,
		TransportPolicy: TransportPolicy{
			AllowExperimentalUDP: true,
		},
	})
}

func udpPublicCandidate(deviceID, address string, port int, priority int) Candidate {
	candidate := publicCandidate(deviceID, address, port, priority)
	candidate.Protocol = ProtocolUDP
	return candidate
}

func natProbeSummaryPtrForHolePunch(natType NATType) *NATProbeSummary {
	summary := natSummaryForTransport(natType)
	return &summary
}

type scriptedHolePunchNetwork struct {
	outcomes []HolePunchOutcome
	calls    []HolePunchAttempt
}

func (n *scriptedHolePunchNetwork) Punch(ctx context.Context, attempt HolePunchAttempt) HolePunchOutcome {
	n.calls = append(n.calls, attempt)
	if err := ctx.Err(); err != nil {
		return HolePunchOutcome{Status: HolePunchOutcomeTimeout, DiagnosticMessage: err.Error()}
	}
	idx := len(n.calls) - 1
	if idx >= len(n.outcomes) {
		return HolePunchOutcome{Status: HolePunchOutcomeFailed, DiagnosticMessage: "no scripted outcome"}
	}
	return n.outcomes[idx]
}
