package p2p

import "strings"

type ConnectionQualityInput struct {
	PathType           PathType  `json:"path_type,omitempty"`
	State              PathState `json:"state,omitempty"`
	LatencyMS          int64     `json:"latency_ms,omitempty"`
	PacketLossPermille int       `json:"packet_loss_per_mille,omitempty"`
	JitterMS           int64     `json:"jitter_ms,omitempty"`
	RelayBytesIn       int64     `json:"relay_bytes_in,omitempty"`
	RelayBytesOut      int64     `json:"relay_bytes_out,omitempty"`
	SwitchCount        int       `json:"switch_count,omitempty"`
	SwitchReasons      []string  `json:"switch_reasons,omitempty"`
}

type ConnectionQuality struct {
	PathType           PathType  `json:"path_type,omitempty"`
	State              PathState `json:"state,omitempty"`
	Score              int       `json:"score"`
	LatencyMS          int64     `json:"latency_ms,omitempty"`
	PacketLossPermille int       `json:"packet_loss_per_mille,omitempty"`
	JitterMS           int64     `json:"jitter_ms,omitempty"`
	RelayBytesIn       int64     `json:"relay_bytes_in,omitempty"`
	RelayBytesOut      int64     `json:"relay_bytes_out,omitempty"`
	SwitchCount        int       `json:"switch_count,omitempty"`
	SwitchReasons      []string  `json:"switch_reasons,omitempty"`
}

type PathQualityBucket struct {
	Samples             int   `json:"samples"`
	AverageScore        int   `json:"average_score"`
	AverageLatencyMS    int64 `json:"average_latency_ms,omitempty"`
	AverageLossPermille int   `json:"average_loss_per_mille,omitempty"`
	AverageJitterMS     int64 `json:"average_jitter_ms,omitempty"`
	RelayBytesIn        int64 `json:"relay_bytes_in,omitempty"`
	RelayBytesOut       int64 `json:"relay_bytes_out,omitempty"`
}

type ConnectionQualitySummary struct {
	Latest        ConnectionQuality `json:"latest"`
	Direct        PathQualityBucket `json:"direct"`
	Relay         PathQualityBucket `json:"relay"`
	SwitchCount   int               `json:"switch_count,omitempty"`
	SwitchReasons []string          `json:"switch_reasons,omitempty"`
}

func ScoreConnectionQuality(input ConnectionQualityInput) ConnectionQuality {
	quality := ConnectionQuality{
		PathType:           input.PathType,
		State:              input.State,
		LatencyMS:          clampInt64(input.LatencyMS),
		PacketLossPermille: clampInt(input.PacketLossPermille),
		JitterMS:           clampInt64(input.JitterMS),
		RelayBytesIn:       clampInt64(input.RelayBytesIn),
		RelayBytesOut:      clampInt64(input.RelayBytesOut),
		SwitchCount:        clampInt(input.SwitchCount),
		SwitchReasons:      sanitizeQualityReasons(input.SwitchReasons),
	}
	if quality.SwitchCount < len(quality.SwitchReasons) {
		quality.SwitchCount = len(quality.SwitchReasons)
	}
	if quality.PathType != PathTypeRelay {
		quality.RelayBytesIn = 0
		quality.RelayBytesOut = 0
	}
	quality.Score = calculateQualityScore(quality)
	return quality
}

func SummarizeConnectionQuality(inputs []ConnectionQualityInput) ConnectionQualitySummary {
	var summary ConnectionQualitySummary
	var direct qualityAccumulator
	var relay qualityAccumulator
	for _, input := range inputs {
		quality := ScoreConnectionQuality(input)
		summary.Latest = quality
		summary.SwitchCount += quality.SwitchCount
		summary.SwitchReasons = append(summary.SwitchReasons, quality.SwitchReasons...)
		switch quality.PathType {
		case PathTypeRelay:
			relay.add(quality)
		case PathTypeLANDirect, PathTypePublicDirect:
			direct.add(quality)
		}
	}
	summary.Direct = direct.bucket()
	summary.Relay = relay.bucket()
	summary.SwitchReasons = uniqueStrings(summary.SwitchReasons)
	return summary
}

func calculateQualityScore(quality ConnectionQuality) int {
	if quality.State == PathStateFailed || quality.State == PathStateOffline || quality.State == PathStateRDPUnreachable {
		return 0
	}
	score := 100
	if quality.PathType == PathTypeRelay {
		score -= 5
	}
	switch quality.State {
	case PathStateConnecting, PathStateTryingLANDirect, PathStateTryingPublicDirect:
		score -= 15
	}
	if quality.LatencyMS > 25 {
		score -= int((quality.LatencyMS - 25 + 4) / 5)
	}
	score -= quality.PacketLossPermille / 5
	if quality.JitterMS > 5 {
		score -= int((quality.JitterMS - 5 + 2) / 3)
	}
	score -= quality.SwitchCount * 3
	return clampScore(score)
}

type qualityAccumulator struct {
	samples       int
	score         int
	latencyMS     int64
	lossPermille  int
	jitterMS      int64
	relayBytesIn  int64
	relayBytesOut int64
}

func (a *qualityAccumulator) add(quality ConnectionQuality) {
	a.samples++
	a.score += quality.Score
	a.latencyMS += quality.LatencyMS
	a.lossPermille += quality.PacketLossPermille
	a.jitterMS += quality.JitterMS
	a.relayBytesIn += quality.RelayBytesIn
	a.relayBytesOut += quality.RelayBytesOut
}

func (a qualityAccumulator) bucket() PathQualityBucket {
	if a.samples == 0 {
		return PathQualityBucket{}
	}
	return PathQualityBucket{
		Samples:             a.samples,
		AverageScore:        a.score / a.samples,
		AverageLatencyMS:    a.latencyMS / int64(a.samples),
		AverageLossPermille: a.lossPermille / a.samples,
		AverageJitterMS:     a.jitterMS / int64(a.samples),
		RelayBytesIn:        a.relayBytesIn,
		RelayBytesOut:       a.relayBytesOut,
	}
}

func sanitizeQualityReasons(reasons []string) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		if containsSensitiveReasonTerm(reason) {
			reason = "redacted"
		}
		reason = stripControlCharacters(reason)
		if len([]rune(reason)) > 160 {
			reason = string([]rune(reason)[:160])
		}
		out = append(out, reason)
	}
	return uniqueStrings(out)
}

func containsSensitiveReasonTerm(reason string) bool {
	lower := strings.ToLower(reason)
	for _, term := range []string{"token", "password", "secret", "private", "key_pem", "csr", "clipboard", "rdp_content", "file_content"} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func stripControlCharacters(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func clampScore(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func clampInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func clampInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
