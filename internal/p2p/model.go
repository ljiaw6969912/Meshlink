package p2p

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCandidateTTLSeconds int64 = 120
	MaxCandidateTTLSeconds     int64 = 600
)

type CandidateScope string

const (
	CandidateScopeLAN    CandidateScope = "lan"
	CandidateScopePublic CandidateScope = "public"
	CandidateScopeRelay  CandidateScope = "relay"
)

type Protocol string

const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

type PathType string

const (
	PathTypeLANDirect    PathType = "lan_direct"
	PathTypePublicDirect PathType = "public_direct"
	PathTypeRelay        PathType = "relay"
)

type PathState string

const (
	PathStateIdle                  PathState = "idle"
	PathStateRequesting            PathState = "requesting"
	PathStatePreparing             PathState = "preparing"
	PathStatePunching              PathState = "punching"
	PathStateAuthenticating        PathState = "authenticating"
	PathStateLANDirect             PathState = "lan_direct"
	PathStatePublicDirect          PathState = "public_direct"
	PathStateReconnecting          PathState = "reconnecting"
	PathStateWaitingCoordinator    PathState = "waiting_coordinator"
	PathStateConnecting            PathState = "connecting"
	PathStateTryingLANDirect       PathState = "trying_lan_direct"
	PathStateTryingPublicDirect    PathState = "trying_public_direct"
	PathStateLANDirectConnected    PathState = "lan_direct_connected"
	PathStatePublicDirectConnected PathState = "public_direct_connected"
	PathStateFallbackRelay         PathState = "fallback_relay"
	PathStateFailed                PathState = "failed"
	PathStateClosed                PathState = "closed"
	PathStateOffline               PathState = "offline"
	PathStateRDPUnreachable        PathState = "rdp-unreachable"
)

type AttemptStatus string

const (
	AttemptStatusConnecting AttemptStatus = "connecting"
	AttemptStatusSucceeded  AttemptStatus = "succeeded"
	AttemptStatusFailed     AttemptStatus = "failed"
	AttemptStatusSkipped    AttemptStatus = "skipped"
)

type Candidate struct {
	DeviceID     string         `json:"device_id"`
	NetworkID    string         `json:"network_id"`
	Address      string         `json:"address"`
	Port         int            `json:"port"`
	Protocol     Protocol       `json:"protocol"`
	Scope        CandidateScope `json:"scope"`
	Priority     int            `json:"priority"`
	Source       string         `json:"source,omitempty"`
	ObservedFrom string         `json:"observed_from,omitempty"`
	TTLSeconds   int64          `json:"ttl_seconds,omitempty"`
	SeenAt       time.Time      `json:"seen_at"`
	ExpiresAt    time.Time      `json:"expires_at"`
}

func NormalizeCandidates(candidates []Candidate, now time.Time) ([]Candidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	byKey := make(map[string]Candidate, len(candidates))
	for i, candidate := range candidates {
		normalized, err := normalizeCandidate(candidate, now)
		if err != nil {
			return nil, fmt.Errorf("candidate %d: %w", i, err)
		}
		key := candidateKey(normalized)
		existing, ok := byKey[key]
		if !ok || betterCandidate(normalized, existing) {
			byKey[key] = normalized
		}
	}
	out := make([]Candidate, 0, len(byKey))
	for _, candidate := range byKey {
		out = append(out, candidate)
	}
	sortCandidates(out)
	return out, nil
}

func (c Candidate) IsExpired(now time.Time) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !now.UTC().Before(c.ExpiresAt)
}

func normalizeCandidate(candidate Candidate, now time.Time) (Candidate, error) {
	candidate.DeviceID = strings.TrimSpace(candidate.DeviceID)
	candidate.NetworkID = strings.TrimSpace(candidate.NetworkID)
	candidate.Address = strings.TrimSpace(candidate.Address)
	candidate.Source = strings.TrimSpace(candidate.Source)
	candidate.ObservedFrom = strings.TrimSpace(candidate.ObservedFrom)
	if candidate.DeviceID == "" {
		return Candidate{}, fmt.Errorf("device_id is required")
	}
	if candidate.NetworkID == "" {
		return Candidate{}, fmt.Errorf("network_id is required")
	}
	if candidate.Address == "" {
		return Candidate{}, fmt.Errorf("address is required")
	}
	addr, err := netip.ParseAddr(candidate.Address)
	if err != nil {
		return Candidate{}, fmt.Errorf("address must be an IPv4 address: %w", err)
	}
	if !addr.Is4() {
		return Candidate{}, fmt.Errorf("address must be IPv4 for Task 6A")
	}
	candidate.Address = addr.String()
	if candidate.Port <= 0 || candidate.Port > 65535 {
		return Candidate{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if candidate.Protocol == "" {
		candidate.Protocol = ProtocolTCP
	}
	switch candidate.Protocol {
	case ProtocolTCP, ProtocolUDP:
	default:
		return Candidate{}, fmt.Errorf("protocol %q is not supported", candidate.Protocol)
	}
	switch candidate.Scope {
	case CandidateScopeLAN, CandidateScopePublic, CandidateScopeRelay:
	default:
		return Candidate{}, fmt.Errorf("scope %q is not supported", candidate.Scope)
	}
	if candidate.TTLSeconds <= 0 {
		candidate.TTLSeconds = DefaultCandidateTTLSeconds
	}
	if candidate.TTLSeconds > MaxCandidateTTLSeconds {
		candidate.TTLSeconds = MaxCandidateTTLSeconds
	}
	if candidate.SeenAt.IsZero() {
		candidate.SeenAt = now
	} else {
		candidate.SeenAt = candidate.SeenAt.UTC()
	}
	if candidate.ExpiresAt.IsZero() || !candidate.ExpiresAt.After(candidate.SeenAt) {
		candidate.ExpiresAt = candidate.SeenAt.Add(time.Duration(candidate.TTLSeconds) * time.Second)
	} else {
		candidate.ExpiresAt = candidate.ExpiresAt.UTC()
	}
	return candidate, nil
}

func candidateKey(candidate Candidate) string {
	return strings.Join([]string{
		candidate.DeviceID,
		candidate.NetworkID,
		string(candidate.Scope),
		string(candidate.Protocol),
		candidate.Address,
		strconv.Itoa(candidate.Port),
	}, "\x00")
}

func betterCandidate(candidate, existing Candidate) bool {
	if candidate.Priority != existing.Priority {
		return candidate.Priority > existing.Priority
	}
	if !candidate.SeenAt.Equal(existing.SeenAt) {
		return candidate.SeenAt.After(existing.SeenAt)
	}
	return candidate.Source > existing.Source
}

func sortCandidates(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if candidateScopeRank(left.Scope) != candidateScopeRank(right.Scope) {
			return candidateScopeRank(left.Scope) < candidateScopeRank(right.Scope)
		}
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		if left.Address != right.Address {
			return left.Address < right.Address
		}
		if left.Port != right.Port {
			return left.Port < right.Port
		}
		return left.DeviceID < right.DeviceID
	})
}

func candidateScopeRank(scope CandidateScope) int {
	switch scope {
	case CandidateScopeLAN:
		return 0
	case CandidateScopePublic:
		return 1
	case CandidateScopeRelay:
		return 2
	default:
		return 3
	}
}

type CandidatePair struct {
	Source   Candidate `json:"source"`
	Target   Candidate `json:"target"`
	PathType PathType  `json:"path_type"`
	Priority int       `json:"priority"`
}

func BuildCandidatePairs(sourceCandidates, targetCandidates []Candidate) []CandidatePair {
	var pairs []CandidatePair
	for _, source := range sourceCandidates {
		for _, target := range targetCandidates {
			pathType, ok := directPathType(source.Scope)
			if !ok {
				continue
			}
			if target.Scope != source.Scope || target.Protocol != source.Protocol {
				continue
			}
			if source.NetworkID != "" && target.NetworkID != "" && source.NetworkID != target.NetworkID {
				continue
			}
			if source.DeviceID != "" && target.DeviceID != "" && source.DeviceID == target.DeviceID {
				continue
			}
			pairs = append(pairs, CandidatePair{
				Source:   source,
				Target:   target,
				PathType: pathType,
				Priority: source.Priority + target.Priority,
			})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		left := pairs[i]
		right := pairs[j]
		if pathRank(left.PathType) != pathRank(right.PathType) {
			return pathRank(left.PathType) < pathRank(right.PathType)
		}
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		if left.Target.Address != right.Target.Address {
			return left.Target.Address < right.Target.Address
		}
		return left.Target.Port < right.Target.Port
	})
	return pairs
}

type NegotiationRequest struct {
	AccountID          string           `json:"account_id"`
	NetworkID          string           `json:"network_id"`
	SourceDeviceID     string           `json:"source_device_id"`
	TargetDeviceID     string           `json:"target_device_id"`
	SourceCandidates   []Candidate      `json:"source_candidates,omitempty"`
	TargetCandidates   []Candidate      `json:"target_candidates,omitempty"`
	SourceNATProbe     *NATProbeSummary `json:"source_nat_probe,omitempty"`
	TargetNATProbe     *NATProbeSummary `json:"target_nat_probe,omitempty"`
	TransportPolicy    TransportPolicy  `json:"transport_policy,omitempty"`
	AllowRelayFallback bool             `json:"allow_relay_fallback,omitempty"`
}

type RelayFallback struct {
	CreateRelaySession bool   `json:"create_relay_session"`
	Reason             string `json:"reason,omitempty"`
	AccountID          string `json:"account_id,omitempty"`
	NetworkID          string `json:"network_id,omitempty"`
	SourceDeviceID     string `json:"source_device_id,omitempty"`
	TargetDeviceID     string `json:"target_device_id,omitempty"`
}

type Negotiation struct {
	ID                 string              `json:"id,omitempty"`
	AccountID          string              `json:"account_id"`
	NetworkID          string              `json:"network_id"`
	SourceDeviceID     string              `json:"source_device_id"`
	TargetDeviceID     string              `json:"target_device_id"`
	CandidatePairs     []CandidatePair     `json:"candidate_pairs,omitempty"`
	SourceNATProbe     *NATProbeSummary    `json:"source_nat_probe,omitempty"`
	TargetNATProbe     *NATProbeSummary    `json:"target_nat_probe,omitempty"`
	TransportSelection *TransportSelection `json:"transport_selection,omitempty"`
	PreferredPathType  PathType            `json:"preferred_path_type,omitempty"`
	State              PathState           `json:"state"`
	FallbackReason     string              `json:"fallback_reason,omitempty"`
	DiagnosticMessage  string              `json:"diagnostic_message,omitempty"`
	RelayFallback      RelayFallback       `json:"relay_fallback"`
	Attempts           []ConnectionAttempt `json:"attempts,omitempty"`
}

type ConnectionAttempt struct {
	SourceDeviceID    string        `json:"source_device_id"`
	TargetDeviceID    string        `json:"target_device_id"`
	Pair              CandidatePair `json:"pair"`
	PathType          PathType      `json:"path_type"`
	Status            AttemptStatus `json:"status"`
	FallbackReason    string        `json:"fallback_reason,omitempty"`
	DiagnosticMessage string        `json:"diagnostic_message,omitempty"`
	StartedAt         time.Time     `json:"started_at,omitempty"`
	EndedAt           time.Time     `json:"ended_at,omitempty"`
}

type ConnectionResult struct {
	NegotiationID      string              `json:"negotiation_id,omitempty"`
	AccountID          string              `json:"account_id"`
	NetworkID          string              `json:"network_id"`
	SourceDeviceID     string              `json:"source_device_id"`
	TargetDeviceID     string              `json:"target_device_id"`
	PathType           PathType            `json:"path_type,omitempty"`
	SourceNATProbe     *NATProbeSummary    `json:"source_nat_probe,omitempty"`
	TargetNATProbe     *NATProbeSummary    `json:"target_nat_probe,omitempty"`
	TransportSelection *TransportSelection `json:"transport_selection,omitempty"`
	State              PathState           `json:"state"`
	FallbackReason     string              `json:"fallback_reason,omitempty"`
	DiagnosticMessage  string              `json:"diagnostic_message,omitempty"`
	RelayFallback      RelayFallback       `json:"relay_fallback"`
	Attempts           []ConnectionAttempt `json:"attempts,omitempty"`
}

func BuildNegotiation(req NegotiationRequest) Negotiation {
	pairs := BuildCandidatePairs(req.SourceCandidates, req.TargetCandidates)
	relayFallback := RelayFallback{
		CreateRelaySession: req.AllowRelayFallback,
		Reason:             "direct_attempt_failed",
		AccountID:          strings.TrimSpace(req.AccountID),
		NetworkID:          strings.TrimSpace(req.NetworkID),
		SourceDeviceID:     strings.TrimSpace(req.SourceDeviceID),
		TargetDeviceID:     strings.TrimSpace(req.TargetDeviceID),
	}
	negotiation := Negotiation{
		AccountID:      relayFallback.AccountID,
		NetworkID:      relayFallback.NetworkID,
		SourceDeviceID: relayFallback.SourceDeviceID,
		TargetDeviceID: relayFallback.TargetDeviceID,
		CandidatePairs: pairs,
		SourceNATProbe: CloneNATProbeSummary(req.SourceNATProbe),
		TargetNATProbe: CloneNATProbeSummary(req.TargetNATProbe),
		State:          PathStateConnecting,
		RelayFallback:  relayFallback,
	}
	if reason := natRelayFallbackReason(negotiation.SourceNATProbe, negotiation.TargetNATProbe); reason != "" {
		negotiation.FallbackReason = reason
		negotiation.TransportSelection = transportSelectionPtr(SelectTransport(TransportSelectionRequest{
			Pairs:               pairs,
			SourceNATProbe:      negotiation.SourceNATProbe,
			TargetNATProbe:      negotiation.TargetNATProbe,
			Policy:              req.TransportPolicy,
			AllowRelayFallback:  req.AllowRelayFallback,
			RelayRequiredReason: reason,
		}))
		if req.AllowRelayFallback {
			negotiation.State = PathStateFallbackRelay
			negotiation.PreferredPathType = PathTypeRelay
			negotiation.RelayFallback.Reason = reason
			negotiation.DiagnosticMessage = "NAT probe recommends Relay fallback"
			return negotiation
		}
		negotiation.State = PathStateFailed
		negotiation.DiagnosticMessage = "NAT probe recommends Relay fallback but Relay fallback is disabled"
		return negotiation
	}
	if len(pairs) > 0 {
		negotiation.TransportSelection = transportSelectionPtr(SelectTransport(TransportSelectionRequest{
			Pairs:              pairs,
			SourceNATProbe:     negotiation.SourceNATProbe,
			TargetNATProbe:     negotiation.TargetNATProbe,
			Policy:             req.TransportPolicy,
			AllowRelayFallback: req.AllowRelayFallback,
		}))
		negotiation.PreferredPathType = pairs[0].PathType
		negotiation.DiagnosticMessage = "direct candidate pairs are available"
		return negotiation
	}
	negotiation.FallbackReason = "no_direct_candidates"
	negotiation.TransportSelection = transportSelectionPtr(SelectTransport(TransportSelectionRequest{
		Pairs:              pairs,
		SourceNATProbe:     negotiation.SourceNATProbe,
		TargetNATProbe:     negotiation.TargetNATProbe,
		Policy:             req.TransportPolicy,
		AllowRelayFallback: req.AllowRelayFallback,
		NoDirectCandidates: true,
	}))
	if req.AllowRelayFallback {
		negotiation.State = PathStateFallbackRelay
		negotiation.PreferredPathType = PathTypeRelay
		negotiation.RelayFallback.Reason = negotiation.FallbackReason
		negotiation.DiagnosticMessage = "no direct candidate pairs are available; create a Relay session fallback"
		return negotiation
	}
	negotiation.State = PathStateFailed
	negotiation.DiagnosticMessage = "no direct candidate pairs are available"
	return negotiation
}

type DirectDialer interface {
	Dial(context.Context, CandidatePair) error
}

type Connector struct {
	Dialer DirectDialer
	Now    func() time.Time
}

func (c Connector) Connect(ctx context.Context, negotiation Negotiation) ConnectionResult {
	result := ConnectionResult{
		NegotiationID:      negotiation.ID,
		AccountID:          negotiation.AccountID,
		NetworkID:          negotiation.NetworkID,
		SourceDeviceID:     negotiation.SourceDeviceID,
		TargetDeviceID:     negotiation.TargetDeviceID,
		State:              negotiation.State,
		SourceNATProbe:     CloneNATProbeSummary(negotiation.SourceNATProbe),
		TargetNATProbe:     CloneNATProbeSummary(negotiation.TargetNATProbe),
		TransportSelection: cloneTransportSelection(negotiation.TransportSelection),
		RelayFallback:      negotiation.RelayFallback,
		FallbackReason:     negotiation.FallbackReason,
		DiagnosticMessage:  negotiation.DiagnosticMessage,
		Attempts:           append([]ConnectionAttempt(nil), negotiation.Attempts...),
	}
	if len(negotiation.CandidatePairs) == 0 {
		if negotiation.RelayFallback.CreateRelaySession {
			return result.withRelayFallback("no_direct_candidates", "no direct candidate pairs are available; create a Relay session fallback")
		}
		if result.State == "" || result.State == PathStateConnecting {
			result.State = PathStateFailed
		}
		if result.DiagnosticMessage == "" {
			result.DiagnosticMessage = "no direct candidate pairs are available"
		}
		return result
	}
	if c.Dialer == nil {
		reason := "direct_dialer_unavailable"
		if negotiation.RelayFallback.CreateRelaySession {
			return result.withRelayFallback(reason, "direct dialer is unavailable; create a Relay session fallback")
		}
		result.State = PathStateFailed
		result.PathType = ""
		result.FallbackReason = reason
		result.DiagnosticMessage = "direct dialer is unavailable and Relay fallback is disabled"
		return result
	}
	now := c.nowTime
	var lastErr error
	for _, pair := range negotiation.CandidatePairs {
		startedAt := now()
		attempt := ConnectionAttempt{
			SourceDeviceID: pair.Source.DeviceID,
			TargetDeviceID: pair.Target.DeviceID,
			Pair:           pair,
			PathType:       pair.PathType,
			Status:         AttemptStatusConnecting,
			StartedAt:      startedAt,
		}
		result.State = tryingState(pair.PathType)
		err := c.Dialer.Dial(ctx, pair)
		endedAt := now()
		attempt.EndedAt = endedAt
		if err == nil {
			attempt.Status = AttemptStatusSucceeded
			attempt.DiagnosticMessage = "direct path connected"
			result.Attempts = append(result.Attempts, attempt)
			result.State = connectedState(pair.PathType)
			result.PathType = pair.PathType
			result.FallbackReason = ""
			result.DiagnosticMessage = "direct path connected"
			return result
		}
		lastErr = err
		attempt.Status = AttemptStatusFailed
		attempt.FallbackReason = err.Error()
		attempt.DiagnosticMessage = "direct path failed"
		result.Attempts = append(result.Attempts, attempt)
	}
	reason := "direct_candidates_failed"
	if lastErr != nil {
		reason = fmt.Sprintf("direct candidates failed: %v", lastErr)
	}
	if negotiation.RelayFallback.CreateRelaySession {
		return result.withRelayFallback(reason, "direct candidates failed; create a Relay session fallback")
	}
	result.State = PathStateFailed
	result.PathType = ""
	result.FallbackReason = reason
	result.DiagnosticMessage = "direct candidates failed and Relay fallback is disabled"
	return result
}

func transportSelectionPtr(selection TransportSelection) *TransportSelection {
	return cloneTransportSelection(&selection)
}

func cloneTransportSelection(selection *TransportSelection) *TransportSelection {
	if selection == nil {
		return nil
	}
	cloned := *selection
	cloned.Candidates = append([]TransportDecision(nil), selection.Candidates...)
	cloned.Excluded = append([]TransportDecision(nil), selection.Excluded...)
	return &cloned
}

func (c Connector) nowTime() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (r ConnectionResult) withRelayFallback(reason, diagnostic string) ConnectionResult {
	r.State = PathStateFallbackRelay
	r.PathType = PathTypeRelay
	r.FallbackReason = reason
	r.DiagnosticMessage = diagnostic
	r.RelayFallback.CreateRelaySession = true
	if r.RelayFallback.Reason == "" || r.RelayFallback.Reason == "direct_attempt_failed" {
		r.RelayFallback.Reason = reason
	}
	if r.RelayFallback.AccountID == "" {
		r.RelayFallback.AccountID = r.AccountID
	}
	if r.RelayFallback.NetworkID == "" {
		r.RelayFallback.NetworkID = r.NetworkID
	}
	if r.RelayFallback.SourceDeviceID == "" {
		r.RelayFallback.SourceDeviceID = r.SourceDeviceID
	}
	if r.RelayFallback.TargetDeviceID == "" {
		r.RelayFallback.TargetDeviceID = r.TargetDeviceID
	}
	return r
}

type FakeDialer struct {
	Err    error
	Errors map[string]error
	Dialed []CandidatePair
}

func (d *FakeDialer) Dial(ctx context.Context, pair CandidatePair) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.Dialed = append(d.Dialed, pair)
	if d.Errors != nil {
		if err, ok := d.Errors[pairKey(pair)]; ok {
			return err
		}
	}
	return d.Err
}

func pairKey(pair CandidatePair) string {
	return strings.Join([]string{
		string(pair.PathType),
		pair.Source.Address,
		strconv.Itoa(pair.Source.Port),
		pair.Target.Address,
		strconv.Itoa(pair.Target.Port),
	}, "\x00")
}

func directPathType(scope CandidateScope) (PathType, bool) {
	switch scope {
	case CandidateScopeLAN:
		return PathTypeLANDirect, true
	case CandidateScopePublic:
		return PathTypePublicDirect, true
	default:
		return "", false
	}
}

func natRelayFallbackReason(source, target *NATProbeSummary) string {
	if source != nil {
		normalized := NormalizeNATProbeSummary(*source)
		if normalized.RelayRecommended {
			return "source_nat_" + string(normalized.Type)
		}
	}
	if target != nil {
		normalized := NormalizeNATProbeSummary(*target)
		if normalized.RelayRecommended {
			return "target_nat_" + string(normalized.Type)
		}
	}
	return ""
}

func pathRank(pathType PathType) int {
	switch pathType {
	case PathTypeLANDirect:
		return 0
	case PathTypePublicDirect:
		return 1
	case PathTypeRelay:
		return 2
	default:
		return 3
	}
}

func tryingState(pathType PathType) PathState {
	switch pathType {
	case PathTypeLANDirect:
		return PathStateTryingLANDirect
	case PathTypePublicDirect:
		return PathStateTryingPublicDirect
	default:
		return PathStateConnecting
	}
}

func connectedState(pathType PathType) PathState {
	switch pathType {
	case PathTypeLANDirect:
		return PathStateLANDirectConnected
	case PathTypePublicDirect:
		return PathStatePublicDirectConnected
	default:
		return PathStateFailed
	}
}
