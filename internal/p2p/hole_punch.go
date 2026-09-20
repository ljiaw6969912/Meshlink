package p2p

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultHolePunchMaxAttempts = 3
	MaxHolePunchAttempts        = 8
	DefaultHolePunchTimeout     = 2 * time.Second
)

type HolePunchPhase string

const (
	HolePunchPhaseSyncing         HolePunchPhase = "syncing"
	HolePunchPhaseTryingDirect    HolePunchPhase = "trying_direct"
	HolePunchPhaseDirectConnected HolePunchPhase = "direct_connected"
	HolePunchPhaseFallbackRelay   HolePunchPhase = "fallback_relay"
	HolePunchPhaseFailed          HolePunchPhase = "failed"
)

type HolePunchOutcomeStatus string

const (
	HolePunchOutcomeConnected     HolePunchOutcomeStatus = "connected"
	HolePunchOutcomeFailed        HolePunchOutcomeStatus = "failed"
	HolePunchOutcomeTimeout       HolePunchOutcomeStatus = "timeout"
	HolePunchOutcomeHalfConnected HolePunchOutcomeStatus = "half_connected"
	HolePunchOutcomePortChanged   HolePunchOutcomeStatus = "port_changed"
)

type HolePunchPolicy struct {
	MaxAttempts    int           `json:"max_attempts,omitempty"`
	AttemptTimeout time.Duration `json:"-"`
}

type HolePunchCandidatePort struct {
	DeviceID string         `json:"device_id"`
	Port     int            `json:"port"`
	Protocol Protocol       `json:"protocol"`
	Scope    CandidateScope `json:"scope"`
}

type HolePunchSummary struct {
	Ready              bool   `json:"ready"`
	Mode               string `json:"mode,omitempty"`
	CandidatePortCount int    `json:"candidate_port_count"`
	MaxAttempts        int    `json:"max_attempts"`
	Reason             string `json:"reason,omitempty"`
}

type HolePunchPlan struct {
	Ready          bool                     `json:"ready"`
	Mode           string                   `json:"mode,omitempty"`
	MaxAttempts    int                      `json:"max_attempts"`
	CandidatePorts []HolePunchCandidatePort `json:"candidate_ports,omitempty"`
	FallbackReason string                   `json:"fallback_reason,omitempty"`
	Diagnostic     string                   `json:"diagnostic,omitempty"`
	CandidatePairs []CandidatePair          `json:"-"`
}

type HolePunchAttempt struct {
	Number            int                    `json:"number"`
	SourceDeviceID    string                 `json:"source_device_id"`
	TargetDeviceID    string                 `json:"target_device_id"`
	Pair              CandidatePair          `json:"pair"`
	PathType          PathType               `json:"path_type"`
	Simultaneous      bool                   `json:"simultaneous"`
	Status            AttemptStatus          `json:"status"`
	OutcomeStatus     HolePunchOutcomeStatus `json:"outcome_status,omitempty"`
	FallbackReason    string                 `json:"fallback_reason,omitempty"`
	DiagnosticMessage string                 `json:"diagnostic_message,omitempty"`
	StartedAt         time.Time              `json:"started_at,omitempty"`
	EndedAt           time.Time              `json:"ended_at,omitempty"`
}

type HolePunchOutcome struct {
	Status             HolePunchOutcomeStatus `json:"status"`
	ObservedSourcePort int                    `json:"observed_source_port,omitempty"`
	ObservedTargetPort int                    `json:"observed_target_port,omitempty"`
	DiagnosticMessage  string                 `json:"diagnostic_message,omitempty"`
}

type HolePunchResult struct {
	NegotiationID        string                   `json:"negotiation_id,omitempty"`
	AccountID            string                   `json:"account_id"`
	NetworkID            string                   `json:"network_id"`
	SourceDeviceID       string                   `json:"source_device_id"`
	TargetDeviceID       string                   `json:"target_device_id"`
	State                PathState                `json:"state"`
	Phase                HolePunchPhase           `json:"phase"`
	PathType             PathType                 `json:"path_type,omitempty"`
	SourceNATProbe       *NATProbeSummary         `json:"source_nat_probe,omitempty"`
	TargetNATProbe       *NATProbeSummary         `json:"target_nat_probe,omitempty"`
	SyncedCandidatePorts []HolePunchCandidatePort `json:"synced_candidate_ports,omitempty"`
	FallbackReason       string                   `json:"fallback_reason,omitempty"`
	DiagnosticMessage    string                   `json:"diagnostic_message,omitempty"`
	RelayFallback        RelayFallback            `json:"relay_fallback"`
	Attempts             []HolePunchAttempt       `json:"attempts,omitempty"`
}

type HolePunchNetwork interface {
	Punch(context.Context, HolePunchAttempt) HolePunchOutcome
}

type HolePuncher struct {
	Network HolePunchNetwork
	Policy  HolePunchPolicy
	Now     func() time.Time
}

func SummarizeHolePunch(negotiation Negotiation, policy HolePunchPolicy) HolePunchSummary {
	plan := BuildHolePunchPlan(negotiation, policy)
	return HolePunchSummary{
		Ready:              plan.Ready,
		Mode:               plan.Mode,
		CandidatePortCount: len(plan.CandidatePorts),
		MaxAttempts:        plan.MaxAttempts,
		Reason:             plan.FallbackReason,
	}
}

func BuildHolePunchPlan(negotiation Negotiation, policy HolePunchPolicy) HolePunchPlan {
	maxAttempts := normalizedHolePunchMaxAttempts(policy.MaxAttempts)
	plan := HolePunchPlan{
		MaxAttempts: maxAttempts,
		Mode:        "simultaneous_udp",
	}
	if !natSummaryAllowsHolePunch(negotiation.SourceNATProbe) || !natSummaryAllowsHolePunch(negotiation.TargetNATProbe) {
		plan.FallbackReason = "hole_punch_nat_not_viable"
		plan.Diagnostic = "NAT summaries do not allow coordinated UDP hole punching"
		return plan
	}
	pairs := udpHolePunchPairs(negotiation.CandidatePairs)
	if len(pairs) == 0 {
		plan.FallbackReason = "hole_punch_no_udp_candidates"
		plan.Diagnostic = "no synced UDP candidate ports are available"
		return plan
	}
	plan.Ready = true
	plan.CandidatePairs = pairs
	plan.CandidatePorts = syncedHolePunchCandidatePorts(pairs)
	plan.Diagnostic = "synced UDP candidate ports are ready for simultaneous hole punching"
	return plan
}

func (h HolePuncher) Connect(ctx context.Context, negotiation Negotiation) HolePunchResult {
	if ctx == nil {
		ctx = context.Background()
	}
	result := newHolePunchResult(negotiation)
	plan := BuildHolePunchPlan(negotiation, h.Policy)
	result.SyncedCandidatePorts = append([]HolePunchCandidatePort(nil), plan.CandidatePorts...)
	if !plan.Ready {
		return result.withHolePunchFallback(negotiation, plan.FallbackReason, plan.Diagnostic)
	}
	if h.Network == nil {
		return result.withHolePunchFallback(negotiation, "hole_punch_network_unavailable", "hole punch network is unavailable")
	}

	result.Phase = HolePunchPhaseTryingDirect
	pairIndex := 0
	pair := plan.CandidatePairs[pairIndex]
	for attemptNumber := 1; attemptNumber <= plan.MaxAttempts; attemptNumber++ {
		startedAt := h.nowTime()
		attempt := HolePunchAttempt{
			Number:         attemptNumber,
			SourceDeviceID: pair.Source.DeviceID,
			TargetDeviceID: pair.Target.DeviceID,
			Pair:           pair,
			PathType:       pair.PathType,
			Simultaneous:   true,
			Status:         AttemptStatusConnecting,
			StartedAt:      startedAt,
		}

		attemptCtx := ctx
		cancel := func() {}
		if timeout := normalizedHolePunchTimeout(h.Policy.AttemptTimeout); timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		outcome := h.Network.Punch(attemptCtx, attempt)
		cancel()
		if outcome.Status == "" {
			outcome.Status = HolePunchOutcomeFailed
		}

		attempt.EndedAt = h.nowTime()
		attempt.OutcomeStatus = outcome.Status
		attempt.DiagnosticMessage = sanitizedHolePunchDiagnostic(outcome)
		switch outcome.Status {
		case HolePunchOutcomeConnected:
			attempt.Status = AttemptStatusSucceeded
			if attempt.DiagnosticMessage == "" {
				attempt.DiagnosticMessage = "simultaneous UDP hole punch connected"
			}
			result.Attempts = append(result.Attempts, attempt)
			result.State = connectedState(pair.PathType)
			result.Phase = HolePunchPhaseDirectConnected
			result.PathType = pair.PathType
			result.FallbackReason = ""
			result.DiagnosticMessage = "simultaneous UDP hole punch connected"
			return result
		case HolePunchOutcomePortChanged:
			attempt.Status = AttemptStatusFailed
			attempt.FallbackReason = "hole_punch_port_changed"
			result.Attempts = append(result.Attempts, attempt)
			pair = resyncHolePunchPairPorts(pair, outcome)
		case HolePunchOutcomeTimeout:
			attempt.Status = AttemptStatusFailed
			attempt.FallbackReason = "hole_punch_timeout"
			result.Attempts = append(result.Attempts, attempt)
		case HolePunchOutcomeHalfConnected:
			attempt.Status = AttemptStatusFailed
			attempt.FallbackReason = "hole_punch_half_connected"
			result.Attempts = append(result.Attempts, attempt)
		default:
			attempt.Status = AttemptStatusFailed
			attempt.FallbackReason = "hole_punch_failed"
			result.Attempts = append(result.Attempts, attempt)
			if pairIndex+1 < len(plan.CandidatePairs) {
				pairIndex++
				pair = plan.CandidatePairs[pairIndex]
			}
		}
	}
	return result.withHolePunchFallback(negotiation, "hole_punch_retry_limit_reached", "hole punch retry limit reached; use Relay fallback")
}

func newHolePunchResult(negotiation Negotiation) HolePunchResult {
	return HolePunchResult{
		NegotiationID:  negotiation.ID,
		AccountID:      negotiation.AccountID,
		NetworkID:      negotiation.NetworkID,
		SourceDeviceID: negotiation.SourceDeviceID,
		TargetDeviceID: negotiation.TargetDeviceID,
		State:          PathStateConnecting,
		Phase:          HolePunchPhaseSyncing,
		SourceNATProbe: CloneNATProbeSummary(negotiation.SourceNATProbe),
		TargetNATProbe: CloneNATProbeSummary(negotiation.TargetNATProbe),
		RelayFallback:  negotiation.RelayFallback,
	}
}

func (r HolePunchResult) withHolePunchFallback(negotiation Negotiation, reason, diagnostic string) HolePunchResult {
	reason = firstNonEmpty(reason, "hole_punch_failed")
	if negotiation.RelayFallback.CreateRelaySession {
		r.State = PathStateFallbackRelay
		r.Phase = HolePunchPhaseFallbackRelay
		r.PathType = PathTypeRelay
		r.FallbackReason = reason
		r.DiagnosticMessage = diagnostic
		r.RelayFallback.CreateRelaySession = true
		r.RelayFallback.Reason = reason
		r.RelayFallback.AccountID = firstNonEmpty(r.RelayFallback.AccountID, negotiation.AccountID)
		r.RelayFallback.NetworkID = firstNonEmpty(r.RelayFallback.NetworkID, negotiation.NetworkID)
		r.RelayFallback.SourceDeviceID = firstNonEmpty(r.RelayFallback.SourceDeviceID, negotiation.SourceDeviceID)
		r.RelayFallback.TargetDeviceID = firstNonEmpty(r.RelayFallback.TargetDeviceID, negotiation.TargetDeviceID)
		return r
	}
	r.State = PathStateFailed
	r.Phase = HolePunchPhaseFailed
	r.PathType = ""
	r.FallbackReason = reason
	r.DiagnosticMessage = firstNonEmpty(diagnostic, "hole punch failed and Relay fallback is disabled")
	r.RelayFallback.CreateRelaySession = false
	return r
}

func normalizedHolePunchMaxAttempts(maxAttempts int) int {
	if maxAttempts <= 0 {
		return DefaultHolePunchMaxAttempts
	}
	if maxAttempts > MaxHolePunchAttempts {
		return MaxHolePunchAttempts
	}
	return maxAttempts
}

func normalizedHolePunchTimeout(timeout time.Duration) time.Duration {
	if timeout < 0 {
		return 0
	}
	if timeout == 0 {
		return DefaultHolePunchTimeout
	}
	return timeout
}

func natSummaryAllowsHolePunch(summary *NATProbeSummary) bool {
	if summary == nil {
		return false
	}
	normalized := NormalizeNATProbeSummary(*summary)
	return normalized.UDPAvailable && normalized.MappingStable && normalized.HolePunchRecommended && !normalized.RelayRecommended
}

func udpHolePunchPairs(pairs []CandidatePair) []CandidatePair {
	out := make([]CandidatePair, 0, len(pairs))
	for _, pair := range pairs {
		if pair.Source.Protocol != ProtocolUDP || pair.Target.Protocol != ProtocolUDP {
			continue
		}
		if _, ok := directPathType(pair.Source.Scope); !ok {
			continue
		}
		if pair.PathType == "" {
			if pathType, ok := directPathType(pair.Source.Scope); ok {
				pair.PathType = pathType
			}
		}
		out = append(out, pair)
	}
	return out
}

func syncedHolePunchCandidatePorts(pairs []CandidatePair) []HolePunchCandidatePort {
	seen := map[string]struct{}{}
	ports := make([]HolePunchCandidatePort, 0, len(pairs)*2)
	for _, pair := range pairs {
		for _, candidate := range []Candidate{pair.Source, pair.Target} {
			port := HolePunchCandidatePort{
				DeviceID: candidate.DeviceID,
				Port:     candidate.Port,
				Protocol: candidate.Protocol,
				Scope:    candidate.Scope,
			}
			key := fmt.Sprintf("%s\x00%d\x00%s\x00%s", port.DeviceID, port.Port, port.Protocol, port.Scope)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			ports = append(ports, port)
		}
	}
	return ports
}

func resyncHolePunchPairPorts(pair CandidatePair, outcome HolePunchOutcome) CandidatePair {
	if validPort(outcome.ObservedSourcePort) {
		pair.Source.Port = outcome.ObservedSourcePort
	}
	if validPort(outcome.ObservedTargetPort) {
		pair.Target.Port = outcome.ObservedTargetPort
	}
	return pair
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

func sanitizedHolePunchDiagnostic(outcome HolePunchOutcome) string {
	switch outcome.Status {
	case HolePunchOutcomeConnected:
		return "simultaneous UDP hole punch connected"
	case HolePunchOutcomePortChanged:
		return "candidate port changed; retry with resynced port"
	case HolePunchOutcomeTimeout:
		return "hole punch attempt timed out"
	case HolePunchOutcomeHalfConnected:
		return "hole punch reached only one side; retry"
	case HolePunchOutcomeFailed:
		return "hole punch attempt failed"
	default:
		if strings.TrimSpace(outcome.DiagnosticMessage) != "" {
			return "hole punch attempt failed"
		}
		return ""
	}
}

func (h HolePuncher) nowTime() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}
