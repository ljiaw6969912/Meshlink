package cloudhub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const MaxSubscriptionWebhookBodyBytes int64 = 64 << 10

const (
	SubscriptionEventAccepted SubscriptionEventOutcome = "accepted"
	SubscriptionEventRepeated SubscriptionEventOutcome = "repeated"
	SubscriptionEventRejected SubscriptionEventOutcome = "rejected"
	SubscriptionEventConflict SubscriptionEventOutcome = "conflict"
	SubscriptionEventStale    SubscriptionEventOutcome = "stale"
)

type SubscriptionEventOutcome string

type SubscriptionProvider interface {
	Name() string
	VerifyWebhook(context.Context, http.Header, []byte, time.Time) error
	ParseWebhook(context.Context, []byte) (SubscriptionEvent, error)
}

type SubscriptionEvent struct {
	Provider               string
	EventID                string
	Type                   string
	AccountID              string
	PlanID                 PlanID
	Status                 SubscriptionStatus
	ProviderSubscriptionID string
	ProviderCustomerID     string
	EffectiveAt            time.Time
	EffectiveUntil         *time.Time
	Version                int64
	PayloadDigest          string
	ReceivedAt             time.Time
}

type SubscriptionEventApplyResult struct {
	Outcome        SubscriptionEventOutcome
	Subscription   Subscription
	Account        Account
	payloadDigest  string
	PreviousStatus SubscriptionStatus
	PreviousPlanID PlanID
	StatusChanged  bool
	PlanChanged    bool
}

type SubscriptionRefreshResult struct {
	Account        Account
	Subscription   Subscription
	Found          bool
	Changed        bool
	PreviousStatus SubscriptionStatus
	PreviousPlanID PlanID
}

type SubscriptionWebhookResult struct {
	Outcome      SubscriptionEventOutcome  `json:"outcome"`
	Subscription AccountSubscriptionStatus `json:"subscription,omitempty"`
}

type HMACSubscriptionProvider struct {
	name      string
	secret    []byte
	tolerance time.Duration
}

func NewHMACSubscriptionProvider(name string, secret []byte, tolerance time.Duration) SubscriptionProvider {
	name = strings.TrimSpace(name)
	if tolerance <= 0 {
		tolerance = 5 * time.Minute
	}
	copiedSecret := append([]byte(nil), secret...)
	return &HMACSubscriptionProvider{name: name, secret: copiedSecret, tolerance: tolerance}
}

func (p *HMACSubscriptionProvider) Name() string {
	return p.name
}

func (p *HMACSubscriptionProvider) VerifyWebhook(_ context.Context, header http.Header, body []byte, now time.Time) error {
	if p == nil || p.name == "" || len(p.secret) == 0 {
		return fmt.Errorf("subscription provider is not configured: %w", ErrForbidden)
	}
	timestampRaw := strings.TrimSpace(header.Get("X-CloudHub-Timestamp"))
	signatureRaw := strings.TrimSpace(header.Get("X-CloudHub-Signature"))
	if timestampRaw == "" || signatureRaw == "" {
		return fmt.Errorf("subscription webhook signature is missing: %w", ErrForbidden)
	}
	timestampUnix, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("subscription webhook timestamp is invalid: %w", ErrForbidden)
	}
	timestamp := time.Unix(timestampUnix, 0).UTC()
	now = now.UTC()
	if now.Sub(timestamp) > p.tolerance {
		return fmt.Errorf("subscription webhook timestamp is expired: %w", ErrForbidden)
	}
	if timestamp.Sub(now) > p.tolerance {
		return fmt.Errorf("subscription webhook timestamp is too far in the future: %w", ErrForbidden)
	}
	got := strings.TrimPrefix(signatureRaw, "v1=")
	expected := subscriptionWebhookSignature(p.secret, timestampUnix, body)
	gotBytes, err := hex.DecodeString(got)
	if err != nil {
		return fmt.Errorf("subscription webhook signature is invalid: %w", ErrForbidden)
	}
	expectedBytes, err := hex.DecodeString(expected)
	if err != nil {
		return fmt.Errorf("subscription webhook signature is invalid: %w", ErrForbidden)
	}
	if subtle.ConstantTimeCompare(gotBytes, expectedBytes) != 1 {
		return fmt.Errorf("subscription webhook signature is invalid: %w", ErrForbidden)
	}
	return nil
}

func (p *HMACSubscriptionProvider) ParseWebhook(_ context.Context, body []byte) (SubscriptionEvent, error) {
	var raw struct {
		EventID                string             `json:"event_id"`
		Type                   string             `json:"type"`
		AccountID              string             `json:"account_id"`
		PlanID                 PlanID             `json:"plan_id"`
		Status                 SubscriptionStatus `json:"status"`
		ProviderSubscriptionID string             `json:"provider_subscription_id"`
		ProviderCustomerID     string             `json:"provider_customer_id,omitempty"`
		EffectiveAt            string             `json:"effective_at"`
		EffectiveUntil         string             `json:"effective_until,omitempty"`
		CurrentPeriodEnd       string             `json:"current_period_end,omitempty"`
		ExpiresAt              string             `json:"expires_at,omitempty"`
		Version                int64              `json:"version,omitempty"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return SubscriptionEvent{}, fmt.Errorf("subscription webhook payload is invalid: %w", err)
	}
	effectiveAt, err := parseRequiredWebhookTime(raw.EffectiveAt, "effective_at")
	if err != nil {
		return SubscriptionEvent{}, err
	}
	effectiveUntil, err := parseOptionalWebhookTime(firstNonEmpty(raw.EffectiveUntil, raw.CurrentPeriodEnd, raw.ExpiresAt), "effective_until")
	if err != nil {
		return SubscriptionEvent{}, err
	}
	event := SubscriptionEvent{
		Provider:               p.Name(),
		EventID:                strings.TrimSpace(raw.EventID),
		Type:                   strings.TrimSpace(raw.Type),
		AccountID:              strings.TrimSpace(raw.AccountID),
		PlanID:                 PlanID(strings.TrimSpace(string(raw.PlanID))),
		Status:                 SubscriptionStatus(strings.TrimSpace(string(raw.Status))),
		ProviderSubscriptionID: strings.TrimSpace(raw.ProviderSubscriptionID),
		ProviderCustomerID:     strings.TrimSpace(raw.ProviderCustomerID),
		EffectiveAt:            effectiveAt,
		EffectiveUntil:         effectiveUntil,
		Version:                raw.Version,
	}
	if err := validateSubscriptionEvent(event); err != nil {
		return SubscriptionEvent{}, err
	}
	return event, nil
}

func (s *Service) ProcessSubscriptionWebhook(ctx context.Context, providerName string, header http.Header, body []byte) (SubscriptionWebhookResult, error) {
	providerName = strings.TrimSpace(providerName)
	provider := s.subscriptionProvider(providerName)
	if provider == nil {
		_ = s.recordAudit(ctx, AuditSubscriptionWebhookRejected, "", "", "", map[string]any{
			"provider": providerName,
			"outcome":  string(SubscriptionEventRejected),
			"reason":   "unknown provider",
		})
		return SubscriptionWebhookResult{}, fmt.Errorf("subscription provider was not found: %w", ErrNotFound)
	}
	now := s.nowTime()
	if err := provider.VerifyWebhook(ctx, header, body, now); err != nil {
		_ = s.recordAudit(ctx, AuditSubscriptionWebhookRejected, "", "", "", map[string]any{
			"provider": provider.Name(),
			"outcome":  string(SubscriptionEventRejected),
			"reason":   "verification_failed",
		})
		return SubscriptionWebhookResult{}, err
	}
	event, err := provider.ParseWebhook(ctx, body)
	if err != nil {
		_ = s.recordAudit(ctx, AuditSubscriptionWebhookRejected, "", "", "", map[string]any{
			"provider": provider.Name(),
			"outcome":  string(SubscriptionEventRejected),
			"reason":   "invalid_event",
		})
		return SubscriptionWebhookResult{}, err
	}
	event.Provider = provider.Name()
	event.PayloadDigest = digestSubscriptionWebhookBody(body)
	event.ReceivedAt = now
	if _, ok := LookupPlan(event.PlanID); !ok {
		_ = s.recordAudit(ctx, AuditSubscriptionWebhookRejected, event.AccountID, "", "", subscriptionAuditMetadata(event, SubscriptionEventRejected, "unknown plan"))
		return SubscriptionWebhookResult{}, fmt.Errorf("plan was not found: %w", ErrNotFound)
	}

	s.opMu.Lock()
	result, err := s.store.ApplySubscriptionEvent(ctx, event)
	s.opMu.Unlock()
	if err != nil {
		outcome := SubscriptionEventRejected
		auditEvent := AuditSubscriptionWebhookRejected
		if errors.Is(err, ErrConflict) {
			outcome = SubscriptionEventConflict
			auditEvent = AuditSubscriptionWebhookConflict
		}
		_ = s.recordAudit(ctx, auditEvent, event.AccountID, "", "", subscriptionAuditMetadata(event, outcome, err.Error()))
		return SubscriptionWebhookResult{}, err
	}
	if result.Outcome == SubscriptionEventRepeated {
		_ = s.recordAudit(ctx, AuditSubscriptionWebhookRepeated, result.Subscription.AccountID, "", "", subscriptionAuditMetadata(event, result.Outcome, ""))
		return SubscriptionWebhookResult{Outcome: result.Outcome, Subscription: publicSubscriptionStatus(result.Subscription, result.Account)}, nil
	}
	if result.Outcome == SubscriptionEventStale {
		_ = s.recordAudit(ctx, AuditSubscriptionWebhookRejected, result.Subscription.AccountID, "", "", subscriptionAuditMetadata(event, result.Outcome, "stale event"))
		return SubscriptionWebhookResult{Outcome: result.Outcome, Subscription: publicSubscriptionStatus(result.Subscription, result.Account)}, nil
	}
	if err := s.recordAudit(ctx, AuditSubscriptionWebhookAccepted, result.Subscription.AccountID, "", "", subscriptionAuditMetadata(event, result.Outcome, "")); err != nil {
		return SubscriptionWebhookResult{}, err
	}
	if result.StatusChanged || result.PlanChanged {
		if err := s.recordAudit(ctx, AuditSubscriptionStatusChanged, result.Subscription.AccountID, "", "", map[string]any{
			"provider":         event.Provider,
			"event_id":         event.EventID,
			"previous_status":  string(result.PreviousStatus),
			"status":           string(result.Subscription.Status),
			"previous_plan_id": string(result.PreviousPlanID),
			"plan_id":          string(result.Account.PlanID),
			"effective_at":     result.Subscription.EffectiveAt,
			"effective_until":  result.Subscription.EffectiveUntil,
		}); err != nil {
			return SubscriptionWebhookResult{}, err
		}
	}
	return SubscriptionWebhookResult{Outcome: result.Outcome, Subscription: publicSubscriptionStatus(result.Subscription, result.Account)}, nil
}

func (s *Service) GetAccountSubscriptionStatus(ctx context.Context, accountID string) (AccountSubscriptionStatus, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AccountSubscriptionStatus{}, fmt.Errorf("account_id is required")
	}
	if _, err := s.store.GetAccount(ctx, accountID); err != nil {
		return AccountSubscriptionStatus{}, fmt.Errorf("account was not found: %w", err)
	}
	refresh, err := s.store.RefreshAccountSubscription(ctx, accountID, s.nowTime())
	if err != nil {
		return AccountSubscriptionStatus{}, err
	}
	if !refresh.Found {
		return AccountSubscriptionStatus{}, fmt.Errorf("subscription was not found: %w", ErrNotFound)
	}
	if refresh.Changed {
		if err := s.recordAudit(ctx, AuditSubscriptionStatusChanged, refresh.Subscription.AccountID, "", "", map[string]any{
			"provider":         refresh.Subscription.Provider,
			"previous_status":  string(refresh.PreviousStatus),
			"status":           string(refresh.Subscription.Status),
			"previous_plan_id": string(refresh.PreviousPlanID),
			"plan_id":          string(refresh.Account.PlanID),
			"effective_at":     refresh.Subscription.EffectiveAt,
			"effective_until":  refresh.Subscription.EffectiveUntil,
			"reason":           "subscription effective period ended",
		}); err != nil {
			return AccountSubscriptionStatus{}, err
		}
	}
	return publicSubscriptionStatus(refresh.Subscription, refresh.Account), nil
}

func (s *Service) subscriptionProvider(name string) SubscriptionProvider {
	s.subscriptionMu.RLock()
	defer s.subscriptionMu.RUnlock()
	return s.subscriptionProviders[strings.TrimSpace(name)]
}

func (s *Service) refreshAccountSubscriptionLocked(ctx context.Context, account Account) (Account, error) {
	refresh, err := s.store.RefreshAccountSubscription(ctx, account.ID, s.nowTime())
	if err != nil {
		return Account{}, err
	}
	if !refresh.Found {
		return account, nil
	}
	if refresh.Changed {
		if err := s.recordAudit(ctx, AuditSubscriptionStatusChanged, refresh.Subscription.AccountID, "", "", map[string]any{
			"provider":         refresh.Subscription.Provider,
			"previous_status":  string(refresh.PreviousStatus),
			"status":           string(refresh.Subscription.Status),
			"previous_plan_id": string(refresh.PreviousPlanID),
			"plan_id":          string(refresh.Account.PlanID),
			"effective_at":     refresh.Subscription.EffectiveAt,
			"effective_until":  refresh.Subscription.EffectiveUntil,
			"reason":           "subscription effective period ended",
		}); err != nil {
			return Account{}, err
		}
	}
	return refresh.Account, nil
}

func publicSubscriptionStatus(subscription Subscription, account Account) AccountSubscriptionStatus {
	effectiveAt := subscription.EffectiveAt
	return AccountSubscriptionStatus{
		PlanID:         account.PlanID,
		Status:         subscription.Status,
		EffectiveAt:    &effectiveAt,
		EffectiveUntil: cloneTimePtr(subscription.EffectiveUntil),
	}
}

func subscriptionWebhookSignature(secret []byte, timestampUnix int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(strconv.FormatInt(timestampUnix, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func digestSubscriptionWebhookBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validateSubscriptionEvent(event SubscriptionEvent) error {
	if event.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if event.EventID == "" {
		return fmt.Errorf("event_id is required")
	}
	if event.AccountID == "" {
		return fmt.Errorf("account_id is required")
	}
	if event.PlanID == "" {
		return fmt.Errorf("plan_id is required")
	}
	if event.ProviderSubscriptionID == "" {
		return fmt.Errorf("provider_subscription_id is required")
	}
	switch event.Status {
	case SubscriptionStatusPending, SubscriptionStatusActive, SubscriptionStatusPastDue, SubscriptionStatusCanceled, SubscriptionStatusExpired:
		return nil
	default:
		return fmt.Errorf("subscription status %q is not supported", event.Status)
	}
}

func parseRequiredWebhookTime(value, field string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required", field)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is invalid: %w", field, err)
	}
	return parsed.UTC(), nil
}

func parseOptionalWebhookTime(value, field string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid: %w", field, err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func subscriptionAuditMetadata(event SubscriptionEvent, outcome SubscriptionEventOutcome, reason string) map[string]any {
	metadata := map[string]any{
		"provider":     event.Provider,
		"event_id":     event.EventID,
		"outcome":      string(outcome),
		"status":       string(event.Status),
		"plan_id":      string(event.PlanID),
		"effective_at": event.EffectiveAt,
	}
	if event.Type != "" {
		metadata["type"] = event.Type
	}
	if event.EffectiveUntil != nil {
		metadata["effective_until"] = event.EffectiveUntil
	}
	if reason != "" {
		metadata["reason"] = reason
	}
	return metadata
}

func cloneTimePtr(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := in.UTC()
	return &out
}
