package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"meshlink/internal/licensing"
)

const OfflineManifestSchema = "meshlink-private-offline-update-v1"

var (
	ErrOfflineManifest = errors.New("offline update manifest is invalid")
	ErrOfflineBinding  = errors.New("offline update binding mismatch")
	ErrOfflineArtifact = errors.New("offline update artifact is invalid")
	ErrOfflineOrder    = errors.New("offline update ordering is invalid")
	ErrOfflineExpired  = errors.New("offline update manifest is expired")
	ErrOfflineLicense  = errors.New("offline update is not authorized by the private license")
)

type OfflineManifest struct {
	UpdateID        string `json:"update_id"`
	KeyID           string `json:"key_id"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version"`
	Sequence        uint64 `json:"sequence"`
	Platform        string `json:"platform"`
	Architecture    string `json:"architecture"`
	ArtifactFile    string `json:"artifact_file"`
	ArtifactSHA256  string `json:"artifact_sha256"`
	ArtifactSize    int64  `json:"artifact_size"`
	DeploymentID    string `json:"deployment_id"`
	LicenseID       string `json:"license_id"`
	ExpiresAt       string `json:"expires_at"`
}

type signedOfflineManifest struct {
	Schema    string          `json:"schema"`
	KeyID     string          `json:"key_id"`
	Manifest  json.RawMessage `json:"manifest"`
	Signature string          `json:"signature"`
}

type OfflineVerifyOptions struct {
	SignedManifest  []byte
	ArtifactPath    string
	TrustedKeys     licensing.TrustedKeys
	License         licensing.Verified
	Platform        string
	Architecture    string
	CurrentVersion  string
	CurrentSequence uint64
	Now             time.Time
	Audit           func(OfflineAuditRecord)
}

type OfflineAuditRecord struct {
	UpdateID     string    `json:"update_id,omitempty"`
	LicenseID    string    `json:"license_id,omitempty"`
	DeploymentID string    `json:"deployment_id,omitempty"`
	Action       string    `json:"action"`
	Result       string    `json:"result"`
	Version      string    `json:"version,omitempty"`
	ErrorKind    string    `json:"error_kind,omitempty"`
	Time         time.Time `json:"time"`
}

type OfflineVerifiedPackage struct {
	Manifest     OfflineManifest `json:"manifest"`
	ArtifactPath string          `json:"artifact_path"`
}

type OfflineStageResult struct {
	Manifest             OfflineManifest `json:"manifest"`
	StagedPath           string          `json:"staged_path"`
	CurrentVersion       string          `json:"current_version"`
	LastKnownGoodVersion string          `json:"last_known_good_version"`
	CurrentSequence      uint64          `json:"current_sequence"`
}

var offlineIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var offlineVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func SignOfflineManifest(manifest OfflineManifest, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key size is invalid: %w", ErrOfflineManifest)
	}
	canonical, _, err := canonicalOfflineManifest(manifest)
	if err != nil {
		return nil, err
	}
	document := signedOfflineManifest{
		Schema: OfflineManifestSchema, KeyID: manifest.KeyID, Manifest: canonical,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, offlineSignatureMessage(manifest.KeyID, canonical))),
	}
	return json.Marshal(document)
}

func VerifyOfflinePackage(opts OfflineVerifyOptions) (OfflineVerifiedPackage, error) {
	manifest, expiresAt, err := verifyOfflineManifestSignature(opts.SignedManifest, opts.TrustedKeys)
	if err != nil {
		emitOfflineAudit(opts, OfflineAuditRecord{Action: "offline_update_rejected", Result: "rejected", ErrorKind: offlineErrorKind(err)})
		return OfflineVerifiedPackage{}, err
	}
	rejected := func(err error) (OfflineVerifiedPackage, error) {
		emitOfflineAudit(opts, offlineAuditForManifest(manifest, "offline_update_rejected", "rejected", offlineErrorKind(err)))
		return OfflineVerifiedPackage{}, err
	}
	now := opts.Now.UTC()
	if !now.Before(expiresAt) {
		return rejected(ErrOfflineExpired)
	}
	if manifest.Platform != strings.TrimSpace(opts.Platform) || manifest.Architecture != strings.TrimSpace(opts.Architecture) ||
		manifest.DeploymentID != opts.License.Payload.DeploymentID || manifest.LicenseID != opts.License.Payload.LicenseID {
		return rejected(ErrOfflineBinding)
	}
	if err := opts.License.Authorize(licensing.OperationOfflineUpdate, now); err != nil {
		return rejected(fmt.Errorf("%w: %v", ErrOfflineLicense, err))
	}
	currentVersion := strings.TrimSpace(opts.CurrentVersion)
	if !offlineVersionPattern.MatchString(currentVersion) || manifest.PreviousVersion != currentVersion ||
		CompareVersions(manifest.Version, currentVersion) <= 0 || manifest.Sequence != opts.CurrentSequence+1 {
		return rejected(ErrOfflineOrder)
	}
	artifactPath := filepath.Clean(opts.ArtifactPath)
	if filepath.Base(artifactPath) != manifest.ArtifactFile {
		return rejected(ErrOfflineArtifact)
	}
	if err := verifyOfflineArtifact(artifactPath, manifest); err != nil {
		return rejected(err)
	}
	result := OfflineVerifiedPackage{Manifest: manifest, ArtifactPath: artifactPath}
	emitOfflineAudit(opts, offlineAuditForManifest(manifest, "offline_update_verified", "verified", ""))
	return result, nil
}

func StageOfflinePackage(opts OfflineVerifyOptions, stageDir string) (result OfflineStageResult, resultErr error) {
	verified, err := VerifyOfflinePackage(opts)
	if err != nil {
		return OfflineStageResult{}, err
	}
	defer func() {
		if resultErr == nil {
			return
		}
		errorKind := offlineErrorKind(resultErr)
		if errorKind == "manifest_invalid" {
			errorKind = "stage_failed"
		}
		emitOfflineAudit(opts, offlineAuditForManifest(verified.Manifest, "offline_update_rejected", "rejected", errorKind))
	}()
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return OfflineStageResult{}, err
	}
	destination := filepath.Join(stageDir, verified.Manifest.ArtifactFile)
	tmp, err := os.CreateTemp(stageDir, ".offline-update-*.tmp")
	if err != nil {
		return OfflineStageResult{}, err
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		_ = tmp.Close()
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	input, err := os.Open(verified.ArtifactPath)
	if err != nil {
		return OfflineStageResult{}, err
	}
	_, copyErr := io.Copy(tmp, input)
	closeInputErr := input.Close()
	if copyErr != nil {
		return OfflineStageResult{}, copyErr
	}
	if closeInputErr != nil {
		return OfflineStageResult{}, closeInputErr
	}
	if err := tmp.Chmod(0o600); err != nil {
		return OfflineStageResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		return OfflineStageResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return OfflineStageResult{}, err
	}
	if err := verifyOfflineArtifact(tmpPath, verified.Manifest); err != nil {
		return OfflineStageResult{}, err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return OfflineStageResult{}, err
	}
	remove = false
	_ = os.Chmod(destination, 0o600)
	result = OfflineStageResult{
		Manifest: verified.Manifest, StagedPath: destination,
		CurrentVersion: strings.TrimSpace(opts.CurrentVersion), LastKnownGoodVersion: strings.TrimSpace(opts.CurrentVersion),
		CurrentSequence: opts.CurrentSequence,
	}
	emitOfflineAudit(opts, offlineAuditForManifest(verified.Manifest, "offline_update_staged", "staged", ""))
	return result, nil
}

func offlineAuditForManifest(manifest OfflineManifest, action, result, errorKind string) OfflineAuditRecord {
	return OfflineAuditRecord{
		UpdateID: manifest.UpdateID, LicenseID: manifest.LicenseID, DeploymentID: manifest.DeploymentID,
		Action: action, Result: result, Version: manifest.Version, ErrorKind: errorKind,
	}
}

func emitOfflineAudit(opts OfflineVerifyOptions, record OfflineAuditRecord) {
	if opts.Audit == nil {
		return
	}
	record.Time = opts.Now.UTC()
	opts.Audit(record)
}

func offlineErrorKind(err error) string {
	switch {
	case errors.Is(err, licensing.ErrUnknownKey):
		return "unknown_key"
	case errors.Is(err, licensing.ErrSignatureInvalid):
		return "signature_invalid"
	case errors.Is(err, ErrOfflineBinding):
		return "binding_mismatch"
	case errors.Is(err, ErrOfflineArtifact):
		return "artifact_invalid"
	case errors.Is(err, ErrOfflineOrder):
		return "ordering_invalid"
	case errors.Is(err, ErrOfflineExpired):
		return "expired"
	case errors.Is(err, ErrOfflineLicense):
		return "license_denied"
	default:
		return "manifest_invalid"
	}
}

func verifyOfflineManifestSignature(raw []byte, keys licensing.TrustedKeys) (OfflineManifest, time.Time, error) {
	var document signedOfflineManifest
	if err := decodeOfflineStrict(raw, &document); err != nil {
		return OfflineManifest{}, time.Time{}, fmt.Errorf("%w: %v", ErrOfflineManifest, err)
	}
	if document.Schema != OfflineManifestSchema || !offlineIDPattern.MatchString(document.KeyID) || len(document.Manifest) == 0 {
		return OfflineManifest{}, time.Time{}, ErrOfflineManifest
	}
	publicKey, ok := keys[document.KeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return OfflineManifest{}, time.Time{}, licensing.ErrUnknownKey
	}
	signature, err := base64.RawURLEncoding.DecodeString(document.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, offlineSignatureMessage(document.KeyID, document.Manifest), signature) {
		return OfflineManifest{}, time.Time{}, licensing.ErrSignatureInvalid
	}
	var manifest OfflineManifest
	if err := decodeOfflineStrict(document.Manifest, &manifest); err != nil {
		return OfflineManifest{}, time.Time{}, fmt.Errorf("%w: %v", ErrOfflineManifest, err)
	}
	canonical, expiresAt, err := canonicalOfflineManifest(manifest)
	if err != nil {
		return OfflineManifest{}, time.Time{}, err
	}
	if manifest.KeyID != document.KeyID || !bytes.Equal(canonical, document.Manifest) {
		return OfflineManifest{}, time.Time{}, ErrOfflineManifest
	}
	return manifest, expiresAt, nil
}

func canonicalOfflineManifest(manifest OfflineManifest) ([]byte, time.Time, error) {
	for name, value := range map[string]string{
		"update_id": manifest.UpdateID, "key_id": manifest.KeyID, "deployment_id": manifest.DeploymentID, "license_id": manifest.LicenseID,
	} {
		if !offlineIDPattern.MatchString(value) {
			return nil, time.Time{}, fmt.Errorf("%s is invalid: %w", name, ErrOfflineManifest)
		}
	}
	if !offlineVersionPattern.MatchString(manifest.Version) || !offlineVersionPattern.MatchString(manifest.PreviousVersion) || manifest.Sequence == 0 {
		return nil, time.Time{}, ErrOfflineManifest
	}
	if manifest.Platform == "" || manifest.Platform != strings.ToLower(strings.TrimSpace(manifest.Platform)) ||
		manifest.Architecture == "" || manifest.Architecture != strings.ToLower(strings.TrimSpace(manifest.Architecture)) {
		return nil, time.Time{}, ErrOfflineManifest
	}
	if filepath.Base(manifest.ArtifactFile) != manifest.ArtifactFile || !strings.HasSuffix(strings.ToLower(manifest.ArtifactFile), ".zip") ||
		!sha256Pattern.MatchString(manifest.ArtifactSHA256) || manifest.ArtifactSize <= 0 {
		return nil, time.Time{}, ErrOfflineManifest
	}
	expectedArtifactFile := fmt.Sprintf("meshlink-private-%s-%s-%s.zip", manifest.Version, manifest.Platform, manifest.Architecture)
	if manifest.ArtifactFile != expectedArtifactFile {
		return nil, time.Time{}, ErrOfflineManifest
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, manifest.ExpiresAt)
	if err != nil || manifest.ExpiresAt != expiresAt.UTC().Format(time.RFC3339Nano) {
		return nil, time.Time{}, ErrOfflineManifest
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return nil, time.Time{}, err
	}
	return canonical, expiresAt.UTC(), nil
}

func verifyOfflineArtifact(path string, manifest OfflineManifest) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOfflineArtifact, err)
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(hasher, f)
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("%w: %v", ErrOfflineArtifact, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: %v", ErrOfflineArtifact, closeErr)
	}
	if size != manifest.ArtifactSize || hex.EncodeToString(hasher.Sum(nil)) != manifest.ArtifactSHA256 {
		return ErrOfflineArtifact
	}
	if err := validateZip(path); err != nil {
		return fmt.Errorf("%w: %v", ErrOfflineArtifact, err)
	}
	return nil
}

func offlineSignatureMessage(keyID string, manifest []byte) []byte {
	message := make([]byte, 0, len(keyID)+len(manifest)+64)
	message = append(message, "meshlink-signed-document\x00"...)
	message = append(message, OfflineManifestSchema...)
	message = append(message, 0)
	message = append(message, keyID...)
	message = append(message, 0)
	message = append(message, manifest...)
	return message
}

func decodeOfflineStrict(raw []byte, target any) error {
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
