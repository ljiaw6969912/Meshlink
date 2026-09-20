package p2p

import (
	"strings"
	"time"
)

// SessionSnapshot is the observable state of one authorized device pair.
// Direct states are published only after both TLS identity and SessionHello
// authorization have completed.
type SessionSnapshot struct {
	PeerNodeID       string        `json:"peer_node_id"`
	SessionID        string        `json:"session_id,omitempty"`
	ErrorCode        string        `json:"error_code,omitempty"`
	Generation       uint64        `json:"generation,omitempty"`
	State            PathState     `json:"state"`
	PathType         PathType      `json:"path_type,omitempty"`
	RTT              time.Duration `json:"rtt,omitempty"`
	LastHeartbeat    time.Time     `json:"last_heartbeat,omitempty"`
	BytesSent        uint64        `json:"bytes_sent,omitempty"`
	BytesReceived    uint64        `json:"bytes_received,omitempty"`
	PendingPackets   int           `json:"pending_packets,omitempty"`
	PendingBytes     int           `json:"pending_bytes,omitempty"`
	PendingDropped   uint64        `json:"pending_dropped,omitempty"`
	SendQueueDropped uint64        `json:"send_queue_dropped,omitempty"`
	InboundDropped   uint64        `json:"inbound_dropped,omitempty"`
}

type ConnectionStatus struct {
	PathType           PathType  `json:"path_type"`
	PathState          PathState `json:"path_state"`
	QualityScore       int       `json:"quality_score"`
	LatencyMS          int64     `json:"latency_ms"`
	PacketLossPermille int       `json:"packet_loss_per_mille,omitempty"`
	JitterMS           int64     `json:"jitter_ms,omitempty"`
	RelayBytesIn       int64     `json:"relay_bytes_in"`
	RelayBytesOut      int64     `json:"relay_bytes_out"`
	SwitchCount        int       `json:"switch_count,omitempty"`
	SwitchReasons      []string  `json:"switch_reasons,omitempty"`
	SwitchFromPath     PathType  `json:"switch_from_path,omitempty"`
	SwitchToPath       PathType  `json:"switch_to_path,omitempty"`
	SwitchScoreDelta   int       `json:"switch_score_delta,omitempty"`
	AutoSwitched       bool      `json:"auto_switched,omitempty"`
	LastError          string    `json:"last_error"`
}

type ConnectionStatusDefaults struct {
	Online           bool
	DefaultPathType  PathType
	DefaultPathState PathState
}

func NormalizeConnectionStatus(status ConnectionStatus, defaults ConnectionStatusDefaults) ConnectionStatus {
	status.PathType = normalizeConnectionPathType(status.PathType)
	status.PathState = normalizeConnectionPathState(status.PathState)
	status.SwitchFromPath = normalizeConnectionPathType(status.SwitchFromPath)
	status.SwitchToPath = normalizeConnectionPathType(status.SwitchToPath)
	status.SwitchScoreDelta = clampInt(status.SwitchScoreDelta)
	status.LastError = sanitizeConnectionStatusText(status.LastError)

	if !defaults.Online && !isConnectionFailureState(status.PathState) {
		status.PathType = ""
		status.PathState = PathStateOffline
		status.QualityScore = 0
		status.LatencyMS = 0
		status.PacketLossPermille = 0
		status.JitterMS = 0
		status.RelayBytesIn = 0
		status.RelayBytesOut = 0
		status.SwitchReasons = sanitizeQualityReasons(status.SwitchReasons)
		if status.SwitchCount < len(status.SwitchReasons) {
			status.SwitchCount = len(status.SwitchReasons)
		}
		return status
	}

	if status.PathType == "" {
		status.PathType = normalizeConnectionPathType(defaults.DefaultPathType)
		if status.PathType == "" && defaults.Online {
			status.PathType = PathTypeLANDirect
		}
	}
	if status.PathState == "" || (defaults.Online && status.PathState == PathStateOffline) {
		status.PathState = normalizeConnectionPathState(defaults.DefaultPathState)
		if status.PathState == "" {
			status.PathState = connectedConnectionPathState(status.PathType)
		}
	}
	if status.PathState == PathStateOffline {
		status.PathType = ""
	}

	quality := ScoreConnectionQuality(ConnectionQualityInput{
		PathType:           status.PathType,
		State:              status.PathState,
		LatencyMS:          status.LatencyMS,
		PacketLossPermille: status.PacketLossPermille,
		JitterMS:           status.JitterMS,
		RelayBytesIn:       status.RelayBytesIn,
		RelayBytesOut:      status.RelayBytesOut,
		SwitchCount:        status.SwitchCount,
		SwitchReasons:      status.SwitchReasons,
	})
	status.PathType = quality.PathType
	status.PathState = quality.State
	status.QualityScore = quality.Score
	status.LatencyMS = quality.LatencyMS
	status.PacketLossPermille = quality.PacketLossPermille
	status.JitterMS = quality.JitterMS
	status.RelayBytesIn = quality.RelayBytesIn
	status.RelayBytesOut = quality.RelayBytesOut
	status.SwitchCount = quality.SwitchCount
	status.SwitchReasons = append([]string(nil), quality.SwitchReasons...)
	if isConnectionFailureState(status.PathState) {
		status.QualityScore = 0
	}
	return status
}

func sanitizeConnectionStatusText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if containsSensitiveReasonTerm(text) {
		return "redacted"
	}
	text = stripControlCharacters(text)
	if len([]rune(text)) > 200 {
		text = string([]rune(text)[:200])
	}
	return text
}

func normalizeConnectionPathType(pathType PathType) PathType {
	switch PathType(strings.TrimSpace(string(pathType))) {
	case PathTypeLANDirect:
		return PathTypeLANDirect
	case PathTypePublicDirect:
		return PathTypePublicDirect
	case PathTypeRelay:
		return PathTypeRelay
	default:
		return ""
	}
}

func normalizeConnectionPathState(state PathState) PathState {
	switch PathState(strings.TrimSpace(string(state))) {
	case PathStateIdle:
		return PathStateIdle
	case PathStateRequesting:
		return PathStateRequesting
	case PathStatePreparing:
		return PathStatePreparing
	case PathStatePunching:
		return PathStatePunching
	case PathStateAuthenticating:
		return PathStateAuthenticating
	case PathStateLANDirect:
		return PathStateLANDirect
	case PathStatePublicDirect:
		return PathStatePublicDirect
	case PathStateReconnecting:
		return PathStateReconnecting
	case PathStateWaitingCoordinator:
		return PathStateWaitingCoordinator
	case PathStateConnecting:
		return PathStateConnecting
	case PathStateTryingLANDirect:
		return PathStateTryingLANDirect
	case PathStateTryingPublicDirect:
		return PathStateTryingPublicDirect
	case PathStateLANDirectConnected:
		return PathStateLANDirectConnected
	case PathStatePublicDirectConnected:
		return PathStatePublicDirectConnected
	case PathStateFallbackRelay:
		return PathStateFallbackRelay
	case PathStateFailed:
		return PathStateFailed
	case PathStateClosed:
		return PathStateClosed
	case PathStateOffline:
		return PathStateOffline
	case PathStateRDPUnreachable:
		return PathStateRDPUnreachable
	default:
		return ""
	}
}

func connectedConnectionPathState(pathType PathType) PathState {
	switch pathType {
	case PathTypePublicDirect:
		return PathStatePublicDirectConnected
	case PathTypeRelay:
		return PathStateFallbackRelay
	default:
		return PathStateLANDirectConnected
	}
}

func isConnectionFailureState(state PathState) bool {
	return state == PathStateFailed || state == PathStateOffline || state == PathStateRDPUnreachable
}
