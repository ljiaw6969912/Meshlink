package licensing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLicenseSignatureBindingRotationAndTamperRejection(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	pubOld, privOld, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubNew, privNew, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := TrustedKeys{"license-old": pubOld, "license-new": pubNew}
	expected := Binding{OrganizationID: "org-private", DeploymentID: "deployment-private"}

	for _, tc := range []struct {
		name string
		key  string
		priv ed25519.PrivateKey
	}{
		{name: "old key remains trusted during rotation", key: "license-old", priv: privOld},
		{name: "new key is accepted", key: "license-new", priv: privNew},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := validPayload(now)
			payload.KeyID = tc.key
			raw, err := Sign(payload, tc.priv)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := Verify(raw, keys, expected, now)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if verified.Payload.LicenseID != payload.LicenseID || verified.Payload.KeyID != tc.key {
				t.Fatalf("verified payload = %+v", verified.Payload)
			}
		})
	}

	raw, err := Sign(validPayload(now), privOld)
	if err != nil {
		t.Fatal(err)
	}
	var doc SignedDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var tamperedPayload Payload
	if err := json.Unmarshal(doc.Payload, &tamperedPayload); err != nil {
		t.Fatal(err)
	}
	tamperedPayload.Customer = "Tampered Customer"
	doc.Payload, err = json.Marshal(tamperedPayload)
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(tampered, keys, expected, now); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("tampered Verify error = %v, want ErrSignatureInvalid", err)
	}

	unknown := validPayload(now)
	unknown.KeyID = "license-unknown"
	raw, err = Sign(unknown, privNew)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(raw, keys, expected, now); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key Verify error = %v, want ErrUnknownKey", err)
	}

	for _, tc := range []struct {
		name    string
		binding Binding
	}{
		{name: "wrong organization", binding: Binding{OrganizationID: "org-other", DeploymentID: expected.DeploymentID}},
		{name: "wrong deployment", binding: Binding{OrganizationID: expected.OrganizationID, DeploymentID: "deployment-other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Sign(validPayload(now), privOld)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(raw, keys, tc.binding, now); !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("Verify error = %v, want ErrBindingMismatch", err)
			}
		})
	}
}

func TestLicenseTimeBoundariesAndExpiryPolicies(t *testing.T) {
	base := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := TrustedKeys{"license-old": pub}
	expected := Binding{OrganizationID: "org-private", DeploymentID: "deployment-private"}
	payload := validPayload(base)
	raw, err := Sign(payload, priv)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Verify(raw, keys, expected, base.Add(-time.Nanosecond)); !errors.Is(err, ErrNotEffective) {
		t.Fatalf("before not_before error = %v, want ErrNotEffective", err)
	}
	verified, err := Verify(raw, keys, expected, base)
	if err != nil {
		t.Fatalf("at not_before: %v", err)
	}
	expires := base.Add(time.Hour)
	if _, err := Verify(raw, keys, expected, expires.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("before expires_at: %v", err)
	}
	if _, err := Verify(raw, keys, expected, expires); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expires_at error = %v, want ErrExpired", err)
	}

	if err := verified.Authorize(OperationNewDevice, expires); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("new device after expiry = %v, want denied", err)
	}
	if err := verified.Authorize(OperationExistingConnection, expires); err != nil {
		t.Fatalf("continue_existing existing connection = %v", err)
	}
	if err := verified.Authorize(Operation("unknown_operation"), base); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("unknown operation = %v, want denied", err)
	}

	grace := validPayload(base)
	grace.ExpiryPolicy = ExpiryPolicyGracePeriod
	grace.GracePeriodSeconds = 600
	grace.GraceEndPolicy = ExpiryPolicyDenyAll
	raw, err = Sign(grace, priv)
	if err != nil {
		t.Fatal(err)
	}
	verified, err = VerifyAllowExpired(raw, keys, expected, expires.Add(time.Minute))
	if err != nil {
		t.Fatalf("VerifyAllowExpired in grace: %v", err)
	}
	if err := verified.Authorize(OperationExistingHeartbeat, expires.Add(599*time.Second)); err != nil {
		t.Fatalf("heartbeat inside grace = %v", err)
	}
	if err := verified.Authorize(OperationExistingHeartbeat, expires.Add(600*time.Second)); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("heartbeat at grace end = %v, want denied", err)
	}
}

func TestLicenseManagerAtomicImportRetainsCurrentAndReloads(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-license.json")
	cfg := ManagerConfig{
		Path:        path,
		TrustedKeys: TrustedKeys{"license-old": pub},
		Binding:     Binding{OrganizationID: "org-private", DeploymentID: "deployment-private"},
		Now:         func() time.Time { return now },
	}
	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := Sign(validPayload(now), priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Import(valid); err != nil {
		t.Fatalf("Import valid: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, valid) {
		t.Fatal("persisted license differs from imported signed document")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("license mode = %o, want no group/other permissions", info.Mode().Perm())
	}

	invalid := append([]byte(nil), valid...)
	invalid[len(invalid)/2] ^= 1
	if _, err := manager.Import(invalid); err == nil {
		t.Fatal("Import tampered license succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("invalid import overwrote current license")
	}

	reloaded, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager reload: %v", err)
	}
	summary, err := reloaded.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.LicenseID != "lic-private-001" || summary.Signature != "" || summary.RawPayload != "" {
		t.Fatalf("public summary = %+v", summary)
	}
}

func TestTrustedKeyFileRotationAndMissingEntitlementRejection(t *testing.T) {
	publicOld, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicNew, privateNew, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trusted-license-keys.json")
	config := TrustedKeyFile{Keys: []TrustedKeyEntry{
		{KeyID: "license-old", PublicKey: encodePublicKey(publicOld)},
		{KeyID: "license-new", PublicKey: encodePublicKey(publicNew)},
	}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadTrustedKeysFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !bytes.Equal(keys["license-new"], publicNew) {
		t.Fatalf("keys = %+v", keys)
	}

	payload := validPayload(time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC))
	payload.KeyID = "license-new"
	payload.Entitlements.DeviceCount = 0
	if _, err := Sign(payload, privateNew); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("missing entitlement Sign error = %v, want ErrInvalidDocument", err)
	}
}

func validPayload(now time.Time) Payload {
	return Payload{
		LicenseID:      "lic-private-001",
		KeyID:          "license-old",
		Customer:       "Example Customer",
		OrganizationID: "org-private",
		DeploymentID:   "deployment-private",
		IssuedAt:       now.Add(-time.Minute).Format(time.RFC3339Nano),
		NotBefore:      now.Format(time.RFC3339Nano),
		ExpiresAt:      now.Add(time.Hour).Format(time.RFC3339Nano),
		ExpiryPolicy:   ExpiryPolicyContinueExisting,
		Entitlements: Entitlements{
			DeviceCount: 25, MemberCount: 8, ConcurrentOnlineDevices: 10,
			RelayBytesPerMonth: 1 << 30, ActiveRelaySessions: 4,
			AuditRetentionDays: 90, DeploymentCount: 3,
			PrivateDeployment: true, Relay: true, Rollout: true, OfflineUpdates: true,
		},
		Support: Support{ID: "support-standard", Contact: "support@example.invalid"},
	}
}
