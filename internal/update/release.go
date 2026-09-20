package update

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var releaseVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var certificateThumbprintPattern = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)

var requiredUpdateFiles = []string{
	"VERSION",
	"bin/mesh-agent.exe",
	"bin/mesh-cloudhub.exe",
	"bin/mesh-desktop.exe",
	"bin/mesh-update-server.exe",
	"bin/meshctl.exe",
}

type packageManifest struct {
	Schema    string            `json:"schema"`
	Product   string            `json:"product"`
	Version   string            `json:"version"`
	Mode      string            `json:"mode"`
	BuildTime string            `json:"build_time"`
	Files     []packageFileInfo `json:"files"`
	Signing   SigningInfo       `json:"signing"`
}

type packageFileInfo struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func ValidateManifest(manifest Manifest) error {
	manifest.Version = strings.TrimSpace(manifest.Version)
	if !releaseVersionPattern.MatchString(manifest.Version) {
		return errors.New("manifest version is invalid")
	}
	expectedFile := "meshlink-" + manifest.Version + ".zip"
	if manifest.Package.File != expectedFile {
		return fmt.Errorf("manifest version %q does not match package file %q; expected %q", manifest.Version, manifest.Package.File, expectedFile)
	}
	wantHash := strings.TrimSpace(manifest.Package.SHA256)
	if len(wantHash) != sha256.Size*2 {
		return errors.New("manifest package sha256 is invalid")
	}
	if _, err := hex.DecodeString(wantHash); err != nil {
		return errors.New("manifest package sha256 is invalid")
	}
	if manifest.Package.Size <= 0 {
		return errors.New("manifest package size is invalid")
	}
	if manifest.GeneratedAt.IsZero() {
		return errors.New("manifest generated_at is invalid")
	}

	if manifest.Schema == "" {
		if manifest.Mode != "" || manifest.Signing.CodeSigned || manifest.Signing.CertificateThumbprint != "" {
			return errors.New("legacy manifest cannot declare release signing metadata")
		}
		return nil
	}
	if manifest.Schema != ReleaseManifestSchema {
		return fmt.Errorf("unsupported release manifest schema %q", manifest.Schema)
	}
	switch manifest.Mode {
	case DevelopmentMode:
		if manifest.Signing.CodeSigned || strings.TrimSpace(manifest.Signing.CertificateThumbprint) != "" {
			return errors.New("development manifest must be explicitly unsigned")
		}
	case ReleaseMode:
		if !manifest.Signing.CodeSigned {
			return errors.New("release manifest must be code signed")
		}
		if !certificateThumbprintPattern.MatchString(strings.TrimSpace(manifest.Signing.CertificateThumbprint)) {
			return errors.New("release manifest certificate thumbprint is invalid")
		}
	default:
		return fmt.Errorf("manifest mode %q is invalid", manifest.Mode)
	}
	return nil
}

func validateUpdatePackage(packagePath string, manifest Manifest) error {
	r, err := zip.OpenReader(packagePath)
	if err != nil {
		return fmt.Errorf("invalid update package: %w", err)
	}
	defer r.Close()

	entries := make(map[string]*zip.File, len(r.File))
	for _, file := range r.File {
		name, err := safeZipEntryName(file.Name)
		if err != nil {
			return err
		}
		key := strings.ToLower(name)
		if _, ok := entries[key]; ok {
			return fmt.Errorf("duplicate update package entry %q", name)
		}
		entries[key] = file
		if err := rejectCredentialEntry(file, name); err != nil {
			return err
		}
	}

	root, err := packageRoot(entries)
	if err != nil {
		return err
	}
	versionRaw, err := readZipEntry(entries[strings.ToLower(root+"/VERSION")], 256)
	if err != nil {
		return fmt.Errorf("read package VERSION: %w", err)
	}
	if strings.TrimSpace(string(versionRaw)) != manifest.Version {
		return fmt.Errorf("package VERSION does not match manifest version %q", manifest.Version)
	}
	for _, relative := range requiredUpdateFiles {
		if _, ok := entries[strings.ToLower(root+"/"+relative)]; !ok {
			return fmt.Errorf("required update package file is missing: %s", relative)
		}
	}

	if manifest.Schema == "" {
		return nil
	}
	manifestEntry, ok := entries[strings.ToLower(root+"/manifest.json")]
	if !ok {
		return errors.New("required update package file is missing: manifest.json")
	}
	manifestRaw, err := readZipEntry(manifestEntry, 2<<20)
	if err != nil {
		return fmt.Errorf("read package manifest: %w", err)
	}
	var packageDocument packageManifest
	if err := decodeReleaseStrict(manifestRaw, &packageDocument); err != nil {
		return fmt.Errorf("package manifest is invalid: %w", err)
	}
	if packageDocument.Schema != PackageManifestSchema || packageDocument.Version != manifest.Version || packageDocument.Mode != manifest.Mode {
		return errors.New("package manifest identity does not match release manifest")
	}
	if packageDocument.Signing.CodeSigned != manifest.Signing.CodeSigned ||
		!strings.EqualFold(strings.TrimSpace(packageDocument.Signing.CertificateThumbprint), strings.TrimSpace(manifest.Signing.CertificateThumbprint)) {
		return errors.New("package signature metadata does not match release manifest")
	}
	return verifyPackageFileManifest(entries, root, packageDocument.Files)
}

func verifyPackageFileManifest(entries map[string]*zip.File, root string, files []packageFileInfo) error {
	paths := make([]string, 0, len(files))
	covered := make(map[string]struct{}, len(files))
	for _, item := range files {
		path, err := safeRelativePackagePath(item.Path)
		if err != nil {
			return err
		}
		if len(item.SHA256) != sha256.Size*2 {
			return fmt.Errorf("package manifest sha256 is invalid: %s", path)
		}
		if _, err := hex.DecodeString(item.SHA256); err != nil || item.Size < 0 {
			return fmt.Errorf("package manifest hash or size is invalid: %s", path)
		}
		key := strings.ToLower(root + "/" + path)
		if _, exists := covered[key]; exists {
			return fmt.Errorf("package manifest contains duplicate file: %s", path)
		}
		entry, ok := entries[key]
		if !ok {
			return fmt.Errorf("package manifest references missing file: %s", path)
		}
		actual, err := hashZipEntry(entry)
		if err != nil {
			return err
		}
		if int64(entry.UncompressedSize64) != item.Size || !strings.EqualFold(actual, item.SHA256) {
			return fmt.Errorf("package manifest hash or size mismatch: %s", path)
		}
		covered[key] = struct{}{}
		paths = append(paths, path)
	}
	if !sort.StringsAreSorted(paths) {
		return errors.New("package manifest file list is not sorted")
	}
	for key, entry := range entries {
		if strings.HasSuffix(entry.Name, "/") || key == strings.ToLower(root+"/manifest.json") {
			continue
		}
		if _, ok := covered[key]; !ok {
			return fmt.Errorf("update package contains file not covered by manifest: %s", entry.Name)
		}
	}
	return nil
}

func packageRoot(entries map[string]*zip.File) (string, error) {
	root := ""
	for _, entry := range entries {
		name := filepath.ToSlash(entry.Name)
		if !strings.HasSuffix(strings.ToLower(name), "/version") || strings.Count(name, "/") != 1 {
			continue
		}
		candidate := name[:strings.IndexByte(name, '/')]
		if root != "" && !strings.EqualFold(root, candidate) {
			return "", errors.New("update package contains multiple roots")
		}
		root = candidate
	}
	if root == "" {
		return "", errors.New("required update package file is missing: VERSION")
	}
	if !strings.EqualFold(root, "meshlink") {
		return "", fmt.Errorf("update package root must be meshlink, got %q", root)
	}
	return root, nil
}

func safeZipEntryName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("unsafe update package entry %q", raw)
	}
	name := filepath.ToSlash(raw)
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", fmt.Errorf("unsafe update package entry %q", raw)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || part == "" {
			if part == "" && strings.HasSuffix(name, "/") {
				continue
			}
			return "", fmt.Errorf("unsafe update package entry %q", raw)
		}
	}
	return name, nil
}

func safeRelativePackagePath(raw string) (string, error) {
	name, err := safeZipEntryName(raw)
	if err != nil || strings.Contains(name, "/../") || strings.HasSuffix(name, "/") {
		return "", fmt.Errorf("unsafe package manifest path %q", raw)
	}
	if strings.Contains(name, "//") {
		return "", fmt.Errorf("unsafe package manifest path %q", raw)
	}
	return name, nil
}

func rejectCredentialEntry(file *zip.File, name string) error {
	lower := strings.ToLower(name)
	for _, suffix := range []string{".key", ".pem", ".p12", ".pfx", ".jks", ".keystore"} {
		if strings.HasSuffix(lower, suffix) {
			return fmt.Errorf("forbidden credential file in update package: %s", name)
		}
	}
	if !hasTextExtension(lower) || file.UncompressedSize64 > 4<<20 {
		return nil
	}
	raw, err := readZipEntry(file, 4<<20)
	if err != nil {
		return err
	}
	upper := bytes.ToUpper(raw)
	for _, marker := range [][]byte{[]byte("BEGIN PRIVATE KEY"), []byte("BEGIN RSA PRIVATE KEY"), []byte("BEGIN OPENSSH PRIVATE KEY"), []byte("BEGIN EC PRIVATE KEY")} {
		if bytes.Contains(upper, marker) {
			return fmt.Errorf("private key material found in update package: %s", name)
		}
	}
	credentialPattern := regexp.MustCompile(`(?i)["']?(password|private_key|client_secret|access_token)["']?\s*[:=]\s*["'][^"'<$%{][^"']+["']`)
	if credentialPattern.Match(raw) {
		return fmt.Errorf("credential-like value found in update package: %s", name)
	}
	return nil
}

func hasTextExtension(name string) bool {
	for _, suffix := range []string{".json", ".md", ".txt", ".ps1", ".bat", ".env", ".yml", ".yaml", ".config"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func readZipEntry(file *zip.File, limit int64) ([]byte, error) {
	if file == nil || int64(file.UncompressedSize64) > limit {
		return nil, errors.New("zip entry exceeds size limit")
	}
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, limit+1))
}

func hashZipEntry(file *zip.File) (string, error) {
	r, err := file.Open()
	if err != nil {
		return "", err
	}
	defer r.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func decodeReleaseStrict(raw []byte, target any) error {
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
