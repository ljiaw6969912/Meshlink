package cloudhub

import (
	"context"
	"errors"
	"time"

	"meshlink/internal/p2p"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrForbidden     = errors.New("forbidden")
	ErrRevoked       = errors.New("revoked")
	ErrExpired       = errors.New("expired")
	ErrQuotaExceeded = errors.New("quota exceeded")
	ErrConflict      = errors.New("conflict")
)

type AcceptOrganizationInviteStoreRequest struct {
	OrganizationID string
	AccountID      string
	TokenDigest    string
	CodeDigest     string
	Member         Membership
	Now            time.Time
	Quota          QuotaDecision
	PlanID         PlanID
}

type Store interface {
	CreateAccount(context.Context, Account) (Account, error)
	GetAccount(context.Context, string) (Account, error)
	UpdateAccount(context.Context, Account) (Account, error)
	GetAccountSubscription(context.Context, string) (Subscription, error)
	ApplySubscriptionEvent(context.Context, SubscriptionEvent) (SubscriptionEventApplyResult, error)
	RefreshAccountSubscription(context.Context, string, time.Time) (SubscriptionRefreshResult, error)

	CreateNetwork(context.Context, Network) (Network, error)
	GetNetwork(context.Context, string) (Network, error)
	ListAccountNetworks(context.Context, string) ([]Network, error)
	UpdateNetwork(context.Context, Network) (Network, error)

	CreateMember(context.Context, Member) (Member, error)

	CreateOrganization(context.Context, Organization) (Organization, error)
	GetOrganization(context.Context, string) (Organization, error)
	UpdateOrganization(context.Context, Organization) (Organization, error)
	CreateMembership(context.Context, Membership) (Membership, error)
	GetOrganizationMembership(context.Context, string, string) (Membership, error)
	ListOrganizationMemberships(context.Context, string) ([]Membership, error)
	UpdateMembership(context.Context, Membership) (Membership, error)
	CreateOrganizationInvite(context.Context, OrganizationInvite) (OrganizationInvite, error)
	GetOrganizationInvite(context.Context, string) (OrganizationInvite, error)
	GetOrganizationInviteByTokenDigest(context.Context, string) (OrganizationInvite, error)
	UpdateOrganizationInvite(context.Context, OrganizationInvite) (OrganizationInvite, error)
	AcceptOrganizationInvite(context.Context, AcceptOrganizationInviteStoreRequest) (Membership, error)
	CreateDeviceGroup(context.Context, DeviceGroup) (DeviceGroup, error)
	GetDeviceGroup(context.Context, string, string) (DeviceGroup, error)
	ListDeviceGroups(context.Context, string) ([]DeviceGroup, error)
	UpdateDeviceGroup(context.Context, DeviceGroup) (DeviceGroup, error)
	DeleteDeviceGroup(context.Context, string, string, time.Time) (DeviceGroup, error)
	CreateOrganizationDevice(context.Context, OrganizationDevice) (OrganizationDevice, error)
	GetOrganizationDevice(context.Context, string, string) (OrganizationDevice, error)
	FindOrganizationDevice(context.Context, string) (OrganizationDevice, error)
	ListOrganizationDevices(context.Context, string) ([]OrganizationDevice, error)
	UpdateOrganizationDevice(context.Context, OrganizationDevice) (OrganizationDevice, error)
	DeleteOrganizationDevice(context.Context, string, string) (OrganizationDevice, error)
	CreateDeploymentBundle(context.Context, DeploymentBundle, BootstrapCredential) (DeploymentBundle, BootstrapCredential, error)
	GetDeploymentBundle(context.Context, string, string) (DeploymentBundle, error)
	ListDeploymentBundles(context.Context, string) ([]DeploymentBundle, error)
	GetBootstrapCredential(context.Context, string, string) (BootstrapCredential, error)
	GetBootstrapCredentialByDigest(context.Context, string) (BootstrapCredential, error)
	UpdateBootstrapCredential(context.Context, BootstrapCredential) (BootstrapCredential, error)
	RedeemBootstrapCredential(context.Context, RedeemBootstrapCredentialStoreRequest) (BootstrapRedemption, error)
	CreateRollout(context.Context, Rollout, []DeploymentTarget) (Rollout, []DeploymentTarget, error)
	GetRollout(context.Context, string, string) (Rollout, error)
	FindRollout(context.Context, string) (Rollout, error)
	ListRollouts(context.Context, string) ([]Rollout, error)
	UpdateRollout(context.Context, Rollout) (Rollout, error)
	GetDeploymentTarget(context.Context, string, string, string) (DeploymentTarget, error)
	ListDeploymentTargets(context.Context, string, string) ([]DeploymentTarget, error)
	UpdateDeploymentTarget(context.Context, DeploymentTarget) (DeploymentTarget, error)
	CreateConnectionGrant(context.Context, ConnectionGrant) (ConnectionGrant, error)
	GetConnectionGrant(context.Context, string, string) (ConnectionGrant, error)
	ListConnectionGrants(context.Context, string) ([]ConnectionGrant, error)
	DeleteConnectionGrant(context.Context, string, string) (ConnectionGrant, error)

	CreateInvite(context.Context, Invite) (Invite, error)
	GetInvite(context.Context, string) (Invite, error)
	GetInviteByToken(context.Context, string) (Invite, error)
	UpdateInvite(context.Context, Invite) (Invite, error)

	CreateDevice(context.Context, Device) (Device, error)
	GetDevice(context.Context, string) (Device, error)
	UpdateDevice(context.Context, Device) (Device, error)
	ListNetworkDevices(context.Context, string) ([]Device, error)
	ReplaceP2PCandidates(context.Context, string, []p2p.Candidate) ([]p2p.Candidate, error)
	ListP2PCandidates(context.Context, string, string) ([]p2p.Candidate, error)

	CreateSession(context.Context, Session) (Session, error)
	CreateRelaySession(context.Context, RelaySession) (RelaySession, error)
	GetRelaySession(context.Context, string) (RelaySession, error)
	UpdateRelaySession(context.Context, RelaySession) (RelaySession, error)
	ListRelaySessions(context.Context, string) ([]RelaySession, error)
	CreateConnectionLog(context.Context, ConnectionLog) (ConnectionLog, error)
	ListConnectionLogs(context.Context, string) ([]ConnectionLog, error)
	ListOrganizationConnectionLogs(context.Context, string) ([]ConnectionLog, error)
	CreateRelayUsage(context.Context, RelayUsage) (RelayUsage, error)
	ListRelayUsage(context.Context, string) ([]RelayUsage, error)
	CreateBan(context.Context, Ban) (Ban, error)
	CreateRevocation(context.Context, Revocation) (Revocation, error)
	AddRiskEvent(context.Context, RiskEvent) (RiskEvent, error)
	ListRiskEvents(context.Context, string) ([]RiskEvent, error)

	AddAuditEvent(context.Context, AuditEvent) (AuditEvent, error)
	ListAuditEvents(context.Context) ([]AuditEvent, error)
	ListOrganizationAuditEvents(context.Context, string) ([]AuditEvent, error)
	DeleteOrganizationAuditBefore(context.Context, string, time.Time, int) (OrganizationAuditStoreCleanupResult, error)
}
