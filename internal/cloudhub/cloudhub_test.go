package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestServiceAccountNetworkInviteJoinHeartbeatAndRevoke(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))

	account, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "owner@example.com",
		DisplayName: "Owner",
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	if account.ID == "" || account.Email != "owner@example.com" {
		t.Fatalf("account = %+v, want id and email", account)
	}

	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{
		AccountID: account.ID,
		Name:      "Home",
	})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	if network.ID == "" || network.AccountID != account.ID {
		t.Fatalf("network = %+v, want account id %q", network, account.ID)
	}

	invite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if invite.Token == "" {
		t.Fatal("invite token is empty")
	}
	if !regexp.MustCompile(`^\d{6}$`).MatchString(invite.Code) {
		t.Fatalf("invite code = %q, want 6 digits", invite.Code)
	}
	if invite.ExpiresAt.IsZero() || !invite.OneTime || invite.MaxUses != 1 {
		t.Fatalf("invite = %+v, want default expiry, one-time, max uses 1", invite)
	}

	joined, err := svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:       invite.Token,
		Code:        invite.Code,
		DeviceName:  "office-pc",
		Fingerprint: "SHA256:AA:BB",
		RemoteAddr:  "203.0.113.10:443",
	})
	if err != nil {
		t.Fatalf("JoinDevice returned error: %v", err)
	}
	if joined.ID == "" || joined.NetworkID != network.ID || joined.AccountID != account.ID {
		t.Fatalf("joined device = %+v, want account/network binding", joined)
	}

	online, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: joined.ID,
		Status:   DeviceStatusOnline,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice online returned error: %v", err)
	}
	if online.Status != DeviceStatusOnline || online.LastSeen == nil {
		t.Fatalf("online device = %+v, want online with last seen", online)
	}

	offline, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: joined.ID,
		Status:   DeviceStatusOffline,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice offline returned error: %v", err)
	}
	if offline.Status != DeviceStatusOffline {
		t.Fatalf("offline device status = %q, want %q", offline.Status, DeviceStatusOffline)
	}

	connectionLog, err := svc.RecordConnectionLog(ctx, RecordConnectionLogRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: joined.ID,
		TargetDeviceID: joined.ID,
		PathType:       "relay",
		RelayBytesIn:   128,
		RelayBytesOut:  256,
	})
	if err != nil {
		t.Fatalf("RecordConnectionLog returned error: %v", err)
	}
	if connectionLog.ID == "" || connectionLog.RelayBytesIn != 128 || connectionLog.RelayBytesOut != 256 {
		t.Fatalf("connection log = %+v, want relay byte metadata", connectionLog)
	}

	relayUsage, err := svc.RecordRelayUsage(ctx, RecordRelayUsageRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		DeviceID:  joined.ID,
		BytesIn:   128,
		BytesOut:  256,
	})
	if err != nil {
		t.Fatalf("RecordRelayUsage returned error: %v", err)
	}
	if relayUsage.ID == "" || relayUsage.BytesIn != 128 || relayUsage.BytesOut != 256 {
		t.Fatalf("relay usage = %+v, want relay byte metadata", relayUsage)
	}

	devices, err := svc.ListNetworkDevices(ctx, network.ID)
	if err != nil {
		t.Fatalf("ListNetworkDevices returned error: %v", err)
	}
	if len(devices) != 1 || devices[0].ID != joined.ID {
		t.Fatalf("devices = %+v, want joined device", devices)
	}

	networks, err := svc.ListAccountNetworks(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListAccountNetworks returned error: %v", err)
	}
	if len(networks) != 1 || networks[0].ID != network.ID {
		t.Fatalf("networks = %+v, want created network", networks)
	}

	revoked, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{
		DeviceID: joined.ID,
		Reason:   "lost laptop",
	})
	if err != nil {
		t.Fatalf("RevokeDevice returned error: %v", err)
	}
	if revoked.RevokedAt == nil || revoked.Status != DeviceStatusRevoked {
		t.Fatalf("revoked device = %+v, want revoked status and time", revoked)
	}

	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: joined.ID,
		Status:   DeviceStatusOnline,
	}); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("heartbeat after revoke error = %v, want revoked error", err)
	}

	audit, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	wantEvents := []string{
		AuditAccountCreated,
		AuditNetworkCreated,
		AuditInviteCreated,
		AuditDeviceJoined,
		AuditDeviceHeartbeat,
		AuditDeviceHeartbeat,
		AuditDeviceRevoked,
	}
	if len(audit) < len(wantEvents) {
		t.Fatalf("audit events = %+v, want at least %d events", audit, len(wantEvents))
	}
	for _, want := range wantEvents {
		if !hasAuditEvent(audit, want) {
			t.Fatalf("audit events = %+v, missing %q", audit, want)
		}
	}
	for _, event := range audit {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal audit event: %v", err)
		}
		lower := strings.ToLower(string(encoded))
		for _, forbidden := range []string{"private_key", "private_key_pem", "clipboard", "rdp_content", "file_content"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("audit event leaks forbidden field %q: %s", forbidden, encoded)
			}
		}
	}
}

func TestDeviceHeartbeatPersistsConnectionPathQualityFields(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	device := mustJoinDevices(t, ctx, svc, account.ID, network.ID, 1)[0]

	updated, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		AccountID:        account.ID,
		NetworkID:        network.ID,
		DeviceID:         device.ID,
		Status:           DeviceStatusOnline,
		PathType:         p2p.PathTypeRelay,
		PathState:        p2p.PathStateFallbackRelay,
		LatencyMS:        88,
		RelayBytesIn:     64,
		RelayBytesOut:    96,
		SwitchCount:      1,
		SwitchReasons:    []string{"direct_quality_degraded", "private key should not leak"},
		SwitchFromPath:   p2p.PathTypeLANDirect,
		SwitchToPath:     p2p.PathTypeRelay,
		SwitchScoreDelta: 31,
		AutoSwitched:     true,
		LastError:        "token should not leak",
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice returned error: %v", err)
	}
	if updated.PathType != p2p.PathTypeRelay || updated.PathState != p2p.PathStateFallbackRelay {
		t.Fatalf("device path = %+v, want relay fallback", updated.ConnectionStatus)
	}
	if updated.LatencyMS != 88 || updated.RelayBytesIn != 64 || updated.RelayBytesOut != 96 || updated.QualityScore == 0 {
		t.Fatalf("device quality = %+v, want latency, relay bytes, and quality score", updated.ConnectionStatus)
	}
	if updated.SwitchCount != 2 || len(updated.SwitchReasons) != 2 || updated.SwitchReasons[1] != "redacted" || updated.LastError != "redacted" {
		t.Fatalf("device sanitized fields = %+v, want sensitive text redacted", updated.ConnectionStatus)
	}
	if updated.SwitchFromPath != p2p.PathTypeLANDirect || updated.SwitchToPath != p2p.PathTypeRelay || updated.SwitchScoreDelta != 31 || !updated.AutoSwitched {
		t.Fatalf("device switch summary = %+v, want Task 7E fields", updated.ConnectionStatus)
	}

	devices, err := svc.ListNetworkDevices(ctx, network.ID)
	if err != nil {
		t.Fatalf("ListNetworkDevices returned error: %v", err)
	}
	var listed *Device
	for i := range devices {
		if devices[i].ID == device.ID {
			listed = &devices[i]
			break
		}
	}
	if listed == nil || listed.PathType != p2p.PathTypeRelay || listed.LastError != "redacted" {
		t.Fatalf("listed devices = %+v, want persisted connection fields", devices)
	}
}

func TestInviteFailuresExpiryAndOneTimeUse(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)

	wrongCodeInvite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:      wrongCodeInvite.Token,
		Code:       differentCode(wrongCodeInvite.Code),
		DeviceName: "wrong-code",
	}); err == nil || !strings.Contains(err.Error(), "verification code is incorrect") {
		t.Fatalf("wrong code error = %v, want verification failure", err)
	}
	storedWrongCodeInvite, err := svc.GetInvite(ctx, wrongCodeInvite.ID)
	if err != nil {
		t.Fatalf("GetInvite returned error: %v", err)
	}
	if storedWrongCodeInvite.Failures != 1 {
		t.Fatalf("Failures = %d, want 1", storedWrongCodeInvite.Failures)
	}

	expiringInvite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateInvite expiring returned error: %v", err)
	}
	svc.SetNowForTest(func() time.Time { return now.Add(2 * time.Minute) })
	if _, err := svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:      expiringInvite.Token,
		Code:       expiringInvite.Code,
		DeviceName: "late-device",
	}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired invite error = %v, want expired", err)
	}

	svc.SetNowForTest(func() time.Time { return now })
	oneTimeInvite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
	})
	if err != nil {
		t.Fatalf("CreateInvite one-time returned error: %v", err)
	}
	if _, err := svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:      oneTimeInvite.Token,
		Code:       oneTimeInvite.Code,
		DeviceName: "first-device",
	}); err != nil {
		t.Fatalf("first one-time join returned error: %v", err)
	}
	if _, err := svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:      oneTimeInvite.Token,
		Code:       oneTimeInvite.Code,
		DeviceName: "second-device",
	}); err == nil || !strings.Contains(err.Error(), "already been used") {
		t.Fatalf("second one-time join error = %v, want used invite", err)
	}
}

func TestServiceCreatesRelaySessionWithHashedJoinTokens(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	result, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
		TTL:            time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	if result.Session.ID == "" || result.Session.Status != RelaySessionPending {
		t.Fatalf("relay session = %+v, want pending session with id", result.Session)
	}
	if result.Session.PathType != RelayPathType {
		t.Fatalf("relay session path type = %q, want %q", result.Session.PathType, RelayPathType)
	}
	if result.SourceJoinToken == "" || result.TargetJoinToken == "" || result.SourceJoinToken == result.TargetJoinToken {
		t.Fatal("join tokens were not generated independently")
	}
	if got := result.Session.ExpiresAt.Sub(result.Session.CreatedAt); got != time.Minute {
		t.Fatalf("relay session TTL = %s, want 1m", got)
	}

	stored := store.relaySessions[result.Session.ID]
	if stored.SourceJoinTokenHash == "" || stored.TargetJoinTokenHash == "" {
		t.Fatalf("stored relay session = %+v, want hashed join credentials", stored)
	}
	if strings.Contains(stored.SourceJoinTokenHash, result.SourceJoinToken) ||
		strings.Contains(stored.TargetJoinTokenHash, result.TargetJoinToken) {
		t.Fatalf("stored relay session leaked plaintext join token: %+v", stored)
	}
	encoded, err := json.Marshal(result.Session)
	if err != nil {
		t.Fatalf("marshal relay session: %v", err)
	}
	if bytes.Contains(encoded, []byte(result.SourceJoinToken)) || bytes.Contains(encoded, []byte(result.TargetJoinToken)) {
		t.Fatal("relay session JSON leaked plaintext join token")
	}

	events, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	if !hasAuditEvent(events, AuditRelaySessionCreated) {
		t.Fatalf("audit events = %+v, want relay session created", events)
	}
	auditJSON, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal audit events: %v", err)
	}
	if bytes.Contains(auditJSON, []byte(result.SourceJoinToken)) || bytes.Contains(auditJSON, []byte(result.TargetJoinToken)) {
		t.Fatal("audit events leaked plaintext join token")
	}
}

func TestServiceRejectsUnauthorizedRelaySessionRequests(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)

	t.Run("source device is required", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		_, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		_, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !strings.Contains(err.Error(), "source_device_id is required") {
			t.Fatalf("CreateRelaySession error = %v, want source_device_id validation", err)
		}
	})

	t.Run("banned account is forbidden", func(t *testing.T) {
		store := NewMemoryStore()
		svc := NewService(store, WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		account.Status = AccountStatusBanned
		if _, err := store.UpdateAccount(ctx, account); err != nil {
			t.Fatalf("UpdateAccount returned error: %v", err)
		}
		_, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) {
			t.Fatalf("CreateRelaySession error = %v, want ErrForbidden", err)
		}
	})

	t.Run("target in another network is forbidden", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		otherNetwork, err := svc.CreateNetwork(ctx, CreateNetworkRequest{
			AccountID: account.ID,
			Name:      "Other",
		})
		if err != nil {
			t.Fatalf("CreateNetwork other returned error: %v", err)
		}
		source, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		target, _ := mustJoinTwoDevices(t, ctx, svc, account.ID, otherNetwork.ID)
		_, err = svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) {
			t.Fatalf("CreateRelaySession error = %v, want ErrForbidden", err)
		}
	})

	t.Run("revoked target is forbidden", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		if _, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: target.ID, Reason: "retired"}); err != nil {
			t.Fatalf("RevokeDevice returned error: %v", err)
		}
		_, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrRevoked) {
			t.Fatalf("CreateRelaySession error = %v, want ErrRevoked", err)
		}
	})
}

func TestServiceRelayPolicyQuotaDeniesNewSessionsAndRecordsRisk(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(),
		WithNow(func() time.Time { return now }),
		WithAccountPolicies("tiny-test", AccountPolicy{
			Name:                   "tiny-test",
			RelayBytesQuota:        64,
			MaxActiveRelaySessions: 4,
			MaxRelaySessionsPerDay: 8,
		}),
	)
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)

	status, err := svc.GetAccountPolicyStatus(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountPolicyStatus returned error: %v", err)
	}
	if status.Policy.Name != "tiny-test" || status.Policy.RelayBytesQuota != 64 {
		t.Fatalf("policy status = %+v, want tiny-test quota", status)
	}
	if status.RelayBytesUsed != 0 || status.RelayBytesRemaining != 64 {
		t.Fatalf("policy usage = %+v, want empty quota usage", status)
	}

	result, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	if _, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID:     result.Session.ID,
		RelayBytesIn:  40,
		RelayBytesOut: 40,
	}); err != nil {
		t.Fatalf("CloseRelaySession returned error: %v", err)
	}

	status, err = svc.GetAccountPolicyStatus(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountPolicyStatus after usage returned error: %v", err)
	}
	if status.RelayBytesUsed < 64 || status.RelayBytesRemaining != 0 {
		t.Fatalf("policy usage = %+v, want quota exhausted", status)
	}

	_, err = svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err == nil || !errors.Is(err, ErrQuotaExceeded) || !strings.Contains(err.Error(), "relay byte quota exceeded") {
		t.Fatalf("CreateRelaySession quota error = %v, want clear ErrQuotaExceeded", err)
	}

	events, err := svc.ListRiskEvents(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	if !hasRiskEvent(events, RiskQuotaExceeded) || !hasRiskEvent(events, RiskRelaySessionDenied) {
		t.Fatalf("risk events = %+v, want quota_exceeded and relay_session_denied", events)
	}
	assertNoSensitiveJSON(t, events)
}

func TestServiceRelayUsageReminderThresholds(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		used      int64
		wantLevel string
		wantTitle string
	}{
		{name: "below eighty percent", used: 79},
		{name: "approaching limit", used: 80, wantLevel: "approaching", wantTitle: "接近中继流量上限"},
		{name: "at limit", used: 100, wantLevel: "reached", wantTitle: "中继流量已达上限"},
		{name: "well over limit", used: 120, wantLevel: "overage", wantTitle: "中继流量明显超额"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(NewMemoryStore(),
				WithAccountPolicies("tiny-reminder", AccountPolicy{
					Name:                   "tiny-reminder",
					RelayBytesQuota:        100,
					MaxActiveRelaySessions: 4,
					MaxRelaySessionsPerDay: 8,
				}),
			)
			account, network := mustAccountAndNetwork(t, ctx, svc)
			if tc.used > 0 {
				if _, err := svc.RecordRelayUsage(ctx, RecordRelayUsageRequest{
					AccountID: account.ID,
					NetworkID: network.ID,
					BytesIn:   tc.used,
				}); err != nil {
					t.Fatalf("RecordRelayUsage returned error: %v", err)
				}
			}
			summary, err := svc.GetAccountManagementSummary(ctx, account.ID)
			if err != nil {
				t.Fatalf("GetAccountManagementSummary returned error: %v", err)
			}
			reminder := summary.RelayUsageReminder
			if tc.wantLevel == "" {
				if reminder != nil {
					t.Fatalf("relay usage reminder = %+v, want nil below 80%%", reminder)
				}
				return
			}
			if reminder == nil {
				t.Fatalf("relay usage reminder is nil, want %s", tc.wantLevel)
			}
			if reminder.Level != tc.wantLevel || reminder.Title != tc.wantTitle {
				t.Fatalf("relay usage reminder = %+v, want level %q title %q", reminder, tc.wantLevel, tc.wantTitle)
			}
			if reminder.UsedBytes != tc.used || reminder.LimitBytes != 100 || reminder.UsagePercent != int(tc.used) {
				t.Fatalf("relay usage numbers = %+v, want used %d limit 100 percent %d", reminder, tc.used, tc.used)
			}
			for _, want := range []string{
				"当前连接正在走中继",
				"已用",
				"上限",
				"不是普通网络错误",
				"优先尝试直连",
				"自建中继",
				"管理员中继",
				"升级入口",
			} {
				if !strings.Contains(reminder.Message+" "+reminder.Impact+" "+reminder.Recommendation, want) {
					t.Fatalf("relay usage reminder = %+v, missing plain-language text %q", reminder, want)
				}
			}
			if summary.Policy.RelayUsageReminder == nil || summary.Policy.RelayUsageReminder.Level != tc.wantLevel {
				t.Fatalf("summary policy reminder = %+v, want same %s reminder", summary.Policy.RelayUsageReminder, tc.wantLevel)
			}
		})
	}
}

func TestServiceAccountEnforcementClosesPendingAndActiveRelaySessions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	devices := mustJoinDevices(t, ctx, svc, account.ID, network.ID, 4)

	activeResult, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[0].ID,
		TargetDeviceID: devices[1].ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession active returned error: %v", err)
	}
	if _, err := svc.ActivateRelaySession(ctx, ActivateRelaySessionRequest{SessionID: activeResult.Session.ID}); err != nil {
		t.Fatalf("ActivateRelaySession returned error: %v", err)
	}
	pendingResult, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[2].ID,
		TargetDeviceID: devices[3].ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession pending returned error: %v", err)
	}

	frozen, err := svc.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "risk review"})
	if err != nil {
		t.Fatalf("FreezeAccount returned error: %v", err)
	}
	if frozen.Status != AccountStatusFrozen {
		t.Fatalf("frozen account = %+v, want frozen", frozen)
	}

	for _, sessionID := range []string{activeResult.Session.ID, pendingResult.Session.ID} {
		session, err := svc.GetRelaySession(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetRelaySession %s returned error: %v", sessionID, err)
		}
		if session.Status != RelaySessionClosed || session.EndedAt == nil {
			t.Fatalf("session after freeze = %+v, want closed with end time", session)
		}
		if !strings.Contains(session.Error, "account frozen") {
			t.Fatalf("session error = %q, want account frozen reason", session.Error)
		}
	}
	if _, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[0].ID,
		TargetDeviceID: devices[1].ID,
	}); err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("CreateRelaySession after freeze error = %v, want frozen ErrForbidden", err)
	}

	events, err := svc.ListRiskEvents(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	if !hasRiskEvent(events, RiskAccountEnforcementApplied) || countRiskEvents(events, RiskRelaySessionRevoked) != 2 {
		t.Fatalf("risk events = %+v, want account enforcement and two relay_session_revoked events", events)
	}
	assertNoSensitiveJSON(t, events)
}

func TestServiceDeviceRevokeClosesOnlyRelatedRelaySessions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	devices := mustJoinDevices(t, ctx, svc, account.ID, network.ID, 4)

	revokedDeviceResult, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[0].ID,
		TargetDeviceID: devices[1].ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession revoked-device returned error: %v", err)
	}
	if _, err := svc.ActivateRelaySession(ctx, ActivateRelaySessionRequest{SessionID: revokedDeviceResult.Session.ID}); err != nil {
		t.Fatalf("ActivateRelaySession revoked-device returned error: %v", err)
	}
	unrelatedResult, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[2].ID,
		TargetDeviceID: devices[3].ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession unrelated returned error: %v", err)
	}
	if _, err := svc.ActivateRelaySession(ctx, ActivateRelaySessionRequest{SessionID: unrelatedResult.Session.ID}); err != nil {
		t.Fatalf("ActivateRelaySession unrelated returned error: %v", err)
	}

	if _, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: devices[0].ID, Reason: "lost laptop"}); err != nil {
		t.Fatalf("RevokeDevice returned error: %v", err)
	}

	closed, err := svc.GetRelaySession(ctx, revokedDeviceResult.Session.ID)
	if err != nil {
		t.Fatalf("GetRelaySession revoked-device returned error: %v", err)
	}
	if closed.Status != RelaySessionClosed || closed.EndedAt == nil || !strings.Contains(closed.Error, "device revoked") {
		t.Fatalf("revoked-device session = %+v, want closed by device revoke", closed)
	}
	stillActive, err := svc.GetRelaySession(ctx, unrelatedResult.Session.ID)
	if err != nil {
		t.Fatalf("GetRelaySession unrelated returned error: %v", err)
	}
	if stillActive.Status != RelaySessionActive {
		t.Fatalf("unrelated session = %+v, want still active", stillActive)
	}
	if _, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: devices[0].ID,
		TargetDeviceID: devices[1].ID,
	}); err == nil || !errors.Is(err, ErrRevoked) {
		t.Fatalf("CreateRelaySession with revoked device error = %v, want ErrRevoked", err)
	}

	events, err := svc.ListRiskEvents(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	if countRiskEvents(events, RiskRelaySessionRevoked) != 1 {
		t.Fatalf("risk events = %+v, want one relay_session_revoked event", events)
	}
	assertNoSensitiveJSON(t, events)
}

func TestServiceAccountFreezeBanBlocksControlPlaneJoinHeartbeatAndRelay(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	invite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   4,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	source, err := svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:      invite.Token,
		Code:       invite.Code,
		DeviceName: "source-pc",
	})
	if err != nil {
		t.Fatalf("JoinDevice source returned error: %v", err)
	}
	target, err := svc.JoinDevice(ctx, JoinDeviceRequest{
		Token:      invite.Token,
		Code:       invite.Code,
		DeviceName: "target-pc",
	})
	if err != nil {
		t.Fatalf("JoinDevice target returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: source.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice source returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice target returned error: %v", err)
	}

	if _, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: source.ID, Reason: "lost"}); err != nil {
		t.Fatalf("RevokeDevice returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: source.ID, Status: DeviceStatusOnline}); err == nil || !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked source heartbeat error = %v, want ErrRevoked", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("target heartbeat after source revoke returned error: %v", err)
	}

	frozen, err := svc.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "risk review"})
	if err != nil {
		t.Fatalf("FreezeAccount returned error: %v", err)
	}
	if frozen.Status != AccountStatusFrozen || frozen.FrozenAt == nil {
		t.Fatalf("frozen account = %+v, want frozen status", frozen)
	}
	for name, run := range map[string]func() error{
		"create network": func() error {
			_, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "Blocked"})
			return err
		},
		"create invite": func() error {
			_, err := svc.CreateInvite(ctx, CreateInviteRequest{AccountID: account.ID, NetworkID: network.ID})
			return err
		},
		"join device": func() error {
			_, err := svc.JoinDevice(ctx, JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "blocked"})
			return err
		},
		"heartbeat": func() error {
			_, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOnline})
			return err
		},
		"relay session": func() error {
			_, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
				AccountID:      account.ID,
				NetworkID:      network.ID,
				SourceDeviceID: target.ID,
				TargetDeviceID: target.ID,
			})
			return err
		},
	} {
		if err := run(); err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "frozen") {
			t.Fatalf("%s error = %v, want frozen ErrForbidden", name, err)
		}
	}

	unfrozen, err := svc.UnfreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "cleared"})
	if err != nil {
		t.Fatalf("UnfreezeAccount returned error: %v", err)
	}
	if unfrozen.Status != AccountStatusActive || unfrozen.FrozenAt != nil {
		t.Fatalf("unfrozen account = %+v, want active without frozen time", unfrozen)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice after unfreeze returned error: %v", err)
	}

	banned, err := svc.BanAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "abuse"})
	if err != nil {
		t.Fatalf("BanAccount returned error: %v", err)
	}
	if banned.Status != AccountStatusBanned || banned.BannedAt == nil {
		t.Fatalf("banned account = %+v, want banned status", banned)
	}
	if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOnline}); err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("HeartbeatDevice banned error = %v, want banned ErrForbidden", err)
	}

	events, err := svc.ListRiskEvents(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	if !hasRiskEvent(events, RiskAccountFrozen) || !hasRiskEvent(events, RiskAccountBanned) || !hasRiskEvent(events, RiskRelaySessionDenied) {
		t.Fatalf("risk events = %+v, want account_frozen, account_banned and relay denial", events)
	}
	assertNoSensitiveJSON(t, events)
}

func TestInviteAbuseRiskEventDoesNotLeakInviteSecrets(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	invite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	for i := 0; i < defaultInviteMaxFailures; i++ {
		_, _ = svc.JoinDevice(ctx, JoinDeviceRequest{
			Token:      invite.Token,
			Code:       differentCode(invite.Code),
			DeviceName: "wrong-code",
			RemoteAddr: "203.0.113.10:443",
		})
	}

	events, err := svc.ListRiskEvents(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	if !hasRiskEvent(events, RiskInviteAbuseSuspected) {
		t.Fatalf("risk events = %+v, want invite_abuse_suspected", events)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal risk events: %v", err)
	}
	if bytes.Contains(encoded, []byte(invite.Token)) || bytes.Contains(encoded, []byte(invite.Code)) {
		t.Fatalf("risk events leaked invite secret: %s", encoded)
	}
	assertNoSensitiveJSON(t, events)
}

func TestServiceClosesRelaySessionAndRecordsUsage(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, WithNow(func() time.Time { return now }))
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
	result, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}

	svc.SetNowForTest(func() time.Time { return now.Add(30 * time.Second) })
	closed, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID:     result.Session.ID,
		RelayBytesIn:  128,
		RelayBytesOut: 256,
	})
	if err != nil {
		t.Fatalf("CloseRelaySession returned error: %v", err)
	}
	if closed.Status != RelaySessionClosed || closed.EndedAt == nil {
		t.Fatalf("closed relay session = %+v, want closed with end time", closed)
	}
	if closed.RelayBytesIn != 128 || closed.RelayBytesOut != 256 {
		t.Fatalf("closed relay bytes = in:%d out:%d, want 128/256", closed.RelayBytesIn, closed.RelayBytesOut)
	}
	if len(store.connectionLogs) != 1 {
		t.Fatalf("connection logs = %+v, want one relay connection log", store.connectionLogs)
	}
	for _, log := range store.connectionLogs {
		if log.SessionID != result.Session.ID || log.PathType != RelayPathType || log.SourceDeviceID != source.ID || log.TargetDeviceID != target.ID {
			t.Fatalf("connection log = %+v, want relay source/target metadata", log)
		}
		if log.RelayBytesIn != 128 || log.RelayBytesOut != 256 || log.EndedAt == nil {
			t.Fatalf("connection log = %+v, want bytes and end time", log)
		}
	}
	if len(store.relayUsage) != 2 {
		t.Fatalf("relay usage = %+v, want usage rows for both endpoints", store.relayUsage)
	}
	got, err := svc.GetRelaySession(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("GetRelaySession returned error: %v", err)
	}
	if got.Status != RelaySessionClosed {
		t.Fatalf("GetRelaySession = %+v, want closed session", got)
	}
	logs, err := svc.ListRelaySessionConnectionLogs(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("ListRelaySessionConnectionLogs returned error: %v", err)
	}
	if len(logs) != 1 || logs[0].SessionID != result.Session.ID {
		t.Fatalf("session connection logs = %+v, want log for relay session", logs)
	}
	usage, err := svc.ListRelaySessionUsage(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("ListRelaySessionUsage returned error: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("session relay usage = %+v, want usage rows for both endpoints", usage)
	}
}

func TestCloudHubAPIFlow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	health := getJSON[map[string]any](t, server.URL+"/healthz")
	if health["ok"] != true {
		t.Fatalf("health = %+v, want ok true", health)
	}

	accountResp := postJSON[struct {
		OK      bool    `json:"ok"`
		Account Account `json:"account"`
	}](t, server.URL+"/api/accounts", map[string]string{
		"email":        "owner@example.com",
		"display_name": "Owner",
	})
	if !accountResp.OK || accountResp.Account.ID == "" {
		t.Fatalf("account response = %+v", accountResp)
	}

	networkResp := postJSON[struct {
		OK      bool    `json:"ok"`
		Network Network `json:"network"`
	}](t, server.URL+"/api/networks", map[string]string{
		"account_id": accountResp.Account.ID,
		"name":       "Home",
	})
	if !networkResp.OK || networkResp.Network.ID == "" {
		t.Fatalf("network response = %+v", networkResp)
	}

	inviteResp := postJSON[struct {
		OK     bool         `json:"ok"`
		Invite InviteResult `json:"invite"`
	}](t, server.URL+"/api/invites", map[string]string{
		"account_id": accountResp.Account.ID,
		"network_id": networkResp.Network.ID,
	})
	if !inviteResp.OK || inviteResp.Invite.Token == "" || inviteResp.Invite.Code == "" {
		t.Fatalf("invite response = %+v", inviteResp)
	}

	joinResp := postJSON[struct {
		OK     bool   `json:"ok"`
		Device Device `json:"device"`
	}](t, server.URL+"/api/devices/join", map[string]string{
		"token":       inviteResp.Invite.Token,
		"code":        inviteResp.Invite.Code,
		"device_name": "office-pc",
		"fingerprint": "SHA256:AA:BB",
	})
	if !joinResp.OK || joinResp.Device.ID == "" {
		t.Fatalf("join response = %+v", joinResp)
	}

	heartbeatResp := postJSON[struct {
		OK     bool   `json:"ok"`
		Device Device `json:"device"`
	}](t, server.URL+"/api/devices/heartbeat", map[string]string{
		"device_id": joinResp.Device.ID,
		"status":    string(DeviceStatusOnline),
	})
	if !heartbeatResp.OK || heartbeatResp.Device.Status != DeviceStatusOnline {
		t.Fatalf("heartbeat response = %+v", heartbeatResp)
	}

	devicesResp := getJSON[struct {
		OK      bool     `json:"ok"`
		Devices []Device `json:"devices"`
	}](t, server.URL+"/api/networks/"+networkResp.Network.ID+"/devices")
	if !devicesResp.OK || len(devicesResp.Devices) != 1 || devicesResp.Devices[0].ID != joinResp.Device.ID {
		t.Fatalf("devices response = %+v, want joined device", devicesResp)
	}

	revokeResp := postJSON[struct {
		OK     bool   `json:"ok"`
		Device Device `json:"device"`
	}](t, server.URL+"/api/devices/revoke", map[string]string{
		"device_id": joinResp.Device.ID,
		"reason":    "lost laptop",
	})
	if !revokeResp.OK || revokeResp.Device.RevokedAt == nil {
		t.Fatalf("revoke response = %+v", revokeResp)
	}

	errorResp, status := postJSONStatus[struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}](t, server.URL+"/api/devices/heartbeat", map[string]string{
		"device_id": joinResp.Device.ID,
		"status":    string(DeviceStatusOnline),
	})
	if status != http.StatusForbidden || errorResp.OK || !strings.Contains(errorResp.Error, "revoked") {
		t.Fatalf("heartbeat after revoke status=%d body=%+v, want forbidden revoked error", status, errorResp)
	}

	events, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	if !hasAuditEvent(events, AuditDeviceRevoked) {
		t.Fatalf("audit events = %+v, want device revoked", events)
	}
}

func TestCloudHubRelayAPIFlow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	accountResp := postJSON[struct {
		OK      bool    `json:"ok"`
		Account Account `json:"account"`
	}](t, server.URL+"/api/accounts", map[string]string{
		"email": "relay-owner@example.com",
	})
	networkResp := postJSON[struct {
		OK      bool    `json:"ok"`
		Network Network `json:"network"`
	}](t, server.URL+"/api/networks", map[string]string{
		"account_id": accountResp.Account.ID,
		"name":       "RelayNet",
	})
	inviteResp := postJSON[struct {
		OK     bool         `json:"ok"`
		Invite InviteResult `json:"invite"`
	}](t, server.URL+"/api/invites", map[string]any{
		"account_id": accountResp.Account.ID,
		"network_id": networkResp.Network.ID,
		"max_uses":   2,
		"one_time":   false,
	})
	join := func(name string) Device {
		t.Helper()
		resp := postJSON[struct {
			OK     bool   `json:"ok"`
			Device Device `json:"device"`
		}](t, server.URL+"/api/devices/join", map[string]string{
			"token":       inviteResp.Invite.Token,
			"code":        inviteResp.Invite.Code,
			"device_name": name,
		})
		return resp.Device
	}
	source := join("source-pc")
	target := join("target-pc")

	for _, device := range []Device{source, target} {
		heartbeatResp := postJSON[struct {
			OK     bool   `json:"ok"`
			Device Device `json:"device"`
		}](t, server.URL+"/api/devices/heartbeat", map[string]string{
			"device_id": device.ID,
			"status":    string(DeviceStatusOnline),
		})
		if !heartbeatResp.OK || heartbeatResp.Device.Status != DeviceStatusOnline {
			t.Fatalf("heartbeat response = %+v", heartbeatResp)
		}
	}

	createResp := postJSON[struct {
		OK      bool               `json:"ok"`
		Session RelaySessionResult `json:"session"`
	}](t, server.URL+"/api/relay/sessions", map[string]any{
		"account_id":       accountResp.Account.ID,
		"network_id":       networkResp.Network.ID,
		"source_device_id": source.ID,
		"target_device_id": target.ID,
		"ttl_seconds":      60,
	})
	if !createResp.OK || createResp.Session.Session.ID == "" ||
		createResp.Session.SourceJoinToken == "" || createResp.Session.TargetJoinToken == "" {
		t.Fatal("create relay session response did not include session and join tokens")
	}

	getResp := getJSON[struct {
		OK      bool         `json:"ok"`
		Session RelaySession `json:"session"`
	}](t, server.URL+"/api/relay/sessions/"+createResp.Session.Session.ID)
	if !getResp.OK || getResp.Session.ID != createResp.Session.Session.ID || getResp.Session.Status != RelaySessionPending {
		t.Fatalf("get relay session response = %+v, want pending session", getResp)
	}

	usageResp := postJSON[struct {
		OK      bool         `json:"ok"`
		Session RelaySession `json:"session"`
	}](t, server.URL+"/api/relay/sessions/"+createResp.Session.Session.ID+"/usage", map[string]int64{
		"relay_bytes_in":  12,
		"relay_bytes_out": 34,
	})
	if !usageResp.OK || usageResp.Session.RelayBytesIn != 12 || usageResp.Session.RelayBytesOut != 34 {
		t.Fatalf("usage response = %+v, want accumulated relay bytes", usageResp)
	}

	closeResp := postJSON[struct {
		OK      bool         `json:"ok"`
		Session RelaySession `json:"session"`
	}](t, server.URL+"/api/relay/sessions/"+createResp.Session.Session.ID+"/close", map[string]int64{
		"relay_bytes_in":  56,
		"relay_bytes_out": 78,
	})
	if !closeResp.OK || closeResp.Session.Status != RelaySessionClosed || closeResp.Session.EndedAt == nil {
		t.Fatalf("close response = %+v, want closed relay session", closeResp)
	}

	events, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	if !hasAuditEvent(events, AuditRelaySessionClosed) {
		t.Fatalf("audit events = %+v, want relay session closed", events)
	}

	logsResp := getJSON[struct {
		OK   bool            `json:"ok"`
		Logs []ConnectionLog `json:"connection_logs"`
	}](t, server.URL+"/api/relay/sessions/"+createResp.Session.Session.ID+"/connection-logs")
	if !logsResp.OK || len(logsResp.Logs) != 1 || logsResp.Logs[0].SessionID != createResp.Session.Session.ID {
		t.Fatalf("connection logs response = %+v, want one session log", logsResp)
	}

	usageRowsResp := getJSON[struct {
		OK    bool         `json:"ok"`
		Usage []RelayUsage `json:"usage"`
	}](t, server.URL+"/api/relay/sessions/"+createResp.Session.Session.ID+"/usage")
	if !usageRowsResp.OK || len(usageRowsResp.Usage) != 4 {
		t.Fatalf("relay usage response = %+v, want four endpoint usage rows from usage and close", usageRowsResp)
	}
}

func TestCloudHubRelayServerHooksEndpointAndHealth(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	var created RelaySessionResult
	var closed RelaySession
	server := httptest.NewServer(NewServer(svc,
		WithRelayEndpoint("127.0.0.1:18082"),
		WithRelayStatusProvider(func() RelayStatus {
			return RelayStatus{Enabled: true, Listen: "127.0.0.1:18082"}
		}),
		WithRelaySessionCreatedHook(func(ctx context.Context, result RelaySessionResult) error {
			created = result
			return nil
		}),
		WithRelaySessionClosedHook(func(ctx context.Context, session RelaySession) error {
			closed = session
			return nil
		}),
	))
	defer server.Close()

	health := getJSON[struct {
		OK    bool        `json:"ok"`
		Relay RelayStatus `json:"relay"`
	}](t, server.URL+"/healthz")
	if !health.OK || !health.Relay.Enabled || health.Relay.Listen != "127.0.0.1:18082" {
		t.Fatalf("health = %+v, want enabled relay status", health)
	}

	account, network := mustAccountAndNetwork(t, context.Background(), svc)
	source, target := mustJoinTwoDevices(t, context.Background(), svc, account.ID, network.ID)
	createResp := postJSON[struct {
		OK      bool               `json:"ok"`
		Session RelaySessionResult `json:"session"`
	}](t, server.URL+"/api/relay/sessions", map[string]any{
		"account_id":       account.ID,
		"network_id":       network.ID,
		"source_device_id": source.ID,
		"target_device_id": target.ID,
	})
	if !createResp.OK || createResp.Session.RelayEndpoint != "127.0.0.1:18082" {
		t.Fatalf("create relay response = %+v, want relay endpoint", createResp)
	}
	if created.Session.ID != createResp.Session.Session.ID || created.SourceJoinToken == "" || created.TargetJoinToken == "" {
		t.Fatalf("created hook result = %+v, want same session and one-time join tokens", created)
	}

	closeResp := postJSON[struct {
		OK      bool         `json:"ok"`
		Session RelaySession `json:"session"`
	}](t, server.URL+"/api/relay/sessions/"+createResp.Session.Session.ID+"/close", map[string]int64{
		"relay_bytes_in":  1,
		"relay_bytes_out": 2,
	})
	if !closeResp.OK || closed.ID != createResp.Session.Session.ID || closed.Status != RelaySessionClosed {
		t.Fatalf("close hook session = %+v close response = %+v, want closed session hook", closed, closeResp)
	}
}

func TestCloudHubPolicyAccountStatusAndRiskAPIFlow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(),
		WithNow(func() time.Time { return now }),
		WithAccountPolicies("tiny-api", AccountPolicy{
			Name:                   "tiny-api",
			RelayBytesQuota:        32,
			MaxActiveRelaySessions: 4,
			MaxRelaySessionsPerDay: 8,
		}),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	accountResp := postJSON[struct {
		OK      bool    `json:"ok"`
		Account Account `json:"account"`
	}](t, server.URL+"/api/accounts", map[string]string{
		"email": "policy-api@example.com",
	})
	policyResp := getJSON[struct {
		OK     bool                `json:"ok"`
		Status AccountPolicyStatus `json:"status"`
	}](t, server.URL+"/api/accounts/"+accountResp.Account.ID+"/policy")
	if !policyResp.OK || policyResp.Status.Policy.Name != "tiny-api" || policyResp.Status.RelayBytesRemaining != 32 {
		t.Fatalf("policy response = %+v, want tiny-api remaining quota", policyResp)
	}

	networkResp := postJSON[struct {
		OK      bool    `json:"ok"`
		Network Network `json:"network"`
	}](t, server.URL+"/api/networks", map[string]string{
		"account_id": accountResp.Account.ID,
		"name":       "RelayNet",
	})
	inviteResp := postJSON[struct {
		OK     bool         `json:"ok"`
		Invite InviteResult `json:"invite"`
	}](t, server.URL+"/api/invites", map[string]any{
		"account_id": accountResp.Account.ID,
		"network_id": networkResp.Network.ID,
		"max_uses":   3,
		"one_time":   false,
	})
	join := func(name string) Device {
		t.Helper()
		resp := postJSON[struct {
			OK     bool   `json:"ok"`
			Device Device `json:"device"`
		}](t, server.URL+"/api/devices/join", map[string]string{
			"token":       inviteResp.Invite.Token,
			"code":        inviteResp.Invite.Code,
			"device_name": name,
		})
		return resp.Device
	}
	source := join("source-pc")
	target := join("target-pc")
	createResp := postJSON[struct {
		OK      bool               `json:"ok"`
		Session RelaySessionResult `json:"session"`
	}](t, server.URL+"/api/relay/sessions", map[string]any{
		"account_id":       accountResp.Account.ID,
		"network_id":       networkResp.Network.ID,
		"source_device_id": source.ID,
		"target_device_id": target.ID,
	})
	_ = postJSON[struct {
		OK      bool         `json:"ok"`
		Session RelaySession `json:"session"`
	}](t, server.URL+"/api/relay/sessions/"+createResp.Session.Session.ID+"/close", map[string]int64{
		"relay_bytes_in":  20,
		"relay_bytes_out": 20,
	})
	errorResp, statusCode := postJSONStatus[struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}](t, server.URL+"/api/relay/sessions", map[string]any{
		"account_id":       accountResp.Account.ID,
		"network_id":       networkResp.Network.ID,
		"source_device_id": source.ID,
		"target_device_id": target.ID,
	})
	if statusCode != http.StatusTooManyRequests || errorResp.OK || !strings.Contains(errorResp.Error, "relay byte quota exceeded") {
		t.Fatalf("quota response status=%d body=%+v, want clear quota error", statusCode, errorResp)
	}

	freezeResp := postJSON[struct {
		OK      bool    `json:"ok"`
		Account Account `json:"account"`
	}](t, server.URL+"/api/accounts/"+accountResp.Account.ID+"/freeze", map[string]string{
		"reason": "risk review",
	})
	if !freezeResp.OK || freezeResp.Account.Status != AccountStatusFrozen {
		t.Fatalf("freeze response = %+v, want frozen", freezeResp)
	}
	heartbeatError, statusCode := postJSONStatus[struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}](t, server.URL+"/api/devices/heartbeat", map[string]string{
		"device_id": target.ID,
		"status":    string(DeviceStatusOnline),
	})
	if statusCode != http.StatusForbidden || !strings.Contains(heartbeatError.Error, "frozen") {
		t.Fatalf("heartbeat frozen status=%d body=%+v, want frozen forbidden", statusCode, heartbeatError)
	}
	eventsResp := getJSON[struct {
		OK     bool        `json:"ok"`
		Events []RiskEvent `json:"events"`
	}](t, server.URL+"/api/risk-events?account_id="+accountResp.Account.ID)
	if !eventsResp.OK || !hasRiskEvent(eventsResp.Events, RiskQuotaExceeded) || !hasRiskEvent(eventsResp.Events, RiskAccountFrozen) {
		t.Fatalf("risk events response = %+v, want quota and frozen events", eventsResp)
	}
	assertNoSensitiveJSON(t, eventsResp.Events)

	if _, err := svc.ListAuditEvents(ctx); err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
}

func TestCloudHubAccountManagementSummaryAPIFlow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
	result, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	if _, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID:     result.Session.ID,
		RelayBytesIn:  21,
		RelayBytesOut: 34,
		Error:         "normal close",
	}); err != nil {
		t.Fatalf("CloseRelaySession returned error: %v", err)
	}
	if _, err := svc.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "risk review"}); err != nil {
		t.Fatalf("FreezeAccount returned error: %v", err)
	}

	summaryResp := getJSON[struct {
		OK      bool                     `json:"ok"`
		Summary AccountManagementSummary `json:"summary"`
	}](t, server.URL+"/api/accounts/"+account.ID+"/summary")
	if !summaryResp.OK {
		t.Fatalf("summary response = %+v, want ok", summaryResp)
	}
	summary := summaryResp.Summary
	if summary.Account.ID != account.ID || summary.Policy.AccountID != account.ID {
		t.Fatalf("summary account/policy = %+v, want account %s", summary, account.ID)
	}
	if len(summary.RiskEvents) == 0 || !hasRiskEvent(summary.RiskEvents, RiskAccountFrozen) {
		t.Fatalf("summary risk events = %+v, want account_frozen", summary.RiskEvents)
	}
	if len(summary.AuditEvents) == 0 || !hasAuditEvent(summary.AuditEvents, AuditRelaySessionClosed) {
		t.Fatalf("summary audit events = %+v, want relay_session_closed", summary.AuditEvents)
	}
	if len(summary.RelayUsage) != 2 || len(summary.ConnectionLogs) != 1 {
		t.Fatalf("summary usage/logs = %d/%d, want 2 usage rows and 1 connection log", len(summary.RelayUsage), len(summary.ConnectionLogs))
	}
	if summary.RelaySessions.Total != 1 || summary.RelaySessions.Closed != 1 || len(summary.RelaySessions.Sessions) != 1 {
		t.Fatalf("summary relay sessions = %+v, want one closed session", summary.RelaySessions)
	}
	assertNoSensitiveJSON(t, summary)
}

func mustAccountAndNetwork(t *testing.T, ctx context.Context, svc *Service) (Account, Network) {
	t.Helper()
	account, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "owner@example.com",
		DisplayName: "Owner",
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{
		AccountID: account.ID,
		Name:      "Home",
	})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	return account, network
}

func mustJoinTwoDevices(t *testing.T, ctx context.Context, svc *Service, accountID, networkID string) (Device, Device) {
	t.Helper()
	devices := mustJoinDevices(t, ctx, svc, accountID, networkID, 2)
	return devices[0], devices[1]
}

func mustJoinDevices(t *testing.T, ctx context.Context, svc *Service, accountID, networkID string, count int) []Device {
	t.Helper()
	invite, err := svc.CreateInvite(ctx, CreateInviteRequest{
		AccountID: accountID,
		NetworkID: networkID,
		MaxUses:   count,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	devices := make([]Device, 0, count)
	for i := 0; i < count; i++ {
		device, err := svc.JoinDevice(ctx, JoinDeviceRequest{
			Token:      invite.Token,
			Code:       invite.Code,
			DeviceName: fmt.Sprintf("device-%d", i+1),
		})
		if err != nil {
			t.Fatalf("JoinDevice %d returned error: %v", i+1, err)
		}
		devices = append(devices, device)
	}
	return devices
}

func hasAuditEvent(events []AuditEvent, event string) bool {
	for _, got := range events {
		if got.Event == event {
			return true
		}
	}
	return false
}

func hasRiskEvent(events []RiskEvent, kind RiskEventKind) bool {
	for _, got := range events {
		if got.Kind == kind {
			return true
		}
	}
	return false
}

func countRiskEvents(events []RiskEvent, kind RiskEventKind) int {
	var count int
	for _, got := range events {
		if got.Kind == kind {
			count++
		}
	}
	return count
}

func assertNoSensitiveJSON(t *testing.T, v any) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"private_key", "private_key_pem", "clipboard", "rdp_content", "file_content", "join_token", "invite_token", "token_hash", " code", "code_hash"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("JSON leaks forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func differentCode(code string) string {
	if code != "000000" {
		return "000000"
	}
	return "000001"
}

func postJSON[T any](t *testing.T, url string, body any) T {
	t.Helper()
	got, status := postJSONStatus[T](t, url, body)
	if status != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", url, status)
	}
	return got
}

func postJSONStatus[T any](t *testing.T, url string, body any) (T, int) {
	t.Helper()
	var zero T
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&zero); err != nil {
		t.Fatalf("decode response from %s: %v", url, err)
	}
	return zero, resp.StatusCode
}

func getJSON[T any](t *testing.T, url string) T {
	t.Helper()
	var got T
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response from %s: %v", url, err)
	}
	return got
}
