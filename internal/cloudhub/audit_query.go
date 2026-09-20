package cloudhub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultOrganizationAuditPageSize  = 50
	MaxOrganizationAuditPageSize      = 100
	DefaultOrganizationAuditBatchSize = 100
	MaxOrganizationAuditBatchSize     = 1000

	OrganizationAuditKindEvent      = "audit"
	OrganizationAuditKindConnection = "connection"
)

type OrganizationAuditQuery struct {
	ActorAccountID   string     `json:"-"`
	OrganizationID   string     `json:"organization_id"`
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

// OrganizationAuditEntry is intentionally a field whitelist. Raw audit metadata,
// connection errors, addresses, provider data, traffic and user content never
// cross this boundary.
type OrganizationAuditEntry struct {
	ID               string                     `json:"id"`
	Kind             string                     `json:"kind"`
	Time             time.Time                  `json:"time"`
	ActorAccountID   string                     `json:"actor_account_id,omitempty"`
	MemberAccountID  string                     `json:"member_account_id,omitempty"`
	OrganizationID   string                     `json:"organization_id"`
	SourceDeviceID   string                     `json:"source_device_id,omitempty"`
	TargetDeviceID   string                     `json:"target_device_id,omitempty"`
	BundleID         string                     `json:"bundle_id,omitempty"`
	CredentialID     string                     `json:"credential_id,omitempty"`
	RolloutID        string                     `json:"rollout_id,omitempty"`
	TargetID         string                     `json:"target_id,omitempty"`
	GroupID          string                     `json:"group_id,omitempty"`
	CurrentVersion   string                     `json:"current_version,omitempty"`
	TargetVersion    string                     `json:"target_version,omitempty"`
	Action           string                     `json:"action"`
	Result           string                     `json:"result,omitempty"`
	ConnectionMethod string                     `json:"connection_method,omitempty"`
	PermissionSource ConnectionPermissionSource `json:"permission_source,omitempty"`
	RelayBytesIn     int64                      `json:"relay_bytes_in,omitempty"`
	RelayBytesOut    int64                      `json:"relay_bytes_out,omitempty"`
}

type OrganizationAuditRetentionMetadata struct {
	RequestedStart *time.Time    `json:"requested_start,omitempty"`
	RequestedEnd   *time.Time    `json:"requested_end,omitempty"`
	EffectiveStart *time.Time    `json:"effective_start,omitempty"`
	EffectiveEnd   *time.Time    `json:"effective_end,omitempty"`
	RetentionDays  *int64        `json:"retention_days,omitempty"`
	Mode           PlanQuotaMode `json:"mode"`
	PlanID         PlanID        `json:"plan_id"`
	Truncated      bool          `json:"truncated"`
	RangeEmpty     bool          `json:"range_empty"`
}

type OrganizationAuditPage struct {
	Entries    []OrganizationAuditEntry           `json:"entries"`
	NextCursor string                             `json:"next_cursor,omitempty"`
	Retention  OrganizationAuditRetentionMetadata `json:"retention"`
}

type CleanupOrganizationAuditRequest struct {
	ActorAccountID string `json:"actor_account_id,omitempty"`
	OrganizationID string `json:"organization_id"`
	BatchSize      int    `json:"batch_size,omitempty"`
}

type OrganizationAuditCleanupResult struct {
	OrganizationID        string        `json:"organization_id"`
	Mode                  PlanQuotaMode `json:"mode"`
	RetentionDays         *int64        `json:"retention_days,omitempty"`
	Cutoff                *time.Time    `json:"cutoff,omitempty"`
	BatchSize             int           `json:"batch_size"`
	DeletedAuditEvents    int           `json:"deleted_audit_events"`
	DeletedConnectionLogs int           `json:"deleted_connection_logs"`
	DeletedTotal          int           `json:"deleted_total"`
	HasMore               bool          `json:"has_more"`
}

type OrganizationAuditStoreCleanupResult struct {
	DeletedAuditEvents    int
	DeletedConnectionLogs int
	HasMore               bool
}

type organizationAuditRetention struct {
	Mode          PlanQuotaMode
	PlanID        PlanID
	RetentionDays *int64
	Cutoff        *time.Time
}

type organizationAuditCursor struct {
	Version        int       `json:"v"`
	OrganizationID string    `json:"organization_id"`
	FilterHash     string    `json:"filter_hash"`
	SnapshotEnd    time.Time `json:"snapshot_end"`
	LastTime       time.Time `json:"last_time"`
	LastKind       string    `json:"last_kind"`
	LastID         string    `json:"last_id"`
}

func (s *Service) QueryOrganizationAudit(ctx context.Context, query OrganizationAuditQuery) (OrganizationAuditPage, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	query = normalizeOrganizationAuditQuery(query)
	if query.PageSize == 0 {
		query.PageSize = DefaultOrganizationAuditPageSize
	}
	if query.PageSize < 1 || query.PageSize > MaxOrganizationAuditPageSize {
		return OrganizationAuditPage{}, fmt.Errorf("page_size must be between 1 and %d", MaxOrganizationAuditPageSize)
	}
	if query.MinRelayBytes < 0 {
		return OrganizationAuditPage{}, fmt.Errorf("min_relay_bytes cannot be negative")
	}
	if query.StartTime != nil && query.EndTime != nil && query.StartTime.After(*query.EndTime) {
		return OrganizationAuditPage{}, fmt.Errorf("start_time must not be after end_time")
	}
	organization, _, _, err := s.authorizeOrganizationLocked(ctx, query.OrganizationID, query.ActorAccountID, permissionAuditRead, true)
	if err != nil {
		return OrganizationAuditPage{}, err
	}
	snapshotEnd := s.nowTime()
	var cursor organizationAuditCursor
	if query.Cursor != "" {
		cursor, err = s.decodeOrganizationAuditCursor(query.Cursor)
		if err != nil {
			return OrganizationAuditPage{}, err
		}
		if cursor.OrganizationID != organization.ID {
			return OrganizationAuditPage{}, fmt.Errorf("audit cursor does not match organization or filters")
		}
		snapshotEnd = cursor.SnapshotEnd.UTC()
	}
	retention, err := s.organizationAuditRetentionAtLocked(ctx, organization, snapshotEnd)
	if err != nil {
		return OrganizationAuditPage{}, err
	}
	filterHash := organizationAuditFilterHash(query, retention)
	if query.Cursor != "" && cursor.FilterHash != filterHash {
		return OrganizationAuditPage{}, fmt.Errorf("audit cursor does not match organization or filters")
	}
	metadata, effectiveStart, effectiveEnd := organizationAuditRange(query, retention, snapshotEnd)

	events, err := s.store.ListOrganizationAuditEvents(ctx, organization.ID)
	if err != nil {
		return OrganizationAuditPage{}, err
	}
	logs, err := s.store.ListOrganizationConnectionLogs(ctx, organization.ID)
	if err != nil {
		return OrganizationAuditPage{}, err
	}
	entries := make([]OrganizationAuditEntry, 0, len(events)+len(logs))
	for _, event := range events {
		entry := publicOrganizationAuditEvent(organization.ID, event)
		if organizationAuditEntryMatches(entry, query, effectiveStart, effectiveEnd) {
			entries = append(entries, entry)
		}
	}
	for _, log := range logs {
		entry := publicOrganizationConnectionLog(organization.ID, log)
		if organizationAuditEntryMatches(entry, query, effectiveStart, effectiveEnd) {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return organizationAuditEntryBefore(entries[i], entries[j])
	})
	if query.Cursor != "" {
		out := entries[:0]
		for _, entry := range entries {
			if organizationAuditEntryIsAfterCursor(entry, cursor) {
				out = append(out, entry)
			}
		}
		entries = out
	}
	page := OrganizationAuditPage{Entries: entries, Retention: metadata}
	if len(page.Entries) > query.PageSize {
		page.Entries = page.Entries[:query.PageSize]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor, err = s.encodeOrganizationAuditCursor(organizationAuditCursor{
			Version: 1, OrganizationID: organization.ID, FilterHash: filterHash, SnapshotEnd: snapshotEnd,
			LastTime: last.Time, LastKind: last.Kind, LastID: last.ID,
		})
		if err != nil {
			return OrganizationAuditPage{}, err
		}
	}
	return page, nil
}

func (s *Service) CleanupOrganizationAudit(ctx context.Context, req CleanupOrganizationAuditRequest) (OrganizationAuditCleanupResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	batchSize, err := normalizeOrganizationAuditBatchSize(req.BatchSize)
	if err != nil {
		return OrganizationAuditCleanupResult{}, err
	}
	organization, _, membership, err := s.authorizeOrganizationLocked(ctx, req.OrganizationID, req.ActorAccountID, permissionAuditMaintain, true)
	if err != nil {
		return OrganizationAuditCleanupResult{}, err
	}
	return s.cleanupOrganizationAuditLocked(ctx, organization, strings.TrimSpace(req.ActorAccountID), string(managementPermissionSource(membership.Role)), batchSize)
}

// MaintainOrganizationAudit is the trusted system-maintenance entry point. It
// shares the same organization status, owner-plan retention and Store cleanup
// policy as the owner/admin path, but is intentionally not exposed over HTTP.
func (s *Service) MaintainOrganizationAudit(ctx context.Context, organizationID string, batchSize int) (OrganizationAuditCleanupResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	batchSize, err := normalizeOrganizationAuditBatchSize(batchSize)
	if err != nil {
		return OrganizationAuditCleanupResult{}, err
	}
	organization, err := s.store.GetOrganization(ctx, strings.TrimSpace(organizationID))
	if err != nil {
		return OrganizationAuditCleanupResult{}, fmt.Errorf("organization was not found: %w", err)
	}
	if err := ensureOrganizationActive(organization); err != nil {
		return OrganizationAuditCleanupResult{}, err
	}
	return s.cleanupOrganizationAuditLocked(ctx, organization, "", "system", batchSize)
}

func (s *Service) cleanupOrganizationAuditLocked(ctx context.Context, organization Organization, actorAccountID, permissionSource string, batchSize int) (OrganizationAuditCleanupResult, error) {
	retention, err := s.organizationAuditRetentionLocked(ctx, organization)
	if err != nil {
		return OrganizationAuditCleanupResult{}, err
	}
	result := OrganizationAuditCleanupResult{
		OrganizationID: organization.ID, Mode: retention.Mode, RetentionDays: cloneInt64Ptr(retention.RetentionDays),
		Cutoff: auditTimePtrValue(retention.Cutoff), BatchSize: batchSize,
	}
	if retention.Cutoff != nil {
		stored, err := s.store.DeleteOrganizationAuditBefore(ctx, organization.ID, *retention.Cutoff, batchSize)
		if err != nil {
			return OrganizationAuditCleanupResult{}, err
		}
		result.DeletedAuditEvents = stored.DeletedAuditEvents
		result.DeletedConnectionLogs = stored.DeletedConnectionLogs
		result.DeletedTotal = stored.DeletedAuditEvents + stored.DeletedConnectionLogs
		result.HasMore = stored.HasMore
	}
	if err := s.recordOrganizationAudit(ctx, AuditOrganizationAuditCleaned, actorAccountID, organization.ID, "", "cleanup_audit", "completed", map[string]any{
		"permission_source": permissionSource,
		"retention_mode":    string(result.Mode), "retention_days": int64Value(result.RetentionDays),
		"deleted_audit_events": result.DeletedAuditEvents, "deleted_connection_logs": result.DeletedConnectionLogs,
		"has_more": result.HasMore,
	}); err != nil {
		return OrganizationAuditCleanupResult{}, err
	}
	return result, nil
}

func (s *Service) organizationAuditRetentionLocked(ctx context.Context, organization Organization) (organizationAuditRetention, error) {
	return s.organizationAuditRetentionAtLocked(ctx, organization, s.nowTime())
}

func (s *Service) organizationAuditRetentionAtLocked(ctx context.Context, organization Organization, now time.Time) (organizationAuditRetention, error) {
	owner, err := s.activeOrganizationOwnerLocked(ctx, organization)
	if err != nil {
		return organizationAuditRetention{}, err
	}
	plan, err := s.planForAccount(owner)
	if err != nil {
		return organizationAuditRetention{}, err
	}
	decision, err := s.evaluateQuotaLocked(ctx, owner.ID, QuotaDimensionAuditLogRetention, plan.Entitlements.AuditLogRetention)
	if err != nil {
		return organizationAuditRetention{}, withQuotaPlan(err, plan.ID)
	}
	return resolveOrganizationAuditRetentionDecision(now, plan.ID, decision)
}

func resolveOrganizationAuditRetention(now time.Time, ownerAccountID string, planID PlanID, quota PlanQuota, evaluator PlanQuotaEvaluator) (organizationAuditRetention, error) {
	decision, err := evaluator.Evaluate(ownerAccountID, QuotaDimensionAuditLogRetention, quota)
	if err != nil {
		return organizationAuditRetention{}, withQuotaPlan(err, planID)
	}
	return resolveOrganizationAuditRetentionDecision(now, planID, decision)
}

func resolveOrganizationAuditRetentionDecision(now time.Time, planID PlanID, decision QuotaDecision) (organizationAuditRetention, error) {
	retention := organizationAuditRetention{Mode: decision.Mode, PlanID: planID}
	switch decision.Mode {
	case PlanQuotaUnlimited:
		return retention, nil
	case PlanQuotaUnavailable:
		return organizationAuditRetention{}, newQuotaError(QuotaDimensionAuditLogRetention, 0, 0, decision.Mode, planID, "audit log retention is unavailable")
	case PlanQuotaLimited, PlanQuotaContractCustom:
		if !decision.Limited || decision.Limit <= 0 {
			return organizationAuditRetention{}, newQuotaError(QuotaDimensionAuditLogRetention, 0, 0, decision.Mode, planID, "audit log retention days are not configured")
		}
		days := decision.Limit
		cutoff := now.UTC().Add(-time.Duration(days) * 24 * time.Hour)
		retention.RetentionDays = &days
		retention.Cutoff = &cutoff
		return retention, nil
	default:
		return organizationAuditRetention{}, newQuotaError(QuotaDimensionAuditLogRetention, 0, 0, decision.Mode, planID, "audit log retention mode is unsupported")
	}
}

func normalizeOrganizationAuditQuery(query OrganizationAuditQuery) OrganizationAuditQuery {
	query.ActorAccountID = strings.TrimSpace(query.ActorAccountID)
	query.OrganizationID = strings.TrimSpace(query.OrganizationID)
	query.MemberAccountID = strings.TrimSpace(query.MemberAccountID)
	query.SourceDeviceID = strings.TrimSpace(query.SourceDeviceID)
	query.TargetDeviceID = strings.TrimSpace(query.TargetDeviceID)
	query.ConnectionMethod = strings.ToLower(strings.TrimSpace(query.ConnectionMethod))
	query.Cursor = strings.TrimSpace(query.Cursor)
	if query.StartTime != nil {
		start := query.StartTime.UTC()
		query.StartTime = &start
	}
	if query.EndTime != nil {
		end := query.EndTime.UTC()
		query.EndTime = &end
	}
	return query
}

func organizationAuditRange(query OrganizationAuditQuery, retention organizationAuditRetention, snapshotEnd time.Time) (OrganizationAuditRetentionMetadata, *time.Time, *time.Time) {
	metadata := OrganizationAuditRetentionMetadata{
		RequestedStart: auditTimePtrValue(query.StartTime), RequestedEnd: auditTimePtrValue(query.EndTime),
		RetentionDays: cloneInt64Ptr(retention.RetentionDays), Mode: retention.Mode, PlanID: retention.PlanID,
	}
	start := auditTimePtrValue(query.StartTime)
	if retention.Cutoff != nil && (start == nil || start.Before(*retention.Cutoff)) {
		if start != nil {
			metadata.Truncated = true
		}
		start = auditTimePtrValue(retention.Cutoff)
	}
	end := snapshotEnd.UTC()
	if query.EndTime != nil && query.EndTime.Before(end) {
		end = query.EndTime.UTC()
	}
	metadata.EffectiveStart = auditTimePtrValue(start)
	metadata.EffectiveEnd = &end
	metadata.RangeEmpty = start != nil && end.Before(*start)
	return metadata, start, &end
}

func publicOrganizationAuditEvent(organizationID string, event AuditEvent) OrganizationAuditEntry {
	memberID := auditMetadataIdentifier(event.Metadata, "member_account_id")
	if memberID == "" {
		memberID = auditMetadataIdentifier(event.Metadata, "target_account_id")
	}
	action := auditSymbol(auditMetadataString(event.Metadata, "action"))
	if action == "" {
		action = auditSymbol(event.Event)
	}
	if action == "" {
		action = "organization_event"
	}
	method := auditMetadataString(event.Metadata, "path_type")
	if method == "" {
		method = auditMetadataString(event.Metadata, "preferred_path_type")
	}
	return OrganizationAuditEntry{
		ID: strings.TrimSpace(event.ID), Kind: OrganizationAuditKindEvent, Time: event.Time.UTC(),
		ActorAccountID: strings.TrimSpace(event.AccountID), MemberAccountID: memberID, OrganizationID: organizationID,
		SourceDeviceID: auditMetadataIdentifier(event.Metadata, "source_device_id"), TargetDeviceID: auditMetadataIdentifier(event.Metadata, "target_device_id"),
		BundleID: auditMetadataIdentifier(event.Metadata, "bundle_id"), CredentialID: auditMetadataIdentifier(event.Metadata, "credential_id"),
		RolloutID: auditMetadataIdentifier(event.Metadata, "rollout_id"), TargetID: auditMetadataIdentifier(event.Metadata, "target_id"),
		GroupID: auditMetadataIdentifier(event.Metadata, "group_id"), CurrentVersion: auditMetadataIdentifier(event.Metadata, "current_version"),
		TargetVersion: auditMetadataIdentifier(event.Metadata, "target_version"),
		Action:        action, Result: auditSymbol(auditMetadataString(event.Metadata, "result")), ConnectionMethod: publicConnectionMethod(method),
		PermissionSource: publicConnectionPermissionSource(auditMetadataString(event.Metadata, "permission_source")),
		RelayBytesIn:     nonNegativeInt64(auditMetadataInt64(event.Metadata, "relay_bytes_in")),
		RelayBytesOut:    nonNegativeInt64(auditMetadataInt64(event.Metadata, "relay_bytes_out")),
	}
}

func publicOrganizationConnectionLog(organizationID string, log ConnectionLog) OrganizationAuditEntry {
	result := auditSymbol(log.PathState)
	if strings.TrimSpace(log.Error) != "" {
		result = "failed"
	}
	return OrganizationAuditEntry{
		ID: strings.TrimSpace(log.ID), Kind: OrganizationAuditKindConnection, Time: log.StartedAt.UTC(),
		ActorAccountID: strings.TrimSpace(log.AccountID), MemberAccountID: strings.TrimSpace(log.AccountID), OrganizationID: organizationID,
		SourceDeviceID: strings.TrimSpace(log.SourceDeviceID), TargetDeviceID: strings.TrimSpace(log.TargetDeviceID),
		Action: "connection", Result: result, ConnectionMethod: publicConnectionMethod(log.PathType),
		PermissionSource: publicConnectionPermissionSource(string(log.PermissionSource)),
		RelayBytesIn:     nonNegativeInt64(log.RelayBytesIn), RelayBytesOut: nonNegativeInt64(log.RelayBytesOut),
	}
}

func organizationAuditEntryMatches(entry OrganizationAuditEntry, query OrganizationAuditQuery, start, end *time.Time) bool {
	if start != nil && entry.Time.Before(*start) {
		return false
	}
	if end != nil && entry.Time.After(*end) {
		return false
	}
	if query.MemberAccountID != "" && entry.MemberAccountID != query.MemberAccountID && entry.ActorAccountID != query.MemberAccountID {
		return false
	}
	if query.SourceDeviceID != "" && entry.SourceDeviceID != query.SourceDeviceID {
		return false
	}
	if query.TargetDeviceID != "" && entry.TargetDeviceID != query.TargetDeviceID {
		return false
	}
	if query.ConnectionMethod != "" && entry.ConnectionMethod != query.ConnectionMethod {
		return false
	}
	relayBytes := entry.RelayBytesIn + entry.RelayBytesOut
	if query.RelayOnly && relayBytes <= 0 {
		return false
	}
	return relayBytes >= query.MinRelayBytes
}

func organizationAuditEntryBefore(a, b OrganizationAuditEntry) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.After(b.Time)
	}
	if a.Kind != b.Kind {
		return a.Kind > b.Kind
	}
	return a.ID > b.ID
}

func organizationAuditEntryIsAfterCursor(entry OrganizationAuditEntry, cursor organizationAuditCursor) bool {
	if !entry.Time.Equal(cursor.LastTime) {
		return entry.Time.Before(cursor.LastTime)
	}
	if entry.Kind != cursor.LastKind {
		return entry.Kind < cursor.LastKind
	}
	return entry.ID < cursor.LastID
}

func organizationAuditFilterHash(query OrganizationAuditQuery, retention organizationAuditRetention) string {
	value := struct {
		Member, Source, Target, Method, Start, End, Mode, Plan string
		RelayOnly                                              bool
		MinRelayBytes                                          int64
		PageSize                                               int
		RetentionDays                                          int64
	}{
		Member: query.MemberAccountID, Source: query.SourceDeviceID, Target: query.TargetDeviceID, Method: query.ConnectionMethod,
		Start: auditTimeString(query.StartTime), End: auditTimeString(query.EndTime), Mode: string(retention.Mode), Plan: string(retention.PlanID),
		RelayOnly: query.RelayOnly, MinRelayBytes: query.MinRelayBytes, PageSize: query.PageSize, RetentionDays: int64Value(retention.RetentionDays),
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) encodeOrganizationAuditCursor(cursor organizationAuditCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.auditCursorKey)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) decodeOrganizationAuditCursor(value string) (organizationAuditCursor, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return organizationAuditCursor{}, fmt.Errorf("audit cursor is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != parts[0] {
		return organizationAuditCursor{}, fmt.Errorf("audit cursor is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[1] {
		return organizationAuditCursor{}, fmt.Errorf("audit cursor is invalid")
	}
	mac := hmac.New(sha256.New, s.auditCursorKey)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return organizationAuditCursor{}, fmt.Errorf("audit cursor signature is invalid")
	}
	var cursor organizationAuditCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || cursor.OrganizationID == "" || cursor.FilterHash == "" || cursor.LastID == "" || cursor.SnapshotEnd.IsZero() || cursor.LastTime.IsZero() {
		return organizationAuditCursor{}, fmt.Errorf("audit cursor payload is invalid")
	}
	return cursor, nil
}

func normalizeOrganizationAuditBatchSize(value int) (int, error) {
	if value == 0 {
		return DefaultOrganizationAuditBatchSize, nil
	}
	if value < 1 || value > MaxOrganizationAuditBatchSize {
		return 0, fmt.Errorf("batch_size must be between 1 and %d", MaxOrganizationAuditBatchSize)
	}
	return value, nil
}

func publicConnectionPermissionSource(value string) ConnectionPermissionSource {
	switch ConnectionPermissionSource(strings.TrimSpace(value)) {
	case ConnectionPermissionOwner, ConnectionPermissionAdmin, ConnectionPermissionDeviceGrant,
		ConnectionPermissionGroupGrant, ConnectionPermissionDeny, ConnectionPermissionLegacy:
		return ConnectionPermissionSource(strings.TrimSpace(value))
	default:
		return ""
	}
}

func auditMetadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func auditMetadataIdentifier(metadata map[string]any, key string) string {
	return auditIdentifier(auditMetadataString(metadata, key))
}

func auditIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return ""
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return ""
	}
	return value
}

func auditSymbol(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 80 {
		return ""
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return ""
	}
	return value
}

func publicConnectionMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "lan_direct":
		return "lan_direct"
	case "public_direct":
		return "public_direct"
	case "relay":
		return "relay"
	default:
		return ""
	}
}

func auditMetadataInt64(metadata map[string]any, key string) int64 {
	switch value := metadata[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		got, _ := value.Int64()
		return got
	default:
		return 0
	}
}

func auditTimePtrValue(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	got := value.UTC()
	return &got
}

func auditTimeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	got := *value
	return &got
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
