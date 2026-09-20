package cloudhub

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	MonitoringBackendMemory = "memory"
	MonitoringSystemAccount = "__system__"
)

type MonitoringAccountScope string

const (
	MonitoringAccountScopeAccount MonitoringAccountScope = "account"
	MonitoringAccountScopeSystem  MonitoringAccountScope = "system"
)

type MonitoringDataStatus string

const (
	MonitoringDataAvailable MonitoringDataStatus = "available"
	MonitoringDataMissing   MonitoringDataStatus = "missing"
)

type MonitoringUnit string

const (
	MonitoringUnitCount          MonitoringUnit = "count"
	MonitoringUnitBytesPerSecond MonitoringUnit = "bytes_per_second"
	MonitoringUnitRatio          MonitoringUnit = "ratio"
	MonitoringUnitMilliseconds   MonitoringUnit = "milliseconds"
)

type MonitoringAlertStatus string

const (
	MonitoringAlertFiring   MonitoringAlertStatus = "firing"
	MonitoringAlertResolved MonitoringAlertStatus = "resolved"
)

type MonitoringAlertName string

const (
	MonitoringAlertRelayErrorRate MonitoringAlertName = "relay_error_rate_high"
	MonitoringAlertRelayBandwidth MonitoringAlertName = "relay_bandwidth_high"
)

type MonitoringThresholds struct {
	RelayErrorRate               float64 `json:"relay_error_rate"`
	RelayBandwidthBytesPerSecond float64 `json:"relay_bandwidth_bytes_per_second"`
}

type MonitoringConfig struct {
	Service        string
	Region         string
	Window         time.Duration
	Thresholds     MonitoringThresholds
	Now            func() time.Time
	MaxAlertEvents int
}

type MonitoringDimensions struct {
	Service      string                 `json:"service"`
	Region       string                 `json:"region"`
	Account      string                 `json:"account"`
	AccountScope MonitoringAccountScope `json:"account_scope"`
}

type MonitoringMetric struct {
	Value      float64              `json:"value"`
	Unit       MonitoringUnit       `json:"unit"`
	DataStatus MonitoringDataStatus `json:"data_status"`
}

type AccountMonitoringMetrics struct {
	Dimensions     MonitoringDimensions `json:"dimensions"`
	AccountStatus  AccountStatus        `json:"account_status,omitempty"`
	StatusData     MonitoringDataStatus `json:"account_status_data_status"`
	LoginFailures  MonitoringMetric     `json:"login_failures"`
	OnlineDevices  MonitoringMetric     `json:"online_devices"`
	RelaySessions  MonitoringMetric     `json:"relay_sessions"`
	RelayBandwidth MonitoringMetric     `json:"relay_bandwidth"`
	RelayErrorRate MonitoringMetric     `json:"relay_error_rate"`
	BanEvents      MonitoringMetric     `json:"ban_events"`
}

type SystemMonitoringMetrics struct {
	Dimensions     MonitoringDimensions `json:"dimensions"`
	LoginFailures  MonitoringMetric     `json:"login_failures"`
	StorageLatency MonitoringMetric     `json:"storage_latency"`
}

type MonitoringAlert struct {
	Name          MonitoringAlertName   `json:"name"`
	Status        MonitoringAlertStatus `json:"status"`
	Dimensions    MonitoringDimensions  `json:"dimensions"`
	Value         float64               `json:"value"`
	ValueStatus   MonitoringDataStatus  `json:"value_data_status"`
	Threshold     float64               `json:"threshold"`
	Window        time.Duration         `json:"-"`
	WindowSeconds int64                 `json:"window_seconds"`
	StartedAt     time.Time             `json:"started_at"`
	ChangedAt     time.Time             `json:"changed_at"`
}

type MonitoringAlertEvent = MonitoringAlert

type MonitoringSnapshot struct {
	GeneratedAt   time.Time                  `json:"generated_at"`
	Service       string                     `json:"service"`
	Region        string                     `json:"region"`
	Backend       string                     `json:"backend"`
	Window        time.Duration              `json:"-"`
	WindowSeconds int64                      `json:"window_seconds"`
	Thresholds    MonitoringThresholds       `json:"thresholds"`
	Accounts      []AccountMonitoringMetrics `json:"accounts"`
	System        SystemMonitoringMetrics    `json:"system"`
	ActiveAlerts  []MonitoringAlert          `json:"active_alerts"`
	AlertEvents   []MonitoringAlertEvent     `json:"alert_events"`
}

// StorageLatencyRecorder is the collection boundary that future database
// store wrappers can use without changing the public monitoring snapshot.
type StorageLatencyRecorder interface {
	RecordStorageLatency(time.Duration, error)
}

type timedAccountEvent struct {
	at        time.Time
	accountID string
}

type relayObservation struct {
	at        time.Time
	accountID string
	bytes     int64
	failed    bool
}

type storageLatencyObservation struct {
	at      time.Time
	latency time.Duration
}

type monitoredDevice struct {
	accountID string
	status    DeviceStatus
}

type Monitor struct {
	mu sync.Mutex

	service        string
	region         string
	window         time.Duration
	thresholds     MonitoringThresholds
	now            func() time.Time
	maxAlertEvents int

	accountStatuses map[string]AccountStatus
	devices         map[string]monitoredDevice
	relaySessions   map[string]string
	loginFailures   []timedAccountEvent
	banEvents       []timedAccountEvent
	relay           []relayObservation
	storage         []storageLatencyObservation
	activeAlerts    map[string]MonitoringAlert
	alertEvents     []MonitoringAlertEvent
}

func NewMonitor(cfg MonitoringConfig) *Monitor {
	service := strings.TrimSpace(cfg.Service)
	if service == "" {
		service = "cloud-hub"
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "unknown"
	}
	window := cfg.Window
	if window <= 0 {
		window = 5 * time.Minute
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	maxAlertEvents := cfg.MaxAlertEvents
	if maxAlertEvents <= 0 {
		maxAlertEvents = 256
	}
	return &Monitor{
		service:         service,
		region:          region,
		window:          window,
		thresholds:      cfg.Thresholds,
		now:             now,
		maxAlertEvents:  maxAlertEvents,
		accountStatuses: map[string]AccountStatus{},
		devices:         map[string]monitoredDevice{},
		relaySessions:   map[string]string{},
		activeAlerts:    map[string]MonitoringAlert{},
	}
}

func (m *Monitor) RecordAccountStatus(accountID string, status AccountStatus) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountStatuses[accountID] = status
}

func (m *Monitor) RecordLoginFailure(accountID string) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		accountID = MonitoringSystemAccount
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loginFailures = append(m.loginFailures, timedAccountEvent{at: m.nowTime(), accountID: accountID})
}

func (m *Monitor) RecordDeviceStatus(accountID, deviceID string, status DeviceStatus) {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	if accountID == "" || deviceID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[deviceID] = monitoredDevice{accountID: accountID, status: status}
}

func (m *Monitor) RecordRelaySessionStarted(accountID, sessionID string) {
	accountID = strings.TrimSpace(accountID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.relaySessions[sessionID] = accountID
}

func (m *Monitor) RecordRelaySessionStopped(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.relaySessions, sessionID)
}

func (m *Monitor) RecordRelayObservation(accountID string, relayBytes int64, failed bool) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return
	}
	if relayBytes < 0 {
		relayBytes = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.relay = append(m.relay, relayObservation{at: m.nowTime(), accountID: accountID, bytes: relayBytes, failed: failed})
}

func (m *Monitor) RecordBanEvent(accountID string) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountStatuses[accountID] = AccountStatusBanned
	m.banEvents = append(m.banEvents, timedAccountEvent{at: m.nowTime(), accountID: accountID})
}

func (m *Monitor) RecordStorageLatency(latency time.Duration, err error) {
	if err != nil || latency < 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storage = append(m.storage, storageLatencyObservation{at: m.nowTime(), latency: latency})
}

func (m *Monitor) Snapshot() MonitoringSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowTime()
	m.pruneLocked(now)
	m.evaluateAlertsLocked(now)
	return m.snapshotLocked(now)
}

func (m *Monitor) EvaluateAlerts() []MonitoringAlertEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowTime()
	m.pruneLocked(now)
	return m.evaluateAlertsLocked(now)
}

func (m *Monitor) nowTime() time.Time {
	return m.now().UTC()
}

func (m *Monitor) pruneLocked(now time.Time) {
	cutoff := now.Add(-m.window)
	m.loginFailures = pruneAccountEvents(m.loginFailures, cutoff)
	m.banEvents = pruneAccountEvents(m.banEvents, cutoff)
	relay := m.relay[:0]
	for _, observation := range m.relay {
		if !observation.at.Before(cutoff) {
			relay = append(relay, observation)
		}
	}
	m.relay = relay
	storage := m.storage[:0]
	for _, observation := range m.storage {
		if !observation.at.Before(cutoff) {
			storage = append(storage, observation)
		}
	}
	m.storage = storage
}

func pruneAccountEvents(events []timedAccountEvent, cutoff time.Time) []timedAccountEvent {
	kept := events[:0]
	for _, event := range events {
		if !event.at.Before(cutoff) {
			kept = append(kept, event)
		}
	}
	return kept
}

func (m *Monitor) snapshotLocked(now time.Time) MonitoringSnapshot {
	accounts := m.accountIDsLocked()
	metrics := make([]AccountMonitoringMetrics, 0, len(accounts))
	for _, accountID := range accounts {
		metrics = append(metrics, m.accountMetricsLocked(accountID))
	}
	activeAlerts := make([]MonitoringAlert, 0, len(m.activeAlerts))
	for _, alert := range m.activeAlerts {
		activeAlerts = append(activeAlerts, alert)
	}
	sort.Slice(activeAlerts, func(i, j int) bool {
		if activeAlerts[i].Dimensions.Account == activeAlerts[j].Dimensions.Account {
			return activeAlerts[i].Name < activeAlerts[j].Name
		}
		return activeAlerts[i].Dimensions.Account < activeAlerts[j].Dimensions.Account
	})
	return MonitoringSnapshot{
		GeneratedAt:   now,
		Service:       m.service,
		Region:        m.region,
		Backend:       MonitoringBackendMemory,
		Window:        m.window,
		WindowSeconds: int64(m.window / time.Second),
		Thresholds:    m.thresholds,
		Accounts:      metrics,
		System: SystemMonitoringMetrics{
			Dimensions: MonitoringDimensions{
				Service: m.service, Region: m.region, Account: MonitoringSystemAccount, AccountScope: MonitoringAccountScopeSystem,
			},
			LoginFailures:  m.systemLoginFailuresMetricLocked(),
			StorageLatency: m.storageLatencyMetricLocked(),
		},
		ActiveAlerts: activeAlerts,
		AlertEvents:  append([]MonitoringAlertEvent(nil), m.alertEvents...),
	}
}

func (m *Monitor) accountIDsLocked() []string {
	set := map[string]struct{}{}
	for accountID := range m.accountStatuses {
		set[accountID] = struct{}{}
	}
	for _, device := range m.devices {
		set[device.accountID] = struct{}{}
	}
	for _, accountID := range m.relaySessions {
		set[accountID] = struct{}{}
	}
	for _, event := range m.loginFailures {
		if event.accountID != MonitoringSystemAccount {
			set[event.accountID] = struct{}{}
		}
	}
	for _, event := range m.banEvents {
		set[event.accountID] = struct{}{}
	}
	for _, observation := range m.relay {
		set[observation.accountID] = struct{}{}
	}
	accounts := make([]string, 0, len(set))
	for accountID := range set {
		accounts = append(accounts, accountID)
	}
	sort.Strings(accounts)
	return accounts
}

func (m *Monitor) accountMetricsLocked(accountID string) AccountMonitoringMetrics {
	loginFailures := 0
	for _, event := range m.loginFailures {
		if event.accountID == accountID {
			loginFailures++
		}
	}
	banEvents := 0
	for _, event := range m.banEvents {
		if event.accountID == accountID {
			banEvents++
		}
	}
	onlineDevices := 0
	for _, device := range m.devices {
		if device.accountID == accountID && device.status == DeviceStatusOnline {
			onlineDevices++
		}
	}
	relaySessions := 0
	for _, sessionAccountID := range m.relaySessions {
		if sessionAccountID == accountID {
			relaySessions++
		}
	}
	observations := 0
	failed := 0
	var relayBytes int64
	for _, observation := range m.relay {
		if observation.accountID != accountID {
			continue
		}
		observations++
		relayBytes += observation.bytes
		if observation.failed {
			failed++
		}
	}
	bandwidth := missingMetric(MonitoringUnitBytesPerSecond)
	errorRate := missingMetric(MonitoringUnitRatio)
	if observations > 0 {
		bandwidth = availableMetric(float64(relayBytes)/m.window.Seconds(), MonitoringUnitBytesPerSecond)
		errorRate = availableMetric(float64(failed)/float64(observations), MonitoringUnitRatio)
	}
	status, hasStatus := m.accountStatuses[accountID]
	statusData := MonitoringDataMissing
	if hasStatus {
		statusData = MonitoringDataAvailable
	}
	return AccountMonitoringMetrics{
		Dimensions: MonitoringDimensions{
			Service: m.service, Region: m.region, Account: accountID, AccountScope: MonitoringAccountScopeAccount,
		},
		AccountStatus:  status,
		StatusData:     statusData,
		LoginFailures:  availableMetric(float64(loginFailures), MonitoringUnitCount),
		OnlineDevices:  availableMetric(float64(onlineDevices), MonitoringUnitCount),
		RelaySessions:  availableMetric(float64(relaySessions), MonitoringUnitCount),
		RelayBandwidth: bandwidth,
		RelayErrorRate: errorRate,
		BanEvents:      availableMetric(float64(banEvents), MonitoringUnitCount),
	}
}

func (m *Monitor) storageLatencyMetricLocked() MonitoringMetric {
	if len(m.storage) == 0 {
		return missingMetric(MonitoringUnitMilliseconds)
	}
	var total time.Duration
	for _, observation := range m.storage {
		total += observation.latency
	}
	averageMilliseconds := float64(total) / float64(len(m.storage)) / float64(time.Millisecond)
	return availableMetric(averageMilliseconds, MonitoringUnitMilliseconds)
}

func (m *Monitor) systemLoginFailuresMetricLocked() MonitoringMetric {
	count := 0
	for _, event := range m.loginFailures {
		if event.accountID == MonitoringSystemAccount {
			count++
		}
	}
	return availableMetric(float64(count), MonitoringUnitCount)
}

func availableMetric(value float64, unit MonitoringUnit) MonitoringMetric {
	return MonitoringMetric{Value: value, Unit: unit, DataStatus: MonitoringDataAvailable}
}

func missingMetric(unit MonitoringUnit) MonitoringMetric {
	return MonitoringMetric{Unit: unit, DataStatus: MonitoringDataMissing}
}

type alertCandidate struct {
	name       MonitoringAlertName
	dimensions MonitoringDimensions
	value      float64
	status     MonitoringDataStatus
	threshold  float64
}

func (m *Monitor) evaluateAlertsLocked(now time.Time) []MonitoringAlertEvent {
	candidates := make([]alertCandidate, 0)
	for _, accountID := range m.accountIDsLocked() {
		metrics := m.accountMetricsLocked(accountID)
		if m.thresholds.RelayErrorRate > 0 {
			candidates = append(candidates, alertCandidate{
				name: MonitoringAlertRelayErrorRate, dimensions: metrics.Dimensions,
				value: metrics.RelayErrorRate.Value, status: metrics.RelayErrorRate.DataStatus, threshold: m.thresholds.RelayErrorRate,
			})
		}
		if m.thresholds.RelayBandwidthBytesPerSecond > 0 {
			candidates = append(candidates, alertCandidate{
				name: MonitoringAlertRelayBandwidth, dimensions: metrics.Dimensions,
				value: metrics.RelayBandwidth.Value, status: metrics.RelayBandwidth.DataStatus, threshold: m.thresholds.RelayBandwidthBytesPerSecond,
			})
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	transitions := make([]MonitoringAlertEvent, 0)
	for _, candidate := range candidates {
		key := alertKey(candidate.name, candidate.dimensions.Account)
		seen[key] = struct{}{}
		active, wasActive := m.activeAlerts[key]
		firing := candidate.status == MonitoringDataAvailable && candidate.value >= candidate.threshold
		switch {
		case firing && !wasActive:
			alert := MonitoringAlert{
				Name: candidate.name, Status: MonitoringAlertFiring, Dimensions: candidate.dimensions,
				Value: candidate.value, ValueStatus: candidate.status, Threshold: candidate.threshold,
				Window: m.window, WindowSeconds: int64(m.window / time.Second), StartedAt: now, ChangedAt: now,
			}
			m.activeAlerts[key] = alert
			transitions = append(transitions, alert)
		case firing && wasActive:
			active.Value = candidate.value
			active.ValueStatus = candidate.status
			m.activeAlerts[key] = active
		case !firing && wasActive:
			delete(m.activeAlerts, key)
			active.Status = MonitoringAlertResolved
			active.Value = candidate.value
			active.ValueStatus = candidate.status
			active.ChangedAt = now
			transitions = append(transitions, active)
		}
	}
	for key, active := range m.activeAlerts {
		if _, ok := seen[key]; ok {
			continue
		}
		delete(m.activeAlerts, key)
		active.Status = MonitoringAlertResolved
		active.Value = 0
		active.ValueStatus = MonitoringDataMissing
		active.ChangedAt = now
		transitions = append(transitions, active)
	}
	sort.Slice(transitions, func(i, j int) bool {
		if transitions[i].Dimensions.Account == transitions[j].Dimensions.Account {
			return transitions[i].Name < transitions[j].Name
		}
		return transitions[i].Dimensions.Account < transitions[j].Dimensions.Account
	})
	for _, event := range transitions {
		m.alertEvents = append(m.alertEvents, event)
	}
	if len(m.alertEvents) > m.maxAlertEvents {
		m.alertEvents = append([]MonitoringAlertEvent(nil), m.alertEvents[len(m.alertEvents)-m.maxAlertEvents:]...)
	}
	return transitions
}

func alertKey(name MonitoringAlertName, accountID string) string {
	return string(name) + "\x00" + accountID
}
