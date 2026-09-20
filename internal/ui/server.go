package ui

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/cloudhub"
	"meshlink/internal/config"
	"meshlink/internal/diagnose"
	"meshlink/internal/networklifecycle"
	"meshlink/internal/onboarding"
	"meshlink/internal/p2p"
	"meshlink/internal/productflags"
	"meshlink/internal/rdp"
	"meshlink/internal/winservice"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	log     *slog.Logger
	baseDir string
}

func NewServer(logger *slog.Logger) http.Handler {
	return NewServerWithBaseDir(logger, "")
}

func NewServerWithBaseDir(logger *slog.Logger, baseDir string) http.Handler {
	server := &Server{log: logger, baseDir: baseDir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", server.handleInfo)
	mux.HandleFunc("GET /api/onboarding/defaults", server.handleOnboardingDefaults)
	mux.HandleFunc("POST /api/onboarding/start-server", server.handleOnboardingStartServer)
	mux.HandleFunc("POST /api/onboarding/create-hub", server.handleOnboardingCreateHub)
	mux.HandleFunc("POST /api/onboarding/invite", server.handleOnboardingInvite)
	mux.HandleFunc("POST /api/onboarding/join", server.handleOnboardingJoin)
	mux.HandleFunc("POST /api/onboarding/leave", server.handleOnboardingLeave)
	mux.HandleFunc("GET /api/onboarding/devices", server.handleOnboardingDevices)
	mux.HandleFunc("POST /api/onboarding/device/rename", server.handleOnboardingDeviceRename)
	mux.HandleFunc("POST /api/onboarding/device/disable", server.handleOnboardingDeviceDisable)
	mux.HandleFunc("POST /api/onboarding/device/remove", server.handleOnboardingDeviceRemove)
	mux.HandleFunc("POST /api/self-relay/check", server.handleSelfRelayCheck)
	mux.HandleFunc("POST /api/self-relay/deploy", server.handleSelfRelayDeploy)
	mux.HandleFunc("GET /api/official-hub/state", server.handleOfficialHubState)
	mux.HandleFunc("POST /api/official-hub/account", server.handleOfficialHubAccount)
	mux.HandleFunc("POST /api/official-hub/network", server.handleOfficialHubNetwork)
	mux.HandleFunc("POST /api/official-hub/invite", server.handleOfficialHubInvite)
	mux.HandleFunc("POST /api/official-hub/join-device", server.handleOfficialHubJoinDevice)
	mux.HandleFunc("POST /api/official-hub/heartbeat", server.handleOfficialHubHeartbeat)
	mux.HandleFunc("GET /api/official-hub/devices", server.handleOfficialHubDevices)
	mux.HandleFunc("GET /api/official-hub/subscription-experience", server.handleOfficialHubSubscriptionExperience)
	mux.HandleFunc("POST /api/official-hub/organization", server.handleOfficialHubOrganization)
	mux.HandleFunc("GET /api/official-hub/team", server.handleOfficialHubTeam)
	mux.HandleFunc("POST /api/official-hub/team/member-role", server.handleOfficialHubMemberRole)
	mux.HandleFunc("POST /api/official-hub/team/group", server.handleOfficialHubGroup)
	mux.HandleFunc("POST /api/official-hub/team/group/rename", server.handleOfficialHubGroupRename)
	mux.HandleFunc("POST /api/official-hub/team/group/delete", server.handleOfficialHubGroupDelete)
	mux.HandleFunc("POST /api/official-hub/team/device/enroll", server.handleOfficialHubTeamDeviceEnroll)
	mux.HandleFunc("POST /api/official-hub/team/device/group", server.handleOfficialHubTeamDeviceGroup)
	mux.HandleFunc("POST /api/official-hub/team/grant", server.handleOfficialHubConnectionGrant)
	mux.HandleFunc("POST /api/official-hub/team/grant/revoke", server.handleOfficialHubConnectionGrantRevoke)
	mux.HandleFunc("GET /api/official-hub/team/audit", server.handleOfficialHubAudit)
	mux.HandleFunc("POST /api/official-hub/team/audit/cleanup", server.handleOfficialHubAuditCleanup)
	mux.HandleFunc("POST /api/official-hub/team/deployment-bundle", server.handleOfficialHubDeploymentBundle)
	mux.HandleFunc("GET /api/official-hub/team/deployment-bundles", server.handleOfficialHubDeploymentBundles)
	mux.HandleFunc("POST /api/official-hub/team/bootstrap-credential/revoke", server.handleOfficialHubBootstrapCredentialRevoke)
	mux.HandleFunc("POST /api/official-hub/team/rollout", server.handleOfficialHubRolloutCreate)
	mux.HandleFunc("GET /api/official-hub/team/rollouts", server.handleOfficialHubRollouts)
	mux.HandleFunc("GET /api/official-hub/team/rollout", server.handleOfficialHubRollout)
	mux.HandleFunc("POST /api/official-hub/team/rollout/cancel", server.handleOfficialHubRolloutCancel)
	mux.HandleFunc("POST /api/official-hub/team/rollout/retry", server.handleOfficialHubRolloutRetry)
	mux.HandleFunc("GET /api/official-hub/team/private-license", server.handleOfficialHubPrivateLicense)
	mux.HandleFunc("POST /api/official-hub/team/private-license", server.handleOfficialHubPrivateLicenseImport)
	mux.HandleFunc("GET /api/service/status", server.handleServiceStatus)
	mux.HandleFunc("POST /api/service", server.handleServiceAction)
	mux.HandleFunc("POST /api/certs/init-ca", server.handleInitCA)
	mux.HandleFunc("POST /api/certs/issue", server.handleIssueCert)
	mux.HandleFunc("GET /api/config", server.handleGetConfig)
	mux.HandleFunc("POST /api/config", server.handleSaveConfig)
	mux.HandleFunc("GET /api/logs", server.handleLogs)
	mux.HandleFunc("POST /api/rdp/open", server.handleOpenRDP)
	mux.HandleFunc("GET /api/diagnostics", server.handleDiagnostics)
	mux.HandleFunc("GET /api/diagnostics/report", server.handleOneClickDiagnostics)
	mux.HandleFunc("GET /api/diagnostics/rdp", server.handleRDPDiagnostics)

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return localOnly(mux)
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.RemoteAddr
		if !strings.HasPrefix(host, "127.0.0.1:") {
			http.Error(w, "local access only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	cwd := s.effectiveBaseDir()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"cwd": cwd,
		"features": map[string]bool{
			"official_hub_mvp": productflags.OfficialHubMVPEnabled(),
		},
	})
}

func (s *Server) handleOnboardingDefaults(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"base_dir":    s.effectiveBaseDir(),
		"node_name":   host,
		"network":     "我的组网",
		"listen_port": 8443,
		"protocol":    "tcp_tls_v1",
		"config_path": filepath.Join(s.effectiveBaseDir(), "configs", "active.json"),
	})
}

func (s *Server) handleOnboardingStartServer(w http.ResponseWriter, r *http.Request) {
	var req onboarding.StartServerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ServerAddress) == "" {
		writeError(w, fmt.Errorf("请填写其他设备能够访问的域名或公网地址"))
		return
	}
	manager, err := onboarding.SelectRole(s.effectiveBaseDir(), "hub")
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := manager.StartServerMode(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOnboardingCreateHub(w http.ResponseWriter, r *http.Request) {
	var req onboarding.CreateHubRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CreateHub(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOnboardingInvite(w http.ResponseWriter, r *http.Request) {
	var req onboarding.CreateInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	invite, err := s.onboardingManager().CreateServerInvite(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite": invite})
}

func (s *Server) handleOnboardingJoin(w http.ResponseWriter, r *http.Request) {
	var req onboarding.JoinSpokeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.onboardingManager().IsOwnServerInvite(req.InviteLink) {
		writeError(w, fmt.Errorf("本机就是此网络的服务器，本机节点会自动连接，请使用启动服务器"))
		return
	}
	manager, err := onboarding.SelectRole(s.effectiveBaseDir(), "spoke")
	if err != nil {
		writeError(w, err)
		return
	}
	installed, _ := winservice.Status(winservice.DefaultName)
	imported, err := manager.ImportInstalledSpoke(installed.ConfigPath, req.InviteLink)
	if err != nil {
		writeError(w, err)
		return
	}
	if imported {
		req.NodeName = ""
	}
	result, err := manager.JoinSpoke(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOnboardingDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.onboardingManager().Devices(r.URL.Query().Get("service_name"))
	if err != nil {
		writeError(w, err)
		return
	}
	if runtime.GOOS == "windows" {
		service, err := winservice.Status(r.URL.Query().Get("service_name"))
		devices = onboarding.WithServiceRunning(devices, err == nil && service.Installed && service.State == "running")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": devices})
}

func (s *Server) handleOnboardingLeave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceName string `json:"service_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	manager := s.onboardingManager()
	if err := networklifecycle.Leave(manager, req.ServiceName); err != nil {
		writeError(w, err)
		return
	}
	devices, err := manager.Devices(req.ServiceName)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": devices})
}

func (s *Server) handleOnboardingDeviceRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID      string `json:"node_id"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	node, err := s.onboardingManager().RenameDevice(req.NodeID, req.DisplayName)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": node})
}

func (s *Server) handleOnboardingDeviceDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"node_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	node, err := s.onboardingManager().DisableDevice(req.NodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": node})
}

func (s *Server) handleOnboardingDeviceRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"node_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	node, err := s.onboardingManager().RemoveDevice(req.NodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": node})
}

func (s *Server) handleSelfRelayCheck(w http.ResponseWriter, r *http.Request) {
	var req onboarding.SelfHostedRelayRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CheckSelfHostedRelay(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleSelfRelayDeploy(w http.ResponseWriter, r *http.Request) {
	var req onboarding.SelfHostedRelayRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().DeploySelfHostedRelay(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOfficialHubState(w http.ResponseWriter, _ *http.Request) {
	state, err := s.onboardingManager().OfficialHubState()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (s *Server) handleOfficialHubAccount(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubAccountRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CreateOfficialHubAccount(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"account": result.Account,
		"state":   result.State,
	})
}

func (s *Server) handleOfficialHubNetwork(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubNetworkRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CreateOfficialHubNetwork(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"network": result.Network,
		"state":   result.State,
	})
}

func (s *Server) handleOfficialHubInvite(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CreateOfficialHubInvite(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"invite": result.Invite,
		"state":  result.State,
	})
}

func (s *Server) handleOfficialHubJoinDevice(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubJoinDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().JoinOfficialHubDevice(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"device": result.Device,
		"state":  result.State,
	})
}

func (s *Server) handleOfficialHubHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubHeartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().HeartbeatOfficialHubDevice(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"device": result.Device,
		"state":  result.State,
	})
}

func (s *Server) handleOfficialHubDevices(w http.ResponseWriter, r *http.Request) {
	result, err := s.onboardingManager().ListOfficialHubDevices(r.Context(), onboarding.OfficialHubDevicesRequest{
		HubAPIURL: r.URL.Query().Get("hub_api_url"),
		NetworkID: r.URL.Query().Get("network_id"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"devices":              result.Devices,
		"state":                result.State,
		"relay_usage_reminder": result.RelayUsageReminder,
	})
}

func (s *Server) handleOfficialHubSubscriptionExperience(w http.ResponseWriter, r *http.Request) {
	experience, err := s.onboardingManager().OfficialHubSubscriptionExperience(r.Context(), onboarding.OfficialHubSubscriptionExperienceRequest{
		HubAPIURL: r.URL.Query().Get("hub_api_url"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"experience": experience,
	})
}

func (s *Server) handleOfficialHubOrganization(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubOrganizationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	organization, state, err := s.onboardingManager().CreateOfficialHubOrganization(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "organization": organization, "state": state})
}

func (s *Server) handleOfficialHubTeam(w http.ResponseWriter, r *http.Request) {
	result, err := s.onboardingManager().OfficialHubTeam(r.Context(), onboarding.OfficialHubTeamRequest{
		HubAPIURL: r.URL.Query().Get("hub_api_url"), OrganizationID: r.URL.Query().Get("organization_id"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "organization": result.Organization, "members": result.Members,
		"groups": result.Groups, "devices": result.Devices, "grants": result.Grants, "state": result.State,
	})
}

func (s *Server) handleOfficialHubPrivateLicense(w http.ResponseWriter, r *http.Request) {
	license, err := s.onboardingManager().GetOfficialHubPrivateLicense(r.Context(), onboarding.OfficialHubTeamRequest{
		HubAPIURL: r.URL.Query().Get("hub_api_url"), OrganizationID: r.URL.Query().Get("organization_id"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "license": license})
}

func (s *Server) handleOfficialHubPrivateLicenseImport(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubPrivateLicenseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	license, err := s.onboardingManager().ImportOfficialHubPrivateLicense(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "license": license})
}

func (s *Server) handleOfficialHubMemberRole(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubMemberRoleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	member, err := s.onboardingManager().ChangeOfficialHubMemberRole(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": member})
}

func (s *Server) handleOfficialHubGroup(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubGroupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	group, err := s.onboardingManager().CreateOfficialHubGroup(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
}

func (s *Server) handleOfficialHubGroupRename(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubGroupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	group, err := s.onboardingManager().RenameOfficialHubGroup(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
}

func (s *Server) handleOfficialHubGroupDelete(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubGroupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	group, err := s.onboardingManager().DeleteOfficialHubGroup(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
}

func (s *Server) handleOfficialHubTeamDeviceEnroll(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubTeamDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	device, err := s.onboardingManager().EnrollOfficialHubTeamDevice(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
}

func (s *Server) handleOfficialHubTeamDeviceGroup(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubTeamDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	device, err := s.onboardingManager().ChangeOfficialHubTeamDeviceGroup(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
}

func (s *Server) handleOfficialHubConnectionGrant(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubConnectionGrantRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	grant, err := s.onboardingManager().GrantOfficialHubConnection(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
}

func (s *Server) handleOfficialHubConnectionGrantRevoke(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubConnectionGrantRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	grant, err := s.onboardingManager().RevokeOfficialHubConnection(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
}

func (s *Server) handleOfficialHubAudit(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	start, err := parseOfficialHubAuditTime(values.Get("start_time"), "start_time")
	if err != nil {
		writeError(w, err)
		return
	}
	end, err := parseOfficialHubAuditTime(values.Get("end_time"), "end_time")
	if err != nil {
		writeError(w, err)
		return
	}
	relayOnly := false
	if raw := strings.TrimSpace(values.Get("relay_only")); raw != "" {
		relayOnly, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, fmt.Errorf("relay_only is invalid"))
			return
		}
	}
	minRelayBytes := int64(0)
	if raw := strings.TrimSpace(values.Get("min_relay_bytes")); raw != "" {
		minRelayBytes, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, fmt.Errorf("min_relay_bytes is invalid"))
			return
		}
	}
	pageSize := 0
	if raw := strings.TrimSpace(values.Get("page_size")); raw != "" {
		pageSize, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, fmt.Errorf("page_size is invalid"))
			return
		}
	}
	page, err := s.onboardingManager().QueryOfficialHubAudit(r.Context(), onboarding.OfficialHubAuditRequest{
		HubAPIURL: values.Get("hub_api_url"), OrganizationID: values.Get("organization_id"),
		MemberAccountID: values.Get("member_account_id"), SourceDeviceID: values.Get("source_device_id"), TargetDeviceID: values.Get("target_device_id"),
		StartTime: start, EndTime: end, ConnectionMethod: values.Get("connection_method"), RelayOnly: relayOnly,
		MinRelayBytes: minRelayBytes, PageSize: pageSize, Cursor: values.Get("cursor"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "page": page})
}

func (s *Server) handleOfficialHubAuditCleanup(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubAuditCleanupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CleanupOfficialHubAudit(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOfficialHubDeploymentBundle(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubDeploymentBundleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CreateOfficialHubDeploymentBundle(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOfficialHubDeploymentBundles(w http.ResponseWriter, r *http.Request) {
	bundles, err := s.onboardingManager().ListOfficialHubDeploymentBundles(r.Context(), onboarding.OfficialHubTeamRequest{
		HubAPIURL: r.URL.Query().Get("hub_api_url"), OrganizationID: r.URL.Query().Get("organization_id"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bundles": bundles})
}

func (s *Server) handleOfficialHubBootstrapCredentialRevoke(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubDeploymentCredentialRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	credential, err := s.onboardingManager().RevokeOfficialHubBootstrapCredential(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "credential": credential})
}

func (s *Server) handleOfficialHubRolloutCreate(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubRolloutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CreateOfficialHubRollout(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOfficialHubRollouts(w http.ResponseWriter, r *http.Request) {
	rollouts, err := s.onboardingManager().ListOfficialHubRollouts(r.Context(), onboarding.OfficialHubTeamRequest{
		HubAPIURL: r.URL.Query().Get("hub_api_url"), OrganizationID: r.URL.Query().Get("organization_id"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rollouts": rollouts})
}

func (s *Server) handleOfficialHubRollout(w http.ResponseWriter, r *http.Request) {
	result, err := s.onboardingManager().GetOfficialHubRollout(r.Context(), onboarding.OfficialHubRolloutRequest{
		HubAPIURL: r.URL.Query().Get("hub_api_url"), OrganizationID: r.URL.Query().Get("organization_id"), RolloutID: r.URL.Query().Get("rollout_id"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOfficialHubRolloutCancel(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubRolloutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CancelOfficialHubRollout(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOfficialHubRolloutRetry(w http.ResponseWriter, r *http.Request) {
	var req onboarding.OfficialHubRolloutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	target, err := s.onboardingManager().RetryOfficialHubRolloutTarget(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target})
}

func parseOfficialHubAuditTime(raw, field string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be RFC3339", field)
	}
	value = value.UTC()
	return &value, nil
}

func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	status, err := winservice.Status(r.URL.Query().Get("service_name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action      string `json:"action"`
		ServiceName string `json:"service_name"`
		ConfigPath  string `json:"config_path"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	var err error
	switch req.Action {
	case "install":
		err = installAgentService(req.ServiceName, req.ConfigPath)
	case "uninstall":
		err = winservice.Uninstall(req.ServiceName)
	case "start":
		err = winservice.Start(req.ServiceName)
	case "stop":
		err = winservice.Stop(req.ServiceName)
	default:
		err = errors.New("unknown service action")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleInitCA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OutDir string `json:"out_dir"`
		Name   string `json:"name"`
		Days   int    `json:"days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := certutil.InitCA(certutil.CAOptions{
		OutDir: req.OutDir,
		Name:   req.Name,
		Days:   req.Days,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleIssueCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OutDir    string `json:"out_dir"`
		Name      string `json:"name"`
		CAPath    string `json:"ca_path"`
		CAKeyPath string `json:"ca_key_path"`
		DNS       string `json:"dns"`
		IPs       string `json:"ips"`
		Days      int    `json:"days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := certutil.Issue(certutil.IssueOptions{
		OutDir:    req.OutDir,
		Name:      req.Name,
		CAPath:    req.CAPath,
		CAKeyPath: req.CAKeyPath,
		DNSNames:  certutil.SplitCSV(req.DNS),
		IPAddrs:   certutil.SplitCSV(req.IPs),
		Days:      req.Days,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, errors.New("path is required"))
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"content": string(b),
	})
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeError(w, errors.New("path is required"))
		return
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(req.Content), &cfg); err != nil {
		writeError(w, err)
		return
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, err)
		return
	}
	pretty, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0o700); err != nil {
		writeError(w, err)
		return
	}
	if err := os.WriteFile(req.Path, append(pretty, '\n'), 0o600); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": string(pretty)})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config_path")
	serviceName := r.URL.Query().Get("service_name")
	if serviceName == "" {
		serviceName = winservice.DefaultName
	}
	if configPath == "" {
		writeError(w, errors.New("config_path is required"))
		return
	}
	logPath := filepath.Join(filepath.Dir(configPath), "logs", serviceName+".log")
	n := int64(65536)
	if raw := r.URL.Query().Get("bytes"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && parsed > 0 && parsed <= 1024*1024 {
			n = parsed
		}
	}
	text, err := tailFile(logPath, n)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": logPath, "content": text})
}

func (s *Server) handleOpenRDP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := rdp.Open(req.Target); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	report := diagnose.Run(r.URL.Query().Get("config_path"), r.URL.Query().Get("service_name"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "report": report})
}

func (s *Server) handleOneClickDiagnostics(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	port, _ := strconv.Atoi(query.Get("port"))
	qualityScore, _ := strconv.Atoi(query.Get("quality_score"))
	latencyMS, _ := strconv.ParseInt(query.Get("latency_ms"), 10, 64)
	switchCount, _ := strconv.Atoi(query.Get("switch_count"))
	report := diagnose.RunOneClick(diagnose.OneClickRequest{
		ConfigPath:   query.Get("config_path"),
		ServiceName:  query.Get("service_name"),
		Target:       query.Get("target"),
		TargetDevice: query.Get("target_device"),
		NetworkState: query.Get("network_state"),
		TargetStatus: query.Get("target_status"),
		TunnelStatus: query.Get("tunnel_status"),
		Port:         port,
		DeviceStatus: query.Get("device_status"),
		ConnectionStatus: p2p.ConnectionStatus{
			PathType:     p2p.PathType(query.Get("path_type")),
			PathState:    p2p.PathState(query.Get("path_state")),
			QualityScore: qualityScore,
			LatencyMS:    latencyMS,
			SwitchCount:  switchCount,
			LastError:    query.Get("last_error"),
		},
		IncludeRDP: query.Get("include_rdp") == "1" || strings.EqualFold(query.Get("include_rdp"), "true"),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "report": report})
}

func (s *Server) handleRDPDiagnostics(w http.ResponseWriter, r *http.Request) {
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	check := diagnose.CheckRDPTarget(diagnose.RDPCheckRequest{
		Target:       r.URL.Query().Get("target"),
		TargetDevice: r.URL.Query().Get("target_device"),
		NetworkState: r.URL.Query().Get("network_state"),
		TargetStatus: r.URL.Query().Get("target_status"),
		TunnelStatus: r.URL.Query().Get("tunnel_status"),
		Port:         port,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "check": check})
}

func tailFile(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func installAgentService(name, configPath string) error {
	if name == "" {
		name = winservice.DefaultName
	}
	agentPath, err := findAgentExe()
	if err != nil {
		return err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(agentPath, "-service", "install", "-service-name", name, "-config", configPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func findAgentExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cwd, _ := os.Getwd()
	candidates := []string{
		filepath.Join(filepath.Dir(exe), "mesh-agent.exe"),
		filepath.Join(cwd, "mesh-agent.exe"),
		filepath.Join(cwd, "bin", "mesh-agent.exe"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("找不到 mesh-agent.exe，请确认它和当前程序在同一目录，或位于当前目录的 bin 目录下")
}

func (s *Server) effectiveBaseDir() string {
	if s.baseDir != "" {
		return s.baseDir
	}
	cwd, _ := os.Getwd()
	return cwd
}

func (s *Server) onboardingManager() onboarding.Manager {
	status, _ := winservice.Status(winservice.DefaultName)
	return onboarding.ManagerForConfig(s.effectiveBaseDir(), status.ConfigPath)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		writeError(w, errors.New("missing request body"))
		return false
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, err)
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	body := map[string]any{
		"ok":    false,
		"error": err.Error(),
	}
	var apiErr *cloudhub.APIError
	if errors.As(err, &apiErr) && apiErr.Quota != nil {
		body["quota"] = apiErr.Quota
	}
	writeJSON(w, http.StatusBadRequest, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
