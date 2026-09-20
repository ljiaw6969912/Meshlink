package update

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/licensing"
)

func TestOfflineUpdateVerificationRejectsInvalidManifestAndOrdering(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	licensePublic, licensePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifiedLicense := testOfflineLicense(t, now, licensePublic, licensePrivate)
	artifact := filepath.Join(t.TempDir(), "meshlink-private-0.2.0-windows-amd64.zip")
	writeTestZip(t, artifact, "mesh-cloudhub.exe", []byte("private hub artifact"))
	info, err := os.Stat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	sum := fileSHA256(t, artifact)
	manifest := OfflineManifest{
		UpdateID: "offline-update-002", KeyID: "update-2026", Version: "0.2.0", PreviousVersion: "0.1.0",
		Sequence: 2, Platform: "windows", Architecture: "amd64", ArtifactFile: filepath.Base(artifact),
		ArtifactSHA256: sum, ArtifactSize: info.Size(), DeploymentID: "deployment-private", LicenseID: "lic-private-001",
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
	}
	signed, err := SignOfflineManifest(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	base := OfflineVerifyOptions{
		SignedManifest: signed, ArtifactPath: artifact, TrustedKeys: licensing.TrustedKeys{"update-2026": publicKey},
		License: verifiedLicense, Platform: "windows", Architecture: "amd64", CurrentVersion: "0.1.0", CurrentSequence: 1, Now: now,
	}
	if got, err := VerifyOfflinePackage(base); err != nil || got.Manifest.UpdateID != manifest.UpdateID {
		t.Fatalf("VerifyOfflinePackage = %+v, %v", got, err)
	}
	var tampered signedOfflineManifest
	if err := json.Unmarshal(signed, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Manifest = []byte(strings.Replace(string(tampered.Manifest), `"version":"0.2.0"`, `"version":"0.2.1"`, 1))
	tamperedRaw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	tamperedOpts := base
	tamperedOpts.SignedManifest = tamperedRaw
	if _, err := VerifyOfflinePackage(tamperedOpts); !errors.Is(err, licensing.ErrSignatureInvalid) {
		t.Fatalf("tampered signed manifest error = %v", err)
	}
	unauthorized := base
	unauthorized.License.Payload.Entitlements.OfflineUpdates = false
	if _, err := VerifyOfflinePackage(unauthorized); !errors.Is(err, ErrOfflineLicense) {
		t.Fatalf("license without offline entitlement error = %v", err)
	}

	mutate := func(change func(*OfflineManifest)) []byte {
		t.Helper()
		changed := manifest
		change(&changed)
		changed.ArtifactFile = "meshlink-private-" + changed.Version + "-" + changed.Platform + "-" + changed.Architecture + ".zip"
		raw, signErr := SignOfflineManifest(changed, privateKey)
		if signErr != nil {
			t.Fatal(signErr)
		}
		return raw
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		err  error
	}{
		{name: "platform", raw: mutate(func(m *OfflineManifest) { m.Platform = "linux" }), err: ErrOfflineBinding},
		{name: "architecture", raw: mutate(func(m *OfflineManifest) { m.Architecture = "arm64" }), err: ErrOfflineBinding},
		{name: "deployment", raw: mutate(func(m *OfflineManifest) { m.DeploymentID = "deployment-other" }), err: ErrOfflineBinding},
		{name: "license", raw: mutate(func(m *OfflineManifest) { m.LicenseID = "lic-other" }), err: ErrOfflineBinding},
		{name: "downgrade", raw: mutate(func(m *OfflineManifest) { m.Version = "0.0.9" }), err: ErrOfflineOrder},
		{name: "previous version", raw: mutate(func(m *OfflineManifest) { m.PreviousVersion = "0.0.9" }), err: ErrOfflineOrder},
		{name: "sequence", raw: mutate(func(m *OfflineManifest) { m.Sequence = 3 }), err: ErrOfflineOrder},
		{name: "expired", raw: mutate(func(m *OfflineManifest) { m.ExpiresAt = now.Format(time.RFC3339Nano) }), err: ErrOfflineExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			opts.SignedManifest = tc.raw
			if _, err := VerifyOfflinePackage(opts); !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
		})
	}

	tamperedArtifact := filepath.Join(filepath.Dir(artifact), filepath.Base(artifact))
	if err := os.WriteFile(tamperedArtifact, []byte("not the signed zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyOfflinePackage(base); !errors.Is(err, ErrOfflineArtifact) {
		t.Fatalf("tampered artifact error = %v, want ErrOfflineArtifact", err)
	}

	unknown := base
	unknown.TrustedKeys = licensing.TrustedKeys{}
	if _, err := VerifyOfflinePackage(unknown); !errors.Is(err, licensing.ErrUnknownKey) {
		t.Fatalf("unknown key error = %v", err)
	}
}

func TestOfflineUpdateStageIsAtomicAndKeepsKnownGoodOnFailure(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	updatePublic, updatePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	licensePublic, licensePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifiedLicense := testOfflineLicense(t, now, licensePublic, licensePrivate)
	dir := t.TempDir()
	artifact := filepath.Join(dir, "meshlink-private-0.2.0-windows-amd64.zip")
	writeTestZip(t, artifact, "mesh-cloudhub.exe", []byte("valid artifact"))
	info, _ := os.Stat(artifact)
	manifest := OfflineManifest{
		UpdateID: "offline-update-002", KeyID: "update-2026", Version: "0.2.0", PreviousVersion: "0.1.0", Sequence: 2,
		Platform: "windows", Architecture: "amd64", ArtifactFile: filepath.Base(artifact), ArtifactSHA256: fileSHA256(t, artifact), ArtifactSize: info.Size(),
		DeploymentID: "deployment-private", LicenseID: "lic-private-001", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
	}
	signed, err := SignOfflineManifest(manifest, updatePrivate)
	if err != nil {
		t.Fatal(err)
	}
	var audit []OfflineAuditRecord
	opts := OfflineVerifyOptions{SignedManifest: signed, ArtifactPath: artifact, TrustedKeys: licensing.TrustedKeys{"update-2026": updatePublic}, License: verifiedLicense,
		Audit:    func(record OfflineAuditRecord) { audit = append(audit, record) },
		Platform: "windows", Architecture: "amd64", CurrentVersion: "0.1.0", CurrentSequence: 1, Now: now}
	stageDir := filepath.Join(dir, "staged")
	result, err := StageOfflinePackage(opts, stageDir)
	if err != nil {
		t.Fatalf("StageOfflinePackage: %v", err)
	}
	before, err := os.ReadFile(result.StagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.CurrentVersion != "0.1.0" || result.LastKnownGoodVersion != "0.1.0" {
		t.Fatalf("stage result = %+v", result)
	}
	if len(audit) != 2 || audit[0].Action != "offline_update_verified" || audit[1].Action != "offline_update_staged" ||
		audit[1].UpdateID != manifest.UpdateID || audit[1].DeploymentID != manifest.DeploymentID || audit[1].LicenseID != manifest.LicenseID {
		t.Fatalf("successful offline audit = %+v", audit)
	}

	if err := os.WriteFile(artifact, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StageOfflinePackage(opts, stageDir); !errors.Is(err, ErrOfflineArtifact) {
		t.Fatalf("invalid stage error = %v", err)
	}
	if got := audit[len(audit)-1]; got.Action != "offline_update_rejected" || got.Result != "rejected" || got.ErrorKind != "artifact_invalid" {
		t.Fatalf("rejected offline audit = %+v", got)
	}
	after, err := os.ReadFile(result.StagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed staging overwrote the existing verified package")
	}
}

func TestOfflineManifestRejectsArtifactFilenameVersionMismatch(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := OfflineManifest{
		UpdateID: "offline-update-002", KeyID: "update-2026", Version: "0.2.0", PreviousVersion: "0.1.5",
		Sequence: 2, Platform: "windows", Architecture: "amd64", ArtifactFile: "meshlink-private-0.2.1-windows-amd64.zip",
		ArtifactSHA256: strings.Repeat("a", 64), ArtifactSize: 123, DeploymentID: "deployment-private", LicenseID: "lic-private-001",
		ExpiresAt: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
	if _, err := SignOfflineManifest(manifest, privateKey); !errors.Is(err, ErrOfflineManifest) {
		t.Fatalf("SignOfflineManifest error = %v, want ErrOfflineManifest", err)
	}
}

func testOfflineLicense(t *testing.T, now time.Time, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey) licensing.Verified {
	t.Helper()
	payload := licensing.Payload{
		LicenseID: "lic-private-001", KeyID: "license-2026", Customer: "Example Customer", OrganizationID: "org-private", DeploymentID: "deployment-private",
		IssuedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		ExpiryPolicy: licensing.ExpiryPolicyContinueExisting,
		Entitlements: licensing.Entitlements{DeviceCount: 25, MemberCount: 8, ConcurrentOnlineDevices: 10, RelayBytesPerMonth: 1 << 30, ActiveRelaySessions: 4,
			AuditRetentionDays: 90, DeploymentCount: 3, PrivateDeployment: true, Relay: true, Rollout: true, OfflineUpdates: true},
		Support: licensing.Support{ID: "support-standard", Contact: "support@example.invalid"},
	}
	raw, err := licensing.Sign(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := licensing.Verify(raw, licensing.TrustedKeys{"license-2026": publicKey}, licensing.Binding{OrganizationID: "org-private", DeploymentID: "deployment-private"}, now)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func writeTestZip(t *testing.T, path, name string, data []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
