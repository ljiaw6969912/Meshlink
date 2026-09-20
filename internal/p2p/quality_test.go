package p2p

import "testing"

func TestConnectionQualityScoreDeclinesAsLatencyIncreases(t *testing.T) {
	lowLatency := ScoreConnectionQuality(ConnectionQualityInput{
		PathType:           PathTypeLANDirect,
		State:              PathStateLANDirectConnected,
		LatencyMS:          20,
		PacketLossPermille: 5,
		JitterMS:           3,
		SwitchCount:        1,
		SwitchReasons:      []string{"initial_lan_direct"},
	})
	highLatency := ScoreConnectionQuality(ConnectionQualityInput{
		PathType:           PathTypeLANDirect,
		State:              PathStateLANDirectConnected,
		LatencyMS:          350,
		PacketLossPermille: 5,
		JitterMS:           3,
	})

	if lowLatency.Score <= highLatency.Score {
		t.Fatalf("low latency score=%d high latency score=%d, want score to fall as latency rises", lowLatency.Score, highLatency.Score)
	}
	if lowLatency.PathType != PathTypeLANDirect || lowLatency.State != PathStateLANDirectConnected {
		t.Fatalf("quality summary = %+v, want path type and state preserved", lowLatency)
	}
	if lowLatency.LatencyMS != 20 || lowLatency.PacketLossPermille != 5 || lowLatency.JitterMS != 3 {
		t.Fatalf("quality metrics = %+v, want latency/loss/jitter recorded", lowLatency)
	}
}

func TestConnectionQualitySummarySeparatesDirectAndRelayMetrics(t *testing.T) {
	summary := SummarizeConnectionQuality([]ConnectionQualityInput{
		{
			PathType:           PathTypeLANDirect,
			State:              PathStateLANDirectConnected,
			LatencyMS:          18,
			PacketLossPermille: 2,
			JitterMS:           4,
			RelayBytesIn:       900,
			RelayBytesOut:      1200,
		},
		{
			PathType:           PathTypeRelay,
			State:              PathStateFallbackRelay,
			LatencyMS:          95,
			PacketLossPermille: 15,
			JitterMS:           20,
			RelayBytesIn:       300,
			RelayBytesOut:      400,
			SwitchCount:        1,
			SwitchReasons:      []string{"direct_candidates_failed"},
		},
	})

	if summary.Direct.Samples != 1 || summary.Relay.Samples != 1 {
		t.Fatalf("summary = %+v, want one direct and one relay sample", summary)
	}
	if summary.Direct.RelayBytesIn != 0 || summary.Direct.RelayBytesOut != 0 {
		t.Fatalf("direct bucket relay bytes = %d/%d, want zero so relay bytes cannot pollute direct metrics", summary.Direct.RelayBytesIn, summary.Direct.RelayBytesOut)
	}
	if summary.Relay.RelayBytesIn != 300 || summary.Relay.RelayBytesOut != 400 {
		t.Fatalf("relay bucket bytes = %d/%d, want relay-only byte counts", summary.Relay.RelayBytesIn, summary.Relay.RelayBytesOut)
	}
	if summary.SwitchCount != 1 || len(summary.SwitchReasons) != 1 || summary.SwitchReasons[0] != "direct_candidates_failed" {
		t.Fatalf("switch audit = count:%d reasons:%v, want recorded switch reason", summary.SwitchCount, summary.SwitchReasons)
	}
}

func TestNormalizeConnectionStatusDefaultsOldOnlinePeerToDirect(t *testing.T) {
	status := NormalizeConnectionStatus(ConnectionStatus{
		LastError: "token should not leak",
	}, ConnectionStatusDefaults{Online: true})

	if status.PathType != PathTypeLANDirect || status.PathState != PathStateLANDirectConnected {
		t.Fatalf("status path = %q/%q, want lan direct connected defaults", status.PathType, status.PathState)
	}
	if status.QualityScore == 0 {
		t.Fatalf("quality score = 0, want computed conservative direct score")
	}
	if status.RelayBytesIn != 0 || status.RelayBytesOut != 0 || status.LatencyMS != 0 {
		t.Fatalf("status metrics = %+v, want zeroed default counters", status)
	}
	if status.LastError != "redacted" {
		t.Fatalf("last error = %q, want sensitive text redacted", status.LastError)
	}
}

func TestNormalizeConnectionStatusPreservesRelayAndSwitchSummary(t *testing.T) {
	status := NormalizeConnectionStatus(ConnectionStatus{
		PathType:         PathTypeRelay,
		PathState:        PathStateFallbackRelay,
		LatencyMS:        88,
		RelayBytesIn:     64,
		RelayBytesOut:    96,
		SwitchCount:      1,
		SwitchReasons:    []string{"direct_quality_degraded", "private key should not leak"},
		SwitchFromPath:   PathTypeLANDirect,
		SwitchToPath:     PathTypeRelay,
		SwitchScoreDelta: 31,
		AutoSwitched:     true,
	}, ConnectionStatusDefaults{Online: true})

	if status.PathType != PathTypeRelay || status.PathState != PathStateFallbackRelay {
		t.Fatalf("status path = %q/%q, want relay fallback", status.PathType, status.PathState)
	}
	if status.RelayBytesIn != 64 || status.RelayBytesOut != 96 || status.LatencyMS != 88 {
		t.Fatalf("status metrics = %+v, want relay byte counters and latency", status)
	}
	if status.SwitchCount != 2 || len(status.SwitchReasons) != 2 || status.SwitchReasons[1] != "redacted" {
		t.Fatalf("switch summary = %+v, want sanitized reasons counted", status)
	}
	if status.SwitchFromPath != PathTypeLANDirect || status.SwitchToPath != PathTypeRelay || status.SwitchScoreDelta != 31 || !status.AutoSwitched {
		t.Fatalf("switch decision = %+v, want lan_direct -> relay delta 31 auto", status)
	}
}

func TestNormalizeConnectionStatusDefaultsOfflineWithoutQuality(t *testing.T) {
	status := NormalizeConnectionStatus(ConnectionStatus{
		PathType:      PathTypeRelay,
		PathState:     PathStateFallbackRelay,
		LatencyMS:     -10,
		RelayBytesIn:  12,
		RelayBytesOut: 34,
		LastError:     "rdp-unreachable",
	}, ConnectionStatusDefaults{Online: false})

	if status.PathType != "" || status.PathState != PathStateOffline {
		t.Fatalf("status path = %q/%q, want offline without active path type", status.PathType, status.PathState)
	}
	if status.QualityScore != 0 || status.LatencyMS != 0 || status.RelayBytesIn != 0 || status.RelayBytesOut != 0 {
		t.Fatalf("offline metrics = %+v, want quality and counters zeroed", status)
	}
	if status.LastError != "rdp-unreachable" {
		t.Fatalf("last error = %q, want non-sensitive error preserved", status.LastError)
	}
}
