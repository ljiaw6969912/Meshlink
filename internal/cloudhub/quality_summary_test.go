package cloudhub

import (
	"context"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestAccountManagementSummarySeparatesDirectAndRelayConnectionQuality(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	directLog, err := svc.RecordConnectionLog(ctx, RecordConnectionLogRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		PathType:           string(p2p.PathTypeLANDirect),
		PathState:          string(p2p.PathStateLANDirectConnected),
		LatencyMS:          24,
		PacketLossPermille: 3,
		JitterMS:           5,
		RelayBytesIn:       999,
		RelayBytesOut:      1000,
	})
	if err != nil {
		t.Fatalf("RecordConnectionLog direct returned error: %v", err)
	}
	if directLog.QualityScore == 0 {
		t.Fatalf("direct log = %+v, want computed quality score", directLog)
	}
	if directLog.RelayBytesIn != 0 || directLog.RelayBytesOut != 0 {
		t.Fatalf("direct log relay bytes = %d/%d, want sanitized to zero", directLog.RelayBytesIn, directLog.RelayBytesOut)
	}

	relayLog, err := svc.RecordConnectionLog(ctx, RecordConnectionLogRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		PathType:           string(p2p.PathTypeRelay),
		PathState:          string(p2p.PathStateFallbackRelay),
		LatencyMS:          110,
		PacketLossPermille: 12,
		JitterMS:           18,
		RelayBytesIn:       64,
		RelayBytesOut:      96,
		SwitchCount:        1,
		SwitchReasons:      []string{"direct_candidates_failed"},
	})
	if err != nil {
		t.Fatalf("RecordConnectionLog relay returned error: %v", err)
	}
	if relayLog.QualityScore >= directLog.QualityScore {
		t.Fatalf("relay score=%d direct score=%d, want higher latency/loss relay sample to score lower", relayLog.QualityScore, directLog.QualityScore)
	}

	summary, err := svc.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if summary.ConnectionQuality.Direct.Samples != 1 || summary.ConnectionQuality.Relay.Samples != 1 {
		t.Fatalf("connection quality = %+v, want direct and relay buckets", summary.ConnectionQuality)
	}
	if summary.ConnectionQuality.Direct.RelayBytesIn != 0 || summary.ConnectionQuality.Direct.RelayBytesOut != 0 {
		t.Fatalf("direct quality bucket relay bytes = %d/%d, want zero", summary.ConnectionQuality.Direct.RelayBytesIn, summary.ConnectionQuality.Direct.RelayBytesOut)
	}
	if summary.ConnectionQuality.Relay.RelayBytesIn != 64 || summary.ConnectionQuality.Relay.RelayBytesOut != 96 {
		t.Fatalf("relay quality bucket bytes = %d/%d, want 64/96", summary.ConnectionQuality.Relay.RelayBytesIn, summary.ConnectionQuality.Relay.RelayBytesOut)
	}
	if summary.ConnectionQuality.SwitchCount != 1 || len(summary.ConnectionQuality.SwitchReasons) != 1 {
		t.Fatalf("connection quality switch audit = %+v, want switch count and reason", summary.ConnectionQuality)
	}
	assertNoSensitiveJSON(t, summary)
}

func TestConnectionLogRecordsMultipathSwitchDecisionSummary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	log, err := svc.RecordConnectionLog(ctx, RecordConnectionLogRequest{
		AccountID:          account.ID,
		NetworkID:          network.ID,
		SourceDeviceID:     source.ID,
		TargetDeviceID:     target.ID,
		PathType:           string(p2p.PathTypeRelay),
		PathState:          string(p2p.PathStateFallbackRelay),
		LatencyMS:          88,
		PacketLossPermille: 8,
		JitterMS:           12,
		SwitchCount:        1,
		SwitchReasons:      []string{"direct_quality_degraded", "token should not leak"},
		SwitchFromPath:     string(p2p.PathTypeLANDirect),
		SwitchToPath:       string(p2p.PathTypeRelay),
		SwitchScoreDelta:   31,
		AutoSwitched:       true,
	})
	if err != nil {
		t.Fatalf("RecordConnectionLog returned error: %v", err)
	}
	if log.SwitchFromPath != string(p2p.PathTypeLANDirect) || log.SwitchToPath != string(p2p.PathTypeRelay) {
		t.Fatalf("switch paths = %q -> %q, want lan_direct -> relay", log.SwitchFromPath, log.SwitchToPath)
	}
	if log.SwitchScoreDelta != 31 || !log.AutoSwitched {
		t.Fatalf("switch summary = delta:%d auto:%v, want delta 31 and auto switch", log.SwitchScoreDelta, log.AutoSwitched)
	}
	if len(log.SwitchReasons) != 2 || log.SwitchReasons[1] != "redacted" {
		t.Fatalf("switch reasons = %+v, want sanitized sensitive reason", log.SwitchReasons)
	}

	summary, err := svc.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if len(summary.ConnectionLogs) != 1 {
		t.Fatalf("connection logs = %+v, want one log", summary.ConnectionLogs)
	}
	got := summary.ConnectionLogs[0]
	if got.SwitchFromPath != string(p2p.PathTypeLANDirect) || got.SwitchToPath != string(p2p.PathTypeRelay) || got.SwitchScoreDelta != 31 || !got.AutoSwitched {
		t.Fatalf("summary switch log = %+v, want non-sensitive switch decision summary", got)
	}
	assertNoSensitiveJSON(t, summary)
}
