package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"meshlink/internal/p2p"
)

type Client struct {
	BaseURL        string
	HTTPClient     *http.Client
	Timeout        time.Duration
	ActorAccountID string
}

type APIError struct {
	StatusCode int
	Path       string
	Message    string
	Kind       error
	Quota      *QuotaErrorDetail
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("cloud hub request failed (%d): %s", e.StatusCode, msg)
	}
	return "cloud hub request failed: " + msg
}

func (e *APIError) Unwrap() error {
	return e.Kind
}

func (c *Client) Health(ctx context.Context) error {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := c.do(ctx, http.MethodGet, "/healthz", nil, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return &APIError{Path: "/healthz", Message: fallbackError(resp.Error)}
	}
	return nil
}

func (c *Client) GetMonitoringSnapshot(ctx context.Context) (MonitoringSnapshot, error) {
	const path = "/api/operations/monitoring"
	var resp struct {
		OK         bool               `json:"ok"`
		Error      string             `json:"error,omitempty"`
		Monitoring MonitoringSnapshot `json:"monitoring"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return MonitoringSnapshot{}, err
	}
	if !resp.OK {
		return MonitoringSnapshot{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	resp.Monitoring.Window = time.Duration(resp.Monitoring.WindowSeconds) * time.Second
	for i := range resp.Monitoring.ActiveAlerts {
		resp.Monitoring.ActiveAlerts[i].Window = time.Duration(resp.Monitoring.ActiveAlerts[i].WindowSeconds) * time.Second
	}
	for i := range resp.Monitoring.AlertEvents {
		resp.Monitoring.AlertEvents[i].Window = time.Duration(resp.Monitoring.AlertEvents[i].WindowSeconds) * time.Second
	}
	return resp.Monitoring, nil
}

func (c *Client) CreateAccount(ctx context.Context, req CreateAccountRequest) (Account, error) {
	var resp struct {
		OK      bool    `json:"ok"`
		Error   string  `json:"error,omitempty"`
		Account Account `json:"account"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/accounts", req, &resp); err != nil {
		return Account{}, err
	}
	if !resp.OK {
		return Account{}, &APIError{Path: "/api/accounts", Message: fallbackError(resp.Error)}
	}
	return resp.Account, nil
}

func (c *Client) ListPlans(ctx context.Context) ([]Plan, error) {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		Plans []Plan `json:"plans"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/plans", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: "/api/plans", Message: fallbackError(resp.Error)}
	}
	return resp.Plans, nil
}

func (c *Client) GetPlan(ctx context.Context, planID PlanID) (Plan, error) {
	path := "/api/plans/" + url.PathEscape(string(planID))
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		Plan  Plan   `json:"plan"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return Plan{}, err
	}
	if !resp.OK {
		return Plan{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Plan, nil
}

func (c *Client) GetAccountPolicyStatus(ctx context.Context, accountID string) (AccountPolicyStatus, error) {
	path := "/api/accounts/" + url.PathEscape(accountID) + "/policy"
	var resp struct {
		OK     bool                `json:"ok"`
		Error  string              `json:"error,omitempty"`
		Status AccountPolicyStatus `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return AccountPolicyStatus{}, err
	}
	if !resp.OK {
		return AccountPolicyStatus{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Status, nil
}

func (c *Client) GetAccountSubscriptionStatus(ctx context.Context, accountID string) (AccountSubscriptionStatus, error) {
	path := "/api/accounts/" + url.PathEscape(accountID) + "/subscription"
	var resp struct {
		OK           bool                      `json:"ok"`
		Error        string                    `json:"error,omitempty"`
		Subscription AccountSubscriptionStatus `json:"subscription"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return AccountSubscriptionStatus{}, err
	}
	if !resp.OK {
		return AccountSubscriptionStatus{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Subscription, nil
}

func (c *Client) GetAccountManagementSummary(ctx context.Context, accountID string) (AccountManagementSummary, error) {
	path := "/api/accounts/" + url.PathEscape(accountID) + "/summary"
	var resp struct {
		OK      bool                     `json:"ok"`
		Error   string                   `json:"error,omitempty"`
		Summary AccountManagementSummary `json:"summary"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return AccountManagementSummary{}, err
	}
	if !resp.OK {
		return AccountManagementSummary{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Summary, nil
}

func (c *Client) FreezeAccount(ctx context.Context, req AccountStatusChangeRequest) (Account, error) {
	return c.changeAccountStatus(ctx, req, "freeze")
}

func (c *Client) UnfreezeAccount(ctx context.Context, req AccountStatusChangeRequest) (Account, error) {
	return c.changeAccountStatus(ctx, req, "unfreeze")
}

func (c *Client) BanAccount(ctx context.Context, req AccountStatusChangeRequest) (Account, error) {
	return c.changeAccountStatus(ctx, req, "ban")
}

func (c *Client) changeAccountStatus(ctx context.Context, req AccountStatusChangeRequest, action string) (Account, error) {
	path := "/api/accounts/" + url.PathEscape(req.AccountID) + "/" + action
	body := struct {
		Reason string `json:"reason,omitempty"`
	}{
		Reason: req.Reason,
	}
	var resp struct {
		OK      bool    `json:"ok"`
		Error   string  `json:"error,omitempty"`
		Account Account `json:"account"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return Account{}, err
	}
	if !resp.OK {
		return Account{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Account, nil
}

func (c *Client) CreateOrganization(ctx context.Context, req CreateOrganizationRequest) (Organization, error) {
	var resp struct {
		OK           bool         `json:"ok"`
		Error        string       `json:"error,omitempty"`
		Organization Organization `json:"organization"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/organizations", req, &resp); err != nil {
		return Organization{}, err
	}
	if !resp.OK {
		return Organization{}, &APIError{Path: "/api/organizations", Message: fallbackError(resp.Error)}
	}
	return resp.Organization, nil
}

func (c *Client) GetOrganization(ctx context.Context, organizationID string) (Organization, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID)
	var resp struct {
		OK           bool         `json:"ok"`
		Error        string       `json:"error,omitempty"`
		Organization Organization `json:"organization"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return Organization{}, err
	}
	if !resp.OK {
		return Organization{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Organization, nil
}

func (c *Client) GetPrivateLicenseSummary(ctx context.Context, organizationID string) (PrivateLicenseSummary, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/private-license"
	var resp struct {
		OK      bool                  `json:"ok"`
		Error   string                `json:"error,omitempty"`
		License PrivateLicenseSummary `json:"license"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return PrivateLicenseSummary{}, err
	}
	if !resp.OK {
		return PrivateLicenseSummary{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.License, nil
}

func (c *Client) ImportPrivateLicense(ctx context.Context, req ImportPrivateLicenseRequest) (PrivateLicenseSummary, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/private-license"
	body := struct {
		SignedLicense string `json:"signed_license"`
	}{SignedLicense: string(req.SignedLicense)}
	var resp struct {
		OK      bool                  `json:"ok"`
		Error   string                `json:"error,omitempty"`
		License PrivateLicenseSummary `json:"license"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return PrivateLicenseSummary{}, err
	}
	if !resp.OK {
		return PrivateLicenseSummary{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.License, nil
}

func (c *Client) SuspendOrganization(ctx context.Context, req OrganizationStatusChangeRequest) (Organization, error) {
	return c.changeOrganizationStatus(ctx, req, "suspend")
}

func (c *Client) ResumeOrganization(ctx context.Context, req OrganizationStatusChangeRequest) (Organization, error) {
	return c.changeOrganizationStatus(ctx, req, "resume")
}

func (c *Client) changeOrganizationStatus(ctx context.Context, req OrganizationStatusChangeRequest, action string) (Organization, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/" + action
	body := struct {
		ActorAccountID string `json:"actor_account_id"`
		Reason         string `json:"reason,omitempty"`
	}{
		ActorAccountID: req.ActorAccountID,
		Reason:         req.Reason,
	}
	var resp struct {
		OK           bool         `json:"ok"`
		Error        string       `json:"error,omitempty"`
		Organization Organization `json:"organization"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return Organization{}, err
	}
	if !resp.OK {
		return Organization{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Organization, nil
}

func (c *Client) ListOrganizationMembers(ctx context.Context, organizationID string) ([]Membership, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/members"
	var resp struct {
		OK      bool         `json:"ok"`
		Error   string       `json:"error,omitempty"`
		Members []Membership `json:"members"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Members, nil
}

func (c *Client) CreateOrganizationInvite(ctx context.Context, req CreateOrganizationInviteRequest) (OrganizationInviteResult, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/invites"
	body := struct {
		ActorAccountID   string         `json:"actor_account_id"`
		InvitedAccountID string         `json:"invited_account_id"`
		Role             MembershipRole `json:"role,omitempty"`
		TTLSeconds       int64          `json:"ttl_seconds,omitempty"`
	}{
		ActorAccountID:   req.ActorAccountID,
		InvitedAccountID: req.InvitedAccountID,
		Role:             req.Role,
	}
	if req.TTL > 0 {
		body.TTLSeconds = int64(req.TTL / time.Second)
		if body.TTLSeconds <= 0 {
			body.TTLSeconds = 1
		}
	}
	var resp struct {
		OK     bool                     `json:"ok"`
		Error  string                   `json:"error,omitempty"`
		Invite OrganizationInviteResult `json:"invite"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return OrganizationInviteResult{}, err
	}
	if !resp.OK {
		return OrganizationInviteResult{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Invite, nil
}

func (c *Client) AcceptOrganizationInvite(ctx context.Context, req AcceptOrganizationInviteRequest) (Membership, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/invites/accept"
	body := struct {
		AccountID string `json:"account_id"`
		Token     string `json:"token"`
		Code      string `json:"code"`
	}{
		AccountID: req.AccountID,
		Token:     req.Token,
		Code:      req.Code,
	}
	var resp struct {
		OK     bool       `json:"ok"`
		Error  string     `json:"error,omitempty"`
		Member Membership `json:"member"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return Membership{}, err
	}
	if !resp.OK {
		return Membership{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Member, nil
}

func (c *Client) RevokeOrganizationInvite(ctx context.Context, req RevokeOrganizationInviteRequest) (OrganizationInvite, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/invites/" + url.PathEscape(req.InviteID) + "/revoke"
	body := struct {
		ActorAccountID string `json:"actor_account_id"`
		Reason         string `json:"reason,omitempty"`
	}{
		ActorAccountID: req.ActorAccountID,
		Reason:         req.Reason,
	}
	var resp struct {
		OK     bool               `json:"ok"`
		Error  string             `json:"error,omitempty"`
		Invite OrganizationInvite `json:"invite"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return OrganizationInvite{}, err
	}
	if !resp.OK {
		return OrganizationInvite{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Invite, nil
}

func (c *Client) SuspendOrganizationMember(ctx context.Context, req OrganizationMemberStatusRequest) (Membership, error) {
	return c.changeOrganizationMemberStatus(ctx, req, "suspend")
}

func (c *Client) ResumeOrganizationMember(ctx context.Context, req OrganizationMemberStatusRequest) (Membership, error) {
	return c.changeOrganizationMemberStatus(ctx, req, "resume")
}

func (c *Client) RemoveOrganizationMember(ctx context.Context, req OrganizationMemberStatusRequest) (Membership, error) {
	return c.changeOrganizationMemberStatus(ctx, req, "remove")
}

func (c *Client) changeOrganizationMemberStatus(ctx context.Context, req OrganizationMemberStatusRequest, action string) (Membership, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/members/" + url.PathEscape(req.AccountID) + "/" + action
	body := struct {
		ActorAccountID string `json:"actor_account_id"`
		Reason         string `json:"reason,omitempty"`
	}{
		ActorAccountID: req.ActorAccountID,
		Reason:         req.Reason,
	}
	var resp struct {
		OK     bool       `json:"ok"`
		Error  string     `json:"error,omitempty"`
		Member Membership `json:"member"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return Membership{}, err
	}
	if !resp.OK {
		return Membership{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Member, nil
}

func (c *Client) ChangeOrganizationMemberRole(ctx context.Context, req ChangeOrganizationMemberRoleRequest) (Membership, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/members/" + url.PathEscape(req.AccountID) + "/role"
	var resp struct {
		OK     bool       `json:"ok"`
		Error  string     `json:"error,omitempty"`
		Member Membership `json:"member"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"role": req.Role}, &resp); err != nil {
		return Membership{}, err
	}
	if !resp.OK {
		return Membership{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Member, nil
}

func (c *Client) CreateDeviceGroup(ctx context.Context, req CreateDeviceGroupRequest) (DeviceGroup, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/groups"
	var resp struct {
		OK    bool        `json:"ok"`
		Error string      `json:"error,omitempty"`
		Group DeviceGroup `json:"group"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"name": req.Name}, &resp); err != nil {
		return DeviceGroup{}, err
	}
	if !resp.OK {
		return DeviceGroup{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Group, nil
}

func (c *Client) GetDeviceGroup(ctx context.Context, organizationID, groupID string) (DeviceGroup, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/groups/" + url.PathEscape(groupID)
	var resp struct {
		OK    bool        `json:"ok"`
		Error string      `json:"error,omitempty"`
		Group DeviceGroup `json:"group"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return DeviceGroup{}, err
	}
	if !resp.OK {
		return DeviceGroup{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Group, nil
}

func (c *Client) ListDeviceGroups(ctx context.Context, organizationID string) ([]DeviceGroup, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/groups"
	var resp struct {
		OK     bool          `json:"ok"`
		Error  string        `json:"error,omitempty"`
		Groups []DeviceGroup `json:"groups"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Groups, nil
}

func (c *Client) RenameDeviceGroup(ctx context.Context, req RenameDeviceGroupRequest) (DeviceGroup, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/groups/" + url.PathEscape(req.GroupID) + "/rename"
	var resp struct {
		OK    bool        `json:"ok"`
		Error string      `json:"error,omitempty"`
		Group DeviceGroup `json:"group"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"name": req.Name}, &resp); err != nil {
		return DeviceGroup{}, err
	}
	if !resp.OK {
		return DeviceGroup{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Group, nil
}

func (c *Client) DeleteDeviceGroup(ctx context.Context, req DeleteDeviceGroupRequest) (DeviceGroup, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/groups/" + url.PathEscape(req.GroupID) + "/delete"
	var resp struct {
		OK    bool        `json:"ok"`
		Error string      `json:"error,omitempty"`
		Group DeviceGroup `json:"group"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{}, &resp); err != nil {
		return DeviceGroup{}, err
	}
	if !resp.OK {
		return DeviceGroup{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Group, nil
}

func (c *Client) ListOrganizationDevices(ctx context.Context, organizationID string) ([]OrganizationDevice, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/devices"
	var resp struct {
		OK      bool                 `json:"ok"`
		Error   string               `json:"error,omitempty"`
		Devices []OrganizationDevice `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Devices, nil
}

func (c *Client) EnrollOrganizationDevice(ctx context.Context, req EnrollOrganizationDeviceRequest) (OrganizationDevice, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/devices/" + url.PathEscape(req.DeviceID) + "/enroll"
	return c.organizationDeviceMutation(ctx, path, map[string]any{})
}

func (c *Client) RemoveOrganizationDevice(ctx context.Context, req RemoveOrganizationDeviceRequest) (OrganizationDevice, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/devices/" + url.PathEscape(req.DeviceID) + "/remove"
	return c.organizationDeviceMutation(ctx, path, map[string]any{})
}

func (c *Client) AddDeviceToGroup(ctx context.Context, req OrganizationDeviceGroupRequest) (OrganizationDevice, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/groups/" + url.PathEscape(req.GroupID) + "/devices/" + url.PathEscape(req.DeviceID) + "/add"
	return c.organizationDeviceMutation(ctx, path, map[string]any{})
}

func (c *Client) RemoveDeviceFromGroup(ctx context.Context, req OrganizationDeviceGroupRequest) (OrganizationDevice, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/groups/" + url.PathEscape(req.GroupID) + "/devices/" + url.PathEscape(req.DeviceID) + "/remove"
	return c.organizationDeviceMutation(ctx, path, map[string]any{})
}

func (c *Client) organizationDeviceMutation(ctx context.Context, path string, body any) (OrganizationDevice, error) {
	var resp struct {
		OK     bool               `json:"ok"`
		Error  string             `json:"error,omitempty"`
		Device OrganizationDevice `json:"device"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return OrganizationDevice{}, err
	}
	if !resp.OK {
		return OrganizationDevice{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Device, nil
}

func (c *Client) GrantConnectionAccess(ctx context.Context, req ConnectionGrantRequest) (ConnectionGrant, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/connection-grants"
	body := map[string]any{"member_account_id": req.MemberAccountID}
	if req.DeviceID != "" {
		body["device_id"] = req.DeviceID
	}
	if req.GroupID != "" {
		body["group_id"] = req.GroupID
	}
	var resp struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error,omitempty"`
		Grant ConnectionGrant `json:"grant"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return ConnectionGrant{}, err
	}
	if !resp.OK {
		return ConnectionGrant{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Grant, nil
}

func (c *Client) ListConnectionGrants(ctx context.Context, organizationID string) ([]ConnectionGrant, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/connection-grants"
	var resp struct {
		OK     bool              `json:"ok"`
		Error  string            `json:"error,omitempty"`
		Grants []ConnectionGrant `json:"grants"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Grants, nil
}

func (c *Client) RevokeConnectionAccess(ctx context.Context, req RevokeConnectionGrantRequest) (ConnectionGrant, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/connection-grants/" + url.PathEscape(req.GrantID) + "/revoke"
	var resp struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error,omitempty"`
		Grant ConnectionGrant `json:"grant"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{}, &resp); err != nil {
		return ConnectionGrant{}, err
	}
	if !resp.OK {
		return ConnectionGrant{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Grant, nil
}

func (c *Client) QueryOrganizationAudit(ctx context.Context, query OrganizationAuditQuery) (OrganizationAuditPage, error) {
	path := "/api/organizations/" + url.PathEscape(query.OrganizationID) + "/audit"
	values := url.Values{}
	if query.MemberAccountID != "" {
		values.Set("member_account_id", query.MemberAccountID)
	}
	if query.SourceDeviceID != "" {
		values.Set("source_device_id", query.SourceDeviceID)
	}
	if query.TargetDeviceID != "" {
		values.Set("target_device_id", query.TargetDeviceID)
	}
	if query.StartTime != nil {
		values.Set("start_time", query.StartTime.UTC().Format(time.RFC3339Nano))
	}
	if query.EndTime != nil {
		values.Set("end_time", query.EndTime.UTC().Format(time.RFC3339Nano))
	}
	if query.ConnectionMethod != "" {
		values.Set("connection_method", query.ConnectionMethod)
	}
	if query.RelayOnly {
		values.Set("relay_only", "true")
	}
	if query.MinRelayBytes != 0 {
		values.Set("min_relay_bytes", fmt.Sprintf("%d", query.MinRelayBytes))
	}
	if query.PageSize != 0 {
		values.Set("page_size", fmt.Sprintf("%d", query.PageSize))
	}
	if query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		OK    bool                  `json:"ok"`
		Error string                `json:"error,omitempty"`
		Page  OrganizationAuditPage `json:"page"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return OrganizationAuditPage{}, err
	}
	if !resp.OK {
		return OrganizationAuditPage{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Page, nil
}

func (c *Client) CleanupOrganizationAudit(ctx context.Context, req CleanupOrganizationAuditRequest) (OrganizationAuditCleanupResult, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/audit/cleanup"
	var resp struct {
		OK     bool                           `json:"ok"`
		Error  string                         `json:"error,omitempty"`
		Result OrganizationAuditCleanupResult `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"batch_size": req.BatchSize}, &resp); err != nil {
		return OrganizationAuditCleanupResult{}, err
	}
	if !resp.OK {
		return OrganizationAuditCleanupResult{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Result, nil
}

func (c *Client) CreateDeploymentBundle(ctx context.Context, req CreateDeploymentBundleRequest) (DeploymentBundleResult, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/deployment-bundles"
	body := struct {
		NetworkID    string                 `json:"network_id"`
		GroupID      string                 `json:"group_id,omitempty"`
		Platform     DeploymentPlatform     `json:"platform"`
		Architecture DeploymentArchitecture `json:"architecture"`
		TTLSeconds   int64                  `json:"ttl_seconds,omitempty"`
		MaxUses      int                    `json:"max_uses"`
	}{req.NetworkID, req.GroupID, req.Platform, req.Architecture, 0, req.MaxUses}
	if req.TTL > 0 {
		body.TTLSeconds = int64(req.TTL / time.Second)
	}
	var resp struct {
		OK     bool                   `json:"ok"`
		Error  string                 `json:"error,omitempty"`
		Result DeploymentBundleResult `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return DeploymentBundleResult{}, err
	}
	if !resp.OK {
		return DeploymentBundleResult{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Result, nil
}

func (c *Client) ListDeploymentBundles(ctx context.Context, organizationID string) ([]DeploymentBundle, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/deployment-bundles"
	var resp struct {
		OK      bool               `json:"ok"`
		Error   string             `json:"error,omitempty"`
		Bundles []DeploymentBundle `json:"bundles"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Bundles, nil
}

func (c *Client) RevokeBootstrapCredential(ctx context.Context, req RevokeBootstrapCredentialRequest) (BootstrapCredential, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/bootstrap-credentials/" + url.PathEscape(req.CredentialID) + "/revoke"
	var resp struct {
		OK         bool                `json:"ok"`
		Error      string              `json:"error,omitempty"`
		Credential BootstrapCredential `json:"credential"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{}, &resp); err != nil {
		return BootstrapCredential{}, err
	}
	if !resp.OK {
		return BootstrapCredential{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Credential, nil
}

func (c *Client) RedeemBootstrapCredential(ctx context.Context, req RedeemBootstrapCredentialRequest) (BootstrapRedemption, error) {
	const path = "/api/deployments/bootstrap/redeem"
	var resp struct {
		OK     bool                `json:"ok"`
		Error  string              `json:"error,omitempty"`
		Result BootstrapRedemption `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, path, req, &resp); err != nil {
		return BootstrapRedemption{}, err
	}
	if !resp.OK {
		return BootstrapRedemption{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Result, nil
}

func (c *Client) CreateRollout(ctx context.Context, req CreateRolloutRequest) (RolloutResult, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/rollouts"
	body := map[string]any{"target_version": req.TargetVersion}
	if req.GroupID != "" {
		body["group_id"] = req.GroupID
	}
	if len(req.DeviceIDs) > 0 {
		body["device_ids"] = req.DeviceIDs
	}
	var resp struct {
		OK     bool          `json:"ok"`
		Error  string        `json:"error,omitempty"`
		Result RolloutResult `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return RolloutResult{}, err
	}
	if !resp.OK {
		return RolloutResult{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Result, nil
}

func (c *Client) ListRollouts(ctx context.Context, organizationID string) ([]Rollout, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/rollouts"
	var resp struct {
		OK       bool      `json:"ok"`
		Error    string    `json:"error,omitempty"`
		Rollouts []Rollout `json:"rollouts"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Rollouts, nil
}

func (c *Client) GetRollout(ctx context.Context, organizationID, rolloutID string) (RolloutResult, error) {
	path := "/api/organizations/" + url.PathEscape(organizationID) + "/rollouts/" + url.PathEscape(rolloutID)
	var resp struct {
		OK     bool          `json:"ok"`
		Error  string        `json:"error,omitempty"`
		Result RolloutResult `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return RolloutResult{}, err
	}
	if !resp.OK {
		return RolloutResult{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Result, nil
}

func (c *Client) CancelRollout(ctx context.Context, req CancelRolloutRequest) (RolloutResult, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/rollouts/" + url.PathEscape(req.RolloutID) + "/cancel"
	var resp struct {
		OK     bool          `json:"ok"`
		Error  string        `json:"error,omitempty"`
		Result RolloutResult `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{}, &resp); err != nil {
		return RolloutResult{}, err
	}
	if !resp.OK {
		return RolloutResult{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Result, nil
}

func (c *Client) RetryRolloutTarget(ctx context.Context, req RetryRolloutTargetRequest) (DeploymentTarget, error) {
	path := "/api/organizations/" + url.PathEscape(req.OrganizationID) + "/rollouts/" + url.PathEscape(req.RolloutID) + "/targets/" + url.PathEscape(req.DeviceID) + "/retry"
	var resp struct {
		OK     bool             `json:"ok"`
		Error  string           `json:"error,omitempty"`
		Target DeploymentTarget `json:"target"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{}, &resp); err != nil {
		return DeploymentTarget{}, err
	}
	if !resp.OK {
		return DeploymentTarget{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Target, nil
}

func (c *Client) ReportRolloutTarget(ctx context.Context, req ReportRolloutTargetRequest) (DeploymentTarget, error) {
	path := "/api/deployments/rollouts/" + url.PathEscape(req.RolloutID) + "/targets/" + url.PathEscape(req.DeviceID) + "/report"
	body := struct {
		Sequence       int64                  `json:"sequence"`
		Status         DeploymentTargetStatus `json:"status"`
		CurrentVersion string                 `json:"current_version,omitempty"`
		TargetVersion  string                 `json:"target_version"`
		ErrorCode      RolloutErrorCode       `json:"error_code,omitempty"`
		Fingerprint    string                 `json:"fingerprint"`
	}{req.Sequence, req.Status, req.CurrentVersion, req.TargetVersion, req.ErrorCode, req.Fingerprint}
	var resp struct {
		OK     bool             `json:"ok"`
		Error  string           `json:"error,omitempty"`
		Target DeploymentTarget `json:"target"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return DeploymentTarget{}, err
	}
	if !resp.OK {
		return DeploymentTarget{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Target, nil
}

func (c *Client) CreateNetwork(ctx context.Context, req CreateNetworkRequest) (Network, error) {
	var resp struct {
		OK      bool    `json:"ok"`
		Error   string  `json:"error,omitempty"`
		Network Network `json:"network"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/networks", req, &resp); err != nil {
		return Network{}, err
	}
	if !resp.OK {
		return Network{}, &APIError{Path: "/api/networks", Message: fallbackError(resp.Error)}
	}
	return resp.Network, nil
}

func (c *Client) CreateInvite(ctx context.Context, req CreateInviteRequest) (InviteResult, error) {
	body := struct {
		AccountID  string `json:"account_id"`
		NetworkID  string `json:"network_id"`
		TTLSeconds int64  `json:"ttl_seconds,omitempty"`
		MaxUses    int    `json:"max_uses,omitempty"`
		OneTime    bool   `json:"one_time,omitempty"`
	}{
		AccountID: req.AccountID,
		NetworkID: req.NetworkID,
		MaxUses:   req.MaxUses,
		OneTime:   req.OneTime,
	}
	if req.TTL > 0 {
		body.TTLSeconds = int64(req.TTL / time.Second)
		if body.TTLSeconds <= 0 {
			body.TTLSeconds = 1
		}
	}
	var resp struct {
		OK     bool         `json:"ok"`
		Error  string       `json:"error,omitempty"`
		Invite InviteResult `json:"invite"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/invites", body, &resp); err != nil {
		return InviteResult{}, err
	}
	if !resp.OK {
		return InviteResult{}, &APIError{Path: "/api/invites", Message: fallbackError(resp.Error)}
	}
	return resp.Invite, nil
}

func (c *Client) JoinDevice(ctx context.Context, req JoinDeviceRequest) (Device, error) {
	var resp struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
		Device Device `json:"device"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/devices/join", req, &resp); err != nil {
		return Device{}, err
	}
	if !resp.OK {
		return Device{}, &APIError{Path: "/api/devices/join", Message: fallbackError(resp.Error)}
	}
	return resp.Device, nil
}

func (c *Client) HeartbeatDevice(ctx context.Context, req HeartbeatDeviceRequest) (Device, error) {
	var resp struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
		Device Device `json:"device"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/devices/heartbeat", req, &resp); err != nil {
		return Device{}, err
	}
	if !resp.OK {
		return Device{}, &APIError{Path: "/api/devices/heartbeat", Message: fallbackError(resp.Error)}
	}
	return resp.Device, nil
}

func (c *Client) RevokeDevice(ctx context.Context, req RevokeDeviceRequest) (Device, error) {
	var resp struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
		Device Device `json:"device"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/devices/revoke", req, &resp); err != nil {
		return Device{}, err
	}
	if !resp.OK {
		return Device{}, &APIError{Path: "/api/devices/revoke", Message: fallbackError(resp.Error)}
	}
	return resp.Device, nil
}

func (c *Client) ListRiskEvents(ctx context.Context, accountID string) ([]RiskEvent, error) {
	path := "/api/risk-events"
	if strings.TrimSpace(accountID) != "" {
		path += "?account_id=" + url.QueryEscape(accountID)
	}
	var resp struct {
		OK     bool        `json:"ok"`
		Error  string      `json:"error,omitempty"`
		Events []RiskEvent `json:"events"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Events, nil
}

func (c *Client) ListNetworkDevices(ctx context.Context, networkID string) ([]Device, error) {
	path := "/api/networks/" + url.PathEscape(networkID) + "/devices"
	var resp struct {
		OK      bool     `json:"ok"`
		Error   string   `json:"error,omitempty"`
		Devices []Device `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Devices, nil
}

func (c *Client) RegisterP2PCandidates(ctx context.Context, req RegisterP2PCandidatesRequest) ([]p2p.Candidate, error) {
	var resp struct {
		OK         bool            `json:"ok"`
		Error      string          `json:"error,omitempty"`
		Candidates []p2p.Candidate `json:"candidates"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/p2p/candidates", req, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: "/api/p2p/candidates", Message: fallbackError(resp.Error)}
	}
	return resp.Candidates, nil
}

func (c *Client) QueryP2PCandidates(ctx context.Context, req QueryP2PCandidatesRequest) ([]p2p.Candidate, error) {
	var resp struct {
		OK         bool            `json:"ok"`
		Error      string          `json:"error,omitempty"`
		Candidates []p2p.Candidate `json:"candidates"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/p2p/candidates/query", req, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: "/api/p2p/candidates/query", Message: fallbackError(resp.Error)}
	}
	return resp.Candidates, nil
}

func (c *Client) NegotiateP2PConnection(ctx context.Context, req NegotiateP2PConnectionRequest) (p2p.Negotiation, error) {
	var resp struct {
		OK          bool            `json:"ok"`
		Error       string          `json:"error,omitempty"`
		Negotiation p2p.Negotiation `json:"negotiation"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/p2p/negotiate", req, &resp); err != nil {
		return p2p.Negotiation{}, err
	}
	if !resp.OK {
		return p2p.Negotiation{}, &APIError{Path: "/api/p2p/negotiate", Message: fallbackError(resp.Error)}
	}
	return resp.Negotiation, nil
}

func (c *Client) CreateRelaySession(ctx context.Context, req CreateRelaySessionRequest) (RelaySessionResult, error) {
	body := struct {
		AccountID      string `json:"account_id"`
		NetworkID      string `json:"network_id"`
		SourceDeviceID string `json:"source_device_id"`
		TargetDeviceID string `json:"target_device_id"`
		TTLSeconds     int64  `json:"ttl_seconds,omitempty"`
	}{
		AccountID:      req.AccountID,
		NetworkID:      req.NetworkID,
		SourceDeviceID: req.SourceDeviceID,
		TargetDeviceID: req.TargetDeviceID,
	}
	if req.TTL > 0 {
		body.TTLSeconds = int64(req.TTL / time.Second)
		if body.TTLSeconds <= 0 {
			body.TTLSeconds = 1
		}
	}
	var resp struct {
		OK      bool               `json:"ok"`
		Error   string             `json:"error,omitempty"`
		Session RelaySessionResult `json:"session"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/relay/sessions", body, &resp); err != nil {
		return RelaySessionResult{}, err
	}
	if !resp.OK {
		return RelaySessionResult{}, &APIError{Path: "/api/relay/sessions", Message: fallbackError(resp.Error)}
	}
	return resp.Session, nil
}

func (c *Client) GetRelaySession(ctx context.Context, sessionID string) (RelaySession, error) {
	path := "/api/relay/sessions/" + url.PathEscape(sessionID)
	var resp struct {
		OK      bool         `json:"ok"`
		Error   string       `json:"error,omitempty"`
		Session RelaySession `json:"session"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return RelaySession{}, err
	}
	if !resp.OK {
		return RelaySession{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Session, nil
}

func (c *Client) RecordRelaySessionUsage(ctx context.Context, req RecordRelaySessionUsageRequest) (RelaySession, error) {
	path := "/api/relay/sessions/" + url.PathEscape(req.SessionID) + "/usage"
	body := struct {
		RelayBytesIn  int64 `json:"relay_bytes_in,omitempty"`
		RelayBytesOut int64 `json:"relay_bytes_out,omitempty"`
	}{
		RelayBytesIn:  req.RelayBytesIn,
		RelayBytesOut: req.RelayBytesOut,
	}
	var resp struct {
		OK      bool         `json:"ok"`
		Error   string       `json:"error,omitempty"`
		Session RelaySession `json:"session"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return RelaySession{}, err
	}
	if !resp.OK {
		return RelaySession{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Session, nil
}

func (c *Client) ListRelaySessionUsage(ctx context.Context, sessionID string) ([]RelayUsage, error) {
	path := "/api/relay/sessions/" + url.PathEscape(sessionID) + "/usage"
	var resp struct {
		OK    bool         `json:"ok"`
		Error string       `json:"error,omitempty"`
		Usage []RelayUsage `json:"usage"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Usage, nil
}

func (c *Client) ListRelaySessionConnectionLogs(ctx context.Context, sessionID string) ([]ConnectionLog, error) {
	path := "/api/relay/sessions/" + url.PathEscape(sessionID) + "/connection-logs"
	var resp struct {
		OK             bool            `json:"ok"`
		Error          string          `json:"error,omitempty"`
		ConnectionLogs []ConnectionLog `json:"connection_logs"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.ConnectionLogs, nil
}

func (c *Client) CloseRelaySession(ctx context.Context, req CloseRelaySessionRequest) (RelaySession, error) {
	path := "/api/relay/sessions/" + url.PathEscape(req.SessionID) + "/close"
	body := struct {
		RelayBytesIn  int64  `json:"relay_bytes_in,omitempty"`
		RelayBytesOut int64  `json:"relay_bytes_out,omitempty"`
		Error         string `json:"error,omitempty"`
	}{
		RelayBytesIn:  req.RelayBytesIn,
		RelayBytesOut: req.RelayBytesOut,
		Error:         req.Error,
	}
	var resp struct {
		OK      bool         `json:"ok"`
		Error   string       `json:"error,omitempty"`
		Session RelaySession `json:"session"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return RelaySession{}, err
	}
	if !resp.OK {
		return RelaySession{}, &APIError{Path: path, Message: fallbackError(resp.Error)}
	}
	return resp.Session, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, dest any) error {
	base, err := c.normalizedBaseURL()
	if err != nil {
		return err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if actorAccountID := strings.TrimSpace(c.ActorAccountID); actorAccountID != "" {
		req.Header.Set(ActorAccountHeader, actorAccountID)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cloud hub request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, quota := decodeErrorResponse(resp.Body)
		return &APIError{
			StatusCode: resp.StatusCode,
			Path:       path,
			Message:    message,
			Kind:       classifyAPIError(resp.StatusCode, message),
			Quota:      quota,
		}
	}
	if dest == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decode cloud hub response: %w", err)
	}
	return nil
}

func (c *Client) normalizedBaseURL() (string, error) {
	raw := strings.TrimSpace(c.BaseURL)
	if raw == "" {
		return "", fmt.Errorf("hub API URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("hub API URL is invalid")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func decodeErrorResponse(body io.Reader) (string, *QuotaErrorDetail) {
	var out struct {
		Error string            `json:"error"`
		Quota *QuotaErrorDetail `json:"quota,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&out); err == nil && strings.TrimSpace(out.Error) != "" {
		return out.Error, out.Quota
	}
	return "request failed", nil
}

func classifyAPIError(status int, message string) error {
	lower := strings.ToLower(message)
	switch {
	case status == http.StatusNotFound || strings.Contains(lower, "not found"):
		return ErrNotFound
	case strings.Contains(lower, "revoked"):
		return ErrRevoked
	case status == http.StatusGone || strings.Contains(lower, "expired"):
		return ErrExpired
	case status == http.StatusTooManyRequests || strings.Contains(lower, "quota exceeded"):
		return ErrQuotaExceeded
	case status == http.StatusConflict || strings.Contains(lower, "conflict"):
		return ErrConflict
	case status == http.StatusForbidden || strings.Contains(lower, "forbidden") || strings.Contains(lower, "banned"):
		return ErrForbidden
	default:
		return nil
	}
}

func fallbackError(message string) string {
	if strings.TrimSpace(message) == "" {
		return "request failed"
	}
	return message
}
