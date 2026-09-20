package cloudhub

import (
	"time"

	"meshlink/internal/p2p"
)

type AccountStatus string

const (
	AccountStatusActive AccountStatus = "active"
	AccountStatusFrozen AccountStatus = "frozen"
	AccountStatusBanned AccountStatus = "banned"
)

type DeviceStatus string

const (
	DeviceStatusOffline DeviceStatus = "offline"
	DeviceStatusOnline  DeviceStatus = "online"
	DeviceStatusRevoked DeviceStatus = "revoked"
)

type RelaySessionStatus string

const (
	RelaySessionPending RelaySessionStatus = "pending"
	RelaySessionActive  RelaySessionStatus = "active"
	RelaySessionClosed  RelaySessionStatus = "closed"

	RelayPathType = "relay"
)

type OrganizationStatus string

const (
	OrganizationStatusActive    OrganizationStatus = "active"
	OrganizationStatusSuspended OrganizationStatus = "suspended"
)

type MembershipRole string

const (
	MembershipRoleOwner    MembershipRole = "owner"
	MembershipRoleAdmin    MembershipRole = "admin"
	MembershipRoleOperator MembershipRole = "operator"
	MembershipRoleMember   MembershipRole = "member"
)

type MembershipStatus string

const (
	MembershipStatusActive    MembershipStatus = "active"
	MembershipStatusSuspended MembershipStatus = "suspended"
	MembershipStatusRemoved   MembershipStatus = "removed"
)

type BanScope string

const (
	BanScopeAccount BanScope = "account"
	BanScopeDevice  BanScope = "device"
	BanScopeNetwork BanScope = "network"
)

type RevocationScope string

const (
	RevocationScopeDevice RevocationScope = "device"
	RevocationScopeInvite RevocationScope = "invite"
)

type Account struct {
	ID           string        `json:"id"`
	Email        string        `json:"email"`
	DisplayName  string        `json:"display_name,omitempty"`
	Status       AccountStatus `json:"status"`
	PolicyName   string        `json:"policy_name,omitempty"`
	PlanID       PlanID        `json:"plan_id,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	FrozenAt     *time.Time    `json:"frozen_at,omitempty"`
	FrozenReason string        `json:"frozen_reason,omitempty"`
	BannedAt     *time.Time    `json:"banned_at,omitempty"`
	BanReason    string        `json:"ban_reason,omitempty"`
}

type SubscriptionStatus string

const (
	SubscriptionStatusPending  SubscriptionStatus = "pending"
	SubscriptionStatusActive   SubscriptionStatus = "active"
	SubscriptionStatusPastDue  SubscriptionStatus = "past_due"
	SubscriptionStatusCanceled SubscriptionStatus = "canceled"
	SubscriptionStatusExpired  SubscriptionStatus = "expired"
)

type Subscription struct {
	ID                     string             `json:"id"`
	AccountID              string             `json:"account_id"`
	PlanID                 PlanID             `json:"plan_id"`
	Status                 SubscriptionStatus `json:"status"`
	EffectiveAt            time.Time          `json:"effective_at"`
	EffectiveUntil         *time.Time         `json:"effective_until,omitempty"`
	Provider               string             `json:"-"`
	ProviderSubscriptionID string             `json:"-"`
	ProviderCustomerID     string             `json:"-"`
	Version                int64              `json:"version,omitempty"`
	CreatedAt              time.Time          `json:"created_at"`
	UpdatedAt              time.Time          `json:"updated_at"`
}

type AccountSubscriptionStatus struct {
	PlanID         PlanID             `json:"plan_id,omitempty"`
	Status         SubscriptionStatus `json:"status"`
	EffectiveAt    *time.Time         `json:"effective_at,omitempty"`
	EffectiveUntil *time.Time         `json:"effective_until,omitempty"`
}

type Network struct {
	ID        string     `json:"id"`
	AccountID string     `json:"account_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type Member struct {
	ID        string     `json:"id"`
	AccountID string     `json:"account_id"`
	NetworkID string     `json:"network_id"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type Organization struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Status          OrganizationStatus `json:"status"`
	OwnerAccountID  string             `json:"owner_account_id"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	SuspendedAt     *time.Time         `json:"suspended_at,omitempty"`
	SuspendedReason string             `json:"-"`
}

type Membership struct {
	ID             string           `json:"id"`
	OrganizationID string           `json:"organization_id"`
	AccountID      string           `json:"account_id"`
	Role           MembershipRole   `json:"role"`
	Status         MembershipStatus `json:"status"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	SuspendedAt    *time.Time       `json:"suspended_at,omitempty"`
	RemovedAt      *time.Time       `json:"removed_at,omitempty"`
}

type DeviceGroup struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Name           string     `json:"name"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

type OrganizationDevice struct {
	ID                  string    `json:"id"`
	OrganizationID      string    `json:"organization_id"`
	DeviceID            string    `json:"device_id"`
	AccountID           string    `json:"account_id"`
	GroupID             string    `json:"group_id,omitempty"`
	EnrolledByAccountID string    `json:"enrolled_by_account_id"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type ConnectionGrantScope string

const (
	ConnectionGrantDevice ConnectionGrantScope = "device"
	ConnectionGrantGroup  ConnectionGrantScope = "group"
)

type ConnectionGrant struct {
	ID                 string               `json:"id"`
	OrganizationID     string               `json:"organization_id"`
	MemberAccountID    string               `json:"member_account_id"`
	Scope              ConnectionGrantScope `json:"scope"`
	DeviceID           string               `json:"device_id,omitempty"`
	GroupID            string               `json:"group_id,omitempty"`
	GrantedByAccountID string               `json:"granted_by_account_id"`
	CreatedAt          time.Time            `json:"created_at"`
}

type ConnectionPermissionSource string

const (
	ConnectionPermissionOwner       ConnectionPermissionSource = "owner"
	ConnectionPermissionAdmin       ConnectionPermissionSource = "admin"
	ConnectionPermissionDeviceGrant ConnectionPermissionSource = "device_grant"
	ConnectionPermissionGroupGrant  ConnectionPermissionSource = "group_grant"
	ConnectionPermissionDeny        ConnectionPermissionSource = "deny"
	ConnectionPermissionLegacy      ConnectionPermissionSource = "legacy"
)

type ConnectionAuthorization struct {
	Allowed          bool                       `json:"allowed"`
	OrganizationID   string                     `json:"organization_id,omitempty"`
	MemberAccountID  string                     `json:"member_account_id,omitempty"`
	SourceDeviceID   string                     `json:"source_device_id"`
	TargetDeviceID   string                     `json:"target_device_id"`
	GroupID          string                     `json:"group_id,omitempty"`
	PermissionSource ConnectionPermissionSource `json:"permission_source"`
}

type OrganizationInvite struct {
	ID                 string         `json:"id"`
	OrganizationID     string         `json:"organization_id"`
	InvitedAccountID   string         `json:"invited_account_id"`
	InvitedByAccountID string         `json:"invited_by_account_id"`
	Role               MembershipRole `json:"role"`
	TokenDigest        string         `json:"-"`
	CodeDigest         string         `json:"-"`
	CreatedAt          time.Time      `json:"created_at"`
	ExpiresAt          time.Time      `json:"expires_at"`
	UsedAt             *time.Time     `json:"used_at,omitempty"`
	RevokedAt          *time.Time     `json:"revoked_at,omitempty"`
}

type OrganizationInviteResult struct {
	ID                 string         `json:"id"`
	OrganizationID     string         `json:"organization_id"`
	InvitedAccountID   string         `json:"invited_account_id"`
	InvitedByAccountID string         `json:"invited_by_account_id"`
	Role               MembershipRole `json:"role"`
	Token              string         `json:"token"`
	Code               string         `json:"code"`
	CreatedAt          time.Time      `json:"created_at"`
	ExpiresAt          time.Time      `json:"expires_at"`
}

type Device struct {
	p2p.ConnectionStatus
	ID             string               `json:"id"`
	AccountID      string               `json:"account_id"`
	NetworkID      string               `json:"network_id"`
	Name           string               `json:"name"`
	Fingerprint    string               `json:"fingerprint,omitempty"`
	Status         DeviceStatus         `json:"status"`
	RemoteAddr     string               `json:"remote_addr,omitempty"`
	NATProbe       *p2p.NATProbeSummary `json:"nat_probe,omitempty"`
	JoinedAt       time.Time            `json:"joined_at"`
	LastSeen       *time.Time           `json:"last_seen,omitempty"`
	RevokedAt      *time.Time           `json:"revoked_at,omitempty"`
	RevokedReason  string               `json:"revoked_reason,omitempty"`
	CurrentVersion string               `json:"current_version,omitempty"`
	RolloutID      string               `json:"rollout_id,omitempty"`
	TargetVersion  string               `json:"target_version,omitempty"`
}

type Invite struct {
	ID          string     `json:"id"`
	AccountID   string     `json:"account_id"`
	NetworkID   string     `json:"network_id"`
	Token       string     `json:"token"`
	CodeHash    string     `json:"code_hash"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	OneTime     bool       `json:"one_time"`
	Uses        int        `json:"uses"`
	MaxUses     int        `json:"max_uses"`
	Failures    int        `json:"failures"`
	MaxFailures int        `json:"max_failures"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type InviteResult struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	NetworkID   string    `json:"network_id"`
	Token       string    `json:"token"`
	Code        string    `json:"code"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	OneTime     bool      `json:"one_time"`
	Uses        int       `json:"uses"`
	MaxUses     int       `json:"max_uses"`
	Failures    int       `json:"failures"`
	MaxFailures int       `json:"max_failures"`
}

type Session struct {
	ID        string     `json:"id"`
	AccountID string     `json:"account_id"`
	NetworkID string     `json:"network_id"`
	DeviceID  string     `json:"device_id"`
	TokenHash string     `json:"token_hash,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type RelaySession struct {
	ID                  string                     `json:"id"`
	OrganizationID      string                     `json:"-"`
	PermissionSource    ConnectionPermissionSource `json:"-"`
	AccountID           string                     `json:"account_id"`
	NetworkID           string                     `json:"network_id"`
	SourceDeviceID      string                     `json:"source_device_id"`
	TargetDeviceID      string                     `json:"target_device_id"`
	PathType            string                     `json:"path_type"`
	Status              RelaySessionStatus         `json:"status"`
	CreatedAt           time.Time                  `json:"created_at"`
	StartedAt           time.Time                  `json:"started_at"`
	ExpiresAt           time.Time                  `json:"expires_at"`
	EndedAt             *time.Time                 `json:"ended_at,omitempty"`
	RelayBytesIn        int64                      `json:"relay_bytes_in,omitempty"`
	RelayBytesOut       int64                      `json:"relay_bytes_out,omitempty"`
	Error               string                     `json:"error,omitempty"`
	SourceJoinTokenHash string                     `json:"-"`
	TargetJoinTokenHash string                     `json:"-"`
}

type RelaySessionResult struct {
	Session         RelaySession    `json:"session"`
	SourceJoinToken string          `json:"source_join_token,omitempty"`
	TargetJoinToken string          `json:"target_join_token,omitempty"`
	RelayEndpoint   string          `json:"relay_endpoint,omitempty"`
	RelayByteBudget RelayByteBudget `json:"relay_byte_budget,omitempty"`
}

type RelayStatus struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen,omitempty"`
}

type ConnectionLog struct {
	ID                 string                     `json:"id"`
	OrganizationID     string                     `json:"-"`
	PermissionSource   ConnectionPermissionSource `json:"-"`
	SessionID          string                     `json:"session_id,omitempty"`
	AccountID          string                     `json:"account_id"`
	NetworkID          string                     `json:"network_id"`
	SourceDeviceID     string                     `json:"source_device_id,omitempty"`
	TargetDeviceID     string                     `json:"target_device_id,omitempty"`
	PathType           string                     `json:"path_type,omitempty"`
	PathState          string                     `json:"path_state,omitempty"`
	QualityScore       int                        `json:"quality_score"`
	LatencyMS          int64                      `json:"latency_ms,omitempty"`
	PacketLossPermille int                        `json:"packet_loss_per_mille,omitempty"`
	JitterMS           int64                      `json:"jitter_ms,omitempty"`
	RelayBytesIn       int64                      `json:"relay_bytes_in,omitempty"`
	RelayBytesOut      int64                      `json:"relay_bytes_out,omitempty"`
	SwitchCount        int                        `json:"switch_count,omitempty"`
	SwitchReasons      []string                   `json:"switch_reasons,omitempty"`
	SwitchFromPath     string                     `json:"switch_from_path,omitempty"`
	SwitchToPath       string                     `json:"switch_to_path,omitempty"`
	SwitchScoreDelta   int                        `json:"switch_score_delta,omitempty"`
	AutoSwitched       bool                       `json:"auto_switched"`
	StartedAt          time.Time                  `json:"started_at"`
	EndedAt            *time.Time                 `json:"ended_at,omitempty"`
	Error              string                     `json:"error,omitempty"`
}

type RelayUsage struct {
	ID         string    `json:"id"`
	AccountID  string    `json:"account_id"`
	NetworkID  string    `json:"network_id"`
	DeviceID   string    `json:"device_id,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	RecordedAt time.Time `json:"recorded_at"`
}

type AccountPolicy struct {
	Name                   string `json:"name"`
	RelayBytesQuota        int64  `json:"relay_bytes_quota"`
	MaxActiveRelaySessions int    `json:"max_active_relay_sessions"`
	MaxRelaySessionsPerDay int    `json:"max_relay_sessions_per_day"`
}

type AccountPolicyStatus struct {
	AccountID                 string              `json:"account_id"`
	AccountStatus             AccountStatus       `json:"account_status"`
	Policy                    AccountPolicy       `json:"policy"`
	RelayBytesUsed            int64               `json:"relay_bytes_used"`
	RelayBytesRemaining       int64               `json:"relay_bytes_remaining"`
	ActiveRelaySessions       int                 `json:"active_relay_sessions"`
	RelaySessionsCreatedToday int                 `json:"relay_sessions_created_today"`
	AsOf                      time.Time           `json:"as_of"`
	RelayUsageReminder        *RelayUsageReminder `json:"relay_usage_reminder,omitempty"`
}

type AccountPlanQuotaUsage struct {
	Dimension QuotaDimension  `json:"dimension"`
	Mode      PlanQuotaMode   `json:"mode"`
	Unit      PlanQuotaUnit   `json:"unit"`
	Limited   bool            `json:"limited"`
	Used      int64           `json:"used"`
	Limit     *int64          `json:"limit,omitempty"`
	Remaining *int64          `json:"remaining,omitempty"`
	Source    PlanQuotaSource `json:"source,omitempty"`
}

type AccountPlanQuotaSummary struct {
	DeviceCount             AccountPlanQuotaUsage `json:"device_count"`
	ConcurrentOnlineDevices AccountPlanQuotaUsage `json:"concurrent_online_devices"`
	OfficialRelayTraffic    AccountPlanQuotaUsage `json:"official_relay_traffic"`
}

type RelayUsageReminder struct {
	Level          string   `json:"level"`
	Severity       string   `json:"severity"`
	Title          string   `json:"title"`
	UsedBytes      int64    `json:"used_bytes"`
	LimitBytes     int64    `json:"limit_bytes"`
	UsagePercent   int      `json:"usage_percent"`
	Message        string   `json:"message"`
	Impact         string   `json:"impact"`
	Recommendation string   `json:"recommendation"`
	Actions        []string `json:"actions,omitempty"`
}

type RiskEventKind string

const (
	RiskQuotaExceeded             RiskEventKind = "quota_exceeded"
	RiskAccountFrozen             RiskEventKind = "account_frozen"
	RiskAccountBanned             RiskEventKind = "account_banned"
	RiskAccountEnforcementApplied RiskEventKind = "account_enforcement_applied"
	RiskRelaySessionRevoked       RiskEventKind = "relay_session_revoked"
	RiskRelaySessionDenied        RiskEventKind = "relay_session_denied"
	RiskInviteAbuseSuspected      RiskEventKind = "invite_abuse_suspected"
)

type RiskEvent struct {
	ID        string         `json:"id"`
	Time      time.Time      `json:"time"`
	Kind      RiskEventKind  `json:"kind"`
	AccountID string         `json:"account_id,omitempty"`
	NetworkID string         `json:"network_id,omitempty"`
	DeviceID  string         `json:"device_id,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type Ban struct {
	ID        string     `json:"id"`
	Scope     BanScope   `json:"scope"`
	TargetID  string     `json:"target_id"`
	Reason    string     `json:"reason,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type Revocation struct {
	ID        string          `json:"id"`
	Scope     RevocationScope `json:"scope"`
	TargetID  string          `json:"target_id"`
	Reason    string          `json:"reason,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type AuditEvent struct {
	ID        string         `json:"id"`
	Time      time.Time      `json:"time"`
	Event     string         `json:"event"`
	AccountID string         `json:"account_id,omitempty"`
	NetworkID string         `json:"network_id,omitempty"`
	DeviceID  string         `json:"device_id,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type RelaySessionsSummary struct {
	Total    int            `json:"total"`
	Pending  int            `json:"pending"`
	Active   int            `json:"active"`
	Closed   int            `json:"closed"`
	Sessions []RelaySession `json:"sessions,omitempty"`
}

type AccountManagementSummary struct {
	Account            Account                      `json:"account"`
	Policy             AccountPolicyStatus          `json:"policy"`
	PlanQuotas         *AccountPlanQuotaSummary     `json:"plan_quotas,omitempty"`
	RelayUsageReminder *RelayUsageReminder          `json:"relay_usage_reminder,omitempty"`
	RiskEvents         []RiskEvent                  `json:"risk_events,omitempty"`
	AuditEvents        []AuditEvent                 `json:"audit_events,omitempty"`
	RelayUsage         []RelayUsage                 `json:"relay_usage,omitempty"`
	ConnectionLogs     []ConnectionLog              `json:"connection_logs,omitempty"`
	ConnectionQuality  p2p.ConnectionQualitySummary `json:"connection_quality"`
	RelaySessions      RelaySessionsSummary         `json:"relay_sessions"`
}

type CreateAccountRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	PlanID      PlanID `json:"plan_id,omitempty"`
}

type AccountStatusChangeRequest struct {
	AccountID string `json:"account_id"`
	Reason    string `json:"reason,omitempty"`
}

type CreateNetworkRequest struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
}

type CreateOrganizationRequest struct {
	OwnerAccountID string `json:"owner_account_id"`
	Name           string `json:"name"`
}

type OrganizationStatusChangeRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	Reason         string `json:"reason,omitempty"`
}

type CreateOrganizationInviteRequest struct {
	ActorAccountID   string         `json:"actor_account_id"`
	OrganizationID   string         `json:"organization_id"`
	InvitedAccountID string         `json:"invited_account_id"`
	Role             MembershipRole `json:"role,omitempty"`
	TTL              time.Duration  `json:"ttl,omitempty"`
}

type AcceptOrganizationInviteRequest struct {
	OrganizationID string `json:"organization_id"`
	AccountID      string `json:"account_id"`
	Token          string `json:"token"`
	Code           string `json:"code"`
}

type RevokeOrganizationInviteRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	InviteID       string `json:"invite_id"`
	Reason         string `json:"reason,omitempty"`
}

type OrganizationMemberStatusRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	AccountID      string `json:"account_id"`
	Reason         string `json:"reason,omitempty"`
}

type ChangeOrganizationMemberRoleRequest struct {
	ActorAccountID string         `json:"actor_account_id"`
	OrganizationID string         `json:"organization_id"`
	AccountID      string         `json:"account_id"`
	Role           MembershipRole `json:"role"`
}

type CreateDeviceGroupRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
}

type RenameDeviceGroupRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	GroupID        string `json:"group_id"`
	Name           string `json:"name"`
}

type DeleteDeviceGroupRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	GroupID        string `json:"group_id"`
}

type EnrollOrganizationDeviceRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	DeviceID       string `json:"device_id"`
}

type RemoveOrganizationDeviceRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	DeviceID       string `json:"device_id"`
}

type OrganizationDeviceGroupRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	GroupID        string `json:"group_id"`
	DeviceID       string `json:"device_id"`
}

type ConnectionGrantRequest struct {
	ActorAccountID  string `json:"actor_account_id"`
	OrganizationID  string `json:"organization_id"`
	MemberAccountID string `json:"member_account_id"`
	DeviceID        string `json:"device_id,omitempty"`
	GroupID         string `json:"group_id,omitempty"`
}

type RevokeConnectionGrantRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	GrantID        string `json:"grant_id"`
}

type CreateInviteRequest struct {
	AccountID string        `json:"account_id"`
	NetworkID string        `json:"network_id"`
	TTL       time.Duration `json:"ttl,omitempty"`
	MaxUses   int           `json:"max_uses,omitempty"`
	OneTime   bool          `json:"one_time,omitempty"`
}

type JoinDeviceRequest struct {
	Token       string `json:"token"`
	Code        string `json:"code"`
	DeviceName  string `json:"device_name"`
	Fingerprint string `json:"fingerprint,omitempty"`
	RemoteAddr  string `json:"remote_addr,omitempty"`
}

type HeartbeatDeviceRequest struct {
	AccountID          string                 `json:"account_id,omitempty"`
	NetworkID          string                 `json:"network_id,omitempty"`
	DeviceID           string                 `json:"device_id"`
	Fingerprint        string                 `json:"fingerprint,omitempty"`
	Status             DeviceStatus           `json:"status,omitempty"`
	NATProbe           *p2p.NATProbeSummary   `json:"nat_probe,omitempty"`
	PathType           p2p.PathType           `json:"path_type"`
	PathState          p2p.PathState          `json:"path_state"`
	LatencyMS          int64                  `json:"latency_ms"`
	PacketLossPermille int                    `json:"packet_loss_per_mille,omitempty"`
	JitterMS           int64                  `json:"jitter_ms,omitempty"`
	RelayBytesIn       int64                  `json:"relay_bytes_in"`
	RelayBytesOut      int64                  `json:"relay_bytes_out"`
	SwitchCount        int                    `json:"switch_count,omitempty"`
	SwitchReasons      []string               `json:"switch_reasons,omitempty"`
	SwitchFromPath     p2p.PathType           `json:"switch_from_path,omitempty"`
	SwitchToPath       p2p.PathType           `json:"switch_to_path,omitempty"`
	SwitchScoreDelta   int                    `json:"switch_score_delta,omitempty"`
	AutoSwitched       bool                   `json:"auto_switched,omitempty"`
	LastError          string                 `json:"last_error"`
	CurrentVersion     string                 `json:"current_version,omitempty"`
	RolloutID          string                 `json:"rollout_id,omitempty"`
	TargetVersion      string                 `json:"target_version,omitempty"`
	VersionStatus      DeploymentTargetStatus `json:"version_status,omitempty"`
	VersionSequence    int64                  `json:"version_sequence,omitempty"`
	VersionErrorCode   RolloutErrorCode       `json:"version_error_code,omitempty"`
}

type RevokeDeviceRequest struct {
	DeviceID string `json:"device_id"`
	Reason   string `json:"reason,omitempty"`
}

type CreateRelaySessionRequest struct {
	AccountID      string        `json:"account_id"`
	NetworkID      string        `json:"network_id"`
	SourceDeviceID string        `json:"source_device_id"`
	TargetDeviceID string        `json:"target_device_id"`
	TTL            time.Duration `json:"ttl,omitempty"`
}

type ActivateRelaySessionRequest struct {
	SessionID string `json:"session_id"`
}

type RecordRelaySessionUsageRequest struct {
	SessionID     string `json:"session_id"`
	RelayBytesIn  int64  `json:"relay_bytes_in,omitempty"`
	RelayBytesOut int64  `json:"relay_bytes_out,omitempty"`
}

type CloseRelaySessionRequest struct {
	SessionID     string `json:"session_id"`
	RelayBytesIn  int64  `json:"relay_bytes_in,omitempty"`
	RelayBytesOut int64  `json:"relay_bytes_out,omitempty"`
	Error         string `json:"error,omitempty"`
}

type RecordConnectionLogRequest struct {
	SessionID          string     `json:"session_id,omitempty"`
	AccountID          string     `json:"account_id"`
	NetworkID          string     `json:"network_id"`
	SourceDeviceID     string     `json:"source_device_id,omitempty"`
	TargetDeviceID     string     `json:"target_device_id,omitempty"`
	PathType           string     `json:"path_type,omitempty"`
	PathState          string     `json:"path_state,omitempty"`
	LatencyMS          int64      `json:"latency_ms,omitempty"`
	PacketLossPermille int        `json:"packet_loss_per_mille,omitempty"`
	JitterMS           int64      `json:"jitter_ms,omitempty"`
	RelayBytesIn       int64      `json:"relay_bytes_in,omitempty"`
	RelayBytesOut      int64      `json:"relay_bytes_out,omitempty"`
	SwitchCount        int        `json:"switch_count,omitempty"`
	SwitchReasons      []string   `json:"switch_reasons,omitempty"`
	SwitchFromPath     string     `json:"switch_from_path,omitempty"`
	SwitchToPath       string     `json:"switch_to_path,omitempty"`
	SwitchScoreDelta   int        `json:"switch_score_delta,omitempty"`
	AutoSwitched       bool       `json:"auto_switched"`
	StartedAt          time.Time  `json:"started_at,omitempty"`
	EndedAt            *time.Time `json:"ended_at,omitempty"`
	Error              string     `json:"error,omitempty"`
}

type RecordRelayUsageRequest struct {
	AccountID string `json:"account_id"`
	NetworkID string `json:"network_id"`
	DeviceID  string `json:"device_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	BytesIn   int64  `json:"bytes_in"`
	BytesOut  int64  `json:"bytes_out"`
}
