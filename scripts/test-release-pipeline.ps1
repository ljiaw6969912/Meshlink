param()

$ErrorActionPreference = "Stop"

function Write-Utf8NoBom([string]$Path, [string]$Content) {
  $encoding = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($Path, $Content, $encoding)
}

function Get-SHA256Hex([string]$Path) {
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Assert-Fails([scriptblock]$Action, [string]$ExpectedMessage) {
  try {
    & $Action
  } catch {
    if ($_.Exception.Message -notmatch $ExpectedMessage) {
      throw "Expected failure matching '$ExpectedMessage', got: $($_.Exception.Message)"
    }
    return
  }
  throw "Expected command to fail with '$ExpectedMessage'"
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$verifyScript = Join-Path $PSScriptRoot "verify-release.ps1"
$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("meshlink-task11c-" + [guid]::NewGuid().ToString("N"))
$releaseDir = Join-Path $tempRoot "release"
$stageDir = Join-Path $tempRoot "stage"
$packageRoot = Join-Path $stageDir "meshlink"
$version = "9.8.7"

try {
  foreach ($script in @(
    (Join-Path $PSScriptRoot "build.ps1"),
    (Join-Path $PSScriptRoot "package.ps1"),
    (Join-Path $PSScriptRoot "release.ps1"),
    $verifyScript,
    $PSCommandPath
  )) {
    $tokens = $null
    $parseErrors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile($script, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count -ne 0) {
      throw "PowerShell syntax error in $script`: $($parseErrors[0].Message)"
    }
  }

  $publishBatch = Get-Content -Raw -LiteralPath (Join-Path $root "publish-update.bat")
  foreach ($requiredBatchText in @('if /i "%~1"=="-Help"', 'scripts\release.ps1')) {
    if (-not $publishBatch.Contains($requiredBatchText)) {
      throw "publish-update.bat is missing required pipeline text: $requiredBatchText"
    }
  }
  if ($publishBatch -match '(?im)^\s*>\s*VERSION\s+echo') {
    throw "publish-update.bat must not mutate VERSION"
  }

  New-Item -ItemType Directory -Force -Path (Join-Path $packageRoot "bin"), $releaseDir | Out-Null
  Write-Utf8NoBom (Join-Path $packageRoot "VERSION") "$version`n"
  Write-Utf8NoBom (Join-Path $packageRoot "README.md") "Meshlink development fixture`n"
  foreach ($name in @("mesh-agent.exe", "mesh-cloudhub.exe", "mesh-desktop.exe", "mesh-update-server.exe", "meshctl.exe", "wintun.dll")) {
    Write-Utf8NoBom (Join-Path $packageRoot "bin\$name") "unsigned test artifact: $name`n"
  }

  $files = @()
  [string[]]$relativeFiles = @(Get-ChildItem -LiteralPath $packageRoot -File -Recurse | ForEach-Object {
    $_.FullName.Substring($packageRoot.Length + 1).Replace('\', '/')
  })
  [System.Array]::Sort($relativeFiles, [System.StringComparer]::Ordinal)
  foreach ($relative in $relativeFiles) {
    $file = Get-Item -LiteralPath (Join-Path $packageRoot $relative.Replace('/', '\'))
    $files += [ordered]@{ path = $relative; sha256 = Get-SHA256Hex $file.FullName; size = [int64]$file.Length }
  }
  $packageManifest = [ordered]@{
    schema = "meshlink-package-v1"
    product = "Meshlink"
    version = $version
    mode = "development"
    build_time = "2026-07-15T00:00:00Z"
    files = $files
    signing = [ordered]@{ code_signed = $false; certificate_thumbprint = "" }
  }
  Write-Utf8NoBom (Join-Path $packageRoot "manifest.json") (($packageManifest | ConvertTo-Json -Depth 8) + "`n")

  Add-Type -AssemblyName System.IO.Compression
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $packagePath = Join-Path $releaseDir "meshlink-$version.zip"
  [System.IO.Compression.ZipFile]::CreateFromDirectory($stageDir, $packagePath, [System.IO.Compression.CompressionLevel]::Optimal, $false)
  $referencePackage = Join-Path $tempRoot "reference.zip"
  Copy-Item -LiteralPath $packagePath -Destination $referencePackage

  $notesPath = Join-Path $releaseDir "release-notes-$version.zh-CN.md"
  Write-Utf8NoBom $notesPath "# Meshlink $version Release Notes`n`n- Unsigned development fixture.`n"
  $previousPackage = Join-Path $releaseDir "meshlink-9.8.6.zip"
  Write-Utf8NoBom $previousPackage "known-good rollback fixture`n"
  $rollbackPath = Join-Path $releaseDir "rollback-manifest-$version.json"
  $rollback = [ordered]@{
    schema = "meshlink-rollback-v1"
    version = $version
    previous_version = "9.8.6"
    package = [ordered]@{ file = "meshlink-9.8.6.zip"; sha256 = Get-SHA256Hex $previousPackage; size = (Get-Item $previousPackage).Length }
  }
  Write-Utf8NoBom $rollbackPath (($rollback | ConvertTo-Json -Depth 6) + "`n")

  $manifestPath = Join-Path $releaseDir "manifest.json"
  $releaseManifest = [ordered]@{
    schema = "meshlink-release-v1"
    product = "Meshlink"
    version = $version
    mode = "development"
    build_time = "2026-07-15T00:00:00Z"
    generated_at = "2026-07-15T00:00:00Z"
    package = [ordered]@{ file = "meshlink-$version.zip"; sha256 = Get-SHA256Hex $packagePath; size = (Get-Item $packagePath).Length }
    signing = [ordered]@{ code_signed = $false; certificate_thumbprint = "" }
    release_notes = [ordered]@{ file = [System.IO.Path]::GetFileName($notesPath); sha256 = Get-SHA256Hex $notesPath }
    rollback = [ordered]@{ file = [System.IO.Path]::GetFileName($rollbackPath); sha256 = Get-SHA256Hex $rollbackPath }
  }
  Write-Utf8NoBom $manifestPath (($releaseManifest | ConvertTo-Json -Depth 8) + "`n")

  $checksumsPath = Join-Path $releaseDir "checksums-$version.sha256"
  $checksumLines = foreach ($path in @($packagePath, $manifestPath, $notesPath, $rollbackPath, $previousPackage)) {
    "$(Get-SHA256Hex $path)  $([System.IO.Path]::GetFileName($path))"
  }
  Write-Utf8NoBom $checksumsPath (($checksumLines -join "`n") + "`n")

  & $verifyScript -ReleaseDir $releaseDir -Version $version -ExpectedMode Development -ReferencePackagePath $referencePackage

  [System.IO.File]::AppendAllText($packagePath, "tampered", [System.Text.Encoding]::UTF8)
  Assert-Fails { & $verifyScript -ReleaseDir $releaseDir -Version $version -ExpectedMode Development } "SHA-256|sha256|hash"
  Copy-Item -LiteralPath $referencePackage -Destination $packagePath -Force

  $differentPackage = Join-Path $tempRoot "different.zip"
  Copy-Item -LiteralPath $referencePackage -Destination $differentPackage
  [System.IO.File]::AppendAllText($differentPackage, "different", [System.Text.Encoding]::UTF8)
  Assert-Fails { & $verifyScript -ReleaseDir $releaseDir -Version $version -ExpectedMode Development -ReferencePackagePath $differentPackage } "reproduc"

  $higherPackage = Join-Path $releaseDir "meshlink-9.9.0.zip"
  Copy-Item -LiteralPath $previousPackage -Destination $higherPackage
  $rollback.previous_version = "9.9.0"
  $rollback.package.file = [System.IO.Path]::GetFileName($higherPackage)
  $rollback.package.sha256 = Get-SHA256Hex $higherPackage
  $rollback.package.size = (Get-Item $higherPackage).Length
  Write-Utf8NoBom $rollbackPath (($rollback | ConvertTo-Json -Depth 6) + "`n")
  $releaseManifest.rollback.sha256 = Get-SHA256Hex $rollbackPath
  Write-Utf8NoBom $manifestPath (($releaseManifest | ConvertTo-Json -Depth 8) + "`n")
  $checksumLines = foreach ($path in @($packagePath, $manifestPath, $notesPath, $rollbackPath, $higherPackage)) {
    "$(Get-SHA256Hex $path)  $([System.IO.Path]::GetFileName($path))"
  }
  Write-Utf8NoBom $checksumsPath (($checksumLines -join "`n") + "`n")
  Assert-Fails { & $verifyScript -ReleaseDir $releaseDir -Version $version -ExpectedMode Development } "older|previous"

  $rollback.previous_version = "9.8.6"
  $rollback.package.file = [System.IO.Path]::GetFileName($previousPackage)
  $rollback.package.sha256 = Get-SHA256Hex $previousPackage
  $rollback.package.size = (Get-Item $previousPackage).Length
  Write-Utf8NoBom $rollbackPath (($rollback | ConvertTo-Json -Depth 6) + "`n")
  $releaseManifest.rollback.sha256 = Get-SHA256Hex $rollbackPath
  Write-Utf8NoBom $manifestPath (($releaseManifest | ConvertTo-Json -Depth 8) + "`n")
  $checksumLines = foreach ($path in @($packagePath, $manifestPath, $notesPath, $rollbackPath, $previousPackage)) {
    "$(Get-SHA256Hex $path)  $([System.IO.Path]::GetFileName($path))"
  }
  Write-Utf8NoBom $checksumsPath (($checksumLines -join "`n") + "`n")

  $releaseManifest.mode = "release"
  $releaseManifest.signing.code_signed = $false
  Write-Utf8NoBom $manifestPath (($releaseManifest | ConvertTo-Json -Depth 8) + "`n")
  Assert-Fails { & $verifyScript -ReleaseDir $releaseDir -Version $version -ExpectedMode Release } "signed"

  Write-Output "Task 11C release pipeline script tests passed."
} catch {
  Write-Error $_
  exit 1
} finally {
  Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
