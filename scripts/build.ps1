param(
  [switch]$Package,
  [switch]$PrivatePackage,
  [ValidateSet("Development", "Release")]
  [string]$Mode = "Development",
  [string]$Version = "",
  [string]$BuildTime = "",
  [string]$SigningCertificateThumbprint = "",
  [string]$PreviousVersion = "",
  [string]$PreviousPackagePath = "",
  [string]$OutDir = "dist",
  [string]$ReleaseDir = "release",
  [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

function Write-Utf8NoBom([string]$Path, [string]$Content) {
  $encoding = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($Path, $Content, $encoding)
}

function Get-SHA256Hex([string]$Path) {
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Invoke-Go([string[]]$Arguments) {
  & go @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "go $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
  }
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
Set-Location $root
$versionPath = Join-Path $root "VERSION"
if (-not (Test-Path -LiteralPath $versionPath -PathType Leaf)) {
  throw "VERSION is missing"
}
$sourceVersion = (Get-Content -Raw -LiteralPath $versionPath).Trim()
if ($sourceVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
  throw "VERSION is invalid"
}
if (-not [string]::IsNullOrWhiteSpace($Version) -and $Version -cne $sourceVersion) {
  throw "Requested version '$Version' does not match VERSION '$sourceVersion'"
}
$Version = $sourceVersion

if ([string]::IsNullOrWhiteSpace($BuildTime)) {
  $BuildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
} else {
  $parsedBuildTime = [datetimeoffset]::MinValue
  if (-not [datetimeoffset]::TryParse($BuildTime, [ref]$parsedBuildTime)) {
    throw "BuildTime is invalid"
  }
  $BuildTime = $parsedBuildTime.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
}

$modeValue = $Mode.ToLowerInvariant()
$certificate = $null
$certificateThumbprint = ""
if ($Mode -eq "Release") {
  if ([string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
    $SigningCertificateThumbprint = [string]$env:MESHLINK_SIGNING_CERT_THUMBPRINT
  }
  $certificateThumbprint = $SigningCertificateThumbprint.Replace(' ', '').ToUpperInvariant()
  if ($certificateThumbprint -notmatch '^[0-9A-F]{40}$') {
    throw "Formal release requires MESHLINK_SIGNING_CERT_THUMBPRINT or -SigningCertificateThumbprint"
  }
  $certificatePath = "Cert:\CurrentUser\My\$certificateThumbprint"
  if (-not (Test-Path -LiteralPath $certificatePath)) {
    throw "Signing certificate is not present in the CurrentUser certificate store: $certificateThumbprint"
  }
  $certificate = Get-Item -LiteralPath $certificatePath
  if (-not $certificate.HasPrivateKey) {
    throw "Signing certificate does not expose a private key: $certificateThumbprint"
  }
  $codeSigningUsage = @($certificate.EnhancedKeyUsageList | Where-Object { $_.ObjectId.Value -eq '1.3.6.1.5.5.7.3.3' })
  if ($codeSigningUsage.Count -eq 0) {
    throw "Signing certificate is not valid for code signing: $certificateThumbprint"
  }
}

$binDir = Join-Path $root "bin"
New-Item -ItemType Directory -Force -Path (Join-Path $binDir "linux") | Out-Null
$versionFlags = "-X meshlink/internal/version.Version=$Version -X meshlink/internal/version.BuildTime=$BuildTime"

if (-not $SkipTests) {
  Invoke-Go -Arguments @("test", "-count=1", "./...")
}
Invoke-Go -Arguments @("build", "-ldflags", $versionFlags, "-o", (Join-Path $binDir "mesh-agent.exe"), ".\cmd\mesh-agent")
Invoke-Go -Arguments @("build", "-ldflags", $versionFlags, "-o", (Join-Path $binDir "meshctl.exe"), ".\cmd\meshctl")
Invoke-Go -Arguments @("build", "-ldflags", $versionFlags, "-o", (Join-Path $binDir "mesh-update-server.exe"), ".\cmd\mesh-update-server")
Invoke-Go -Arguments @("build", "-ldflags", $versionFlags, "-o", (Join-Path $binDir "mesh-cloudhub.exe"), ".\cmd\mesh-cloudhub")
Invoke-Go -Arguments @("build", "-ldflags", "-H windowsgui $versionFlags", "-o", (Join-Path $binDir "mesh-desktop.exe"), ".\cmd\mesh-desktop")

$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
try {
  $env:GOOS = "linux"
  $env:GOARCH = "amd64"
  $env:CGO_ENABLED = "0"
  Invoke-Go -Arguments @("build", "-ldflags", $versionFlags, "-o", (Join-Path $binDir "linux\mesh-agent"), ".\cmd\mesh-agent")
} finally {
  $env:GOOS = $oldGOOS
  $env:GOARCH = $oldGOARCH
  $env:CGO_ENABLED = $oldCGO
}

$requiredExecutables = @(
  "mesh-agent.exe", "mesh-cloudhub.exe", "mesh-desktop.exe", "mesh-update-server.exe", "meshctl.exe"
)
if ($Mode -eq "Release") {
  foreach ($name in $requiredExecutables) {
    $path = Join-Path $binDir $name
    $result = Set-AuthenticodeSignature -LiteralPath $path -Certificate $certificate -HashAlgorithm SHA256
    if ($result.Status -ne [System.Management.Automation.SignatureStatus]::Valid) {
      throw "Authenticode signing failed for $name`: $($result.StatusMessage)"
    }
  }
}

$artifacts = @()
foreach ($relative in @($requiredExecutables + "linux/mesh-agent")) {
  $path = Join-Path $binDir $relative.Replace('/', '\')
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "Required build artifact is missing: $relative"
  }
  $file = Get-Item -LiteralPath $path
  $artifacts += [ordered]@{ path = $relative; sha256 = Get-SHA256Hex $path; size = [int64]$file.Length }
}
$buildMetadata = [ordered]@{
  schema = "meshlink-build-v1"
  version = $Version
  build_time = $BuildTime
  mode = $modeValue
  signing = [ordered]@{ code_signed = ($Mode -eq "Release"); certificate_thumbprint = $certificateThumbprint }
  artifacts = $artifacts
}
Write-Utf8NoBom (Join-Path $binDir "build-metadata.json") (($buildMetadata | ConvertTo-Json -Depth 8) + "`n")

if ($Package) {
  $packageArgs = @{
    OutDir = $OutDir
    ReleaseDir = $ReleaseDir
    Mode = $Mode
    Version = $Version
    BuildTime = $BuildTime
    SigningCertificateThumbprint = $certificateThumbprint
    PreviousVersion = $PreviousVersion
    PreviousPackagePath = $PreviousPackagePath
  }
  & (Join-Path $PSScriptRoot "package.ps1") @packageArgs
  if ($LASTEXITCODE -ne 0) {
    throw "package.ps1 failed with exit code $LASTEXITCODE"
  }
}
if ($PrivatePackage) {
  & (Join-Path $PSScriptRoot "package-private.ps1")
  if ($LASTEXITCODE -ne 0) {
    throw "package-private.ps1 failed with exit code $LASTEXITCODE"
  }
}

Write-Output "Built Meshlink $Version ($modeValue) at $BuildTime"
