package p2p

import (
	"testing"
	"time"
)

func TestSelectMultipathPrefersBestScoredDirectCandidate(t *testing.T) {
	decision := SelectMultipath(MultipathSelectionRequest{
		CurrentPathType: "",
		Candidates: []MultipathCandidate{
			{
				PathType: PathTypeRelay,
				Quality: ConnectionQualityInput{
					PathType:  PathTypeRelay,
					State:     PathStateFallbackRelay,
					LatencyMS: 80,
					JitterMS:  15,
				},
			},
			{
				PathType: PathTypePublicDirect,
				Quality: ConnectionQualityInput{
					PathType:  PathTypePublicDirect,
					State:     PathStatePublicDirectConnected,
					LatencyMS: 24,
					JitterMS:  4,
				},
			},
		},
	})

	if decision.PreferredPathType != PathTypePublicDirect {
		t.Fatalf("preferred path = %q, want public direct", decision.PreferredPathType)
	}
	if decision.SwitchReason != "direct_quality_better" {
		t.Fatalf("switch reason = %q, want direct_quality_better", decision.SwitchReason)
	}
	if decision.ScoreDelta <= 0 {
		t.Fatalf("score delta = %d, want direct score to beat relay", decision.ScoreDelta)
	}
	if decision.AutoSwitched {
		t.Fatalf("auto switched = true, want new-session selection only")
	}
}

func TestSelectMultipathAutoSwitchesDirectToRelayWhenDirectIsDegraded(t *testing.T) {
	decision := SelectMultipath(MultipathSelectionRequest{
		CurrentPathType: PathTypeLANDirect,
		Candidates: []MultipathCandidate{
			{
				PathType: PathTypeLANDirect,
				Quality: ConnectionQualityInput{
					PathType:           PathTypeLANDirect,
					State:              PathStateLANDirectConnected,
					LatencyMS:          420,
					PacketLossPermille: 260,
					JitterMS:           70,
				},
			},
			{
				PathType: PathTypeRelay,
				Quality: ConnectionQualityInput{
					PathType:  PathTypeRelay,
					State:     PathStateFallbackRelay,
					LatencyMS: 90,
					JitterMS:  10,
				},
			},
		},
	})

	if decision.PreferredPathType != PathTypeRelay || decision.ToPathType != PathTypeRelay {
		t.Fatalf("decision = %+v, want relay as selected target", decision)
	}
	if decision.FromPathType != PathTypeLANDirect {
		t.Fatalf("from path = %q, want lan_direct", decision.FromPathType)
	}
	if !decision.AutoSwitched {
		t.Fatalf("auto switched = false, want direct-to-relay hot switch")
	}
	if decision.SwitchReason != "direct_quality_degraded" {
		t.Fatalf("switch reason = %q, want direct_quality_degraded", decision.SwitchReason)
	}
	if decision.ScoreDelta <= 0 {
		t.Fatalf("score delta = %d, want relay score to beat degraded direct", decision.ScoreDelta)
	}
}

func TestSelectMultipathUsesRelayWhenDirectIsDisconnected(t *testing.T) {
	decision := SelectMultipath(MultipathSelectionRequest{
		CurrentPathType: PathTypePublicDirect,
		Candidates: []MultipathCandidate{
			{
				PathType: PathTypePublicDirect,
				Quality: ConnectionQualityInput{
					PathType: PathTypePublicDirect,
					State:    PathStateFailed,
				},
			},
			{
				PathType: PathTypeRelay,
				Quality: ConnectionQualityInput{
					PathType:  PathTypeRelay,
					State:     PathStateFallbackRelay,
					LatencyMS: 120,
				},
			},
		},
	})

	if decision.PreferredPathType != PathTypeRelay || !decision.AutoSwitched {
		t.Fatalf("decision = %+v, want automatic relay switch after direct disconnect", decision)
	}
	if decision.SwitchReason != "direct_disconnected" {
		t.Fatalf("switch reason = %q, want direct_disconnected", decision.SwitchReason)
	}
}

func TestSelectMultipathKeepsActiveRelayButPrefersRecoveredDirectForNewSession(t *testing.T) {
	decision := SelectMultipath(MultipathSelectionRequest{
		CurrentPathType: PathTypeRelay,
		Candidates: []MultipathCandidate{
			{
				PathType: PathTypeLANDirect,
				Quality: ConnectionQualityInput{
					PathType:  PathTypeLANDirect,
					State:     PathStateLANDirectConnected,
					LatencyMS: 16,
					JitterMS:  2,
				},
			},
			{
				PathType: PathTypeRelay,
				Quality: ConnectionQualityInput{
					PathType:  PathTypeRelay,
					State:     PathStateFallbackRelay,
					LatencyMS: 95,
					JitterMS:  12,
				},
			},
		},
	})

	if decision.PreferredPathType != PathTypeLANDirect {
		t.Fatalf("preferred path = %q, want recovered direct for new sessions", decision.PreferredPathType)
	}
	if decision.FromPathType != PathTypeRelay || decision.ToPathType != PathTypeLANDirect {
		t.Fatalf("decision = %+v, want relay-to-direct decision summary", decision)
	}
	if decision.AutoSwitched {
		t.Fatalf("auto switched = true, want active relay session to stay put")
	}
	if decision.SwitchReason != "direct_recovered_better_for_new_session" {
		t.Fatalf("switch reason = %q, want direct_recovered_better_for_new_session", decision.SwitchReason)
	}
}

func TestSelectMultipathSuppressesRelayToDirectJitterWithinCooldown(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	decision := SelectMultipath(MultipathSelectionRequest{
		CurrentPathType: PathTypeRelay,
		Now:             now,
		LastSwitchAt:    now.Add(-10 * time.Second),
		Policy: MultipathPolicy{
			SwitchCooldown:   time.Minute,
			MinScoreDelta:    8,
			DirectPoorScore:  55,
			DisconnectedOnly: false,
		},
		Candidates: []MultipathCandidate{
			{
				PathType: PathTypeLANDirect,
				Quality: ConnectionQualityInput{
					PathType:  PathTypeLANDirect,
					State:     PathStateLANDirectConnected,
					LatencyMS: 20,
				},
			},
			{
				PathType: PathTypeRelay,
				Quality: ConnectionQualityInput{
					PathType:  PathTypeRelay,
					State:     PathStateFallbackRelay,
					LatencyMS: 60,
				},
			},
		},
	})

	if decision.PreferredPathType != PathTypeRelay {
		t.Fatalf("preferred path = %q, want relay retained during cooldown", decision.PreferredPathType)
	}
	if !decision.SwitchSuppressed || decision.SuppressionReason != "switch_cooldown_active" {
		t.Fatalf("suppression = %v/%q, want cooldown suppression", decision.SwitchSuppressed, decision.SuppressionReason)
	}
	if decision.ToPathType != "" || decision.AutoSwitched {
		t.Fatalf("decision = %+v, want no switch target while suppressed", decision)
	}
}
