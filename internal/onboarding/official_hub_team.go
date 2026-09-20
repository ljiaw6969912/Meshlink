package onboarding

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meshlink/internal/cloudhub"
)

type OfficialHubOrganizationRequest struct {
	HubAPIURL string `json:"hub_api_url,omitempty"`
	Name      string `json:"name"`
}

type OfficialHubTeamRequest struct {
	HubAPIURL      string `json:"hub_api_url,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
}

type OfficialHubPrivateLicenseRequest struct {
	HubAPIURL      string `json:"hub_api_url,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	SignedLicense  string `json:"signed_license"`
}

type OfficialHubMemberRoleRequest struct {
	HubAPIURL      string                  `json:"hub_api_url,omitempty"`
	OrganizationID string                  `json:"organization_id,omitempty"`
	AccountID      string                  `json:"account_id"`
	Role           cloudhub.MembershipRole `json:"role"`
}

type OfficialHubGroupRequest struct {
	HubAPIURL      string `json:"hub_api_url,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	GroupID        string `json:"group_id,omitempty"`
	Name           string `json:"name,omitempty"`
}

type OfficialHubTeamDeviceRequest struct {
	HubAPIURL      string `json:"hub_api_url,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	GroupID        string `json:"group_id,omitempty"`
	Action         string `json:"action,omitempty"`
}

type OfficialHubConnectionGrantRequest struct {
	HubAPIURL       string `json:"hub_api_url,omitempty"`
	OrganizationID  string `json:"organization_id,omitempty"`
	MemberAccountID string `json:"member_account_id,omitempty"`
	DeviceID        string `json:"device_id,omitempty"`
	GroupID         string `json:"group_id,omitempty"`
	GrantID         string `json:"grant_id,omitempty"`
}

type OfficialHubAuditRequest struct {
	HubAPIURL        string     `json:"hub_api_url,omitempty"`
	OrganizationID   string     `json:"organization_id,omitempty"`
	MemberAccountID  string     `json:"member_account_id,omitempty"`
	SourceDeviceID   string     `json:"source_device_id,omitempty"`
	TargetDeviceID   string     `json:"target_device_id,omitempty"`
	StartTime        *time.Time `json:"start_time,omitempty"`
	EndTime          *time.Time `json:"end_time,omitempty"`
	ConnectionMethod string     `json:"connection_method,omitempty"`
	RelayOnly        bool       `json:"relay_only,omitempty"`
	MinRelayBytes    int64      `json:"min_relay_bytes,omitempty"`
	PageSize         int        `json:"page_size,omitempty"`
	Cursor           string     `json:"cursor,omitempty"`
}

type OfficialHubAuditCleanupRequest struct {
	HubAPIURL      string `json:"hub_api_url,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	BatchSize      int    `json:"batch_size,omitempty"`
}

type OfficialHubDeploymentBundleRequest struct {
	HubAPIURL      string                          `json:"hub_api_url,omitempty"`
	OrganizationID string                          `json:"organization_id,omitempty"`
	NetworkID      string                          `json:"network_id,omitempty"`
	GroupID        string                          `json:"group_id,omitempty"`
	Platform       cloudhub.DeploymentPlatform     `json:"platform"`
	Architecture   cloudhub.DeploymentArchitecture `json:"architecture"`
	TTLSeconds     int64                           `json:"ttl_seconds,omitempty"`
	MaxUses        int                             `json:"max_uses"`
}

type OfficialHubDeploymentCredentialRequest struct {
	HubAPIURL      string `json:"hub_api_url,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	CredentialID   string `json:"credential_id"`
}

type OfficialHubBootstrapRedeemRequest struct {
	HubAPIURL      string                          `json:"hub_api_url,omitempty"`
	Credential     string                          `json:"credential"`
	OrganizationID string                          `json:"organization_id"`
	GroupID        string                          `json:"group_id,omitempty"`
	Platform       cloudhub.DeploymentPlatform     `json:"platform"`
	Architecture   cloudhub.DeploymentArchitecture `json:"architecture"`
	DeviceName     string                          `json:"device_name"`
	Fingerprint    string                          `json:"fingerprint"`
	CurrentVersion string                          `json:"current_version,omitempty"`
}

type OfficialHubRolloutRequest struct {
	HubAPIURL      string   `json:"hub_api_url,omitempty"`
	OrganizationID string   `json:"organization_id,omitempty"`
	RolloutID      string   `json:"rollout_id,omitempty"`
	GroupID        string   `json:"group_id,omitempty"`
	DeviceIDs      []string `json:"device_ids,omitempty"`
	DeviceID       string   `json:"device_id,omitempty"`
	TargetVersion  string   `json:"target_version,omitempty"`
}

type OfficialHubTeamResult struct {
	Organization cloudhub.Organization         `json:"organization"`
	Members      []cloudhub.Membership         `json:"members"`
	Groups       []cloudhub.DeviceGroup        `json:"groups"`
	Devices      []cloudhub.OrganizationDevice `json:"devices"`
	Grants       []cloudhub.ConnectionGrant    `json:"grants"`
	State        OfficialHubState              `json:"state"`
}

func (m Manager) CreateOfficialHubOrganization(ctx context.Context, req OfficialHubOrganizationRequest) (cloudhub.Organization, OfficialHubState, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return cloudhub.Organization{}, OfficialHubState{}, err
	}
	if strings.TrimSpace(state.AccountID) == "" {
		return cloudhub.Organization{}, OfficialHubState{}, fmt.Errorf("account_id is required; please create or sign in to an account first")
	}
	client := m.officialHubClient(baseURL)
	client.ActorAccountID = state.AccountID
	organization, err := client.CreateOrganization(ctx, cloudhub.CreateOrganizationRequest{
		OwnerAccountID: state.AccountID,
		Name:           strings.TrimSpace(req.Name),
	})
	if err != nil {
		return cloudhub.Organization{}, OfficialHubState{}, err
	}
	state.HubAPIURL = baseURL
	state.OrganizationID = organization.ID
	state.OrganizationName = organization.Name
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return cloudhub.Organization{}, OfficialHubState{}, err
	}
	return organization, state.withSuggestion(), nil
}

func (m Manager) OfficialHubTeam(ctx context.Context, req OfficialHubTeamRequest) (OfficialHubTeamResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubTeamResult{}, err
	}
	client, organizationID, err := m.officialHubTeamClient(baseURL, state, req.OrganizationID)
	if err != nil {
		return OfficialHubTeamResult{}, err
	}
	organization, err := client.GetOrganization(ctx, organizationID)
	if err != nil {
		return OfficialHubTeamResult{}, err
	}
	members, err := client.ListOrganizationMembers(ctx, organizationID)
	if err != nil {
		return OfficialHubTeamResult{}, err
	}
	groups, err := client.ListDeviceGroups(ctx, organizationID)
	if err != nil {
		return OfficialHubTeamResult{}, err
	}
	devices, err := client.ListOrganizationDevices(ctx, organizationID)
	if err != nil {
		return OfficialHubTeamResult{}, err
	}
	grants, err := client.ListConnectionGrants(ctx, organizationID)
	if err != nil {
		return OfficialHubTeamResult{}, err
	}
	return OfficialHubTeamResult{
		Organization: organization, Members: members, Groups: groups, Devices: devices, Grants: grants, State: state.withSuggestion(),
	}, nil
}

func (m Manager) GetOfficialHubPrivateLicense(ctx context.Context, req OfficialHubTeamRequest) (cloudhub.PrivateLicenseSummary, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.PrivateLicenseSummary{}, err
	}
	return client.GetPrivateLicenseSummary(ctx, organizationID)
}

func (m Manager) ImportOfficialHubPrivateLicense(ctx context.Context, req OfficialHubPrivateLicenseRequest) (cloudhub.PrivateLicenseSummary, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.PrivateLicenseSummary{}, err
	}
	if strings.TrimSpace(req.SignedLicense) == "" {
		return cloudhub.PrivateLicenseSummary{}, fmt.Errorf("signed_license is required")
	}
	return client.ImportPrivateLicense(ctx, cloudhub.ImportPrivateLicenseRequest{OrganizationID: organizationID, SignedLicense: []byte(req.SignedLicense)})
}

func (m Manager) ChangeOfficialHubMemberRole(ctx context.Context, req OfficialHubMemberRoleRequest) (cloudhub.Membership, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.Membership{}, err
	}
	return client.ChangeOrganizationMemberRole(ctx, cloudhub.ChangeOrganizationMemberRoleRequest{
		OrganizationID: organizationID, AccountID: strings.TrimSpace(req.AccountID), Role: req.Role,
	})
}

func (m Manager) CreateOfficialHubGroup(ctx context.Context, req OfficialHubGroupRequest) (cloudhub.DeviceGroup, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.DeviceGroup{}, err
	}
	return client.CreateDeviceGroup(ctx, cloudhub.CreateDeviceGroupRequest{OrganizationID: organizationID, Name: strings.TrimSpace(req.Name)})
}

func (m Manager) RenameOfficialHubGroup(ctx context.Context, req OfficialHubGroupRequest) (cloudhub.DeviceGroup, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.DeviceGroup{}, err
	}
	return client.RenameDeviceGroup(ctx, cloudhub.RenameDeviceGroupRequest{OrganizationID: organizationID, GroupID: strings.TrimSpace(req.GroupID), Name: strings.TrimSpace(req.Name)})
}

func (m Manager) DeleteOfficialHubGroup(ctx context.Context, req OfficialHubGroupRequest) (cloudhub.DeviceGroup, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.DeviceGroup{}, err
	}
	return client.DeleteDeviceGroup(ctx, cloudhub.DeleteDeviceGroupRequest{OrganizationID: organizationID, GroupID: strings.TrimSpace(req.GroupID)})
}

func (m Manager) EnrollOfficialHubTeamDevice(ctx context.Context, req OfficialHubTeamDeviceRequest) (cloudhub.OrganizationDevice, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.OrganizationDevice{}, err
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		state, stateErr := m.readOfficialHubState()
		if stateErr != nil {
			return cloudhub.OrganizationDevice{}, stateErr
		}
		deviceID = state.DeviceID
	}
	return client.EnrollOrganizationDevice(ctx, cloudhub.EnrollOrganizationDeviceRequest{OrganizationID: organizationID, DeviceID: deviceID})
}

func (m Manager) ChangeOfficialHubTeamDeviceGroup(ctx context.Context, req OfficialHubTeamDeviceRequest) (cloudhub.OrganizationDevice, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.OrganizationDevice{}, err
	}
	mutation := cloudhub.OrganizationDeviceGroupRequest{
		OrganizationID: organizationID, DeviceID: strings.TrimSpace(req.DeviceID), GroupID: strings.TrimSpace(req.GroupID),
	}
	if strings.EqualFold(strings.TrimSpace(req.Action), "remove") {
		return client.RemoveDeviceFromGroup(ctx, mutation)
	}
	return client.AddDeviceToGroup(ctx, mutation)
}

func (m Manager) GrantOfficialHubConnection(ctx context.Context, req OfficialHubConnectionGrantRequest) (cloudhub.ConnectionGrant, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.ConnectionGrant{}, err
	}
	return client.GrantConnectionAccess(ctx, cloudhub.ConnectionGrantRequest{
		OrganizationID: organizationID, MemberAccountID: strings.TrimSpace(req.MemberAccountID),
		DeviceID: strings.TrimSpace(req.DeviceID), GroupID: strings.TrimSpace(req.GroupID),
	})
}

func (m Manager) RevokeOfficialHubConnection(ctx context.Context, req OfficialHubConnectionGrantRequest) (cloudhub.ConnectionGrant, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.ConnectionGrant{}, err
	}
	return client.RevokeConnectionAccess(ctx, cloudhub.RevokeConnectionGrantRequest{OrganizationID: organizationID, GrantID: strings.TrimSpace(req.GrantID)})
}

func (m Manager) QueryOfficialHubAudit(ctx context.Context, req OfficialHubAuditRequest) (cloudhub.OrganizationAuditPage, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.OrganizationAuditPage{}, err
	}
	return client.QueryOrganizationAudit(ctx, cloudhub.OrganizationAuditQuery{
		OrganizationID: organizationID, MemberAccountID: strings.TrimSpace(req.MemberAccountID),
		SourceDeviceID: strings.TrimSpace(req.SourceDeviceID), TargetDeviceID: strings.TrimSpace(req.TargetDeviceID),
		StartTime: req.StartTime, EndTime: req.EndTime, ConnectionMethod: strings.TrimSpace(req.ConnectionMethod),
		RelayOnly: req.RelayOnly, MinRelayBytes: req.MinRelayBytes, PageSize: req.PageSize, Cursor: strings.TrimSpace(req.Cursor),
	})
}

func (m Manager) CleanupOfficialHubAudit(ctx context.Context, req OfficialHubAuditCleanupRequest) (cloudhub.OrganizationAuditCleanupResult, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.OrganizationAuditCleanupResult{}, err
	}
	return client.CleanupOrganizationAudit(ctx, cloudhub.CleanupOrganizationAuditRequest{OrganizationID: organizationID, BatchSize: req.BatchSize})
}

func (m Manager) CreateOfficialHubDeploymentBundle(ctx context.Context, req OfficialHubDeploymentBundleRequest) (cloudhub.DeploymentBundleResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return cloudhub.DeploymentBundleResult{}, err
	}
	client, organizationID, err := m.officialHubTeamClient(baseURL, state, req.OrganizationID)
	if err != nil {
		return cloudhub.DeploymentBundleResult{}, err
	}
	networkID := fallbackText(req.NetworkID, state.NetworkID)
	if networkID == "" {
		return cloudhub.DeploymentBundleResult{}, fmt.Errorf("network_id is required; please create or select an official network first")
	}
	return client.CreateDeploymentBundle(ctx, cloudhub.CreateDeploymentBundleRequest{
		OrganizationID: organizationID, NetworkID: networkID, GroupID: strings.TrimSpace(req.GroupID),
		Platform: req.Platform, Architecture: req.Architecture,
		TTL: time.Duration(req.TTLSeconds) * time.Second, MaxUses: req.MaxUses,
	})
}

func (m Manager) ListOfficialHubDeploymentBundles(ctx context.Context, req OfficialHubTeamRequest) ([]cloudhub.DeploymentBundle, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return nil, err
	}
	return client.ListDeploymentBundles(ctx, organizationID)
}

func (m Manager) RevokeOfficialHubBootstrapCredential(ctx context.Context, req OfficialHubDeploymentCredentialRequest) (cloudhub.BootstrapCredential, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.BootstrapCredential{}, err
	}
	return client.RevokeBootstrapCredential(ctx, cloudhub.RevokeBootstrapCredentialRequest{
		OrganizationID: organizationID, CredentialID: strings.TrimSpace(req.CredentialID),
	})
}

func (m Manager) RedeemOfficialHubBootstrapCredential(ctx context.Context, req OfficialHubBootstrapRedeemRequest) (cloudhub.BootstrapRedemption, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return cloudhub.BootstrapRedemption{}, err
	}
	result, err := m.officialHubClient(baseURL).RedeemBootstrapCredential(ctx, cloudhub.RedeemBootstrapCredentialRequest{
		Credential: strings.TrimSpace(req.Credential), OrganizationID: strings.TrimSpace(req.OrganizationID), GroupID: strings.TrimSpace(req.GroupID),
		Platform: req.Platform, Architecture: req.Architecture, DeviceName: strings.TrimSpace(req.DeviceName),
		Fingerprint: strings.TrimSpace(req.Fingerprint), CurrentVersion: strings.TrimSpace(req.CurrentVersion),
	})
	if err != nil {
		return cloudhub.BootstrapRedemption{}, err
	}
	state.HubAPIURL = baseURL
	state.AccountID = result.Device.AccountID
	state.NetworkID = result.Device.NetworkID
	state.OrganizationID = result.OrganizationDevice.OrganizationID
	state.DeviceID = result.Device.ID
	state.LocalDeviceName = result.Device.Name
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return cloudhub.BootstrapRedemption{}, err
	}
	return result, nil
}

func (m Manager) CreateOfficialHubRollout(ctx context.Context, req OfficialHubRolloutRequest) (cloudhub.RolloutResult, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.RolloutResult{}, err
	}
	return client.CreateRollout(ctx, cloudhub.CreateRolloutRequest{
		OrganizationID: organizationID, GroupID: strings.TrimSpace(req.GroupID),
		DeviceIDs: append([]string(nil), req.DeviceIDs...), TargetVersion: strings.TrimSpace(req.TargetVersion),
	})
}

func (m Manager) ListOfficialHubRollouts(ctx context.Context, req OfficialHubTeamRequest) ([]cloudhub.Rollout, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return nil, err
	}
	return client.ListRollouts(ctx, organizationID)
}

func (m Manager) GetOfficialHubRollout(ctx context.Context, req OfficialHubRolloutRequest) (cloudhub.RolloutResult, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.RolloutResult{}, err
	}
	return client.GetRollout(ctx, organizationID, strings.TrimSpace(req.RolloutID))
}

func (m Manager) CancelOfficialHubRollout(ctx context.Context, req OfficialHubRolloutRequest) (cloudhub.RolloutResult, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.RolloutResult{}, err
	}
	return client.CancelRollout(ctx, cloudhub.CancelRolloutRequest{OrganizationID: organizationID, RolloutID: strings.TrimSpace(req.RolloutID)})
}

func (m Manager) RetryOfficialHubRolloutTarget(ctx context.Context, req OfficialHubRolloutRequest) (cloudhub.DeploymentTarget, error) {
	client, organizationID, err := m.officialHubTeamClientForRequest(req.HubAPIURL, req.OrganizationID)
	if err != nil {
		return cloudhub.DeploymentTarget{}, err
	}
	return client.RetryRolloutTarget(ctx, cloudhub.RetryRolloutTargetRequest{
		OrganizationID: organizationID, RolloutID: strings.TrimSpace(req.RolloutID), DeviceID: strings.TrimSpace(req.DeviceID),
	})
}

func (m Manager) officialHubTeamClientForRequest(rawURL, requestedOrganizationID string) (*cloudhub.Client, string, error) {
	state, baseURL, err := m.stateAndHubURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	return m.officialHubTeamClient(baseURL, state, requestedOrganizationID)
}

func (m Manager) officialHubTeamClient(baseURL string, state OfficialHubState, requestedOrganizationID string) (*cloudhub.Client, string, error) {
	actorAccountID := strings.TrimSpace(state.AccountID)
	if actorAccountID == "" {
		return nil, "", fmt.Errorf("account_id is required; please create or sign in to an account first")
	}
	organizationID := strings.TrimSpace(requestedOrganizationID)
	if organizationID == "" {
		organizationID = strings.TrimSpace(state.OrganizationID)
	}
	if organizationID == "" {
		return nil, "", fmt.Errorf("organization_id is required; please create or select an organization first")
	}
	client := m.officialHubClient(baseURL)
	client.ActorAccountID = actorAccountID
	return client, organizationID, nil
}
