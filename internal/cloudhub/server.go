package cloudhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"meshlink/internal/licensing"
)

const ActorAccountHeader = "X-Mesh-Actor-Account-ID"

type ServerOption func(*serverConfig)

type serverConfig struct {
	relayEndpoint        string
	relayStatusProvider  func() RelayStatus
	relaySessionCreated  func(context.Context, RelaySessionResult) error
	relaySessionClosed   func(context.Context, RelaySession) error
	monitoring           *Monitor
	monitoringAuthorizer func(context.Context, string) bool
}

func WithRelayEndpoint(endpoint string) ServerOption {
	return func(cfg *serverConfig) {
		cfg.relayEndpoint = strings.TrimSpace(endpoint)
	}
}

func WithRelayStatusProvider(provider func() RelayStatus) ServerOption {
	return func(cfg *serverConfig) {
		cfg.relayStatusProvider = provider
	}
}

func WithRelaySessionCreatedHook(hook func(context.Context, RelaySessionResult) error) ServerOption {
	return func(cfg *serverConfig) {
		cfg.relaySessionCreated = hook
	}
}

func WithRelaySessionClosedHook(hook func(context.Context, RelaySession) error) ServerOption {
	return func(cfg *serverConfig) {
		cfg.relaySessionClosed = hook
	}
}

func WithMonitoring(monitoring *Monitor) ServerOption {
	return func(cfg *serverConfig) {
		cfg.monitoring = monitoring
	}
}

func WithMonitoringAuthorizer(authorizer func(context.Context, string) bool) ServerOption {
	return func(cfg *serverConfig) {
		cfg.monitoringAuthorizer = authorizer
	}
}

func NewServer(service *Service, opts ...ServerOption) http.Handler {
	if service == nil {
		service = NewService(NewMemoryStore())
	}
	cfg := serverConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		handleHealth(w, r, cfg)
	})
	mux.HandleFunc("GET /api/operations/monitoring", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		if cfg.monitoringAuthorizer == nil || !cfg.monitoringAuthorizer(r.Context(), actorAccountID) {
			writeError(w, fmt.Errorf("monitoring access denied: %w", ErrForbidden))
			return
		}
		if cfg.monitoring == nil {
			writeError(w, fmt.Errorf("monitoring is unavailable: %w", ErrNotFound))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "monitoring": cfg.monitoring.Snapshot()})
	})
	mux.HandleFunc("GET /api/plans", func(w http.ResponseWriter, r *http.Request) {
		plans, err := service.ListPlans(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plans": plans})
	})
	mux.HandleFunc("GET /api/plans/{id}", func(w http.ResponseWriter, r *http.Request) {
		plan, err := service.GetPlan(r.Context(), PlanID(r.PathValue("id")))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": plan})
	})
	mux.HandleFunc("POST /api/accounts", func(w http.ResponseWriter, r *http.Request) {
		var req CreateAccountRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		account, err := service.CreateAccount(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": account})
	})
	mux.HandleFunc("GET /api/accounts/{id}/policy", func(w http.ResponseWriter, r *http.Request) {
		status, err := service.GetAccountPolicyStatus(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
	})
	mux.HandleFunc("GET /api/accounts/{id}/subscription", func(w http.ResponseWriter, r *http.Request) {
		status, err := service.GetAccountSubscriptionStatus(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subscription": status})
	})
	mux.HandleFunc("GET /api/accounts/{id}/summary", func(w http.ResponseWriter, r *http.Request) {
		summary, err := service.GetAccountManagementSummary(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "summary": summary})
	})
	mux.HandleFunc("POST /api/accounts/{id}/freeze", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		account, err := service.FreezeAccount(r.Context(), AccountStatusChangeRequest{
			AccountID: r.PathValue("id"),
			Reason:    req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": account})
	})
	mux.HandleFunc("POST /api/accounts/{id}/unfreeze", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		account, err := service.UnfreezeAccount(r.Context(), AccountStatusChangeRequest{
			AccountID: r.PathValue("id"),
			Reason:    req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": account})
	})
	mux.HandleFunc("POST /api/accounts/{id}/ban", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		account, err := service.BanAccount(r.Context(), AccountStatusChangeRequest{
			AccountID: r.PathValue("id"),
			Reason:    req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": account})
	})
	mux.HandleFunc("POST /api/organizations", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req CreateOrganizationRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		req.OwnerAccountID = actorAccountID
		organization, err := service.CreateOrganization(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "organization": organization})
	})
	mux.HandleFunc("GET /api/organizations/{id}", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		organization, err := service.GetOrganization(r.Context(), r.PathValue("id"), actorAccountID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "organization": organization})
	})
	mux.HandleFunc("GET /api/organizations/{id}/private-license", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		summary, err := service.GetPrivateLicenseSummary(r.Context(), actorAccountID, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "license": summary})
	})
	mux.HandleFunc("POST /api/organizations/{id}/private-license", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			SignedLicense string `json:"signed_license"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		summary, err := service.ImportPrivateLicense(r.Context(), ImportPrivateLicenseRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), SignedLicense: []byte(req.SignedLicense),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "license": summary})
	})
	mux.HandleFunc("POST /api/organizations/{id}/suspend", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id"`
			Reason         string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		organization, err := service.SuspendOrganization(r.Context(), OrganizationStatusChangeRequest{
			ActorAccountID: actorAccountID,
			OrganizationID: r.PathValue("id"),
			Reason:         req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "organization": organization})
	})
	mux.HandleFunc("POST /api/organizations/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id"`
			Reason         string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		organization, err := service.ResumeOrganization(r.Context(), OrganizationStatusChangeRequest{
			ActorAccountID: actorAccountID,
			OrganizationID: r.PathValue("id"),
			Reason:         req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "organization": organization})
	})
	mux.HandleFunc("GET /api/organizations/{id}/members", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		members, err := service.ListOrganizationMembers(r.Context(), r.PathValue("id"), actorAccountID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "members": members})
	})
	mux.HandleFunc("POST /api/organizations/{id}/invites", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID   string         `json:"actor_account_id"`
			InvitedAccountID string         `json:"invited_account_id"`
			Role             MembershipRole `json:"role,omitempty"`
			TTLSeconds       int64          `json:"ttl_seconds,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		inviteReq := CreateOrganizationInviteRequest{
			ActorAccountID:   actorAccountID,
			OrganizationID:   r.PathValue("id"),
			InvitedAccountID: req.InvitedAccountID,
			Role:             req.Role,
		}
		if req.TTLSeconds > 0 {
			inviteReq.TTL = time.Duration(req.TTLSeconds) * time.Second
		}
		invite, err := service.CreateOrganizationInvite(r.Context(), inviteReq)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite": invite})
	})
	mux.HandleFunc("POST /api/organizations/{id}/invites/accept", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			AccountID string `json:"account_id"`
			Token     string `json:"token"`
			Code      string `json:"code"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		member, err := service.AcceptOrganizationInvite(r.Context(), AcceptOrganizationInviteRequest{
			OrganizationID: r.PathValue("id"),
			AccountID:      actorAccountID,
			Token:          req.Token,
			Code:           req.Code,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": member})
	})
	mux.HandleFunc("POST /api/organizations/{id}/invites/{invite_id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id"`
			Reason         string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		invite, err := service.RevokeOrganizationInvite(r.Context(), RevokeOrganizationInviteRequest{
			ActorAccountID: actorAccountID,
			OrganizationID: r.PathValue("id"),
			InviteID:       r.PathValue("invite_id"),
			Reason:         req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite": invite})
	})
	mux.HandleFunc("POST /api/organizations/{id}/members/{account_id}/suspend", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id"`
			Reason         string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		member, err := service.SuspendOrganizationMember(r.Context(), OrganizationMemberStatusRequest{
			ActorAccountID: actorAccountID,
			OrganizationID: r.PathValue("id"),
			AccountID:      r.PathValue("account_id"),
			Reason:         req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": member})
	})
	mux.HandleFunc("POST /api/organizations/{id}/members/{account_id}/resume", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id"`
			Reason         string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		member, err := service.ResumeOrganizationMember(r.Context(), OrganizationMemberStatusRequest{
			ActorAccountID: actorAccountID,
			OrganizationID: r.PathValue("id"),
			AccountID:      r.PathValue("account_id"),
			Reason:         req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": member})
	})
	mux.HandleFunc("POST /api/organizations/{id}/members/{account_id}/remove", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id"`
			Reason         string `json:"reason,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		member, err := service.RemoveOrganizationMember(r.Context(), OrganizationMemberStatusRequest{
			ActorAccountID: actorAccountID,
			OrganizationID: r.PathValue("id"),
			AccountID:      r.PathValue("account_id"),
			Reason:         req.Reason,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": member})
	})
	mux.HandleFunc("POST /api/organizations/{id}/members/{account_id}/role", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string         `json:"actor_account_id,omitempty"`
			Role           MembershipRole `json:"role"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		member, err := service.ChangeOrganizationMemberRole(r.Context(), ChangeOrganizationMemberRoleRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"),
			AccountID: r.PathValue("account_id"), Role: req.Role,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": member})
	})
	mux.HandleFunc("GET /api/organizations/{id}/groups", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		groups, err := service.ListDeviceGroups(r.Context(), actorAccountID, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": groups})
	})
	mux.HandleFunc("POST /api/organizations/{id}/groups", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id,omitempty"`
			Name           string `json:"name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		group, err := service.CreateDeviceGroup(r.Context(), CreateDeviceGroupRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), Name: req.Name,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
	})
	mux.HandleFunc("GET /api/organizations/{id}/groups/{group_id}", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		group, err := service.GetDeviceGroup(r.Context(), actorAccountID, r.PathValue("id"), r.PathValue("group_id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
	})
	mux.HandleFunc("POST /api/organizations/{id}/groups/{group_id}/rename", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		group, err := service.RenameDeviceGroup(r.Context(), RenameDeviceGroupRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"),
			GroupID: r.PathValue("group_id"), Name: req.Name,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
	})
	mux.HandleFunc("POST /api/organizations/{id}/groups/{group_id}/delete", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		group, err := service.DeleteDeviceGroup(r.Context(), DeleteDeviceGroupRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), GroupID: r.PathValue("group_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
	})
	mux.HandleFunc("GET /api/organizations/{id}/devices", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		devices, err := service.ListOrganizationDevices(r.Context(), actorAccountID, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": devices})
	})
	mux.HandleFunc("POST /api/organizations/{id}/devices/{device_id}/enroll", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		device, err := service.EnrollOrganizationDevice(r.Context(), EnrollOrganizationDeviceRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), DeviceID: r.PathValue("device_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
	})
	mux.HandleFunc("POST /api/organizations/{id}/devices/{device_id}/remove", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		device, err := service.RemoveOrganizationDevice(r.Context(), RemoveOrganizationDeviceRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), DeviceID: r.PathValue("device_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
	})
	mux.HandleFunc("POST /api/organizations/{id}/groups/{group_id}/devices/{device_id}/add", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		device, err := service.AddDeviceToGroup(r.Context(), OrganizationDeviceGroupRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"),
			GroupID: r.PathValue("group_id"), DeviceID: r.PathValue("device_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
	})
	mux.HandleFunc("POST /api/organizations/{id}/groups/{group_id}/devices/{device_id}/remove", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		device, err := service.RemoveDeviceFromGroup(r.Context(), OrganizationDeviceGroupRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"),
			GroupID: r.PathValue("group_id"), DeviceID: r.PathValue("device_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
	})
	mux.HandleFunc("GET /api/organizations/{id}/connection-grants", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		grants, err := service.ListConnectionGrants(r.Context(), actorAccountID, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grants": grants})
	})
	mux.HandleFunc("POST /api/organizations/{id}/connection-grants", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			MemberAccountID string `json:"member_account_id"`
			DeviceID        string `json:"device_id,omitempty"`
			GroupID         string `json:"group_id,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		grant, err := service.GrantConnectionAccess(r.Context(), ConnectionGrantRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), MemberAccountID: req.MemberAccountID,
			DeviceID: req.DeviceID, GroupID: req.GroupID,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
	})
	mux.HandleFunc("POST /api/organizations/{id}/connection-grants/{grant_id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		grant, err := service.RevokeConnectionAccess(r.Context(), RevokeConnectionGrantRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), GrantID: r.PathValue("grant_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
	})
	mux.HandleFunc("GET /api/organizations/{id}/audit", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		values := r.URL.Query()
		query := OrganizationAuditQuery{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"),
			MemberAccountID: values.Get("member_account_id"), SourceDeviceID: values.Get("source_device_id"),
			TargetDeviceID: values.Get("target_device_id"), ConnectionMethod: values.Get("connection_method"),
			Cursor: values.Get("cursor"),
		}
		var err error
		if query.StartTime, err = parseOrganizationAuditTime(values.Get("start_time"), "start_time"); err != nil {
			writeError(w, err)
			return
		}
		if query.EndTime, err = parseOrganizationAuditTime(values.Get("end_time"), "end_time"); err != nil {
			writeError(w, err)
			return
		}
		if raw := strings.TrimSpace(values.Get("relay_only")); raw != "" {
			query.RelayOnly, err = strconv.ParseBool(raw)
			if err != nil {
				writeError(w, fmt.Errorf("relay_only is invalid"))
				return
			}
		}
		if raw := strings.TrimSpace(values.Get("min_relay_bytes")); raw != "" {
			query.MinRelayBytes, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				writeError(w, fmt.Errorf("min_relay_bytes is invalid"))
				return
			}
		}
		if raw := strings.TrimSpace(values.Get("page_size")); raw != "" {
			query.PageSize, err = strconv.Atoi(raw)
			if err != nil {
				writeError(w, fmt.Errorf("page_size is invalid"))
				return
			}
		}
		page, err := service.QueryOrganizationAudit(r.Context(), query)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "page": page})
	})
	mux.HandleFunc("POST /api/organizations/{id}/audit/cleanup", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			ActorAccountID string `json:"actor_account_id,omitempty"`
			BatchSize      int    `json:"batch_size,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		result, err := service.CleanupOrganizationAudit(r.Context(), CleanupOrganizationAuditRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), BatchSize: req.BatchSize,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	})
	mux.HandleFunc("GET /api/organizations/{id}/deployment-bundles", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		bundles, err := service.ListDeploymentBundles(r.Context(), actorAccountID, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bundles": bundles})
	})
	mux.HandleFunc("POST /api/organizations/{id}/deployment-bundles", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			NetworkID    string                 `json:"network_id"`
			GroupID      string                 `json:"group_id,omitempty"`
			Platform     DeploymentPlatform     `json:"platform"`
			Architecture DeploymentArchitecture `json:"architecture"`
			TTLSeconds   int64                  `json:"ttl_seconds,omitempty"`
			MaxUses      int                    `json:"max_uses"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		result, err := service.CreateDeploymentBundle(r.Context(), CreateDeploymentBundleRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), NetworkID: req.NetworkID,
			GroupID: req.GroupID, Platform: req.Platform, Architecture: req.Architecture,
			TTL: time.Duration(req.TTLSeconds) * time.Second, MaxUses: req.MaxUses,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	})
	mux.HandleFunc("POST /api/organizations/{id}/bootstrap-credentials/{credential_id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var body struct{}
		if !decodeJSON(w, r, &body) {
			return
		}
		credential, err := service.RevokeBootstrapCredential(r.Context(), RevokeBootstrapCredentialRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), CredentialID: r.PathValue("credential_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "credential": credential})
	})
	mux.HandleFunc("POST /api/deployments/bootstrap/redeem", func(w http.ResponseWriter, r *http.Request) {
		var req RedeemBootstrapCredentialRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		result, err := service.RedeemBootstrapCredential(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	})
	mux.HandleFunc("GET /api/organizations/{id}/rollouts", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		rollouts, err := service.ListRollouts(r.Context(), actorAccountID, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rollouts": rollouts})
	})
	mux.HandleFunc("POST /api/organizations/{id}/rollouts", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var req struct {
			GroupID       string   `json:"group_id,omitempty"`
			DeviceIDs     []string `json:"device_ids,omitempty"`
			TargetVersion string   `json:"target_version"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		result, err := service.CreateRollout(r.Context(), CreateRolloutRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), GroupID: req.GroupID,
			DeviceIDs: req.DeviceIDs, TargetVersion: req.TargetVersion,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	})
	mux.HandleFunc("GET /api/organizations/{id}/rollouts/{rollout_id}", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		result, err := service.GetRollout(r.Context(), actorAccountID, r.PathValue("id"), r.PathValue("rollout_id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	})
	mux.HandleFunc("POST /api/organizations/{id}/rollouts/{rollout_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var body struct{}
		if !decodeJSON(w, r, &body) {
			return
		}
		result, err := service.CancelRollout(r.Context(), CancelRolloutRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), RolloutID: r.PathValue("rollout_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	})
	mux.HandleFunc("POST /api/organizations/{id}/rollouts/{rollout_id}/targets/{device_id}/retry", func(w http.ResponseWriter, r *http.Request) {
		actorAccountID, ok := requestActorAccountID(w, r)
		if !ok {
			return
		}
		var body struct{}
		if !decodeJSON(w, r, &body) {
			return
		}
		target, err := service.RetryRolloutTarget(r.Context(), RetryRolloutTargetRequest{
			ActorAccountID: actorAccountID, OrganizationID: r.PathValue("id"), RolloutID: r.PathValue("rollout_id"), DeviceID: r.PathValue("device_id"),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target})
	})
	mux.HandleFunc("POST /api/deployments/rollouts/{rollout_id}/targets/{device_id}/report", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Sequence       int64                  `json:"sequence"`
			Status         DeploymentTargetStatus `json:"status"`
			CurrentVersion string                 `json:"current_version,omitempty"`
			TargetVersion  string                 `json:"target_version"`
			ErrorCode      RolloutErrorCode       `json:"error_code,omitempty"`
			Fingerprint    string                 `json:"fingerprint"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		target, err := service.ReportRolloutTarget(r.Context(), ReportRolloutTargetRequest{
			RolloutID: r.PathValue("rollout_id"), DeviceID: r.PathValue("device_id"), Sequence: req.Sequence,
			Status: req.Status, CurrentVersion: req.CurrentVersion, TargetVersion: req.TargetVersion, ErrorCode: req.ErrorCode, Fingerprint: req.Fingerprint,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target})
	})
	mux.HandleFunc("POST /api/networks", func(w http.ResponseWriter, r *http.Request) {
		var req CreateNetworkRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		network, err := service.CreateNetwork(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "network": network})
	})
	mux.HandleFunc("POST /api/invites", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AccountID  string `json:"account_id"`
			NetworkID  string `json:"network_id"`
			TTLSeconds int64  `json:"ttl_seconds,omitempty"`
			MaxUses    int    `json:"max_uses,omitempty"`
			OneTime    bool   `json:"one_time,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		inviteReq := CreateInviteRequest{
			AccountID: req.AccountID,
			NetworkID: req.NetworkID,
			MaxUses:   req.MaxUses,
			OneTime:   req.OneTime,
		}
		if req.TTLSeconds > 0 {
			inviteReq.TTL = time.Duration(req.TTLSeconds) * time.Second
		}
		invite, err := service.CreateInvite(r.Context(), inviteReq)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite": invite})
	})
	mux.HandleFunc("POST /api/devices/join", func(w http.ResponseWriter, r *http.Request) {
		var req JoinDeviceRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.RemoteAddr == "" {
			req.RemoteAddr = r.RemoteAddr
		}
		device, err := service.JoinDevice(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
	})
	mux.HandleFunc("POST /api/devices/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var req HeartbeatDeviceRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		device, err := service.HeartbeatDevice(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
	})
	mux.HandleFunc("POST /api/devices/revoke", func(w http.ResponseWriter, r *http.Request) {
		var req RevokeDeviceRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		device, err := service.RevokeDevice(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
	})
	mux.HandleFunc("GET /api/risk-events", func(w http.ResponseWriter, r *http.Request) {
		events, err := service.ListRiskEvents(r.Context(), r.URL.Query().Get("account_id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "events": events})
	})
	mux.HandleFunc("GET /api/networks/{id}/devices", func(w http.ResponseWriter, r *http.Request) {
		devices, err := service.ListNetworkDevices(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": devices})
	})
	mux.HandleFunc("POST /api/p2p/candidates", func(w http.ResponseWriter, r *http.Request) {
		var req RegisterP2PCandidatesRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		for i := range req.Candidates {
			if req.Candidates[i].ObservedFrom == "" {
				req.Candidates[i].ObservedFrom = r.RemoteAddr
			}
		}
		candidates, err := service.RegisterP2PCandidates(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "candidates": candidates})
	})
	mux.HandleFunc("POST /api/p2p/candidates/query", func(w http.ResponseWriter, r *http.Request) {
		var req QueryP2PCandidatesRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		candidates, err := service.QueryP2PCandidates(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "candidates": candidates})
	})
	mux.HandleFunc("POST /api/p2p/negotiate", func(w http.ResponseWriter, r *http.Request) {
		var req NegotiateP2PConnectionRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		negotiation, err := service.NegotiateP2PConnection(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "negotiation": negotiation})
	})
	mux.HandleFunc("POST /api/relay/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AccountID      string `json:"account_id"`
			NetworkID      string `json:"network_id"`
			SourceDeviceID string `json:"source_device_id"`
			TargetDeviceID string `json:"target_device_id"`
			TTLSeconds     int64  `json:"ttl_seconds,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		relayReq := CreateRelaySessionRequest{
			AccountID:      req.AccountID,
			NetworkID:      req.NetworkID,
			SourceDeviceID: req.SourceDeviceID,
			TargetDeviceID: req.TargetDeviceID,
		}
		if req.TTLSeconds > 0 {
			relayReq.TTL = time.Duration(req.TTLSeconds) * time.Second
		}
		session, err := service.CreateRelaySession(r.Context(), relayReq)
		if err != nil {
			writeError(w, err)
			return
		}
		if cfg.relayEndpoint != "" {
			session.RelayEndpoint = cfg.relayEndpoint
		}
		if cfg.relaySessionCreated != nil {
			if err := cfg.relaySessionCreated(r.Context(), session); err != nil {
				_, _ = service.CloseRelaySession(r.Context(), CloseRelaySessionRequest{
					SessionID: session.Session.ID,
					Error:     "relay authorization failed",
				})
				writeError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
	})
	mux.HandleFunc("GET /api/relay/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		session, err := service.GetRelaySession(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
	})
	mux.HandleFunc("GET /api/relay/sessions/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		usage, err := service.ListRelaySessionUsage(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "usage": usage})
	})
	mux.HandleFunc("POST /api/relay/sessions/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RelayBytesIn  int64 `json:"relay_bytes_in,omitempty"`
			RelayBytesOut int64 `json:"relay_bytes_out,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		session, err := service.RecordRelaySessionUsage(r.Context(), RecordRelaySessionUsageRequest{
			SessionID:     r.PathValue("id"),
			RelayBytesIn:  req.RelayBytesIn,
			RelayBytesOut: req.RelayBytesOut,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
	})
	mux.HandleFunc("GET /api/relay/sessions/{id}/connection-logs", func(w http.ResponseWriter, r *http.Request) {
		logs, err := service.ListRelaySessionConnectionLogs(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "connection_logs": logs})
	})
	mux.HandleFunc("POST /api/relay/sessions/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RelayBytesIn  int64  `json:"relay_bytes_in,omitempty"`
			RelayBytesOut int64  `json:"relay_bytes_out,omitempty"`
			Error         string `json:"error,omitempty"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		session, err := service.CloseRelaySession(r.Context(), CloseRelaySessionRequest{
			SessionID:     r.PathValue("id"),
			RelayBytesIn:  req.RelayBytesIn,
			RelayBytesOut: req.RelayBytesOut,
			Error:         req.Error,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		if cfg.relaySessionClosed != nil {
			if err := cfg.relaySessionClosed(r.Context(), session); err != nil {
				writeError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
	})
	mux.HandleFunc("POST /api/subscriptions/webhooks/{provider}", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readLimitedBody(w, r, MaxSubscriptionWebhookBodyBytes)
		if !ok {
			return
		}
		result, err := service.ProcessSubscriptionWebhook(r.Context(), r.PathValue("provider"), r.Header, body)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"outcome":      result.Outcome,
			"subscription": result.Subscription,
		})
	})
	return mux
}

func parseOrganizationAuditTime(raw, field string) (*time.Time, error) {
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

func handleHealth(w http.ResponseWriter, _ *http.Request, cfg serverConfig) {
	body := map[string]any{"ok": true}
	if cfg.relayStatusProvider != nil {
		body["relay"] = cfg.relayStatusProvider()
	} else if cfg.relayEndpoint != "" {
		body["relay"] = RelayStatus{Enabled: true, Listen: cfg.relayEndpoint}
	} else {
		body["relay"] = RelayStatus{Enabled: false}
	}
	writeJSON(w, http.StatusOK, body)
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

func requestActorAccountID(w http.ResponseWriter, r *http.Request) (string, bool) {
	actorAccountID := strings.TrimSpace(r.Header.Get(ActorAccountHeader))
	if actorAccountID == "" {
		writeError(w, fmt.Errorf("trusted actor identity header is required: %w", ErrForbidden))
		return "", false
	}
	return actorAccountID, true
}

func readLimitedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	if r.Body == nil {
		writeError(w, errors.New("missing request body"))
		return nil, false
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	if int64(len(body)) > limit {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"ok":    false,
			"error": "request body is too large",
		})
		return nil, false
	}
	return body, true
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrRevoked) {
		status = http.StatusForbidden
	}
	if errors.Is(err, ErrExpired) {
		status = http.StatusGone
	}
	if errors.Is(err, ErrQuotaExceeded) {
		status = http.StatusTooManyRequests
	}
	if errors.Is(err, ErrConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, licensing.ErrBindingMismatch) || errors.Is(err, licensing.ErrPolicyDenied) || errors.Is(err, licensing.ErrUnavailable) {
		status = http.StatusForbidden
	}
	if errors.Is(err, licensing.ErrInvalidDocument) || errors.Is(err, licensing.ErrUnknownKey) ||
		errors.Is(err, licensing.ErrSignatureInvalid) || errors.Is(err, licensing.ErrNotEffective) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, licensing.ErrExpired) {
		status = http.StatusGone
	}
	body := map[string]any{
		"ok":    false,
		"error": err.Error(),
	}
	var quotaErr *QuotaError
	if errors.As(err, &quotaErr) {
		body["quota"] = quotaErr.Detail
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
