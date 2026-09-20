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
$Stage = Join-Path $UpdateDir ('stage-' + $Version)
$LastKnownGoodRoot = Join-Path $UpdateDir 'last-known-good'
$script:BackupReady = $false
$script:BackupRecords = @()
$script:ServiceWasRunning = $false

function Resolve-PackageFile([string]$RelativePath) {
  $Normalized = $RelativePath.Replace('/', '\')
  if ([System.IO.Path]::IsPathRooted($Normalized) -or $Normalized -match '(^|[\\/])\.\.([\\/]|$)' -or $Normalized -match '^[A-Za-z]:') {
    throw ('unsafe package manifest path: ' + $RelativePath)
  }
  $FullPath = [System.IO.Path]::GetFullPath((Join-Path $PackageRoot $Normalized))
  $Prefix = $PackageRoot.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
  if (-not $FullPath.StartsWith($Prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw ('package manifest path escaped package root: ' + $RelativePath)
  }
  return $FullPath
}

function Backup-Target([string]$RelativePath) {
  $Target = Join-Path $BaseDir $RelativePath
  $Existed = Test-Path -LiteralPath $Target -PathType Leaf
  if ($Existed) {
    $BackupPath = Join-Path $BackupDir $RelativePath
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $BackupPath) | Out-Null
    Copy-Item -LiteralPath $Target -Destination $BackupPath -Force
  }
  $script:BackupRecords += [ordered]@{ path = $RelativePath.Replace('\', '/'); existed = [bool]$Existed }
}

function Restore-LastKnownGood {
  if (-not $script:BackupReady) {
    return
  }
  foreach ($Record in $script:BackupRecords) {
    $RelativePath = ([string]$Record.path).Replace('/', '\')
    $Target = Join-Path $BaseDir $RelativePath
    if ([bool]$Record.existed) {
      $BackupPath = Join-Path $BackupDir $RelativePath
      New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Target) | Out-Null
      Copy-Item -LiteralPath $BackupPath -Destination $Target -Force
    } else {
      Remove-Item -LiteralPath $Target -Force -ErrorAction SilentlyContinue
    }
  }
}

New-Item -ItemType Directory -Force -Path $UpdateDir | Out-Null
try { Start-Transcript -Path $LogPath -Append | Out-Null } catch {}
try {
  if ($WaitPid -gt 0) {
    try { Wait-Process -Id $WaitPid -Timeout 60 -ErrorAction SilentlyContinue } catch {}
  }

  Remove-Item -LiteralPath $Stage -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force -Path $Stage | Out-Null
  Expand-Archive -LiteralPath $PackagePath -DestinationPath $Stage -Force

  $Dirs = @(Get-ChildItem -LiteralPath $Stage -Directory)
  $RootFiles = @(Get-ChildItem -LiteralPath $Stage -File)
  if ($Dirs.Count -ne 1 -or $RootFiles.Count -ne 0 -or $Dirs[0].Name -ne 'meshlink') {
    throw 'update package must contain exactly one meshlink root directory'
  }
  $PackageRoot = $Dirs[0].FullName

  $ManifestPath = Join-Path $PackageRoot 'manifest.json'
  if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
    throw 'required package manifest.json is missing'
  }
  $PackageManifest = Get-Content -Raw -LiteralPath $ManifestPath | ConvertFrom-Json
  if ($PackageManifest.schema -ne 'meshlink-package-v1') {
    throw 'unsupported package manifest schema'
  }
  if ([string]$PackageManifest.version -ne $Version) {
    throw 'package manifest version does not match requested version'
  }
  $VersionPath = Join-Path $PackageRoot 'VERSION'
  if (-not (Test-Path -LiteralPath $VersionPath -PathType Leaf) -or (Get-Content -Raw -LiteralPath $VersionPath).Trim() -ne $Version) {
    throw 'package VERSION does not match requested version'
  }

  $RequiredFiles = @(
    'VERSION', 'bin/mesh-agent.exe', 'bin/mesh-cloudhub.exe', 'bin/mesh-desktop.exe',
    'bin/mesh-update-server.exe', 'bin/meshctl.exe'
  )
  foreach ($RelativePath in $RequiredFiles) {
    if (-not (Test-Path -LiteralPath (Resolve-PackageFile $RelativePath) -PathType Leaf)) {
      throw ('required update package file is missing: ' + $RelativePath)
    }
  }

  $ManifestPaths = @($PackageManifest.files | ForEach-Object { [string]$_.path })
  [string[]]$SortedPaths = @($ManifestPaths)
  [System.Array]::Sort($SortedPaths, [System.StringComparer]::Ordinal)
  if (($ManifestPaths -join [char]10) -cne ($SortedPaths -join [char]10)) {
    throw 'package manifest file list is not sorted'
  }
  $Covered = @{}
  foreach ($File in @($PackageManifest.files)) {
    $RelativePath = [string]$File.path
    $Source = Resolve-PackageFile $RelativePath
    $Key = $RelativePath.Replace('\', '/').ToLowerInvariant()
    if ($Covered.ContainsKey($Key)) {
      throw ('duplicate package manifest file: ' + $RelativePath)
    }
    if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
      throw ('package manifest references missing file: ' + $RelativePath)
    }
    $Info = Get-Item -LiteralPath $Source
    $ActualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $Source).Hash.ToLowerInvariant()
    if ([int64]$Info.Length -ne [int64]$File.size -or $ActualHash -cne ([string]$File.sha256).ToLowerInvariant()) {
      throw ('package manifest hash or size mismatch: ' + $RelativePath)
    }
    $Covered[$Key] = $true
  }
  foreach ($SourceFile in @(Get-ChildItem -LiteralPath $PackageRoot -File -Recurse)) {
    $RelativePath = $SourceFile.FullName.Substring($PackageRoot.Length + 1).Replace('\', '/')
    if ($RelativePath -ceq 'manifest.json') {
      continue
    }
    if (-not $Covered.ContainsKey($RelativePath.ToLowerInvariant())) {
      throw ('package contains file not covered by manifest: ' + $RelativePath)
    }
  }

  $PackageMode = [string]$PackageManifest.mode
  if ($PackageMode -eq 'release') {
    if (-not [bool]$PackageManifest.signing.code_signed) {
      throw 'release package is not marked code signed'
    }
    $CertificateThumbprint = ([string]$PackageManifest.signing.certificate_thumbprint).Replace(' ', '').ToUpperInvariant()
    if ($CertificateThumbprint -notmatch '^[0-9A-F]{40}$') {
      throw 'release package certificate thumbprint is invalid'
    }
    foreach ($Executable in @(Get-ChildItem -LiteralPath (Join-Path $PackageRoot 'bin') -File -Filter '*.exe')) {
      $Signature = Get-AuthenticodeSignature -LiteralPath $Executable.FullName
      if ($Signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or $null -eq $Signature.SignerCertificate) {
        throw ('Authenticode signature is invalid: ' + $Executable.Name)
      }
      $ActualThumbprint = $Signature.SignerCertificate.Thumbprint.Replace(' ', '').ToUpperInvariant()
      if ($ActualThumbprint -cne $CertificateThumbprint) {
        throw ('Authenticode signer certificate mismatch: ' + $Executable.Name)
      }
    }
  } elseif ($PackageMode -eq 'development') {
    if ([bool]$PackageManifest.signing.code_signed -or -not [string]::IsNullOrWhiteSpace([string]$PackageManifest.signing.certificate_thumbprint)) {
      throw 'development package must be explicitly unsigned'
    }
  } else {
    throw ('unsupported package mode: ' + $PackageMode)
  }

  $InstalledVersionPath = Join-Path $BaseDir 'VERSION'
  if (-not (Test-Path -LiteralPath $InstalledVersionPath -PathType Leaf)) {
    throw 'installed VERSION is missing; cannot establish last-known-good'
  }
  $CurrentVersion = (Get-Content -Raw -LiteralPath $InstalledVersionPath).Trim()
  if ($CurrentVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
    throw 'installed VERSION is invalid; cannot establish last-known-good'
  }

  $Svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  if ($null -ne $Svc -and $Svc.Status -eq 'Running') {
    $script:ServiceWasRunning = $true
    Stop-Service -Name $ServiceName -Force -ErrorAction Stop
    Start-Sleep -Seconds 3
  }

  New-Item -ItemType Directory -Force -Path $LastKnownGoodRoot | Out-Null
  $BackupLeaf = $CurrentVersion + '-' + (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
  $BackupDir = Join-Path $LastKnownGoodRoot $BackupLeaf
  New-Item -ItemType Directory -Force -Path $BackupDir | Out-Null

  $ManagedFiles = @(
    'VERSION', 'bin\mesh-agent.exe', 'bin\mesh-cloudhub.exe', 'bin\mesh-desktop.exe',
    'bin\mesh-update-server.exe', 'bin\meshctl.exe', 'README.md', 'DEPLOY.zh-CN.md',
    'THIRD_PARTY_NOTICES.md', 'LICENSE'
  )
  $PackageConfigs = Join-Path $PackageRoot 'configs'
  if (Test-Path -LiteralPath $PackageConfigs -PathType Container) {
    foreach ($Config in @(Get-ChildItem -LiteralPath $PackageConfigs -File -Filter '*.example.json')) {
      $ManagedFiles += 'configs\' + $Config.Name
    }
  }
  foreach ($RelativePath in $ManagedFiles) {
    Backup-Target $RelativePath
  }
  $script:BackupReady = $true
  $BackupDocument = [ordered]@{
    schema = 'meshlink-last-known-good-v1'
    current_version = $CurrentVersion
    attempted_version = $Version
    created_at = (Get-Date).ToUniversalTime().ToString('o')
    files = $script:BackupRecords
  }
  $BackupDocument | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $BackupDir 'backup-manifest.json') -Encoding UTF8

  New-Item -ItemType Directory -Force -Path (Join-Path $BaseDir 'bin') | Out-Null
  foreach ($Name in @('mesh-agent.exe', 'mesh-cloudhub.exe', 'mesh-desktop.exe', 'meshctl.exe', 'mesh-update-server.exe')) {
    $Src = Join-Path $PackageRoot ('bin\' + $Name)
    Copy-Item -LiteralPath $Src -Destination (Join-Path $BaseDir ('bin\' + $Name)) -Force
  }

  foreach ($Name in @('README.md', 'DEPLOY.zh-CN.md', 'THIRD_PARTY_NOTICES.md', 'LICENSE')) {
    $Src = Join-Path $PackageRoot $Name
    if (Test-Path -LiteralPath $Src -PathType Leaf) {
      Copy-Item -LiteralPath $Src -Destination (Join-Path $BaseDir $Name) -Force
    }
  }

  if (Test-Path -LiteralPath $PackageConfigs -PathType Container) {
    New-Item -ItemType Directory -Force -Path (Join-Path $BaseDir 'configs') | Out-Null
    Get-ChildItem -LiteralPath $PackageConfigs -File -Filter '*.example.json' | ForEach-Object {
      Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $BaseDir ('configs\' + $_.Name)) -Force
    }
  }
  Copy-Item -LiteralPath $VersionPath -Destination $InstalledVersionPath -Force

  if ($script:ServiceWasRunning) {
    Start-Service -Name $ServiceName -ErrorAction Stop
  }

  $Desktop = Join-Path $BaseDir 'bin\mesh-desktop.exe'
  if (Test-Path -LiteralPath $Desktop -PathType Leaf) {
    Start-Process -FilePath $Desktop
  }
} catch {
  Write-Host ('update failed: ' + $_.Exception.Message)
  if ($script:ServiceWasRunning) {
    try { Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue } catch {}
  }
  try { Restore-LastKnownGood } catch { Write-Host ('rollback failed: ' + $_.Exception.Message) }
  if ($script:ServiceWasRunning) {
    try { Start-Service -Name $ServiceName -ErrorAction SilentlyContinue } catch {}
  }
  exit 1
} finally {
  Remove-Item -LiteralPath $Stage -Recurse -Force -ErrorAction SilentlyContinue
  try { Stop-Transcript | Out-Null } catch {}
}
`
}
