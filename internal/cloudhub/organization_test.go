package cloudhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOrganizationLifecycleInvitesMembersAndAuditRedaction(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return now }),
		WithPlanQuotaLimits(map[string]int64{"cloudhub.plans.team.members": 3}),
	)
	owner, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "org-owner@example.com", PlanID: PlanTeam})
	if err != nil {
		t.Fatalf("CreateAccount owner returned error: %v", err)
	}
	admin, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "org-admin@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount admin returned error: %v", err)
	}

	org, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{
		OwnerAccountID: owner.ID,
		Name:           "Ops Team",
	})
	if err != nil {
		t.Fatalf("CreateOrganization returned error: %v", err)
	}
	if org.ID == "" || org.OwnerAccountID != owner.ID || org.Status != OrganizationStatusActive {
		t.Fatalf("organization = %+v, want active org owned by %s", org, owner.ID)
	}
	gotOrg, err := svc.GetOrganization(ctx, org.ID, owner.ID)
	if err != nil {
		t.Fatalf("GetOrganization returned error: %v", err)
	}
	if gotOrg.ID != org.ID || gotOrg.Name != "Ops Team" {
		t.Fatalf("GetOrganization = %+v, want created org", gotOrg)
	}

	members, err := svc.ListOrganizationMembers(ctx, org.ID, owner.ID)
	if err != nil {
		t.Fatalf("ListOrganizationMembers returned error: %v", err)
	}
	if len(members) != 1 || members[0].AccountID != owner.ID ||
		members[0].Role != MembershipRoleOwner || members[0].Status != MembershipStatusActive {
		t.Fatalf("initial members = %+v, want owner membership", members)
	}

	invite, err := svc.CreateOrganizationInvite(ctx, CreateOrganizationInviteRequest{
		ActorAccountID:   owner.ID,
		OrganizationID:   org.ID,
		InvitedAccountID: admin.ID,
		Role:             MembershipRoleAdmin,
		TTL:              2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateOrganizationInvite returned error: %v", err)
	}
	if invite.Token == "" || invite.Code == "" || invite.OrganizationID != org.ID || invite.InvitedAccountID != admin.ID {
		t.Fatalf("invite = %+v, want plaintext credentials returned once for target account", invite)
	}
	storedInvite, err := store.GetOrganizationInvite(ctx, invite.ID)
	if err != nil {
		t.Fatalf("GetOrganizationInvite returned error: %v", err)
	}
	if storedInvite.TokenDigest == "" || storedInvite.CodeDigest == "" {
		t.Fatalf("stored invite = %+v, want irreversible digests", storedInvite)
	}
	if strings.Contains(storedInvite.TokenDigest, invite.Token) ||
		strings.Contains(storedInvite.CodeDigest, invite.Code) ||
		strings.Contains(storedInvite.CodeDigest, invite.Token) {
		t.Fatalf("stored invite leaked plaintext token/code: %+v", storedInvite)
	}
	storedJSON, err := json.Marshal(storedInvite)
	if err != nil {
		t.Fatalf("marshal stored invite: %v", err)
	}
	if bytes.Contains(storedJSON, []byte("digest")) || bytes.Contains(storedJSON, []byte(invite.Token)) || bytes.Contains(storedJSON, []byte(invite.Code)) {
		t.Fatalf("stored invite JSON leaks internal invite data: %s", storedJSON)
	}

	member, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      admin.ID,
		Token:          invite.Token,
		Code:           invite.Code,
	})
	if err != nil {
		t.Fatalf("AcceptOrganizationInvite returned error: %v", err)
	}
	if member.ID == "" || member.OrganizationID != org.ID || member.AccountID != admin.ID ||
		member.Role != MembershipRoleAdmin || member.Status != MembershipStatusActive {
		t.Fatalf("accepted member = %+v, want active admin membership", member)
	}
	_, err = svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      admin.ID,
		Token:          invite.Token,
		Code:           invite.Code,
	})
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed accept error = %v, want stable conflict", err)
	}

	suspended, err := svc.SuspendOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      admin.ID,
		Reason:         "offboarding hold",
	})
	if err != nil {
		t.Fatalf("SuspendOrganizationMember returned error: %v", err)
	}
	if suspended.Status != MembershipStatusSuspended || suspended.SuspendedAt == nil {
		t.Fatalf("suspended member = %+v, want suspended", suspended)
	}
	resumed, err := svc.ResumeOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      admin.ID,
	})
	if err != nil {
		t.Fatalf("ResumeOrganizationMember returned error: %v", err)
	}
	if resumed.Status != MembershipStatusActive || resumed.SuspendedAt != nil {
		t.Fatalf("resumed member = %+v, want active", resumed)
	}
	removed, err := svc.RemoveOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      admin.ID,
		Reason:         "left team",
	})
	if err != nil {
		t.Fatalf("RemoveOrganizationMember returned error: %v", err)
	}
	if removed.Status != MembershipStatusRemoved || removed.RemovedAt == nil {
		t.Fatalf("removed member = %+v, want removed", removed)
	}
	if _, err := svc.SuspendOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      owner.ID,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspend owner error = %v, want forbidden", err)
	}
	if _, err := svc.RemoveOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      owner.ID,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("remove owner error = %v, want forbidden", err)
	}

	events, err := svc.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("ListAuditEvents returned error: %v", err)
	}
	for _, want := range []string{
		AuditOrganizationCreated,
		AuditOrganizationInviteCreated,
		AuditOrganizationInviteAccepted,
		AuditOrganizationMemberSuspended,
		AuditOrganizationMemberResumed,
		AuditOrganizationMemberRemoved,
	} {
		if !hasAuditEvent(events, want) {
			t.Fatalf("audit events = %+v, missing %q", events, want)
		}
		event := mustAuditEvent(t, events, want)
		if event.AccountID == "" || event.Metadata["organization_id"] != org.ID || event.Metadata["result"] == "" {
			t.Fatalf("audit event %s = %+v, want actor, organization and result", want, event)
		}
	}
	accepted := mustAuditEvent(t, events, AuditOrganizationInviteAccepted)
	if accepted.Metadata["target_account_id"] != admin.ID {
		t.Fatalf("accepted audit = %+v, want target account %s", accepted, admin.ID)
	}
	assertNoSensitiveJSON(t, events)
}

func TestOrganizationInvitesRejectExpiryRevocationMismatchAndAccountOrOrganizationState(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 16, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(),
		WithNow(func() time.Time { return now }),
		WithPlanQuotaLimits(map[string]int64{"cloudhub.plans.team.members": 10}),
	)
	owner, org := mustOrganizationOwnerAndOrg(t, ctx, svc, PlanTeam)
	target, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "target@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount target returned error: %v", err)
	}
	other, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "other@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount other returned error: %v", err)
	}
	secondOrg, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Other Team"})
	if err != nil {
		t.Fatalf("CreateOrganization second returned error: %v", err)
	}

	invite := mustOrganizationInvite(t, ctx, svc, owner.ID, org.ID, target.ID, MembershipRoleMember)
	if _, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      other.ID,
		Token:          invite.Token,
		Code:           invite.Code,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong account accept error = %v, want forbidden", err)
	}
	if _, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: secondOrg.ID,
		AccountID:      target.ID,
		Token:          invite.Token,
		Code:           invite.Code,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong organization accept error = %v, want forbidden", err)
	}

	expiring := mustOrganizationInviteWithTTL(t, ctx, svc, owner.ID, org.ID, target.ID, MembershipRoleMember, time.Minute)
	svc.SetNowForTest(func() time.Time { return now.Add(2 * time.Minute) })
	if _, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      target.ID,
		Token:          expiring.Token,
		Code:           expiring.Code,
	}); err == nil || !errors.Is(err, ErrExpired) {
		t.Fatalf("expired accept error = %v, want expired", err)
	}
	svc.SetNowForTest(func() time.Time { return now })

	revoked := mustOrganizationInvite(t, ctx, svc, owner.ID, org.ID, target.ID, MembershipRoleMember)
	if _, err := svc.RevokeOrganizationInvite(ctx, RevokeOrganizationInviteRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		InviteID:       revoked.ID,
		Reason:         "wrong recipient",
	}); err != nil {
		t.Fatalf("RevokeOrganizationInvite returned error: %v", err)
	}
	if _, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      target.ID,
		Token:          revoked.Token,
		Code:           revoked.Code,
	}); err == nil || !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked accept error = %v, want revoked", err)
	}

	frozenTarget, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "frozen-target@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount frozen target returned error: %v", err)
	}
	if _, err := svc.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: frozenTarget.ID, Reason: "risk"}); err != nil {
		t.Fatalf("FreezeAccount returned error: %v", err)
	}
	if _, err := svc.CreateOrganizationInvite(ctx, CreateOrganizationInviteRequest{
		ActorAccountID:   owner.ID,
		OrganizationID:   org.ID,
		InvitedAccountID: frozenTarget.ID,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("invite frozen target error = %v, want forbidden", err)
	}

	bannedTarget, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "banned-target@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount banned target returned error: %v", err)
	}
	banInvite := mustOrganizationInvite(t, ctx, svc, owner.ID, org.ID, bannedTarget.ID, MembershipRoleMember)
	if _, err := svc.BanAccount(ctx, AccountStatusChangeRequest{AccountID: bannedTarget.ID, Reason: "abuse"}); err != nil {
		t.Fatalf("BanAccount returned error: %v", err)
	}
	if _, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      bannedTarget.ID,
		Token:          banInvite.Token,
		Code:           banInvite.Code,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("accept banned target error = %v, want forbidden", err)
	}

	suspendedInvite := mustOrganizationInvite(t, ctx, svc, owner.ID, org.ID, target.ID, MembershipRoleMember)
	if _, err := svc.SuspendOrganization(ctx, OrganizationStatusChangeRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		Reason:         "billing hold",
	}); err != nil {
		t.Fatalf("SuspendOrganization returned error: %v", err)
	}
	if _, err := svc.CreateOrganizationInvite(ctx, CreateOrganizationInviteRequest{
		ActorAccountID:   owner.ID,
		OrganizationID:   org.ID,
		InvitedAccountID: target.ID,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("invite suspended organization error = %v, want forbidden", err)
	}
	if _, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      target.ID,
		Token:          suspendedInvite.Token,
		Code:           suspendedInvite.Code,
	}); err == nil || !errors.Is(err, ErrForbidden) {
		t.Fatalf("accept suspended organization error = %v, want forbidden", err)
	}
	if _, err := svc.ResumeOrganization(ctx, OrganizationStatusChangeRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
	}); err != nil {
		t.Fatalf("ResumeOrganization returned error: %v", err)
	}
}

func TestOrganizationMemberQuotaModesAndConcurrentAccepts(t *testing.T) {
	ctx := context.Background()

	t.Run("unavailable member quota refuses organization creation", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		owner, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "personal-owner@example.com", PlanID: PlanPersonal})
		if err != nil {
			t.Fatalf("CreateAccount returned error: %v", err)
		}
		_, err = svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Personal Org"})
		assertQuotaExceeded(t, err, QuotaDimensionMemberCount, 0, 0)
	})

	t.Run("finite member quota is enforced atomically", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithPlanQuotaLimits(map[string]int64{"cloudhub.plans.team.members": 2}))
		owner, org := mustOrganizationOwnerAndOrg(t, ctx, svc, PlanTeam)
		first, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "first-member@example.com", PlanID: PlanPersonal})
		if err != nil {
			t.Fatalf("CreateAccount first returned error: %v", err)
		}
		second, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "second-member@example.com", PlanID: PlanPersonal})
		if err != nil {
			t.Fatalf("CreateAccount second returned error: %v", err)
		}
		firstInvite := mustOrganizationInvite(t, ctx, svc, owner.ID, org.ID, first.ID, MembershipRoleMember)
		if _, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
			OrganizationID: org.ID,
			AccountID:      first.ID,
			Token:          firstInvite.Token,
			Code:           firstInvite.Code,
		}); err != nil {
			t.Fatalf("AcceptOrganizationInvite first returned error: %v", err)
		}
		secondInvite := mustOrganizationInvite(t, ctx, svc, owner.ID, org.ID, second.ID, MembershipRoleMember)
		_, err = svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
			OrganizationID: org.ID,
			AccountID:      second.ID,
			Token:          secondInvite.Token,
			Code:           secondInvite.Code,
		})
		assertQuotaExceeded(t, err, QuotaDimensionMemberCount, 2, 2)
	})

	t.Run("contract custom member quota is safe refused when unresolved", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		owner, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "enterprise-owner@example.com", PlanID: PlanEnterprise})
		if err != nil {
			t.Fatalf("CreateAccount returned error: %v", err)
		}
		_, err = svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Enterprise Org"})
		if err == nil || !errors.Is(err, ErrQuotaExceeded) || !strings.Contains(err.Error(), "contract custom quota is not configured") {
			t.Fatalf("CreateOrganization custom quota error = %v, want unresolved custom refusal", err)
		}
	})

	t.Run("member quota evaluator supports all explicit modes", func(t *testing.T) {
		evaluator := NewPlanQuotaEvaluator(PlanQuotaEvaluatorConfig{
			OperatorLimits: map[string]int64{"configured.members": 4},
			ContractLimits: map[string]int64{"acct_contract:member_count": 9},
		})
		cases := []struct {
			name      string
			accountID string
			quota     PlanQuota
			want      QuotaDecision
			wantErr   string
		}{
			{name: "unavailable", quota: UnavailablePlanQuota(PlanQuotaUnitMembers), want: QuotaDecision{Mode: PlanQuotaUnavailable, Limited: true, Limit: 0}},
			{name: "finite", quota: ConfiguredLimitedPlanQuota(PlanQuotaUnitMembers, "configured.members"), want: QuotaDecision{Mode: PlanQuotaLimited, Limited: true, Limit: 4}},
			{name: "unlimited", quota: UnlimitedPlanQuota(PlanQuotaUnitMembers), want: QuotaDecision{Mode: PlanQuotaUnlimited}},
			{name: "custom", accountID: "acct_contract", quota: ContractCustomPlanQuota(PlanQuotaUnitMembers), want: QuotaDecision{Mode: PlanQuotaContractCustom, Limited: true, Limit: 9}},
			{name: "custom unresolved", accountID: "acct_missing", quota: ContractCustomPlanQuota(PlanQuotaUnitMembers), wantErr: "contract custom quota is not configured"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := evaluator.Evaluate(tc.accountID, QuotaDimensionMemberCount, tc.quota)
				if tc.wantErr != "" {
					if err == nil || !errors.Is(err, ErrQuotaExceeded) || !strings.Contains(err.Error(), tc.wantErr) {
						t.Fatalf("Evaluate error = %v, want quota error containing %q", err, tc.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("Evaluate returned error: %v", err)
				}
				if got != tc.want {
					t.Fatalf("decision = %+v, want %+v", got, tc.want)
				}
			})
		}
	})

	t.Run("concurrent accepts cannot exceed finite quota", func(t *testing.T) {
		svc := NewService(NewMemoryStore(), WithPlanQuotaLimits(map[string]int64{"cloudhub.plans.team.members": 2}))
		owner, org := mustOrganizationOwnerAndOrg(t, ctx, svc, PlanTeam)
		const workers = 24
		type pendingInvite struct {
			account Account
			invite  OrganizationInviteResult
		}
		pending := make([]pendingInvite, 0, workers)
		for i := 0; i < workers; i++ {
			account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "concurrent-member-" + string(rune('a'+i)) + "@example.com", PlanID: PlanPersonal})
			if err != nil {
				t.Fatalf("CreateAccount %d returned error: %v", i, err)
			}
			invite := mustOrganizationInvite(t, ctx, svc, owner.ID, org.ID, account.ID, MembershipRoleMember)
			pending = append(pending, pendingInvite{account: account, invite: invite})
		}
		var wg sync.WaitGroup
		results := make(chan error, workers)
		for _, item := range pending {
			item := item
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := svc.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
					OrganizationID: org.ID,
					AccountID:      item.account.ID,
					Token:          item.invite.Token,
					Code:           item.invite.Code,
				})
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		var accepted, quotaDenied int
		for err := range results {
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrQuotaExceeded):
				quotaDenied++
			default:
				t.Fatalf("unexpected concurrent accept error: %v", err)
			}
		}
		if accepted != 1 || quotaDenied != workers-1 {
			t.Fatalf("concurrent accepts accepted=%d quotaDenied=%d, want 1/%d", accepted, quotaDenied, workers-1)
		}
		members, err := svc.ListOrganizationMembers(ctx, org.ID, owner.ID)
		if err != nil {
			t.Fatalf("ListOrganizationMembers returned error: %v", err)
		}
		if len(activeOrganizationMembers(members)) != 2 {
			t.Fatalf("members = %+v, want exactly owner plus one accepted member", members)
		}
	})
}

func TestOrganizationHTTPClientAndLegacyAccountCompatibility(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 17, 0, 0, 0, time.UTC)
	svc := NewService(NewMemoryStore(),
		WithNow(func() time.Time { return now }),
		WithPlanQuotaLimits(map[string]int64{"cloudhub.plans.team.members": 2}),
		WithAccountPolicies("legacy-org-test", AccountPolicy{
			Name:                   "legacy-org-test",
			RelayBytesQuota:        64,
			MaxActiveRelaySessions: 4,
			MaxRelaySessionsPerDay: 8,
		}),
	)
	server := httptest.NewServer(NewServer(svc))
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), Timeout: 5 * time.Second}

	owner, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "org-client-owner@example.com", PlanID: PlanTeam})
	if err != nil {
		t.Fatalf("CreateAccount owner returned error: %v", err)
	}
	target, err := client.CreateAccount(ctx, CreateAccountRequest{Email: "org-client-member@example.com", PlanID: PlanPersonal})
	if err != nil {
		t.Fatalf("CreateAccount target returned error: %v", err)
	}
	client.ActorAccountID = owner.ID
	org, err := client.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Client Org"})
	if err != nil {
		t.Fatalf("CreateOrganization returned error: %v", err)
	}
	if org.ID == "" || org.OwnerAccountID != owner.ID {
		t.Fatalf("organization = %+v, want owner binding", org)
	}
	gotOrg, err := client.GetOrganization(ctx, org.ID)
	if err != nil {
		t.Fatalf("GetOrganization returned error: %v", err)
	}
	if gotOrg.ID != org.ID || gotOrg.Status != OrganizationStatusActive {
		t.Fatalf("GetOrganization = %+v, want active created org", gotOrg)
	}
	invite, err := client.CreateOrganizationInvite(ctx, CreateOrganizationInviteRequest{
		ActorAccountID:   owner.ID,
		OrganizationID:   org.ID,
		InvitedAccountID: target.ID,
		Role:             MembershipRoleMember,
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateOrganizationInvite returned error: %v", err)
	}
	if invite.Token == "" || invite.Code == "" {
		t.Fatalf("invite = %+v, want one-time plaintext credentials", invite)
	}
	rawInvite := postRawJSON(t, server.Client(), server.URL+"/api/organizations/"+org.ID+"/invites", owner.ID, map[string]any{
		"actor_account_id":   owner.ID,
		"invited_account_id": target.ID,
		"role":               string(MembershipRoleMember),
	})
	if strings.Contains(strings.ToLower(rawInvite), "digest") || strings.Contains(rawInvite, invite.Token) {
		t.Fatalf("organization invite response leaks digest or prior token: %s", rawInvite)
	}

	client.ActorAccountID = target.ID
	member, err := client.AcceptOrganizationInvite(ctx, AcceptOrganizationInviteRequest{
		OrganizationID: org.ID,
		AccountID:      target.ID,
		Token:          invite.Token,
		Code:           invite.Code,
	})
	if err != nil {
		t.Fatalf("AcceptOrganizationInvite returned error: %v", err)
	}
	if member.AccountID != target.ID || member.Role != MembershipRoleMember {
		t.Fatalf("accepted member = %+v, want target member", member)
	}
	members, err := client.ListOrganizationMembers(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrganizationMembers returned error: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %+v, want owner and target", members)
	}
	client.ActorAccountID = owner.ID
	if _, err := client.SuspendOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      target.ID,
	}); err != nil {
		t.Fatalf("SuspendOrganizationMember returned error: %v", err)
	}
	if _, err := client.ResumeOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      target.ID,
	}); err != nil {
		t.Fatalf("ResumeOrganizationMember returned error: %v", err)
	}
	if _, err := client.RemoveOrganizationMember(ctx, OrganizationMemberStatusRequest{
		ActorAccountID: owner.ID,
		OrganizationID: org.ID,
		AccountID:      target.ID,
	}); err != nil {
		t.Fatalf("RemoveOrganizationMember returned error: %v", err)
	}
	publicRaw := getRawWithActor(t, server.Client(), server.URL+"/api/organizations/"+org.ID, owner.ID)
	assertOrganizationPublicResponseNoInternalFields(t, publicRaw)
	publicRaw = getRawWithActor(t, server.Client(), server.URL+"/api/organizations/"+org.ID+"/members", owner.ID)
	assertOrganizationPublicResponseNoInternalFields(t, publicRaw)

	legacy, network := mustAccountAndNetwork(t, ctx, svc)
	if legacy.PlanID != "" {
		t.Fatalf("legacy plan = %q, want empty plan binding", legacy.PlanID)
	}
	if _, err := svc.CreateInvite(ctx, CreateInviteRequest{AccountID: legacy.ID, NetworkID: network.ID, MaxUses: 2, OneTime: false}); err != nil {
		t.Fatalf("legacy CreateInvite returned error: %v", err)
	}
	status, err := svc.GetAccountPolicyStatus(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("legacy GetAccountPolicyStatus returned error: %v", err)
	}
	if status.Policy.Name != "legacy-org-test" {
		t.Fatalf("legacy policy = %+v, want unchanged AccountPolicy behavior", status.Policy)
	}
}

func mustOrganizationOwnerAndOrg(t *testing.T, ctx context.Context, svc *Service, planID PlanID) (Account, Organization) {
	t.Helper()
	owner, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "owner-" + string(planID) + "@example.com", PlanID: planID})
	if err != nil {
		t.Fatalf("CreateAccount owner returned error: %v", err)
	}
	org, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Team " + string(planID)})
	if err != nil {
		t.Fatalf("CreateOrganization returned error: %v", err)
	}
	return owner, org
}

func mustOrganizationInvite(t *testing.T, ctx context.Context, svc *Service, actorID, orgID, accountID string, role MembershipRole) OrganizationInviteResult {
	t.Helper()
	return mustOrganizationInviteWithTTL(t, ctx, svc, actorID, orgID, accountID, role, time.Minute)
}

func mustOrganizationInviteWithTTL(t *testing.T, ctx context.Context, svc *Service, actorID, orgID, accountID string, role MembershipRole, ttl time.Duration) OrganizationInviteResult {
	t.Helper()
	invite, err := svc.CreateOrganizationInvite(ctx, CreateOrganizationInviteRequest{
		ActorAccountID:   actorID,
		OrganizationID:   orgID,
		InvitedAccountID: accountID,
		Role:             role,
		TTL:              ttl,
	})
	if err != nil {
		t.Fatalf("CreateOrganizationInvite returned error: %v", err)
	}
	return invite
}

func mustAuditEvent(t *testing.T, events []AuditEvent, event string) AuditEvent {
	t.Helper()
	for _, got := range events {
		if got.Event == event {
			return got
		}
	}
	t.Fatalf("missing audit event %q in %+v", event, events)
	return AuditEvent{}
}

func activeOrganizationMembers(members []Membership) []Membership {
	var out []Membership
	for _, member := range members {
		if member.Status != MembershipStatusRemoved {
			out = append(out, member)
		}
	}
	return out
}

func postRawJSON(t *testing.T, client *http.Client, url, actorAccountID string, body any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(ActorAccountHeader, actorAccountID)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status=%d body=%s, want 200", url, resp.StatusCode, buf.String())
	}
	return buf.String()
}

func getRawWithActor(t *testing.T, client *http.Client, url, actorAccountID string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("create GET request: %v", err)
	}
	req.Header.Set(ActorAccountHeader, actorAccountID)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s, want 200", url, resp.StatusCode, buf.String())
	}
	return buf.String()
}

func assertOrganizationPublicResponseNoInternalFields(t *testing.T, raw string) {
	t.Helper()
	lower := strings.ToLower(raw)
	for _, forbidden := range []string{
		"digest",
		"risk_score",
		"provider_customer",
		"provider_subscription",
		"payment",
		"card",
		"checkout",
		"token",
		"code",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("public organization response leaks forbidden field %q: %s", forbidden, raw)
		}
	}
}
