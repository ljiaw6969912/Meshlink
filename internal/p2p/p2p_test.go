package p2p

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCandidatesDeduplicatesAndSortsIPv4Candidates(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	candidates, err := NormalizeCandidates([]Candidate{
		{
			DeviceID:   "dev_source",
			NetworkID:  "net_home",
			Address:    "198.51.100.10",
			Port:       443,
			Protocol:   ProtocolTCP,
			Scope:      CandidateScopePublic,
			Priority:   100,
			Source:     "upnp",
			TTLSeconds: 30,
		},
		{
			DeviceID:   "dev_source",
			NetworkID:  "net_home",
			Address:    "192.168.1.20",
			Port:       9000,
			Protocol:   ProtocolTCP,
			Scope:      CandidateScopeLAN,
			Priority:   10,
			Source:     "interface",
			TTLSeconds: 30,
		},
		{
			DeviceID:   "dev_source",
			NetworkID:  "net_home",
			Address:    "192.168.1.20",
			Port:       9000,
			Protocol:   ProtocolTCP,
			Scope:      CandidateScopeLAN,
			Priority:   80,
			Source:     "interface-rescan",
			TTLSeconds: 30,
		},
		{
			DeviceID:   "dev_source",
			NetworkID:  "net_home",
			Address:    "203.0.113.8",
			Port:       18082,
			Protocol:   ProtocolTCP,
			Scope:      CandidateScopeRelay,
			Priority:   1,
			Source:     "relay",
			TTLSeconds: 30,
		},
	}, now)
	if err != nil {
		t.Fatalf("NormalizeCandidates returned error: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidate count = %d, want 3 after dedupe: %+v", len(candidates), candidates)
	}
	if candidates[0].Scope != CandidateScopeLAN || candidates[0].Priority != 80 || candidates[0].Source != "interface-rescan" {
		t.Fatalf("first candidate = %+v, want highest-priority LAN duplicate first", candidates[0])
	}
	if candidates[1].Scope != CandidateScopePublic {
		t.Fatalf("second candidate = %+v, want public after LAN", candidates[1])
	}
	if candidates[2].Scope != CandidateScopeRelay {
		t.Fatalf("third candidate = %+v, want relay last", candidates[2])
	}
	for _, candidate := range candidates {
		if !candidate.SeenAt.Equal(now) {
			t.Fatalf("candidate SeenAt = %s, want %s", candidate.SeenAt, now)
		}
		if !candidate.ExpiresAt.Equal(now.Add(30 * time.Second)) {
			t.Fatalf("candidate ExpiresAt = %s, want %s", candidate.ExpiresAt, now.Add(30*time.Second))
		}
	}
}

func TestNormalizeCandidatesRejectsIPv6ForTask6A(t *testing.T) {
	_, err := NormalizeCandidates([]Candidate{
		{
			DeviceID:   "dev_source",
			NetworkID:  "net_home",
			Address:    "2001:db8::10",
			Port:       9000,
			Protocol:   ProtocolTCP,
			Scope:      CandidateScopeLAN,
			Priority:   10,
			TTLSeconds: 30,
		},
	}, time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "IPv4") {
		t.Fatalf("NormalizeCandidates error = %v, want IPv4-only rejection", err)
	}
}

func TestConnectorUsesLANDirectOnSuccess(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
		AllowRelayFallback: true,
	})
	dialer := &FakeDialer{}

	result := Connector{Dialer: dialer}.Connect(context.Background(), negotiation)

	if result.State != PathStateLANDirectConnected || result.PathType != PathTypeLANDirect {
		t.Fatalf("result = %+v, want LAN direct connected", result)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Status != AttemptStatusSucceeded {
		t.Fatalf("attempts = %+v, want one successful attempt", result.Attempts)
	}
	if len(dialer.Dialed) != 1 || dialer.Dialed[0].Target.Address != "10.0.0.12" {
		t.Fatalf("dialed pairs = %+v, want target LAN address", dialer.Dialed)
	}
}

func TestBuildCandidatePairsOrdersLANDirectBeforePublicDirectAndSkipsRelay(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:      "acct_owner",
		NetworkID:      "net_home",
		SourceDeviceID: "dev_source",
		TargetDeviceID: "dev_target",
		SourceCandidates: []Candidate{
			publicCandidate("dev_source", "198.51.100.10", 8443, 900),
			lanCandidate("dev_source", "10.0.0.11", 9100, 10),
			{
				DeviceID:   "dev_source",
				NetworkID:  "net_home",
				Address:    "203.0.113.10",
				Port:       18082,
				Protocol:   ProtocolTCP,
				Scope:      CandidateScopeRelay,
				Priority:   1000,
				TTLSeconds: 60,
				SeenAt:     time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
				ExpiresAt:  time.Date(2026, 7, 9, 10, 1, 0, 0, time.UTC),
			},
		},
		TargetCandidates: []Candidate{
			publicCandidate("dev_target", "198.51.100.20", 8443, 900),
			lanCandidate("dev_target", "10.0.0.12", 9100, 10),
			{
				DeviceID:   "dev_target",
				NetworkID:  "net_home",
				Address:    "203.0.113.20",
				Port:       18082,
				Protocol:   ProtocolTCP,
				Scope:      CandidateScopeRelay,
				Priority:   1000,
				TTLSeconds: 60,
				SeenAt:     time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
				ExpiresAt:  time.Date(2026, 7, 9, 10, 1, 0, 0, time.UTC),
			},
		},
		AllowRelayFallback: true,
	})

	if negotiation.PreferredPathType != PathTypeLANDirect {
		t.Fatalf("preferred path = %q, want LAN direct before public direct", negotiation.PreferredPathType)
	}
	if len(negotiation.CandidatePairs) != 2 {
		t.Fatalf("candidate pairs = %+v, want LAN and public direct pairs only", negotiation.CandidatePairs)
	}
	if negotiation.CandidatePairs[0].PathType != PathTypeLANDirect {
		t.Fatalf("first pair = %+v, want LAN direct despite lower priority", negotiation.CandidatePairs[0])
	}
	if negotiation.CandidatePairs[1].PathType != PathTypePublicDirect {
		t.Fatalf("second pair = %+v, want public direct", negotiation.CandidatePairs[1])
	}
	for _, pair := range negotiation.CandidatePairs {
		if pair.Source.Scope == CandidateScopeRelay || pair.Target.Scope == CandidateScopeRelay {
			t.Fatalf("candidate pairs include relay candidate as direct pair: %+v", negotiation.CandidatePairs)
		}
	}
}

func TestConnectorTriesPublicDirectAfterLANDirectFailure(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:      "acct_owner",
		NetworkID:      "net_home",
		SourceDeviceID: "dev_source",
		TargetDeviceID: "dev_target",
		SourceCandidates: []Candidate{
			lanCandidate("dev_source", "10.0.0.11", 9100, 50),
			publicCandidate("dev_source", "198.51.100.10", 8443, 50),
		},
		TargetCandidates: []Candidate{
			lanCandidate("dev_target", "10.0.0.12", 9100, 60),
			publicCandidate("dev_target", "198.51.100.20", 8443, 60),
		},
		AllowRelayFallback: true,
	})
	if len(negotiation.CandidatePairs) != 2 {
		t.Fatalf("candidate pairs = %+v, want LAN then public pairs", negotiation.CandidatePairs)
	}
	dialer := &FakeDialer{
		Errors: map[string]error{
			pairKey(negotiation.CandidatePairs[0]): errors.New("lan refused"),
		},
	}

	result := Connector{Dialer: dialer}.Connect(context.Background(), negotiation)

	if result.State != PathStatePublicDirectConnected || result.PathType != PathTypePublicDirect {
		t.Fatalf("result = %+v, want public direct connected after LAN failure", result)
	}
	if len(result.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want LAN failure followed by public success", result.Attempts)
	}
	if result.Attempts[0].PathType != PathTypeLANDirect || result.Attempts[0].Status != AttemptStatusFailed {
		t.Fatalf("first attempt = %+v, want failed LAN direct", result.Attempts[0])
	}
	if result.Attempts[1].PathType != PathTypePublicDirect || result.Attempts[1].Status != AttemptStatusSucceeded {
		t.Fatalf("second attempt = %+v, want successful public direct", result.Attempts[1])
	}
	if len(dialer.Dialed) != 2 || dialer.Dialed[1].Target.Address != "198.51.100.20" {
		t.Fatalf("dialed pairs = %+v, want public target tried second", dialer.Dialed)
	}
}

func TestConnectorHandlesPublicDirectFailureWithRelayFallbackPolicy(t *testing.T) {
	for _, tc := range []struct {
		name               string
		allowRelayFallback bool
		wantState          PathState
		wantPathType       PathType
	}{
		{
			name:               "fallback enabled",
			allowRelayFallback: true,
			wantState:          PathStateFallbackRelay,
			wantPathType:       PathTypeRelay,
		},
		{
			name:               "fallback disabled",
			allowRelayFallback: false,
			wantState:          PathStateFailed,
			wantPathType:       "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			negotiation := BuildNegotiation(NegotiationRequest{
				AccountID:          "acct_owner",
				NetworkID:          "net_home",
				SourceDeviceID:     "dev_source",
				TargetDeviceID:     "dev_target",
				SourceCandidates:   []Candidate{publicCandidate("dev_source", "198.51.100.10", 8443, 50)},
				TargetCandidates:   []Candidate{publicCandidate("dev_target", "198.51.100.20", 8443, 60)},
				AllowRelayFallback: tc.allowRelayFallback,
			})

			result := Connector{Dialer: &FakeDialer{Err: errors.New("public refused")}}.Connect(context.Background(), negotiation)

			if result.State != tc.wantState || result.PathType != tc.wantPathType {
				t.Fatalf("result = %+v, want state %q path %q", result, tc.wantState, tc.wantPathType)
			}
			if len(result.Attempts) != 1 || result.Attempts[0].PathType != PathTypePublicDirect || result.Attempts[0].Status != AttemptStatusFailed {
				t.Fatalf("attempts = %+v, want one failed public direct attempt", result.Attempts)
			}
			if result.RelayFallback.CreateRelaySession != tc.allowRelayFallback {
				t.Fatalf("relay fallback = %+v, want create=%v", result.RelayFallback, tc.allowRelayFallback)
			}
		})
	}
}

func TestConnectorFallsBackToRelayAfterLANDirectFailure(t *testing.T) {
	dialErr := errors.New("connection refused")
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
		AllowRelayFallback: true,
	})

	result := Connector{Dialer: &FakeDialer{Err: dialErr}}.Connect(context.Background(), negotiation)

	if result.State != PathStateFallbackRelay || result.PathType != PathTypeRelay {
		t.Fatalf("result = %+v, want Relay fallback", result)
	}
	if !result.RelayFallback.CreateRelaySession || result.RelayFallback.Reason == "" {
		t.Fatalf("relay fallback = %+v, want create-session semantics and reason", result.RelayFallback)
	}
	if !strings.Contains(result.FallbackReason, "connection refused") {
		t.Fatalf("fallback reason = %q, want dial error", result.FallbackReason)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Status != AttemptStatusFailed {
		t.Fatalf("attempts = %+v, want failed LAN attempt", result.Attempts)
	}
}

func TestConnectorFailsWithoutCandidatesWhenRelayFallbackDisabled(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		AllowRelayFallback: false,
	})

	result := Connector{Dialer: &FakeDialer{}}.Connect(context.Background(), negotiation)

	if result.State != PathStateFailed {
		t.Fatalf("state = %q, want failed", result.State)
	}
	if result.PathType != "" {
		t.Fatalf("path type = %q, want empty path when no candidate exists", result.PathType)
	}
	if result.DiagnosticMessage == "" || strings.Contains(result.DiagnosticMessage, string(PathTypeLANDirect)) {
		t.Fatalf("diagnostic = %q, want no-candidate failure without direct success claim", result.DiagnosticMessage)
	}
}

func TestConnectorRespectsDisabledRelayFallbackWhenDialerUnavailable(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
		AllowRelayFallback: false,
	})

	result := Connector{}.Connect(context.Background(), negotiation)

	if result.State != PathStateFailed {
		t.Fatalf("state = %q, want failed when dialer is unavailable and Relay fallback is disabled", result.State)
	}
	if result.PathType == PathTypeRelay || result.RelayFallback.CreateRelaySession {
		t.Fatalf("result = %+v, must not force Relay fallback when disabled", result)
	}
	if !strings.Contains(result.FallbackReason, "direct_dialer_unavailable") {
		t.Fatalf("fallback reason = %q, want dialer unavailable reason", result.FallbackReason)
	}
}

func publicCandidate(deviceID, address string, port int, priority int) Candidate {
	return Candidate{
		DeviceID:   deviceID,
		NetworkID:  "net_home",
		Address:    address,
		Port:       port,
		Protocol:   ProtocolTCP,
		Scope:      CandidateScopePublic,
		Priority:   priority,
		TTLSeconds: 60,
		SeenAt:     time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
		ExpiresAt:  time.Date(2026, 7, 9, 10, 1, 0, 0, time.UTC),
	}
}

func lanCandidate(deviceID, address string, port int, priority int) Candidate {
	return Candidate{
		DeviceID:   deviceID,
		NetworkID:  "net_home",
		Address:    address,
		Port:       port,
		Protocol:   ProtocolTCP,
		Scope:      CandidateScopeLAN,
		Priority:   priority,
		TTLSeconds: 60,
		SeenAt:     time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
		ExpiresAt:  time.Date(2026, 7, 9, 10, 1, 0, 0, time.UTC),
	}
}
