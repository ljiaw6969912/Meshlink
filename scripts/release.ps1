param(
  [ValidateSet("Development", "Release")]
  [string]$Mode = "Development",
  [string]$Version = "",
  [string]$BuildTime = "",
  [string]$OutDir = "dist",
  [string]$ReleaseDir = "release",
  [string]$SigningCertificateThumbprint = "",
  [string]$PreviousVersion = "",
  [string]$PreviousPackagePath = "",
  [switch]$PrivatePackage
)

$ErrorActionPreference = "Stop"

function Invoke-MeshlinkRelease {
  $root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
  $sourceVersion = (Get-Content -Raw -LiteralPath (Join-Path $root "VERSION")).Trim()
  if ($sourceVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
    throw "VERSION is missing or invalid"
  }
  if (-not [string]::IsNullOrWhiteSpace($Version) -and $Version -cne $sourceVersion) {
    throw "Requested version '$Version' does not match VERSION '$sourceVersion'"
  }
  $Version = $sourceVersion
  if ([string]::IsNullOrWhiteSpace($BuildTime)) {
    $BuildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
  }

  if ($Mode -eq "Release") {
    if ([string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
      $SigningCertificateThumbprint = [string]$env:MESHLINK_SIGNING_CERT_THUMBPRINT
    }
    if ($SigningCertificateThumbprint.Replace(' ', '') -notmatch '^[0-9A-Fa-f]{40}$') {
      throw "Formal release requires MESHLINK_SIGNING_CERT_THUMBPRINT or -SigningCertificateThumbprint"
    }
    if ([string]::IsNullOrWhiteSpace($PreviousPackagePath)) {
      throw "Formal release requires -PreviousPackagePath for rollback"
    }
  }

  $buildArgs = @{
    Package = $true
    PrivatePackage = [bool]$PrivatePackage
    Mode = $Mode
    Version = $Version
    BuildTime = $BuildTime
    SigningCertificateThumbprint = $SigningCertificateThumbprint
    PreviousVersion = $PreviousVersion
    PreviousPackagePath = $PreviousPackagePath
    OutDir = $OutDir
    ReleaseDir = $ReleaseDir
  }
  & (Join-Path $PSScriptRoot "build.ps1") @buildArgs

  $resolvedRelease = if ([System.IO.Path]::IsPathRooted($ReleaseDir)) { [System.IO.Path]::GetFullPath($ReleaseDir) } else { [System.IO.Path]::GetFullPath((Join-Path $root $ReleaseDir)) }
  $primaryPackage = Join-Path $resolvedRelease "meshlink-$Version.zip"
  if (-not (Test-Path -LiteralPath $primaryPackage -PathType Leaf)) {
    throw "Primary release package was not created: $primaryPackage"
  }

  $tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("meshlink-release-repeat-" + [guid]::NewGuid().ToString("N"))
  try {
    $repeatOut = Join-Path $tempRoot "dist"
    $repeatRelease = Join-Path $tempRoot "release"
    $packageArgs = @{
      OutDir = $repeatOut
      ReleaseDir = $repeatRelease
      BinDir = (Join-Path $root "bin")
      Mode = $Mode
      Version = $Version
      BuildTime = $BuildTime
      SigningCertificateThumbprint = $SigningCertificateThumbprint
      PreviousVersion = $PreviousVersion
      PreviousPackagePath = $PreviousPackagePath
    }
    & (Join-Path $PSScriptRoot "package.ps1") @packageArgs
    $repeatPackage = Join-Path $repeatRelease "meshlink-$Version.zip"
    & (Join-Path $PSScriptRoot "verify-release.ps1") -ReleaseDir $resolvedRelease -Version $Version -ExpectedMode $Mode -ReferencePackagePath $repeatPackage
  } finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
  }

  Write-Output "Meshlink $Mode release pipeline completed for $Version"
  Write-Output "Artifacts: $resolvedRelease"
}

try {
  Invoke-MeshlinkRelease
} catch {
  Write-Error $_
  exit 1
}
