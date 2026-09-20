package cloudhub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type task10CFixture struct {
	ctx      context.Context
	now      time.Time
	clock    *time.Time
	store    *MemoryStore
	svc      *Service
	org      Organization
	owner    Account
	admin    Account
	operator Account
	member   Account
}

func newTask10CFixture(t *testing.T, retentionDays int64) task10CFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	clock := now
	store := NewMemoryStore()
	svc := NewService(store,
		WithNow(func() time.Time { return clock }),
		WithPlanQuotaLimits(map[string]int64{
			"cloudhub.plans.team.members":                  20,
			"cloudhub.plans.team.audit_log_retention_days": retentionDays,
		}),
	)
	createAccount := func(email string, plan PlanID) Account {
		account, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: email, PlanID: plan})
		if err != nil {
			t.Fatalf("CreateAccount(%s): %v", email, err)
		}
		return account
	}
	owner := createAccount("owner-10c@example.com", PlanTeam)
	admin := createAccount("admin-10c@example.com", PlanPersonal)
	operator := createAccount("operator-10c@example.com", PlanPersonal)
	member := createAccount("member-10c@example.com", PlanPersonal)
	org, err := svc.CreateOrganization(ctx, CreateOrganizationRequest{OwnerAccountID: owner.ID, Name: "Task 10C Team"})
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	for _, item := range []struct {
		account Account
		role    MembershipRole
	}{
		{admin, MembershipRoleAdmin},
		{operator, MembershipRoleOperator},
		{member, MembershipRoleMember},
	} {
		if _, err := store.CreateMembership(ctx, Membership{
			ID: mustID("mem"), OrganizationID: org.ID, AccountID: item.account.ID,
			Role: item.role, Status: MembershipStatusActive, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreateMembership(%s): %v", item.role, err)
		}
	}
	return task10CFixture{ctx: ctx, now: now, clock: &clock, store: store, svc: svc, org: org, owner: owner, admin: admin, operator: operator, member: member}
}

func (f task10CFixture) addAudit(t *testing.T, id string, at time.Time, actorID string, metadata map[string]any) {
	t.Helper()
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, ok := metadata["organization_id"]; !ok {
		metadata["organization_id"] = f.org.ID
	}
	if _, err := f.store.AddAuditEvent(f.ctx, AuditEvent{
		ID: id, Time: at, Event: "test_event", AccountID: actorID, NetworkID: "sensitive-network", DeviceID: "sensitive-device", Metadata: metadata,
	}); err != nil {
		t.Fatalf("AddAuditEvent(%s): %v", id, err)
	}
}

func (f task10CFixture) addConnection(t *testing.T, id string, at time.Time, organizationID, accountID, sourceID, targetID, path string, in, out int64) {
	t.Helper()
	if _, err := f.store.CreateConnectionLog(f.ctx, ConnectionLog{
		ID: id, OrganizationID: organizationID, AccountID: accountID, NetworkID: "net-secret",
		SourceDeviceID: sourceID, TargetDeviceID: targetID, PathType: path, PathState: "connected",
		PermissionSource: ConnectionPermissionDeviceGrant, StartedAt: at, RelayBytesIn: in, RelayBytesOut: out,
		Error: "free text must not be public",
	}); err != nil {
		t.Fatalf("CreateConnectionLog(%s): %v", id, err)
	}
}

func TestTask10CAuditQueryRBACIsolationFiltersAndDTO(t *testing.T) {
	f := newTask10CFixture(t, 30)
	otherOwner, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: "other-owner-10c@example.com", PlanID: PlanTeam})
	if err != nil {
		t.Fatal(err)
	}
	otherOrg, err := f.svc.CreateOrganization(f.ctx, CreateOrganizationRequest{OwnerAccountID: otherOwner.ID, Name: "Other Team"})
	if err != nil {
		t.Fatal(err)
	}

	at := f.now.Add(-2 * time.Hour)
	f.addAudit(t, "audit-match", at, f.admin.ID, map[string]any{
		"member_account_id": f.member.ID, "source_device_id": "dev-source", "target_device_id": "dev-target",
		"action": "must-not-leak action", "result": "must-not-leak result", "path_type": "relay",
		"permission_source": "device_grant", "relay_bytes_in": int64(120), "relay_bytes_out": int64(80),
		"token": "must-not-leak", "private_key": "must-not-leak", "candidate_address": "must-not-leak",
		"provider_customer_id": "must-not-leak", "digest": "must-not-leak", "user_content": "must-not-leak",
	})
	f.addConnection(t, "conn-match", at.Add(-time.Minute), f.org.ID, f.member.ID, "dev-source", "dev-target", "relay", 200, 100)
	f.addConnection(t, "conn-direct", at.Add(-2*time.Minute), f.org.ID, f.member.ID, "dev-source", "dev-target", "lan_direct", 0, 0)
	f.addAudit(t, "audit-other-org", at, otherOwner.ID, map[string]any{"organization_id": otherOrg.ID, "member_account_id": otherOwner.ID})
	f.addConnection(t, "conn-other-org", at, otherOrg.ID, otherOwner.ID, "other-source", "other-target", "relay", 999, 999)
	f.addAudit(t, "audit-unowned", at, f.member.ID, map[string]any{"organization_id": "", "target_device_id": "dev-target"})
	f.addConnection(t, "conn-unowned", at, "", f.member.ID, "dev-source", "dev-target", "relay", 999, 999)

	query := OrganizationAuditQuery{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, MemberAccountID: f.member.ID,
		SourceDeviceID: "dev-source", TargetDeviceID: "dev-target", ConnectionMethod: "relay",
		RelayOnly: true, MinRelayBytes: 150, PageSize: 20,
	}
	page, err := f.svc.QueryOrganizationAudit(f.ctx, query)
	if err != nil {
		t.Fatalf("QueryOrganizationAudit owner: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %+v, want matching audit and connection records only", page.Entries)
	}
	if !page.Entries[0].Time.After(page.Entries[1].Time) {
		t.Fatalf("entries are not newest-first: %+v", page.Entries)
	}
	for _, entry := range page.Entries {
		if entry.OrganizationID != f.org.ID || entry.SourceDeviceID != "dev-source" || entry.TargetDeviceID != "dev-target" || entry.ConnectionMethod != "relay" {
			t.Fatalf("entry = %+v, want organization-scoped combined filters", entry)
		}
		if entry.RelayBytesIn+entry.RelayBytesOut < 150 {
			t.Fatalf("entry = %+v, want minimum relay bytes", entry)
		}
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{
		"metadata", "network_id", "session_id", "quality_score", "latency", "packet_loss", "jitter", "switch_", "error",
		"token", "code", "private", "candidate", "raw_traffic", "provider", "digest", "user_content", "must-not-leak", "free text",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("public audit DTO leaked %q: %s", forbidden, encoded)
		}
	}

	if _, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: f.admin.ID, OrganizationID: f.org.ID}); err != nil {
		t.Fatalf("admin query: %v", err)
	}
	for _, actor := range []Account{f.operator, f.member} {
		if _, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: actor.ID, OrganizationID: f.org.ID}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("role %s error = %v, want forbidden", actor.ID, err)
		}
	}

	probe, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, TargetDeviceID: "other-target",
	})
	if err != nil {
		t.Fatalf("cross-organization target filter must be a non-probing empty result: %v", err)
	}
	if len(probe.Entries) != 0 {
		t.Fatalf("cross-organization target filter returned %+v", probe.Entries)
	}
}

func TestTask10CAuditQueryStablePaginationAndCursorValidation(t *testing.T) {
	f := newTask10CFixture(t, 30)
	at := f.now.Add(-time.Hour)
	for _, id := range []string{"audit-a", "audit-b", "audit-c", "audit-d", "audit-e"} {
		f.addAudit(t, id, at, f.owner.ID, map[string]any{"action": id, "result": "ok"})
	}

	first, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want two records and cursor", first)
	}
	second, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, PageSize: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 2 || second.NextCursor == "" {
		t.Fatalf("second page = %+v, want two records and cursor", second)
	}
	seen := map[string]bool{}
	for _, entry := range append(first.Entries, second.Entries...) {
		if seen[entry.ID] {
			t.Fatalf("duplicate paginated id %q", entry.ID)
		}
		seen[entry.ID] = true
	}

	tampered := first.NextCursor[:len(first.NextCursor)-1] + "x"
	if _, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, PageSize: 2, Cursor: tampered}); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
	if _, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, PageSize: MaxOrganizationAuditPageSize + 1}); err == nil {
		t.Fatal("oversized page was accepted")
	}
	if _, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, PageSize: 2, Cursor: first.NextCursor, MemberAccountID: "changed-filter",
	}); err == nil {
		t.Fatal("cursor was accepted with changed filters")
	}
	otherOwner, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: "cursor-other-owner@example.com", PlanID: PlanTeam})
	if err != nil {
		t.Fatal(err)
	}
	otherOrganization, err := f.svc.CreateOrganization(f.ctx, CreateOrganizationRequest{OwnerAccountID: otherOwner.ID, Name: "Cursor Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
		ActorAccountID: otherOwner.ID, OrganizationID: otherOrganization.ID, PageSize: 2, Cursor: first.NextCursor,
	}); err == nil {
		t.Fatal("same-service cross-organization cursor was accepted")
	}

	other := newTask10CFixture(t, 30)
	if _, err := other.svc.QueryOrganizationAudit(other.ctx, OrganizationAuditQuery{
		ActorAccountID: other.owner.ID, OrganizationID: other.org.ID, PageSize: 2, Cursor: first.NextCursor,
	}); err == nil {
		t.Fatal("cross-organization/service cursor was accepted")
	}
}

func TestAuditCursorRejectsNonCanonicalBase64Encoding(t *testing.T) {
	svc := NewService(NewMemoryStore())
	cursor, err := svc.encodeOrganizationAuditCursor(organizationAuditCursor{
		Version: 1, OrganizationID: "org_cursor", FilterHash: "filter", SnapshotEnd: time.Now().UTC(),
		LastTime: time.Now().UTC().Add(-time.Minute), LastKind: OrganizationAuditKindEvent, LastID: "audit_cursor",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(cursor, ".")
	original, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var nonCanonical string
	for _, candidate := range alphabet {
		if byte(candidate) == parts[1][len(parts[1])-1] {
			continue
		}
		changed := parts[1][:len(parts[1])-1] + string(candidate)
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(changed)
		if decodeErr == nil && bytes.Equal(decoded, original) {
			nonCanonical = changed
			break
		}
	}
	if nonCanonical == "" {
		t.Fatal("failed to construct non-canonical equivalent signature")
	}
	if _, err := svc.decodeOrganizationAuditCursor(parts[0] + "." + nonCanonical); err == nil {
		t.Fatal("non-canonical cursor signature encoding was accepted")
	}
}

func TestTask10CAuditPaginationPinsRetentionCutoffToSnapshot(t *testing.T) {
	f := newTask10CFixture(t, 30)
	cutoff := f.now.Add(-30 * 24 * time.Hour)
	f.addAudit(t, "snapshot-new", f.now.Add(-time.Hour), f.owner.ID, map[string]any{"target_device_id": "snapshot-target"})
	f.addAudit(t, "snapshot-boundary", cutoff, f.owner.ID, map[string]any{"target_device_id": "snapshot-target"})

	first, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, TargetDeviceID: "snapshot-target", PageSize: 1,
	})
	if err != nil || len(first.Entries) != 1 || first.Entries[0].ID != "snapshot-new" || first.NextCursor == "" {
		t.Fatalf("first page = %+v, err=%v", first, err)
	}
	*f.clock = f.now.Add(2 * time.Hour)
	second, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, TargetDeviceID: "snapshot-target", PageSize: 1, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 || second.Entries[0].ID != "snapshot-boundary" {
		t.Fatalf("second page = %+v, want cutoff-boundary record from first-page snapshot", second)
	}
	if second.Retention.EffectiveStart == nil || !second.Retention.EffectiveStart.Equal(cutoff) {
		t.Fatalf("second page retention = %+v, want pinned cutoff %s", second.Retention, cutoff)
	}
}

func TestTask10CAuditRetentionFiniteUnlimitedUnavailableAndCustom(t *testing.T) {
	for _, days := range []int64{30, 90, 180} {
		t.Run(fmt.Sprintf("%d days", days), func(t *testing.T) {
			f := newTask10CFixture(t, days)
			cutoff := f.now.Add(-time.Duration(days) * 24 * time.Hour)
			f.addAudit(t, "expired", cutoff.Add(-time.Nanosecond), f.owner.ID, nil)
			f.addAudit(t, "boundary", cutoff, f.owner.ID, nil)
			f.addAudit(t, "recent", cutoff.Add(time.Nanosecond), f.owner.ID, nil)
			requestedStart := cutoff.Add(-24 * time.Hour)
			page, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
				ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, StartTime: &requestedStart,
			})
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for _, entry := range page.Entries {
				ids[entry.ID] = true
			}
			if ids["expired"] || !ids["boundary"] || !ids["recent"] || page.Retention.RetentionDays == nil || *page.Retention.RetentionDays != days || page.Retention.Mode != PlanQuotaLimited || !page.Retention.Truncated {
				t.Fatalf("page retention = %+v entries=%+v", page.Retention, page.Entries)
			}
			if page.Retention.RequestedStart == nil || !page.Retention.RequestedStart.Equal(requestedStart) || page.Retention.EffectiveStart == nil || !page.Retention.EffectiveStart.Equal(cutoff) {
				t.Fatalf("retention range = %+v, want requested/effective cutoff", page.Retention)
			}
			oldEnd := cutoff.Add(-time.Nanosecond)
			oldPage, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{
				ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, StartTime: &requestedStart, EndTime: &oldEnd,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(oldPage.Entries) != 0 || !oldPage.Retention.Truncated || !oldPage.Retention.RangeEmpty {
				t.Fatalf("old-only range = %+v entries=%+v, want explicit retention exclusion", oldPage.Retention, oldPage.Entries)
			}
		})
	}

	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	unlimited, err := resolveOrganizationAuditRetention(now, "owner", PlanEnterprise, UnlimitedPlanQuota(PlanQuotaUnitDays), NewPlanQuotaEvaluator(PlanQuotaEvaluatorConfig{}))
	if err != nil || unlimited.Mode != PlanQuotaUnlimited || unlimited.RetentionDays != nil || unlimited.Cutoff != nil {
		t.Fatalf("unlimited retention = %+v, err=%v", unlimited, err)
	}

	for _, tc := range []struct {
		name  string
		plan  PlanID
		quota PlanQuota
		cfg   PlanQuotaEvaluatorConfig
		mode  PlanQuotaMode
	}{
		{"unavailable", PlanFree, UnavailablePlanQuota(PlanQuotaUnitDays), PlanQuotaEvaluatorConfig{}, PlanQuotaUnavailable},
		{"unresolved custom", PlanEnterprise, ContractCustomPlanQuota(PlanQuotaUnitDays), PlanQuotaEvaluatorConfig{}, PlanQuotaContractCustom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveOrganizationAuditRetention(now, "owner", tc.plan, tc.quota, NewPlanQuotaEvaluator(tc.cfg))
			var quotaErr *QuotaError
			if !errors.As(err, &quotaErr) || quotaErr.Detail.Dimension != QuotaDimensionAuditLogRetention || quotaErr.Detail.Mode != tc.mode {
				t.Fatalf("error = %v, want structured audit retention %s refusal", err, tc.mode)
			}
		})
	}
}

func TestTask10CAuditQueryRejectsInactiveStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(task10CFixture)
		actor  func(task10CFixture) string
	}{
		{"suspended organization", func(f task10CFixture) {
			org := f.org
			org.Status = OrganizationStatusSuspended
			_, _ = f.store.UpdateOrganization(f.ctx, org)
		}, func(f task10CFixture) string { return f.owner.ID }},
		{"frozen account", func(f task10CFixture) {
			account := f.admin
			account.Status = AccountStatusFrozen
			_, _ = f.store.UpdateAccount(f.ctx, account)
		}, func(f task10CFixture) string { return f.admin.ID }},
		{"banned account", func(f task10CFixture) {
			account := f.admin
			account.Status = AccountStatusBanned
			_, _ = f.store.UpdateAccount(f.ctx, account)
		}, func(f task10CFixture) string { return f.admin.ID }},
		{"suspended membership", func(f task10CFixture) {
			member, _ := f.store.GetOrganizationMembership(f.ctx, f.org.ID, f.admin.ID)
			member.Status = MembershipStatusSuspended
			_, _ = f.store.UpdateMembership(f.ctx, member)
		}, func(f task10CFixture) string { return f.admin.ID }},
		{"removed membership", func(f task10CFixture) {
			member, _ := f.store.GetOrganizationMembership(f.ctx, f.org.ID, f.admin.ID)
			member.Status = MembershipStatusRemoved
			_, _ = f.store.UpdateMembership(f.ctx, member)
		}, func(f task10CFixture) string { return f.admin.ID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTask10CFixture(t, 30)
			tc.mutate(f)
			actorAccountID := tc.actor(f)
			if _, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: actorAccountID, OrganizationID: f.org.ID}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("error = %v, want forbidden", err)
			}
			if _, err := f.svc.CleanupOrganizationAudit(f.ctx, CleanupOrganizationAuditRequest{ActorAccountID: actorAccountID, OrganizationID: f.org.ID}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("cleanup error = %v, want forbidden", err)
			}
		})
	}
}

func TestTask10CAuditServiceRejectsUnavailableAndUnresolvedCustomRetention(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan PlanID
		mode PlanQuotaMode
	}{
		{"unavailable", PlanFree, PlanQuotaUnavailable},
		{"unresolved custom", PlanEnterprise, PlanQuotaContractCustom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTask10CFixture(t, 30)
			owner := f.owner
			owner.PlanID = tc.plan
			if _, err := f.store.UpdateAccount(f.ctx, owner); err != nil {
				t.Fatal(err)
			}
			for _, err := range []error{
				func() error {
					_, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: owner.ID, OrganizationID: f.org.ID})
					return err
				}(),
				func() error {
					_, err := f.svc.CleanupOrganizationAudit(f.ctx, CleanupOrganizationAuditRequest{ActorAccountID: owner.ID, OrganizationID: f.org.ID})
					return err
				}(),
			} {
				var quotaErr *QuotaError
				if !errors.As(err, &quotaErr) || quotaErr.Detail.Dimension != QuotaDimensionAuditLogRetention || quotaErr.Detail.Mode != tc.mode || quotaErr.Detail.PlanID != tc.plan {
					t.Fatalf("error = %v, want %s audit retention refusal for %s", err, tc.mode, tc.plan)
				}
			}
		})
	}
}

func TestTask10CAuditCleanupBatchBoundaryIsolationIdempotencyAndConcurrency(t *testing.T) {
	f := newTask10CFixture(t, 30)
	cutoff := f.now.Add(-30 * 24 * time.Hour)
	for _, id := range []string{"old-a", "old-b", "old-c"} {
		f.addAudit(t, id, cutoff.Add(-time.Hour), f.owner.ID, nil)
	}
	f.addAudit(t, "boundary", cutoff, f.owner.ID, nil)
	f.addAudit(t, "new", cutoff.Add(time.Hour), f.owner.ID, nil)
	f.addConnection(t, "old-conn-a", cutoff.Add(-time.Hour), f.org.ID, f.member.ID, "src", "dst", "relay", 10, 10)
	f.addConnection(t, "old-conn-b", cutoff.Add(-time.Hour), f.org.ID, f.member.ID, "src", "dst", "relay", 10, 10)
	f.addConnection(t, "boundary-conn", cutoff, f.org.ID, f.member.ID, "src", "dst", "relay", 10, 10)
	f.addAudit(t, "unowned-old", cutoff.Add(-time.Hour), f.owner.ID, map[string]any{"organization_id": ""})
	f.addConnection(t, "unowned-old-conn", cutoff.Add(-time.Hour), "", f.member.ID, "src", "dst", "relay", 10, 10)

	otherOwner, err := f.svc.CreateAccount(f.ctx, CreateAccountRequest{Email: "cleanup-other@example.com", PlanID: PlanTeam})
	if err != nil {
		t.Fatal(err)
	}
	otherOrg, err := f.svc.CreateOrganization(f.ctx, CreateOrganizationRequest{OwnerAccountID: otherOwner.ID, Name: "Cleanup Other"})
	if err != nil {
		t.Fatal(err)
	}
	f.addAudit(t, "other-old", cutoff.Add(-time.Hour), otherOwner.ID, map[string]any{"organization_id": otherOrg.ID})
	f.addConnection(t, "other-old-conn", cutoff.Add(-time.Hour), otherOrg.ID, otherOwner.ID, "src2", "dst2", "relay", 10, 10)

	if _, err := f.svc.CleanupOrganizationAudit(f.ctx, CleanupOrganizationAuditRequest{ActorAccountID: f.operator.ID, OrganizationID: f.org.ID, BatchSize: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator cleanup error = %v, want forbidden", err)
	}

	var wg sync.WaitGroup
	results := make(chan OrganizationAuditCleanupResult, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.svc.CleanupOrganizationAudit(f.ctx, CleanupOrganizationAuditRequest{ActorAccountID: f.admin.ID, OrganizationID: f.org.ID, BatchSize: 1})
			if err != nil {
				t.Errorf("concurrent cleanup: %v", err)
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	var deleted int
	for result := range results {
		if result.DeletedTotal > 1 || result.DeletedTotal != result.DeletedAuditEvents+result.DeletedConnectionLogs {
			t.Fatalf("invalid batch result %+v", result)
		}
		deleted += result.DeletedTotal
	}
	if deleted != 5 {
		t.Fatalf("deleted total = %d, want five expired organization records", deleted)
	}

	again, err := f.svc.CleanupOrganizationAudit(f.ctx, CleanupOrganizationAuditRequest{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, BatchSize: 10})
	if err != nil || again.DeletedTotal != 0 || again.HasMore {
		t.Fatalf("idempotent cleanup = %+v, err=%v", again, err)
	}
	page, err := f.svc.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, entry := range page.Entries {
		ids[entry.ID] = true
	}
	for _, want := range []string{"boundary", "new", "boundary-conn"} {
		if !ids[want] {
			t.Fatalf("boundary/new record %q missing after cleanup: %+v", want, page.Entries)
		}
	}
	otherEvents, err := f.store.ListOrganizationAuditEvents(f.ctx, otherOrg.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherLogs, err := f.store.ListOrganizationConnectionLogs(f.ctx, otherOrg.ID)
	if err != nil {
		t.Fatal(err)
	}
	var otherAuditFound, otherLogFound bool
	for _, event := range otherEvents {
		otherAuditFound = otherAuditFound || event.ID == "other-old"
	}
	for _, log := range otherLogs {
		otherLogFound = otherLogFound || log.ID == "other-old-conn"
	}
	if !otherAuditFound || !otherLogFound {
		t.Fatalf("other organization records changed: audit=%v connection=%v", otherAuditFound, otherLogFound)
	}
	legacy, err := f.store.ListConnectionLogs(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var unownedFound bool
	for _, log := range legacy {
		if log.ID == "unowned-old-conn" {
			unownedFound = true
		}
	}
	if !unownedFound {
		t.Fatal("cleanup deleted an unowned legacy security record")
	}
	allAuditEvents, err := f.store.ListAuditEvents(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var unownedAuditFound bool
	for _, event := range allAuditEvents {
		if event.ID == "unowned-old" {
			unownedAuditFound = true
		}
	}
	if !unownedAuditFound {
		t.Fatal("cleanup deleted an unowned legacy audit record")
	}

	maintained, err := f.svc.MaintainOrganizationAudit(f.ctx, f.org.ID, 10)
	if err != nil || maintained.DeletedTotal != 0 {
		t.Fatalf("system maintenance policy = %+v, err=%v", maintained, err)
	}
}

func TestTask10CNewConnectionLogsReceiveAuthoritativeOrganizationOwnership(t *testing.T) {
	f := newTask10CFixture(t, 30)
	network, err := f.svc.CreateNetwork(f.ctx, CreateNetworkRequest{AccountID: f.member.ID, Name: "member network"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.store.CreateDevice(f.ctx, Device{ID: "source-10c", AccountID: f.member.ID, NetworkID: network.ID, Status: DeviceStatusOnline, JoinedAt: f.now})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.store.CreateDevice(f.ctx, Device{ID: "target-10c", AccountID: f.member.ID, NetworkID: network.ID, Status: DeviceStatusOnline, JoinedAt: f.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateOrganizationDevice(f.ctx, OrganizationDevice{
		ID: "orgdev-10c", OrganizationID: f.org.ID, DeviceID: target.ID, AccountID: target.AccountID,
		EnrolledByAccountID: f.owner.ID, CreatedAt: f.now, UpdatedAt: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	log, err := f.svc.RecordConnectionLog(f.ctx, RecordConnectionLogRequest{
		AccountID: f.member.ID, NetworkID: network.ID, SourceDeviceID: source.ID, TargetDeviceID: target.ID,
		PathType: "relay", RelayBytesIn: 10, RelayBytesOut: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if log.OrganizationID != f.org.ID {
		t.Fatalf("connection organization_id = %q, want %q", log.OrganizationID, f.org.ID)
	}

	legacy, err := f.svc.RecordConnectionLog(f.ctx, RecordConnectionLogRequest{
		AccountID: f.member.ID, NetworkID: network.ID, SourceDeviceID: source.ID, TargetDeviceID: source.ID, PathType: "lan_direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.OrganizationID != "" {
		t.Fatalf("legacy connection organization_id = %q, want empty", legacy.OrganizationID)
	}
}

func TestTask10CAuditHTTPClientRoundTripAndBodyActorCannotEscalate(t *testing.T) {
	f := newTask10CFixture(t, 30)
	f.addAudit(t, "http-audit", f.now.Add(-time.Minute), f.owner.ID, map[string]any{"action": "query_test", "result": "ok"})
	hub := httptest.NewServer(NewServer(f.svc))
	defer hub.Close()

	ownerClient := &Client{BaseURL: hub.URL, HTTPClient: hub.Client(), ActorAccountID: f.owner.ID}
	page, err := ownerClient.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{OrganizationID: f.org.ID, PageSize: 10})
	if err != nil || len(page.Entries) == 0 || page.Retention.Mode != PlanQuotaLimited {
		t.Fatalf("client query page=%+v err=%v", page, err)
	}
	cleanup, err := ownerClient.CleanupOrganizationAudit(f.ctx, CleanupOrganizationAuditRequest{OrganizationID: f.org.ID, BatchSize: 10})
	if err != nil || cleanup.OrganizationID != f.org.ID {
		t.Fatalf("client cleanup=%+v err=%v", cleanup, err)
	}

	operatorClient := &Client{BaseURL: hub.URL, HTTPClient: hub.Client(), ActorAccountID: f.operator.ID}
	if _, err := operatorClient.QueryOrganizationAudit(f.ctx, OrganizationAuditQuery{OrganizationID: f.org.ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator query error = %v, want forbidden", err)
	}
	_, err = operatorClient.CleanupOrganizationAudit(f.ctx, CleanupOrganizationAuditRequest{
		ActorAccountID: f.owner.ID, OrganizationID: f.org.ID, BatchSize: 10,
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("spoofed cleanup actor error = %v, want forbidden", err)
	}
}
