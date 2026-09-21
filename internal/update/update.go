package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListen         = "10.77.0.1:1263"
	DefaultServerURL      = "http://10.77.0.1:1263"
	ManifestFile          = "manifest.json"
	ReleaseManifestSchema = "meshlink-release-v1"
	PackageManifestSchema = "meshlink-package-v1"
	ReleaseMode           = "release"
	DevelopmentMode       = "development"
)

type Manifest struct {
	Schema      string      `json:"schema,omitempty"`
	Product     string      `json:"product"`
	Version     string      `json:"version"`
	Mode        string      `json:"mode,omitempty"`
	BuildTime   string      `json:"build_time,omitempty"`
	GeneratedAt time.Time   `json:"generated_at"`
	Package     PackageInfo `json:"package"`
	Signing     SigningInfo `json:"signing,omitempty"`
	Notes       []string    `json:"notes,omitempty"`
}

type PackageInfo struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type SigningInfo struct {
	CodeSigned            bool   `json:"code_signed"`
	CertificateThumbprint string `json:"certificate_thumbprint,omitempty"`
}

type CheckResult struct {
	CurrentVersion  string   `json:"current_version"`
	Manifest        Manifest `json:"manifest"`
	UpdateAvailable bool     `json:"update_available"`
}

type ApplyOptions struct {
	BaseDir       string
	PackagePath   string
	Version       string
	ServiceName   string
	WaitPID       int
	PackageSHA256 string
}

func Check(ctx context.Context, baseURL, currentVersion string) (CheckResult, error) {
	return CheckWithClient(ctx, http.DefaultClient, baseURL, currentVersion)
}

func CheckWithClient(ctx context.Context, client *http.Client, baseURL, currentVersion string) (CheckResult, error) {
	baseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return CheckResult{}, err
	}
	manifestURL, err := joinURL(baseURL, ManifestFile)
	if err != nil {
		return CheckResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return CheckResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CheckResult{}, fmt.Errorf("update server returned %s", resp.Status)
	}
	var manifest Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&manifest); err != nil {
		return CheckResult{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{
		CurrentVersion:  currentVersion,
		Manifest:        manifest,
		UpdateAvailable: CompareVersions(manifest.Version, currentVersion) > 0,
	}, nil
}

func DownloadPackage(ctx context.Context, baseURL string, manifest Manifest, destDir string) (string, error) {
	return DownloadPackageWithClient(ctx, http.DefaultClient, baseURL, manifest, destDir)
}

func DownloadPackageWithClient(ctx context.Context, client *http.Client, baseURL string, manifest Manifest, destDir string) (string, error) {
	if err := ValidateManifest(manifest); err != nil {
		return "", err
	}
	baseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", err
	}
	packageURL, err := joinURL(baseURL, manifest.Package.File)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, packageURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %s", resp.Status)
	}
	fileName := filepath.Base(manifest.Package.File)
	if fileName == "." || fileName == string(filepath.Separator) {
		return "", errors.New("invalid package file name")
	}
	outPath := filepath.Join(destDir, fileName)
	tmpPath := outPath + ".download"
	out, err := os.Create(tmpPath)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(resp.Body, manifest.Package.Size+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", closeErr
	}
	if manifest.Package.Size > 0 && written != manifest.Package.Size {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("download size mismatch: got %d, want %d", written, manifest.Package.Size)
	}
	sum := strings.ToLower(hex.EncodeToString(hasher.Sum(nil)))
	want := strings.ToLower(strings.TrimSpace(manifest.Package.SHA256))
	if want != "" && sum != want {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("sha256 mismatch: got %s, want %s", sum, want)
	}
	if err := validateUpdatePackage(tmpPath, manifest); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	_ = os.Remove(outPath)
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return outPath, nil
}

func WriteApplyScript(opts ApplyOptions) (string, error) {
	if strings.TrimSpace(opts.BaseDir) == "" {
		return "", errors.New("base dir is empty")
	}
	if strings.TrimSpace(opts.PackagePath) == "" {
		return "", errors.New("package path is empty")
	}
	var err error
	opts.BaseDir, err = filepath.Abs(opts.BaseDir)
	if err != nil {
		return "", err
	}
	opts.PackagePath, err = filepath.Abs(opts.PackagePath)
	if err != nil {
		return "", err
	}
	if opts.ServiceName == "" {
		opts.ServiceName = "MeshlinkAgent"
	}
	version := sanitizeVersion(opts.Version)
	if version == "" {
		version = "unknown"
	}
	updatesDir := filepath.Join(opts.BaseDir, "updates")
	if err := rejectLinkedPath(updatesDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		return "", err
	}
	scriptPath := filepath.Join(updatesDir, "apply-update.ps1")
	if err := rejectLinkedPath(scriptPath); err != nil {
		return "", err
	}
	script := applyScript(opts, version)
	return scriptPath, os.WriteFile(scriptPath, []byte("\xef\xbb\xbf"+script), 0o600)
}

// Reject linked ancestors before writing elevated installer artifacts.
func rejectLinkedPath(name string) error {
	for {
		info, err := os.Lstat(name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked update path rejected: %s", name)
		}
		parent := filepath.Dir(name)
		if parent == name {
			return nil
		}
		name = parent
	}
}

func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultServerURL
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported update URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func CompareVersions(a, b string) int {
	an, as := parseVersion(a)
	bn, bs := parseVersion(b)
	n := len(an)
	if len(bn) > n {
		n = len(bn)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	if as == bs {
		return 0
	}
	if as == "" {
		return 1
	}
	if bs == "" {
		return -1
	}
	if as > bs {
		return 1
	}
	if as < bs {
		return -1
	}
	return 0
}

func joinURL(baseURL, file string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	file = strings.TrimSpace(file)
	if file == "" {
		return "", errors.New("empty URL path")
	}
	u.Path = path.Join(u.Path, file)
	return u.String(), nil
}

func validateZip(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("invalid update package: %w", err)
	}
	return r.Close()
}

func packageVersionFromFile(file string) string {
	base := filepath.Base(strings.TrimSpace(file))
	ext := filepath.Ext(base)
	if !strings.EqualFold(ext, ".zip") {
		return ""
	}
	const prefix = "meshlink-"
	if !strings.HasPrefix(strings.ToLower(base), prefix) {
		return ""
	}
	version := strings.TrimSuffix(base[len(prefix):], ext)
	return strings.TrimSpace(version)
}

func parseVersion(raw string) ([]int, string) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "v"))
	if raw == "" {
		return nil, ""
	}
	main := raw
	suffix := ""
	for _, sep := range []string{"-", "+"} {
		if i := strings.Index(main, sep); i >= 0 {
			suffix = main[i+1:]
			main = main[:i]
			break
		}
	}
	parts := strings.Split(main, ".")
	nums := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			nums = append(nums, 0)
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			suffix = strings.TrimSpace(strings.TrimPrefix(raw, main))
			break
		}
		nums = append(nums, n)
	}
	return nums, suffix
}

func sanitizeVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

//go:embed apply.ps1
var applyScriptTemplate string

func applyScript(opts ApplyOptions, safeVersion string) string {
	return strings.NewReplacer("@@WAIT_PID@@", strconv.Itoa(opts.WaitPID), "@@BASE_DIR@@", psQuote(opts.BaseDir), "@@PACKAGE_PATH@@", psQuote(opts.PackagePath), "@@VERSION@@", psQuote(safeVersion), "@@SERVICE_NAME@@", psQuote(opts.ServiceName), "@@PACKAGE_HASH@@", psQuote(opts.PackageSHA256)).Replace(applyScriptTemplate)
}
