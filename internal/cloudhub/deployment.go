package cloudhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"meshlink/internal/licensing"
	"meshlink/internal/p2p"
)

const (
	DeploymentTemplateVersion = "1"
	DeploymentBootstrapPath   = "/api/deployments/bootstrap/redeem"
)

type DeploymentPlatform string

const (
	DeploymentPlatformWindows DeploymentPlatform = "windows"
	DeploymentPlatformLinux   DeploymentPlatform = "linux"
)

type DeploymentArchitecture string

const DeploymentArchitectureAMD64 DeploymentArchitecture = "amd64"

type DeploymentBundleStatus string

const DeploymentBundleActive DeploymentBundleStatus = "active"

type BootstrapCredentialStatus string

const (
	BootstrapCredentialActive    BootstrapCredentialStatus = "active"
	BootstrapCredentialRevoked   BootstrapCredentialStatus = "revoked"
	BootstrapCredentialExhausted BootstrapCredentialStatus = "exhausted"
	BootstrapCredentialExpired   BootstrapCredentialStatus = "expired"
)

type DeploymentFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type DeploymentArtifact struct {
	DeploymentFile
	Content string `json:"content"`
}

type DeploymentSignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

// DeploymentManifestSigner is the boundary for a future production signing
// service. Task 10D intentionally does not provide or claim a signing key.
type DeploymentManifestSigner interface {
	SignDeploymentManifest(context.Context, []byte) (DeploymentSignature, error)
}

type DeploymentManifest struct {
	BundleID        string                 `json:"bundle_id"`
	CredentialID    string                 `json:"credential_id"`
	OrganizationID  string                 `json:"organization_id"`
	GroupID         string                 `json:"group_id,omitempty"`
	Platform        DeploymentPlatform     `json:"platform"`
	Architecture    DeploymentArchitecture `json:"architecture"`
	TemplateVersion string                 `json:"template_version"`
	BootstrapPath   string                 `json:"bootstrap_path"`
	Files           []DeploymentFile       `json:"files"`
	Signature       *DeploymentSignature   `json:"signature,omitempty"`
}

type DeploymentBundle struct {
	ID                 string                 `json:"id"`
	OrganizationID     string                 `json:"organization_id"`
	GroupID            string                 `json:"group_id,omitempty"`
	Platform           DeploymentPlatform     `json:"platform"`
	Architecture       DeploymentArchitecture `json:"architecture"`
	TemplateVersion    string                 `json:"template_version"`
	Status             DeploymentBundleStatus `json:"status"`
	CredentialID       string                 `json:"credential_id"`
	ManifestFile       string                 `json:"manifest_file"`
	Files              []DeploymentFile       `json:"files"`
	CreatedByAccountID string                 `json:"created_by_account_id"`
	CreatedAt          time.Time              `json:"created_at"`
	Credential         *BootstrapCredential   `json:"credential,omitempty"`
}

type BootstrapCredential struct {
	ID                 string                    `json:"id"`
	BundleID           string                    `json:"bundle_id"`
	OrganizationID     string                    `json:"organization_id"`
	NetworkID          string                    `json:"-"`
	GroupID            string                    `json:"group_id,omitempty"`
	Platform           DeploymentPlatform        `json:"platform"`
	Architecture       DeploymentArchitecture    `json:"architecture"`
	Status             BootstrapCredentialStatus `json:"status"`
	Digest             string                    `json:"-"`
	Uses               int                       `json:"uses"`
	MaxUses            int                       `json:"max_uses"`
	CreatedByAccountID string                    `json:"created_by_account_id"`
	CreatedAt          time.Time                 `json:"created_at"`
	ExpiresAt          time.Time                 `json:"expires_at"`
	RevokedAt          *time.Time                `json:"revoked_at,omitempty"`
}

type CreateDeploymentBundleRequest struct {
	ActorAccountID string                 `json:"actor_account_id"`
	OrganizationID string                 `json:"organization_id"`
	NetworkID      string                 `json:"network_id"`
	GroupID        string                 `json:"group_id,omitempty"`
	Platform       DeploymentPlatform     `json:"platform"`
	Architecture   DeploymentArchitecture `json:"architecture"`
	TTL            time.Duration          `json:"ttl,omitempty"`
	MaxUses        int                    `json:"max_uses"`
}

type DeploymentBundleResult struct {
	Bundle     DeploymentBundle     `json:"bundle"`
	Credential string               `json:"credential"`
	Files      []DeploymentArtifact `json:"files"`
}

type RevokeBootstrapCredentialRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	CredentialID   string `json:"credential_id"`
}

type RedeemBootstrapCredentialRequest struct {
	Credential     string                 `json:"credential"`
	OrganizationID string                 `json:"organization_id"`
	GroupID        string                 `json:"group_id,omitempty"`
	Platform       DeploymentPlatform     `json:"platform"`
	Architecture   DeploymentArchitecture `json:"architecture"`
	DeviceName     string                 `json:"device_name"`
	Fingerprint    string                 `json:"fingerprint"`
	CurrentVersion string                 `json:"current_version,omitempty"`
}

type BootstrapRedemption struct {
	Device             Device              `json:"device"`
	OrganizationDevice OrganizationDevice  `json:"organization_device"`
	Credential         BootstrapCredential `json:"credential"`
}

type RedeemBootstrapCredentialStoreRequest struct {
	Digest         string
	OrganizationID string
	GroupID        string
	Platform       DeploymentPlatform
	Architecture   DeploymentArchitecture
	Now            time.Time
	Device         Device
	OrgDevice      OrganizationDevice
	Quota          QuotaDecision
	PlanID         PlanID
	Audit          AuditEvent
}

type TrustedUpdateSource string

const TrustedUpdateSourceManifest TrustedUpdateSource = "update_manifest"

type TrustedUpdateVersion struct {
	Version     string              `json:"version"`
	PackageFile string              `json:"package_file"`
	SHA256      string              `json:"sha256,omitempty"`
	Source      TrustedUpdateSource `json:"source"`
}

type RolloutStatus string

const (
	RolloutStatusPending    RolloutStatus = "pending"
	RolloutStatusInProgress RolloutStatus = "in_progress"
	RolloutStatusSucceeded  RolloutStatus = "succeeded"
	RolloutStatusFailed     RolloutStatus = "failed"
	RolloutStatusCanceled   RolloutStatus = "canceled"
)

type DeploymentTargetStatus string

const (
	DeploymentTargetPending    DeploymentTargetStatus = "pending"
	DeploymentTargetInProgress DeploymentTargetStatus = "in_progress"
	DeploymentTargetSucceeded  DeploymentTargetStatus = "succeeded"
	DeploymentTargetFailed     DeploymentTargetStatus = "failed"
	DeploymentTargetCanceled   DeploymentTargetStatus = "canceled"
)

type RolloutErrorCode string

const (
	RolloutErrorDownloadFailed RolloutErrorCode = "download_failed"
	RolloutErrorChecksumFailed RolloutErrorCode = "checksum_failed"
	RolloutErrorApplyFailed    RolloutErrorCode = "apply_failed"
	RolloutErrorHealthFailed   RolloutErrorCode = "health_check_failed"
)

type Rollout struct {
	ID                 string               `json:"id"`
	OrganizationID     string               `json:"organization_id"`
	GroupID            string               `json:"group_id,omitempty"`
	TargetVersion      string               `json:"target_version"`
	Update             TrustedUpdateVersion `json:"update"`
	Status             RolloutStatus        `json:"status"`
	CreatedByAccountID string               `json:"created_by_account_id"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	CanceledAt         *time.Time           `json:"canceled_at,omitempty"`
}

// Deployment is the stable rollout model used by deployment-oriented callers.
type Deployment = Rollout

type DeploymentTarget struct {
	ID                   string                 `json:"id"`
	RolloutID            string                 `json:"rollout_id"`
	OrganizationID       string                 `json:"organization_id"`
	DeviceID             string                 `json:"device_id"`
	GroupID              string                 `json:"group_id,omitempty"`
	Status               DeploymentTargetStatus `json:"status"`
	CurrentVersion       string                 `json:"current_version,omitempty"`
	TargetVersion        string                 `json:"target_version"`
	LastKnownGoodVersion string                 `json:"last_known_good_version,omitempty"`
	ErrorCode            RolloutErrorCode       `json:"error_code,omitempty"`
	Attempt              int                    `json:"attempt"`
	ReportSequence       int64                  `json:"report_sequence,omitempty"`
	CreatedAt            time.Time              `json:"created_at"`
	UpdatedAt            time.Time              `json:"updated_at"`
	StartedAt            *time.Time             `json:"started_at,omitempty"`
	CompletedAt          *time.Time             `json:"completed_at,omitempty"`
}

type CreateRolloutRequest struct {
	ActorAccountID string   `json:"actor_account_id"`
	OrganizationID string   `json:"organization_id"`
	GroupID        string   `json:"group_id,omitempty"`
	DeviceIDs      []string `json:"device_ids,omitempty"`
	TargetVersion  string   `json:"target_version"`
}

type CancelRolloutRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	RolloutID      string `json:"rollout_id"`
}

type RetryRolloutTargetRequest struct {
	ActorAccountID string `json:"actor_account_id"`
	OrganizationID string `json:"organization_id"`
	RolloutID      string `json:"rollout_id"`
	DeviceID       string `json:"device_id"`
}

type ReportRolloutTargetRequest struct {
	RolloutID      string                 `json:"rollout_id"`
	DeviceID       string                 `json:"device_id"`
	Sequence       int64                  `json:"sequence"`
	Status         DeploymentTargetStatus `json:"status"`
	CurrentVersion string                 `json:"current_version,omitempty"`
	TargetVersion  string                 `json:"target_version"`
	ErrorCode      RolloutErrorCode       `json:"error_code,omitempty"`
	Fingerprint    string                 `json:"fingerprint"`
}

type RolloutResult struct {
	Rollout Rollout            `json:"rollout"`
	Targets []DeploymentTarget `json:"targets"`
}

var deploymentIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var deploymentVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func (s *Service) CreateDeploymentBundle(ctx context.Context, req CreateDeploymentBundleRequest) (DeploymentBundleResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionDeploymentManage, true)
	if err != nil {
		return DeploymentBundleResult{}, err
	}
	if err := s.enforcePrivateLicenseOrganizationOperationLocked(ctx, organization.ID, actor.AccountID, licensing.OperationNewDeployment, "create_deployment_bundle"); err != nil {
		return DeploymentBundleResult{}, err
	}
	if err := s.enforcePrivateDeploymentCountLocked(ctx, organization, actor.AccountID); err != nil {
		return DeploymentBundleResult{}, err
	}
	platform, architecture, err := normalizeDeploymentTarget(req.Platform, req.Architecture)
	if err != nil {
		return DeploymentBundleResult{}, err
	}
	network, err := s.store.GetNetwork(ctx, strings.TrimSpace(req.NetworkID))
	if err != nil || network.AccountID != organization.OwnerAccountID {
		return DeploymentBundleResult{}, fmt.Errorf("deployment network is not available to organization: %w", ErrForbidden)
	}
	groupID := strings.TrimSpace(req.GroupID)
	if groupID != "" {
		group, groupErr := s.store.GetDeviceGroup(ctx, organization.ID, groupID)
		if groupErr != nil || group.DeletedAt != nil {
			return DeploymentBundleResult{}, fmt.Errorf("device group was not found: %w", ErrNotFound)
		}
	}
	ttl := req.TTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return DeploymentBundleResult{}, fmt.Errorf("deployment credential ttl must be between 1 minute and 24 hours")
	}
	if req.MaxUses <= 0 || req.MaxUses > 1000 {
		return DeploymentBundleResult{}, fmt.Errorf("max_uses must be between 1 and 1000")
	}
	secret, err := randomTokenBytes(32)
	if err != nil {
		return DeploymentBundleResult{}, err
	}
	now := s.nowTime()
	bundleID, credentialID := mustID("bundle"), mustID("boot")
	artifacts, manifestFile, err := renderDeploymentArtifacts(bundleID, credentialID, organization.ID, groupID, platform, architecture, secret)
	if err != nil {
		return DeploymentBundleResult{}, err
	}
	files := make([]DeploymentFile, len(artifacts))
	for i := range artifacts {
		files[i] = artifacts[i].DeploymentFile
	}
	bundle := DeploymentBundle{
		ID: bundleID, OrganizationID: organization.ID, GroupID: groupID, Platform: platform, Architecture: architecture,
		TemplateVersion: DeploymentTemplateVersion, Status: DeploymentBundleActive, CredentialID: credentialID,
		ManifestFile: manifestFile, Files: files, CreatedByAccountID: actor.AccountID, CreatedAt: now,
	}
	credential := BootstrapCredential{
		ID: credentialID, BundleID: bundleID, OrganizationID: organization.ID, NetworkID: network.ID, GroupID: groupID,
		Platform: platform, Architecture: architecture, Status: BootstrapCredentialActive,
		Digest: hashBootstrapCredential(secret), MaxUses: req.MaxUses, CreatedByAccountID: actor.AccountID,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	created, _, err := s.store.CreateDeploymentBundle(ctx, bundle, credential)
	if err != nil {
		return DeploymentBundleResult{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditDeploymentBundleCreated, actor.AccountID, organization.ID, "", "create_deployment_bundle", "created", map[string]any{
		"bundle_id": created.ID, "credential_id": credential.ID, "group_id": groupID,
		"platform": string(platform), "architecture": string(architecture), "template_version": DeploymentTemplateVersion,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return DeploymentBundleResult{}, err
	}
	created.Credential = publicBootstrapCredential(credential, now)
	return DeploymentBundleResult{Bundle: created, Credential: secret, Files: artifacts}, nil
}

func (s *Service) GetDeploymentBundle(ctx context.Context, actorAccountID, organizationID, bundleID string) (DeploymentBundle, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionDeploymentManage, true); err != nil {
		return DeploymentBundle{}, err
	}
	bundle, err := s.store.GetDeploymentBundle(ctx, strings.TrimSpace(organizationID), strings.TrimSpace(bundleID))
	if err != nil {
		return DeploymentBundle{}, err
	}
	credential, err := s.store.GetBootstrapCredential(ctx, bundle.OrganizationID, bundle.CredentialID)
	if err != nil {
		return DeploymentBundle{}, err
	}
	bundle.Credential = publicBootstrapCredential(credential, s.nowTime())
	return bundle, nil
}

func (s *Service) ListDeploymentBundles(ctx context.Context, actorAccountID, organizationID string) ([]DeploymentBundle, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionDeploymentManage, true); err != nil {
		return nil, err
	}
	bundles, err := s.store.ListDeploymentBundles(ctx, strings.TrimSpace(organizationID))
	if err != nil {
		return nil, err
	}
	for i := range bundles {
		credential, credentialErr := s.store.GetBootstrapCredential(ctx, bundles[i].OrganizationID, bundles[i].CredentialID)
		if credentialErr != nil {
			return nil, credentialErr
		}
		bundles[i].Credential = publicBootstrapCredential(credential, s.nowTime())
	}
	return bundles, nil
}

func (s *Service) RevokeBootstrapCredential(ctx context.Context, req RevokeBootstrapCredentialRequest) (BootstrapCredential, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionDeploymentManage, true)
	if err != nil {
		return BootstrapCredential{}, err
	}
	credential, err := s.store.GetBootstrapCredential(ctx, organization.ID, strings.TrimSpace(req.CredentialID))
	if err != nil {
		return BootstrapCredential{}, fmt.Errorf("bootstrap credential was not found: %w", ErrNotFound)
	}
	if credential.RevokedAt != nil || credential.Status == BootstrapCredentialRevoked {
		return *publicBootstrapCredential(credential, s.nowTime()), nil
	}
	now := s.nowTime()
	credential.Status = BootstrapCredentialRevoked
	credential.RevokedAt = &now
	updated, err := s.store.UpdateBootstrapCredential(ctx, credential)
	if err != nil {
		return BootstrapCredential{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditBootstrapCredentialRevoked, actor.AccountID, organization.ID, "", "revoke_bootstrap_credential", "revoked", map[string]any{
		"bundle_id": credential.BundleID, "credential_id": credential.ID,
		"group_id": credential.GroupID, "platform": string(credential.Platform), "architecture": string(credential.Architecture),
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return BootstrapCredential{}, err
	}
	return *publicBootstrapCredential(updated, now), nil
}

func (s *Service) RedeemBootstrapCredential(ctx context.Context, req RedeemBootstrapCredentialRequest) (BootstrapRedemption, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	secret := strings.TrimSpace(req.Credential)
	if secret == "" {
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential is required")
	}
	credential, err := s.store.GetBootstrapCredentialByDigest(ctx, hashBootstrapCredential(secret))
	if err != nil {
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential was not found: %w", ErrNotFound)
	}
	platform, architecture, err := normalizeDeploymentTarget(req.Platform, req.Architecture)
	if err != nil {
		return BootstrapRedemption{}, err
	}
	organizationID := strings.TrimSpace(req.OrganizationID)
	groupID := strings.TrimSpace(req.GroupID)
	if organizationID != credential.OrganizationID || groupID != credential.GroupID || platform != credential.Platform || architecture != credential.Architecture {
		s.recordBootstrapRedemptionFailure(ctx, credential, "binding_mismatch")
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential binding mismatch: %w", ErrForbidden)
	}
	deviceName := strings.TrimSpace(req.DeviceName)
	fingerprint := strings.TrimSpace(req.Fingerprint)
	currentVersion := strings.TrimSpace(req.CurrentVersion)
	if !deploymentIdentityPattern.MatchString(deviceName) || !deploymentIdentityPattern.MatchString(fingerprint) {
		return BootstrapRedemption{}, fmt.Errorf("device identity is invalid")
	}
	if currentVersion != "" && !deploymentVersionPattern.MatchString(currentVersion) {
		return BootstrapRedemption{}, fmt.Errorf("current_version is invalid")
	}
	organization, err := s.store.GetOrganization(ctx, credential.OrganizationID)
	if err != nil || ensureOrganizationActive(organization) != nil {
		s.recordBootstrapRedemptionFailure(ctx, credential, "organization_inactive")
		return BootstrapRedemption{}, fmt.Errorf("organization is not active: %w", ErrForbidden)
	}
	owner, err := s.activeOrganizationOwnerLocked(ctx, organization)
	if err != nil {
		s.recordBootstrapRedemptionFailure(ctx, credential, "owner_inactive")
		return BootstrapRedemption{}, err
	}
	if err := s.enforcePrivateLicenseOrganizationOperationLocked(ctx, organization.ID, owner.ID, licensing.OperationNewDevice, "redeem_bootstrap_credential"); err != nil {
		s.recordBootstrapRedemptionFailure(ctx, credential, "license_denied")
		return BootstrapRedemption{}, err
	}
	ownerMembership, err := s.store.GetOrganizationMembership(ctx, organization.ID, owner.ID)
	if err != nil || ownerMembership.Status != MembershipStatusActive {
		s.recordBootstrapRedemptionFailure(ctx, credential, "membership_inactive")
		return BootstrapRedemption{}, fmt.Errorf("organization owner membership is not active: %w", ErrForbidden)
	}
	credentialCreator, err := s.store.GetAccount(ctx, credential.CreatedByAccountID)
	if err != nil || ensureAccountActive(credentialCreator) != nil {
		s.recordBootstrapRedemptionFailure(ctx, credential, "credential_creator_inactive")
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential creator account is not active: %w", ErrForbidden)
	}
	creatorMembership, err := s.store.GetOrganizationMembership(ctx, organization.ID, credentialCreator.ID)
	if err != nil || creatorMembership.Status != MembershipStatusActive || (creatorMembership.Role != MembershipRoleOwner && creatorMembership.Role != MembershipRoleAdmin) {
		s.recordBootstrapRedemptionFailure(ctx, credential, "credential_creator_membership_inactive")
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential creator membership is not active or authorized: %w", ErrForbidden)
	}
	plan, err := s.planForAccount(owner)
	if err != nil {
		return BootstrapRedemption{}, err
	}
	quota, err := s.evaluateQuotaLocked(ctx, owner.ID, QuotaDimensionDeviceCount, plan.Entitlements.DeviceCount)
	if err != nil {
		return BootstrapRedemption{}, withQuotaPlan(err, plan.ID)
	}
	now := s.nowTime()
	device := Device{
		ConnectionStatus: p2p.NormalizeConnectionStatus(p2p.ConnectionStatus{}, p2p.ConnectionStatusDefaults{}),
		ID:               mustID("dev"), AccountID: owner.ID, NetworkID: credential.NetworkID, Name: deviceName,
		Fingerprint: fingerprint, Status: DeviceStatusOffline, JoinedAt: now, CurrentVersion: currentVersion,
	}
	organizationDevice := OrganizationDevice{
		ID: mustID("orgdev"), OrganizationID: organization.ID, DeviceID: device.ID, AccountID: owner.ID,
		GroupID: credential.GroupID, EnrolledByAccountID: credential.CreatedByAccountID, CreatedAt: now, UpdatedAt: now,
	}
	audit := AuditEvent{
		ID: mustID("audit"), Time: now, Event: AuditBootstrapCredentialRedeemed,
		AccountID: credential.CreatedByAccountID, NetworkID: credential.NetworkID, DeviceID: device.ID,
		Metadata: map[string]any{
			"organization_id": organization.ID, "bundle_id": credential.BundleID, "credential_id": credential.ID,
			"target_device_id": device.ID, "group_id": credential.GroupID, "action": "redeem_bootstrap_credential",
			"result": "redeemed", "actor_type": "bootstrap_credential", "platform": string(platform),
			"architecture": string(architecture), "current_version": currentVersion,
		},
	}
	redemption, err := s.store.RedeemBootstrapCredential(ctx, RedeemBootstrapCredentialStoreRequest{
		Digest: credential.Digest, OrganizationID: organization.ID, GroupID: credential.GroupID,
		Platform: platform, Architecture: architecture, Now: now, Device: device, OrgDevice: organizationDevice,
		Quota: quota, PlanID: plan.ID, Audit: audit,
	})
	if err != nil {
		s.recordBootstrapRedemptionFailure(ctx, credential, bootstrapRedemptionFailureResult(err))
		return BootstrapRedemption{}, err
	}
	redemption.Credential = *publicBootstrapCredential(redemption.Credential, now)
	return redemption, nil
}

func (s *Service) recordBootstrapRedemptionFailure(ctx context.Context, credential BootstrapCredential, result string) {
	_ = s.recordOrganizationAudit(ctx, AuditBootstrapCredentialRedeemed, credential.CreatedByAccountID, credential.OrganizationID, "", "redeem_bootstrap_credential", result, map[string]any{
		"bundle_id": credential.BundleID, "credential_id": credential.ID, "group_id": credential.GroupID,
		"platform": string(credential.Platform), "architecture": string(credential.Architecture), "actor_type": "bootstrap_credential",
	})
}

func (s *Service) CreateRollout(ctx context.Context, req CreateRolloutRequest) (RolloutResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionRolloutManage, true)
	if err != nil {
		return RolloutResult{}, err
	}
	if err := s.enforcePrivateLicenseOrganizationOperationLocked(ctx, organization.ID, actor.AccountID, licensing.OperationNewRollout, "create_rollout"); err != nil {
		return RolloutResult{}, err
	}
	version := strings.TrimSpace(req.TargetVersion)
	trusted, ok := s.trustedUpdates[version]
	if !ok {
		return RolloutResult{}, fmt.Errorf("target version is not in the trusted update catalog: %w", ErrForbidden)
	}
	groupID := strings.TrimSpace(req.GroupID)
	if groupID != "" && len(req.DeviceIDs) > 0 {
		return RolloutResult{}, fmt.Errorf("group_id and device_ids cannot be combined")
	}
	if groupID != "" {
		group, groupErr := s.store.GetDeviceGroup(ctx, organization.ID, groupID)
		if groupErr != nil || group.DeletedAt != nil {
			return RolloutResult{}, fmt.Errorf("device group was not found: %w", ErrNotFound)
		}
	}
	organizationDevices, err := s.store.ListOrganizationDevices(ctx, organization.ID)
	if err != nil {
		return RolloutResult{}, err
	}
	requested := map[string]struct{}{}
	for _, id := range req.DeviceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return RolloutResult{}, fmt.Errorf("device_ids contains an empty id")
		}
		requested[id] = struct{}{}
	}
	now := s.nowTime()
	rollout := Rollout{
		ID: mustID("rollout"), OrganizationID: organization.ID, GroupID: groupID, TargetVersion: version,
		Update: trusted, Status: RolloutStatusPending, CreatedByAccountID: actor.AccountID, CreatedAt: now, UpdatedAt: now,
	}
	var targets []DeploymentTarget
	seen := map[string]struct{}{}
	for _, organizationDevice := range organizationDevices {
		if groupID != "" && organizationDevice.GroupID != groupID {
			continue
		}
		if len(requested) > 0 {
			if _, ok := requested[organizationDevice.DeviceID]; !ok {
				continue
			}
		}
		device, deviceErr := s.store.GetDevice(ctx, organizationDevice.DeviceID)
		if deviceErr != nil || device.Status == DeviceStatusRevoked || device.RevokedAt != nil {
			continue
		}
		if !deploymentIdentityPattern.MatchString(strings.TrimSpace(device.Fingerprint)) {
			continue
		}
		seen[device.ID] = struct{}{}
		if device.CurrentVersion == version {
			continue
		}
		targets = append(targets, DeploymentTarget{
			ID: mustID("target"), RolloutID: rollout.ID, OrganizationID: organization.ID, DeviceID: device.ID,
			GroupID: organizationDevice.GroupID, Status: DeploymentTargetPending, CurrentVersion: device.CurrentVersion,
			LastKnownGoodVersion: device.CurrentVersion, TargetVersion: version, Attempt: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	if len(requested) > 0 && len(seen) != len(requested) {
		return RolloutResult{}, fmt.Errorf("one or more rollout devices were not found in organization: %w", ErrNotFound)
	}
	if len(targets) == 0 {
		return RolloutResult{}, fmt.Errorf("no eligible devices require target version: %w", ErrConflict)
	}
	created, createdTargets, err := s.store.CreateRollout(ctx, rollout, targets)
	if err != nil {
		return RolloutResult{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditRolloutCreated, actor.AccountID, organization.ID, "", "create_rollout", "created", map[string]any{
		"rollout_id": created.ID, "group_id": groupID, "target_version": version, "target_count": len(createdTargets),
		"update_source": string(trusted.Source), "package_file": trusted.PackageFile,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return RolloutResult{}, err
	}
	return RolloutResult{Rollout: created, Targets: createdTargets}, nil
}

func (s *Service) GetRollout(ctx context.Context, actorAccountID, organizationID, rolloutID string) (RolloutResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionRolloutManage, true); err != nil {
		return RolloutResult{}, err
	}
	rollout, err := s.store.GetRollout(ctx, strings.TrimSpace(organizationID), strings.TrimSpace(rolloutID))
	if err != nil {
		return RolloutResult{}, err
	}
	targets, err := s.store.ListDeploymentTargets(ctx, rollout.OrganizationID, rollout.ID)
	return RolloutResult{Rollout: rollout, Targets: targets}, err
}

func (s *Service) ListRollouts(ctx context.Context, actorAccountID, organizationID string) ([]Rollout, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionRolloutManage, true); err != nil {
		return nil, err
	}
	return s.store.ListRollouts(ctx, strings.TrimSpace(organizationID))
}

func (s *Service) GetRolloutTarget(ctx context.Context, actorAccountID, organizationID, rolloutID, deviceID string) (DeploymentTarget, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, _, _, err := s.authorizeOrganizationLocked(ctx, organizationID, actorAccountID, permissionRolloutManage, true); err != nil {
		return DeploymentTarget{}, err
	}
	return s.store.GetDeploymentTarget(ctx, strings.TrimSpace(organizationID), strings.TrimSpace(rolloutID), strings.TrimSpace(deviceID))
}

func (s *Service) ReportRolloutTarget(ctx context.Context, req ReportRolloutTargetRequest) (DeploymentTarget, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.reportRolloutTargetLocked(ctx, req)
}

func (s *Service) reportRolloutTargetLocked(ctx context.Context, req ReportRolloutTargetRequest) (DeploymentTarget, error) {
	rollout, err := s.store.FindRollout(ctx, strings.TrimSpace(req.RolloutID))
	if err != nil {
		return DeploymentTarget{}, fmt.Errorf("rollout was not found: %w", ErrNotFound)
	}
	target, err := s.store.GetDeploymentTarget(ctx, rollout.OrganizationID, rollout.ID, strings.TrimSpace(req.DeviceID))
	if err != nil {
		return DeploymentTarget{}, fmt.Errorf("rollout target was not found: %w", ErrNotFound)
	}
	device, err := s.store.GetDevice(ctx, target.DeviceID)
	if err != nil {
		return DeploymentTarget{}, fmt.Errorf("rollout device was not found: %w", ErrNotFound)
	}
	if strings.TrimSpace(req.Fingerprint) == "" || !constantTimeStringEqual(strings.TrimSpace(req.Fingerprint), device.Fingerprint) {
		s.recordRolloutTargetReportAudit(ctx, rollout, target, device.AccountID, "identity_mismatch")
		return DeploymentTarget{}, fmt.Errorf("rollout device identity mismatch: %w", ErrForbidden)
	}
	if req.Sequence <= 0 {
		return DeploymentTarget{}, fmt.Errorf("report sequence must be positive")
	}
	if strings.TrimSpace(req.TargetVersion) != target.TargetVersion {
		return DeploymentTarget{}, fmt.Errorf("rollout target version mismatch: %w", ErrForbidden)
	}
	if req.Sequence <= target.ReportSequence || target.Status == DeploymentTargetSucceeded || target.Status == DeploymentTargetCanceled {
		s.recordRolloutTargetReportAudit(ctx, rollout, target, device.AccountID, "ignored_stale")
		return target, nil
	}
	if !validDeploymentTargetReportStatus(req.Status) {
		return DeploymentTarget{}, fmt.Errorf("rollout target status is invalid")
	}
	if target.Status == DeploymentTargetFailed {
		return DeploymentTarget{}, fmt.Errorf("failed rollout target must be retried by an administrator: %w", ErrConflict)
	}
	if req.CurrentVersion != "" && !deploymentVersionPattern.MatchString(strings.TrimSpace(req.CurrentVersion)) {
		return DeploymentTarget{}, fmt.Errorf("current_version is invalid")
	}
	if req.Status == DeploymentTargetFailed {
		if !validRolloutErrorCode(req.ErrorCode) {
			return DeploymentTarget{}, fmt.Errorf("controlled error_code is required for failed rollout")
		}
	} else if req.ErrorCode != "" {
		return DeploymentTarget{}, fmt.Errorf("error_code is only accepted for failed rollout")
	}
	now := s.nowTime()
	target.ReportSequence = req.Sequence
	target.UpdatedAt = now
	if target.StartedAt == nil {
		target.StartedAt = &now
	}
	switch req.Status {
	case DeploymentTargetInProgress:
		if req.CurrentVersion != "" && req.CurrentVersion != target.TargetVersion {
			target.CurrentVersion = strings.TrimSpace(req.CurrentVersion)
			target.LastKnownGoodVersion = target.CurrentVersion
		}
	case DeploymentTargetSucceeded:
		if strings.TrimSpace(req.CurrentVersion) != target.TargetVersion {
			return DeploymentTarget{}, fmt.Errorf("successful rollout must report the target version: %w", ErrConflict)
		}
		target.CurrentVersion = target.TargetVersion
		target.LastKnownGoodVersion = target.TargetVersion
		target.CompletedAt = &now
	case DeploymentTargetFailed:
		target.ErrorCode = req.ErrorCode
		target.CurrentVersion = target.LastKnownGoodVersion
		target.CompletedAt = &now
	}
	target.Status = req.Status
	updated, err := s.store.UpdateDeploymentTarget(ctx, target)
	if err != nil {
		return DeploymentTarget{}, err
	}
	if updated.Status == DeploymentTargetSucceeded {
		device, deviceErr := s.store.GetDevice(ctx, updated.DeviceID)
		if deviceErr != nil {
			return DeploymentTarget{}, deviceErr
		}
		device.CurrentVersion = updated.TargetVersion
		device.RolloutID = ""
		device.TargetVersion = ""
		if _, deviceErr = s.store.UpdateDevice(ctx, device); deviceErr != nil {
			return DeploymentTarget{}, deviceErr
		}
	}
	if rollout.Status != RolloutStatusCanceled {
		if _, err := s.refreshRolloutStatusLocked(ctx, rollout); err != nil {
			return DeploymentTarget{}, err
		}
	}
	result := "reported"
	if updated.ReportSequence != req.Sequence || updated.Status != req.Status {
		result = "ignored_stale"
	}
	s.recordRolloutTargetReportAudit(ctx, rollout, updated, device.AccountID, result)
	return updated, nil
}

func (s *Service) recordRolloutTargetReportAudit(ctx context.Context, rollout Rollout, target DeploymentTarget, actorAccountID, result string) {
	_ = s.recordOrganizationAudit(ctx, AuditRolloutTargetReported, actorAccountID, rollout.OrganizationID, "", "report_rollout_target", result, map[string]any{
		"rollout_id": rollout.ID, "target_id": target.ID, "target_device_id": target.DeviceID, "group_id": target.GroupID,
		"status": string(target.Status), "target_version": target.TargetVersion, "current_version": target.CurrentVersion,
		"attempt": target.Attempt, "report_sequence": target.ReportSequence, "error_code": string(target.ErrorCode), "actor_type": "device",
	})
}

func (s *Service) RetryRolloutTarget(ctx context.Context, req RetryRolloutTargetRequest) (DeploymentTarget, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionRolloutManage, true)
	if err != nil {
		return DeploymentTarget{}, err
	}
	rollout, err := s.store.GetRollout(ctx, organization.ID, strings.TrimSpace(req.RolloutID))
	if err != nil {
		return DeploymentTarget{}, fmt.Errorf("rollout was not found: %w", ErrNotFound)
	}
	if rollout.Status == RolloutStatusCanceled {
		return DeploymentTarget{}, fmt.Errorf("canceled rollout cannot be retried: %w", ErrConflict)
	}
	target, err := s.store.GetDeploymentTarget(ctx, organization.ID, rollout.ID, strings.TrimSpace(req.DeviceID))
	if err != nil {
		return DeploymentTarget{}, fmt.Errorf("rollout target was not found: %w", ErrNotFound)
	}
	if target.Status != DeploymentTargetFailed {
		return DeploymentTarget{}, fmt.Errorf("only a failed rollout target can be retried: %w", ErrConflict)
	}
	now := s.nowTime()
	target.Status = DeploymentTargetPending
	target.Attempt++
	target.ReportSequence = 0
	target.ErrorCode = ""
	target.StartedAt = nil
	target.CompletedAt = nil
	target.UpdatedAt = now
	updated, err := s.store.UpdateDeploymentTarget(ctx, target)
	if err != nil {
		return DeploymentTarget{}, err
	}
	rollout.Status = RolloutStatusInProgress
	rollout.UpdatedAt = now
	if _, err := s.store.UpdateRollout(ctx, rollout); err != nil {
		return DeploymentTarget{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditRolloutTargetRetried, actor.AccountID, organization.ID, "", "retry_rollout_target", "retried", map[string]any{
		"rollout_id": rollout.ID, "target_id": updated.ID, "target_device_id": updated.DeviceID,
		"group_id": updated.GroupID, "target_version": updated.TargetVersion, "attempt": updated.Attempt,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return DeploymentTarget{}, err
	}
	return updated, nil
}

func (s *Service) CancelRollout(ctx context.Context, req CancelRolloutRequest) (RolloutResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	organization, _, actor, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionRolloutManage, true)
	if err != nil {
		return RolloutResult{}, err
	}
	rollout, err := s.store.GetRollout(ctx, organization.ID, strings.TrimSpace(req.RolloutID))
	if err != nil {
		return RolloutResult{}, fmt.Errorf("rollout was not found: %w", ErrNotFound)
	}
	targets, err := s.store.ListDeploymentTargets(ctx, organization.ID, rollout.ID)
	if err != nil {
		return RolloutResult{}, err
	}
	if rollout.Status == RolloutStatusCanceled {
		return RolloutResult{Rollout: rollout, Targets: targets}, nil
	}
	now := s.nowTime()
	for i := range targets {
		if targets[i].Status != DeploymentTargetPending {
			continue
		}
		targets[i].Status = DeploymentTargetCanceled
		targets[i].UpdatedAt = now
		targets[i].CompletedAt = &now
		updated, updateErr := s.store.UpdateDeploymentTarget(ctx, targets[i])
		if updateErr != nil {
			return RolloutResult{}, updateErr
		}
		targets[i] = updated
	}
	rollout.Status = RolloutStatusCanceled
	rollout.UpdatedAt = now
	rollout.CanceledAt = &now
	rollout, err = s.store.UpdateRollout(ctx, rollout)
	if err != nil {
		return RolloutResult{}, err
	}
	if err := s.recordOrganizationAudit(ctx, AuditRolloutCanceled, actor.AccountID, organization.ID, "", "cancel_rollout", "canceled", map[string]any{
		"rollout_id": rollout.ID, "group_id": rollout.GroupID, "target_version": rollout.TargetVersion,
		"permission_source": string(managementPermissionSource(actor.Role)),
	}); err != nil {
		return RolloutResult{}, err
	}
	return RolloutResult{Rollout: rollout, Targets: targets}, nil
}

func (s *Service) refreshRolloutStatusLocked(ctx context.Context, rollout Rollout) (Rollout, error) {
	targets, err := s.store.ListDeploymentTargets(ctx, rollout.OrganizationID, rollout.ID)
	if err != nil {
		return Rollout{}, err
	}
	allSucceeded, allTerminal, anyFailed, anyStarted := true, true, false, false
	for _, target := range targets {
		if target.Status != DeploymentTargetSucceeded {
			allSucceeded = false
		}
		if target.Status == DeploymentTargetPending || target.Status == DeploymentTargetInProgress {
			allTerminal = false
		}
		if target.Status == DeploymentTargetFailed {
			anyFailed = true
		}
		if target.Status != DeploymentTargetPending {
			anyStarted = true
		}
	}
	switch {
	case allSucceeded:
		rollout.Status = RolloutStatusSucceeded
	case allTerminal && anyFailed:
		rollout.Status = RolloutStatusFailed
	case anyStarted:
		rollout.Status = RolloutStatusInProgress
	default:
		rollout.Status = RolloutStatusPending
	}
	rollout.UpdatedAt = s.nowTime()
	return s.store.UpdateRollout(ctx, rollout)
}

func renderDeploymentArtifacts(bundleID, credentialID, organizationID, groupID string, platform DeploymentPlatform, architecture DeploymentArchitecture, secret string) ([]DeploymentArtifact, string, error) {
	prefix := fmt.Sprintf("meshlink-bootstrap-%s-%s-v%s", platform, architecture, DeploymentTemplateVersion)
	scriptName := prefix + ".sh"
	script := linuxDeploymentScript(secret, organizationID, groupID)
	if platform == DeploymentPlatformWindows {
		scriptName = prefix + ".ps1"
		script = windowsDeploymentScript(secret, organizationID, groupID)
	}
	configName := prefix + ".json"
	configBytes, err := json.MarshalIndent(map[string]any{
		"architecture": architecture, "bootstrap_path": DeploymentBootstrapPath, "group_id": groupID,
		"organization_id": organizationID, "platform": platform, "template_version": DeploymentTemplateVersion,
	}, "", "  ")
	if err != nil {
		return nil, "", err
	}
	config := string(append(configBytes, '\n'))
	artifacts := []DeploymentArtifact{deploymentArtifact(scriptName, script), deploymentArtifact(configName, config)}
	manifest := DeploymentManifest{
		BundleID: bundleID, CredentialID: credentialID, OrganizationID: organizationID, GroupID: groupID,
		Platform: platform, Architecture: architecture, TemplateVersion: DeploymentTemplateVersion,
		BootstrapPath: DeploymentBootstrapPath,
		Files:         []DeploymentFile{artifacts[0].DeploymentFile, artifacts[1].DeploymentFile},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, "", err
	}
	manifestName := prefix + ".manifest.json"
	artifacts = append(artifacts, deploymentArtifact(manifestName, string(append(manifestBytes, '\n'))))
	return artifacts, manifestName, nil
}

func windowsDeploymentScript(secret, organizationID, groupID string) string {
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$StatePath = Join-Path $env:ProgramData 'Meshlink\configs\official-hub.json'
if (-not (Test-Path -LiteralPath $StatePath)) { throw 'Official Hub configuration is required before bootstrap.' }
$HubState = Get-Content -Raw -LiteralPath $StatePath | ConvertFrom-Json
$HubUri = [Uri]$HubState.hub_api_url
if ($HubUri.Scheme -ne 'https') { throw 'Official Hub bootstrap requires HTTPS.' }
$Body = @{
  credential = '%s'
  organization_id = '%s'
  group_id = '%s'
  platform = 'windows'
  architecture = 'amd64'
  device_name = $env:COMPUTERNAME
  fingerprint = ('sha256:' + (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $PSScriptRoot 'mesh-agent.exe')).Hash.ToLowerInvariant())
} | ConvertTo-Json
Invoke-RestMethod -Method Post -ContentType 'application/json' -Uri ([Uri]::new($HubUri, '%s')) -Body $Body | Out-Null
`, secret, organizationID, groupID, DeploymentBootstrapPath)
}

func linuxDeploymentScript(secret, organizationID, groupID string) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu
state=/etc/meshlink/official-hub.json
test -f "$state" || { echo 'Official Hub configuration is required before bootstrap.' >&2; exit 1; }
hub=$(sed -n 's/.*"hub_api_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$state")
case "$hub" in https://*) ;; *) echo 'Official Hub bootstrap requires HTTPS.' >&2; exit 1;; esac
fingerprint="sha256:$(sha256sum ./mesh-agent | awk '{print $1}')"
curl --fail --silent --show-error --proto '=https' --tlsv1.2 \
  -H 'Content-Type: application/json' \
  --data '{"credential":"%s","organization_id":"%s","group_id":"%s","platform":"linux","architecture":"amd64","device_name":"'"$(hostname)"'","fingerprint":"'"$fingerprint"'"}' \
  "${hub}%s"
`, secret, organizationID, groupID, DeploymentBootstrapPath)
}

func deploymentArtifact(name, content string) DeploymentArtifact {
	sum := sha256.Sum256([]byte(content))
	return DeploymentArtifact{DeploymentFile: DeploymentFile{Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))}, Content: content}
}

func normalizeDeploymentTarget(platform DeploymentPlatform, architecture DeploymentArchitecture) (DeploymentPlatform, DeploymentArchitecture, error) {
	platform = DeploymentPlatform(strings.TrimSpace(string(platform)))
	architecture = DeploymentArchitecture(strings.TrimSpace(string(architecture)))
	if platform != DeploymentPlatformWindows && platform != DeploymentPlatformLinux {
		return "", "", fmt.Errorf("deployment platform %q is not supported", platform)
	}
	if architecture != DeploymentArchitectureAMD64 {
		return "", "", fmt.Errorf("deployment architecture %q is not supported", architecture)
	}
	return platform, architecture, nil
}

func normalizeTrustedUpdateVersion(version TrustedUpdateVersion) (TrustedUpdateVersion, error) {
	version.Version = strings.TrimSpace(version.Version)
	version.PackageFile = strings.TrimSpace(version.PackageFile)
	version.SHA256 = strings.ToLower(strings.TrimSpace(version.SHA256))
	if version.Source == "" {
		version.Source = TrustedUpdateSourceManifest
	}
	if !deploymentVersionPattern.MatchString(version.Version) {
		return TrustedUpdateVersion{}, fmt.Errorf("trusted update version is invalid")
	}
	if version.PackageFile != "meshlink-"+version.Version+".zip" {
		return TrustedUpdateVersion{}, fmt.Errorf("trusted update package filename does not match version")
	}
	if version.SHA256 != "" {
		if len(version.SHA256) != 64 {
			return TrustedUpdateVersion{}, fmt.Errorf("trusted update sha256 is invalid")
		}
		if _, err := hex.DecodeString(version.SHA256); err != nil {
			return TrustedUpdateVersion{}, fmt.Errorf("trusted update sha256 is invalid")
		}
	}
	if version.Source != TrustedUpdateSourceManifest {
		return TrustedUpdateVersion{}, fmt.Errorf("trusted update source is invalid")
	}
	return version, nil
}

func hashBootstrapCredential(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return hex.EncodeToString(sum[:])
}

func bootstrapRedemptionFailureResult(err error) string {
	switch {
	case errors.Is(err, ErrQuotaExceeded):
		return "quota_exceeded"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrRevoked):
		return "revoked"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	default:
		return "rejected"
	}
}

func validDeploymentTargetReportStatus(status DeploymentTargetStatus) bool {
	return status == DeploymentTargetInProgress || status == DeploymentTargetSucceeded || status == DeploymentTargetFailed
}

func validRolloutErrorCode(code RolloutErrorCode) bool {
	switch code {
	case RolloutErrorDownloadFailed, RolloutErrorChecksumFailed, RolloutErrorApplyFailed, RolloutErrorHealthFailed:
		return true
	default:
		return false
	}
}

func cloneDeploymentBundle(bundle DeploymentBundle) DeploymentBundle {
	bundle.Files = append([]DeploymentFile(nil), bundle.Files...)
	if bundle.Credential != nil {
		credential := *bundle.Credential
		bundle.Credential = &credential
	}
	return bundle
}

func publicBootstrapCredential(credential BootstrapCredential, now time.Time) *BootstrapCredential {
	credential.Digest = ""
	credential.NetworkID = ""
	if credential.RevokedAt != nil {
		credential.Status = BootstrapCredentialRevoked
	} else if !credential.ExpiresAt.IsZero() && !now.Before(credential.ExpiresAt) {
		credential.Status = BootstrapCredentialExpired
	} else if credential.MaxUses > 0 && credential.Uses >= credential.MaxUses {
		credential.Status = BootstrapCredentialExhausted
	}
	return &credential
}

func sortDeploymentTargets(targets []DeploymentTarget) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].CreatedAt.Equal(targets[j].CreatedAt) {
			return targets[i].ID < targets[j].ID
		}
		return targets[i].CreatedAt.Before(targets[j].CreatedAt)
	})
}
