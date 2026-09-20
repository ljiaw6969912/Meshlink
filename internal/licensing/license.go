package licensing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const Schema = "meshlink-private-license-v1"

var (
	ErrInvalidDocument  = errors.New("invalid private license document")
	ErrUnknownKey       = errors.New("private license key is not trusted")
	ErrSignatureInvalid = errors.New("private license signature is invalid")
	ErrBindingMismatch  = errors.New("private license binding mismatch")
	ErrNotEffective     = errors.New("private license is not effective")
	ErrExpired          = errors.New("private license is expired")
	ErrPolicyDenied     = errors.New("private license policy denied operation")
	ErrUnavailable      = errors.New("private license is unavailable")
)

type ExpiryPolicy string

const (
	ExpiryPolicyContinueExisting ExpiryPolicy = "continue_existing"
	ExpiryPolicyGracePeriod      ExpiryPolicy = "grace_period"
	ExpiryPolicyDenyAll          ExpiryPolicy = "deny_all"
)

type Operation string

const (
	OperationNewDevice          Operation = "new_device"
	OperationNewMember          Operation = "new_member"
	OperationNewDeployment      Operation = "new_deployment"
	OperationNewRollout         Operation = "new_rollout"
	OperationOfflineUpdate      Operation = "offline_update"
	OperationExistingHeartbeat  Operation = "existing_heartbeat"
	OperationExistingConnection Operation = "existing_connection"
	OperationExistingP2P        Operation = "existing_p2p"
	OperationExistingRelay      Operation = "existing_relay"
)

type Entitlements struct {
	DeviceCount             int64 `json:"device_count"`
	MemberCount             int64 `json:"member_count"`
	ConcurrentOnlineDevices int64 `json:"concurrent_online_devices"`
	RelayBytesPerMonth      int64 `json:"relay_bytes_per_month"`
	ActiveRelaySessions     int64 `json:"active_relay_sessions"`
	AuditRetentionDays      int64 `json:"audit_retention_days"`
	DeploymentCount         int64 `json:"deployment_count"`
	PrivateDeployment       bool  `json:"private_deployment"`
	Relay                   bool  `json:"relay"`
	Rollout                 bool  `json:"rollout"`
	OfflineUpdates          bool  `json:"offline_updates"`
}

type Support struct {
	ID      string `json:"id"`
	Contact string `json:"contact"`
}

type Payload struct {
	LicenseID          string       `json:"license_id"`
	KeyID              string       `json:"key_id"`
	Customer           string       `json:"customer"`
	OrganizationID     string       `json:"organization_id"`
	DeploymentID       string       `json:"deployment_id"`
	IssuedAt           string       `json:"issued_at"`
	NotBefore          string       `json:"not_before"`
	ExpiresAt          string       `json:"expires_at"`
	GracePeriodSeconds int64        `json:"grace_period_seconds,omitempty"`
	ExpiryPolicy       ExpiryPolicy `json:"expiry_policy"`
	GraceEndPolicy     ExpiryPolicy `json:"grace_end_policy,omitempty"`
	Entitlements       Entitlements `json:"entitlements"`
	Support            Support      `json:"support"`
}

type SignedDocument struct {
	Schema    string          `json:"schema"`
	KeyID     string          `json:"key_id"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

type TrustedKeys map[string]ed25519.PublicKey

type Binding struct {
	OrganizationID string
	DeploymentID   string
}

type Verified struct {
	Payload   Payload
	IssuedAt  time.Time
	NotBefore time.Time
	ExpiresAt time.Time
}

type Summary struct {
	LicenseID      string       `json:"license_id"`
	KeyID          string       `json:"key_id"`
	OrganizationID string       `json:"organization_id"`
	DeploymentID   string       `json:"deployment_id"`
	NotBefore      time.Time    `json:"not_before"`
	ExpiresAt      time.Time    `json:"expires_at"`
	ExpiryPolicy   ExpiryPolicy `json:"expiry_policy"`
	GraceEndsAt    *time.Time   `json:"grace_ends_at,omitempty"`
	GraceEndPolicy ExpiryPolicy `json:"grace_end_policy,omitempty"`
	State          string       `json:"state"`
	Entitlements   Entitlements `json:"entitlements"`
	Support        Support      `json:"support"`
	Signature      string       `json:"-"`
	RawPayload     string       `json:"-"`
}

var stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func Sign(payload Payload, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key size is invalid: %w", ErrInvalidDocument)
	}
	canonical, _, err := canonicalPayload(payload)
	if err != nil {
		return nil, err
	}
	document := SignedDocument{
		Schema:    Schema,
		KeyID:     payload.KeyID,
		Payload:   canonical,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, signatureMessage(Schema, payload.KeyID, canonical))),
	}
	return json.Marshal(document)
}

func Verify(raw []byte, keys TrustedKeys, binding Binding, now time.Time) (Verified, error) {
	verified, err := verifyDocument(raw, keys, binding)
	if err != nil {
		return Verified{}, err
	}
	now = now.UTC()
	if now.Before(verified.NotBefore) {
		return Verified{}, ErrNotEffective
	}
	if !now.Before(verified.ExpiresAt) {
		return Verified{}, ErrExpired
	}
	return verified, nil
}

func VerifyAllowExpired(raw []byte, keys TrustedKeys, binding Binding, now time.Time) (Verified, error) {
	verified, err := verifyDocument(raw, keys, binding)
	if err != nil {
		return Verified{}, err
	}
	if now.UTC().Before(verified.NotBefore) {
		return Verified{}, ErrNotEffective
	}
	return verified, nil
}

func verifyDocument(raw []byte, keys TrustedKeys, binding Binding) (Verified, error) {
	var document SignedDocument
	if err := decodeStrict(raw, &document); err != nil {
		return Verified{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if document.Schema != Schema || !stableIDPattern.MatchString(document.KeyID) || len(document.Payload) == 0 || strings.TrimSpace(document.Signature) == "" {
		return Verified{}, ErrInvalidDocument
	}
	publicKey, ok := keys[document.KeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return Verified{}, ErrUnknownKey
	}
	signature, err := base64.RawURLEncoding.DecodeString(document.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, signatureMessage(document.Schema, document.KeyID, document.Payload), signature) {
		return Verified{}, ErrSignatureInvalid
	}
	var payload Payload
	if err := decodeStrict(document.Payload, &payload); err != nil {
		return Verified{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	canonical, verified, err := canonicalPayload(payload)
	if err != nil {
		return Verified{}, err
	}
	if !bytes.Equal(canonical, document.Payload) {
		return Verified{}, fmt.Errorf("payload is not canonical: %w", ErrInvalidDocument)
	}
	if payload.KeyID != document.KeyID {
		return Verified{}, fmt.Errorf("key id differs between envelope and payload: %w", ErrInvalidDocument)
	}
	if strings.TrimSpace(binding.OrganizationID) == "" || strings.TrimSpace(binding.DeploymentID) == "" ||
		payload.OrganizationID != strings.TrimSpace(binding.OrganizationID) || payload.DeploymentID != strings.TrimSpace(binding.DeploymentID) {
		return Verified{}, ErrBindingMismatch
	}
	return verified, nil
}

func canonicalPayload(payload Payload) ([]byte, Verified, error) {
	issuedAt, err := parseCanonicalTime(payload.IssuedAt)
	if err != nil {
		return nil, Verified{}, fmt.Errorf("issued_at: %w", err)
	}
	notBefore, err := parseCanonicalTime(payload.NotBefore)
	if err != nil {
		return nil, Verified{}, fmt.Errorf("not_before: %w", err)
	}
	expiresAt, err := parseCanonicalTime(payload.ExpiresAt)
	if err != nil {
		return nil, Verified{}, fmt.Errorf("expires_at: %w", err)
	}
	if err := validatePayload(payload, issuedAt, notBefore, expiresAt); err != nil {
		return nil, Verified{}, err
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, Verified{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	return canonical, Verified{Payload: payload, IssuedAt: issuedAt, NotBefore: notBefore, ExpiresAt: expiresAt}, nil
}

func validatePayload(payload Payload, issuedAt, notBefore, expiresAt time.Time) error {
	for name, value := range map[string]string{
		"license_id": payload.LicenseID, "key_id": payload.KeyID, "organization_id": payload.OrganizationID, "deployment_id": payload.DeploymentID,
	} {
		if !stableIDPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid: %w", name, ErrInvalidDocument)
		}
	}
	if strings.TrimSpace(payload.Customer) == "" || payload.Customer != strings.TrimSpace(payload.Customer) {
		return fmt.Errorf("customer is required and must be trimmed: %w", ErrInvalidDocument)
	}
	if issuedAt.After(notBefore) || !notBefore.Before(expiresAt) {
		return fmt.Errorf("license time range is invalid: %w", ErrInvalidDocument)
	}
	if err := validateExpiryPolicy(payload); err != nil {
		return err
	}
	e := payload.Entitlements
	for name, value := range map[string]int64{
		"device_count": e.DeviceCount, "member_count": e.MemberCount, "concurrent_online_devices": e.ConcurrentOnlineDevices,
		"relay_bytes_per_month": e.RelayBytesPerMonth, "active_relay_sessions": e.ActiveRelaySessions,
		"audit_retention_days": e.AuditRetentionDays, "deployment_count": e.DeploymentCount,
	} {
		if value <= 0 {
			return fmt.Errorf("entitlement %s must be positive: %w", name, ErrInvalidDocument)
		}
	}
	if !e.PrivateDeployment {
		return fmt.Errorf("private_deployment entitlement is required: %w", ErrInvalidDocument)
	}
	if strings.TrimSpace(payload.Support.ID) == "" || strings.TrimSpace(payload.Support.Contact) == "" ||
		payload.Support.ID != strings.TrimSpace(payload.Support.ID) || payload.Support.Contact != strings.TrimSpace(payload.Support.Contact) {
		return fmt.Errorf("support id and contact are required and must be trimmed: %w", ErrInvalidDocument)
	}
	return nil
}

func validateExpiryPolicy(payload Payload) error {
	switch payload.ExpiryPolicy {
	case ExpiryPolicyContinueExisting, ExpiryPolicyDenyAll:
		if payload.GracePeriodSeconds != 0 || payload.GraceEndPolicy != "" {
			return fmt.Errorf("non-grace expiry policy cannot include grace settings: %w", ErrInvalidDocument)
		}
	case ExpiryPolicyGracePeriod:
		if payload.GracePeriodSeconds <= 0 || (payload.GraceEndPolicy != ExpiryPolicyContinueExisting && payload.GraceEndPolicy != ExpiryPolicyDenyAll) {
			return fmt.Errorf("grace period and terminal policy are required: %w", ErrInvalidDocument)
		}
	default:
		return fmt.Errorf("expiry policy is invalid: %w", ErrInvalidDocument)
	}
	return nil
}

func parseCanonicalTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("time is invalid: %w", ErrInvalidDocument)
	}
	parsed = parsed.UTC()
	if raw != parsed.Format(time.RFC3339Nano) {
		return time.Time{}, fmt.Errorf("time must use canonical UTC RFC3339 encoding: %w", ErrInvalidDocument)
	}
	return parsed, nil
}

func signatureMessage(schema, keyID string, payload []byte) []byte {
	message := make([]byte, 0, len(schema)+len(keyID)+len(payload)+32)
	message = append(message, "meshlink-signed-document\x00"...)
	message = append(message, schema...)
	message = append(message, 0)
	message = append(message, keyID...)
	message = append(message, 0)
	message = append(message, payload...)
	return message
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (v Verified) Authorize(operation Operation, now time.Time) error {
	now = now.UTC()
	if now.Before(v.NotBefore) {
		return ErrNotEffective
	}
	if now.Before(v.ExpiresAt) {
		return v.authorizeEntitlement(operation)
	}
	if isNewOperation(operation) {
		return ErrPolicyDenied
	}
	policy := v.Payload.ExpiryPolicy
	if policy == ExpiryPolicyGracePeriod {
		graceEnd := v.ExpiresAt.Add(time.Duration(v.Payload.GracePeriodSeconds) * time.Second)
		if now.Before(graceEnd) {
			return v.authorizeEntitlement(operation)
		}
		policy = v.Payload.GraceEndPolicy
	}
	if policy == ExpiryPolicyContinueExisting {
		return v.authorizeEntitlement(operation)
	}
	return ErrPolicyDenied
}

func (v Verified) authorizeEntitlement(operation Operation) error {
	switch operation {
	case OperationNewDevice, OperationNewMember, OperationExistingHeartbeat, OperationExistingConnection, OperationExistingP2P:
		return nil
	case OperationNewDeployment:
		if !v.Payload.Entitlements.PrivateDeployment {
			return ErrPolicyDenied
		}
	case OperationNewRollout:
		if !v.Payload.Entitlements.Rollout {
			return ErrPolicyDenied
		}
	case OperationOfflineUpdate:
		if !v.Payload.Entitlements.OfflineUpdates {
			return ErrPolicyDenied
		}
	case OperationExistingRelay:
		if !v.Payload.Entitlements.Relay {
			return ErrPolicyDenied
		}
	default:
		return ErrPolicyDenied
	}
	return nil
}

func isNewOperation(operation Operation) bool {
	switch operation {
	case OperationNewDevice, OperationNewMember, OperationNewDeployment, OperationNewRollout, OperationOfflineUpdate:
		return true
	default:
		return false
	}
}

func (v Verified) Summary(now time.Time) Summary {
	now = now.UTC()
	state := "active"
	var graceEnd *time.Time
	if !now.Before(v.ExpiresAt) {
		state = "expired"
		if v.Payload.ExpiryPolicy == ExpiryPolicyGracePeriod {
			end := v.ExpiresAt.Add(time.Duration(v.Payload.GracePeriodSeconds) * time.Second)
			graceEnd = &end
			if now.Before(end) {
				state = "grace_period"
			}
		}
	}
	return Summary{
		LicenseID: v.Payload.LicenseID, KeyID: v.Payload.KeyID,
		OrganizationID: v.Payload.OrganizationID, DeploymentID: v.Payload.DeploymentID,
		NotBefore: v.NotBefore, ExpiresAt: v.ExpiresAt, ExpiryPolicy: v.Payload.ExpiryPolicy,
		GraceEndsAt: graceEnd, GraceEndPolicy: v.Payload.GraceEndPolicy, State: state,
		Entitlements: v.Payload.Entitlements, Support: v.Payload.Support,
	}
}

type ManagerConfig struct {
	Path        string
	TrustedKeys TrustedKeys
	Binding     Binding
	Now         func() time.Time
}

type Manager struct {
	mu          sync.RWMutex
	path        string
	trustedKeys TrustedKeys
	binding     Binding
	now         func() time.Time
	current     *Verified
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, fmt.Errorf("license path is required: %w", ErrInvalidDocument)
	}
	if len(cfg.TrustedKeys) == 0 {
		return nil, fmt.Errorf("trusted public keys are required: %w", ErrInvalidDocument)
	}
	if !stableIDPattern.MatchString(cfg.Binding.OrganizationID) || !stableIDPattern.MatchString(cfg.Binding.DeploymentID) ||
		cfg.Binding.OrganizationID != strings.TrimSpace(cfg.Binding.OrganizationID) || cfg.Binding.DeploymentID != strings.TrimSpace(cfg.Binding.DeploymentID) {
		return nil, fmt.Errorf("private license binding is invalid: %w", ErrInvalidDocument)
	}
	manager := &Manager{
		path: filepath.Clean(cfg.Path), trustedKeys: cloneTrustedKeys(cfg.TrustedKeys), binding: cfg.Binding,
		now: func() time.Time { return time.Now().UTC() },
	}
	if cfg.Now != nil {
		manager.now = cfg.Now
	}
	if err := manager.reload(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Import(raw []byte) (Summary, error) {
	verified, err := Verify(raw, m.trustedKeys, m.binding, m.now())
	if err != nil {
		return Summary{}, err
	}
	if err := writeAtomic(m.path, raw, 0o600); err != nil {
		return Summary{}, err
	}
	m.mu.Lock()
	m.current = &verified
	m.mu.Unlock()
	return verified.Summary(m.now()), nil
}

func (m *Manager) Current() (Verified, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return Verified{}, ErrUnavailable
	}
	return *m.current, nil
}

func (m *Manager) Binding() Binding {
	return m.binding
}

func (m *Manager) Summary() (Summary, error) {
	current, err := m.Current()
	if err != nil {
		return Summary{}, err
	}
	return current.Summary(m.now()), nil
}

func (m *Manager) reload() error {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	verified, err := VerifyAllowExpired(raw, m.trustedKeys, m.binding, m.now())
	if err != nil {
		return err
	}
	m.current = &verified
	return nil
}

func cloneTrustedKeys(keys TrustedKeys) TrustedKeys {
	out := make(TrustedKeys, len(keys))
	for id, key := range keys {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out[id] = append(ed25519.PublicKey(nil), key...)
	}
	return out
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".private-license-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		_ = tmp.Close()
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	remove = false
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}
