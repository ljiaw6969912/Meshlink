package cloudhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/licensing"
)

type privateLicenseFixture struct {
	t       *testing.T
	ctx     context.Context
	now     *time.Time
	svc     *Service
	store   *MemoryStore
	manager *licensing.Manager
	priv    ed25519.PrivateKey
	owner   Account
	org     Organization
	network Network
	expires time.Time
}

func TestPrivateHubCanBootstrapConfiguredOrganizationThenImportLicense(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := licensing.NewManager(licensing.ManagerConfig{
		Path: filepath.Join(t.TempDir(), "private-license.json"), TrustedKeys: licensing.TrustedKeys{"license-2026": publicKey},
		Binding: licensing.Binding{OrganizationID: "org-private-bootstrap", DeploymentID: "deployment-private-bootstrap"}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(NewMemoryStore(), WithNow(func() time.Time { return now }), WithPrivateLicenseManager(manager))
	owner, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "bootstrap-owner@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if owner.PlanID != PlanEnterprise {
		t.Fatalf("private-mode default plan = %q, want enterprise contract_custom", owner.PlanID)
	}
	organization, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Private Bootstrap"})
	if err != nil {
		t.Fatalf("bootstrap configured private organization: %v", err)
	}
	if organization.ID != manager.Binding().OrganizationID {
		t.Fatalf("bootstrap organization ID = %q, want configured binding", organization.ID)
	}
	payload := licensing.Payload{
		LicenseID: "lic-private-bootstrap", KeyID: "license-2026", Customer: "Bootstrap Customer",
		OrganizationID: organization.ID, DeploymentID: manager.Binding().DeploymentID,
		IssuedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
		ExpiryPolicy: licensing.ExpiryPolicyContinueExisting,
		Entitlements: licensing.Entitlements{DeviceCount: 2, MemberCount: 2, ConcurrentOnlineDevices: 2, RelayBytesPerMonth: 1024, ActiveRelaySessions: 1,
			AuditRetentionDays: 30, DeploymentCount: 1, PrivateDeployment: true, Relay: true, Rollout: true, OfflineUpdates: true},
		Support: licensing.Support{ID: "support-bootstrap", Contact: "support@example.invalid"},
	}
	raw, err := licensing.Sign(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportPrivateLicense(ctx, ImportPrivateLicenseRequest{ActorAccountID: owner.ID, OrganizationID: organization.ID, SignedLicense: raw}); err != nil {
		t.Fatalf("import after bootstrap: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: owner.ID, Name: "Private Network"})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := svc.CreateInvite(ctx, CreateInviteRequest{AccountID: owner.ID, NetworkID: network.ID, OneTime: true, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.JoinDevice(ctx, JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "private-client", Fingerprint: "fp-private-client"}); err != nil {
		t.Fatalf("client join after signed license import: %v", err)
	}
}

func TestPrivateLicenseImportRBACSummaryRedactionAndContractQuota(t *testing.T) {
	f := newPrivateLicenseFixture(t, licensing.ExpiryPolicyContinueExisting, "", func(e *licensing.Entitlements) {
		e.DeviceCount = 1
	})

	summary, err := f.svc.GetPrivateLicenseSummary(f.ctx, f.owner.ID, f.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "signature") || strings.Contains(text, "payload") || strings.Contains(text, "PRIVATE KEY") || strings.Contains(text, "Example Customer") {
		t.Fatalf("summary leaked signed material: %s", text)
	}

	operator := f.createMember(MembershipRoleOperator)
	if _, err := f.svc.GetPrivateLicenseSummary(f.ctx, operator.ID, f.org.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator summary error = %v, want forbidden", err)
	}
	admin := f.createMember(MembershipRoleAdmin)
	if _, err := f.svc.GetPrivateLicenseSummary(f.ctx, admin.ID, f.org.ID); err != nil {
		t.Fatalf("admin summary: %v", err)
	}

	firstInvite, err := f.svc.CreateInvite(f.ctx, CreateInviteRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, OneTime: true, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.JoinDevice(f.ctx, JoinDeviceRequest{Token: firstInvite.Token, Code: firstInvite.Code, DeviceName: "first", Fingerprint: "fp-first"}); err != nil {
		t.Fatalf("first JoinDevice: %v", err)
	}
	secondInvite, err := f.svc.CreateInvite(f.ctx, CreateInviteRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, OneTime: true, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.JoinDevice(f.ctx, JoinDeviceRequest{Token: secondInvite.Token, Code: secondInvite.Code, DeviceName: "second", Fingerprint: "fp-second"}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("second JoinDevice error = %v, want contract quota refusal", err)
	}

	wrongPayload := f.payload(licensing.ExpiryPolicyContinueExisting, "")
	wrongPayload.OrganizationID = "org-other"
	wrong, err := licensing.Sign(wrongPayload, f.priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ImportPrivateLicense(f.ctx, ImportPrivateLicenseRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, SignedLicense: wrong}); !errors.Is(err, licensing.ErrBindingMismatch) {
		t.Fatalf("wrong binding import error = %v", err)
	}
	retained, err := f.svc.GetPrivateLicenseSummary(f.ctx, f.owner.ID, f.org.ID)
	if err != nil || retained.LicenseID != summary.LicenseID {
		t.Fatalf("current license after invalid import = %+v, %v", retained, err)
	}
	f.assertAuditSafe(AuditPrivateLicenseImported, AuditPrivateLicenseValidationFailed)
}

func TestPrivateLicenseCrossOrganizationImportDoesNotReplaceCurrent(t *testing.T) {
	f := newPrivateLicenseFixture(t, licensing.ExpiryPolicyContinueExisting, "", nil)
	otherOwner := f.createAccount("other-private-owner@example.invalid")
	now := *f.now
	otherOrganization, err := f.store.CreateOrganization(f.ctx, Organization{
		ID: "org_private_other", Name: "Other Organization", Status: OrganizationStatusActive,
		OwnerAccountID: otherOwner.ID, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateMembership(f.ctx, Membership{
		ID: "mem_private_other_owner", OrganizationID: otherOrganization.ID, AccountID: otherOwner.ID,
		Role: MembershipRoleOwner, Status: MembershipStatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload := f.payload(licensing.ExpiryPolicyContinueExisting, "")
	payload.LicenseID = "lic-must-not-replace"
	raw, err := licensing.Sign(payload, f.priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ImportPrivateLicense(f.ctx, ImportPrivateLicenseRequest{
		ActorAccountID: otherOwner.ID, OrganizationID: otherOrganization.ID, SignedLicense: raw,
	}); !errors.Is(err, licensing.ErrBindingMismatch) {
		t.Fatalf("cross-organization import error = %v", err)
	}
	current, err := f.manager.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if current.LicenseID != "lic-private-001" {
		t.Fatalf("cross-organization import replaced current license: %+v", current)
	}
}

func TestPrivateLicenseContractEntitlementsNeverResolveAsUnlimited(t *testing.T) {
	f := newPrivateLicenseFixture(t, licensing.ExpiryPolicyContinueExisting, "", func(e *licensing.Entitlements) {
		e.DeviceCount = 11
		e.MemberCount = 7
		e.ConcurrentOnlineDevices = 5
		e.RelayBytesPerMonth = 123456
		e.ActiveRelaySessions = 3
		e.AuditRetentionDays = 45
		e.DeploymentCount = 2
	})
	for _, tc := range []struct {
		dimension QuotaDimension
		want      int64
	}{
		{QuotaDimensionDeviceCount, 11},
		{QuotaDimensionMemberCount, 7},
		{QuotaDimensionConcurrentOnlineDevices, 5},
		{QuotaDimensionOfficialRelayTraffic, 123456},
		{QuotaDimensionActiveRelaySessions, 3},
		{QuotaDimensionAuditLogRetention, 45},
		{QuotaDimensionDeploymentCount, 2},
	} {
		decision, err := f.svc.evaluateQuotaLocked(f.ctx, f.owner.ID, tc.dimension, PlanQuota{Mode: PlanQuotaContractCustom})
		if err != nil {
			t.Fatalf("%s entitlement: %v", tc.dimension, err)
		}
		if decision.Mode != PlanQuotaContractCustom || !decision.Limited || decision.Limit != tc.want {
			t.Fatalf("%s decision = %+v, want custom limit %d", tc.dimension, decision, tc.want)
		}
	}

	publicKey := f.priv.Public().(ed25519.PublicKey)
	empty, err := licensing.NewManager(licensing.ManagerConfig{
		Path: filepath.Join(t.TempDir(), "missing-license.json"), TrustedKeys: licensing.TrustedKeys{"license-2026": publicKey},
		Binding: licensing.Binding{OrganizationID: f.org.ID, DeploymentID: "deployment-private"}, Now: func() time.Time { return *f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetPrivateLicenseManager(empty)
	if decision, err := f.svc.evaluateQuotaLocked(f.ctx, f.owner.ID, QuotaDimensionDeviceCount, PlanQuota{Mode: PlanQuotaContractCustom}); err == nil || decision.Mode == PlanQuotaUnlimited {
		t.Fatalf("missing license decision = %+v, %v; want safe refusal", decision, err)
	}
}

func TestPrivateLicenseOptionalCapabilitiesAreDeniedWhenNotEntitled(t *testing.T) {
	f := newPrivateLicenseFixture(t, licensing.ExpiryPolicyContinueExisting, "", func(e *licensing.Entitlements) {
		e.Relay = false
		e.Rollout = false
		e.OfflineUpdates = false
	})
	current, err := f.manager.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []licensing.Operation{licensing.OperationExistingRelay, licensing.OperationNewRollout, licensing.OperationOfflineUpdate} {
		if err := current.Authorize(operation, *f.now); !errors.Is(err, licensing.ErrPolicyDenied) {
			t.Fatalf("operation %s error = %v, want policy denied", operation, err)
		}
	}
}

func TestPrivateLicenseContinueExistingEnforcedOnRealServicePaths(t *testing.T) {
	f := newPrivateLicenseFixture(t, licensing.ExpiryPolicyContinueExisting, "", nil)
	source := f.joinAndEnrollDevice("source", "fp-source")
	target := f.joinAndEnrollDevice("target", "fp-target")
	if _, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, MemberAccountID: f.owner.ID, DeviceID: target.ID}); err != nil {
		t.Fatal(err)
	}
	for _, device := range []Device{source, target} {
		if _, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, DeviceID: device.ID, Status: DeviceStatusOnline}); err != nil {
			t.Fatalf("pre-expiry heartbeat %s: %v", device.ID, err)
		}
	}

	deviceInvite, err := f.svc.CreateInvite(f.ctx, CreateInviteRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, OneTime: true, MaxUses: 1, TTL: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	member := f.createAccount("pending-member@example.invalid")
	memberInvite, err := f.svc.CreateOrganizationInvite(f.ctx, CreateOrganizationInviteRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, InvitedAccountID: member.ID, Role: MembershipRoleMember, TTL: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := f.svc.CreateDeploymentBundle(f.ctx, CreateDeploymentBundleRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, NetworkID: f.network.ID, Platform: DeploymentPlatformWindows, Architecture: DeploymentArchitectureAMD64, MaxUses: 1, TTL: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	*f.now = f.expires
	if _, err := f.svc.JoinDevice(f.ctx, JoinDeviceRequest{Token: deviceInvite.Token, Code: deviceInvite.Code, DeviceName: "new-after-expiry", Fingerprint: "fp-new"}); !errors.Is(err, licensing.ErrPolicyDenied) {
		t.Fatalf("expired JoinDevice = %v", err)
	}
	if _, err := f.svc.CreateOrganizationInvite(f.ctx, CreateOrganizationInviteRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, InvitedAccountID: member.ID}); !errors.Is(err, licensing.ErrPolicyDenied) {
		t.Fatalf("expired member invite = %v", err)
	}
	if _, err := f.svc.AcceptOrganizationInvite(f.ctx, AcceptOrganizationInviteRequest{OrganizationID: f.org.ID, AccountID: member.ID, Token: memberInvite.Token, Code: memberInvite.Code}); !errors.Is(err, licensing.ErrPolicyDenied) {
		t.Fatalf("expired member accept = %v", err)
	}
	if _, err := f.svc.CreateDeploymentBundle(f.ctx, CreateDeploymentBundleRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, NetworkID: f.network.ID, Platform: DeploymentPlatformWindows, Architecture: DeploymentArchitectureAMD64, MaxUses: 1}); !errors.Is(err, licensing.ErrPolicyDenied) {
		t.Fatalf("expired deployment = %v", err)
	}
	if _, err := f.svc.RedeemBootstrapCredential(f.ctx, RedeemBootstrapCredentialRequest{Credential: bundle.Credential, OrganizationID: f.org.ID, Platform: DeploymentPlatformWindows, Architecture: DeploymentArchitectureAMD64, DeviceName: "deployed", Fingerprint: "fp-deployed", CurrentVersion: "0.1.0"}); !errors.Is(err, licensing.ErrPolicyDenied) {
		t.Fatalf("expired deployment redemption = %v", err)
	}
	if _, err := f.svc.CreateRollout(f.ctx, CreateRolloutRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceIDs: []string{target.ID}, TargetVersion: "0.1.0-dev"}); !errors.Is(err, licensing.ErrPolicyDenied) {
		t.Fatalf("expired rollout = %v", err)
	}
	if _, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, DeviceID: source.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("continue_existing heartbeat = %v", err)
	}
	if _, err := f.svc.NegotiateP2PConnection(f.ctx, NegotiateP2PConnectionRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, SourceDeviceID: source.ID, TargetDeviceID: target.ID}); err != nil {
		t.Fatalf("continue_existing P2P = %v", err)
	}
	if _, err := f.svc.CreateRelaySession(f.ctx, CreateRelaySessionRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, SourceDeviceID: source.ID, TargetDeviceID: target.ID}); err != nil {
		t.Fatalf("continue_existing Relay = %v", err)
	}
	f.assertAuditSafe(AuditPrivateLicenseExpired, AuditPrivateLicensePolicyDenied)
}

func TestPrivateLicenseDenyAllAndGracePeriodBlockExistingNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		policy         licensing.ExpiryPolicy
		graceEndPolicy licensing.ExpiryPolicy
		advance        time.Duration
	}{
		{name: "deny all at expiry", policy: licensing.ExpiryPolicyDenyAll},
		{name: "grace end deny all", policy: licensing.ExpiryPolicyGracePeriod, graceEndPolicy: licensing.ExpiryPolicyDenyAll, advance: 10 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPrivateLicenseFixture(t, tc.policy, tc.graceEndPolicy, nil)
			source := f.joinAndEnrollDevice("source", "fp-source")
			target := f.joinAndEnrollDevice("target", "fp-target")
			if _, err := f.svc.GrantConnectionAccess(f.ctx, ConnectionGrantRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, MemberAccountID: f.owner.ID, DeviceID: target.ID}); err != nil {
				t.Fatal(err)
			}
			for _, device := range []Device{source, target} {
				if _, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, DeviceID: device.ID, Status: DeviceStatusOnline}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.policy == licensing.ExpiryPolicyGracePeriod {
				*f.now = f.expires.Add(9 * time.Minute)
				if _, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, DeviceID: source.ID, Status: DeviceStatusOnline}); err != nil {
					t.Fatalf("heartbeat inside grace = %v", err)
				}
			}
			*f.now = f.expires.Add(tc.advance)
			if _, err := f.svc.HeartbeatDevice(f.ctx, HeartbeatDeviceRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, DeviceID: source.ID, Status: DeviceStatusOnline}); !errors.Is(err, licensing.ErrPolicyDenied) {
				t.Fatalf("heartbeat after policy boundary = %v", err)
			}
			if _, err := f.svc.NegotiateP2PConnection(f.ctx, NegotiateP2PConnectionRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, SourceDeviceID: source.ID, TargetDeviceID: target.ID}); !errors.Is(err, licensing.ErrPolicyDenied) {
				t.Fatalf("P2P after policy boundary = %v", err)
			}
			if _, err := f.svc.CreateRelaySession(f.ctx, CreateRelaySessionRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, SourceDeviceID: source.ID, TargetDeviceID: target.ID}); !errors.Is(err, licensing.ErrPolicyDenied) {
				t.Fatalf("Relay after policy boundary = %v", err)
			}
		})
	}
}

func newPrivateLicenseFixture(t *testing.T, policy, graceEnd licensing.ExpiryPolicy, mutate func(*licensing.Entitlements)) *privateLicenseFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, WithNow(func() time.Time { return now }), WithContractPlanQuotaLimits(map[string]int64{}))
	owner, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "owner@example.invalid", PlanID: PlanEnterprise})
	if err != nil {
		t.Fatal(err)
	}
	// Bootstrap the organization before enabling private-mode licensing. The signed
	// license is then bound to the stable ID returned here.
	svc.quotaEvaluator = NewPlanQuotaEvaluator(PlanQuotaEvaluatorConfig{ContractLimits: map[string]int64{contractQuotaKey(owner.ID, QuotaDimensionMemberCount): 50}})
	org, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Private Organization"})
	if err != nil {
		t.Fatal(err)
	}
	network, err := svc.CreateNetwork(ctx, CreateNetworkRequest{AccountID: owner.ID, Name: "Private Network"})
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := licensing.NewManager(licensing.ManagerConfig{
		Path: filepath.Join(t.TempDir(), "private-license.json"), TrustedKeys: licensing.TrustedKeys{"license-2026": publicKey},
		Binding: licensing.Binding{OrganizationID: org.ID, DeploymentID: "deployment-private"}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetPrivateLicenseManager(manager)
	f := &privateLicenseFixture{t: t, ctx: ctx, now: &now, svc: svc, store: store, manager: manager, priv: privateKey, owner: owner, org: org, network: network, expires: now.Add(time.Hour)}
	payload := f.payload(policy, graceEnd)
	if mutate != nil {
		mutate(&payload.Entitlements)
	}
	raw, err := licensing.Sign(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportPrivateLicense(ctx, ImportPrivateLicenseRequest{ActorAccountID: owner.ID, OrganizationID: org.ID, SignedLicense: raw}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *privateLicenseFixture) payload(policy, graceEnd licensing.ExpiryPolicy) licensing.Payload {
	payload := licensing.Payload{
		LicenseID: "lic-private-001", KeyID: "license-2026", Customer: "Example Customer", OrganizationID: f.org.ID, DeploymentID: "deployment-private",
		IssuedAt: f.expires.Add(-2 * time.Hour).Format(time.RFC3339Nano), NotBefore: f.expires.Add(-time.Hour).Format(time.RFC3339Nano), ExpiresAt: f.expires.Format(time.RFC3339Nano),
		ExpiryPolicy: policy,
		Entitlements: licensing.Entitlements{DeviceCount: 20, MemberCount: 10, ConcurrentOnlineDevices: 10, RelayBytesPerMonth: 1 << 30, ActiveRelaySessions: 4,
			AuditRetentionDays: 90, DeploymentCount: 3, PrivateDeployment: true, Relay: true, Rollout: true, OfflineUpdates: true},
		Support: licensing.Support{ID: "support-standard", Contact: "support@example.invalid"},
	}
	if policy == licensing.ExpiryPolicyGracePeriod {
		payload.GracePeriodSeconds = 600
		payload.GraceEndPolicy = graceEnd
	}
	return payload
}

func (f *privateLicenseFixture) createAccount(email string) Account {
	f.t.Helper()
	account, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: email, PlanID: PlanEnterprise})
	if err != nil {
		f.t.Fatal(err)
	}
	return account
}

func (f *privateLicenseFixture) createMember(role MembershipRole) Account {
	f.t.Helper()
	account := f.createAccount(string(role) + "@example.invalid")
	now := *f.now
	if _, err := f.store.CreateMembership(f.ctx, Membership{ID: mustID("mem"), OrganizationID: f.org.ID, AccountID: account.ID, Role: role, Status: MembershipStatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		f.t.Fatal(err)
	}
	return account
}

func (f *privateLicenseFixture) joinAndEnrollDevice(name, fingerprint string) Device {
	f.t.Helper()
	invite, err := f.svc.CreateInvite(f.ctx, CreateInviteRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, OneTime: true, MaxUses: 1})
	if err != nil {
		f.t.Fatal(err)
	}
	device, err := f.svc.JoinDevice(f.ctx, JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: name, Fingerprint: fingerprint})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.EnrollOrganizationDevice(f.ctx, EnrollOrganizationDeviceRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, DeviceID: device.ID}); err != nil {
		f.t.Fatal(err)
	}
	return device
}

func (f *privateLicenseFixture) assertAuditSafe(events ...string) {
	f.t.Helper()
	all, err := f.svc.ListAuditEvents(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	wanted := map[string]bool{}
	for _, event := range events {
		wanted[event] = false
	}
	for _, event := range all {
		if _, ok := wanted[event.Event]; ok {
			wanted[event.Event] = true
			encoded, _ := json.Marshal(event)
			text := string(encoded)
			if strings.Contains(text, "signature") || strings.Contains(text, "signed_license") || strings.Contains(text, "Example Customer") {
				f.t.Fatalf("audit leaked license material: %s", text)
			}
		}
	}
	for event, found := range wanted {
		if !found {
			f.t.Fatalf("audit event %s was not recorded", event)
		}
	}
}
