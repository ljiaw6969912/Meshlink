package ui

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/cloudhub"
	"meshlink/internal/onboarding"
	"meshlink/internal/productflags"
)

func TestInfoReportsOfficialHubMVPCapability(t *testing.T) {
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())

	t.Setenv(productflags.OfficialHubMVPEnv, "")
	features := getJSONForTest(t, handler, "/api/info")["features"].(map[string]any)
	if features["official_hub_mvp"] != false {
		t.Fatalf("official_hub_mvp = %v, want false by default", features["official_hub_mvp"])
	}

	t.Setenv(productflags.OfficialHubMVPEnv, "1")
	features = getJSONForTest(t, handler, "/api/info")["features"].(map[string]any)
	if features["official_hub_mvp"] != true {
		t.Fatalf("official_hub_mvp = %v, want true when explicitly enabled", features["official_hub_mvp"])
	}
}

func TestOnboardingCreateHubAndInviteAPI(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	createResp := postJSONForTest(t, handler, "/api/onboarding/create-hub", map[string]any{
		"network_name": "mesh",
		"node_name":    "hub",
		"listen_port":  8443,
	})
	if createResp["ok"] != true {
		t.Fatalf("create response: %+v", createResp)
	}
	result := createResp["result"].(map[string]any)
	if result["config_path"] != filepath.Join(dir, "configs", "active.json") {
		t.Fatalf("config_path = %v", result["config_path"])
	}

	inviteResp := postJSONForTest(t, handler, "/api/onboarding/invite", map[string]any{
		"server": "example.com:8443",
	})
	if inviteResp["ok"] != true {
		t.Fatalf("invite response: %+v", inviteResp)
	}
	invite := inviteResp["invite"].(map[string]any)
	if invite["code"] == "" || invite["link"] == "" {
		t.Fatalf("invite missing code/link: %+v", invite)
	}
}

func TestOnboardingStartServerModeAPIReturnsInvite(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	resp := postJSONForTest(t, handler, "/api/onboarding/start-server", map[string]any{
		"server_address": "desk.example.com",
		"listen_port":    9443,
	})
	if resp["ok"] != true {
		t.Fatalf("start server response: %+v", resp)
	}
	result := resp["result"].(map[string]any)
	if result["virtual_ip"] != "10.77.0.1" {
		t.Fatalf("virtual_ip = %v, want 10.77.0.1", result["virtual_ip"])
	}
	invite := result["invite"].(map[string]any)
	if invite["server"] != "desk.example.com:9443" {
		t.Fatalf("invite.server = %v, want desk.example.com:9443", invite["server"])
	}
	if invite["code"] == "" || invite["link"] == "" {
		t.Fatalf("invite missing code/link: %+v", invite)
	}
}

func TestOnboardingLeaveAPI(t *testing.T) {
	dir := t.TempDir()
	serviceName := "MeshlinkAgentDefinitelyNotInstalledForLeaveTest"
	configPath := filepath.Join(dir, "configs", "active.json")
	for path, body := range map[string]string{
		configPath: `{
  "node_id": "desk",
  "mode": "spoke",
  "connect": "example.com:8443",
  "ca_file": "../certs/ca.pem",
  "cert_file": "../certs/desk.pem",
  "key_file": "../certs/desk-key.pem",
  "virtual_ip": "10.77.0.2",
  "device": {"type": "null"}
}`,
		filepath.Join(dir, "certs", "ca.pem"):                             "ca",
		filepath.Join(dir, "certs", "desk.pem"):                           "cert",
		filepath.Join(dir, "certs", "desk-key.pem"):                       "key",
		filepath.Join(dir, "configs", "logs", serviceName+".status.json"): `{"state":"running"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	resp := postJSONForTest(t, handler, "/api/onboarding/leave", map[string]any{
		"service_name": serviceName,
	})
	if resp["ok"] != true {
		t.Fatalf("leave response: %+v", resp)
	}
	devices := resp["devices"].(map[string]any)
	if devices["network_state"] != "not_joined" {
		t.Fatalf("network_state = %v, want not_joined", devices["network_state"])
	}
	if nodes, ok := devices["nodes"].([]any); !ok || len(nodes) != 0 {
		t.Fatalf("nodes = %#v, want empty array", devices["nodes"])
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("active config still exists after leave: %v", err)
	}
}

func TestOnboardingInviteAPIExposesLongLivedDeviceLimit(t *testing.T) {
	dir := t.TempDir()
	manager := onboarding.Manager{BaseDir: dir, LocalIPv4: func() (string, error) { return "127.0.0.1", nil }}
	if _, err := manager.CreateHub(onboarding.CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	inviteResp := postJSONForTest(t, handler, "/api/onboarding/invite", map[string]any{
		"server":     "example.com:8443",
		"long_lived": true,
		"max_uses":   3,
	})
	if inviteResp["ok"] != true {
		t.Fatalf("invite response: %+v", inviteResp)
	}
	invite := inviteResp["invite"].(map[string]any)
	maxUses, _ := invite["max_uses"].(float64)
	if invite["long_lived"] != true || int(maxUses) != 3 {
		t.Fatalf("invite = %+v, want long-lived max_uses=3", invite)
	}
}

func TestOnboardingDeviceAdminAPI(t *testing.T) {
	dir := t.TempDir()
	mgr := onboarding.Manager{BaseDir: dir, LocalIPv4: func() (string, error) { return "192.168.1.23", nil }}
	if _, err := mgr.CreateHub(onboarding.CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(onboarding.CreateInviteRequest{Server: "example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.HandleEnroll(onboarding.EnrollRequest{
		Token:    invite.Token,
		Code:     invite.Code,
		NodeName: "laptop",
		CSRPEM:   csr.CSRPEM,
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	renameResp := postJSONForTest(t, handler, "/api/onboarding/device/rename", map[string]any{
		"node_id":      "laptop",
		"display_name": "Alice laptop",
	})
	if renameResp["ok"] != true {
		t.Fatalf("rename response: %+v", renameResp)
	}
	disableResp := postJSONForTest(t, handler, "/api/onboarding/device/disable", map[string]any{
		"node_id": "laptop",
	})
	if disableResp["ok"] != true {
		t.Fatalf("disable response: %+v", disableResp)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/onboarding/devices", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("devices returned %d: %s", rec.Code, rec.Body.String())
	}
	var devicesResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &devicesResp); err != nil {
		t.Fatal(err)
	}
	devices := devicesResp["devices"].(map[string]any)["nodes"].([]any)
	var found map[string]any
	for _, item := range devices {
		node := item.(map[string]any)
		if node["node_id"] == "laptop" {
			found = node
			break
		}
	}
	if found == nil || found["status"] != "disabled" || found["display_name"] != "Alice laptop" {
		t.Fatalf("devices = %+v, want disabled renamed laptop", devices)
	}

	removeResp := postJSONForTest(t, handler, "/api/onboarding/device/remove", map[string]any{
		"node_id": "laptop",
	})
	if removeResp["ok"] != true {
		t.Fatalf("remove response: %+v", removeResp)
	}
}

func TestOfficialHubAPIFlow(t *testing.T) {
	dir := t.TempDir()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	stateResp := getJSONForTest(t, handler, "/api/official-hub/state")
	if stateResp["ok"] != true {
		t.Fatalf("state response: %+v", stateResp)
	}
	state := stateResp["state"].(map[string]any)
	if state["suggested_hub_api_url"] != "http://127.0.0.1:18080" {
		t.Fatalf("state = %+v, want local suggested hub API URL", state)
	}

	accountResp := postJSONForTest(t, handler, "/api/official-hub/account", map[string]any{
		"hub_api_url":  hub.URL,
		"email":        "owner@example.com",
		"display_name": "Owner",
		"password":     "do-not-store",
	})
	account := accountResp["account"].(map[string]any)
	if account["id"] == "" || account["email"] != "owner@example.com" {
		t.Fatalf("account response: %+v", accountResp)
	}

	networkResp := postJSONForTest(t, handler, "/api/official-hub/network", map[string]any{
		"name": "Home",
	})
	network := networkResp["network"].(map[string]any)
	if network["id"] == "" || network["account_id"] != account["id"] {
		t.Fatalf("network response: %+v", networkResp)
	}

	inviteResp := postJSONForTest(t, handler, "/api/official-hub/invite", map[string]any{
		"max_uses": 2,
		"one_time": false,
	})
	invite := inviteResp["invite"].(map[string]any)
	if invite["token"] == "" || invite["code"] == "" {
		t.Fatalf("invite response: %+v", inviteResp)
	}

	joinResp := postJSONForTest(t, handler, "/api/official-hub/join-device", map[string]any{
		"token":       invite["token"],
		"code":        invite["code"],
		"device_name": "office-pc",
	})
	device := joinResp["device"].(map[string]any)
	if device["id"] == "" || device["network_id"] != network["id"] {
		t.Fatalf("join response: %+v", joinResp)
	}

	heartbeatResp := postJSONForTest(t, handler, "/api/official-hub/heartbeat", map[string]any{
		"status": "online",
	})
	heartbeat := heartbeatResp["device"].(map[string]any)
	if heartbeat["status"] != "online" {
		t.Fatalf("heartbeat response: %+v", heartbeatResp)
	}

	if _, err := svc.RecordRelayUsage(context.Background(), cloudhub.RecordRelayUsageRequest{
		AccountID: account["id"].(string),
		NetworkID: network["id"].(string),
		DeviceID:  device["id"].(string),
		BytesIn:   9 * 1024 * 1024,
	}); err != nil {
		t.Fatalf("RecordRelayUsage returned error: %v", err)
	}

	devicesResp := getJSONForTest(t, handler, "/api/official-hub/devices")
	devices := devicesResp["devices"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["id"] != device["id"] {
		t.Fatalf("devices response: %+v", devicesResp)
	}
	reminder := devicesResp["relay_usage_reminder"].(map[string]any)
	if reminder["level"] != "approaching" || !strings.Contains(reminder["message"].(string), "当前连接正在走中继") {
		t.Fatalf("relay usage reminder = %+v, want approaching plain-language relay usage reminder", reminder)
	}
}

func TestOfficialHubAPIRequiresConfiguredHubURL(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	req := httptest.NewRequest(http.MethodPost, "/api/official-hub/account", bytes.NewReader([]byte(`{"email":"owner@example.com"}`)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("account returned %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Hub API 地址") {
		t.Fatalf("body = %s, want clear Hub API URL error", rec.Body.String())
	}
}

func TestOfficialHubTeamManagementAPIAndStaticUI(t *testing.T) {
	dir := t.TempDir()
	store := cloudhub.NewMemoryStore()
	now := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	svc := cloudhub.NewService(store,
		cloudhub.WithNow(func() time.Time { return now }),
		cloudhub.WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.team.members":                   10,
			"cloudhub.plans.team.devices":                   10,
			"cloudhub.plans.team.concurrent_online_devices": 10,
			"cloudhub.plans.team.audit_log_retention_days":  30,
		}),
	)
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	owner, err := svc.CreateAccount(context.Background(), cloudhub.CreateAccountRequest{
		Email: "team-owner@example.com", DisplayName: "Team Owner", PlanID: cloudhub.PlanTeam,
	})
	if err != nil {
		t.Fatalf("CreateAccount owner: %v", err)
	}
	network, err := svc.CreateNetwork(context.Background(), cloudhub.CreateNetworkRequest{AccountID: owner.ID, Name: "Team Network"})
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	device, err := store.CreateDevice(context.Background(), cloudhub.Device{
		ID: "dev_ui_team", AccountID: owner.ID, NetworkID: network.ID,
		Name: "team-server", Status: cloudhub.DeviceStatusOnline, JoinedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	ownerID := owner.ID
	networkID := network.ID
	deviceID := device.ID
	writeOfficialHubStateForUITest(t, dir, onboarding.OfficialHubState{
		HubAPIURL: hub.URL, AccountID: ownerID, AccountEmail: owner.Email, AccountName: owner.DisplayName,
		NetworkID: networkID, NetworkName: network.Name, DeviceID: deviceID, LocalDeviceName: device.Name,
	})

	organizationResp := postJSONForTest(t, handler, "/api/official-hub/organization", map[string]any{"name": "Example Team"})
	organizationID := organizationResp["organization"].(map[string]any)["id"].(string)
	member, err := svc.CreateAccount(context.Background(), cloudhub.CreateAccountRequest{Email: "team-member@example.com", PlanID: cloudhub.PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount member: %v", err)
	}
	if _, err := store.CreateMembership(context.Background(), cloudhub.Membership{
		ID: "mem_ui_team", OrganizationID: organizationID, AccountID: member.ID,
		Role: cloudhub.MembershipRoleMember, Status: cloudhub.MembershipStatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateMembership: %v", err)
	}

	roleResp := postJSONForTest(t, handler, "/api/official-hub/team/member-role", map[string]any{
		"account_id": member.ID, "role": "operator",
	})
	if roleResp["member"].(map[string]any)["role"] != "operator" {
		t.Fatalf("role response = %+v", roleResp)
	}
	groupResp := postJSONForTest(t, handler, "/api/official-hub/team/group", map[string]any{"name": "Production"})
	groupID := groupResp["group"].(map[string]any)["id"].(string)
	postJSONForTest(t, handler, "/api/official-hub/team/device/enroll", map[string]any{"device_id": deviceID})
	postJSONForTest(t, handler, "/api/official-hub/team/device/group", map[string]any{"device_id": deviceID, "group_id": groupID, "action": "add"})
	grantResp := postJSONForTest(t, handler, "/api/official-hub/team/grant", map[string]any{
		"member_account_id": member.ID, "group_id": groupID,
	})
	grantID := grantResp["grant"].(map[string]any)["id"].(string)

	teamResp := getJSONForTest(t, handler, "/api/official-hub/team")
	if teamResp["ok"] != true || len(teamResp["members"].([]any)) != 2 || len(teamResp["groups"].([]any)) != 1 || len(teamResp["devices"].([]any)) != 1 || len(teamResp["grants"].([]any)) != 1 {
		t.Fatalf("team response = %+v", teamResp)
	}
	if _, err := store.AddAuditEvent(context.Background(), cloudhub.AuditEvent{
		ID: "ui-old-audit", Time: now.Add(-31 * 24 * time.Hour), Event: "old_test", AccountID: ownerID,
		Metadata: map[string]any{"organization_id": organizationID, "action": "old_test", "result": "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	auditResp := getJSONForTest(t, handler, "/api/official-hub/team/audit?member_account_id="+ownerID+"&page_size=10")
	page := auditResp["page"].(map[string]any)
	retention := page["retention"].(map[string]any)
	if retention["mode"] != "limited" || int(retention["retention_days"].(float64)) != 30 || len(page["entries"].([]any)) == 0 {
		t.Fatalf("audit response = %+v, want entries and 30-day retention", auditResp)
	}
	cleanupResp := postJSONForTest(t, handler, "/api/official-hub/team/audit/cleanup", map[string]any{"batch_size": 10})
	cleanup := cleanupResp["result"].(map[string]any)
	if int(cleanup["deleted_total"].(float64)) != 1 || cleanup["organization_id"] != organizationID {
		t.Fatalf("cleanup response = %+v, want one expired organization record", cleanupResp)
	}
	encoded, _ := json.Marshal(teamResp)
	for _, forbidden := range []string{"digest", "provider_", "remote_addr", "risk_event", "subscription"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("team response leaked %q: %s", forbidden, encoded)
		}
	}
	postJSONForTest(t, handler, "/api/official-hub/team/grant/revoke", map[string]any{"grant_id": grantID})

	writeOfficialHubStateForUITest(t, dir, onboarding.OfficialHubState{
		HubAPIURL: hub.URL, AccountID: member.ID, NetworkID: networkID,
		OrganizationID: organizationID, OrganizationName: "Example Team", DeviceID: deviceID,
	})
	denied := postJSONAllowErrorForTest(t, handler, "/api/official-hub/team/group", map[string]any{"name": "Denied"})
	if denied["ok"] != false || !strings.Contains(strings.ToLower(denied["error"].(string)), "forbidden") {
		t.Fatalf("operator denial = %+v, want explicit forbidden reason", denied)
	}

	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, want := range []string{
		"officialTeamPanel", "officialCreateOrganization", "officialChangeMemberRole", "officialCreateGroup", "officialEnrollTeamDevice", "officialGrantConnection", "officialTeamMessage",
		"officialAuditPanel", "officialAuditMemberID", "officialAuditSourceDeviceID", "officialAuditTargetDeviceID", "officialAuditStartTime", "officialAuditEndTime",
		"officialAuditConnectionMethod", "officialAuditRelayOnly", "officialAuditMinRelayBytes", "officialAuditQuery", "officialAuditPrevious", "officialAuditNext",
		"officialAuditRetention", "officialAuditList", "officialAuditCleanup", "officialAuditCleanupResult",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing team control %q", want)
		}
	}
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"/api/official-hub/team", "renderOfficialTeam", "officialRevokeGrant", "权限不足", "组织已暂停",
		"/api/official-hub/team/audit", "renderOfficialAuditPage", "audit_log_retention", "保留期", "暂无符合条件的审计记录", "window.confirm",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing team behavior %q", want)
		}
	}
	if strings.Contains(js, `plan_id: "team"`) {
		t.Fatal("app.js must not let the local UI self-assign the Team plan")
	}
	suspendedReason := strings.Index(js, `lower.includes("organization is suspended")`)
	genericForbidden := strings.Index(js, `lower.includes("forbidden")`)
	if suspendedReason < 0 || genericForbidden < 0 || suspendedReason > genericForbidden {
		t.Fatal("app.js must classify suspended organization before generic forbidden errors")
	}
	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	for _, want := range []string{
		".team-management", ".team-grid", ".audit-management", ".audit-filters", ".audit-row", "@media (max-width: 720px)",
		"repeat(3, minmax(0, 1fr))", "grid-template-columns: 1fr", "overflow-wrap: anywhere", "width: min(100vw - 20px, 1180px)",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("style.css missing team responsive rule %q", want)
		}
	}
	if ownerID == "" {
		t.Fatal("owner id must be present")
	}
}

func TestOfficialHubSubscriptionExperienceAPIIsSafeAndAggregated(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	secret := []byte("task-9d-ui-secret")
	svc := cloudhub.NewService(cloudhub.NewMemoryStore(),
		cloudhub.WithNow(func() time.Time { return now }),
		cloudhub.WithSubscriptionProviders(cloudhub.NewHMACSubscriptionProvider("fake-provider", secret, 5*time.Minute)),
		cloudhub.WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.family.concurrent_online_devices":      5,
			"cloudhub.plans.family.official_relay_bytes_per_month": 50 * 1024 * 1024,
			"cloudhub.plans.family.members":                        5,
			"cloudhub.plans.family.audit_log_retention_days":       90,
		}),
	)
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	accountResp := postJSONForTest(t, handler, "/api/official-hub/account", map[string]any{
		"hub_api_url":  hub.URL,
		"email":        "owner@example.com",
		"display_name": "Owner",
		"password":     "do-not-store",
	})
	account := accountResp["account"].(map[string]any)
	accountID := account["id"].(string)
	postSignedSubscriptionForUITest(t, hub.URL, hub.Client(), "fake-provider", secret, now, map[string]any{
		"event_id":                 "evt_ui_active",
		"type":                     "subscription.updated",
		"account_id":               accountID,
		"plan_id":                  string(cloudhub.PlanFamily),
		"status":                   string(cloudhub.SubscriptionStatusActive),
		"provider_subscription_id": "sub_ui_active",
		"effective_at":             now.Format(time.RFC3339),
		"version":                  1,
	})

	resp := getJSONForTest(t, handler, "/api/official-hub/subscription-experience")
	experience := resp["experience"].(map[string]any)
	currentPlan := experience["current_plan"].(map[string]any)
	if currentPlan["id"] != string(cloudhub.PlanFamily) || currentPlan["display_name"] != "Family" {
		t.Fatalf("current_plan = %+v, want Family", currentPlan)
	}
	subscription := experience["subscription"].(map[string]any)
	if subscription["status"] != string(cloudhub.SubscriptionStatusActive) ||
		!strings.Contains(subscription["message"].(string), "已生效") {
		t.Fatalf("subscription = %+v, want active plain-language status", subscription)
	}
	plans := experience["plan_comparison"].([]any)
	if len(plans) != 5 {
		t.Fatalf("plan comparison length = %d, want five plans", len(plans))
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"fake-provider",
		"evt_ui_active",
		"sub_ui_active",
		"provider",
		"event_id",
		"digest",
		"signature",
		"do-not-store",
		"token",
		"private_key",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("subscription experience leaked sensitive field %q: %s", forbidden, encoded)
		}
	}
}

func TestOfficialHubQuotaErrorsPassThroughLocalAPIAsQuotaNotNetworkError(t *testing.T) {
	dir := t.TempDir()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	ctx := context.Background()
	account, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "quota@example.com", PlanID: cloudhub.PlanPersonal})
	if err != nil {
		t.Fatal(err)
	}
	network, err := svc.CreateNetwork(ctx, cloudhub.CreateNetworkRequest{AccountID: account.ID, Name: "QuotaNet"})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := svc.CreateInvite(ctx, cloudhub.CreateInviteRequest{AccountID: account.ID, NetworkID: network.ID, MaxUses: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		if _, err := svc.JoinDevice(ctx, cloudhub.JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: name}); err != nil {
			t.Fatal(err)
		}
	}
	hub := httptest.NewServer(cloudhub.NewServer(svc))
	defer hub.Close()
	writeOfficialHubStateForUITest(t, dir, onboarding.OfficialHubState{
		HubAPIURL: hub.URL,
		AccountID: account.ID,
		NetworkID: network.ID,
	})
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	body := postJSONAllowErrorForTest(t, handler, "/api/official-hub/join-device", map[string]any{
		"token":       invite.Token,
		"code":        invite.Code,
		"device_name": "four",
	})
	if body["ok"] != false {
		t.Fatalf("join body = %+v, want quota failure", body)
	}
	quota, ok := body["quota"].(map[string]any)
	if !ok || quota["dimension"] != string(cloudhub.QuotaDimensionDeviceCount) {
		t.Fatalf("quota = %+v, want structured device_count quota", body["quota"])
	}
	if strings.Contains(strings.ToLower(body["error"].(string)), "network") || strings.Contains(body["error"].(string), "网络错误") {
		t.Fatalf("quota error should not masquerade as network failure: %+v", body)
	}
}

func TestStaticSubscriptionQuotaExperienceEntryUsesPlainText(t *testing.T) {
	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, want := range []string{
		"subscriptionPanel",
		"currentPlanName",
		"subscriptionStatusMessage",
		"quotaList",
		"planComparison",
		"查看套餐/了解升级",
		"自建服务器、自建 Relay 和基础设备互联可继续使用",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("home page missing subscription quota UI %q", want)
		}
	}

	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"loadSubscriptionExperience",
		"renderSubscriptionExperience",
		"renderQuotaIssue",
		"renderPlanComparison",
		"pending",
		"active",
		"past_due",
		"canceled",
		"expired",
		"free",
		"personal",
		"family",
		"team",
		"enterprise",
		"3 台",
		"10 台",
		"需配置",
		"按合同",
		"不是普通网络错误",
		"优先尝试直连",
		"自建 Relay",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing subscription quota behavior %q", want)
		}
	}
	rendererIndex := strings.Index(js, "function renderSubscriptionExperience")
	if rendererIndex < 0 {
		t.Fatal("app.js missing renderSubscriptionExperience function")
	}
	rendererEnd := strings.Index(js[rendererIndex:], "function renderOfficialDevices")
	if rendererEnd < 0 {
		t.Fatal("app.js missing renderOfficialDevices after subscription renderer")
	}
	subscriptionRenderer := js[rendererIndex : rendererIndex+rendererEnd]
	for _, forbidden := range []string{"checkout", "invoice", "tax", "discount", "provider", "payload_digest", "signature", "private_key"} {
		if strings.Contains(subscriptionRenderer, forbidden) || strings.Contains(html, forbidden) {
			t.Fatalf("subscription UI should not expose forbidden payment/internal term %q", forbidden)
		}
	}

	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	for _, want := range []string{
		".subscription-panel",
		".quota-grid",
		".plan-table-wrap",
		"overflow-wrap: anywhere",
		"minmax(0, 1fr)",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("style.css missing subscription quota responsive guard %q", want)
		}
	}
}

func TestStaticWebLifecycleAndAutomaticRefreshContract(t *testing.T) {
	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}

	html := string(htmlBytes)
	js := string(jsBytes)
	css := string(cssBytes)
	for _, want := range []string{
		`id="disconnectNetwork"`,
		`id="joinNetwork" type="button">连接`,
		`data-official-hub hidden`,
		`window.confirm("退出后需要重新使用邀请码才能加入。确定退出网络吗？")`,
		`/api/onboarding/leave`,
		`startStatusPolling`,
	} {
		if !strings.Contains(html+js, want) {
			t.Fatalf("web lifecycle contract missing %q", want)
		}
	}
	if !strings.Contains(css, `[hidden]`) || !strings.Contains(css, `display: none !important`) {
		t.Fatal("hidden official Hub content must not be overridden by active view styles")
	}
}

func TestStaticHomeHighlightsSimpleModes(t *testing.T) {
	b, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, want := range []string{
		"我有公网 IP，创建服务器",
		"我没有公网 IP，使用官方 Hub",
		"官方 Hub MVP/内测",
		"Hub API 地址",
		"http://127.0.0.1:18080",
		"创建测试账号",
		"创建官方网络",
		"生成官方邀请",
		"加入当前设备",
		"发送 heartbeat",
		"刷新官方设备",
		"尚未接入真实订阅和 Relay 数据面",
		"加入已有网络",
		"设备列表",
		"协调服务器",
		"对端路径",
		"重命名",
		"禁用",
		"移除",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("home page does not contain %q", want)
		}
	}
	for _, unavailable := range []string{"官方 Hub 尚未开放", "官方订阅已开通", "Relay 数据面已完成"} {
		if strings.Contains(html, unavailable) {
			t.Fatalf("home page should not contain misleading official hub text %q", unavailable)
		}
	}
	if strings.Contains(html, "高级设置") || strings.Contains(html, `id="reconnectNetwork"`) {
		t.Fatal("home page must remove advanced settings and the duplicate connect action")
	}
	for _, hidden := range []string{"JSON 配置", "CA 名称", "签发节点证书", "服务名", "配置文件路径", "开始诊断", "读取日志"} {
		index := strings.Index(html, hidden)
		if index >= 0 {
			t.Fatalf("home page exposes removed advanced field %q", hidden)
		}
	}
}

func TestStaticUISeparatesCoordinatorAndDirectState(t *testing.T) {
	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, id := range []string{`id="coordinatorState"`, `id="p2pPathState"`} {
		if !strings.Contains(html, id) {
			t.Fatalf("index.html missing independent status field %s", id)
		}
	}

	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"coordinatorStateText",
		`reconnecting: "重连中"`,
		`lan_direct: "局域网直连"`,
		`public_direct: "公网直连"`,
		"协调服务器离线，当前直连不受影响",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing truthful coordinator/path mapping %q", want)
		}
	}
}

func TestStaticUISelfHostedFlowHasNoRelayEntryOrFallbackCopy(t *testing.T) {
	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, forbidden := range []string{
		`data-view="relayView"`,
		`id="relayView"`,
		`id="relayCloudAddress"`,
		`id="relayListenPort"`,
		`id="relayPublicAddress"`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("self-hosted UI still exposes Relay control %s", forbidden)
		}
	}

	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, forbidden := range []string{
		`bindClick("checkSelfRelay"`,
		`bindClick("deploySelfRelay"`,
		"function selfRelayRequest",
	} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("self-hosted UI still binds Relay behavior %q", forbidden)
		}
	}
}

func TestStaticUIDoesNotExposeRemovedAdvancedSettings(t *testing.T) {
	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	if strings.Contains(html, `id="p2pListen"`) {
		t.Fatal("removed advanced diagnostics are still exposed")
	}
	if strings.Contains(strings.ToLower(html), "allowrelay") {
		t.Fatal("advanced settings must not expose allowRelay input")
	}

	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{"devices.p2p_listen", `$("p2pListen")`} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js does not render P2P UDP listen diagnostics %q", want)
		}
	}
}

func TestStaticUIKeepsOfficialCloudFailureSeparateFromSelfHostedNoRelay(t *testing.T) {
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		`officialCloud ? "连接失败"`,
		`"直连失败 · 本版本未启用中继"`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js does not separate official-cloud and self-hosted failures %q", want)
		}
	}
}

func TestSelfHostedRelayDeployAPIValidatesRequiredFields(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	req := httptest.NewRequest(http.MethodPost, "/api/self-relay/deploy", bytes.NewReader([]byte(`{"cloud_server_address":""}`)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy returned %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "云服务器地址不能为空") {
		t.Fatalf("body = %s, want validation error", rec.Body.String())
	}
}

func TestStaticDeviceListShowsConnectionStatusWithoutTechnicalLabels(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		"deviceConnectionSummary",
		"connectionSummaryParts",
		"直连",
		"中继",
		"离线",
		"直连失败",
		"RDP 不可达",
		"质量",
		"ms",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("device list UI missing connection status display %q", want)
		}
	}
	for _, forbidden := range []string{" NAT", "CSR", "CA 路径", "证书路径", "路由表"} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("device list UI should not expose technical label %q", forbidden)
		}
	}

	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	for _, want := range []string{
		".device-main",
		".device-title-line",
		".connection-pill",
		"minmax(0, 1fr)",
		"flex-wrap: wrap",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("device list CSS missing narrow-width guard %q", want)
		}
	}
}

func TestRDPDiagnosticsAPIPrioritizesLocalNetworkState(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/rdp?target=10.77.0.9&target_device=office-pc&network_state=disconnected&target_status=offline", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics returned %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	check := out["check"].(map[string]any)
	if check["status"] != "fail" {
		t.Fatalf("RDP check = %+v, want fail", check)
	}
	detail := check["detail"].(string)
	for _, want := range []string{"网络未连接", "目标设备：office-pc", "本机网络状态：disconnected", "目标设备状态：offline"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("RDP detail = %q, want %q", detail, want)
		}
	}
	if strings.Contains(detail, "目标设备离线") {
		t.Fatalf("RDP detail = %q, must not blame target while local network is disconnected", detail)
	}
}

func TestOneClickDiagnosticsReportAPIPrioritizesLocalNetworkState(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/report?target=10.77.0.9&target_device=office-pc&network_state=reconnecting&target_status=offline&path_state=offline", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics report returned %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	report := out["report"].(map[string]any)
	findings := report["findings"].([]any)
	if len(findings) == 0 {
		t.Fatalf("report = %+v, want findings", report)
	}
	var found bool
	for _, item := range findings {
		finding := item.(map[string]any)
		if finding["code"] == "network_not_connected" {
			found = true
			for _, field := range []string{"problem", "impact", "recommendation", "action"} {
				if strings.TrimSpace(finding[field].(string)) == "" {
					t.Fatalf("finding = %+v, want non-empty %s", finding, field)
				}
			}
		}
	}
	if !found {
		t.Fatalf("findings = %+v, want network_not_connected", findings)
	}
	for _, item := range findings {
		if item.(map[string]any)["code"] == "target_offline" {
			t.Fatalf("findings = %+v, must not blame target while local network is reconnecting", findings)
		}
	}
}

func TestStaticRDPDiagnosticsSendSeparateNetworkAndTargetStates(t *testing.T) {
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"networkState: \"\"",
		"state.networkState = devices.network_state || \"\"",
		"state.networkState = \"not_joined\"",
		"network_state: state.networkState",
		"target_status: selected?.status || \"\"",
		"params.set(\"target_status\", selected.status || \"\")",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing separate RDP state propagation %q", want)
		}
	}
	start := strings.Index(js, "function oneClickDiagnosticParams()")
	end := strings.Index(js[start:], "async function runDiagnostics")
	if start < 0 || end < 0 || !strings.Contains(js[start:start+end], "network_state: state.networkState") {
		t.Fatal("one-click diagnostics must send network_state before checking for a selected target")
	}
}

func TestStaticDeviceRefreshFailureInvalidatesOnlineSnapshotAndCanRecover(t *testing.T) {
	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"devices: []",
		"function conservativeDeviceSnapshot(devices)",
		"status === \"disabled\" || status === \"revoked\" ? status : \"offline\"",
		"state.networkState = \"disconnected\"",
		"state.devices = conservativeDeviceSnapshot(state.devices)",
		"renderDevices(state.devices)",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing refresh-failure invalidation %q", want)
		}
	}
	start := strings.Index(js, "async function refreshDevices()")
	end := strings.Index(js[start:], "async function pollStatus()")
	if start < 0 || end < 0 {
		t.Fatal("refreshDevices source boundary missing")
	}
	refresh := js[start : start+end]
	for _, want := range []string{
		"try {",
		"state.networkState = devices.network_state || \"\"",
		"state.devices = devices.nodes || []",
		"catch (err)",
		"invalidateDeviceSnapshot()",
		"throw err",
	} {
		if !strings.Contains(refresh, want) {
			t.Fatalf("refreshDevices missing success/recovery behavior %q", want)
		}
	}
}

func TestStaticOneClickDiagnosticReportEntryUsesPlainLayout(t *testing.T) {
	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, want := range []string{
		"一键诊断",
		"diagnosticReport",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("home page missing one-click diagnostic entry %q", want)
		}
	}

	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"runOneClickDiagnostics",
		"renderDiagnosticReport",
		"/api/diagnostics/report",
		"发现的问题",
		"影响原因",
		"建议处理",
		"下一步动作",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing diagnostic report behavior %q", want)
		}
	}
	rendererIndex := strings.Index(js, "function renderDiagnosticReport")
	if rendererIndex < 0 {
		t.Fatal("app.js missing renderDiagnosticReport function")
	}
	rendererEnd := strings.Index(js[rendererIndex:], "function renderChecks")
	if rendererEnd < 0 {
		t.Fatal("app.js missing renderChecks function after diagnostic report renderer")
	}
	reportRenderer := js[rendererIndex : rendererIndex+rendererEnd]
	if strings.Contains(reportRenderer, "report.checks") {
		t.Fatal("diagnostic report UI should not render raw low-level checks")
	}
	for _, forbidden := range []string{"NAT", "CSR", "CA", "证书路径", "路由表", "服务名", "JSON", "MeshlinkAgent"} {
		if strings.Contains(reportRenderer, forbidden) {
			t.Fatalf("diagnostic report UI exposes technical term %q", forbidden)
		}
	}

	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	for _, want := range []string{
		".diagnostic-report",
		".finding",
		".finding-head",
		"overflow-wrap: anywhere",
		"flex-wrap: wrap",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("diagnostic report CSS missing narrow-width guard %q", want)
		}
	}
}

func TestStaticRelayUsageReminderEntryUsesPlainText(t *testing.T) {
	htmlBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	if !strings.Contains(html, "officialRelayReminder") {
		t.Fatal("official hub device list should include relay usage reminder entry")
	}

	jsBytes, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	for _, want := range []string{
		"renderRelayUsageReminder",
		"relay_usage_reminder",
		"接近中继流量上限",
		"中继流量已达上限",
		"中继流量明显超额",
		"优先尝试直连",
		"自建中继",
		"管理员中继",
		"升级入口",
		"不是普通网络错误",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing relay reminder text or behavior %q", want)
		}
	}
	rendererIndex := strings.Index(js, "function renderRelayUsageReminder")
	if rendererIndex < 0 {
		t.Fatal("app.js missing renderRelayUsageReminder function")
	}
	rendererEnd := strings.Index(js[rendererIndex:], "function formatInviteExpiry")
	if rendererEnd < 0 {
		t.Fatal("app.js missing formatInviteExpiry function after relay reminder renderer")
	}
	relayRenderer := js[rendererIndex : rendererIndex+rendererEnd]
	for _, forbidden := range []string{"TURN", "STUN", "QUIC", "tgken", "token", "private_key"} {
		if strings.Contains(relayRenderer, forbidden) {
			t.Fatalf("relay reminder UI should not expose low-level term or secret marker %q", forbidden)
		}
	}

	cssBytes, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	for _, want := range []string{
		".relay-reminder",
		"overflow-wrap: anywhere",
		"flex-wrap: wrap",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("relay reminder CSS missing narrow-width guard %q", want)
		}
	}
}

func postJSONForTest(t *testing.T, handler http.Handler, path string, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getJSONForTest(t *testing.T, handler http.Handler, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func postJSONAllowErrorForTest(t *testing.T, handler http.Handler, path string, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s returned %d invalid JSON: %s", path, rec.Code, rec.Body.String())
	}
	return out
}

func writeOfficialHubStateForUITest(t *testing.T, dir string, state onboarding.OfficialHubState) {
	t.Helper()
	path := filepath.Join(dir, "configs", "official-hub.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	state.SuggestedHubAPIURL = onboarding.DefaultOfficialHubAPIURL
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func postSignedSubscriptionForUITest(t *testing.T, hubURL string, client *http.Client, provider string, secret []byte, now time.Time, body map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, hubURL+"/api/subscriptions/webhooks/"+provider, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CloudHub-Timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("X-CloudHub-Signature", "v1="+subscriptionSignatureForUITest(secret, now.Unix(), encoded))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscription webhook returned %d", resp.StatusCode)
	}
}

func subscriptionSignatureForUITest(secret []byte, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
