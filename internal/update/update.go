package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
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
	DefaultListen    = "10.77.0.1:1263"
	DefaultServerURL = "http://10.77.0.1:1263"
	ManifestFile     = "manifest.json"
)

type Manifest struct {
	Product     string      `json:"product"`
	Version     string      `json:"version"`
	BuildTime   string      `json:"build_time,omitempty"`
	GeneratedAt time.Time   `json:"generated_at"`
	Package     PackageInfo `json:"package"`
	Notes       []string    `json:"notes,omitempty"`
}

type PackageInfo struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type CheckResult struct {
	CurrentVersion  string   `json:"current_version"`
	Manifest        Manifest `json:"manifest"`
	UpdateAvailable bool     `json:"update_available"`
}

type ApplyOptions struct {
	BaseDir     string
	PackagePath string
	Version     string
	ServiceName string
	WaitPID     int
}

func Check(ctx context.Context, baseURL, currentVersion string) (CheckResult, error) {
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
	resp, err := http.DefaultClient.Do(req)
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
	if strings.TrimSpace(manifest.Version) == "" {
		return CheckResult{}, errors.New("manifest version is empty")
	}
	if strings.TrimSpace(manifest.Package.File) == "" {
		return CheckResult{}, errors.New("manifest package file is empty")
	}
	if packageVersion := packageVersionFromFile(manifest.Package.File); packageVersion != "" && CompareVersions(packageVersion, manifest.Version) != 0 {
		return CheckResult{}, fmt.Errorf("manifest version %q does not match package file version %q (%s)", manifest.Version, packageVersion, manifest.Package.File)
	}
	return CheckResult{
		CurrentVersion:  currentVersion,
		Manifest:        manifest,
		UpdateAvailable: CompareVersions(manifest.Version, currentVersion) > 0,
	}, nil
}

func DownloadPackage(ctx context.Context, baseURL string, manifest Manifest, destDir string) (string, error) {
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
	resp, err := http.DefaultClient.Do(req)
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
	written, copyErr := io.Copy(io.MultiWriter(out, hasher), resp.Body)
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
	if err := validateZip(tmpPath); err != nil {
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
	if opts.ServiceName == "" {
		opts.ServiceName = "MeshlinkAgent"
	}
	version := sanitizeVersion(opts.Version)
	if version == "" {
		version = "unknown"
	}
	updatesDir := filepath.Join(opts.BaseDir, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		return "", err
	}
	scriptPath := filepath.Join(updatesDir, "apply-update-"+version+".ps1")
	script := applyScript(opts, version)
	return scriptPath, os.WriteFile(scriptPath, []byte(script), 0o600)
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

func applyScript(opts ApplyOptions, safeVersion string) string {
	return `$ErrorActionPreference = 'Stop'
$WaitPid = ` + strconv.Itoa(opts.WaitPID) + `
$BaseDir = ` + psQuote(opts.BaseDir) + `
$PackagePath = ` + psQuote(opts.PackagePath) + `
$Version = ` + psQuote(safeVersion) + `
$ServiceName = ` + psQuote(opts.ServiceName) + `
$UpdateDir = Join-Path $BaseDir 'updates'
$LogPath = Join-Path $UpdateDir ('apply-update-' + $Version + '.log')
New-Item -ItemType Directory -Force -Path $UpdateDir | Out-Null
try { Start-Transcript -Path $LogPath -Append | Out-Null } catch {}
try {
  if ($WaitPid -gt 0) {
    try { Wait-Process -Id $WaitPid -Timeout 60 -ErrorAction SilentlyContinue } catch {}
  }

  $serviceWasRunning = $false
  try {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($null -ne $svc -and $svc.Status -eq 'Running') {
      $serviceWasRunning = $true
      Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
      Start-Sleep -Seconds 3
    }
  } catch {
    Write-Host ('stop service skipped: ' + $_.Exception.Message)
  }

  $Stage = Join-Path $UpdateDir ('stage-' + $Version)
  Remove-Item -LiteralPath $Stage -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force -Path $Stage | Out-Null
  Expand-Archive -LiteralPath $PackagePath -DestinationPath $Stage -Force

  $PackageRoot = $Stage
  $Dirs = @(Get-ChildItem -LiteralPath $Stage -Directory)
  if ($Dirs.Count -eq 1 -and (Test-Path (Join-Path $Dirs[0].FullName 'bin'))) {
    $PackageRoot = $Dirs[0].FullName
  }

  New-Item -ItemType Directory -Force -Path (Join-Path $BaseDir 'bin') | Out-Null
  foreach ($Name in @('mesh-agent.exe', 'mesh-desktop.exe', 'meshctl.exe', 'mesh-update-server.exe')) {
    $Src = Join-Path $PackageRoot ('bin\' + $Name)
    if (Test-Path $Src) {
      Copy-Item -LiteralPath $Src -Destination (Join-Path $BaseDir ('bin\' + $Name)) -Force
    }
  }

  foreach ($Name in @('README.md', 'DEPLOY.zh-CN.md', 'THIRD_PARTY_NOTICES.md', 'LICENSE')) {
    $Src = Join-Path $PackageRoot $Name
    if (Test-Path $Src) {
      Copy-Item -LiteralPath $Src -Destination (Join-Path $BaseDir $Name) -Force
    }
  }

  $PackageConfigs = Join-Path $PackageRoot 'configs'
  if (Test-Path $PackageConfigs) {
    New-Item -ItemType Directory -Force -Path (Join-Path $BaseDir 'configs') | Out-Null
    Get-ChildItem -LiteralPath $PackageConfigs -File -Filter '*.example.json' | ForEach-Object {
      Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $BaseDir ('configs\' + $_.Name)) -Force
    }
  }

  if ($serviceWasRunning) {
    try { Start-Service -Name $ServiceName -ErrorAction SilentlyContinue } catch {
      Write-Host ('start service skipped: ' + $_.Exception.Message)
    }
  }

  $Desktop = Join-Path $BaseDir 'bin\mesh-desktop.exe'
  if (Test-Path $Desktop) {
    Start-Process -FilePath $Desktop
  }
} catch {
  Write-Host ('update failed: ' + $_.Exception.Message)
  Start-Sleep -Seconds 10
  exit 1
} finally {
  try { Stop-Transcript | Out-Null } catch {}
}
`
}
