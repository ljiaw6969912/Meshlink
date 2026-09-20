param(
  [string]$OutDir = "dist",
  [string]$ReleaseDir = "release",
  [string]$BinDir = "bin",
  [string]$Name = "meshlink",
  [ValidateSet("Development", "Release")]
  [string]$Mode = "Development",
  [string]$Version = "",
  [string]$BuildTime = "",
  [string]$SigningCertificateThumbprint = "",
  [string]$PreviousVersion = "",
  [string]$PreviousPackagePath = ""
)

$ErrorActionPreference = "Stop"

function Write-Utf8NoBom([string]$Path, [string]$Content) {
  $encoding = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($Path, $Content, $encoding)
}

function Get-SHA256Hex([string]$Path) {
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Get-SortedRelativeFiles([string]$Directory) {
  [string[]]$paths = @(Get-ChildItem -LiteralPath $Directory -File -Recurse | ForEach-Object {
    $_.FullName.Substring($Directory.Length + 1).Replace('\', '/')
  })
  [System.Array]::Sort($paths, [System.StringComparer]::Ordinal)
  return $paths
}

function Compare-SemVer([string]$Left, [string]$Right) {
  if ($Left -notmatch '^(\d+\.\d+\.\d+)(?:-([0-9A-Za-z.-]+))?$') {
    throw "Invalid semantic version: $Left"
  }
  $leftCore = [version]$Matches[1]
  $leftSuffix = [string]$Matches[2]
  if ($Right -notmatch '^(\d+\.\d+\.\d+)(?:-([0-9A-Za-z.-]+))?$') {
    throw "Invalid semantic version: $Right"
  }
  $rightCore = [version]$Matches[1]
  $rightSuffix = [string]$Matches[2]
  $coreComparison = $leftCore.CompareTo($rightCore)
  if ($coreComparison -ne 0) {
    return $coreComparison
  }
  if ($leftSuffix -ceq $rightSuffix) {
    return 0
  }
  if ([string]::IsNullOrEmpty($leftSuffix)) {
    return 1
  }
  if ([string]::IsNullOrEmpty($rightSuffix)) {
    return -1
  }
  return [string]::CompareOrdinal($leftSuffix, $rightSuffix)
}

function Assert-NoSecrets([string]$Directory) {
  foreach ($file in @(Get-ChildItem -LiteralPath $Directory -File -Recurse)) {
    $relative = $file.FullName.Substring($Directory.Length + 1).Replace('\', '/')
    $lower = $relative.ToLowerInvariant()
    foreach ($suffix in @('.key', '.pem', '.p12', '.pfx', '.jks', '.keystore')) {
      if ($lower.EndsWith($suffix)) {
        throw "Forbidden credential file in package: $relative"
      }
    }
    $isText = $false
    foreach ($suffix in @('.json', '.md', '.txt', '.ps1', '.bat', '.env', '.yml', '.yaml', '.config')) {
      if ($lower.EndsWith($suffix)) {
        $isText = $true
        break
      }
    }
    if (-not $isText -or $file.Length -gt 4194304) {
      continue
    }
    $text = Get-Content -Raw -LiteralPath $file.FullName
    if ($text -match '-----BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY-----') {
      throw "Private key material found in package: $relative"
    }
    if ($text -match '(?i)["'']?(password|private_key|client_secret|access_token)["'']?\s*[:=]\s*["''][^"''<\$%{][^"'']+["'']') {
      throw "Credential-like value found in package: $relative"
    }
  }
}

function New-DeterministicZip([string]$SourceDirectory, [string]$RootName, [string]$DestinationPath) {
  Add-Type -AssemblyName System.IO.Compression
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  if (Test-Path -LiteralPath $DestinationPath) {
    Remove-Item -LiteralPath $DestinationPath -Force
  }
  $outputStream = [System.IO.File]::Open($DestinationPath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
  $archive = New-Object System.IO.Compression.ZipArchive($outputStream, [System.IO.Compression.ZipArchiveMode]::Create, $false, [System.Text.Encoding]::UTF8)
  try {
    foreach ($relative in @(Get-SortedRelativeFiles $SourceDirectory)) {
      $source = Join-Path $SourceDirectory $relative.Replace('/', '\')
      $entry = $archive.CreateEntry("$RootName/$relative", [System.IO.Compression.CompressionLevel]::Optimal)
      $entry.LastWriteTime = [System.DateTimeOffset]::Parse("2000-01-01T00:00:00Z")
      $entryStream = $entry.Open()
      $inputStream = [System.IO.File]::OpenRead($source)
      try {
        $inputStream.CopyTo($entryStream)
      } finally {
        $inputStream.Dispose()
        $entryStream.Dispose()
      }
    }
  } finally {
    $archive.Dispose()
    $outputStream.Dispose()
  }
}

function Assert-GoBuildVersion([string]$Path, [string]$ExpectedVersion, [string]$ExpectedBuildTime) {
  $output = @(& go version -m $Path 2>&1)
  if ($LASTEXITCODE -ne 0) {
    throw "Cannot inspect Go build metadata: $Path"
  }
  $joined = $output -join "`n"
  if (-not $joined.Contains("meshlink/internal/version.Version=$ExpectedVersion")) {
    throw "Internal version mismatch: $Path"
  }
  if (-not $joined.Contains("meshlink/internal/version.BuildTime=$ExpectedBuildTime")) {
    throw "Internal build time mismatch: $Path"
  }
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
if ($Name -cne "meshlink") {
  throw "Release package name is fixed to meshlink"
}
$packageRootName = $Name
$versionPath = Join-Path $root "VERSION"
$sourceVersion = (Get-Content -Raw -LiteralPath $versionPath).Trim()
if ($sourceVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
  throw "VERSION is missing or invalid"
}
if (-not [string]::IsNullOrWhiteSpace($Version) -and $Version -cne $sourceVersion) {
  throw "Requested version '$Version' does not match VERSION '$sourceVersion'"
}
$Version = $sourceVersion
$modeValue = $Mode.ToLowerInvariant()

$resolvedBin = if ([System.IO.Path]::IsPathRooted($BinDir)) { [System.IO.Path]::GetFullPath($BinDir) } else { [System.IO.Path]::GetFullPath((Join-Path $root $BinDir)) }
$resolvedOut = if ([System.IO.Path]::IsPathRooted($OutDir)) { [System.IO.Path]::GetFullPath($OutDir) } else { [System.IO.Path]::GetFullPath((Join-Path $root $OutDir)) }
$resolvedRelease = if ([System.IO.Path]::IsPathRooted($ReleaseDir)) { [System.IO.Path]::GetFullPath($ReleaseDir) } else { [System.IO.Path]::GetFullPath((Join-Path $root $ReleaseDir)) }
$packageDir = [System.IO.Path]::GetFullPath((Join-Path $resolvedOut $Name))
$zipPath = Join-Path $resolvedOut "$Name.zip"
$releaseZipName = "$Name-$Version.zip"
$releaseZipPath = Join-Path $resolvedRelease $releaseZipName
$manifestPath = Join-Path $resolvedRelease "manifest.json"

$buildMetadataPath = Join-Path $resolvedBin "build-metadata.json"
$wintunPath = Join-Path $resolvedBin "wintun.dll"
if (-not (Test-Path -LiteralPath $wintunPath -PathType Leaf)) {
  throw "Required Windows runtime is missing: $wintunPath"
}
if (-not (Test-Path -LiteralPath $buildMetadataPath -PathType Leaf)) {
  throw "Build metadata is missing; run scripts/build.ps1 first"
}
$buildMetadata = Get-Content -Raw -LiteralPath $buildMetadataPath | ConvertFrom-Json
if ($buildMetadata.schema -ne 'meshlink-build-v1' -or [string]$buildMetadata.version -cne $Version -or
    [string]$buildMetadata.mode -cne $modeValue) {
  throw "Build metadata version or mode mismatch"
}
if ([string]::IsNullOrWhiteSpace($BuildTime)) {
  $BuildTime = [string]$buildMetadata.build_time
}
if ([string]$buildMetadata.build_time -cne $BuildTime) {
  throw "Build metadata time mismatch"
}

$certificateThumbprint = $SigningCertificateThumbprint.Replace(' ', '').ToUpperInvariant()
if ($Mode -eq "Release") {
  if ($certificateThumbprint -notmatch '^[0-9A-F]{40}$' -or -not [bool]$buildMetadata.signing.code_signed -or
      ([string]$buildMetadata.signing.certificate_thumbprint).Replace(' ', '').ToUpperInvariant() -cne $certificateThumbprint) {
    throw "Formal release signing configuration does not match build metadata"
  }
} else {
  if ([bool]$buildMetadata.signing.code_signed -or -not [string]::IsNullOrWhiteSpace([string]$buildMetadata.signing.certificate_thumbprint)) {
    throw "Development build metadata must be explicitly unsigned"
  }
  $certificateThumbprint = ""
}

$requiredBinaries = @("mesh-agent.exe", "mesh-cloudhub.exe", "mesh-desktop.exe", "mesh-update-server.exe", "meshctl.exe")
$metadataArtifacts = @{}
foreach ($artifact in @($buildMetadata.artifacts)) {
  $metadataArtifacts[([string]$artifact.path).ToLowerInvariant()] = $artifact
}
foreach ($relative in @($requiredBinaries + "linux/mesh-agent")) {
  $path = Join-Path $resolvedBin $relative.Replace('/', '\')
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "Required existing build artifact is missing: $path"
  }
  $key = $relative.ToLowerInvariant()
  if (-not $metadataArtifacts.ContainsKey($key) -or
      ([string]$metadataArtifacts[$key].sha256).ToLowerInvariant() -cne (Get-SHA256Hex $path) -or
      [int64]$metadataArtifacts[$key].size -ne (Get-Item -LiteralPath $path).Length) {
    throw "Build artifact differs from build metadata: $relative"
  }
  Assert-GoBuildVersion $path $Version $BuildTime
}
if ($Mode -eq "Release") {
  foreach ($name in $requiredBinaries) {
    $signature = Get-AuthenticodeSignature -LiteralPath (Join-Path $resolvedBin $name)
    if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or $null -eq $signature.SignerCertificate) {
      throw "Authenticode signature is invalid: $name"
    }
    if ($signature.SignerCertificate.Thumbprint.Replace(' ', '').ToUpperInvariant() -cne $certificateThumbprint) {
      throw "Authenticode signer certificate mismatch: $name"
    }
  }
}

New-Item -ItemType Directory -Force -Path $resolvedOut, $resolvedRelease | Out-Null
if (Test-Path -LiteralPath $packageDir) {
  Remove-Item -LiteralPath $packageDir -Recurse -Force
}
foreach ($relative in @("bin", "bin\linux", "configs", "scripts", "docs\ops")) {
  New-Item -ItemType Directory -Force -Path (Join-Path $packageDir $relative) | Out-Null
}

foreach ($name in $requiredBinaries) {
  Copy-Item -LiteralPath (Join-Path $resolvedBin $name) -Destination (Join-Path $packageDir "bin\$name")
}
Copy-Item -LiteralPath (Join-Path $resolvedBin "linux\mesh-agent") -Destination (Join-Path $packageDir "bin\linux\mesh-agent")
Copy-Item -LiteralPath $wintunPath -Destination (Join-Path $packageDir "bin\wintun.dll")

foreach ($config in @("hub.example.json", "spoke.example.json")) {
  Copy-Item -LiteralPath (Join-Path $root "configs\$config") -Destination (Join-Path $packageDir "configs\$config")
}
foreach ($required in @("README.md", "DEPLOY.zh-CN.md", "VERSION")) {
  Copy-Item -LiteralPath (Join-Path $root $required) -Destination (Join-Path $packageDir $required)
}
foreach ($optional in @("LICENSE", "THIRD_PARTY_NOTICES.md", "repair-meshlink-admin.ps1", "publish-update.bat")) {
  $source = Join-Path $root $optional
  if (Test-Path -LiteralPath $source -PathType Leaf) {
    Copy-Item -LiteralPath $source -Destination (Join-Path $packageDir $optional)
  }
}
foreach ($script in @("build-linux-agent.ps1", "e2e-self-relay.ps1")) {
  $source = Join-Path $root "scripts\$script"
  if (Test-Path -LiteralPath $source -PathType Leaf) {
    Copy-Item -LiteralPath $source -Destination (Join-Path $packageDir "scripts\$script")
  }
}
$linuxSystemd = Join-Path $root "internal\deployssh\scripts\linux-systemd.sh"
if (Test-Path -LiteralPath $linuxSystemd -PathType Leaf) {
  Copy-Item -LiteralPath $linuxSystemd -Destination (Join-Path $packageDir "scripts\linux-systemd.sh")
}
foreach ($runbook in @("self-hosted-relay-runbook.zh-CN.md", "release-runbook.zh-CN.md")) {
  $source = Join-Path $root "docs\ops\$runbook"
  if (Test-Path -LiteralPath $source -PathType Leaf) {
    Copy-Item -LiteralPath $source -Destination (Join-Path $packageDir "docs\ops\$runbook")
  }
}
Copy-Item -LiteralPath $buildMetadataPath -Destination (Join-Path $packageDir "build-metadata.json")

Assert-NoSecrets $packageDir
$packageFiles = @()
foreach ($relative in @(Get-SortedRelativeFiles $packageDir)) {
  $file = Get-Item -LiteralPath (Join-Path $packageDir $relative.Replace('/', '\'))
  $packageFiles += [ordered]@{ path = $relative; sha256 = Get-SHA256Hex $file.FullName; size = [int64]$file.Length }
}
$packageManifest = [ordered]@{
  schema = "meshlink-package-v1"
  product = "Meshlink"
  version = $Version
  mode = $modeValue
  build_time = $BuildTime
  files = $packageFiles
  signing = [ordered]@{ code_signed = ($Mode -eq "Release"); certificate_thumbprint = $certificateThumbprint }
}
Write-Utf8NoBom (Join-Path $packageDir "manifest.json") (($packageManifest | ConvertTo-Json -Depth 8) + "`n")

New-DeterministicZip $packageDir $packageRootName $releaseZipPath
Copy-Item -LiteralPath $releaseZipPath -Destination $zipPath -Force

$notesPath = Join-Path $resolvedRelease "release-notes-$Version.zh-CN.md"
$signatureLabel = if ($Mode -eq "Release") { "Passed the Authenticode production signing gate." } else { "Unsigned development artifact; not approved for production release." }
$releaseNotes = @"
# Meshlink $Version Release Notes

- Artifact mode: $modeValue. $signatureLabel
- Compatibility: existing free, self-hosted, official Hub, team and enterprise behaviors are unchanged.
- Update safety: validate version, filename, SHA-256, required files and signing metadata before preserving last-known-good and replacing binaries. Local configs, certificates and logs are not overwritten.
- Change summary: add user-visible changes during release approval. Never include private keys, certificate passwords or access tokens.
- Rollback: see rollback-manifest-$Version.json. Formal releases require a verified previous stable package.
"@
Write-Utf8NoBom $notesPath ($releaseNotes.TrimEnd() + "`n")

$rollbackPackageDescriptor = $null
if (-not [string]::IsNullOrWhiteSpace($PreviousPackagePath)) {
  $resolvedPrevious = if ([System.IO.Path]::IsPathRooted($PreviousPackagePath)) { [System.IO.Path]::GetFullPath($PreviousPackagePath) } else { [System.IO.Path]::GetFullPath((Join-Path $root $PreviousPackagePath)) }
  if (-not (Test-Path -LiteralPath $resolvedPrevious -PathType Leaf)) {
    throw "Rollback package does not exist: $resolvedPrevious"
  }
  $previousName = [System.IO.Path]::GetFileName($resolvedPrevious)
  if ([string]::IsNullOrWhiteSpace($PreviousVersion) -and $previousName -match '^meshlink-(.+)\.zip$') {
    $PreviousVersion = $Matches[1]
  }
  if ($PreviousVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$' -or $previousName -cne "meshlink-$PreviousVersion.zip" -or
      (Compare-SemVer $PreviousVersion $Version) -ge 0) {
    throw "Rollback package version or filename is invalid"
  }
  $rollbackDestination = Join-Path $resolvedRelease $previousName
  if ([System.IO.Path]::GetFullPath($resolvedPrevious) -cne [System.IO.Path]::GetFullPath($rollbackDestination)) {
    Copy-Item -LiteralPath $resolvedPrevious -Destination $rollbackDestination -Force
  }
  $rollbackFile = Get-Item -LiteralPath $rollbackDestination
  $rollbackPackageDescriptor = [ordered]@{ file = $previousName; sha256 = Get-SHA256Hex $rollbackDestination; size = [int64]$rollbackFile.Length }
} elseif ($Mode -eq "Release") {
  throw "Formal release requires -PreviousPackagePath and a matching previous version package"
}

$rollbackPath = Join-Path $resolvedRelease "rollback-manifest-$Version.json"
$rollback = [ordered]@{
  schema = "meshlink-rollback-v1"
  version = $Version
  previous_version = $PreviousVersion
  package = $rollbackPackageDescriptor
  preserves = @("configs", "certs", "logs", "invites", "device registry")
}
Write-Utf8NoBom $rollbackPath (($rollback | ConvertTo-Json -Depth 8) + "`n")

$releaseFile = Get-Item -LiteralPath $releaseZipPath
$manifest = [ordered]@{
  schema = "meshlink-release-v1"
  product = "Meshlink"
  version = $Version
  mode = $modeValue
  build_time = $BuildTime
  generated_at = (Get-Date).ToUniversalTime().ToString("o")
  package = [ordered]@{ file = $releaseZipName; sha256 = Get-SHA256Hex $releaseZipPath; size = [int64]$releaseFile.Length }
  signing = [ordered]@{ code_signed = ($Mode -eq "Release"); certificate_thumbprint = $certificateThumbprint }
  release_notes = [ordered]@{ file = [System.IO.Path]::GetFileName($notesPath); sha256 = Get-SHA256Hex $notesPath }
  rollback = [ordered]@{ file = [System.IO.Path]::GetFileName($rollbackPath); sha256 = Get-SHA256Hex $rollbackPath }
  notes = @("Updates binaries and example configs only; preserves local configs, certificates, registry, invites and logs.")
}
Write-Utf8NoBom $manifestPath (($manifest | ConvertTo-Json -Depth 8) + "`n")

$checksumTargets = @($releaseZipPath, $manifestPath, $notesPath, $rollbackPath)
if ($null -ne $rollbackPackageDescriptor) {
  $checksumTargets += Join-Path $resolvedRelease ([string]$rollbackPackageDescriptor.file)
}
$checksumLines = foreach ($path in $checksumTargets) {
  "$(Get-SHA256Hex $path)  $([System.IO.Path]::GetFileName($path))"
}
$checksumsPath = Join-Path $resolvedRelease "checksums-$Version.sha256"
Write-Utf8NoBom $checksumsPath (($checksumLines -join "`n") + "`n")

& (Join-Path $PSScriptRoot "verify-release.ps1") -ReleaseDir $resolvedRelease -Version $Version -ExpectedMode $Mode
if ($LASTEXITCODE -ne 0) {
  throw "verify-release.ps1 failed with exit code $LASTEXITCODE"
}

Write-Output "Created deterministic package: $zipPath"
Write-Output "Created update package: $releaseZipPath"
Write-Output "Created release manifest: $manifestPath"
Write-Output "SHA256: $(Get-SHA256Hex $releaseZipPath)"
