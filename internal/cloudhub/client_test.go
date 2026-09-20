package cloudhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientFlowAgainstHTTPServer(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()

	client := &Client{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    5 * time.Second,
	}

	if err := client.Health(ctx); err != nil {
		t.Fatalf("Health returned error: %v", err)
	}

	account, err := client.CreateAccount(ctx, CreateAccountRequest{
		Email:       "owner@example.com",
		DisplayName: "Owner",
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	if account.ID == "" || account.Email != "owner@example.com" {
		t.Fatalf("account = %+v, want id and email", account)
	}

	network, err := client.CreateNetwork(ctx, CreateNetworkRequest{
		AccountID: account.ID,
		Name:      "Home",
	})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	if network.ID == "" || network.AccountID != account.ID {
		t.Fatalf("network = %+v, want account binding", network)
	}

	invite, err := client.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		TTL:       time.Minute,
		MaxUses:   2,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if invite.Token == "" || invite.Code == "" || invite.MaxUses != 2 || invite.OneTime {
		t.Fatalf("invite = %+v, want reusable invite with token and code", invite)
	}
	if got := invite.ExpiresAt.Sub(invite.CreatedAt); got != time.Minute {
		t.Fatalf("invite TTL = %s, want 1m", got)
	}

	device, err := client.JoinDevice(ctx, JoinDeviceRequest{
		Token:       invite.Token,
		Code:        invite.Code,
		DeviceName:  "office-pc",
		Fingerprint: "SHA256:AA:BB",
	})
	if err != nil {
		t.Fatalf("JoinDevice returned error: %v", err)
	}
	if device.ID == "" || device.NetworkID != network.ID || device.AccountID != account.ID {
		t.Fatalf("device = %+v, want account/network binding", device)
	}

	heartbeat, err := client.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: device.ID,
		Status:   DeviceStatusOnline,
	})
	if err != nil {
		t.Fatalf("HeartbeatDevice returned error: %v", err)
	}
	if heartbeat.Status != DeviceStatusOnline || heartbeat.LastSeen == nil {
		t.Fatalf("heartbeat device = %+v, want online with last seen", heartbeat)
	}

	devices, err := client.ListNetworkDevices(ctx, network.ID)
	if err != nil {
		t.Fatalf("ListNetworkDevices returned error: %v", err)
	}
	if len(devices) != 1 || devices[0].ID != device.ID {
		t.Fatalf("devices = %+v, want joined device", devices)
	}

	secondInvite, err := client.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   2,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite for relay peer returned error: %v", err)
	}
	target, err := client.JoinDevice(ctx, JoinDeviceRequest{
		Token:      secondInvite.Token,
		Code:       secondInvite.Code,
		DeviceName: "relay-target",
	})
	if err != nil {
		t.Fatalf("JoinDevice target returned error: %v", err)
	}
	relayResult, err := client.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: device.ID,
		TargetDeviceID: target.ID,
		TTL:            time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	if relayResult.Session.ID == "" || relayResult.SourceJoinToken == "" || relayResult.TargetJoinToken == "" {
		t.Fatal("relay result did not include session and one-time join tokens")
	}
	relaySession, err := client.GetRelaySession(ctx, relayResult.Session.ID)
	if err != nil {
		t.Fatalf("GetRelaySession returned error: %v", err)
	}
	if relaySession.ID != relayResult.Session.ID || relaySession.Status != RelaySessionPending {
		t.Fatalf("relay session = %+v, want pending session", relaySession)
	}
	usageSession, err := client.RecordRelaySessionUsage(ctx, RecordRelaySessionUsageRequest{
		SessionID:     relayResult.Session.ID,
		RelayBytesIn:  9,
		RelayBytesOut: 11,
	})
	if err != nil {
		t.Fatalf("RecordRelaySessionUsage returned error: %v", err)
	}
	if usageSession.RelayBytesIn != 9 || usageSession.RelayBytesOut != 11 {
		t.Fatalf("usage session = %+v, want relay byte counters", usageSession)
	}
	closedSession, err := client.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID:     relayResult.Session.ID,
		RelayBytesIn:  13,
		RelayBytesOut: 17,
	})
	if err != nil {
		t.Fatalf("CloseRelaySession returned error: %v", err)
	}
	if closedSession.Status != RelaySessionClosed || closedSession.EndedAt == nil {
		t.Fatalf("closed relay session = %+v, want closed session", closedSession)
	}
	connectionLogs, err := client.ListRelaySessionConnectionLogs(ctx, relayResult.Session.ID)
	if err != nil {
		t.Fatalf("ListRelaySessionConnectionLogs returned error: %v", err)
	}
	if len(connectionLogs) != 1 || connectionLogs[0].SessionID != relayResult.Session.ID {
		t.Fatalf("connection logs = %+v, want one relay session log", connectionLogs)
	}
	relayUsage, err := client.ListRelaySessionUsage(ctx, relayResult.Session.ID)
	if err != nil {
		t.Fatalf("ListRelaySessionUsage returned error: %v", err)
	}
	if len(relayUsage) != 4 {
		t.Fatalf("relay usage = %+v, want four endpoint usage rows from usage and close", relayUsage)
	}

	revoked, err := client.RevokeDevice(ctx, RevokeDeviceRequest{
		DeviceID: device.ID,
		Reason:   "lost laptop",
	})
	if err != nil {
		t.Fatalf("RevokeDevice returned error: %v", err)
	}
	if revoked.Status != DeviceStatusRevoked || revoked.RevokedAt == nil {
		t.Fatalf("revoked = %+v, want revoked device", revoked)
	}

	_, err = client.HeartbeatDevice(ctx, HeartbeatDeviceRequest{
		DeviceID: device.ID,
		Status:   DeviceStatusOnline,
	})
	if err == nil {
		t.Fatal("HeartbeatDevice after revoke returned nil error")
	}
	if !errors.Is(err, ErrRevoked) || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("heartbeat error = %v, want ErrRevoked with clear message", err)
	}
}

func TestClientMapsServerErrors(t *testing.T) {
	server := httptest.NewServer(NewServer(NewService(NewMemoryStore())))
	defer server.Close()

	client := &Client{BaseURL: server.URL, HTTPClient: server.Client()}
	_, err := client.CreateNetwork(context.Background(), CreateNetworkRequest{
		AccountID: "acct_missing",
		Name:      "Home",
	})
	if err == nil {
		t.Fatal("CreateNetwork returned nil error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound || !errors.Is(err, ErrNotFound) {
		t.Fatalf("api error = %+v, want 404 ErrNotFound", apiErr)
	}
	if !strings.Contains(err.Error(), "account was not found") {
		t.Fatalf("error = %q, want clear server message", err.Error())
	}
}

func TestClientPolicyStatusAccountActionsAndRiskEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(),
		WithNow(func() time.Time { return now }),
		WithAccountPolicies("client-tiny", AccountPolicy{
			Name:                   "client-tiny",
			RelayBytesQuota:        48,
			MaxActiveRelaySessions: 2,
			MaxRelaySessionsPerDay: 4,
		}),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	account, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "client-policy@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	status, err := client.GetAccountPolicyStatus(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountPolicyStatus returned error: %v", err)
	}
	if status.Policy.Name != "client-tiny" || status.Policy.RelayBytesQuota != 48 {
		t.Fatalf("policy status = %+v, want client-tiny quota", status)
	}

	frozen, err := client.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "risk review"})
	if err != nil {
		t.Fatalf("FreezeAccount returned error: %v", err)
	}
	if frozen.Status != AccountStatusFrozen {
		t.Fatalf("frozen account = %+v, want frozen", frozen)
	}
	_, err = client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "blocked"})
	if err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("CreateNetwork frozen error = %v, want clear ErrForbidden", err)
	}
	unfrozen, err := client.UnfreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "cleared"})
	if err != nil {
		t.Fatalf("UnfreezeAccount returned error: %v", err)
	}
	if unfrozen.Status != AccountStatusActive {
		t.Fatalf("unfrozen account = %+v, want active", unfrozen)
	}
	banned, err := client.BanAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "abuse"})
	if err != nil {
		t.Fatalf("BanAccount returned error: %v", err)
	}
	if banned.Status != AccountStatusBanned {
		t.Fatalf("banned account = %+v, want banned", banned)
	}
	events, err := client.ListRiskEvents(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	if !hasRiskEvent(events, RiskAccountFrozen) || !hasRiskEvent(events, RiskAccountBanned) {
		t.Fatalf("risk events = %+v, want freeze and ban", events)
	}
}

func TestClientGetsAccountManagementSummary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }))
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	account, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "summary-client@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := client.CreateNetwork(ctx, CreateNetworkRequest{AccountID: account.ID, Name: "SummaryNet"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	invite, err := client.CreateInvite(ctx, CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   2,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	source, err := client.JoinDevice(ctx, JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "source"})
	if err != nil {
		t.Fatalf("JoinDevice source returned error: %v", err)
	}
	target, err := client.JoinDevice(ctx, JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "target"})
	if err != nil {
		t.Fatalf("JoinDevice target returned error: %v", err)
	}
	result, err := client.CreateRelaySession(ctx, CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	if _, err := client.CloseRelaySession(ctx, CloseRelaySessionRequest{SessionID: result.Session.ID, RelayBytesIn: 5, RelayBytesOut: 8}); err != nil {
		t.Fatalf("CloseRelaySession returned error: %v", err)
	}
	if _, err := client.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "summary risk"}); err != nil {
		t.Fatalf("FreezeAccount returned error: %v", err)
	}

	summary, err := client.GetAccountManagementSummary(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccountManagementSummary returned error: %v", err)
	}
	if summary.Account.ID != account.ID || summary.RelaySessions.Closed != 1 {
		t.Fatalf("summary = %+v, want account and one closed relay session", summary)
	}
	if len(summary.RiskEvents) == 0 || len(summary.AuditEvents) == 0 || len(summary.RelayUsage) != 2 || len(summary.ConnectionLogs) != 1 {
		t.Fatalf("summary slices = risk:%d audit:%d usage:%d logs:%d, want populated management data",
			len(summary.RiskEvents), len(summary.AuditEvents), len(summary.RelayUsage), len(summary.ConnectionLogs))
	}
}

func TestClientRejectsMissingLocalBaseURL(t *testing.T) {
	client := &Client{}
	if err := client.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "hub API URL is required") {
		t.Fatalf("Health error = %v, want missing URL error", err)
	}
}
