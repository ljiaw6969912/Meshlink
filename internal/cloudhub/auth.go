package cloudhub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"meshlink/internal/licensing"
)

type Service struct {
	store          Store
	auditCursorKey []byte

	opMu  sync.Mutex
	nowMu sync.RWMutex
	now   func() time.Time

	hookMu              sync.RWMutex
	relaySessionRevoked func(context.Context, RelaySession) error

	subscriptionMu        sync.RWMutex
	subscriptionProviders map[string]SubscriptionProvider

	policies          map[string]AccountPolicy
	defaultPolicyName string
	quotaEvaluator    PlanQuotaEvaluator
	trustedUpdates    map[string]TrustedUpdateVersion
	privateLicenseMu  sync.RWMutex
	privateLicense    *licensing.Manager
}

type Option func(*Service)

func WithTrustedUpdateVersions(versions ...TrustedUpdateVersion) Option {
	return func(s *Service) {
		for _, version := range versions {
			if normalized, err := normalizeTrustedUpdateVersion(version); err == nil {
				s.trustedUpdates[normalized.Version] = normalized
			}
		}
	}
}

func WithPrivateLicenseManager(manager *licensing.Manager) Option {
	return func(s *Service) {
		s.privateLicense = manager
	}
}

func WithNow(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

const DefaultAccountPolicyName = "official-starter-low"

var DefaultAccountPolicy = AccountPolicy{
	Name:                   DefaultAccountPolicyName,
	RelayBytesQuota:        10 * 1024 * 1024,
	MaxActiveRelaySessions: 2,
	MaxRelaySessionsPerDay: 20,
}

func WithAccountPolicies(defaultPolicyName string, policies ...AccountPolicy) Option {
	return func(s *Service) {
		catalog := map[string]AccountPolicy{
			DefaultAccountPolicy.Name: DefaultAccountPolicy,
		}
		for _, policy := range policies {
			policy.Name = strings.TrimSpace(policy.Name)
			if policy.Name == "" {
				continue
			}
			catalog[policy.Name] = policy
		}
		defaultPolicyName = strings.TrimSpace(defaultPolicyName)
		if defaultPolicyName == "" {
			defaultPolicyName = DefaultAccountPolicy.Name
		}
		if _, ok := catalog[defaultPolicyName]; !ok {
			defaultPolicyName = DefaultAccountPolicy.Name
		}
		s.policies = catalog
		s.defaultPolicyName = defaultPolicyName
	}
}

func WithPlanQuotaLimits(limits map[string]int64) Option {
	return func(s *Service) {
		cfg := PlanQuotaEvaluatorConfig{
			OperatorLimits: cloneInt64Map(s.quotaEvaluator.operatorLimits),
			ContractLimits: cloneInt64Map(s.quotaEvaluator.contractLimits),
		}
		for key, value := range limits {
			key = strings.TrimSpace(key)
			if key != "" {
				cfg.OperatorLimits[key] = value
			}
		}
		s.quotaEvaluator = NewPlanQuotaEvaluator(cfg)
	}
}

func WithContractPlanQuotaLimits(limits map[string]int64) Option {
	return func(s *Service) {
		cfg := PlanQuotaEvaluatorConfig{
			OperatorLimits: cloneInt64Map(s.quotaEvaluator.operatorLimits),
			ContractLimits: cloneInt64Map(s.quotaEvaluator.contractLimits),
		}
		for key, value := range limits {
			key = strings.TrimSpace(key)
			if key != "" {
				cfg.ContractLimits[key] = value
			}
		}
		s.quotaEvaluator = NewPlanQuotaEvaluator(cfg)
	}
}

func WithRelaySessionRevokedHook(hook func(context.Context, RelaySession) error) Option {
	return func(s *Service) {
		s.relaySessionRevoked = hook
	}
}

func WithSubscriptionProviders(providers ...SubscriptionProvider) Option {
	return func(s *Service) {
		for _, provider := range providers {
			if provider == nil {
				continue
			}
			name := strings.TrimSpace(provider.Name())
			if name == "" {
				continue
			}
			s.subscriptionProviders[name] = provider
		}
	}
}

func NewService(store Store, opts ...Option) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	auditCursorKey := make([]byte, 32)
	if _, err := rand.Read(auditCursorKey); err != nil {
		panic(err)
	}
	s := &Service{
		store:          store,
		auditCursorKey: auditCursorKey,
		now: func() time.Time {
			return time.Now().UTC()
		},
		policies: map[string]AccountPolicy{
			DefaultAccountPolicy.Name: DefaultAccountPolicy,
		},
		defaultPolicyName:     DefaultAccountPolicy.Name,
		quotaEvaluator:        NewPlanQuotaEvaluator(PlanQuotaEvaluatorConfig{}),
		subscriptionProviders: map[string]SubscriptionProvider{},
		trustedUpdates: map[string]TrustedUpdateVersion{
			"0.1.0-dev": {Version: "0.1.0-dev", PackageFile: "meshlink-0.1.0-dev.zip", Source: TrustedUpdateSourceManifest},
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) SetRelaySessionRevokedHook(hook func(context.Context, RelaySession) error) {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	s.relaySessionRevoked = hook
}

func (s *Service) SetNowForTest(now func() time.Time) {
	s.nowMu.Lock()
	defer s.nowMu.Unlock()
	if now != nil {
		s.now = now
	}
}

func (s *Service) notifyRelaySessionsRevoked(ctx context.Context, sessions []RelaySession) error {
	if len(sessions) == 0 {
		return nil
	}
	s.hookMu.RLock()
	hook := s.relaySessionRevoked
	s.hookMu.RUnlock()
	if hook == nil {
		return nil
	}
	for _, session := range sessions {
		if err := hook(ctx, publicRelaySession(session)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) nowTime() time.Time {
	s.nowMu.RLock()
	defer s.nowMu.RUnlock()
	return s.now().UTC()
}

func (s *Service) CreateAccount(ctx context.Context, req CreateAccountRequest) (Account, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	email := strings.TrimSpace(req.Email)
	if email == "" {
		return Account{}, fmt.Errorf("email is required")
	}
	planID := PlanID(strings.TrimSpace(string(req.PlanID)))
	if planID == "" && s.privateLicenseEnabled() {
		planID = PlanEnterprise
	}
	if planID != "" {
		if _, ok := LookupPlan(planID); !ok {
			return Account{}, fmt.Errorf("plan was not found: %w", ErrNotFound)
		}
	}
	now := s.nowTime()
	account := Account{
		ID:          mustID("acct"),
		Email:       email,
		DisplayName: strings.TrimSpace(req.DisplayName),
		Status:      AccountStatusActive,
		PolicyName:  s.defaultPolicyName,
		PlanID:      planID,
		CreatedAt:   now,
	}
	created, err := s.store.CreateAccount(ctx, account)
	if err != nil {
		return Account{}, err
	}
	if err := s.recordAudit(ctx, AuditAccountCreated, created.ID, "", "", map[string]any{
		"email": created.Email,
	}); err != nil {
		return Account{}, err
	}
	return created, nil
}

func randomTokenBytes(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func newID(prefix string) (string, error) {
	token, err := randomTokenBytes(12)
	if err != nil {
		return "", err
	}
	return prefix + "_" + token, nil
}

func mustID(prefix string) string {
	id, err := newID(prefix)
	if err != nil {
		panic(err)
	}
	return id
}
