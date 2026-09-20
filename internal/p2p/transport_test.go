package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBuildNegotiationSelectsAuditableTransport(t *testing.T) {
	sourceNAT := natSummaryForTransport(NATTypeFullCone)
	sourceNAT.Reason = "raw probe mentioned private_key and 10.0.0.10"
	targetNAT := natSummaryForTransport(NATTypeRestrictedCone)
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:      "acct_owner",
		NetworkID:      "net_home",
		SourceDeviceID: "dev_source",
		TargetDeviceID: "dev_target",
		SourceCandidates: []Candidate{
			publicCandidate("dev_source", "198.51.100.10", 443, 50),
		},
		TargetCandidates: []Candidate{
			publicCandidate("dev_target", "198.51.100.20", 443, 60),
		},
		SourceNATProbe:     &sourceNAT,
		TargetNATProbe:     &targetNAT,
		AllowRelayFallback: true,
		TransportPolicy: TransportPolicy{
			AllowExperimentalUDP:  true,
			AllowExperimentalQUIC: true,
		},
	})

	if negotiation.TransportSelection == nil {
		t.Fatal("transport selection is nil, want auditable selection")
	}
	if negotiation.TransportSelection.Preferred.Kind != TransportKindQUICV1 {
		t.Fatalf("preferred transport = %+v, want QUIC when policy and NAT allow it", negotiation.TransportSelection.Preferred)
	}
	if !negotiation.TransportSelection.Preferred.Experimental {
		t.Fatalf("preferred transport = %+v, want experimental marker", negotiation.TransportSelection.Preferred)
	}
	if !hasTransportCandidate(negotiation.TransportSelection, TransportKindUDPDatagramV1) {
		t.Fatalf("transport selection = %+v, want UDP datagram candidate", negotiation.TransportSelection)
	}
	if !hasTransportCandidate(negotiation.TransportSelection, TransportKindTCPTLSV1) {
		t.Fatalf("transport selection = %+v, want TCP/TLS fallback candidate", negotiation.TransportSelection)
	}
	assertP2PJSONDoesNotContain(t, negotiation.TransportSelection, "private_key", "10.0.0.10", "join_token")
}

func TestBuildNegotiationKeepsConservativeTransportByDefault(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
		AllowRelayFallback: true,
	})

	if negotiation.TransportSelection == nil {
		t.Fatal("transport selection is nil, want TCP/TLS decision")
	}
	if negotiation.TransportSelection.Preferred.Kind != TransportKindTCPTLSV1 {
		t.Fatalf("preferred transport = %+v, want TCP/TLS by default", negotiation.TransportSelection.Preferred)
	}
	if negotiation.TransportSelection.Preferred.Experimental {
		t.Fatalf("preferred transport = %+v, default transport must not be experimental", negotiation.TransportSelection.Preferred)
	}
	if hasTransportCandidate(negotiation.TransportSelection, TransportKindUDPDatagramV1) ||
		hasTransportCandidate(negotiation.TransportSelection, TransportKindQUICV1) {
		t.Fatalf("transport selection = %+v, default policy must not expose UDP/QUIC candidates", negotiation.TransportSelection)
	}
}

func TestBuildNegotiationRequiresNATProbeBeforeExperimentalTransport(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{publicCandidate("dev_source", "198.51.100.10", 443, 50)},
		TargetCandidates:   []Candidate{publicCandidate("dev_target", "198.51.100.20", 443, 60)},
		AllowRelayFallback: true,
		TransportPolicy: TransportPolicy{
			AllowExperimentalUDP:  true,
			AllowExperimentalQUIC: true,
		},
	})

	if negotiation.TransportSelection == nil {
		t.Fatal("transport selection is nil, want conservative TCP/TLS decision")
	}
	if negotiation.TransportSelection.Preferred.Kind != TransportKindTCPTLSV1 {
		t.Fatalf("preferred transport = %+v, want TCP/TLS until both NAT probes are available", negotiation.TransportSelection.Preferred)
	}
	if !hasExcludedTransport(negotiation.TransportSelection, TransportKindUDPDatagramV1) ||
		!hasExcludedTransport(negotiation.TransportSelection, TransportKindQUICV1) {
		t.Fatalf("transport selection = %+v, want UDP/QUIC excluded without NAT probes", negotiation.TransportSelection)
	}
}

func TestBuildNegotiationExcludesExperimentalTransportsWhenNATRequiresRelay(t *testing.T) {
	blocked := natSummaryForTransport(NATTypeUDPBlocked)
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:      "acct_owner",
		NetworkID:      "net_home",
		SourceDeviceID: "dev_source",
		TargetDeviceID: "dev_target",
		SourceCandidates: []Candidate{
			publicCandidate("dev_source", "198.51.100.10", 443, 50),
		},
		TargetCandidates: []Candidate{
			publicCandidate("dev_target", "198.51.100.20", 443, 60),
		},
		SourceNATProbe:     &blocked,
		AllowRelayFallback: true,
		TransportPolicy: TransportPolicy{
			AllowExperimentalUDP:  true,
			AllowExperimentalQUIC: true,
		},
	})

	if negotiation.State != PathStateFallbackRelay || negotiation.PreferredPathType != PathTypeRelay {
		t.Fatalf("negotiation = %+v, want Relay fallback when NAT blocks UDP", negotiation)
	}
	if negotiation.TransportSelection == nil || negotiation.TransportSelection.Preferred.Kind != TransportKindRelay {
		t.Fatalf("transport selection = %+v, want Relay transport", negotiation.TransportSelection)
	}
	if !hasExcludedTransport(negotiation.TransportSelection, TransportKindUDPDatagramV1) ||
		!hasExcludedTransport(negotiation.TransportSelection, TransportKindQUICV1) {
		t.Fatalf("transport selection = %+v, want UDP/QUIC exclusions", negotiation.TransportSelection)
	}
}

func TestUDPDatagramProberLoopbackSmokeAndCancellation(t *testing.T) {
	endpoint, stop := startUDPEchoServer(t)
	defer stop()

	result, err := UDPDatagramProber{Timeout: time.Second}.Probe(context.Background(), DatagramProbeRequest{
		Endpoint: endpoint,
		Payload:  []byte("meshlink-udp-probe"),
	})
	if err != nil {
		t.Fatalf("UDP probe returned error: %v", err)
	}
	if result.Transport != TransportKindUDPDatagramV1 || result.BytesSent == 0 || result.BytesReceived == 0 {
		t.Fatalf("probe result = %+v, want UDP datagram bytes", result)
	}
	if result.AuditReason == "" || strings.Contains(strings.ToLower(result.AuditReason), "private_key") {
		t.Fatalf("audit reason = %q, want sanitized reason", result.AuditReason)
	}
	assertP2PJSONDoesNotContain(t, result, "private_key", "join_token")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = UDPDatagramProber{Timeout: time.Second}.Probe(cancelled, DatagramProbeRequest{
		Endpoint: endpoint,
		Payload:  []byte("cancelled"),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled UDP probe error = %v, want context.Canceled", err)
	}
}

func hasTransportCandidate(selection *TransportSelection, kind TransportKind) bool {
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

func hasExcludedTransport(selection *TransportSelection, kind TransportKind) bool {
	if selection == nil {
		return false
	}
	for _, candidate := range selection.Excluded {
		if candidate.Kind == kind {
			return true
		}
	}
	return false
}

func natSummaryForTransport(natType NATType) NATProbeSummary {
	return NormalizeNATProbeSummary(NATProbeSummary{
		Type:                   natType,
		UDPAvailable:           natType != NATTypeUDPBlocked && natType != NATTypeProbeFailed,
		MappingStable:          natType != NATTypeSymmetric,
		HolePunchRecommended:   natType != NATTypeUDPBlocked && natType != NATTypeProbeFailed && natType != NATTypeSymmetric,
		RelayRecommended:       natType == NATTypeUDPBlocked || natType == NATTypeProbeFailed || natType == NATTypeSymmetric,
		SuccessfulObservations: 2,
		Reason:                 "transport test NAT summary",
	})
}

func startUDPEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp4: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1024)
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		_, _ = conn.WriteTo(buf[:n], addr)
	}()
	return conn.LocalAddr().String(), func() {
		_ = conn.Close()
		<-done
	}
}

func TestTransportSelectionJSONDoesNotLeakRawReason(t *testing.T) {
	selection := SelectTransport(TransportSelectionRequest{
		Pairs: []CandidatePair{{
			Source:   publicCandidate("dev_source", "198.51.100.10", 443, 50),
			Target:   publicCandidate("dev_target", "198.51.100.20", 443, 60),
			PathType: PathTypePublicDirect,
			Priority: 110,
		}},
		SourceNATProbe: &NATProbeSummary{
			Type:                 NATTypeFullCone,
			UDPAvailable:         true,
			MappingStable:        true,
			HolePunchRecommended: true,
			Reason:               "raw private_key and 10.0.0.10",
		},
		Policy: TransportPolicy{AllowExperimentalUDP: true},
	})
	encoded, err := json.Marshal(selection)
	if err != nil {
		t.Fatalf("marshal selection: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	if strings.Contains(lower, "private_key") || strings.Contains(lower, "10.0.0.10") {
		t.Fatalf("transport selection leaked raw NAT reason: %s", encoded)
	}
}
