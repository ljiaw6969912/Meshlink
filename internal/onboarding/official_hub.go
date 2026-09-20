package onboarding

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meshlink/internal/cloudhub"
	"meshlink/internal/p2p"
)

const DefaultOfficialHubAPIURL = "http://127.0.0.1:18080"

type OfficialHubState struct {
	HubAPIURL          string                    `json:"hub_api_url,omitempty"`
	SuggestedHubAPIURL string                    `json:"suggested_hub_api_url,omitempty"`
	AccountID          string                    `json:"account_id,omitempty"`
	AccountEmail       string                    `json:"account_email,omitempty"`
	AccountName        string                    `json:"account_name,omitempty"`
	NetworkID          string                    `json:"network_id,omitempty"`
	NetworkName        string                    `json:"network_name,omitempty"`
	OrganizationID     string                    `json:"organization_id,omitempty"`
	OrganizationName   string                    `json:"organization_name,omitempty"`
	DeviceID           string                    `json:"device_id,omitempty"`
	LocalDeviceName    string                    `json:"local_device_name,omitempty"`
	LastInvite         *OfficialHubInviteSummary `json:"last_invite,omitempty"`
	UpdatedAt          time.Time                 `json:"updated_at,omitempty"`
}

type OfficialHubInviteSummary struct {
	ID          string    `json:"id,omitempty"`
	AccountID   string    `json:"account_id,omitempty"`
	NetworkID   string    `json:"network_id,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	OneTime     bool      `json:"one_time,omitempty"`
	Uses        int       `json:"uses,omitempty"`
	MaxUses     int       `json:"max_uses,omitempty"`
	Failures    int       `json:"failures,omitempty"`
	MaxFailures int       `json:"max_failures,omitempty"`
}

type OfficialHubAccountRequest struct {
	HubAPIURL   string `json:"hub_api_url"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Password    string `json:"password,omitempty"`
}

type OfficialHubNetworkRequest struct {
	HubAPIURL string `json:"hub_api_url,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Name      string `json:"name"`
}

type OfficialHubInviteRequest struct {
	HubAPIURL  string        `json:"hub_api_url,omitempty"`
	AccountID  string        `json:"account_id,omitempty"`
	NetworkID  string        `json:"network_id,omitempty"`
	TTL        time.Duration `json:"ttl,omitempty"`
	TTLSeconds int64         `json:"ttl_seconds,omitempty"`
	MaxUses    int           `json:"max_uses,omitempty"`
	OneTime    bool          `json:"one_time,omitempty"`
}

type OfficialHubJoinDeviceRequest struct {
	HubAPIURL   string `json:"hub_api_url,omitempty"`
	Token       string `json:"token"`
	Code        string `json:"code"`
	DeviceName  string `json:"device_name,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	RemoteAddr  string `json:"remote_addr,omitempty"`
}

type OfficialHubHeartbeatRequest struct {
	HubAPIURL          string                          `json:"hub_api_url,omitempty"`
	DeviceID           string                          `json:"device_id,omitempty"`
	Fingerprint        string                          `json:"fingerprint,omitempty"`
	Status             cloudhub.DeviceStatus           `json:"status,omitempty"`
	PathType           p2p.PathType                    `json:"path_type"`
	PathState          p2p.PathState                   `json:"path_state"`
	LatencyMS          int64                           `json:"latency_ms"`
	PacketLossPermille int                             `json:"packet_loss_per_mille,omitempty"`
	JitterMS           int64                           `json:"jitter_ms,omitempty"`
	RelayBytesIn       int64                           `json:"relay_bytes_in"`
	RelayBytesOut      int64                           `json:"relay_bytes_out"`
	SwitchCount        int                             `json:"switch_count,omitempty"`
	SwitchReasons      []string                        `json:"switch_reasons,omitempty"`
	SwitchFromPath     p2p.PathType                    `json:"switch_from_path,omitempty"`
	SwitchToPath       p2p.PathType                    `json:"switch_to_path,omitempty"`
	SwitchScoreDelta   int                             `json:"switch_score_delta,omitempty"`
	AutoSwitched       bool                            `json:"auto_switched,omitempty"`
	LastError          string                          `json:"last_error"`
	CurrentVersion     string                          `json:"current_version,omitempty"`
	RolloutID          string                          `json:"rollout_id,omitempty"`
	TargetVersion      string                          `json:"target_version,omitempty"`
	VersionStatus      cloudhub.DeploymentTargetStatus `json:"version_status,omitempty"`
	VersionSequence    int64                           `json:"version_sequence,omitempty"`
	VersionErrorCode   cloudhub.RolloutErrorCode       `json:"version_error_code,omitempty"`
}

type OfficialHubRevokeDeviceRequest struct {
	HubAPIURL string `json:"hub_api_url,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type OfficialHubDevicesRequest struct {
	HubAPIURL string `json:"hub_api_url,omitempty"`
	NetworkID string `json:"network_id,omitempty"`
}

type OfficialHubAccountResult struct {
	Account cloudhub.Account `json:"account"`
	State   OfficialHubState `json:"state"`
}

type OfficialHubNetworkResult struct {
	Network cloudhub.Network `json:"network"`
	State   OfficialHubState `json:"state"`
}

type OfficialHubInviteResult struct {
	Invite cloudhub.InviteResult `json:"invite"`
	State  OfficialHubState      `json:"state"`
}

type OfficialHubDeviceResult struct {
	Device cloudhub.Device  `json:"device"`
	State  OfficialHubState `json:"state"`
}

type OfficialHubDevicesResult struct {
	Devices            []cloudhub.Device            `json:"devices"`
	State              OfficialHubState             `json:"state"`
	RelayUsageReminder *cloudhub.RelayUsageReminder `json:"relay_usage_reminder,omitempty"`
}

func (m Manager) OfficialHubState() (OfficialHubState, error) {
	state, err := m.readOfficialHubState()
	if err != nil {
		return OfficialHubState{}, err
	}
	return state.withSuggestion(), nil
}

func (m Manager) CreateOfficialHubAccount(ctx context.Context, req OfficialHubAccountRequest) (OfficialHubAccountResult, error) {
	baseURL, err := normalizeOfficialHubAPIURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubAccountResult{}, err
	}
	client := m.officialHubClient(baseURL)
	account, err := client.CreateAccount(ctx, cloudhub.CreateAccountRequest{
		Email:       strings.TrimSpace(req.Email),
		DisplayName: strings.TrimSpace(req.DisplayName),
	})
	if err != nil {
		return OfficialHubAccountResult{}, err
	}
	state, err := m.readOfficialHubState()
	if err != nil {
		return OfficialHubAccountResult{}, err
	}
	state.HubAPIURL = baseURL
	state.AccountID = account.ID
	state.AccountEmail = account.Email
	state.AccountName = account.DisplayName
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return OfficialHubAccountResult{}, err
	}
	return OfficialHubAccountResult{Account: account, State: state.withSuggestion()}, nil
}

func (m Manager) CreateOfficialHubNetwork(ctx context.Context, req OfficialHubNetworkRequest) (OfficialHubNetworkResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubNetworkResult{}, err
	}
	accountID := fallbackText(req.AccountID, state.AccountID)
	if strings.TrimSpace(accountID) == "" {
		return OfficialHubNetworkResult{}, fmt.Errorf("account_id is required; please create or log in with a test account first")
	}
	network, err := m.officialHubClient(baseURL).CreateNetwork(ctx, cloudhub.CreateNetworkRequest{
		AccountID: accountID,
		Name:      strings.TrimSpace(req.Name),
	})
	if err != nil {
		return OfficialHubNetworkResult{}, err
	}
	state.HubAPIURL = baseURL
	state.AccountID = accountID
	state.NetworkID = network.ID
	state.NetworkName = network.Name
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return OfficialHubNetworkResult{}, err
	}
	return OfficialHubNetworkResult{Network: network, State: state.withSuggestion()}, nil
}

func (m Manager) CreateOfficialHubInvite(ctx context.Context, req OfficialHubInviteRequest) (OfficialHubInviteResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubInviteResult{}, err
	}
	accountID := fallbackText(req.AccountID, state.AccountID)
	networkID := fallbackText(req.NetworkID, state.NetworkID)
	if strings.TrimSpace(accountID) == "" {
		return OfficialHubInviteResult{}, fmt.Errorf("account_id is required; please create or log in with a test account first")
	}
	if strings.TrimSpace(networkID) == "" {
		return OfficialHubInviteResult{}, fmt.Errorf("network_id is required; please create an official network first")
	}
	ttl := req.TTL
	if ttl <= 0 && req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	invite, err := m.officialHubClient(baseURL).CreateInvite(ctx, cloudhub.CreateInviteRequest{
		AccountID: accountID,
		NetworkID: networkID,
		TTL:       ttl,
		MaxUses:   req.MaxUses,
		OneTime:   req.OneTime,
	})
	if err != nil {
		return OfficialHubInviteResult{}, err
	}
	state.HubAPIURL = baseURL
	state.AccountID = accountID
	state.NetworkID = networkID
	state.LastInvite = officialHubInviteSummary(invite)
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return OfficialHubInviteResult{}, err
	}
	return OfficialHubInviteResult{Invite: invite, State: state.withSuggestion()}, nil
}

func (m Manager) JoinOfficialHubDevice(ctx context.Context, req OfficialHubJoinDeviceRequest) (OfficialHubDeviceResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubDeviceResult{}, err
	}
	deviceName := strings.TrimSpace(req.DeviceName)
	if deviceName == "" {
		deviceName = defaultOfficialHubDeviceName()
	}
	device, err := m.officialHubClient(baseURL).JoinDevice(ctx, cloudhub.JoinDeviceRequest{
		Token:       strings.TrimSpace(req.Token),
		Code:        strings.TrimSpace(req.Code),
		DeviceName:  deviceName,
		Fingerprint: strings.TrimSpace(req.Fingerprint),
		RemoteAddr:  strings.TrimSpace(req.RemoteAddr),
	})
	if err != nil {
		return OfficialHubDeviceResult{}, err
	}
	state.HubAPIURL = baseURL
	state.AccountID = device.AccountID
	state.NetworkID = device.NetworkID
	state.DeviceID = device.ID
	state.LocalDeviceName = device.Name
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return OfficialHubDeviceResult{}, err
	}
	return OfficialHubDeviceResult{Device: device, State: state.withSuggestion()}, nil
}

func (m Manager) HeartbeatOfficialHubDevice(ctx context.Context, req OfficialHubHeartbeatRequest) (OfficialHubDeviceResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubDeviceResult{}, err
	}
	deviceID := fallbackText(req.DeviceID, state.DeviceID)
	if strings.TrimSpace(deviceID) == "" {
		return OfficialHubDeviceResult{}, fmt.Errorf("device_id is required; please join the current device first")
	}
	device, err := m.officialHubClient(baseURL).HeartbeatDevice(ctx, cloudhub.HeartbeatDeviceRequest{
		DeviceID:           deviceID,
		Fingerprint:        req.Fingerprint,
		Status:             req.Status,
		PathType:           req.PathType,
		PathState:          req.PathState,
		LatencyMS:          req.LatencyMS,
		PacketLossPermille: req.PacketLossPermille,
		JitterMS:           req.JitterMS,
		RelayBytesIn:       req.RelayBytesIn,
		RelayBytesOut:      req.RelayBytesOut,
		SwitchCount:        req.SwitchCount,
		SwitchReasons:      append([]string(nil), req.SwitchReasons...),
		SwitchFromPath:     req.SwitchFromPath,
		SwitchToPath:       req.SwitchToPath,
		SwitchScoreDelta:   req.SwitchScoreDelta,
		AutoSwitched:       req.AutoSwitched,
		LastError:          req.LastError,
		CurrentVersion:     req.CurrentVersion,
		RolloutID:          req.RolloutID,
		TargetVersion:      req.TargetVersion,
		VersionStatus:      req.VersionStatus,
		VersionSequence:    req.VersionSequence,
		VersionErrorCode:   req.VersionErrorCode,
	})
	if err != nil {
		return OfficialHubDeviceResult{}, err
	}
	state.HubAPIURL = baseURL
	state.AccountID = device.AccountID
	state.NetworkID = device.NetworkID
	state.DeviceID = device.ID
	state.LocalDeviceName = device.Name
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return OfficialHubDeviceResult{}, err
	}
	return OfficialHubDeviceResult{Device: device, State: state.withSuggestion()}, nil
}

func (m Manager) RevokeOfficialHubDevice(ctx context.Context, req OfficialHubRevokeDeviceRequest) (OfficialHubDeviceResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubDeviceResult{}, err
	}
	deviceID := fallbackText(req.DeviceID, state.DeviceID)
	if strings.TrimSpace(deviceID) == "" {
		return OfficialHubDeviceResult{}, fmt.Errorf("device_id is required; please join the current device first")
	}
	device, err := m.officialHubClient(baseURL).RevokeDevice(ctx, cloudhub.RevokeDeviceRequest{
		DeviceID: deviceID,
		Reason:   strings.TrimSpace(req.Reason),
	})
	if err != nil {
		return OfficialHubDeviceResult{}, err
	}
	state.HubAPIURL = baseURL
	state.AccountID = device.AccountID
	state.NetworkID = device.NetworkID
	state.DeviceID = device.ID
	state.UpdatedAt = m.now().UTC()
	if err := m.writeOfficialHubState(state); err != nil {
		return OfficialHubDeviceResult{}, err
	}
	return OfficialHubDeviceResult{Device: device, State: state.withSuggestion()}, nil
}

func (m Manager) ListOfficialHubDevices(ctx context.Context, req OfficialHubDevicesRequest) (OfficialHubDevicesResult, error) {
	state, baseURL, err := m.stateAndHubURL(req.HubAPIURL)
	if err != nil {
		return OfficialHubDevicesResult{}, err
	}
	networkID := fallbackText(req.NetworkID, state.NetworkID)
	if strings.TrimSpace(networkID) == "" {
		return OfficialHubDevicesResult{}, fmt.Errorf("network_id is required; please create an official network first")
	}
	devices, err := m.officialHubClient(baseURL).ListNetworkDevices(ctx, networkID)
	if err != nil {
		return OfficialHubDevicesResult{}, err
	}
	var reminder *cloudhub.RelayUsageReminder
	if accountID := strings.TrimSpace(state.AccountID); accountID != "" {
		if summary, err := m.officialHubClient(baseURL).GetAccountManagementSummary(ctx, accountID); err == nil {
			reminder = summary.RelayUsageReminder
		}
	}
	return OfficialHubDevicesResult{Devices: devices, State: state.withSuggestion(), RelayUsageReminder: reminder}, nil
}

func (m Manager) officialHubClient(baseURL string) *cloudhub.Client {
	return &cloudhub.Client{
		BaseURL:    baseURL,
		HTTPClient: m.HTTPClient,
		Timeout:    10 * time.Second,
	}
}

func (m Manager) stateAndHubURL(raw string) (OfficialHubState, string, error) {
	state, err := m.readOfficialHubState()
	if err != nil {
		return OfficialHubState{}, "", err
	}
	baseURL := strings.TrimSpace(raw)
	if baseURL == "" {
		baseURL = state.HubAPIURL
	}
	baseURL, err = normalizeOfficialHubAPIURL(baseURL)
	if err != nil {
		return OfficialHubState{}, "", err
	}
	return state, baseURL, nil
}

func (m Manager) readOfficialHubState() (OfficialHubState, error) {
	path := m.officialHubStatePath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return OfficialHubState{}.withSuggestion(), nil
		}
		return OfficialHubState{}, err
	}
	var state OfficialHubState
	if err := jsonUnmarshalStrict(b, &state); err != nil {
		return OfficialHubState{}, err
	}
	return state.withSuggestion(), nil
}

func (m Manager) writeOfficialHubState(state OfficialHubState) error {
	state.SuggestedHubAPIURL = DefaultOfficialHubAPIURL
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = m.now().UTC()
	}
	return writePrettyJSON(m.officialHubStatePath(), state)
}

func (m Manager) officialHubStatePath() string {
	return filepath.Join(m.configsDir(), "official-hub.json")
}

func (state OfficialHubState) withSuggestion() OfficialHubState {
	state.SuggestedHubAPIURL = DefaultOfficialHubAPIURL
	return state
}

func normalizeOfficialHubAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("Hub API 地址不能为空，请填写本地 Hub API 地址，例如 %s，或明确填写内测官方 Hub 地址", DefaultOfficialHubAPIURL)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("Hub API 地址无效，请使用 %s 这样的完整地址", DefaultOfficialHubAPIURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("Hub API 地址只支持 http 或 https")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func officialHubInviteSummary(invite cloudhub.InviteResult) *OfficialHubInviteSummary {
	return &OfficialHubInviteSummary{
		ID:          invite.ID,
		AccountID:   invite.AccountID,
		NetworkID:   invite.NetworkID,
		CreatedAt:   invite.CreatedAt,
		ExpiresAt:   invite.ExpiresAt,
		OneTime:     invite.OneTime,
		Uses:        invite.Uses,
		MaxUses:     invite.MaxUses,
		Failures:    invite.Failures,
		MaxFailures: invite.MaxFailures,
	}
}

func defaultOfficialHubDeviceName() string {
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return strings.TrimSpace(host)
	}
	return "meshlink-device"
}

func fallbackText(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
