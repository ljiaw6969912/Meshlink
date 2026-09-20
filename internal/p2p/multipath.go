package p2p

import "time"

const (
	defaultMultipathMinScoreDelta = 8
	defaultDirectPoorScore        = 55
)

type MultipathCandidate struct {
	PathType PathType               `json:"path_type"`
	Quality  ConnectionQualityInput `json:"quality"`
}

type MultipathPolicy struct {
	MinScoreDelta    int           `json:"min_score_delta,omitempty"`
	DirectPoorScore  int           `json:"direct_poor_score,omitempty"`
	SwitchCooldown   time.Duration `json:"switch_cooldown,omitempty"`
	DisconnectedOnly bool          `json:"disconnected_only,omitempty"`
}

type MultipathSelectionRequest struct {
	CurrentPathType PathType             `json:"current_path_type,omitempty"`
	Candidates      []MultipathCandidate `json:"candidates,omitempty"`
	Policy          MultipathPolicy      `json:"policy,omitempty"`
	Now             time.Time            `json:"now,omitempty"`
	LastSwitchAt    time.Time            `json:"last_switch_at,omitempty"`
}

type MultipathDecision struct {
	PreferredPathType PathType          `json:"preferred_path_type,omitempty"`
	CurrentPathType   PathType          `json:"current_path_type,omitempty"`
	FromPathType      PathType          `json:"from_path_type,omitempty"`
	ToPathType        PathType          `json:"to_path_type,omitempty"`
	DirectQuality     ConnectionQuality `json:"direct_quality,omitempty"`
	RelayQuality      ConnectionQuality `json:"relay_quality,omitempty"`
	ScoreDelta        int               `json:"score_delta,omitempty"`
	SwitchReason      string            `json:"switch_reason,omitempty"`
	SwitchReasons     []string          `json:"switch_reasons,omitempty"`
	AutoSwitched      bool              `json:"auto_switched"`
	SwitchSuppressed  bool              `json:"switch_suppressed,omitempty"`
	SuppressionReason string            `json:"suppression_reason,omitempty"`
}

func SelectMultipath(req MultipathSelectionRequest) MultipathDecision {
	policy := normalizeMultipathPolicy(req.Policy)
	direct, hasDirect := bestDirectCandidate(req.Candidates)
	relay, hasRelay := bestRelayCandidate(req.Candidates)
	decision := MultipathDecision{
		CurrentPathType: req.CurrentPathType,
	}
	if hasDirect {
		decision.DirectQuality = direct.quality
	}
	if hasRelay {
		decision.RelayQuality = relay.quality
	}

	switch {
	case !hasDirect && !hasRelay:
		decision.PreferredPathType = req.CurrentPathType
		setMultipathReason(&decision, "no_path_candidates")
		return decision
	case hasDirect && !hasRelay:
		return directOnlyDecision(decision, req.CurrentPathType, direct)
	case !hasDirect && hasRelay:
		return relayOnlyDecision(decision, req.CurrentPathType, relay)
	}

	if isDirectPath(req.CurrentPathType) {
		return selectFromActiveDirect(decision, req, policy, direct, relay)
	}
	if req.CurrentPathType == PathTypeRelay {
		return selectFromActiveRelay(decision, req, policy, direct, relay)
	}
	return selectForNewSession(decision, direct, relay)
}

type scoredMultipathCandidate struct {
	pathType PathType
	quality  ConnectionQuality
}

func bestDirectCandidate(candidates []MultipathCandidate) (scoredMultipathCandidate, bool) {
	var best scoredMultipathCandidate
	var ok bool
	for _, candidate := range candidates {
		scored, candidateOK := scoreMultipathCandidate(candidate)
		if !candidateOK || !isDirectPath(scored.pathType) {
			continue
		}
		if !ok || betterMultipathCandidate(scored, best) {
			best = scored
			ok = true
		}
	}
	return best, ok
}

func bestRelayCandidate(candidates []MultipathCandidate) (scoredMultipathCandidate, bool) {
	var best scoredMultipathCandidate
	var ok bool
	for _, candidate := range candidates {
		scored, candidateOK := scoreMultipathCandidate(candidate)
		if !candidateOK || scored.pathType != PathTypeRelay {
			continue
		}
		if !ok || betterMultipathCandidate(scored, best) {
			best = scored
			ok = true
		}
	}
	return best, ok
}

func scoreMultipathCandidate(candidate MultipathCandidate) (scoredMultipathCandidate, bool) {
	pathType := candidate.PathType
	if pathType == "" {
		pathType = candidate.Quality.PathType
	}
	if pathType == "" {
		return scoredMultipathCandidate{}, false
	}
	input := candidate.Quality
	input.PathType = pathType
	quality := ScoreConnectionQuality(input)
	return scoredMultipathCandidate{pathType: pathType, quality: quality}, true
}

func betterMultipathCandidate(left, right scoredMultipathCandidate) bool {
	if left.quality.Score != right.quality.Score {
		return left.quality.Score > right.quality.Score
	}
	return pathRank(left.pathType) < pathRank(right.pathType)
}

func directOnlyDecision(decision MultipathDecision, current PathType, direct scoredMultipathCandidate) MultipathDecision {
	decision.PreferredPathType = direct.pathType
	if current == PathTypeRelay {
		decision.FromPathType = PathTypeRelay
		decision.ToPathType = direct.pathType
		decision.ScoreDelta = direct.quality.Score
		setMultipathReason(&decision, "direct_recovered_better_for_new_session")
		return decision
	}
	setMultipathReason(&decision, "direct_available")
	return decision
}

func relayOnlyDecision(decision MultipathDecision, current PathType, relay scoredMultipathCandidate) MultipathDecision {
	decision.PreferredPathType = relay.pathType
	if isDirectPath(current) {
		decision.FromPathType = current
		decision.ToPathType = relay.pathType
		decision.ScoreDelta = relay.quality.Score
		decision.AutoSwitched = true
		setMultipathReason(&decision, "direct_disconnected")
		return decision
	}
	setMultipathReason(&decision, "relay_available")
	return decision
}

func selectFromActiveDirect(decision MultipathDecision, req MultipathSelectionRequest, policy MultipathPolicy, direct, relay scoredMultipathCandidate) MultipathDecision {
	relayDelta := relay.quality.Score - direct.quality.Score
	if directDisconnected(direct.quality) {
		decision.PreferredPathType = relay.pathType
		decision.FromPathType = req.CurrentPathType
		decision.ToPathType = relay.pathType
		decision.ScoreDelta = relayDelta
		decision.AutoSwitched = true
		setMultipathReason(&decision, "direct_disconnected")
		return decision
	}
	if !policy.DisconnectedOnly && direct.quality.Score <= policy.DirectPoorScore && relayDelta >= policy.MinScoreDelta {
		decision.PreferredPathType = relay.pathType
		decision.FromPathType = req.CurrentPathType
		decision.ToPathType = relay.pathType
		decision.ScoreDelta = relayDelta
		decision.AutoSwitched = true
		setMultipathReason(&decision, "direct_quality_degraded")
		return decision
	}
	decision.PreferredPathType = direct.pathType
	if direct.quality.Score >= relay.quality.Score {
		decision.ScoreDelta = direct.quality.Score - relay.quality.Score
		setMultipathReason(&decision, "direct_quality_better")
		return decision
	}
	decision.ScoreDelta = relayDelta
	setMultipathReason(&decision, "current_path_retained")
	return decision
}

func selectFromActiveRelay(decision MultipathDecision, req MultipathSelectionRequest, policy MultipathPolicy, direct, relay scoredMultipathCandidate) MultipathDecision {
	directDelta := direct.quality.Score - relay.quality.Score
	if directDelta >= policy.MinScoreDelta && !directDisconnected(direct.quality) {
		if cooldownActive(req.Now, req.LastSwitchAt, policy.SwitchCooldown) {
			decision.PreferredPathType = PathTypeRelay
			decision.ScoreDelta = directDelta
			decision.SwitchSuppressed = true
			decision.SuppressionReason = "switch_cooldown_active"
			setMultipathReason(&decision, "current_path_retained")
			return decision
		}
		decision.PreferredPathType = direct.pathType
		decision.FromPathType = PathTypeRelay
		decision.ToPathType = direct.pathType
		decision.ScoreDelta = directDelta
		setMultipathReason(&decision, "direct_recovered_better_for_new_session")
		return decision
	}
	decision.PreferredPathType = PathTypeRelay
	if relay.quality.Score >= direct.quality.Score {
		decision.ScoreDelta = relay.quality.Score - direct.quality.Score
		setMultipathReason(&decision, "relay_quality_better")
		return decision
	}
	decision.ScoreDelta = directDelta
	setMultipathReason(&decision, "current_path_retained")
	return decision
}

func selectForNewSession(decision MultipathDecision, direct, relay scoredMultipathCandidate) MultipathDecision {
	if directDisconnected(direct.quality) || relay.quality.Score > direct.quality.Score {
		decision.PreferredPathType = relay.pathType
		decision.ScoreDelta = relay.quality.Score - direct.quality.Score
		if directDisconnected(direct.quality) {
			setMultipathReason(&decision, "direct_disconnected")
		} else {
			setMultipathReason(&decision, "relay_quality_better")
		}
		return decision
	}
	decision.PreferredPathType = direct.pathType
	decision.ScoreDelta = direct.quality.Score - relay.quality.Score
	setMultipathReason(&decision, "direct_quality_better")
	return decision
}

func normalizeMultipathPolicy(policy MultipathPolicy) MultipathPolicy {
	if policy.MinScoreDelta <= 0 {
		policy.MinScoreDelta = defaultMultipathMinScoreDelta
	}
	if policy.DirectPoorScore <= 0 {
		policy.DirectPoorScore = defaultDirectPoorScore
	}
	return policy
}

func directDisconnected(quality ConnectionQuality) bool {
	return quality.State == PathStateFailed || quality.State == PathStateClosed || quality.State == PathStateOffline || quality.State == PathStateRDPUnreachable
}

func cooldownActive(now, lastSwitchAt time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 || lastSwitchAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	lastSwitchAt = lastSwitchAt.UTC()
	return !now.Before(lastSwitchAt) && now.Sub(lastSwitchAt) < cooldown
}

func setMultipathReason(decision *MultipathDecision, reason string) {
	decision.SwitchReason = reason
	decision.SwitchReasons = sanitizeQualityReasons([]string{reason})
}

func isDirectPath(pathType PathType) bool {
	return pathType == PathTypeLANDirect || pathType == PathTypePublicDirect
}
