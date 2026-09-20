package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func newTask11DMonitor(now *time.Time, thresholds MonitoringThresholds) *Monitor {
	return NewMonitor(MonitoringConfig{
		Service:    "cloud-hub",
		Region:     "cn-east-1",
		Window:     5 * time.Minute,
		Thresholds: thresholds,
		Now: func() time.Time {
			return now.UTC()
		},
	})
}

func TestMonitoringSnapshotAggregatesRequiredMetricsAndMarksMemoryBackend(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{})

	monitor.RecordAccountStatus("acct-1", AccountStatusActive)
	monitor.RecordLoginFailure("acct-1")
	monitor.RecordLoginFailure("acct-1")
	monitor.RecordDeviceStatus("acct-1", "device-online", DeviceStatusOnline)
	monitor.RecordDeviceStatus("acct-1", "device-offline", DeviceStatusOffline)
	monitor.RecordRelaySessionStarted("acct-1", "relay-active")
	monitor.RecordRelayObservation("acct-1", 600, false)
	monitor.RecordRelayObservation("acct-1", 300, true)
	monitor.RecordBanEvent("acct-1")
	monitor.RecordStorageLatency(10*time.Millisecond, nil)
	monitor.RecordStorageLatency(30*time.Millisecond, nil)

	snapshot := monitor.Snapshot()
	if snapshot.Service != "cloud-hub" || snapshot.Region != "cn-east-1" || snapshot.Window != 5*time.Minute {
		t.Fatalf("snapshot identity/window = %+v", snapshot)
	}
	if snapshot.Backend != MonitoringBackendMemory {
		t.Fatalf("backend = %q, want memory", snapshot.Backend)
	}
	if len(snapshot.Accounts) != 1 {
		t.Fatalf("accounts = %+v, want one account", snapshot.Accounts)
	}
	account := snapshot.Accounts[0]
	if account.Dimensions != (MonitoringDimensions{Service: "cloud-hub", Region: "cn-east-1", Account: "acct-1", AccountScope: MonitoringAccountScopeAccount}) {
		t.Fatalf("account dimensions = %+v", account.Dimensions)
	}
	if account.AccountStatus != AccountStatusBanned {
		t.Fatalf("account status = %q, want banned", account.AccountStatus)
	}
	if account.LoginFailures.Value != 2 || account.OnlineDevices.Value != 1 || account.RelaySessions.Value != 1 || account.BanEvents.Value != 1 {
		t.Fatalf("account counters = %+v", account)
	}
	if account.RelayBandwidth.Value != 3 || account.RelayBandwidth.Unit != MonitoringUnitBytesPerSecond {
		t.Fatalf("relay bandwidth = %+v, want 3 bytes/second", account.RelayBandwidth)
	}
	if account.RelayErrorRate.Value != 0.5 || account.RelayErrorRate.Unit != MonitoringUnitRatio {
		t.Fatalf("relay error rate = %+v, want 0.5", account.RelayErrorRate)
	}
	if account.RelayErrorRate.DataStatus != MonitoringDataAvailable {
		t.Fatalf("relay error data status = %q, want available", account.RelayErrorRate.DataStatus)
	}
	if snapshot.System.Dimensions.Account != MonitoringSystemAccount || snapshot.System.Dimensions.AccountScope != MonitoringAccountScopeSystem {
		t.Fatalf("system dimensions = %+v, want explicit system account", snapshot.System.Dimensions)
	}
	if snapshot.System.StorageLatency.Value != 20 || snapshot.System.StorageLatency.Unit != MonitoringUnitMilliseconds || snapshot.System.StorageLatency.DataStatus != MonitoringDataAvailable {
		t.Fatalf("storage latency = %+v, want available 20ms", snapshot.System.StorageLatency)
	}
	monitor.RecordDeviceStatus("acct-1", "device-online", DeviceStatusOffline)
	monitor.RecordRelaySessionStopped("relay-active")
	stopped := monitor.Snapshot().Accounts[0]
	if stopped.OnlineDevices.Value != 0 || stopped.RelaySessions.Value != 0 {
		t.Fatalf("stopped device/session gauges = %+v, want zero", stopped)
	}
}

func TestMonitoringMissingDataAndRollingWindowBoundary(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{RelayBandwidthBytesPerSecond: 1})
	monitor.RecordAccountStatus("acct-window", AccountStatusActive)
	monitor.RecordStorageLatency(time.Second, errors.New("local storage failure must not be exposed"))

	initial := monitor.Snapshot()
	if initial.Accounts[0].RelayBandwidth.DataStatus != MonitoringDataMissing || initial.Accounts[0].RelayErrorRate.DataStatus != MonitoringDataMissing {
		t.Fatalf("relay metrics without observations = %+v, want data missing", initial.Accounts[0])
	}
	if initial.System.StorageLatency.DataStatus != MonitoringDataMissing {
		t.Fatalf("storage latency without observations = %+v, want data missing", initial.System.StorageLatency)
	}

	monitor.RecordLoginFailure("acct-window")
	monitor.RecordRelayObservation("acct-window", 300, false)
	now = now.Add(5 * time.Minute)
	atBoundary := monitor.Snapshot()
	if atBoundary.Accounts[0].LoginFailures.Value != 1 || atBoundary.Accounts[0].RelayBandwidth.DataStatus != MonitoringDataAvailable {
		t.Fatalf("metrics at inclusive window boundary = %+v", atBoundary.Accounts[0])
	}

	now = now.Add(time.Nanosecond)
	afterBoundary := monitor.Snapshot()
	if afterBoundary.Accounts[0].LoginFailures.Value != 0 || afterBoundary.Accounts[0].RelayBandwidth.DataStatus != MonitoringDataMissing {
		t.Fatalf("expired rolling-window metrics = %+v", afterBoundary.Accounts[0])
	}
}

func TestMonitoringLoginFailureWithoutAccountUsesExplicitSystemDimension(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{})
	monitor.RecordLoginFailure("")

	snapshot := monitor.Snapshot()
	if len(snapshot.Accounts) != 0 {
		t.Fatalf("account metrics = %+v, want no invented account", snapshot.Accounts)
	}
	if snapshot.System.LoginFailures.Value != 1 || snapshot.System.LoginFailures.DataStatus != MonitoringDataAvailable {
		t.Fatalf("system login failures = %+v, want one available event", snapshot.System.LoginFailures)
	}
	if snapshot.System.Dimensions.Account != MonitoringSystemAccount || snapshot.System.Dimensions.AccountScope != MonitoringAccountScopeSystem {
		t.Fatalf("system dimensions = %+v", snapshot.System.Dimensions)
	}
}

func TestMonitoringRelayAlertsTriggerAtThresholdDeduplicateAndRecover(t *testing.T) {
	now := time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{
		RelayErrorRate:               0.10,
		RelayBandwidthBytesPerSecond: 1000,
	})

	for i := 0; i < 10; i++ {
		monitor.RecordRelayObservation("acct-alert", 30_000, i == 0)
	}
	triggered := monitor.EvaluateAlerts()
	if len(triggered) != 2 {
		t.Fatalf("triggered alerts = %+v, want error-rate and bandwidth alerts", triggered)
	}
	for _, event := range triggered {
		if event.Status != MonitoringAlertFiring {
			t.Fatalf("trigger event = %+v, want firing", event)
		}
		if event.Dimensions.Service != "cloud-hub" || event.Dimensions.Region != "cn-east-1" || event.Dimensions.Account != "acct-alert" || event.Dimensions.AccountScope != MonitoringAccountScopeAccount {
			t.Fatalf("alert dimensions = %+v", event.Dimensions)
		}
	}
	if duplicate := monitor.EvaluateAlerts(); len(duplicate) != 0 {
		t.Fatalf("duplicate evaluation emitted %+v", duplicate)
	}

	snapshot := monitor.Snapshot()
	if len(snapshot.ActiveAlerts) != 2 {
		t.Fatalf("active alerts = %+v, want two", snapshot.ActiveAlerts)
	}
	now = now.Add(5*time.Minute + time.Nanosecond)
	recovered := monitor.EvaluateAlerts()
	if len(recovered) != 2 {
		t.Fatalf("recovered alerts = %+v, want two", recovered)
	}
	for _, event := range recovered {
		if event.Status != MonitoringAlertResolved || event.StartedAt.IsZero() || !event.ChangedAt.Equal(now) {
			t.Fatalf("recovery event = %+v", event)
		}
	}
	if duplicateRecovery := monitor.EvaluateAlerts(); len(duplicateRecovery) != 0 {
		t.Fatalf("duplicate recovery emitted %+v", duplicateRecovery)
	}
	if active := monitor.Snapshot().ActiveAlerts; len(active) != 0 {
		t.Fatalf("active alerts after recovery = %+v", active)
	}
}

func TestMonitoringRelayAlertBelowThresholdDoesNotFire(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{
		RelayErrorRate:               0.11,
		RelayBandwidthBytesPerSecond: 1001,
	})
	for i := 0; i < 10; i++ {
		monitor.RecordRelayObservation("acct-below", 30_000, i == 0)
	}
	if events := monitor.EvaluateAlerts(); len(events) != 0 {
		t.Fatalf("below-threshold alerts = %+v, want none", events)
	}
}

func TestMonitoringSnapshotIsSanitizedAndDoesNotExposeEntityIdentifiers(t *testing.T) {
	now := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{})
	monitor.RecordAccountStatus("acct-safe", AccountStatusActive)
	monitor.RecordDeviceStatus("acct-safe", "device-secret-id", DeviceStatusOnline)
	monitor.RecordRelaySessionStarted("acct-safe", "session-secret-id")
	monitor.RecordRelayObservation("acct-safe", 42, true)

	encoded, err := json.Marshal(monitor.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, forbidden := range [][]byte{
		[]byte("device-secret-id"), []byte("session-secret-id"), []byte("password"), []byte("token"),
		[]byte("private_key"), []byte("candidate"), []byte("raw_traffic"), []byte("content"),
	} {
		if bytes.Contains(bytes.ToLower(encoded), forbidden) {
			t.Fatalf("monitoring snapshot leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestMonitoringRecorderIsConcurrentSafe(t *testing.T) {
	now := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{})
	const workers = 20
	const recordsPerWorker = 100
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < recordsPerWorker; i++ {
				monitor.RecordLoginFailure("acct-concurrent")
				monitor.RecordRelayObservation("acct-concurrent", 1, false)
			}
		}()
	}
	wg.Wait()

	account := monitor.Snapshot().Accounts[0]
	if account.LoginFailures.Value != workers*recordsPerWorker {
		t.Fatalf("login failures = %v, want %d", account.LoginFailures.Value, workers*recordsPerWorker)
	}
	if account.RelayBandwidth.Value != float64(workers*recordsPerWorker)/300 {
		t.Fatalf("relay bandwidth = %v, want %v", account.RelayBandwidth.Value, float64(workers*recordsPerWorker)/300)
	}
}

func TestMonitoringAPIAndClientRequireTrustedAuthorizedActor(t *testing.T) {
	now := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	monitor := newTask11DMonitor(&now, MonitoringThresholds{})
	monitor.RecordAccountStatus("acct-customer", AccountStatusActive)
	hub := httptest.NewServer(NewServer(nil,
		WithMonitoring(monitor),
		WithMonitoringAuthorizer(func(_ context.Context, actorAccountID string) bool {
			return actorAccountID == "acct-ops"
		}),
	))
	defer hub.Close()

	unauthenticated := &Client{BaseURL: hub.URL, HTTPClient: hub.Client()}
	if _, err := unauthenticated.GetMonitoringSnapshot(context.Background()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing actor error = %v, want forbidden", err)
	}
	unauthorized := &Client{BaseURL: hub.URL, HTTPClient: hub.Client(), ActorAccountID: "acct-customer"}
	if _, err := unauthorized.GetMonitoringSnapshot(context.Background()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized actor error = %v, want forbidden", err)
	}
	authorized := &Client{BaseURL: hub.URL, HTTPClient: hub.Client(), ActorAccountID: "acct-ops"}
	snapshot, err := authorized.GetMonitoringSnapshot(context.Background())
	if err != nil {
		t.Fatalf("authorized monitoring query: %v", err)
	}
	if snapshot.Backend != MonitoringBackendMemory || len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Dimensions.Account != "acct-customer" {
		t.Fatalf("monitoring response = %+v", snapshot)
	}
}
